// gen_adversarial_test.go adds adversarial coverage beyond gen_test.go's
// AC-transcribed tests: the "otherwise green" property each edge builder
// must have (every column outside its one intended defect stays clean),
// determinism/seed-sensitivity across the whole manifest, and an
// independent literal for the canonical header -- gen_test.go's own check
// compares generateGreen's output against gen.go's OWN canonicalHeader
// constant, so it can't catch a mutation to that constant's value.
package main

import (
	"bytes"
	"math"
	"testing"
)

// distinctLines dedupes grp's rows by (Item, Qty, Unit Price) so an
// in-file duplicate row (buildEdgeInFileDupes) doesn't get double-counted
// when reconciling subtotal.
func distinctLines(grp [][]string) [][]string {
	seen := map[[3]string]bool{}
	var out [][]string
	for _, r := range grp {
		key := [3]string{r[colItem], r[colQty], r[colUnitPrice]}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// assertGroupWellFormed checks the green invariants (row count, header-field
// repeat except skipRepeat, currency, ISO dates, TIN shape) for one invoice
// group. Money reconciliation is checked separately by callers since
// tolerance/skip rules differ per edge case.
func assertGroupWellFormed(t *testing.T, inv string, grp [][]string, wantRows int, skipRepeat map[int]bool) {
	t.Helper()
	if len(grp) != wantRows {
		t.Errorf("invoice %q has %d rows, want %d", inv, len(grp), wantRows)
	}
	first := grp[0]
	for _, r := range grp[1:] {
		for _, c := range headerFieldCols {
			if skipRepeat[c] {
				continue
			}
			if r[c] != first[c] {
				t.Errorf("invoice %q: column %q = %q on one row, %q on another; header fields must repeat", inv, canonicalHeaderCols[c], r[c], first[c])
			}
		}
	}
	for _, r := range grp {
		if r[colCurrency] != "NGN" {
			t.Errorf("invoice %q: currency = %q, want NGN", inv, r[colCurrency])
		}
		if !isoDateRE.MatchString(r[colIssueDate]) {
			t.Errorf("invoice %q: issue date %q does not match %s", inv, r[colIssueDate], isoDateRE.String())
		}
	}
	if !tinRE.MatchString(first[colBuyerTIN]) {
		t.Errorf("invoice %q: Buyer TIN %q does not match %s", inv, first[colBuyerTIN], tinRE.String())
	}
}

// --- edge_bad_tin is otherwise-green ---------------------------------------

func TestAdversarial_EdgeBadTin_OtherwiseGreen(t *testing.T) {
	data := buildEdgeBadTin(42, 10)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)
	if len(groups) != 10 {
		t.Fatalf("got %d invoice groups, want 10", len(groups))
	}

	var badTinCount int
	for inv, grp := range groups {
		if len(grp) != 3 {
			t.Errorf("invoice %q has %d rows, want 3", inv, len(grp))
			continue
		}
		first := grp[0]
		for _, r := range grp[1:] {
			for _, c := range headerFieldCols {
				if r[c] != first[c] {
					t.Errorf("invoice %q: column %q not repeating: %q vs %q", inv, canonicalHeaderCols[c], r[c], first[c])
				}
			}
		}
		for _, r := range grp {
			if r[colCurrency] != "NGN" {
				t.Errorf("invoice %q: currency = %q, want NGN", inv, r[colCurrency])
			}
			if !isoDateRE.MatchString(r[colIssueDate]) {
				t.Errorf("invoice %q: issue date %q not ISO", inv, r[colIssueDate])
			}
		}

		subtotal := round2(parseMoney(t, first[colSubtotal]))
		vat := parseMoney(t, first[colVAT])
		sum := lineSum(t, grp)
		if subtotal != sum {
			t.Errorf("invoice %q: subtotal = %.2f, want line sum = %.2f; edge_bad_tin must not also break money reconciliation", inv, subtotal, sum)
		}
		if diff := math.Abs(vat - 0.075*subtotal); diff > vatTolerance {
			t.Errorf("invoice %q: vat = %.2f, want within tolerance of 0.075*subtotal = %.4f", inv, vat, 0.075*subtotal)
		}

		if !tinRE.MatchString(first[colBuyerTIN]) {
			badTinCount++
		}
	}
	if badTinCount != 1 {
		t.Fatalf("got %d invoices with a Buyer TIN failing %s, want exactly 1", badTinCount, tinRE.String())
	}

	order := invoiceOrder(rows)
	if len(order) != 10 {
		t.Fatalf("got %d invoice groups by row scan, want 10", len(order))
	}
	seen := map[string]bool{}
	for _, inv := range order {
		if seen[inv] {
			t.Errorf("Invoice No %q appears in more than one non-contiguous group", inv)
		}
		seen[inv] = true
	}
}

// --- edge_vat_math_wrong is otherwise-green --------------------------------

func TestAdversarial_EdgeVatMathWrong_OtherwiseGreen(t *testing.T) {
	data := buildEdgeVatMathWrong(42, 10)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)
	if len(groups) != 10 {
		t.Fatalf("got %d invoice groups, want 10", len(groups))
	}

	var zeroVATCount int
	for inv, grp := range groups {
		if len(grp) != 3 {
			t.Errorf("invoice %q has %d rows, want 3", inv, len(grp))
			continue
		}
		first := grp[0]
		for _, r := range grp[1:] {
			for _, c := range headerFieldCols {
				if r[c] != first[c] {
					t.Errorf("invoice %q: column %q not repeating: %q vs %q", inv, canonicalHeaderCols[c], r[c], first[c])
				}
			}
		}
		for _, r := range grp {
			if r[colCurrency] != "NGN" {
				t.Errorf("invoice %q: currency = %q, want NGN", inv, r[colCurrency])
			}
			if !isoDateRE.MatchString(r[colIssueDate]) {
				t.Errorf("invoice %q: issue date %q not ISO", inv, r[colIssueDate])
			}
		}
		if !tinRE.MatchString(first[colBuyerTIN]) {
			t.Errorf("invoice %q: Buyer TIN %q fails %s; edge_vat_math_wrong must not also break TIN shape", inv, first[colBuyerTIN], tinRE.String())
		}

		subtotal := round2(parseMoney(t, first[colSubtotal]))
		vat := parseMoney(t, first[colVAT])
		sum := lineSum(t, grp)
		if subtotal != sum {
			t.Errorf("invoice %q: subtotal = %.2f, want line sum = %.2f", inv, subtotal, sum)
		}

		if vat == 0 {
			zeroVATCount++
			if subtotal < 1.00 {
				t.Errorf("invoice %q: VAT==0.00 but subtotal=%.2f, want >= 1.00", inv, subtotal)
			}
			continue
		}
		if diff := math.Abs(vat - 0.075*subtotal); diff > vatTolerance {
			t.Errorf("invoice %q (not the mutated one): vat=%.2f, want within tolerance of 0.075*subtotal=%.4f", inv, vat, 0.075*subtotal)
		}
	}
	if zeroVATCount != 1 {
		t.Fatalf("got %d invoices with VAT==0.00, want exactly 1", zeroVATCount)
	}

	order := invoiceOrder(rows)
	seen := map[string]bool{}
	for _, inv := range order {
		if seen[inv] {
			t.Errorf("Invoice No %q appears in more than one non-contiguous group", inv)
		}
		seen[inv] = true
	}
}

