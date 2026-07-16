// M3-04-06 (Test-first: yes) — DB-backed contract tests for the Store's
// LoadActiveRuleSet + ToggleRule, written BEFORE the real implementation exists
// (store.go's methods are `panic("not implemented")` stubs). This suite is RED
// against that panic until the executor fills in the real SQL.
//
// Coverage (see M3-04-06 Test Specs):
//  1. TestStore_LoadActiveRuleSet        — active version + rules materialize into a RuleSet.
//  2. TestStore_LoadNoActiveErrors       — no active version -> ErrNoActiveRuleSet.
//  3. TestStore_ToggleFlipsAndAudits     — toggle flips enabled + writes exactly one audit row, same tx.
//  4. TestStore_ToggleLiveReload         — a fresh LoadActiveRuleSet sees the flip, no redeploy.
//  5. TestStore_ToggleAppliesCrossTenant — rules are GLOBAL: a toggle under tenant A is visible under tenant B.
//  6. TestStore_ToggleRedundant          — already-at-target toggle -> ErrRedundantTransition, no UPDATE, no audit row.
//  7. TestStore_ToggleUnknownKey         — unknown key under the active version -> ErrNotFound.
//  8. TestStore_AuditRollsBackWithToggle — a failed in-tx audit write rolls back the toggle too (atomicity).
//
// Fixtures are seeded as the SUPERUSER (bypasses the app-role grant, which is
// SELECT + UPDATE(enabled)-only — see schema_test.go's seedVersion/seedRule,
// reused here, plus this file's own seedFullRule for fixtures that need
// non-default field values).
//
// Isolation of the "at most one active rule_set_versions row" partial unique
// index across tests: this package never calls t.Parallel() (grep confirms no
// test file in internal/validation or internal/portfolio does either), so `go
// test` runs every test in this binary strictly sequentially. seedVersion's
// t.Cleanup DELETEs the version row (not just deactivates it) at the end of
// each test that seeds one, and t.Cleanup always runs — pass or fail — before
// the next test function starts. So by the time any later test's seedVersion
// call runs, no earlier test's active-version fixture can still exist. See
// TestStore_LoadNoActiveErrors below for the defensive check that makes this
// invariant loud (rather than assumed) at the one test that depends on
// "no active version" being literally true.
//
// Run: `make dev-db` once, then with the per-role DSNs set directly (see
// dbTestPools in schema_test.go):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestStore_' ./internal/validation/...
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ruleFixture is the full field set for seedFullRule below. Every field the
// zero value would otherwise leave ambiguous (Enabled, in particular — Go's
// bool zero value is false, which is NOT the table's `enabled` DEFAULT true)
// MUST be set explicitly by the caller; there is no "unset" sentinel here.
type ruleFixture struct {
	Key      string
	Type     string // defaults to "required" when empty
	Target   string
	Params   string // raw JSON text, e.g. `{}` or `{"min":1}`; defaults to "{}" when empty
	Severity string // defaults to "error" when empty
	When     *string
	Message  string // defaults to "qa fixture rule" when empty
	Scope    string // defaults to "document" when empty
	Enabled  bool
}

