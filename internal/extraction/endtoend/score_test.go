// score_test.go: what the end-to-end path is scored against -- the written-field vocabulary,
// the per-layout expectation table taken off the fixture generators, and the rendered report.
// No database.
//
// The table is built from the generators (internal/extraction/fixtures_test.go), never from
// corpusExpect: that table is deliberately partial, and copying its partiality is the inflation
// this suite exists to close.
package endtoend

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// eeReportMarker titles the report, so a rename cannot leave a grep pointed at nothing.
const eeReportMarker = "end-to-end field accuracy, document in to invoice row out"

const (
	// eeWrittenCells is hand-written: 6 layouts x 8 written fields. A deleted expectByLayout
	// row must fail here rather than flatter the rate by shrinking the denominator.
	eeWrittenCells = 48
	eeLayoutCount  = 6

	// The cells the six layouts carry no value for. Pinned so an expectation cannot be
	// silently emptied to dodge a miss.
	eeAbsentCellCount = 12

	// eeQuarantineLayout is image-only: zero text chars, so the import quarantines it and
	// writes no invoices row.
	eeQuarantineLayout = "scanned_invoice.pdf"
)

// eeCorpusHits is the six-layout figure, measured 2026-09-06 and pinned. Equality, not a
// floor: an unrecorded improvement must red too. EXTR-21-09 owns the ratchet, once the wild
// layouts land.
//
// The 16 misses are named cell by cell in eeAbsentCells and eeRealMisses, and
// TestRLS_EndToEndScoresTheCorpus holds the score to that exact set.
const (
	eeCorpusHits  = 32
	eeCorpusCells = eeWrittenCells
)

// writtenFields is what documentCreateInput actually puts in the invoices row, in
// mapperFieldNames order (internal/importer/document.go:174-183).
var writtenFields = []string{
	"invoice_number", "issue_date", "buyer_tin", "buyer_name",
	"currency", "subtotal", "vat", "total",
}

// entityDerivedFields are read off the document but never written from it: Store.Create
// overwrites both from the business entity on every write (internal/invoice/store.go:220-221),
// so scoring them here would score a value the store discards.
var entityDerivedFields = []string{"supplier_tin", "supplier_name"}

// eeShapeOf is the normaliser each written field's values are compared under.
var eeShapeOf = map[string]extraction.Shape{
	"invoice_number": extraction.ShapeInvoiceNumber,
	"issue_date":     extraction.ShapeDate,
	"buyer_tin":      extraction.ShapeTIN,
	"buyer_name":     extraction.ShapeName,
	"currency":       extraction.ShapeCurrency,
	"subtotal":       extraction.ShapeAmount,
	"vat":            extraction.ShapeAmount,
	"total":          extraction.ShapeAmount,
}

// expectByLayout holds ONE key per writtenFields entry on every row, always 8. An empty value
// list is an assertion too: it can never be a hit, so a field the bytes do not carry lowers the
// number instead of vanishing from the denominator.
var expectByLayout = []struct {
	file   string
	fields map[string][]string
}{
	{
		file: "corpus_inline_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1001"},
			"issue_date":     {"2026-03-04"},
			"buyer_tin":      {"99999999-0102"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {"1000.00"},
			"vat":            {"75.00"},
			"total":          {"1075.00"},
		},
	},
	{
		file: "corpus_split_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1002"},
			"issue_date":     {"2026-04-15"},
			"buyer_tin":      {"99999999-0202"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {"2000.00"},
			"vat":            {"150.00"},
			"total":          {"2150.00"},
		},
	},
	{
		file: "corpus_stacked_labels.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1003"},
			"issue_date":     {"2026-04-22"},
			"buyer_tin":      {"99999999-0302"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {},
			"vat":            {},
			"total":          {"3225.00"},
		},
	},
	{
		file: "corpus_two_column.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1004"},
			"issue_date":     {"2026-05-06"},
			"buyer_tin":      {"99999999-0402"},
			"buyer_name":     {"Honeywell Group"},
			"currency":       {"NGN"},
			"subtotal":       {},
			"vat":            {},
			"total":          {"6450.00"},
		},
	},
	{
		file: "corpus_ambiguous_date.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1005"},
			// 12/03/2026 is ambiguous: ShapeDate returns both readings and either is a hit.
			"issue_date": {"2026-03-12", "2026-12-03"},
			"buyer_tin":  {},
			"buyer_name": {},
			"currency":   {"NGN"},
			"subtotal":   {},
			"vat":        {},
			"total":      {"4300.00"},
		},
	},
	{
		file: "corpus_totals_block.pdf",
		fields: map[string][]string{
			"invoice_number": {"INV-1006"},
			"issue_date":     {},
			"buyer_tin":      {},
			"buyer_name":     {},
			"currency":       {},
			"subtotal":       {"5000.00"},
			"vat":            {"375.00"},
			"total":          {"5375.00"},
		},
	},
}

