//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The defaults from the plan's Sandbox limits table, beyond those in
// limits_test.go.
const defaultPids = 64

// processCountCode spawns n background processes from ⎕SH, then prints how
// many processes the run can see. Counting uses only shell builtins, so it
// still works once no more processes can be created.
func processCountCode(n int) string {
	return fmt.Sprintf(`{⎕←⍵}¨⎕SH'for i in $(seq %d); do sleep 30 >/dev/null 2>&1 & done 2>/dev/null; n=0; for p in /proc/[0-9]*; do n=$((n+1)); done; echo $n'`+"\n", n)
}

// childAllocationCode has a child of ⎕SH buffer bytes bytes in memory and
// prints the child's exit status; 137 means SIGKILL.
func childAllocationCode(bytes int) string {
	return fmt.Sprintf(`{⎕←⍵}¨⎕SH'head -c %d /dev/zero | tail > /dev/null; echo "status $?"'`+"\n", bytes)
}

// busyLoop is a shell command that keeps one CPU busy for two seconds.
const busyLoop = `timeout 2 sh -c "while :; do :; done"`

func firstLineInt(t *testing.T, r runResponse) int64 {
	t.Helper()
	l := lines(r.Stdout)
	if len(l) == 0 {
		t.Fatalf("no output; stderr %q", r.Stderr)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(l[0]), 10, 64)
	if err != nil {
		t.Fatalf("first output line %q is not an integer; stderr %q", l[0], r.Stderr)
	}
	return n
}

func TestRunCannotOpenAnOutboundTCPConnection(t *testing.T) {
	r := run(t, `{⎕←⍵}¨⎕SH'bash -c ''exec 3<>/dev/tcp/1.1.1.1/80'' 2>&1; echo "exit $?"'`+"\n", "")

	if !strings.Contains(r.Stdout, "Network is unreachable") {
		t.Errorf("output %q does not report an unreachable network", r.Stdout)
	}
	if strings.Contains(r.Stdout, "exit 0") {
		t.Errorf("the connection succeeded: %q", r.Stdout)
	}
}

func TestRunCannotWriteOutsideTmpAndHome(t *testing.T) {
	r := run(t, "{⎕←⍵}¨{0::'failed' ⋄ _←(⊂'a')⎕NPUT ⍵ ⋄ 'written'}¨'/marker' '/usr/marker' '/etc/marker' '/opt/mdyalog/marker' '/job/marker'\n", "")

	assertLines(t, r.Stdout, "failed", "failed", "failed", "failed", "failed")
}

func TestRunCannotWriteMoreThanTheTmpfsLimitToTmpOrHome(t *testing.T) {
	// Each tmpfs holds 16 MiB: the first 10 MB file fits, the second does not.
	r := run(t, `home←2⎕NQ#'GetEnvironment' 'HOME'
write←{0::'failed' ⋄ _←(⊂10e6⍴'x')⎕NPUT ⍵ ⋄ 'written'}
{⎕←⍵}¨write¨'/tmp/a' '/tmp/b' (home,'/a') (home,'/b')
`, "")

	assertLines(t, r.Stdout, "written", "failed", "written", "failed")
}

func TestFilesDoNotSurviveFromOneRunToTheNext(t *testing.T) {
	first := run(t, `home←2⎕NQ#'GetEnvironment' 'HOME'
_←(⊂'a')⎕NPUT'/tmp/marker'
_←(⊂'a')⎕NPUT home,'/marker'
⎕←⎕NEXISTS'/tmp/marker' (home,'/marker')
`, "")
	assertLines(t, first.Stdout, "1 1")

	second := run(t, "home←2⎕NQ#'GetEnvironment' 'HOME'\n⎕←⎕NEXISTS'/tmp/marker' (home,'/marker')\n", "")

	assertLines(t, second.Stdout, "0 0")
}

func TestAllocatingPastMAXWSGivesWSFULL(t *testing.T) {
	r := run(t, "x←1e9⍴0.5\n⎕←'allocated'\n", "")

	if !strings.Contains(r.Stderr, "WS FULL") {
		t.Errorf("stderr %q does not report WS FULL", r.Stderr)
	}
	if strings.Contains(r.Stdout, "allocated") {
		t.Errorf("an 8 GB array was allocated")
	}
}

func TestAChildPastTheMemoryLimitIsKilledAndTheServiceKeepsServing(t *testing.T) {
	r := run(t, childAllocationCode(1_000_000_000), "")

	assertLines(t, r.Stdout, "status 137")
	after := run(t, "⎕←'still serving'\n", "")
	assertLines(t, after.Stdout, "still serving")
}

func TestProcessCreationStopsAtTheProcessLimit(t *testing.T) {
	r := run(t, processCountCode(500), "")

	if n := firstLineInt(t, r); n > defaultPids || n < 10 {
		t.Errorf("the run saw %d processes, want between 10 and %d", n, defaultPids)
	}
	if r.TimedOut {
		t.Errorf("the run timed out instead of ending")
	}
	assertExitCode(t, r, 0)
}

