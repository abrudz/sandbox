//go:build deploy

// Tests of a host provisioned by deploy/setup-host. They run as root on that
// host, after provisioning, with the environment it was provisioned with:
//
//	SITE_ADDRESS   Caddy's site address, a URL the tests can reach
//	SANDBOX_IMAGE  the image the instance runs, in a registry the tests can
//	               push to
//
// The tests run in file order; the update test, which replaces the running
// image, comes last.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// composeFile is where setup-host installs the instance's compose file.
const composeFile = "/opt/sandbox/compose.yaml"

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set", name)
	}
	return v
}

// command runs a command and returns its trimmed combined output, failing
// the test if it does not succeed.
func command(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func repoPath(rel string) string {
	return filepath.Join("..", "..", rel)
}

// sandboxContainer returns the ID of the instance's sandbox container.
func sandboxContainer(t *testing.T) string {
	t.Helper()
	id := command(t, "docker", "compose", "-f", composeFile, "ps", "-q", "sandbox")
	if id == "" {
		t.Fatal("the sandbox service has no container")
	}
	return id
}

func smoke(t *testing.T) {
	t.Helper()
	out, err := exec.Command(repoPath("scripts/smoke"), env(t, "SITE_ADDRESS")).CombinedOutput()
	if err != nil {
		t.Errorf("scripts/smoke %s: %v\n%s", env(t, "SITE_ADDRESS"), err, out)
	}
}

func TestSmokePassesThroughTheProxy(t *testing.T) {
	smoke(t)
}

func TestRerunningSetupHostChangesNothing(t *testing.T) {
	before := sandboxContainer(t)

	out := command(t, repoPath("deploy/setup-host"))

	if !strings.Contains(out, "No changes") {
		t.Errorf("setup-host did not report \"No changes\":\n%s", out)
	}
	if after := sandboxContainer(t); after != before {
		t.Errorf("the sandbox container changed from %s to %s", before, after)
	}
}

func TestFirewallAllowsOnlySSHHTTPAndHTTPS(t *testing.T) {
	out := command(t, "ufw", "status", "verbose")

	for _, want := range []string{"Status: active", "Default: deny (incoming)"} {
		if !strings.Contains(out, want) {
			t.Errorf("ufw status lacks %q:\n%s", want, out)
		}
	}
	var allowed []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.Contains(line, "ALLOW IN") && !slices.Contains(allowed, f[0]) {
			allowed = append(allowed, f[0])
		}
	}
	slices.Sort(allowed)
	if want := []string{"22/tcp", "443/tcp", "443/udp", "80/tcp"}; !slices.Equal(allowed, want) {
		t.Errorf("ufw allows %q, want %q:\n%s", allowed, want, out)
	}
}

func TestNothingListensOnPort8080(t *testing.T) {
	if out := command(t, "ss", "-Hltn", "sport = :8080"); out != "" {
		t.Errorf("port 8080 listens:\n%s", out)
	}
}

func TestSandboxContainerHasNoNetwork(t *testing.T) {
	id := sandboxContainer(t)

	if mode := command(t, "docker", "inspect", "-f", "{{.HostConfig.NetworkMode}}", id); mode != "none" {
		t.Errorf("network mode %q, want none", mode)
	}
	if ifaces := command(t, "docker", "exec", id, "ls", "/sys/class/net"); ifaces != "lo" {
		t.Errorf("network interfaces %q, want lo only", ifaces)
	}
}

func TestSandboxContainerCannotConnectOutOrToTheHost(t *testing.T) {
	id := sandboxContainer(t)
	// Caddy listens on port 80 of every host address.
	host := strings.Fields(command(t, "hostname", "-I"))[0]

	for _, target := range []string{"1.1.1.1/80", host + "/80"} {
		out, err := exec.Command("docker", "exec", id, "timeout", "10", "bash", "-c", "exec 3<>/dev/tcp/"+target).CombinedOutput()
		if err == nil {
			t.Errorf("root in the sandbox container connected to %s", target)
		} else if !strings.Contains(string(out), "Network is unreachable") {
			t.Errorf("connecting to %s: %v, %q; want an unreachable network", target, err, out)
		}
	}
}

