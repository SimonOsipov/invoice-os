// pdfium_extractor_test.go: AC-6 and AC-7. The verdict is document-level (D-9), so every spec
// below drives the whole fixture and reads the whole result.
//
// The corpus helpers are pdfium_test.go's ptDoc and ptRead, reused: a floor that measures what
// the reader actually saw is what stops each verdict below from passing on a fixture that
// stopped being what its name says.
package extraction_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// peTextLayerField is the one field name this extractor emits, written out rather than read
// from the package: it is persisted as extraction_field_results.field_name, so a rename is a
// decision and not a refactor.
const peTextLayerField = "document_text_layer"

// --- harness ----------------------------------------------------------------

func peExtract(t *testing.T, name string) []extraction.FieldResult {
	t.Helper()

	fields, err := extraction.NewPDFiumExtractor().Extract(t.Context(), ptDoc(t, name))
	if err != nil {
		t.Fatalf("Extract(%s): %v", name, err)
	}
	return fields
}

// peCountingReader is a PageReader double that counts its calls and ignores the context. A
// reader that honoured cancellation would return the same error whether or not Extract checked
// first, which is the whole point of the counter.
type peCountingReader struct {
	reads  int
	result extraction.PageResult
}

func (*peCountingReader) Name() string    { return "counting" }
func (*peCountingReader) Version() string { return "v0" }

func (r *peCountingReader) Read(context.Context, extraction.Document, func(extraction.Page) error) (extraction.PageResult, error) {
	r.reads++
	return r.result, nil
}

// --- the tests --------------------------------------------------------------

// TestPDFiumExtractor_PassesTheContract: AC-7. *testing.T satisfies ContractT, so a violation is
// reported at its own law's message. Mirrors TestMockExtractor_PassesTheContract.
func TestPDFiumExtractor_PassesTheContract(t *testing.T) {
	built := 0
	RunExtractorContract(t, func() extraction.Extractor {
		built++
		return extraction.NewPDFiumExtractor()
	})

	if built < 2 {
		t.Fatalf("the suite built %d extractor(s), want at least 2; a run that built one exercised no cross-instance law and the green above says nothing", built)
	}
}

