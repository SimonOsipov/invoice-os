// Kill-switch suite: staff switch a rule off with an owner-role statement
// (vault runbook "Rule kill switch"). `rules` is global, so these tests mutate the
// shared seeded rows; each registers a superuser restore in t.Cleanup before
// its first write. No t.Parallel().
//
//	DATABASE_URL=... DATABASE_SUPERUSER_URL=... go test -p 1 -count=1 ./internal/validation/...
package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// killSwitchStatement is the operator's statement:
// the active version's row for $2, by key.
const killSwitchStatement = "UPDATE rules r SET enabled = $1 FROM rule_set_versions v WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = $2"

// runKillSwitch runs killSwitchStatement as invoice_migrator in its own tx and
// returns the rows affected.
func runKillSwitch(t *testing.T, super *pgxpool.Pool, key string, enabled bool) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin kill-switch tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE invoice_migrator"); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
	}
	tag, err := tx.Exec(ctx, killSwitchStatement, enabled, key)
	if err != nil {
		t.Fatalf("kill switch (%s, %t): %v", key, enabled, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit kill switch (%s, %t): %v", key, enabled, err)
	}
	return tag.RowsAffected()
}

// killSwitchCase pairs a seeded rule key with a mutator that, applied to a
// fresh validInvoicePayload(), fires exactly that rule (and no other) so the
// "dropped after disable / restored after enable" assertions are unambiguous
// about which rule caused the change.
type killSwitchCase struct {
	name   string
	key    string
	mutate func(p Payload)
}

// TestKillSwitch_E2E disables and restores two seeded rules of different types
// (tax_math, enum) through runKillSwitch and checks the effect on evaluation.
func TestKillSwitch_E2E(t *testing.T) {
	super, app := dbTestPools(t)

	restoreRulesOnCleanup(t, super)

	cases := []killSwitchCase{
		{
			name: "vat-standard-rate (tax_math)",
			key:  "vat-standard-rate",
			mutate: func(p Payload) {
				invoiceOf(p)["vat"] = 70.0 // valid subtotal 1000 -> expected vat 75 +/- 0.005; 70 fires.
			},
		},
		{
			name: "currency-allowed (enum)",
			key:  "currency-allowed",
			mutate: func(p Payload) {
				invoiceOf(p)["currency"] = "USD"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			firePayload := validInvoicePayload()
			tc.mutate(firePayload)
			engine := NewDefaultEngine()

			rs := loadActive(t, app)
			result, err := engine.Evaluate(firePayload, rs)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) baseline: %v", err)
			}
			if !hasViolation(result, tc.key) {
				t.Fatalf("baseline: %s did not fire -- violations=%+v (fixture payload must trip this rule before the kill switch)", tc.key, result.Violations)
			}

			if n := runKillSwitch(t, super, tc.key, false); n != 1 {
				t.Fatalf("kill switch (%s, false) rows = %d, want 1", tc.key, n)
			}
			if ruleEnabledActive(t, super, tc.key) {
				t.Errorf("%s: rules.enabled = true after the kill switch, want false", tc.key)
			}

			// A fresh load sees it with no redeploy; the other rule still fires.
			rs2 := loadActive(t, app)
			result2, err := engine.Evaluate(firePayload, rs2)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) after disable: %v", err)
			}
			if hasViolation(result2, tc.key) {
				t.Errorf("%s still fired after the kill switch -- violations=%+v", tc.key, result2.Violations)
			}
			controlResult, err := engine.Evaluate(badInvoicePayload(), rs2)
			if err != nil {
				t.Fatalf("Evaluate(badInvoicePayload) control check after disabling %s: %v", tc.key, err)
			}
			if !hasViolation(controlResult, "supplier-tin-format") {
				t.Errorf("control rule supplier-tin-format did not fire after disabling %s -- only the switched rule should drop", tc.key)
			}

			if n := runKillSwitch(t, super, tc.key, true); n != 1 {
				t.Fatalf("kill switch (%s, true) rows = %d, want 1", tc.key, n)
			}
			if !ruleEnabledActive(t, super, tc.key) {
				t.Errorf("%s: rules.enabled = false after restore, want true", tc.key)
			}

			rs3 := loadActive(t, app)
			result3, err := engine.Evaluate(firePayload, rs3)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) after restore: %v", err)
			}
			if !hasViolation(result3, tc.key) {
				t.Errorf("%s did not fire after restore -- violations=%+v", tc.key, result3.Violations)
			}
		})
	}
}

// restoreRulesOnCleanup snapshots every rules row and registers a superuser
// restore of any row whose enabled value differs at cleanup. Call it before the
// first write.
func restoreRulesOnCleanup(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	snap := rulesEnabledByID(t, super)
	if len(snap) == 0 {
		t.Fatal("no rules rows to snapshot")
	}
	t.Cleanup(func() {
		for id, want := range snap {
			if _, err := super.Exec(context.Background(),
				`UPDATE rules SET enabled = $1 WHERE id = $2 AND enabled IS DISTINCT FROM $1`, want, id,
			); err != nil {
				t.Errorf("cleanup: restore rules.id=%s enabled=%t: %v", id, want, err)
			}
		}
	})
}

