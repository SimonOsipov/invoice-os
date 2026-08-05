// reset_test.go — suite for db.Reset/db.ResetEnabled (persona-handoff-fix,
// Decision [pr-only-reset]): the PR-environment-only destructive reset that
// runs inside Provision, between MigrateUp and Seed.
//
// Design, mirroring this package's established conventions:
//   - Pure-logic gate cases (TestResetEnabledAllowlist) never touch a
//     database, in the shape of bootstrap_test.go's
//     TestBootstrapEnabledAllowlist — the brief's own required coverage list.
//   - DB-backed cases are env-gated on DATABASE_SUPERUSER_URL
//     (requireSuperuserDSN, bootstrap_test.go) for direct db.Reset tests, or
//     both DSNs (requireProvisionDSNs, provision_test.go) for the Provision-
//     level end-to-end cases.
//   - Every DB-backed test that calls db.Reset directly against the shared
//     dev/CI Postgres restores the curated demo state afterward (db.Seed, or
//     resetDemoBusinessEntities + a fresh db.Seed), matching
//     TestMigrateUpFromEmbedded / TestProvisionFromEmptyDatabase's own
//     "this test disturbs shared fixtures, restore them in cleanup" precedent
//     — so the rest of the package's run (seed_demo_test.go,
//     provision_test.go) stays green regardless of run order.
//   - The negative gate assertions (Reset must NOT fire for production /
//     development / empty / a non-"true" flag) are the ones the brief calls
//     out as more important than the positive pr-<N> case, so each gets its
//     own dedicated test rather than being folded into one big table.
package db_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// ---- Pure-logic: the allowlist itself (no database) -------------------------

// TestResetEnabledAllowlist mirrors bootstrap_test.go's
// TestBootstrapEnabledAllowlist case-for-case, with the one deliberate
// difference the brief requires: "development" must be false here (it is
// true for BootstrapEnabled). Covers every case the brief names explicitly:
// production/development/empty/PR-42/pr-/pr-abc/pr-42x/"pr-42 " (trailing
// space), plus the same whitespace/case/flag-exactness adversarial set
// TestBootstrapEnabledAllowlist already established for this codebase's one
// other allowlist guard.
func TestResetEnabledAllowlist(t *testing.T) {
	cases := []struct {
		name        string
		environment string
		flag        string
		want        bool
	}{
		// --- the brief's required cases, verbatim ---
		{`("pr-42","true")`, "pr-42", "true", true},
		{`("invoice-os-pr-42","true")`, "invoice-os-pr-42", "true", true},
		{`("production","true")`, "production", "true", false},
		// THE key divergence from BootstrapEnabled: "development" is allowed
		// there (it's what actually fires Bootstrap/Seed inside a real fork,
		// per ResetEnabled's doc comment) and is NOT allowed here.
		{`("development","true") -- excluded, unlike BootstrapEnabled`, "development", "true", false},
		{`("development","false")`, "development", "false", false},
		{`("production","")`, "production", "", false},
		{`("","true")`, "", "true", false},
		{`("staging","true") — unrecognised value`, "staging", "true", false},
		{`("Production","true")`, "Production", "true", false},
		{`("prod","true")`, "prod", "true", false},

		// --- production/allowlist lookalikes (whitespace/case) ---
		{"leading-whitespace production", " production", "true", false},
		{"trailing-whitespace production", "production ", "true", false},
		{"leading-whitespace development", " development", "true", false},
		{"trailing-whitespace development", "development ", "true", false},
		{"all-caps DEVELOPMENT", "DEVELOPMENT", "true", false},

		// --- PR-env shape edge cases (identical predicate to BootstrapEnabled) ---
		{"uppercase PR- prefix", "PR-42", "true", false},
		{"pr- with non-numeric suffix", "pr-abc", "true", false},
		{"pr- with no number", "pr-", "true", false},
		{"pr- with trailing garbage after the number", "pr-42x", "true", false},
		{"pr- with trailing whitespace", "pr-42 ", "true", false},

		// --- the flag itself must be exactly "true" ---
		{"flag uppercase TRUE", "pr-42", "TRUE", false},
		{"flag numeric 1", "pr-42", "1", false},
		{"flag yes", "invoice-os-pr-42", "yes", false},
		{"flag empty, PR-shaped environment", "pr-42", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := db.ResetEnabled(tc.environment, tc.flag); got != tc.want {
				t.Errorf("ResetEnabled(%q, %q) = %v, want %v", tc.environment, tc.flag, got, tc.want)
			}
		})
	}
}

