// ci_test.go: the static guards over the step that publishes this suite's number, and over the
// gated step that runs it. No database.
package endtoend

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	// eeReportStepKey is how eeReportStep finds its chunk. Not the -run value: that is what
	// TestEndToEnd_TheReportStepsRunFilterNamesARealTest tests.
	eeReportStepKey = "./internal/extraction/endtoend"

	// eeGatedStepPkg is the glob the gated step must keep, so this subpackage stays covered.
	eeGatedStepPkg = "./internal/extraction/..."

	// Measured at 1793080b: 4 DB-backed renderers, 5 pure ones. The pure floor sits one below
	// the measurement so a deliberate consolidation does not red.
	eeDBRendererFloor   = 4
	eePureRendererFloor = 4

	// eeRenderNeedle / eeDBNeedle partition the renderers. A body holding both self-skips
	// without the DSNs (harness_db_test.go), so the -run filter must select it.
	eeRenderNeedle = "eeRenderReport("
	eeDBNeedle     = "eeRequire("
)

// eeGatedStepRE is the one ci.yml run: line that gates this package's glob. Flag-tolerant on
// purpose: -p 1 is pinned by TestEndToEnd_TheGatedStepCarriesOnePackageAtATime alone, so a
// literal needle here would red with a message about reading the wrong file. Measured: exactly
// one of ci.yml's 22 rls-test-gate.sh lines matches.
var eeGatedStepRE = regexp.MustCompile(`(?m)^ *run: .*rls-test-gate\.sh .*\./internal/extraction(/\.\.\.)?$`)

// eeGateLineRE is every gated run: line, for the discrimination needle in AC-7.
var eeGateLineRE = regexp.MustCompile(`(?m)^ *run: .*rls-test-gate\.sh.*$`)

// eePFlagRE matches -p 1 as a whole argument, not as a substring of another flag.
var eePFlagRE = regexp.MustCompile(`(^|\s)-p 1(\s|$)`)

// eeRunFilterRE lifts the -run value out of a step. Restated from
// internal/extraction/accuracy_adversarial_test.go:30 -- another package.
var eeRunFilterRE = regexp.MustCompile(`-run '([^']+)'`)

// eeJobHeaderRE is a top-level `jobs:` entry header in ci.yml.
var eeJobHeaderRE = regexp.MustCompile(`(?m)^  [A-Za-z][\w-]*:$`)

// eeReportStep is the ci.yml step that publishes this suite's report, cut at its own end.
// Mirrors cwCIStep + cwStepBody (internal/extraction/corpus_wired_db_test.go).
func eeReportStep(t *testing.T, yaml string) string {
	t.Helper()

	for _, chunk := range strings.Split(yaml, "\n      - ") {
		if strings.Contains(chunk, eeReportStepKey) {
			return eeStepBody(chunk)
		}
	}
	t.Fatalf("no %s step names %s; the gated step that runs this package discards a passing test's buffered output (internal/tools/rlsgate/rlsgate.go, case \"pass\"), so without a step of its own this suite's number never reaches CI", wildCIFile, eeReportStepKey)
	return ""
}

// eeStepBody cuts a step chunk at the end of the step. The chunk of a job's LAST step runs on
// into the next job, and every needle asserted against it could then be met by an unrelated job.
func eeStepBody(chunk string) string {
	lines := strings.Split(chunk, "\n")
	for i, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) < 8 {
			return strings.Join(lines[:i+1], "\n")
		}
	}
	return chunk
}

// eeTestBodies maps this package's test function names to their source bodies.
func eeTestBodies(t *testing.T) map[string]string {
	t.Helper()

	// "." not a path: a test binary's CWD is its own package directory, so a sibling
	// /ralph worktree cannot be walked.
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	if len(names) < eeMinTestFiles {
		t.Fatalf("read %d test file(s) in internal/extraction/endtoend, want at least %d; this scan is reading the wrong directory", len(names), eeMinTestFiles)
	}

	topLevel := regexp.MustCompile(`(?m)^func `)
	testDecl := regexp.MustCompile(`^func (Test\w+)\(`)

	bodies := map[string]string{}
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		starts := topLevel.FindAllStringIndex(src, -1)
		for i, s := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			body := src[s[0]:end]
			if m := testDecl.FindStringSubmatch(body); m != nil {
				bodies[m[1]] = body
			}
		}
	}
	return bodies
}

