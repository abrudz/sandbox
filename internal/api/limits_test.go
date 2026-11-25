package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/sandbox"
)

// blockingExecutor holds every run until release is closed, or until the
// run's context ends, which it reports on cancelled.
type blockingExecutor struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{
		started:   make(chan struct{}, 100),
		cancelled: make(chan struct{}, 100),
		release:   make(chan struct{}),
	}
}

func (b *blockingExecutor) Run(ctx context.Context, _ sandbox.Job) (sandbox.Result, error) {
	b.started <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
		b.cancelled <- struct{}{}
		return sandbox.Result{}, ctx.Err()
	}
	zero := 0
	return sandbox.Result{ExitCode: &zero}, nil
}

// waitStarted waits until n runs have reached the executor.
func (b *blockingExecutor) waitStarted(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d runs reached the executor", i, n)
		}
	}
}

// handler returns a Server whose limits are generous except where adjust
// changes them.
func handler(exec sandbox.Executor, adjust func(*api.Options)) *api.Server {
	opts := api.Options{
		Executor:          exec,
		DyalogVersion:     version,
		RequestLimit:      64 * 1024,
		Concurrency:       100,
		ClientConcurrency: 100,
		ClientRate:        1000,
	}
	if adjust != nil {
		adjust(&opts)
	}
	return api.NewHandler(opts)
}

const okBody = `{"code":"⎕←1","input":"","args":[]}`

// post sends POST /api/v1/run from the peer at addr, with optional header
// name/value pairs, and returns the status. A request still running after
// 3 s is abandoned, so a run that should have been refused cannot hang the
// test; it then reports 200, the recorder's default.
func post(h http.Handler, addr, body string, header ...string) int {
	return postReader(h, addr, strings.NewReader(body), header...)
}

// postReader is post with a body read from body.
func postReader(h http.Handler, addr string, body io.Reader, header ...string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/run", body)
	req.RemoteAddr = addr + ":40000"
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func health(h http.Handler) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec.Code
}

// start sends post in the background and delivers its status.
func start(h http.Handler, addr string) <-chan int {
	status := make(chan int, 1)
	go func() { status <- post(h, addr, okBody) }()
	return status
}

func receive(t *testing.T, status <-chan int) int {
	t.Helper()
	select {
	case s := <-status:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no response within 5 s")
		return 0
	}
}

// stalledBody starts a run request from addr whose body stops after its
// first bytes, as a slow upload does, and returns once the handler is
// reading it. finish sends the rest of a valid body and returns the
// request's status.
func stalledBody(t *testing.T, h http.Handler, addr string) (finish func() int) {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() { w.CloseWithError(errors.New("test over")) })
	status := make(chan int, 1)
	go func() { status <- postReader(h, addr, r) }()

	// A pipe write returns once the reader has taken the bytes.
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, `{"code":"`)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("writing the start of the body: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the handler did not read the body of a request from %s within 5 s", addr)
	}
	return func() int {
		if _, err := io.WriteString(w, `","input":"","args":[]}`); err != nil {
			t.Fatalf("writing the rest of the body: %v", err)
		}
		w.Close()
		return receive(t, status)
	}
}

// stuckWriter is the ResponseWriter of a client that does not read: Write
// blocks until unblock is closed.
type stuckWriter struct {
	header  http.Header
	once    sync.Once
	writing chan struct{} // closed on the first Write
	unblock chan struct{}
}

func (w *stuckWriter) Header() http.Header { return w.header }

func (w *stuckWriter) WriteHeader(int) {}

func (w *stuckWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.unblock
	return len(p), nil
}

func TestPostRunAnswers400ForMalformedJSON(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)

	for _, body := range []string{`{"code":`, `{"code":1}`, `not json`} {
		if got := post(h, "192.0.2.1", body); got != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, got)
		}
	}
}

func TestPostRunAnswers400ForDataAfterTheJSONValue(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)

	for _, body := range []string{okBody + "x", okBody + "{}", okBody + ` "more"`} {
		if got := post(h, "192.0.2.1", body); got != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, got)
		}
	}
	if got := post(h, "192.0.2.1", okBody+" \r\n\t"); got != http.StatusOK {
		t.Errorf("whitespace after the JSON value: status %d, want 200", got)
	}
}

