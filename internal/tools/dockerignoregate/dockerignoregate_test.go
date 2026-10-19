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
	"regexp"
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
// a trailing slash -- the shape every existing entry (cmd, internal,
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

		// Control needle: ".claude" is a bare-line entry both files already
		// carry. Without it, an absence below could mean the scan found
		// nothing at all.
		if !deniesTree(content, ".claude") {
			t.Fatalf("%s: the known entry `.claude` was not found -- the scan reached nothing", rel)
		}

		if !deniesTree(content, "sidecar") {
			t.Errorf("%s: no `sidecar` deny entry -- every other service's build context uploads sidecar/docling's Python source, models config and fixtures", rel)
		}
	}
}

// ignored applies BuildKit's last-match-wins rule to one path: a pattern covers
// the path and everything under it, `!` re-includes, `*` stays inside one segment.
func ignored(patterns []string, p string) bool {
	out := false
	for _, pat := range patterns {
		neg := strings.HasPrefix(pat, "!")
		pat = strings.Trim(strings.TrimPrefix(pat, "!"), "/")
		re := regexp.QuoteMeta(pat)
		re = strings.ReplaceAll(re, `\*\*/`, `(.*/)?`)
		re = strings.ReplaceAll(re, `\*`, `[^/]*`)
		if regexp.MustCompile(`^` + re + `(/.*)?$`).MatchString(p) {
			out = !neg
		}
	}
	return out
}

func TestBlogIgnoreFileKeepsTheServerSourcesAndDeniesSidecar(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "frontend/blog/Dockerfile.dockerignore"))
	if err != nil {
		t.Fatalf("read the blog ignore file: %v", err)
	}
	var patterns []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			patterns = append(patterns, l)
		}
	}

	// Control: the matcher denies a path the file names outright.
	if !ignored(patterns, ".claude/rules/blog.md") {
		t.Fatal("control: .claude/rules/blog.md is not ignored; the matcher reached nothing")
	}

	tracked, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "go.mod", "go.sum", "cmd/blog", "internal/blogweb", "internal/platform").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	need := strings.Fields(string(tracked))
	if len(need) < 6 {
		t.Fatalf("only %d tracked files under the Go stage's inputs: %v", len(need), need)
	}
	for _, f := range need {
		if ignored(patterns, f) {
			t.Errorf("%s is ignored; the Go stage of the blog image cannot build", f)
		}
	}

	for _, f := range []string{"sidecar/docling/Dockerfile", "cmd/gateway/main.go", "internal/gateway/gateway.go", "migrations/0001_init.sql"} {
		if !ignored(patterns, f) {
			t.Errorf("%s is in the blog build context", f)
		}
	}
}