// seedFullRule inserts one rules row under versionID as the superuser, like
// schema_test.go's seedRule, but exposes every column so tests can assert
// field-mapping and toggle behavior precisely. No cleanup of its own: it is
// always reachable from the seedVersion call that produced versionID, whose
// cleanup cascades onto this row (rules.rule_set_version_id is ON DELETE
// CASCADE — see schema_test.go's seedVersion doc comment).
func seedFullRule(t *testing.T, super *pgxpool.Pool, versionID string, f ruleFixture) (id string) {
	t.Helper()
	ctx := context.Background()
	if f.Type == "" {
		f.Type = "required"
	}
	if f.Severity == "" {
		f.Severity = "error"
	}
	if f.Message == "" {
		f.Message = "qa fixture rule"
	}
	if f.Scope == "" {
		f.Scope = "document"
	}
	if f.Params == "" {
		f.Params = "{}"
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)
		 RETURNING id`,
		versionID, f.Key, f.Type, f.Target, f.Params, f.Severity, f.When, f.Message, f.Scope, f.Enabled,
	).Scan(&id); err != nil {
		t.Fatalf("seed full rule(key=%q): %v", f.Key, err)
	}
	return id
}

// auditCountTenant returns the count of audit_log rows for tenantID+event,
// scoped via the app pool + db.WithinTenantTx (FORCE RLS does the tenant
// filtering) — copied from internal/portfolio/portfolio_test.go's auditCount
// (~line 784), same env/pool convention.
func auditCountTenant(t *testing.T, pool *pgxpool.Pool, tenantID, event string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

// auditPayloadTenant returns the JSON payload of the most recent audit_log
// row for tenantID+event, scoped the same way as auditCountTenant.
func auditPayloadTenant(t *testing.T, pool *pgxpool.Pool, tenantID, event string) []byte {
	t.Helper()
	ctx := context.Background()
	var payload []byte
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&payload)
	}); err != nil {
		t.Fatalf("read audit_log payload: %v", err)
	}
	return payload
}

// TestStore_LoadActiveRuleSet (Test Spec #1): the active version's number and
// both its rules -- with every field (key/type/target/severity/message/scope
// /enabled, plus a structural check on params) -- must come back from
// LoadActiveRuleSet.
func TestStore_LoadActiveRuleSet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, version := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "rule-a", Type: "range", Target: "invoice.total", Params: `{"min":0,"max":100}`,
		Severity: "warning", Message: "total out of range", Scope: "document", Enabled: true,
	})
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "rule-b", Type: "required", Target: "supplier.tin", Params: `{}`,
		Severity: "error", Message: "TIN required", Scope: "document", Enabled: false,
	})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if rs.Version != version {
		t.Errorf("RuleSet.Version = %d, want %d", rs.Version, version)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("len(RuleSet.Rules) = %d, want 2", len(rs.Rules))
	}

	byKey := map[string]Rule{}
	for _, r := range rs.Rules {
		byKey[r.Key] = r
	}

	a, ok := byKey["rule-a"]
	if !ok {
		t.Fatal("rule-a not found in loaded RuleSet.Rules")
	}
	if a.Type != TypeRange || a.Target != "invoice.total" || a.Severity != Severity("warning") ||
		a.Message != "total out of range" || a.Scope != "document" || !a.Enabled {
		t.Errorf("rule-a = %+v, want type=range target=invoice.total severity=warning message=%q scope=document enabled=true",
			a, "total out of range")
	}
	var aParams map[string]any
	if err := json.Unmarshal(a.Params, &aParams); err != nil {
		t.Fatalf("unmarshal rule-a params %s: %v", a.Params, err)
	}
	if aParams["min"] != float64(0) || aParams["max"] != float64(100) {
		t.Errorf("rule-a params = %v, want min=0 max=100", aParams)
	}

	b, ok := byKey["rule-b"]
	if !ok {
		t.Fatal("rule-b not found in loaded RuleSet.Rules")
	}
	if b.Type != TypeRequired || b.Target != "supplier.tin" || b.Severity != Severity("error") ||
		b.Message != "TIN required" || b.Scope != "document" || b.Enabled {
		t.Errorf("rule-b = %+v, want type=required target=supplier.tin severity=error message=%q scope=document enabled=false",
			b, "TIN required")
	}
}

// TestStore_LoadNoActiveErrors (Test Spec #2): with no rule_set_versions row
// carrying is_active=true, LoadActiveRuleSet must return ErrNoActiveRuleSet
// (errors.Is). See file header for why no earlier test's active-version
// fixture can still be present when this test runs; this pre-check makes
// that invariant a loud failure (with a fix pointer) instead of a silent
// false pass/fail if it is ever violated (e.g. a leaked fixture from a
// crashed prior run, or real content seeded by hand in the local dev DB).
func TestStore_LoadNoActiveErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// The migrations ship a permanent active version, so "no active version" is
	// no longer the migrated DB's natural resting state -- CAPTURE whichever row
	// holds it, deactivate it here, and restore THAT ROW BY ID on cleanup
	// (independent of seedVersion, since this test seeds no fixture of its own)
	// so the ErrNoActiveRuleSet path is genuinely exercised.
	//
	// Capturing the id rather than naming `version = 1` is the point: the
	// hardcode was correct only while v1 was the active version, and would
	// otherwise deactivate nothing (leaving the real active version up, so this
	// test asserts ErrNoActiveRuleSet against a DB that still HAS one) while
	// reactivating the wrong row on the way out (RS-V2-12).
	var activeID string
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&activeID); err != nil {
		t.Fatalf("capture the active version id: %v", err)
	}
	if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET is_active = false WHERE id = $1`, activeID); err != nil {
		t.Fatalf("deactivate the active version (id=%s): %v", activeID, err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rule_set_versions SET is_active = true WHERE id = $1`, activeID,
		); err != nil {
			t.Errorf("cleanup: restore the active version (id=%s): %v", activeID, err)
		}
	})

	// The leaked-fixture pre-check excludes the row just deactivated (by id, the
	// same discovered value) -- it still catches a real leaked fixture (or stray
	// hand-seeded content) from a prior test/run, just not the sanctioned seed
	// this test is about to deactivate.
	var leaked int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rule_set_versions WHERE is_active AND id <> $1`, activeID,
	).Scan(&leaked); err != nil {
		t.Fatalf("check pre-existing active version: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("found %d pre-existing is_active=true rule_set_versions row(s) (other than the sanctioned seed) -- a prior "+
			"test leaked an active fixture (or the dev DB has real seeded content); reset the local DB and re-run", leaked)
	}

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	_, err := store.LoadActiveRuleSet(c)
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Fatalf("LoadActiveRuleSet with no active version: err = %v, want ErrNoActiveRuleSet", err)
	}
}