// clientAt sends requests from the loopback address ip.
func clientAt(ip string) *http.Client {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
}

func TestPerClientLimitsApplyToTheAddressCaddySaw(t *testing.T) {
	url := env(t, "SITE_ADDRESS") + "/api/v1/run"
	run := func(c *http.Client, code string) (int, string) {
		body, err := json.Marshal(map[string]any{"code": code, "input": "", "args": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	first, second := clientAt("127.0.0.2"), clientAt("127.0.0.3")

	// Two runs in progress are the first client's limit.
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 2)
	for range 2 {
		go func() {
			status, body := run(first, "⎕DL 3\n")
			done <- result{status, body}
		}()
	}
	time.Sleep(time.Second)

	if status, body := run(first, "⎕←1\n"); status != http.StatusTooManyRequests {
		t.Errorf("a third run from the first client: status %d, want 429: %s", status, body)
	}
	if status, body := run(second, "⎕←1\n"); status != http.StatusOK {
		t.Errorf("a run from the second client: status %d, want 200: %s", status, body)
	}
	for range 2 {
		if r := <-done; r.status != http.StatusOK {
			t.Errorf("a run in progress from the first client: status %d, want 200: %s", r.status, r.body)
		}
	}
}

// hostConfig is the part of a container's HostConfig that grants it
// privileges.
type hostConfig struct {
	Privileged   bool
	CapAdd       []string
	CapDrop      []string
	SecurityOpt  []string
	CgroupnsMode string
	// The Docker CLI and Compose record systempaths=unconfined as empty
	// lists here, not among the security options.
	MaskedPaths   *[]string
	ReadonlyPaths *[]string
	// Memory and MemorySwap are in bytes; 0 means no limit.
	Memory     int64
	MemorySwap int64
	PidsLimit  *int64
}

func sandboxHostConfig(t *testing.T) hostConfig {
	t.Helper()
	var hc hostConfig
	out := command(t, "docker", "inspect", "-f", "{{json .HostConfig}}", sandboxContainer(t))
	if err := json.Unmarshal([]byte(out), &hc); err != nil {
		t.Fatalf("parsing the sandbox container's HostConfig: %v\n%s", err, out)
	}
	return hc
}

func TestSandboxContainerIsNotPrivilegedAndHoldsOnlySysAdminSetuidAndSetgid(t *testing.T) {
	hc := sandboxHostConfig(t)

	if hc.Privileged {
		t.Error("the sandbox container is privileged")
	}
	if !slices.Equal(hc.CapDrop, []string{"ALL"}) {
		t.Errorf("the sandbox container drops capabilities %q, want ALL", hc.CapDrop)
	}
	// Docker accepts a capability with or without its CAP_ prefix.
	var added []string
	for _, c := range hc.CapAdd {
		added = append(added, strings.TrimPrefix(c, "CAP_"))
	}
	slices.Sort(added)
	if want := []string{"SETGID", "SETUID", "SYS_ADMIN"}; !slices.Equal(added, want) {
		t.Errorf("the sandbox container adds capabilities %q, want %q", hc.CapAdd, want)
	}
	// SETGID, SETUID and SYS_ADMIN are bits 6, 7 and 21.
	const want = "00000000002000c0"
	out := command(t, "docker", "exec", sandboxContainer(t), "grep", "-E", "^Cap(Eff|Bnd):", "/proc/1/status")
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) != 2 || f[1] != want {
			t.Errorf("%q, want %s", line, want)
		}
	}
}

func TestSandboxContainerHasAPrivateCgroupNamespace(t *testing.T) {
	if mode := sandboxHostConfig(t).CgroupnsMode; mode != "private" {
		t.Errorf("the sandbox container's cgroup namespace mode is %q, want private", mode)
	}
}

