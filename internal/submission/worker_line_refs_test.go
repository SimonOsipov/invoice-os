// worker_line_refs_test.go: pins every worker.go:N [anchor] reference in this package's
// comments against the text it cites, so a future edit to worker.go cannot silently strand
// one out from under it.
package submission

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// wlrRefPattern matches an anchored line reference: worker.go:NNN [anchor text].
var wlrRefPattern = regexp.MustCompile(`worker\.go:(\d+) \[([^\]]+)\]`)

// wlrBarePattern matches a worker.go:NNN citation with no anchor at all.
var wlrBarePattern = regexp.MustCompile(`worker\.go:\d+\b`)

// wlrMinMatches is the population floor: 17 anchored citations exist across the package
// today. Below this, every check below would be checking nothing.
const wlrMinMatches = 11

// wlrViolations resolves every worker.go:N [anchor] match in src against workerGoLines
// (1-indexed; index 0 is unused) and returns one description per match whose anchor is not
// a literal substring of the line it cites.
func wlrViolations(src string, workerGoLines []string) []string {
	var out []string
	for _, m := range wlrRefPattern.FindAllStringSubmatch(src, -1) {
		n, err := strconv.Atoi(m[1])
		anchor := m[2]
		if err != nil || n < 1 || n >= len(workerGoLines) {
			out = append(out, "worker.go:"+m[1]+" ["+anchor+"] -- line does not exist in worker.go")
			continue
		}
		if !strings.Contains(workerGoLines[n], anchor) {
			out = append(out, "worker.go:"+m[1]+" ["+anchor+"] -- anchor not found on that line")
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
	// 1-indexed so line N sits at workerGoLines[N]; index 0 is an unused sentinel.
	workerGoLines := append([]string{""}, strings.Split(string(workerGoBytes), "\n")...)

	entries, err := os.ReadDir(subDir)
	if err != nil {
		t.Fatalf("read %s: %v", subDir, err)
	}

	var matched int
	var bareSites []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(subDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(body)

		matched += len(wlrRefPattern.FindAllStringSubmatch(src, -1))
		for _, v := range wlrViolations(src, workerGoLines) {
			t.Errorf("%s: %s", name, v)
		}

		stripped := wlrRefPattern.ReplaceAllString(src, "")
		for _, bare := range wlrBarePattern.FindAllString(stripped, -1) {
			bareSites = append(bareSites, name+": "+bare)
		}
	}

	if matched < wlrMinMatches {
		t.Fatalf("resolved %d worker.go:N [anchor] references, want >= %d -- the directory "+
			"walk is broken, so every check above is vacuous", matched, wlrMinMatches)
	}
	if len(bareSites) != 0 {
		t.Errorf("found %d worker.go:N citation(s) without a bracketed anchor: %v -- every "+
			"citation must use the worker.go:N [anchor] form", len(bareSites), bareSites)
	}

	// Control: prove the checker itself can fail, not just clear. Built via concatenation
	// so this fixture text does not match wlrRefPattern when this file is itself scanned
	// above -- a self-matching source scan is how AUDIT-02's L04 corpus leaked before.
	t.Run("control_needle", func(t *testing.T) {
		fixtureLines := []string{"", "the correct anchor lives here", "something else entirely"}
		correct := "worker.go:" + "1" + " [correct anchor]"
		wrong := "worker.go:" + "2" + " [correct anchor]"
		src := correct + "\n" + wrong

		got := wlrViolations(src, fixtureLines)
		if len(got) != 1 {
			t.Fatalf("fixture (one right pair, one wrong pair) produced %d violations, want "+
				"exactly 1 -- a checker that only ever clears catches nothing: %v", len(got), got)
		}
	})
}
