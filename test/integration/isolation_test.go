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

// processCountCode has a child of ⎕SH fork up to n sleeping processes, then
// print how many processes the run can see. Perl stops at the first failed
// fork and counts without creating a process; dash, the shell ⎕SH runs,
// exits when a fork fails.
func processCountCode(n int) string {
	return fmt.Sprintf(`{⎕←⍵}¨⎕SH'perl -e ''for (1..%d) { defined(my $pid = fork) or last; if (!$pid) { close STDOUT; close STDERR; sleep 30; exit } } opendir my $d, "/proc"; print scalar(grep { /^\d+$/ } readdir $d), "\n"'''`+"\n", n)
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
		t.Fatalf("no output; %s", describe(r))
	}
	n, err := strconv.ParseInt(strings.TrimSpace(l[0]), 10, 64)
	if err != nil {
		t.Fatalf("first output line %q is not an integer; %s", l[0], describe(r))
	}
	return n
}

// describe summarises a run for failure messages.
func describe(r runResponse) string {
	exit, signal := "null", "null"
	if r.ExitCode != nil {
		exit = strconv.Itoa(*r.ExitCode)
	}
	if r.Signal != nil {
		signal = *r.Signal
	}
	return fmt.Sprintf("exit %s, signal %s, timedOut %v, wall %d ms, stdout %q, stderr %q",
		exit, signal, r.TimedOut, r.WallTimeMs, r.Stdout, r.Stderr)
}

// assertRunLines is assertLines on a run's stdout, reporting the whole run
// when the lines differ.
func assertRunLines(t *testing.T, r runResponse, want ...string) {
	t.Helper()
	if got := lines(r.Stdout); !slices.Equal(got, want) {
		t.Errorf("output lines %q, want %q; %s", got, want, describe(r))
	}
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
	r := run(t, "{⎕←⍵}¨{0::'failed' ⋄ _←(⊂'a')⎕NPUT ⍵ ⋄ 'written'}¨'/marker' '/usr/marker' '/etc/marker' '/opt/mdyalog/marker' '/opt/dotnet/marker' '/job/marker'\n", "")

	assertRunLines(t, r, "failed", "failed", "failed", "failed", "failed", "failed")
}

func TestRunCannotWriteMoreThanTheTmpfsLimitToTmpOrHome(t *testing.T) {
	// Each tmpfs holds 16 MiB: the first 10 MB file fits, the second does not.
	r := run(t, `home←2⎕NQ#'GetEnvironment' 'HOME'
write←{0::'failed' ⋄ _←(⊂10e6⍴'x')⎕NPUT ⍵ ⋄ 'written'}
{⎕←⍵}¨write¨'/tmp/a' '/tmp/b' (home,'/a') (home,'/b')
`, "")

	assertRunLines(t, r, "written", "failed", "written", "failed")
}

