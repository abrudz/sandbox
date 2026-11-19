package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/sandbox"
)

// fakeExecutor records the job it receives and returns a fixed outcome.
type fakeExecutor struct {
	got    sandbox.Job
	result sandbox.Result
	err    error
}

func (f *fakeExecutor) Run(_ context.Context, job sandbox.Job) (sandbox.Result, error) {
	f.got = job
	return f.result, f.err
}

const version = "21.0.55109.0"

func postRun(t *testing.T, fake *fakeExecutor, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := api.NewHandler(api.Options{Executor: fake, DyalogVersion: version})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body)
	}
	return got
}

func TestPostRunPassesCodeInputAndArgumentsToTheExecutor(t *testing.T) {
	fake := &fakeExecutor{}

	rec := postRun(t, fake, `{"code":"⎕←2+2","input":"abc\n","args":["x","y z"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
	}
	want := sandbox.Job{Code: []byte("⎕←2+2"), Input: []byte("abc\n"), Args: []string{"x", "y z"}}
	if !reflect.DeepEqual(fake.got, want) {
		t.Fatalf("executor received %+v, want %+v", fake.got, want)
	}
}

func TestPostRunReturnsTheResultInTheDocumentedShape(t *testing.T) {
	exit := 3
	fake := &fakeExecutor{result: sandbox.Result{
		Stdout:   []byte("4\r"),
		Stderr:   []byte("DOMAIN ERROR\n"),
		ExitCode: &exit,
		WallTime: 1500 * time.Millisecond,
		UserTime: 600 * time.Millisecond,
		SysTime:  25 * time.Millisecond,
	}}

	rec := postRun(t, fake, `{"code":"⎕←2+2","input":"","args":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	want := map[string]any{
		"stdout":          "4\r",
		"stderr":          "DOMAIN ERROR\n",
		"exitCode":        3.0,
		"signal":          nil,
		"timedOut":        false,
		"stdoutTruncated": false,
		"stderrTruncated": false,
		"wallTimeMs":      1500.0,
		"userTimeMs":      600.0,
		"sysTimeMs":       25.0,
		"dyalogVersion":   version,
	}
	if got := decode(t, rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("response\n got %v\nwant %v", got, want)
	}
}

func TestPostRunReplacesInvalidUTF8InOutput(t *testing.T) {
	zero := 0
	fake := &fakeExecutor{result: sandbox.Result{Stdout: []byte("a\xffb"), ExitCode: &zero}}

	rec := postRun(t, fake, `{"code":"","input":"","args":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decode(t, rec)["stdout"]; got != "a�b" {
		t.Fatalf("stdout %q, want %q", got, "a�b")
	}
}

func TestPostRunAnswers503WhenTheSandboxCannotRunTheJob(t *testing.T) {
	fake := &fakeExecutor{err: errors.New("sandbox setup failed")}

	rec := postRun(t, fake, `{"code":"⎕←1","input":"","args":[]}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetVersionReportsTheDyalogVersion(t *testing.T) {
	h := api.NewHandler(api.Options{Executor: &fakeExecutor{}, DyalogVersion: version})
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decode(t, rec)["dyalogVersion"]; got != version {
		t.Fatalf("dyalogVersion %v, want %q", got, version)
	}
}
