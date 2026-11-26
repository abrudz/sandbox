//go:build integration

package integration

import (
	"strings"
	"testing"
)

// dotNetCall loads .NET, reports whether the bridge loaded, and prints the
// result of a .NET method that has a single overload.
const dotNetCall = `⎕USING←'System'
⎕←2↑2250⌶0
⎕←Math.Sqrt 16
`

// heapLimitCode prints the most memory the .NET garbage collector may use;
// with a heap limit set, that is the limit.
const heapLimitCode = `⎕USING←'System'
GC.Collect
⎕←GC.GetGCMemoryInfo.TotalAvailableMemoryBytes
`

func TestARunCallsADotNetMethod(t *testing.T) {
	r := run(t, dotNetCall, "")

	assertRunLines(t, r, "1 1", "4")
	assertExitCode(t, r, 0)
}

func TestARunThatLoadsDotNetFinishesInUnder3Seconds(t *testing.T) {
	r := run(t, dotNetCall, "")

	assertRunLines(t, r, "1 1", "4")
	if r.WallTimeMs >= 3000 {
		t.Errorf("the run took %d ms, want under 3000", r.WallTimeMs)
	}
}

func TestADotNetAllocationPastTheHeapLimitThrowsAnExceptionTheRunCanTrap(t *testing.T) {
	// A StringBuilder allocates its capacity at once: 100 million UTF-16
	// characters are 200 MB, more than a quarter of the default 512 MiB
	// memory limit and less than the limit itself.
	r := run(t, `⎕USING←'System' 'System.Text'
⎕←{90::⍕⎕EXCEPTION.GetType ⋄ _←⎕NEW StringBuilder ⍵ ⋄ 'allocated'}100000000
⎕←'continued'
`, "")

	assertRunLines(t, r, "System.OutOfMemoryException", "continued")
	assertExitCode(t, r, 0)
}

func TestTheDotNetHeapLimitIsAQuarterOfTheMemoryLimit(t *testing.T) {
	// A quarter of the default 512 MiB...
	r := run(t, heapLimitCode, "")
	assertRunLines(t, r, "134217728")

	// ...and of 256 MiB with SANDBOX_MEMORY=256M.
	url, _ := startContainer(t, "SANDBOX_MEMORY=256M")
	r = runAt(t, url, heapLimitCode, "")

	assertRunLines(t, r, "67108864")
}

func TestARunUsingDotNetOpensNoDiagnosticsChannel(t *testing.T) {
	// The runtime creates its diagnostics socket and debugger pipes in /tmp
	// as it starts, unless diagnostics are disabled.
	r := run(t, dotNetCall+"{⎕←⍵}¨⎕SH'ls -A /tmp'\n", "")

	got := lines(r.Stdout)
	if len(got) < 2 || got[0] != "1 1" || got[1] != "4" {
		t.Fatalf("the .NET call did not complete; %s", describe(r))
	}
	for _, name := range got[2:] {
		if strings.HasPrefix(name, "dotnet-diagnostic-") || strings.HasPrefix(name, "clr-debug-pipe-") {
			t.Errorf("/tmp holds %s", name)
		}
	}
}