// unreadBody is a request body that records whether it was read.
type unreadBody struct{ read bool }

func (b *unreadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

func TestPostRunAnswers415BeforeReadingABodyThatIsNotJSON(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)

	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		body := &unreadBody{}
		if got := postReader(h, "192.0.2.1", body, "Content-Type", contentType); got != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status %d, want 415", contentType, got)
		}
		if body.read {
			t.Errorf("Content-Type %q: the body was read", contentType)
		}
	}
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		if got := post(h, "192.0.2.1", okBody, "Content-Type", contentType); got != http.StatusOK {
			t.Errorf("Content-Type %q: status %d, want 200", contentType, got)
		}
	}
}

func TestARunRequestFromAnotherOriginGetsNoCORSPermission(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/run", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Without this header a browser sends no cross-origin JSON request.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("preflight answered with Access-Control-Allow-Origin %q", got)
	}
}

func TestPostRunAnswers400ForAnArgumentContainingNUL(t *testing.T) {
	fake := &fakeExecutor{}
	h := handler(fake, nil)

	got := post(h, "192.0.2.1", `{"code":"","input":"","args":["a\u0000b"]}`)

	if got != http.StatusBadRequest {
		t.Errorf("status %d, want 400", got)
	}
	if fake.got.Args != nil {
		t.Errorf("the run started with arguments %q", fake.got.Args)
	}
}

func TestPostRunAnswers413WhenCodeAndInputExceedTheLimit(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.RequestLimit = 1024 })

	atLimit := `{"code":"` + strings.Repeat("x", 1024) + `","input":"","args":[]}`
	if got := post(h, "192.0.2.1", atLimit); got != http.StatusOK {
		t.Errorf("1024 bytes of code: status %d, want 200", got)
	}
	over := `{"code":"` + strings.Repeat("x", 600) + `","input":"` + strings.Repeat("y", 425) + `","args":[]}`
	if got := post(h, "192.0.2.1", over); got != http.StatusRequestEntityTooLarge {
		t.Errorf("1025 bytes of code and input: status %d, want 413", got)
	}
}

func TestPostRunAnswers413ForAnOversizedBody(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.RequestLimit = 1024 })

	// Whitespace keeps the decoded request small; the body itself is 1 MB.
	body := `{"code":"",` + strings.Repeat(" ", 1<<20) + `"input":"","args":[]}`

	if got := post(h, "192.0.2.1", body); got != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", got)
	}
}

func TestPostRunAnswers400ForMoreThanSixteenArguments(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)
	args := func(n int) string {
		return `{"code":"","input":"","args":[` + strings.TrimSuffix(strings.Repeat(`"a",`, n), ",") + `]}`
	}

	if got := post(h, "192.0.2.1", args(16)); got != http.StatusOK {
		t.Errorf("16 arguments: status %d, want 200", got)
	}
	if got := post(h, "192.0.2.1", args(17)); got != http.StatusBadRequest {
		t.Errorf("17 arguments: status %d, want 400", got)
	}
}

func TestPostRunAnswers400ForAnArgumentLongerThan1KiB(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)
	arg := func(n int) string { return `{"code":"","input":"","args":["` + strings.Repeat("a", n) + `"]}` }

	if got := post(h, "192.0.2.1", arg(1024)); got != http.StatusOK {
		t.Errorf("1024-byte argument: status %d, want 200", got)
	}
	if got := post(h, "192.0.2.1", arg(1025)); got != http.StatusBadRequest {
		t.Errorf("1025-byte argument: status %d, want 400", got)
	}
}

