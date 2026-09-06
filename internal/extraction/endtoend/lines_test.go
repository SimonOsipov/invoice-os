// lines_test.go: the pure half of the line-item outcome -- the expected-line denominator taken
// off the committed goldens, the rule that keeps line names out of the header count, and the
// two report sections that must render two raw integers rather than a ratio.
// No database.
package endtoend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The two headings the line figures render under. Both carry a space-separated name, so the
// flat len(parts)==2 parser in TestEndToEnd_TheReportPrintsEachRowsOwnNumbers steps over them
// and the layout names appearing in three sections cannot collide in its map.
const (
	eeLinesReachedHeading = "  line items reached / expected:"
	eeLinesPricedHeading  = "  line items priced / reached:"
)

// eeLineBlockName is Reconcile's block row (internal/extraction/reconcile.go:121 sets the
// literal, :197 appends it). It is not a cell name, so ParseLineFieldName refuses it and the
// header walk needs a second clause to exclude it.
const eeLineBlockName = "line_items"

// eeLineRichLines is how many DocLines rich_invoice.docling.json yields -- the positive control
// on the oracle below, which would otherwise be satisfied by a reader that always returns none.
// Measured: one 5x4 table (Widget, Gadget, Delivery, Handling).
const eeLineRichLines = 4

// eeLineRichGolden is the only committed golden carrying a table. It is deliberately NOT in
// requiredGoldens: TestEndToEnd_TheScoredSetIsTheRequiredSet fatals unless requiredPDFs holds
// exactly eeLayoutCount entries.
const eeLineRichGolden = "rich_invoice.docling.json"

// eeLinesExpected is how many line rows each layout's document really carries -- the
// denominator of the reached figure. Never hand-trusted: the spec below re-derives every entry
// from that layout's committed docling golden.
//
// Measured: only wild_ruled_lines_totals.pdf prints an item table; no other scored layout
// carries one, so every other denominator is 0.
var eeLinesExpected = map[string]int{
	"corpus_inline_labels.pdf":    0,
	"corpus_split_labels.pdf":     0,
	"corpus_stacked_labels.pdf":   0,
	"corpus_two_column.pdf":       0,
	"corpus_ambiguous_date.pdf":   0,
	"corpus_totals_block.pdf":     0,
	"wild_two_party_bare_tin.pdf": 0,
	"wild_ruled_lines_totals.pdf": 3,
	"wild_rc_due_naira.pdf":       0,
	"wild_stacked_borderless.pdf": 0,
}

// eeGoldenReader replays one committed docling golden through the real DoclingReader. The
// production reader, not a hand-rolled decoder: Page.Tables is what LineItems reads, and only
// this reader fills it (internal/extraction/docling.go:112). PDFiumReader leaves it nil
// (internal/extraction/pagereader.go:69-71), which is why the worker needs this on the Text
// seam to write a line row at all. Precedent: wpDoclingReader, worker_pipeline_db_test.go:129.
func eeGoldenReader(t *testing.T, golden string) extraction.PageReader {
	t.Helper()
	eeRequireFixtures(t, []string{golden})

	body := eeFixtureBytes(t, golden)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	r, err := extraction.NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}
	return r
}

// eeGoldenPages is every page one golden emits, for the pure line-count oracle.
func eeGoldenPages(t *testing.T, golden string) []extraction.Page {
	t.Helper()
	r := eeGoldenReader(t, golden)

	var pages []extraction.Page
	onPage := func(p extraction.Page) error {
		pages = append(pages, p)
		return nil
	}
	if _, err := r.Read(t.Context(), extraction.Document{ContentType: eeContentType}, onPage); err != nil {
		t.Fatalf("replay golden %s: %v", golden, err)
	}
	if len(pages) == 0 {
		t.Fatalf("golden %s replayed 0 page(s); a line count taken off it would be zero for the wrong reason", golden)
	}
	return pages
}

