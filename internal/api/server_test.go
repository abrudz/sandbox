package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abrudz/sandbox/internal/api"
)

func TestGetHealthzReturnsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	api.NewHandler(api.Options{Executor: &fakeExecutor{}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: status %d, want %d", rec.Code, http.StatusOK)
	}
}
