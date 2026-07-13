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
	"fmt"
	"os"
	"reflect"
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

// TestRLS_DemoResetGuardRejectsWrongKind: [M3-12-01 QA] adversarial coverage. The
// existing GuardRefusesWrongTarget case only mutates `name`, which would still pass a
// guard that (incorrectly) checked id+name and ignored `kind`. This case keeps id and
// name canonical and mutates ONLY `kind` (to the other value the column's CHECK
// permits, 'in_house' — migrations/20260709153027_tenants_add_kind.sql), proving the
// guard's exact-match requires ALL THREE columns, not a subset. Also strengthens the
// "commits nothing on refusal" evidence from a single disabled rule to two, so a
// guard/rollback bug that only protects the first row touched cannot hide.
func TestRLS_DemoResetGuardRejectsWrongKind(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	const ruleKeyA = "vat-standard-rate"
	const ruleKeyB = "supplier-tin-required"
	disableRule(t, ruleKeyA)
	disableRule(t, ruleKeyB)

	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET kind = 'in_house' WHERE id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("trip the guard (mutate tenant kind): %v", err)
	}

	err := applyDemoReset(t)

	if err == nil {
		t.Fatal("apply against a fixture with the wrong kind succeeded, want an error containing \"demo-reset refused\"")
	}
	if !strings.Contains(err.Error(), "demo-reset refused") {
		t.Errorf("apply error = %q, want it to contain %q", err.Error(), "demo-reset refused")
	}

	if got := ruleEnabled(t, ruleKeyA); got {
		t.Errorf("rule %q enabled = %v after a refused apply, want false (still disabled — nothing committed)", ruleKeyA, got)
	}
	if got := ruleEnabled(t, ruleKeyB); got {
		t.Errorf("rule %q enabled = %v after a refused apply, want false (still disabled — nothing committed)", ruleKeyB, got)
	}
}

// TestRLS_DemoResetGuardRejectsWrongName: [M3-12-01 QA] adversarial coverage,
// complementary to GuardRejectsWrongKind above — mutates ONLY `name` (id and kind stay
// canonical) using a value that still contains the substring "Okafor" and starts with
// the right prefix, so a guard that used a loose match (e.g. LIKE 'Okafor%' instead of
// an exact `=`) would wrongly accept it. Confirms the guard's name check is exact.
func TestRLS_DemoResetGuardRejectsWrongName(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	const ruleKey = "currency-allowed"
	disableRule(t, ruleKey)

	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET name = 'Okafor & Partners LLC' WHERE id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("trip the guard (mutate tenant name to a near-match): %v", err)
	}

	err := applyDemoReset(t)

	if err == nil {
		t.Fatal("apply against a near-match fixture name succeeded, want an error containing \"demo-reset refused\"")
	}
	if !strings.Contains(err.Error(), "demo-reset refused") {
		t.Errorf("apply error = %q, want it to contain %q", err.Error(), "demo-reset refused")
	}
	if got := ruleEnabled(t, ruleKey); got {
		t.Errorf("rule %q enabled = %v after a refused apply, want false (still disabled — nothing committed)", ruleKey, got)
	}
}

// TestRLS_DemoResetReenablesMultipleDisabledRules: [M3-12-01 QA] adversarial coverage.
// The Core-AC-3 case (ReenablesDisabledRule) only disables a single key
// (vat-standard-rate), which a narrower fix (e.g. `UPDATE rules SET enabled = true
// WHERE key = 'vat-standard-rate'`) would also satisfy. This disables THREE distinct
// keys spanning different rule `type`s (tax_math, required, enum) and asserts the
// GLOBAL `WHERE enabled = false` predicate restores every one of them in a single
// apply, proving the fix is genuinely global and not hardcoded to one key.
func TestRLS_DemoResetReenablesMultipleDisabledRules(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	setAllRulesEnabled(t)

	keys := []string{"vat-standard-rate", "supplier-tin-required", "currency-allowed"}
	for _, k := range keys {
		disableRule(t, k)
	}

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	if n := disabledRuleCount(t); n != 0 {
		t.Errorf("count(rules WHERE enabled=false) = %d after apply, want 0", n)
	}
	for _, k := range keys {
		if got := ruleEnabled(t, k); !got {
			t.Errorf("rule %q enabled = %v after apply, want true", k, got)
		}
	}
}

