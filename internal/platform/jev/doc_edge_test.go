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
	"strconv"
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

var htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)

// The probe test lives outside this package, so a lone `TestJevLiveProbe` would fail TestJevDoc_EveryNamedTestExists.
func TestJevDoc_WhatItIsNamesTheProbeCommandAsOneSpan(t *testing.T) {
	root := jevRepoRoot(t)
	src := readRepoFile(t, root, "internal/platform/jev/liveprobe/liveprobe_test.go")
	if !strings.Contains(src, "\nfunc TestJevLiveProbe(t *testing.T) {") {
		t.Fatal("internal/platform/jev/liveprobe declares no TestJevLiveProbe; the command would run nothing")
	}
	cmd := "JEV_PROBE=1 " + EnvKey + "=<key> go test -count=1 -v -run '^TestJevLiveProbe$' ./internal/platform/jev/liveprobe"
	doc := htmlCommentRE.ReplaceAllString(readJevDoc(t, root), "")
	// A code span may wrap; it renders with one space per line break.
	what := strings.Join(strings.Fields(docSection(t, doc, "## What it is", "## ")), " ")
	if !strings.Contains(what, "`"+cmd+"`") {
		t.Errorf("%s What it is does not name the probe command as one backticked span: `%s`", jevDoc, cmd)
	}
}

// The consts are matched in exact case; prose is matched in any case and across line wraps.
func TestJevDoc_NamesTheOpenRouterRouteNotTheStaleLines(t *testing.T) {
	// Newlines survive the strip, so a reported line number stays the file's.
	doc := htmlCommentRE.ReplaceAllStringFunc(readJevDoc(t, jevRepoRoot(t)), func(c string) string {
		return strings.Repeat("\n", strings.Count(c, "\n"))
	})
	for _, want := range []struct{ name, value string }{{"endpoint", endpoint}, {"Model", Model}, {"EnvKey", EnvKey}} {
		if !strings.Contains(doc, want.value) {
			t.Errorf("%s does not name the %s const's value %q", jevDoc, want.name, want.value)
		}
	}

	flat := strings.Join(strings.Fields(strings.ToLower(doc)), " ")
	for _, stale := range []string{
		"No production key exists",
		"`output_tokens` is not logged",
		"the version that answered is not logged",
		"sends `State` to TypeSafe",
		"before a production key is set",
	} {
		if strings.Contains(flat, strings.ToLower(stale)) {
			t.Errorf("%s still says %q", jevDoc, stale)
		}
	}

	// stalerefs reads line by line and exempts a line holding "retired".
	type docLine struct {
		n    int
		text string
	}
	var named []docLine
	sawKey := false
	for i, line := range strings.Split(doc, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "typesafe_api_key") || strings.Contains(lower, "api.typesafe.ai") {
			named = append(named, docLine{i + 1, line})
			sawKey = sawKey || strings.Contains(lower, "typesafe_api_key")
		}
	}
	if !sawKey {
		t.Fatalf("%s names TYPESAFE_API_KEY nowhere, want the key row to mark it retired", jevDoc)
	}
	for _, l := range named {
		if !strings.Contains(strings.ToLower(l.text), "retired") {
			t.Errorf("%s:%d names the retired route or key without the word retired: %q", jevDoc, l.n, l.text)
		}
	}
}

