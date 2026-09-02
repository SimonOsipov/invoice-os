// mock_lines_qa_db_test.go: QA (Mode B) -- the ORDER the widened default result reaches the
// review screen in. Every unit spec in mock_test.go reads mockDefaultResult's slice order; the
// SPA reads Reader.Detail, which re-orders. The two are not the same list, and the difference
// is what a positional `.find` in an e2e spec resolves against.
package extraction_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestExtractionDetail_MockDefaultArrivesInFieldNameOrder: writeFieldResultsTx writes every row
// of one job in ONE transaction and now() is transaction-constant (reader_detail_db_test.go:670
// states the same fact from the other side), so reader.go:385's `ORDER BY created_at,
// field_name, ...` degenerates to field_name. The mock's emit order -- seven headers, then the
// block, then the cells -- is NOT what the screen reads.
//
// The consequence this pins: a line cell sorts before subtotal, supplier_tin and total, so any
// spec that picks a subject by POSITION ("the first inconsistent field") now lands on
// line_items[2].line_total rather than on total. EXTR12-E2E-06 does exactly that
// (import-wizard.spec.ts:4945). Subtasks 07 and 09 must pick by NAME, not by position.
func TestExtractionDetail_MockDefaultArrivesInFieldNameOrder(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	// Seeded from the mock's own output, so this test moves with mock.go rather than with a
	// hand-copied list. One shared created_at, exactly as the worker writes them.
	results := mxDefault(t)
	if len(results) != 23 {
		t.Fatalf("the default result carries %d field(s), want 23; the ordering claim below would be over the wrong fixture", len(results))
	}
	emitted := make([]string, 0, len(results))
	for _, fr := range results {
		var box *rvdBox
		if fr.Region != nil {
			box = &rvdBox{Page: fr.Region.Page, X0: fr.Region.X0, Y0: fr.Region.Y0, X1: fr.Region.X1, Y1: fr.Region.Y1}
		}
		var reason *string
		if fr.Reason != "" {
			s := string(fr.Reason)
			reason = &s
		}
		rvdSeedField(t, ctx, tenantA, jobA, fr.Name, fr.Value, box, 0, reason, now)
		emitted = append(emitted, fr.Name)
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	names := rvdFieldNames(got.Fields)
	if len(names) != len(emitted) {
		t.Fatalf("the wire carries %d field(s), want %d; the order comparison below would be over a different set", len(names), len(emitted))
	}

	sorted := slices.Clone(emitted)
	slices.Sort(sorted)
	// The control: the mock's emit order and field_name order really do DIFFER, so the equality
	// below is a claim about re-ordering and not a coincidence.
	if slices.Equal(emitted, sorted) {
		t.Fatal("the mock already emits in field_name order; this spec cannot show that the read path re-orders")
	}
	if !slices.Equal(names, sorted) {
		t.Fatalf("the wire came back in order\n  %v\nwant field_name order\n  %v", names, sorted)
	}

	// The hazard, named in numbers rather than left to a reader to notice.
	firstLine := slices.IndexFunc(names, func(n string) bool { return strings.HasPrefix(n, "line_items[") })
	if firstLine < 0 {
		t.Fatal("no line cell reached the wire; the precedence claim below would compare nothing")
	}
	for _, after := range []string{"subtotal", "supplier_tin", "total", "vat"} {
		i := slices.Index(names, after)
		if i < 0 {
			t.Errorf("the wire carries no %q; the precedence claim is under-examined", after)
			continue
		}
		if firstLine > i {
			t.Errorf("%s at index %d precedes the first line cell at index %d; the precedence this spec pins has changed and EXTR12-E2E-06's positional pick may need revisiting", after, i, firstLine)
		}
	}
	// And the other side of it: buyer_tin, invoice_number and issue_date still precede the grid,
	// so the header partition is not wholly displaced.
	for _, before := range []string{"buyer_tin", "invoice_number", "issue_date"} {
		i := slices.Index(names, before)
		if i < 0 {
			t.Errorf("the wire carries no %q", before)
			continue
		}
		if i > firstLine {
			t.Errorf("%s at index %d follows the first line cell at index %d", before, i, firstLine)
		}
	}
}
