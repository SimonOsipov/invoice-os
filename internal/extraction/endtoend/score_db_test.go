// score_db_test.go: the walk that produces the score -- each layout's bytes through the
// worker, through ImportDocument, and out of the invoices row it wrote.
package endtoend

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// eeWrittenColumns is the read order of eeWrittenRowSQL. Zipped against writtenFields at run
// time, so a reordered vocabulary cannot silently file one field's value under another.
var eeWrittenColumns = []string{
	"invoice_number", "issue_date", "buyer_tin", "buyer_name",
	"currency", "subtotal", "vat", "total",
}

// numeric(14,2)::text renders 1075.00 and to_char renders 2026-03-04, both already in the form
// the shapes normalise to, so the comparison needs no second parser.
const eeWrittenRowSQL = `SELECT invoice_number,
       coalesce(to_char(issue_date, 'YYYY-MM-DD'), ''),
       coalesce(buyer_tin, ''),
       coalesce(buyer_name, ''),
       coalesce(currency, ''),
       coalesce(subtotal::text, ''),
       coalesce(vat::text, ''),
       coalesce(total::text, '')
  FROM invoices WHERE source_document_id = $1`

// eeWrittenRow is what the import actually wrote for one document, keyed by written field.
// A nil return is "no invoices row at all" -- quarantined, not empty.
func eeWrittenRow(t *testing.T, ctx context.Context, documentID string) map[string]string {
	t.Helper()
	if !slices.Equal(eeWrittenColumns, writtenFields) {
		t.Fatalf("eeWrittenRowSQL reads %v but the score is over %v; the values would be filed under the wrong fields", eeWrittenColumns, writtenFields)
	}

	vals := make([]string, len(eeWrittenColumns))
	dst := make([]any, len(vals))
	for i := range vals {
		dst[i] = &vals[i]
	}
	if err := eeRequire(t).super.QueryRow(ctx, eeWrittenRowSQL, documentID).Scan(dst...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		t.Fatalf("read the invoices row for document %s: %v", documentID, err)
	}

	out := make(map[string]string, len(vals))
	for i, col := range eeWrittenColumns {
		out[col] = vals[i]
	}
	return out
}

// eeLayoutResult is one layout's contribution to the score.
type eeLayoutResult struct {
	row         eeScoreRow
	missed      []eeCell
	saw         map[eeCell]string
	quarantined bool
	imported    int // QuarantinedInvoices
	lines       eeLineOutcome
	// fields is every extraction_field_results row the run wrote, read INSIDE the subtest:
	// eeSeed's teardown deletes the tenant on that t. Precedent: eeDecoyRun.rows.
	fields []eeRow
}

// eeOCRLayouts are the layouts whose text seam is their committed docling golden rather than
// pdfium. An image-only page reads zero pdfium tokens, so worker.go's no-text-layer branch would
// collapse it to one unreadable field -- a free 0/8 instead of an earned one.
var eeOCRLayouts = map[string]bool{"wild_scanned_no_number.pdf": true}

// eeOptsFor is the ONE site that routes a layout's text seam. A second hand-written site drifts,
// and the drifted one silently tests pdfium while claiming to test the golden.
func eeOptsFor(t *testing.T, layout string) []eeOpt {
	t.Helper()
	if !eeOCRLayouts[layout] {
		return nil
	}
	return []eeOpt{eeWithText(eeGoldenReader(t, wildGolden(layout)))}
}

