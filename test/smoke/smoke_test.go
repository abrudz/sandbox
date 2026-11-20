// Tests of scripts/smoke against fake services. Each fake answers like the
// sandbox except in the one respect a test breaks, and the script must fail
// that check alone. The fakes recognise the script's programs by what each
// one does: printing Hello, World!, waiting with ⎕DL past the time limit,
// and opening /dev/tcp.
package smoke

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var checks = []string{"page", "hello", "timeout", "network"}

// fakeService starts a service that behaves like the sandbox except for the
// check named by broken, and returns its URL.
func fakeService(t *testing.T, broken string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			if broken == "page" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<!DOCTYPE html>\n<title>dyalog.run</title>\n"))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/run" {
			http.NotFound(w, r)
			return
		}
		var req struct{ Code string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		zero, one := 0, 1
		res := map[string]any{"stdout": "", "stderr": "", "exitCode": &zero, "signal": nil, "timedOut": false,
			"stdoutTruncated": false, "stderrTruncated": false, "wallTimeMs": 20, "userTimeMs": 10, "sysTimeMs": 5,
			"dyalogVersion": "21.0.55109.0"}
		switch {
		case strings.Contains(req.Code, "Hello, World!"):
			res["stdout"] = "Hello, World!\r"
			if broken == "hello" {
				res["stdout"] = "Goodbye\r"
			}
		case strings.Contains(req.Code, "⎕DL"):
			res["exitCode"], res["signal"], res["timedOut"], res["wallTimeMs"] = nil, "SIGKILL", true, 10012
			if broken == "timeout" {
				res["exitCode"], res["signal"], res["timedOut"], res["wallTimeMs"] = &zero, nil, false, 60015
				res["stdout"] = "still running\r"
			}
		case strings.Contains(req.Code, "/dev/tcp"):
			res["stdout"] = "bash: connect: Network is unreachable\rbash: line 1: /dev/tcp/1.1.1.1/80: Network is unreachable\rexit 1\r"
			if broken == "network" {
				res["stdout"] = "exit 0\r"
			}
		default:
			res["exitCode"], res["stderr"] = &one, "unrecognised program\r"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// smoke runs scripts/smoke against url and returns its output and whether
// it exited with status 0.
func smoke(t *testing.T, url string) (string, bool) {
	t.Helper()
	out, err := exec.Command(filepath.Join("..", "..", "scripts", "smoke"), url).CombinedOutput()
	if _, failed := err.(*exec.ExitError); err != nil && !failed {
		t.Fatalf("running scripts/smoke: %v", err)
	}
	return string(out), err == nil
}

// assertChecks requires one PASS or FAIL line per check, failing exactly
// the checks named in failing.
func assertChecks(t *testing.T, out string, failing ...string) {
	t.Helper()
	for _, c := range checks {
		want := "PASS"
		for _, f := range failing {
			if f == c {
				want = "FAIL"
			}
		}
		if !regexp.MustCompile(`(?m)^` + want + ` ` + c + `\b`).MatchString(out) {
			t.Errorf("no %q line for %s in the output:\n%s", want+" "+c, c, out)
		}
	}
}

func TestSmokePassesAgainstAServiceThatServesThePageAndSandboxesRuns(t *testing.T) {
	out, ok := smoke(t, fakeService(t, ""))

	if !ok {
		t.Errorf("scripts/smoke failed:\n%s", out)
	}
	assertChecks(t, out)
}

func TestSmokeFailsWhenThePageIsNotServed(t *testing.T) {
	out, ok := smoke(t, fakeService(t, "page"))

	if ok {
		t.Errorf("scripts/smoke succeeded:\n%s", out)
	}
	assertChecks(t, out, "page")
}

func TestSmokeFailsWhenHelloWorldPrintsSomethingElse(t *testing.T) {
	out, ok := smoke(t, fakeService(t, "hello"))

	if ok {
		t.Errorf("scripts/smoke succeeded:\n%s", out)
	}
	assertChecks(t, out, "hello")
}

func TestSmokeFailsWhenARunOutlivesTheTimeLimit(t *testing.T) {
	out, ok := smoke(t, fakeService(t, "timeout"))

	if ok {
		t.Errorf("scripts/smoke succeeded:\n%s", out)
	}
	assertChecks(t, out, "timeout")
}

func TestSmokeFailsWhenARunReachesTheNetwork(t *testing.T) {
	out, ok := smoke(t, fakeService(t, "network"))

	if ok {
		t.Errorf("scripts/smoke succeeded:\n%s", out)
	}
	assertChecks(t, out, "network")
}

func TestSmokeFailsEveryCheckWhenTheServiceIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	out, ok := smoke(t, url)

	if ok {
		t.Errorf("scripts/smoke succeeded:\n%s", out)
	}
	assertChecks(t, out, checks...)
}