// TestResetEnabledArbitrarilyLargePRNumber: adversarial coverage mirroring
// TestBootstrapEnabledAllowlistAcceptsArbitrarilyLargePRNumber — the same
// prEnvironmentPattern backs both gates, so a huge PR number must still match.
func TestResetEnabledArbitrarilyLargePRNumber(t *testing.T) {
	huge := "pr-99999999999999999999999999999999999999"
	if !db.ResetEnabled(huge, "true") {
		t.Errorf("ResetEnabled(%q, \"true\") = false, want true", huge)
	}
}

// ---- DB-backed: db.Reset itself --------------------------------------------

// resetTargetTables is every table db.Reset truncates, PARSED OUT of reset.go's
// resetTables rather than re-typed. The previous hand-copy claimed it would
// "fail loudly if the two ever drift apart" and did no such thing: `documents`
// was added to the schema by DOC-01, never added to either list, and survived
// every PR-environment reset until it broke uploader attribution in the
// source-document previewer. Deriving the list is what makes that class of
// omission impossible rather than merely discouraged.
// reset.go is read rather than imported: every test file in this package is
// package db_test, so the unexported constant is out of reach. Same technique
// internal/demodocs uses to pin its tenant allowlist to db/seed.dev.sql.
var resetTargetTables = parseResetTables()