// eeRunLayout drives one layout end to end and scores every written field against expect. A
// quarantined layout still contributes a full denominator: the cells are misses, not absences.
//
// Its own subtest, so eeExtract's Stop cleanup fires before the next layout enqueues. Each
// client's Open serves ONE layout's bytes regardless of document, so two live clients on the
// extraction queue hand a job to whichever fetches first and a layout scores another layout's
// invoice -- the cross-layout guard in TestRLS_EndToEndScoresTheCorpus.
func eeRunLayout(t *testing.T, ctx context.Context, layout string, expect map[string][]string) eeLayoutResult {
	t.Helper()
	var out eeLayoutResult
	ok := t.Run(layout, func(t *testing.T) {
		w := eeSeed(t, ctx, layout)
		jobID := eeExtract(t, ctx, w, layout, eeOptsFor(t, layout)...)
		fields := eeFieldResults(t, ctx, jobID)
		res := eeImport(t, ctx, w)

		got := eeWrittenRow(t, ctx, w.documentID)
		out = eeLayoutResult{
			row:         eeScoreRow{name: layout},
			saw:         map[eeCell]string{},
			quarantined: got == nil,
			imported:    res.QuarantinedInvoices,
			fields:      fields,
		}
		for _, field := range writtenFields {
			cell := eeCell{layout: layout, field: field}
			out.row.total++
			actual := got[field] // "" when there is no row at all
			out.saw[cell] = actual
			// An empty expectation names nothing to equal, so it can never be a hit.
			if actual != "" && slices.Contains(expect[field], actual) {
				out.row.hits++
				continue
			}
			out.missed = append(out.missed, cell)
		}

		// Read inside the subtest: eeSeed registers its cleanup on this t, so the invoice is
		// gone by the time eeRunLayout returns. A quarantined layout has no row and stays 0/0.
		if id, ok := eeInvoiceIDForDocument(t, ctx, w.documentID); ok {
			out.lines = eeScoreLines(t, ctx, id)
		}
	})
	if !ok {
		t.Fatalf("the end-to-end run for %s failed; anything scored from here measures the harness", layout)
	}
	return out
}

// eeScoreCorpus walks every scored layout in table order.
func eeScoreCorpus(t *testing.T, ctx context.Context) eeScore {
	t.Helper()
	eeRequireFixtures(t, requiredPDFs)

	s := eeScore{saw: map[eeCell]string{}}
	perField := map[string]*eeScoreRow{}
	for _, field := range writtenFields {
		perField[field] = &eeScoreRow{name: field}
	}

	for _, want := range expectByLayout {
		r := eeRunLayout(t, ctx, want.file, want.fields)
		s.hits += r.row.hits
		s.total += r.row.total
		s.byLayout = append(s.byLayout, r.row)
		s.missed = append(s.missed, r.missed...)
		for cell, v := range r.saw {
			s.saw[cell] = v
			perField[cell.field].total++
			if !slices.Contains(r.missed, cell) {
				perField[cell.field].hits++
			}
		}
		if r.quarantined {
			s.quarantined = append(s.quarantined, want.file)
		}
		// priced counts a subset of reached, so reached IS its denominator -- two raw counts,
		// never a re-derived one.
		s.linesReached = append(s.linesReached, eeScoreRow{name: want.file, hits: r.lines.reached, total: eeLinesExpected[want.file]})
		s.linesPriced = append(s.linesPriced, eeScoreRow{name: want.file, hits: r.lines.priced, total: r.lines.reached})
		if r.lines.read {
			s.linesScored++
		}
	}

	// Indexed by writtenFields, not by the map, so a field that never resolves renders 0/N.
	for _, field := range writtenFields {
		s.byField = append(s.byField, *perField[field])
	}
	return s
}

// --- the specs --------------------------------------------------------------