// --- edge_in_file_dupes minimality / otherwise-well-formed -----------------

func TestAdversarial_EdgeInFileDupes_OtherwiseWellFormed(t *testing.T) {
	data := buildEdgeInFileDupes(42)
	header, rows := mustParseCSV(t, data)

	if len(header) != len(canonicalHeaderCols) {
		t.Fatalf("header has %d fields, want %d (canonical)", len(header), len(canonicalHeaderCols))
	}
	for i, h := range header {
		if h != canonicalHeaderCols[i] {
			t.Errorf("header field %d = %q, want %q", i, h, canonicalHeaderCols[i])
		}
	}
	if len(rows) != 16 {
		t.Fatalf("got %d data rows, want 16 (5 invoices x 3 + 1 in-file duplicate row)", len(rows))
	}

	groups := groupByInvoice(rows)
	if len(groups) != 5 {
		t.Fatalf("got %d distinct Invoice No groups, want 5", len(groups))
	}

	var fourRowInvoices int
	skipIssueDate := map[int]bool{colIssueDate: true}
	for inv, grp := range groups {
		switch len(grp) {
		case 3:
			assertGroupWellFormed(t, inv, grp, 3, nil)
		case 4:
			fourRowInvoices++
			// The dupe invoice legitimately has two distinct Issue Date
			// values (that is the whole point of this edge file) -- every
			// OTHER header field must still repeat identically.
			assertGroupWellFormed(t, inv, grp, 4, skipIssueDate)
		default:
			t.Fatalf("invoice %q has %d rows, want 3 (normal) or 4 (the one with the in-file duplicate)", inv, len(grp))
			continue
		}

		distinct := distinctLines(grp)
		if len(distinct) != 3 {
			t.Fatalf("invoice %q: %d distinct (item,qty,unit price) lines after deduping, want 3", inv, len(distinct))
		}
		subtotal := round2(parseMoney(t, grp[0][colSubtotal]))
		vat := parseMoney(t, grp[0][colVAT])
		sum := lineSum(t, distinct)
		if subtotal != sum {
			t.Errorf("invoice %q: subtotal = %.2f, want round(sum(qty*unit_price),2) over its 3 distinct lines = %.2f", inv, subtotal, sum)
		}
		if diff := math.Abs(vat - 0.075*subtotal); diff > vatTolerance {
			t.Errorf("invoice %q: vat = %.2f, want within tolerance of 0.075*subtotal = %.4f", inv, vat, 0.075*subtotal)
		}
	}
	if fourRowInvoices != 1 {
		t.Fatalf("got %d invoices with 4 rows (an in-file duplicate), want exactly 1", fourRowInvoices)
	}
}

