// resolve_internal_test.go: V-16, V-18, V-19, V-20. Package extraction, not extraction_test:
// V-20 calls the unexported comparator, and the three source scans read files in this package.
//
// The three scans are green from the start -- an absence scan over clean source reports
// all-clear, which reads exactly like a scan that reached nothing. Each therefore carries two
// guards: a floor (every named file parses and yields declarations) and a needle (a source
// STRING carrying the banned construct is reported, and a near-miss is not). The needle is
// never a committed file: proving the map scan works must not commit a map.
package extraction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rvMapScanFiles are the files that declare or read every type on the resolution path. A map
// FIELD in any of them puts map iteration one dereference from Resolve. anchor_store.go is out:
// it runs before Resolve, not on its path.
var rvMapScanFiles = []string{
	"resolve.go", "tier1.go", "vocabulary.go", "anchor.go",
	"shapes.go", "fingerprint.go", "extractor.go", "pagereader.go", "learn.go",
	"party.go",
}

// rvPureFiles are the four files Resolve's purity is asserted over. shapes.go is out: it
// legitimately imports time for a fixed-layout time.Parse, which reads no clock. learn.go
// (EXTR-14-04) is in: LearnRule shares resolve.go's own purity fences. party.go (EXTR-22-02) is
// in: Resolve calls partyOrder, so it is on the path and shares the same fences.
var rvPureFiles = []string{"resolve.go", "tier1.go", "learn.go", "party.go"}

// rvAllowedImports excludes time, math/rand, net/*, database/sql and pgx by omission.
var rvAllowedImports = []string{"math", "regexp", "slices", "strings", "unicode"}

// rvParse parses one source. src nil reads the named file; a string is a needle.
func rvParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// rvHasMapType reports whether a map type is written anywhere in f. go/ast alone cannot type a
// range expression, so "no map type is written here" is the syntactic superset.
func rvHasMapType(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.MapType); ok {
			found = true
		}
		return !found
	})
	return found
}

func rvImportPaths(f *ast.File) []string {
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			p = imp.Path.Value
		}
		out = append(out, p)
	}
	return out
}

// rvConcurrency names every concurrency construct in f.
func rvConcurrency(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.GoStmt:
			hits = append(hits, "go statement")
		case *ast.SelectStmt:
			hits = append(hits, "select statement")
		case *ast.ChanType:
			hits = append(hits, "channel type")
		}
		return true
	})
	return hits
}

// V-16
func TestResolve_TouchesNoMap(t *testing.T) {
	// The per-file floor below must itself be discriminating: an aggregate decls==0 check
	// (the shape this test used before EXTR-14-04) would pass over a genuinely empty file --
	// an unimplemented learn.go, say -- as long as a SIBLING file in the set carries content.
	if got := len(rvParse(t, "emptyDecls.go", "package p\n").Decls); got != 0 {
		t.Fatalf("the empty-file fixture has %d declaration(s); the per-file floor below would prove nothing", got)
	}
	if got := len(rvParse(t, "oneDecl.go", "package p\nvar x = 1\n").Decls); got == 0 {
		t.Fatal("the one-declaration fixture parses to zero declarations; the per-file floor below would prove nothing")
	}

	for _, name := range rvMapScanFiles {
		// A mistyped name, or a file that does not exist yet, Fatalfs here rather than
		// silently scanning nothing.
		f := rvParse(t, name, nil)
		if len(f.Decls) == 0 {
			t.Fatalf("%s parses to zero declarations; a file with nothing in it is not a clean one", name)
		}
		if rvHasMapType(f) {
			t.Errorf("%s writes a map type; map iteration order is not deterministic and this file is on the resolution path", name)
		}
	}

	const needle = `package p

func f() {
	var m map[string]int
	for k := range m {
		_ = k
	}
}
`
	const control = `package p

func f() {
	var s []int
	for i := range s {
		_ = i
	}
}
`
	if !rvHasMapType(rvParse(t, "needle.go", needle)) {
		t.Error("the needle source ranges a map and the scan did not report it; the all-clear above proves nothing")
	}
	if rvHasMapType(rvParse(t, "control.go", control)) {
		t.Error("the control source ranges a slice and the scan called it a map; the scan is not specific")
	}
}