// AC-4, AC-6. Every scored layout runs end to end, graded on what the invoices row holds.
func TestRLS_EndToEndScoresTheCorpus(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	s := eeScoreCorpus(t, ctx)
	report := eeRenderReport(s)
	t.Log("\n" + report)

	// Floors first: an empty walk satisfies every sum below.
	if len(s.byLayout) != eeLayoutCount {
		t.Fatalf("the walk scored %d layout(s), want %d -- the sums below would pass vacuously", len(s.byLayout), eeLayoutCount)
	}
	if len(s.byField) != len(writtenFields) {
		t.Fatalf("the walk reported %d field row(s), want %d", len(s.byField), len(writtenFields))
	}
	if s.hits == 0 {
		t.Fatalf("the walk scored 0 hits over %d cell(s); nothing reached an invoices row, so the figure below measures the harness and not the extraction:\n%s", s.total, report)
	}

	if s.total != eeCorpusCells {
		t.Errorf("the score is taken over %d cell(s), want %d", s.total, eeCorpusCells)
	}
	byLayout := 0
	for _, r := range s.byLayout {
		byLayout += r.total
		if r.total != len(writtenFields) {
			t.Errorf("layout %s carries a denominator of %d, want %d", r.name, r.total, len(writtenFields))
		}
	}
	if byLayout != eeCorpusCells {
		t.Errorf("the per-layout denominators sum to %d, want %d", byLayout, eeCorpusCells)
	}
	byField := 0
	for _, r := range s.byField {
		byField += r.total
		if r.total != eeLayoutCount {
			t.Errorf("field %s carries a denominator of %d, want %d", r.name, r.total, eeLayoutCount)
		}
	}
	if byField != eeCorpusCells {
		t.Errorf("the per-field denominators sum to %d, want %d", byField, eeCorpusCells)
	}
	// Cross-layout guard: every layout's invoice_number is unique to it, so one layout holding
	// another's number is the worker having read the wrong bytes -- a miss the rate alone reads
	// as a poor score.
	for _, want := range expectByLayout {
		got := s.saw[eeCell{layout: want.file, field: "invoice_number"}]
		for _, other := range expectByLayout {
			if other.file == want.file || got == "" {
				continue
			}
			if slices.Contains(other.fields["invoice_number"], got) {
				t.Errorf("%s wrote invoice_number %q, which belongs to %s -- the run scored another layout's bytes", want.file, got, other.file)
			}
		}
	}

	if s.hits+len(s.missed) != s.total {
		t.Errorf("%d hit(s) + %d miss(es) = %d, want %d -- a cell is counted twice or not at all", s.hits, len(s.missed), s.hits+len(s.missed), s.total)
	}

	if !strings.Contains(report, eeReportMarker) {
		t.Errorf("the report does not carry %q:\n%s", eeReportMarker, report)
	}
	for _, want := range expectByLayout {
		if !strings.Contains(report, want.file) {
			t.Errorf("the report names no row for layout %s:\n%s", want.file, report)
		}
	}
	for _, field := range writtenFields {
		if !strings.Contains(report, field) {
			t.Errorf("the report names no row for field %s; a field at 0/eeLayoutCount must still render:\n%s", field, report)
		}
	}
	if got := strings.Count(report, "MISS "); got != len(s.missed) {
		t.Errorf("the report carries %d MISS line(s) for %d miss(es):\n%s", got, len(s.missed), report)
	}
	for _, c := range s.missed {
		line := "MISS " + c.layout + " / " + c.field
		if !strings.Contains(report, line) {
			t.Errorf("the report does not name the miss %s / %s:\n%s", c.layout, c.field, report)
		}
	}

	// Which cells miss, not just how many: eeCorpusHits survives one absent cell resolving while
	// one real hit breaks, and that trade would retire an EXTR-22..28 defect unnoticed.
	wantMiss := map[string]string{}
	for key, why := range eeAbsentCells {
		wantMiss[key] = why
	}
	for key, why := range eeRealMisses {
		wantMiss[key] = why
	}
	if len(wantMiss) == 0 {
		t.Fatal("no miss is pinned, so the comparison below asserts nothing")
	}
	gotMiss := map[string]bool{}
	for _, c := range s.missed {
		if gotMiss[c.key()] {
			t.Errorf("%s is counted as a miss twice", c.key())
		}
		gotMiss[c.key()] = true
	}
	for key := range wantMiss {
		if !gotMiss[key] {
			t.Errorf("%s is pinned as a miss and now scores a hit -- re-measure and move it out of eeAbsentCells/eeRealMisses; EXTR-21-09 owns the ratchet.\n%s", key, report)
		}
	}
	for key := range gotMiss {
		if _, ok := wantMiss[key]; !ok {
			t.Errorf("%s missed but is pinned as neither an absent cell nor a known extraction defect:\n%s", key, report)
		}
	}

	if s.hits != eeCorpusHits {
		t.Errorf("the corpus scores %d / %d, pinned at %d / %d -- re-measure and update this constant; EXTR-21-09 owns the ratchet.\n%s",
			s.hits, s.total, eeCorpusHits, eeCorpusCells, report)
	}
}

