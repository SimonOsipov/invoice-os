// gen.go implements the builder API for the synthetic invoice-import
// fixtures: deterministic CSV generation for the green (canonical) shape
// and edge-case mutations, plus an in-memory oversized-body inflator.
// Every builder is seeded only from its seed parameter (math/rand, no
// wall-clock), so identical (seed, invoices) always produces identical
// bytes.
//
// Canonical header and money math mirror
// internal/importer/service_test.go's stdHeader/stdMapping and
// e2e/importFixtures.ts's PERF_HEADER/PERF_MAPPING byte-for-byte, and the
// seeded vat-standard-rate rule (rate=0.075, tolerance=0.005).
//
// Money is computed in integer cents throughout, never float64, so
// subtotal/vat/total round-trip exactly through decimal-string CSV cells.

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

// canonicalHeader mirrors internal/importer/service_test.go's stdHeader
// and e2e/importFixtures.ts's PERF_HEADER byte-for-byte.
const canonicalHeader = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price"

// Column indices into the canonical 11-field row (kept separate from
// gen_test.go's own colXxx; production code can't depend on _test.go
// declarations).
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

// buyerNames is the fixed pool of Nigerian buyer names drawn from for
// every invoice.
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

// nonASCIIBuyer is forced onto buildEdgeBadEncoding's first invoice: a
// pure-ASCII UTF-16LE re-encoding is still valid UTF-8, so this guarantees
// non-ASCII content regardless of seed.
const nonASCIIBuyer = "Chukwuemeka Okàfor Ltd"

// invoiceRec is one invoice's header-repeating fields plus its 3 line
// rows, held in integer cents (formatted to 2dp only at render time; see
// moneyStr).
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

// genInvoices builds n deterministic invoices from rng: subtotal =
// sum(qty*unit_price) cents, vat = round(0.075*subtotal), total =
// subtotal+vat; invoice numbers are sequential (INV-SYN-00001, ...), so
// unique by construction.
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

// genBuyerTIN generates a Luhn-valid NNNNNNNN-NNNN TIN; Luhn validity is
// decorative only -- the shipped rule just checks the ^[0-9]{8}-[0-9]{4}$
// shape (tinRE).
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

// luhnCheckDigit computes the standard Luhn check digit for payload (MSB
// first): every second digit from the right is doubled (minus 9 if >9),
// then the check digit brings the sum to a multiple of 10.
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

// renderCSV writes header + rows as RFC 4180 CSV ('\n'-terminated). A
// write/flush error means malformed input -- a generator bug -- so it
// panics rather than returning an error.
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

// generateGreen builds the canonical green CSV: invoices invoices, 3 line
// rows each, deterministic for seed.
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

// buildEdgeInFileDupes derives from a green base then adds a duplicate row
// for one invoice, differing only on Issue Date. Uses a small, fixed
// invoice count to keep the file small.
func buildEdgeInFileDupes(seed int64) []byte {
	const n = 5
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, n)
	rows := allRows(invs)

	// Duplicate the first invoice's first row, changing only Issue Date.
	dupe := csvRow(invs[0], 0)
	origDate, err := time.Parse("2006-01-02", invs[0].issueDate)
	if err != nil {
		panic(fmt.Sprintf("fixturegen: parsing generated issue date %q: %v", invs[0].issueDate, err))
	}
	dupe[idxIssueDate] = origDate.AddDate(0, 0, 1).Format("2006-01-02")
	rows = append(rows, dupe)

	return renderCSV(strings.Split(canonicalHeader, ","), rows)
}

// buildEdgeBadEncoding derives from a green base, forces the first buyer
// name non-ASCII (see nonASCIIBuyer), then re-encodes as UTF-16LE without
// a BOM.
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
		// regex shape.
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
		// subtotal/lines stay untouched so they still reconcile (min
		// possible subtotal is NGN 3.00, above the NGN 1.00 floor); Total
		// is recomputed to subtotal+0 so VAT is the only intended defect.
		invs[0].vatC = 0
		invs[0].totalC = invs[0].subtotalC
	}
	return renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))
}

// oversizedSeed / oversizedTargetBytes drive buildOversized: a pinned
// seed and a target comfortably past maxUploadBytes
// (internal/importer/handlers.go, 10 MiB).
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

// submissionDemoTINs are the M5-03 mock-adapter trigger TINs, in the order
// this fixture assigns them. The mock decides its verdict by EXACT buyer-TIN
// match (internal/submission/mock_script.go, mockTriggerFor) -- any TIN
// outside this reserved block takes the ordinary accept path, which is why
// every other committed fixture demos only the happy path.
//
// Deliberately Luhn-INVALID, so genBuyerTIN can never mint one by accident;
// that is what makes them safe as reserved triggers. They still satisfy the
// seeded buyer-tin-format rule (^[0-9]{8}-[0-9]{4}$), so invoices carrying
// them validate normally and are genuinely submittable -- the whole point.
var submissionDemoTINs = []string{
	"99999999-0001", // accepted
	"99999999-0002", // rejected, with a reason on buyer.tin
	"99999999-0003", // pending, then accepted after exactly two re-polls
	"99999999-0004", // permanently unavailable: retries, then dead-letters
}

// buildGreenSubmissionDemo derives from a green base and overrides the first
// len(submissionDemoTINs) buyer TINs with the mock's trigger block, leaving
// the remaining invoices ordinary (and therefore accepted). The result is a
// fully valid, importable CSV that exercises every M5-04 submission outcome
// -- accept, reject, deferred re-poll, and dead-letter -- in one import.
//
// Same override idiom as buildEdgeBadTin/buildEdgeBadEncoding: generate green,
// mutate the minimum, re-render. Unlike those, nothing here is malformed.
func buildGreenSubmissionDemo(seed int64, invoices int) []byte {
	rng := rand.New(rand.NewSource(seed))
	invs := genInvoices(rng, invoices)
	for i, tin := range submissionDemoTINs {
		if i < len(invs) {
			invs[i].buyerTIN = tin
		}
	}
	return renderCSV(strings.Split(canonicalHeader, ","), allRows(invs))
}
