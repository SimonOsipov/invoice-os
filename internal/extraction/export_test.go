// export_test.go: the DB-backed worker suite lives in package extraction_test, which cannot
// name the unexported args type. These constructors hand it a value of that type instead.
// Compiled only under go test, so the production surface is unchanged.
package extraction

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// AppendCorrectionForTest and LatestCorrectionsPerFieldForTest hand the external test package
// the tx-taking halves of the correction record. The exported wrappers they replace opened a
// db.WithinTenantTx of their own -- the worker posture, on a request-path table -- so each
// caller now opens its own.
func AppendCorrectionForTest(ctx context.Context, tx pgx.Tx, tenantID, jobID string, c Correction) (Correction, error) {
	return appendCorrectionTx(ctx, tx, tenantID, jobID, c)
}

func LatestCorrectionsPerFieldForTest(ctx context.Context, tx pgx.Tx, jobID string) ([]Correction, error) {
	return latestCorrectionsPerFieldTx(ctx, tx, jobID)
}

// PageOneReadTimeoutForTest hands the external handler specs the page-read bound.
const PageOneReadTimeoutForTest = pageOneReadTimeout

// NewExtractArgsForTest builds the args EnqueueTx takes. The return type is river.JobArgs, so
// the caller never writes the concrete name.
func NewExtractArgsForTest(tenantID, documentID, key string) river.JobArgs {
	return extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key}
}

// NewExtractJobForTest builds the job Work takes, for the specs that call Work directly
// rather than through a River client.
func NewExtractJobForTest(riverJobID int64, attempt, maxAttempts int, tenantID, documentID, key string) *river.Job[extractArgs] {
	return &river.Job[extractArgs]{
		JobRow: &rivertype.JobRow{ID: riverJobID, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key},
	}
}

// NewPDFiumReaderAtDPIForTest builds a reader that renders at a non-default DPI. AC-4's
// alignment sweep runs 100/150/200 to prove the box space and the pixel space are one space at
// any resolution.
func NewPDFiumReaderAtDPIForTest(dpi int) *PDFiumReader { return &PDFiumReader{dpi: dpi} }

// PDFiumCleanupsForTest reads the render-bitmap release counter.
func PDFiumCleanupsForTest() int64 { return pdfiumCleanups.Load() }

// NewPDFiumExtractorWithReaderForTest builds an extractor over a substitute PageReader.
// TestPDFiumExtractor_ChecksCancellationBeforeTheWasmPool counts the calls that reach it.
func NewPDFiumExtractorWithReaderForTest(r PageReader) *PDFiumExtractor {
	return &PDFiumExtractor{reader: r}
}

// NewDoclingExtractorWithReaderForTest builds an extractor over a substitute PageReader,
// bypassing NewDoclingExtractor's baseURL requirement.
// TestDoclingExtractor_ChecksCancellationBeforeTheReader counts the calls that reach it.
func NewDoclingExtractorWithReaderForTest(r PageReader) *DoclingExtractor {
	return &DoclingExtractor{reader: r}
}

// ColumnBandForTest exposes the band Fingerprint sorts and hashes by, so the corpus specs
// assert the production thirds rather than a reimplementation of them.
func ColumnBandForTest(r Region) int { return columnBand(r) }

// LabelViewForTest hands external specs the production label view.
func LabelViewForTest(text string) string { return labelView(text) }

