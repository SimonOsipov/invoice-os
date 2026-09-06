// lines_db_test.go: the line-item outcome, read off the invoice the path actually wrote.
//
// The headline figure is a permanent zero for this story's life: documentCreateInput's return
// literal names no LineItems key (internal/importer/document.go:173-185), while
// invoice.Store.Create does write one line_items row per LineItemInput
// (internal/invoice/store.go:244-258) and extraction does write line_items[N].<role> rows. The
// two are simply not connected; EXTR-24 owns connecting them. So every zero here ships with a
// positive control in the same test, and the mutilations act on that control rather than on the
// zero -- a scorer that cannot read line_items at all satisfies a bare zero exactly as well.
package endtoend

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	// The only committed fixture carrying BOTH a table and a docling golden. dense_invoice.pdf
	// is tracked but has no golden, so it cannot be replayed.
	//
	// Neither name may enter requiredPDFs/requiredGoldens: TestEndToEnd_TheScoredSetIsTheRequiredSet
	// fatals unless requiredPDFs holds exactly eeLayoutCount entries.
	eeLineFixture = "rich_invoice.pdf"
	eeLineGolden  = eeLineRichGolden

	// eeLineMinIdx is the floor AC-4 and AC-8 need: a divergence against zero is vacuous.
	// Precedent: TestRLS_RichFixtureCarriesAtLeastTwoLineRows, corpus_wired_db_test.go:1046
	// (package internal/extraction, rank-0 only).
	eeLineMinIdx = 2
)

// eeLineIdxMeasured is how many distinct line_items[N] indices the rich fixture writes into
// extraction_field_results -- measured the same at rank 0 and at any rank (one 5x4 table).
const eeLineIdxMeasured = 4

// The control invoice's own numbers. NOT held at a placeholder: these are fixed by the control's
// inputs -- two LineItemInput entries, one carrying a UnitPrice -- and a zero here would be
// satisfied by the very scorer this control exists to catch.
// TestRLS_EndToEndTheLineScorerReadsLinesWhenTheyExist re-derives both off eeLineControl, so a
// drifted control cannot leave the pins pointing at nothing.
const (
	eeLineControlReached = 2
	eeLineControlPriced  = 1
)

// eeLineOutcome is one invoice's line reality: rows that reached it, and how many carry a
// price. read is set only by eeScoreLines, so a walk that stops calling the scorer is
// distinguishable from one that reads a genuine zero -- both report reached=0.
type eeLineOutcome struct {
	reached, priced int
	read            bool
}

// count(unit_price) counts non-NULL only, so "priced" is a column fact rather than a parse.
const eeLineCountSQL = `SELECT count(*), count(unit_price) FROM line_items WHERE invoice_id = $1`

// eeScoreLines reads the invoice's OWN rows -- never extraction_field_results. That distinction
// is the whole point of AC-8: the two disagree on a table-bearing document.
func eeScoreLines(t *testing.T, ctx context.Context, invoiceID string) eeLineOutcome {
	t.Helper()
	var out eeLineOutcome
	// count(*) over a WHERE always returns one row, so an invoice with no lines reads 0/0
	// rather than pgx.ErrNoRows.
	if err := eeRequire(t).super.QueryRow(ctx, eeLineCountSQL, invoiceID).
		Scan(&out.reached, &out.priced); err != nil {
		t.Fatalf("count the line_items rows for invoice %s: %v", invoiceID, err)
	}
	out.read = true
	return out
}

// eeInvoiceIDForDocument is the invoices row one document produced. A false return is "no row
// at all" -- quarantined, mirroring eeWrittenRow's nil.
func eeInvoiceIDForDocument(t *testing.T, ctx context.Context, documentID string) (string, bool) {
	t.Helper()
	var id string
	err := eeRequire(t).super.QueryRow(ctx,
		`SELECT id FROM invoices WHERE source_document_id = $1`, documentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read the invoices row for document %s: %v", documentID, err)
	}
	return id, true
}

// eeWithText swaps the worker's Text seam. The worker derives lines from the Text reader alone
// (internal/extraction/worker.go:250), so a run left on the default PDFiumReader writes no line
// row whatever the document carries.
func eeWithText(r extraction.PageReader) eeOpt {
	return func(ew *extraction.ExtractWorker) { ew.Text = r }
}

