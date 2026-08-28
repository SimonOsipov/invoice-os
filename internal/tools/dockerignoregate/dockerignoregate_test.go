// dockerignoregate_test.go: T-02-7. sidecar/docling/ is a new top-level tree
// neither root ignore-file excludes, so every other service's build context
// uploads its Python source, models config and fixtures.
//
// Lives here rather than internal/extraction: this scans two repo-root ignore
// files that govern all fourteen services' build contexts, not extraction
// package behavior -- internal/tools/{rlsgate,forcepushgate,stalerefs} are the
// existing home for a CI hygiene gate with no single feature owner. No main.go:
// unlike those three, this needs no runtime arguments (PR json, git refs), so a
// plain `go test ./...` reaches it with no extra ci.yml wiring.
package dockerignoregate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root")
	}
	return root
}

// deniesTree reports whether content denies tree as a whole line, bare or with
// a trailing slash -- the shape every existing entry (cmd, internal, backlog,
// docs, ...) already uses in both files.
func deniesTree(content, tree string) bool {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == tree || line == tree+"/" {
			return true
		}
	}
	return false
}

func TestBothRootIgnoreFilesDenySidecar(t *testing.T) {
	root := repoRoot(t)

	for _, rel := range []string{".dockerignore", "Dockerfile.dockerignore"} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		content := string(raw)

		// Control needle: "backlog" is a bare-line entry both files already
		// carry. Without it, an absence below could mean the scan found
		// nothing at all.
		if !deniesTree(content, "backlog") {
			t.Fatalf("%s: the known entry `backlog` was not found -- the scan reached nothing", rel)
		}

		if !deniesTree(content, "sidecar") {
			t.Errorf("%s: no `sidecar` deny entry -- every other service's build context uploads sidecar/docling's Python source, models config and fixtures", rel)
		}
	}
}