// AnchorLabelIDsForTest returns every anchorLexicon ID whose pattern matches text.
func AnchorLabelIDsForTest(text string) []string {
	var ids []string
	for _, m := range anchorLabelMatchers {
		if m.RE.MatchString(text) {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// AnchorLabelPlacementsForTest returns "<id>:<placement>" for every anchorLexicon pattern
// matching text, in BoxlessFingerprint's own (token, matcher) order. Sibling of
// AnchorLabelIDsForTest: labelPlacement is unexported and every fingerprint spec is external,
// so this hands them the production classifier instead of a copy of it.
func AnchorLabelPlacementsForTest(text string) []string {
	var out []string
	for _, m := range anchorLabelMatchers {
		if loc := m.RE.FindStringIndex(text); loc != nil {
			out = append(out, m.ID+":"+labelPlacement(text, loc))
		}
	}
	return out
}

// PartyOrderForTest hands the external test package the party partition. The corpus specs read
// a real fixture, and only an external test may do that: reading one builds the pdfium pool,
// which TestPDFiumPool_NotBuiltOnACancelledContext fatals on if an internal test gets there
// first.
func PartyOrderForTest(page TokenPage) []Party { return partyOrder(page) }

// MaxCandidatesPerFieldForTest exposes the per-field cap so V-13 asserts the production
// constant rather than a copy of it.
const MaxCandidatesPerFieldForTest = maxCandidatesPerField

// Tier1MaxDistanceRightForTest / Tier1MaxDistanceBelowForTest expose the two distance dials so
// TestTier1_DialsStayInsideTheirMeasuredWindow bounds the production constants rather than a
// copy of them.
const (
	Tier1MaxDistanceRightForTest = tier1MaxDistanceRight
	Tier1MaxDistanceBelowForTest = tier1MaxDistanceBelow
)

// Tier1DropRightForTest exposes the drop-band dial so
// TestTier1_TheDropBandStaysInsideItsMeasuredWindow bounds the production constant rather than a
// copy of it.
const Tier1DropRightForTest = tier1DropRight

// RightGapForTest mirrors the right relation's box gap for a pair past the dial.
// TestAdvisory_TheLabelValueGapsAreRecorded welds it to Resolve.
func RightGapForTest(anchor, value Region) float64 { return value.X0 - anchor.X1 }

// RelationClausesForTest wraps the production clause predicate so external specs assert the
// clause production applies rather than a reimplementation of the conjuncts.
func RelationClausesForTest(anchor, value Region, kind RelationKind, maxDistance, drop float64) (order, distance, overlap bool) {
	_, _, order, distance, overlap = relationClauses(anchor, value, Relation{Kind: kind, MaxDistance: maxDistance}, drop)
	return order, distance, overlap
}

// MaxUploadBytesForTest exposes the request-body cap so the 413 spec asserts the production
// constant rather than a copy of it.
const MaxUploadBytesForTest = maxUploadBytes

// AppendAnchorRuleForTest and JobLayoutForTest hand the external test package the tx-taking
// halves of the anchor-rule writer. Each caller opens its own db.WithinTenantTx.
func AppendAnchorRuleForTest(ctx context.Context, tx pgx.Tx, tenantID, fingerprint string, lr LearnedRule) (string, error) {
	return appendAnchorRuleTx(ctx, tx, tenantID, fingerprint, lr)
}

func JobLayoutForTest(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (JobLayout, bool, error) {
	return jobLayoutTx(ctx, tx, tenantID, jobID)
}

// JobLayoutTokensForTest exposes the boxless derivation's own reader. The correction route can
// never reach a foreign job -- the document lookup 404s first -- so its tenant predicate and its
// error mapping have no oracle above this seam (TestJobLayoutTokens_*).
func JobLayoutTokensForTest(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]string, bool, error) {
	return jobLayoutTokensTx(ctx, tx, tenantID, jobID)
}

// MaxLayoutTokensJSONForTest and LayoutTokensStorableForTest hand the external test package
// EXTR-19-06's cap and gate: worker_db_test.go is package extraction_test and can name neither.
const MaxLayoutTokensJSONForTest = maxLayoutTokensJSON

func LayoutTokensStorableForTest(tokens []string) ([]byte, bool) { return layoutTokensStorable(tokens) }

// PDFiumStructuredPageForTest is one page's raw rects and chars at whatever mode the caller
// requested. Raw, not converted to Token: the charsIn specs need PointPosition in chars' own
// unflipped space, which a normalised Region cannot carry back.
type PDFiumStructuredPageForTest struct {
	Number int
	Rects  []*responses.GetPageTextStructuredRect
	Chars  []*responses.GetPageTextStructuredChar
}

// PDFiumStructuredTextForTest reads every page of doc through the same request shape Read
// issues, at an explicit mode ("rect" or "both"), so the mode-comparison control and the charsIn
// specs can each choose the mode they need rather than trusting Read's own choice. mode is a
// plain string so the external test package needs no go-pdfium/requests import.
func PDFiumStructuredTextForTest(ctx context.Context, doc Document, mode string) ([]PDFiumStructuredPageForTest, error) {
	var pages []PDFiumStructuredPageForTest
	err := withPDFiumInstance(ctx, func(inst pdfium.Pdfium) error {
		opened, err := inst.OpenDocument(&requests.OpenDocument{File: &doc.Bytes})
		if err != nil {
			return fmt.Errorf("pdfium: open document: %w", err)
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: opened.Document})

		count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: opened.Document})
		if err != nil {
			return fmt.Errorf("pdfium: page count: %w", err)
		}

		for i := range count.PageCount {
			ref := requests.Page{ByIndex: &requests.PageByIndex{Document: opened.Document, Index: i}}
			text, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
				Page: ref,
				Mode: requests.GetPageTextStructuredMode(mode),
			})
			if err != nil {
				return fmt.Errorf("pdfium: page %d text: %w", i+1, err)
			}
			pages = append(pages, PDFiumStructuredPageForTest{Number: i + 1, Rects: text.Rects, Chars: text.Chars})
		}
		return nil
	})
	return pages, err
}

// CharsInForTest hands the external test package the char-to-box helper the ModeBoth merge
// seam (subtask 03) will consume. Unexported because production has no caller for it yet.
func CharsInForTest(chars []*responses.GetPageTextStructuredChar, box responses.CharPosition) string {
	return charsIn(chars, box)
}
