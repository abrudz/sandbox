//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The defaults from the plan's Sandbox limits table.
const (
	defaultTimeout     = 10 * time.Second
	defaultOutputLimit = 128 * 1024
)

func TestRunPastTheWallClockLimitIsKilled(t *testing.T) {
	r := run(t, "⎕DL 60\n⎕←'still running'\n", "")

	if !r.TimedOut {
		t.Errorf("timedOut is false, want true (wall time %d ms)", r.WallTimeMs)
	}
	if r.ExitCode != nil {
		t.Errorf("exit code %d, want null for a killed run", *r.ExitCode)
	}
	if strings.Contains(r.Stdout, "still running") {
		t.Errorf("the program ran past its limit: stdout %q", r.Stdout)
	}
	if limit := defaultTimeout.Milliseconds(); r.WallTimeMs < limit || r.WallTimeMs > limit+2000 {
		t.Errorf("wall time %d ms, want between %d and %d", r.WallTimeMs, limit, limit+2000)
	}
}

func TestStdoutPastTheCapIsTruncatedAndFlagged(t *testing.T) {
	r := run(t, "⎕←200000⍴'x'\n", "")

	if len(r.Stdout) != defaultOutputLimit {
		t.Errorf("stdout has %d bytes, want %d", len(r.Stdout), defaultOutputLimit)
	}
	if !r.StdoutTruncated {
		t.Errorf("stdoutTruncated is false, want true")
	}
	if r.TimedOut {
		t.Errorf("timedOut is true, want false")
	}
	assertExitCode(t, r, 0)
}

func TestStderrPastTheCapIsTruncatedAndFlagged(t *testing.T) {
	r := run(t, "{}⎕SH'yes x | head -c 200000 >&2'\n", "")

	if len(r.Stderr) != defaultOutputLimit {
		t.Errorf("stderr has %d bytes, want %d", len(r.Stderr), defaultOutputLimit)
	}
	if !r.StderrTruncated {
		t.Errorf("stderrTruncated is false, want true")
	}
	if r.TimedOut {
		t.Errorf("timedOut is true, want false")
	}
	assertExitCode(t, r, 0)
}

// Tests that inspect leftovers use a container of their own, so nothing an
// earlier test left behind can affect them.

func TestClosingTheConnectionKillsTheRunWithinOneSecond(t *testing.T) {
	url, container := startContainer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		body := strings.NewReader(`{"code":"⎕DL 30\n","input":"","args":[]}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/v1/run", body)
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	startDeadline := time.Now().Add(10 * time.Second)
	for !slices.ContainsFunc(leftovers(t, container), isInterpreter) {
		if time.Now().After(startDeadline) {
			t.Fatalf("the interpreter did not start within 10 s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	<-finished
	deadline := time.Now().Add(time.Second)
	for {
		left := leftovers(t, container)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("one second after the client disconnected, the run left %q", left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isInterpreter reports whether leftover is a running dyalog process.
func isInterpreter(leftover string) bool {
	f := strings.Fields(leftover)
	return len(f) == 5 && f[0] == "process" && f[3] != "Z" && f[4] == "dyalog"
}

func TestNothingRemainsAfterACompletedRun(t *testing.T) {
	url, container := startContainer(t)

	runAt(t, url, "⎕←'done'\n", "")

	if left := leftovers(t, container); len(left) > 0 {
		t.Errorf("a completed run left %q", left)
	}
}

func TestNothingRemainsAfterATimedOutRun(t *testing.T) {
	url, container := startContainer(t, "SANDBOX_TIMEOUT=2s")

	r := runAt(t, url, "⎕DL 30\n", "")

	if !r.TimedOut {
		t.Fatalf("the run did not time out (wall time %d ms)", r.WallTimeMs)
	}
	if left := leftovers(t, container); len(left) > 0 {
		t.Errorf("a timed-out run left %q", left)
	}
}

func TestVersionReportsTheWallClockLimitSetBySandboxTimeout(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_TIMEOUT=2s")

	var v struct {
		TimeoutMs *int64 `json:"timeoutMs"`
	}
	data := requestAt(t, url, http.MethodGet, "/api/v1/version", nil)
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("GET /api/v1/version: response is not JSON: %v\n%s", err, data)
	}
	if v.TimeoutMs == nil || *v.TimeoutMs != 2000 {
		t.Errorf("GET /api/v1/version: timeoutMs is not 2000 with SANDBOX_TIMEOUT=2s: %s", data)
	}
}

func TestSandboxTimeoutSetsTheWallClockLimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_TIMEOUT=2s")

	r := runAt(t, url, "⎕DL 3\n⎕←'finished'\n", "")

	if !r.TimedOut {
		t.Errorf("timedOut is false, want true (wall time %d ms)", r.WallTimeMs)
	}
	if strings.Contains(r.Stdout, "finished") {
		t.Errorf("the program ran past the 2 s limit: stdout %q", r.Stdout)
	}
	if r.WallTimeMs >= 3000 {
		t.Errorf("wall time %d ms, want under 3000 with SANDBOX_TIMEOUT=2s", r.WallTimeMs)
	}
}
