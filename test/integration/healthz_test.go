//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"
)

func TestRunningContainerServesHealthz(t *testing.T) {
	url := env(t, "SANDBOX_URL") + "/healthz"
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: status %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
