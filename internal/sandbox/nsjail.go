package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Nsjail runs each job in a fresh nsjail sandbox described by Config.
type Nsjail struct {
	Binary  string // nsjail executable
	Config  string // nsjail configuration; it must set log_fd: 3
	Command string // program run inside the jail; receives the job's arguments
	JobsDir string // parent of the per-run directories
	// Timeout is the wall-clock limit of one run.
	Timeout time.Duration
	// OutputLimit is the number of bytes kept from each of stdout and stderr.
	OutputLimit int
	// Memory is the run's cgroup memory limit in bytes; swap is disabled.
	Memory int64
	// CPUs is the CPU time the run may use per second of wall-clock time.
	CPUs float64
	// Pids is the run's cgroup limit on processes and threads.
	Pids int
	// Workspace is the interpreter's MAXWS in bytes.
	Workspace int64
	// Logf receives nsjail's log when nsjail cannot run a job.
	Logf func(format string, args ...any)
}

// nsjail logs the jailed process's fate at level INFO; without one of these
// lines the job never ran. Its own exit status cannot tell the two apart,
// because it reports a signal N as status 128+N.
var (
	exitedLine     = regexp.MustCompile(`exited with status: (\d+)`)
	terminatedLine = regexp.MustCompile(`terminated with signal: (\w+) \(\d+\)`)
	timeLimitLine  = regexp.MustCompile(`run time >= time limit`)
)

// stopGrace is how long nsjail has to kill and reap the jail after SIGTERM
// before it is killed itself. The jailed process then dies through its
// parent-death signal, and the container's init reaps it.
const stopGrace = time.Second

// Run runs job inside nsjail. The code is visible in the jail, read-only,
// as /job/code; the input is the jailed program's standard input. When ctx
// is done before the run ends, Run stops the run and returns ctx's error.
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

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	timer := time.AfterFunc(n.Timeout, stop)

	args := []string{
		"--config", n.Config,
		"--bindmount_ro", dir + ":/job",
		// nsjail counts whole seconds, so its own limit is only a backstop
		// that never fires before the timer above.
		"--time_limit", strconv.Itoa(int(math.Ceil(n.Timeout.Seconds())) + 2),
		// The run's own cgroup; nsjail creates and removes it.
		"--cgroup_mem_max", strconv.FormatInt(n.Memory, 10),
		"--cgroup_mem_swap_max", "0",
		"--cgroup_pids_max", strconv.Itoa(n.Pids),
		"--cgroup_cpu_ms_per_sec", strconv.Itoa(max(1, int(math.Round(n.CPUs*1000)))),
		// Dyalog reads a size without a unit as KiB; the unit is explicit.
		"--env", fmt.Sprintf("MAXWS=%dK", n.Workspace/1024),
		"--", n.Command,
	}
	cmd := exec.CommandContext(runCtx, n.Binary, append(args, job.Args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopGrace
	cmd.Stdin = bytes.NewReader(job.Input)
	stdout := &capped{limit: n.OutputLimit}
	stderr := &capped{limit: n.OutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
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
	fired := !timer.Stop()
	nsjailLog := <-logged

	if !fired && ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	exited := exitedLine.FindSubmatch(nsjailLog)
	terminated := terminatedLine.FindSubmatch(nsjailLog)
	result := Result{
		Stdout:          stdout.buf.Bytes(),
		Stderr:          stderr.buf.Bytes(),
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
		// A program that exited by itself as the timer fired was not stopped.
		TimedOut: timeLimitLine.Match(nsjailLog) || (fired && exited == nil),
		WallTime: wall,
	}
	if ps := cmd.ProcessState; ps != nil {
		// nsjail reaps the jailed process, so its usage includes the program's.
		result.UserTime = ps.UserTime()
		result.SysTime = ps.SystemTime()
	}
	switch {
	case terminated != nil:
		result.Signal = string(terminated[1])
		return result, nil
	case exited != nil:
		code, err := strconv.Atoi(string(exited[1]))
		if err != nil {
			return Result{}, fmt.Errorf("parsing exit status %q: %w", exited[1], err)
		}
		result.ExitCode = &code
		return result, nil
	case result.TimedOut:
		// nsjail itself was killed after stopGrace, before logging the outcome.
		return result, nil
	}
	if n.Logf != nil {
		n.Logf("nsjail did not run the job (%v); its log:\n%s", waitErr, nsjailLog)
	}
	return Result{}, fmt.Errorf("nsjail did not run the job: %v", waitErr)
}

// capped keeps the first limit bytes written to it and discards the rest
// while still accepting them, so a program never blocks on a full pipe.
type capped struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	switch {
	case room <= 0:
		c.truncated = c.truncated || len(p) > 0
	case len(p) > room:
		c.buf.Write(p[:room])
		c.truncated = true
	default:
		c.buf.Write(p)
	}
	return len(p), nil
}
