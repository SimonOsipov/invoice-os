// QA Mode-B adversarial coverage for task-108 / M4-04-02, added AFTER
// implementation (payload.go exists and PAY-01..21 are green) per the
// QA agent's own charter: extend coverage the Test-first red specs did not
// include. Two gaps identified against task-108's Stage-1 addendum and the
// executor's own judgment-call comments in payload.go:
//
//   - the LineItem.ID == "" (dry-run) case: payload.go's mbsLine doc comment
//     explains WHY "" must be omitted rather than emitted (it would give
//     every dry-run line the SAME id, "", and fire no-duplicate-line-items
//     on every multi-line dry-run invoice) but no PAY spec exercises it --
//     task-108's Stage-1 addendum flags this as a candidate for QA coverage
//     rather than adding a test itself. Covered here (pure-mapper half);
//     the real-rule half (does the guard actually pass a multi-line
//     dry-run invoice, and does the REJECTED alternative actually violate)
//     lives in payload_engine_adversarial_test.go, package invoice_test,
//     since it needs the validation engine.
//   - the jsonNumber grammar boundary: PAY-02/12/14 exercise exactly two
//     literals ("1058875.00" well-formed, "NaN" not). The mapper's own
//     comment claims jsonNumberRe is "verified equivalent to
//     json.Marshal(json.Number(s)) == nil ... with ONE deliberate
//     divergence: the empty string". That equivalence claim is untested --
//     this file tests it, table-driven, over the tricky literals a
//     Postgres numeric(14,2)::text read can plausibly produce or a
//     store-invalid-faithfully caller can inject.
//
// Neither test below duplicates a PAY spec; both target failure modes this
// story's own history flags as central: a silently WRONG accept (a
// fabricated/garbled number reaching the engine) or a silently WRONG reject
// (a valid invoice violating), and a silently skipped rule masquerading as
// a pass (no-duplicate-line-items on empty-string ids).
package invoice

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------
// LineItem.ID == "" (dry-run): the mapped line omits "id" entirely, for
// one line and for several lines at once (the multi-line dry-run shape
// the design doc's own worry is about).
// ---------------------------------------------------------------------

// TestMBSPayload_EmptyLineItemID_OmitsIDKey confirms mbsLine's `if li.ID !=
// ""` guard actually fires for dry-run-shaped lines (no PK yet). A
// regression to `m["id"] = li.ID` unconditionally would make every line
// below carry the identical key "id":"" -- exactly the shared-id hazard
// [payload-line-id]'s comment describes.
func TestMBSPayload_EmptyLineItemID_OmitsIDKey(t *testing.T) {
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "", LineNo: 1, UnitPrice: strPtr("100.00")},
			{ID: "", LineNo: 2, UnitPrice: strPtr("50.00")},
			{ID: "", LineNo: 3, UnitPrice: strPtr("25.00")},
		},
	}
	p := MBSPayload(inv)

	lines, ok := p["line_items"].([]any)
	if !ok || len(lines) != 3 {
		t.Fatalf("p[\"line_items\"] = %#v, want a 3-element slice", p["line_items"])
	}
	for i, raw := range lines {
		line, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("line_items[%d] not a map: %#v", i, raw)
		}
		if v, present := line["id"]; present {
			t.Errorf(`line_items[%d]["id"] present = %#v, want the key entirely ABSENT for `+
				`a dry-run line with no PK yet -- an emitted "" would give every dry-run `+
				`line the SAME id and trip no-duplicate-line-items on any multi-line `+
				`dry-run invoice`, i, v)
		}
		// line_no must still be present -- absence is specific to id, not the
		// whole line object.
		if line["line_no"] != i+1 {
			t.Errorf("line_items[%d][\"line_no\"] = %#v, want %d", i, line["line_no"], i+1)
		}
	}
}

// TestMBSPayload_MixedEmptyAndRealLineItemIDs confirms the id/no-id decision
// is per-line, not invoice-wide -- a real regression scenario is a partially
// hydrated Invoice (should not occur via Store.Get/CreateInput today, but
// the mapper has no way to know that and must not silently do something
// invoice-wide like "if any line lacks an id, drop ids from all lines").
func TestMBSPayload_MixedEmptyAndRealLineItemIDs(t *testing.T) {
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "11111111-1111-1111-1111-111111111111", LineNo: 1},
			{ID: "", LineNo: 2},
		},
	}
	p := MBSPayload(inv)
	lines := p["line_items"].([]any)

	l0 := lines[0].(map[string]any)
	if l0["id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf(`line_items[0]["id"] = %#v, want the real uuid`, l0["id"])
	}
	l1 := lines[1].(map[string]any)
	if _, present := l1["id"]; present {
		t.Errorf(`line_items[1]["id"] present = %#v, want absent (this line has no id)`, l1["id"])
	}
}