func parseResetTables() []string {
	src, err := os.ReadFile("reset.go")
	if err != nil {
		return nil
	}
	m := regexp.MustCompile("(?s)const resetTables = `TRUNCATE(.*?)RESTART IDENTITY`").FindSubmatch(src)
	if m == nil {
		return nil
	}
	var out []string
	for _, name := range strings.Split(string(m[1]), ",") {
		if n := strings.TrimSpace(name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// A parser that silently yields nothing would make every assertion below
// vacuous, so the floor is pinned independently of the list's contents.
func TestResetTargetTablesParsedFromResetTables(t *testing.T) {
	if len(resetTargetTables) < 14 {
		t.Fatalf("parsed %d tables out of reset.go (%v); the parser is broken and every reset assertion below is vacuous",
			len(resetTargetTables), resetTargetTables)
	}
	for _, want := range []string{"invoices", "audit_log", "documents"} {
		if !slices.Contains(resetTargetTables, want) {
			t.Errorf("resetTables does not truncate %q", want)
		}
	}
	for _, got := range resetTargetTables {
		if strings.ContainsAny(got, " \t\n") {
			t.Errorf("parsed table name %q still carries whitespace", got)
		}
	}
}

// seedFullResetFixture inserts one row into every table db.Reset truncates,
// chained through the real FK graph (business_entities -> invoices ->
// line_items / invoice_status_history / submission_jobs -> app_exchange;
// import_batches -> business_entities), plus one independent row in each of
// idempotency_keys/audit_log/the four River tables. Connects as the
// superuser (BYPASSRLS; several of these tables are FORCE RLS) and returns
// nothing — the assertion is that db.Reset empties ALL of it, so there is
// nothing here to hand back for a targeted check.
func seedFullResetFixture(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	marker := "reset-fixture-" + uuid.NewString()

	var entityID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, marker, "99999999-"+uuid.NewString()[:4],
	).Scan(&entityID); err != nil {
		t.Fatalf("seed business_entities fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2)`,
		tenantID, entityID,
	); err != nil {
		t.Fatalf("seed import_batches fixture: %v", err)
	}

	var invoiceID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, issue_date,
		     supplier_tin, supplier_name, currency, subtotal, vat, total)
		 VALUES ($1, $2, $3, 'draft', '2026-01-01', '99999999-0001', $4, 'NGN', 100, 7.5, 107.5)
		 RETURNING id`,
		tenantID, entityID, marker, marker,
	).Scan(&invoiceID); err != nil {
		t.Fatalf("seed invoices fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total)
		 VALUES ($1, $2, 1, $3, 1, 100, 100)`,
		tenantID, invoiceID, marker,
	); err != nil {
		t.Fatalf("seed line_items fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1, $2, NULL, 'draft', $3)`,
		tenantID, invoiceID, marker,
	); err != nil {
		t.Fatalf("seed invoice_status_history fixture: %v", err)
	}

	var jobID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
		 VALUES ($1, $2, $3, 'firs-app', 'v1') RETURNING id`,
		tenantID, invoiceID, marker,
	).Scan(&jobID); err != nil {
		t.Fatalf("seed submission_jobs fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO app_exchange
		     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
		 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, 'v1')`,
		tenantID, jobID, invoiceID, marker,
	); err != nil {
		t.Fatalf("seed app_exchange fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`,
		tenantID, marker,
	); err != nil {
		t.Fatalf("seed idempotency_keys fixture: %v", err)
	}

	// submission_rate_limits is PK'd on tenant_id alone (at most one row per
	// tenant) — ON CONFLICT DO NOTHING tolerates the demo tenant already
	// having a ceiling row from a prior run.
	if _, err := pool.Exec(ctx,
		`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, 60)
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID,
	); err != nil {
		t.Fatalf("seed submission_rate_limits fixture: %v", err)
	}

	// Superuser BYPASSRLS insert, simulating residue the append-only trigger
	// would otherwise make impossible to clear — this is precisely the case
	// Reset's session_replication_role override exists for.
	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1, $2, $3)`,
		tenantID, marker, marker,
	); err != nil {
		t.Fatalf("seed audit_log fixture: %v", err)
	}

	// content_hash is CHECKed at exactly 64 chars. Left unreferenced by the
	// invoices row above: this only has to prove documents is truncated, and a
	// RESTRICT pointer would add nothing the single TRUNCATE does not already
	// exercise.
	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
		 VALUES ($1, $2, $3, 1, $4)`,
		tenantID, "reset-fixture/"+marker, fmt.Sprintf("%064d", 7), marker,
	); err != nil {
		t.Fatalf("seed documents fixture: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO river_job (kind, max_attempts, args) VALUES ($1, 25, '{}')`,
		marker,
	); err != nil {
		t.Fatalf("seed river_job fixture: %v", err)
	}
	// river_leader.name is constrained to the literal 'default' (River's own
	// migration 003, migrations/20260707193000_river_and_idempotency.sql:
	// "ADD CONSTRAINT name_length CHECK (name = 'default')") — there is only
	// ever one row here, so ON CONFLICT DO NOTHING tolerates one already
	// existing (a real River worker may hold leadership in this DB).
	if _, err := pool.Exec(ctx,
		`INSERT INTO river_leader (name, leader_id, elected_at, expires_at)
		 VALUES ('default', 'reset-test', now(), now() + interval '1 hour')
		 ON CONFLICT (name) DO NOTHING`,
	); err != nil {
		t.Fatalf("seed river_leader fixture: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO river_queue (name, updated_at) VALUES ($1, now())`,
		marker,
	); err != nil {
		t.Fatalf("seed river_queue fixture: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO river_notification (payload, topic) VALUES ('{}', $1)`,
		marker,
	); err != nil {
		t.Fatalf("seed river_notification fixture: %v", err)
	}
}

// countAllResetTargetTables returns the total row count summed across every
// table db.Reset truncates — 0 iff every one of them is empty.
func countAllResetTargetTables(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	total := 0
	for _, table := range resetTargetTables {
		total += mustCount(t, pool, `SELECT count(*) FROM `+table)
	}
	return total
}

// TestResetTruncatesEveryConfiguredTable: the central positive claim. Seeds
// one row into every table db.Reset is documented to truncate (chained
// through the real FK graph, including the audit_log row an ordinary DELETE
// could never remove), calls db.Reset, and asserts the combined row count
// across all of them is exactly 0 — proving both the FK-ordering (the
// multi-table TRUNCATE succeeds at all) and the audit_log trigger bypass
// (session_replication_role) actually work end to end, not just in isolation
// against a hand-run psql session.
//
// Restores the curated demo state afterward (db.Seed) so downstream tests in
// this package (seed_demo_test.go, provision_test.go) see the fixtures they
// expect regardless of run order — matching TestMigrateUpFromEmbedded's own
// "this test disturbs shared state, restore it" precedent.
func TestResetTruncatesEveryConfiguredTable(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state after reset test: %v", err)
		}
	})

	seedFullResetFixture(t, pool, demoTenantID)
	if before := countAllResetTargetTables(t, pool); before == 0 {
		t.Fatalf("precondition: 0 rows across the reset target tables after seeding a fixture in every one of them")
	}

	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if after := countAllResetTargetTables(t, pool); after != 0 {
		t.Errorf("rows remaining across the reset target tables after Reset = %d, want 0", after)
	}
}

