//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// containerCapabilities are the only capabilities a sandbox container
// holds, by number: SYS_ADMIN, with which the entry point remounts the
// cgroup filesystem and Docker's seccomp profile allows nsjail's namespace,
// mount and pivot_root calls, and SETUID and SETGID, with which nsjail maps
// the jail's uid and gid, which are not its own.
var containerCapabilities = map[string]uint{"SETGID": 6, "SETUID": 7, "SYS_ADMIN": 21}

// dockerDevices are the entries Docker puts in an unprivileged container's
// /dev.
var dockerDevices = []string{
	"console", "core", "fd", "full", "mqueue", "null", "ptmx", "pts",
	"random", "shm", "stderr", "stdin", "stdout", "tty", "urandom", "zero",
}

func TestImageKeepsDyalogLicence(t *testing.T) {
	image := env(t, "SANDBOX_IMAGE")

	out, err := docker(t, "run", "--rm", "--entrypoint", "cat", image, "/LICENSE")
	if err != nil {
		t.Fatalf("reading /LICENSE from %s: %v\n%s", image, err, out)
	}
	if !strings.Contains(out, "Dyalog") {
		t.Fatalf("/LICENSE does not mention Dyalog; first 500 bytes:\n%.500s", out)
	}
}

func TestImageShipsThirdPartyNotices(t *testing.T) {
	image := env(t, "SANDBOX_IMAGE")

	out, err := docker(t, "run", "--rm", "--entrypoint", "cat", image, "/THIRD_PARTY_NOTICES")
	if err != nil {
		t.Fatalf("reading /THIRD_PARTY_NOTICES from %s: %v\n%s", image, err, out)
	}
	// Each component compiled into the image, and a phrase from its licence text.
	for _, want := range []string{"nsjail", "Kafel", "Apache License", "The Go Authors"} {
		if !strings.Contains(out, want) {
			t.Errorf("/THIRD_PARTY_NOTICES does not contain %q", want)
		}
	}
}

// jailConfig is the smallest nsjail configuration that jails a process as
// runs are jailed: as uid and gid 99999, inside the jail and outside it.
// nsjail's --uid_mapping and --gid_mapping options need newuidmap, which
// the image does not have, and --user alone maps the jailed uid onto root
// outside, which needs CAP_SETFCAP.
const jailConfig = `mode: ONCE
uidmap { inside_id: "99999" outside_id: "99999" }
gidmap { inside_id: "99999" outside_id: "99999" }
`

func TestImageRunsAJailedProcess(t *testing.T) {
	image := env(t, "SANDBOX_IMAGE")
	config := filepath.Join(t.TempDir(), "jail.cfg")
	if err := os.WriteFile(config, []byte(jailConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"run", "--rm", "-v", config + ":/tmp/jail.cfg:ro"}, runFlags(t)...)
	args = append(args, "--entrypoint", "nsjail", image,
		"--config", "/tmp/jail.cfg", "--chroot", "/", "--quiet",
		"--", "/bin/echo", "jailed")
	out, err := docker(t, args...)
	if err != nil {
		t.Fatalf("running nsjail in %s: %v\n%s", image, err, out)
	}
	if !strings.Contains(out, "jailed") {
		t.Fatalf("jailed process output missing; got:\n%s", out)
	}
}

func TestContainerDelegatesCgroupControllers(t *testing.T) {
	name := env(t, "SANDBOX_CONTAINER")

	out, err := docker(t, "exec", name, "cat", "/sys/fs/cgroup/cgroup.subtree_control")
	if err != nil {
		t.Fatalf("reading cgroup.subtree_control in %s: %v\n%s", name, err, out)
	}
	enabled := strings.Fields(out)
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !slices.Contains(enabled, controller) {
			t.Errorf("controller %s is not enabled for child cgroups (cgroup.subtree_control: %q)", controller, strings.TrimSpace(out))
		}
	}
}

