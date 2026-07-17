// This file is 03 (the Invoice context)'s own knowledge of its MBS
// projection: how a canonical invoice row becomes the payload object that
// 04 (internal/validation) evaluates. 04 must not import 03, and 03 must
// not import 04 ([payload-mapper]) -- the two contexts are separate services
// that meet over HTTP, so this mapper's only contract with 04 is the JSON it
// emits. `go list -deps ./internal/invoice` is validation-free by design;
// the contract tests that drive real rules against this output live in the
// external test package invoice_test (payload_engine_test.go).
//
// ONE mapper serves both the real and dry-run paths (M4-04-04 maps a
// Store.Get-hydrated Invoice; M4-04-07 builds an in-memory Invoice from each
// READY group's CreateInput and maps that). Two mappers that must agree
// forever is precisely how dry-run and real drift.
//
// Everything here is PURE: no DB, no HTTP, no clock (`time` is not even
// imported -- IssueDate formatting goes through the time.Time the caller
// already holds, never time.Now).
package invoice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// mbsDateLayout is dateEval's default layout (internal/validation/
// evaluators.go:256). issue_date crosses the wire in exactly this form.
const mbsDateLayout = "2006-01-02"

// jsonNumberRe is the JSON number grammar (RFC 7159 s6) -- the same predicate
// encoding/json applies to a json.Number when marshaling. Verified equivalent
// to `json.Marshal(json.Number(s)) == nil` across the Postgres numeric::text
// shapes and the grammar edges, with ONE deliberate divergence: the empty
// string, which encoding/json marshals to `0` for Go 1.5 back-compat. We
// reject it, so a blank money column can never be fabricated into a passing
// zero -- it falls back to the raw string and violates, faithfully.
var jsonNumberRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// jsonNumber projects a canonical money column (*string, read via ::text
// [D13]) onto its MBS wire value. nil reports nil -- the CALLER omits the key
// ([payload-absence]); a well-formed JSON number literal becomes a
// json.Number, which encoding/json emits as a BARE numeric literal preserving
// the exact decimal text (no float round-trip in 03); anything else becomes
// the RAW STRING.
//
// Money must cross the wire as a JSON number, never a string
// ([payload-numerics]). A string is silently wrong in BOTH directions: 04's
// toFloat (evaluators.go:68-103) accepts float64/json.Number/native ints but
// REJECTS strings, so `range` and `tax_math` would violate every CORRECT
// invoice; and line-cost-non-negative's CEL guard `type(x.unit_price) !=
// double` is true for a string, so that check would silently SKIP.
//
// The raw-string fallback is what keeps one bad invoice from killing a batch.
// 'NaN'::numeric is a valid Postgres value, so ::text can yield "NaN", and
// json.Marshal(json.Number("NaN")) ERRORS -- aborting the marshal of the
// whole batch (one bad invoice -> zero output -> a 500 for all 500). Import
// cannot produce it (bestEffortBadNumericField quarantines it at classify
// time, importer/service.go:267) but POST /v1/invoices can (createRequest.
// Subtotal is a *string with no numeric check -- store-invalid-faithfully).
// Validating the literal FIRST turns a batch-killer into a violation on that
// invoice alone: `range`/`tax_math` see a non-numeric value -> bad DATA.
func jsonNumber(v *string) any {
	if v == nil {
		return nil
	}
	if !jsonNumberRe.MatchString(*v) {
		return *v
	}
	return json.Number(*v)
}

// MBSPayload projects a canonical invoice onto the MBS payload OBJECT. The
// caller roots it under "invoice" (Decision N19: rule targets carry no
// "invoice." prefix, so resolvePath -- engine.go:111-131 -- roots at
// p["invoice"] itself).
//
// Callers must pass a Store.Get-hydrated (or CreateInput-built) Invoice:
// Store.List returns headers with LineItems nil ([D7], store.go:209-213), and
// a List-sourced invoice would silently omit line_items -- violating
// line-items-required on a perfectly valid invoice. The mapper is pure and
// cannot detect this.
func MBSPayload(inv Invoice) map[string]any {
	p := map[string]any{
		"invoice_number": inv.InvoiceNumber,
	}
	if inv.IssueDate != nil {
		p["issue_date"] = inv.IssueDate.Format(mbsDateLayout)
	}

	putString(p, "currency", inv.Currency)
	putNumber(p, "subtotal", inv.Subtotal)
	putNumber(p, "vat", inv.VAT)
	putNumber(p, "total", inv.Total)

	// The flat supplier_*/buyer_* columns nest under "supplier"/"buyer" --
	// v2's supplier-tin-required/supplier-name-required/supplier-tin-format/
	// buyer-tin-format all resolve dotted paths ("supplier.tin"). An all-NULL
	// party is omitted wholesale; resolvePath walks a missing intermediate
	// segment to present=false, so `required` still fires exactly as it would
	// on an emitted-but-empty object.
	if supplier := party(inv.SupplierTIN, inv.SupplierName); len(supplier) > 0 {
		p["supplier"] = supplier
	}
	if buyer := party(inv.BuyerTIN, inv.BuyerName); len(buyer) > 0 {
		p["buyer"] = buyer
	}

	// Omit line_items entirely when there are none -- never []. This is
	// CORRECTNESS, not style ([payload-absence]): an emitted [] is present,
	// non-null and not a blank string, so line-items-required would PASS a
	// line-less invoice when it must fail. Omitting makes it violate.
	if len(inv.LineItems) > 0 {
		lines := make([]any, len(inv.LineItems))
		for i, li := range inv.LineItems {
			lines[i] = mbsLine(li)
		}
		p["line_items"] = lines
	}

	return p
}

