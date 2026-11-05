// caddy_pins_test.go pins the Caddy image of the five frontend Dockerfiles and scripts/ci/caddy-headers.sh to one digest-pinned reference.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const pinnedCaddyImage = "caddy:2.11.7-alpine@sha256:e70b164da81ef698b3ad19af799ea6086ce6d9e48ed4079eb9fe26f6fbf14ee5"

var (
	caddyFromRE   = regexp.MustCompile(`(?mi)^FROM\s+(caddy:\S+)`)
	caddyImgRE    = regexp.MustCompile(`(?m)^img=(\S+)`)
	caddyPinRE    = regexp.MustCompile(`^caddy:\d+\.\d+\.\d+-alpine@sha256:[0-9a-f]{64}$`)
	caddyPinFiles = []string{
		"frontend/app/Dockerfile",
		"frontend/landing/Dockerfile",
		"frontend/library/Dockerfile",
		"frontend/support-console/Dockerfile",
		"frontend/ops-console/Dockerfile",
		"scripts/ci/caddy-headers.sh",
	}
)

func stripShellComments(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// caddyRefs maps each file path to its Caddy image reference; a file with none is absent.
func caddyRefs(files map[string]string) map[string]string {
	refs := map[string]string{}
	for path, content := range files {
		content = stripShellComments(content)
		re := caddyFromRE
		if strings.HasSuffix(path, ".sh") {
			re = caddyImgRE
		}
		if m := re.FindStringSubmatch(content); m != nil {
			refs[path] = m[1]
		}
	}
	return refs
}

func caddyPinProblems(files map[string]string) []string {
	var problems []string
	refs := caddyRefs(files)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	first := ""
	for _, p := range paths {
		ref, ok := refs[p]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s: no Caddy image reference", p))
		case !caddyPinRE.MatchString(ref):
			problems = append(problems, fmt.Sprintf("%s: %q is not caddy:<x.y.z>-alpine@sha256:<64 hex>", p, ref))
		case first == "":
			first = ref
		case ref != first:
			problems = append(problems, fmt.Sprintf("%s: %q differs from %q", p, ref, first))
		}
	}
	return problems
}

func wantCaddyPinProblem(t *testing.T, files map[string]string, substr string) {
	t.Helper()
	problems := caddyPinProblems(files)
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return
		}
	}
	t.Errorf("caddyPinProblems = %q, want one containing %q", problems, substr)
}

func pinnedCaddyFiles() map[string]string {
	files := map[string]string{}
	for _, p := range caddyPinFiles {
		if strings.HasSuffix(p, ".sh") {
			files[p] = "img=" + pinnedCaddyImage + " # comment\n"
		} else {
			files[p] = "FROM " + pinnedCaddyImage + " AS serve\n"
		}
	}
	return files
}

func TestCaddyPins_RepoPinned(t *testing.T) {
	root := repoRoot(t)
	files := map[string]string{}
	for _, p := range caddyPinFiles {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(b)
	}
	refs := caddyRefs(files)
	if len(refs) != 6 {
		t.Fatalf("found %d Caddy references %v, want exactly 6", len(refs), refs)
	}
	if p := caddyPinProblems(files); len(p) != 0 {
		t.Errorf("caddy pin problems: %q", p)
	}
	for path, ref := range refs {
		if ref != pinnedCaddyImage {
			t.Errorf("%s: %q, want %q", path, ref, pinnedCaddyImage)
		}
	}
}

func TestCaddyPins_AllPinnedPasses(t *testing.T) {
	files := pinnedCaddyFiles()
	if n := len(caddyRefs(files)); n != 6 {
		t.Fatalf("control: found %d references, want 6", n)
	}
	if p := caddyPinProblems(files); len(p) != 0 {
		t.Errorf("caddyPinProblems = %q, want none", p)
	}
}

func TestCaddyPins_FloatingTagFails(t *testing.T) {
	files := pinnedCaddyFiles()
	files["frontend/app/Dockerfile"] = "FROM caddy:2-alpine AS serve\n"
	wantCaddyPinProblem(t, files, "frontend/app/Dockerfile")
}

func TestCaddyPins_TagWithoutDigestFails(t *testing.T) {
	files := pinnedCaddyFiles()
	files["scripts/ci/caddy-headers.sh"] = "img=caddy:2.11.7-alpine\n"
	wantCaddyPinProblem(t, files, "scripts/ci/caddy-headers.sh")
}

func TestCaddyPins_DifferentDigestNamesFile(t *testing.T) {
	files := pinnedCaddyFiles()
	other := strings.Replace(pinnedCaddyImage, "e70b", "f70b", 1)
	files["frontend/landing/Dockerfile"] = "FROM " + other + " AS serve\n"
	wantCaddyPinProblem(t, files, "frontend/landing/Dockerfile: ")
	wantCaddyPinProblem(t, files, "differs")
}

func TestCaddyPins_CommentedFromIgnored(t *testing.T) {
	files := pinnedCaddyFiles()
	files["frontend/app/Dockerfile"] = "# FROM caddy:2-alpine\nFROM " + pinnedCaddyImage + " AS serve\n"
	if p := caddyPinProblems(files); len(p) != 0 {
		t.Errorf("caddyPinProblems = %q, want none", p)
	}
	files["frontend/app/Dockerfile"] = "# FROM " + pinnedCaddyImage + "\n"
	wantCaddyPinProblem(t, files, "no Caddy image reference")
}

var caddyFromAnyRE = regexp.MustCompile(`(?mi)^FROM\b[^\n]*\bcaddy\b`)

func TestCaddyPins_EveryCaddyDockerfileIsListed(t *testing.T) {
	root := repoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "frontend", "*", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range caddyPinFiles {
		listed[p] = true
	}
	found := 0
	for _, abs := range paths {
		rel, _ := filepath.Rel(root, abs)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		n := len(caddyFromAnyRE.FindAllString(string(b), -1))
		if n > 0 {
			found++
			if !listed[rel] {
				t.Errorf("%s has a Caddy FROM but is not in caddyPinFiles", rel)
			}
		}
		if n > 1 {
			t.Errorf("%s has %d Caddy FROM lines, the pin test reads the first only", rel, n)
		}
	}
	if found != 5 {
		t.Errorf("found %d Dockerfiles with a Caddy FROM, want 5", found)
	}
}

func TestCaddyPins_UnreadFromFormsAreFlagged(t *testing.T) {
	for _, from := range []string{
		"FROM --platform=linux/amd64 caddy:2-alpine AS serve\n",
		"FROM docker.io/library/caddy:2-alpine AS serve\n",
	} {
		if len(caddyFromAnyRE.FindAllString(from, -1)) != 1 {
			t.Errorf("discovery regexp misses %q", from)
		}
		files := pinnedCaddyFiles()
		files["frontend/app/Dockerfile"] = from
		wantCaddyPinProblem(t, files, "frontend/app/Dockerfile: no Caddy image reference")
	}
}
