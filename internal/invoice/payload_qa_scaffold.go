// This file is a QA Mode-A compile scaffold for task-108 / M4-04-02
// ("Canonical invoice -> MBS payload mapper") -- NOT the mapper, and NOT
// meant to be reused, patched, or extended. It exists for exactly one
// reason: PAY-01..21 (payload_test.go, payload_engine_test.go) reference
// MBSPayload/contentFingerprint, and Go cannot compile a test file whose
// symbols don't exist. The RALPH orchestrator's Mode-A brief for this
// subtask is explicit -- "You do NOT write payload.go. The executor drives
// your reds to green." -- so this scaffold is deliberately NOT named
// payload.go, and is deliberately NOT a good-faith attempt at the real
// mapper.
//
// Every field below is mapped the NAIVE, WRONG way the story's Description/
// Decisions section explicitly warns against, so that PAY-01..21 fail on a
// real assertion (not a Go compile error) and, for most specs, fail for a
// semantically meaningful reason that the real payload.go must fix:
//   - supplier/buyer stay FLAT ("supplier_tin"/"buyer_tin"), never nested
//     under "supplier"/"buyer" ([payload-mapper]/Decision N19) -- PAY-01,
//     and (via the resulting *-required misses) part of PAY-18.
//   - money (subtotal/vat/total/quantity/unit_price/line_total/line_tax)
//     is emitted as the raw *string Invoice/LineItem field, NEVER as
//     json.Number -- never a bare JSON number ([payload-numerics]) -- PAY-02,
//     PAY-03, PAY-04, PAY-14, PAY-15, PAY-16.
//   - every field is emitted even when its source column is nil (present-
//     with-null, never omitted) -- [payload-absence] -- PAY-05.
//   - line_items is ALWAYS present (even []), never omitted when there are
//     zero lines -- [payload-absence] -- PAY-07, PAY-08.
//   - each mapped line deliberately omits "id" -- [payload-line-id] --
//     PAY-11.
//   - issue_date uses time.Time's default String() form, not "YYYY-MM-DD"
//     -- PAY-19.
//   - contentFingerprint is a constant, ignoring content entirely -- PAY-20.
//
// A handful of PAY specs cannot be forced RED by ANY stub shape -- they
// assert an invariant of the *validation* engine's rule semantics that
// holds regardless of whether MBSPayload is implemented (e.g. "required"
// treats a present-null field exactly like an absent one, so PAY-06 passes
// under any stub that doesn't fabricate a real currency value; "line_sum"/
// the CEL guard's absence branch both no-op on an empty list REGARDLESS of
// whether it's represented as `null` or `[]`, so PAY-09/PAY-10 hold too).
// See the QA report (not this file) for the full per-spec RED/pass
// breakdown and rationale.
//
// The executor deletes this ENTIRE file when authoring the real
// internal/invoice/payload.go (task-108 / M4-04-02).
package invoice

// MBSPayload -- QA Mode-A scaffold. See file header: deliberately naive/
// wrong on every axis the real mapper must get right.
func MBSPayload(inv Invoice) map[string]any {
	lines := make([]map[string]any, len(inv.LineItems))
	for i, li := range inv.LineItems {
		lines[i] = map[string]any{
			// "id" deliberately omitted -- PAY-11 must RED on this.
			"line_no":     li.LineNo,
			"description": li.Description,
			"quantity":    li.Quantity,  // WRONG: *string, never a JSON number.
			"unit_price":  li.UnitPrice, // WRONG: *string, never a JSON number.
			"line_total":  li.LineTotal, // WRONG: *string, never a JSON number.
			"line_tax":    li.LineTax,   // WRONG: *string, never a JSON number.
		}
	}

	var issueDate any
	if inv.IssueDate != nil {
		issueDate = inv.IssueDate.String() // WRONG: not "YYYY-MM-DD".
	}

	return map[string]any{
		"invoice_number": inv.InvoiceNumber,
		"issue_date":     issueDate,
		"currency":       inv.Currency,     // WRONG: flat *string -> null, never omitted.
		"subtotal":       inv.Subtotal,     // WRONG: *string, never a JSON number.
		"vat":            inv.VAT,          // WRONG: *string, never a JSON number.
		"total":          inv.Total,        // WRONG: *string, never a JSON number.
		"supplier_tin":   inv.SupplierTIN,  // WRONG: flat, never nested under "supplier".
		"supplier_name":  inv.SupplierName, // WRONG: flat, never nested under "supplier".
		"buyer_tin":      inv.BuyerTIN,     // WRONG: flat, never nested under "buyer".
		"buyer_name":     inv.BuyerName,    // WRONG: flat, never nested under "buyer".
		"line_items":     lines,            // WRONG: always present, even when empty.
	}
}

// contentFingerprint -- QA Mode-A scaffold. Constant, ignores content
// entirely -- see file header.
func contentFingerprint(inv Invoice) string {
	return ""
}
