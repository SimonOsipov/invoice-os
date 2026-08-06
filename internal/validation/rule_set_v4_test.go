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

// ---------------------------------------------------------------------
// AC-3 adversarial -- blank detection at edges strings.TrimSpace does and doesn't cover.
// ---------------------------------------------------------------------

// TestV4_BuyerTinRequiredBlankEdgeCases (AC-3 adversarial): tab/newline-only and a
// non-breaking-space-only TIN are blank per strings.TrimSpace (unicode.White_Space) and
// fire buyer-tin-required, same as the plain-space case AC-3 already pins.
func TestV4_BuyerTinRequiredBlankEdgeCases(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadRuleSetByVersion(t, app, 4)
	engine := NewDefaultEngine()

	cases := []struct {
		name string
		tin  string
	}{
		{"tab and newline only", "\t\n"},
		{"non-breaking space only", "\u00A0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validInvoicePayload()
			invoiceOf(p)["buyer"].(map[string]any)["tin"] = tc.tin

			result, err := engine.Evaluate(p, rs)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if !hasViolation(result, "buyer-tin-required") {
				t.Errorf("buyer-tin-required did not fire for TIN=%q -- violations=%+v [AC-3]", tc.tin, result.Violations)
			}
		})
	}
}

// TestV4_BuyerTinRequiredZeroWidthSpaceGap (AC-3 adversarial, KNOWN GAP): U+200B is not
// unicode.White_Space, so strings.TrimSpace does not blank it -- requiredEval treats it
// as present and buyer-tin-required does not fire. buyer-tin-format's regex still
// rejects it, so the invoice is still blocked overall, just mislabeled "malformed"
// rather than "missing". Pins the actual behavior rather than the assumed one.
func TestV4_BuyerTinRequiredZeroWidthSpaceGap(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadRuleSetByVersion(t, app, 4)
	engine := NewDefaultEngine()

	p := validInvoicePayload()
	invoiceOf(p)["buyer"].(map[string]any)["tin"] = "\u200B"

	result, err := engine.Evaluate(p, rs)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if hasViolation(result, "buyer-tin-required") {
		t.Error("buyer-tin-required fired for a zero-width-space TIN -- TrimSpace behavior changed, update this pin [AC-3 known gap]")
	}
	if !hasViolation(result, "buyer-tin-format") {
		t.Error("buyer-tin-format did not fire for a zero-width-space TIN -- the invoice would pass validation entirely [AC-3 known gap]")
	}
}

// ---------------------------------------------------------------------
// AC-2 adversarial -- the SELECT-copy forces enabled=true, never inherits v3's runtime state.
// ---------------------------------------------------------------------