// TestResetLeavesTenantsRulesAndMembershipsUntouched: the exclusion half of
// the same claim — tenants, memberships and the sealed rule_set_versions/rules
// tables must survive Reset completely unchanged, proving Reset's table list
// really is scoped to tenant DATA and not a blanket wipe.
func TestResetLeavesTenantsRulesAndMembershipsUntouched(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state after reset test: %v", err)
		}
	})

	tenantsBefore := mustCount(t, pool, `SELECT count(*) FROM tenants`)
	membershipsBefore := mustCount(t, pool, `SELECT count(*) FROM memberships`)
	rulesBefore := mustCount(t, pool, `SELECT count(*) FROM rules`)
	ruleSetVersionsBefore := mustCount(t, pool, `SELECT count(*) FROM rule_set_versions`)
	if tenantsBefore == 0 || rulesBefore == 0 || ruleSetVersionsBefore == 0 {
		t.Fatalf("precondition: expected pre-existing tenants/rules/rule_set_versions, got tenants=%d rules=%d rule_set_versions=%d",
			tenantsBefore, rulesBefore, ruleSetVersionsBefore)
	}

	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if got := mustCount(t, pool, `SELECT count(*) FROM tenants`); got != tenantsBefore {
		t.Errorf("count(tenants) after Reset = %d, want unchanged %d", got, tenantsBefore)
	}
	if got := mustCount(t, pool, `SELECT count(*) FROM memberships`); got != membershipsBefore {
		t.Errorf("count(memberships) after Reset = %d, want unchanged %d", got, membershipsBefore)
	}
	if got := mustCount(t, pool, `SELECT count(*) FROM rules`); got != rulesBefore {
		t.Errorf("count(rules) after Reset = %d, want unchanged %d", got, rulesBefore)
	}
	if got := mustCount(t, pool, `SELECT count(*) FROM rule_set_versions`); got != ruleSetVersionsBefore {
		t.Errorf("count(rule_set_versions) after Reset = %d, want unchanged %d", got, ruleSetVersionsBefore)
	}
}

// TestResetIsIdempotent: re-running Reset against an already-empty set of
// target tables (or calling it twice back to back) must never error — a
// redeploy of the same PR re-runs Provision, and Reset must tolerate that.
func TestResetIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state after reset test: %v", err)
		}
	})

	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("first Reset: %v", err)
	}
	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("second Reset (idempotency, already-empty tables): %v", err)
	}
}

// ---- Provision-level end-to-end: the real cmd/gateway/main.go wiring shape -

