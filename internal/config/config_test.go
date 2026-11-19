package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/abrudz/sandbox/internal/config"
)

// environment returns a getenv function over vars.
func environment(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestDefaultsApplyWhenNoVariableIsSet(t *testing.T) {
	got, err := config.FromEnv(environment(nil))
	if err != nil {
		t.Fatal(err)
	}

	want := config.Limits{Timeout: 10 * time.Second, OutputLimit: 128 * 1024}
	if got != want {
		t.Fatalf("limits %+v, want %+v", got, want)
	}
}

func TestSandboxTimeoutSetsTheWallClockLimit(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_TIMEOUT": "2s"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.Timeout != 2*time.Second {
		t.Fatalf("timeout %v, want 2s", got.Timeout)
	}
}

func TestSandboxOutputLimitSetsThePerStreamCap(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_OUTPUT_LIMIT": "1024"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.OutputLimit != 1024 {
		t.Fatalf("output limit %d, want 1024", got.OutputLimit)
	}
}

func TestInvalidLimitsAreRejectedNamingTheVariable(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"SANDBOX_TIMEOUT", "soon"},
		{"SANDBOX_TIMEOUT", "0s"},
		{"SANDBOX_TIMEOUT", "-1s"},
		{"SANDBOX_OUTPUT_LIMIT", "lots"},
		{"SANDBOX_OUTPUT_LIMIT", "0"},
		{"SANDBOX_OUTPUT_LIMIT", "-5"},
	} {
		_, err := config.FromEnv(environment(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want an error naming %s", tc.name, tc.value, err, tc.name)
		}
	}
}
