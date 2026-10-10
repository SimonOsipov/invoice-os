// set_rule_enabled behaviour against the seeded rule sets. `rules` is global, so each test
// registers restoreRulesOnCleanup before its first write. No t.Parallel().
package validation

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const functionKey = "vat-standard-rate"

// seedRulesStaffRow inserts a rules-role staff row and removes it in t.Cleanup.
func seedRulesStaffRow(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	actor := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO staff_members (user_id, rules_role) VALUES ($1, true)`, actor); err != nil {
		t.Fatalf("seed a rules-role staff row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM staff_members WHERE user_id = $1`, actor)
	})
	return actor
}

// switchRuleViaFunction calls set_rule_enabled as invoice_app and returns was_enabled.
func switchRuleViaFunction(t *testing.T, app *pgxpool.Pool, actor, key string, enabled bool) bool {
	t.Helper()
	var was bool
	if err := app.QueryRow(context.Background(),
		`SELECT was_enabled FROM set_rule_enabled($1::uuid, $2, $3)`, actor, key, enabled).Scan(&was); err != nil {
		t.Fatalf("set_rule_enabled(%s, %t) as invoice_app: %v (is the migration applied?)", key, enabled, err)
	}
	return was
}

// AC2: a fresh load sees the switch, with no redeploy.
func TestStaffRulesFunction_NextLoadSeesTheSwitch(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	engine := NewDefaultEngine()

	firePayload := validInvoicePayload()
	invoiceOf(firePayload)["vat"] = 70.0
	result, err := engine.Evaluate(firePayload, loadActive(t, app))
	if err != nil {
		t.Fatalf("Evaluate baseline: %v", err)
	}
	if !hasViolation(result, functionKey) {
		t.Fatalf("baseline: %s did not fire -- violations=%+v", functionKey, result.Violations)
	}

	if was := switchRuleViaFunction(t, app, actor, functionKey, false); !was {
		t.Error("disable returned was_enabled = false, want true")
	}
	rs := loadActive(t, app)
	result, err = engine.Evaluate(firePayload, rs)
	if err != nil {
		t.Fatalf("Evaluate after the switch: %v", err)
	}
	if hasViolation(result, functionKey) {
		t.Errorf("%s still fires after the switch -- violations=%+v", functionKey, result.Violations)
	}
	control, err := engine.Evaluate(badInvoicePayload(), rs)
	if err != nil {
		t.Fatalf("Evaluate control: %v", err)
	}
	if !hasViolation(control, "supplier-tin-format") {
		t.Errorf("control rule supplier-tin-format did not fire -- only the switched rule should drop")
	}

	switchRuleViaFunction(t, app, actor, functionKey, true)
	result, err = engine.Evaluate(firePayload, loadActive(t, app))
	if err != nil {
		t.Fatalf("Evaluate after the restore: %v", err)
	}
	if !hasViolation(result, functionKey) {
		t.Errorf("%s did not fire again after the restore", functionKey)
	}
}

// AC3: only the version in force changes.
func TestStaffRulesFunction_LeavesAScheduledAndASupersededVersionAlone(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	superseded := ruleRowsOfVersions(t, super, functionKey, 1, 2, 3)
	if len(superseded) == 0 {
		t.Fatalf("no superseded version carries %s: the test cannot discriminate", functionKey)
	}
	versionID, _ := seedVersion(t, super)
	scheduled := seedRule(t, super, versionID, functionKey)
	sealAndDate(t, super, versionID, "3001-01-01")
	if !ruleEnabledByID(t, super, scheduled) || !ruleEnabledActive(t, super, functionKey) {
		t.Fatal("a fixture starts disabled: the test cannot discriminate")
	}

	if was := switchRuleViaFunction(t, app, actor, functionKey, false); !was {
		t.Error("disable returned was_enabled = false, want true")
	}
	if ruleEnabledActive(t, super, functionKey) {
		t.Error("the version in force still has the rule enabled")
	}
	if !ruleEnabledByID(t, super, scheduled) {
		t.Error("the scheduled version's rule was disabled, want it untouched")
	}
	for id, want := range superseded {
		if got := ruleEnabledByID(t, super, id); got != want {
			t.Errorf("superseded row %s enabled = %t, want %t unchanged", id, got, want)
		}
	}
}

// AC4: the flip passes the content lock on a sealed version and touches no other column.
func TestStaffRulesFunction_FlipsASealedVersionAndTouchesNoContent(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	ctx := context.Background()

	var sealed bool
	var ruleID, ruleBefore, versionBefore string
	if err := super.QueryRow(ctx, `
		SELECT v.sealed, r.id::text, (to_jsonb(r) - 'enabled')::text, to_jsonb(v)::text
		  FROM rule_set_versions v JOIN rules r ON r.rule_set_version_id = v.id AND r.key = $1
		 WHERE v.id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`, functionKey,
	).Scan(&sealed, &ruleID, &ruleBefore, &versionBefore); err != nil {
		t.Fatalf("read the rule in force: %v", err)
	}
	if !sealed {
		t.Fatal("the version in force is not sealed: the test cannot exercise the content lock")
	}

	switchRuleViaFunction(t, app, actor, functionKey, false)

	var ruleAfter, versionAfter string
	var enabled bool
	if err := super.QueryRow(ctx, `
		SELECT (to_jsonb(r) - 'enabled')::text, r.enabled, to_jsonb(v)::text
		  FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE r.id = $1`, ruleID,
	).Scan(&ruleAfter, &enabled, &versionAfter); err != nil {
		t.Fatalf("re-read the rule: %v", err)
	}
	if enabled {
		t.Error("enabled = true after the disable, want false")
	}
	if ruleAfter != ruleBefore {
		t.Errorf("a content column of the rule changed:\n before %s\n after  %s", ruleBefore, ruleAfter)
	}
	if versionAfter != versionBefore {
		t.Errorf("the version row changed:\n before %s\n after  %s", versionBefore, versionAfter)
	}
}

// AC3/AC7: a key that exists only in a scheduled version is unknown to the function.
func TestStaffRulesFunction_KeyOnlyInAScheduledVersionReturnsZeroRows(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	versionID, _ := seedVersion(t, super)
	const scheduledOnly = "scheduled-only-qa-rule"
	scheduled := seedRule(t, super, versionID, scheduledOnly)
	sealAndDate(t, super, versionID, "3001-01-01")
	if !ruleEnabledByID(t, super, scheduled) {
		t.Fatal("the scheduled fixture starts disabled: the test cannot discriminate")
	}

	rows, err := app.Query(context.Background(), `SELECT was_enabled FROM set_rule_enabled($1::uuid, $2, false)`, actor, scheduledOnly)
	if err != nil {
		t.Fatalf("set_rule_enabled as invoice_app: %v (is the migration applied?)", err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if n != 0 {
		t.Errorf("returned %d rows for a key only a scheduled version carries, want 0", n)
	}
	if !ruleEnabledByID(t, super, scheduled) {
		t.Error("the scheduled version's rule was disabled")
	}
}
