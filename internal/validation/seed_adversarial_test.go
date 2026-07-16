// M3-05-01 QA (Mode B) -- adversarial / edge coverage added during
// verification, closing gaps the Mode-A RED suite (seed_test.go) did not
// cover. Reuses seed_test.go's shared fixtures and helpers verbatim
// (loadActive, newTestIdentity, validInvoicePayload, badInvoicePayload,
// invoiceOf, hasViolation, violationKeys) -- same package, no re-declaration.
//
// Coverage added here:
//  1. TestSeed_DemoDecomposition       -- isolates each half of the demo's two
//     violations: bad TIN alone, bad VAT alone, and both-correct (zero).
//  2. TestSeed_ToleranceBoundaryStrict -- pins the evaluator's strict `>`
//     comparison at the exact tolerance value (0.005 passes; 0.006 fires).
//  3. TestSeed_CollectAllOrdering      -- a payload tripping 3 rules at once
//     returns all 3, sorted by rule_key ascending (engine Decision N16).
//  4. TestSeed_BuyerTINPresent         -- buyer.tin PRESENT and malformed
//     fires; PRESENT and well-formed passes (the "absent -> pass" case is
//     already covered by seed_test.go's TestSeed_TINFormat).
//  5. TestSeed_CELDupVariants          -- three items with one dup pair fires
//     the rule exactly ONCE (not once per matching pair); two id-less items
//     together do not fire and do not 500; one id-full + one id-less item
//     does not spuriously fire.
//  6. TestSeed_RangeNonNumericValue    -- a non-numeric (string) subtotal
//     value fires subtotal-non-negative (present-but-bad-DATA -> violation,
//     per evaluators.go's rangeEval contract), documenting the evaluator's
//     behavior for this case.
//  7. TestSeed_KillSwitchSymmetry      -- toggling supplier-tin-format (not
//     just vat-standard-rate, per seed_test.go's TestSeed_KillSwitch) off
//     drops only that violation from the bad payload; restored + reverified
//     in the same test.
//
// NOT re-added here (already meaningfully asserted by seed_test.go, Mode A):
//   - "enum negative: currency=USD fires currency-allowed, and
//     currency-required does not" -- TestSeed_CurrencyEnum's first subtest
//     already asserts exactly this (seed_test.go:415-429).
//
// Every test that mutates shared DB state (KillSwitchSymmetry) restores it
// via t.Cleanup, mirroring seed_test.go's TestSeed_KillSwitch; all other
// tests here are read-only against the migrated v1.
package validation

import (
	"context"
	"reflect"
	"testing"
)

// countViolations returns how many times key appears in result.Violations --
// used where "fires" must mean exactly once, not merely "at least once"
// (TestSeed_CELDupVariants: a single cel rule can only ever contribute at
// most one Violation per Evaluate call, since Engine.Evaluate calls each
// applicable rule's Eval exactly once -- this test makes that guarantee an
// explicit, checked assertion rather than an implicit one).
func countViolations(result Result, key string) int {
	n := 0
	for _, v := range result.Violations {
		if v.RuleKey == key {
			n++
		}
	}
	return n
}

// TestSeed_DemoDecomposition (gap: the Mode-A demo-contract test only proves
// "both wrong -> both violations"; it never isolates which half of the bad
// payload is responsible for which violation). Confirms supplier-tin-format
// and vat-standard-rate are independently triggered -- a bad TIN alone does
// NOT also trip vat-standard-rate, and a bad VAT alone does NOT also trip
// supplier-tin-format -- and that the fully-corrected payload (both fixed)
// returns zero violations.
func TestSeed_DemoDecomposition(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("bad TIN + correct VAT -> only supplier-tin-format", func(t *testing.T) {
		p := badInvoicePayload()
		invoiceOf(p)["vat"] = 75.0 // correct the VAT half; TIN stays malformed

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		wantKeys := []string{"supplier-tin-format"}
		if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("violation keys = %v, want %v (only the TIN half should fire)", got, wantKeys)
		}
	})

	t.Run("correct TIN + bad VAT -> only vat-standard-rate", func(t *testing.T) {
		p := badInvoicePayload()
		invoiceOf(p)["supplier"].(map[string]any)["tin"] = "12345678-0001" // correct the TIN half; VAT stays wrong

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		wantKeys := []string{"vat-standard-rate"}
		if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("violation keys = %v, want %v (only the VAT half should fire)", got, wantKeys)
		}
	})

	t.Run("both corrected -> zero violations", func(t *testing.T) {
		p := badInvoicePayload()
		invoiceOf(p)["supplier"].(map[string]any)["tin"] = "12345678-0001"
		invoiceOf(p)["vat"] = 75.0

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if len(result.Violations) != 0 {
			t.Errorf("violations = %+v, want none once both halves are corrected", result.Violations)
		}
	})
}