func TestSandboxContainerUnconfinesAppArmorAndMaskedPathsAndUsesTheSeccompProfile(t *testing.T) {
	hc := sandboxHostConfig(t)

	if !slices.Contains(hc.SecurityOpt, "apparmor=unconfined") {
		t.Errorf("the sandbox container's security options %q lack apparmor=unconfined", hc.SecurityOpt)
	}
	for name, paths := range map[string]*[]string{"masked": hc.MaskedPaths, "read-only": hc.ReadonlyPaths} {
		switch {
		case paths == nil:
			t.Errorf("the sandbox container has no %s paths list, so systempaths=unconfined is not set", name)
		case len(*paths) > 0:
			t.Errorf("the sandbox container has %s paths %q, so systempaths=unconfined is not set", name, *paths)
		}
	}
	// Docker keeps a seccomp profile in the container's configuration as
	// seccomp= followed by the profile itself.
	var want, got any
	if err := json.Unmarshal(readFile(t, repoPath("deploy/seccomp.json")), &want); err != nil {
		t.Fatalf("parsing deploy/seccomp.json: %v", err)
	}
	i := slices.IndexFunc(hc.SecurityOpt, func(o string) bool { return strings.HasPrefix(o, "seccomp=") })
	if i < 0 {
		t.Fatalf("the sandbox container's security options %q have no seccomp profile", hc.SecurityOpt)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(hc.SecurityOpt[i], "seccomp=")), &got); err != nil {
		t.Fatalf("parsing the sandbox container's seccomp profile: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("the sandbox container's seccomp profile differs from deploy/seccomp.json")
	}
}

func TestSetupHostInstallsTheSeccompProfile(t *testing.T) {
	installed := readFile(t, "/opt/sandbox/seccomp.json")

	if !bytes.Equal(installed, readFile(t, repoPath("deploy/seccomp.json"))) {
		t.Error("/opt/sandbox/seccomp.json differs from deploy/seccomp.json")
	}
}

// bodyLimit is runner-api's largest valid request body with its default
// limits: code plus input of 64 KiB and 16 arguments of 1 KiB, every byte
// JSON-escaped as six, plus 4 KiB of framing.
const bodyLimit = 6*(64*1024+16*1024) + 4096

func TestCaddyRefusesABodyOverRunnerAPIsLimitBeforeReadingIt(t *testing.T) {
	// The body never arrives, so only a refusal based on Content-Length
	// can answer quickly; runner-api would wait for it.
	body, w := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A request that fails waits for its body to be written, so the body
	// ends when the request is given up.
	context.AfterFunc(ctx, func() { w.Close() })
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env(t, "SITE_ADDRESS")+"/api/v1/run", body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = bodyLimit + 1
	req.Header.Set("Content-Type", "application/json")

	begun := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(begun)

	if err != nil {
		t.Fatalf("no response within 5 s: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", resp.StatusCode)
	}
	if elapsed > 2*time.Second {
		t.Errorf("answered after %v; a refusal before reading the body is immediate", elapsed)
	}
}

func TestCaddyPassesABodyAtRunnerAPIsLimit(t *testing.T) {
	run := `{"code":"⎕←1","input":"","args":[]}`
	body := run + strings.Repeat(" ", bodyLimit-len(run))

	resp, err := http.Post(env(t, "SITE_ADDRESS")+"/api/v1/run", "application/json", strings.NewReader(body))

	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a %d-byte body: status %d, want 200", len(body), resp.StatusCode)
	}
}

// memTotal is the host's memory in bytes.
func memTotal(t *testing.T) int64 {
	t.Helper()
	for _, line := range strings.Split(string(readFile(t, "/proc/meminfo")), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb * 1024
		}
	}
	t.Fatal("/proc/meminfo has no MemTotal")
	return 0
}

// hostReserve is the memory kept for the operating system, Docker and Caddy.
const hostReserve = 512 << 20

func TestSandboxContainerMemoryLimitLeavesAReserveForTheHost(t *testing.T) {
	hc := sandboxHostConfig(t)
	total := memTotal(t)

	if hc.Memory <= 0 {
		t.Fatal("the sandbox container has no memory limit")
	}
	if hc.Memory > total-hostReserve {
		t.Errorf("memory limit %d bytes of %d leaves less than %d for the host", hc.Memory, total, hostReserve)
	}
	if hc.MemorySwap != hc.Memory {
		t.Errorf("memory plus swap %d bytes, want %d: no swap", hc.MemorySwap, hc.Memory)
	}
}

// runnerReserve is the container memory kept for runner-api itself and the
// request bodies it is reading.
const runnerReserve = 128 << 20

