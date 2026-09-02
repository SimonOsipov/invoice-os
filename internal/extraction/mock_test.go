// mock_test.go: MockExtractor's specs -- the twelve-law contract run, pointer-only interface
// satisfaction, determinism in three shapes, distinct inputs, AC-3's reason matrix, the
// ambient-dependency scan, non-mutation, fresh memory per call on BOTH arms, the pinned
// Name/Version literals, and MockFixtures handing back a copy. EXTR-12-01 added the default
// result's field states; QA added the fixture arms' vocabulary, alternative memory and the
// marshalled Alternatives.
//
// HONEST FRAMING (do not relabel a spec without re-reading this):
//   - Before mock.go exists NOTHING here is red. The package does not compile, which is a BUILD
//     FAILURE, not a failing test. The red-first baseline is the Stage 2.5 probe stub -- a
//     constant mock over one shared slice that satisfies Extractor and passes all twelve laws.
//   - Measured against that stub: TestMockExtractor_DistinctInputsDistinctResults,
//     TestMockExtractor_CoversEveryReason and TestMockExtractor_ReturnsFreshMemoryPerCall are
//     genuinely RED, and TestMockExtractor_IsDeterministic is red on its cross-instance pointer
//     clause alone. THREE and a half, not seven.
//   - TestMockExtractor_PassesTheContract and TestMockExtractor_DoesNotMutateInput are
//     CONFIRMATORY: the stub passes both. TestMockExtractor_HasNoAmbientDependency is a
//     REGRESSION GUARD, shown to fire by planting time.Now, a sibling func and a sibling var in
//     mock.go rather than by a red-to-green transition.
//   - TestMockExtractor_OnlyThePointerSatisfiesExtractor is a REGRESSION GUARD added in QA:
//     moving all four methods to value receivers left every other spec green, so the var _
//     Extractor line documented the aliasing hazard without defending it.
//   - TestMockExtractor_ReturnsFreshMemoryPerCall probes BOTH arms, but the DECIDED reading
//     only. Dropping cloneFields from the FIXTURE arm alone left every other spec green until
//     it did; shallow-copying Alternatives still did until
//     TestMockExtractor_ReturnsFreshAlternativeMemoryPerCall.
//   - TestMockExtractor_PinsNameAndVersion and TestMockFixtures_HandsBackACopy arrived with the
//     real mock.go and are GREEN from their first run. Neither is a transition. The pin closes
//     laws E01/E02, which require only a non-empty value that is stable within one run and so
//     cannot see a rename. The copy guard fires against a memoised MockFixtures, not against
//     anything the current source does.
//
// THE GAP THE AMBIENT SCAN DOES NOT CLOSE. A method on a sibling type -- doc.stamp() where
// stamp reads the clock -- is invisible to it: doc resolves locally and stamp is a selector,
// never an unresolved identifier (go/parser/resolver.go:267-270). Structural, measured, and not
// a bug in the scan. The scan is not total.
//
// Every clause below that could quietly examine nothing counts what it examined and calls
// Fatalf at zero: a vacuous green is the failure mode these specs exist to prevent.
//
// Package extraction_test (external), matching every other test file here. This file carries no
// skip call and no database-DSN environment-variable name, in a comment or anywhere else:
// internal/tools/rlsgate/ci_registration_test.go:110 classifies a package DB-gated when both
// appear in ONE file's raw bytes, and that scan cannot tell a comment from code.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const mockSourceFile = "mock.go"

// mxUnknown is bytes no fixture claims, so Extract takes the default arm.
func mxUnknown(s string) extraction.Document {
	return extraction.Document{Bytes: []byte(s), ContentType: "application/pdf"}
}

func mxExtract(t *testing.T, ext extraction.Extractor, doc extraction.Document) []extraction.FieldResult {
	t.Helper()
	fields, err := ext.Extract(context.Background(), doc)
	if err != nil {
		t.Fatalf("Extract(%d bytes): unexpected error %v", len(doc.Bytes), err)
	}
	return fields
}

func mxReasons(fields []extraction.FieldResult) map[extraction.Reason]bool {
	out := map[extraction.Reason]bool{}
	for _, f := range fields {
		out[f.Reason] = true
	}
	return out
}

func mxSortedReasons(m map[extraction.Reason]bool) []string {
	out := make([]string, 0, len(m))
	for r := range m {
		out = append(out, strconv.Quote(string(r)))
	}
	sort.Strings(out)
	return out
}

// TestMockExtractor_PassesTheContract (CONFIRMATORY): all twelve laws, zero failures. *testing.T
// satisfies ContractT, so a violation is reported at its own law's message.
func TestMockExtractor_PassesTheContract(t *testing.T) {
	RunExtractorContract(t, func() extraction.Extractor { return extraction.NewMockExtractor() })
}

// TestMockExtractor_OnlyThePointerSatisfiesExtractor (REGRESSION GUARD): var _ Extractor =
// (*MockExtractor)(nil) is satisfied whether or not the value type also implements, so it cannot
// see a receiver change. A value satisfying the seam is the aliasing hazard
// internal/submission/mock_adapter.go:150-153 documents.
func TestMockExtractor_OnlyThePointerSatisfiesExtractor(t *testing.T) {
	iface := reflect.TypeOf((*extraction.Extractor)(nil)).Elem()
	if iface.NumMethod() == 0 {
		t.Fatalf("extraction.Extractor declares no method; Implements would be true of every type")
	}

	ptr := reflect.TypeOf(extraction.NewMockExtractor())
	if ptr.Kind() != reflect.Ptr {
		t.Fatalf("NewMockExtractor returned %s, want a pointer", ptr)
	}
	if !ptr.Implements(iface) {
		t.Fatalf("%s does not satisfy Extractor; the %d method(s) it needs are %v", ptr, iface.NumMethod(), mxMethodNames(iface))
	}
	if val := ptr.Elem(); val.Implements(iface) {
		t.Errorf("the VALUE type %s satisfies Extractor too; every method must take a POINTER receiver, or a copied MockExtractor silently satisfies the seam", val)
	}
}

