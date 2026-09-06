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
		eeExtract(t, ctx, w, layout)
		res := eeImport(t, ctx, w)

		got := eeWrittenRow(t, ctx, w.documentID)
		out = eeLayoutResult{
			row:         eeScoreRow{name: layout},
			saw:         map[eeCell]string{},
			quarantined: got == nil,
			imported:    res.QuarantinedInvoices,
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
	})
	if !ok {
		t.Fatalf("the end-to-end run for %s failed; anything scored from here measures the harness", layout)
	}
	return out
}

// eeScoreCorpus walks the six layouts in table order.
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
	}

	// Indexed by writtenFields, not by the map, so a field that never resolves renders 0/N.
	for _, field := range writtenFields {
		s.byField = append(s.byField, *perField[field])
	}
	return s
}

// --- the specs --------------------------------------------------------------

// AC-4, AC-6. The six layouts run end to end and scored on what the invoices row holds.
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
			t.Errorf("the report names no row for field %s; a field at 0/6 must still render:\n%s", field, report)
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

	// Which cells miss, not just how many: 32 survives one absent cell resolving while one real
	// hit breaks, and that trade would retire an EXTR-22..28 defect without anyone noticing.
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
		t.Errorf("the six layouts score %d / %d, pinned at %d / %d -- re-measure and update this constant; EXTR-21-09 owns the ratchet.\n%s",
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
