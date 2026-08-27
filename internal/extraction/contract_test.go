// contract_test.go: the reusable, database-free, network-free extractor contract suite --
// ContractT, the twelve-law id list, the per-law corpus factory, the reference extractor, and
// the twelve-law RunExtractorContract itself.
//
// HONEST FRAMING (do not relabel any spec below without re-reading this):
//   - TestAllLaws_IdsAreUniqueAndUsed and TestRunExtractorContract_CallsNewExtractorMoreThanOnce
//     are the only two that were genuinely RED against the Stage 1 no-op runner. The empty
//     body emits no law id and never calls the factory, so these two record a real
//     red-to-green transition. TWO, not three: TestCorpusCoversTheDeclaredCases was planned
//     as a third red and is not one. Stage 1 ships the complete newCorpus, so that spec passed
//     from the moment it compiled.
//   - TestCorpusCoversTheDeclaredCases and TestCorpusIsFreshPerLaw are DESIGN LOCKS and
//     TestContractSuite_UsesNarrowT a REGRESSION GUARD. All three pass in this commit. Every
//     assertion in them was instead shown to fire by breaking the thing it guards: a corpus
//     case one byte under the ceiling, a spare-capacity blob, a second nil case, a blob hoisted
//     to a package var behind a fresh outer slice, a whole corpus hoisted to a package var,
//     a corpus with fewer than three cases carrying bytes, and a helper widened to *testing.T.
//   - TestContractSuite_RunsWithoutDatabase and TestReferenceExtractorPassesEveryLaw are
//     CONFIRMATORY. A no-op runner records zero failures too, so their green says nothing yet
//     about the law checks.
//
// TWO LIMITS NO STATIC SCAN CLOSES. A law id emitted from unreachable code -- an Errorf inside
// an if false block -- still counts as emitted; measured on this file, not assumed. And a law
// whose condition can never be true is invisible to every check here. EXTR-01-07's red suite
// is the only thing that closes either: it drives deliberately unlawful extractors and asserts
// each one trips exactly its own law. Nothing in this file proves the suite REJECTS a bad
// extractor.
//
// THE CORPUS IS A FACTORY (newCorpus), departing from the precedent, which iterates a
// package-level var (internal/submission/contract_test.go:99) from a RunAdapterContract that
// takes no corpus parameter (:205). An extractor is handed Document.Bytes it must not retain
// or mutate, so a shared corpus would carry one law's mutation into the next law's input and
// turn a real violation into a passing run somewhere else. Nine laws build one each; the
// migration rescan that used to dominate that cost is now memoised on documentSizeLimit,
// leaving the 15 MiB allocation at about 161us apiece.
//
// Package extraction_test (external), matching every other test file here. The imports are
// stdlib plus internal/extraction only: deps_test.go scan B sees test imports and reports any
// in-module import outside internal/platform. No test in this file skips -- these are pure Go
// with no database and no network, and internal/tools/rlsgate fails the CI queue job on a
// test-level skip.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	// unsafe is the first use in this repo. TestCorpusIsFreshPerLaw needs a slice's data
	// pointer, and &b[0] panics on the empty and nil corpus cases.
	"unsafe"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// ContractT is the slice of *testing.T the suite needs. EXTR-01-06 and EXTR-01-07 substitute
// narrow doubles for it, which is only possible while the runner never reaches for t.Run
// (whose callback is typed *testing.T, a concrete type no interface satisfies) or a fatal
// (which calls runtime.Goexit and would abort the whole binary). TestContractSuite_UsesNarrowT
// locks that. Deliberately not deps_test.go's fenceT, which is unexported and belongs to the
// import fence.
type ContractT interface {
	Helper()
	Errorf(format string, args ...any)
}

var _ ContractT = (*testing.T)(nil)

// AllLaws is the complete law id list: exactly E01 through E12, no extras and no omissions.
// EXTR-01-07 binds its per-law red table against this slice.
var AllLaws = []string{
	"E01", "E02", "E03", "E04", "E05", "E06",
	"E07", "E08", "E09", "E10", "E11", "E12",
}

