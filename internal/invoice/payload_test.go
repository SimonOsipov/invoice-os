// task-108 / M4-04-02 (Test-first: yes) -- Mode A RED specs for the
// canonical invoice -> MBS payload mapper. Transcribes task-108's PAY-*
// Test Specs table (plus PAY-21 from the Stage-1 architecture addendum's D3)
// into runnable Go tests, authored BEFORE internal/invoice/payload.go
// exists. See task-108 (mcp__backlog__task_view id=task-108) for the
// authoritative Description/Acceptance Criteria/Test Specs/Decisions, and
// the M4-04 Validate Gate story (Sec M4-04-02) + QA Debate Log for context.
//
// Split per the Stage-1 addendum's D2 (the compile blocker: every evaluator
// in internal/validation is deliberately unexported, so a rule can only be
// driven through validation.NewDefaultEngine() + a validation.RuleSet --
// never by naming an evaluator type from package invoice):
//   - payload_test.go (THIS FILE), package invoice -- the pure-mapper specs
//     that assert on MBSPayload's/contentFingerprint's DIRECT output, no
//     internal/validation import: PAY-01, 02, 05, 07, 12, 14, 19, 20, 21.
//   - payload_engine_test.go, package invoice_test (external) -- the
//     real-rule specs, which import both internal/invoice and
//     internal/validation: PAY-03, 04, 06, 08, 09, 10, 11, 13, 15, 16, 17,
//     18.
//
// This keeps `go list -deps ./internal/invoice` free of internal/validation
// -- [payload-mapper]'s 03-must-not-import-04 ban stays provably intact.
//
// Every test below runs against internal/invoice/payload_qa_scaffold.go, a
// QA Mode-A compile scaffold (NOT the mapper -- see that file's header for
// why it exists and exactly how it is deliberately wrong). Per-spec
// RED/pass status against that scaffold is called out in each test's
// comment; the QA report summarizes all 21.
package invoice

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// PAY-01 -- flat supplier/buyer columns nest under "supplier"/"buyer".
// ---------------------------------------------------------------------

// TestMBSPayload_SupplierBuyerNested (PAY-01): RED against the scaffold --
// supplier_tin/supplier_name/buyer_tin/buyer_name stay flat there, never
// nested.
func TestMBSPayload_SupplierBuyerNested(t *testing.T) {
	inv := Invoice{
		SupplierTIN:  strPtr("12345678-0001"),
		SupplierName: strPtr("Acme Ltd"),
		BuyerTIN:     strPtr("87654321-0002"),
		BuyerName:    strPtr("Beta Ltd"),
	}
	p := MBSPayload(inv)

	supplier, ok := p["supplier"].(map[string]any)
	if !ok {
		t.Fatalf(`MBSPayload()["supplier"] = %#v (%T), want a map[string]any -- flat `+
			`supplier_tin/supplier_name must nest under "supplier" [PAY-01]`, p["supplier"], p["supplier"])
	}
	if got := supplier["tin"]; got != "12345678-0001" {
		t.Errorf(`p["supplier"]["tin"] = %#v, want "12345678-0001" [PAY-01]`, got)
	}
	if got := supplier["name"]; got != "Acme Ltd" {
		t.Errorf(`p["supplier"]["name"] = %#v, want "Acme Ltd" [PAY-01]`, got)
	}

	buyer, ok := p["buyer"].(map[string]any)
	if !ok {
		t.Fatalf(`MBSPayload()["buyer"] = %#v (%T), want a map[string]any -- flat `+
			`buyer_tin/buyer_name must nest under "buyer" [PAY-01]`, p["buyer"], p["buyer"])
	}
	if got := buyer["tin"]; got != "87654321-0002" {
		t.Errorf(`p["buyer"]["tin"] = %#v, want "87654321-0002" [PAY-01]`, got)
	}
	if got := buyer["name"]; got != "Beta Ltd" {
		t.Errorf(`p["buyer"]["name"] = %#v, want "Beta Ltd" [PAY-01]`, got)
	}
}

// ---------------------------------------------------------------------
// PAY-02 -- money is a bare JSON number, exact decimal text, no float
// round-trip.
// ---------------------------------------------------------------------

// TestMBSPayload_SubtotalMarshalsAsBareNumber (PAY-02): RED against the
// scaffold -- Subtotal is emitted as the raw *string, so it marshals
// quoted ("subtotal":"1058875.00"), not as a bare number.
func TestMBSPayload_SubtotalMarshalsAsBareNumber(t *testing.T) {
	inv := Invoice{Subtotal: strPtr("1058875.00")}
	b, err := json.Marshal(MBSPayload(inv))
	if err != nil {
		t.Fatalf("json.Marshal(MBSPayload(inv)): %v [PAY-02]", err)
	}
	if !strings.Contains(string(b), `"subtotal":1058875.00`) {
		t.Errorf(`json.Marshal output = %s, want it to contain "subtotal":1058875.00 `+
			`(a bare JSON number, exact decimal text, no float round-trip in 03) [PAY-02]`, b)
	}
	if strings.Contains(string(b), `"subtotal":"1058875.00"`) {
		t.Errorf(`json.Marshal output = %s -- subtotal was emitted as a JSON STRING, not a number [PAY-02]`, b)
	}
}