// flatLower is text lower-cased with every whitespace run as one space, so a re-wrap cannot hide a claim.
func flatLower(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// docRow returns the body row of sec's first table whose first cell starts with first.
func docRow(t *testing.T, sec, first string) []string {
	t.Helper()
	rows := tableBodyRows(sec)
	if len(rows) == 0 {
		t.Fatalf("%s: section has no table rows", jevDoc)
	}
	for _, r := range rows {
		if strings.HasPrefix(strings.ToLower(r[0]), strings.ToLower(first)) {
			return r
		}
	}
	t.Fatalf("%s: no table row starts with %q", jevDoc, first)
	return nil
}

var docStatusRE = regexp.MustCompile(`\b[1-5]\d\d\b`)

// Each claim is read from the one section, row or clause that states it; comments are stripped first.
func TestJevDoc_StatesEachOpenRouterItem(t *testing.T) {
	doc := htmlCommentRE.ReplaceAllStringFunc(readJevDoc(t, jevRepoRoot(t)), func(c string) string {
		return strings.Repeat("\n", strings.Count(c, "\n"))
	})
	wantAll := func(t *testing.T, where, text string, needles ...string) {
		t.Helper()
		flat := flatLower(text)
		if flat == "" {
			t.Fatalf("%s: %s is empty", jevDoc, where)
		}
		for _, n := range needles {
			if !strings.Contains(flat, flatLower(n)) {
				t.Errorf("%s: %s does not say %q", jevDoc, where, n)
			}
		}
	}

	t.Run("audience", func(t *testing.T) {
		head, _, ok := strings.Cut(doc, "\n## What it is\n")
		if !ok {
			t.Fatalf("%s has no %q heading", jevDoc, "## What it is")
		}
		_, aud, ok := strings.Cut(head, "**Audience:**")
		if !ok {
			t.Fatalf("%s has no **Audience:** line", jevDoc)
		}
		aud, _, _ = strings.Cut(aud, "\n\n")
		wantAll(t, "the audience", aud, "anyone setting `"+EnvKey+"` for Jev")
	})

	what := docSection(t, doc, "## What it is", "## ")
	t.Run("what_it_is_route", func(t *testing.T) {
		wantAll(t, "What it is", what, "`POST` to `"+endpoint+"`", "`Authorization: Bearer <"+EnvKey+">`", "The model is `"+Model+"`")
	})
	t.Run("what_it_is_logged_fields", func(t *testing.T) {
		wantAll(t, "What it is", what, "echoes the versioned model that answered", "its `usage.cost`. Both reach the log line")
	})

	knobs := docSection(t, doc, "## Env knobs", "## ")
	t.Run("key_row", func(t *testing.T) {
		wantAll(t, "the "+EnvKey+" row", strings.Join(docRow(t, knobs, "`"+EnvKey+"`"), " | "),
			"shared with the AI client", "The retired `TYPESAFE_API_KEY` is read by no product code")
	})
	t.Run("fake_row_off_lever", func(t *testing.T) {
		wantAll(t, "the "+EnvFake+" row", strings.Join(docRow(t, knobs, "`"+EnvFake+"`"), " | "),
			"with a non-empty `"+EnvKey+"` makes `FromEnv` return an error naming both variables",
			"in production the only Jev off-lever is deleting the shared key, which also turns off Gemini")
	})

	t.Run("production_row", func(t *testing.T) {
		row := docRow(t, docSection(t, doc, "## Per environment", "## "), "production")
		if len(row) != 5 {
			t.Fatalf("%s: production row has %d cells, want 5: %q", jevDoc, len(row), row)
		}
		if !strings.HasPrefix(flatLower(row[1]), "set") || flatLower(row[2]) != "unset" ||
			!strings.HasPrefix(flatLower(row[3]), "real") || !strings.HasPrefix(flatLower(row[4]), "the user") {
			t.Errorf("%s: production row = %q, want key set, %s unset, client real, set by the user", jevDoc, row, EnvFake)
		}
	})

	// The two clauses are cut from one paragraph: "Retried once" runs to "Not retried", which runs to its first full stop.
	retries := flatLower(docSection(t, doc, "## Retries and the budget", "## "))
	_, retried, ok1 := strings.Cut(retries, "**retried once:**")
	retried, notRetried, ok2 := strings.Cut(retried, "**not retried:**")
	notRetried, _, ok3 := strings.Cut(notRetried, ". ")
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("%s: Retries section has no **Retried once:** clause followed by a **Not retried:** sentence", jevDoc)
	}
	clause := map[bool][]string{true: docStatusRE.FindAllString(retried, -1), false: docStatusRE.FindAllString(notRetried, -1)}
	for _, tc := range []struct {
		statuses []int
		retry    bool
	}{
		{[]int{400, 401, 402, 403, 404, 413, 422}, false},
		{[]int{502, 524, 529}, true},
	} {
		name := map[bool]string{true: "retried_once", false: "not_retried"}[tc.retry]
		for _, s := range tc.statuses {
			t.Run(name+"/"+strconv.Itoa(s), func(t *testing.T) {
				if retryable(s) != tc.retry {
					t.Fatalf("retryable(%d) = %v, want %v: the list below is checked against the code", s, !tc.retry, tc.retry)
				}
				if !slices.Contains(clause[tc.retry], strconv.Itoa(s)) {
					t.Errorf("%s: the %s clause %v does not name %d", jevDoc, name, clause[tc.retry], s)
				}
				if slices.Contains(clause[!tc.retry], strconv.Itoa(s)) {
					t.Errorf("%s: %d is named in both clauses", jevDoc, s)
				}
			})
		}
	}

	logSec := docSection(t, doc, "## The log line", "### Outcomes")
	t.Run("log_model_row", func(t *testing.T) {
		wantAll(t, "the log model row", strings.Join(docRow(t, logSec, "`model`"), " | "),
			"from the final attempt only", "its first "+strconv.Itoa(maxModelLen)+" bytes")
	})
	t.Run("log_output_tokens_and_cost_rows", func(t *testing.T) {
		wantAll(t, "the log output_tokens row", strings.Join(docRow(t, logSec, "`output_tokens`"), " | "), "`usage.output_tokens`, summed")
		wantAll(t, "the log cost row", strings.Join(docRow(t, logSec, "`cost`"), " | "), "`usage.cost`", "summed",
			"a missing or `null` `cost` counts as `0`")
	})

	t.Run("data_terms", func(t *testing.T) {
		wantAll(t, "Data terms", docSection(t, doc, "## Data terms", "## "),
			"sends `State` to OpenRouter, which routes it to TypeSafe's endpoint on OpenRouter's zero-data-retention (ZDR) list",
			"The user owns them")
	})

	limits := docSection(t, doc, "## Known limitations", "## ")
	t.Run("limitation_answering_version", func(t *testing.T) {
		wantAll(t, "Known limitations", limits, "The log line's `model` records the version that answered")
	})
	t.Run("limitation_thresholds_trusted", func(t *testing.T) {
		wantAll(t, "Known limitations", limits, "before the thresholds are trusted")
	})
	t.Run("limitation_probe", func(t *testing.T) {
		wantAll(t, "Known limitations", limits, "The extra OpenRouter hop is measured only by the pre-merge live probe")
	})
	t.Run("limitation_zdr_alias", func(t *testing.T) {
		wantAll(t, "Known limitations", limits,
			"The alias can move to a release that is not on the ZDR list, and nothing enforces ZDR per call")
	})
}

// The fork rule must say why the retired key is no longer blanked, or a reader restores the blank.
func TestJevDoc_ForkRuleStatesTheRetiredKeyIsNotBlanked(t *testing.T) {
	doc := htmlCommentRE.ReplaceAllString(readJevDoc(t, jevRepoRoot(t)), "")
	_, rule, ok := strings.Cut(docSection(t, doc, "## Per environment", "## "), "**Fork rule.**")
	if !ok {
		t.Fatalf("%s: Per environment has no **Fork rule.** paragraph", jevDoc)
	}
	for _, want := range []string{
		"`set-ai-fake` blanks and audits `OPENROUTER_API_KEY` only.",
		"The retired `TYPESAFE_API_KEY` was deleted from production on 2026-09-27, so no fork copies it.",
	} {
		if !strings.Contains(flatLower(rule), flatLower(want)) {
			t.Errorf("%s: the fork rule does not say %q", jevDoc, want)
		}
	}
}
