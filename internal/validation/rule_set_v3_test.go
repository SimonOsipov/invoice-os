// task-289 / INVCR-01-13 (D8, Test-first: yes) -- RED specs for the rule-set v3 publish.
// Transcribes task-289's AC-1..4/9 Test Specs table into runnable Go tests, authored
// BEFORE migrations/20260731090000_rule_set_v3.sql exists (no version=3 row, so every
// test below fails at its own "read v3" step with pgx.ErrNoRows, never a compile error).
//
// See task-289 (mcp__backlog__task_view id=task-289) for the authoritative
// Description/Acceptance Criteria/Test Specs/Decisions this file transcribes, and
// rule_set_v2_test.go / schema_test.go for the harness conventions (dbTestPools,
// seedVersion, sealAndActivate, assertSQLState) this file reuses.
//
// AC-3's "TestRuleSetV3_ReversibilityRoundTrip" row is NOT re-authored as a Go test
// here, for the identical reason rule_set_v2_test.go's header gives for RS-V2-08: it is
// already generically covered by the existing `migrations` CI job
// (.github/workflows/ci.yml, `make migrate-reset` then `make migrate-up`), which
// re-validates on every migration including this one -- duplicating it as a
// package-level Go test would mean tearing down and rebuilding the ENTIRE migration
// history from inside a unit test, corrupting the shared dev DB every other test in this
// package depends on. TestRuleSetV3_DownRestoresV2Active below is this file's narrower,
// same-package sanity check on the Down's own mechanics (mirrors
// TestRuleSetV2_DownRestoresV1).
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestRuleSetV3_|TestRuleSetV2_StillHasNineteenRules|TestV3' -v ./internal/validation/...
package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v3TargetOverrides is the D8 publish's whole content delta: exactly these 4 rule keys
// carry a new Rule.Target under v3; every other rule (and every other column, including
// on these 4) is byte-identical to v2's row -- see migrations/20260731090000_rule_set_v3.sql.
var v3TargetOverrides = map[string]string{
	"vat-standard-rate":       "vat",
	"line-items-sum-subtotal": "subtotal",
	"line-cost-non-negative":  "line_items",
	"no-duplicate-line-items": "line_items",
}

// ruleRow is one rules row's content columns, keyed by `key` by ruleRowsByKey below.
type ruleRow struct {
	Type   string
	Target string
	Params string // raw JSON text -- compared as text, since both sides always come
	// from the same jsonb column shape (byte-identity, not semantic JSON equality, is
	// exactly what [v2-copy-not-redeclare] promises).
	Severity string
	When     *string
	Message  string
	Scope    string
	Enabled  bool
}

// ruleRowsByKey reads every rule under the rule_set_versions row with the given
// `version` number, keyed by `key` -- the shared fixture TestRuleSetV3_IsV2PlusFourTargets
// and TestRuleSetV2_StillHasNineteenRules both build their comparisons over.
func ruleRowsByKey(t *testing.T, app *pgxpool.Pool, version int) map[string]ruleRow {
	t.Helper()
	ctx := context.Background()

	rows, err := app.Query(ctx,
		`SELECT r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, r.enabled
		   FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.version = $1`, version)
	if err != nil {
		t.Fatalf("query rules under version=%d: %v", version, err)
	}
	defer rows.Close()

	out := map[string]ruleRow{}
	for rows.Next() {
		var key string
		var rr ruleRow
		var params []byte
		if err := rows.Scan(&key, &rr.Type, &rr.Target, &params, &rr.Severity, &rr.When, &rr.Message, &rr.Scope, &rr.Enabled); err != nil {
			t.Fatalf("scan rule row under version=%d: %v", version, err)
		}
		rr.Params = string(params)
		out[key] = rr
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rules under version=%d: %v", version, err)
	}
	if len(out) == 0 {
		t.Fatalf("count(rules under version=%d) = 0 -- expected the migration-seeded version to carry rules "+
			"(has `make migrate-up` been run?)", version)
	}
	return out
}