func TestFilesDoNotSurviveFromOneRunToTheNext(t *testing.T) {
	first := run(t, `home←2⎕NQ#'GetEnvironment' 'HOME'
_←(⊂'a')⎕NPUT'/tmp/marker'
_←(⊂'a')⎕NPUT home,'/marker'
⎕←⎕NEXISTS'/tmp/marker' (home,'/marker')
`, "")
	assertRunLines(t, first, "1 1")

	second := run(t, "home←2⎕NQ#'GetEnvironment' 'HOME'\n⎕←⎕NEXISTS'/tmp/marker' (home,'/marker')\n", "")

	assertRunLines(t, second, "0 0")
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

// assertKilledForMemory checks that an out-of-memory kill ended the whole
// run: the interpreter was killed with its child, before printing the
// child's status, and not by the time limit.
func assertKilledForMemory(t *testing.T, r runResponse) {
	t.Helper()
	if r.Signal == nil || *r.Signal != "SIGKILL" || r.TimedOut || strings.Contains(r.Stdout, "status") {
		t.Errorf("want the whole run killed with SIGKILL, before the time limit; %s", describe(r))
	}
}

func TestAChildPastTheMemoryLimitEndsTheWholeRunAndTheServiceKeepsServing(t *testing.T) {
	r := run(t, childAllocationCode(1_000_000_000), "")

	assertKilledForMemory(t, r)
	after := run(t, "⎕←'still serving'\n", "")
	assertRunLines(t, after, "still serving")
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
	// 268435456 is CLONE_NEWUSER. The kernel refuses it in a multithreaded
	// process such as the interpreter with EINVAL; a seccomp filter runs
	// before the kernel sees the call, so EPERM comes only from the policy.
	r := run(t, `⎕NA'I4 libc.so.6|unshare I4'
⎕NA'P libc.so.6|__errno_location'
⎕NA'libc.so.6|memcpy >I4[1] P P'
result←unshare 268435456
errno←⊃memcpy 1 __errno_location 4
⎕←result errno
`, "")

	assertRunLines(t, r, "¯1 1")
}

func TestRunCannotTurnOffAddressSpaceRandomisation(t *testing.T) {
	// 262144 is ADDR_NO_RANDOMIZE. nsjail's policy allows personality;
	// Docker's seccomp profile allows five personalities, not this one, and
	// fails the call with EPERM.
	r := run(t, `⎕NA'I4 libc.so.6|personality U8'
⎕NA'P libc.so.6|__errno_location'
⎕NA'libc.so.6|memcpy >I4[1] P P'
result←personality 262144
errno←⊃memcpy 1 __errno_location 4
⎕←result errno
`, "")

	assertRunLines(t, r, "¯1 1")
}

// systemCall makes the x86_64 system call nr with up to six arguments from a
// run, through ⎕NA and the C library's syscall, and returns the run with
// one output line: the result and errno.
func systemCall(t *testing.T, nr int, args ...int64) runResponse {
	t.Helper()
	all := []string{"0", "0", "0", "0", "0", "0"}
	for i, a := range args {
		all[i] = strings.Replace(strconv.FormatInt(a, 10), "-", "¯", 1)
	}
	return run(t, fmt.Sprintf(`⎕NA'I8 libc.so.6|syscall I8 I8 I8 I8 I8 I8 I8'
⎕NA'P libc.so.6|__errno_location'
⎕NA'libc.so.6|memcpy >I4[1] P P'
result←syscall %d %s
errno←⊃memcpy 1 __errno_location 4
⎕←result errno
`, nr, strings.Join(all, " ")), "")
}

func TestRarelyNeededSystemCallsFailWithEPERM(t *testing.T) {
	// The arguments are ones the kernel accepts from an unprivileged process,
	// or rejects with an error other than EPERM, so EPERM comes only from a
	// seccomp filter. Docker's profile already refuses the NUMA calls here.
	for _, c := range []struct {
		name string
		nr   int
		args []int64
	}{
		{"fanotify_init", 300, []int64{0x200, 0}}, // FAN_REPORT_FID, unprivileged since Linux 5.13
		{"modify_ldt", 154, []int64{0, 0, 0}},     // read no bytes of the LDT
		{"vmsplice", 278, []int64{-1, 0, 0, 0}},
		{"name_to_handle_at", 303, []int64{-1, 0, 0, 0, 0}},
		{"mbind", 237, nil},
		{"migrate_pages", 256, nil},
		{"move_pages", 279, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertRunLines(t, systemCall(t, c.nr, c.args...), "¯1 1")
		})
	}
}

func TestSocketsOtherThanUnixAndIPFailWithEPERM(t *testing.T) {
	// socket is system call 41. Some of these families need a capability the
	// run lacks, or a kernel module, and fail anyway; AF_NETLINK needs
	// neither.
	for _, c := range []struct {
		name         string
		family, kind int64
	}{
		{"AF_NETLINK", 16, 3}, // SOCK_RAW
		{"AF_PACKET", 17, 3},
		{"AF_KEY", 15, 3},
		{"AF_TIPC", 30, 5}, // SOCK_SEQPACKET
		{"AF_ALG", 38, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertRunLines(t, systemCall(t, 41, c.family, c.kind, 0), "¯1 1")
		})
	}
}

func TestUnixAndIPSocketsOpen(t *testing.T) {
	for _, c := range []struct {
		name   string
		family int64
	}{
		{"AF_UNIX", 1},
		{"AF_INET", 2},
		{"AF_INET6", 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := systemCall(t, 41, c.family, 1, 0) // SOCK_STREAM
			if got := lines(r.Stdout); len(got) != 1 || strings.HasPrefix(got[0], "¯") {
				t.Errorf("output lines %q, want a descriptor and errno; %s", got, describe(r))
			}
		})
	}
}

func TestIPSocketsWithAProtocolOtherThanTCPOrUDPFailWithEPERM(t *testing.T) {
	// In IPv4 and IPv6 only the default, TCP and UDP protocols are allowed.
	// The kernel loads the module for a protocol it lacks, such as SCTP,
	// DCCP or L2TP, before it checks any capability, and these need none,
	// so a run could bring their code into the host's kernel. MPTCP is
	// built in, and stands for any other protocol. The kernel would
	// otherwise open these sockets or answer EPROTONOSUPPORT or
	// ESOCKTNOSUPPORT, never EPERM.
	for _, c := range []struct {
		name                   string
		family, kind, protocol int64
	}{
		{"SCTP", 2, 1, 132},     // SOCK_STREAM, IPPROTO_SCTP
		{"DCCP", 2, 6, 33},      // SOCK_DCCP, IPPROTO_DCCP
		{"UDP-Lite", 2, 2, 136}, // SOCK_DGRAM, IPPROTO_UDPLITE
		{"L2TP", 2, 2, 115},     // IPPROTO_L2TP
		{"MPTCP", 2, 1, 262},    // IPPROTO_MPTCP
		{"SCTP over IPv6", 10, 1, 132},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertRunLines(t, systemCall(t, 41, c.family, c.kind, c.protocol), "¯1 1")
		})
	}
}

