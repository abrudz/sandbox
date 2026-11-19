// Package sandbox runs submitted programs in isolation.
package sandbox

import (
	"context"
	"time"
)

// Job is one program to run.
type Job struct {
	Code  []byte   // program text
	Input []byte   // standard input
	Args  []string // command-line arguments passed to the program
}

// Result is the outcome of a Job.
type Result struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is nil when a signal ended the program.
	ExitCode *int
	// Signal names the signal that ended the program; empty otherwise.
	Signal          string
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
	WallTime        time.Duration
	UserTime        time.Duration
	SysTime         time.Duration
}

// Executor runs jobs. An error means the sandbox could not run the job; a
// program that fails is a successful run with a non-zero exit code.
type Executor interface {
	Run(ctx context.Context, job Job) (Result, error)
}