// documentCase names one entry in the corpus the runner drives Extract with.
type documentCase struct {
	name string
	doc  extraction.Document
}

// exactBytes returns a fresh slice with cap == len. A []byte(string) conversion is free to
// round the capacity up to a size class, and spare capacity would let an extractor append into
// a corpus blob without reallocating -- a mutation no law could observe.
func exactBytes(s string) []byte {
	b := make([]byte, len(s))
	copy(b, s)
	return b
}

// newCorpus builds a fresh corpus. Six cases: a native-PDF-shaped blob, a scan-shaped one, an
// empty non-nil slice, a nil slice, a blob whose content type is unknown, and one at the
// documents CHECK ceiling. Called once per law, never shared -- see this file's header.
func newCorpus() []documentCase {
	limit, err := documentSizeLimit()
	if err != nil {
		panic("newCorpus: " + err.Error())
	}

	return []documentCase{
		{
			name: "native-pdf",
			doc: extraction.Document{
				Bytes:       exactBytes("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n"),
				ContentType: "application/pdf",
			},
		},
		{
			name: "scan-jpeg",
			doc: extraction.Document{
				Bytes:       exactBytes("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xdb\x00\x01\x00\xff\xd9"),
				ContentType: "image/jpeg",
			},
		},
		{
			name: "empty",
			doc: extraction.Document{
				Bytes:       []byte{},
				ContentType: "application/pdf",
			},
		},
		{
			name: "nil-bytes",
			doc: extraction.Document{
				Bytes:       nil,
				ContentType: "application/pdf",
			},
		},
		{
			// The port documents an unknown content type as the empty string
			// (extractor.go:36), so that is the value here rather than a made-up media type.
			name: "unknown-content-type",
			doc: extraction.Document{
				Bytes:       exactBytes("neither a PDF header nor a JPEG marker, just text"),
				ContentType: "",
			},
		},
		{
			// Zeros, not a random fill: 15 MiB of entropy would cost more than the case is
			// worth, and no law reads the content.
			name: "ceiling",
			doc: extraction.Document{
				Bytes:       make([]byte, limit),
				ContentType: "application/pdf",
			},
		},
	}
}

// documentSizeLimitRE anchors on the documents upload ceiling. The >= 0 half of the same CHECK
// does not match, so the one file carrying size_bytes yields exactly one limit.
var documentSizeLimitRE = regexp.MustCompile(`size_bytes\s*<=\s*(\d+)`)

// documentSizeLimit is memoised. newCorpus runs once per law, and one glob-and-read of all 50
// migrations measures about 755us against about 161us for the 15 MiB blob beside it. Only the
// parsed integer is cached: TestCorpusIsFreshPerLaw compares blobs, and newCorpus still
// allocates every one of those per call.
var documentSizeLimit = sync.OnceValues(scanDocumentSizeLimit)

// scanDocumentSizeLimit reads the ceiling out of the migrations rather than repeating the
// number, and errors when more than one migration carries it -- a later ALTER that moved the
// ceiling elsewhere would otherwise leave the corpus pinned to a limit the database no longer
// enforces.
func scanDocumentSizeLimit() (int, error) {
	all, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		return 0, fmt.Errorf("glob migrations: %w", err)
	}
	if len(all) < 2 {
		return 0, fmt.Errorf("globbed %d migrations; the scan below would pass vacuously", len(all))
	}

	var carriers, limits []string
	for _, p := range all {
		body, err := os.ReadFile(p)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", p, err)
		}
		groups := documentSizeLimitRE.FindAllStringSubmatch(string(body), -1)
		if len(groups) == 0 {
			continue
		}
		carriers = append(carriers, filepath.Base(p))
		for _, g := range groups {
			limits = append(limits, g[1])
		}
	}
	if len(carriers) != 1 || len(limits) != 1 {
		return 0, fmt.Errorf("the size_bytes ceiling is written in %v with %d limit(s), want exactly one migration and one limit", carriers, len(limits))
	}

	n, err := strconv.Atoi(limits[0])
	if err != nil {
		return 0, fmt.Errorf("parse ceiling %q: %w", limits[0], err)
	}
	return n, nil
}

