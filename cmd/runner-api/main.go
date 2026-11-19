// Command runner-api serves the sandbox runner's HTTP API on port 8080.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/config"
	"github.com/abrudz/sandbox/internal/sandbox"
)

// Paths in the sandbox image; see image/Containerfile.
const (
	nsjailBinary = "/usr/local/bin/nsjail"
	nsjailConfig = "/etc/sandbox/nsjail.cfg"
	jailCommand  = "/usr/local/bin/run"
	versionFile  = "/usr/share/sandbox/dyalog-version"
	jobsDir      = "/tmp/sandbox-jobs"
)

func main() {
	limits, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	version, err := os.ReadFile(versionFile)
	if err != nil {
		log.Fatalf("reading the Dyalog version: %v", err)
	}
	if err := os.MkdirAll(jobsDir, 0o711); err != nil {
		log.Fatalf("creating %s: %v", jobsDir, err)
	}

	executor := &sandbox.Nsjail{
		Binary:      nsjailBinary,
		Config:      nsjailConfig,
		Command:     jailCommand,
		JobsDir:     jobsDir,
		Timeout:     limits.Timeout,
		OutputLimit: limits.OutputLimit,
		Memory:      limits.Memory,
		CPUs:        limits.CPUs,
		Pids:        limits.Pids,
		Workspace:   limits.Workspace(),
		Logf:        log.Printf,
	}
	srv := &http.Server{
		Addr: ":8080",
		Handler: api.NewHandler(api.Options{
			Executor:      executor,
			DyalogVersion: strings.TrimSpace(string(version)),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("runner-api listening on %s (Dyalog %s; per run: timeout %v, output %d bytes per stream, "+
		"memory %d bytes, workspace %d bytes, %g CPUs, %d processes)",
		srv.Addr, strings.TrimSpace(string(version)), limits.Timeout, limits.OutputLimit,
		limits.Memory, limits.Workspace(), limits.CPUs, limits.Pids)
	log.Fatal(srv.ListenAndServe())
}