// eeRenderers partitions the package's test functions into the DB-backed renderers the -run
// filter must select and the pure ones it must not.
func eeRenderers(t *testing.T) (db, pure []string) {
	t.Helper()

	for name, body := range eeTestBodies(t) {
		if !strings.Contains(body, eeRenderNeedle) {
			continue
		}
		if strings.Contains(body, eeDBNeedle) {
			db = append(db, name)
			continue
		}
		pure = append(pure, name)
	}
	return db, pure
}

// AC-1, AC-5. rls-test-gate.sh pipes `go test -json` into rlsgate, which deletes a passing
// test's buffered output, so this suite's number reaches CI only from a step of its own.
func TestEndToEnd_CIPrintsTheReport(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)

	// Control needle: a scan reading the wrong file finds no step either. Bare glob, no flags.
	if !strings.Contains(yaml, eeGatedStepPkg) {
		t.Fatalf("%s never names %s; this scan is reading the wrong file and would report a missing step that is there", wildCIFile, eeGatedStepPkg)
	}

	step := eeReportStep(t, yaml)
	for _, want := range []struct{ needle, why string }{
		{"go test", "the step must actually run the suite"},
		{"-count=1", "the test cache would otherwise replay an older run's report"},
		{"-v", "the report is a t.Log; without -v it is never written"},
		{eeReportStepKey, "the step must name this package"},
		{"set -o pipefail", "ci.yml has no defaults.run.shell, so run: is bash -e and a `| tee` would mask a failed go test"},
		// The Go constant, never a copy of its text: a rename on either side alone reds here.
		{eeReportMarker, "the step must grep for the report marker, or a broken -run filter prints nothing and still passes"},
	} {
		if !strings.Contains(step, want.needle) {
			t.Errorf("the end-to-end report step does not carry %q: %s", want.needle, want.why)
		}
	}
	if strings.Contains(step, "rls-test-gate.sh") {
		t.Errorf("the end-to-end report step runs through rls-test-gate.sh, which deletes a passing test's buffered output; the report would never print, and no other step would notice")
	}
}

// AC-5. eeReportStep finds the step by substring, so it cannot tell a step that RUNS from one
// gated off, mistyped, or whose grep was neutered. Mirrors
// TestRLS_WiredPathTheReportStepActuallyRuns, whose comment records those survivors.
func TestEndToEnd_TheReportStepActuallyRuns(t *testing.T) {
	step := eeReportStep(t, wildReadFile(t, wildCIFile))

	if !regexp.MustCompile(`(?m)^\s+run: \|`).MatchString(step) {
		t.Errorf("the end-to-end report step has no `run: |` block; a mistyped key makes it a no-op step that still carries every string the other specs grep for")
	}
	if m := regexp.MustCompile(`(?m)^\s+if:.*$`).FindString(step); m != "" {
		t.Errorf("the end-to-end report step is conditional (%q); a step gated off never prints the report, and the substring scans cannot see the difference", strings.TrimSpace(m))
	}
	if strings.Contains(step, "continue-on-error") {
		t.Errorf("the end-to-end report step is continue-on-error; a failed grep would no longer fail the job")
	}

	grep := regexp.MustCompile(`grep -q '` + regexp.QuoteMeta(eeReportMarker) + `' (\S+)\s*(\S*)`).FindStringSubmatch(step)
	if grep == nil {
		t.Fatalf("the end-to-end report step has no `grep -q '%s' <file>`; without it the step passes on output that says nothing", eeReportMarker)
	}
	if grep[2] != "" {
		t.Errorf("the grep is followed by %q; `|| true` and friends turn the only reader-side assertion into a no-op", grep[2])
	}
	tee := regexp.MustCompile(`\| tee (\S+)`).FindStringSubmatch(step)
	if tee == nil {
		t.Fatalf("the end-to-end report step does not tee the go test output; there is nothing for the grep to read")
	}
	if tee[1] != grep[1] {
		t.Errorf("the step tees into %s and greps %s; the grep would read another job's file, or none", tee[1], grep[1])
	}
}