// loadRuleSetByVersion loads a RuleSet by its published version number directly (raw
// SQL, mirroring store.go's loadActiveRuleSetTx shape) rather than via Store's
// active-only LoadActiveRuleSet -- both v2 and v3 are sealed, so their content is stable
// regardless of which one is currently active. This lets TestV3PathIsTheOnlyDelta
// evaluate the SAME golden corpus under both without needing to flip which one is
// active. Test-only: store.go's production surface is untouched.
func loadRuleSetByVersion(t *testing.T, app *pgxpool.Pool, version int) RuleSet {
	t.Helper()
	ctx := context.Background()

	var versionID string
	if err := app.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE version = $1`, version).Scan(&versionID); err != nil {
		t.Fatalf("read rule_set_versions.id WHERE version=%d: %v -- expected the migration-seeded version "+
			"(has `make migrate-up` been run?)", version, err)
	}

	rows, err := app.Query(ctx,
		`SELECT key, type, target, params, severity, "when", message, scope, enabled
		   FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, versionID)
	if err != nil {
		t.Fatalf("query rules under version=%d: %v", version, err)
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Key, &r.Type, &r.Target, &r.Params, &r.Severity, &r.When, &r.Message, &r.Scope, &r.Enabled); err != nil {
			t.Fatalf("scan rule row under version=%d: %v", version, err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rules under version=%d: %v", version, err)
	}
	if len(rules) == 0 {
		t.Fatalf("count(rules under version=%d) = 0, want > 0", version)
	}
	return RuleSet{ID: versionID, Version: version, Rules: rules}
}

// ---------------------------------------------------------------------
// AC-1 -- v3 = v2's 19 rules, target overridden on exactly the 4 D8 keys.
// ---------------------------------------------------------------------

// TestRuleSetV3_IsV2PlusFourTargets (AC-1): v3 carries the same 19 rule keys as v2; the
// 4 D8 keys carry their new target (vat/subtotal/line_items/line_items); every other
// column, on every key, is byte-identical to v2's row -- proving the copy is a real
// SELECT-copy ([v2-copy-not-redeclare]), not a hand-retyped re-declaration that could
// silently drift.
func TestRuleSetV3_IsV2PlusFourTargets(t *testing.T) {
	_, app := dbTestPools(t)

	v2Rows := ruleRowsByKey(t, app, 2)
	v3Rows := ruleRowsByKey(t, app, 3)

	if len(v3Rows) != 19 {
		t.Fatalf("count(rules under v3) = %d, want 19 [AC-1]", len(v3Rows))
	}
	if len(v2Rows) != 19 {
		t.Fatalf("count(rules under v2) = %d, want 19 (v2 must be unmutated) [AC-1 precondition]", len(v2Rows))
	}

	for key, v2r := range v2Rows {
		v3r, ok := v3Rows[key]
		if !ok {
			t.Errorf("key %q present under v2 but missing under v3 [AC-1]", key)
			continue
		}

		wantTarget := v2r.Target
		if override, isOverridden := v3TargetOverrides[key]; isOverridden {
			wantTarget = override
		}
		if v3r.Target != wantTarget {
			t.Errorf("%s: v3.target = %q, want %q [AC-1]", key, v3r.Target, wantTarget)
		}

		// Every OTHER column must be byte-identical to v2's -- SELECT-copy, never
		// re-declared. enabled is forced true by BOTH v2's and v3's own publish
		// ([v2-ships-as-authored]), so v2r.Enabled == v3r.Enabled here is still a
		// byte-identity check, not a coincidence -- both should read true.
		if v3r.Type != v2r.Type {
			t.Errorf("%s: v3.type = %q, want %q (byte-identical to v2) [AC-1]", key, v3r.Type, v2r.Type)
		}
		if v3r.Params != v2r.Params {
			t.Errorf("%s: v3.params = %s, want %s (byte-identical to v2) [AC-1]", key, v3r.Params, v2r.Params)
		}
		if v3r.Severity != v2r.Severity {
			t.Errorf("%s: v3.severity = %q, want %q (byte-identical to v2) [AC-1]", key, v3r.Severity, v2r.Severity)
		}
		if !strPtrEqual(v3r.When, v2r.When) {
			t.Errorf("%s: v3.when = %v, want %v (byte-identical to v2) [AC-1]", key, v3r.When, v2r.When)
		}
		if v3r.Message != v2r.Message {
			t.Errorf("%s: v3.message = %q, want %q (byte-identical to v2) [AC-1]", key, v3r.Message, v2r.Message)
		}
		if v3r.Scope != v2r.Scope {
			t.Errorf("%s: v3.scope = %q, want %q (byte-identical to v2) [AC-1]", key, v3r.Scope, v2r.Scope)
		}
		if !v3r.Enabled {
			t.Errorf("%s: v3.enabled = false, want true [AC-1, v2-ships-as-authored]", key)
		}
	}

	for key := range v3TargetOverrides {
		if v3Rows[key].Target == "" {
			t.Errorf("%s: v3.target is still blank, want a concrete field [AC-1]", key)
		}
	}
}