// TestProvisionResetWipesResidueThenReseedsCuratedDemo: the story-level claim
// (measured directly against the live pr-110 environment) — a PR fork that
// has accumulated E2E test residue converges to EXACTLY the curated 10-entity
// demo portfolio + its fixture invoices after one Provision call, not the
// residue plus the curated set. Uses the REAL field shape cmd/gateway/main.go
// constructs: Environment stays "development" (ENVIRONMENT forks verbatim —
// see ResetEnabled's doc comment), and RailwayEnvironmentName is the
// PR-shaped value that actually distinguishes the fork.
func TestProvisionResetWipesResidueThenReseedsCuratedDemo(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state after provision-reset test: %v", err)
		}
	})

	// Simulate the measured pr-110 problem: residue business_entities/invoices
	// under the demo tenant that are NOT part of the curated 10.
	residueTIN := "88888888-" + uuid.NewString()[:4]
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3)`,
		demoTenantID, "E2E test residue co", residueTIN,
	); err != nil {
		t.Fatalf("seed residue business_entities (precondition): %v", err)
	}

	cfg := db.ProvisionConfig{
		Environment:            "development", // ENVIRONMENT forks verbatim — see ResetEnabled doc comment
		BootstrapFlag:          "true",
		RailwayEnvironmentName: "pr-110",
		ResetFlag:              "true",
		SuperuserDSN:           superDSN,
		MigrationDSN:           migDSN,
		Passwords:              devRolePasswords(),
		BootstrapFS:            dbsql.FS,
		MigrationsFS:           migrations.FS,
		SeedFS:                 dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (pr-110-shaped, ResetFlag=true): %v", err)
	}

	entities := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(entities) != 10 {
		t.Fatalf("count(business_entities) for the demo tenant after Provision = %d, want exactly 10 (residue must be gone, curated set restored)", len(entities))
	}
	got := sortedEntityRows(entities)
	want := sortedEntityRows(curatedDemoEntities)
	if len(got) != len(want) {
		t.Fatalf("entity count mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entity[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if n := mustCount(t, pool, `SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, residueTIN); n != 0 {
		t.Errorf("residue business_entities row (tin=%s) survived Provision's reset, want 0, got %d", residueTIN, n)
	}
}

// ---- Provision-level negative gate coverage (the brief's stated priority) --
//
// "A test asserting the reset does NOT run for production/development/empty
// is more important than a test asserting it does run for pr-110." Each case
// below seeds a distinctive residue row, runs Provision with ResetFlag="true"
// but a RailwayEnvironmentName that must NOT satisfy the gate, and asserts
// the residue survives untouched.

// assertResidueSurvivesProvision seeds one throwaway business_entities row
// under the demo tenant, runs Provision with cfg, and asserts the row is
// still there afterward — proving Reset did NOT fire. Shared by every
// negative gate case below.
func assertResidueSurvivesProvision(t *testing.T, cfg db.ProvisionConfig, caseLabel string) {
	t.Helper()
	pool := bootstrapSuperuserPool(t, cfg.SuperuserDSN)
	ctx := context.Background()

	residueTIN := "77777777-" + uuid.NewString()[:4]
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3)`,
		demoTenantID, "gate-probe "+caseLabel, residueTIN,
	); err != nil {
		t.Fatalf("seed gate-probe residue (precondition): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, residueTIN)
	})

	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (%s): %v", caseLabel, err)
	}

	if n := mustCount(t, pool, `SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, residueTIN); n != 1 {
		t.Fatalf("%s: gate-probe residue row count after Provision = %d, want 1 (Reset must NOT have fired)", caseLabel, n)
	}
}

// TestProvisionResetRefusesPersistentEnvironmentName: RailwayEnvironmentName
// = "production" (the persistent environment's real post-rename name) with
// ResetFlag="true" must never trigger Reset.
func TestProvisionResetRefusesPersistentEnvironmentName(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	cfg := db.ProvisionConfig{
		Environment: "development", BootstrapFlag: "true",
		RailwayEnvironmentName: "production", ResetFlag: "true",
		SuperuserDSN: superDSN, MigrationDSN: migDSN, Passwords: devRolePasswords(),
		BootstrapFS: dbsql.FS, MigrationsFS: migrations.FS, SeedFS: dbsql.FS,
	}
	assertResidueSurvivesProvision(t, cfg, "RailwayEnvironmentName=production")
}