// refExtractor is lawful by construction: the yardstick the suite is calibrated against, and
// what EXTR-01-07 mutates one law at a time.
type refExtractor struct{}

func newRefExtractor() extraction.Extractor { return refExtractor{} }

func (refExtractor) Name() string    { return "contract-reference" }
func (refExtractor) Version() string { return "v1" }

// Extract reads nothing out of doc, so it cannot retain or mutate the caller's bytes.
func (refExtractor) Extract(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A fresh pointer per call: a shared one would be state leaking between instances.
	value := "REF-0001"
	return []extraction.Field{{
		Name:   "invoice_number",
		Value:  &value,
		Region: &extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.20},
		Reason: extraction.ReasonNone,
	}}, nil
}

// lawRecorder is a ContractT double that accumulates every Errorf and has no Run or fatal
// method, so a runner that grew one would fail to COMPILE rather than fail an assertion.
type lawRecorder struct {
	messages []string
}

func (*lawRecorder) Helper() {}

func (r *lawRecorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// lawIDs returns the set of law ids observed, cut from each message at its first colon.
func (r *lawRecorder) lawIDs() map[string]bool {
	ids := make(map[string]bool, len(r.messages))
	for _, m := range r.messages {
		if i := strings.Index(m, ":"); i > 0 {
			ids[m[:i]] = true
		}
	}
	return ids
}

// cancelledExtractBudget bounds the E12 probe. An extractor that ignores cancellation and
// blocks would otherwise run into the test binary's own timeout and be reported as a stack
// dump rather than as E12. Six orders of magnitude above a conforming return, so it cannot
// flake.
const cancelledExtractBudget = 5 * time.Second

// declaredReasons is the Reason set E09 admits, written out rather than derived from the
// package: a sixth constant added to extractor.go should fail E09 until someone decides it
// belongs to the contract.
var declaredReasons = map[extraction.Reason]bool{
	extraction.ReasonNone:         true,
	extraction.ReasonUnreadable:   true,
	extraction.ReasonAmbiguous:    true,
	extraction.ReasonInconsistent: true,
	extraction.ReasonMissing:      true,
}

// callExtract runs one Extract on a LIVE context, which it builds itself so no cancelled
// context can reach the value laws -- E12's probe must contaminate no other law, since
// EXTR-01-07 grades each red extractor by set equality rather than containment.
//
// ok is false when the extractor returned an error, which E04 and E06-E11 then skip for that
// case: erroring on a document it cannot read is lawful. E05 does not skip, because mutating
// the caller's bytes on the way out is still a mutation.
func callExtract(ext extraction.Extractor, doc extraction.Document) ([]extraction.Field, bool) {
	fields, err := ext.Extract(context.Background(), doc)
	if err != nil {
		return nil, false
	}
	return fields, true
}

// cancelledOutcome carries one E12 probe back off its goroutine. panicked is the formatted
// panic value and stack, empty when Extract returned.
type cancelledOutcome struct {
	fields   []extraction.Field
	err      error
	panicked string
}

// callExtractCancelled runs Extract under an already-cancelled context, bounded by
// cancelledExtractBudget. The channel is buffered, so a goroutine that returns after the
// deadline still sends and exits; one that never returns is leaked for the binary's life.
//
// A panic is carried back and re-raised on the caller's goroutine, never reported: E01-E12
// hold no panic law to attribute one to, and a non-law Errorf would break the emissions
// invariant TestAllLaws_IdsAreUniqueAndUsed enforces. Re-raising only buys attribution -- it
// makes a panic here fail the running test the way a panic under the other eleven laws does,
// instead of killing the binary from an anonymous goroutine.
func callExtractCancelled(ext extraction.Extractor, doc extraction.Document) (fields []extraction.Field, err error, timedOut bool) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan cancelledOutcome, 1)
	go func() {
		var out cancelledOutcome
		defer func() {
			if p := recover(); p != nil {
				out = cancelledOutcome{panicked: fmt.Sprintf("%v\n\n%s", p, debug.Stack())}
			}
			done <- out
		}()
		out.fields, out.err = ext.Extract(ctx, doc)
	}()

	timer := time.NewTimer(cancelledExtractBudget)
	defer timer.Stop()

	select {
	case got := <-done:
		if got.panicked != "" {
			panic("E12 probe: Extract panicked: " + got.panicked)
		}
		return got.fields, got.err, false
	case <-timer.C:
		return nil, nil, true
	}
}

