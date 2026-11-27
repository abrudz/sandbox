// Package api implements the HTTP interface of the sandbox runner.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/abrudz/sandbox/internal/sandbox"
)

// Options configures the handler. A limit of zero means no limit.
type Options struct {
	// Executor runs submitted programs.
	Executor sandbox.Executor
	// DyalogVersion is the interpreter's version, reported by
	// /api/v1/version and in every run result.
	DyalogVersion string
	// RequestLimit is the largest code plus input one request may carry,
	// in bytes.
	RequestLimit int
	// Concurrency is the number of runs in progress at once.
	Concurrency int
	// ClientConcurrency is the number of run requests one client may have
	// in progress at once, counted from the arrival of their headers.
	ClientConcurrency int
	// ClientRate is the number of runs one client may start per minute.
	ClientRate int
	// ReadConcurrency is the number of run request bodies read at once.
	ReadConcurrency int
	// ReadTimeout is the time a request has to arrive in full, headers and
	// body, and IdleTimeout the time a connection may wait for its next
	// request; HTTPServer applies both.
	ReadTimeout time.Duration
	IdleTimeout time.Duration
	// ResponseTimeout is the time a run's response has to be written.
	ResponseTimeout time.Duration
	// TrustedProxies are the peers whose X-Forwarded-For header names the
	// client; any other peer is the client itself.
	TrustedProxies []netip.Prefix
	// Now is the clock for per-minute limits; nil means time.Now.
	Now func() time.Time
	// WebDir holds the frontend's files, served at /; empty serves none.
	WebDir string
	// ImageDigest is the digest of the docker.io/dyalog/dyalog image the
	// sandbox image was built from.
	ImageDigest string
	// Commit is the commit of this repository the image was built from.
	Commit string
	// Timeout is the wall-clock limit of one run, reported by
	// /api/v1/version; the executor enforces it.
	Timeout time.Duration
}

// Server is the runner's HTTP handler.
type Server struct {
	opts      Options
	mux       *http.ServeMux
	admission admission
}

// NewHandler returns the runner's HTTP handler.
func NewHandler(opts Options) *Server {
	s := &Server{
		opts: opts,
		mux:  http.NewServeMux(),
		admission: admission{
			drained:   make(chan struct{}),
			perClient: map[string]int{},
			starts:    map[string][]time.Time{},
		},
	}
	s.mux.HandleFunc("GET /healthz", healthz)
	s.mux.HandleFunc("POST /api/v1/run", s.run)
	s.mux.HandleFunc("GET /api/v1/version", s.version)
	if opts.WebDir != "" {
		s.mux.Handle("GET /", http.FileServer(http.Dir(opts.WebDir)))
	}
	return s
}

// Listen returns the listener runner-api serves on: the Unix socket at
// socket, or TCP port 8080 when socket is empty. The socket is open to its
// owner only, so a request on it comes from a process as privileged as
// runner-api, and its X-Forwarded-For header names the client. A socket
// that a process left behind without closing its listener is replaced;
// closing the listener removes the socket.
func Listen(socket string) (net.Listener, error) {
	if socket == "" {
		return net.Listen("tcp", ":8080")
	}
	if info, err := os.Lstat(socket); err == nil && info.Mode().Type() == fs.ModeSocket {
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// HTTPServer returns an http.Server that serves s on addr with the read and
// idle timeouts in Options. The read timeout also bounds the headers.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:        addr,
		Handler:     s,
		ReadTimeout: s.opts.ReadTimeout,
		IdleTimeout: s.opts.IdleTimeout,
	}
}