func mxMethodNames(t reflect.Type) []string {
	out := make([]string, t.NumMethod())
	for i := range out {
		out[i] = t.Method(i).Name
	}
	return out
}

// TestMockExtractor_IsDeterministic (CONFIRMATORY on its DeepEqual clauses): reflect.DeepEqual
// follows pointers, so it compares *string VALUES -- measured, not assumed. Three shapes,
// because cross-instance equality alone is passed by a per-instance call counter.
func TestMockExtractor_IsDeterministic(t *testing.T) {
	doc := mxUnknown("determinism probe: an unrecognised document")

	a := mxExtract(t, extraction.NewMockExtractor(), doc)
	b := mxExtract(t, extraction.NewMockExtractor(), doc)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two separately constructed mocks disagreed on one document:\n %#v\n %#v", a, b)
	}

	one := extraction.NewMockExtractor()
	c := mxExtract(t, one, doc)
	d := mxExtract(t, one, doc)
	if !reflect.DeepEqual(c, d) {
		t.Errorf("one mock returned different results on two calls with the same bytes")
	}

	other := mxUnknown("determinism probe: a different unrecognised document")
	e := mxExtract(t, one, doc)
	mxExtract(t, one, other)
	g := mxExtract(t, one, doc)
	if !reflect.DeepEqual(e, g) {
		t.Errorf("an interleaved call changed what the mock returns for the first document")
	}

	// The clauses below index both results, and the whole point of the *string comparison is
	// that it ran at all.
	if len(a) != len(b) {
		t.Fatalf("two separately constructed mocks returned %d and %d field(s)", len(a), len(b))
	}
	var compared int
	for i := range a {
		if a[i].Value == nil {
			continue
		}
		if b[i].Value == nil {
			t.Errorf("field %d has a Value in one result and nil in the other", i)
			continue
		}
		compared++
		if a[i].Value == b[i].Value {
			t.Errorf("field %d: both results carry ONE *string at %p; determinism must be equal VALUES, not one shared address", i, a[i].Value)
		}
		if *a[i].Value != *b[i].Value {
			t.Errorf("field %d: %q then %q", i, *a[i].Value, *b[i].Value)
		}
	}
	if compared == 0 {
		t.Fatalf("no field in the result carries a Value; the pointer clause above compared nothing and DeepEqual could be matching addresses")
	}
}

// TestMockExtractor_DistinctInputsDistinctResults (RED-FIRST): every pair of the two fixtures and
// the default differs, so a constant mock cannot pass determinism vacuously.
func TestMockExtractor_DistinctInputsDistinctResults(t *testing.T) {
	fixtures := extraction.MockFixtures()
	if len(fixtures) < 2 {
		t.Fatalf("MockFixtures returned %d fixture(s), want at least 2; the comparisons below would be meaningless", len(fixtures))
	}

	ext := extraction.NewMockExtractor()
	results := map[string][]extraction.FieldResult{}
	for _, fx := range fixtures {
		results[fx.Name] = mxExtract(t, ext, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"})
	}
	results["<default>"] = mxExtract(t, ext, mxUnknown("no fixture claims these bytes"))
	// Two fixtures sharing a Name would collapse into one map entry and drop a comparison.
	if len(results) != len(fixtures)+1 {
		t.Fatalf("%d fixture(s) plus the default produced %d distinct key(s); fixture names must be unique", len(fixtures), len(results))
	}

	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)

	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if reflect.DeepEqual(results[names[i]], results[names[j]]) {
				t.Errorf("%q and %q produce identical field sets; a constant mock would pass determinism vacuously", names[i], names[j])
			}
		}
	}
}

// TestMockExtractor_CoversEveryReason (RED-FIRST): AC-3 -- the DEFAULT result alone carries all
// five Reason values and both Region states. That is stronger than the union across fixtures,
// which it implies; the union is asserted in the other direction so a sixth reason cannot appear.
func TestMockExtractor_CoversEveryReason(t *testing.T) {
	want := map[extraction.Reason]bool{
		extraction.ReasonNone:         true,
		extraction.ReasonUnreadable:   true,
		extraction.ReasonAmbiguous:    true,
		extraction.ReasonInconsistent: true,
		extraction.ReasonMissing:      true,
	}

	ext := extraction.NewMockExtractor()
	def := mxExtract(t, ext, mxUnknown("no fixture claims these bytes either"))

	got := mxReasons(def)
	for r := range want {
		if !got[r] {
			t.Errorf("the default result carries no field with Reason %q; it has %v", r, mxSortedReasons(got))
		}
	}

	var withRegion, withoutRegion int
	for _, f := range def {
		if f.Region != nil {
			withRegion++
		} else {
			withoutRegion++
		}
	}
	if withRegion == 0 || withoutRegion == 0 {
		t.Errorf("the default result has %d field(s) with a Region and %d without; want at least one of each", withRegion, withoutRegion)
	}

	union := map[extraction.Reason]bool{}
	for r := range got {
		union[r] = true
	}
	for _, fx := range extraction.MockFixtures() {
		for r := range mxReasons(mxExtract(t, ext, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"})) {
			union[r] = true
		}
	}
	for r := range union {
		if !want[r] {
			t.Errorf("the mock emits the undeclared Reason %q", r)
		}
	}
}

var mockAllowedImports = map[string]bool{
	"context":       true,
	"crypto/sha256": true,
}

// mxParse parses one non-test file of internal/extraction. Mode 0: comments are never attached
// to the AST, so a denylisted name inside a comment cannot fail this scan.
func mxParse(t *testing.T, name string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f, fset
}

// mxSiblingTypesAndConsts returns the type and const names the package's OTHER non-test files
// declare. mock.go may name those; a sibling FUNC or VAR it may not, since only those can run.
func mxSiblingTypesAndConsts(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}
	out := map[string]bool{}
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") || name == mockSourceFile {
			continue
		}
		scanned++
		f, _ := mxParse(t, name)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.TYPE && gd.Tok != token.CONST) {
				continue
			}
			for _, spec := range gd.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out[n.Name] = true
					}
				}
			}
		}
	}
	if scanned == 0 || len(out) == 0 {
		t.Fatalf("scanned %d sibling file(s) and found %d type/const name(s); the scan below would misreport", scanned, len(out))
	}
	return out
}

