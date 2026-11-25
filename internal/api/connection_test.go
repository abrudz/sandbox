package api_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/sandbox"
)

// Tests of behaviour that needs a real connection: timeouts, and noticing
// that a client has gone.

// serve serves h with its HTTPServer on a loopback port and returns the
// address.
func serve(t *testing.T, h *api.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := h.HTTPServer(ln.Addr().String())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// runRequest is the head of a POST /api/v1/run request whose body is n
// bytes long.
func runRequest(n int) string {
	return fmt.Sprintf("POST /api/v1/run HTTP/1.1\r\nHost: sandbox\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", n)
}

// closed waits for the server to close conn, discarding anything it still
// sends, and returns an error if that does not happen before deadline.
func closed(br *bufio.Reader, conn net.Conn, deadline time.Time) error {
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	// io.Copy reports the end of the stream as success.
	_, err := io.Copy(io.Discard, br)
	return err
}

func TestARequestWhoseBodyStallsGets408AndItsConnectionCloses(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ReadTimeout = 500 * time.Millisecond })
	conn := dial(t, serve(t, h))
	br := bufio.NewReader(conn)

	begun := time.Now()
	if _, err := io.WriteString(conn, runRequest(len(okBody))+okBody[:10]); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(begun.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	elapsed := time.Since(begun)

	if err != nil {
		t.Fatalf("no response within 3 s: %v", err)
	}
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Errorf("status %d, want 408", resp.StatusCode)
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("answered after %v, before the 500 ms read timeout", elapsed)
	}
	if err := closed(br, conn, time.Now().Add(time.Second)); err != nil {
		t.Errorf("the connection stayed open after the 408: %v", err)
	}
}

// slowExecutor takes delay to finish each run.
type slowExecutor struct{ delay time.Duration }

func (s slowExecutor) Run(ctx context.Context, _ sandbox.Job) (sandbox.Result, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return sandbox.Result{}, ctx.Err()
	}
	zero := 0
	return sandbox.Result{ExitCode: &zero}, nil
}

func TestARunThatOutlastsTheReadTimeoutCompletes(t *testing.T) {
	h := handler(slowExecutor{delay: 600 * time.Millisecond}, func(o *api.Options) { o.ReadTimeout = 300 * time.Millisecond })
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post("http://"+serve(t, h)+"/api/v1/run", "application/json", strings.NewReader(okBody))

	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a 600 ms run with a 300 ms read timeout: status %d, want 200", resp.StatusCode)
	}
}

func TestAConnectionIdleBetweenRequestsIsClosedAfterTheIdleTimeout(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) {
		o.ReadTimeout = 5 * time.Second
		o.IdleTimeout = 300 * time.Millisecond
	})
	conn := dial(t, serve(t, h))
	br := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: sandbox\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	answered := time.Now()

	err = closed(br, conn, answered.Add(3*time.Second))
	elapsed := time.Since(answered)

	if err != nil {
		t.Fatalf("the idle connection was still open 3 s after its response: %v", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("closed %v after the response, before the 300 ms idle timeout", elapsed)
	}
}

func TestAClientThatDisconnectsAfterAPaddedBodyEndsItsRun(t *testing.T) {
	exec := newBlockingExecutor()
	t.Cleanup(func() { close(exec.release) })
	h := handler(exec, nil)
	conn := dial(t, serve(t, h))

	// Whitespace after the JSON value is valid; the server sees a
	// disconnect only once it has read the body to its end.
	body := okBody + strings.Repeat(" ", 300<<10)
	go func() { _, _ = io.WriteString(conn, runRequest(len(body))+body) }()
	exec.waitStarted(t, 1)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exec.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the run went on for 2 s after its client disconnected")
	}
}

func TestAConnectionServesItsNextRequestAfterARunsResponseTimeoutHasPassed(t *testing.T) {
	h := handler(&fakeExecutor{}, func(o *api.Options) { o.ResponseTimeout = 200 * time.Millisecond })
	conn := dial(t, serve(t, h))
	br := bufio.NewReader(conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, runRequest(len(okBody))+okBody); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("the run: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}

	// A proxy keeps the connection for later requests.
	time.Sleep(400 * time.Millisecond)
	if _, err := io.WriteString(conn, "GET /api/v1/version HTTP/1.1\r\nHost: sandbox\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(br, nil)

	if err != nil {
		t.Fatalf("the next request on the connection, after the run's response timeout: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the next request on the connection: status %d, want 200", resp.StatusCode)
	}
}

// bigFirstExecutor answers its first run with 16 MiB of output, more than
// a loopback connection's buffers hold, and later runs with none.
type bigFirstExecutor struct {
	runs    atomic.Int32
	started chan struct{} // closed when the first run starts
}

func (b *bigFirstExecutor) Run(context.Context, sandbox.Job) (sandbox.Result, error) {
	zero := 0
	if b.runs.Add(1) == 1 {
		close(b.started)
		return sandbox.Result{Stdout: bytes.Repeat([]byte("x"), 16<<20), ExitCode: &zero}, nil
	}
	return sandbox.Result{ExitCode: &zero}, nil
}

// dialNotReading connects to addr with a 4 KiB receive buffer, for a
// client that reads nothing.
func dialNotReading(t *testing.T, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		if cerr := c.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		}); cerr != nil {
			return cerr
		}
		return err
	}}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestAResponseTheClientDoesNotReadIsAbandonedAfterTheResponseTimeout(t *testing.T) {
	exec := &bigFirstExecutor{started: make(chan struct{})}
	h := handler(exec, func(o *api.Options) {
		o.ClientConcurrency = 1
		o.ResponseTimeout = 300 * time.Millisecond
	})
	addr := serve(t, h)
	stuck := dialNotReading(t, addr)
	if _, err := io.WriteString(stuck, runRequest(len(okBody))+okBody); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run did not start within 5 s")
	}
	client := &http.Client{Timeout: 5 * time.Second}

	// The same client, from the same address, may have one request in
	// progress; the unread response holds it until it is abandoned.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Post("http://"+addr+"/api/v1/run", "application/json", strings.NewReader(okBody))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429 until the unread response is abandoned, then 200", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatal("3 s after its response was due, the client's next run still gets 429")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
