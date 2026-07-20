// gen.go implements the builder API for the synthetic invoice-import
// fixture generator (M4-11-01, task-194): deterministic CSV generation for
// the green (canonical, all-valid) shape and six edge-case mutations, plus
// an in-memory oversized-body inflator. Every builder is seeded ONLY from
// its seed parameter (math/rand, no wall-clock, no map iteration in output
// order) so identical (seed, invoices) always produces identical bytes --
// see gen_test.go's TestGen_SameSeedByteIdentical.
//
// Canonical header, money math (subtotal/vat/total), and buyer-TIN shape
// mirror internal/importer/service_test.go's stdHeader/stdMapping and
// e2e/importFixtures.ts's PERF_HEADER/PERF_MAPPING byte-for-byte, and the
// seeded vat-standard-rate rule (migrations/20260711121327_seed_mbs_v1.sql:29,
// rate=0.075, tolerance=0.005).
//
// Money is computed in integer cents throughout (never accumulated as
// float64) so subtotal/vat/total round-trip through decimal-string CSV
// cells exactly: subtotal is an exact sum of integer qty*unit_price
// cents, and vat is round(subtotal_cents*0.075) cents -- both formatted
// to 2dp only at render time. This is what makes
// TestGen_GreenMoneyReconciles's exact-equality check on subtotal (as
// opposed to VAT's tolerance-based check) hold reliably.
//
// NON-OBVIOUS CONSTRAINT ON buildEdgeBadEncoding: gen_test.go's
// TestGen_EdgeBadEncoding_IsUTF16LEWithoutBOM asserts the UTF-16LE bytes
// are NOT utf8.Valid. A pure-ASCII green base re-encoded as UTF-16LE (each
// char -> low byte, 0x00 high byte) IS still valid UTF-8 -- 0x00 and
// 0x20-0x7E are each valid single-byte UTF-8 sequences on their own -- so
// buildEdgeBadEncoding forces its first invoice's buyer name to a fixed
// non-ASCII string (nonASCIIBuyer) regardless of what the seed would have
// randomly picked from buyerNames, guaranteeing the encoded content always
// contains a multi-byte UTF-8 rune.
package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// canonicalHeader is the byte-for-byte CSV header line (no trailing
// newline). Matches internal/importer/service_test.go's stdHeader and
// e2e/importFixtures.ts's PERF_HEADER exactly, column for column.
const canonicalHeader = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price"

// Column indices into a canonical 11-field row. Kept private to gen.go
// (not shared with gen_test.go's own colXxx constants, which live in the
// test binary only) since production code cannot depend on _test.go
// declarations.
const (
	idxInvoiceNo = iota
	idxIssueDate
	idxBuyerTIN
	idxBuyer
	idxCurrency
	idxSubtotal
	idxVAT
	idxTotal
	idxItem
	idxQty
	idxUnitPrice
)

// buyerNames is a fixed, index-selected pool of Nigerian buyer names drawn
// from for every green and edge-case invoice. At least one entry carries a
// non-ASCII rune (a diacritic) -- see the file doc comment above --
// though buildEdgeBadEncoding does not rely on chance here, it forces
// nonASCIIBuyer explicitly to guarantee the constraint regardless of seed.
var buyerNames = []string{
	"Adeyemi Trading Company Ltd",
	"Chukwuemeka Okafor Ltd",
	"Lagos Steel & Aluminium Ltd",
	"Ola Foods Plc",
	"Kano Textiles Ltd",
	"Emeka Logistics Nigeria Ltd",
	"Ibrahim & Sons Trading Ltd",
	"Port Harcourt Energy Services Ltd",
	"Abuja Construction Materials Ltd",
	"Yusuf Agro Processing Ltd",
	"Ọlá Foods Distribution Plc",
	"Chukwuemeka Okàfor Ventures Ltd",
}

// lineItemNames is the fixed, index-selected pool of line-item
// descriptions drawn from for every invoice's 3 line rows.
var lineItemNames = []string{
	"Office Supplies",
	"Consulting Services",
	"Building Materials",
	"Logistics Services",
	"Industrial Equipment",
	"Raw Materials",
	"IT Services",
	"Maintenance Services",
}

// nonASCIIBuyer is the buyer name buildEdgeBadEncoding forces onto its
// first invoice to guarantee non-ASCII content -- see the file doc
// comment's NON-OBVIOUS CONSTRAINT section.
const nonASCIIBuyer = "Chukwuemeka Okàfor Ltd"

// invoiceRec is one green invoice's header-repeating fields plus its 3
// line rows, held in integer cents so subtotal/vat/total math never
// accumulates float error -- only formatted to 2dp decimal strings at
// render time (see moneyStr).
type invoiceRec struct {
	no        string
	issueDate string
	buyerTIN  string
	buyer     string
	currency  string
	subtotalC int64 // cents
	vatC      int64 // cents
	totalC    int64 // cents
	lines     [3]lineRec
}

