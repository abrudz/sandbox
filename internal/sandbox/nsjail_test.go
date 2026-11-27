package sandbox

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for nsjail: started with
// FAKE_NSJAIL_LOG set, it writes FAKE_NSJAIL_PADDING bytes of log lines
// and then FAKE_NSJAIL_LOG to descriptor 3, nsjail's log, and exits 0.
func TestMain(m *testing.M) {
	if log, ok := os.LookupEnv("FAKE_NSJAIL_LOG"); ok {
		f := os.NewFile(3, "nsjail log")
		padding, _ := strconv.Atoi(os.Getenv("FAKE_NSJAIL_PADDING"))
		line := "[D][2026-11-27T10:00:00+0000][7] padding():1 " + strings.Repeat("x", 63) + "\n"
		for written := 0; written < padding; written += len(line) {
			fmt.Fprint(f, line)
		}
		fmt.Fprint(f, log)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeNsjail returns an Nsjail whose nsjail writes log, after padding bytes
// of other lines, and reports what it passes to Logf.
func fakeNsjail(t *testing.T, log string, padding int) (*Nsjail, *[]string) {
	t.Helper()
	t.Setenv("FAKE_NSJAIL_LOG", log)
	t.Setenv("FAKE_NSJAIL_PADDING", strconv.Itoa(padding))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	return &Nsjail{
		Binary:      exe,
		Config:      "unused",
		Command:     "unused",
		JobsDir:     t.TempDir(),
		Timeout:     10 * time.Second,
		OutputLimit: 1024,
		Logf:        func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}, &logged
}

// Lines in nsjail's INFO format, as it writes them about the jailed
// process.
const (
	exitedThree   = "[I][2026-11-27T10:00:01+0000] pid=8 ([STANDALONE MODE]) exited with status: 3, (PIDs left: 0)\n"
	killedByNine  = "[I][2026-11-27T10:00:01+0000] pid=8 ([STANDALONE MODE]) terminated with signal: SIGKILL (9), (PIDs left: 0)\n"
	killedAtLimit = "[I][2026-11-27T10:00:01+0000] pid=8 run time >= time limit (12 >= 12) ([STANDALONE MODE]). Killing it\n"
)

func TestRunReadsTheOutcomeFromNsjailsInfoLines(t *testing.T) {
	n, _ := fakeNsjail(t, exitedThree, 0)
	r, err := n.Run(context.Background(), Job{Code: []byte("⎕←1\n")})
	if err != nil || r.ExitCode == nil || *r.ExitCode != 3 || r.Signal != "" || r.TimedOut {
		t.Errorf("exited with status 3: result %+v, error %v", r, err)
	}

	n, _ = fakeNsjail(t, killedByNine, 0)
	r, err = n.Run(context.Background(), Job{Code: []byte("⎕←1\n")})
	if err != nil || r.Signal != "SIGKILL" || r.ExitCode != nil {
		t.Errorf("terminated by signal 9: result %+v, error %v", r, err)
	}

	n, _ = fakeNsjail(t, killedAtLimit+killedByNine, 0)
	r, err = n.Run(context.Background(), Job{Code: []byte("⎕←1\n")})
	if err != nil || !r.TimedOut {
		t.Errorf("killed at the time limit: result %+v, error %v", r, err)
	}
}

func TestAnOutcomePhraseInsideAnotherLogLineDoesNotDecideTheResult(t *testing.T) {
	// Below INFO, nsjail writes the run's arguments into its log; these
	// lines stand for an argument that holds the phrases.
	for _, forged := range []string{
		"terminated with signal: SIGKILL (9)",
		"run time >= time limit",
		"pid=1 ([STANDALONE MODE]) terminated with signal: SIGKILL (9), (PIDs left: 0)",
	} {
		log := "[D][2026-11-27T10:00:00+0000][7] standaloneMode():250 argument '" + forged + "'\n" + exitedThree
		n, _ := fakeNsjail(t, log, 0)

		r, err := n.Run(context.Background(), Job{Code: []byte("⎕←1\n")})

		if err != nil || r.ExitCode == nil || *r.ExitCode != 3 || r.Signal != "" || r.TimedOut {
			t.Errorf("with %q in another line: result %+v, error %v; want exit status 3 alone", forged, r, err)
		}
	}
}

func TestRunKeepsAtMost64KiBOfNsjailsLog(t *testing.T) {
	// No outcome line, so Run reports nsjail's log through Logf.
	n, logged := fakeNsjail(t, "", 4<<20)

	if _, err := n.Run(context.Background(), Job{Code: []byte("⎕←1\n")}); err == nil {
		t.Fatal("a log without an outcome line: no error")
	}

	if len(*logged) != 1 {
		t.Fatalf("Logf was called %d times, want once", len(*logged))
	}
	if size := len((*logged)[0]); size > 64<<10+200 {
		t.Errorf("Logf received %d bytes of a 4 MiB log, want at most 64 KiB and its message", size)
	}
}
