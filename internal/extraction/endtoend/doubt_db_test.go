// doubt_db_test.go: a doubtful cell through the wired path -- the reason and its alternative
// reach extraction_field_results, and the value still reaches the invoices row.
package endtoend

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// dtDBLayout is the doubtful layout whose text seam is pdfium, so the run needs no golden
// routing beyond eeOptsFor's own decision.
const (
	dtDBLayout = "wild_two_party_bare_tin.pdf"
	dtDBField  = "buyer_name"
	dtDBValue  = "Honeywell Group"
	dtDBAlt    = "TIN:"
)

// AC-5, AC-6. The doubt writes a reason and one extra row; the importer reads Value, so the
// invoices row is untouched. The third assertion is the invariance half and holds today -- the
// first two are what the widening has to earn.
func TestRLS_EndToEndADoubtfulFieldStillReachesTheInvoiceRow(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, dtDBLayout)

	jobID := eeExtract(t, ctx, w, dtDBLayout, eeOptsFor(t, dtDBLayout)...)
	rows := eeFieldResults(t, ctx, jobID)
	if len(rows) == 0 {
		t.Fatalf("extraction job %s wrote no field-result row for %s; every clause below reads an empty table", jobID, dtDBLayout)
	}

	var ranks []int
	reason, value := "", ""
	alt := ""
	for _, r := range rows {
		if r.name != dtDBField {
			continue
		}
		ranks = append(ranks, r.rank)
		switch r.rank {
		case 0:
			if r.reason != nil {
				reason = *r.reason
			}
			if r.value != nil {
				value = *r.value
			}
		case 1:
			if r.value != nil {
				alt = *r.value
			}
		}
	}
	if len(ranks) == 0 {
		t.Fatalf("%s wrote no %s row at all; the reason asserted below would be the reason of an absent field", dtDBLayout, dtDBField)
	}
	if value != dtDBValue {
		t.Errorf("the rank-0 %s row holds %q, want %q", dtDBField, value, dtDBValue)
	}
	if reason != string(extraction.ReasonAmbiguous) {
		t.Errorf("the rank-0 %s row holds reason_code %q, want %q -- the doubt has to survive the store, not only Reconcile", dtDBField, reason, extraction.ReasonAmbiguous)
	}
	if alt != dtDBAlt {
		t.Errorf("the rank-1 %s row holds %q, want %q -- an ambiguous field with nothing to choose between is a shape the review screen cannot render", dtDBField, alt, dtDBAlt)
	}

	eeImport(t, ctx, w)
	got := eeWrittenRow(t, ctx, w.documentID)
	if got == nil {
		t.Fatalf("%s wrote no invoices row; the value assertion below has nothing to read", dtDBLayout)
	}
	if got[dtDBField] != dtDBValue {
		t.Errorf("the invoices row holds %s = %q, want %q -- the importer reads Value, so a doubtful field still lands", dtDBField, got[dtDBField], dtDBValue)
	}
}

// AC-2: the found total outranks the anchored addend under both readers, and the winning value
// reaches the invoices row. Each reader gets its own subtest: eeExtract registers its client Stop
// on the t it is given, and two live clients would race for the same extraction queue.
func TestRLS_EndToEndTheFoundTotalReachesTheInvoiceRow(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	const layout = "wild_ruled_lines_totals.pdf"
	eeRequireFixtures(t, []string{layout, wildGolden(layout)})

	check := func(t *testing.T, w eeWorld, jobID string) {
		rows := eeFieldResults(t, ctx, jobID)
		if len(rows) == 0 {
			t.Fatalf("extraction job %s wrote no field-result row for %s; every clause below reads an empty table", jobID, layout)
		}

		var totals []eeRow
		for _, r := range rows {
			if r.name == "total" {
				totals = append(totals, r)
			}
		}
		if len(totals) == 0 {
			t.Fatalf("%s wrote no total row at all", layout)
		}
		if len(totals) != 2 {
			t.Errorf("%s wrote %d total row(s): %s, want 2 -- rank 0 the found 8600.00, rank 1 the anchored 1000.00", layout, len(totals), eeShowRows(totals))
		}

		var rank0Present, rank1Present bool
		var rank0Value, rank1Value string
		var rank0Reason, rank1Reason *string
		for _, r := range totals {
			switch r.rank {
			case 0:
				rank0Present = true
				if r.value != nil {
					rank0Value = *r.value
				}
				rank0Reason = r.reason
			case 1:
				rank1Present = true
				if r.value != nil {
					rank1Value = *r.value
				}
				rank1Reason = r.reason
			}
		}

		if !rank0Present || rank0Value != "8600.00" {
			t.Errorf("%s rank-0 total holds %q, want %q -- the found total must outrank the anchored addend", layout, rank0Value, "8600.00")
		}
		rank0ReasonStr := "<NULL>"
		if rank0Reason != nil {
			rank0ReasonStr = *rank0Reason
		}
		if rank0ReasonStr != string(extraction.ReasonAmbiguous) {
			t.Errorf("%s rank-0 total holds reason_code %s, want %q", layout, rank0ReasonStr, extraction.ReasonAmbiguous)
		}
		if rank1Present {
			if rank1Value != "1000.00" {
				t.Errorf("%s rank-1 total holds %q, want %q -- the anchored addend, still reachable one rank down", layout, rank1Value, "1000.00")
			}
			if rank1Reason != nil {
				t.Errorf("%s rank-1 total holds reason_code %q, want NULL", layout, *rank1Reason)
			}
		}

		eeImport(t, ctx, w)
		got := eeWrittenRow(t, ctx, w.documentID)
		if got == nil {
			t.Fatalf("%s wrote no invoices row; the total assertion below has nothing to read", layout)
		}
		if got["total"] != "8600.00" {
			t.Errorf("the invoices row holds total = %q, want %q", got["total"], "8600.00")
		}
	}

	t.Run("pdfium", func(t *testing.T) {
		w := eeSeed(t, ctx, layout)
		jobID := eeExtract(t, ctx, w, layout)
		check(t, w, jobID)
	})

	t.Run("docling", func(t *testing.T) {
		w := eeSeed(t, ctx, layout)
		jobID := eeExtract(t, ctx, w, layout, eeWithText(eeGoldenReader(t, wildGolden(layout))))
		check(t, w, jobID)
	})
}

