// docling_extractor_test.go: T-06-1..8. DoclingExtractor.Extract is a stub (docling_extractor.go)
// until the accompanying feat commit lands, so every behaviour spec here is red against it -- on
// the target assertion, not a compile error. The two declaration-level specs (T-06-3, T-06-8) are
// already green: they pin what must NOT change, not what Extract does.
//
// Reuses docling_test.go's stub-server plumbing (dcServer, dcNewReader, dcBody, dcPage,
// dcSimplePage, dcMarshal) rather than rebuilding it.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// deTextLayerField mirrors peTextLayerField (pdfium_extractor_test.go): the one field name this
// extractor emits, written out rather than read from the package.
const deTextLayerField = "document_text_layer"

// deCountingReader mirrors peCountingReader: a PageReader double that counts its calls and
// ignores the context, so T-06-6 can prove the reader was never reached rather than merely
// that Extract returned an error.
type deCountingReader struct {
	reads  int
	result extraction.PageResult
}

func (*deCountingReader) Name() string    { return "counting" }
func (*deCountingReader) Version() string { return "v0" }

func (r *deCountingReader) Read(context.Context, extraction.Document, func(extraction.Page) error) (extraction.PageResult, error) {
	r.reads++
	return r.result, nil
}

// --- T-06-1/T-06-2: the twelve-law contract, over a stub sidecar --------------

// TestDoclingExtractor_PassesTheContract runs the twelve-law suite against a stub Docling
// server that always answers with a token-bearing page. The law check alone can pass
// vacuously -- E04-E11 only examine a successful result and E12 wants an error either way, so
// an extractor that dispatches nothing still trips nothing. The stub's request count is what
// turns that "zero violations" into a real one (T-06-2).
func TestDoclingExtractor_PassesTheContract(t *testing.T) {
	var requests atomic.Int64
	body := dcMarshal(t, dcBody{Pages: []dcPage{dcSimplePage(1)}})
	srv := dcServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})

	built := 0
	newDoclingExtractor := func() extraction.Extractor {
		built++
		ext, err := extraction.NewDoclingExtractor(srv.URL)
		if err != nil {
			t.Fatalf("NewDoclingExtractor(%q): %v", srv.URL, err)
		}
		return ext
	}

	RunExtractorContract(t, newDoclingExtractor)

	if built < 2 {
		t.Fatalf("the suite built %d extractor(s), want at least 2; a run that built one exercised no cross-instance law and the green above says nothing", built)
	}
	// T-06-2: at least 9, one per document-driving law's newCorpus() call (contract_test.go
	// calls it exactly 9 times, for E04-E12). A connection-refused extractor would record 0
	// requests and pass the law check above vacuously.
	if n := requests.Load(); n < 9 {
		t.Errorf("the stub server recorded %d request(s), want at least 9 -- one per document-driving law; a connection-refused run would record 0 and pass the check above vacuously", n)
	}
}

// --- T-06-3: the contract suite files are untouched ---------------------------

// deLockedContractFiles pins each file's SHA-256, computed once against the committed suite.
// EXTR-03-06's whole point is that DoclingExtractor passes RunExtractorContract WITHOUT
// changing it -- a hash drift here means the suite moved, not that DoclingExtractor did.
var deLockedContractFiles = map[string]string{
	"contract_test.go":     "a3f65a895519acf0078506efb4b4f095c3ffee79c514c5d933a379f4d2b5daa8",
	"contract_red_test.go": "cade0ce8c31008db557a5b93af5f6cdd371b1c865142e9facd0551d4189d2efd",
}

// deContractFileMinBytes floors a read: a typo'd path or a truncated file would otherwise
// hash to something that just happens not to collide, and pass for the wrong reason.
const deContractFileMinBytes = 10_000

func TestDoclingExtractor_ContractSuiteFilesAreUntouched(t *testing.T) {
	if len(deLockedContractFiles) != 2 {
		t.Fatalf("deLockedContractFiles pins %d file(s), want exactly 2", len(deLockedContractFiles))
	}
	for name, want := range deLockedContractFiles {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(body) < deContractFileMinBytes {
			t.Fatalf("%s is %d byte(s), want at least %d; the digest below would prove nothing about a read that silently failed or found the wrong file", name, len(body), deContractFileMinBytes)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
			t.Errorf("%s hashes to %s, want the pinned %s -- if this change is deliberate, review the diff and update deLockedContractFiles", name, got, want)
		}
	}
}

