// Package api implements the HTTP interface of the sandbox runner.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
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
	// ClientConcurrency is the number of runs one client may have in
	// progress at once.
	ClientConcurrency int
	// ClientRate is the number of runs one client may start per minute.
	ClientRate int
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

// ServeHTTP implements http.Handler. While draining, every new request
// gets 503, so a proxy in front stops sending traffic.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	req, status, reason := s.decode(w, r)
	if status != 0 {
		http.Error(w, reason, status)
		return
	}
	release, status, reason := s.admit(s.clientOf(r))
	if status != 0 {
		http.Error(w, reason, status)
		return
	}
	defer release()

	job := sandbox.Job{Code: []byte(req.Code), Input: []byte(req.Input), Args: req.Args}
	result, err := s.opts.Executor.Run(r.Context(), job)
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
// reason for refusing it.
func (s *Server) decode(w http.ResponseWriter, r *http.Request) (runRequest, int, string) {
	var req runRequest
	body := r.Body
	if s.opts.RequestLimit > 0 {
		body = http.MaxBytesReader(w, r.Body, s.bodyLimit())
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return req, http.StatusRequestEntityTooLarge, "request body too large"
		}
		return req, http.StatusBadRequest, "malformed request: " + err.Error()
	}
	if len(req.Args) > maxArgs {
		return req, http.StatusBadRequest, fmt.Sprintf("at most %d arguments are allowed", maxArgs)
	}
	for _, arg := range req.Args {
		if len(arg) > maxArgLen {
			return req, http.StatusBadRequest, fmt.Sprintf("an argument may be at most %d bytes", maxArgLen)
		}
	}
	if s.opts.RequestLimit > 0 && len(req.Code)+len(req.Input) > s.opts.RequestLimit {
		return req, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("code and input may be at most %d bytes together", s.opts.RequestLimit)
	}
	return req, 0, ""
}

// version reports the interpreter version that runs programs.
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{
		"dyalogVersion": s.opts.DyalogVersion,
		"imageDigest":   s.opts.ImageDigest,
		"commit":        s.opts.Commit,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}