// ---------------------------------------------------------------------------
// M3-12-02: clear + curate the demo-tenant portfolio.
//
// [M3-12-02] extends db/demo-reset.sql (after the guard + rule re-enable, before
// COMMIT) with a DELETE FROM business_entities WHERE tenant_id = <demo> followed by
// an INSERT of the 27 curated rows (story § Curated dataset). At authoring time the
// checked-in file still only has the guard + rule re-enable — the placeholder
// comment "-- (M3-12-02 adds: DELETE demo portfolio + INSERT the 27 curated rows,
// before COMMIT.)" — so every portfolio-behavior case below (CuratesExactSet,
// ClearsJunkAndConverges, IdempotentNoGrowth, StatusMixPresent) is RED: applying the
// file leaves business_entities completely untouched, so the demo tenant has 0 rows
// instead of the curated 27. TestRLS_DemoResetLeavesOtherTenantUntouched is a
// cross-tenant invariant/regression guard, not new portfolio-behavior coverage — it
// is expected to already PASS against the pre-M3-12-02 file, since a file that
// writes nothing to business_entities trivially leaves every tenant's portfolio
// (including Honeywell's) untouched. That is by design, not a gap in the RED
// coverage.
//
// Assertion strategy (deliberately not a full duplication of all 27 curated rows):
// count + status-mix + non-generic-name + TIN-uniqueness + a 2-3-name spot-check
// proves "the curated set materialized correctly" without hardcoding all 27 names
// twice (once in db/demo-reset.sql, once here) — a change to the executor's exact
// wording of an uninvolved row would not spuriously break these tests. The
// idempotency and cross-tenant cases instead capture the FULL (name,tin,status) set
// with fetchEntities and compare it against itself (apply 1 vs apply 2, or before vs
// after), a stronger, self-contained determinism check that needs no hardcoded
// curated list at all.
// ---------------------------------------------------------------------------

// honeywellTenantID is the second seeded tenant fixture used only by the
// cross-tenant-untouched case below (db/seed.dev.sql) — Honeywell Group,
// kind='in_house'.
const honeywellTenantID = "22222222-2222-2222-2222-222222222222"

// entityRow captures a business_entities row's presentable identity — name, TIN, and
// status — the three columns the curated dataset (story § Curated dataset) fixes.
// Deliberately excludes id: entity ids use the table default (gen_random_uuid()) and
// are explicitly not part of the presentable state (System Design, "Clear + curate").
type entityRow struct {
	name   string
	tin    string
	status string
}

// fetchEntities returns every business_entities row for tenantID as (name, tin,
// status) tuples, ordered by name so two independent fetches (e.g. before/after a
// second apply, or two different tenants) are directly comparable with
// reflect.DeepEqual regardless of physical row/insertion order.
func fetchEntities(t *testing.T, tenantID string) []entityRow {
	t.Helper()
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT name, coalesce(tin, ''), status FROM business_entities WHERE tenant_id = $1 ORDER BY name`,
		tenantID,
	)
	if err != nil {
		t.Fatalf("query business_entities for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []entityRow
	for rows.Next() {
		var r entityRow
		if err := rows.Scan(&r.name, &r.tin, &r.status); err != nil {
			t.Fatalf("scan business_entities row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate business_entities rows for tenant %s: %v", tenantID, err)
	}
	return got
}

// seedHoneywellTenantFixture upserts the Honeywell tenant fixture as the superuser,
// mirroring seedDemoTenantFixture above (same trusted BYPASSRLS path, same
// hardcoded-id cleanup convention: t.Cleanup removes any business_entities left
// under it and restores its canonical name/kind, since 22222222-… is a fixed
// fixture id shared across tests, not a per-test-random uuid).
func seedHoneywellTenantFixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'Honeywell Group', 'in_house')
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, kind = EXCLUDED.kind`,
		honeywellTenantID,
	); err != nil {
		t.Fatalf("seed Honeywell tenant fixture: %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = h.super.Exec(cctx, `DELETE FROM business_entities WHERE tenant_id = $1`, honeywellTenantID)
		_, _ = h.super.Exec(cctx,
			`UPDATE tenants SET name = 'Honeywell Group', kind = 'in_house' WHERE id = $1`,
			honeywellTenantID,
		)
	})
}