// --- green_second demo variety ----------------------------------------------

func TestAdversarial_GreenSecond_DiffersFromGreen500AndIsGreen(t *testing.T) {
	const baseSeed = 42
	const baseInvoices = 500

	green500 := generateGreen(baseSeed, baseInvoices)
	greenSecond := generateGreen(baseSeed+1, baseInvoices/2)

	if bytes.Equal(green500, greenSecond) {
		t.Fatal("green_second is byte-identical to green_500; demo variety requires distinct content")
	}

	_, rows := mustParseCSV(t, greenSecond)
	wantRows := (baseInvoices / 2) * 3
	if len(rows) != wantRows {
		t.Fatalf("green_second has %d rows, want %d (%d invoices x 3)", len(rows), wantRows, baseInvoices/2)
	}

	// A different seed offset must actually change the generated content
	// (buyer names, amounts, dates), not merely produce a shorter file with
	// otherwise-identical rows -- which would indicate the +1 seed offset is
	// silently not taking effect.
	_, rows500 := mustParseCSV(t, green500)
	n := len(rows)
	if len(rows500) < n {
		n = len(rows500)
	}
	identicalPrefix := true
outer:
	for i := 0; i < n; i++ {
		for c := range rows[i] {
			if rows[i][c] != rows500[i][c] {
				identicalPrefix = false
				break outer
			}
		}
	}
	if identicalPrefix {
		t.Error("green_second's rows are byte-identical to green_500's corresponding prefix rows; seed+1 must change generated content, not just row count")
	}

	groups := groupByInvoice(rows)
	if len(groups) != baseInvoices/2 {
		t.Fatalf("got %d distinct Invoice No groups, want %d", len(groups), baseInvoices/2)
	}
	for inv, grp := range groups {
		assertGroupWellFormed(t, inv, grp, 3, nil)
		subtotal := round2(parseMoney(t, grp[0][colSubtotal]))
		vat := parseMoney(t, grp[0][colVAT])
		sum := lineSum(t, grp)
		if subtotal != sum {
			t.Errorf("invoice %q: subtotal = %.2f, want line sum = %.2f", inv, subtotal, sum)
		}
		if diff := math.Abs(vat - 0.075*subtotal); diff > vatTolerance {
			t.Errorf("invoice %q: vat = %.2f, want within tolerance of 0.075*subtotal = %.4f", inv, vat, 0.075*subtotal)
		}
	}
}