// mbsLine projects one line onto its MBS object, under the same absence and
// numeric rules as the header ([payload-line-id]).
func mbsLine(li LineItem) map[string]any {
	m := map[string]any{
		"line_no": li.LineNo,
	}

	// id is the STABLE line id no-duplicate-line-items keys on
	// (migrations/20260714105151_line_items.sql's header). Stored invoices
	// always have one; dry-run lines have none yet, and for them the id must
	// be ABSENT rather than "" -- the rule's `!has(x.id)` guard is what passes
	// them. Emitting "" instead would make every dry-run line share the id ""
	// and fire no-duplicate-line-items on every multi-line dry-run invoice.
	if li.ID != "" {
		m["id"] = li.ID
	}

	putString(m, "description", li.Description)
	putNumber(m, "quantity", li.Quantity)
	putNumber(m, "unit_price", li.UnitPrice)
	putNumber(m, "line_total", li.LineTotal)
	putNumber(m, "line_tax", li.LineTax)
	return m
}

// party builds a nested {tin, name} object, omitting the NULL members.
func party(tin, name *string) map[string]any {
	m := make(map[string]any, 2)
	putString(m, "tin", tin)
	putString(m, "name", name)
	return m
}

// putString sets key to *v, or omits it entirely when v is nil. A NULL
// canonical column is ABSENT on the wire, never present-with-null
// ([payload-absence]) -- every v1/v2 rule treats absent and null alike, but
// null is a CEL landmine: CEL's has() is TRUE for a present-but-null key, so
// `!has(invoice.line_items) || invoice.line_items.all(...)` would fall
// through to .all() on a null and ERROR -> a 500 (Decision N15 fails loud on
// engine faults).
func putString(m map[string]any, key string, v *string) {
	if v != nil {
		m[key] = *v
	}
}

// putNumber sets key to jsonNumber(v), or omits it entirely when v is nil.
func putNumber(m map[string]any, key string, v *string) {
	if n := jsonNumber(v); n != nil {
		m[key] = n
	}
}

// contentFingerprint is a sha256 over the ten MBS-content columns of an
// invoices row -- invoice_number, issue_date, supplier_tin, supplier_name,
// buyer_tin, buyer_name, currency, subtotal, vat, total. It deliberately
// excludes everything that is not MBS content (id, tenant_id, entity_id,
// import_batch_id, status, violations, rule_set_version_id, created_at).
//
// [toctou-staleness] uses it to detect that an invoice's content changed
// under a validate run: re-fingerprinting the row inside the write tx and
// comparing against the fingerprint taken when the payload was built catches
// a concurrent edit that would otherwise let stale violations be written
// against content that no longer exists.
//
// Pure and deterministic: fields are hashed in a fixed order, each
// length-prefixed and NULL-marked, so the encoding is injective -- no pair of
// distinct column tuples can collide by concatenation (("ab","c") and
// ("a","bc") hash differently, and a NULL is distinct from "").
func contentFingerprint(inv Invoice) string {
	h := sha256.New()

	var issueDate *string
	if inv.IssueDate != nil {
		// Hash the same YYYY-MM-DD projection the payload carries: issue_date
		// is a `date` column, so the time-of-day/zone of the scanned
		// time.Time is representation, not content.
		s := inv.IssueDate.Format(mbsDateLayout)
		issueDate = &s
	}

	writeFingerprintField(h, &inv.InvoiceNumber)
	writeFingerprintField(h, issueDate)
	writeFingerprintField(h, inv.SupplierTIN)
	writeFingerprintField(h, inv.SupplierName)
	writeFingerprintField(h, inv.BuyerTIN)
	writeFingerprintField(h, inv.BuyerName)
	writeFingerprintField(h, inv.Currency)
	writeFingerprintField(h, inv.Subtotal)
	writeFingerprintField(h, inv.VAT)
	writeFingerprintField(h, inv.Total)

	return hex.EncodeToString(h.Sum(nil))
}

// writeFingerprintField writes one length-prefixed, NULL-marked field into h.
func writeFingerprintField(h io.Writer, v *string) {
	if v == nil {
		fmt.Fprint(h, "N;")
		return
	}
	fmt.Fprintf(h, "S%d:%s;", len(*v), *v)
}
