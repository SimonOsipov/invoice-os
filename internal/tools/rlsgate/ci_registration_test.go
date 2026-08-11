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
	"strings"
	"testing"
)

// rlsgate fails a step whose tests ran and skipped. It cannot see a suite that
// no step runs at all, nor one a -run filter excludes: never-run is not SKIP.
// These two guards cover that blind spot. APPR-02 lost 64 DB-backed tests to the
// first mode, M4-03 about 40, DEMO-02 the whole seed suite; M3-02 hit the second.

var ciSkipCallRE = regexp.MustCompile(`t\.Skipf?\(`)

// ciGateStepRE matches a ci.yml step invoking this tool's wrapper. The go job's
// bare `go test ./...` deliberately does not match: it carries no DSNs, which is
// the environment where these tests skip themselves.
var ciGateStepRE = regexp.MustCompile(`(?m)^[ \t]*run:[ \t]*(.*rls-test-gate\.sh.*?)[ \t]*$`)

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
	steps := ciGateSteps(ciYAML(t, root))

	var unregistered []string
	for _, pkg := range ciGatedPackages(t, root) {
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

	for stepPkg, filters := range ciGateSteps(ciYAML(t, root)) {
		if slices.Contains(filters, "") {
			continue // an unfiltered step already runs the whole package
		}
		funcs := ciPackageFuncs(t, filepath.Join(root, stepPkg))

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
