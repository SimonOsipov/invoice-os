// gen_test.go pins the contract of the fixturegen builders (gen.go),
// transcribed from task-194's Test Specs. Pure, DB-free package main
// tests: no network, filesystem, or database.
package main

import (
	"bytes"
	"encoding/csv"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// canonicalUploadCap mirrors internal/importer/handlers.go's maxUploadBytes
// (10 MiB); pinned as a literal since fixturegen doesn't import
// internal/importer.
const canonicalUploadCap = 10 << 20

// isoDateRE / tinRE are the ISO-date and buyer-TIN shape oracles.
var isoDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var tinRE = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}$`)

// vatTolerance widens the 0.005 spec tolerance (vat-standard-rate) by a
// float64 epsilon -- decimal-string round-trip noise can otherwise push a
// correct VAT a few femto-units over 0.005 (verified empirically at seed
// 42/30 invoices).
const vatTolerance = 0.005 + 1e-9

// canonicalHeaderCols is canonicalHeader split into field names, for
// column-named error messages.
var canonicalHeaderCols = strings.Split(canonicalHeader, ",")

// Column indices into a canonical 11-field row.
const (
	colInvoiceNo = iota
	colIssueDate
	colBuyerTIN
	colBuyer
	colCurrency
	colSubtotal
	colVAT
	colTotal
	colItem
	colQty
	colUnitPrice
)

// headerFieldCols are the columns that must repeat identically across every
// row of one invoice's group (everything except the three line-item
// columns and Invoice No itself, which is the group key).
var headerFieldCols = []int{colIssueDate, colBuyerTIN, colBuyer, colCurrency, colSubtotal, colVAT, colTotal}

// mustParseCSV parses data as CSV and returns the header row and data rows
// separately.
func mustParseCSV(t *testing.T, data []byte) (header []string, rows [][]string) {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(data))
	all, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parsing generated CSV: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("generated CSV has no rows at all, not even a header")
	}
	return all[0], all[1:]
}

// groupByInvoice buckets data rows by their Invoice No column (0), in
// first-seen order within each bucket.
func groupByInvoice(rows [][]string) map[string][][]string {
	groups := map[string][][]string{}
	for _, r := range rows {
		groups[r[colInvoiceNo]] = append(groups[r[colInvoiceNo]], r)
	}
	return groups
}

// invoiceOrder returns one entry per contiguous run of Invoice No; a
// non-contiguous repeat means the number appears twice here -- the signal
// TestGen_InvoiceNumbersUnique checks.
func invoiceOrder(rows [][]string) []string {
	var order []string
	for _, r := range rows {
		inv := r[colInvoiceNo]
		if len(order) == 0 || order[len(order)-1] != inv {
			order = append(order, inv)
		}
	}
	return order
}

// parseMoney parses a decimal money cell as float64, failing the test on a
// malformed cell rather than silently treating it as zero.
func parseMoney(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parsing money cell %q: %v", s, err)
	}
	return v
}

// round2 rounds to 2 decimal places using the same half-up convention the
// spec's round(...,2) implies.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// lineSum sums qty*unit_price across an invoice's rows, rounded to 2dp.
func lineSum(t *testing.T, grp [][]string) float64 {
	t.Helper()
	var sum float64
	for _, r := range grp {
		qty := parseMoney(t, r[colQty])
		unitPrice := parseMoney(t, r[colUnitPrice])
		sum += qty * unitPrice
	}
	return round2(sum)
}

// --- 1: TestGen_SameSeedByteIdentical --------------------------------------

func TestGen_SameSeedByteIdentical(t *testing.T) {
	a := generateGreen(42, 50)
	b := generateGreen(42, 50)
	if !bytes.Equal(a, b) {
		t.Errorf("generateGreen(42, 50) called twice produced different output; generation must be deterministic for a given seed")
	}
}

// --- 2: TestGen_DifferentSeedDiffers ---------------------------------------

func TestGen_DifferentSeedDiffers(t *testing.T) {
	a := generateGreen(1, 50)
	b := generateGreen(2, 50)
	if bytes.Equal(a, b) {
		t.Errorf("generateGreen(1, 50) and generateGreen(2, 50) produced byte-identical output; different seeds must produce different data")
	}
}

// --- 3: TestGen_InvoiceCountFollowsFlag ------------------------------------

func TestGen_InvoiceCountFollowsFlag(t *testing.T) {
	data := generateGreen(7, 10)
	_, rows := mustParseCSV(t, data)

	if len(rows) != 30 {
		t.Fatalf("got %d data rows, want 30 (10 invoices x 3 line rows)", len(rows))
	}
	groups := groupByInvoice(rows)
	if len(groups) != 10 {
		t.Errorf("got %d distinct Invoice No groups, want 10", len(groups))
	}
	for inv, grp := range groups {
		if len(grp) != 3 {
			t.Errorf("invoice %q has %d rows, want 3", inv, len(grp))
		}
	}
}

// --- 4: TestGen_HeaderIsCanonical -------------------------------------------

func TestGen_HeaderIsCanonical(t *testing.T) {
	data := generateGreen(42, 5)
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		t.Fatalf("generated output has no newline; cannot isolate the header line")
	}
	got := string(data[:nl])
	if got != canonicalHeader {
		t.Errorf("header line = %q, want %q", got, canonicalHeader)
	}
}

// --- 5: TestGen_HeaderFieldsRepeatWithinInvoice -----------------------------

func TestGen_HeaderFieldsRepeatWithinInvoice(t *testing.T) {
	data := generateGreen(42, 20)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)

	for inv, grp := range groups {
		if len(grp) == 0 {
			continue
		}
		first := grp[0]
		for _, r := range grp[1:] {
			for _, c := range headerFieldCols {
				if r[c] != first[c] {
					t.Errorf("invoice %q: column %q = %q on one row, %q on another; header fields must be identical across an invoice's rows", inv, canonicalHeaderCols[c], r[c], first[c])
				}
			}
		}
	}
}

// --- 6: TestGen_DatesAreISO --------------------------------------------------

func TestGen_DatesAreISO(t *testing.T) {
	data := generateGreen(42, 30)
	_, rows := mustParseCSV(t, data)

	for i, r := range rows {
		d := r[colIssueDate]
		if !isoDateRE.MatchString(d) {
			t.Errorf("row %d: Issue Date %q does not match %s", i, d, isoDateRE.String())
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			t.Errorf("row %d: Issue Date %q matched the regex but failed time.Parse: %v", i, d, err)
		}
	}
}

// --- 7: TestGen_GreenMoneyReconciles -----------------------------------------

func TestGen_GreenMoneyReconciles(t *testing.T) {
	data := generateGreen(42, 30)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)

	for inv, grp := range groups {
		subtotal := round2(parseMoney(t, grp[0][colSubtotal]))
		vat := parseMoney(t, grp[0][colVAT])
		sum := lineSum(t, grp)

		if subtotal != sum {
			t.Errorf("invoice %q: subtotal = %.2f, want round(sum(qty*unit_price),2) = %.2f", inv, subtotal, sum)
		}
		expectedVAT := 0.075 * subtotal
		if diff := math.Abs(vat - expectedVAT); diff > vatTolerance {
			t.Errorf("invoice %q: vat = %.2f, want within 0.005 of 0.075*subtotal = %.4f (diff %.4f)", inv, vat, expectedVAT, diff)
		}
	}
}

// --- 8: TestGen_BuyerTinFormat ------------------------------------------------

func TestGen_BuyerTinFormat(t *testing.T) {
	data := generateGreen(42, 30)
	_, rows := mustParseCSV(t, data)

	for i, r := range rows {
		tin := r[colBuyerTIN]
		if !tinRE.MatchString(tin) {
			t.Errorf("row %d: Buyer TIN %q does not match %s", i, tin, tinRE.String())
		}
	}
}

// --- 9: TestGen_CurrencyNGN ----------------------------------------------------

func TestGen_CurrencyNGN(t *testing.T) {
	data := generateGreen(42, 30)
	_, rows := mustParseCSV(t, data)

	for i, r := range rows {
		if r[colCurrency] != "NGN" {
			t.Errorf("row %d: Currency = %q, want %q", i, r[colCurrency], "NGN")
		}
	}
}

// --- 10: TestGen_InvoiceNumbersUnique -------------------------------------------

func TestGen_InvoiceNumbersUnique(t *testing.T) {
	data := generateGreen(42, 30)
	_, rows := mustParseCSV(t, data)

	order := invoiceOrder(rows)
	if len(order) != 30 {
		t.Fatalf("got %d invoice groups by row scan, want 30", len(order))
	}
	seen := map[string]bool{}
	for _, inv := range order {
		if seen[inv] {
			t.Errorf("Invoice No %q appears in more than one non-contiguous group; invoice numbers must be file-unique", inv)
		}
		seen[inv] = true
	}
}

// --- 11: TestGen_EdgeMissingColumns_LacksTotalColumn -----------------------------

func TestGen_EdgeMissingColumns_LacksTotalColumn(t *testing.T) {
	data := buildEdgeMissingColumns(42, 10)
	header, rows := mustParseCSV(t, data)

	if len(header) != 10 {
		t.Fatalf("header has %d fields, want 10 (canonical 11 minus Total); header = %v", len(header), header)
	}
	for _, h := range header {
		if h == "Total" {
			t.Errorf("header still contains a Total column: %v", header)
		}
	}
	for i, r := range rows {
		if len(r) != 10 {
			t.Errorf("row %d has %d fields, want 10: %v", i, len(r), r)
		}
	}
}

// --- 12: TestGen_EdgeInFileDupes_DifferOnlyOnIssueDate ---------------------------

func TestGen_EdgeInFileDupes_DifferOnlyOnIssueDate(t *testing.T) {
	data := buildEdgeInFileDupes(42)
	_, rows := mustParseCSV(t, data)

	// tuple captures every header-repeating field (Issue Date, Buyer TIN,
	// Buyer, Currency, Subtotal, VAT, Total) for one row, so two rows with
	// the same Invoice No but a different tuple disagree on at least one of
	// them.
	type tuple [7]string
	seenTuples := map[string]map[tuple]bool{}
	for _, r := range rows {
		var tp tuple
		for i, c := range headerFieldCols {
			tp[i] = r[c]
		}
		inv := r[colInvoiceNo]
		if seenTuples[inv] == nil {
			seenTuples[inv] = map[tuple]bool{}
		}
		seenTuples[inv][tp] = true
	}

	var dupeInvoice string
	var tuples []tuple
	for inv, set := range seenTuples {
		if len(set) > 1 {
			dupeInvoice = inv
			for tp := range set {
				tuples = append(tuples, tp)
			}
			break
		}
	}
	if dupeInvoice == "" {
		t.Fatal("no Invoice No has rows disagreeing on any header field; expected at least one in-file duplicate pair")
	}
	if len(tuples) != 2 {
		t.Fatalf("invoice %q has %d distinct header-field tuples, want exactly 2 (one duplicate pair); tuples = %v", dupeInvoice, len(tuples), tuples)
	}

	a, b := tuples[0], tuples[1]
	// headerFieldCols[0] is Issue Date -- the two tuples must differ there...
	if a[0] == b[0] {
		t.Errorf("invoice %q: the two duplicate rows do not differ on Issue Date at all; a=%v b=%v", dupeInvoice, a, b)
	}
	// ...and must NOT differ anywhere else.
	for i := 1; i < len(headerFieldCols); i++ {
		if a[i] != b[i] {
			t.Errorf("invoice %q: duplicate rows also differ on %q, want them to differ ONLY on Issue Date; a=%v b=%v", dupeInvoice, canonicalHeaderCols[headerFieldCols[i]], a, b)
		}
	}
}

// --- 13: TestGen_EdgeBadEncoding_IsUTF16LEWithoutBOM -----------------------------

func TestGen_EdgeBadEncoding_IsUTF16LEWithoutBOM(t *testing.T) {
	data := buildEdgeBadEncoding(42, 5)

	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		t.Fatalf("output begins with a UTF-16LE byte-order-mark (0xFF 0xFE); must be emitted WITHOUT a BOM")
	}
	if utf8.Valid(data) {
		t.Errorf("output is valid UTF-8; a UTF-16LE-encoded CSV of ASCII header/field text should NOT also be valid UTF-8 (every other byte is 0x00)")
	}

	decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
	decoded, _, err := transform.Bytes(decoder, data)
	if err != nil {
		t.Fatalf("output does not decode cleanly as UTF-16LE: %v", err)
	}
	if !utf8.Valid(decoded) {
		t.Errorf("decoding the output as UTF-16LE did not yield valid UTF-8 text")
	}
	if !strings.Contains(string(decoded), "Invoice No") {
		t.Errorf("UTF-16LE-decoded output does not contain the expected header text %q; decoded = %q", "Invoice No", string(decoded))
	}
}

// --- 14: TestGen_EdgeBadTin_ShapeFailsRegex ---------------------------------------

func TestGen_EdgeBadTin_ShapeFailsRegex(t *testing.T) {
	data := buildEdgeBadTin(42, 10)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)

	var bad []string
	for inv, grp := range groups {
		if !tinRE.MatchString(grp[0][colBuyerTIN]) {
			bad = append(bad, inv)
		}
	}
	if len(bad) != 1 {
		t.Fatalf("got %d invoices with a Buyer TIN failing %s, want exactly 1; offenders = %v", len(bad), tinRE.String(), bad)
	}
}

// --- 15: TestGen_EdgeVatMathWrong_VATZeroSubtotalPositiveLinesReconcile -----------

func TestGen_EdgeVatMathWrong_VATZeroSubtotalPositiveLinesReconcile(t *testing.T) {
	data := buildEdgeVatMathWrong(42, 10)
	_, rows := mustParseCSV(t, data)
	groups := groupByInvoice(rows)

	var zeroVAT []string
	for inv, grp := range groups {
		subtotal := round2(parseMoney(t, grp[0][colSubtotal]))
		vat := parseMoney(t, grp[0][colVAT])
		sum := lineSum(t, grp)

		if vat == 0 {
			zeroVAT = append(zeroVAT, inv)
			if subtotal < 1.00 {
				t.Errorf("invoice %q: VAT==0.00 but subtotal=%.2f, want >= 1.00 (past vat-standard-rate's tolerance/rate ~ 0.0667 floor)", inv, subtotal)
			}
			if subtotal != sum {
				t.Errorf("invoice %q: VAT==0.00 but its lines do not reconcile: subtotal=%.2f, sum(qty*unit_price)=%.2f", inv, subtotal, sum)
			}
			continue
		}

		expectedVAT := 0.075 * subtotal
		if diff := math.Abs(vat - expectedVAT); diff > vatTolerance {
			t.Errorf("invoice %q (not the mutated one): vat=%.2f, want within 0.005 of 0.075*subtotal=%.4f (diff %.4f)", inv, vat, expectedVAT, diff)
		}
	}

	if len(zeroVAT) != 1 {
		t.Fatalf("got %d invoices with VAT==0.00, want exactly 1; offenders = %v", len(zeroVAT), zeroVAT)
	}
}

// --- 16: TestGen_OversizedInflator_ExceedsMaxUploadBytes --------------------------

func TestGen_OversizedInflator_ExceedsMaxUploadBytes(t *testing.T) {
	data := buildOversized()
	if len(data) <= canonicalUploadCap {
		t.Errorf("buildOversized() returned %d bytes, want > %d (internal/importer/handlers.go's maxUploadBytes, 10 MiB)", len(data), canonicalUploadCap)
	}
}
