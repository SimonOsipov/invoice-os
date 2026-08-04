package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// targetPackageArg is the only package this file's coverage check governs —
// a repo-wide check would false-positive on deliberately split suites elsewhere.
const targetPackageArg = "./internal/platform/db/..."

// unreachable mirrors `go test -run`'s unanchored-regexp semantics (empty
// filter reaches everything); TestMain never accepts -run so it's skipped.
func unreachable(filters, names []string) []string {
	var out []string
	for _, name := range names {
		if name == "TestMain" {
			continue
		}
		reached := false
		for _, f := range filters {
			if f == "" {
				reached = true
				break
			}
			re, err := regexp.Compile(f)
			if err != nil {
				continue
			}
			if re.MatchString(name) {
				reached = true
				break
			}
		}
		if !reached {
			out = append(out, name)
		}
	}
	return out
}

// runLineRE matches an inline `run: <command>` step line (not a `run: |` block's
// continuation lines, which never repeat the package arg for this job's steps).
var runLineRE = regexp.MustCompile(`(?m)^[ \t]*run:[ \t]*(.+?)[ \t]*$`)

// runFlagRE captures a `-run` value, quoted (alternation) or bare.
var runFlagRE = regexp.MustCompile(`-run[= ]+'([^']*)'|-run[= ]+(\S+)`)

// discoverCIRunFilters returns the -run value of every `run:` line targeting
// targetPackageArg. A matching line with no -run flag contributes "" (unfiltered).
func discoverCIRunFilters(ciYAML string) []string {
	var filters []string
	for _, m := range runLineRE.FindAllStringSubmatch(ciYAML, -1) {
		cmd := m[1]
		if !strings.Contains(cmd, targetPackageArg) {
			continue
		}
		fm := runFlagRE.FindStringSubmatch(cmd)
		switch {
		case fm == nil:
			filters = append(filters, "")
		case fm[1] != "":
			filters = append(filters, fm[1])
		default:
			filters = append(filters, fm[2])
		}
	}
	return filters
}

// repoCIYAML reads .github/workflows/ci.yml from the repo root, located via git
// since `go test` sets cwd to this package's directory (TestDocument_CIRLSJobRunsThisPackage).
func repoCIYAML(t *testing.T) string {
	t.Helper()
	root, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	path := filepath.Join(strings.TrimSpace(string(root)), ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// enumerateTestFuncs AST-parses every *_test.go in this directory for top-level
// `func Test*` declarations — no hand-maintained name list.
func enumerateTestFuncs(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no *_test.go files found — enumeration is broken")
	}

	fset := token.NewFileSet()
	var names []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") {
				names = append(names, fn.Name.Name)
			}
		}
	}
	return names
}

// preFixOrphans transcribes the 14 non-TestSeed orphans from BUG-02-02's task
// description (verified unreachable under the four pre-BUG-02 filters).
var preFixOrphans = []string{
	"TestBootstrapBoundedAgainstBlackHoleHost",
	"TestBootstrapEnabledAllowlist",
	"TestBootstrapEnabledAllowlistAcceptsArbitrarilyLargePRNumber",
	"TestMakefileDevDBPipesBootstrapViaExplicitFileFlag",
	"TestGatewayMainPassesRawEnvironmentToProvisioningGuard",
	"TestBootstrapConcurrentCallsSerialiseUnderAdvisoryLock",
	"TestBootstrapConvergesWhenRolesAlreadyHaveDifferentPasswords",
	"TestBootstrapFromEmbedded",
	"TestBootstrapRejectsEmptyPasswords",
	"TestBootstrapReleasesAdvisoryLock",
	"TestBootstrapReleasesAdvisoryLockAfterMidSequenceFailure",
	"TestBootstrapRespectsContextDeadlineUnderAdvisoryLockContention",
	"TestBootstrapRetriesThenFailsOnUnreachableDB",
	"TestBootstrapThenMigrateSucceedsAsMigrator",
}

func TestCIRunFiltersReachEveryTestInThePackage(t *testing.T) {
	names := enumerateTestFuncs(t)
	filters := discoverCIRunFilters(repoCIYAML(t))

	if got := unreachable(filters, names); len(got) != 0 {
		sort.Strings(got)
		t.Errorf("ci.yml's -run filters on %s leave these unreachable: %v", targetPackageArg, got)
	}

	// AC-1 table case: the four pre-BUG-02 filters, against the real enumerated
	// names, must reproduce the full 63-name orphan set — the visible RED.
	t.Run("pre_fix_filters", func(t *testing.T) {
		preFix := []string{
			"TestMigrateUpFromEmbedded",
			"TestBootstrapSQL",
			"TestProvision|TestReset|TestSuperuserDSNNotRetainedForRequestPath",
			"TestRLS",
		}

		// This test postdates the frozen pre-fix filters, so it can't be part of
		// what they orphaned; the live check above already covers it separately.
		var historical []string
		for _, n := range names {
			if n != "TestCIRunFiltersReachEveryTestInThePackage" {
				historical = append(historical, n)
			}
		}

		var want []string
		for _, n := range historical {
			if strings.HasPrefix(n, "TestSeed") {
				want = append(want, n)
			}
		}
		if len(want) != 49 {
			t.Fatalf("found %d TestSeed* functions, want 49 — enumeration or the package changed", len(want))
		}
		want = append(want, preFixOrphans...)
		if len(want) != 63 {
			t.Fatalf("expected orphan set has %d names, want 63 (49 TestSeed* + 14 non-Seed)", len(want))
		}
		sort.Strings(want)

		got := unreachable(preFix, historical)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pre-fix unreachable()\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("unfiltered_invocation_reaches_everything", func(t *testing.T) {
		names := []string{"TestFoo", "TestBar"}
		if got := unreachable([]string{""}, names); len(got) != 0 {
			t.Errorf("unreachable([\"\"], %v) = %v, want none", names, got)
		}
	})

	t.Run("dot_dot_dot_is_not_a_filter", func(t *testing.T) {
		fixture := "jobs:\n  go:\n    steps:\n      - run: go test ./...\n"
		if got := discoverCIRunFilters(fixture); len(got) != 0 {
			t.Errorf("discoverCIRunFilters(only `go test ./...`) = %v, want none", got)
		}
	})

	t.Run("TestMain_is_never_reported", func(t *testing.T) {
		got := unreachable([]string{"NoMatch"}, []string{"TestMain", "TestFoo"})
		want := []string{"TestFoo"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("unreachable() = %v, want %v (TestFoo unreachable, TestMain never reported)", got, want)
		}
	})

	t.Run("matching_is_unanchored_substring_regexp", func(t *testing.T) {
		got := unreachable([]string{"TestBootstrap"}, []string{
			"TestBootstrapFromEmbedded",
			"TestMakefileDevDBPipesBootstrapViaExplicitFileFlag",
		})
		want := []string{"TestMakefileDevDBPipesBootstrapViaExplicitFileFlag"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("unreachable() = %v, want %v", got, want)
		}
	})

	t.Run("alternation_is_honoured", func(t *testing.T) {
		got := unreachable([]string{"TestProvision|TestReset"}, []string{"TestProvisionX", "TestResetY", "TestOther"})
		want := []string{"TestOther"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("unreachable() = %v, want %v", got, want)
		}
	})
}