// ---------------------------------------------------------------------
// jsonNumber grammar boundary: table-driven agreement with
// json.Marshal(json.Number(s)) over every tricky literal, except the one
// deliberate divergence (the empty string).
// ---------------------------------------------------------------------

// TestJSONNumber_GrammarBoundary_MatchesEncodingJSON exercises jsonNumberRe
// (and therefore jsonNumber) against literals a Postgres numeric::text read
// or an unchecked *string HTTP field can plausibly produce -- whitespace,
// signs, leading zeros, exponents, underscores, "Infinity"/"NaN", and
// several well-formed shapes -- and asserts the mapper's accept/reject
// decision matches json.Marshal(json.Number(s))'s own accept/reject
// decision EXACTLY, except for "" (documented, deliberate: encoding/json
// marshals json.Number("") to the literal 0 for Go 1.5 back-compat, which
// would fabricate a value for a blank money column; the mapper instead
// rejects it and falls back to the raw string, so a blank column violates
// rather than silently passing as zero).
//
// A false ACCEPT here would let a fabricated/garbled number reach the
// engine as a bare JSON number (range/tax_math would treat garbage as
// valid data); a false REJECT would turn a valid invoice's money field
// into a raw string, silently mistaken for bad data by every numeric rule.
func TestJSONNumber_GrammarBoundary_MatchesEncodingJSON(t *testing.T) {
	cases := []string{
		"",                // deliberate divergence -- asserted separately below
		" ",               // whitespace only
		"+1",              // explicit leading plus -- JSON numbers never allow it
		"1e5",             // valid: exponent, no fractional part
		".5",              // invalid: JSON requires a leading digit before "."
		"01",              // invalid: JSON forbids a leading zero before other digits
		"-",               // invalid: bare sign, no digits
		"-0",              // valid
		"-01",             // invalid: leading zero applies to negatives too
		"Infinity",        // invalid: not a JSON number token
		"-Infinity",       // invalid
		"1_000",           // invalid: JSON has no digit-group separator
		"1.",              // invalid: JSON requires a digit after "."
		"1.0",             // valid
		"1e",              // invalid: exponent with no digits
		"1e+5",            // valid: explicit exponent sign
		"1E5",             // valid: uppercase E
		"1.5e10",          // valid: fraction + exponent
		"  1",             // invalid: leading whitespace
		"1  ",             // invalid: trailing whitespace
		"1058875.00",      // valid -- the PAY-02 fixture, included for the table's completeness
		"999999999999.99", // valid -- numeric(14,2)'s widest value
		"0",               // valid
		"-0.5",            // valid
		"5.",              // invalid
		"0x1A",            // invalid: hex is not a JSON number
		"1,000",           // invalid: comma is not part of the grammar
		"1.5.5",           // invalid: two decimal points
		"--1",             // invalid: double sign
		"1-",              // invalid: trailing sign
	}

	for _, tc := range cases {
		tc := tc
		t.Run("literal="+tc, func(t *testing.T) {
			_, marshalErr := json.Marshal(json.Number(tc))
			wellFormed := marshalErr == nil

			got := jsonNumber(&tc)

			if tc == "" {
				// The one deliberate divergence: json.Marshal accepts "" (emits
				// 0) but the mapper must reject it and fall back to the raw
				// string, so a blank column violates rather than being
				// fabricated into a passing zero.
				s, ok := got.(string)
				if !ok || s != "" {
					t.Fatalf(`jsonNumber(&"") = %#v (%T), want the raw string "" -- the `+
						`documented divergence from json.Marshal(json.Number("")), which `+
						`emits 0 and would fabricate a value for a blank money column`, got, got)
				}
				return
			}

			if wellFormed {
				n, ok := got.(json.Number)
				if !ok {
					t.Fatalf("jsonNumber(%q) = %#v (%T), want json.Number(%q) -- "+
						"json.Marshal(json.Number(%q)) succeeds, so the mapper must accept "+
						"it as a bare number, not fall back to a string", tc, got, got, tc, tc)
				}
				if string(n) != tc {
					t.Errorf("jsonNumber(%q) = json.Number(%q), want the exact input text "+
						"preserved with no re-encoding", tc, string(n))
				}
			} else {
				s, ok := got.(string)
				if !ok {
					t.Fatalf("jsonNumber(%q) = %#v (%T), want the raw string %q -- "+
						"json.Marshal(json.Number(%q)) fails (%v), so wrapping it in "+
						"json.Number would poison the whole batch marshal; the mapper "+
						"must fall back to the raw string instead", tc, got, got, tc, tc, marshalErr)
				}
				if s != tc {
					t.Errorf("jsonNumber(%q) = raw string %q, want the exact input text %q", tc, s, tc)
				}
			}
		})
	}
}