// TestProvisionResetRefusesDevelopmentEnvironmentName: RailwayEnvironmentName
// = "development" (a stale/misnamed environment, or the pre-2026-07-27 name)
// with ResetFlag="true" must never trigger Reset — the exact case the brief
// calls out as the safety margin ResetEnabled exists to provide.
func TestProvisionResetRefusesDevelopmentEnvironmentName(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	cfg := db.ProvisionConfig{
		Environment: "development", BootstrapFlag: "true",
		RailwayEnvironmentName: "development", ResetFlag: "true",
		SuperuserDSN: superDSN, MigrationDSN: migDSN, Passwords: devRolePasswords(),
		BootstrapFS: dbsql.FS, MigrationsFS: migrations.FS, SeedFS: dbsql.FS,
	}
	assertResidueSurvivesProvision(t, cfg, "RailwayEnvironmentName=development")
}

// TestProvisionResetRefusesEmptyRailwayEnvironmentName: an unset
// RAILWAY_ENVIRONMENT_NAME (empty string) with ResetFlag="true" must never
// trigger Reset — fail-closed on missing signal, exactly like BootstrapEnabled's
// own empty-environment case.
func TestProvisionResetRefusesEmptyRailwayEnvironmentName(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	cfg := db.ProvisionConfig{
		Environment: "development", BootstrapFlag: "true",
		RailwayEnvironmentName: "", ResetFlag: "true",
		SuperuserDSN: superDSN, MigrationDSN: migDSN, Passwords: devRolePasswords(),
		BootstrapFS: dbsql.FS, MigrationsFS: migrations.FS, SeedFS: dbsql.FS,
	}
	assertResidueSurvivesProvision(t, cfg, "RailwayEnvironmentName=<empty>")
}

// TestProvisionResetRefusesWhenFlagNotExactlyTrue: a PR-shaped
// RailwayEnvironmentName with ResetFlag anything other than the exact string
// "true" (unset, "TRUE", "1") must never trigger Reset.
func TestProvisionResetRefusesWhenFlagNotExactlyTrue(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	for _, flag := range []string{"", "TRUE", "1"} {
		t.Run("ResetFlag="+flag, func(t *testing.T) {
			cfg := db.ProvisionConfig{
				Environment: "development", BootstrapFlag: "true",
				RailwayEnvironmentName: "pr-999", ResetFlag: flag,
				SuperuserDSN: superDSN, MigrationDSN: migDSN, Passwords: devRolePasswords(),
				BootstrapFS: dbsql.FS, MigrationsFS: migrations.FS, SeedFS: dbsql.FS,
			}
			assertResidueSurvivesProvision(t, cfg, "ResetFlag="+flag+" (PR-shaped environment)")
		})
	}
}

// TestProvisionResetRefusesWhenBootstrapDisabled: a PR-shaped
// RailwayEnvironmentName with ResetFlag="true" but BootstrapFlag NOT "true"
// must never trigger Reset — Reset is nested under the bootstrap/seed guard
// (provision.go) so a reset is never left unpaired from the Seed call that
// would repopulate the database (see Provision's doc comment).
func TestProvisionResetRefusesWhenBootstrapDisabled(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	cfg := db.ProvisionConfig{
		Environment: "development", BootstrapFlag: "false",
		RailwayEnvironmentName: "pr-999", ResetFlag: "true",
		SuperuserDSN: superDSN, MigrationDSN: migDSN, Passwords: devRolePasswords(),
		BootstrapFS: dbsql.FS, MigrationsFS: migrations.FS, SeedFS: dbsql.FS,
	}
	assertResidueSurvivesProvision(t, cfg, "BootstrapFlag=false (PR-shaped environment, ResetFlag=true)")
}
