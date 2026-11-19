package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Nsjail runs each job in a fresh nsjail sandbox described by Config.
type Nsjail struct {
	Binary  string // nsjail executable
	Config  string // nsjail configuration; it must set log_fd: 3
	Command string // program run inside the jail; receives the job's arguments
	JobsDir string // parent of the per-run directories
	// Logf receives nsjail's log when nsjail cannot run a job.
	Logf func(format string, args ...any)
}

// nsjail logs the jailed process's fate at level INFO; without one of these
// lines the job never ran. Its own exit status cannot tell the two apart,
// because it reports a signal N as status 128+N.
var (
	exitedLine     = regexp.MustCompile(`exited with status: (\d+)`)
	terminatedLine = regexp.MustCompile(`terminated with signal: (\w+) \(\d+\)`)
)

// Run runs job inside nsjail. The code is visible in the jail, read-only,
// as /job/code; the input is the jailed program's standard input.
func (n *Nsjail) Run(ctx context.Context, job Job) (Result, error) {
	dir, err := os.MkdirTemp(n.JobsDir, "job-")
	if err != nil {
		return Result{}, fmt.Errorf("creating job directory: %w", err)
	}
	defer os.RemoveAll(dir)
	// The jailed user maps to an unprivileged uid that owns nothing here.
	if err := os.Chmod(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("opening job directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "code"), job.Code, 0o644); err != nil {
		return Result{}, fmt.Errorf("writing job code: %w", err)
	}

	logR, logW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("creating log pipe: %w", err)
	}
	defer logR.Close()

	args := append([]string{"--config", n.Config, "--bindmount_ro", dir + ":/job", "--", n.Command}, job.Args...)
	cmd := exec.CommandContext(ctx, n.Binary, args...)
	cmd.Stdin = bytes.NewReader(job.Input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.ExtraFiles = []*os.File{logW} // descriptor 3 in nsjail

	start := time.Now()
	if err := cmd.Start(); err != nil {
		logW.Close()
		return Result{}, fmt.Errorf("starting nsjail: %w", err)
	}
	logW.Close()
	logged := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(logR)
		logged <- b
	}()
	waitErr := cmd.Wait()
	wall := time.Since(start)
	nsjailLog := <-logged

	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), WallTime: wall}
	if ps := cmd.ProcessState; ps != nil {
		// nsjail reaps the jailed process, so its usage includes the program's.
		result.UserTime = ps.UserTime()
		result.SysTime = ps.SystemTime()
	}
	if m := terminatedLine.FindSubmatch(nsjailLog); m != nil {
		result.Signal = string(m[1])
		return result, nil
	}
	if m := exitedLine.FindSubmatch(nsjailLog); m != nil {
		code, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return Result{}, fmt.Errorf("parsing exit status %q: %w", m[1], err)
		}
		result.ExitCode = &code
		return result, nil
	}
	if n.Logf != nil {
		n.Logf("nsjail did not run the job (%v); its log:\n%s", waitErr, nsjailLog)
	}
	return Result{}, fmt.Errorf("nsjail did not run the job: %v", waitErr)
}