// TestMockExtractor_HasNoAmbientDependency (REGRESSION GUARD): AC-4. Part 1 is an import
// ALLOWLIST, strictly stronger than the denylist the AC names -- time, os, math/rand,
// crypto/rand and net/* all need an import, and so do runtime, sync/atomic and unsafe, which the
// denylist never mentions. Part 2 closes the one route part 1 cannot see.
func TestMockExtractor_HasNoAmbientDependency(t *testing.T) {
	f, fset := mxParse(t, mockSourceFile)

	seen := map[string]bool{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Errorf("%s: unparseable import %s", fset.Position(spec.Pos()), spec.Path.Value)
			continue
		}
		seen[path] = true
		if !mockAllowedImports[path] {
			t.Errorf("%s: %s imports %q; the allowlist is %v -- time, os, math/rand, crypto/rand and net/* are all outside it",
				fset.Position(spec.Pos()), mockSourceFile, path, mxSortedStrings(mockAllowedImports))
		}
	}
	if len(seen) == 0 {
		t.Fatalf("%s declares no imports at all; part 1 examined nothing", mockSourceFile)
	}

	// ast.File.Unresolved is deprecated; an empty one would read as a clean file, so it is
	// checked before it is trusted.
	if len(f.Unresolved) == 0 {
		t.Fatalf("go/parser resolved every identifier in %s; ast.File.Unresolved is deprecated and this scan has stopped working", mockSourceFile)
	}
	siblings := mxSiblingTypesAndConsts(t)
	var sawQualifier bool
	for _, id := range f.Unresolved {
		switch {
		case types.Universe.Lookup(id.Name) != nil:
		case seen[id.Name] || mxImportQualifier(seen, id.Name):
			sawQualifier = true
		case siblings[id.Name]:
		default:
			t.Errorf("%s: %s names %q, which is neither a universe name, one of its own imports, nor a sibling type or const -- a sibling func or var is the one way this file reaches ambient state without importing anything",
				fset.Position(id.Pos()), mockSourceFile, id.Name)
		}
	}
	if !sawQualifier {
		t.Fatalf("no import qualifier appeared among %d unresolved identifier(s) in %s; part 2 is not seeing this file's calls", len(f.Unresolved), mockSourceFile)
	}
}

// mxImportQualifier reports whether name is the last path element of an imported package.
func mxImportQualifier(paths map[string]bool, name string) bool {
	for p := range paths {
		if p[strings.LastIndex(p, "/")+1:] == name {
			return true
		}
	}
	return false
}

func mxSortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMockExtractor_DoesNotMutateInput (CONFIRMATORY): law E05 proven directly rather than only
// through the runner, and over nil and empty bytes as well as the fixtures.
func TestMockExtractor_DoesNotMutateInput(t *testing.T) {
	ext := extraction.NewMockExtractor()

	blobs := [][]byte{[]byte("a document nothing recognises"), nil, {}}
	for _, fx := range extraction.MockFixtures() {
		blobs = append(blobs, fx.Bytes)
	}

	for i, b := range blobs {
		before := sha256.Sum256(b)
		if _, err := ext.Extract(context.Background(), extraction.Document{Bytes: b, ContentType: "application/pdf"}); err != nil {
			t.Fatalf("blob %d: %v", i, err)
		}
		if after := sha256.Sum256(b); after != before {
			t.Errorf("blob %d: Extract mutated the caller's bytes: SHA-256 %x before, %x after", i, before, after)
		}
	}
}

