// task-410 (BUG-05-01, Test-first: yes) -- RED specs for the rule-set v4 publish
// (v3's 19 rules + buyer-tin-required). Authored BEFORE
// migrations/<ts>_rule_set_v4.sql exists, so every test below fails at its own
// "read v4" step (pgx.ErrNoRows via loadRuleSetByVersion/ruleRowsByKey, or a direct
// rule_set_versions query), never a compile error -- see rule_set_v3_test.go's header
// for the identical convention this file follows.
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -p 1 -count=1 -run TestV4 -v ./internal/validation/...
package validation

import (
	"context"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------
// AC-1 -- v4 is the sole active+sealed version; v3 stays sealed, inactive, unmutated.
// ---------------------------------------------------------------------

// TestV4_IsTheSoleSealedActiveVersion (AC-1): v4 is_active+sealed, v3 sealed+inactive,
// exactly one active row overall.
func TestV4_IsTheSoleSealedActiveVersion(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var activeCount int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE is_active`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rule_set_versions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("count(rule_set_versions WHERE is_active) = %d, want 1 [AC-1]", activeCount)
	}

	var v4Active, v4Sealed bool
	if err := app.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE version = 4`).Scan(&v4Active, &v4Sealed); err != nil {
		t.Fatalf("read v4 is_active/sealed: %v -- expected the v4 migration to be applied "+
			"(has `make migrate-up` been run?) [AC-1]", err)
	}
	if !v4Active {
		t.Error("v4.is_active = false, want true [AC-1]")
	}
	if !v4Sealed {
		t.Error("v4.sealed = false, want true [AC-1]")
	}

	var v3Active, v3Sealed bool
	if err := app.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE version = 3`).Scan(&v3Active, &v3Sealed); err != nil {
		t.Fatalf("read v3 is_active/sealed: %v [AC-1]", err)
	}
	if v3Active {
		t.Error("v3.is_active = true, want false (v4 supersedes it) [AC-1]")
	}
	if !v3Sealed {
		t.Error("v3.sealed = false, want still true (sealing is permanent) [AC-1]")
	}
}

// TestV4_V3ContentIsUnmutated (AC-1): after the v4 migration, v3's 19 rows are still
// byte-identical to their known-correct content (v2's rows with v3TargetOverrides
// applied, the same invariant TestRuleSetV3_IsV2PlusFourTargets pins) -- the v4
// SELECT-copy must never touch its own source rows.
func TestV4_V3ContentIsUnmutated(t *testing.T) {
	_, app := dbTestPools(t)

	// Precondition: v4 must exist, or "after the v4 migration" isn't true yet.
	loadRuleSetByVersion(t, app, 4)

	v2Rows := ruleRowsByKey(t, app, 2)
	v3Rows := ruleRowsByKey(t, app, 3)
	if len(v3Rows) != 19 {
		t.Fatalf("count(rules under v3) = %d, want 19 (v3 must be unmutated by the v4 publish) [AC-1]", len(v3Rows))
	}

	for key, v2r := range v2Rows {
		v3r, ok := v3Rows[key]
		if !ok {
			t.Errorf("key %q present under v2 but missing under v3 after the v4 migration [AC-1]", key)
			continue
		}
		want := v2r
		if override, isOverridden := v3TargetOverrides[key]; isOverridden {
			want.Target = override
		}
		if !reflect.DeepEqual(v3r, want) {
			t.Errorf("v3 %s = %+v, want %+v (unmutated by the v4 publish) [AC-1]", key, v3r, want)
		}
	}
}

// ---------------------------------------------------------------------
// AC-2 -- v4 = v3's 19 rules, byte-identical, plus buyer-tin-required.
// ---------------------------------------------------------------------

// TestV4_CarriesV3PlusExactlyOneRule (AC-2): v4 has 20 rows; every v3 key carries over
// byte-identical; the one new key is buyer-tin-required.
func TestV4_CarriesV3PlusExactlyOneRule(t *testing.T) {
	_, app := dbTestPools(t)

	v3Rows := ruleRowsByKey(t, app, 3)
	v4Rows := ruleRowsByKey(t, app, 4)

	if len(v4Rows) != 20 {
		t.Fatalf("count(rules under v4) = %d, want 20 (v3's 19 + buyer-tin-required) [AC-2]", len(v4Rows))
	}

	for key, v3r := range v3Rows {
		v4r, ok := v4Rows[key]
		if !ok {
			t.Errorf("key %q present under v3 but missing under v4 [AC-2]", key)
			continue
		}
		if !reflect.DeepEqual(v4r, v3r) {
			t.Errorf("v4 %s = %+v, want %+v (byte-identical to v3, carried over) [AC-2]", key, v4r, v3r)
		}
	}

	if _, ok := v4Rows["buyer-tin-required"]; !ok {
		t.Error(`v4 is missing key "buyer-tin-required" [AC-2]`)
	}
}

// TestV4_BuyerTinRequiredRowShape (AC-2): the new row's every column matches the
// literal spec in task-410's Mechanism table (mirrors supplier-tin-required's shape).
func TestV4_BuyerTinRequiredRowShape(t *testing.T) {
	_, app := dbTestPools(t)

	v4Rows := ruleRowsByKey(t, app, 4)
	row, ok := v4Rows["buyer-tin-required"]
	if !ok {
		t.Fatalf(`v4 rules missing key "buyer-tin-required" [AC-2]`)
	}

	if row.Type != "required" {
		t.Errorf("buyer-tin-required.type = %q, want %q [AC-2]", row.Type, "required")
	}
	if row.Target != "buyer.tin" {
		t.Errorf("buyer-tin-required.target = %q, want %q [AC-2]", row.Target, "buyer.tin")
	}
	if row.Params != "{}" {
		t.Errorf("buyer-tin-required.params = %s, want {} [AC-2]", row.Params)
	}
	if row.Severity != "error" {
		t.Errorf("buyer-tin-required.severity = %q, want %q [AC-2]", row.Severity, "error")
	}
	if row.When != nil {
		t.Errorf("buyer-tin-required.when = %v, want nil [AC-2]", *row.When)
	}
	if row.Message != "Buyer TIN is required." {
		t.Errorf("buyer-tin-required.message = %q, want %q [AC-2]", row.Message, "Buyer TIN is required.")
	}
	if row.Scope != "document" {
		t.Errorf("buyer-tin-required.scope = %q, want %q [AC-2]", row.Scope, "document")
	}
	if !row.Enabled {
		t.Error("buyer-tin-required.enabled = false, want true [AC-2]")
	}
}

// ---------------------------------------------------------------------
// AC-3 -- buyer-tin-required fires on absent/null/blank, passes on present.
// ---------------------------------------------------------------------

// TestV4_BuyerTinRequiredFiresOnAbsentNullAndBlank (AC-3): four independent mutations
// of a fresh validInvoicePayload() -- whole buyer deleted, buyer.tin deleted,
// buyer.tin=nil, buyer.tin="  " -- each fire buyer-tin-required.
func TestV4_BuyerTinRequiredFiresOnAbsentNullAndBlank(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadRuleSetByVersion(t, app, 4)
	engine := NewDefaultEngine()

	cases := []struct {
		name   string
		mutate func(p Payload)
	}{
		{"buyer deleted entirely", func(p Payload) { delete(invoiceOf(p), "buyer") }},
		{"buyer.tin deleted", func(p Payload) { delete(invoiceOf(p)["buyer"].(map[string]any), "tin") }},
		{"buyer.tin = nil", func(p Payload) { invoiceOf(p)["buyer"].(map[string]any)["tin"] = nil }},
		{"buyer.tin = whitespace-only", func(p Payload) { invoiceOf(p)["buyer"].(map[string]any)["tin"] = "  " }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validInvoicePayload()
			tc.mutate(p)

			result, err := engine.Evaluate(p, rs)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if !hasViolation(result, "buyer-tin-required") {
				t.Errorf("buyer-tin-required did not fire for %s -- violations=%+v [AC-3]", tc.name, result.Violations)
			}
		})
	}
}

// TestV4_BuyerTinRequiredPassesOnPresentTin (AC-3): the unmodified
// validInvoicePayload() (buyer.tin present and non-blank) is still zero-violation
// under v4.
func TestV4_BuyerTinRequiredPassesOnPresentTin(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadRuleSetByVersion(t, app, 4)
	engine := NewDefaultEngine()

	result, err := engine.Evaluate(validInvoicePayload(), rs)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(result.Violations) != 0 {
		t.Errorf("valid payload violations under v4 = %+v, want none [AC-3]", result.Violations)
	}
}

// ---------------------------------------------------------------------
// AC-4 -- missing and malformed are different rules; each fires only its own.
// ---------------------------------------------------------------------

// TestV4_MissingAndMalformedAreDifferentRules (AC-4): a malformed-but-present
// buyer.tin fires buyer-tin-format and not buyer-tin-required; an absent buyer fires
// buyer-tin-required and not buyer-tin-format.
func TestV4_MissingAndMalformedAreDifferentRules(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadRuleSetByVersion(t, app, 4)
	engine := NewDefaultEngine()

	t.Run("malformed buyer TIN fires format, not required", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["buyer"].(map[string]any)["tin"] = "BADTIN"

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "buyer-tin-format") {
			t.Errorf("buyer-tin-format did not fire for TIN=%q -- violations=%+v [AC-4]", "BADTIN", result.Violations)
		}
		if hasViolation(result, "buyer-tin-required") {
			t.Errorf("buyer-tin-required fired for a present (if malformed) TIN, want only buyer-tin-format -- violations=%+v [AC-4]", result.Violations)
		}
	})

	t.Run("absent buyer fires required, not format", func(t *testing.T) {
		p := validInvoicePayload()
		delete(invoiceOf(p), "buyer")

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "buyer-tin-required") {
			t.Errorf("buyer-tin-required did not fire for an absent buyer -- violations=%+v [AC-4]", result.Violations)
		}
		if hasViolation(result, "buyer-tin-format") {
			t.Errorf("buyer-tin-format fired for an absent buyer, want only buyer-tin-required -- violations=%+v [AC-4]", result.Violations)
		}
	})
}
