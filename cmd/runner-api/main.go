// Command runner-api serves the sandbox runner's HTTP API and its web
// frontend on port 8080.
//
// On SIGTERM or SIGINT it stops accepting runs (new requests get 503), lets
// the runs in progress finish, and exits with status 0.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	webDir       = "/usr/share/sandbox/web"
	jobsDir      = "/tmp/sandbox-jobs"
)

func main() {
	limits, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	versionBytes, err := os.ReadFile(versionFile)
	if err != nil {
		log.Fatalf("reading the Dyalog version: %v", err)
	}
	version := strings.TrimSpace(string(versionBytes))
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
	handler := api.NewHandler(api.Options{
		Executor:          executor,
		DyalogVersion:     version,
		RequestLimit:      limits.RequestLimit,
		Concurrency:       limits.Concurrency,
		ClientConcurrency: limits.ClientConcurrency,
		ClientRate:        limits.ClientRate,
		TrustedProxies:    limits.TrustedProxies,
		WebDir:            webDir,
	})
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	log.Printf("runner-api listening on %s (Dyalog %s; per run: timeout %v, output %d bytes per stream, "+
		"memory %d bytes, workspace %d bytes, %g CPUs, %d processes; requests: %d bytes, "+
		"%d runs at once, %d per client, %d per client per minute; trusted proxies %v)",
		srv.Addr, version, limits.Timeout, limits.OutputLimit,
		limits.Memory, limits.Workspace(), limits.CPUs, limits.Pids, limits.RequestLimit,
		limits.Concurrency, limits.ClientConcurrency, limits.ClientRate, limits.TrustedProxies)

	select {
	case err := <-served:
		log.Fatal(err)
	case <-stop.Done():
	}

	// A run lasts at most its timeout plus the executor's grace period for
	// stopping it, so draining is bounded.
	log.Printf("draining: new requests get 503")
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), limits.Timeout+5*time.Second)
	defer cancelDrain()
	if err := handler.Drain(drainCtx); err != nil {
		log.Printf("draining: %v", err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("shutting down: %v", err)
	}
	log.Printf("stopped")
}