// --- determinism / seed-sensitivity across the WHOLE manifest --------------

func TestAdversarial_ManifestDeterministicAndSeedSensitive(t *testing.T) {
	// Kept small so this test stays cheap; generateGreen's own 500-invoice
	// scale is exercised by TestAdversarial_GreenSecond_... and the CLI.
	const baseInvoices = 20

	m1a := manifest(1000, baseInvoices)
	m1b := manifest(1000, baseInvoices)
	if len(m1a) != len(m1b) {
		t.Fatalf("manifest(1000, %d) returned %d entries one call, %d the next", baseInvoices, len(m1a), len(m1b))
	}
	for i := range m1a {
		if m1a[i].name != m1b[i].name {
			t.Fatalf("entry %d: name %q vs %q; manifest ordering/content must be deterministic", i, m1a[i].name, m1b[i].name)
		}
		a := m1a[i].build()
		b := m1b[i].build()
		if !bytes.Equal(a, b) {
			t.Errorf("manifest entry %q: two build() calls at the same (baseSeed, baseInvoices) produced different bytes", m1a[i].name)
		}
	}

	// A different baseSeed must change EVERY manifest entry's output, since
	// every file's seed is defined as an offset from baseSeed -- not just
	// green_500's.
	m2 := manifest(2000, baseInvoices)
	if len(m2) != len(m1a) {
		t.Fatalf("manifest(2000, %d) returned %d entries, want %d", baseInvoices, len(m2), len(m1a))
	}
	for i := range m1a {
		if m1a[i].name != m2[i].name {
			t.Fatalf("entry %d: name %q vs %q; manifest file list must not depend on baseSeed", i, m1a[i].name, m2[i].name)
		}
		a := m1a[i].build()
		b := m2[i].build()
		if bytes.Equal(a, b) {
			t.Errorf("manifest entry %q: baseSeed 1000 and baseSeed 2000 produced byte-identical output", m1a[i].name)
		}
	}
}

// --- buildOversized sanity ---------------------------------------------------

func TestAdversarial_BuildOversized_StaysCheap(t *testing.T) {
	data := buildOversized()
	if len(data) <= canonicalUploadCap {
		t.Fatalf("buildOversized() returned %d bytes, want > %d (maxUploadBytes)", len(data), canonicalUploadCap)
	}
	const sanityCeiling = 64 << 20
	if len(data) >= sanityCeiling {
		t.Errorf("buildOversized() returned %d bytes, want < %d (%d MiB) so the in-memory test stays cheap", len(data), sanityCeiling, sanityCeiling>>20)
	}
}

// --- header value pinned against an INDEPENDENT oracle ----------------------

// canonicalHeaderIndependentLiteral is a hand-transcribed copy (from
// internal/importer/service_test.go's stdHeader / e2e's PERF_HEADER), not
// a reference to gen.go's own canonicalHeader constant.
const canonicalHeaderIndependentLiteral = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price"

func TestAdversarial_HeaderMatchesIndependentLiteral(t *testing.T) {
	data := generateGreen(42, 5)
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		t.Fatalf("generated output has no newline; cannot isolate the header line")
	}
	got := string(data[:nl])
	if got != canonicalHeaderIndependentLiteral {
		t.Errorf("header line = %q, want %q (independently transcribed from internal/importer's stdHeader / e2e's PERF_HEADER, not gen.go's own canonicalHeader constant)", got, canonicalHeaderIndependentLiteral)
	}
}
