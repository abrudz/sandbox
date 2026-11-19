// Package api implements the HTTP interface of the sandbox runner.
package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/abrudz/sandbox/internal/sandbox"
)

// Options configures the handler.
type Options struct {
	// Executor runs submitted programs.
	Executor sandbox.Executor
	// DyalogVersion is the interpreter's version, reported by
	// /api/v1/version and in every run result.
	DyalogVersion string
}

// NewHandler returns the runner's HTTP handler.
func NewHandler(opts Options) http.Handler {
	s := &server{opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/v1/run", s.run)
	mux.HandleFunc("GET /api/v1/version", s.version)
	return mux
}

type server struct {
	opts Options
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
func (s *server) run(w http.ResponseWriter, r *http.Request) {
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
func (s *server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"dyalogVersion": s.opts.DyalogVersion})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}
