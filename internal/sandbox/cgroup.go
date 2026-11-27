package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// runControllers are the controllers nsjail's limits use. A run's cgroup
// delegates them to the cgroup nsjail creates inside it; its parent must
// delegate them too (image/entrypoint does so for the container's root).
const runControllers = "+cpu +memory +pids"

// stuckWarning is how long removeRunCgroup waits for a run's processes to
// die before it reports them.
const stuckWarning = 5 * time.Second

// createRunCgroup creates the cgroup a run's nsjail works in.
func createRunCgroup(path string) error {
	if err := os.Mkdir(path, 0o755); err != nil {
		return fmt.Errorf("creating the run's cgroup: %w", err)
	}
	if err := writeCgroupFile(filepath.Join(path, "cgroup.subtree_control"), runControllers); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("delegating controllers to the run's cgroup: %w", err)
	}
	return nil
}

// removeRunCgroup kills every process left in a run's cgroup, waits until
// it is empty, and removes it with the cgroups inside it, nsjail's among
// them when nsjail died before its own teardown. Run returns, and the run's
// slot is freed, only after this, so no process of a run outlives its
// slot; a process that cannot be killed holds the slot.
func (n *Nsjail) removeRunCgroup(path string) {
	start := time.Now()
	reported := false
	for populated(path) {
		killCgroup(path)
		if !reported && time.Since(start) > stuckWarning {
			n.logf("the run's cgroup %s still holds processes %v after nsjail exited", path, stuckWarning)
			reported = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := removeCgroupTree(path); err != nil {
		n.logf("removing the run's cgroup: %v", err)
	}
}

// populated reports whether the cgroup at path, or one below it, holds a
// process. A cgroup that cannot be read holds none.
func populated(path string) bool {
	f, err := os.Open(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if s.Text() == "populated 1" {
			return true
		}
	}
	return false
}

// killCgroup sends SIGKILL to every process in the cgroup at path and below
// it: through cgroup.kill (Linux 5.14 and later), which also catches
// processes forked meanwhile, or else one process at a time, which the
// caller repeats until none is left.
func killCgroup(path string) {
	if writeCgroupFile(filepath.Join(path, "cgroup.kill"), "1") == nil {
		return
	}
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		procs, err := os.ReadFile(filepath.Join(p, "cgroup.procs"))
		if err != nil {
			return nil
		}
		for _, field := range strings.Fields(string(procs)) {
			if pid, err := strconv.Atoi(field); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		return nil
	})
}

// removeCgroupTree removes the empty cgroup at path and those below it,
// deepest first.
func removeCgroupTree(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := removeCgroupTree(filepath.Join(path, e.Name())); err != nil {
				return err
			}
		}
	}
	return os.Remove(path)
}

// writeCgroupFile writes value to an existing cgroup interface file.
func writeCgroupFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (n *Nsjail) logf(format string, args ...any) {
	if n.Logf != nil {
		n.Logf(format, args...)
	}
}