// eeLaterPageTotal moves every 8,600.00 token onto one extra page after the last.
type eeLaterPageTotal struct {
	extraction.PageReader
	moved *atomic.Int32
}

func (r eeLaterPageTotal) Read(ctx context.Context, doc extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	var moved []extraction.Token
	var next extraction.Page
	res, err := r.PageReader.Read(ctx, doc, func(p extraction.Page) error {
		kept := make([]extraction.Token, 0, len(p.Tokens))
		for _, tok := range p.Tokens {
			if tok.Text == "8,600.00" {
				moved = append(moved, tok)
				continue
			}
			kept = append(kept, tok)
		}
		p.Tokens = kept
		next = extraction.Page{Number: p.Number + 1, WidthPt: p.WidthPt, HeightPt: p.HeightPt}
		return onPage(p)
	})
	if err != nil {
		return res, err
	}
	for i := range moved {
		moved[i].Region.Page = next.Number
	}
	next.Tokens = moved
	r.moved.Store(int32(len(moved)))
	res.Pages++
	return res, onPage(next)
}

// The worker must pass every page it read, not only the first: the total sits on page 2.
func TestRLS_EndToEndAFoundTotalOnALaterPageReachesTheInvoiceRow(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	const layout = "wild_ruled_lines_totals.pdf"
	eeRequireFixtures(t, []string{layout, wildGolden(layout)})

	var moved atomic.Int32
	w := eeSeed(t, ctx, layout)
	jobID := eeExtract(t, ctx, w, layout, eeWithText(eeLaterPageTotal{PageReader: eeGoldenReader(t, wildGolden(layout)), moved: &moved}))
	if n := moved.Load(); n != 1 {
		t.Fatalf("the reader moved %d 8,600.00 token(s) to page 2, want 1", n)
	}

	var value, reason *string
	var page int
	if err := h.super.QueryRow(ctx,
		`SELECT value, reason_code, coalesce(page, 0) FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = 'total' AND candidate_rank = 0`,
		jobID).Scan(&value, &reason, &page); err != nil {
		t.Fatalf("read the rank-0 total for job %s: %v", jobID, err)
	}
	show := func(s *string) string {
		if s == nil {
			return "<NULL>"
		}
		return *s
	}
	if value == nil || *value != "8600.00" {
		t.Errorf("rank-0 total holds %s, want 8600.00", show(value))
	}
	if page != 2 {
		t.Errorf("rank-0 total holds page %d, want 2", page)
	}
	if reason == nil || *reason != string(extraction.ReasonAmbiguous) {
		t.Errorf("rank-0 total holds reason_code %s, want %q", show(reason), extraction.ReasonAmbiguous)
	}

	eeImport(t, ctx, w)
	got := eeWrittenRow(t, ctx, w.documentID)
	if got == nil {
		t.Fatalf("%s wrote no invoices row", layout)
	}
	if got["total"] != "8600.00" {
		t.Errorf("the invoices row holds total = %q, want %q", got["total"], "8600.00")
	}
}
