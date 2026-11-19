//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRunPrintsHelloWorld(t *testing.T) {
	r := run(t, "⎕←'Hello, World!'\n", "")

	switch r.Stdout {
	case "Hello, World!\r", "Hello, World!\n", "Hello, World!\r\n":
	default:
		t.Errorf("stdout %q, want Hello, World! followed by a line ending", r.Stdout)
	}
	assertExitCode(t, r, 0)
}

func TestRunCallsAMultiLineDfnDefinedEarlierInTheCode(t *testing.T) {
	r := run(t, "f←{\n  ⍵+1\n}\n⎕←f 2\n", "")

	assertLines(t, r.Stdout, "3")
}

func TestRunReadsSuccessiveInputLinesWithQuoteQuad(t *testing.T) {
	r := run(t, "a←⍞\nb←⍞\n⎕←b\n⎕←a\n", "first\nsecond\n")

	assertLines(t, r.Stdout, "second", "first")
}

func TestRunPrintsAWideResultOnOneLine(t *testing.T) {
	numbers := make([]string, 40)
	for i := range numbers {
		numbers[i] = fmt.Sprint(i + 1)
	}

	r := run(t, "⎕←⍳40\n", "")

	assertLines(t, r.Stdout, strings.Join(numbers, " "))
}

func TestRunStopsAtAnUntrappedError(t *testing.T) {
	r := run(t, "⎕←'before'\n1÷0\n⎕←'after'\n", "")

	if !strings.Contains(r.Stderr, "DOMAIN ERROR") {
		t.Errorf("stderr %q does not contain DOMAIN ERROR", r.Stderr)
	}
	assertLines(t, r.Stdout, "before")
	switch {
	case r.ExitCode == nil:
		t.Errorf("exit code is null, want non-zero")
	case *r.ExitCode == 0:
		t.Errorf("exit code 0, want non-zero")
	}
}

func TestRunExitsWithTheStatusGivenToQuadOff(t *testing.T) {
	r := run(t, "⎕OFF 3\n", "")

	assertExitCode(t, r, 3)
}

func TestRunPassesArgumentsToTheProgram(t *testing.T) {
	r := run(t, "{⎕←⍵}¨¯2↑2⎕NQ#'GetCommandLineArgs'\n", "", "foo", "bar baz")

	assertLines(t, r.Stdout, "foo", "bar baz")
}

func TestRunExecutesALastLineWithoutANewline(t *testing.T) {
	r := run(t, "⎕←'first'\n⎕←'last'", "")

	assertLines(t, r.Stdout, "first", "last")
}

func TestRunDoesNotRepeatStdoutOnStderr(t *testing.T) {
	r := run(t, "⎕←'visible on stdout'\n", "")

	assertLines(t, r.Stdout, "visible on stdout")
	if strings.Contains(r.Stderr, "visible on stdout") {
		t.Errorf("stderr repeats stdout: %q", r.Stderr)
	}
}

func TestRunReportsWallUserAndSystemTime(t *testing.T) {
	r := run(t, "⎕←+/⍳1e6\n", "")

	for _, field := range []string{"wallTimeMs", "userTimeMs", "sysTimeMs"} {
		if _, ok := r.fields[field]; !ok {
			t.Errorf("response has no %s field", field)
		}
	}
	if r.WallTimeMs <= 0 {
		t.Errorf("wallTimeMs %d, want > 0", r.WallTimeMs)
	}
	if r.UserTimeMs+r.SysTimeMs <= 0 {
		t.Errorf("userTimeMs %d + sysTimeMs %d, want > 0", r.UserTimeMs, r.SysTimeMs)
	}
}

func TestVersionMatchesTheInterpreterInsideARun(t *testing.T) {
	var v struct {
		DyalogVersion string `json:"dyalogVersion"`
	}
	data := request(t, http.MethodGet, "/api/v1/version", nil)
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("GET /api/v1/version: response is not JSON: %v\n%s", err, data)
	}
	if v.DyalogVersion == "" {
		t.Fatalf("GET /api/v1/version: empty dyalogVersion: %s", data)
	}

	r := run(t, "⎕←2⊃'.'⎕WG'APLVersion'\n", "")

	assertLines(t, r.Stdout, v.DyalogVersion)
	if r.DyalogVersion != v.DyalogVersion {
		t.Errorf("run result dyalogVersion %q, want %q", r.DyalogVersion, v.DyalogVersion)
	}
}
