// Package api implements the HTTP interface of the sandbox runner.
package api

import "net/http"

// NewHandler returns the runner's HTTP handler.
func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
}