// rulesEnabledByID reads rules.id -> enabled for every version.
func rulesEnabledByID(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id::text, enabled FROM rules`)
	if err != nil {
		t.Fatalf("read rules.enabled: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			t.Fatalf("scan rules row: %v", err)
		}
		got[id] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rules rows: %v", err)
	}
	return got
}

// TestKillSwitch_TouchesOnlyTheActiveVersion: sealed versions keep their rows'
// prior enabled value.
func TestKillSwitch_TouchesOnlyTheActiveVersion(t *testing.T) {
	super, _ := dbTestPools(t)
	const key = "vat-standard-rate"

	restoreRulesOnCleanup(t, super)

	sealedRows := func() map[string]bool {
		rows, err := super.Query(context.Background(),
			`SELECT r.id::text, r.enabled FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
			 WHERE NOT v.is_active AND r.key = $1`, key)
		if err != nil {
			t.Fatalf("read sealed rows for %s: %v", key, err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var id string
			var enabled bool
			if err := rows.Scan(&id, &enabled); err != nil {
				t.Fatalf("scan sealed row: %v", err)
			}
			got[id] = enabled
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate sealed rows: %v", err)
		}
		return got
	}

	before := sealedRows()
	if len(before) == 0 {
		t.Fatalf("no non-active version carries %s: the test cannot discriminate", key)
	}

	if n := runKillSwitch(t, super, key, false); n != 1 {
		t.Fatalf("kill switch (%s, false) rows = %d, want 1", key, n)
	}
	if ruleEnabledActive(t, super, key) {
		t.Errorf("active %s row enabled = true after the kill switch, want false", key)
	}
	after := sealedRows()
	for id, want := range before {
		if got, ok := after[id]; !ok || got != want {
			t.Errorf("sealed row %s enabled = %t (present=%t), want %t unchanged", id, got, ok, want)
		}
	}
}

// TestKillSwitch_UnknownKeyUpdatesNothing: a key absent from the active version
// matches no row, including one that a non-active version carries.
func TestKillSwitch_UnknownKeyUpdatesNothing(t *testing.T) {
	super, _ := dbTestPools(t)
	restoreRulesOnCleanup(t, super)

	const nonActiveOnly = "ks-non-active-only"
	versionID, _ := seedVersion(t, super, false)
	seedRule(t, super, versionID, nonActiveOnly)

	for _, key := range []string{"no-such-rule", nonActiveOnly} {
		t.Run(key, func(t *testing.T) {
			before := rulesEnabledByID(t, super)
			if n := runKillSwitch(t, super, key, false); n != 0 {
				t.Errorf("kill switch (%s, false) rows = %d, want 0", key, n)
			}
			after := rulesEnabledByID(t, super)
			if len(after) != len(before) {
				t.Fatalf("rules row count %d -> %d", len(before), len(after))
			}
			for id, want := range before {
				if after[id] != want {
					t.Errorf("rules.id=%s enabled = %t, want %t unchanged", id, after[id], want)
				}
			}
		})
	}
}

// TestKillSwitch_OnlyTheOwnerCanRunIt: killSwitchStatement updates 1 row as
// invoice_migrator and is refused with 42501 as invoice_app. Both run in a tx
// that is rolled back.
func TestKillSwitch_OnlyTheOwnerCanRunIt(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	const key = "vat-standard-rate"

	restoreRulesOnCleanup(t, super)

	run := func(role string) (rows int64, who string, err error) {
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin %s tx: %v", role, err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			t.Fatalf("SET LOCAL ROLE %s: %v", role, err)
		}
		if err := tx.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
			t.Fatalf("SELECT current_user as %s: %v", role, err)
		}
		tag, err := tx.Exec(ctx, killSwitchStatement, false, key)
		return tag.RowsAffected(), who, err
	}

	_, who, err := run("invoice_app")
	if who != "invoice_app" {
		t.Fatalf("current_user = %q, want invoice_app", who)
	}
	assertAppRefused(t, err, "kill switch statement")

	rows, who, err := run("invoice_migrator")
	if who != "invoice_migrator" {
		t.Fatalf("current_user = %q, want invoice_migrator", who)
	}
	if err != nil {
		t.Fatalf("kill switch as invoice_migrator: %v", err)
	}
	if rows != 1 {
		t.Errorf("kill switch as invoice_migrator rows = %d, want 1", rows)
	}
	if !ruleEnabledActive(t, super, key) {
		t.Errorf("%s enabled = false after rolled-back statements, want true", key)
	}
}

// ruleEnabledActive reads rules.enabled for key on the active version via the
// superuser pool, with the same row choice as killSwitchStatement.
func ruleEnabledActive(t *testing.T, pool *pgxpool.Pool, key string) bool {
	t.Helper()
	var enabled bool
	if err := pool.QueryRow(context.Background(),
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON r.rule_set_version_id = v.id
		 WHERE v.is_active AND r.key = $1`, key,
	).Scan(&enabled); err != nil {
		t.Fatalf("read rules.enabled for key=%q: %v", key, err)
	}
	return enabled
}