// eeAbsentCells names every cell the bytes carry no value for, with the reason. A shape-level
// "no amount on the page" check cannot stand in: stacked prints 3,225.00 and still has no
// sub-total.
var eeAbsentCells = map[string]string{
	"corpus_stacked_labels.pdf/subtotal":   "the layout prints one total and no sub-total line",
	"corpus_stacked_labels.pdf/vat":        "the layout prints no VAT line",
	"corpus_two_column.pdf/subtotal":       "the layout prints one total and no sub-total line",
	"corpus_two_column.pdf/vat":            "the layout prints no VAT line",
	"corpus_ambiguous_date.pdf/buyer_tin":  "the layout carries the supplier block only",
	"corpus_ambiguous_date.pdf/buyer_name": "the layout carries the supplier block only",
	"corpus_ambiguous_date.pdf/subtotal":   "the layout prints one total and no sub-total line",
	"corpus_ambiguous_date.pdf/vat":        "the layout prints no VAT line",
	"corpus_totals_block.pdf/issue_date":   "the totals block carries no date",
	"corpus_totals_block.pdf/buyer_tin":    "the totals block carries no buyer",
	"corpus_totals_block.pdf/buyer_name":   "the totals block carries no buyer",
	"corpus_totals_block.pdf/currency":     "every amount is bare, with no currency token",
}

// eeRealMisses are the cells the bytes DO carry and the invoices row does not. Pinned beside
// eeAbsentCells so the miss set splits into what the corpus can never win and what extraction
// owes: the hit count alone holds at 32 while one absent cell resolves and one real hit breaks.
// EXTR-22..28 close these; do not close them here.
var eeRealMisses = map[string]string{
	"corpus_stacked_labels.pdf/currency": "NGN is printed inside the total, with no currency label to anchor it",
	"corpus_two_column.pdf/currency":     "NGN is printed inside the total, with no currency label to anchor it",
	"corpus_ambiguous_date.pdf/currency": "NGN is printed inside the total, with no currency label to anchor it",
	"corpus_two_column.pdf/buyer_tin":    "the bare TIN label matches supplier_tin only; docs/extraction-corpus.md records it as t1aGaps",
}

// eeCell is one (layout, field) cell -- the unit the rate counts.
type eeCell struct{ layout, field string }

func (c eeCell) key() string { return c.layout + "/" + c.field }

// eeScoreRow is one line of the report. eeRow is taken by harness_db_test.go.
type eeScoreRow struct {
	name        string
	hits, total int
}

// eeScore is one walk of expectByLayout through the end-to-end path.
type eeScore struct {
	hits, total int
	missed      []eeCell
	saw         map[eeCell]string // what the invoices row holds; "" is NULL
	quarantined []string
	byLayout    []eeScoreRow // one per expectByLayout row, table order
	byField     []eeScoreRow // one per writtenFields entry, INCLUDING any at zero

	// The line-item outcome, one row per layout each. Both ride eeScoreRow's two integers, so
	// the renderer never divides: linesPriced at 0/0 is a slash between two counts, not NaN.
	linesReached []eeScoreRow // hits = lines that reached the invoice, total = lines the document carries
	linesPriced  []eeScoreRow // hits = lines carrying a unit price, total = lines that reached
}