// AC-5. A document with no text layer reaches no invoices row, and that costs a full 0/8
// rather than dropping out of the denominator.
func TestRLS_EndToEndAQuarantinedLayoutScoresZeroNotAbsent(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeQuarantineLayout, expectByLayout[0].file})

	// Scored against a FULL eight-value expectation on purpose: the zero below has to come
	// from the missing row, not from an expectation naming nothing to equal.
	full := expectByLayout[0].fields
	for _, field := range writtenFields {
		if len(full[field]) == 0 {
			t.Fatalf("the control expectation names nothing for %q, so a 0/8 here would prove nothing", field)
		}
	}

	q := eeRunLayout(t, ctx, eeQuarantineLayout, full)
	if !q.quarantined {
		t.Fatalf("%s produced an invoices row; it carries no text layer and must quarantine", eeQuarantineLayout)
	}
	if q.imported != 1 {
		t.Errorf("ImportDocument reported QuarantinedInvoices = %d for %s, want 1", q.imported, eeQuarantineLayout)
	}
	if q.row.total != len(writtenFields) {
		t.Errorf("%s contributes a denominator of %d, want %d -- a quarantined layout is scored, not omitted", eeQuarantineLayout, q.row.total, len(writtenFields))
	}
	if q.row.hits != 0 {
		t.Errorf("%s scored %d hit(s) with no invoices row at all", eeQuarantineLayout, q.row.hits)
	}
	if len(q.missed) != len(writtenFields) {
		t.Errorf("%s recorded %d miss(es), want %d -- every written cell", eeQuarantineLayout, len(q.missed), len(writtenFields))
	}

	// Positive control, same test: the identical call on a readable layout is NOT quarantined
	// and does score, so the zero above is the quarantine branch answering and not an inert
	// scorer.
	ok := eeRunLayout(t, ctx, expectByLayout[0].file, full)
	if ok.quarantined {
		t.Fatalf("%s quarantined too; the run itself is broken, so the zero above proves nothing", expectByLayout[0].file)
	}
	if ok.imported != 0 {
		t.Errorf("ImportDocument reported QuarantinedInvoices = %d for %s, want 0", ok.imported, expectByLayout[0].file)
	}
	if ok.row.hits == 0 {
		t.Fatalf("%s scored 0 / %d against its own expectation; the scorer reads nothing, so the zero above proves nothing", expectByLayout[0].file, ok.row.total)
	}

	rendered := eeRenderReport(eeScore{
		total:       q.row.total,
		missed:      q.missed,
		saw:         q.saw,
		quarantined: []string{eeQuarantineLayout},
		byLayout:    []eeScoreRow{q.row},
	})
	for _, needle := range []string{"QUARANTINED", eeQuarantineLayout, "0/8"} {
		if !strings.Contains(rendered, needle) {
			t.Errorf("the report does not name %q for a quarantined layout:\n%s", needle, rendered)
		}
	}
}

// AC-3.1. wild_rc_due_naira.pdf prints the naira symbol and never the word "Currency" -- that
// value must reach the invoices row it writes, not only Resolve's candidate list.
func TestRLS_EndToEndTheSymbolOnlyCurrencyReachesTheInvoiceRow(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	const layout = "wild_rc_due_naira.pdf"
	eeRequireFixtures(t, []string{layout})

	w := eeSeed(t, ctx, layout)
	eeExtract(t, ctx, w, layout, eeOptsFor(t, layout)...)
	eeImport(t, ctx, w)

	got := eeWrittenRow(t, ctx, w.documentID)
	if got == nil {
		t.Fatalf("%s produced no invoices row; the currency assertion below would read an absent row", layout)
	}
	if got["currency"] != "NGN" {
		t.Errorf("%s: invoices.currency = %q, want %q -- the naira symbol alone must resolve the field", layout, got["currency"], "NGN")
	}
}

// eeScannedFieldFloor is how many of the seven non-invoice_number written fields the image-only
// layout resolves to a rank-0 value, re-measured 2026-09-08 off its committed golden: all seven,
// since each party's TIN binds to the heading that owns it. A floor -- the point is that the read
// succeeded.
const eeScannedFieldFloor = 7