// TestSeed_ToleranceBoundaryStrict (gap: seed_test.go's TestSeed_TaxMathTolerance
// exercises 0 and 0.01 mismatches but never the tolerance value itself --
// evaluators_math.go's taxMathEval uses `mismatch.GreaterThan(tolerance)`,
// a STRICT `>`, so a mismatch exactly equal to 0.005 must still PASS, and
// only a mismatch strictly greater than 0.005 fires). Proves the seeded
// tolerance's boundary matches the evaluator's actual comparison operator,
// not just its documented value.
func TestSeed_ToleranceBoundaryStrict(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	cases := []struct {
		name          string
		vat           float64
		wantViolation bool
	}{
		// subtotal=1000, rate=0.075 -> base*rate = 75.000 exactly.
		{"mismatch exactly 0.005 passes (strict > boundary)", 75.005, false},
		{"mismatch 0.006 fires (just past the boundary)", 75.006, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validInvoicePayload()
			invoiceOf(p)["subtotal"] = 1000.0
			invoiceOf(p)["vat"] = tc.vat

			result, err := engine.Evaluate(p, rs)
			if err != nil {
				t.Fatalf("Evaluate(vat=%.3f): %v", tc.vat, err)
			}
			if got := hasViolation(result, "vat-standard-rate"); got != tc.wantViolation {
				t.Errorf("vat-standard-rate fired = %v, want %v (vat=%.3f, mismatch=%.3f) -- violations=%+v",
					got, tc.wantViolation, tc.vat, absDiff(tc.vat, 75.0), result.Violations)
			}
		})
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestSeed_CollectAllOrdering (gap: seed_test.go's demo contract always trips
// exactly 2 rules; nothing in the RED suite exercises 3+ simultaneous
// violations). Starts from the malformed-TIN + wrong-VAT bad payload and
// forces the subtotal negative: that alone trips BOTH subtotal-non-negative
// and line-items-sum-subtotal (the still-positive line amounts no longer
// reconcile to a negative subtotal), so four independent rules fire at once.
// Asserts the engine's collect-all pass returns all four, sorted by rule_key
// ascending (Decision N16) -- not fail-fast, not in evaluation/insertion order.
func TestSeed_CollectAllOrdering(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	p := badInvoicePayload() // already wrong TIN + wrong VAT
	invoiceOf(p)["subtotal"] = -1.0

	result, err := engine.Evaluate(p, rs)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	wantKeys := []string{"line-items-sum-subtotal", "subtotal-non-negative", "supplier-tin-format", "vat-standard-rate"}
	if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("violation keys = %v, want %v (4 independent violations, sorted rule_key ascending)", got, wantKeys)
	}
}

// TestSeed_BuyerTINPresent (gap: seed_test.go's TestSeed_TINFormat only
// proves buyer-tin-format's "absent -> pass" semantics; it never exercises a
// PRESENT buyer.tin, malformed or well-formed). Confirms buyer-tin-format
// behaves identically to supplier-tin-format when the value is actually
// present: fires on a malformed TIN, passes on a well-formed one.
func TestSeed_BuyerTINPresent(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("present malformed buyer TIN fires buyer-tin-format", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["buyer"].(map[string]any)["tin"] = "BADTIN"

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "buyer-tin-format") {
			t.Errorf("buyer-tin-format did not fire for buyer.tin=%q -- violations=%+v", "BADTIN", result.Violations)
		}
	})

	t.Run("present well-formed buyer TIN passes", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs) // buyer.tin = 87654321-0002
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if hasViolation(result, "buyer-tin-format") {
			t.Errorf("buyer-tin-format fired for a well-formed present buyer TIN -- violations=%+v", result.Violations)
		}
	})
}

// TestSeed_CELDupVariants (gap: seed_test.go's TestSeed_DuplicateLineItemsCEL
// only ever tests exactly 2 line items -- it never proves the rule fires
// EXACTLY ONCE with a larger mixed set, nor exercises two id-less items
// together, nor a mixed id-full/id-less pair). Extends the dup-check
// coverage with three-and-more-item scenarios the RED suite's 2-item cases
// cannot distinguish.
func TestSeed_CELDupVariants(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("3 items, one dup pair -> fires exactly once", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"id": "1", "description": "Widget", "quantity": 10.0, "unit_price": 100.0, "line_total": 1000.0},
			map[string]any{"id": "1", "description": "Widget (dup)", "quantity": 1.0, "unit_price": 5.0, "line_total": 5.0},
			map[string]any{"id": "2", "description": "Gadget", "quantity": 1.0, "unit_price": 50.0, "line_total": 50.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(3 items, one dup pair): %v", err)
		}
		if n := countViolations(result, "no-duplicate-line-items"); n != 1 {
			t.Errorf("no-duplicate-line-items fired %d times, want exactly 1 -- violations=%+v", n, result.Violations)
		}
	})

	t.Run("two id-less items together -> no fire, no 500", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"description": "no id A", "quantity": 1.0, "unit_price": 10.0, "line_total": 10.0},
			map[string]any{"description": "no id B", "quantity": 2.0, "unit_price": 20.0, "line_total": 40.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(two id-less items): got error %v, want no error (no 500)", err)
		}
		if hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items fired for two id-less items -- both are exempt from dedup -- violations=%+v", result.Violations)
		}
	})

	t.Run("one id-full + one id-less item -> no spurious fire", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"id": "1", "description": "Widget", "quantity": 10.0, "unit_price": 100.0, "line_total": 1000.0},
			map[string]any{"description": "no id", "quantity": 1.0, "unit_price": 10.0, "line_total": 10.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(mixed id-full/id-less): got error %v, want no error", err)
		}
		if hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items fired for a mixed id-full/id-less pair with no actual duplicate id -- violations=%+v", result.Violations)
		}
	})
}