// ---------------------------------------------------------------------
// PAY-05 -- a NULL column omits its key, never present-with-null.
// ---------------------------------------------------------------------

// TestMBSPayload_NilCurrencyOmitsKey (PAY-05): RED against the scaffold --
// Currency is placed in the map even when nil, so the key is present with
// a nil value instead of being absent.
func TestMBSPayload_NilCurrencyOmitsKey(t *testing.T) {
	inv := Invoice{Currency: nil}
	p := MBSPayload(inv)
	if v, ok := p["currency"]; ok {
		t.Errorf(`MBSPayload()["currency"] present = %#v, want the key entirely ABSENT `+
			`for a NULL column (never present-with-null) [PAY-05]`, v)
	}
}

// ---------------------------------------------------------------------
// PAY-07 -- a line-less invoice omits line_items entirely.
// ---------------------------------------------------------------------

// TestMBSPayload_EmptyLineItemsOmitsKey (PAY-07): RED against the scaffold
// -- line_items is always set to a (possibly empty) slice, so the key is
// present even with zero lines.
func TestMBSPayload_EmptyLineItemsOmitsKey(t *testing.T) {
	inv := Invoice{LineItems: nil}
	p := MBSPayload(inv)
	if v, ok := p["line_items"]; ok {
		t.Errorf(`MBSPayload()["line_items"] present = %#v, want the key entirely ABSENT `+
			`when there are zero line items [PAY-07]`, v)
	}
}

// ---------------------------------------------------------------------
// PAY-12 -- a store-invalid numeric ("NaN") marshals cleanly as a raw
// string.
// ---------------------------------------------------------------------