// RunExtractorContract drives one extractor through all twelve laws, reporting each violation
// as an Errorf whose format string begins with the law id and a colon.
//
// Every law that needs documents calls newCorpus for itself, so no law can hand the next one a
// blob a previous extractor mutated -- this file's header on why that is a factory. A panic out
// of Extract propagates rather than being recorded, which callExtractCancelled documents.
func RunExtractorContract(t ContractT, newExtractor func() extraction.Extractor) {
	t.Helper()

	first := newExtractor()
	second := newExtractor()

	// E01-E03. The cross-instance comparison reads each instance's FIRST call, so an
	// extractor whose Name varies per call trips E01 alone rather than E01 and E03 together.
	name := first.Name()
	nameAgain := first.Name()
	version := first.Version()
	versionAgain := first.Version()
	secondName := second.Name()
	secondVersion := second.Version()

	if name == "" {
		t.Errorf("E01: Name() returned an empty string")
	}
	if name != nameAgain {
		t.Errorf("E01: Name() returned %q then %q on one instance", name, nameAgain)
	}
	if version == "" {
		t.Errorf("E02: Version() returned an empty string")
	}
	if version != versionAgain {
		t.Errorf("E02: Version() returned %q then %q on one instance", version, versionAgain)
	}
	if name != secondName {
		t.Errorf("E03: Name() returned %q on one instance and %q on another", name, secondName)
	}
	if version != secondVersion {
		t.Errorf("E03: Version() returned %q on one instance and %q on another", version, secondVersion)
	}

	// E04: a successful Extract returns a non-nil slice.
	for _, c := range newCorpus() {
		if fields, ok := callExtract(first, c.doc); ok && fields == nil {
			t.Errorf("E04: %s: Extract returned a nil []Field alongside a nil error; success is an empty non-nil slice", c.name)
		}
	}

	// E05: Extract does not mutate doc.Bytes, on the error path too. Document is passed by
	// value but Bytes shares its backing array, so an in-place write is visible to these
	// hashes; the corpus asserts cap == len, which forces any append to reallocate rather
	// than clobber spare capacity nothing would hash.
	for _, c := range newCorpus() {
		before := sha256.Sum256(c.doc.Bytes)
		callExtract(first, c.doc)
		if after := sha256.Sum256(c.doc.Bytes); after != before {
			t.Errorf("E05: %s: Extract mutated doc.Bytes: SHA-256 %x before the call, %x after", c.name, before, after)
		}
	}

	// E06: every Field.Name is non-empty.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		for i, f := range fields {
			if f.Name == "" {
				t.Errorf("E06: %s: field %d has an empty Name", c.name, i)
			}
		}
	}

	// E07: Field.Name values are unique within one result.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		at := map[string]int{}
		for i, f := range fields {
			// E06 owns emptiness. Leaving empty names out keeps the two laws disjoint for
			// EXTR-01-07's set equality; a duplicated empty name still fails E06 once per field.
			if f.Name == "" {
				continue
			}
			if prev, dup := at[f.Name]; dup {
				t.Errorf("E07: %s: fields %d and %d share the Name %q", c.name, prev, i, f.Name)
				continue
			}
			at[f.Name] = i
		}
	}

	// E08: a non-nil Value points at a non-empty string.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		for i, f := range fields {
			if f.Value != nil && *f.Value == "" {
				t.Errorf("E08: %s: field %d (%q) has a non-nil Value pointing at an empty string", c.name, i, f.Name)
			}
		}
	}

	// E09: every Reason is one of the five declared values.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		for i, f := range fields {
			if !declaredReasons[f.Reason] {
				t.Errorf("E09: %s: field %d (%q) carries the undeclared Reason %q", c.name, i, f.Name, f.Reason)
			}
		}
	}

	// E10: ReasonMissing implies a nil Value.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		for i, f := range fields {
			if f.Reason == extraction.ReasonMissing && f.Value != nil {
				t.Errorf("E10: %s: field %d (%q) is ReasonMissing but carries the Value %q", c.name, i, f.Name, *f.Value)
			}
		}
	}

	// E11: a non-nil Region is a 1-based page and a normalised, non-inverted box. The bounds
	// are a NEGATED CONJUNCTION, not a disjunction of violations: every comparison against a
	// NaN coordinate is false, which the disjunctive form reads as lawful -- measured.
	for _, c := range newCorpus() {
		fields, ok := callExtract(first, c.doc)
		if !ok {
			continue
		}
		for i, f := range fields {
			r := f.Region
			if r == nil {
				continue
			}
			if r.Page < 1 {
				t.Errorf("E11: %s: field %d (%q) has Region.Page %d, want 1 or greater", c.name, i, f.Name, r.Page)
			}
			if !(r.X0 >= 0 && r.X0 <= r.X1 && r.X1 <= 1) {
				t.Errorf("E11: %s: field %d (%q) has X0=%v X1=%v, want 0 <= X0 <= X1 <= 1", c.name, i, f.Name, r.X0, r.X1)
			}
			if !(r.Y0 >= 0 && r.Y0 <= r.Y1 && r.Y1 <= 1) {
				t.Errorf("E11: %s: field %d (%q) has Y0=%v Y1=%v, want 0 <= Y0 <= Y1 <= 1", c.name, i, f.Name, r.Y0, r.Y1)
			}
		}
	}

	// E12: an already-cancelled context yields an error AND a nil slice, checked
	// independently so (nil, nil) trips the first and (fields, err) the second. Over the whole
	// corpus, because an extractor testing ctx.Err() only inside a scan loop passes the
	// 21-byte case and fails the 15 MiB one.
	probe := first
	for _, c := range newCorpus() {
		fields, err, timedOut := callExtractCancelled(probe, c.doc)
		if timedOut {
			t.Errorf("E12: %s: Extract did not return within %s of a call on an already-cancelled context", c.name, cancelledExtractBudget)
			// That call is still running inside probe. Hand the remaining cases a fresh
			// instance so a stateful extractor cannot race the goroutine it just stranded.
			// Only a timeout constructs one, so a conforming extractor is still built twice.
			probe = newExtractor()
			continue
		}
		if err == nil {
			t.Errorf("E12: %s: Extract returned a nil error for an already-cancelled context", c.name)
		}
		if fields != nil {
			t.Errorf("E12: %s: Extract returned a non-nil %d-field slice for an already-cancelled context", c.name, len(fields))
		}
	}
}