// eeCountCells counts the denominator straight off the table, resolving nothing. A row missing
// a writtenFields key drops the count, so the 8-keys-per-row rule is enforced here.
func eeCountCells() int {
	n := 0
	for _, want := range expectByLayout {
		for _, field := range writtenFields {
			if _, ok := want.fields[field]; ok {
				n++
			}
		}
	}
	return n
}

// eeExpectedValues is what the table names for one cell, for a failure message.
func eeExpectedValues(c eeCell) []string {
	for _, want := range expectByLayout {
		if want.file == c.layout {
			return want.fields[c.field]
		}
	}
	return nil
}

// eeRenderReport is the block CI reads. Both loops are unconditional: a field standing at 0/N
// renders 0/N rather than vanishing, because a field silently absent from the report reads as
// tested.
func eeRenderReport(s eeScore) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s: %d / %d\n", eeReportMarker, s.hits, s.total)

	b.WriteString("  per layout:\n")
	for _, r := range s.byLayout {
		fmt.Fprintf(&b, "    %-28s %d/%d\n", r.name, r.hits, r.total)
	}
	b.WriteString("  per field:\n")
	for _, r := range s.byField {
		fmt.Fprintf(&b, "    %-28s %d/%d\n", r.name, r.hits, r.total)
	}
	for _, layout := range s.quarantined {
		fmt.Fprintf(&b, "  QUARANTINED %s -- no invoices row; all %d cells count as misses\n", layout, len(writtenFields))
	}
	for _, c := range s.missed {
		fmt.Fprintf(&b, "  MISS %s / %s wanted %v, invoice holds %q\n", c.layout, c.field, eeExpectedValues(c), s.saw[c])
	}
	return b.String()
}

// eeNGrams splits one token on whitespace, drops a trailing colon from each word, and returns
// every 1-, 2- and 3-gram. Values are printed inside a label token ("Buyer: Honeywell Group"),
// so a whole-token reading alone cannot see them.
func eeNGrams(text string) []string {
	words := strings.Fields(text)
	for i, w := range words {
		words[i] = strings.TrimSuffix(w, ":")
	}
	var out []string
	for n := 1; n <= 3; n++ {
		for i := 0; i+n <= len(words); i++ {
			out = append(out, strings.Join(words[i:i+n], " "))
		}
	}
	return out
}

// eePageTokenReadings is every reading of one layout's own bytes under one field's shape. The
// fixture is read with the production PDFium reader, so the union is what the page really
// carries and not what the generator source says it does.
func eePageTokenReadings(t *testing.T, layout, field string) []string {
	t.Helper()
	shape, ok := eeShapeOf[field]
	if !ok {
		t.Fatalf("no shape for field %q; eeShapeOf must carry one key per writtenFields entry", field)
	}

	var readings []string
	onPage := func(p extraction.Page) error {
		for _, tok := range p.Tokens {
			for _, gram := range eeNGrams(tok.Text) {
				readings = append(readings, shape.Normalize(gram)...)
			}
		}
		return nil
	}
	doc := extraction.Document{Bytes: eeFixtureBytes(t, layout), ContentType: eeContentType}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, onPage); err != nil {
		t.Fatalf("read %s with the PDFium reader: %v", layout, err)
	}
	slices.Sort(readings)
	return slices.Compact(readings)
}

// --- the specs --------------------------------------------------------------

