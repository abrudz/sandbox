//go:build integration

package integration

import (
	"slices"
	"strings"
	"testing"
)

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

	out, err := docker(t, "run", "--rm", "--privileged", "--entrypoint", "nsjail", image,
		"--mode", "o", "--chroot", "/", "--user", "99999", "--group", "99999", "--quiet",
		"--", "/bin/echo", "jailed")
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