// ---------------------------------------------------------------------
// AC-2 -- exactly one version is active, and both v2/v3 are sealed.
// ---------------------------------------------------------------------

// TestRuleSetV3_ActiveAndSealed (AC-2; the "and it's v3" half retired below): exactly one
// rule_set_versions row is active, and v3/v2 are both sealed -- sealing is permanent.
//
// AC-2's ORIGINAL claim ("the active row is v3") held only until BUG-05 published v4 and
// superseded it. Which version is active is a moving target every publish changes, and is
// asserted going forward by rule_set_v4_test.go's TestV4_IsTheSoleSealedActiveVersion, not
// re-litigated here -- so v3 is resolved by its permanent version number instead.
func TestRuleSetV3_ActiveAndSealed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var activeCount int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE is_active`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rule_set_versions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("count(rule_set_versions WHERE is_active) = %d, want 1 [AC-2]", activeCount)
	}

	var v3Sealed bool
	if err := app.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE version = 3`).Scan(&v3Sealed); err != nil {
		t.Fatalf("read v3 sealed: %v -- expected the v3 migration to be applied "+
			"(has `make migrate-up` been run?) [AC-2]", err)
	}
	if !v3Sealed {
		t.Error("v3.sealed = false, want true [AC-2]")
	}

	var v2Active, v2Sealed bool
	if err := app.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE version = 2`).Scan(&v2Active, &v2Sealed); err != nil {
		t.Fatalf("read v2 is_active/sealed: %v", err)
	}
	if v2Active {
		t.Error("v2.is_active = true, want false (v3 supersedes it) [AC-2]")
	}
	if !v2Sealed {
		t.Error("v2.sealed = false, want still true (sealing is permanent) [AC-2]")
	}
}

// ---------------------------------------------------------------------
// AC-4 -- v2 retains all 19 of its rules, unmutated, target values unchanged.
// ---------------------------------------------------------------------

// TestRuleSetV2_StillHasNineteenRules (AC-4): v3 is purely additive -- v2 must still
// carry exactly 19 rules, and its OWN target values (blank on the same 4 keys v3
// overrides -- v2 is sealed, so only a new version can ever carry a different target)
// must be exactly what 20260716185106_rule_set_v2.sql published, forever.
func TestRuleSetV2_StillHasNineteenRules(t *testing.T) {
	_, app := dbTestPools(t)

	v2Rows := ruleRowsByKey(t, app, 2)
	if len(v2Rows) != 19 {
		t.Fatalf("count(rules under v2) = %d, want 19 -- v3 must be additive; v2 must not be mutated [AC-4]", len(v2Rows))
	}

	for key, row := range v2Rows {
		_, wantBlank := v3TargetOverrides[key]
		switch {
		case wantBlank && row.Target != "":
			t.Errorf("v2 %s.target = %q, want \"\" (v2 must be unmutated by the v3 publish) [AC-4]", key, row.Target)
		case !wantBlank && row.Target == "":
			t.Errorf("v2 %s.target is blank, want a concrete path (unexpected -- not one of the 4 D8 keys) [AC-4]", key)
		}
	}
}

// ---------------------------------------------------------------------
// AC-3 -- the Down restores v2 as active and re-enables both guard triggers.
// ---------------------------------------------------------------------

// TestRuleSetV3_DownRestoresV2Active (AC-3): mirrors TestRuleSetV2_DownRestoresV1's
// pattern -- runs the v3 migration's Down (migrations/20260731090000_rule_set_v3.sql)
// inside a superuser tx that is ALWAYS rolled back, so it never permanently mutates the
// shared DB other tests in this package depend on. Guards against a vacuous pass by
// asserting v3 really is active+sealed before attempting the Down.
func TestRuleSetV3_DownRestoresV2Active(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// This test simulates the v3 migration's OWN Down, whose precondition is "v3 active"
	// -- true only until BUG-05 published v4 and superseded it. Establish that
	// precondition inside this always-rolled-back tx: clear whichever version is really
	// active, then activate v3. The seal guard permits an is_active flip on a sealed row.
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE is_active`); err != nil {
		t.Fatalf("clear the active slot (simulated AC-3 precondition): %v", err)
	}
	// Rowcount-checked: a missing v3 row would otherwise let the whole Down sequence
	// below pass trivially.
	tag, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 3`)
	if err != nil {
		t.Fatalf("activate v3 (simulated AC-3 precondition): %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("activating v3 touched %d rows, want 1 [AC-3 precondition]", tag.RowsAffected())
	}

	var v3Active, v3Sealed bool
	if err := tx.QueryRow(ctx, `SELECT is_active, sealed FROM rule_set_versions WHERE version = 3`).Scan(&v3Active, &v3Sealed); err != nil {
		t.Fatalf("read v3 is_active/sealed: %v -- expected the v3 migration's active+sealed row, got none "+
			"(has the v3 migration been applied via `make migrate-up`?) [AC-3 precondition]", err)
	}
	if !v3Active || !v3Sealed {
		t.Fatalf("v3 is_active=%t sealed=%t before running the simulated Down, want both true [AC-3 precondition]", v3Active, v3Sealed)
	}

	// db/seed.dev.sql seeds demo invoices that STAMP the active version via
	// rule_set_version_id, whose FK carries no ON DELETE clause (NO ACTION) --
	// [v2-down-is-dev-irreversible], carried over for v3. Clearing them restores the
	// premise this simulated Down needs; harmless, since the enclosing tx is always
	// rolled back (mirrors TestRuleSetV2_DownRestoresV1 verbatim, delete order included:
	// approval_runs -> app_exchange -> submission_jobs -> invoices, all ON DELETE RESTRICT).
	for _, stmt := range []string{
		`DELETE FROM approval_runs WHERE invoice_id IN (SELECT id FROM invoices WHERE rule_set_version_id IS NOT NULL)`,
		`DELETE FROM app_exchange WHERE invoice_id IN (SELECT id FROM invoices WHERE rule_set_version_id IS NOT NULL)`,
		`DELETE FROM submission_jobs WHERE invoice_id IN (SELECT id FROM invoices WHERE rule_set_version_id IS NOT NULL)`,
		`DELETE FROM invoices WHERE rule_set_version_id IS NOT NULL`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("clear rule-set-stamped seed invoices before the simulated Down: %v", err)
		}
	}

	// The migration's own Down, reproduced verbatim (migrations/20260731090000_rule_set_v3.sql).
	if _, err := tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER rules_content_lock`); err != nil {
		t.Fatalf("Down step: disable rules_content_lock: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER rule_set_versions_seal_guard`); err != nil {
		t.Fatalf("Down step: disable rule_set_versions_seal_guard: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = false, sealed = false WHERE version = 3`); err != nil {
		t.Fatalf("Down step: unseal+deactivate v3: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 2`); err != nil {
		t.Fatalf("Down step: reactivate v2: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE version = 3`); err != nil {
		t.Fatalf("Down step: delete v3: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions ENABLE TRIGGER rule_set_versions_seal_guard`); err != nil {
		t.Fatalf("Down step: re-enable rule_set_versions_seal_guard: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rules ENABLE TRIGGER rules_content_lock`); err != nil {
		t.Fatalf("Down step: re-enable rules_content_lock: %v", err)
	}

	var v3Exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rule_set_versions WHERE version = 3)`).Scan(&v3Exists); err != nil {
		t.Fatalf("check v3 existence after Down: %v", err)
	}
	if v3Exists {
		t.Error("rule_set_versions WHERE version=3 still exists after Down, want absent [AC-3]")
	}

	var v2Active bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE version = 2`).Scan(&v2Active); err != nil {
		t.Fatalf("read v2.is_active after Down: %v", err)
	}
	if !v2Active {
		t.Error("v2.is_active after Down = false, want true [AC-3]")
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
			t.Errorf("pg_trigger.tgenabled for %s on %s = %q, want %q (re-enabled) [AC-3]", tc.trigger, tc.table, enabled, "O")
		}
	}
}

// ---------------------------------------------------------------------
// AC-7's deployed-suite hazard -- settled by reasoning + this pinned test, since a live
// e2e run against a v3-active deployment is not available pre-merge (this branch's
// migration is not deployed; see this task's PR/story notes for the deferral to task-292).
// ---------------------------------------------------------------------

// TestV3ViolationOrderStableUnderPathSort (AC-7 hazard): topology/invoice-surfaces.spec.ts
// reads the rule-set version off the Compliance card's `compliance-ruleset-version` chip,
// which is per-invoice, so row order cannot change what that assertion observes. BUG-13-01
// retired the per-row Rule-set version column this paragraph used to describe; the spec no
// longer reads a `<td>` by ordinal for the version at all.
//
// More fundamentally, the engine's rule_key-then-path sort (Decision N16, engine.go)
// can only ever reorder two violations that SHARE a rule_key -- and rules.key carries
// UNIQUE(rule_set_version_id, key) (20260711051711_rule_set_versions.sql:31), so a
// single Engine.Evaluate call can never produce two violations with the same RuleKey in
// the first place (engine.go's loop appends at most one Violation per rs.Rules entry).
// The path leg of the sort is therefore DEAD for any one invoice's result -- under v2,
// v3, or any future version alike -- so filling Path on the 4 D8 keys cannot reorder
// rows AT ALL, let alone change which one is first.
//
// This test pins the structural precondition against the real v3-active corpus: no two
// violations in a single Result ever share a RuleKey, for the same fixture payloads
// TestV3PathIsTheOnlyDelta already proves index-for-index identical between v2 and v3.
func TestV3ViolationOrderStableUnderPathSort(t *testing.T) {
	_, app := dbTestPools(t)
	engine := NewDefaultEngine()
	rsV3 := loadRuleSetByVersion(t, app, 3)

	cases := []struct {
		name    string
		payload func() Payload
	}{
		{"demo_bad_invoice", badInvoicePayload},
		{"many_violations", manyViolationsPayload},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Evaluate(tc.payload(), rsV3)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(result.Violations) == 0 {
				t.Fatalf("0 violations for %s, want > 0 -- nothing to prove ordering-stability over", tc.name)
			}
			seen := make(map[string]bool, len(result.Violations))
			for _, v := range result.Violations {
				if seen[v.RuleKey] {
					t.Fatalf("RuleKey %q appears twice in one Result -- this is the ONE precondition the "+
						"path-sort tiebreak would need for reordering to matter, and it must never hold "+
						"(rules.key is UNIQUE per rule_set_version_id) [AC-7]", v.RuleKey)
				}
				seen[v.RuleKey] = true
			}
		})
	}
}

// AC-9's TestV3PathIsTheOnlyDelta lives in golden_test.go (per task-289's own Test Specs
// table), evaluating the SAME golden corpus payloads this file's other tests reference --
// see that file for the test itself; it reuses v3TargetOverrides and loadRuleSetByVersion
// defined above (this is one package -- helpers are shared across files by design, not
// duplicated).