const contractSuiteFile = "contract_test.go"

// lawEmissionRE matches a law-prefixed Errorf format string. The trailing colon-space is part
// of the pattern, so the bare ids in AllLaws could never be mistaken for emissions.
var lawEmissionRE = regexp.MustCompile(`^(E\d{2}): `)

// parseContractSuite parses this file by its own name; a test binary's CWD is its package
// directory (deps_test.go:18). Mode 0, so comments are never attached to the AST. The
// precedent scans with a regex over the raw bytes, which counts a commented-out law check --
// measured: its own pattern matches a line reading // t.Errorf then a law id -- and that is
// the dangerous direction, since commenting a check out would leave the guard green.
func parseContractSuite() (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, contractSuiteFile, nil, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", contractSuiteFile, err)
	}
	return f, fset, nil
}

// nonTestFuncs returns every declaration in this file except the Test functions themselves --
// the suite machinery, which is what both static scans are about.
func nonTestFuncs(f *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		out = append(out, fn)
	}
	return out
}

// importPackageNames returns the identifiers this file's imports bind, so a scan can tell
// t.Errorf (a law emission) from fmt.Errorf (building an error value).
func importPackageNames(f *ast.File) map[string]bool {
	names := make(map[string]bool, len(f.Imports))
	for _, spec := range f.Imports {
		if spec.Name != nil {
			names[spec.Name.Name] = true
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		names[path[strings.LastIndex(path, "/")+1:]] = true
	}
	return names
}

// selectorCall reports the receiver identifier and method name of a call like x.M(...).
func selectorCall(n ast.Node) (recv, method string, ok bool) {
	call, isCall := n.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	return id.Name, sel.Sel.Name, true
}

// lawIDsInSuiteSource returns the law ids the suite machinery emits, plus every Errorf on a
// non-package receiver whose first argument is not a law-prefixed string literal. The second
// set turns a law refactored behind a helper, where the scan would go blind, into a named
// failure with a file and a line. The invariant it imposes: the runner emits law-prefixed
// Errorf calls and nothing else. go vet already rejects a NON-CONSTANT format string on a
// ContractT.Errorf, so what is left for this to catch is a constant format built elsewhere and
// a format carrying no law id.
func lawIDsInSuiteSource() (map[string]bool, []string, error) {
	f, fset, err := parseContractSuite()
	if err != nil {
		return nil, nil, err
	}
	pkgs := importPackageNames(f)

	emitted := map[string]bool{}
	var unparseable []string

	for _, fn := range nonTestFuncs(f) {
		ast.Inspect(fn, func(n ast.Node) bool {
			recv, method, ok := selectorCall(n)
			if !ok || method != "Errorf" || pkgs[recv] {
				return true
			}
			pos := fset.Position(n.Pos())
			args := n.(*ast.CallExpr).Args
			if len(args) == 0 {
				unparseable = append(unparseable, fmt.Sprintf("%s: %s.Errorf has no arguments", pos, recv))
				return true
			}
			lit, isLit := args[0].(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				unparseable = append(unparseable, fmt.Sprintf("%s: %s.Errorf's first argument is not a string literal", pos, recv))
				return true
			}
			format, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				unparseable = append(unparseable, fmt.Sprintf("%s: %s.Errorf: unquote %s: %v", pos, recv, lit.Value, uerr))
				return true
			}
			m := lawEmissionRE.FindStringSubmatch(format)
			if m == nil {
				unparseable = append(unparseable, fmt.Sprintf("%s: %s.Errorf format %q carries no leading law id", pos, recv, format))
				return true
			}
			emitted[m[1]] = true
			return true
		})
	}
	return emitted, unparseable, nil
}

