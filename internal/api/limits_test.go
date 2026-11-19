package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/sandbox"
)

// blockingExecutor holds every run until release is closed.
type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan struct{}, 100), release: make(chan struct{})}
}

func (b *blockingExecutor) Run(ctx context.Context, _ sandbox.Job) (sandbox.Result, error) {
	b.started <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/run", strings.NewReader(body))
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

func TestPostRunAnswers400ForMalformedJSON(t *testing.T) {
	h := handler(&fakeExecutor{}, nil)

	for _, body := range []string{`{"code":`, `{"code":1}`, `not json`} {
		if got := post(h, "192.0.2.1", body); got != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, got)
		}
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
