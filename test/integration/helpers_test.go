//go:build integration

// Package integration holds black-box tests that run against a built sandbox
// image and a running container started from it. CI provides:
//
//	SANDBOX_URL        base URL of the running container's API
//	SANDBOX_IMAGE      reference of the image under test
//	SANDBOX_CONTAINER  name of the running container
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var client = &http.Client{Timeout: 60 * time.Second}

// runResponse is the body of a POST /api/v1/run response.
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

	// fields holds every key of the response, to check that a field is
	// present rather than defaulted.
	fields map[string]json.RawMessage
}

// run submits code, input and args to POST /api/v1/run on the CI container
// and fails the test unless the API answers 200 with a JSON body.
func run(t *testing.T, code, input string, args ...string) runResponse {
	t.Helper()
	return runAt(t, env(t, "SANDBOX_URL"), code, input, args...)
}

// runAt is run against the container whose API is at baseURL.
func runAt(t *testing.T, baseURL, code, input string, args ...string) runResponse {
	t.Helper()
	if args == nil {
		args = []string{}
	}
	body, err := json.Marshal(map[string]any{"code": code, "input": input, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	data := requestAt(t, baseURL, http.MethodPost, "/api/v1/run", body)
	var r runResponse
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("POST /api/v1/run: response is not JSON: %v\n%s", err, data)
	}
	if err := json.Unmarshal(data, &r.fields); err != nil {
		t.Fatalf("POST /api/v1/run: response is not a JSON object: %v\n%s", err, data)
	}
	return r
}

// request sends a request to the CI container and returns the body of a 200
// response; any other status fails the test.
func request(t *testing.T, method, path string, body []byte) []byte {
	t.Helper()
	return requestAt(t, env(t, "SANDBOX_URL"), method, path, body)
}

// requestAt is request against the container whose API is at baseURL.
func requestAt(t *testing.T, baseURL, method, path string, body []byte) []byte {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, http.StatusOK, data)
	}
	return data
}

var lineEnd = regexp.MustCompile(`\r\n|\r|\n`)

// lines splits interpreter output into lines. Dyalog in script mode ends
// output lines with CR; LF and CRLF are accepted too.
func lines(output string) []string {
	output = strings.TrimRight(output, "\r\n")
	if output == "" {
		return nil
	}
	return lineEnd.Split(output, -1)
}

func assertLines(t *testing.T, output string, want ...string) {
	t.Helper()
	if got := lines(output); !slices.Equal(got, want) {
		t.Errorf("output lines %q, want %q (raw output %q)", got, want, output)
	}
}

func assertExitCode(t *testing.T, r runResponse, want int) {
	t.Helper()
	if r.ExitCode == nil {
		signal := "none"
		if r.Signal != nil {
			signal = *r.Signal
		}
		t.Errorf("exit code is null (signal %s), want %d", signal, want)
	} else if *r.ExitCode != want {
		t.Errorf("exit code %d, want %d", *r.ExitCode, want)
	}
}

// env returns the named environment variable and fails the test when it is
// unset, so a misconfigured run fails instead of passing vacuously.
func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; integration tests run against a built image and a running container (see .github/workflows/ci.yml)", name)
	}
	return v
}

// docker runs the docker CLI and returns its combined output.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}
