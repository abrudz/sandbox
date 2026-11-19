//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// imageLabels returns the labels of the running container's image.
func imageLabels(t *testing.T) map[string]string {
	t.Helper()
	out, err := docker(t, "inspect", "--format", "{{json .Config.Labels}}", env(t, "SANDBOX_CONTAINER"))
	if err != nil {
		t.Fatalf("docker inspect: %v\n%s", err, out)
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		t.Fatalf("docker inspect: labels are not JSON: %v\n%s", err, out)
	}
	return labels
}

func TestVersionReportsTheImageLabels(t *testing.T) {
	var v struct {
		DyalogVersion string `json:"dyalogVersion"`
		ImageDigest   string `json:"imageDigest"`
		Commit        string `json:"commit"`
	}
	data := request(t, http.MethodGet, "/api/v1/version", nil)
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("GET /api/v1/version: response is not JSON: %v\n%s", err, data)
	}
	labels := imageLabels(t)

	for _, c := range []struct{ label, api, pattern string }{
		{"org.opencontainers.image.version", v.DyalogVersion, `^\d+(\.\d+)+$`},
		{"org.opencontainers.image.base.digest", v.ImageDigest, `^sha256:[0-9a-f]{64}$`},
		{"org.opencontainers.image.revision", v.Commit, `^[0-9a-f]{40}$`},
	} {
		if labels[c.label] != c.api {
			t.Errorf("label %s is %q; /api/v1/version reports %q", c.label, labels[c.label], c.api)
		}
		if !regexp.MustCompile(c.pattern).MatchString(c.api) {
			t.Errorf("/api/v1/version value %q for %s does not match %s", c.api, c.label, c.pattern)
		}
	}
	if got, want := labels["org.opencontainers.image.base.name"], "docker.io/dyalog/dyalog:latest"; got != want {
		t.Errorf("label org.opencontainers.image.base.name is %q, want %q", got, want)
	}
}

func TestImageIsBuiltFromTheCommitUnderTest(t *testing.T) {
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}

	if got, want := imageLabels(t)["org.opencontainers.image.revision"], strings.TrimSpace(string(head)); got != want {
		t.Errorf("label org.opencontainers.image.revision is %q, want the checked-out commit %q", got, want)
	}
}
