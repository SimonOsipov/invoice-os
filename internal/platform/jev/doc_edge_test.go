package jev

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// unfencedLines drops fenced code blocks: a heading inside one renders as code.
func unfencedLines(doc string) []string {
	var out []string
	fenced := false
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			out = append(out, l)
		}
	}
	return out
}

func TestJevDoc_EveryHeadingIsOutsideACodeFence(t *testing.T) {
	lines := unfencedLines(readJevDoc(t, jevRepoRoot(t)))
	for _, h := range jevDocHeadings {
		if !slices.Contains(lines, h) {
			t.Errorf("%s has no %q heading outside a code fence", jevDoc, h)
		}
	}
}

func TestJevDocUnfencedLines_AFencedHeadingIsNotAHeading(t *testing.T) {
	lines := unfencedLines("## A\n\n```\n## B\n```\n\n## C\n")
	if !slices.Contains(lines, "## A") || !slices.Contains(lines, "## C") {
		t.Errorf("unfencedLines = %q, want ## A and ## C kept", lines)
	}
	if slices.Contains(lines, "## B") {
		t.Errorf("unfencedLines = %q, want the fenced ## B dropped", lines)
	}
}

func TestJevDocBacktickedCell_IsExactlyOneToken(t *testing.T) {
	if m := backtickedCellRE.FindStringSubmatch("`ok`"); m == nil || m[1] != "ok" {
		t.Errorf("backtickedCellRE on %q = %q, want [`ok` ok]", "`ok`", m)
	}
	for _, cell := range []string{"`ok` `fake`", "`ok` and more", "the `ok` row", "ok", "``"} {
		if backtickedCellRE.MatchString(cell) {
			t.Errorf("backtickedCellRE matched %q, want no match: a first cell holds one token", cell)
		}
	}
}

const subcommandFixtureEnv = "JEV_TEST_SUBCOMMAND_FIXTURE_ROOT"

// subcommandFixture writes railway-env.sh with extra inserted at the top of
// its dispatch block, and returns the fixture root.
func subcommandFixture(t *testing.T, extra, appended string) string {
	t.Helper()
	const start = "\ncase \"${1:-}\" in\n"
	script := readRepoFile(t, jevRepoRoot(t), "scripts/ci/railway-env.sh")
	if n := strings.Count(script, start); n != 1 {
		t.Fatalf("railway-env.sh holds %d top-level dispatch starts, want 1", n)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "scripts", "ci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script = strings.Replace(script, start, start+extra, 1) + appended
	if err := os.WriteFile(filepath.Join(dir, "railway-env.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestJevDocDerivedSubcommand_SurvivesAnUnrelatedArm(t *testing.T) {
	root := subcommandFixture(t,
		"  set-jev-fake)              cmd_set_jev_fake \"${2:-}\" ;; # not cmd_set_ai_fake\n", "")
	if got := derivedSubcommand(t, root); got != "set-ai-fake" {
		t.Errorf("derivedSubcommand = %q, want set-ai-fake", got)
	}
}

// Only a re-exec can observe the derivation's t.Fatalf.
func TestJevDocDerivedSubcommand_RefusesAnAmbiguousDispatch(t *testing.T) {
	if root := os.Getenv(subcommandFixtureEnv); root != "" {
		t.Logf("RETURNED %q", derivedSubcommand(t, root))
		return
	}
	cases := []struct{ name, extra, appended, clause string }{
		{"a second arm calls cmd_set_ai_fake", "  set-both-fake)             cmd_set_ai_fake \"${2:-}\" ;;\n", "", "want exactly 1"},
		{"a second top-level dispatch block", "", "\ncase \"${1:-}\" in\n  x) true ;;\nesac\n", "two top-level dispatch blocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := subcommandFixture(t, tc.extra, tc.appended)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJevDocDerivedSubcommand_RefusesAnAmbiguousDispatch$", "-test.v")
			cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
				return strings.HasPrefix(kv, envSubprocess+"=")
			}), subcommandFixtureEnv+"="+root)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("derivedSubcommand accepted the fixture, want a fatal naming %q. output: %s", tc.clause, out)
			}
			if !strings.Contains(string(out), tc.clause) {
				t.Errorf("child output does not name %q: %s", tc.clause, out)
			}
		})
	}
}

