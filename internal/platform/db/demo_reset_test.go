// M3-12-01: tests for the guard + global rule re-enable half of `db/demo-reset.sql`,
// written BEFORE that SQL logic exists — the checked-in file is a `BEGIN; COMMIT;`
// STUB (see db/demo-reset.sql's header), so every case here is RED against it: the
// guard test expects an error the stub never raises, and the rule-reenable tests
// expect a mutation the stub never performs. [M3-12-01] (Executor, Stage 3) adds the
// `DO $$ … RAISE EXCEPTION …$$` fixture guard and the global
// `UPDATE rules SET enabled = true WHERE enabled = false;`; [M3-12-02] later extends
// the same file (and this test file) with the clear + curate block.
//
// Fixture self-seed (F1, per the story's disposition): CI's `rls` job runs migrations
// but does NOT run db/seed.dev.sql, so the demo tenant fixture
// (11111111-1111-1111-1111-111111111111 / 'Okafor & Partners' / kind='firm') does not
// exist in CI. Each test below self-seeds it via the superuser pool (BYPASSRLS, same
// trusted path as db/seed.dev.sql) before applying the file, and never depends on
// seed.dev.sql having run. Because the tenant id is a hardcoded fixture (not a
// per-test-random uuid like the shared harness's tenantA/tenantB), each test's
// t.Cleanup restores/removes exactly what it touched so the fixed-id rows don't leak
// between tests, and none of these tests use t.Parallel() (shared global `rules` +
// shared fixture tenant id would race).
//
// The file is applied via a single no-arg pool.Exec(ctx, string(fileBytes)) — see the
// story's [A9]: pgx v5 only uses the simple protocol (required for a multi-statement
// BEGIN…COMMIT body to run in one round trip) when there are zero $1-style params.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS`
// (.github/workflows/ci.yml) and `make test-rls` both pick these up automatically.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_DemoReset ./internal/platform/db/...
package db_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// demoTenantID is the hardcoded demo-tenant fixture id the guard checks for
// (db/seed.dev.sql, [A6]/[A9]) — Okafor & Partners, kind='firm'.
const demoTenantID = "11111111-1111-1111-1111-111111111111"

// demoResetSQLPath is db/demo-reset.sql relative to this package directory
// (internal/platform/db), i.e. the repo root's db/demo-reset.sql.
const demoResetSQLPath = "../../../db/demo-reset.sql"

// seedDemoTenantFixture upserts the demo tenant fixture as the superuser (BYPASSRLS,
// same trusted path as db/seed.dev.sql) so the guard finds it and any
// business_entities FK'd to it can be inserted. Callers must have already called
// requireHarness(t) (matches seedBusinessEntity's convention above: relies on the
// package-level h, doesn't re-derive it). Registers a t.Cleanup that restores the
// canonical name/kind (in case a test mutated them, e.g. the guard-refusal case) and
// removes any business_entities rows left under this fixture id.
func seedDemoTenantFixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'Okafor & Partners', 'firm')
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, kind = EXCLUDED.kind`,
		demoTenantID,
	); err != nil {
		t.Fatalf("seed demo tenant fixture: %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = h.super.Exec(cctx, `DELETE FROM business_entities WHERE tenant_id = $1`, demoTenantID)
		_, _ = h.super.Exec(cctx,
			`UPDATE tenants SET name = 'Okafor & Partners', kind = 'firm' WHERE id = $1`,
			demoTenantID,
		)
	})
}

// setAllRulesEnabled forces every row in the GLOBAL rules table (no tenant_id, no
// RLS — [A2]) to enabled = true, so each test starts from — and, for the idempotent
// case, is restored to — a known baseline. Registers a t.Cleanup that re-applies the
// same baseline, since rules is shared, global state that must not leak a disabled
// row into a later test.
func setAllRulesEnabled(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if _, err := h.super.Exec(ctx, `UPDATE rules SET enabled = true WHERE enabled = false`); err != nil {
		t.Fatalf("baseline: enable all rules: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `UPDATE rules SET enabled = true WHERE enabled = false`)
	})
}

// disableRule disables the single rule the Day-30 demo suite kill-switches
// (vat-standard-rate), as the superuser.
func disableRule(t *testing.T, key string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE rules SET enabled = false WHERE key = $1`, key,
	); err != nil {
		t.Fatalf("disable rule %q: %v", key, err)
	}
}

// ruleEnabled reads back the enabled flag for a single rule key. Fails the test if
// the key does not resolve to exactly one row (the fixture assumption: 'vat-standard-
// rate' is unique in the seeded MBS v1 active version).
func ruleEnabled(t *testing.T, key string) bool {
	t.Helper()
	var enabled bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT enabled FROM rules WHERE key = $1`, key,
	).Scan(&enabled); err != nil {
		t.Fatalf("read back rule %q: %v", key, err)
	}
	return enabled
}

// disabledRuleCount returns count(*) FROM rules WHERE enabled = false.
func disabledRuleCount(t *testing.T) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM rules WHERE enabled = false`)
}