// eeLineControl is the positive baseline: two entries, one priced. Built fresh on every call,
// so a mutilated build can never share a backing array with the unmutilated one.
func eeLineControl() []invoice.LineItemInput {
	sp := func(s string) *string { return &s }
	return []invoice.LineItemInput{
		{Description: sp("Widget"), Quantity: sp("2"), UnitPrice: sp("500.00"), LineTotal: sp("1000.00")},
		{Description: sp("Handling"), LineTotal: sp("60.00")}, // no UnitPrice: the unpriced half of 2/1
	}
}

// eeCreateInvoice writes one invoice directly through invoice.Store.Create, as the seeded
// member. This is the write path the importer does NOT take, which is what makes it a control
// on the scorer rather than on the mapper.
func eeTryCreateInvoice(t *testing.T, ctx context.Context, w eeWorld, number string, lines []invoice.LineItemInput) (invoice.Invoice, error) {
	t.Helper()
	store := invoice.NewStore(eeRequire(t).app)
	rctx := auth.WithIdentity(ctx, auth.Identity{
		Subject: w.subject, Role: "authenticated", TenantID: w.tenantID,
	})
	return store.Create(rctx, invoice.CreateInput{
		EntityID: w.entityID, InvoiceNumber: number, LineItems: lines,
	})
}

func eeCreateInvoice(t *testing.T, ctx context.Context, w eeWorld, number string, lines []invoice.LineItemInput) string {
	t.Helper()
	inv, err := eeTryCreateInvoice(t, ctx, w, number, lines)
	if err != nil {
		t.Fatalf("Store.Create(%s) with %d line input(s): %v", number, len(lines), err)
	}
	if len(inv.LineItems) != len(lines) {
		t.Fatalf("Store.Create(%s) returned %d line item(s) for %d input(s); the control was not written as asked",
			number, len(inv.LineItems), len(lines))
	}
	return inv.ID
}

// eeAssertControl is the positive control every asserted zero in this file carries: the same
// scorer, over an invoice whose lines demonstrably exist.
func eeAssertControl(t *testing.T, ctx context.Context, w eeWorld, number string) {
	t.Helper()
	id := eeCreateInvoice(t, ctx, w, number, eeLineControl())
	got := eeScoreLines(t, ctx, id)
	if got.reached != eeLineControlReached || got.priced != eeLineControlPriced {
		t.Errorf("the control invoice (2 line inputs, 1 priced) scores %d reached / %d priced, want %d / %d -- the zero asserted in this test is satisfied by a scorer that reads nothing",
			got.reached, got.priced, eeLineControlReached, eeLineControlPriced)
	}
}

// --- the specs --------------------------------------------------------------

// AC-2, AC-5. Every layout's lines reaching the invoice, pinned, with the denominator taken off
// the layout's own document -- and both figures rendered in the report the header number rides.
func TestRLS_EndToEndScoresLineItemOutcome(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	s := eeScoreCorpus(t, ctx)
	report := eeRenderReport(s)
	t.Log("\n" + report)

	// Floors first: nil slices satisfy every per-row check below.
	if len(s.linesReached) != eeLayoutCount || len(s.linesPriced) != eeLayoutCount {
		t.Fatalf("the walk reported %d reached row(s) and %d priced row(s), want %d each -- the line outcome is not being scored at all",
			len(s.linesReached), len(s.linesPriced), eeLayoutCount)
	}

	// A walk that never calls the scorer reports these same zeros, so it has to prove it read
	// them.
	if s.linesScored != eeLayoutCount {
		t.Fatalf("the walk read line rows off %d of %d layout invoices; a walk that never scores reports the same zeros",
			s.linesScored, eeLayoutCount)
	}

	for i, want := range expectByLayout {
		reached, priced := s.linesReached[i], s.linesPriced[i]
		if reached.name != want.file || priced.name != want.file {
			t.Errorf("row %d is named %q/%q, want %q on both -- the line rows are out of table order", i, reached.name, priced.name, want.file)
			continue
		}
		if reached.total != eeLinesExpected[want.file] {
			t.Errorf("%s scores its reached figure against a denominator of %d, want %d -- a layout carrying a table cannot be scored against zero",
				want.file, reached.total, eeLinesExpected[want.file])
		}
		if reached.hits != 0 {
			t.Errorf("%s reached %d line(s) on the invoice; the mapper writes none (documentCreateInput names no LineItems key), so a non-zero here means the read is not the invoice's own rows",
				want.file, reached.hits)
		}
		// priced counts a subset of reached, so its denominator IS the reached figure.
		if priced.total != reached.hits {
			t.Errorf("%s scores its priced figure against %d, want %d -- priced/reached must be the two raw counts, not a re-derived denominator",
				want.file, priced.total, reached.hits)
		}
		if priced.hits != 0 {
			t.Errorf("%s priced %d line(s) out of %d reached", want.file, priced.hits, reached.hits)
		}
	}

	for _, heading := range []string{eeLinesReachedHeading, eeLinesPricedHeading} {
		if _, ok := eeReportSection(report, heading); !ok {
			t.Errorf("the report carries no %q section; the line figures are measured and not reported:\n%s", heading, report)
		}
	}

	// The zero above is satisfied by a scorer that reads nothing at all, so it ships with a
	// control the same scorer must move.
	w := eeSeed(t, ctx, eeLayout)
	eeAssertControl(t, ctx, w, "EE-LINES-CONTROL-1")
}