func sortedLawIDs(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestAllLaws_IdsAreUniqueAndUsed (RED-FIRST): AllLaws lists twelve unique ids, and that list
// equals -- in both directions -- the set of ids the suite machinery emits, discovered by
// parsing this file rather than by running the suite. Against the Stage 1 no-op runner the
// emitted set is empty while AllLaws lists twelve, so this fails on assertions.
func TestAllLaws_IdsAreUniqueAndUsed(t *testing.T) {
	// Uniqueness and set equality together still allow dropping a law from both sides, so the
	// count is asserted separately.
	if len(AllLaws) != 12 {
		t.Errorf("AllLaws lists %d ids %v, want exactly 12: E01 through E12", len(AllLaws), AllLaws)
	}

	declared := make(map[string]bool, len(AllLaws))
	for _, id := range AllLaws {
		if declared[id] {
			t.Errorf("AllLaws lists the duplicate id %q", id)
		}
		declared[id] = true
	}

	emitted, unparseable, err := lawIDsInSuiteSource()
	if err != nil {
		t.Fatalf("scan %s: %v", contractSuiteFile, err)
	}
	for _, u := range unparseable {
		t.Errorf("%s -- the suite machinery emits law-prefixed Errorf calls and nothing else", u)
	}

	for _, id := range AllLaws {
		if !emitted[id] {
			t.Errorf("AllLaws lists %s but no Errorf call site outside a Test function in %s emits it", id, contractSuiteFile)
		}
	}
	for _, id := range sortedLawIDs(emitted) {
		if !declared[id] {
			t.Errorf("%s emits %s from an Errorf call site but AllLaws does not list it", contractSuiteFile, id)
		}
	}
}

// TestRunExtractorContract_CallsNewExtractorMoreThanOnce (RED-FIRST): the cross-instance laws
// need two instances, so the factory must be invoked more than once rather than merely
// documented to be. Against the Stage 1 no-op runner it is invoked zero times.
func TestRunExtractorContract_CallsNewExtractorMoreThanOnce(t *testing.T) {
	var calls int32
	counting := func() extraction.Extractor {
		atomic.AddInt32(&calls, 1)
		return newRefExtractor()
	}

	RunExtractorContract(&lawRecorder{}, counting)

	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Errorf("RunExtractorContract called newExtractor %d time(s), want at least 2: one instance cannot show a cross-instance law", n)
	}
}