// AC-3. The image-only arrangement reaches no invoices row and costs a full 0/8.
func TestRLS_EndToEndTheScannedLayoutWritesNoInvoice(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	layout := eeQuarantinedLayouts[0]
	r := eeRunLayout(t, ctx, layout, wildExpectRow(t, layout))

	if !r.quarantined {
		t.Fatalf("%s produced an invoices row; it prints no invoice number and must quarantine", layout)
	}
	if r.imported != 1 {
		t.Errorf("ImportDocument reported QuarantinedInvoices = %d for %s, want 1", r.imported, layout)
	}
	if r.row.total != len(writtenFields) {
		t.Errorf("%s contributes a denominator of %d, want %d -- a quarantined layout is scored, not omitted", layout, r.row.total, len(writtenFields))
	}
	if r.row.hits != 0 {
		t.Errorf("%s scored %d hit(s) with no invoices row at all", layout, r.row.hits)
	}
	if len(r.missed) != len(writtenFields) {
		t.Errorf("%s recorded %d miss(es), want %d -- every written cell", layout, len(r.missed), len(writtenFields))
	}
}

// AC-3. The same run's extraction rows: the quarantine is the missing invoice number, not an
// unreadable document. Without this the 0/8 above is score_test.go:527's banned free zero.
func TestRLS_EndToEndTheScannedLayoutStillReadFields(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	layout := eeQuarantinedLayouts[0]
	r := eeRunLayout(t, ctx, layout, wildExpectRow(t, layout))

	if len(r.fields) == 0 {
		t.Fatalf("%s wrote no extraction_field_results row at all; every clause below would hold over nothing", layout)
	}
	for _, row := range r.fields {
		if row.reason != nil && *row.reason == string(extraction.ReasonUnreadable) {
			t.Errorf("%s wrote %s with reason %q; the run took the no-text-layer branch, so its 0/8 is free and not earned", layout, row.name, *row.reason)
		}
	}

	valued := 0
	for _, field := range writtenFields {
		if field == "invoice_number" {
			continue
		}
		for _, row := range r.fields {
			if row.name == field && row.rank == 0 && row.value != nil && *row.value != "" {
				valued++
				break
			}
		}
	}
	if valued < eeScannedFieldFloor {
		t.Errorf("%s resolved %d of the %d non-invoice_number written fields to a rank-0 value, want at least %d -- OCR read the page, so a lower figure is a read that stopped working",
			layout, valued, len(writtenFields)-1, eeScannedFieldFloor)
	}

	for _, row := range r.fields {
		if row.name == "invoice_number" && row.rank == 0 && row.value != nil && *row.value != "" {
			t.Errorf("%s resolved invoice_number to %q at rank 0; the page prints none, so the quarantine would not be the missing number", layout, *row.value)
		}
	}
}

// eeBaselineHits is every cell EXTR-21 measured as a hit, named one by one. AC-7's oracle: no
// field that read correctly in that baseline reads differently now. Derived once from EXTR-21's
// miss set and FROZEN -- never re-derive it from eeAbsentCells/eeRealMisses, because those two
// move with each improvement and a derived list would ratify a regression the moment the same
// commit pinned it as a miss.
var eeBaselineHits = []string{
	"corpus_ambiguous_date.pdf/invoice_number",
	"corpus_ambiguous_date.pdf/issue_date",
	"corpus_ambiguous_date.pdf/total",
	"corpus_inline_labels.pdf/buyer_name",
	"corpus_inline_labels.pdf/buyer_tin",
	"corpus_inline_labels.pdf/currency",
	"corpus_inline_labels.pdf/invoice_number",
	"corpus_inline_labels.pdf/issue_date",
	"corpus_inline_labels.pdf/subtotal",
	"corpus_inline_labels.pdf/total",
	"corpus_inline_labels.pdf/vat",
	"corpus_split_labels.pdf/buyer_name",
	"corpus_split_labels.pdf/buyer_tin",
	"corpus_split_labels.pdf/currency",
	"corpus_split_labels.pdf/invoice_number",
	"corpus_split_labels.pdf/issue_date",
	"corpus_split_labels.pdf/subtotal",
	"corpus_split_labels.pdf/total",
	"corpus_split_labels.pdf/vat",
	"corpus_stacked_labels.pdf/buyer_name",
	"corpus_stacked_labels.pdf/buyer_tin",
	"corpus_stacked_labels.pdf/invoice_number",
	"corpus_stacked_labels.pdf/issue_date",
	"corpus_stacked_labels.pdf/total",
	"corpus_totals_block.pdf/invoice_number",
	"corpus_totals_block.pdf/subtotal",
	"corpus_totals_block.pdf/total",
	"corpus_totals_block.pdf/vat",
	"corpus_two_column.pdf/buyer_name",
	"corpus_two_column.pdf/invoice_number",
	"corpus_two_column.pdf/issue_date",
	"corpus_two_column.pdf/total",
	"wild_rc_due_naira.pdf/buyer_name",
	"wild_rc_due_naira.pdf/buyer_tin",
	"wild_rc_due_naira.pdf/invoice_number",
	"wild_rc_due_naira.pdf/issue_date",
	"wild_rc_due_naira.pdf/subtotal",
	"wild_rc_due_naira.pdf/total",
	"wild_rc_due_naira.pdf/vat",
	"wild_ruled_lines_totals.pdf/buyer_name",
	"wild_ruled_lines_totals.pdf/buyer_tin",
	"wild_ruled_lines_totals.pdf/currency",
	"wild_ruled_lines_totals.pdf/invoice_number",
	"wild_ruled_lines_totals.pdf/issue_date",
	"wild_ruled_lines_totals.pdf/subtotal",
	"wild_ruled_lines_totals.pdf/vat",
	"wild_stacked_borderless.pdf/invoice_number",
	"wild_two_party_bare_tin.pdf/currency",
	"wild_two_party_bare_tin.pdf/invoice_number",
	"wild_two_party_bare_tin.pdf/issue_date",
	"wild_two_party_bare_tin.pdf/subtotal",
	"wild_two_party_bare_tin.pdf/total",
	"wild_two_party_bare_tin.pdf/vat",
}