func TestTwoBusyProcessesShareOneCPU(t *testing.T) {
	r := run(t, "_←⎕SH'"+busyLoop+" & "+busyLoop+"; wait'\n", "")

	assertExitCode(t, r, 0)
	cpu, wall := r.UserTimeMs+r.SysTimeMs, r.WallTimeMs
	if float64(cpu) > 1.2*float64(wall) {
		t.Errorf("CPU time %d ms in %d ms wall-clock time, want at most one CPU", cpu, wall)
	}
	if float64(cpu) < 0.7*float64(wall) {
		t.Errorf("CPU time %d ms in %d ms wall-clock time: the busy loops did not run", cpu, wall)
	}
}

func TestBlockedSystemCallThroughQuadNAFailsWithEPERM(t *testing.T) {
	// 268435456 is CLONE_NEWUSER: creating a user namespace needs no
	// capability, so only the seccomp policy can refuse it.
	r := run(t, `⎕NA'I4 libc.so.6|unshare I4'
⎕NA'P libc.so.6|__errno_location'
⎕NA'libc.so.6|memcpy >I4[1] P P'
result←unshare 268435456
errno←⊃memcpy 1 __errno_location 4
⎕←result errno
`, "")

	assertLines(t, r.Stdout, "¯1 1")
}

func TestRunIsUnprivilegedAndSeesOnlyItsOwnProcesses(t *testing.T) {
	r := run(t, `{⎕←⍵}¨⎕SH'id -u; grep CapEff /proc/self/status; cat /proc/[0-9]*/comm'`+"\n", "")

	out := lines(r.Stdout)
	if len(out) < 3 {
		t.Fatalf("output %q too short; stderr %q", r.Stdout, r.Stderr)
	}
	if out[0] == "0" {
		t.Errorf("the program runs as root")
	}
	if f := strings.Fields(out[1]); len(f) != 2 || f[1] != "0000000000000000" {
		t.Errorf("effective capabilities %q, want none", out[1])
	}
	for _, name := range out[2:] {
		if slices.Contains([]string{"tini", "entrypoint", "runner-api", "nsjail"}, name) {
			t.Errorf("the run can see %s; processes visible: %q", name, out[2:])
		}
	}
}

func TestConcurrentRunsCannotSeeEachOthersFilesOrProcesses(t *testing.T) {
	url, container := startContainer(t)
	firstDone := make(chan error, 1)
	go func() {
		body := `{"code":"home←2⎕NQ#'GetEnvironment' 'HOME'\n_←(⊂'a')⎕NPUT'/tmp/first-run'\n_←(⊂'a')⎕NPUT home,'/first-run'\n_←⎕SH'sleep 5.000123 >/dev/null 2>&1 &'\n⎕DL 5\n","input":"","args":[]}`
		resp, err := client.Post(url+"/api/v1/run", "application/json", strings.NewReader(body))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %d", resp.StatusCode)
			}
		}
		firstDone <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !slices.ContainsFunc(leftovers(t, container), func(l string) bool { return strings.HasSuffix(l, " sleep") }) {
		if time.Now().After(deadline) {
			t.Fatalf("the first run's sleep did not start within 10 s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// grep's own command line holds "5[.]000123", which the pattern does not match.
	second := runAt(t, url, `home←2⎕NQ#'GetEnvironment' 'HOME'
⎕←⎕NEXISTS'/tmp/first-run' (home,'/first-run')
⎕←⊃⎕SH'cat /proc/[0-9]*/cmdline 2>/dev/null | tr "\0" " " | grep -c "5[.]000123" || true'
`, "")

	assertLines(t, second.Stdout, "0 0", "0")
	if err := <-firstDone; err != nil {
		t.Errorf("first run: %v", err)
	}
}

func TestSandboxPidsSetsTheProcessLimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_PIDS=20")

	r := runAt(t, url, processCountCode(100), "")

	if n := firstLineInt(t, r); n > 20 {
		t.Errorf("the run saw %d processes, want at most 20 with SANDBOX_PIDS=20", n)
	}
}

func TestSandboxMemorySetsTheMemoryLimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_MEMORY=128M")

	r := runAt(t, url, childAllocationCode(200_000_000), "")

	assertLines(t, r.Stdout, "status 137")
}

func TestSandboxMemorySetsTheWorkspaceToHalfTheMemoryLimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_MEMORY=128M")

	r := runAt(t, url, "⎕←⎕WA\n", "")

	if wa := firstLineInt(t, r); wa <= 0 || wa > 64<<20 {
		t.Errorf("⎕WA %d, want at most %d (half of 128 MiB)", wa, 64<<20)
	}
}

func TestSandboxCpusSetsTheCPULimit(t *testing.T) {
	url, _ := startContainer(t, "SANDBOX_CPUS=0.5")

	r := runAt(t, url, "_←⎕SH'"+busyLoop+"; true'\n", "")

	cpu, wall := r.UserTimeMs+r.SysTimeMs, r.WallTimeMs
	if float64(cpu) > 0.6*float64(wall) {
		t.Errorf("CPU time %d ms in %d ms wall-clock time, want at most half a CPU", cpu, wall)
	}
	if float64(cpu) < 0.3*float64(wall) {
		t.Errorf("CPU time %d ms in %d ms wall-clock time: the busy loop did not run", cpu, wall)
	}
}