// eeIsLineName is the one predicate both directions of AC-6 use: the per-cell
// line_items[N].<role> names and the block row that carries them.
func eeIsLineName(name string) bool {
	if _, _, ok := extraction.ParseLineFieldName(name); ok {
		return true
	}
	return name == eeLineBlockName
}

// eeReportSection returns the lines rendered under heading, up to the next line that is not
// indented as a row. Per section, never a flat name->ratio map: a layout name appears in
// byLayout AND in both line sections, and a flat map keeps only the last of the three.
func eeReportSection(report, heading string) ([]string, bool) {
	var rows []string
	var found bool
	for _, line := range strings.Split(report, "\n") {
		if line == heading {
			found = true
			continue
		}
		if !found {
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			break
		}
		rows = append(rows, strings.TrimSpace(line))
	}
	return rows, found
}

// --- the specs --------------------------------------------------------------

// AC-1. Both line figures render under the SAME marker as the header number, in one rendered
// output, so the CI step that greps for the marker carries both or neither.
func TestEndToEnd_TheLineScoreRendersUnderTheSameMarker(t *testing.T) {
	var s eeScore
	s.hits, s.total = 32, eeWrittenCells
	for _, want := range expectByLayout {
		s.byLayout = append(s.byLayout, eeScoreRow{name: want.file, hits: 4, total: len(writtenFields)})
		s.linesReached = append(s.linesReached, eeScoreRow{name: want.file, hits: 0, total: 2})
		s.linesPriced = append(s.linesPriced, eeScoreRow{name: want.file, hits: 0, total: 0})
	}
	if len(s.linesReached) == 0 || len(s.linesPriced) == 0 {
		t.Fatal("the synthetic score carries no line rows, so the checks below assert nothing")
	}

	report := eeRenderReport(s)

	// Control needle: the marker itself must be found, or every check below reads the same on
	// a renderer that emitted nothing at all.
	if !strings.Contains(report, eeReportMarker) {
		t.Fatalf("the render carries no %q, so it is not the report this spec is about:\n%s", eeReportMarker, report)
	}
	// The header block is the other control: a renderer that dropped BOTH blocks must not read
	// as "the line block is missing".
	if !strings.Contains(report, "  per layout:") {
		t.Fatalf("the render carries no per-layout block; the whole report is missing, not just the line rows:\n%s", report)
	}

	for _, heading := range []string{eeLinesReachedHeading, eeLinesPricedHeading} {
		rows, ok := eeReportSection(report, heading)
		if !ok {
			t.Errorf("the render carries no %q section; a renderer that emits the header block and drops the line block leaves the number unreported:\n%s", strings.TrimSpace(heading), report)
			continue
		}
		if len(rows) != len(expectByLayout) {
			t.Errorf("the %q section renders %d row(s), want %d -- one per layout", strings.TrimSpace(heading), len(rows), len(expectByLayout))
		}
		for _, want := range expectByLayout {
			var named bool
			for _, row := range rows {
				if strings.HasPrefix(row, want.file) {
					named = true
				}
			}
			if !named {
				t.Errorf("the %q section names no row for %s:\n%s", strings.TrimSpace(heading), want.file, report)
			}
		}
	}
}

