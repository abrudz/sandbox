package api_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/api"
)

// Tests of serving on a Unix socket, through which a proxy on the same host
// reaches a server that has no network.

// listenSocket listens with api.Listen on a socket in a new directory.
func listenSocket(t *testing.T) (string, net.Listener) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner-api.sock")
	ln, err := api.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return path, ln
}

// serveOn serves h with its HTTPServer on ln.
func serveOn(t *testing.T, h *api.Server, ln net.Listener) {
	t.Helper()
	srv := h.HTTPServer("")
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// socketClient sends every request to the Unix socket at path.
func socketClient(path string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
}

// postForwardedFor sends POST /api/v1/run through c with the given
// X-Forwarded-For header and returns the status.
func postForwardedFor(t *testing.T, c *http.Client, forwardedFor string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://sandbox/api/v1/run", strings.NewReader(okBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", forwardedFor)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/run on the socket: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestARequestOnTheSocketNamesItsClientInXForwardedFor(t *testing.T) {
	path, ln := listenSocket(t)
	// No trusted proxies: only a process that can open the socket file
	// reaches the server through it.
	serveOn(t, handler(&fakeExecutor{}, func(o *api.Options) { o.ClientRate = 1 }), ln)
	c := socketClient(path)

	if got := postForwardedFor(t, c, "203.0.113.1"); got != http.StatusOK {
		t.Fatalf("first client: status %d, want 200", got)
	}
	if got := postForwardedFor(t, c, "203.0.113.2"); got != http.StatusOK {
		t.Errorf("second client through the socket: status %d, want 200", got)
	}
	if got := postForwardedFor(t, c, "198.51.100.9, 203.0.113.1"); got != http.StatusTooManyRequests {
		t.Errorf("first client again, with a forged entry: status %d, want 429", got)
	}
}

func TestTheSocketIsOpenToItsOwnerOnly(t *testing.T) {
	path, _ := listenSocket(t)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() != os.ModeSocket {
		t.Errorf("%s is %v, not a socket", path, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions %v, want %v", perm, os.FileMode(0o600))
	}
}

func TestListenReplacesASocketLeftByAnEarlierProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-api.sock")
	// A process that ends without closing its listener leaves the socket
	// file behind, and nothing accepts on it.
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	_ = old.Close()

	ln, err := api.Listen(path)
	if err != nil {
		t.Fatalf("listening where a socket was left: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	serveOn(t, handler(&fakeExecutor{}, nil), ln)

	resp, err := socketClient(path).Get("http://sandbox/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on the socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz on the socket: status %d, want 200", resp.StatusCode)
	}
}
