//go:build integration

// Package integration holds black-box tests that run against a built sandbox
// image and a running container started from it. CI provides:
//
//	SANDBOX_URL        base URL of the running container's API
//	SANDBOX_IMAGE      reference of the image under test
//	SANDBOX_CONTAINER  name of the running container
package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// env returns the named environment variable and fails the test when it is
// unset, so a misconfigured run fails instead of passing vacuously.
func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; integration tests run against a built image and a running container (see .github/workflows/ci.yml)", name)
	}
	return v
}

// docker runs the docker CLI and returns its combined output.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}
