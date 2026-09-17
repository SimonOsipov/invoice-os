package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// rlsgate fails a step whose tests ran and skipped. It cannot see a suite that
// no step runs at all, nor one a -run filter excludes: never-run is not SKIP.
// These two guards cover that blind spot. APPR-02 lost 64 DB-backed tests to the
// first mode, M4-03 about 40, DEMO-02 the whole seed suite; M3-02 hit the second.

var ciSkipCallRE = regexp.MustCompile(`t\.Skipf?\(`)

// ciGateScript is the wrapper a gate step invokes. It doubles as the CONTROL
// NEEDLE for ciAssertScansFound: a scan whose regexp stopped matching is
// indistinguishable from a clean repo unless something that must be found is
// looked for alongside it.
const ciGateScript = "rls-test-gate.sh"

// ciGateStepRE matches a ci.yml step invoking this tool's wrapper. The go job's
// bare `go test ./...` deliberately does not match: it carries no DSNs, which is
// the environment where these tests skip themselves.
var ciGateStepRE = regexp.MustCompile(`(?m)^[ \t]*run:[ \t]*(.*` + regexp.QuoteMeta(ciGateScript) + `.*?)[ \t]*$`)

var ciPkgArgRE = regexp.MustCompile(`\./(internal/[a-zA-Z0-9_/-]+?)(?:/\.\.\.)?(?:\s|$)`)

var ciRunFlagRE = regexp.MustCompile(`-run[= ]+'([^']*)'|-run[= ]+(\S+)`)

func ciRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func ciYAML(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	return string(raw)
}

// ciReaches mirrors `go test -run` unanchored-regexp semantics; an empty filter
// reaches everything. TestMain never accepts -run.
func ciReaches(filters []string, name string) bool {
	if name == "TestMain" {
		return true
	}
	for _, f := range filters {
		if f == "" {
			return true
		}
		if re, err := regexp.Compile(f); err == nil && re.MatchString(name) {
			return true
		}
	}
	return false
}

// ciGateSteps maps each package argument appearing in a gate step to that step's
// -run filters.
func ciGateSteps(yaml string) map[string][]string {
	steps := map[string][]string{}
	for _, m := range ciGateStepRE.FindAllStringSubmatch(yaml, -1) {
		cmd := m[1]
		pm := ciPkgArgRE.FindStringSubmatch(cmd + " ")
		if pm == nil {
			continue
		}
		filter := ""
		if fm := ciRunFlagRE.FindStringSubmatch(cmd); fm != nil {
			if fm[1] != "" {
				filter = fm[1]
			} else {
				filter = fm[2]
			}
		}
		steps[pm[1]] = append(steps[pm[1]], filter)
	}
	return steps
}