// AC-1. The vocabulary is the mapper's, not a second list that can drift from it. HeaderFields
// minus entityDerivedFields must be writtenFields exactly, in order.
func TestEndToEnd_WrittenFieldsMatchTheMapper(t *testing.T) {
	// Floors first: an empty vocabulary satisfies every quantifier below.
	if len(extraction.HeaderFields) != 10 {
		t.Fatalf("extraction.HeaderFields holds %d name(s), want 10 -- the guard below is comparing against the wrong list", len(extraction.HeaderFields))
	}
	if len(writtenFields) != 8 {
		t.Errorf("writtenFields holds %d name(s), want 8 -- the fields documentCreateInput writes", len(writtenFields))
	}
	if len(writtenFields)+len(entityDerivedFields) != len(extraction.HeaderFields) {
		t.Fatalf("writtenFields (%d) + entityDerivedFields (%d) = %d, want %d -- the two lists do not partition the vocabulary",
			len(writtenFields), len(entityDerivedFields), len(writtenFields)+len(entityDerivedFields), len(extraction.HeaderFields))
	}

	derived := func(name string) bool { return slices.Contains(entityDerivedFields, name) }

	var wantWritten, wantDerived []string
	for _, f := range extraction.HeaderFields {
		if derived(f) {
			wantDerived = append(wantDerived, f)
			continue
		}
		wantWritten = append(wantWritten, f)
	}
	if !slices.Equal(writtenFields, wantWritten) {
		t.Errorf("writtenFields = %v, want %v -- HeaderFields minus entityDerivedFields, in HeaderFields order", writtenFields, wantWritten)
	}
	if !slices.Equal(entityDerivedFields, wantDerived) {
		t.Errorf("entityDerivedFields = %v, want %v -- in HeaderFields order", entityDerivedFields, wantDerived)
	}

	// No name may sit in both halves, and none may be invented.
	seen := map[string]int{}
	for _, f := range slices.Concat(writtenFields, entityDerivedFields) {
		seen[f]++
		if !slices.Contains(extraction.HeaderFields, f) {
			t.Errorf("%q is scored but is not in extraction.HeaderFields", f)
		}
	}
	for f, n := range seen {
		if n != 1 {
			t.Errorf("%q appears %d times across writtenFields and entityDerivedFields, want once", f, n)
		}
	}
	for _, f := range extraction.HeaderFields {
		if seen[f] == 0 {
			t.Errorf("%q is in extraction.HeaderFields but neither written nor pinned as entity-derived -- the mapper gained a field the score cannot see", f)
		}
	}

	// eeShapeOf is keyed on the same list, or a cell would be compared under no normaliser.
	if len(eeShapeOf) != len(writtenFields) {
		t.Errorf("eeShapeOf holds %d key(s), want %d -- one per written field", len(eeShapeOf), len(writtenFields))
	}
	for _, f := range writtenFields {
		if _, ok := eeShapeOf[f]; !ok {
			t.Errorf("eeShapeOf has no shape for %q", f)
		}
	}
}

// AC-2. The exclusion list is pinned and non-empty: a zero-length list would silently swallow
// every real miss on the two supplier fields.
func TestEndToEnd_TheEntityDerivedListIsPinnedAndNonEmpty(t *testing.T) {
	if len(entityDerivedFields) == 0 {
		t.Fatal("entityDerivedFields is empty -- an empty exclusion list excludes nothing today but swallows any field added to it tomorrow without a reason")
	}
	want := []string{"supplier_tin", "supplier_name"}
	if !slices.Equal(entityDerivedFields, want) {
		t.Errorf("entityDerivedFields = %v, want %v -- Store.Create overwrites both from the entity, so neither can be scored against the document", entityDerivedFields, want)
	}
	for _, f := range entityDerivedFields {
		if slices.Contains(writtenFields, f) {
			t.Errorf("%q is excluded and also scored", f)
		}
		if !slices.Contains(extraction.HeaderFields, f) {
			t.Errorf("%q is excluded but is not a vocabulary field at all", f)
		}
	}
}