// TestCorpusCoversTheDeclaredCases (DESIGN LOCK -- not red-first, see this file's header): the
// six declared cases, each with cap == len, exactly one nil and one empty, and exactly one at
// the ceiling the migration sets.
//
// The ceiling assertion shares newCorpus's parse, so it locks the corpus shape rather than
// independently rederiving the number; documentSizeLimit itself errors when more than one
// migration carries the ceiling, and the band check below catches a regex that grabbed the
// wrong half of the CHECK.
func TestCorpusCoversTheDeclaredCases(t *testing.T) {
	limit, err := documentSizeLimit()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if limit < 1<<20 || limit > 1<<30 {
		t.Fatalf("parsed a documents ceiling of %d bytes, which is outside 1 MiB..1 GiB; the assertions below would be meaningless", limit)
	}

	corpus := newCorpus()
	if len(corpus) != 6 {
		t.Errorf("newCorpus returned %d cases, want 6: native-pdf, scan-jpeg, empty, nil-bytes, unknown-content-type, ceiling", len(corpus))
	}

	names := map[string]int{}
	var nils, empties, ceilings int
	for _, c := range corpus {
		names[c.name]++
		b := c.doc.Bytes
		if cap(b) != len(b) {
			t.Errorf("case %q has cap %d and len %d; spare capacity lets an extractor append into the blob without reallocating, which no law would see", c.name, cap(b), len(b))
		}
		switch {
		case b == nil:
			nils++
		case len(b) == 0:
			empties++
		}
		if len(b) == limit {
			ceilings++
		}
	}

	for name, n := range names {
		if n > 1 {
			t.Errorf("the corpus names %q %d times, want once", name, n)
		}
	}
	if nils != 1 {
		t.Errorf("the corpus holds %d nil-Bytes case(s), want exactly 1", nils)
	}
	if empties != 1 {
		t.Errorf("the corpus holds %d empty-but-non-nil case(s), want exactly 1", empties)
	}
	if ceilings != 1 {
		t.Errorf("the corpus holds %d case(s) of exactly %d bytes, want exactly 1 at the documents ceiling", ceilings, limit)
	}
}