// AC-5. The reached figure's denominator is taken off each layout's own golden, not pinned by
// hand: a layout that grows a table cannot then be scored against a denominator of zero.
func TestEndToEnd_TheExpectedLineCountIsTakenOffTheGoldens(t *testing.T) {
	// Positive control FIRST: an oracle that always returns none satisfies every zero row
	// below, so the one golden known to carry a table has to move it.
	rich := len(extraction.LineItems(eeGoldenPages(t, eeLineRichGolden)))
	if rich == 0 {
		t.Fatalf("%s yields 0 line(s); the oracle reads no table at all, so the per-layout counts below prove nothing", eeLineRichGolden)
	}
	if rich != eeLineRichLines {
		t.Errorf("%s yields %d line(s), pinned at %d -- re-measure and set eeLineRichLines", eeLineRichGolden, rich, eeLineRichLines)
	}

	if len(expectByLayout) == 0 {
		t.Fatal("expectByLayout is empty; every per-layout check below walks nothing")
	}

	// One key per scored layout, no extras: an entry for a layout nobody scores is a
	// denominator nothing checks.
	if len(eeLinesExpected) != len(expectByLayout) {
		t.Errorf("eeLinesExpected holds %d entry(ies), want %d -- one per expectByLayout row", len(eeLinesExpected), len(expectByLayout))
	}
	for _, want := range expectByLayout {
		if _, ok := eeLinesExpected[want.file]; !ok {
			t.Errorf("eeLinesExpected names no denominator for %s", want.file)
		}
	}
	for file := range eeLinesExpected {
		var scored bool
		for _, want := range expectByLayout {
			if want.file == file {
				scored = true
			}
		}
		if !scored {
			t.Errorf("eeLinesExpected names %s, which no expectByLayout row scores", file)
		}
	}

	for i, want := range expectByLayout {
		golden := requiredGoldens[i]
		if got := strings.TrimSuffix(want.file, ".pdf") + ".docling.json"; golden != got {
			t.Fatalf("requiredGoldens[%d] = %q but expectByLayout[%d] is %q; the two lists have drifted and the counts would be taken off the wrong file", i, golden, i, want.file)
		}
		got := len(extraction.LineItems(eeGoldenPages(t, golden)))
		if got != eeLinesExpected[want.file] {
			t.Errorf("%s carries %d line(s) by its own golden, but eeLinesExpected pins %d -- the denominator is not the document's",
				want.file, got, eeLinesExpected[want.file])
		}
	}
}

// AC-6. Line-item names never enter the header denominator: neither the block row nor any
// line_items[N].<role> cell is a header field, so a layout carrying a table cannot change the
// header cell count.
func TestEndToEnd_TheLineBlockIsNotAHeaderField(t *testing.T) {
	// Positive control, same test: a predicate that always returns false satisfies every
	// absence below.
	for _, name := range []string{
		extraction.LineFieldName(1, extraction.LineRoleUnitPrice),
		extraction.LineFieldName(2, extraction.LineRoleDescription),
		eeLineBlockName,
	} {
		if !eeIsLineName(name) {
			t.Fatalf("eeIsLineName(%q) is false; the predicate recognises nothing, so the exclusions below prove nothing", name)
		}
	}
	// Negative control: a header field must not read as a line name.
	if eeIsLineName("invoice_number") {
		t.Fatal("eeIsLineName(\"invoice_number\") is true; the predicate matches everything")
	}

	// Floors: an empty vocabulary satisfies every exclusion.
	if len(extraction.HeaderFields) == 0 || len(writtenFields) == 0 || len(expectByLayout) == 0 {
		t.Fatalf("HeaderFields=%d writtenFields=%d expectByLayout=%d; nothing is walked", len(extraction.HeaderFields), len(writtenFields), len(expectByLayout))
	}

	for _, name := range extraction.HeaderFields {
		if eeIsLineName(name) {
			t.Errorf("extraction.HeaderFields carries %q, a line-item name; the header vocabulary is counting line cells", name)
		}
	}
	for _, name := range writtenFields {
		if eeIsLineName(name) {
			t.Errorf("writtenFields carries %q, a line-item name; the header denominator would grow with the table", name)
		}
	}
	for _, want := range expectByLayout {
		for name := range want.fields {
			if eeIsLineName(name) {
				t.Errorf("%s expects %q, a line-item name; the header score would be taken over line cells", want.file, name)
			}
		}
	}

	// The same fact from the count side: the header denominator is what it was before any
	// layout grew a table.
	if got := eeCountCells(); got != eeWrittenCells {
		t.Errorf("the header walk counts %d cell(s), want %d -- a line name has entered the header denominator", got, eeWrittenCells)
	}
}

