package workflows

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTheImageBuildsNsjailFromACommitNamedByItsHash(t *testing.T) {
	// nsjail is the jail's security boundary, and a tag or branch can be
	// moved to other content. The nsjail upstream check reads the same ARG.
	data, err := os.ReadFile(filepath.Join("..", "..", "image", "Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	if !regexp.MustCompile(`(?m)^ARG NSJAIL_COMMIT=[0-9a-f]{40}$`).MatchString(text) {
		t.Error("image/Containerfile does not set NSJAIL_COMMIT to a full commit hash")
	}
	if !strings.Contains(text, "${NSJAIL_COMMIT}") {
		t.Error("image/Containerfile does not build nsjail from NSJAIL_COMMIT")
	}
	if strings.Contains(text, "--branch") {
		t.Error("image/Containerfile clones a branch or tag")
	}
}
