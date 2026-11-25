package workflows

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// needsBuild runs scripts/needs-build and reports whether it says to build,
// with what it printed.
func needsBuild(t *testing.T, upstream, builtFrom, created, now string) (bool, string) {
	t.Helper()
	out, err := exec.Command(filepath.Join("..", "..", "scripts", "needs-build"), upstream, builtFrom, created, now).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, string(out)
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, string(out)
	}
	t.Fatalf("scripts/needs-build: %v\n%s", err, out)
	return false, ""
}

const (
	now      = "2026-11-25T04:23:00Z"
	upstream = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	other    = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func TestTheDailyRunBuildsWhenTheUpstreamImageHasChanged(t *testing.T) {
	if build, out := needsBuild(t, upstream, other, "2026-11-24T04:30:00Z", now); !build {
		t.Errorf("a published image built from another upstream digest a day ago: no build\n%s", out)
	}
}

func TestTheDailyRunSkipsAnImageBuiltFromTheSameUpstreamWithinTheWeek(t *testing.T) {
	for _, created := range []string{"2026-11-24T04:30:00Z", "2026-11-19T04:30:00Z"} {
		if build, out := needsBuild(t, upstream, upstream, created, now); build {
			t.Errorf("a published image built from the same upstream at %s: build\n%s", created, out)
		}
	}
}

func TestTheDailyRunRebuildsAnImageAWeekAfterItWasBuilt(t *testing.T) {
	// Built by the run a week earlier, a few minutes after that run's check.
	if build, out := needsBuild(t, upstream, upstream, "2026-11-18T04:31:00Z", now); !build {
		t.Errorf("a published image built a week before: no build\n%s", out)
	}
}

func TestTheDailyRunBuildsWhenThePublishedImageCannotBeRead(t *testing.T) {
	for _, c := range []struct{ builtFrom, created string }{
		{"", "2026-11-24T04:30:00Z"},
		{upstream, ""},
		{upstream, "yesterday"},
	} {
		if build, out := needsBuild(t, upstream, c.builtFrom, c.created, now); !build {
			t.Errorf("built from %q at %q: no build\n%s", c.builtFrom, c.created, out)
		}
	}
}