// TestPDFiumExtractor_ReportsAScanAsUnreadable: AC-6. One field, no value and no box -- the
// shape writeFieldResultsTx binds as reason_code 'unreadable' with all five region columns NULL.
func TestPDFiumExtractor_ReportsAScanAsUnreadable(t *testing.T) {
	// Floor: the verdict is only about a scan while the fixture still reads as one page of no
	// text. A fixture pdfium opened as zero pages would reach the same verdict and mean nothing.
	if _, res := ptRead(t, fxScanned); res.Pages != 1 || res.TextChars != 0 {
		t.Fatalf("%s read as %d page(s) carrying %d character(s), want 1 page and 0 characters", fxScanned, res.Pages, res.TextChars)
	}

	fields := peExtract(t, fxScanned)
	if len(fields) != 1 {
		t.Fatalf("Extract(%s) returned %d field(s), want exactly 1: %+v", fxScanned, len(fields), fields)
	}

	f := fields[0]
	if f.Name != peTextLayerField {
		t.Errorf("the field is named %q, want %q", f.Name, peTextLayerField)
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

// TestPDFiumExtractor_NativePDFYieldsAnEmptyNonNilSlice: AC-6's other half. Law E04 refuses a
// nil slice alongside a nil error, and EXTR-04 fills this one.
func TestPDFiumExtractor_NativePDFYieldsAnEmptyNonNilSlice(t *testing.T) {
	// Floor: an empty result is only meaningful while the fixture still carries text.
	if _, res := ptRead(t, fxNative); res.TextChars == 0 {
		t.Fatalf("%s read as %d character(s), want more than 0; a text-free fixture makes the empty result below the SCAN verdict instead", fxNative, res.TextChars)
	}

	fields := peExtract(t, fxNative)
	if fields == nil {
		t.Fatalf("Extract(%s) returned a nil slice alongside a nil error; success is an empty NON-NIL slice", fxNative)
	}
	if len(fields) != 0 {
		t.Errorf("Extract(%s) returned %d field(s), want 0: this story reads no invoice field, it only decides whether there is text at all: %+v", fxNative, len(fields), fields)
	}
}

// TestPDFiumExtractor_DoesNotFlagAHybridDocument pins the unowned gap in D-9: the verdict is
// document-level, so a document with one native page and one image-only page is not flagged.
// The day someone closes that gap this spec fails, which is what makes the change deliberate.
func TestPDFiumExtractor_DoesNotFlagAHybridDocument(t *testing.T) {
	// The half that makes this a pin and not a tautology: the reader really does see one page
	// of two carrying text, and Extract really does decline to act on it.
	_, res := ptRead(t, fxHybrid)
	if res.Pages != 2 || res.PagesWithText != 1 {
		t.Fatalf("%s read as %d of %d page(s) carrying text, want 1 of 2; without a mixed document this spec pins nothing", fxHybrid, res.PagesWithText, res.Pages)
	}

	fields := peExtract(t, fxHybrid)
	if fields == nil {
		t.Fatalf("Extract(%s) returned a nil slice alongside a nil error; success is an empty NON-NIL slice", fxHybrid)
	}
	if len(fields) != 0 {
		t.Errorf("Extract(%s) returned %d field(s), want 0: PagesWithText %d of %d is carried, not acted on: %+v", fxHybrid, len(fields), res.PagesWithText, res.Pages, fields)
	}
}

// TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool: law E12 over the 15 MiB case. The
// oracle is the reader call count, not the clock: PDFiumReader.Read tests ctx.Err() first too,
// so a cancelled call returns the same error whether or not Extract checked, and the pool is
// already warm from an earlier test in this binary.
func TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	reader := &peCountingReader{result: extraction.PageResult{Pages: 1, TextChars: 1, PagesWithText: 1}}
	ext := extraction.NewPDFiumExtractorWithReaderForTest(reader)
	doc := ptDoc(t, fxNative)

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

// TestPDFiumExtractor_PinsNameAndVersion: both are persisted as extraction_jobs.extractor /
// .extractor_version, so a drifting value orphans every stored row. Mirrors
// TestMockExtractor_PinsNameAndVersion.
func TestPDFiumExtractor_PinsNameAndVersion(t *testing.T) {
	first, second := extraction.NewPDFiumExtractor(), extraction.NewPDFiumExtractor()

	if got := first.Name(); got != "pdfium" {
		t.Errorf("Name() is %q, want %q", got, "pdfium")
	}
	if got := first.Version(); got != "v1" {
		t.Errorf("Version() is %q, want %q", got, "v1")
	}
	if first.Name() != second.Name() || first.Version() != second.Version() {
		t.Errorf("a second extractor reports %q/%q, want %q/%q", second.Name(), second.Version(), first.Name(), first.Version())
	}
}

// TestPDFiumExtractor_OnlyThePointerSatisfiesExtractor (REGRESSION GUARD): var _ Extractor =
// (*PDFiumExtractor)(nil) is satisfied whether or not the value type also implements, so it
// cannot see a receiver change. Mirrors TestMockExtractor_OnlyThePointerSatisfiesExtractor.
func TestPDFiumExtractor_OnlyThePointerSatisfiesExtractor(t *testing.T) {
	iface := reflect.TypeOf((*extraction.Extractor)(nil)).Elem()
	if iface.NumMethod() == 0 {
		t.Fatalf("extraction.Extractor declares no method; Implements would be true of every type")
	}

	ptr := reflect.TypeOf(extraction.NewPDFiumExtractor())
	if ptr.Kind() != reflect.Ptr {
		t.Fatalf("NewPDFiumExtractor returned %s, want a pointer", ptr)
	}
	if !ptr.Implements(iface) {
		t.Fatalf("%s does not satisfy Extractor; the %d method(s) it needs are %v", ptr, iface.NumMethod(), mxMethodNames(iface))
	}
	if val := ptr.Elem(); val.Implements(iface) {
		t.Errorf("the VALUE type %s satisfies Extractor too; every method must take a POINTER receiver, or a copied PDFiumExtractor silently satisfies the seam", val)
	}
}

// --- EXTR-13-02 (Mode A, RED-FIRST): the boundary the mock's new line items must not cross --

const pdfiumExtractorFile = "pdfium_extractor.go"

// peFieldNameLiteralRE mirrors deFieldNameLiteralRE (docling_extractor_test.go): a snake_case
// identifier shape, lowercase, digits and underscores, at least one underscore.
var peFieldNameLiteralRE = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)

// peFieldNameLiterals returns every field-name-shaped string literal in pdfium_extractor.go,
// deduplicated. Mirrors deFieldNameLiterals.
func peFieldNameLiterals(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, pdfiumExtractorFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", pdfiumExtractorFile, err)
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
		if peFieldNameLiteralRE.MatchString(s) {
			found[s] = true
		}
		return true
	})
	return found
}

// TestPDFiumExtractor_SourceNamesOnlyTheTextLayerField: the missing sibling of
// TestDoclingExtractor_SourceNamesOnlyTheTextLayerField -- the only field-name-shaped string
// literal in pdfium_extractor.go is "document_text_layer", and the file calls none of the
// line-item API. EXTR-12's fork F-3 keeps both real extractors off the line-item vocabulary
// this subtask adds to the mock.
func TestPDFiumExtractor_SourceNamesOnlyTheTextLayerField(t *testing.T) {
	found := peFieldNameLiterals(t)
	if len(found) == 0 {
		t.Fatalf("found 0 field-name-shaped string literal(s) in %s; the scan below would pass vacuously", pdfiumExtractorFile)
	}
	if !found[peTextLayerField] {
		t.Errorf("%s carries no %q literal; the field-name constant itself is missing or was renamed", pdfiumExtractorFile, peTextLayerField)
	}
	if len(found) != 1 {
		t.Errorf("%s carries %d field-name-shaped string literal(s) %v, want exactly 1: %q", pdfiumExtractorFile, len(found), found, peTextLayerField)
	}

	// Control first: the walker really does find an identifier this file uses (its own type).
	control := xForbiddenIdentifiers(t, pdfiumExtractorFile, "PDFiumExtractor")
	if !control["PDFiumExtractor"] {
		t.Fatalf("%s never references its own PDFiumExtractor identifier; the AST walk below is broken", pdfiumExtractorFile)
	}
	if forbidden := xForbiddenIdentifiers(t, pdfiumExtractorFile, "LineItemResults", "LineFieldName", "LineItems", "DocLine"); len(forbidden) != 0 {
		t.Errorf("%s references %v; EXTR-12's fork F-3 keeps this extractor off the line-item API", pdfiumExtractorFile, forbidden)
	}
}