// seedJunkEntities inserts ~30 generic rows under tenantID as the superuser,
// mirroring the junk that accumulates from the Day-30 demo suite's
// ensurePortfolioSeeded top-up and repeated CI/demo runs: 25 "Demo Client <tin>"
// rows (mostly active, every 6th archived — "extra archived rows" per Core AC-4)
// plus 5 "Demo Onboarding <tin>" rows. This is exactly the junk AC-4 requires the
// reset to clear.
func seedJunkEntities(t *testing.T, tenantID string) {
	t.Helper()
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		tin := fmt.Sprintf("99%06d-9999", i)
		status := "active"
		if i%6 == 0 {
			status = "archived"
		}
		if _, err := h.super.Exec(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, $4)`,
			tenantID, fmt.Sprintf("Demo Client %s", tin), tin, status,
		); err != nil {
			t.Fatalf("seed junk business_entities row %d: %v", i, err)
		}
	}
	for i := 0; i < 5; i++ {
		tin := fmt.Sprintf("98%06d-9999", i)
		if _, err := h.super.Exec(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, 'active')`,
			tenantID, fmt.Sprintf("Demo Onboarding %s", tin), tin,
		); err != nil {
			t.Fatalf("seed junk Demo Onboarding row %d: %v", i, err)
		}
	}
}

// TestRLS_DemoResetCuratesExactSet: [M3-12-02] Test Spec row 1 / Core AC-2. Starting
// from an empty demo portfolio, a successful apply must leave the demo tenant with
// exactly the 27 curated rows: 21 active / 6 archived, no generic seed-junk names,
// unique TINs, and (spot-checked) the real curated business names from the story
// table — not the generic "Demo Client <tin>" seed.
//
// RED against the file as of [M3-12-01] (guard + rule re-enable only, no portfolio
// write): applying it leaves business_entities completely untouched, so
// len(fetchEntities(...)) = 0, failing the "want 27" assertion below — not a
// compile/skip error.
func TestRLS_DemoResetCuratesExactSet(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	got := fetchEntities(t, demoTenantID)
	if len(got) != 27 {
		t.Fatalf("count(business_entities) for demo tenant = %d, want 27", len(got))
	}

	var active, archived int
	tins := make(map[string]int, len(got))
	for _, r := range got {
		switch r.status {
		case "active":
			active++
		case "archived":
			archived++
		default:
			t.Errorf("row %q has status %q, want active or archived", r.name, r.status)
		}
		if strings.HasPrefix(r.name, "Demo Client") || strings.HasPrefix(r.name, "Demo Onboarding") {
			t.Errorf("curated row name = %q, want a real curated business name, not generic seed junk", r.name)
		}
		tins[r.tin]++
	}
	if active != 21 {
		t.Errorf("count(active) = %d, want 21", active)
	}
	if archived != 6 {
		t.Errorf("count(archived) = %d, want 6", archived)
	}
	for tin, n := range tins {
		if n != 1 {
			t.Errorf("TIN %q appears %d times among curated rows, want exactly 1 (unique)", tin, n)
		}
	}

	// Spot-check known curated names from the story's § Curated dataset table
	// (#1 active, #23 and #27 archived) rather than duplicating all 27 — proves
	// the INSERT used the real curated names, not just the right count/mix.
	wantNames := map[string]bool{
		"Adeyemi & Sons Trading Ltd": false,
		"Halima Boutique Ltd":        false,
		"Ekene Auto Parts Ltd":       false,
	}
	for _, r := range got {
		if _, ok := wantNames[r.name]; ok {
			wantNames[r.name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected curated name %q not found in the demo tenant's portfolio", name)
		}
	}
}

// TestRLS_DemoResetClearsJunkAndConverges: [M3-12-02] Test Spec row 2 / Core AC-4.
// Starting from a demo portfolio polluted with ~30 generic junk rows (the kind the
// Day-30 demo suite's top-up and repeated runs leave behind), a successful apply
// must clear ALL of it and converge to exactly the 27 curated rows — no leftovers.
//
// RED against the file as of [M3-12-01]: it never touches business_entities, so the
// 30 pre-seeded junk rows are still all present after "apply" — this fails both the
// "want 27" count assertion and the "zero Demo Client/Demo Onboarding rows"
// assertion, not a compile/skip error.
func TestRLS_DemoResetClearsJunkAndConverges(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)

	seedJunkEntities(t, demoTenantID)
	if n := mustCount(t, h.super, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, demoTenantID); n != 30 {
		t.Fatalf("precondition: seeded junk count = %d, want 30", n)
	}

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	got := fetchEntities(t, demoTenantID)
	if len(got) != 27 {
		t.Fatalf("count(business_entities) for demo tenant after clearing junk = %d, want exactly 27 (curated set only, no leftovers)", len(got))
	}
	for _, r := range got {
		if strings.HasPrefix(r.name, "Demo Client") || strings.HasPrefix(r.name, "Demo Onboarding") {
			t.Errorf("junk row survived apply: name = %q", r.name)
		}
	}
}