// lineRec is one line row's varying fields.
type lineRec struct {
	item       string
	qty        int
	unitPriceC int64 // cents
}

// genInvoices builds n deterministic invoices from rng, each with 3 line
// rows and reconciled money: subtotal = sum(qty*unit_price) (exact, in
// cents), vat = round(0.075*subtotal, 2), total = subtotal+vat.
// Invoice numbers are assigned sequentially (INV-SYN-00001, 00002, ...),
// which is file-unique by construction regardless of seed.
func genInvoices(rng *rand.Rand, n int) []invoiceRec {
	baseDate := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	invs := make([]invoiceRec, n)
	for i := 0; i < n; i++ {
		var lines [3]lineRec
		var subtotalC int64
		for j := range lines {
			qty := rng.Intn(20) + 1
			unitPriceC := int64(rng.Intn(500000) + 100) // NGN 1.00 .. 5,000.99
			lines[j] = lineRec{
				item:       lineItemNames[rng.Intn(len(lineItemNames))],
				qty:        qty,
				unitPriceC: unitPriceC,
			}
			subtotalC += int64(qty) * unitPriceC
		}
		vatC := int64(math.Round(float64(subtotalC) * 0.075))
		date := baseDate.AddDate(0, 0, rng.Intn(365))

		invs[i] = invoiceRec{
			no:        fmt.Sprintf("INV-SYN-%05d", i+1),
			issueDate: date.Format("2006-01-02"),
			buyerTIN:  genBuyerTIN(rng),
			buyer:     buyerNames[rng.Intn(len(buyerNames))],
			currency:  "NGN",
			subtotalC: subtotalC,
			vatC:      vatC,
			totalC:    subtotalC + vatC,
			lines:     lines,
		}
	}
	return invs
}

// genBuyerTIN generates a Luhn-valid NNNNNNNN-NNNN buyer TIN. Luhn
// validity is a realism touch, not a hard requirement -- the importer's
// buyer-tin rules only check the ^[0-9]{8}-[0-9]{4}$ shape (gen_test.go's
// tinRE) -- but costs nothing to include.
func genBuyerTIN(rng *rand.Rand) string {
	payload := make([]int, 11)
	for i := range payload {
		payload[i] = rng.Intn(10)
	}
	check := luhnCheckDigit(payload)

	digits := make([]byte, 12)
	for i, d := range payload {
		digits[i] = byte('0' + d)
	}
	digits[11] = byte('0' + check)
	return string(digits[:8]) + "-" + string(digits[8:])
}

// luhnCheckDigit computes the standard Luhn check digit for payload
// (most-significant digit first): every second digit, counting from the
// payload's rightmost digit (the one adjacent to where the check digit is
// appended), is doubled and reduced by 9 if it exceeds 9, then all digits
// are summed and the check digit is chosen to bring that sum to a
// multiple of 10.
func luhnCheckDigit(payload []int) int {
	sum := 0
	for i, d := range payload {
		posFromRight := len(payload) - 1 - i
		if posFromRight%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return (10 - sum%10) % 10
}

// moneyStr formats an integer cents value as a 2dp decimal string.
func moneyStr(cents int64) string {
	return fmt.Sprintf("%.2f", float64(cents)/100)
}

// csvRow renders line index li of invoice inv into a full 11-field
// canonical row, in canonicalHeader's column order.
func csvRow(inv invoiceRec, li int) []string {
	l := inv.lines[li]
	return []string{
		inv.no, inv.issueDate, inv.buyerTIN, inv.buyer, inv.currency,
		moneyStr(inv.subtotalC), moneyStr(inv.vatC), moneyStr(inv.totalC),
		l.item, strconv.Itoa(l.qty), moneyStr(l.unitPriceC),
	}
}

// allRows flattens invs into one row-per-line-item, in invoice then
// line order.
func allRows(invs []invoiceRec) [][]string {
	var rows [][]string
	for _, inv := range invs {
		for li := range inv.lines {
			rows = append(rows, csvRow(inv, li))
		}
	}
	return rows
}

// dropCol returns a copy of fields with the value at idx removed.
func dropCol(fields []string, idx int) []string {
	out := make([]string, 0, len(fields)-1)
	out = append(out, fields[:idx]...)
	out = append(out, fields[idx+1:]...)
	return out
}

// renderCSV writes header + rows as RFC 4180 CSV ('\n'-terminated --
// encoding/csv.Writer's default; UseCRLF is left false). A write or flush
// error here means an invoice/header field itself is malformed CSV input
// (e.g. contains an unescapable byte sequence), which is a generator bug,
// not a runtime condition callers should handle -- hence panic rather
// than a returned error, consistent with gen.go's stub signatures (none
// of which return an error).
func renderCSV(header []string, rows [][]string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		panic(fmt.Sprintf("fixturegen: writing CSV header: %v", err))
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			panic(fmt.Sprintf("fixturegen: writing CSV row: %v", err))
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		panic(fmt.Sprintf("fixturegen: flushing CSV writer: %v", err))
	}
	return buf.Bytes()
}