// TestSeed_RangeNonNumericValue (gap: seed_test.go's TestSeed_RangeNonNegative
// only exercises a negative NUMBER; the story's evaluator contract
// (evaluators.go rangeEval doc comment) separately states that a
// present-but-non-numeric VALUE is also a violation, not a config error --
// this pins that documented behavior against the real seeded
// subtotal-non-negative rule so a future evaluator regression that instead
// silently passed non-numeric data would be caught here).
func TestSeed_RangeNonNumericValue(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	p := validInvoicePayload()
	invoiceOf(p)["subtotal"] = "not-a-number"

	result, err := engine.Evaluate(p, rs)
	if err != nil {
		t.Fatalf("Evaluate(subtotal=non-numeric string): got error %v, want no error (a bad VALUE is a violation, not a config fault)", err)
	}
	if !hasViolation(result, "subtotal-non-negative") {
		t.Errorf("subtotal-non-negative did not fire for a non-numeric subtotal value -- violations=%+v (rangeEval must treat present-but-non-numeric DATA as a violation)", result.Violations)
	}
}

// TestSeed_KillSwitchSymmetry (gap: seed_test.go's TestSeed_KillSwitch only
// ever toggles vat-standard-rate; nothing proves the kill-switch works for
// ANY seeded rule, specifically one from a different content area
// (TIN format vs tax-math). Toggles supplier-tin-format off, confirms only
// vat-standard-rate remains on the bad payload, then restores it and
// reverifies both violations are back -- proving the switch is genuinely
// reversible, not just a one-way mutation with a best-effort cleanup.
func TestSeed_KillSwitchSymmetry(t *testing.T) {
	super, app := dbTestPools(t)

	// Restore on the ACTIVE version -- matching ToggleRule's own predicate
	// (`WHERE is_active`, store.go:137-139), which is what disabled the rule
	// below. The same live-data hazard as TestSeed_KillSwitch's cleanup
	// (RS-V2-11): `v.version = 1` would silently leave supplier-tin-format
	// DISABLED on the live active rule-set once the active version is not
	// literally 1.
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rules r SET enabled = true
			   FROM rule_set_versions v
			  WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = 'supplier-tin-format'`,
		); err != nil {
			t.Errorf("cleanup: restore supplier-tin-format enabled=true: %v", err)
		}
	})

	store := NewStore(app)
	if _, err := store.ToggleRule(newTestIdentity(), "supplier-tin-format", false); err != nil {
		t.Fatalf("ToggleRule(supplier-tin-format, false): %v", err)
	}

	engine := NewDefaultEngine()

	rsDisabled := loadActive(t, app)
	result, err := engine.Evaluate(badInvoicePayload(), rsDisabled)
	if err != nil {
		t.Fatalf("Evaluate(bad payload) after disabling supplier-tin-format: %v", err)
	}
	wantKeys := []string{"vat-standard-rate"}
	if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("violations after disabling supplier-tin-format = %v, want %v (only vat-standard-rate remains)", got, wantKeys)
	}

	// Restore explicitly (in addition to the Cleanup above) and reverify both
	// violations return -- proves the switch is symmetric, not one-way.
	if _, err := store.ToggleRule(newTestIdentity(), "supplier-tin-format", true); err != nil {
		t.Fatalf("ToggleRule(supplier-tin-format, true) restore: %v", err)
	}
	rsRestored := loadActive(t, app)
	result, err = engine.Evaluate(badInvoicePayload(), rsRestored)
	if err != nil {
		t.Fatalf("Evaluate(bad payload) after restoring supplier-tin-format: %v", err)
	}
	wantKeys = []string{"supplier-tin-format", "vat-standard-rate"}
	if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("violations after restoring supplier-tin-format = %v, want %v (both back)", got, wantKeys)
	}
}