// TestRLS_DemoResetIdempotentNoGrowth: [M3-12-02] Test Spec row 3 / Core AC-5, AC-2.
// Applying the file twice in a row must converge, not grow: exactly 27 rows after
// each apply, and — the stronger determinism check — the FULL (name,tin,status) set
// must be byte-identical across both applies (same curated names + mix every run,
// per the story's "same names and mix on every run").
//
// RED against the file as of [M3-12-01]: business_entities stays untouched by either
// apply, so both counts are 0 (not 27) — fails the "want 27" assertions, not a
// compile/skip error. (The two empty sets would trivially DeepEqual each other,
// which is why the count assertions are load-bearing here, not the DeepEqual
// alone.)
func TestRLS_DemoResetIdempotentNoGrowth(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first := fetchEntities(t, demoTenantID)
	if len(first) != 27 {
		t.Fatalf("count(business_entities) after FIRST apply = %d, want 27", len(first))
	}

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("second apply (idempotency): %v", err)
	}
	second := fetchEntities(t, demoTenantID)
	if len(second) != 27 {
		t.Fatalf("count(business_entities) after SECOND apply = %d, want 27 (no growth)", len(second))
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("curated (name,tin,status) set differs between the first and second apply, want byte-identical\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// TestRLS_DemoResetStatusMixPresent: [M3-12-02] Test Spec row 4 / Core AC-2 (mix).
// The demo tenant's portfolio must have BOTH statuses present after an apply — 21
// active and 6 archived — the deliberate mix the story calls out, not an all-active
// or all-archived set.
//
// RED against the file as of [M3-12-01]: with business_entities untouched, both
// counts are 0, failing the "want 21"/"want 6" assertions, not a compile/skip error.
func TestRLS_DemoResetStatusMixPresent(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	active := mustCount(t, h.super,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND status = 'active'`, demoTenantID)
	archived := mustCount(t, h.super,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND status = 'archived'`, demoTenantID)

	if active != 21 {
		t.Errorf("count(active) = %d, want 21", active)
	}
	if archived != 6 {
		t.Errorf("count(archived) = %d, want 6", archived)
	}
}

// TestRLS_DemoResetLeavesOtherTenantUntouched: [M3-12-02] Test Spec row 5 / Core
// AC-6. A demo-reset apply must never read or write another tenant's portfolio —
// here Honeywell (kind='in_house'), a firm-vs-in_house kind boundary chosen
// deliberately (not just "any other tenant") to also guard against a future
// DELETE/INSERT that mistakenly scopes by kind instead of by the exact demo tenant
// id.
//
// NOT RED by design: this is a cross-tenant invariant/regression guard, not new
// portfolio-behavior coverage. The file as of [M3-12-01] writes nothing to
// business_entities at all, so Honeywell's rows are trivially untouched and this
// already PASSES today — it stays green through [M3-12-02] specifically because the
// executor's DELETE/INSERT must stay scoped to `tenant_id = <demo>`.
func TestRLS_DemoResetLeavesOtherTenantUntouched(t *testing.T) {
	requireHarness(t)
	seedDemoTenantFixture(t)
	seedHoneywellTenantFixture(t)

	honeywellNames := []string{"Honeywell Flour Mills Ltd", "Honeywell Estate Ltd", "Honeywell Foods Ltd"}
	for _, name := range honeywellNames {
		seedBusinessEntity(t, honeywellTenantID, name)
	}

	before := fetchEntities(t, honeywellTenantID)
	if len(before) != len(honeywellNames) {
		t.Fatalf("precondition: Honeywell business_entities count = %d, want %d", len(before), len(honeywellNames))
	}

	if err := applyDemoReset(t); err != nil {
		t.Fatalf("apply db/demo-reset.sql: %v", err)
	}

	after := fetchEntities(t, honeywellTenantID)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("Honeywell tenant's business_entities changed after a demo-reset apply, want untouched\nbefore: %+v\nafter:  %+v", before, after)
	}
}