// TestStore_ToggleFlipsAndAudits (Test Spec #3): ToggleRule(key, false) on an
// enabled rule must (a) return the rule with Enabled=false, and (b) write
// exactly one "validation.rule.disabled" audit_log row, in the SAME
// transaction, whose payload carries the rule's key and the active version.
func TestStore_ToggleFlipsAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, version := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	userID := "user-1"
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "validation.rule.disabled"
	before := auditCountTenant(t, app, tenantID, event)

	got, err := store.ToggleRule(c, "R", false)
	if err != nil {
		t.Fatalf("ToggleRule(R,false): %v", err)
	}
	if got.Key != "R" {
		t.Errorf("ToggleRule(R,false).Key = %q, want %q", got.Key, "R")
	}
	if got.Enabled {
		t.Error("ToggleRule(R,false).Enabled = true, want false")
	}

	after := auditCountTenant(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}

	payload := auditPayloadTenant(t, app, tenantID, event)
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	if p["key"] != "R" {
		t.Errorf("audit payload key = %v, want %q", p["key"], "R")
	}
	v, ok := p["version"].(float64)
	if !ok || int(v) != version {
		t.Errorf("audit payload version = %v, want %d", p["version"], version)
	}
}

// TestStore_ToggleLiveReload (Test Spec #4): after ToggleRule(R,false)
// commits, a FRESH LoadActiveRuleSet call must see R.Enabled=false -- no
// redeploy, no cache to bust, the toggle is read straight from the table.
func TestStore_ToggleLiveReload(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	if _, err := store.ToggleRule(c, "R", false); err != nil {
		t.Fatalf("ToggleRule(R,false): %v", err)
	}

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet after toggle: %v", err)
	}
	var found bool
	for _, r := range rs.Rules {
		if r.Key != "R" {
			continue
		}
		found = true
		if r.Enabled {
			t.Error("LoadActiveRuleSet after ToggleRule(R,false): R.Enabled = true, want false (no redeploy)")
		}
	}
	if !found {
		t.Fatal("R not present in RuleSet.Rules after toggle")
	}
}

// TestStore_ToggleAppliesCrossTenant (Test Spec #5): rules are a GLOBAL
// reference table, not tenant-scoped (Decision N1/N14) -- toggling R off
// under tenant A's identity must be visible to a LoadActiveRuleSet call made
// under tenant B's (distinct) identity. This is the test that would fail if
// ToggleRule/LoadActiveRuleSet were ever mistakenly scoped by tenant_id (they
// have no tenant_id column to scope by -- but a regression could add a
// WHERE that assumes one, or filter on the wrong table).
func TestStore_ToggleAppliesCrossTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: "user-a", Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: "user-b", Role: "authenticated", TenantID: tenantB})

	if _, err := store.ToggleRule(cA, "R", false); err != nil {
		t.Fatalf("ToggleRule(R,false) under tenant A: %v", err)
	}

	rs, err := store.LoadActiveRuleSet(cB)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet under tenant B: %v", err)
	}
	var found bool
	for _, r := range rs.Rules {
		if r.Key != "R" {
			continue
		}
		found = true
		if r.Enabled {
			t.Error("R.Enabled = true under tenant B after a toggle-off under tenant A -- rules must be GLOBAL, not tenant-isolated")
		}
	}
	if !found {
		t.Fatal("R not present in RuleSet.Rules under tenant B")
	}
}

