// Package config reads runner-api's limits from the environment.
package config

import (
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"time"
)

// Limits are the limits runner-api enforces, per run and per request.
type Limits struct {
	// Timeout is the wall-clock limit of one run (SANDBOX_TIMEOUT, a Go
	// duration such as "10s").
	Timeout time.Duration
	// OutputLimit is the number of bytes kept from each of stdout and stderr
	// (SANDBOX_OUTPUT_LIMIT, in bytes).
	OutputLimit int
	// Memory is the memory limit of one run in bytes, without swap
	// (SANDBOX_MEMORY: bytes, or a number with K, M or G).
	Memory int64
	// CPUs is the CPU time one run may use per second of wall-clock time
	// (SANDBOX_CPUS, for example 1 or 0.5).
	CPUs float64
	// Pids is the number of processes and threads one run may have
	// (SANDBOX_PIDS).
	Pids int
	// RequestLimit is the largest code plus input one request may carry, in
	// bytes (SANDBOX_REQUEST_LIMIT).
	RequestLimit int
	// Concurrency is the number of runs in progress at once across all
	// clients (SANDBOX_CONCURRENCY).
	Concurrency int
	// ClientConcurrency is the number of runs one client may have in
	// progress at once (SANDBOX_CLIENT_CONCURRENCY).
	ClientConcurrency int
	// ClientRate is the number of runs one client may start per minute
	// (SANDBOX_CLIENT_RATE).
	ClientRate int
	// TrustedProxies are the peers whose X-Forwarded-For header identifies
	// the client (SANDBOX_TRUSTED_PROXIES: comma-separated addresses or
	// networks). Without them, the peer address is the client.
	TrustedProxies []netip.Prefix
}

// Workspace is the interpreter's maximum workspace (MAXWS) in bytes: half
// the memory limit, so the interpreter reports WS FULL before the cgroup's
// memory limit is reached, whatever that limit is.
func (l Limits) Workspace() int64 {
	return l.Memory / 2
}

// The plan's defaults (Sandbox limits table).
const (
	defaultTimeout     = 10 * time.Second
	defaultOutputLimit = 128 * 1024
	defaultMemory      = 512 << 20
	defaultCPUs        = 1
	defaultPids        = 64
)

// FromEnv reads the limits, applying defaults for unset variables. getenv
// is os.Getenv outside tests.
func FromEnv(getenv func(string) string) (Limits, error) {
	limits := Limits{
		Timeout:     defaultTimeout,
		OutputLimit: defaultOutputLimit,
		Memory:      defaultMemory,
		CPUs:        defaultCPUs,
		Pids:        defaultPids,
	}

	if v := getenv("SANDBOX_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Limits{}, fmt.Errorf("SANDBOX_TIMEOUT=%q: want a positive duration such as 10s", v)
		}
		limits.Timeout = d
	}

	if v := getenv("SANDBOX_OUTPUT_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Limits{}, fmt.Errorf("SANDBOX_OUTPUT_LIMIT=%q: want a positive number of bytes", v)
		}
		limits.OutputLimit = n
	}

	if v := getenv("SANDBOX_MEMORY"); v != "" {
		n, err := parseSize(v)
		if err != nil || n <= 0 {
			return Limits{}, fmt.Errorf("SANDBOX_MEMORY=%q: want a positive size such as 512M", v)
		}
		limits.Memory = n
	}

	if v := getenv("SANDBOX_CPUS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !(f > 0) || math.IsInf(f, 1) {
			return Limits{}, fmt.Errorf("SANDBOX_CPUS=%q: want a positive number such as 1 or 0.5", v)
		}
		limits.CPUs = f
	}

	if v := getenv("SANDBOX_PIDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Limits{}, fmt.Errorf("SANDBOX_PIDS=%q: want a positive number of processes", v)
		}
		limits.Pids = n
	}

	return limits, nil
}

// parseSize reads a number of bytes, optionally followed by K, M or G
// (powers of 1024, either case).
func parseSize(s string) (int64, error) {
	unit := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		unit = 1 << 10
	case 'm', 'M':
		unit = 1 << 20
	case 'g', 'G':
		unit = 1 << 30
	}
	digits := s
	if unit > 1 {
		digits = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt64/unit {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n * unit, nil
}