// TestV4_CopyForcesEnabledTrueRegardlessOfSourceState (AC-2 adversarial): the migration's
// copy INSERT literal-inserts `true` for enabled rather than reading r.enabled. Disables a
// real v3 rule's kill-switch (enabled-only UPDATE is allowed on a sealed row, TestRIL03),
// then re-runs the migration's own copy INSERT verbatim against a throwaway draft version
// -- the copied row must still land enabled=true. Always-rolled-back superuser tx.
func TestV4_CopyForcesEnabledTrueRegardlessOfSourceState(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE rules SET enabled = false
		WHERE rule_set_version_id = (SELECT id FROM rule_set_versions WHERE version = 3)
		  AND key = 'supplier-tin-required'`)
	if err != nil {
		t.Fatalf("disable v3's supplier-tin-required (simulated runtime kill-switch): %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("disabling v3 supplier-tin-required touched %d rows, want 1 [precondition]", tag.RowsAffected())
	}

	draft := nextVersion()
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_set_versions (version, is_active, sealed, notes) VALUES ($1, false, false, $2)`,
		draft, fixtureNotes,
	); err != nil {
		t.Fatalf("insert throwaway draft version: %v", err)
	}
	// The migration's own copy INSERT, reproduced verbatim, retargeted from v4 to this
	// throwaway draft (see migrations/20260806131239_rule_set_v4.sql).
	if _, err := tx.Exec(ctx, `
		INSERT INTO rules
		    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
		SELECT nv.id, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, true
		FROM rules r
		JOIN rule_set_versions v3 ON v3.id = r.rule_set_version_id AND v3.version = 3
		CROSS JOIN rule_set_versions nv
		WHERE nv.version = $1`, draft); err != nil {
		t.Fatalf("run the migration's copy INSERT against the draft: %v", err)
	}

	var copiedEnabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM rules
		WHERE rule_set_version_id = (SELECT id FROM rule_set_versions WHERE version = $1)
		  AND key = 'supplier-tin-required'`, draft).Scan(&copiedEnabled); err != nil {
		t.Fatalf("read copied supplier-tin-required.enabled: %v", err)
	}
	if !copiedEnabled {
		t.Error("copied supplier-tin-required.enabled = false, want true (forced, not inherited from a disabled v3 source) [AC-2]")
	}
}

// ---------------------------------------------------------------------
// AC-6 -- the Down round-trips: restores v3 active, removes v4, re-enables both guards.
// ---------------------------------------------------------------------

// TestV4_DownRestoresV3Active (AC-6): mirrors TestRuleSetV3_DownRestoresV2Active's
// pattern -- runs the v4 migration's Down inside a superuser tx that is ALWAYS rolled
// back. v4 is the real active version right now, so no synthetic activation is needed;
// the next publish that supersedes v4 should retrofit this test the same way the house
// convention retrofit rule_set_v3_test.go.
func TestV4_DownRestoresV3Active(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var v4Active, v4Sealed bool
	if err := tx.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE version = 4`).Scan(&v4Active, &v4Sealed); err != nil {
		t.Fatalf("read v4 is_active/sealed: %v -- expected the v4 migration's active+sealed row [AC-6 precondition]", err)
	}
	if !v4Active || !v4Sealed {
		t.Fatalf("v4 is_active=%t sealed=%t before running the simulated Down, want both true [AC-6 precondition]", v4Active, v4Sealed)
	}

	// db/seed.dev.sql seeds demo invoices that stamp the active version via
	// rule_set_version_id, whose FK carries no ON DELETE clause -- clear them so the
	// Down's DELETE below doesn't 23503 (harmless: this tx is always rolled back).
	for _, stmt := range []string{
		`DELETE FROM app_exchange WHERE invoice_id IN (SELECT id FROM invoices WHERE rule_set_version_id IS NOT NULL)`,
		`DELETE FROM submission_jobs WHERE invoice_id IN (SELECT id FROM invoices WHERE rule_set_version_id IS NOT NULL)`,
		`DELETE FROM invoices WHERE rule_set_version_id IS NOT NULL`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("clear rule-set-stamped seed invoices before the simulated Down: %v", err)
		}
	}

	// The migration's own Down, reproduced verbatim (migrations/20260806131239_rule_set_v4.sql).
	if _, err := tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER rules_content_lock`); err != nil {
		t.Fatalf("Down step: disable rules_content_lock: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER rule_set_versions_seal_guard`); err != nil {
		t.Fatalf("Down step: disable rule_set_versions_seal_guard: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false, sealed = false WHERE version = 4`); err != nil {
		t.Fatalf("Down step: unseal+deactivate v4: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 3`); err != nil {
		t.Fatalf("Down step: reactivate v3: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE version = 4`); err != nil {
		t.Fatalf("Down step: delete v4: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions ENABLE TRIGGER rule_set_versions_seal_guard`); err != nil {
		t.Fatalf("Down step: re-enable rule_set_versions_seal_guard: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rules ENABLE TRIGGER rules_content_lock`); err != nil {
		t.Fatalf("Down step: re-enable rules_content_lock: %v", err)
	}

	var v4Exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rule_set_versions WHERE version = 4)`).Scan(&v4Exists); err != nil {
		t.Fatalf("check v4 existence after Down: %v", err)
	}
	if v4Exists {
		t.Error("rule_set_versions WHERE version=4 still exists after Down, want absent [AC-6]")
	}

	var v3Active bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE version = 3`).Scan(&v3Active); err != nil {
		t.Fatalf("read v3.is_active after Down: %v", err)
	}
	if !v3Active {
		t.Error("v3.is_active after Down = false, want true [AC-6]")
	}

	for _, tc := range []struct{ table, trigger string }{
		{"rules", "rules_content_lock"},
		{"rule_set_versions", "rule_set_versions_seal_guard"},
	} {
		var enabled string
		if err := tx.QueryRow(ctx,
			`SELECT tgenabled FROM pg_trigger WHERE tgname = $1 AND tgrelid = $2::regclass`,
			tc.trigger, tc.table,
		).Scan(&enabled); err != nil {
			t.Fatalf("read pg_trigger.tgenabled for %s on %s: %v", tc.trigger, tc.table, err)
		}
		if enabled != "O" {
			t.Errorf("pg_trigger.tgenabled for %s on %s = %q, want %q (re-enabled) [AC-6]", tc.trigger, tc.table, enabled, "O")
		}
	}
}
