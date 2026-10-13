// set_rule_enabled behaviour against the seeded rule sets. `rules` is global, so each test
// registers restoreRulesOnCleanup before its first write. No t.Parallel().
package validation

import (
	"context"
	"reflect"
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

// a fresh load sees the switch, with no redeploy.
func TestStaffRulesFunction_NextLoadSeesTheSwitch(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	seedV5Lists(t, super)
	actor := seedRulesStaffRow(t, super)
	engine := NewDefaultEngine()
	const key = "supplier-tin-format"

	violationKeys := func() []string {
		result, err := engine.Evaluate(badInvoicePayload(), loadToday(t, app))
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		var keys []string
		for _, v := range result.Violations {
			keys = append(keys, v.RuleKey)
		}
		return keys
	}
	without := func(keys []string, drop string) []string {
		var out []string
		for _, k := range keys {
			if k != drop {
				out = append(out, k)
			}
		}
		return out
	}

	baseline := violationKeys()
	if !containsKey(baseline, key) || len(without(baseline, key)) == 0 {
		t.Fatalf("baseline: want %s and a control violation, got %v", key, baseline)
	}

	if was := switchRuleViaFunction(t, app, actor, key, false); !was {
		t.Error("disable returned was_enabled = false, want true")
	}
	if got, want := violationKeys(), without(baseline, key); !reflect.DeepEqual(got, want) {
		t.Errorf("after the switch violations = %v, want only %s dropped: %v", got, key, want)
	}

	switchRuleViaFunction(t, app, actor, key, true)
	if got := violationKeys(); !reflect.DeepEqual(got, baseline) {
		t.Errorf("after the restore violations = %v, want %v", got, baseline)
	}
}

// loadToday loads the version in force today, the one the switch targets.
func loadToday(t *testing.T, app *pgxpool.Pool) RuleSet {
	t.Helper()
	rs, err := NewStore(app).LoadActiveRuleSet(newTestIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	return rs
}

// only the version in force changes.
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

// the flip passes the content lock on a sealed version and touches no other column.
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

// a key that exists only in a scheduled version is unknown to the function.
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