// TestCorpusIsFreshPerLaw (DESIGN LOCK -- not red-first, see this file's header): two
// newCorpus calls share no memory. Three parts, because address inequality alone is both
// unwritable as stated and only a proxy.
//
// Every zero-length allocation returns one process-wide address and a nil slice has the data
// pointer 0, so the empty and nil cases compare equal to themselves across calls and are
// exempt -- measured, and nothing can be written through them anyway. Both nesting levels are
// checked: a factory that rebuilt the outer slice from package-level blob vars would pass a
// one-level check.
func TestCorpusIsFreshPerLaw(t *testing.T) {
	c1 := newCorpus()
	c2 := newCorpus()

	if len(c1) == 0 || len(c1) != len(c2) {
		t.Fatalf("newCorpus returned %d then %d cases; the comparisons below would be meaningless", len(c1), len(c2))
	}
	if unsafe.SliceData(c1) == unsafe.SliceData(c2) {
		t.Errorf("two newCorpus calls returned slices over one []documentCase backing array")
	}

	compared, first := 0, -1
	for i := range c1 {
		if c1[i].name != c2[i].name {
			t.Errorf("case %d is %q in one corpus and %q in the other; newCorpus is not returning a stable order", i, c1[i].name, c2[i].name)
			continue
		}
		b1, b2 := c1[i].doc.Bytes, c2[i].doc.Bytes
		if len(b1) != len(b2) {
			t.Errorf("case %q is %d bytes in one corpus and %d in the other", c1[i].name, len(b1), len(b2))
			continue
		}
		if len(b1) == 0 {
			continue
		}
		compared++
		if first < 0 {
			first = i
		}
		if unsafe.SliceData(b1) == unsafe.SliceData(b2) {
			t.Errorf("case %q: two newCorpus calls returned one byte array at %p", c1[i].name, unsafe.SliceData(b1))
		}
	}
	if compared < 3 {
		t.Errorf("only %d case(s) carry bytes, so the address check ran %d time(s), want at least 3", compared, compared)
	}

	if first < 0 {
		t.Fatalf("no corpus case carries bytes; the sentinel below cannot be written")
	}
	orig := c1[first].doc.Bytes[0]
	if c2[first].doc.Bytes[0] != orig {
		t.Fatalf("case %q starts with %#x in one corpus and %#x in the other; the sentinel below would prove nothing", c1[first].name, orig, c2[first].doc.Bytes[0])
	}
	c1[first].doc.Bytes[0] = ^orig
	if got := c2[first].doc.Bytes[0]; got != orig {
		t.Errorf("writing a sentinel into one corpus's %q blob changed the other's first byte to %#x; the corpora alias", c1[first].name, got)
	}
}

// TestContractSuite_UsesNarrowT (REGRESSION GUARD -- not red-first, see this file's header):
// no function in the suite machinery calls Run or a fatal on a non-package receiver. The
// ContractT parameter already makes both uncompilable, so this catches the wider mistake --
// a helper that took *testing.T instead, which would compile and would silently forbid
// EXTR-01-06 and EXTR-01-07 from substituting a recorder.
func TestContractSuite_UsesNarrowT(t *testing.T) {
	f, fset, err := parseContractSuite()
	if err != nil {
		t.Fatalf("%v", err)
	}
	pkgs := importPackageNames(f)

	fns := nonTestFuncs(f)
	if len(fns) == 0 {
		t.Fatalf("parsed 0 non-Test declarations in %s; the scan below would pass vacuously", contractSuiteFile)
	}

	forbidden := map[string]bool{"Run": true, "Fatal": true, "Fatalf": true}
	for _, fn := range fns {
		ast.Inspect(fn, func(n ast.Node) bool {
			recv, method, ok := selectorCall(n)
			if !ok || pkgs[recv] || !forbidden[method] {
				return true
			}
			t.Errorf("%s: %s calls %s.%s; the suite machinery takes a ContractT, which has none of them -- a recorder could not be substituted for it",
				fset.Position(n.Pos()), fn.Name.Name, recv, method)
			return true
		})
	}
}

// TestContractSuite_RunsWithoutDatabase (CONFIRMATORY -- see this file's header): with
// DATABASE_URL and DATABASE_MIGRATION_URL cleared for the process, the suite runs to
// completion against the reference extractor and records nothing. It never reaches for either.
func TestContractSuite_RunsWithoutDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_MIGRATION_URL", "")

	rec := &lawRecorder{}
	RunExtractorContract(rec, newRefExtractor)

	if len(rec.messages) != 0 {
		t.Errorf("with DATABASE_URL and DATABASE_MIGRATION_URL cleared the suite recorded %d failure(s): %v", len(rec.messages), rec.messages)
	}
}

// TestReferenceExtractorPassesEveryLaw (CONFIRMATORY -- see this file's header): the reference
// extractor trips no law. Green against a no-op runner too, so this calibrates the yardstick
// rather than proving the suite rejects anything.
func TestReferenceExtractorPassesEveryLaw(t *testing.T) {
	rec := &lawRecorder{}
	RunExtractorContract(rec, newRefExtractor)

	if len(rec.messages) != 0 {
		t.Errorf("the reference extractor tripped %d check(s) under laws %v: %v",
			len(rec.messages), sortedLawIDs(rec.lawIDs()), rec.messages)
	}
}