// AC-3. The denominator, pinned to a hand-written constant. `> 0` survives deleting five of the
// six rows; equality catches a deleted row, which would flatter the rate towards 1.0.
func TestEndToEnd_DenominatorIsTheWrittenCellCount(t *testing.T) {
	if len(expectByLayout) != eeLayoutCount {
		t.Errorf("expectByLayout holds %d row(s), want %d", len(expectByLayout), eeLayoutCount)
	}
	if got := eeCountCells(); got != eeWrittenCells {
		t.Errorf("eeCountCells() = %d, want %d (%d layouts x %d written fields) -- every layout carries one key per written field, present or empty",
			got, eeWrittenCells, eeLayoutCount, len(writtenFields))
	}

	// The count above cannot see a row that carries an EXTRA key, nor a duplicate layout.
	seenLayout := map[string]int{}
	for _, want := range expectByLayout {
		seenLayout[want.file]++
		if len(want.fields) != len(writtenFields) {
			t.Errorf("%s names %d field(s), want exactly %d -- one key per written field", want.file, len(want.fields), len(writtenFields))
		}
		for f := range want.fields {
			if !slices.Contains(writtenFields, f) {
				t.Errorf("%s names %q, which is not a written field", want.file, f)
			}
		}
		if !slices.Contains(requiredPDFs, want.file) {
			t.Errorf("%s is scored but is not in requiredPDFs, so the suite never checks it is on disk", want.file)
		}
	}
	for file, n := range seenLayout {
		if n != 1 {
			t.Errorf("%s appears %d times in expectByLayout, want once", file, n)
		}
	}
	// The quarantine vehicle is scored separately and must not swell the denominator.
	if slices.Contains(requiredPDFs, eeQuarantineLayout) || seenLayout[eeQuarantineLayout] != 0 {
		t.Errorf("%s is in the corpus table; it carries no text at all and would score a free 0/8", eeQuarantineLayout)
	}
}

// AC-4. The needle for `if total == 0 { continue }`. Every field on this corpus is expected to
// resolve somewhere, so only a synthetic all-zero score can prove a 0/0 field still renders.
func TestEndToEnd_AZeroTotalFieldStillRenders(t *testing.T) {
	if len(writtenFields) == 0 {
		t.Fatal("writtenFields is empty, so the loop below asserts nothing")
	}
	zero := eeScore{}
	for _, field := range writtenFields {
		zero.byField = append(zero.byField, eeScoreRow{name: field})
	}
	for _, want := range expectByLayout {
		zero.byLayout = append(zero.byLayout, eeScoreRow{name: want.file})
	}

	out := eeRenderReport(zero)
	if !strings.Contains(out, eeReportMarker) {
		t.Errorf("the report does not carry %q:\n%s", eeReportMarker, out)
	}
	for _, field := range writtenFields {
		if !strings.Contains(out, field) {
			t.Errorf("a score with no expectation at all renders no row for %q; a zero-total field must render 0/0, never be skipped:\n%s", field, out)
		}
	}
	for _, want := range expectByLayout {
		if !strings.Contains(out, want.file) {
			t.Errorf("a score with no expectation at all renders no row for layout %q:\n%s", want.file, out)
		}
	}
	if !strings.Contains(out, "0/0") {
		t.Errorf("a score with no expectation at all renders no 0/0 row:\n%s", out)
	}

	// The other half of AC-4: a miss names the value the invoice actually holds.
	miss := eeCell{layout: expectByLayout[0].file, field: "total"}
	one := eeScore{
		total:       1,
		missed:      []eeCell{miss},
		saw:         map[eeCell]string{miss: "999.00"},
		quarantined: []string{eeQuarantineLayout},
	}
	got := eeRenderReport(one)
	for _, needle := range []string{"MISS", miss.layout, miss.field, "999.00", "QUARANTINED", eeQuarantineLayout} {
		if !strings.Contains(got, needle) {
			t.Errorf("the report does not name %q:\n%s", needle, got)
		}
	}
}

