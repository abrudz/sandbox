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
	digestFile   = "/usr/share/sandbox/image-digest"
	commitFile   = "/usr/share/sandbox/commit"
	webDir       = "/usr/share/sandbox/web"
	jobsDir      = "/tmp/sandbox-jobs"
)

// Connection limits. A request arrives in full, headers and body, within
// readTimeout. An idle connection lasts longer than Caddy keeps one open
// (two minutes), so Caddy never sends a request on a connection being
// closed. A run's response is written within responseTimeout.
const (
	readTimeout     = 15 * time.Second
	idleTimeout     = 3 * time.Minute
	responseTimeout = 10 * time.Second
)

func main() {
	limits, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	version := readImageFile(versionFile)
	imageDigest := readImageFile(digestFile)
	commit := readImageFile(commitFile)
	// A run removes its own directory when it ends; directories left by an
	// earlier process, which exited abruptly, hold submissions nobody needs.
	if err := os.RemoveAll(jobsDir); err != nil {
		log.Fatalf("removing %s: %v", jobsDir, err)
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
		DotNetHeap:  limits.DotNetHeap(),
		Logf:        log.Printf,
	}
	handler := api.NewHandler(api.Options{
		Executor:          executor,
		DyalogVersion:     version,
		RequestLimit:      limits.RequestLimit,
		Concurrency:       limits.Concurrency,
		ClientConcurrency: limits.ClientConcurrency,
		ClientRate:        limits.ClientRate,
		ReadConcurrency:   limits.ReadConcurrency,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		ResponseTimeout:   responseTimeout,
		TrustedProxies:    limits.TrustedProxies,
		WebDir:            webDir,
		ImageDigest:       imageDigest,
		Commit:            commit,
		Timeout:           limits.Timeout,
	})
	ln, err := api.Listen(limits.Socket)
	if err != nil {
		log.Fatalf("listening: %v", err)
	}
	srv := handler.HTTPServer(ln.Addr().String())

	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Printf("runner-api listening on %s (Dyalog %s from %s, commit %s; per run: timeout %v, output %d bytes per stream, "+
		"memory %d bytes, workspace %d bytes, .NET heap %d bytes, %g CPUs, %d processes; requests: %d bytes, "+
		"%d runs at once, %d per client, %d per client per minute; trusted proxies %v)",
		srv.Addr, version, imageDigest, commit, limits.Timeout, limits.OutputLimit,
		limits.Memory, limits.Workspace(), limits.DotNetHeap(), limits.CPUs, limits.Pids, limits.RequestLimit,
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

// readImageFile returns the trimmed contents of a file the image build
// writes, and exits if it is missing.
func readImageFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}
