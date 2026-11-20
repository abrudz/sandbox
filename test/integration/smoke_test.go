//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSmokePassesAgainstTheService(t *testing.T) {
	out, err := exec.Command(filepath.Join("..", "..", "scripts", "smoke"), env(t, "SANDBOX_URL")).CombinedOutput()

	if err != nil {
		t.Errorf("scripts/smoke %s: %v\n%s", env(t, "SANDBOX_URL"), err, out)
	}
}