// --- T-06-4/T-06-5: the document-level verdict ---------------------------------

// T-06-4: a stub page carrying zero tokens.
func TestDoclingExtractor_ReportsAZeroTokenPageAsUnreadable(t *testing.T) {
	body := dcMarshal(t, dcBody{Pages: []dcPage{
		{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []dcToken{}, Tables: []dcTable{}},
	}})
	srv := dcServer(t, dcJSONHandler(body))
	ext := dcNewExtractor(t, srv.URL)

	fields, err := ext.Extract(t.Context(), dcDoc("x"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("Extract returned %d field(s), want exactly 1: %+v", len(fields), fields)
	}

	f := fields[0]
	if f.Name != deTextLayerField {
		t.Errorf("the field is named %q, want %q", f.Name, deTextLayerField)
	}
	if f.Reason != extraction.ReasonUnreadable {
		t.Errorf("the field carries Reason %q, want %q", f.Reason, extraction.ReasonUnreadable)
	}
	if f.Value != nil {
		t.Errorf("the field carries the Value %q, want nil: nothing was read to put there", *f.Value)
	}
	if f.Region != nil {
		t.Errorf("the field carries the Region %+v, want nil: the verdict is document-level, not a box", *f.Region)
	}
}

// T-06-5: a stub page carrying tokens. Law E04 refuses a nil slice alongside a nil error, and
// EXTR-04 fills this one.
func TestDoclingExtractor_TokenBearingPageYieldsAnEmptyNonNilSlice(t *testing.T) {
	body := dcMarshal(t, dcBody{Pages: []dcPage{dcSimplePage(1)}})
	srv := dcServer(t, dcJSONHandler(body))
	ext := dcNewExtractor(t, srv.URL)

	fields, err := ext.Extract(t.Context(), dcDoc("x"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if fields == nil {
		t.Fatalf("Extract returned a nil slice alongside a nil error; success is an empty NON-NIL slice")
	}
	if len(fields) != 0 {
		t.Errorf("Extract returned %d field(s), want 0: this story reads no invoice field, it only decides whether there is text at all: %+v", len(fields), fields)
	}
}

// dcNewExtractor mirrors dcNewReader: builds a DoclingExtractor over srv.URL, failing the test
// on a malformed URL rather than a downstream nil-pointer panic.
func dcNewExtractor(t *testing.T, baseURL string) *extraction.DoclingExtractor {
	t.Helper()
	ext, err := extraction.NewDoclingExtractor(baseURL)
	if err != nil {
		t.Fatalf("NewDoclingExtractor(%q): %v", baseURL, err)
	}
	return ext
}

// --- T-06-6: cancellation is checked before the reader -------------------------

// T-06-6: an already-cancelled context yields an error and a nil slice, and the composed
// reader is never reached. The oracle is the reader's call count, not the clock -- mirrors
// TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool.
func TestDoclingExtractor_ChecksCancellationBeforeTheReader(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	reader := &deCountingReader{result: extraction.PageResult{Pages: 1, TextChars: 1, PagesWithText: 1}}
	ext := extraction.NewDoclingExtractorWithReaderForTest(reader)
	doc := dcDoc("x")

	fields, err := ext.Extract(ctx, doc)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Extract on an already-cancelled context returned the error %v, want %v", err, context.Canceled)
	}
	if fields != nil {
		t.Errorf("Extract on an already-cancelled context returned %d field(s), want a nil slice", len(fields))
	}
	if reader.reads != 0 {
		t.Errorf("Extract reached the reader %d time(s) on an already-cancelled context, want 0: the check must be the first statement, or a cancelled job still pays for the read", reader.reads)
	}

	// Floor: a reader Extract could never reach reports zero above whatever the source does.
	if _, err := ext.Extract(t.Context(), doc); err != nil {
		t.Fatalf("Extract on a live context: %v", err)
	}
	if reader.reads != 1 {
		t.Fatalf("Extract reached the substitute reader %d time(s) on a live context, want 1; the zero above is vacuous", reader.reads)
	}
}

// --- T-06-7: only the pointer satisfies Extractor ------------------------------

// TestDoclingExtractor_OnlyThePointerSatisfiesExtractor (REGRESSION GUARD): var _ Extractor =
// (*DoclingExtractor)(nil) is satisfied whether or not the value type also implements, so it
// cannot see a receiver change. Mirrors TestPDFiumExtractor_OnlyThePointerSatisfiesExtractor.
func TestDoclingExtractor_OnlyThePointerSatisfiesExtractor(t *testing.T) {
	iface := reflect.TypeOf((*extraction.Extractor)(nil)).Elem()
	if iface.NumMethod() == 0 {
		t.Fatalf("extraction.Extractor declares no method; Implements would be true of every type")
	}

	ext := extraction.NewDoclingExtractorWithReaderForTest(&deCountingReader{})
	ptr := reflect.TypeOf(ext)
	if ptr.Kind() != reflect.Ptr {
		t.Fatalf("NewDoclingExtractorWithReaderForTest returned %s, want a pointer", ptr)
	}
	if !ptr.Implements(iface) {
		t.Fatalf("%s does not satisfy Extractor; the %d method(s) it needs are %v", ptr, iface.NumMethod(), mxMethodNames(iface))
	}
	if val := ptr.Elem(); val.Implements(iface) {
		t.Errorf("the VALUE type %s satisfies Extractor too; every method must take a POINTER receiver, or a copied DoclingExtractor silently satisfies the seam", val)
	}
}

// --- T-06-8: no invoice field name leaks into the source -----------------------

const doclingExtractorFile = "docling_extractor.go"

// deFieldNameLiteralRE matches a snake_case identifier shape: lowercase, digits and
// underscores, at least one underscore. The stub's own error string ("docling: Extract not
// implemented") carries a colon, spaces and an uppercase letter, so it cannot match.
var deFieldNameLiteralRE = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)

// deFieldNameLiterals returns every field-name-shaped string literal in docling_extractor.go,
// deduplicated.
func deFieldNameLiterals(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, doclingExtractorFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", doclingExtractorFile, err)
	}

	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if deFieldNameLiteralRE.MatchString(s) {
			found[s] = true
		}
		return true
	})
	return found
}