// contentSecurityPolicy lets the page load scripts, styles, fonts and images
// and make requests from its own origin only, and allows no plugins, no
// <base>, no form submissions and no framing. The page has no inline
// scripts, handlers or styles, so the policy needs no 'unsafe-inline'.
const contentSecurityPolicy = "default-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// ServeHTTP implements http.Handler. Every response forbids content-type
// sniffing and carries the page's Content Security Policy. While draining,
// every new request gets 503, so a proxy in front stops sending traffic.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	s.admission.mu.Lock()
	draining := s.admission.draining
	s.admission.mu.Unlock()
	if draining {
		http.Error(w, "the service is shutting down", http.StatusServiceUnavailable)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Drain makes every new request answer 503 and returns once the runs in
// progress have finished, or ctx's error when ctx is done first.
func (s *Server) Drain(ctx context.Context) error {
	a := &s.admission
	a.mu.Lock()
	if !a.draining {
		a.draining = true
		if a.active == 0 {
			close(a.drained)
		}
	}
	a.mu.Unlock()

	select {
	case <-a.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// healthz reports that the API is serving requests.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

type runRequest struct {
	Code  string   `json:"code"`
	Input string   `json:"input"`
	Args  []string `json:"args"`
}

type runResponse struct {
	Stdout          string  `json:"stdout"`
	Stderr          string  `json:"stderr"`
	ExitCode        *int    `json:"exitCode"`
	Signal          *string `json:"signal"`
	TimedOut        bool    `json:"timedOut"`
	StdoutTruncated bool    `json:"stdoutTruncated"`
	StderrTruncated bool    `json:"stderrTruncated"`
	WallTimeMs      int64   `json:"wallTimeMs"`
	UserTimeMs      int64   `json:"userTimeMs"`
	SysTimeMs       int64   `json:"sysTimeMs"`
	DyalogVersion   string  `json:"dyalogVersion"`
}

// run executes one program. Every completed run answers 200, whatever the
// program's exit status; 503 means the sandbox could not run it.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	// A cross-origin JSON request needs a CORS preflight, which is never
	// granted, so web pages cannot make their visitors' browsers start runs.
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		http.Error(w, "a run request must have Content-Type: application/json", http.StatusUnsupportedMediaType)
		return
	}
	client := s.clientOf(r)
	read, leave, status, reason := s.enter(client)
	if status != 0 {
		http.Error(w, reason, status)
		return
	}
	defer leave()
	req, status, reason := s.decode(w, r)
	read()
	if status != 0 {
		if status == http.StatusRequestTimeout {
			w.Header().Set("Connection", "close")
		}
		http.Error(w, reason, status)
		return
	}
	release, status, reason := s.admit(client)
	if status != 0 {
		http.Error(w, reason, status)
		return
	}

	job := sandbox.Job{Code: []byte(req.Code), Input: []byte(req.Input), Args: req.Args}
	result, err := s.opts.Executor.Run(r.Context(), job)
	release()
	if s.opts.ResponseTimeout > 0 {
		// A client that does not read its response is abandoned. The server
		// flushes the response before clearing the deadline for the next
		// request on the connection, so all of it is written under it.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.opts.ResponseTimeout))
	}
	if err != nil {
		if r.Context().Err() != nil {
			// The client disconnected; the executor has stopped the run.
			return
		}
		log.Printf("run failed: %v", err)
		http.Error(w, "sandbox unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := runResponse{
		Stdout:          strings.ToValidUTF8(string(result.Stdout), "�"),
		Stderr:          strings.ToValidUTF8(string(result.Stderr), "�"),
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
		WallTimeMs:      result.WallTime.Milliseconds(),
		UserTimeMs:      result.UserTime.Milliseconds(),
		SysTimeMs:       result.SysTime.Milliseconds(),
		DyalogVersion:   s.opts.DyalogVersion,
	}
	if result.Signal != "" {
		resp.Signal = &result.Signal
	}
	writeJSON(w, resp)
}

// decode reads and validates a run request, or returns the status and
// reason for refusing it. Only whitespace may follow the JSON value; the
// body is read to its end, which is when the server starts watching for
// the client to disconnect.
func (s *Server) decode(w http.ResponseWriter, r *http.Request) (runRequest, int, string) {
	var req runRequest
	body := r.Body
	if s.opts.RequestLimit > 0 {
		body = http.MaxBytesReader(w, r.Body, s.bodyLimit())
	}
	dec := json.NewDecoder(body)
	err := dec.Decode(&req)
	if err == nil {
		// Token skips whitespace and reports the end of the body as io.EOF.
		if _, err = dec.Token(); err == nil {
			return req, http.StatusBadRequest, "malformed request: data after the JSON value"
		}
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	var tooLarge *http.MaxBytesError
	var netErr net.Error
	switch {
	case err == nil:
	case errors.As(err, &tooLarge):
		return req, http.StatusRequestEntityTooLarge, "request body too large"
	case errors.As(err, &netErr) && netErr.Timeout():
		return req, http.StatusRequestTimeout, "the request did not arrive in time"
	default:
		return req, http.StatusBadRequest, "malformed request: " + err.Error()
	}
	if len(req.Args) > maxArgs {
		return req, http.StatusBadRequest, fmt.Sprintf("at most %d arguments are allowed", maxArgs)
	}
	for _, arg := range req.Args {
		if len(arg) > maxArgLen {
			return req, http.StatusBadRequest, fmt.Sprintf("an argument may be at most %d bytes", maxArgLen)
		}
		// The arguments reach nsjail's command line, which cannot hold NUL.
		if strings.ContainsRune(arg, 0) {
			return req, http.StatusBadRequest, "an argument may not contain NUL"
		}
	}
	if s.opts.RequestLimit > 0 && len(req.Code)+len(req.Input) > s.opts.RequestLimit {
		return req, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("code and input may be at most %d bytes together", s.opts.RequestLimit)
	}
	return req, 0, ""
}

type versionResponse struct {
	DyalogVersion string `json:"dyalogVersion"`
	ImageDigest   string `json:"imageDigest"`
	Commit        string `json:"commit"`
	TimeoutMs     int64  `json:"timeoutMs"`
}

// version reports the interpreter version that runs programs, and the run
// time limit, which tells the frontend how long a run's response can take.
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, versionResponse{
		DyalogVersion: s.opts.DyalogVersion,
		ImageDigest:   s.opts.ImageDigest,
		Commit:        s.opts.Commit,
		TimeoutMs:     s.opts.Timeout.Milliseconds(),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}
