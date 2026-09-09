// lines_db_test.go: the line-item outcome, read off the invoice the path actually wrote.
//
// documentCreateInput now groups line_items[N].<role> extraction fields onto
// invoice.CreateInput.LineItems, so the worker's line rows reach the invoice -- proven below on
// rich_invoice.pdf and on wild_ruled_lines_totals.pdf, the two committed fixtures carrying both
// a table and a docling golden.
//
// The corpus-wide headline (TestRLS_EndToEndScoresLineItemOutcome) still reads zero, and that
// zero is now a TEXT-SEAM fact rather than a wiring one: no corpus_* layout carries a table, and
// the one wild_* layout that does is read through pdfium in that walk, which extracts no line
// row. TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling measures both readers on it.
//
// Every non-zero figure here still ships with a positive control in the same test, so a scorer
// that cannot read line_items cannot fake it.
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

// eeLineFixtureReached/eeLineFixturePriced are rich_invoice.pdf's own line figures once
// imported: every extracted index reaches the invoice (so reached ties eeLineIdxMeasured), and
// 3 of the 4 carry a unit_price -- measured, not assumed.
const (
	eeLineFixtureReached = eeLineIdxMeasured
	eeLineFixturePriced  = 3
)

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
// is AC-8's point: a line quarantined out of the invoice would still show up in an any-rank
// extraction read, so the two scores are not interchangeable even where they agree today.
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
	// them. A quarantined layout has no invoice to read, so the rule is the two independent
	// counts together -- TestEndToEnd_TheWidenedLineScoredCheckStillCatchesASilentScorer
	// falsifies the rule itself.
	if err := eeLinesScoredComplete(s); err != nil {
		t.Fatalf("%v", err)
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
		// The zero is a text-seam fact, not a mapper one: eeOptsFor hands a docling golden only
		// to eeOCRLayouts, so every other layout is read through pdfium here and pdfium yields
		// no line_items[N] row on any of them.
		// TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling measures both readers on
		// the one layout that carries a table, so this zero cannot be read as "lines never
		// reach an invoice".
		if reached.hits != 0 {
			t.Errorf("%s reached %d line(s) on the invoice; no layout in this walk extracts a line row through pdfium, so a non-zero here means the read is not the invoice's own rows",
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

// AC-4, AC-6. The worker writes line_items[N].<role> rows for a table-bearing document, and the
// invoice it fed now holds them, one row per extracted index -- documentCreateInput's grouping.
func TestRLS_EndToEndTheWorkerWroteLineRowsAndTheInvoiceGotThem(t *testing.T) {
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
	if got := eeScoreLines(t, ctx, id); got.reached != eeLineFixtureReached || got.priced != eeLineFixturePriced {
		t.Errorf("the invoice holds %d line(s) reached / %d priced after %d were extracted, want %d / %d -- documentCreateInput must group every extracted index onto the invoice",
			got.reached, got.priced, len(rankZero), eeLineFixtureReached, eeLineFixturePriced)
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

// AC-8. rich_invoice.pdf has zero attrition -- every extracted index reaches the invoice -- so
// the invoice-read score and the any-rank extraction score now agree on it. That does not make
// the invoice read a recall measure: eeScoreLines still counts a different table than
// extraction_field_results, and a quarantined/rejected line would separate the two again.
//
// ceiling: this fixture cannot show that separation; bring back a divergence assertion once a
// fixture with a rejected line row exists.
func TestRLS_EndToEndInvoiceReadMatchesAnyRankOnAZeroAttritionFixture(t *testing.T) {
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

	if byInvoice.reached != len(anyRank) {
		t.Errorf("the invoice read scores %d, want %d (this fixture's any-rank count) -- documentCreateInput must group every extracted index onto the invoice", byInvoice.reached, len(anyRank))
	}

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

// --- the corpus zero's real cause (QA, task-991 Mode B) -----------------------------------

// eeRuledLines* are wild_ruled_lines_totals.pdf's own figures under the DOCLING reader,
// measured: 3 of the document's 3 line rows extract at rank 0, all 3 reach the invoice, and 2 of
// them carry a unit_price.
const (
	eeRuledExtracted = 3
	eeRuledReached   = 3
	eeRuledPriced    = 2
)

// TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling separates the two readers on the
// one scored layout that carries a table. TestRLS_EndToEndScoresLineItemOutcome walks it through
// pdfium and reads 0 of 3; that zero is a text-seam outcome, and reading it as "extracted lines
// do not reach an invoice" would be wrong. Deployed extraction runs docling, and under docling
// every extracted index reaches the invoice.
//
// Both halves live in one test: the pdfium zero alone is satisfied by a scorer that reads
// nothing, and the docling figure alone cannot say what the corpus report's zero means.
func TestRLS_EndToEndTheRuledLayoutReachesTheInvoiceUnderDocling(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	const layout = "wild_ruled_lines_totals.pdf"
	eeRequireFixtures(t, []string{layout, wildGolden(layout)})

	var pdfiumExtracted, pdfiumReached, doclingExtracted int
	var doclingLines eeLineOutcome

	// A subtest per reader: eeExtract registers its client Stop on the t it is given, so two
	// live clients would otherwise share the extraction queue and race for the job.
	t.Run("pdfium", func(t *testing.T) {
		w := eeSeed(t, ctx, layout)
		jobID := eeExtract(t, ctx, w, layout)
		pdfiumExtracted = len(eeLineIndices(t, ctx, jobID))
		eeImport(t, ctx, w)
		if id, ok := eeInvoiceIDForDocument(t, ctx, w.documentID); ok {
			pdfiumReached = eeScoreLines(t, ctx, id).reached
		} else {
			t.Fatalf("%s produced no invoices row under pdfium; the zero below would be a quarantine, not a text-seam outcome", layout)
		}
		eeAssertControl(t, ctx, w, "EE-RULED-CONTROL-PDFIUM")
	})

	t.Run("docling", func(t *testing.T) {
		w := eeSeed(t, ctx, layout)
		jobID := eeExtract(t, ctx, w, layout, eeWithText(eeGoldenReader(t, wildGolden(layout))))
		doclingExtracted = len(eeLineIndices(t, ctx, jobID))
		if doclingExtracted != eeRuledExtracted {
			t.Fatalf("%s extracts %d distinct line_items[N] index(es) at rank 0 under docling, pinned at %d -- re-measure", layout, doclingExtracted, eeRuledExtracted)
		}
		eeImport(t, ctx, w)
		id, ok := eeInvoiceIDForDocument(t, ctx, w.documentID)
		if !ok {
			t.Fatalf("%s produced no invoices row under docling", layout)
		}
		doclingLines = eeScoreLines(t, ctx, id)
		if doclingLines.reached != eeRuledReached || doclingLines.priced != eeRuledPriced {
			t.Errorf("%s holds %d line(s) reached / %d priced under docling, want %d / %d -- every extracted index must reach the invoice",
				layout, doclingLines.reached, doclingLines.priced, eeRuledReached, eeRuledPriced)
		}
	})

	if pdfiumExtracted != 0 {
		t.Errorf("%s extracted %d line index(es) under pdfium; the corpus walk's zero is then NOT a text-seam outcome and its stated cause is wrong", layout, pdfiumExtracted)
	}
	if pdfiumReached != 0 {
		t.Errorf("%s reached %d line(s) under pdfium, want 0 -- TestRLS_EndToEndScoresLineItemOutcome pins the same zero", layout, pdfiumReached)
	}
	if doclingLines.reached == pdfiumReached {
		t.Errorf("both readers score %d reached; this test cannot then attribute the corpus zero to the text seam", pdfiumReached)
	}
}

// eeLineIndices is the distinct rank-0 line_items[N] index set jobID wrote.
func eeLineIndices(t *testing.T, ctx context.Context, jobID string) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if r.rank != 0 {
			continue
		}
		if idx, _, ok := extraction.ParseLineFieldName(r.name); ok {
			out[idx] = true
		}
	}
	return out
}
