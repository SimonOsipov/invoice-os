// suggest.go: csvrun.py's SYSTEM/SCHEMA and the AIR-07 mapping guard. Pure Go; no HTTP, no DB.
package importer

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// sampleRows is csvrun.py's SAMPLE_ROWS (:17). NOT handlers.go's maxSampleRows, which
	// caps the preview response; equal at 5 by coincidence, mutated independently.
	sampleRows = 5
	// maxDetectableHeaderRow: csvgen.py:285 builds 1-3 title rows + a blank, so 5 is the
	// deepest header the measurement covered. A choice, not a derived literal (D-03).
	maxDetectableHeaderRow = 5
	windowRows             = maxDetectableHeaderRow + sampleRows
	defaultHeaderRow       = 1
	mappingSchemaName      = "column_mapping"                                              // csvrun.py:87
	mappingIntro           = "The first rows of the file, as CSV. Row numbers start at 1." // csvrun.py:61
	mappingRowFmt          = "Row %d: %s"                                                  // csvrun.py:66
)

// mappingFields is csvrun.py's FIELDS (:13-14), in order. Same eleven, same order, as
// canonicalFields (service.go:154-166).
var mappingFields = []string{"invoice_number", "issue_date", "buyer_tin", "buyer_name",
	"currency", "subtotal", "vat", "total", "line_description", "line_quantity", "line_unit_price"}

// mappingSystem is csvrun.py's SYSTEM (:19-40), byte for byte
// (TestMappingPrompt_MatchesTheMeasuredHarness).
const mappingSystem = `You map the columns of a spreadsheet to the fields of a Nigerian e-invoicing import.

The importer reads one row per invoice line. Invoice-level values (number, date, buyer, currency, subtotal, VAT, total) repeat on every line of the same invoice.

Return a JSON object with exactly these keys.
For each field, the value is the exact header text of the one column that holds that field, copied character for character, or null when no column holds it. A column maps to at most one field. Leave a field null rather than map a column that holds something else.
- invoice_number: the invoice's own number. Not an order, PO, customer, account, internal record ID or payment reference.
- issue_date: the date the invoice was issued. Not a due date, delivery date or payment date.
- buyer_tin: the buyer's (customer's) Tax Identification Number. Not the seller's own TIN or an RC number.
- buyer_name: the buyer's name. Not a customer code, address, email or phone.
- currency: the currency code.
- subtotal: the invoice's amount before VAT. Not a line amount.
- vat: the invoice's VAT amount. Not a VAT rate or a line's tax.
- total: the invoice's total including VAT. Not a line amount, amount paid, balance due or amount after withholding tax.
- line_description: the line's item or service description.
- line_quantity: the line's quantity.
- line_unit_price: the line's price per unit. Not a line total.

Also return:
- header_row: the 1-based row number of the header row among the rows shown. Return 1 when only headers are shown.
- date_format: the format of the issue date values, one of "YYYY-MM-DD", "DD/MM/YYYY", "MM/DD/YYYY", "DD-MMM-YYYY", "other", or "ambiguous" when the values shown fit both DD/MM/YYYY and MM/DD/YYYY. null when there is no issue date column or no values are shown.
- decimal_separator: "." or "," as used in the amount columns, or null when no amounts are shown.`

// mappingSchema is mappingSchemaJSON()'s output, built once at package init.
var mappingSchema = mappingSchemaJSON()

// mappingSchemaJSON mirrors aiSchemaFor (aireading.go:55-68): csvrun.py's SCHEMA (:42-44),
// transcribed.
func mappingSchemaJSON() json.RawMessage {
	props := make(map[string]any, len(mappingFields)+3)
	for _, f := range mappingFields {
		props[f] = map[string]any{"type": []string{"string", "null"}}
	}
	props["header_row"] = map[string]any{"type": "integer"}
	props["date_format"] = map[string]any{"type": []string{"string", "null"}}
	props["decimal_separator"] = map[string]any{"type": []string{"string", "null"}}

	required := append(append([]string{}, mappingFields...), "header_row", "date_format", "decimal_separator")

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           props,
	}
	b, _ := json.Marshal(schema) // fixed, marshalable shape: cannot fail
	return b
}

// suggestWindow reshapes Decode's header+rows into the AI's window (§6): header prepended to
// at most windowRows-1 rows, fewer for a short file, never padded, nil for an empty header.
func suggestWindow(header []string, rows [][]string) [][]string {
	if len(header) == 0 {
		return nil
	}
	n := len(rows)
	if n > windowRows-1 {
		n = windowRows - 1
	}
	out := make([][]string, 0, n+1)
	return append(append(out, header), rows[:n]...)
}

// mappingPromptText renders csvrun.py's `rows` user text (:60-61,:66).
func mappingPromptText(rows [][]string) string {
	if len(rows) == 0 {
		return mappingIntro
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = fmt.Sprintf(mappingRowFmt, i+1, csvLine(r))
	}
	return mappingIntro + "\n" + strings.Join(lines, "\n")
}

// csvLine renders one row the way csvrun.py's csv_line does.
// ceiling: Go's csv quotes a leading-space field, Python's does not; a suggestion is lost,
// never mis-placed (TestCSVLine_MatchesPythonExceptForALeadingSpaceField).
func csvLine(row []string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(row)
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// guardHeaderRow validates the AI's header_row claim against the window (§6 rule 1). Only a
// json.Number in [1, windowLen] resolves; anything else falls back to defaultHeaderRow.
func guardHeaderRow(ans map[string]any, windowLen int) int {
	n, ok := ans["header_row"].(json.Number)
	if !ok {
		return defaultHeaderRow
	}
	v, err := n.Int64()
	if err != nil || v < 1 || v > int64(windowLen) {
		return defaultHeaderRow
	}
	return int(v)
}

// guardPlacements validates the AI's field->header claims against the re-decoded header
// (§6 rules 3-5). Always returns a non-nil map: a nil map marshals to null, which §2 forbids.
func guardPlacements(ans map[string]any, header []string) map[string]string {
	inHeader := make(map[string]bool, len(header))
	for _, h := range header {
		inHeader[h] = true
	}
	placed := make(map[string]string, len(header))
	claims := make(map[string]int, len(header))
	for field, raw := range ans {
		if !canonicalFields[field] { // rule 3
			continue
		}
		name, ok := raw.(string) // rule 4
		if !ok || strings.TrimSpace(name) == "" || !inHeader[name] {
			continue
		}
		placed[field] = name
		claims[name]++
	}
	for field, name := range placed { // rule 5
		if claims[name] > 1 {
			delete(placed, field)
		}
	}
	return placed
}
