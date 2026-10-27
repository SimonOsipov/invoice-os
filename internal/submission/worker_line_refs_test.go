// worker_line_refs_test.go: pins every worker.go [anchor] citation in this package's
// comments against worker.go, so an edit to worker.go cannot strand one, and bans line numbers.
package submission

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// wlrRefPattern matches an anchored citation: worker.go [anchor text].
var wlrRefPattern = regexp.MustCompile(`worker\.go \[([^\]]+)\]`)

// wlrBarePattern matches a worker.go:NNN line-number citation; citegate bans them.
var wlrBarePattern = regexp.MustCompile(`worker\.go:\d+\b`)

// wlrMinMatches is the population floor: 17 anchored citations exist across the package
// today. Below this, every check below would be checking nothing.
const wlrMinMatches = 11

// wlrViolations returns one description per worker.go [anchor] match in src whose anchor is
// not a literal substring of workerGo.
func wlrViolations(src, workerGo string) []string {
	var out []string
	for _, m := range wlrRefPattern.FindAllStringSubmatch(src, -1) {
		if !strings.Contains(workerGo, m[1]) {
			out = append(out, "worker.go ["+m[1]+"] -- anchor not found in worker.go")
		}
	}
	return out
}

func TestWorkerLineReferencesResolve(t *testing.T) {
	subDir := filepath.Join(vaRepoRoot(t), "internal", "submission")

	workerGoBytes, err := os.ReadFile(filepath.Join(subDir, "worker.go"))
	if err != nil {
		t.Fatalf("read worker.go: %v", err)
	}
	workerGo := string(workerGoBytes)

	entries, err := os.ReadDir(subDir)
	if err != nil {
		t.Fatalf("read %s: %v", subDir, err)
	}

	var matched int
	var bareSites []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "worker_line_refs_test.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(subDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(body)

		matched += len(wlrRefPattern.FindAllStringSubmatch(src, -1))
		for _, v := range wlrViolations(src, workerGo) {
			t.Errorf("%s: %s", name, v)
		}

		stripped := wlrRefPattern.ReplaceAllString(src, "")
		for _, bare := range wlrBarePattern.FindAllString(stripped, -1) {
			bareSites = append(bareSites, name+": "+bare)
		}
	}

	if matched < wlrMinMatches {
		t.Fatalf("resolved %d worker.go [anchor] citations, want >= %d -- the directory "+
			"walk is broken, so every check above is vacuous", matched, wlrMinMatches)
	}
	if len(bareSites) != 0 {
		t.Errorf("found %d worker.go:N line-number citation(s): %v -- "+
			"cite the anchor as worker.go [anchor], with no line number", len(bareSites), bareSites)
	}

	// Control: prove the checker itself can fail, not just clear. Built via concatenation
	// so this fixture text does not match wlrRefPattern when this file is itself scanned
	// above -- a self-matching source scan is how AUDIT-02's L04 corpus leaked before.
	t.Run("control_needle", func(t *testing.T) {
		fixture := "the correct anchor lives here"
		src := "worker.go " + "[correct anchor]\nworker.go " + "[wrong anchor]"

		got := wlrViolations(src, fixture)
		if len(got) != 1 {
			t.Fatalf("fixture (one right anchor, one wrong) produced %d violations, want "+
				"exactly 1 -- a checker that only ever clears catches nothing: %v", len(got), got)
		}
	})
}
