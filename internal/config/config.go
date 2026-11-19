// Package config reads runner-api's per-run limits from the environment.
package config

import (
	"fmt"
	"strconv"
	"time"
)

// Limits are the per-run limits runner-api enforces.
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
}

// Workspace is the interpreter's maximum workspace (MAXWS) in bytes.
func (l Limits) Workspace() int64 {
	return 0
}

// The plan's defaults (Sandbox limits table).
const (
	defaultTimeout     = 10 * time.Second
	defaultOutputLimit = 128 * 1024
)

// FromEnv reads the limits, applying defaults for unset variables. getenv
// is os.Getenv outside tests.
func FromEnv(getenv func(string) string) (Limits, error) {
	limits := Limits{Timeout: defaultTimeout, OutputLimit: defaultOutputLimit}

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

	return limits, nil
}
