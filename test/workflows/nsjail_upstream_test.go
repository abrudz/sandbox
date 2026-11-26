package workflows

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nsjailUpstreamScript is the script the nsjail upstream check runs.
var nsjailUpstreamScript = filepath.Join("..", "..", "scripts", "nsjail-upstream")

// nsjailUpstream runs scripts/nsjail-upstream and reports whether it found
// something to report, with what it printed.
func nsjailUpstream(t *testing.T, args ...string) (bool, string) {
	t.Helper()
	out, err := exec.Command(nsjailUpstreamScript, args...).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, string(out)
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, string(out)
	}
	t.Fatalf("scripts/nsjail-upstream: %v\n%s", err, out)
	return false, ""
}

const (
	pinnedCommit = "f78475530b46d0186111a9096b30725f816b55fe"
	masterHead   = "ec259a3edb34c3b138fbad859c09e0e70259ff8b"
)

func TestTheNsjailCheckReportsNothingWhenThePinIsMastersHead(t *testing.T) {
	if report, out := nsjailUpstream(t, masterHead, masterHead, "identical", "0", "3.6"); report {
		t.Errorf("the pin is master's head: reported\n%s", out)
	}
}

func TestTheNsjailCheckReportsHowFarMasterHasMovedPastThePin(t *testing.T) {
	report, out := nsjailUpstream(t, pinnedCommit, masterHead, "ahead", "157", "3.6")

	if !report {
		t.Fatalf("master is 157 commits past the pin: nothing reported\n%s", out)
	}
	for _, want := range []string{
		"157 commits",
		"https://github.com/google/nsjail/compare/" + pinnedCommit + "..." + masterHead,
		"3.6",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
}

func TestTheNsjailCheckReportsAMasterThatNoLongerContainsThePin(t *testing.T) {
	// GitHub compares the pin with master: "behind" or "diverged" means the
	// pinned commit is not in master's history.
	for _, status := range []string{"behind", "diverged"} {
		report, out := nsjailUpstream(t, pinnedCommit, masterHead, status, "3", "3.6")
		if !report || !strings.Contains(out, "no longer contains") {
			t.Errorf("status %s: report %v, want one that says master no longer contains the pin:\n%s", status, report, out)
		}
	}
}

func TestTheNsjailCheckFailsOnAComparisonItCannotRead(t *testing.T) {
	for _, args := range [][]string{
		{pinnedCommit, masterHead, "", "", "3.6"},
		{pinnedCommit, masterHead, "ahead", "many", "3.6"},
		{"3.6", masterHead, "ahead", "1", "3.6"},
		{pinnedCommit, masterHead, "ahead"},
	} {
		out, err := exec.Command(nsjailUpstreamScript, args...).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() < 2 {
			t.Errorf("arguments %q: %v, want an exit status of 2 or more\n%s", args, err, out)
		}
	}
}