// outcomeSites maps each func in the package's non-test source to the places
// it sets an outcome: an outcome: field, an x.outcome = assignment, or x.fail(…).
func outcomeSites(t *testing.T, root string) map[string][]ast.Node {
	t.Helper()
	dir := filepath.Join(root, "internal", "platform", "jev")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sites := map[string][]ast.Node{}
	fset := token.NewFileSet()
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, d := range f.Decls {
			owner := "package scope"
			if fn, ok := d.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(d, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.KeyValueExpr:
					if isIdent(x.Key, "outcome") {
						sites[owner] = append(sites[owner], x)
					}
				case *ast.AssignStmt:
					if slices.ContainsFunc(x.Lhs, func(l ast.Expr) bool {
						sel, ok := l.(*ast.SelectorExpr)
						return ok && sel.Sel.Name == "outcome"
					}) {
						sites[owner] = append(sites[owner], x)
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "fail" {
						sites[owner] = append(sites[owner], x)
					}
				}
				return true
			})
		}
	}
	return sites
}

// derivedOutcomes reads call and fakeCall only; an outcome set anywhere else
// would reach the log without reaching the doc.
func TestJevDoc_OutcomesAreSetOnlyWhereTheDerivationReads(t *testing.T) {
	sites := outcomeSites(t, jevRepoRoot(t))
	for _, fn := range []string{"call", "fakeCall"} {
		if len(sites[fn]) == 0 {
			t.Fatalf("%s sets no outcome; the scan read the wrong package", fn)
		}
	}
	// fail assigns its own parameter, never a literal.
	if n := len(sites["fail"]); n != 1 || !hasNoStringLit(sites["fail"][0]) {
		t.Errorf("fail holds %d outcome site(s), want exactly its one parameter assignment", n)
	}
	for owner, ns := range sites {
		if owner != "call" && owner != "fakeCall" && owner != "fail" {
			t.Errorf("%s sets an outcome at %d site(s); derivedOutcomes reads call and fakeCall only", owner, len(ns))
		}
	}
}

func hasNoStringLit(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			found = true
		}
		return !found
	})
	return !found
}

// The AST derivation cannot see an attr built any other way; the emitted line can.
func TestJevDoc_LogKeyTableMatchesTheEmittedLine(t *testing.T) {
	root := jevRepoRoot(t)
	buf := &bytes.Buffer{}
	c := newClient(config{}, jsonLogger(buf))
	ctx := auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-1"})
	c.logCall(ctx, noulReq("s"), result{outcome: "ok", attempts: 1}, time.Millisecond)

	want := attrKeys(t, rawLine(t, buf))
	if !slices.Contains(want, "tenant_id") {
		t.Fatalf("emitted keys %v lack tenant_id; the identity did not reach logCall", want)
	}
	doc := readJevDoc(t, root)
	if got := tableTokens(t, docSection(t, doc, "## The log line", "### Outcomes"), "log-key table"); !slices.Equal(got, want) {
		t.Errorf("%s's log-key table lists %v, logCall emits %v in that order", jevDoc, got, want)
	}
}

func TestJevDoc_EnvKnobsTableListsBothVariables(t *testing.T) {
	doc := readJevDoc(t, jevRepoRoot(t))
	got := tableTokens(t, docSection(t, doc, "## Env knobs", "## "), "Env knobs table")
	if want := []string{EnvKey, EnvFake}; !slices.Equal(got, want) {
		t.Errorf("%s's Env knobs table lists %v, want %v", jevDoc, got, want)
	}
}

func TestJevDoc_StatesThePerAttemptCap(t *testing.T) {
	doc := readJevDoc(t, jevRepoRoot(t))
	sec := docSection(t, doc, "## Retries and the budget", "## ")
	if want := ((budget - retryWait) / 2).String(); !durationNeedleRE(want).MatchString(sec) {
		t.Errorf("%s's %q section does not state the per-attempt cap %q", jevDoc, "## Retries and the budget", want)
	}
}

func TestJevDoc_NamesEveryPurposeAndQuestionType(t *testing.T) {
	want := []string{
		string(PurposeValueCheck), string(PurposeDocumentType), string(PurposeMappingCheck),
		string(TypeNoul), string(TypeChoice), string(TypeScore),
	}
	if missing := missingNames(readJevDoc(t, jevRepoRoot(t)), want); len(missing) > 0 {
		t.Errorf("%s does not name %v as backticked tokens", jevDoc, missing)
	}
}

var docTestNameRE = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

func TestJevDoc_EveryNamedTestExists(t *testing.T) {
	root := jevRepoRoot(t)
	named := docTestNameRE.FindAllStringSubmatch(readJevDoc(t, root), -1)
	if len(named) == 0 {
		t.Fatalf("%s names no test; the scan reads nothing", jevDoc)
	}
	paths, err := filepath.Glob(filepath.Join(root, "internal", "platform", "jev", "*_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob jev test files: %d file(s), err %v", len(paths), err)
	}
	var src strings.Builder
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	for _, m := range named {
		if !strings.Contains(src.String(), "\nfunc "+m[1]+"(t *testing.T) {") {
			t.Errorf("%s names %s, which no jev test file declares", jevDoc, m[1])
		}
	}
}