func TestANewRunGets429ImmediatelyWhenEverySlotIsBusy(t *testing.T) {
	exec := newBlockingExecutor()
	h := handler(exec, func(o *api.Options) { o.Concurrency = 2 })
	first, second := start(h, "192.0.2.1"), start(h, "192.0.2.2")
	exec.waitStarted(t, 2)

	begun := time.Now()
	got := post(h, "192.0.2.3", okBody)
	elapsed := time.Since(begun)

	if got != http.StatusTooManyRequests {
		t.Errorf("third run: status %d, want 429", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("the 429 took %v; it should not wait for a slot", elapsed)
	}
	close(exec.release)
	if s1, s2 := receive(t, first), receive(t, second); s1 != http.StatusOK || s2 != http.StatusOK {
		t.Errorf("runs holding the slots: status %d and %d, want 200", s1, s2)
	}
}

func TestAClientOverItsConcurrentLimitGets429AndOthersAreUnaffected(t *testing.T) {
	exec := newBlockingExecutor()
	h := handler(exec, func(o *api.Options) { o.ClientConcurrency = 2 })
	a1, a2 := start(h, "192.0.2.1"), start(h, "192.0.2.1")
	exec.waitStarted(t, 2)

	if got := post(h, "192.0.2.1", okBody); got != http.StatusTooManyRequests {
		t.Errorf("client's third concurrent run: status %d, want 429", got)
	}
	other := start(h, "192.0.2.2")
	exec.waitStarted(t, 1)
	close(exec.release)
	if got := receive(t, other); got != http.StatusOK {
		t.Errorf("another client's run: status %d, want 200", got)
	}
	receive(t, a1)
	receive(t, a2)
}

func TestAClientWhoseRequestsAreStillSendingBodiesGets429AtOnce(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ClientConcurrency = 2 })
	stalledBody(t, h, "192.0.2.1")
	stalledBody(t, h, "192.0.2.1")

	begun := time.Now()
	got := post(h, "192.0.2.1", okBody)
	elapsed := time.Since(begun)

	if got != http.StatusTooManyRequests {
		t.Errorf("client's third request: status %d, want 429", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("the 429 took %v; it should not wait for the other bodies", elapsed)
	}
	if got := post(h, "192.0.2.2", okBody); got != http.StatusOK {
		t.Errorf("another client's run: status %d, want 200", got)
	}
}

func TestBodiesBeyondTheReadLimitGet503AtOnceUntilOneFinishes(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ReadConcurrency = 2 })
	finish := stalledBody(t, h, "192.0.2.1")
	stalledBody(t, h, "192.0.2.2")

	begun := time.Now()
	got := post(h, "192.0.2.3", okBody)
	elapsed := time.Since(begun)

	if got != http.StatusServiceUnavailable {
		t.Errorf("a third body while two are being read: status %d, want 503", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("the 503 took %v; it should not wait for the other bodies", elapsed)
	}
	if got := finish(); got != http.StatusOK {
		t.Errorf("the request whose body then arrived: status %d, want 200", got)
	}
	if got := post(h, "192.0.2.3", okBody); got != http.StatusOK {
		t.Errorf("a body once one of the two has been read: status %d, want 200", got)
	}
}

func TestAFinishedRunReleasesItsSlotBeforeItsResponseIsWritten(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.Concurrency = 1 })
	w := &stuckWriter{header: http.Header{}, writing: make(chan struct{}), unblock: make(chan struct{})}
	first := make(chan struct{})
	go func() {
		defer close(first)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/run", strings.NewReader(okBody))
		req.RemoteAddr = "192.0.2.1:40000"
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, req)
	}()
	select {
	case <-w.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run's response was not written within 5 s")
	}
	defer func() {
		close(w.unblock)
		<-first
	}()

	if got := post(h, "192.0.2.2", okBody); got != http.StatusOK {
		t.Errorf("a run while the first run's response is being written: status %d, want 200", got)
	}
}

func TestAClientOverItsPerMinuteLimitGets429AndOthersAreUnaffected(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	h := handler(&fakeExecutor{}, func(o *api.Options) {
		o.ClientRate = 3
		o.Now = func() time.Time { return now }
	})

	for i := 1; i <= 3; i++ {
		if got := post(h, "192.0.2.1", okBody); got != http.StatusOK {
			t.Fatalf("run %d within the limit: status %d, want 200", i, got)
		}
	}
	if got := post(h, "192.0.2.1", okBody); got != http.StatusTooManyRequests {
		t.Errorf("fourth run in a minute: status %d, want 429", got)
	}
	if got := post(h, "192.0.2.2", okBody); got != http.StatusOK {
		t.Errorf("another client's run: status %d, want 200", got)
	}
	now = now.Add(61 * time.Second)
	if got := post(h, "192.0.2.1", okBody); got != http.StatusOK {
		t.Errorf("a minute later: status %d, want 200", got)
	}
}