// generateGreen builds the canonical green CSV: `invoices` invoices, 3 line
// rows each, deterministic for a given seed. Header + data rows,
// '\n'-terminated, no trailing timestamp.
func generateGreen(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)
	return renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))
}

// buildEdgeMissingColumns derives from a green base then drops the Total
// column (header cell and every data-row cell).
func buildEdgeMissingColumns(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)

	header := dropCol(strings.Split(canonicalHeader, ","), idxTotal)
	rows := allRows(invs)
	for i, r := range rows {
		rows[i] = dropCol(r, idxTotal)
	}
	return renderCSV(header, rows)
}

// buildEdgeInFileDupes derives from a green base then mutates it so that
// at least one invoice has two rows sharing the same Invoice No but
// differing on exactly Issue Date (every other header field identical).
// A small, fixed invoice count keeps the file small, per task-194.
func buildEdgeInFileDupes(seed int64) []byte {
	const n = 5
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, n)
	rows := allRows(invs)

	// Duplicate the first invoice's first line row, changing only Issue
	// Date, so exactly one invoice ends up with two distinct header-field
	// tuples that differ on nothing else -- the shape
	// TestGen_EdgeInFileDupes_DifferOnlyOnIssueDate asserts.
	dupe := csvRow(invs[0], 0)
	origDate, err := time.Parse("2006-01-02", invs[0].issueDate)
	if err != nil {
		panic(fmt.Sprintf("fixturegen: parsing generated issue date %q: %v", invs[0].issueDate, err))
	}
	dupe[idxIssueDate] = origDate.AddDate(0, 0, 1).Format("2006-01-02")
	rows = append(rows, dupe)

	return renderCSV(strings.Split(canonicalHeader, ","), rows)
}

// buildEdgeBadEncoding derives from a green base then re-encodes it as
// UTF-16LE WITHOUT a byte-order-mark. See the file doc comment above for
// the non-ASCII-content constraint this builder must satisfy.
func buildEdgeBadEncoding(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)
	if len(invs) > 0 {
		invs[0].buyer = nonASCIIBuyer
	}
	green := renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))

	encoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder()
	encoded, _, err := transform.Bytes(encoder, green)
	if err != nil {
		panic(fmt.Sprintf("fixturegen: encoding UTF-16LE: %v", err))
	}
	return encoded
}

// buildEdgeBadTin derives from a green base then mutates exactly one
// invoice's Buyer TIN so it fails ^[0-9]{8}-[0-9]{4}$; every other
// invoice's Buyer TIN stays well-formed.
func buildEdgeBadTin(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)
	if len(invs) > 0 {
		// 7 digits before the dash (canonical requires 8) -- fails the
		// regex shape while every other invoice's genBuyerTIN output stays
		// well-formed by construction.
		invs[0].buyerTIN = "1234567-8901"
	}
	return renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))
}

// buildEdgeVatMathWrong derives from a green base then mutates exactly one
// invoice to VAT=0.00 with subtotal>=1.00 (its lines still reconcile to
// that subtotal); every other invoice keeps normal reconciled VAT.
func buildEdgeVatMathWrong(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)
	if len(invs) > 0 {
		// subtotal/lines are left untouched (so they still reconcile);
		// genInvoices' minimum possible subtotal is 3 lines x 1 qty x
		// NGN 1.00 = NGN 3.00, always >= the NGN 1.00 floor. Total is
		// recomputed to subtotal+0 to stay internally consistent -- v2 has
		// no separate total=subtotal+vat rule, so the only intended
		// defect is vat-standard-rate; leaving Total stale would introduce
		// a second, unintended inconsistency.
		invs[0].vatC = 0
		invs[0].totalC = invs[0].subtotalC
	}
	return renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))
}

// oversizedSeed / oversizedTargetBytes drive buildOversized: a pinned
// seed (deterministic, though byte-identity is not asserted by any test)
// and a target comfortably past internal/importer/handlers.go's
// maxUploadBytes (10<<20, 10 MiB) so the result reliably exceeds the cap
// regardless of per-invoice row-length variance.
const oversizedSeed = 999003
const oversizedTargetBytes = 10<<20 + 1<<20 // cap + 1 MiB margin

// buildOversized returns an in-memory CSV body exceeding the importer's
// 10<<20-byte upload cap (internal/importer/handlers.go's maxUploadBytes).
func buildOversized() []byte {
	count := 10000
	for {
		data := generateGreen(oversizedSeed, count)
		if len(data) > oversizedTargetBytes {
			return data
		}
		count *= 2
	}
}
