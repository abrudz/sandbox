// Command runner-api serves the sandbox runner's HTTP API on port 8080.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/abrudz/sandbox/internal/api"
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
	version, err := os.ReadFile(versionFile)
	if err != nil {
		log.Fatalf("reading the Dyalog version: %v", err)
	}
	if err := os.MkdirAll(jobsDir, 0o711); err != nil {
		log.Fatalf("creating %s: %v", jobsDir, err)
	}

	executor := &sandbox.Nsjail{
		Binary:  nsjailBinary,
		Config:  nsjailConfig,
		Command: jailCommand,
		JobsDir: jobsDir,
		Logf:    log.Printf,
	}
	srv := &http.Server{
		Addr: ":8080",
		Handler: api.NewHandler(api.Options{
			Executor:      executor,
			DyalogVersion: strings.TrimSpace(string(version)),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("runner-api listening on %s (Dyalog %s)", srv.Addr, strings.TrimSpace(string(version)))
	log.Fatal(srv.ListenAndServe())
}
