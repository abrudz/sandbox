// Package config reads runner-api's per-run limits from the environment.
package config

import (
	"errors"
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
}

// FromEnv reads the limits, applying defaults for unset variables. getenv
// is os.Getenv outside tests.
func FromEnv(getenv func(string) string) (Limits, error) {
	return Limits{}, errors.New("not implemented")
}