func TestTCPAndUDPSocketsOpenWithTheProtocolNamedOrLeftToTheDefault(t *testing.T) {
	for _, c := range []struct {
		name                   string
		family, kind, protocol int64
	}{
		{"TCP over IPv4", 2, 1, 6},
		{"UDP over IPv4", 2, 2, 17},
		{"UDP over IPv4 by default", 2, 2, 0},
		{"TCP over IPv6", 10, 1, 6},
		{"UDP over IPv6", 10, 2, 17},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := systemCall(t, 41, c.family, c.kind, c.protocol)
			if got := lines(r.Stdout); len(got) != 1 || strings.HasPrefix(got[0], "¯") {
				t.Errorf("output lines %q, want a descriptor and errno; %s", got, describe(r))
			}
		})
	}
}

// withErrno runs code that sets result by calling the C library through
// ⎕NA, and returns the run with one output line: result and errno.
func withErrno(t *testing.T, code string) runResponse {
	t.Helper()
	return run(t, `⎕NA'P libc.so.6|__errno_location'
⎕NA'libc.so.6|memcpy >I4[1] P P'
`+code+`errno←⊃memcpy 1 __errno_location 4
⎕←result errno
`, "")
}

// socketPair calls socketpair from a run, with room for the two
// descriptors, and returns the run with one output line: the result and
// errno.
func socketPair(t *testing.T, family, kind, protocol int64) runResponse {
	t.Helper()
	return withErrno(t, fmt.Sprintf(`⎕NA'I4 libc.so.6|socketpair I4 I4 I4 >I4[2]'
result←⊃socketpair %d %d %d 2
`, family, kind, protocol))
}

func TestSocketPairsOtherThanUnixOnesFailWithEPERM(t *testing.T) {
	// socketpair creates its sockets as socket does, so the kernel loads a
	// missing family's or protocol's module before it refuses a family
	// without socket pairs. The kernel would otherwise answer EOPNOTSUPP,
	// EAFNOSUPPORT or EPROTONOSUPPORT, or succeed, never EPERM.
	for _, c := range []struct {
		name                   string
		family, kind, protocol int64
	}{
		{"AF_INET", 2, 1, 0},
		{"AF_INET with SCTP", 2, 1, 132},
		{"AF_INET6", 10, 2, 0},
		{"AF_TIPC", 30, 5, 0}, // SOCK_SEQPACKET
	} {
		t.Run(c.name, func(t *testing.T) {
			assertRunLines(t, socketPair(t, c.family, c.kind, c.protocol), "¯1 1")
		})
	}
}

func TestUnixSocketPairsOpen(t *testing.T) {
	r := socketPair(t, 1, 1, 0)

	if got := lines(r.Stdout); len(got) != 1 || !strings.HasPrefix(got[0], "0 ") {
		t.Errorf("output lines %q, want result 0 and errno; %s", got, describe(r))
	}
}

func TestSocketIoctlsThatLoadAModuleFailWithEPERM(t *testing.T) {
	// On any socket, and before any capability check, SIOCGIFVLAN makes the
	// kernel load 8021q and SIOCGIFBR loads bridge. With no argument the
	// kernel would otherwise answer EFAULT, or ENOPKG without the module,
	// never EPERM.
	for _, c := range []struct {
		name    string
		request int64
	}{
		{"SIOCGIFVLAN", 0x8982},
		{"SIOCGIFBR", 0x8940},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := withErrno(t, fmt.Sprintf(`⎕NA'I4 libc.so.6|socket I4 I4 I4'
⎕NA'I4 libc.so.6|ioctl I4 U8 P'
result←ioctl (socket 2 2 0) %d 0
`, c.request))

			assertRunLines(t, r, "¯1 1")
		})
	}
}

func TestSettingATCPUpperLayerProtocolFailsWithEPERM(t *testing.T) {
	// TCP_ULP (level SOL_TCP, option 31) attaches an upper-layer protocol
	// such as kernel TLS to a socket. The kernel loads its module only for
	// a caller with CAP_NET_ADMIN, but a run could still use one the host
	// has loaded. The kernel would otherwise answer ENOENT, or ENOTCONN on
	// a socket that is not connected, never EPERM.
	r := withErrno(t, `⎕NA'I4 libc.so.6|socket I4 I4 I4'
⎕NA'I4 libc.so.6|setsockopt I4 I4 I4 <U1[] I4'
result←setsockopt (socket 2 1 0) 6 31 (⎕UCS'tls') 3
`)

	assertRunLines(t, r, "¯1 1")
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

	assertRunLines(t, second, "0 0", "0")
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
	// Under the default 512 MiB limit a 200 MB child completes...
	fits := run(t, childAllocationCode(200_000_000), "")
	assertRunLines(t, fits, "status 0")

	// ...and with SANDBOX_MEMORY=128M it ends the run.
	url, _ := startContainer(t, "SANDBOX_MEMORY=128M")
	r := runAt(t, url, childAllocationCode(200_000_000), "")

	assertKilledForMemory(t, r)
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
