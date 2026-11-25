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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

func TestSandboxPortListensOnLoopbackOnly(t *testing.T) {
	out := command(t, "ss", "-Hltn", "sport = :8080")

	lines := strings.Split(out, "\n")
	if out == "" {
		t.Fatal("nothing listens on port 8080")
	}
	for _, line := range lines {
		if f := strings.Fields(line); len(f) < 4 || !strings.HasPrefix(f[3], "127.0.0.1:") {
			t.Errorf("port 8080 listens beyond loopback: %s", line)
		}
	}
}

// hostConfig is the part of a container's HostConfig that grants it
// privileges.
type hostConfig struct {
	Privileged   bool
	CapAdd       []string
	SecurityOpt  []string
	CgroupnsMode string
	// The Docker CLI and Compose record systempaths=unconfined as empty
	// lists here, not among the security options.
	MaskedPaths   *[]string
	ReadonlyPaths *[]string
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

func TestSandboxContainerIsNotPrivilegedAndAddsOnlySysAdmin(t *testing.T) {
	hc := sandboxHostConfig(t)

	if hc.Privileged {
		t.Error("the sandbox container is privileged")
	}
	// Docker accepts a capability with or without its CAP_ prefix.
	if len(hc.CapAdd) != 1 || strings.TrimPrefix(hc.CapAdd[0], "CAP_") != "SYS_ADMIN" {
		t.Errorf("the sandbox container adds capabilities %q, want SYS_ADMIN alone", hc.CapAdd)
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

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
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
