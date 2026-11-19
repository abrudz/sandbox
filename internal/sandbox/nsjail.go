package sandbox

import (
	"context"
	"errors"
)

// Nsjail runs each job in a fresh nsjail sandbox.
type Nsjail struct{}

// Run runs job inside nsjail.
func (Nsjail) Run(context.Context, Job) (Result, error) {
	return Result{}, errors.New("not implemented")
}
