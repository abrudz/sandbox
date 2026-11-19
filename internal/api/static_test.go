package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abrudz/sandbox/internal/api"
)

func TestGetRootServesTheFrontend(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>dyalog.run</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend.js"), []byte("init();"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := api.NewHandler(api.Options{Executor: &fakeExecutor{}, WebDir: dir})

	for path, want := range map[string]string{"/": "<title>dyalog.run</title>", "/frontend.js": "init();"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: status %d, body %q; want 200 containing %q", path, rec.Code, rec.Body, want)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /missing.js: status %d, want 404", rec.Code)
	}
}