// V-18
func TestResolve_ImportsOnlyPureStdlib(t *testing.T) {
	files := 0
	for _, name := range rvPureFiles {
		f := rvParse(t, name, nil)
		files++
		for _, p := range rvImportPaths(f) {
			if !slices.Contains(rvAllowedImports, p) {
				t.Errorf("%s imports %q; the resolution path takes no clock, no network and no database", name, p)
			}
		}
	}
	if files != len(rvPureFiles) {
		t.Fatalf("parsed %d of %d files; the allowlist above ran over a subset", files, len(rvPureFiles))
	}

	const needle = `package p

import "database/sql"

var _ = sql.ErrNoRows
`
	got := rvImportPaths(rvParse(t, "needle.go", needle))
	if len(got) == 0 {
		t.Fatal("the needle source imports database/sql and rvImportPaths found no import at all")
	}
	banned := false
	for _, p := range got {
		if !slices.Contains(rvAllowedImports, p) {
			banned = true
		}
	}
	if !banned {
		t.Errorf("the allowlist accepted the needle's imports %v; the clean result above proves nothing", got)
	}
}

// V-19
func TestResolve_StartsNoGoroutine(t *testing.T) {
	files := 0
	for _, name := range rvPureFiles {
		files++
		if hits := rvConcurrency(rvParse(t, name, nil)); len(hits) != 0 {
			t.Errorf("%s carries %v; Resolve is a pure function and starts nothing", name, hits)
		}
	}
	if files != len(rvPureFiles) {
		t.Fatalf("parsed %d of %d files; the scan above ran over a subset", files, len(rvPureFiles))
	}

	const needle = `package p

func g() {}

func f() {
	go g()
}
`
	const control = `package p

func g() {}

func f() {
	g()
}
`
	if hits := rvConcurrency(rvParse(t, "needle.go", needle)); len(hits) == 0 {
		t.Error("the needle source starts a goroutine and the scan did not report it; the all-clear above proves nothing")
	}
	if hits := rvConcurrency(rvParse(t, "control.go", control)); len(hits) != 0 {
		t.Errorf("the control source only calls a function and the scan reported %v; the scan is not specific", hits)
	}
}

// V-20
func TestResolve_ComparatorIsTotal(t *testing.T) {
	box := func(page int, x0, y0, x1, y1 float64) *Region {
		return &Region{Page: page, X0: x0, Y0: y0, X1: x1, Y1: y1}
	}
	base := Candidate{
		Field:    "total",
		Value:    "A",
		Region:   box(1, 0.10, 0.10, 0.20, 0.20),
		RuleID:   "r1",
		Tier:     TierLearned,
		Distance: 0.10,
	}
	with := func(mut func(*Candidate)) Candidate {
		c := base
		mut(&c)
		return c
	}

	set := []struct {
		name string
		c    Candidate
	}{
		{"base", base},
		{"tier", with(func(c *Candidate) { c.Tier = TierGeneric })},
		{"distance", with(func(c *Candidate) { c.Distance = 0.20 })},
		{"nil region", with(func(c *Candidate) { c.Region = nil })},
		{"page", with(func(c *Candidate) { c.Region = box(2, 0.10, 0.10, 0.20, 0.20) })},
		{"y0", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.15, 0.20, 0.20) })},
		{"x0", with(func(c *Candidate) { c.Region = box(1, 0.15, 0.10, 0.20, 0.20) })},
		{"y1 only", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.10, 0.20, 0.30) })},
		{"x1 only", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.10, 0.30, 0.20) })},
		{"value", with(func(c *Candidate) { c.Value = "B" })},
		{"rule id", with(func(c *Candidate) { c.RuleID = "r2" })},
	}

	// Floor: two identical entries would make the totality assertion below unsatisfiable and
	// the failure unreadable.
	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if reflect.DeepEqual(set[i].c, set[j].c) {
				t.Fatalf("the adversarial set repeats itself: %s and %s are the same candidate", set[i].name, set[j].name)
			}
		}
	}

	for i := range set {
		if got := compareCandidates(set[i].c, set[i].c); got != 0 {
			t.Errorf("compareCandidates is not reflexive on %s: got %d, want 0", set[i].name, got)
		}
	}

	for i := range set {
		for j := i + 1; j < len(set); j++ {
			ij := compareCandidates(set[i].c, set[j].c)
			ji := compareCandidates(set[j].c, set[i].c)
			if ij == 0 {
				t.Errorf("compareCandidates(%s, %s) == 0 but the two differ; the order is not total and an unstable sort may reorder them", set[i].name, set[j].name)
			}
			if (ij > 0) != (ji < 0) || (ij < 0) != (ji > 0) {
				t.Errorf("compareCandidates is not antisymmetric on (%s, %s): got %d and %d", set[i].name, set[j].name, ij, ji)
			}
		}
	}
}