func TestConcurrentRunsFitWithinTheSandboxContainersLimits(t *testing.T) {
	hc := sandboxHostConfig(t)
	// runner-api logs its limits when it starts.
	logs := command(t, "docker", "logs", sandboxContainer(t))
	number := func(pattern string) int64 {
		t.Helper()
		m := regexp.MustCompile(pattern).FindAllStringSubmatch(logs, -1)
		if m == nil {
			t.Fatalf("runner-api's log has no match for %q:\n%s", pattern, logs)
		}
		n, err := strconv.ParseInt(m[len(m)-1][1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	runs := number(`(\d+) runs at once`)
	memory := number(`memory (\d+) bytes`)
	pids := number(`(\d+) processes;`)

	if need := runs*memory + runnerReserve; hc.Memory < need {
		t.Errorf("%d runs of %d bytes need a memory limit of at least %d bytes; it is %d", runs, memory, need, hc.Memory)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit < runs*pids+64 {
		t.Errorf("%d runs of %d processes need a process limit of at least %d; it is %v", runs, pids, runs*pids+64, hc.PidsLimit)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

func TestNoCrashStartsAHelperOnTheHost(t *testing.T) {
	// The configuration the host applies at boot, before any service.
	command(t, "sysctl", "--system")

	if pattern := command(t, "sysctl", "-n", "kernel.core_pattern"); strings.HasPrefix(pattern, "|") {
		t.Errorf("kernel.core_pattern is %q, which pipes every crash to a helper on the host", pattern)
	}
}

func TestApportCannotStart(t *testing.T) {
	// apport sets kernel.core_pattern to itself when its service starts.
	if status, _ := exec.Command("dpkg-query", "-W", "-f=${Status}", "apport").Output(); string(status) != "install ok installed" {
		return
	}
	if got, _ := exec.Command("systemctl", "is-enabled", "apport.service").Output(); strings.TrimSpace(string(got)) != "masked" {
		t.Errorf("apport.service is %q, want masked", strings.TrimSpace(string(got)))
	}
	if got, _ := exec.Command("systemctl", "is-active", "apport.service").Output(); strings.TrimSpace(string(got)) == "active" {
		t.Error("apport.service is active")
	}
}

// unusedSocketModules are socket protocol and family modules, and 8021q,
// which a socket ioctl loads: the kernel loads each when a process asks for
// it, with no capability check, and the host has no use for any of them.
var unusedSocketModules = []string{
	"sctp", "dccp", "dccp_ipv4", "dccp_ipv6", "l2tp_ip", "l2tp_ip6", "smc",
	"af_key", "appletalk", "atm", "ax25", "bluetooth", "caif_socket", "can",
	"ieee802154_socket", "kcm", "llc2", "netrom", "nfc", "phonet", "qrtr",
	"rds", "rose", "rxrpc", "tipc", "x25",
	"8021q",
}

// modprobePlan returns what modprobe would do to load module, without
// loading it.
func modprobePlan(module string) (string, error) {
	out, err := exec.Command("modprobe", "--dry-run", "--verbose", module).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestModprobeRefusesTheSocketModulesTheHostDoesNotUse(t *testing.T) {
	for _, m := range unusedSocketModules {
		if plan, _ := modprobePlan(m); !strings.Contains(plan, "install /bin/false") {
			t.Errorf("modprobe would load %s: %q", m, plan)
		}
	}
}

func TestModprobeStillLoadsTheModulesDockerUses(t *testing.T) {
	for _, m := range []string{"bridge", "veth", "overlay", "br_netfilter", "nf_nat"} {
		if plan, err := modprobePlan(m); err != nil || strings.Contains(plan, "/bin/false") {
			t.Errorf("modprobe would not load %s: %q, %v", m, plan, err)
		}
	}
}

func TestUnattendedUpgradesInstallUpdatesAndRebootWhenNeeded(t *testing.T) {
	if status := command(t, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"); status != "install ok installed" {
		t.Errorf("unattended-upgrades package status %q", status)
	}
	out := command(t, "apt-config", "dump")

	for _, want := range []string{
		`APT::Periodic::Update-Package-Lists "1";`,
		`APT::Periodic::Unattended-Upgrade "1";`,
		`Unattended-Upgrade::Automatic-Reboot "true";`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("apt configuration lacks %s", want)
		}
	}
}

func TestUpdateTimerIsEnabledAndActive(t *testing.T) {
	if got := command(t, "systemctl", "is-enabled", "sandbox-update.timer"); got != "enabled" {
		t.Errorf("sandbox-update.timer is %q, want enabled", got)
	}
	if got := command(t, "systemctl", "is-active", "sandbox-update.timer"); got != "active" {
		t.Errorf("sandbox-update.timer is %q, want active", got)
	}
}

func TestServicesStartAtBoot(t *testing.T) {
	if got := command(t, "systemctl", "is-enabled", "docker.service"); got != "enabled" {
		t.Errorf("docker.service is %q, want enabled", got)
	}
	ids := strings.Fields(command(t, "docker", "compose", "-f", composeFile, "ps", "-q"))
	if len(ids) != 2 {
		t.Fatalf("the instance runs %d containers, want 2 (sandbox and caddy)", len(ids))
	}
	for _, id := range ids {
		if got := command(t, "docker", "inspect", "-f", "{{.Name}} {{.HostConfig.RestartPolicy.Name}}", id); !strings.HasSuffix(got, " unless-stopped") {
			t.Errorf("container %s, want restart policy unless-stopped", got)
		}
	}
}

func TestUpdateBringsInANewlyPublishedImageWithoutFailingARunInProgress(t *testing.T) {
	image := env(t, "SANDBOX_IMAGE")
	oldID := command(t, "docker", "inspect", "-f", "{{.Image}}", sandboxContainer(t))
	// Publish a new image to the instance's tag, then point the local tag
	// back at the running image, so only a pull can bring the new one in.
	// BuildKit resolves FROM by name, so the running image gets a local tag.
	command(t, "docker", "tag", oldID, "sandbox-current")
	build := exec.Command("docker", "build", "--label", fmt.Sprintf("test.update=%d", time.Now().UnixNano()), "-t", "sandbox-next", "-")
	build.Stdin = strings.NewReader("FROM sandbox-current\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the new image: %v\n%s", err, out)
	}
	newID := command(t, "docker", "image", "inspect", "-f", "{{.Id}}", "sandbox-next")
	command(t, "docker", "tag", "sandbox-next", image)
	command(t, "docker", "push", image)
	command(t, "docker", "tag", oldID, image)

	type result struct {
		status int
		body   map[string]any
		err    error
		at     time.Time
	}
	done := make(chan result, 1)
	url := env(t, "SITE_ADDRESS") + "/api/v1/run"
	started := time.Now()
	go func() {
		body := `{"code":"⎕DL 8\n⎕←'finished'\n","input":"","args":[]}`
		resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			done <- result{err: err, at: time.Now()}
			return
		}
		defer resp.Body.Close()
		var m map[string]any
		err = json.NewDecoder(resp.Body).Decode(&m)
		done <- result{status: resp.StatusCode, body: m, err: err, at: time.Now()}
	}()
	time.Sleep(time.Second)

	command(t, "systemctl", "start", "sandbox-update.service")
	updated := time.Now()

	r := <-done
	if r.err != nil || r.status != http.StatusOK {
		t.Fatalf("the run in progress failed: status %d, error %v, body %v", r.status, r.err, r.body)
	}
	if r.body["stdout"] != "finished\r" || r.body["exitCode"] != float64(0) || r.body["timedOut"] != false {
		t.Errorf("the run in progress did not finish normally: %v", r.body)
	}
	// Replacing the container waits for its runs in progress, so the update
	// cannot finish before the run does.
	if r.at.Sub(started) < 8*time.Second || updated.Before(r.at) {
		t.Errorf("the update finished %v after the run started, the run %v after; want the update to wait for the run",
			updated.Sub(started), r.at.Sub(started))
	}
	if got := command(t, "docker", "inspect", "-f", "{{.Image}}", sandboxContainer(t)); got != newID {
		t.Errorf("the sandbox runs image %s, want the newly published %s", got, newID)
	}
	smoke(t)
}
