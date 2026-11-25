//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
)

// readJSON parses data as JSON into a generic value.
func readJSON(t *testing.T, what string, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parsing %s: %v", what, err)
	}
	return v
}

// sortRuleNames sorts the system call names of every rule in profile, so
// that a name added in one place compares equal to one added in another.
func sortRuleNames(t *testing.T, profile map[string]any) {
	t.Helper()
	for _, rule := range profile["syscalls"].([]any) {
		r := rule.(map[string]any)
		names := r["names"].([]any)
		slices.SortFunc(names, func(a, b any) int {
			switch {
			case a.(string) < b.(string):
				return -1
			case a.(string) > b.(string):
				return 1
			}
			return 0
		})
	}
}

func TestSeccompProfileIsMobysWithOnlyTheRecordedChanges(t *testing.T) {
	notices, err := os.ReadFile(filepath.Join("..", "..", "THIRD_PARTY_NOTICES"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Moby's default seccomp profile, revision\s+([0-9a-f]{40})`).FindSubmatch(notices)
	if m == nil {
		t.Fatal("THIRD_PARTY_NOTICES records no revision of Moby's seccomp profile")
	}
	resp, err := http.Get("https://raw.githubusercontent.com/moby/profiles/" + string(m[1]) + "/seccomp/default.json")
	if err != nil {
		t.Fatalf("fetching Moby's profile: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("fetching Moby's profile: status %d, %v", resp.StatusCode, err)
	}
	want := readJSON(t, "Moby's profile", data)
	ours, err := os.ReadFile(filepath.Join("..", "..", "deploy", "seccomp.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, "deploy/seccomp.json", ours)

	// The recorded changes: pivot_root joins mount among the calls allowed
	// under CAP_SYS_ADMIN, and x86_64 has no sub-architectures, so i386 and
	// x32 calls are refused.
	added := false
	for _, rule := range want["syscalls"].([]any) {
		r := rule.(map[string]any)
		includes, _ := r["includes"].(map[string]any)
		if slices.Contains(r["names"].([]any), any("mount")) && reflect.DeepEqual(includes["caps"], []any{"CAP_SYS_ADMIN"}) {
			r["names"] = append(r["names"].([]any), "pivot_root")
			added = true
		}
	}
	if !added {
		t.Fatal("Moby's profile has no rule allowing mount under CAP_SYS_ADMIN")
	}
	for _, arch := range want["archMap"].([]any) {
		if a := arch.(map[string]any); a["architecture"] == "SCMP_ARCH_X86_64" {
			a["subArchitectures"] = []any{}
		}
	}
	sortRuleNames(t, want)
	sortRuleNames(t, got)

	if !reflect.DeepEqual(got, want) {
		t.Error("deploy/seccomp.json differs from Moby's profile with the recorded changes")
		for _, key := range []string{"defaultAction", "defaultErrnoRet", "archMap", "syscalls"} {
			if !reflect.DeepEqual(got[key], want[key]) {
				g, _ := json.Marshal(got[key])
				w, _ := json.Marshal(want[key])
				t.Errorf("%s: got %.600s\nwant %.600s", key, g, w)
			}
		}
	}
}
