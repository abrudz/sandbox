//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// post sends a POST /api/v1/run body to the container at baseURL and
// returns the status and body, whatever the status.
func post(t *testing.T, baseURL, body string) (int, string) {
	t.Helper()
	resp, err := client.Post(baseURL+"/api/v1/run", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/run: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// runInBackground starts code on the container at baseURL and delivers the
// response once the run ends.
func runInBackground(baseURL, code string) <-chan runOutcome {
	done := make(chan runOutcome, 1)
	go func() {
		body := `{"code":` + jsonString(code) + `,"input":"","args":[]}`
		resp, err := client.Post(baseURL+"/api/v1/run", "application/json", strings.NewReader(body))
		if err != nil {
			done <- runOutcome{err: err}
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		done <- runOutcome{status: resp.StatusCode, body: string(data)}
	}()
	return done
}

type runOutcome struct {
	status int
	body   string
	err    error
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// waitForInterpreter waits until a run's interpreter is running in container.
func waitForInterpreter(t *testing.T, container string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !slices.ContainsFunc(leftovers(t, container), isInterpreter) {
		if time.Now().After(deadline) {
			t.Fatal("the interpreter did not start within 10 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTheServiceAnswers400ForMalformedJSONAnd413ForAnOversizedRequest(t *testing.T) {
	url := env(t, "SANDBOX_URL")

	if status, body := post(t, url, `{"code":`); status != http.StatusBadRequest {
		t.Errorf("malformed JSON: status %d, want 400: %s", status, body)
	}
	oversized := `{"code":"` + strings.Repeat("x", 70*1024) + `","input":"","args":[]}`
	if status, body := post(t, url, oversized); status != http.StatusRequestEntityTooLarge {
		t.Errorf("70 KiB of code: status %d, want 413: %s", status, body)
	}
}

func TestTheServiceAnswers415ForARunRequestThatIsNotJSON(t *testing.T) {
	url := env(t, "SANDBOX_URL")

	resp, err := client.Post(url+"/api/v1/run", "text/plain", strings.NewReader(`{"code":"⎕←1","input":"","args":[]}`))

	if err != nil {
		t.Fatalf("POST /api/v1/run: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("Content-Type text/plain: status %d, want 415", resp.StatusCode)
	}
}

func TestTheServiceAnswers400ForAnArgumentContainingNUL(t *testing.T) {
	url := env(t, "SANDBOX_URL")

	if status, body := post(t, url, `{"code":"⎕←1","input":"","args":["a\u0000b"]}`); status != http.StatusBadRequest {
		t.Errorf("status %d, want 400: %s", status, body)
	}
}

func TestSandboxConcurrencySetsTheNumberOfSlots(t *testing.T) {
	url, container := startContainer(t, "SANDBOX_CONCURRENCY=1")
	first := runInBackground(url, "⎕DL 3\n⎕←'finished'\n")
	waitForInterpreter(t, container)

	if status, body := post(t, url, `{"code":"⎕←1","input":"","args":[]}`); status != http.StatusTooManyRequests {
		t.Errorf("second run with one slot: status %d, want 429: %s", status, body)
	}
	if got := <-first; got.err != nil || got.status != http.StatusOK {
		t.Errorf("run holding the slot: %+v, want status 200", got)
	}
}

func TestSandboxClientRateSetsThePerMinuteLimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_CLIENT_RATE=2")
	body := `{"code":"⎕←1","input":"","args":[]}`

	for i := 1; i <= 2; i++ {
		if status, resp := post(t, url, body); status != http.StatusOK {
			t.Fatalf("run %d: status %d, want 200: %s", i, status, resp)
		}
	}
	if status, resp := post(t, url, body); status != http.StatusTooManyRequests {
		t.Errorf("third run in a minute: status %d, want 429: %s", status, resp)
	}
}

func TestSIGTERMLetsRunsFinishRefusesNewOnesAndEndsTheService(t *testing.T) {
	url, container := startContainer(t)
	first := runInBackground(url, "⎕DL 3\n⎕←'finished'\n")
	waitForInterpreter(t, container)

	if out, err := docker(t, "kill", "--signal", "TERM", container); err != nil {
		t.Fatalf("sending SIGTERM: %v\n%s", err, out)
	}

	deadline := time.Now().Add(time.Second)
	for {
		status, _ := post(t, url, `{"code":"⎕←1","input":"","args":[]}`)
		if status == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a second after SIGTERM a new run got status %d, want 503", status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := <-first
	if got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "finished") {
		t.Errorf("run in progress at SIGTERM: %+v, want status 200 with its output", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := dockerContext(ctx, "wait", container)
	if err != nil {
		t.Fatalf("the container did not stop within 15 s of SIGTERM: %v\n%s", err, out)
	}
	if code := strings.TrimSpace(out); code != "0" {
		t.Errorf("container exit code %s, want 0", code)
	}
}
