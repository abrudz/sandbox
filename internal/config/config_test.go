package config_test

import (
	"net/netip"
	"reflect"
	"runtime"
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

	want := config.Limits{
		Timeout:           10 * time.Second,
		OutputLimit:       128 * 1024,
		Memory:            512 << 20,
		CPUs:              1,
		Pids:              64,
		RequestLimit:      64 * 1024,
		Concurrency:       runtime.NumCPU(),
		ClientConcurrency: 2,
		ClientRate:        30,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("limits %+v, want %+v", got, want)
	}
}

func TestSandboxRequestLimitSetsTheLargestRequest(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_REQUEST_LIMIT": "1024"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.RequestLimit != 1024 {
		t.Fatalf("request limit %d, want 1024", got.RequestLimit)
	}
}

func TestSandboxConcurrencySetsTheNumberOfSlots(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_CONCURRENCY": "3"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.Concurrency != 3 {
		t.Fatalf("concurrency %d, want 3", got.Concurrency)
	}
}

func TestSandboxClientConcurrencySetsThePerClientLimit(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_CLIENT_CONCURRENCY": "1"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.ClientConcurrency != 1 {
		t.Fatalf("client concurrency %d, want 1", got.ClientConcurrency)
	}
}

func TestSandboxClientRateSetsThePerMinuteLimit(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_CLIENT_RATE": "10"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.ClientRate != 10 {
		t.Fatalf("client rate %d, want 10", got.ClientRate)
	}
}

func TestSandboxTrustedProxiesListsAddressesAndNetworks(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_TRUSTED_PROXIES": "172.18.0.0/16, 10.1.2.3"}))
	if err != nil {
		t.Fatal(err)
	}

	want := []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("10.1.2.3/32")}
	if !reflect.DeepEqual(got.TrustedProxies, want) {
		t.Fatalf("trusted proxies %v, want %v", got.TrustedProxies, want)
	}
}

func TestSandboxMemorySetsTheMemoryLimit(t *testing.T) {
	for value, want := range map[string]int64{
		"1048576": 1 << 20,
		"64m":     64 << 20,
		"256M":    256 << 20,
		"2G":      2 << 30,
		"512K":    512 << 10,
	} {
		got, err := config.FromEnv(environment(map[string]string{"SANDBOX_MEMORY": value}))
		if err != nil {
			t.Errorf("SANDBOX_MEMORY=%q: %v", value, err)
			continue
		}
		if got.Memory != want {
			t.Errorf("SANDBOX_MEMORY=%q: memory %d, want %d", value, got.Memory, want)
		}
	}
}

func TestSandboxCpusSetsTheCPULimit(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_CPUS": "0.5"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.CPUs != 0.5 {
		t.Fatalf("CPUs %v, want 0.5", got.CPUs)
	}
}

func TestSandboxPidsSetsTheProcessLimit(t *testing.T) {
	got, err := config.FromEnv(environment(map[string]string{"SANDBOX_PIDS": "20"}))
	if err != nil {
		t.Fatal(err)
	}

	if got.Pids != 20 {
		t.Fatalf("pids %d, want 20", got.Pids)
	}
}

func TestWorkspaceIsHalfTheMemoryLimit(t *testing.T) {
	for memory, want := range map[string]int64{"": 256 << 20, "128M": 64 << 20} {
		got, err := config.FromEnv(environment(map[string]string{"SANDBOX_MEMORY": memory}))
		if err != nil {
			t.Fatal(err)
		}
		if ws := got.Workspace(); ws != want {
			t.Errorf("SANDBOX_MEMORY=%q: workspace %d, want %d", memory, ws, want)
		}
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
		{"SANDBOX_MEMORY", "lots"},
		{"SANDBOX_MEMORY", "0"},
		{"SANDBOX_MEMORY", "-1M"},
		{"SANDBOX_MEMORY", "12X"},
		{"SANDBOX_CPUS", "fast"},
		{"SANDBOX_CPUS", "0"},
		{"SANDBOX_CPUS", "-1"},
		{"SANDBOX_PIDS", "many"},
		{"SANDBOX_PIDS", "0"},
		{"SANDBOX_PIDS", "-3"},
		{"SANDBOX_REQUEST_LIMIT", "big"},
		{"SANDBOX_REQUEST_LIMIT", "0"},
		{"SANDBOX_CONCURRENCY", "lots"},
		{"SANDBOX_CONCURRENCY", "0"},
		{"SANDBOX_CLIENT_CONCURRENCY", "-1"},
		{"SANDBOX_CLIENT_CONCURRENCY", "0"},
		{"SANDBOX_CLIENT_RATE", "often"},
		{"SANDBOX_CLIENT_RATE", "0"},
		{"SANDBOX_TRUSTED_PROXIES", "not-an-address"},
		{"SANDBOX_TRUSTED_PROXIES", "10.0.0.0/33"},
	} {
		_, err := config.FromEnv(environment(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want an error naming %s", tc.name, tc.value, err, tc.name)
		}
	}
}