// TestMBSPayload_NaNSubtotalMarshalsAsRawString (PAY-12): passes even
// against the scaffold -- the scaffold always emits the raw *string, which
// happens to coincide with jsonNumber's documented not-well-formed
// fallback for "NaN" specifically. Kept as written (faithful transcription,
// not weakened); see the QA report for why this one can't be forced RED by
// any stub shape.
func TestMBSPayload_NaNSubtotalMarshalsAsRawString(t *testing.T) {
	inv := Invoice{Subtotal: strPtr("NaN")}
	b, err := json.Marshal(MBSPayload(inv))
	if err != nil {
		t.Fatalf(`json.Marshal(MBSPayload(inv)) with Subtotal="NaN": want success `+
			`(never poison the batch marshal), got error: %v [PAY-12]`, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal marshaled payload: %v", err)
	}
	if got, ok := decoded["subtotal"].(string); !ok || got != "NaN" {
		t.Errorf(`decoded subtotal = %#v, want the JSON STRING "NaN" `+
			`(jsonNumber's not-well-formed fallback) [PAY-12]`, decoded["subtotal"])
	}
}

// ---------------------------------------------------------------------
// PAY-14 -- one bad (NaN) invoice cannot poison a batch marshal; the
// sibling invoice is unaffected.
// ---------------------------------------------------------------------

// TestMBSPayload_BatchMarshalSurvivesOneBadInvoice (PAY-14): RED against the
// scaffold -- the clean invoice's numerics are also emitted as *string, so
// the "clean invoice's numerics are intact" number-type assertion fails.
func TestMBSPayload_BatchMarshalSurvivesOneBadInvoice(t *testing.T) {
	bad := Invoice{InvoiceNumber: "BAD-001", Subtotal: strPtr("NaN")}
	clean := Invoice{InvoiceNumber: "CLEAN-001", Subtotal: strPtr("1000.00")}

	batch := []map[string]any{MBSPayload(bad), MBSPayload(clean)}
	b, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("json.Marshal(batch) with one NaN invoice: want success "+
			"(one bad invoice must not poison the whole batch), got: %v [PAY-14]", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal marshaled batch: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded batch len = %d, want 2 [PAY-14]", len(decoded))
	}
	if got := decoded[1]["invoice_number"]; got != "CLEAN-001" {
		t.Errorf(`decoded[1]["invoice_number"] = %#v, want "CLEAN-001" -- the clean `+
			`invoice must be intact [PAY-14]`, got)
	}
	if got, ok := decoded[1]["subtotal"].(float64); !ok || got != 1000.00 {
		t.Errorf(`decoded[1]["subtotal"] = %#v, want the bare number 1000 -- the clean `+
			`invoice's numerics must be untouched by its NaN sibling [PAY-14]`, decoded[1]["subtotal"])
	}
}

// ---------------------------------------------------------------------
// PAY-19 -- issue_date is "YYYY-MM-DD", dateEval's default layout.
// ---------------------------------------------------------------------

// TestMBSPayload_IssueDateFormattedYYYYMMDD (PAY-19): RED against the
// scaffold -- issue_date uses time.Time's default String() form.
func TestMBSPayload_IssueDateFormattedYYYYMMDD(t *testing.T) {
	d := time.Date(2026, 7, 1, 15, 4, 5, 0, time.UTC)
	inv := Invoice{IssueDate: &d}
	p := MBSPayload(inv)

	got, ok := p["issue_date"].(string)
	if !ok {
		t.Fatalf(`MBSPayload()["issue_date"] = %#v (%T), want a string [PAY-19]`, p["issue_date"], p["issue_date"])
	}
	if got != "2026-07-01" {
		t.Errorf(`issue_date = %q, want "2026-07-01" (dateEval's default layout 2006-01-02) [PAY-19]`, got)
	}
	if _, err := time.Parse("2006-01-02", got); err != nil {
		t.Errorf("issue_date %q not parseable under dateEval's default layout: %v [PAY-19]", got, err)
	}
}

// ---------------------------------------------------------------------
// PAY-20 -- contentFingerprint changes iff content changes.
// ---------------------------------------------------------------------

// TestContentFingerprint_ChangesOnlyWithContent (PAY-20): RED against the
// scaffold -- contentFingerprint is a constant, so it Fatals immediately on
// the "non-empty" precondition before ever reaching the differs/matches
// assertions.
func TestContentFingerprint_ChangesOnlyWithContent(t *testing.T) {
	base := Invoice{
		InvoiceNumber: "INV-100",
		SupplierTIN:   strPtr("12345678-0001"),
		SupplierName:  strPtr("Acme"),
		BuyerTIN:      strPtr("87654321-0002"),
		BuyerName:     strPtr("Beta"),
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
	}
	sameAgain := base
	changedVAT := base
	changedVAT.VAT = strPtr("80.00")

	fpBase := contentFingerprint(base)
	fpSame := contentFingerprint(sameAgain)
	fpChanged := contentFingerprint(changedVAT)

	if fpBase == "" {
		t.Fatalf("contentFingerprint(base) is empty -- want a real hash [PAY-20]")
	}
	if fpBase != fpSame {
		t.Errorf("contentFingerprint differs for identical invoices: %q vs %q [PAY-20]", fpBase, fpSame)
	}
	if fpBase == fpChanged {
		t.Errorf("contentFingerprint unchanged despite a VAT change: both %q [PAY-20]", fpBase)
	}
}

// ---------------------------------------------------------------------
// PAY-21 -- MBSPayload/contentFingerprint are pure (AC #11; added by the
// Stage-1 architecture addendum's D3, which found AC #11 had no spec).
// ---------------------------------------------------------------------

// TestMBSPayload_PureAndDeterministic (PAY-21): passes even against the
// scaffold -- ANY deterministic stub (right or wrong) calls itself
// consistently, so a same-input/same-output determinism check can't be
// forced RED at Mode-A stage. Kept as written (faithful transcription of a
// real regression guard for once the real payload.go lands), not weakened;
// see the QA report. The spec's remaining structural facts -- neither
// signature takes a context.Context, and payload.go's import block names no
// DB/HTTP/time.Now dependency -- require the real file to exist and are a
// Mode-B (post-implementation) code-review check, not a Mode-A Go test.
func TestMBSPayload_PureAndDeterministic(t *testing.T) {
	inv := Invoice{
		InvoiceNumber: "INV-200",
		SupplierTIN:   strPtr("12345678-0001"),
		SupplierName:  strPtr("Acme"),
		BuyerTIN:      strPtr("87654321-0002"),
		BuyerName:     strPtr("Beta"),
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
		LineItems: []LineItem{
			{ID: "line-a", LineNo: 1, UnitPrice: strPtr("500.00")},
		},
	}

	p1 := MBSPayload(inv)
	p2 := MBSPayload(inv)
	if !reflect.DeepEqual(p1, p2) {
		t.Errorf("MBSPayload(inv) called twice with the same input produced different "+
			"results -- a clock/random/DB dependency would diverge; MBSPayload must be "+
			"pure [PAY-21]\n  first:  %#v\n  second: %#v", p1, p2)
	}

	f1 := contentFingerprint(inv)
	f2 := contentFingerprint(inv)
	if f1 != f2 {
		t.Errorf("contentFingerprint(inv) called twice produced different results "+
			"(%q vs %q) -- must be pure [PAY-21]", f1, f2)
	}
}
