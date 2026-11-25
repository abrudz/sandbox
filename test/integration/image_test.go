//go:build integration

package integration

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowedCapabilities are the capabilities a sandbox container may hold,
// by number: Docker's defaults, and SYS_ADMIN, which nsjail needs.
var allowedCapabilities = map[string]uint{
	"CHOWN": 0, "DAC_OVERRIDE": 1, "FOWNER": 3, "FSETID": 4, "KILL": 5,
	"SETGID": 6, "SETUID": 7, "SETPCAP": 8, "NET_BIND_SERVICE": 10,
	"NET_RAW": 13, "SYS_CHROOT": 18, "SYS_ADMIN": 21, "MKNOD": 27,
	"AUDIT_WRITE": 29, "SETFCAP": 31,
}

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

func TestImageRunsAJailedProcess(t *testing.T) {
	image := env(t, "SANDBOX_IMAGE")

	args := append([]string{"run", "--rm"}, runFlags(t)...)
	args = append(args, "--entrypoint", "nsjail", image,
		"--mode", "o", "--chroot", "/", "--user", "99999", "--group", "99999", "--quiet",
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

func TestContainerHoldsNoCapabilityBeyondDockersDefaultsAndSysAdmin(t *testing.T) {
	name := env(t, "SANDBOX_CONTAINER")

	out, err := docker(t, "exec", name, "grep", "CapBnd", "/proc/1/status")
	if err != nil {
		t.Fatalf("reading the bounding set in %s: %v\n%s", name, err, out)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		t.Fatalf("unexpected CapBnd line %q", strings.TrimSpace(out))
	}
	bounding, err := strconv.ParseUint(fields[1], 16, 64)
	if err != nil {
		t.Fatalf("parsing CapBnd %q: %v", fields[1], err)
	}
	var allowed uint64
	for _, bit := range allowedCapabilities {
		allowed |= 1 << bit
	}
	var extra []int
	for bit := range 64 {
		if bounding&^allowed&(1<<bit) != 0 {
			extra = append(extra, bit)
		}
	}
	if len(extra) > 0 {
		t.Errorf("the bounding set %s holds capabilities %v beyond Docker's defaults and SYS_ADMIN", fields[1], extra)
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
