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
	log.Printf("runner-api listening on %s (Dyalog %s; timeout %v, output limit %d bytes per stream)",
		srv.Addr, strings.TrimSpace(string(version)), limits.Timeout, limits.OutputLimit)
	log.Fatal(srv.ListenAndServe())
}