const eeBaselineHitCount = 53

// AC-7. No field that read correctly in EXTR-21's baseline reads differently now, cell by cell.
// The rate alone cannot see this: eeCorpusHits holds while one baseline hit breaks and one new
// cell resolves.
func TestRLS_EndToEndNoBaselineHitRegresses(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	// Floor 1: the frozen list is the size EXTR-21 measured, and holds no duplicate. A list
	// silently shrunk to the cells that still pass asserts nothing.
	if len(eeBaselineHits) != eeBaselineHitCount {
		t.Fatalf("eeBaselineHits holds %d cell(s), want %d -- the frozen baseline was edited; it comes from EXTR-21's measurement, never from today's miss set", len(eeBaselineHits), eeBaselineHitCount)
	}
	seen := map[string]bool{}
	for _, key := range eeBaselineHits {
		if seen[key] {
			t.Fatalf("eeBaselineHits names %s twice; the count above then covers fewer cells than it claims", key)
		}
		seen[key] = true
	}

	// Floor 2: every named cell is a (layout, field) the walk scores. A typo'd key would
	// otherwise be a cell nobody checks.
	for _, key := range eeBaselineHits {
		layout, field, ok := strings.Cut(key, "/")
		if !ok {
			t.Fatalf("eeBaselineHits entry %q is not layout/field", key)
		}
		known := false
		for _, want := range expectByLayout {
			if want.file == layout {
				known = true
				break
			}
		}
		if !known {
			t.Fatalf("eeBaselineHits names layout %s, which the walk does not score", layout)
		}
		if !slices.Contains(writtenFields, field) {
			t.Fatalf("eeBaselineHits names field %s, which the invoices row does not carry", field)
		}
	}

	s := eeScoreCorpus(t, ctx)
	report := eeRenderReport(s)

	// Floor 3: the walk is real. Every comparison below passes over an empty one.
	if s.total != eeCorpusCells {
		t.Fatalf("the walk scored %d cell(s), want %d", s.total, eeCorpusCells)
	}
	if s.hits == 0 {
		t.Fatalf("the walk scored 0 hit(s); the comparison below would name every baseline cell:\n%s", report)
	}

	missed := map[string]bool{}
	for _, c := range s.missed {
		missed[c.key()] = true
	}
	hit := map[string]bool{}
	for cell := range s.saw {
		if !missed[cell.key()] {
			hit[cell.key()] = true
		}
	}

	for _, key := range eeBaselineHits {
		if !hit[key] {
			t.Errorf("%s read correctly in EXTR-21's baseline and does not now; this story may only add hits\n%s", key, report)
		}
	}

	// The anti-tautology clause. Without it a list that happens to equal today's hit set
	// satisfies the walk above and ratifies whatever ships. eeCorpusHits - eeBaselineHitCount
	// is what has been gained since: a story that adds a hit widens the gap, a regression
	// narrows it.
	// It is NOT eeBaselineHitCount + eeRealMisses + eeAbsentCells == eeCorpusCells -- that sums
	// to 84, because the four gained cells are in neither collection.
	if eeBaselineHitCount > eeCorpusHits {
		t.Fatalf("the frozen baseline holds %d hit(s) and the corpus is pinned at %d; a pin below the baseline records a regression as the shipped number", eeBaselineHitCount, eeCorpusHits)
	}
	if len(eeAbsentCells)+len(eeRealMisses)+s.hits != eeCorpusCells {
		t.Errorf("%d absent + %d real miss(es) + %d hit(s) = %d, want %d -- the two pinned miss lists no longer partition the walk", len(eeAbsentCells), len(eeRealMisses), s.hits, len(eeAbsentCells)+len(eeRealMisses)+s.hits, eeCorpusCells)
	}
}

