// mock_test.go: MockExtractor's nine specs -- the twelve-law contract run, determinism in three
// shapes, distinct inputs, AC-3's reason matrix, the ambient-dependency scan, non-mutation,
// fresh memory per call, the pinned Name/Version literals, and MockFixtures handing back a copy.
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

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const mockSourceFile = "mock.go"

// mxUnknown is bytes no fixture claims, so Extract takes the default arm.
func mxUnknown(s string) extraction.Document {
	return extraction.Document{Bytes: []byte(s), ContentType: "application/pdf"}
}

func mxExtract(t *testing.T, ext extraction.Extractor, doc extraction.Document) []extraction.Field {
	t.Helper()
	fields, err := ext.Extract(context.Background(), doc)
	if err != nil {
		t.Fatalf("Extract(%d bytes): unexpected error %v", len(doc.Bytes), err)
	}
	return fields
}

func mxReasons(fields []extraction.Field) map[extraction.Reason]bool {
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
	results := map[string][]extraction.Field{}
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
// caller the table's own memory, so a caller mutating one result would corrupt the next. Address
// inequality is only a proxy; the clobber-and-ask-again below is the oracle, and it covers the
// slice backing array as well as the pointers.
func TestMockExtractor_ReturnsFreshMemoryPerCall(t *testing.T) {
	ext := extraction.NewMockExtractor()
	doc := mxUnknown("freshness probe")

	first := mxExtract(t, ext, doc)
	if len(first) == 0 {
		t.Fatalf("the default result is empty; the mutations below would prove nothing")
	}

	pristine := make([]extraction.Field, len(first))
	copy(pristine, first)
	var pristineValues []string
	var pristineRegions int
	for _, f := range first {
		if f.Value != nil {
			pristineValues = append(pristineValues, *f.Value)
		}
		if f.Region != nil {
			pristineRegions++
		}
	}
	if len(pristineValues) == 0 {
		t.Fatalf("no field in the default result carries a Value; the *string mutation below would prove nothing")
	}
	if pristineRegions == 0 {
		t.Fatalf("no field in the default result carries a Region; the *Region mutation below would prove nothing")
	}

	second := mxExtract(t, ext, doc)
	if len(second) != len(first) {
		t.Fatalf("two calls on one instance returned %d and %d field(s)", len(first), len(second))
	}
	for i := range first {
		if first[i].Value != nil && first[i].Value == second[i].Value {
			t.Errorf("field %d: two calls returned ONE *string at %p", i, first[i].Value)
		}
		if first[i].Region != nil && first[i].Region == second[i].Region {
			t.Errorf("field %d: two calls returned ONE *Region at %p", i, first[i].Region)
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
		t.Fatalf("the third call returned %d field(s), the first returned %d", len(third), len(pristine))
	}
	if third[0].Name != pristine[0].Name {
		t.Errorf("mutating a returned Field.Name changed the next call's field 0 to %q, want %q", third[0].Name, pristine[0].Name)
	}
	var seen int
	for _, f := range third {
		if f.Value != nil {
			if *f.Value == "CLOBBERED" {
				t.Errorf("mutating a returned *string changed the next call's %q to %q", f.Name, *f.Value)
			}
			seen++
		}
		if f.Region != nil && f.Region.Page == 9999 {
			t.Errorf("mutating a returned *Region changed the next call's %q to Page %d", f.Name, f.Region.Page)
		}
	}
	if seen != len(pristineValues) {
		t.Errorf("the third call carries %d Value(s), the first carried %d", seen, len(pristineValues))
	}
}

// TestMockExtractor_PinsNameAndVersion (PIN): laws E01 and E02 accept any non-empty string that
// does not change within one run, so a rename passes all twelve laws and all seven behavioural
// specs while silently rewriting what extraction_jobs.extractor and .extractor_version mean for
// every row already stored under the old value.
func TestMockExtractor_PinsNameAndVersion(t *testing.T) {
	ext := extraction.NewMockExtractor()
	if got := ext.Name(); got != "mock" {
		t.Errorf("Name() = %q, want %q; the value is persisted as extraction_jobs.extractor and changing it orphans every existing row", got, "mock")
	}
	if got := ext.Version(); got != "v1" {
		t.Errorf("Version() = %q, want %q; the value is persisted as extraction_jobs.extractor_version", got, "v1")
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
	pristine := make([][]extraction.Field, len(first))
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
