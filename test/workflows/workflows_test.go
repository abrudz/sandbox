// Tests of the GitHub Actions workflows in .github/workflows: what each job
// may do, and the publish workflow's decision to rebuild. The workflows are
// YAML; these tests read the few shapes they need line by line, relying on
// the two-space indentation the workflows use.
package workflows

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workflows returns the lines of every workflow file, by file name.
func workflows(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	files := map[string][]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(p)] = strings.Split(string(data), "\n")
	}
	return files
}

// split returns a workflow's lines before "jobs:", and each job's lines by
// job id: the keys indented two spaces under "jobs:".
func split(lines []string) (top []string, jobs map[string][]string) {
	jobs = map[string][]string{}
	job := ""
	inJobs := false
	for _, line := range lines {
		switch {
		case line == "jobs:":
			inJobs = true
		case !inJobs:
			top = append(top, line)
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":"):
			job = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case job != "":
			jobs[job] = append(jobs[job], line)
		}
	}
	return top, jobs
}

var usesLine = regexp.MustCompile(`^\s*(?:- )?uses:\s*(\S+)\s*(?:#\s*(\S+))?`)

func TestEveryActionIsPinnedToACommitHash(t *testing.T) {
	pinned := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	for file, lines := range workflows(t) {
		for i, line := range lines {
			m := usesLine.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(m[1], "./") {
				continue
			}
			if !pinned.MatchString(m[1]) {
				t.Errorf("%s:%d: %s is not pinned to a commit hash", file, i+1, m[1])
			} else if !strings.HasPrefix(m[2], "v") {
				t.Errorf("%s:%d: %s has no version comment", file, i+1, m[1])
			}
		}
	}
}

func TestEveryCheckoutKeepsNoCredentials(t *testing.T) {
	for file, lines := range workflows(t) {
		for i, line := range lines {
			m := usesLine.FindStringSubmatch(line)
			if m == nil || !strings.HasPrefix(m[1], "actions/checkout@") {
				continue
			}
			// The step's other keys are indented further than its dash.
			dash := strings.Index(line, "- ")
			persists := true
			for _, next := range lines[i+1:] {
				indent := len(next) - len(strings.TrimLeft(next, " "))
				if strings.TrimSpace(next) != "" && indent <= dash {
					break
				}
				if strings.TrimSpace(next) == "persist-credentials: false" {
					persists = false
				}
			}
			if persists {
				t.Errorf("%s:%d: the checkout leaves the job's token in the repository's git configuration", file, i+1)
			}
		}
	}
}

func TestOnlyAJobThatRunsNothingFromTheRepositoryCanWritePackages(t *testing.T) {
	// What a job that can push images must not do: run the repository's code
	// or its test dependencies, or hand its permission to another workflow.
	forbidden := []string{"actions/checkout", "actions/setup-", "npm ", "npx ", "go test", "go run", "playwright", "uses: ./"}
	writers := 0
	for file, lines := range workflows(t) {
		top, jobs := split(lines)
		if strings.Contains(strings.Join(top, "\n"), "packages: write") {
			t.Errorf("%s: the workflow's own permissions include packages: write, which every job without permissions of its own gets", file)
		}
		for job, body := range jobs {
			text := strings.Join(body, "\n")
			if !strings.Contains(text, "packages: write") {
				continue
			}
			writers++
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Errorf("%s: job %s can write packages and contains %q", file, job, f)
				}
			}
		}
	}
	if writers == 0 {
		t.Error("no job can write packages, so none publishes")
	}
}

func TestThePublishingJobChecksTheImageIDBeforePushing(t *testing.T) {
	found := false
	for file, lines := range workflows(t) {
		_, jobs := split(lines)
		for job, body := range jobs {
			if !strings.Contains(strings.Join(body, "\n"), "packages: write") {
				continue
			}
			check, push := -1, -1
			for i, line := range body {
				if check < 0 && strings.Contains(line, "outputs.image-id") {
					check = i
				}
				if push < 0 && strings.Contains(line, "docker push") {
					push = i
				}
			}
			if push < 0 {
				continue
			}
			found = true
			if check < 0 || check > push {
				t.Errorf("%s: job %s pushes without first comparing the image with the build job's image-id output", file, job)
			}
		}
	}
	if !found {
		t.Error("no job that can write packages pushes an image")
	}
}