// rvTopLevelNames is every name f declares at package scope.
func rvTopLevelNames(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					out = append(out, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}

// rvCalledNames is every function n calls by bare name. Calls only, never every identifier: a
// struct field key and a local variable are idents too, and either collides with a package-scope
// name in an unrelated file.
func rvCalledNames(node ast.Node) []string {
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

// rvPackageFiles is every non-test .go file in this directory.
func rvPackageFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Both file lists are hand-maintained, and a file missing from one is scanned by nothing at all
// -- an absence scan over a list that never names the file reports clean, and adding the file to
// the resolution path fails nothing. This derives the files resolve.go calls into and requires
// both lists to name each one.
//
// party.go is the case this exists for: the moment Resolve calls partyOrder, both scans stop
// covering the file that just joined the path.
//
// Vacuous today by measurement -- resolve.go calls no function declared in another file, which
// is why the needle below carries the proof that the derivation can see one.
func TestResolve_ThePurityScansCoverEveryFileResolveCallsInto(t *testing.T) {
	// The needle: a caller and a callee in two sources. Without it the empty derivation below
	// reads exactly like a scan that never worked.
	const caller = `package p

func Resolve() int { return helper() }
`
	const callee = `package p

func helper() int { return 1 }
`
	called := rvCalledNames(rvParse(t, "caller.go", caller))
	if !slices.Contains(called, "helper") {
		t.Fatalf("the needle caller calls helper and the scan found %v; the empty result below proves nothing", called)
	}
	if !slices.Contains(rvTopLevelNames(rvParse(t, "callee.go", callee)), "helper") {
		t.Fatal("the needle callee declares helper and the declaration scan missed it; the empty result below proves nothing")
	}
	if slices.Contains(rvCalledNames(rvParse(t, "control.go", callee)), "helper") {
		t.Error("the control source only DECLARES helper and the call scan reported a call; the scan is not specific")
	}

	self := rvParse(t, "resolve.go", nil)
	own := rvTopLevelNames(self)
	if len(own) == 0 {
		t.Fatal("resolve.go declares no package-scope name; every call below would be credited to another file")
	}
	calls := rvCalledNames(self)
	if len(calls) == 0 {
		t.Fatal("resolve.go calls nothing by bare name; the derivation below would be empty for the wrong reason")
	}

	files := rvPackageFiles(t)
	if len(files) < len(rvMapScanFiles) {
		t.Fatalf("the package directory holds %d non-test .go file(s) against rvMapScanFiles' %d; the derivation is reading the wrong directory", len(files), len(rvMapScanFiles))
	}
	for _, name := range files {
		if name == "resolve.go" {
			continue
		}
		for _, decl := range rvTopLevelNames(rvParse(t, name, nil)) {
			if slices.Contains(own, decl) || !slices.Contains(calls, decl) {
				continue
			}
			if !slices.Contains(rvMapScanFiles, name) {
				t.Errorf("Resolve's path calls %s, declared in %s, which rvMapScanFiles does not list; the map scan reports clean over a file it never opens", decl, name)
			}
			if !slices.Contains(rvPureFiles, name) {
				t.Errorf("Resolve's path calls %s, declared in %s, which rvPureFiles does not list; the file shares Resolve's fences and nothing holds it to them", decl, name)
			}
			break
		}
	}
}

// rvFuncNamed is the top-level function name declares in f, or nil.
func rvFuncNamed(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

// rvIdentsIn is every identifier written inside node. Deliberately wider than rvCalledNames:
// the question below is whether a function READS the lexicon at all, and a range statement is
// not a call.
func rvIdentsIn(node ast.Node) []string {
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

// AC-1. crossesALabel runs once per candidate PAIR, the innermost loop on the resolution path,
// so it must evaluate no lexicon pattern: a per-pair text scan measured 187x one Resolve on an
// 800-token single-band page. Label-ness is precomputed once per page instead, beside the party
// partition -- which is why only Resolve's own callees may read the lexicon.
func TestResolve_TheBoundaryPredicateScansNoLexicon(t *testing.T) {
	const lexicon = "anchorLabelMatchers"

	self := rvParse(t, "resolve.go", nil)
	if len(self.Decls) == 0 {
		t.Fatal("resolve.go parses to zero declarations; every scan below would report clean over nothing")
	}

	// The control: the scan must see the reference this very file already carries.
	if outranked := rvFuncNamed(self, "anchorOutranked"); outranked == nil || !slices.Contains(rvIdentsIn(outranked), lexicon) {
		t.Fatalf("resolve.go's anchorOutranked does not read %s by this scan; the clean results below prove nothing", lexicon)
	}

	// The needle and its near-miss, so an all-clear is not what a scan that reached nothing
	// also reports.
	const needle = `package p

func crossesALabel() bool {
	for _, m := range anchorLabelMatchers {
		_ = m
	}
	return false
}
`
	const control = `package p

func crossesALabel(labels []bool) bool {
	return labels[0]
}
`
	needleFn, controlFn := rvFuncNamed(rvParse(t, "needle.go", needle), "crossesALabel"), rvFuncNamed(rvParse(t, "control.go", control), "crossesALabel")
	if needleFn == nil || controlFn == nil {
		t.Fatal("rvFuncNamed missed crossesALabel in a source that declares it; the scan cannot find the real one either")
	}
	if !slices.Contains(rvIdentsIn(needleFn), lexicon) {
		t.Fatalf("the needle's crossesALabel ranges %s and the scan did not report it; the all-clear below proves nothing", lexicon)
	}
	if slices.Contains(rvIdentsIn(controlFn), lexicon) {
		t.Errorf("the control's crossesALabel reads a precomputed slice and the scan called it a %s read; the scan is not specific", lexicon)
	}

	crosses := rvFuncNamed(self, "crossesALabel")
	if crosses == nil {
		t.Fatal("resolve.go declares no crossesALabel; the rightward boundary has no home and the per-pair cost this spec bounds is unmeasurable")
	}
	for _, banned := range []string{lexicon, "anchorOutranked"} {
		if slices.Contains(rvIdentsIn(crosses), banned) {
			t.Errorf("crossesALabel reads %s; it runs once per candidate pair and a lexicon scan there is cubic in a dense row", banned)
		}
	}

	// The general form: anchorOutranked is the one per-token exception, and everything else
	// touching the lexicon must be called from Resolve's own body -- which is once per page.
	resolve := rvFuncNamed(self, "Resolve")
	if resolve == nil {
		t.Fatal("resolve.go declares no Resolve; the per-page allowance below is derived from nothing")
	}
	allowed := append([]string{"anchorOutranked"}, rvCalledNames(resolve)...)
	for _, d := range self.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || !slices.Contains(rvIdentsIn(fd), lexicon) {
			continue
		}
		if !slices.Contains(allowed, fd.Name.Name) {
			t.Errorf("%s reads %s and Resolve does not call it; a lexicon scan reachable only from inside the token or pair loops is per-anchor or per-pair work, not per-page", fd.Name.Name, lexicon)
		}
	}
}

// --- the boundary's per-page precompute --------------------------------------

// AC-1. labelTokens is what crossesALabel reads in place of the lexicon, so its answer has to be
// right in both directions and its length has to match the page: the boundary indexes it by
// token position. The two floors are what stop an all-false or an all-true return satisfying
// the comparison.
func TestResolve_LabelTokensMarksEveryLabelAndNoValue(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Sub-total", true},
		{"2,500.00", false},
		{"VAT", true},
		{"187.50", false},
		{"Honeywell Group", false},
		{"NGN", false},
	}

	// No boxes: label-ness is a property of the text alone, and the geometry lives in
	// crossesALabel.
	var page TokenPage
	page.Number = 1
	for _, c := range cases {
		page.Tokens = append(page.Tokens, Token{Text: c.text})
	}

	got := labelTokens(page)
	if len(got) != len(cases) {
		t.Fatalf("labelTokens returned %d bool(s) over %d token(s); crossesALabel reads it at the token's own index", len(got), len(cases))
	}

	var marked, clear int
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("labelTokens(%q) = %v, want %v", c.text, got[i], c.want)
		}
		if c.want {
			marked++
		} else {
			clear++
		}
	}
	if marked == 0 || clear == 0 {
		t.Fatalf("the page carries %d label(s) and %d non-label(s); a constant return satisfies a comparison that has only one kind in it", marked, clear)
	}
}