// TestMockExtractor_ReturnsFreshMemoryPerCall (RED-FIRST): no law catches a mock that hands every
// caller the table's own memory, so a caller mutating one result would corrupt the next. Both
// arms: dropping cloneFields from the FIXTURE arm alone left every other spec green.
func TestMockExtractor_ReturnsFreshMemoryPerCall(t *testing.T) {
	ext := extraction.NewMockExtractor()

	type freshProbe struct {
		label string
		doc   extraction.Document
	}
	probes := []freshProbe{{"<default>", mxUnknown("freshness probe")}}
	for _, fx := range extraction.MockFixtures() {
		probes = append(probes, freshProbe{fx.Name, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"}})
	}
	if len(probes) < 2 {
		t.Fatalf("only the default arm is probed; MockFixtures returned %d fixture(s)", len(probes)-1)
	}

	var values, regions int
	for _, p := range probes {
		v, r := mxAssertFreshMemory(t, ext, p.doc, p.label)
		values += v
		regions += r
	}
	if values == 0 {
		t.Fatalf("no probed result carries a Value; the *string mutations proved nothing")
	}
	if regions == 0 {
		t.Fatalf("no probed result carries a Region; the *Region mutations proved nothing")
	}
}

// mxAssertFreshMemory clobbers one result and asserts a later call comes back pristine. Address
// inequality is only a proxy; the clobber-and-ask-again is the oracle, and it covers the slice
// backing array as well as the pointers. Returns how many Values and Regions it clobbered so the
// caller can reject a run that examined neither.
func mxAssertFreshMemory(t *testing.T, ext extraction.Extractor, doc extraction.Document, label string) (int, int) {
	t.Helper()

	first := mxExtract(t, ext, doc)
	if len(first) == 0 {
		t.Fatalf("%s: the result is empty; the mutations below would prove nothing", label)
	}

	pristine := make([]extraction.FieldResult, len(first))
	copy(pristine, first)
	var pristineValues, pristineRegions int
	for _, f := range first {
		if f.Value != nil {
			pristineValues++
		}
		if f.Region != nil {
			pristineRegions++
		}
	}

	second := mxExtract(t, ext, doc)
	if len(second) != len(first) {
		t.Fatalf("%s: two calls returned %d and %d field(s)", label, len(first), len(second))
	}
	for i := range first {
		if first[i].Value != nil && first[i].Value == second[i].Value {
			t.Errorf("%s: field %d: two calls returned ONE *string at %p", label, i, first[i].Value)
		}
		if first[i].Region != nil && first[i].Region == second[i].Region {
			t.Errorf("%s: field %d: two calls returned ONE *Region at %p", label, i, first[i].Region)
		}
	}

	first[0].Name = "CLOBBERED"
	for _, f := range first {
		if f.Value != nil {
			*f.Value = "CLOBBERED"
		}
		if f.Region != nil {
			f.Region.Page = 9999
		}
	}

	third := mxExtract(t, ext, doc)
	if len(third) != len(pristine) {
		t.Fatalf("%s: the third call returned %d field(s), the first returned %d", label, len(third), len(pristine))
	}
	if third[0].Name != pristine[0].Name {
		t.Errorf("%s: mutating a returned Field.Name changed the next call's field 0 to %q, want %q", label, third[0].Name, pristine[0].Name)
	}
	var seen int
	for _, f := range third {
		if f.Value != nil {
			if *f.Value == "CLOBBERED" {
				t.Errorf("%s: mutating a returned *string changed the next call's %q to %q", label, f.Name, *f.Value)
			}
			seen++
		}
		if f.Region != nil && f.Region.Page == 9999 {
			t.Errorf("%s: mutating a returned *Region changed the next call's %q to Page %d", label, f.Name, f.Region.Page)
		}
	}
	if seen != pristineValues {
		t.Errorf("%s: the third call carries %d Value(s), the first carried %d", label, seen, pristineValues)
	}
	return pristineValues, pristineRegions
}

// TestMockExtractor_PinsNameAndVersion (PIN): laws E01 and E02 accept any non-empty string that
// does not change within one run, so a rename passes all twelve laws and every behavioural
// spec while silently rewriting what extraction_jobs.extractor and .extractor_version mean for
// every row already stored under the old value.
func TestMockExtractor_PinsNameAndVersion(t *testing.T) {
	ext := extraction.NewMockExtractor()
	if got := ext.Name(); got != "mock" {
		t.Errorf("Name() = %q, want %q; the value is persisted as extraction_jobs.extractor and changing it orphans every existing row", got, "mock")
	}
	if got := ext.Version(); got != "v1" {
		t.Errorf("Version() = %q, want %q; a deliberate bump edits BOTH the mockExtractorVersion const in mock.go and this literal, and every extraction_jobs.extractor_version row already written keeps the old value", got, "v1")
	}
}

// TestMockFixtures_HandsBackACopy (REGRESSION GUARD): spec 7 covers Extract's results, nothing
// covers MockFixtures. A caller clobbering a returned fx.Bytes must not reach the fixture table,
// or the NEXT caller gets bytes that hash to no key and silently falls to the default result.
// Measured: the current source cannot fail this, because each body is an immutable string and
// []byte(body) allocates per call. It fires against a MockFixtures that memoises its return.
func TestMockFixtures_HandsBackACopy(t *testing.T) {
	first := extraction.MockFixtures()
	if len(first) < 2 {
		t.Fatalf("MockFixtures returned %d fixture(s), want at least 2", len(first))
	}

	ext := extraction.NewMockExtractor()
	def := mxExtract(t, ext, mxUnknown("no fixture claims these bytes"))

	originals := make([][]byte, len(first))
	pristine := make([][]extraction.FieldResult, len(first))
	for i, fx := range first {
		if len(fx.Bytes) == 0 {
			t.Fatalf("fixture %d (%q) carries no bytes; the clobber below would write nothing", i, fx.Name)
		}
		originals[i] = make([]byte, len(fx.Bytes))
		copy(originals[i], fx.Bytes)
		pristine[i] = mxExtract(t, ext, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"})
		// Without this a fixture whose result equals the default would make the arm clause below vacuous.
		if reflect.DeepEqual(pristine[i], def) {
			t.Fatalf("fixture %q produces the default result; the fixture-arm clause below would pass on a lookup miss", fx.Name)
		}
	}

	var clobbered int
	for _, fx := range first {
		for j := range fx.Bytes {
			fx.Bytes[j] = 'X'
			clobbered++
		}
	}
	if clobbered == 0 {
		t.Fatalf("the clobber wrote %d byte(s); the assertions below would prove nothing", clobbered)
	}

	second := extraction.MockFixtures()
	if len(second) != len(first) {
		t.Fatalf("two MockFixtures calls returned %d and %d fixture(s)", len(first), len(second))
	}
	for i := range second {
		if second[i].Name != first[i].Name {
			t.Errorf("fixture %d: Name %q then %q", i, first[i].Name, second[i].Name)
			continue
		}
		// Address inequality is only a proxy; the two clauses after it are the oracle.
		if len(second[i].Bytes) > 0 && len(first[i].Bytes) > 0 && &second[i].Bytes[0] == &first[i].Bytes[0] {
			t.Errorf("fixture %q: two MockFixtures calls returned ONE backing array at %p", second[i].Name, &second[i].Bytes[0])
		}
		if !reflect.DeepEqual(second[i].Bytes, originals[i]) {
			t.Errorf("fixture %q: clobbering the first call's Bytes changed the second call's to %q, want %q", second[i].Name, second[i].Bytes, originals[i])
			continue
		}
		got := mxExtract(t, ext, extraction.Document{Bytes: second[i].Bytes, ContentType: "application/pdf"})
		if reflect.DeepEqual(got, def) {
			t.Errorf("fixture %q: Extract on the second call's Bytes fell to the DEFAULT result; the lookup no longer recognises it", second[i].Name)
			continue
		}
		if !reflect.DeepEqual(got, pristine[i]) {
			t.Errorf("fixture %q: Extract on the second call's Bytes returned a different result than on the first", second[i].Name)
		}
	}
}

// --- EXTR-12-01: the default result's field states -----------------------------

// mxDefault is the default-arm result: bytes no fixture claims.
func mxDefault(t *testing.T) []extraction.FieldResult {
	t.Helper()
	return mxExtract(t, extraction.NewMockExtractor(), mxUnknown("no fixture claims these bytes"))
}

// mxByName indexes a result by field name. Law E07 makes the names unique, so nothing is lost.
func mxByName(t *testing.T, results []extraction.FieldResult) map[string]extraction.FieldResult {
	t.Helper()
	out := make(map[string]extraction.FieldResult, len(results))
	for _, r := range results {
		out[r.Name] = r
	}
	if len(out) != len(results) {
		t.Fatalf("%d result(s) collapsed to %d name(s); law E07 says names are unique", len(results), len(out))
	}
	return out
}

// TestMockExtractor_DefaultResultCoversEveryReasonAndTwoAlternatives (RED-FIRST): AC-3. The
// downstream stories read every field state off this one result, so each state it must carry is
// asserted by name and value rather than by "some field somewhere has this reason".
func TestMockExtractor_DefaultResultCoversEveryReasonAndTwoAlternatives(t *testing.T) {
	results := mxDefault(t)
	if len(results) == 0 {
		t.Fatal("the default result is empty; every clause below would pass vacuously")
	}

	want := map[extraction.Reason]bool{
		extraction.ReasonNone:         true,
		extraction.ReasonUnreadable:   true,
		extraction.ReasonAmbiguous:    true,
		extraction.ReasonInconsistent: true,
		extraction.ReasonMissing:      true,
	}
	got := mxReasons(results)
	for r := range want {
		if !got[r] {
			t.Errorf("the default result carries no field at reason %q; it carries %v", r, mxSortedReasons(got))
		}
	}

	by := mxByName(t, results)

	// AC-3 names these two: an inconsistent field needs a value to be inconsistent WITH.
	for _, name := range []string{"subtotal", "supplier_tin"} {
		f, ok := by[name]
		if !ok {
			t.Errorf("the default result carries no %q field", name)
			continue
		}
		if f.Reason != extraction.ReasonInconsistent {
			t.Errorf("%q is at reason %q, want %q", name, f.Reason, extraction.ReasonInconsistent)
		}
		if f.Value == nil {
			t.Errorf("%q carries a nil Value; an inconsistent field needs a reading to disagree with", name)
		}
	}

	// The missing arm is the one shape laws E08 and E10 leave legal: a nil Value.
	var missing, unreadable, clean int
	for _, r := range results {
		switch r.Reason {
		case extraction.ReasonMissing:
			missing++
			if r.Value != nil {
				t.Errorf("%q is missing yet carries the Value %q; law E10 says a missing field has none", r.Name, *r.Value)
			}
		case extraction.ReasonUnreadable:
			unreadable++
		case extraction.ReasonNone:
			clean++
		}
	}
	if missing == 0 || unreadable == 0 || clean == 0 {
		t.Errorf("the default result carries %d missing, %d unreadable and %d clean field(s); AC-3 wants at least one of each", missing, unreadable, clean)
	}

	// Exactly one field is ambiguous with exactly two alternatives, and no other field carries
	// any: an alternative belongs to an ambiguous reading alone.
	var withTwo []string
	for _, r := range results {
		switch {
		case len(r.Alternatives) == 2:
			withTwo = append(withTwo, r.Name)
			if r.Reason != extraction.ReasonAmbiguous {
				t.Errorf("%q carries two alternatives at reason %q, want %q", r.Name, r.Reason, extraction.ReasonAmbiguous)
			}
		case len(r.Alternatives) != 0:
			t.Errorf("%q carries %d alternative(s), want 0 or 2", r.Name, len(r.Alternatives))
		}
	}
	if len(withTwo) != 1 {
		t.Errorf("%d field(s) carry two alternatives (%v), want exactly 1", len(withTwo), withTwo)
	}
}

// TestMockExtractor_AlternativeRegionsDifferFromTheDecidedReading (RED-FIRST): AC-3's distinct
// regions. Without them EXTR-12-05's region-swap test would compare a fixture against itself
// and pass whatever the code did.
func TestMockExtractor_AlternativeRegionsDifferFromTheDecidedReading(t *testing.T) {
	var amb *extraction.FieldResult
	for _, r := range mxDefault(t) {
		if len(r.Alternatives) == 2 {
			amb = &r
			break
		}
	}
	if amb == nil {
		t.Fatal("no field in the default result carries two alternatives; there is no region triple to compare")
	}

	boxes := []struct {
		label  string
		region *extraction.Region
	}{
		{"the decided reading", amb.Region},
		{"alternative 1", amb.Alternatives[0].Region},
		{"alternative 2", amb.Alternatives[1].Region},
	}
	for _, b := range boxes {
		if b.region == nil {
			t.Fatalf("%q: %s carries a nil Region; the comparison below would be meaningless", amb.Name, b.label)
		}
	}
	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			if *boxes[i].region == *boxes[j].region {
				t.Errorf("%q: %s and %s share the box %+v; all three must be distinct or a region-swap cannot be observed",
					amb.Name, boxes[i].label, boxes[j].label, *boxes[i].region)
			}
		}
	}
}

// TestMockExtractor_InvoiceNumberIsUnchangedAndClean (REGRESSION GUARD): AC-5. Both fixtures
// stamp the same invoice number on purpose -- the collision is what forces a two-document run
// onto the review-batch surface (e2e/topology/import-wizard.spec.ts) and what makes DOCUP-04's
// duplicate quarantine fire (e2e/api/contract-document-upload.spec.ts). Neither deployed spec
// can be run from here, so this stands in for both.
func TestMockExtractor_InvoiceNumberIsUnchangedAndClean(t *testing.T) {
	ext := extraction.NewMockExtractor()

	var clean []extraction.FieldResult
	for _, fx := range extraction.MockFixtures() {
		if fx.Name == "clean-invoice" {
			clean = mxExtract(t, ext, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"})
		}
	}
	if clean == nil {
		t.Fatal("MockFixtures carries no \"clean-invoice\"; the fixture arm below would examine nothing")
	}

	for _, c := range []struct {
		label   string
		results []extraction.FieldResult
	}{
		{"the default result", mxDefault(t)},
		{"the clean-invoice fixture", clean},
	} {
		f, ok := mxByName(t, c.results)["invoice_number"]
		if !ok {
			t.Errorf("%s carries no invoice_number field", c.label)
			continue
		}
		if f.Value == nil {
			t.Errorf("%s: invoice_number carries no value, want %q; two deployed e2e specs key on the collision", c.label, "MOCK-INV-0001")
		} else if *f.Value != "MOCK-INV-0001" {
			t.Errorf("%s: invoice_number = %q, want %q; two deployed e2e specs key on the collision", c.label, *f.Value, "MOCK-INV-0001")
		}
		if f.Reason != extraction.ReasonNone {
			t.Errorf("%s: invoice_number is at reason %q, want clean (%q)", c.label, f.Reason, extraction.ReasonNone)
		}
		if len(f.Alternatives) != 0 {
			t.Errorf("%s: invoice_number carries %d alternative(s), want 0", c.label, len(f.Alternatives))
		}
	}
}

// mxLineNames is every recognised line-item name: the block row plus every <role> of lines
// 1-4, even roles no line actually populates (line 3's quantity). A superset allow-list, not a
// prediction of what the result carries -- the floor below is what pins the real count.
func mxLineNames() map[string]bool {
	out := map[string]bool{"line_items": true}
	for n := 1; n <= 4; n++ {
		for _, role := range extraction.LineRoles {
			out[extraction.LineFieldName(n, role)] = true
		}
	}
	return out
}

// TestMockExtractor_DefaultResultNamesAreOnTheVocabulary (RED-FIRST): AC-4, and the only honest
// oracle for it -- document_deps_test.go fences internal/importer off from this package, so an
// importer-side fixture of these names is hand-copied and self-fulfilling. MAP-11 closes the
// chain from HeaderFields to the mapper. A name outside HeaderFields is dropped by
// documentCreateInput and reaches no invoices column.
//
// EXTR-13-02 (Mode A): a name can now legally be OFF HeaderFields too -- a line-item cell.
// Two partitions, each floored, so neither side can pass over an empty set: header names must
// be on HeaderFields (7 of them), line names must be the block row or a LineFieldName shape
// (16 of them).
func TestMockExtractor_DefaultResultNamesAreOnTheVocabulary(t *testing.T) {
	vocabulary := map[string]bool{}
	for _, n := range extraction.HeaderFields {
		vocabulary[n] = true
	}
	if len(vocabulary) < 10 {
		t.Fatalf("HeaderFields carries %d name(s), want at least 10; the check below would be vacuous", len(vocabulary))
	}
	lineNames := mxLineNames()

	results := mxDefault(t)
	if len(results) == 0 {
		t.Fatal("the default result is empty; the check below would examine nothing")
	}

	var headerCount, lineCount int
	for _, r := range results {
		switch {
		case vocabulary[r.Name]:
			headerCount++
		case lineNames[r.Name]:
			lineCount++
		default:
			t.Errorf("the default result emits %q, which is neither on HeaderFields nor a recognised line-item name; documentCreateInput drops it, so it reaches no invoices column", r.Name)
		}
		// An alternative feeds the same column as the reading it competes with.
		for i, alt := range r.Alternatives {
			if alt.Name != r.Name {
				t.Errorf("%q alternative %d is named %q; an alternative is another reading of the SAME field", r.Name, i, alt.Name)
			}
		}
	}
	if headerCount != 7 {
		t.Errorf("the default result carries %d header-vocabulary field(s), want 7", headerCount)
	}
	if lineCount != 16 {
		t.Errorf("the default result carries %d line-item field(s), want 16 -- the line_items block row plus 15 populated cells", lineCount)
	}
}

// --- QA (Mode B): the arms and the shapes the AC specs above leave open ----------------

// TestMockExtractor_FixtureNamesAreOnTheVocabularyToo: the AC-4 spec above reads the DEFAULT arm
// only, so reverting either named fixture to invoice_date/total_amount leaves the whole Go suite
// green -- measured. Both fixtures feed documentCreateInput the same way the default arm does.
func TestMockExtractor_FixtureNamesAreOnTheVocabularyToo(t *testing.T) {
	vocabulary := map[string]bool{}
	for _, n := range extraction.HeaderFields {
		vocabulary[n] = true
	}
	if len(vocabulary) < 10 {
		t.Fatalf("HeaderFields carries %d name(s), want at least 10; the check below would be vacuous", len(vocabulary))
	}

	ext := extraction.NewMockExtractor()
	fixtures := extraction.MockFixtures()
	if len(fixtures) < 2 {
		t.Fatalf("MockFixtures returned %d fixture(s), want at least 2", len(fixtures))
	}

	var examined int
	for _, fx := range fixtures {
		results := mxExtract(t, ext, extraction.Document{Bytes: fx.Bytes, ContentType: "application/pdf"})
		if len(results) == 0 {
			t.Errorf("the %q fixture returned no field; its arm is unexamined", fx.Name)
			continue
		}
		for _, r := range results {
			examined++
			if !vocabulary[r.Name] {
				t.Errorf("the %q fixture emits %q, which is not in HeaderFields; documentCreateInput drops it, so it reaches no invoices column", fx.Name, r.Name)
			}
		}
	}
	if examined < 10 {
		t.Fatalf("examined %d field(s) across %d fixture(s), want at least 10", examined, len(fixtures))
	}
}

// TestMockExtractor_ReturnsFreshAlternativeMemoryPerCall: mxAssertFreshMemory clobbers the
// DECIDED reading only, so shallow-copying Alternatives in cloneFields leaves the whole package
// green -- measured. A caller mutating a returned alternative would then poison
// mockDefaultResult for every later call in the process.
func TestMockExtractor_ReturnsFreshAlternativeMemoryPerCall(t *testing.T) {
	doc := mxUnknown("fresh-alternative probe: an unrecognised document")
	ext := extraction.NewMockExtractor()

	first := mxExtract(t, ext, doc)
	var values, regions int
	for _, r := range first {
		for _, alt := range r.Alternatives {
			if alt.Value != nil {
				*alt.Value = "CLOBBERED"
				values++
			}
			if alt.Region != nil {
				alt.Region.Page = 9999
				regions++
			}
		}
	}
	if values == 0 || regions == 0 {
		t.Fatalf("clobbered %d alternative Value(s) and %d Region(s); the re-read below would prove nothing", values, regions)
	}

	second := mxExtract(t, ext, doc)
	if len(second) != len(first) {
		t.Fatalf("two calls returned %d and %d result(s)", len(first), len(second))
	}
	for _, r := range second {
		for i, alt := range r.Alternatives {
			if alt.Value != nil && *alt.Value == "CLOBBERED" {
				t.Errorf("%q alternative %d: mutating a returned *string reached the fixture table", r.Name, i)
			}
			if alt.Region != nil && alt.Region.Page == 9999 {
				t.Errorf("%q alternative %d: mutating a returned *Region reached the fixture table", r.Name, i)
			}
		}
	}

	// Pointer identity, the other half: two calls must not hand back ONE alternative pointer.
	for i := range first {
		for j := range first[i].Alternatives {
			a, b := first[i].Alternatives[j], second[i].Alternatives[j]
			if a.Value != nil && a.Value == b.Value {
				t.Errorf("%q alternative %d: two calls returned ONE *string at %p", first[i].Name, j, a.Value)
			}
			if a.Region != nil && a.Region == b.Region {
				t.Errorf("%q alternative %d: two calls returned ONE *Region at %p", first[i].Name, j, a.Region)
			}
		}
	}
}

// TestFieldResult_AlternativesMarshalAsAnArrayNeverNull: every "non-nil, never nil" clause in
// this package cites JSON, and nothing asserted the bytes. FieldResult carries no omitempty, so
// a nil slice ships `"alternatives":null` to a consumer that loops over it.
func TestFieldResult_AlternativesMarshalAsAnArrayNeverNull(t *testing.T) {
	// Control needle: the shape this guard exists to reject really does marshal to null.
	nilled, err := json.Marshal(extraction.FieldResult{Field: extraction.Field{Name: "vat"}})
	if err != nil {
		t.Fatalf("marshal the control: %v", err)
	}
	if !strings.Contains(string(nilled), `"alternatives":null`) {
		t.Fatalf("a nil Alternatives marshalled to %s; this guard is no longer reading the field it names", nilled)
	}

	results := mxDefault(t)
	if len(results) == 0 {
		t.Fatal("the default result is empty; the loop below would examine nothing")
	}
	var empty, populated int
	for _, r := range results {
		body, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal %q: %v", r.Name, err)
		}
		if strings.Contains(string(body), `"alternatives":null`) {
			t.Errorf("%q marshalled to %s; a consumer looping over alternatives gets null", r.Name, body)
		}
		if len(r.Alternatives) == 0 {
			empty++
			if !strings.Contains(string(body), `"alternatives":[]`) {
				t.Errorf("%q has no alternative yet marshalled to %s, want an empty array", r.Name, body)
			}
			continue
		}
		populated++
		if !strings.Contains(string(body), `"alternatives":[{`) {
			t.Errorf("%q carries %d alternative(s) yet marshalled to %s", r.Name, len(r.Alternatives), body)
		}
	}
	if empty == 0 || populated == 0 {
		t.Fatalf("marshalled %d empty and %d populated Alternatives; both arms must be exercised", empty, populated)
	}
}

// --- EXTR-13-02 (Mode A, RED-FIRST): the four-line block in the default result -----------

// mxLineTotalName is LineFieldName(n, line_total), spelled out at each call site below so a
// reader does not have to jump to the helper to see which line a check is about.
func mxLineTotalName(n int) string { return extraction.LineFieldName(n, extraction.LineRoleLineTotal) }

// TestMockExtractor_DefaultEmitsFourLineItemRows (RED-FIRST): Core AC 1. len(results) == 23
// first -- 7 header rows, the line_items block, 15 cells -- then the ordered names from index 7
// on must equal the exact 16-name literal, including line 3's missing quantity: a role-count
// check alone cannot express that ONE role is the hole.
func TestMockExtractor_DefaultEmitsFourLineItemRows(t *testing.T) {
	results := mxDefault(t)
	if len(results) != 23 {
		t.Fatalf("the default result carries %d field(s), want 23 -- 7 header rows, the line_items block row, and 15 line cells", len(results))
	}

	want := []string{
		"line_items",
		extraction.LineFieldName(1, extraction.LineRoleDescription),
		extraction.LineFieldName(1, extraction.LineRoleQuantity),
		extraction.LineFieldName(1, extraction.LineRoleUnitPrice),
		extraction.LineFieldName(1, extraction.LineRoleLineTotal),
		extraction.LineFieldName(2, extraction.LineRoleDescription),
		extraction.LineFieldName(2, extraction.LineRoleQuantity),
		extraction.LineFieldName(2, extraction.LineRoleUnitPrice),
		extraction.LineFieldName(2, extraction.LineRoleLineTotal),
		extraction.LineFieldName(3, extraction.LineRoleDescription),
		// no line_items[3].quantity -- the deliberate hole
		extraction.LineFieldName(3, extraction.LineRoleUnitPrice),
		extraction.LineFieldName(3, extraction.LineRoleLineTotal),
		extraction.LineFieldName(4, extraction.LineRoleDescription),
		extraction.LineFieldName(4, extraction.LineRoleQuantity),
		extraction.LineFieldName(4, extraction.LineRoleUnitPrice),
		extraction.LineFieldName(4, extraction.LineRoleLineTotal),
	}
	got := make([]string, 0, len(want))
	for _, r := range results[7:] {
		got = append(got, r.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results[7:] names = %v, want %v", got, want)
	}
}

// TestMockExtractor_LineTwoIsFlaggedInconsistent (RED-FIRST): Core AC 4. A presence floor of 4
// line_total rows first, so the reason comparisons below cannot pass by comparing zero values.
func TestMockExtractor_LineTwoIsFlaggedInconsistent(t *testing.T) {
	by := mxByName(t, mxDefault(t))

	var totals []string
	for n := 1; n <= 4; n++ {
		if _, ok := by[mxLineTotalName(n)]; ok {
			totals = append(totals, mxLineTotalName(n))
		}
	}
	if len(totals) != 4 {
		t.Fatalf("found %d line_total row(s) (%v), want 4 -- the reason checks below would examine fewer lines than the fixture carries", len(totals), totals)
	}

	for n := 1; n <= 4; n++ {
		want := extraction.ReasonNone
		if n == 2 {
			want = extraction.ReasonInconsistent
		}
		if got := by[mxLineTotalName(n)].Reason; got != want {
			t.Errorf("%s reason = %q, want %q", mxLineTotalName(n), got, want)
		}
	}
}

// TestMockExtractor_LineThreeIsPresentWithoutItsQuantity (RED-FIRST): Core AC 3's normal
// failure -- an absent cell, not a bad one. A present set alongside the absence: line 3's other
// three roles must exist before the quantity check means anything.
func TestMockExtractor_LineThreeIsPresentWithoutItsQuantity(t *testing.T) {
	results := mxDefault(t)

	present := map[string]bool{}
	for _, role := range extraction.LineRoles {
		name := extraction.LineFieldName(3, role)
		for _, r := range results {
			if r.Name == name {
				present[role] = true
				break
			}
		}
	}
	if len(present) != 3 {
		t.Fatalf("line 3 carries %d role(s) (%v), want exactly 3; the absence check below would be meaningless over a different-sized set", len(present), present)
	}
	for _, role := range []string{extraction.LineRoleDescription, extraction.LineRoleUnitPrice, extraction.LineRoleLineTotal} {
		if !present[role] {
			t.Errorf("line 3 carries no %s row; want it present", role)
		}
	}
	if present[extraction.LineRoleQuantity] {
		t.Errorf("line 3 carries a %s row; want it absent -- the deliberate missing cell", extraction.LineRoleQuantity)
	}
}

// TestMockExtractor_SubtotalDisagreesWithTheLineSum (RED-FIRST): Core AC 5. Vacuous over an
// empty line set -- sum(nothing) is 0, and |0 - 950.00| already exceeds the tolerance -- so the
// four line totals are parsed and summed to the exact expected total FIRST, and only then
// compared against subtotal.
func TestMockExtractor_SubtotalDisagreesWithTheLineSum(t *testing.T) {
	by := mxByName(t, mxDefault(t))

	var sum decimal.Decimal
	var found int
	for n := 1; n <= 4; n++ {
		f, ok := by[mxLineTotalName(n)]
		if !ok || f.Value == nil {
			continue
		}
		v, err := decimal.NewFromString(*f.Value)
		if err != nil {
			t.Fatalf("%s = %q, not parseable as money: %v", mxLineTotalName(n), *f.Value, err)
		}
		sum = sum.Add(v)
		found++
	}
	if found != 4 {
		t.Fatalf("parsed %d line_total value(s), want 4 -- the sum below would be over an incomplete set", found)
	}
	wantSum := decimal.RequireFromString("2095.50")
	if !sum.Equal(wantSum) {
		t.Fatalf("the four line totals sum to %s, want %s", sum, wantSum)
	}

	subtotal, ok := by["subtotal"]
	if !ok || subtotal.Value == nil {
		t.Fatal("the default result carries no subtotal value; the disagreement check below would be meaningless")
	}
	sv, err := decimal.NewFromString(*subtotal.Value)
	if err != nil {
		t.Fatalf("subtotal = %q, not parseable as money: %v", *subtotal.Value, err)
	}
	tol := decimal.RequireFromString("0.01")
	diff := sv.Sub(sum).Abs()
	if !diff.GreaterThan(tol) {
		t.Errorf("subtotal %s and the line sum %s disagree by %s, want more than %s", sv, sum, diff, tol)
	}
}

// TestMockExtractor_FifteenLineCellsAtFifteenDistinctBoxes (RED-FIRST): Core AC 6. The count of
// 15 is asserted first, then every region is proven non-nil and on page 1 -- a nil-region set
// would otherwise read as "distinct" by the pairwise comparison that follows.
func TestMockExtractor_FifteenLineCellsAtFifteenDistinctBoxes(t *testing.T) {
	lineNames := map[string]bool{}
	for n := 1; n <= 4; n++ {
		for _, role := range extraction.LineRoles {
			lineNames[extraction.LineFieldName(n, role)] = true
		}
	}

	var cells []extraction.FieldResult
	for _, r := range mxDefault(t) {
		if lineNames[r.Name] {
			cells = append(cells, r)
		}
	}
	if len(cells) != 15 {
		t.Fatalf("found %d line cell(s), want 15 -- the distinctness check below would examine the wrong population", len(cells))
	}

	for _, c := range cells {
		if c.Region == nil {
			t.Fatalf("%s carries a nil Region; the distinctness check below would be meaningless", c.Name)
		}
		if c.Region.Page != 1 {
			t.Errorf("%s.Region.Page = %d, want 1", c.Name, c.Region.Page)
		}
	}

	for i := range cells {
		for j := i + 1; j < len(cells); j++ {
			if *cells[i].Region == *cells[j].Region {
				t.Errorf("%s and %s share the box %+v; every cell must sit at its own box", cells[i].Name, cells[j].Name, *cells[i].Region)
			}
		}
	}
}
