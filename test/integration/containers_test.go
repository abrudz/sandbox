//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// startContainer starts another container from the image under test with
// extra environment variables (NAME=value) and waits until it serves
// /healthz. It returns the container's API URL and name; the container is
// removed when the test ends.
func startContainer(t *testing.T, vars ...string) (url, name string) {
	t.Helper()
	image := env(t, "SANDBOX_IMAGE")
	name = fmt.Sprintf("sandbox-test-%d", time.Now().UnixNano())

	args := []string{"run", "-d", "--privileged", "--name", name, "-p", "127.0.0.1::8080"}
	for _, v := range vars {
		args = append(args, "-e", v)
	}
	args = append(args, image)
	if out, err := docker(t, args...); err != nil {
		t.Fatalf("starting a container from %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			if out, err := docker(t, "logs", name); err == nil {
				t.Logf("logs of %s:\n%s", name, out)
			}
		}
		_, _ = docker(t, "rm", "-f", name)
	})

	out, err := docker(t, "port", name, "8080/tcp")
	if err != nil {
		t.Fatalf("finding the port of %s: %v\n%s", name, err, out)
	}
	url = "http://" + strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])

	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := client.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return url, name
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not serve /healthz within 30 s", name)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// leftoverScript lists what runs have left in a container: processes other
// than the container's PID 1, runner-api, and this script and its children;
// per-run directories; and cgroups other than the entry point's leaf "init".
const leftoverScript = `
self=$$
for d in /proc/[0-9]*; do
  pid=${d#/proc/}
  [ "$pid" = 1 ] && continue
  [ "$pid" = "$self" ] && continue
  ppid=$(awk '/^PPid:/ { print $2 }' "$d/status" 2>/dev/null)
  [ -z "$ppid" ] || [ "$ppid" = "$self" ] && continue
  name=$(cat "$d/comm" 2>/dev/null)
  [ "$name" = runner-api ] && continue
  state=$(awk '/^State:/ { print $2 }' "$d/status" 2>/dev/null)
  echo "process $pid state $state $name"
done
for j in /tmp/sandbox-jobs/*; do
  [ -e "$j" ] && echo "job directory $j"
done
find /sys/fs/cgroup -mindepth 1 -type d ! -path /sys/fs/cgroup/init | sed 's/^/cgroup /'
`

// leftovers returns one line per thing left behind in container. Processes
// read "process <pid> state <state> <name>", so a zombie shows state Z.
func leftovers(t *testing.T, container string) []string {
	t.Helper()
	out, err := docker(t, "exec", container, "sh", "-c", leftoverScript)
	if err != nil {
		t.Fatalf("listing leftovers in %s: %v\n%s", container, err, out)
	}
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			found = append(found, line)
		}
	}
	return found
}
