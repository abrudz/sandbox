//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// head returns the headers of GET path on the shared container.
func head(t *testing.T, path string) http.Header {
	t.Helper()
	resp, err := client.Get(env(t, "SANDBOX_URL") + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	return resp.Header
}

// policyDirectives returns a Content Security Policy's directives by name.
func policyDirectives(policy string) map[string][]string {
	directives := map[string][]string{}
	for _, d := range strings.Split(policy, ";") {
		if f := strings.Fields(d); len(f) > 0 {
			directives[strings.ToLower(f[0])] = f[1:]
		}
	}
	return directives
}

func TestEveryResponseForbidsContentTypeSniffing(t *testing.T) {
	for _, path := range []string{"/", "/frontend.js", "/frontend.css", "/api/v1/version", "/healthz"} {
		if got := head(t, path).Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options %q, want nosniff", path, got)
		}
	}
}

func TestThePageAllowsOnlyItsOwnOriginAndNoFraming(t *testing.T) {
	policy := head(t, "/").Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("GET /: no Content-Security-Policy")
	}
	d := policyDirectives(policy)

	want := map[string]string{
		"default-src":     "'self'",
		"object-src":      "'none'",
		"base-uri":        "'none'",
		"form-action":     "'none'",
		"frame-ancestors": "'none'",
	}
	for name, value := range want {
		if got := strings.Join(d[name], " "); got != value {
			t.Errorf("%s is %q, want %q; policy %q", name, got, value, policy)
		}
	}
	for name, values := range d {
		for _, v := range values {
			if v == "'unsafe-inline'" || v == "'unsafe-eval'" || v == "*" || strings.HasPrefix(v, "http") {
				t.Errorf("%s allows %s; policy %q", name, v, policy)
			}
		}
	}
}
