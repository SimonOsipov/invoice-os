// doubt_db_test.go: a doubtful cell through the wired path -- the reason and its alternative
// reach extraction_field_results, and the value still reaches the invoices row.
package endtoend

import (
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