// AC-3, AC-6. The table is only worth measuring against if the fixture bytes really carry it.
// Every non-empty expectation must be reachable from the layout's own text layer, and the
// empty cells must be exactly the pinned twelve.
func TestEndToEnd_EveryExpectationIsCarriedByTheFixtureBytes(t *testing.T) {
	eeRequireFixtures(t, requiredPDFs)

	if len(eeAbsentCells) != eeAbsentCellCount {
		t.Errorf("eeAbsentCells holds %d entry(ies), want %d", len(eeAbsentCells), eeAbsentCellCount)
	}

	asserted := 0
	absent := map[string]bool{}
	for _, want := range expectByLayout {
		// Control: the layout's text layer must yield SOMETHING, or every containment check
		// below passes on an empty union.
		if got := eePageTokenReadings(t, want.file, "invoice_number"); len(got) == 0 {
			t.Fatalf("%s yields no invoice-number reading at all; the read is broken, so the checks below prove nothing", want.file)
		}
		// Negative control: a value the layout does not carry must NOT be reachable.
		const control = "INV-9999"
		if slices.Contains(eePageTokenReadings(t, want.file, "invoice_number"), control) {
			t.Errorf("%s reports %s as reachable; the union is too wide to discriminate", want.file, control)
		}

		for _, field := range writtenFields {
			values := want.fields[field]
			cell := eeCell{layout: want.file, field: field}
			if len(values) == 0 {
				absent[cell.key()] = true
				if _, ok := eeAbsentCells[cell.key()]; !ok {
					t.Errorf("%s / %s expects nothing but carries no reason in eeAbsentCells; an expectation cannot be emptied to dodge a miss", want.file, field)
				}
				continue
			}
			readings := eePageTokenReadings(t, want.file, field)
			for _, v := range values {
				asserted++
				if !slices.Contains(readings, v) {
					t.Errorf("%s / %s expects %q, which no token on the page normalises to under %s; readings were %v",
						want.file, field, v, eeShapeOf[field], readings)
				}
			}
		}
	}

	if asserted < eeWrittenCells-eeAbsentCellCount {
		t.Errorf("the walk asserted %d expected value(s), want at least %d -- a shrunken table proves less than it looks", asserted, eeWrittenCells-eeAbsentCellCount)
	}
	for key := range eeAbsentCells {
		if !absent[key] {
			t.Errorf("eeAbsentCells names %s, but the table expects a value there -- the reason list has drifted from the table", key)
		}
	}
}

// AC-3. The other direction of the requiredPDFs check: a seventh layout registered on disk but
// never given an expectByLayout row is scored by nothing at all, and the pinned 48 stays green
// while the corpus grew.
func TestEndToEnd_TheScoredSetIsTheRequiredSet(t *testing.T) {
	if len(requiredPDFs) != eeLayoutCount {
		t.Fatalf("requiredPDFs holds %d layout(s), expectByLayout scores %d -- a layout on disk that the score never walks is unmeasured, and docs/extraction-corpus.md's \"Adding a layout\" list is short an edit", len(requiredPDFs), eeLayoutCount)
	}
	scored := map[string]int{}
	for _, want := range expectByLayout {
		scored[want.file]++
	}
	for _, file := range requiredPDFs {
		if scored[file] != 1 {
			t.Errorf("%s is a required fixture but carries %d expectByLayout row(s), want 1", file, scored[file])
		}
	}

	// A blank expected value can never equal a column and would read as a silent always-miss.
	for _, want := range expectByLayout {
		for _, field := range writtenFields {
			for _, v := range want.fields[field] {
				if strings.TrimSpace(v) == "" {
					t.Errorf("%s / %s expects a blank value; an empty string is not an expectation, it is a permanent miss", want.file, field)
				}
			}
		}
	}
}