func TestContainerHoldsOnlySysAdminSetuidAndSetgid(t *testing.T) {
	name := env(t, "SANDBOX_CONTAINER")
	var want uint64
	for _, bit := range containerCapabilities {
		want |= 1 << bit
	}

	out, err := docker(t, "exec", name, "grep", "-E", "^Cap(Eff|Bnd):", "/proc/1/status")
	if err != nil {
		t.Fatalf("reading the capability sets in %s: %v\n%s", name, err, out)
	}
	sets := map[string]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("unexpected line %q", line)
		}
		n, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		sets[strings.TrimSuffix(fields[0], ":")] = n
	}
	for _, set := range []string{"CapEff", "CapBnd"} {
		if got, ok := sets[set]; !ok || got != want {
			t.Errorf("%s %016x, want %016x: SYS_ADMIN, SETUID and SETGID alone", set, got, want)
		}
	}
}

func TestRootInTheContainerCanWriteOnlyToTmp(t *testing.T) {
	// Root there holds no CAP_DAC_OVERRIDE, but owns these directories.
	name := env(t, "SANDBOX_CONTAINER")
	for _, path := range []string{"/marker", "/etc/marker", "/usr/local/bin/marker", "/var/marker"} {
		out, err := docker(t, "exec", name, "sh", "-c", "touch "+path+" && rm "+path)
		if err == nil || !strings.Contains(out, "Read-only file system") {
			t.Errorf("creating %s: %v, %q; want a read-only file system", path, err, out)
		}
	}

	out, err := docker(t, "exec", name, "sh", "-c", "touch /tmp/marker && rm /tmp/marker && stat -f -c %T /tmp")
	if err != nil || strings.TrimSpace(out) != "tmpfs" {
		t.Errorf("writing to /tmp and reading its file system type: %v, %q; want tmpfs", err, out)
	}
}

func TestContainerHasOnlyDockersDefaultDevices(t *testing.T) {
	name := env(t, "SANDBOX_CONTAINER")

	out, err := docker(t, "exec", name, "ls", "-A", "/dev")
	if err != nil {
		t.Fatalf("listing /dev in %s: %v\n%s", name, err, out)
	}
	var extra []string
	for _, entry := range strings.Fields(out) {
		if !slices.Contains(dockerDevices, entry) {
			extra = append(extra, entry)
		}
	}
	if len(extra) > 0 {
		t.Errorf("/dev holds %q beyond Docker's default entries", extra)
	}
}

func TestTheContainerRefusesToStartInTheHostsCgroupNamespace(t *testing.T) {
	// The entry point would create this leaf in whatever cgroup is the root
	// of its namespace; in the host's namespace, that is the host's root.
	const hostLeaf = "/sys/fs/cgroup/init"
	if _, err := os.Stat(hostLeaf); err == nil {
		t.Fatalf("%s exists before the container starts", hostLeaf)
	}
	name := fmt.Sprintf("sandbox-test-%d", time.Now().UnixNano())
	args := append([]string{"run", "-d"}, runFlags(t)...)
	args = append(args, "--cgroupns", "host", "--name", name, env(t, "SANDBOX_IMAGE"))
	if out, err := docker(t, args...); err != nil {
		t.Fatalf("starting a container in the host's cgroup namespace: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = docker(t, "rm", "-f", name) })

	var state string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		out, err := docker(t, "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", name)
		if err != nil {
			t.Fatalf("inspecting %s: %v\n%s", name, err, out)
		}
		if state = strings.TrimSpace(out); strings.HasPrefix(state, "exited ") {
			break
		}
	}
	logs, _ := docker(t, "logs", name)

	if !strings.HasPrefix(state, "exited ") || state == "exited 0" {
		t.Errorf("15 s after starting, the container is %q; want it to have exited with an error. Its log:\n%s", state, logs)
	}
	if !strings.Contains(logs, "cgroup namespace") {
		t.Errorf("the container's log does not name the cgroup namespace as the cause:\n%s", logs)
	}
	if _, err := os.Stat(hostLeaf); err == nil {
		t.Errorf("the entry point created %s in the host's cgroup hierarchy", hostLeaf)
	}
}