// AC-3. The scorer over an invoice whose lines exist: two entries in, one carrying a unit
// price. Without this, every zero in this file holds for a scorer that cannot read line_items.
func TestRLS_EndToEndTheLineScorerReadsLinesWhenTheyExist(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)

	// The two pins re-derived off the control itself, so they can never drift from what is
	// written: a control silently cut to one entry would otherwise leave 2/1 asserting nothing.
	lines := eeLineControl()
	var priced int
	for _, li := range lines {
		if li.UnitPrice != nil {
			priced++
		}
	}
	if len(lines) != eeLineControlReached || priced != eeLineControlPriced {
		t.Fatalf("eeLineControl carries %d entry(ies), %d priced, but the pins say %d / %d -- the control and its expected numbers have drifted apart",
			len(lines), priced, eeLineControlReached, eeLineControlPriced)
	}
	if eeLineControlPriced == eeLineControlReached || eeLineControlPriced == 0 {
		t.Fatalf("the control expects %d reached and %d priced; the two must differ and neither may be zero, or reached and priced are indistinguishable",
			eeLineControlReached, eeLineControlPriced)
	}

	id := eeCreateInvoice(t, ctx, w, "EE-LINES-CONTROL-2", lines)
	got := eeScoreLines(t, ctx, id)
	if got.reached != eeLineControlReached {
		t.Errorf("the scorer reads %d line(s) reached for an invoice created with %d, want %d", got.reached, len(lines), eeLineControlReached)
	}
	if got.priced != eeLineControlPriced {
		t.Errorf("the scorer reads %d priced, want %d -- one of the two inputs carries no UnitPrice", got.priced, eeLineControlPriced)
	}
	// Negative control: an invoice with NO line inputs must not read the control's numbers.
	empty := eeCreateInvoice(t, ctx, w, "EE-LINES-CONTROL-3", nil)
	if none := eeScoreLines(t, ctx, empty); none.reached != 0 || none.priced != 0 {
		t.Errorf("an invoice created with no line inputs scores %d reached / %d priced, want 0 / 0 -- the scorer is not reading this invoice's rows", none.reached, none.priced)
	}
}

// AC-4, AC-6. The worker DID write line_items[N].<role> rows for a table-bearing document, and
// the invoice it fed still holds none: the loss is at the mapper, not at the read.
func TestRLS_EndToEndTheWorkerWroteLineRowsTheInvoiceNeverGot(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture, eeLineGolden})
	w := eeSeed(t, ctx, eeLineFixture)

	jobID := eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeGoldenReader(t, eeLineGolden)))

	rankZero := map[int]bool{}
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if r.rank != 0 {
			continue
		}
		if idx, _, ok := extraction.ParseLineFieldName(r.name); ok {
			rankZero[idx] = true
		}
	}
	if len(rankZero) < eeLineMinIdx {
		t.Fatalf("job %s wrote %d distinct line_items[N] index(es) at rank 0 for %s, want at least %d -- with none written, the zero below is not attributable to the write path",
			jobID, len(rankZero), eeLineFixture, eeLineMinIdx)
	}
	if len(rankZero) != eeLineIdxMeasured {
		t.Errorf("job %s wrote %d distinct line_items[N] index(es), pinned at %d -- re-measure and set eeLineIdxMeasured", jobID, len(rankZero), eeLineIdxMeasured)
	}

	eeImport(t, ctx, w)

	id, ok := eeInvoiceIDForDocument(t, ctx, w.documentID)
	if !ok {
		t.Fatalf("%s produced no invoices row; it must import, not quarantine, or the zero below is the quarantine and not the mapper", eeLineFixture)
	}
	if got := eeScoreLines(t, ctx, id); got.reached != 0 || got.priced != 0 {
		t.Errorf("the invoice holds %d line(s) reached / %d priced after %d were extracted; EXTR-24 owns connecting the two and this story must not",
			got.reached, got.priced, len(rankZero))
	}

	// AC-6, the dynamic half: the header row this document produced carries exactly the written
	// fields and not one line name, so a layout with a table did not widen the header walk.
	row := eeWrittenRow(t, ctx, w.documentID)
	if len(row) != len(writtenFields) {
		t.Errorf("the invoices row projects %d field(s) for a table-bearing document, want %d", len(row), len(writtenFields))
	}
	for name := range row {
		if eeIsLineName(name) {
			t.Errorf("the header projection carries %q, a line-item name; a layout with a table changed the header cell count", name)
		}
	}

	eeAssertControl(t, ctx, w, "EE-LINES-CONTROL-4")
}