// applyDemoReset reads db/demo-reset.sql from disk and applies it as a single no-arg
// pool.Exec against the superuser pool — [A9]: pgx v5 only takes the simple-query
// protocol (required for a multi-statement BEGIN…COMMIT body) when there are zero
// $-style params, so this must stay parameter-free.
func applyDemoReset(t *testing.T) error {
	t.Helper()

	sqlBytes, err := os.ReadFile(demoResetSQLPath)
	if err != nil {
		t.Fatalf("read %s: %v", demoResetSQLPath, err)
	}

	_, err = h.super.Exec(context.Background(), string(sqlBytes))
	return err
}

// TestRLS_DemoResetGuardRefusesWrongTarget: [M3-12-01] Test Spec row 1 / Core AC-7.
// With the demo fixture present but then mutated so its name no longer matches the
// guard's exact check, applying db/demo-reset.sql must be refused with an error
// containing "demo-reset refused", and the refusal must roll back the WHOLE
// transaction — a rule disabled earlier in the same setup must still read disabled
// afterwards (proving zero writes committed, not a partial apply).
//
// RED against the checked-in BEGIN;COMMIT; stub: the stub has no guard at all, so it
// commits successfully (err == nil) instead of refusing — this test fails on the
// "apply returned no error" assertion, not on a compile/skip error.
func TestRLS_DemoResetGuardRefusesWrongTarget(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	const ruleKey = "vat-standard-rate"
	disableRule(t, ruleKey)

	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET name = 'WRONG' WHERE id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("trip the guard (mutate tenant name): %v", err)
	}

	err := applyDemoReset(t)

	if err == nil {
		t.Fatal("apply against a mistargeted fixture succeeded, want an error containing \"demo-reset refused\"")
	}
	if !strings.Contains(err.Error(), "demo-reset refused") {
		t.Errorf("apply error = %q, want it to contain %q", err.Error(), "demo-reset refused")
	}

	if got := ruleEnabled(t, ruleKey); got {
		t.Errorf("rule %q enabled = %v after a refused apply, want false (still disabled — nothing committed)", ruleKey, got)
	}
}

// TestRLS_DemoResetReenablesDisabledRule: [M3-12-01] Test Spec row 2 / Core AC-3.
// With the demo fixture present (guard passes) and one rule disabled, a successful
// apply must leave no rule disabled — the kill-switch a prior demo left behind is
// reset to enabled.
//
// RED against the checked-in stub: the stub applies (no guard to refuse it) but
// performs no UPDATE, so the disabled rule is still disabled afterwards — this test
// fails on the "count(enabled=false) == 0" assertion, not on a compile/skip error.
func TestRLS_DemoResetReenablesDisabledRule(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	const ruleKey = "vat-standard-rate"
	disableRule(t, ruleKey)

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	if n := disabledRuleCount(t); n != 0 {
		t.Errorf("count(rules WHERE enabled=false) = %d after apply, want 0", n)
	}
	if got := ruleEnabled(t, ruleKey); !got {
		t.Errorf("rule %q enabled = %v after apply, want true", ruleKey, got)
	}
}

// TestRLS_DemoResetRuleReenableIdempotent: [M3-12-01] Test Spec row 3 / Core AC-5,
// AC-1. With the demo fixture present and all rules already enabled, applying the
// file TWICE must succeed both times with no error, converging to zero disabled
// rules after each apply — re-running right before a call must never fail or
// regress state.
//
// RED against the checked-in stub: the stub is trivially idempotent (BEGIN;COMMIT;
// twice never errors), so this specific assertion alone would falsely pass against
// the stub — but see the companion assertion below (baseline is disturbed then must
// be restored by the SECOND apply) which the no-op stub cannot satisfy, keeping this
// case meaningfully RED.
func TestRLS_DemoResetRuleReenableIdempotent(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	// Disturb the baseline the way a prior demo run would, so a genuinely no-op
	// (stub) apply is distinguishable from a real re-enabling apply: the first
	// call must fix it, and the second call (now already-clean) must be a true
	// no-op.
	const ruleKey = "vat-standard-rate"
	disableRule(t, ruleKey)

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if n := disabledRuleCount(t); n != 0 {
		t.Fatalf("count(rules WHERE enabled=false) after FIRST apply = %d, want 0", n)
	}

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("second apply (idempotency): %v", err)
	}
	if n := disabledRuleCount(t); n != 0 {
		t.Errorf("count(rules WHERE enabled=false) after SECOND apply = %d, want 0", n)
	}
}