// ciGatedPackages returns every internal/ package whose tests skip on a missing DSN.
func ciGatedPackages(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ciSkipCallRE.Match(src) && strings.Contains(string(src), "DATABASE_") {
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			seen[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ciAssertScansFound fails when the SCANS behind these guards stopped working.
//
// Both guards conclude "nothing is wrong" from an empty scan result, so a broken
// scanner reads exactly like a clean repo — the defect these guards exist to
// catch, turned on the guards themselves. Measured by mutation before this
// existed: renaming the string ciGateStepRE looks for left
// TestCIRunFiltersReachEveryDBGatedTest green, and breaking the `DATABASE_` walk
// left BOTH green. TestEveryDBGatedPackageRunsInACIGateStep survived the first
// mutation only by accident — with zero steps found, every package reads as
// unregistered, so it failed loudly for the wrong reason.
//
// Two defences, the pair this repo already uses elsewhere (filename_removed_test.go,
// envPosture.test.ts): a control needle that must be found, and a floor on the
// population each scan returns.
func ciAssertScansFound(t *testing.T, yaml string, steps map[string][]string, gated []string) {
	t.Helper()

	if !strings.Contains(yaml, ciGateScript) {
		t.Fatalf("ci.yml no longer mentions %s -- either the DB suites moved to a different "+
			"runner, or the script was renamed; either way these guards can no longer see them",
			ciGateScript)
	}
	if len(steps) == 0 {
		t.Fatalf("ci.yml still mentions %s but ciGateStepRE matched no step -- the pattern is "+
			"broken (a reworded or multi-line `run:` does this), so every check below would "+
			"pass having examined nothing", ciGateScript)
	}
	if len(gated) == 0 {
		t.Fatal("the internal/ walk found no DB-gated test package -- either every DB suite is " +
			"gone, or ciSkipCallRE / the DATABASE_ needle stopped matching; either way the " +
			"checks below would pass having examined nothing")
	}
}

type ciFunc struct {
	name string
	src  string
	test bool
}

func ciPackageFuncs(t *testing.T, dir string) []ciFunc {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	var out []ciFunc
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			body := string(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
			out = append(out, ciFunc{
				name: fd.Name.Name,
				src:  body,
				test: fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test"),
			})
		}
	}
	return out
}

// TestEveryDBGatedPackageRunsInACIGateStep is the APPR-02 guard: adding a package
// of DB-backed tests without adding its ci.yml step is a build break, not a pass.
func TestEveryDBGatedPackageRunsInACIGateStep(t *testing.T) {
	root := ciRepoRoot(t)
	yaml := ciYAML(t, root)
	steps := ciGateSteps(yaml)
	gated := ciGatedPackages(t, root)
	ciAssertScansFound(t, yaml, steps, gated)

	var unregistered []string
	for _, pkg := range gated {
		covered := false
		for stepPkg := range steps {
			if pkg == stepPkg || strings.HasPrefix(pkg, stepPkg+"/") {
				covered = true
				break
			}
		}
		if !covered {
			unregistered = append(unregistered, pkg)
		}
	}
	if len(unregistered) != 0 {
		t.Errorf("these packages hold DB-gated tests that no ci.yml rls-test-gate.sh step runs, "+
			"so the tests self-skip and the job still passes: %v", unregistered)
	}
}

// TestCIRunFiltersReachEveryDBGatedTest is the M3-02 guard. Non-gated tests are
// exempt: the go job runs those through go test ./... without DSNs.
func TestCIRunFiltersReachEveryDBGatedTest(t *testing.T) {
	root := ciRepoRoot(t)
	yaml := ciYAML(t, root)
	steps := ciGateSteps(yaml)
	ciAssertScansFound(t, yaml, steps, ciGatedPackages(t, root))

	for stepPkg, filters := range steps {
		if slices.Contains(filters, "") {
			continue // an unfiltered step already runs the whole package
		}
		funcs := ciPackageFuncs(t, filepath.Join(root, stepPkg))
		// Third floor: a package a gate step names must hold tests to reach.
		if len(funcs) == 0 {
			t.Fatalf("ci.yml runs a filtered gate step on ./%s/... but the parse found no test "+
				"function there -- the glob or the parse is broken, so the filter check below "+
				"would pass having examined nothing", stepPkg)
		}

		gates := map[string]bool{}
		for _, fn := range funcs {
			if ciSkipCallRE.MatchString(fn.src) && strings.Contains(fn.src, "DATABASE_") {
				gates[fn.name] = true
			}
		}

		var stranded []string
		for _, fn := range funcs {
			if !fn.test || ciReaches(filters, fn.name) {
				continue
			}
			for g := range gates {
				if fn.name == g || strings.Contains(fn.src, g+"(") {
					stranded = append(stranded, fn.name)
					break
				}
			}
		}
		if len(stranded) != 0 {
			sort.Strings(stranded)
			t.Errorf("ci.yml -run filters %v on ./%s/... never run these DB-gated tests: %v",
				filters, stepPkg, stranded)
		}
	}
}

// ciRunLineRE matches a non-comment go test or gate-script line passing -run, in a
// `run:` step or a `run: |` block. A step `name:` may mention -run in prose.
var ciRunLineRE = regexp.MustCompile(`(?m)^[ \t]*(?:[^#\s].*)?(?:go test|` + regexp.QuoteMeta(ciGateScript) + `).*\s-run[= ].*$`)

type ciRunFilter struct {
	line      string
	pkg       string
	recursive bool
	filter    string
}

// ciRunFilters returns every -run filter in yaml. A -run line whose package or
// filter did not parse lands in unparsed, so a parser gap fails instead of passing.
func ciRunFilters(yaml string) (filters []ciRunFilter, unparsed []string) {
	for _, line := range ciRunLineRE.FindAllString(yaml, -1) {
		line = strings.TrimSpace(line)
		pm := ciPkgArgRE.FindStringSubmatch(line + " ")
		fm := ciRunFlagRE.FindStringSubmatch(line)
		if pm == nil || fm == nil {
			unparsed = append(unparsed, line)
			continue
		}
		filter := fm[1]
		if filter == "" {
			filter = fm[2]
		}
		filters = append(filters, ciRunFilter{
			line:      line,
			pkg:       strings.TrimSuffix(pm[1], "/"),
			recursive: strings.Contains(pm[0], "/..."),
			filter:    filter,
		})
	}
	return filters, unparsed
}

// ciSplitTopLevel splits s on sep outside (), [] and escapes, as go test splits -run.
func ciSplitTopLevel(s string, sep byte) []string {
	var out []string
	depth, bracket, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case bracket:
			if c == ']' {
				bracket = false
			}
		case c == '[':
			bracket = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ciDeadRunFilters returns one message per -run alternative that matches no test.
// Each top-level `|` branch counts: a renamed test inside an alternation is the
// same silent no-op as a whole filter matching nothing.
func ciDeadRunFilters(filters []ciRunFilter, testsIn func(pkg string, recursive bool) []string) []string {
	var dead []string
	for _, f := range filters {
		names := testsIn(f.pkg, f.recursive)
		top := ciSplitTopLevel(f.filter, '/')[0] // subtest names are invisible statically
		for _, branch := range ciSplitTopLevel(top, '|') {
			re, err := regexp.Compile(branch)
			if err != nil {
				dead = append(dead, "invalid regexp "+strconv.Quote(branch)+" in: "+f.line)
				continue
			}
			if !slices.ContainsFunc(names, re.MatchString) {
				dead = append(dead, strconv.Quote(branch)+" matches no test in ./"+f.pkg+
					" ("+strconv.Itoa(len(names))+" tests) in: "+f.line)
			}
		}
	}
	return dead
}

// ciTestNames lists the Test functions `go test` runs for pkg, plus subpackages when recursive.
func ciTestNames(t *testing.T, root, pkg string, recursive bool) []string {
	t.Helper()
	top := filepath.Join(root, pkg)
	dirs := []string{top}
	if recursive {
		dirs = nil
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			if n := d.Name(); path != top && (n == "testdata" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_")) {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk ./%s: %v", pkg, err)
		}
	}
	var names []string
	for _, dir := range dirs {
		for _, fn := range ciPackageFuncs(t, dir) {
			if fn.test && fn.name != "TestMain" {
				names = append(names, fn.name)
			}
		}
	}
	return names
}

// TestCIRunFilterAlternativesEachMatchATest is the reverse of the guards above:
// a -run filter matching nothing prints "no tests to run" and exits 0.
func TestCIRunFilterAlternativesEachMatchATest(t *testing.T) {
	root := ciRepoRoot(t)
	filters, unparsed := ciRunFilters(ciYAML(t, root))

	if len(unparsed) != 0 {
		t.Fatalf("these ci.yml lines pass -run but ciRunFilters could not read their package or "+
			"filter, so they would go unchecked: %q", unparsed)
	}
	// Control needle: the go job's accuracy report filter. Missing means the scan examined nothing.
	const controlNeedle = "TestTier1Accuracy"
	if !slices.ContainsFunc(filters, func(f ciRunFilter) bool { return f.filter == controlNeedle }) {
		t.Fatalf("the scan found %d -run filters but not the control %q -- ciRunLineRE or "+
			"ciRunFlagRE stopped matching, or that step lost its filter", len(filters), controlNeedle)
	}
	if len(filters) < 5 {
		t.Fatalf("the scan found only %d -run filters in ci.yml, want at least 5", len(filters))
	}

	dead := ciDeadRunFilters(filters, func(pkg string, recursive bool) []string {
		return ciTestNames(t, root, pkg, recursive)
	})
	if len(dead) != 0 {
		t.Errorf("ci.yml -run filters that run nothing (go test exits 0 on them):\n  %s",
			strings.Join(dead, "\n  "))
	}
}

func TestCIDeadRunFilters_FlagsABogusFilterAndABogusBranch(t *testing.T) {
	yaml := `
      - name: gate + CI -run filter coverage
        run: scripts/ci/rls-test-gate.sh -count=1 -run 'TestReal|TestRenamedAway' ./internal/a/...
      - name: report
        run: |
          # a -run in a comment is not a filter ./internal/a/...
          go test -count=1 -v -run 'TestNoSuchThing' ./internal/b/ | tee out.txt
          go test -run=TestReal/sub ./internal/a/
          go test -run 'Test(Real|Gone)' ./internal/a/
`
	filters, unparsed := ciRunFilters(yaml)
	if len(unparsed) != 0 || len(filters) != 4 {
		t.Fatalf("want 4 parsed filters and none unparsed, got %+v, unparsed %q", filters, unparsed)
	}
	if !filters[0].recursive || filters[1].recursive || filters[1].pkg != "internal/b" {
		t.Fatalf("package parse wrong: %+v", filters)
	}

	tests := map[string][]string{"internal/a": {"TestReal"}, "internal/b": {"TestOther"}}
	dead := ciDeadRunFilters(filters, func(pkg string, _ bool) []string { return tests[pkg] })
	if len(dead) != 2 ||
		!strings.HasPrefix(dead[0], `"TestRenamedAway" matches no test`) ||
		!strings.HasPrefix(dead[1], `"TestNoSuchThing" matches no test`) {
		t.Fatalf("want exactly TestRenamedAway and TestNoSuchThing flagged, got:\n%s", strings.Join(dead, "\n"))
	}
}

func TestCIRunFilters_ReportsAnUnreadableRunLine(t *testing.T) {
	_, unparsed := ciRunFilters("        run: go test -run TestX ./cmd/gateway/\n")
	if len(unparsed) != 1 {
		t.Fatalf("want the cmd/ line reported unparsed, got %q", unparsed)
	}
}