// AC-7. The line figures carry a mutilation control on the only non-zero state they can have.
// Both mutilations are LineItemInput values inside this test -- no extraction rule, dial,
// lexicon entry or fingerprint version is touched.
func TestRLS_EndToEndAMutilatedLineControlMovesTheNumber(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)

	// Mutilation A: one entry instead of two. reached must fall, priced must not.
	one := eeLineControl()[:1]
	if one[0].UnitPrice == nil {
		t.Fatal("the first control entry carries no UnitPrice, so cutting to it cannot hold priced at 1")
	}
	gotA := eeScoreLines(t, ctx, eeCreateInvoice(t, ctx, w, "EE-LINES-MUT-A", one))
	if gotA.reached != 1 || gotA.priced != 1 {
		t.Errorf("the one-entry build scores %d reached / %d priced, want 1 / 1 -- a scorer that cannot move reads the same number for every input", gotA.reached, gotA.priced)
	}

	// Mutilation B: two entries, every UnitPrice nil. reached must hold, priced must fall.
	nilPriced := eeLineControl()
	for i := range nilPriced {
		nilPriced[i].UnitPrice = nil
	}
	gotB := eeScoreLines(t, ctx, eeCreateInvoice(t, ctx, w, "EE-LINES-MUT-B", nilPriced))
	if gotB.reached != 2 || gotB.priced != 0 {
		t.Errorf("the unpriced build scores %d reached / %d priced, want 2 / 0 -- priced is not reading unit_price", gotB.reached, gotB.priced)
	}

	// The unmutilated build LAST, in the same test: without it, a scorer that always returns
	// the mutilated numbers would pass both checks above.
	gotOK := eeScoreLines(t, ctx, eeCreateInvoice(t, ctx, w, "EE-LINES-MUT-OK", eeLineControl()))
	if gotOK.reached != eeLineControlReached || gotOK.priced != eeLineControlPriced {
		t.Errorf("the unmutilated build scores %d reached / %d priced, want %d / %d", gotOK.reached, gotOK.priced, eeLineControlReached, eeLineControlPriced)
	}
	if gotA.reached == gotOK.reached {
		t.Errorf("cutting an entry left reached at %d; the number does not move", gotA.reached)
	}
	if gotB.priced == gotOK.priced {
		t.Errorf("nilling every UnitPrice left priced at %d; the number does not move", gotB.priced)
	}
}

