// QA Mode-B adversarial coverage for task-108 / M4-04-02, added AFTER
// implementation, continued from payload_adversarial_test.go (package
// invoice): the real-rule half of the LineItem.ID == "" (dry-run) case,
// plus a canary pinning the D1 "silently skipped rule masquerading as a
// pass" failure mode that this whole subtask's numeric design exists to
// avoid. package invoice_test (external), for the same reason
// payload_engine_test.go is external: every evaluator in internal/validation
// is deliberately unexported, so a real rule can only be driven through
// validation.NewDefaultEngine() + validation.RuleSet. This adds no import
// edge to package invoice itself.
package invoice_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// ---------------------------------------------------------------------
// LineItem.ID == "" (dry-run), real-rule half: a multi-line dry-run
// invoice must NOT trip no-duplicate-line-items, and the REJECTED
// alternative (emitting "" as a real id) is shown to actually cause the
// false violation the design comment warns about -- proving the judgment
// call against the real rule, not just against the mapper's own output.
// ---------------------------------------------------------------------

// TestPayloadEngine_DryRunLineItems_NoDuplicateRulePasses_WithoutIDs
// (task-108 Stage-1 addendum, judgment call #2): several line items with no
// PK yet (LineItem.ID == "", the exact shape M4-04-07's in-memory dry-run
// Invoice produces) must pass no-duplicate-line-items -- the rule's own
// `!has(x.id)` guard is what makes that possible, and only holds if the
// mapper actually omits the key rather than emitting "".
func TestPayloadEngine_DryRunLineItems_NoDuplicateRulePasses_WithoutIDs(t *testing.T) {
	inv := invoice.Invoice{
		LineItems: []invoice.LineItem{
			{ID: "", LineNo: 1, UnitPrice: ptr("100.00")},
			{ID: "", LineNo: 2, UnitPrice: ptr("50.00")},
			{ID: "", LineNo: 3, UnitPrice: ptr("25.00")},
		},
	}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleNoDuplicateLineItems()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("no-duplicate-line-items violations = %v, want none -- a dry-run invoice "+
			"with no line PKs yet must pass via the rule's own !has(x.id) guard, which only "+
			"holds if MBSPayload actually OMITS the id key for these lines", res.Violations)
	}
}

// TestPayloadEngine_EmptyStringLineIDs_WouldFalselyViolate_Counterfactual
// documents WHY the omission in TestPayloadEngine_DryRunLineItems_
// NoDuplicateRulePasses_WithoutIDs matters, by constructing -- by hand,
// bypassing MBSPayload entirely -- the REJECTED alternative the mapper's
// own comment warns about: emitting "" as a real id for every dry-run
// line. Per no-duplicate-line-items's expression
// (`!has(x.id) || invoice.line_items.filter(y, has(y.id) && y.id ==
// x.id).size() <= 1`), a present (even empty) id makes has(x.id) TRUE, so
// every line with id "" collects into the SAME filter group -- and with 3
// such lines, that group has size 3 > 1 for every one of them, violating
// three times over. This is the real-rule proof behind judgment call #2:
// the mapper's `if li.ID != ""` guard is not cosmetic, it is what keeps a
// perfectly valid multi-line dry-run invoice from failing every rule run.
func TestPayloadEngine_EmptyStringLineIDs_WouldFalselyViolate_Counterfactual(t *testing.T) {
	// Hand-built payload: three lines all carrying the literal id "" --
	// what MBSPayload would emit if the `if li.ID != ""` guard were removed.
	p := map[string]any{
		"invoice": map[string]any{
			"line_items": []any{
				map[string]any{"id": "", "line_no": 1},
				map[string]any{"id": "", "line_no": 2},
				map[string]any{"id": "", "line_no": 3},
			},
		},
	}

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleNoDuplicateLineItems()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Violations) == 0 {
		t.Fatalf("no-duplicate-line-items violations = 0, want at least one -- three lines " +
			"sharing the literal id \"\" should trip the rule's duplicate-id check; if this " +
			"assertion ever fails, the counterfactual this test exists to demonstrate is no " +
			"longer true and judgment call #2's rationale needs re-checking")
	}
}

// ---------------------------------------------------------------------
// The D1 canary: without the wire round-trip (asWire), a numeric CEL guard
// SILENTLY SKIPS instead of firing -- the central "silently skipped rule
// masquerading as a pass" risk this subtask's whole [payload-numerics]
// design exists to avoid in production (03 marshals, 04 decodes with a
// plain decoder -> float64 -> CEL sees double). This test feeds
// MBSPayload's OWN in-process output straight to the engine, skipping the
// marshal/unmarshal every real-rule PAY spec performs via asWire/rooted,
// and pins the exact failure PAY-15 exists to catch in the correct
// (round-tripped) path. It is deliberately the CONTRAST case: it does not
// test payload.go's correctness (jsonNumber's json.Number output is
// exactly right), it tests that skipping the wire crossing in a future
// test (or, worse, in a future production decoder) reintroduces D1 as a
// silent pass rather than a loud failure.
// ---------------------------------------------------------------------

func TestPayloadEngine_WithoutWireRoundTrip_NegativeUnitPriceSilentlySkips(t *testing.T) {
	inv := invoice.Invoice{LineItems: []invoice.LineItem{
		{ID: uuid.NewString(), LineNo: 1, UnitPrice: ptr("-5.00")},
	}}

	// Deliberately BYPASS asWire: feed MBSPayload's direct in-process
	// output (json.Number values) straight to the engine. cel-go maps
	// json.Number to CEL's `string` (a named string type), so
	// `type(x.unit_price) != double` is TRUE and the guard short-circuits.
	unrooted := map[string]any{"invoice": invoice.MBSPayload(inv)}

	res, err := validation.NewDefaultEngine().Evaluate(unrooted, validation.RuleSet{
		Rules: []validation.Rule{ruleLineCostNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("line-cost-non-negative violations (no wire round-trip) = %v, want ZERO -- "+
			"this pins the D1 trap: without marshal/unmarshal, json.Number reads as a CEL "+
			"string and the guard silently skips even a -5.00 unit_price. If this ever starts "+
			"finding a violation, cel-go's json.Number handling changed and every real-rule "+
			"PAY spec's asWire round-trip may no longer be load-bearing the way this story "+
			"documents", res.Violations)
	}

	// Contrast: the SAME invoice, round-tripped exactly as 04 receives it
	// in production, DOES violate -- proving the wire crossing (not the
	// mapper's own output) is what makes CEL see a double. This is PAY-15's
	// assertion, reproduced here so the contrast is visible in one test.
	b, err := json.Marshal(unrooted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wired map[string]any
	if err := json.Unmarshal(b, &wired); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res2, err := validation.NewDefaultEngine().Evaluate(wired, validation.RuleSet{
		Rules: []validation.Rule{ruleLineCostNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate (wired): %v", err)
	}
	if len(res2.Violations) != 1 || res2.Violations[0].RuleKey != "line-cost-non-negative" {
		t.Errorf("violations (wired) = %v, want exactly one line-cost-non-negative violation "+
			"-- the wire round-trip is what turns json.Number into float64/double for CEL",
			res2.Violations)
	}
}