// TestDoclingExtractor_SourceNamesOnlyTheTextLayerField (T-06-8): the only field-name-shaped
// string literal anywhere in docling_extractor.go is "document_text_layer". A second one would
// be an invoice field name leaking into a document-level verdict -- EXTR-04's vocabulary
// belongs to a different extractor.
func TestDoclingExtractor_SourceNamesOnlyTheTextLayerField(t *testing.T) {
	found := deFieldNameLiterals(t)
	if len(found) == 0 {
		t.Fatalf("found 0 field-name-shaped string literal(s) in %s; the scan below would pass vacuously", doclingExtractorFile)
	}
	if !found[deTextLayerField] {
		t.Errorf("%s carries no %q literal; the field-name constant itself is missing or was renamed", doclingExtractorFile, deTextLayerField)
	}
	if len(found) != 1 {
		t.Errorf("%s carries %d field-name-shaped string literal(s) %v, want exactly 1: %q", doclingExtractorFile, len(found), found, deTextLayerField)
	}

	// A literal scan cannot catch a CALL. Control first: the walker really does find an
	// identifier this file uses (its own type), so an empty result below is not a broken walk.
	control := xForbiddenIdentifiers(t, doclingExtractorFile, "DoclingExtractor")
	if !control["DoclingExtractor"] {
		t.Fatalf("%s never references its own DoclingExtractor identifier; the AST walk below is broken", doclingExtractorFile)
	}
	if forbidden := xForbiddenIdentifiers(t, doclingExtractorFile, "LineItemResults", "LineFieldName", "LineItems", "DocLine"); len(forbidden) != 0 {
		t.Errorf("%s references %v; EXTR-12's fork F-3 keeps this extractor off the line-item API", doclingExtractorFile, forbidden)
	}
}

// xForbiddenIdentifiers parses file and reports which of the given identifier names appear
// anywhere in its AST. Shared with TestPDFiumExtractor_SourceNamesOnlyTheTextLayerField: a
// string-literal scan alone cannot catch a call to a forbidden function.
func xForbiddenIdentifiers(t *testing.T, file string, names ...string) map[string]bool {
	t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && want[id.Name] {
			found[id.Name] = true
		}
		return true
	})
	return found
}