// AC-8. The line figure is not a recall measure: scored from the invoice's own rows and scored
// by any-rank line_items[N] presence in extraction_field_results, the two disagree on the same
// run of the same table-bearing document.
func TestRLS_EndToEndTheLineScoreIsNotARecallMeasure(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture, eeLineGolden})
	w := eeSeed(t, ctx, eeLineFixture)

	jobID := eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeGoldenReader(t, eeLineGolden)))

	anyRank := map[int]bool{}
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if idx, _, ok := extraction.ParseLineFieldName(r.name); ok {
			anyRank[idx] = true
		}
	}
	// Mandated fatal: a zero any-rank read makes the divergence vacuous -- 0 differs from 0
	// nowhere, and a run that extracted nothing would read as proof.
	if len(anyRank) == 0 {
		t.Fatalf("job %s wrote no line_items[N] row at any rank for %s; the divergence below would be 0 against 0", jobID, eeLineFixture)
	}
	if len(anyRank) < eeLineMinIdx {
		t.Fatalf("job %s wrote %d distinct line_items[N] index(es) at any rank, want at least %d", jobID, len(anyRank), eeLineMinIdx)
	}
	if len(anyRank) != eeLineIdxMeasured {
		t.Errorf("job %s wrote %d distinct line_items[N] index(es) at any rank, pinned at %d -- re-measure and set eeLineIdxMeasured", jobID, len(anyRank), eeLineIdxMeasured)
	}

	// The divergence is invoice-vs-extraction, not rank-vs-rank: this fixture writes the same
	// index set at rank 0 as at any rank, so "any rank" is not smuggling in extra rows.
	rankZero := map[int]bool{}
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if r.rank != 0 {
			continue
		}
		if idx, _, ok := extraction.ParseLineFieldName(r.name); ok {
			rankZero[idx] = true
		}
	}
	if len(rankZero) != len(anyRank) {
		t.Errorf("rank 0 carries %d index(es) and any rank %d; the divergence asserted below would rest on rank, which is not what this spec claims",
			len(rankZero), len(anyRank))
	}

	eeImport(t, ctx, w)
	id, ok := eeInvoiceIDForDocument(t, ctx, w.documentID)
	if !ok {
		t.Fatalf("%s produced no invoices row, so there is nothing to score the any-rank read against", eeLineFixture)
	}
	byInvoice := eeScoreLines(t, ctx, id)

	if byInvoice.reached != 0 {
		t.Errorf("the invoice read scores %d, want 0 -- the mapper writes no line row", byInvoice.reached)
	}
	if byInvoice.reached == len(anyRank) {
		t.Errorf("the invoice read and the any-rank read both score %d; the figure cannot tell a line that reached the invoice from one that was merely extracted, which is what a recall measure does",
			byInvoice.reached)
	}

	// The divergence is between a real number and a zero, not between two unread zeros.
	eeAssertControl(t, ctx, w, "EE-LINES-CONTROL-5")
}

// "Priced" is count(unit_price): a column fact, not a judgement about the value. The three
// states a unit price can be in have to land on different sides of that count, or a figure
// named "priced" quietly means something else.
func TestRLS_EndToEndPricedCountsTheColumnNotTheValue(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)
	sp := func(s string) *string { return &s }

	// A zero price is still a price. If this ever read 0, "priced" would be silently measuring
	// non-zero amounts and the control's 1-of-2 would hold for the wrong reason.
	zero := []invoice.LineItemInput{
		{Description: sp("Freebie"), UnitPrice: sp("0.00"), LineTotal: sp("0.00")},
		{Description: sp("Also free"), UnitPrice: sp("0"), LineTotal: sp("0.00")},
	}
	if got := eeScoreLines(t, ctx, eeCreateInvoice(t, ctx, w, "EE-LINES-ZERO", zero)); got.reached != 2 || got.priced != 2 {
		t.Errorf("two lines priced at zero score %d reached / %d priced, want 2 / 2 -- priced counts a present unit_price, not a non-zero one", got.reached, got.priced)
	}

	// A nil price is the only thing that leaves the column NULL, and it is the contrast that
	// makes the zero case above mean something.
	nilPriced := eeLineControl()
	for i := range nilPriced {
		nilPriced[i].UnitPrice = nil
	}
	if got := eeScoreLines(t, ctx, eeCreateInvoice(t, ctx, w, "EE-LINES-NULL", nilPriced)); got.reached != 2 || got.priced != 0 {
		t.Errorf("two lines with no unit price score %d reached / %d priced, want 2 / 0", got.reached, got.priced)
	}

	// An empty string is neither: unit_price is written through ::text::numeric
	// (internal/invoice/store.go:250-252), so "" must be rejected outright rather than land as
	// a NULL that deflates priced without anyone asking for it.
	blank := eeLineControl()
	blank[0].UnitPrice = sp("")
	inv, err := eeTryCreateInvoice(t, ctx, w, "EE-LINES-BLANK", blank)
	if err == nil {
		got := eeScoreLines(t, ctx, inv.ID)
		t.Errorf("an empty-string unit price was accepted and scores %d reached / %d priced; a blank must not become a silent NULL", got.reached, got.priced)
	}

	eeAssertControl(t, ctx, w, "EE-LINES-CONTROL-6")
}