// AC-2. The published rate, its non-vacuity, and the instruction that a ratchet only goes up.
//
// Both clause groups together reduce to s.hits == eeCorpusHits, which is the equality pin in
// TestRLS_EndToEndScoresTheCorpus above. They restate it in the floor's vocabulary and become
// load-bearing only if that pin is ever weakened to an inequality. The two clause groups read
// ONE walk: per the equality they cannot fail independently, so a second walk buys no oracle.
//
// The integer clauses decide the one-cell boundary exactly; the float pair is what a reader
// publishes, and at exactly one cell IEEE-754 does not guarantee it reds.
func TestRLS_EndToEndMeetsTheFloor(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	s := eeScoreCorpus(t, ctx)
	report := eeRenderReport(s)

	// Floors first: an empty walk satisfies every comparison below.
	if len(s.byLayout) != eeLayoutCount {
		t.Fatalf("the walk scored %d layout(s), want %d -- the rate below would be taken over nothing", len(s.byLayout), eeLayoutCount)
	}
	if s.total != eeCorpusCells {
		t.Fatalf("the walk scored %d cell(s), want %d -- one cell of slack would be the wrong size", s.total, eeCorpusCells)
	}
	if s.hits == 0 {
		t.Fatalf("the walk scored 0 hit(s) over %d cell(s); the rate below measures the harness and not the extraction:\n%s", s.total, report)
	}

	rate := float64(s.hits) / float64(s.total)
	if rate < eeCorpusFloor {
		t.Errorf("the corpus reaches %d / %d = %v, below the floor %v. The floor is a ratchet: fix the extraction, never lower it (docs/extraction-corpus.md)\n%s",
			s.hits, s.total, rate, eeCorpusFloor, report)
	}

	// Not below the measurement by a whole cell: an improvement must be recorded, not absorbed.
	if eeCorpusHits < s.hits {
		t.Errorf("the corpus reaches %d / %d and the floor is pinned at %d. Raise eeCorpusHits to %d (a ratchet only goes up) and update docs/extraction-corpus.md in the same commit\n%s",
			s.hits, s.total, eeCorpusHits, s.hits, report)
	}
	if oneCell := 1.0 / float64(s.total); eeCorpusFloor <= rate-oneCell {
		t.Errorf("the corpus reaches %v and the floor is %v, a slack of %v -- a whole cell could regress unnoticed. Raise eeCorpusHits to %d (a ratchet only goes up) and update docs/extraction-corpus.md in the same commit",
			rate, eeCorpusFloor, rate-eeCorpusFloor, s.hits)
	}

	// Not above it either: a floor above the measurement is a prediction, not a ratchet.
	if eeCorpusHits > s.hits {
		t.Errorf("the floor is pinned at %d but the corpus reaches only %d / %d; the pin is a target, and the rate check above can never pass\n%s",
			eeCorpusHits, s.hits, s.total, report)
	}
	if eeCorpusFloor > rate {
		t.Errorf("the floor %v is above the measured rate %v; it is a prediction, not a ratchet", eeCorpusFloor, rate)
	}
}
