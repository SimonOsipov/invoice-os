// fleetgate_test.go: every workflow path filter that gates a deploy lists sidecar/**.
package fleetgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	devEnvRel = ".github/workflows/dev-env.yml"
	ciRel     = ".github/workflows/ci.yml"
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

var yamlItemRe = regexp.MustCompile(`^\s+-\s+'([^']*)'\s*$`)

// yamlListAt collects the quoted list items directly below lines[start],
// returning each item with its 1-based line number.
func yamlListAt(lines []string, start int) (items []string, at []int) {
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := yamlItemRe.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		items = append(items, m[1])
		at = append(at, i+1)
	}
	return items, at
}

// findKey returns the index of the first line at or after from whose trimmed
// text is key.
func findKey(lines []string, from int, key string) int {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == key {
			return i
		}
	}
	return -1
}

// TestWorkflows_SidecarPathsAreFiltered is the measurement that closes Core
// AC 6: both filters already carry sidecar/**, so this passes on first write
// and exists to keep it that way.
func TestWorkflows_SidecarPathsAreFiltered(t *testing.T) {
	root := repoRoot(t)

	devLines := strings.Split(readFile(t, filepath.Join(root, devEnvRel)), "\n")
	jobs := findKey(devLines, 0, "jobs:")
	if jobs < 0 {
		t.Fatalf("%s: no `jobs:` key", devEnvRel)
	}
	changes := findKey(devLines, jobs, "changes:")
	if changes < 0 {
		t.Fatalf("%s: no `changes` job", devEnvRel)
	}
	filterAt := findKey(devLines, changes, "e2e:")
	if filterAt < 0 {
		t.Fatalf("%s: the `changes` job has no `e2e:` path filter", devEnvRel)
	}
	devPaths, devAt := yamlListAt(devLines, filterAt)
	if len(devPaths) == 0 {
		t.Fatalf("%s:%d: parsed an empty `e2e:` filter list", devEnvRel, filterAt+1)
	}
	if !assertMember(t, devEnvRel, devPaths, devAt, "sidecar/**") {
		t.Errorf("%s: `sidecar/**` absent from the `changes` job's e2e filter -- a Python-only PR fires no deploy gate", devEnvRel)
	}
	// Control: a member every version of this list has carried.
	if !contains(devPaths, "internal/**") {
		t.Errorf("%s: the `e2e:` filter parsed to %v, which does not contain `internal/**` -- the parse reached the wrong block", devEnvRel, devPaths)
	}

	ciLines := strings.Split(readFile(t, filepath.Join(root, ciRel)), "\n")
	sc := findKey(ciLines, 0, "sidecar:")
	if sc < 0 {
		t.Fatalf("%s: no `sidecar:` path filter", ciRel)
	}
	ciPaths, ciAt := yamlListAt(ciLines, sc)
	if len(ciPaths) == 0 {
		t.Fatalf("%s:%d: the `sidecar:` filter parsed to an empty list", ciRel, sc+1)
	}
	if !assertMember(t, ciRel, ciPaths, ciAt, "sidecar/**") {
		t.Errorf("%s:%d: the `sidecar:` filter does not list `sidecar/**` -- it is %v", ciRel, sc+1, ciPaths)
	}
}

// assertMember reports whether want is in items and logs where it was found,
// so the passing run records the measured line rather than asserting a
// number that drifts.
func assertMember(t *testing.T, file string, items []string, at []int, want string) bool {
	t.Helper()
	for i, it := range items {
		if it == want {
			t.Logf("%s:%d lists %q", file, at[i], want)
			return true
		}
	}
	return false
}