// --- the boundary's cost, and the clause the below relation makes inert -------

// rvLexiconReaders is every top-level function in f that reads the lexicon, directly or through
// another function in f. The transitive closure is the point: a per-pair predicate calling a
// per-page helper does the same work as inlining the scan, and the identifier it names is the
// helper's, not the lexicon's.
func rvLexiconReaders(f *ast.File, lexicon string) []string {
	direct := map[string]bool{}
	body := map[string][]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		body[fd.Name.Name] = rvCalledNames(fd)
		if slices.Contains(rvIdentsIn(fd), lexicon) {
			direct[fd.Name.Name] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for name, calls := range body {
			if direct[name] {
				continue
			}
			for _, c := range calls {
				if direct[c] {
					direct[name], grew = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(direct))
	for name := range direct {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// AC-1. TestResolve_TheBoundaryPredicateScansNoLexicon bans the lexicon's own name inside
// crossesALabel, which leaves the class open one call deep: crossesALabel calling
// labelTokens(page) names no banned identifier, grows a power faster than the shipped form, and
// is green under every behavioural spec in this package. The two implementations are
// observationally identical by construction, so a source scan is the only oracle there is.
func TestResolve_TheBoundaryPredicateCallsNothingThatReadsTheLexicon(t *testing.T) {
	const lexicon = "anchorLabelMatchers"

	self := rvParse(t, "resolve.go", nil)
	if len(self.Decls) == 0 {
		t.Fatal("resolve.go parses to zero declarations; the scan below reports clean over nothing")
	}

	readers := rvLexiconReaders(self, lexicon)
	// The floor: the closure must hold the two functions that demonstrably read the lexicon, or
	// an empty set satisfies every membership test below.
	for _, want := range []string{"anchorOutranked", "labelTokens"} {
		if !slices.Contains(readers, want) {
			t.Fatalf("the lexicon closure is %v and does not hold %s; the scan is not finding readers and the all-clear below proves nothing", readers, want)
		}
	}
	// The near-miss: a function reading no lexicon must stay out of the closure.
	if slices.Contains(readers, "crossesALabel") {
		t.Errorf("crossesALabel is in the lexicon closure %v; it runs once per candidate pair", readers)
	}

	crosses := rvFuncNamed(self, "crossesALabel")
	if crosses == nil {
		t.Fatal("resolve.go declares no crossesALabel; the per-pair cost this spec bounds is unmeasurable")
	}
	for _, called := range rvCalledNames(crosses) {
		if slices.Contains(readers, called) {
			t.Errorf("crossesALabel calls %s, which reaches %s; per-page work inside the pair loop is per-pair work", called, lexicon)
		}
	}
}

// rvBelowGrid is a fixed grid of pages at every combination of column and row: an anchor, a
// label and an amount on the anchor's own band, and a label just above a second amount on a
// lower band. The first three give the rightward control something to block; the last two are
// the below pair.
func rvBelowGrid() []TokenPage {
	var out []TokenPage
	xs := []float64{0.02, 0.06, 0.10, 0.14, 0.18, 0.22, 0.30, 0.38, 0.44}
	ys := []float64{0.10, 0.20, 0.30, 0.40, 0.50, 0.60, 0.70}
	for _, ax := range xs {
		for _, ay := range ys {
			for _, vx := range xs {
				for _, vy := range ys {
					if vy <= ay {
						continue
					}
					out = append(out, TokenPage{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []Token{
						{Text: "VAT", Region: Region{Page: 1, X0: ax, X1: ax + 0.06, Y0: ay, Y1: ay + 0.02}},
						{Text: "Total", Region: Region{Page: 1, X0: 0.30, X1: 0.38, Y0: ay, Y1: ay + 0.02}},
						{Text: "2,500.00", Region: Region{Page: 1, X0: 0.44, X1: 0.57, Y0: ay, Y1: ay + 0.02}},
						{Text: "Sub-total", Region: Region{Page: 1, X0: 0.24, X1: 0.34, Y0: vy - 0.015, Y1: vy - 0.005}},
						{Text: "2,687.50", Region: Region{Page: 1, X0: vx, X1: vx + 0.13, Y0: vy, Y1: vy + 0.02}},
					}})
				}
			}
		}
	}
	return out
}

// AC-3. bounded's rule.Relation.Kind == RelRight conjunct can never change an answer, which is
// why dropping it survives mutation. relatedTokens admits a below value only when it overlaps
// the anchor in X, forcing value.X0 < anchor.X1; crossesALabel needs a label at
// anchor.X1 <= X0 < value.X0, an empty interval. TestResolve_TheBoundaryDoesNotApplyBelow is
// the separate refusal of a Y-axis twin, which is a different predicate and not this.
func TestResolve_NoBelowPairCanSatisfyTheRightwardCorridor(t *testing.T) {
	below := Relation{Kind: RelBelow, MaxDistance: 0.35}
	right := Relation{Kind: RelRight, MaxDistance: 0.35}

	var belowPairs, rightPairs, rightBlocked int
	for _, page := range rvBelowGrid() {
		labels := labelTokens(page)
		anchor := page.Tokens[0].Region
		for _, r := range relatedTokens(page, anchor, below) {
			belowPairs++
			if crossesALabel(page, labels, anchor, page.Tokens[r.index].Region) {
				t.Fatalf("a below pair satisfied the rightward corridor: anchor %v value %v; the RelRight conjunct in bounded is load-bearing after all and this spec is the wrong shape", anchor, page.Tokens[r.index].Region)
			}
		}
		for _, r := range relatedTokens(page, anchor, right) {
			rightPairs++
			if crossesALabel(page, labels, anchor, page.Tokens[r.index].Region) {
				rightBlocked++
			}
		}
	}

	// The floors. Without the first the loop above ran over nothing; without the second the
	// predicate never fires on this grid, and its silence on the below pairs says nothing about
	// the relation.
	if belowPairs < 100 {
		t.Fatalf("the grid admitted %d below pair(s), want at least 100", belowPairs)
	}
	if rightBlocked < 20 {
		t.Fatalf("the grid admitted %d rightward pair(s) and the boundary blocked %d, want at least 20 blocked; a predicate that never fires reports no below crossing either", rightPairs, rightBlocked)
	}
}