// TestStore_ToggleRedundant (Test Spec #6): a rule already at the requested
// enabled value must return ErrRedundantTransition, write no UPDATE, and
// write no audit row -- the same guard shape as portfolio.Store.SetStatus.
func TestStore_ToggleRedundant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: false}) // already disabled

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	const event = "validation.rule.disabled"
	before := auditCountTenant(t, app, tenantID, event)

	_, err := store.ToggleRule(c, "R", false)
	if !errors.Is(err, ErrRedundantTransition) {
		t.Fatalf("ToggleRule(R,false) on already-disabled rule: err = %v, want ErrRedundantTransition", err)
	}

	after := auditCountTenant(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (redundant toggle must write no audit row)", event, after, before)
	}
}

// TestStore_ToggleUnknownKey (Test Spec #7): a key that does not match any
// rule under the active version must return ErrNotFound.
func TestStore_ToggleUnknownKey(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	_, err := store.ToggleRule(c, "Z", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ToggleRule(Z,false) unknown key: err = %v, want ErrNotFound", err)
	}
}

// TestStore_AuditRollsBackWithToggle (Test Spec #8): proves ToggleRule's
// UPDATE and its audit.Record call share one transaction. Mechanism: call
// ToggleRule under an identity whose Subject is "" -- WithinRequestTenantTx
// only requires a valid TenantID uuid to proceed (Subject is not validated
// there), so the tx opens and the UPDATE runs, but audit.Record's
// `INSERT INTO audit_log (actor, ...) VALUES (”, ...)` then violates
// audit_log's `audit_actor_length` CHECK (char_length(actor) > 0) --
// migrations/20260708062657_audit_log.sql:56 -- and errors. Because
// ToggleRule must run both statements inside ONE db.WithinRequestTenantTx
// closure (same shape as portfolio.Store.Create/Update/SetStatus), that
// error rolls back the whole transaction: the UPDATE must NOT be durable,
// and NO audit row (of any event) must have been written for this tenant.
func TestStore_AuditRollsBackWithToggle(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	ruleID := seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})

	_, err := store.ToggleRule(c, "R", false)
	if err == nil {
		t.Fatal("ToggleRule with empty-actor identity: want an error (audit CHECK violation), got nil")
	}

	var enabled bool
	if err := super.QueryRow(ctx, `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&enabled); err != nil {
		t.Fatalf("read back enabled: %v", err)
	}
	if !enabled {
		t.Error("rule enabled = false after a failed ToggleRule -- the UPDATE was not rolled back with the failed audit write")
	}

	var auditRows int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'validation.rule.%'`, tenantID,
	).Scan(&auditRows); err != nil {
		t.Fatalf("count audit_log for tenant: %v", err)
	}
	if auditRows != 0 {
		t.Errorf("audit_log rows for tenant = %d, want 0 (failed audit write must roll back, leaving no row)", auditRows)
	}
}

// TestStore_LoadNoIdentityErrors (QA adversarial): rule_set_versions/rules are
// GLOBAL tables (no tenant_id, no RLS -- see store.go's file header), but
// both Store methods still wrap db.WithinRequestTenantTx purely to resolve
// the caller's identity for audit.Record. That wrapper's gate must still
// apply even though the underlying data is not tenant-scoped: a context
// carrying NO auth.Identity must be refused with db.ErrNoTenant BEFORE any
// SQL (including the UPDATE and the audit write) runs -- "global" means every
// tenant sees the same content, not that access requires no identity.
func TestStore_LoadNoIdentityErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background() // deliberately NOT auth.WithIdentity -- no identity in ctx

	versionID, _ := seedVersion(t, super, true)
	ruleID := seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)

	if _, err := store.LoadActiveRuleSet(ctx); !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("LoadActiveRuleSet with no identity: err = %v, want db.ErrNoTenant", err)
	}

	var auditBefore int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&auditBefore); err != nil {
		t.Fatalf("count audit_log before: %v", err)
	}

	if _, err := store.ToggleRule(ctx, "R", false); !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("ToggleRule with no identity: err = %v, want db.ErrNoTenant", err)
	}

	// Delta (not an absolute-zero assertion): audit_log accumulates rows
	// across every test in this binary run, so only "did THIS call add a
	// row" is a safe invariant to check.
	var auditAfter int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&auditAfter); err != nil {
		t.Fatalf("count audit_log after: %v", err)
	}
	if auditAfter != auditBefore {
		t.Errorf("audit_log row count changed %d -> %d after a no-identity ToggleRule, want unchanged -- "+
			"the tenant-tx gate must refuse before WithinTenantTx (and therefore audit.Record) ever runs", auditBefore, auditAfter)
	}

	var enabled bool
	if err := super.QueryRow(context.Background(), `SELECT enabled FROM rules WHERE id = $1`, ruleID).Scan(&enabled); err != nil {
		t.Fatalf("read back enabled: %v", err)
	}
	if !enabled {
		t.Error("rule enabled = false after a no-identity ToggleRule -- the tenant-tx gate should have refused before any UPDATE ran")
	}
}

