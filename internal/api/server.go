// Package api implements the HTTP interface of the sandbox runner.
package api

import (
	"io"
	"net/http"

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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/v1/run", notImplemented)
	mux.HandleFunc("GET /api/v1/version", notImplemented)
	return mux
}

// healthz reports that the API is serving requests.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