// AC-1. eeShapeOf decides what "the page carries this value" means, so a field compared under
// the wrong normaliser widens or narrows the byte oracle silently. Pinned against the shipped
// rules rather than a second hand-written list.
func TestEndToEnd_ShapesAreTheOnesTheShippedRulesUse(t *testing.T) {
	shipped := map[string]extraction.Shape{}
	for _, r := range extraction.Tier1Rules {
		if have, ok := shipped[r.Field]; ok && have != r.Rule.Shape {
			t.Fatalf("the shipped rules give %q two shapes (%s and %s); there is no single normaliser to pin against", r.Field, have, r.Rule.Shape)
		}
		shipped[r.Field] = r.Rule.Shape
	}
	// Floor: an empty rule set would satisfy every comparison below.
	for _, field := range writtenFields {
		if _, ok := shipped[field]; !ok {
			t.Fatalf("extraction.Tier1Rules names no rule for %q, so the comparison below proves nothing", field)
		}
	}
	for _, field := range writtenFields {
		if eeShapeOf[field] != shipped[field] {
			t.Errorf("eeShapeOf[%q] = %s, but the shipped rules resolve that field under %s", field, eeShapeOf[field], shipped[field])
		}
	}
}

// AC-4. Naming a row is not printing it: the per-field loop can render its numbers the wrong
// way round and every name-and-marker check still passes, so currency reads 6/2 instead of 2/6
// to whoever picks the next defect off this report.
func TestEndToEnd_TheReportPrintsEachRowsOwnNumbers(t *testing.T) {
	var s eeScore
	// Every row gets a distinct, asymmetric hits/total, so no row can borrow another's numbers.
	for i, want := range expectByLayout {
		s.byLayout = append(s.byLayout, eeScoreRow{name: want.file, hits: i, total: i + 10})
	}
	for i, field := range writtenFields {
		s.byField = append(s.byField, eeScoreRow{name: field, hits: i + 1, total: i + 20})
	}
	if len(s.byLayout) == 0 || len(s.byField) == 0 {
		t.Fatal("the synthetic score carries no rows, so the checks below assert nothing")
	}

	// Read the rendered line back by name and compare its trailing ratio, so the check is
	// independent of the column width the renderer chooses.
	printed := map[string]string{}
	for _, line := range strings.Split(eeRenderReport(s), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		printed[parts[0]] = parts[1]
	}
	for _, r := range slices.Concat(s.byLayout, s.byField) {
		want := fmt.Sprintf("%d/%d", r.hits, r.total)
		got, ok := printed[r.name]
		if !ok {
			t.Errorf("the report prints no ratio for %s", r.name)
			continue
		}
		if got != want {
			t.Errorf("the report prints %s for %s, want %s -- the row is rendering numbers that are not its own", got, r.name, want)
		}
	}
}

// AC-6. The two pinned miss lists must partition the misses: overlapping or empty, they stop
// discriminating an unwinnable cell from an extraction defect.
func TestEndToEnd_TheKnownMissesArePinnedAndWinnable(t *testing.T) {
	if len(eeRealMisses) == 0 {
		t.Fatal("eeRealMisses is empty -- with nothing pinned, any real miss can be absorbed into eeAbsentCells")
	}
	if len(eeAbsentCells)+len(eeRealMisses) != eeCorpusCells-eeCorpusHits {
		t.Errorf("%d absent + %d real = %d pinned miss(es), but the score pins %d",
			len(eeAbsentCells), len(eeRealMisses), len(eeAbsentCells)+len(eeRealMisses), eeCorpusCells-eeCorpusHits)
	}
	for key := range eeRealMisses {
		if _, ok := eeAbsentCells[key]; ok {
			t.Errorf("%s is pinned as both absent and a real miss", key)
		}
		layout, field, ok := strings.Cut(key, "/")
		if !ok || !slices.Contains(writtenFields, field) {
			t.Errorf("%q is not a layout/field cell over the written fields", key)
			continue
		}
		// A real miss is one the bytes can win. An empty expectation belongs in eeAbsentCells.
		if len(eeExpectedValues(eeCell{layout: layout, field: field})) == 0 {
			t.Errorf("%s is pinned as a real miss but the table expects nothing there; it is an absent cell, not a defect", key)
		}
	}
}