// AC-2. The gated step carries no -run filter, so this subpackage is scheduled the moment it
// exists. TestEndToEnd_TheGatedStepNeedsOnePackageAtATime (deps_test.go) proves the glob
// resolves to this package; this spec proves ci.yml still uses the glob.
func TestEndToEnd_TheGatedStepCoversThisPackage(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)

	lines := eeGatedStepRE.FindAllString(yaml, -1)
	if len(lines) != 1 {
		t.Fatalf("%s holds %d gated run: line(s) naming ./internal/extraction, want exactly 1 (%q); this scan is reading the wrong file or the step was split", wildCIFile, len(lines), lines)
	}
	line := lines[0]

	if !strings.Contains(line, eeGatedStepPkg) {
		t.Errorf("the gated extraction step runs %q, which does not name %s; this subpackage would stop being gated while the step still reads correct", strings.TrimSpace(line), eeGatedStepPkg)
	}
	if strings.Contains(line, "-run ") {
		t.Errorf("the gated extraction step carries a -run filter (%q); a filter drops this subpackage's tests while the package pattern still reads correct", strings.TrimSpace(line))
	}
}

// AC-3. A -run filter naming no test runs nothing and prints nothing; a filter widened to
// everything doubles the gated step. The partition oracle is eeRequire, not the file name and
// not the TestRLS_ prefix -- the prefix is the very thing the filter selects on.
func TestEndToEnd_TheReportStepsRunFilterNamesARealTest(t *testing.T) {
	step := eeReportStep(t, wildReadFile(t, wildCIFile))

	m := eeRunFilterRE.FindStringSubmatch(step)
	if m == nil {
		t.Fatalf("the end-to-end report step carries no -run '<pattern>'; it would run the whole package and the report would be buried")
	}
	filter, err := regexp.Compile(m[1])
	if err != nil {
		t.Fatalf("the report step's -run %q does not compile: %v", m[1], err)
	}

	db, pure := eeRenderers(t)
	if len(db) < eeDBRendererFloor {
		t.Fatalf("found %d DB-backed renderer(s) %v, want at least %d; a scan that reads nothing satisfies the negative half below", len(db), db, eeDBRendererFloor)
	}
	if len(pure) < eePureRendererFloor {
		t.Fatalf("found %d pure renderer(s) %v, want at least %d; a scan that reads nothing satisfies the positive half below", len(pure), pure, eePureRendererFloor)
	}

	for _, name := range db {
		if !filter.MatchString(name) {
			t.Errorf("-run %q does not select %s, a DB-backed renderer; the published number silently drops what it measures", m[1], name)
		}
	}
	for _, name := range pure {
		if filter.MatchString(name) {
			t.Errorf("-run %q selects %s, which renders without a database; the filter was widened and the report step now duplicates the gated run", m[1], name)
		}
	}
	if filter.MatchString(t.Name()) {
		t.Errorf("-run %q selects this spec; the filter must name renderers, not the scan that reads it", m[1])
	}
}