// TestStore_ToggleRoundTripEvents (QA adversarial): disable then re-enable
// the same rule and confirm the audit trail is a faithful, ORDERED history
// of both flips -- not just "one row exists" (TestStore_ToggleFlipsAndAudits
// only ever disables once). Asserts event names, from/to payload fields, and
// that the rule really is back to enabled=true after the round trip.
func TestStore_ToggleRoundTripEvents(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, version := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	if _, err := store.ToggleRule(c, "R", false); err != nil {
		t.Fatalf("ToggleRule(R,false): %v", err)
	}
	if _, err := store.ToggleRule(c, "R", true); err != nil {
		t.Fatalf("ToggleRule(R,true): %v", err)
	}

	type auditRow struct {
		Event   string
		Payload map[string]any
	}
	var got []auditRow
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		// ORDER BY id (bigserial, monotonic) rather than created_at -- two
		// separate transactions in quick succession could land the same
		// timestamptz value on a fast machine.
		rows, err := tx.Query(ctx, `SELECT event, payload FROM audit_log WHERE event LIKE 'validation.rule.%' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event string
			var payload []byte
			if err := rows.Scan(&event, &payload); err != nil {
				return err
			}
			var p map[string]any
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			got = append(got, auditRow{Event: event, Payload: p})
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read audit_log rows for tenant: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("audit_log rows for tenant = %d, want 2 (disable then enable)", len(got))
	}
	if got[0].Event != "validation.rule.disabled" {
		t.Errorf("audit_log[0].event = %q, want validation.rule.disabled", got[0].Event)
	}
	if got[0].Payload["from"] != true || got[0].Payload["to"] != false {
		t.Errorf("audit_log[0].payload from/to = %v/%v, want true/false", got[0].Payload["from"], got[0].Payload["to"])
	}
	if got[1].Event != "validation.rule.enabled" {
		t.Errorf("audit_log[1].event = %q, want validation.rule.enabled", got[1].Event)
	}
	if got[1].Payload["from"] != false || got[1].Payload["to"] != true {
		t.Errorf("audit_log[1].payload from/to = %v/%v, want false/true", got[1].Payload["from"], got[1].Payload["to"])
	}
	for i, r := range got {
		v, ok := r.Payload["version"].(float64)
		if !ok || int(v) != version {
			t.Errorf("audit_log[%d].payload version = %v, want %d", i, r.Payload["version"], version)
		}
		if r.Payload["key"] != "R" {
			t.Errorf("audit_log[%d].payload key = %v, want %q", i, r.Payload["key"], "R")
		}
	}

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet after round trip: %v", err)
	}
	var found bool
	for _, r := range rs.Rules {
		if r.Key != "R" {
			continue
		}
		found = true
		if !r.Enabled {
			t.Error("R.Enabled = false after disable-then-enable round trip, want true")
		}
	}
	if !found {
		t.Fatal("R not present in RuleSet.Rules after round trip")
	}
}

// TestStore_ToggleRunsAsAppRole (QA adversarial): every other test in this
// file already calls NewStore(app) where app is dbTestPools' DATABASE_URL
// pool -- i.e. they already run as invoice_app, not the superuser. This test
// makes that fact an explicit, checked assertion (current_user) rather than
// an unverified convention, and proves ToggleRule's UPDATE genuinely
// succeeds under the real production role's column-level grant (SELECT +
// UPDATE(enabled) only -- schema_test.go's TestSchema_AppCanToggleEnabled)
// plus its FOR UPDATE row lock, not because the test happens to run
// privileged.
func TestStore_ToggleRunsAsAppRole(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	var currentUser string
	if err := app.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("select current_user on the app pool: %v", err)
	}
	if currentUser != "invoice_app" {
		t.Fatalf("dbTestPools' app pool runs as %q, want invoice_app -- Store tests would be exercising the wrong role's grants entirely", currentUser)
	}

	versionID, _ := seedVersion(t, super, true)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	got, err := store.ToggleRule(c, "R", false)
	if err != nil {
		t.Fatalf("ToggleRule as invoice_app: %v -- the column-level UPDATE(enabled) grant + FOR UPDATE lock must permit this mutation for the real production role", err)
	}
	if got.Enabled {
		t.Error("ToggleRule(R,false).Enabled = true, want false")
	}
}

// TestStore_LoadOrdersAndRoundTripsFields (QA adversarial): seeds three rules
// out of key order and one carrying a non-null "when" guard plus a
// structured `params` blob, then asserts LoadActiveRuleSet (a) returns them
// sorted ORDER BY key (not insertion order), and (b) faithfully round-trips
// When (*string, non-nil), Params, Scope, Type, and Severity -- fields
// TestStore_LoadActiveRuleSet does not exercise (no "when", no ordering
// check, only two rules already alphabetical).
func TestStore_LoadOrdersAndRoundTripsFields(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, true)
	whenExpr := "invoice.total > 0"
	paramsJSON := `{"expr":"invoice.total > 0 && invoice.total < 1000000"}`
	// Seeded out of key order (zulu, alpha, mike) to prove LoadActiveRuleSet's
	// `ORDER BY key` (store.go), not insertion order.
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "zulu", Type: "cel", Target: "invoice", Params: paramsJSON,
		Severity: "info", When: &whenExpr, Message: "cel rule", Scope: "document", Enabled: true,
	})
	seedFullRule(t, super, versionID, ruleFixture{Key: "alpha", Enabled: true})
	seedFullRule(t, super, versionID, ruleFixture{Key: "mike", Enabled: true})

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if len(rs.Rules) != 3 {
		t.Fatalf("len(RuleSet.Rules) = %d, want 3", len(rs.Rules))
	}

	gotKeys := []string{rs.Rules[0].Key, rs.Rules[1].Key, rs.Rules[2].Key}
	wantKeys := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("RuleSet.Rules key order = %v, want %v (ORDER BY key, not insertion order)", gotKeys, wantKeys)
	}

	zulu := rs.Rules[2]
	if zulu.Type != TypeCEL {
		t.Errorf("zulu.Type = %q, want %q", zulu.Type, TypeCEL)
	}
	if zulu.Severity != Severity("info") {
		t.Errorf("zulu.Severity = %q, want info", zulu.Severity)
	}
	if zulu.Scope != "document" {
		t.Errorf("zulu.Scope = %q, want document", zulu.Scope)
	}
	if zulu.When == nil {
		t.Fatal(`zulu.When = nil, want non-nil (a "when" guard was seeded)`)
	}
	if *zulu.When != whenExpr {
		t.Errorf("zulu.When = %q, want %q", *zulu.When, whenExpr)
	}

	// jsonb re-serializes on round trip (e.g. Postgres inserts a space after
	// ':', and does not guarantee the on-disk key order matches input text),
	// so a literal byte comparison against the seeded input string is not a
	// meaningful invariant. Decode both sides and compare the resulting
	// values instead -- that is what "Params round-trips faithfully" means.
	var wantParams, gotParams map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &wantParams); err != nil {
		t.Fatalf("unmarshal seeded params: %v", err)
	}
	if err := json.Unmarshal(zulu.Params, &gotParams); err != nil {
		t.Fatalf("unmarshal loaded params %s: %v", zulu.Params, err)
	}
	if !reflect.DeepEqual(wantParams, gotParams) {
		t.Errorf("zulu.Params decoded = %v, want %v", gotParams, wantParams)
	}

	// alpha/mike were seeded with no "when" guard -- confirm the nullable
	// column's zero case round-trips to a true nil *string, not a pointer to
	// an empty string.
	for _, key := range []string{"alpha", "mike"} {
		for _, r := range rs.Rules {
			if r.Key != key {
				continue
			}
			if r.When != nil {
				t.Errorf("%s.When = %q, want nil (no when guard seeded)", key, *r.When)
			}
		}
	}
}