func TestClientsBehindATrustedProxyAreToldApartByXForwardedFor(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) {
		o.ClientRate = 1
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	})

	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "203.0.113.1"); got != http.StatusOK {
		t.Fatalf("first client: status %d, want 200", got)
	}
	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "203.0.113.2"); got != http.StatusOK {
		t.Errorf("second client through the same proxy: status %d, want 200", got)
	}
	// The proxy appends the address it saw; entries before it are the
	// client's own claims and do not change who the client is.
	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "198.51.100.9, 203.0.113.1"); got != http.StatusTooManyRequests {
		t.Errorf("first client again, with a forged entry: status %d, want 429", got)
	}
}

func TestIPv6PeersInTheSame64AreOneClient(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ClientRate = 1 })

	if got := post(h, "[2001:db8:1:2::1]", okBody); got != http.StatusOK {
		t.Fatalf("first address: status %d, want 200", got)
	}
	if got := post(h, "[2001:db8:1:2:ffff::9]", okBody); got != http.StatusTooManyRequests {
		t.Errorf("another address in the same /64: status %d, want 429", got)
	}
	if got := post(h, "[2001:db8:1:3::1]", okBody); got != http.StatusOK {
		t.Errorf("an address in another /64: status %d, want 200", got)
	}
}

func TestIPv6ClientsNamedByATrustedProxyAreGroupedBy64(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) {
		o.ClientRate = 1
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	})

	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "2001:db8:1:2::1"); got != http.StatusOK {
		t.Fatalf("first address: status %d, want 200", got)
	}
	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "2001:db8:1:2::2"); got != http.StatusTooManyRequests {
		t.Errorf("another address in the same /64: status %d, want 429", got)
	}
	if got := post(h, "127.0.0.1", okBody, "X-Forwarded-For", "2001:db8:1:3::1"); got != http.StatusOK {
		t.Errorf("an address in another /64: status %d, want 200", got)
	}
}

func TestIPv4MappedAddressesKeepTheirFullAddress(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ClientRate = 1 })

	if got := post(h, "[::ffff:192.0.2.1]", okBody); got != http.StatusOK {
		t.Fatalf("first address: status %d, want 200", got)
	}
	if got := post(h, "[::ffff:192.0.2.2]", okBody); got != http.StatusOK {
		t.Errorf("a neighbouring IPv4 address: status %d, want 200", got)
	}
	if got := post(h, "192.0.2.1", okBody); got != http.StatusTooManyRequests {
		t.Errorf("the first address written as IPv4: status %d, want 429", got)
	}
}

func TestXForwardedForFromAnUntrustedPeerIsIgnored(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ClientRate = 1 })

	if got := post(h, "198.51.100.7", okBody, "X-Forwarded-For", "203.0.113.1"); got != http.StatusOK {
		t.Fatalf("first run: status %d, want 200", got)
	}
	if got := post(h, "198.51.100.7", okBody, "X-Forwarded-For", "203.0.113.2"); got != http.StatusTooManyRequests {
		t.Errorf("same peer claiming another address: status %d, want 429", got)
	}
}

func TestDrainAnswers503ToNewRequestsAndWaitsForRunsInProgress(t *testing.T) {
	exec := newBlockingExecutor()
	h := handler(exec, nil)
	inProgress := start(h, "192.0.2.1")
	exec.waitStarted(t, 1)

	drained := make(chan error, 1)
	go func() { drained <- h.Drain(context.Background()) }()

	// /healthz shows when draining has begun without itself starting a run
	// that the blocking executor would hold.
	deadline := time.Now().Add(time.Second)
	for health(h) != http.StatusServiceUnavailable {
		if time.Now().After(deadline) {
			t.Fatal("/healthz still answered 200 a second after Drain began")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := post(h, "192.0.2.2", okBody); got != http.StatusServiceUnavailable {
		t.Errorf("new run while draining: status %d, want 503", got)
	}
	select {
	case err := <-drained:
		t.Fatalf("Drain returned (%v) while a run was in progress", err)
	default:
	}

	close(exec.release)
	if got := receive(t, inProgress); got != http.StatusOK {
		t.Errorf("run in progress: status %d, want 200", got)
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Errorf("Drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Drain did not return within 1 s of the last run finishing")
	}
}