// AC-4. The report step must sit in the queue job, which carries the DATABASE_* env. Without
// them every TestRLS_* takes eeRequire's skip, the report never renders, and the grep fails for
// the wrong reason.
func TestEndToEnd_TheReportStepRunsInTheQueueJob(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)
	step := eeReportStep(t, yaml)

	loc := eeGatedStepRE.FindStringIndex(yaml)
	report := strings.Index(yaml, step)
	if loc == nil || report < 0 {
		t.Fatalf("could not locate both steps in %s (gated %v, report %d); this scan is reading the wrong file", wildCIFile, loc, report)
	}
	gated := loc[0]
	if report <= gated {
		t.Fatalf("the report step (offset %d) sits above the gated extraction run (offset %d); that is not the shipped shape, and every span read below would be backwards", report, gated)
	}

	if between := eeJobHeaderRE.FindString(yaml[gated:report]); between != "" {
		t.Errorf("a new job (%q) starts between the gated extraction run and the report step; the report step would not inherit the DATABASE_* env and every TestRLS_* would self-skip", strings.TrimSpace(between))
	}

	// Naming the job, not just the gap: this is the only assertion that reds if BOTH steps
	// move together into a job with no Postgres.
	queue := regexp.MustCompile(`(?m)^  queue:$`).FindStringIndex(yaml)
	if queue == nil {
		t.Fatalf("%s has no `queue:` job; this scan is reading the wrong file", wildCIFile)
	}
	next := eeJobHeaderRE.FindStringIndex(yaml[queue[1]:])
	if next == nil {
		t.Fatalf("no job follows `queue:` in %s; the containment check below would pass on any offset", wildCIFile)
	}
	if end := queue[1] + next[0]; report < queue[0] || report > end {
		t.Errorf("the report step (offset %d) is outside the queue job (%d..%d); only that job carries DATABASE_URL and DATABASE_SUPERUSER_URL", report, queue[0], end)
	}
}

// AC-6. docs/extraction-corpus.md must route to the queue job, or a doc-only edit ships against
// a suite that never ran.
func TestEndToEnd_TheChangesFilterRoutesThisSuite(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)

	start := regexp.MustCompile(`(?m)^            go:$`).FindStringIndex(yaml)
	if start == nil {
		t.Fatalf("%s's changes job has no `go:` filter; this scan is reading the wrong file", wildCIFile)
	}
	rest := yaml[start[1]:]
	block := rest
	if next := regexp.MustCompile(`(?m)^            \w+:$`).FindStringIndex(rest); next != nil {
		block = rest[:next[0]]
	}

	// Control needle: a mis-cut or truncated block reads clean on the two assertions below.
	if !strings.Contains(block, "'go.sum'") {
		t.Fatalf("the `go:` filter block does not name 'go.sum'; the block was cut wrong and the assertions below would read an empty string")
	}

	// docs/extraction-corpus.md is routed by 'docs/**'. The filter holds no such filename, so
	// asserting one would red on a correct workflow.
	for _, path := range []string{"'internal/**'", "'docs/**'"} {
		if !strings.Contains(block, path) {
			t.Errorf("the `go:` changes filter does not name %s; a change under it would skip the queue job that runs this suite", path)
		}
	}
}

// AC-7. The glob names two DB-backed packages against one Postgres, and go test runs their
// binaries concurrently by default.
func TestEndToEnd_TheGatedStepCarriesOnePackageAtATime(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)

	line := eeGatedStepRE.FindString(yaml)
	if line == "" {
		t.Fatalf("%s holds no gated run: line naming ./internal/extraction; this scan is reading the wrong file", wildCIFile)
	}
	if !eePFlagRE.MatchString(line) {
		t.Errorf("the gated extraction step (%q) carries no -p 1; the glob names two DB-backed packages sharing one Postgres, and parallel package binaries race into bogus SASL authentication failures", strings.TrimSpace(line))
	}

	// Discrimination needle: this scan must read flags, not match every gated step. Measured
	// at 1793080b: 19 of ci.yml's 22 rls-test-gate.sh lines carry no -p 1.
	var others int
	for _, other := range eeGateLineRE.FindAllString(yaml, -1) {
		if other != line && !eePFlagRE.MatchString(other) {
			others++
		}
	}
	if others == 0 {
		t.Errorf("every rls-test-gate.sh line in %s carries -p 1; this scan would pass on any workflow and pins nothing", wildCIFile)
	}
}
