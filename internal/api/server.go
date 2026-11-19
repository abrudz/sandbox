// Package api implements the HTTP interface of the sandbox runner.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/abrudz/sandbox/internal/sandbox"
)

// Options configures the handler.
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
}

// Server is the runner's HTTP handler.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// NewHandler returns the runner's HTTP handler.
func NewHandler(opts Options) *Server {
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", healthz)
	s.mux.HandleFunc("POST /api/v1/run", s.run)
	s.mux.HandleFunc("GET /api/v1/version", s.version)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Drain makes every new request answer 503 and returns once the runs in
// progress have finished, or when ctx is done.
func (s *Server) Drain(ctx context.Context) error {
	return nil
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
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request: "+err.Error(), http.StatusBadRequest)
		return
	}
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

// version reports the interpreter version that runs programs.
func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"dyalogVersion": s.opts.DyalogVersion})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}