// AC-9. Both figures are two raw integers and a slash. With reached at 0 on every layout the
// priced row renders 0/0: it neither divides, nor omits the row, nor prints NaN.
func TestEndToEnd_AZeroReachedLineRowStillRenders(t *testing.T) {
	var s eeScore
	s.hits, s.total = 32, eeWrittenCells
	for _, want := range expectByLayout {
		s.byLayout = append(s.byLayout, eeScoreRow{name: want.file, hits: 4, total: len(writtenFields)})
		// expected=2 with reached=0, so the two rows carry DIFFERENT ratios: a renderer
		// printing one row's numbers for both would otherwise pass.
		s.linesReached = append(s.linesReached, eeScoreRow{name: want.file, hits: 0, total: 2})
		s.linesPriced = append(s.linesPriced, eeScoreRow{name: want.file, hits: 0, total: 0})
	}
	if len(s.linesReached) != len(expectByLayout) || len(s.linesPriced) != len(expectByLayout) {
		t.Fatal("the synthetic score is short a line row, so the checks below assert nothing")
	}

	report := eeRenderReport(s)
	if !strings.Contains(report, eeReportMarker) {
		t.Fatalf("the render carries no %q:\n%s", eeReportMarker, report)
	}

	for _, sec := range []struct {
		heading string
		ratio   string
	}{
		{eeLinesReachedHeading, "0/2"},
		{eeLinesPricedHeading, "0/0"},
	} {
		rows, ok := eeReportSection(report, sec.heading)
		if !ok {
			t.Errorf("the render carries no %q section; a row omitted for reached==0 reads as tested:\n%s", strings.TrimSpace(sec.heading), report)
			continue
		}
		for _, want := range expectByLayout {
			var got string
			var named bool
			for _, row := range rows {
				name, ratio, cut := strings.Cut(row, " ")
				if cut && name == want.file {
					named, got = true, strings.TrimSpace(ratio)
				}
			}
			if !named {
				t.Errorf("the %q section omits %s; every layout renders, at zero or not:\n%s", strings.TrimSpace(sec.heading), want.file, report)
				continue
			}
			if got != sec.ratio {
				t.Errorf("the %q section renders %q for %s, want %q", strings.TrimSpace(sec.heading), got, want.file, sec.ratio)
			}
		}
	}

	for _, poison := range []string{"NaN", "+Inf", "-Inf"} {
		if strings.Contains(report, poison) {
			t.Errorf("the report carries %q; the line figures are being divided rather than printed as two counts:\n%s", poison, report)
		}
	}
}

// The corpus carries no table at all, so both line sections render 0/0 on every row -- which
// reads exactly like a denominator that was satisfied. The report has to say so in words, and
// stop saying it the moment a table-bearing layout joins the scored set (EXTR-21-06).
func TestEndToEnd_AnEmptyLineCorpusSaysSoInWords(t *testing.T) {
	build := func(expected int) eeScore {
		var s eeScore
		s.hits, s.total = 32, eeWrittenCells
		for _, want := range expectByLayout {
			s.linesReached = append(s.linesReached, eeScoreRow{name: want.file, hits: 0, total: expected})
			s.linesPriced = append(s.linesPriced, eeScoreRow{name: want.file, hits: 0, total: 0})
		}
		return s
	}
	if len(expectByLayout) == 0 {
		t.Fatal("expectByLayout is empty, so neither render below carries a line row")
	}

	empty := eeRenderReport(build(0))
	if !strings.Contains(empty, eeNoLineSignalNote) {
		t.Errorf("every layout scores its reached figure against a denominator of 0, and the report says nothing about it; six rows of 0/0 read as coverage:\n%s", empty)
	}

	// The other direction, or the note becomes boilerplate that survives the corpus growing a
	// table and goes on calling a real measurement no signal.
	withTable := eeRenderReport(build(2))
	if strings.Contains(withTable, eeNoLineSignalNote) {
		t.Errorf("a layout carrying 2 expected line(s) still renders the no-signal note; the note is unconditional:\n%s", withTable)
	}

	// A nil-line score is the quarantine-only render, which carries no line rows to explain.
	if got := eeRenderReport(eeScore{}); strings.Contains(got, eeNoLineSignalNote) {
		t.Errorf("a score with no line rows at all renders the no-signal note:\n%s", got)
	}
}
