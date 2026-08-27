// M4-07-01 (task-155): tests for internal/dashboard's Store.Rollup, written
// BEFORE the real implementation exists (RED against the not-implemented
// stub body in store.go). Store.Rollup wraps db.WithinRequestTenantTx (System
// Design table, M4-07-01 story) — RLS scopes tenant, so no manual `WHERE
// tenant_id` appears in any assertion query run through the app-role pool;
// the superuser pool is used only for seeding/mutating rows out-of-band
// (bypasses RLS, so it needs no tenant context).
//
// Spec-to-test map (Test Specs table, M4-07-01 story / task-155):
//
//	DASH-01 TestStoreRollup_AllSevenStatesReported
//	DASH-02 TestStoreRollup_ZerosAreReportedNotOmitted
//	DASH-03 TestStoreRollup_EmptyTenant
//	DASH-04 TestStoreRollup_TotalsEqualSumOfClients
//	DASH-05 TestStoreRollup_BrokenDraftCountsAsNeedsAttention
//	DASH-06 TestStoreRollup_WarningOnlyDraftIsNotNeedsAttention
//	DASH-07 TestStoreRollup_CleanDraftIsNotNeedsAttention
//	DASH-08 TestStoreRollup_RejectedAndFailedCountAsNeedsAttention
//	DASH-09 TestStoreRollup_ExceptionsFirstOrdering
//	DASH-10 TestStoreRollup_NameTieBreakAtEqualNeed
//	DASH-11 TestStoreRollup_EntityNameIsJoinedNotLookedUp
//	DASH-12 TestStoreRollup_EntityWithNoInvoicesIsAbsent
//	DASH-13 TestStoreRollup_LiveStateChangeIsReflected
//	DASH-15 TestStoreRollup_NoIdentityFailsClosed
//
// DASH-14 (TestRLS_DashboardRollupCrossTenantIsolated) lives in
// cross_tenant_integration_test.go.
//
// Run: `make test-rls`, or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/dashboard/...
package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- shared DB-test harness (mirrors internal/invoice/store_test.go's
// dbTestPools/seedTenant/seedEntity/seedInvoice idiom, package-local per the
// repo's established per-package-duplication convention — see the M4-07-01
// story's Stage 2 Explore findings) --------------------------------------

// dbTestPools returns the superuser (seed) and app-role (Store) pools for
// the dashboard db-integration suite below, or skips the test when the
// per-role DSNs are unset.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("dashboard db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// memberSubject is the caller every DB-backed test in this package acts as.
// Its membership row is a no-op today (a rowless caller is still admitted)
// but keeps these fixtures ready for the predicate's strict successor.
const memberSubject = "d4a10001-0000-4000-8000-000000000001"

// seedTenant inserts one throwaway tenants row (kind 'firm') as the
// superuser and registers a cleanup that deletes it. A tenants delete
// CASCADEs away every business_entities/invoices/memberships row scoped to
// it, so per-test cleanup never has to unwind child rows by hand.
func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'preparer', 'active')`,
		id, memberSubject,
	); err != nil {
		t.Fatalf("seed caller membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

// TestDashboardFixture_SeedTenantSeedsAnActiveCallerMembership fails if
// seedTenant's membership INSERT is silently dropped.
func TestDashboardFixture_SeedTenantSeedsAnActiveCallerMembership(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "fixture membership check")

	var status string
	err := super.QueryRow(context.Background(),
		`SELECT status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, memberSubject,
	).Scan(&status)
	if err != nil {
		t.Fatalf("query caller membership: %v", err)
	}
	if status != "active" {
		t.Fatalf("status = %q, want active", status)
	}
}

// seedEntity inserts one business_entities row for tenantID as the
// superuser (BYPASSRLS) and registers its own cleanup (belt-and-suspenders
// alongside the tenant-cascade above).
func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// seedInvoice inserts one invoices row directly (bypassing any Store write
// path -- Store.Rollup is read-only) as the superuser: born 'draft', with
// violations defaulting to '[]' per the column DEFAULT.
func seedInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, entityID, number,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// seedInvoiceAtStatus seeds a normal draft invoice (via seedInvoice) then,
// unless status is "draft" itself, force-writes invoices.status directly as
// the superuser -- mirrors internal/invoice/transition_adversarial_test.go's
// seedInvoiceAtStatus. invoices.status's CHECK constraint only enumerates
// the 7 values (no state-machine awareness at the schema layer), so any raw
// value here is accepted regardless of any app-level transition legality.
func seedInvoiceAtStatus(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, status string) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if status != "draft" {
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET status = $1 WHERE id = $2`, status, id,
		); err != nil {
			t.Fatalf("force-seed invoice status to %q: %v", status, err)
		}
	}
	return id
}

// seedInvoiceWithViolations seeds an invoice (via seedInvoice) then
// force-writes BOTH status and violations directly as the superuser -- the
// same force-write idiom seedInvoiceAtStatus uses, extended to violations
// since no Store write path exists yet to drive status+violations together
// (Store.Rollup is read-only). violationsJSON must be a well-formed jsonb
// array literal, e.g. `[]` or
// `[{"rule_key":"x","severity":"error","message":"y"}]` (shape per
// internal/invoice/validator.go's Violation: rule_key, severity, message,
// path).
func seedInvoiceWithViolations(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, status, violationsJSON string) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = $1, violations = $2::jsonb WHERE id = $3`,
		status, violationsJSON, id,
	); err != nil {
		t.Fatalf("force-seed invoice status/violations: %v", err)
	}
	return id
}

// seedResolvedFailed seeds a `failed` invoice then force-writes the
// kept_as_is triple directly as the superuser -- package-local per the
// established per-package-duplication convention (mirrors
// internal/invoice's own seedResolvedFailed).
func seedResolvedFailed(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	id := seedInvoiceAtStatus(t, super, tenantID, entityID, number, "failed")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'qa-fixture', kept_as_is_reason = 'resolved outside' WHERE id = $1`,
		id,
	); err != nil {
		t.Fatalf("seed resolved-outside triple: %v", err)
	}
	return id
}

// --- approval fixtures -----------------------------------------------------

// seedApprovalPolicy inserts one approval_policies row plus one
// approval_policy_versions row for tenantID. When active, `sealed` is written
// first: approval_policy_versions_active_is_sealed forbids an active-but-unsealed
// row, and approval_policy_versions_one_active caps the tenant at one active
// version, so a tenant may take active=true only once.
func seedApprovalPolicy(t *testing.T, super *pgxpool.Pool, tenantID string, active bool) (policyID, versionID string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, "dashboard fixture policy "+uuid.NewString(),
	).Scan(&policyID); err != nil {
		t.Fatalf("seed approval_policies: %v", err)
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, sealed)
		 VALUES ($1, $2, 1, $3) RETURNING id`,
		tenantID, policyID, active,
	).Scan(&versionID); err != nil {
		t.Fatalf("seed approval_policy_versions: %v", err)
	}
	if active {
		if _, err := super.Exec(ctx,
			`UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, versionID,
		); err != nil {
			t.Fatalf("activate approval_policy_versions: %v", err)
		}
	}
	t.Cleanup(func() { dropApprovalRows(super, tenantID) })
	return policyID, versionID
}

// activateApprovalVersion flips a sealed version's is_active on. Sealing first is
// the same ordering constraint seedApprovalPolicy documents.
func activateApprovalVersion(t *testing.T, super *pgxpool.Pool, versionID string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET sealed = true WHERE id = $1`, versionID,
	); err != nil {
		t.Fatalf("seal approval_policy_versions: %v", err)
	}
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, versionID,
	); err != nil {
		t.Fatalf("activate approval_policy_versions: %v", err)
	}
}

// seedApprovalRun inserts one approval_runs row at `state` against invoiceID.
// Its own cleanup must outrank the invoice's: approval_runs -> invoices is ON
// DELETE RESTRICT, so a surviving run silently blocks seedInvoice's delete.
func seedApprovalRun(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID, versionID, state string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, state, content_fingerprint)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, invoiceID, versionID, state, "dashboard-fixture-"+uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_runs (state=%q): %v", state, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM approval_runs WHERE id = $1`, id)
	})
	return id
}

// seedApprovalRunAt seeds a run and then force-writes its opened_at. opened_at
// defaults to now(), so insert order and recency coincide and a fixture meant to
// separate them silently cannot.
func seedApprovalRunAt(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID, versionID, state string, openedAt time.Time) string {
	t.Helper()
	id := seedApprovalRun(t, super, tenantID, invoiceID, versionID, state)
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET opened_at = $1 WHERE id = $2`, openedAt, id)
	if err != nil {
		t.Fatalf("set approval_runs.opened_at: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set approval_runs.opened_at affected %d rows, want 1", tag.RowsAffected())
	}
	return id
}

// dropApprovalRows clears a tenant's approval tables bottom-up with triggers and
// FK checks off -- approval_policy_versions_seal_guard refuses to DELETE a sealed
// row, and that refusal also blocks seedTenant's cascading tenant delete. Mirrors
// internal/approval's teardownSealedApprovalFixture.
func dropApprovalRows(super *pgxpool.Pool, tenantID string) {
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		return
	}
	for _, table := range []string{
		"approval_decisions", "approval_run_steps", "approval_runs",
		"approval_policy_steps", "approval_policy_versions", "approval_policies",
	} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			return
		}
	}
	_ = tx.Commit(ctx)
}

// countApprovalRuns reads a tenant's run count as the superuser -- used to prove a
// fixture really has ZERO runs rather than assuming it.
func countApprovalRuns(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_runs WHERE tenant_id = $1`, tenantID,
	).Scan(&n); err != nil {
		t.Fatalf("count approval_runs: %v", err)
	}
	return n
}

// --- DASH-01..13, DASH-15 --------------------------------------------------

// DASH-01: tenant A with one entity and one invoice in each of the 7
// states must produce a single Client row whose Counts has 1 in every
// field.
func TestStoreRollup_AllSevenStatesReported(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-01 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-01 entity")

	for i, status := range []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"} {
		seedInvoiceAtStatus(t, super, tenantID, entityID, fmt.Sprintf("DASH-01-%d", i), status)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.EntityID != entityID {
		t.Errorf("EntityID = %q, want %q", row.EntityID, entityID)
	}
	want := Counts{Draft: 1, Validated: 1, Queued: 1, Submitted: 1, Accepted: 1, Rejected: 1, Failed: 1}
	if row.Counts != want {
		t.Errorf("Counts = %+v, want %+v", row.Counts, want)
	}
}

// DASH-02: a tenant with one entity holding exactly one draft must
// marshal with "rejected":0 and "failed":0 present -- not omitted.
func TestStoreRollup_ZerosAreReportedNotOmitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-02 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-02 entity")
	seedInvoice(t, super, tenantID, entityID, "DASH-02-1")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"rejected":0`)) {
		t.Errorf("marshalled body = %s, want it to contain \"rejected\":0", body)
	}
	if !bytes.Contains(body, []byte(`"failed":0`)) {
		t.Errorf("marshalled body = %s, want it to contain \"failed\":0", body)
	}
}

// DASH-03: a tenant with no entities and no invoices must produce a
// non-nil empty Clients slice, all-zero Totals, and "clients":[] (never
// null) when marshalled.
func TestStoreRollup_EmptyTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-03 tenant")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Clients == nil {
		t.Error("Clients is nil, want a non-nil empty slice")
	}
	if len(got.Clients) != 0 {
		t.Errorf("Clients = %d rows, want 0", len(got.Clients))
	}
	if got.Totals.Counts != (Counts{}) {
		t.Errorf("Totals.Counts = %+v, want all zero", got.Totals.Counts)
	}
	if got.Totals.NeedsAttention != 0 {
		t.Errorf("Totals.NeedsAttention = %d, want 0", got.Totals.NeedsAttention)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"clients":[]`)) {
		t.Errorf("marshalled body = %s, want it to contain \"clients\":[]", body)
	}
	if bytes.Contains(body, []byte(`"clients":null`)) {
		t.Errorf("marshalled body = %s, want \"clients\":[] not null", body)
	}
}

// DASH-04: with 3 entities holding a mixed spread of states, Totals must
// equal the element-wise sum of Clients. Also pins the exact known-seeded
// totals as a hard-coded oracle -- summing Clients back into Totals alone
// would pass even if Store.Rollup miscounted every row identically (e.g.
// always reporting draft:0), since the same bug would shift both the
// manual sum and Totals in lockstep.
func TestStoreRollup_TotalsEqualSumOfClients(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-04 tenant")
	e1 := seedEntity(t, super, tenantID, "DASH-04 entity 1")
	e2 := seedEntity(t, super, tenantID, "DASH-04 entity 2")
	e3 := seedEntity(t, super, tenantID, "DASH-04 entity 3")

	seedInvoiceAtStatus(t, super, tenantID, e1, "DASH-04-1a", "draft")
	seedInvoiceAtStatus(t, super, tenantID, e1, "DASH-04-1b", "validated")
	seedInvoiceAtStatus(t, super, tenantID, e2, "DASH-04-2a", "rejected")
	seedInvoiceAtStatus(t, super, tenantID, e2, "DASH-04-2b", "accepted")
	seedInvoiceAtStatus(t, super, tenantID, e3, "DASH-04-3a", "failed")
	seedInvoiceAtStatus(t, super, tenantID, e3, "DASH-04-3b", "failed")

	// One validated invoice per entity under an active policy, and an approved run
	// on e3's -- so awaiting_approval (2) differs from counts.validated (3) and the
	// oracle cannot be satisfied by wiring the new field to the validated count.
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)
	seedInvoiceAtStatus(t, super, tenantID, e2, "DASH-04-2c", "validated")
	e3Validated := seedInvoiceAtStatus(t, super, tenantID, e3, "DASH-04-3c", "validated")
	seedApprovalRun(t, super, tenantID, e3Validated, versionID, "approved")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 3 {
		t.Fatalf("Clients = %d rows, want 3", len(got.Clients))
	}

	var wantCounts Counts
	var wantNeeds int
	var wantAwaiting int
	for _, c := range got.Clients {
		wantCounts.Draft += c.Counts.Draft
		wantCounts.Validated += c.Counts.Validated
		wantCounts.Queued += c.Counts.Queued
		wantCounts.Submitted += c.Counts.Submitted
		wantCounts.Accepted += c.Counts.Accepted
		wantCounts.Rejected += c.Counts.Rejected
		wantCounts.Failed += c.Counts.Failed
		wantNeeds += c.NeedsAttention
		wantAwaiting += c.AwaitingApproval
	}
	if got.Totals.Counts != wantCounts {
		t.Errorf("Totals.Counts = %+v, want %+v (element-wise sum of Clients)", got.Totals.Counts, wantCounts)
	}
	if got.Totals.NeedsAttention != wantNeeds {
		t.Errorf("Totals.NeedsAttention = %d, want %d (sum of Clients' needs_attention)", got.Totals.NeedsAttention, wantNeeds)
	}
	if got.Totals.AwaitingApproval != wantAwaiting {
		t.Errorf("Totals.AwaitingApproval = %d, want %d (sum of Clients' awaiting_approval)", got.Totals.AwaitingApproval, wantAwaiting)
	}

	wantExact := Counts{Draft: 1, Validated: 3, Rejected: 1, Accepted: 1, Failed: 2}
	if got.Totals.Counts != wantExact {
		t.Errorf("Totals.Counts = %+v, want %+v (known seeded totals: 1 draft, 3 validated, 1 rejected, 1 accepted, 2 failed)", got.Totals.Counts, wantExact)
	}
	if got.Totals.NeedsAttention != 3 { // 1 rejected + 2 failed; draft/validated/accepted never count
		t.Errorf("Totals.NeedsAttention = %d, want 3 (1 rejected + 2 failed)", got.Totals.NeedsAttention)
	}
	if got.Totals.AwaitingApproval != 2 { // 3 validated under an active policy, minus e3's approved run
		t.Errorf("Totals.AwaitingApproval = %d, want 2 (3 validated under an active policy, minus the one with an approved run)", got.Totals.AwaitingApproval)
	}
}

// DASH-05: one draft whose violations contain an error-severity entry
// must count as needs-attention while still counting as a draft.
func TestStoreRollup_BrokenDraftCountsAsNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-05 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-05 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-05-1", "draft",
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"x"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1", row.NeedsAttention)
	}
	if row.Counts.Draft != 1 {
		t.Errorf("Counts.Draft = %d, want 1", row.Counts.Draft)
	}
}

// DASH-06: a draft whose only violation is severity:"warning" must NOT
// count as needs-attention.
func TestStoreRollup_WarningOnlyDraftIsNotNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-06 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-06 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-06-1", "draft",
		`[{"rule_key":"some-rule","severity":"warning","message":"y"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 0 {
		t.Errorf("NeedsAttention = %d, want 0 (warning severity must not trigger needs_attention)", row.NeedsAttention)
	}
}

// DASH-07: a draft with violations = '[]' must NOT count as
// needs-attention.
func TestStoreRollup_CleanDraftIsNotNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-07 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-07 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-07-1", "draft", `[]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 0 {
		t.Errorf("NeedsAttention = %d, want 0 (empty violations must not trigger needs_attention)", row.NeedsAttention)
	}
}

// DASH-08: one rejected + one failed invoice, both with violations = '[]',
// must count needs_attention = 2 -- rejected/failed count regardless of
// violations content.
func TestStoreRollup_RejectedAndFailedCountAsNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-08 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-08 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-08-1", "rejected", `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-08-2", "failed", `[]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 2 {
		t.Errorf("NeedsAttention = %d, want 2 (1 rejected + 1 failed)", row.NeedsAttention)
	}
}

// DASH-09: entities "Zeta" (2 broken drafts), "Alpha" (0 broken), "Mid" (1
// broken) must be ordered by needs_attention DESC: Zeta, Mid, Alpha.
func TestStoreRollup_ExceptionsFirstOrdering(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-09 tenant")
	zeta := seedEntity(t, super, tenantID, "Zeta")
	alpha := seedEntity(t, super, tenantID, "Alpha")
	mid := seedEntity(t, super, tenantID, "Mid")

	broken := `[{"rule_key":"x","severity":"error","message":"x"}]`
	seedInvoiceWithViolations(t, super, tenantID, zeta, "DASH-09-Z1", "draft", broken)
	seedInvoiceWithViolations(t, super, tenantID, zeta, "DASH-09-Z2", "draft", broken)
	seedInvoiceWithViolations(t, super, tenantID, mid, "DASH-09-M1", "draft", broken)
	seedInvoiceWithViolations(t, super, tenantID, alpha, "DASH-09-A1", "draft", `[]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 3 {
		t.Fatalf("Clients = %d rows, want 3", len(got.Clients))
	}
	wantOrder := []string{zeta, mid, alpha}
	gotOrder := []string{got.Clients[0].EntityID, got.Clients[1].EntityID, got.Clients[2].EntityID}
	for i, wantID := range wantOrder {
		if gotOrder[i] != wantID {
			gotNames := []string{got.Clients[0].EntityName, got.Clients[1].EntityName, got.Clients[2].EntityName}
			t.Fatalf("Clients order (by id) = %v (names %v), want [Zeta, Mid, Alpha] by needs_attention DESC", gotOrder, gotNames)
		}
	}
}

// DASH-10: entities "Beta" and "Alpha", each with exactly 1 broken draft
// (equal needs_attention), must tie-break to entity_name ASC: Alpha, Beta.
func TestStoreRollup_NameTieBreakAtEqualNeed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-10 tenant")
	beta := seedEntity(t, super, tenantID, "Beta")
	alpha := seedEntity(t, super, tenantID, "Alpha")

	broken := `[{"rule_key":"x","severity":"error","message":"x"}]`
	seedInvoiceWithViolations(t, super, tenantID, beta, "DASH-10-B1", "draft", broken)
	seedInvoiceWithViolations(t, super, tenantID, alpha, "DASH-10-A1", "draft", broken)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2", len(got.Clients))
	}
	if got.Clients[0].EntityID != alpha || got.Clients[1].EntityID != beta {
		t.Fatalf("Clients order = [%s, %s], want [Alpha, Beta] (name ASC tie-break at equal needs_attention)",
			got.Clients[0].EntityName, got.Clients[1].EntityName)
	}
}

// DASH-11: the row's EntityName/EntityID must come from the
// business_entities join, exact match, no truncation/lookup drift.
func TestStoreRollup_EntityNameIsJoinedNotLookedUp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-11 tenant")
	const name = "Dangote Cement PLC"
	entityID := seedEntity(t, super, tenantID, name)
	seedInvoice(t, super, tenantID, entityID, "DASH-11-1")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.EntityName != name {
		t.Errorf("EntityName = %q, want %q", row.EntityName, name)
	}
	if row.EntityID != entityID {
		t.Errorf("EntityID = %q, want %q", row.EntityID, entityID)
	}
}

// DASH-12: an entity with zero invoices must not appear in Clients at
// all -- the INNER JOIN excludes it, it is not a zero-count row.
func TestStoreRollup_EntityWithNoInvoicesIsAbsent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-12 tenant")
	e1 := seedEntity(t, super, tenantID, "DASH-12 entity with invoice")
	_ = seedEntity(t, super, tenantID, "DASH-12 entity without invoice") // deliberately invoice-less
	seedInvoice(t, super, tenantID, e1, "DASH-12-1")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want exactly 1 (the invoice-less entity must not appear)", len(got.Clients))
	}
	if got.Clients[0].EntityID != e1 {
		t.Errorf("Clients[0].EntityID = %q, want %q", got.Clients[0].EntityID, e1)
	}
}

// DASH-13: a broken draft that is subsequently updated (status ->
// 'validated', violations -> '[]') must show the new counts on the NEXT
// Store.Rollup call -- proving Rollup re-queries live state rather than
// caching (AC-6). The invoice row is genuinely mutated between the two
// Rollup calls.
func TestStoreRollup_LiveStateChangeIsReflected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-13 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-13 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-13-1", "draft",
		`[{"rule_key":"x","severity":"error","message":"x"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	before, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup (before mutation): %v", err)
	}
	if len(before.Clients) != 1 || before.Clients[0].Counts.Draft != 1 || before.Clients[0].NeedsAttention != 1 {
		t.Fatalf("Rollup (before mutation) Clients = %+v, want 1 client with draft=1, needs_attention=1", before.Clients)
	}

	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated', violations = '[]'::jsonb WHERE id = $1`, invID,
	); err != nil {
		t.Fatalf("mutate invoice status/violations: %v", err)
	}

	after, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup (after mutation): %v", err)
	}
	if len(after.Clients) != 1 {
		t.Fatalf("Rollup (after mutation) Clients = %d rows, want 1", len(after.Clients))
	}
	row := after.Clients[0]
	if row.Counts.Draft != 0 {
		t.Errorf("Counts.Draft (after) = %d, want 0", row.Counts.Draft)
	}
	if row.Counts.Validated != 1 {
		t.Errorf("Counts.Validated (after) = %d, want 1", row.Counts.Validated)
	}
	if row.NeedsAttention != 0 {
		t.Errorf("NeedsAttention (after) = %d, want 0", row.NeedsAttention)
	}
}

// DASH-15: a bare context.Background() (no auth.Identity) must fail
// closed with db.ErrNoTenant -- Store.Rollup delegates the check to
// db.WithinRequestTenantTx, which issues no query at all in this case.
func TestStoreRollup_NoIdentityFailsClosed(t *testing.T) {
	_, app := dbTestPools(t)

	store := NewStore(app)
	_, err := store.Rollup(context.Background())
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("Rollup(no identity) err = %v, want db.ErrNoTenant", err)
	}
}

// --- T3-1..T3-3: resolved-failed excluded from needs_attention -------------

// T3-1: a resolved failed invoice (kept_as_is_at set) must not count toward
// needs_attention; an unresolved failed invoice still counts, and both still
// count toward Counts.Failed.
func TestStoreRollup_ResolvedFailedIsNotNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-1 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-1 entity")
	seedResolvedFailed(t, super, tenantID, entityID, "T3-1-resolved")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "T3-1-unresolved", "failed")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.Counts.Failed != 2 {
		t.Errorf("Counts.Failed = %d, want 2 (both failed rows still count as failed)", row.Counts.Failed)
	}
	if row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1 (the resolved failed invoice must not count)", row.NeedsAttention)
	}
}

// T3-2 (reformulated -- the plan's "rejected invoice force-marked at the DB
// level" is unwritable: invoices_kept_as_is_status binds every writer,
// superuser included). Pins the invariant that makes the tuple split's
// rejected arm safe as a bare status = 'rejected' with no defensive
// kept_as_is_at IS NULL clause -- same shape as
// TestKeptAsIs_NonDraftRejected (internal/invoice/kept_as_is_test.go).
func TestStoreRollup_RejectedCannotCarryTheMark(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-2 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-2 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T3-2", "rejected")

	_, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'x' WHERE id = $1`, invID)
	if err == nil {
		t.Fatal("UPDATE setting the kept_as_is triple on a rejected invoice succeeded, want a 23514 (invoices_kept_as_is_status) violation")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("err = %v, want a 23514 (check_violation)", err)
	}
	if !strings.Contains(err.Error(), "invoices_kept_as_is_status") {
		t.Errorf("error = %v, want it to name invoices_kept_as_is_status", err)
	}
}

// T3-3: a blocked draft kept as-is must still count toward needs_attention --
// the draft clause is byte-untouched by this predicate split (Out of Scope
// #4), so a kept mark on a draft has no bearing on whether it counts.
func TestStoreRollup_KeptBlockedDraftStillCounts(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-3 entity")
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "T3-3", "draft",
		`[{"rule_key":"x","severity":"error","message":"blocking"}]`)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'kept' WHERE id = $1`, invID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1 (a kept blocked draft still counts, unchanged from today)", row.NeedsAttention)
	}
}

// T3-8: source-text guard for the naive edit the story specifically warns
// about -- "AND kept_as_is_at IS NULL" suffixed onto a blanket
// status IN ('rejected', 'failed') instead of the tuple split. Given
// invoices_kept_as_is_status (migrations/20260806184800_invoices_kept_as_is_failed.sql,
// pinned by TestStoreRollup_RejectedCannotCarryTheMark), a rejected row can
// NEVER carry the mark, so that naive form is BEHAVIOURALLY INDISTINGUISHABLE
// from the tuple split today -- no DB-backed test can catch a regression to
// it (verified: mutating store.go to the naive form left every test in this
// package green). This source-text check is the only guard that can.
func TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	end := bytes.Index(src, []byte(") AS needs_attention"))
	if end < 0 {
		t.Fatal(`store.go: no ") AS needs_attention" marker -- has the query been restructured?`)
	}
	start := bytes.LastIndex(src[:end], []byte("count(*) FILTER ("))
	if start < 0 {
		t.Fatal(`store.go: no "count(*) FILTER (" found before ") AS needs_attention"`)
	}
	block := string(src[start:end])

	if !strings.Contains(block, "i.status = 'rejected'") {
		t.Errorf("needs_attention FILTER clause lost its bare `i.status = 'rejected'` disjunct:\n%s", block)
	}
	if !strings.Contains(block, "i.status = 'failed' AND i.kept_as_is_at IS NULL") {
		t.Errorf("needs_attention FILTER clause lost its `i.status = 'failed' AND i.kept_as_is_at IS NULL` disjunct:\n%s", block)
	}
	if strings.Contains(block, "IN (") {
		t.Errorf("needs_attention FILTER clause contains an IN(...) -- the naive "+
			"`status IN ('rejected', 'failed') AND kept_as_is_at IS NULL` form is a silent regression "+
			"risk if invoices_kept_as_is_status is ever relaxed to allow rejected+mark; keep the tuple split:\n%s", block)
	}

	// The approval-rejected arm (AC-1): a correlated EXISTS over the MOST RECENT run,
	// with `=`, never `IN (`. Whitespace is normalized so the anchors survive
	// re-indentation, not so they survive a rewrite.
	norm := normalizeSQL(block)
	for _, want := range []string{
		"i.status = 'draft' AND EXISTS (",
		"SELECT r.state FROM approval_runs r",
		"r.invoice_id = i.id",
		"ORDER BY r.opened_at DESC LIMIT 1",
		"state = 'rejected'",
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("needs_attention FILTER clause is missing %q -- the approval-rejected arm reads the "+
				"latest run through a derived table, not `EXISTS (any rejected run)`:\n%s", want, norm)
		}
	}
}

// T3-11: a resolved failed row in one client entity must not affect a
// DIFFERENT client's needs_attention count within the same tenant -- proves
// the exclusion applies inside the per-entity GROUP BY, not just in Totals.
func TestStoreRollup_ResolvedFailedIsolatedPerClient(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-11 tenant")
	entity1 := seedEntity(t, super, tenantID, "T3-11 entity 1")
	entity2 := seedEntity(t, super, tenantID, "T3-11 entity 2")

	seedResolvedFailed(t, super, tenantID, entity1, "T3-11-e1-resolved")
	seedInvoiceAtStatus(t, super, tenantID, entity2, "T3-11-e2-unresolved", "failed")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[entity1]; row.NeedsAttention != 0 {
		t.Errorf("entity1 (resolved failed only) NeedsAttention = %d, want 0", row.NeedsAttention)
	}
	if row := byEntity[entity2]; row.NeedsAttention != 1 {
		t.Errorf("entity2 (unresolved failed) NeedsAttention = %d, want 1", row.NeedsAttention)
	}
	if got.Totals.NeedsAttention != 1 {
		t.Errorf("Totals.NeedsAttention = %d, want 1", got.Totals.NeedsAttention)
	}
}

// --- awaiting_approval -----------------------------------------------------

// The count is validated-only and an overlay on Counts, never an eighth state:
// under an active policy every unapproved validated invoice is blocked from
// transmit, and nothing else is.
func TestStoreRollup_AwaitingApprovalCountsValidatedUnderAnActivePolicy(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-A tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-A entity")
	seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-A-1", "validated")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-A-2", "validated")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-A-3", "draft")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if got.Totals.AwaitingApproval != 2 {
		t.Errorf("Totals.AwaitingApproval = %d, want 2 (both validated invoices)", got.Totals.AwaitingApproval)
	}
	if row := got.Clients[0]; row.AwaitingApproval != 2 {
		t.Errorf("Clients[0].AwaitingApproval = %d, want 2", row.AwaitingApproval)
	}
	if got.Totals.Counts.Validated != 2 {
		t.Errorf("Totals.Counts.Validated = %d, want 2 (the overlay must not disturb the state counts)", got.Totals.Counts.Validated)
	}
	if got.Totals.Counts.Draft != 1 {
		t.Errorf("Totals.Counts.Draft = %d, want 1", got.Totals.Counts.Draft)
	}
	if got.Totals.NeedsAttention != 0 {
		t.Errorf("Totals.NeedsAttention = %d, want 0 (a clean draft and two validated rows flag nothing)", got.Totals.NeedsAttention)
	}
}

// The count keys on approval_policy_versions.is_active, not on a policy row
// existing. The second leg activates the same version and re-reads: without it
// the zero-assertion alone would be satisfied by the field never being populated.
func TestStoreRollup_AwaitingApprovalIsZeroWithNoActivePolicy(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-B tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-B entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, false)

	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-B-1", "validated")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-B-2", "validated")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.AwaitingApproval != 0 {
		t.Errorf("Totals.AwaitingApproval = %d, want 0 (the policy version is not active)", got.Totals.AwaitingApproval)
	}
	if got.Totals.Counts.Validated != 2 {
		t.Fatalf("Totals.Counts.Validated = %d, want 2 -- the fixture is wrong, so the zero above proves nothing", got.Totals.Counts.Validated)
	}

	activateApprovalVersion(t, super, versionID)

	got, err = store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup after activation: %v", err)
	}
	if got.Totals.AwaitingApproval != 2 {
		t.Errorf("after activating the same version, Totals.AwaitingApproval = %d, want 2", got.Totals.AwaitingApproval)
	}
}

// An approved run satisfies TransmitClear, so its invoice leaves the count while
// its unapproved sibling stays.
func TestStoreRollup_AwaitingApprovalExcludesAnApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-C tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-C entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-C-1", "validated")
	approved := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-C-2", "validated")
	seedApprovalRun(t, super, tenantID, approved, versionID, "approved")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.AwaitingApproval != 1 {
		t.Errorf("Totals.AwaitingApproval = %d, want 1 (the approved run's invoice is transmit-clear)", got.Totals.AwaitingApproval)
	}
	if got.Totals.Counts.Validated != 2 {
		t.Errorf("Totals.Counts.Validated = %d, want 2 (an approved run does not change the state count)", got.Totals.Counts.Validated)
	}
}

// Blocked means an active policy AND no approved run -- exactly !TransmitClear.
// A rejected run is not an approved one, so its invoice still counts.
func TestStoreRollup_AwaitingApprovalCountsAValidatedInvoiceWhoseOnlyRunWasRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-D tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-D entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	rejected := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-D-1", "validated")
	seedApprovalRun(t, super, tenantID, rejected, versionID, "rejected")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.AwaitingApproval != 1 {
		t.Errorf("Totals.AwaitingApproval = %d, want 1 (a rejected run is not an approved one)", got.Totals.AwaitingApproval)
	}
	if got.Totals.NeedsAttention != 0 {
		t.Errorf("Totals.NeedsAttention = %d, want 0 (needs_attention never reaches a validated row)", got.Totals.NeedsAttention)
	}
}

// The vacuity property subtask 06's convergence contract rests on: the predicate's
// second conjunct is NOT EXISTS (an approved run), which an invoice with zero runs
// satisfies vacuously. An unarmed tenant therefore reads awaiting_approval ==
// counts.validated, and that has to be an asserted fact, not a reading of the SQL.
func TestStoreRollup_AwaitingApprovalCountsAnUnarmedValidatedInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-E tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-E entity")
	seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-E-1", "validated")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-E-2", "validated")

	if n := countApprovalRuns(t, super, tenantID); n != 0 {
		t.Fatalf("fixture has %d approval_runs, want 0 -- this test is only about the zero-run case", n)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.AwaitingApproval != 2 {
		t.Errorf("Totals.AwaitingApproval = %d, want 2 (zero runs satisfies NOT EXISTS (approved run) vacuously)", got.Totals.AwaitingApproval)
	}
	if got.Totals.AwaitingApproval != got.Totals.Counts.Validated {
		t.Errorf("Totals.AwaitingApproval = %d, Totals.Counts.Validated = %d -- with nothing armed they must coincide",
			got.Totals.AwaitingApproval, got.Totals.Counts.Validated)
	}
}

// bucketIntFieldFloor is a FLOOR, never an equality: 7 Counts + NeedsAttention +
// AwaitingApproval. An equality would turn this vacuity guard into a change
// detector that reds on a correct new field.
const bucketIntFieldFloor = 9

// bucketIntFields flattens a Bucket's int fields, recursing one level into nested
// structs (Counts). Maps and slices are skipped -- they are not element-wise
// summable scalars.
func bucketIntFields(b Bucket) map[string]int {
	out := map[string]int{}
	v := reflect.ValueOf(b)
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), tp.Field(i).Name
		switch f.Kind() {
		case reflect.Int:
			out[name] = int(f.Int())
		case reflect.Struct:
			ft := f.Type()
			for j := 0; j < f.NumField(); j++ {
				if f.Field(j).Kind() == reflect.Int {
					out[name+"."+ft.Field(j).Name] = int(f.Field(j).Int())
				}
			}
		}
	}
	return out
}

// The generic sibling of TestStoreRollup_TotalsEqualSumOfClients: that one is a
// hand-written per-field loop, so a new Bucket scalar gets no assertion from it.
// This walks Bucket by reflection instead, and so covers the field after this one.
func TestStoreRollup_EveryNumericBucketFieldIsTheSumOfClients(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-F tenant")
	e1 := seedEntity(t, super, tenantID, "APPR-11-F entity 1")
	e2 := seedEntity(t, super, tenantID, "APPR-11-F entity 2")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, e1, "APPR-11-F-1a", "validated")
	seedInvoiceAtStatus(t, super, tenantID, e1, "APPR-11-F-1b", "rejected")
	seedInvoiceAtStatus(t, super, tenantID, e2, "APPR-11-F-2a", "validated")
	seedInvoiceAtStatus(t, super, tenantID, e2, "APPR-11-F-2b", "failed")
	approved := seedInvoiceAtStatus(t, super, tenantID, e2, "APPR-11-F-2c", "validated")
	seedApprovalRun(t, super, tenantID, approved, versionID, "approved")
	seedInvoiceWithViolations(t, super, tenantID, e1, "APPR-11-F-1c", "draft",
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"x"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- every per-field assertion below is vacuous on an empty slice", len(got.Clients))
	}

	totals := bucketIntFields(got.Totals)
	if len(totals) < bucketIntFieldFloor {
		names := make([]string, 0, len(totals))
		for name := range totals {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("walked %d int fields on Bucket %v, want at least %d -- a Bucket scalar is missing, so this sum property covers nothing",
			len(totals), names, bucketIntFieldFloor)
	}

	sums := map[string]int{}
	for _, cl := range got.Clients {
		for name, n := range bucketIntFields(cl.Bucket) {
			sums[name] += n
		}
	}
	if len(sums) != len(totals) {
		t.Fatalf("walked %d int fields on Clients' buckets but %d on Totals -- the two walks disagree", len(sums), len(totals))
	}
	for name, want := range sums {
		if totals[name] != want {
			t.Errorf("Totals.%s = %d, want %d (element-wise sum of Clients)", name, totals[name], want)
		}
	}
	// Without this the newest field's sum is 0 == 0 on both sides and proves nothing:
	// the fixture arms two entities precisely so the sum has something to carry.
	if sums["AwaitingApproval"] == 0 {
		t.Errorf("the fixture summed AwaitingApproval to 0 across every client, so the sum property above is vacuous for it")
	}
	if sums["NeedsAttention"] == 0 {
		t.Errorf("the fixture summed NeedsAttention to 0 across every client, so the sum property above is vacuous for it")
	}
}

// --- needs_attention's fourth arm: the approval-rejected draft --------------

// A draft whose most recent run closed 'rejected' joins the overlay. It carries no
// error-severity violation, so nothing else in the predicate can reach it.
func TestStoreRollup_NeedsAttentionIncludesApprovalRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-include tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-02-include entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	sentBack := seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-include-1", "draft", `[]`)
	seedApprovalRun(t, super, tenantID, sentBack, versionID, "rejected")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-include-2", "draft", `[]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1 (the approval-rejected draft counts; the clean draft beside it does not)", row.NeedsAttention)
	}
	if row.Counts.Draft != 2 {
		t.Errorf("Counts.Draft = %d, want 2 (the overlay must not disturb the state counts)", row.Counts.Draft)
	}
	if got.Totals.NeedsAttention != 1 {
		t.Errorf("Totals.NeedsAttention = %d, want 1", got.Totals.NeedsAttention)
	}
}

// There is no 'superseded' run state (approval_runs.state is open/approved/rejected/
// cancelled) -- superseded means a NEWER run row exists, and the newest is the only
// one the arm reads. The two invoices sit on separate entities so the per-entity
// counts prove WHICH one flagged, not just how many did.
func TestStoreRollup_SupersededRejectionDoesNotFlag(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-superseded tenant")
	supersededEntity := seedEntity(t, super, tenantID, "APPR-11-02-superseded entity")
	newestEntity := seedEntity(t, super, tenantID, "APPR-11-02-newest entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-2 * time.Hour)
	t1 := t0.Add(time.Hour)

	superseded := seedInvoiceWithViolations(t, super, tenantID, supersededEntity, "APPR-11-02-sup-1", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, superseded, versionID, "rejected", t0)
	seedApprovalRunAt(t, super, tenantID, superseded, versionID, "open", t1)

	newest := seedInvoiceWithViolations(t, super, tenantID, newestEntity, "APPR-11-02-new-1", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, newest, versionID, "cancelled", t0)
	seedApprovalRunAt(t, super, tenantID, newest, versionID, "rejected", t1)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- the per-entity assertions below are vacuous otherwise", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[supersededEntity]; row.NeedsAttention != 0 {
		t.Errorf("superseded entity NeedsAttention = %d, want 0 (a newer open run replaced the rejection)", row.NeedsAttention)
	}
	if row := byEntity[newestEntity]; row.NeedsAttention != 1 {
		t.Errorf("newest-rejection entity NeedsAttention = %d, want 1 (the rejection is the newest run)", row.NeedsAttention)
	}
	if got.Totals.NeedsAttention != 1 {
		t.Errorf("Totals.NeedsAttention = %d, want 1", got.Totals.NeedsAttention)
	}
}

// The ordering key is opened_at, not created_at and not insert order: the rejection is
// inserted SECOND but opened FIRST, so it is not the most recent run. The control
// entity carries a plain rejection, so a zero on the ordering entity cannot pass by
// the arm never firing at all.
func TestStoreRollup_ApprovalRejectedArmIsMostRecentByOpenedAt(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-ordering tenant")
	orderingEntity := seedEntity(t, super, tenantID, "APPR-11-02-ordering entity")
	controlEntity := seedEntity(t, super, tenantID, "APPR-11-02-ordering control entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-2 * time.Hour)
	t1 := t0.Add(time.Hour)

	ordering := seedInvoiceWithViolations(t, super, tenantID, orderingEntity, "APPR-11-02-ord-1", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, ordering, versionID, "open", t1)
	seedApprovalRunAt(t, super, tenantID, ordering, versionID, "rejected", t0)

	control := seedInvoiceWithViolations(t, super, tenantID, controlEntity, "APPR-11-02-ord-control", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, control, versionID, "rejected", t0)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- the per-entity assertions below are vacuous otherwise", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[controlEntity]; row.NeedsAttention != 1 {
		t.Errorf("control entity NeedsAttention = %d, want 1 -- without it the zero below proves nothing", row.NeedsAttention)
	}
	if row := byEntity[orderingEntity]; row.NeedsAttention != 0 {
		t.Errorf("ordering entity NeedsAttention = %d, want 0 (the rejection was inserted last but opened first, so the open run is the most recent)", row.NeedsAttention)
	}
}

// The arm is draft-only. A validated invoice whose newest run is rejected is
// awaiting_approval's population, never needs_attention's -- the two overlays
// partition by status. The draft control makes the zero non-vacuous.
func TestStoreRollup_ApprovalRejectedArmIsDraftOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-draftonly tenant")
	validatedEntity := seedEntity(t, super, tenantID, "APPR-11-02-draftonly validated entity")
	draftEntity := seedEntity(t, super, tenantID, "APPR-11-02-draftonly draft entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-2 * time.Hour)

	validated := seedInvoiceWithViolations(t, super, tenantID, validatedEntity, "APPR-11-02-do-validated", "validated", `[]`)
	seedApprovalRunAt(t, super, tenantID, validated, versionID, "rejected", t0)

	draft := seedInvoiceWithViolations(t, super, tenantID, draftEntity, "APPR-11-02-do-draft", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, draft, versionID, "rejected", t0)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- the per-entity assertions below are vacuous otherwise", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[draftEntity]; row.NeedsAttention != 1 {
		t.Errorf("draft entity NeedsAttention = %d, want 1 -- without it the zero below proves nothing", row.NeedsAttention)
	}
	if row := byEntity[validatedEntity]; row.NeedsAttention != 0 {
		t.Errorf("validated entity NeedsAttention = %d, want 0 (a validated invoice with a rejected run belongs to awaiting_approval)", row.NeedsAttention)
	}
	if row := byEntity[validatedEntity]; row.AwaitingApproval != 1 {
		t.Errorf("validated entity AwaitingApproval = %d, want 1 (where that invoice does belong)", row.AwaitingApproval)
	}
}

// The new subquery is scoped by RLS, not by a manual WHERE tenant_id: tenant A's
// rejected run must not reach tenant B's clean draft. An uncorrelated or
// tenant-blind EXISTS would flag B.
func TestRLS_RollupApprovalRejectedIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "APPR-11-02-RLS tenant A")
	tenantB := seedTenant(t, super, "APPR-11-02-RLS tenant B")
	entityA := seedEntity(t, super, tenantA, "APPR-11-02-RLS A entity")
	entityB := seedEntity(t, super, tenantB, "APPR-11-02-RLS B entity")
	_, versionA := seedApprovalPolicy(t, super, tenantA, true)

	sentBackA := seedInvoiceWithViolations(t, super, tenantA, entityA, "APPR-11-02-RLS-A-1", "draft", `[]`)
	seedApprovalRun(t, super, tenantA, sentBackA, versionA, "rejected")
	seedInvoiceWithViolations(t, super, tenantB, entityB, "APPR-11-02-RLS-B-1", "draft", `[]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})

	gotA, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup (as A): %v", err)
	}
	if gotA.Totals.NeedsAttention != 1 {
		t.Errorf("A's Totals.NeedsAttention = %d, want 1 -- without it B's zero proves nothing", gotA.Totals.NeedsAttention)
	}

	gotB, err := store.Rollup(cB)
	if err != nil {
		t.Fatalf("Rollup (as B): %v", err)
	}
	if gotB.Totals.Counts.Draft != 1 {
		t.Fatalf("B's Totals.Counts.Draft = %d, want 1 -- B's fixture did not land", gotB.Totals.Counts.Draft)
	}
	if gotB.Totals.NeedsAttention != 0 {
		t.Errorf("B's Totals.NeedsAttention = %d, want 0 (A's rejected run must not reach B's clean draft)", gotB.Totals.NeedsAttention)
	}
}

// --- QA adversarial coverage (Mode B) --------------------------------------

// TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare anchors on the FIRST
// ") AS needs_attention", so a second one added below it leaves that guard
// pinning an arm nobody meant it to pin.
func TestStoreRollup_NeedsAttentionSQLMarkerIsUnique(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	if n := bytes.Count(src, []byte(") AS needs_attention")); n != 1 {
		t.Errorf("store.go holds %d occurrences of `) AS needs_attention`, want exactly 1", n)
	}
}

// normalizeSQL collapses whitespace runs so two copies of one fragment compare
// on their clauses rather than their indentation.
func normalizeSQL(s string) string { return strings.Join(strings.Fields(s), " ") }

// sliceBetween returns the text between open and the first close after it. ok is
// false when either marker is missing -- an absent marker must fail the caller,
// never yield "" and compare equal to another "".
func sliceBetween(src, open, closing string) (string, bool) {
	i := strings.Index(src, open)
	if i < 0 {
		return "", false
	}
	rest := src[i+len(open):]
	j := strings.Index(rest, closing)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// The rollup arm and the list filter are two hand-maintained copies of one
// predicate; docs/approvals.md's "the badge and the filtered list can never
// disagree" is only true while they stay textually identical modulo the alias.
// Nothing else compares them at the source level.
func TestStoreRollup_AwaitingApprovalSQLMatchesTheInvoiceListFilter(t *testing.T) {
	dashSrc, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	invSrc, err := os.ReadFile("../invoice/store.go")
	if err != nil {
		t.Fatalf("read ../invoice/store.go: %v", err)
	}

	end := bytes.Index(dashSrc, []byte(") AS awaiting_approval"))
	if end < 0 {
		t.Fatal(`store.go: no ") AS awaiting_approval" marker -- has the arm been renamed?`)
	}
	start := bytes.LastIndex(dashSrc[:end], []byte("count(*) FILTER ("))
	if start < 0 {
		t.Fatal(`store.go: no "count(*) FILTER (" before ") AS awaiting_approval"`)
	}
	dashArm := normalizeSQL(string(dashSrc[start+len("count(*) FILTER (") : end]))
	dashArm = strings.TrimPrefix(dashArm, "WHERE ")
	// The alias is the ONLY licensed difference (AC-4): the rollup reads the
	// flagged CTE as i, the list filter reads invoices unaliased.
	dashArm = strings.ReplaceAll(dashArm, "i.status", "status")
	dashArm = strings.ReplaceAll(dashArm, "i.id", "invoices.id")

	listArm, ok := sliceBetween(string(invSrc), "(status = 'validated'", "'approved'))")
	if !ok {
		t.Fatal("../invoice/store.go: no awaiting_approval filter fragment found")
	}
	listArm = normalizeSQL("status = 'validated'" + listArm + "'approved')")

	for _, clause := range []string{
		"status = 'validated'",
		"EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)",
		"NOT EXISTS (SELECT 1 FROM approval_runs r",
	} {
		if !strings.Contains(dashArm, clause) {
			t.Fatalf("the rollup arm lost the %q clause, so this comparison proves nothing:\n%s", clause, dashArm)
		}
		if !strings.Contains(listArm, clause) {
			t.Fatalf("the list filter lost the %q clause, so this comparison proves nothing:\n%s", clause, listArm)
		}
	}
	if dashArm != listArm {
		t.Errorf("the two copies of the awaiting_approval predicate have drifted.\nrollup (alias normalized): %s\nlist filter:               %s", dashArm, listArm)
	}
}

// EXISTS (an approved run), not the latest run's state: an invoice approved once
// stays out of the count whatever closed after it, and one that never reached
// approved stays in. Mirrors internal/invoice's
// TestStoreList_AwaitingApprovalApprovedRunSurvivesALaterRun on the rollup side.
func TestStoreRollup_AwaitingApprovalReadsRunHistoryNotTheLatestRun(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-QA-history tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-QA-history entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	seq := func(number string, states ...string) string {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, number, "validated")
		for _, state := range states {
			seedApprovalRun(t, super, tenantID, id, versionID, state)
		}
		return id
	}
	approvedThenCancelled := seq("APPR-11-QA-h-ac", "approved", "cancelled")
	cancelledThenApproved := seq("APPR-11-QA-h-ca", "cancelled", "approved")
	approvedThenRejected := seq("APPR-11-QA-h-ar", "approved", "rejected")
	seq("APPR-11-QA-h-rc", "rejected", "cancelled") // never approved -- counts
	seq("APPR-11-QA-h-c", "cancelled")              // never approved -- counts

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.Counts.Validated != 5 {
		t.Fatalf("Totals.Counts.Validated = %d, want 5 -- the fixture is wrong, so the count below proves nothing", got.Totals.Counts.Validated)
	}
	if got.Totals.AwaitingApproval != 2 {
		t.Errorf("Totals.AwaitingApproval = %d, want 2 (only the two invoices with no approved run anywhere in their history; %s, %s and %s each hold one)",
			got.Totals.AwaitingApproval, approvedThenCancelled, cancelledThenApproved, approvedThenRejected)
	}
}

// An active policy alone counts nothing: the predicate's first conjunct is a
// status test, so a tenant with no validated invoice reads zero however armed.
func TestStoreRollup_AwaitingApprovalIsZeroWithAnActivePolicyAndNoValidatedInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-QA-novalidated tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-QA-novalidated entity")
	seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-QA-nv-1", "draft")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-QA-nv-2", "accepted")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-QA-nv-3", "queued")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1 -- the fixture is wrong, so the zero below proves nothing", len(got.Clients))
	}
	if got.Totals.AwaitingApproval != 0 {
		t.Errorf("Totals.AwaitingApproval = %d, want 0 (nothing is validated)", got.Totals.AwaitingApproval)
	}
	if got.Totals.Counts.Draft+got.Totals.Counts.Accepted+got.Totals.Counts.Queued != 3 {
		t.Errorf("the three seeded non-validated invoices did not land: %+v", got.Totals.Counts)
	}
}

// The two overlays partition by status -- needs_attention never reaches a
// validated row, awaiting_approval reaches nothing else -- so neither may be
// derived from the other. Asserted in both directions, per entity and in Totals.
func TestStoreRollup_AwaitingApprovalAndNeedsAttentionAreIndependentOverlays(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-QA-overlay tenant")
	awaitingOnly := seedEntity(t, super, tenantID, "APPR-11-QA-overlay awaiting")
	attentionOnly := seedEntity(t, super, tenantID, "APPR-11-QA-overlay attention")
	seedApprovalPolicy(t, super, tenantID, true)

	seedInvoiceAtStatus(t, super, tenantID, awaitingOnly, "APPR-11-QA-ov-a1", "validated")
	seedInvoiceAtStatus(t, super, tenantID, awaitingOnly, "APPR-11-QA-ov-a2", "validated")
	seedInvoiceAtStatus(t, super, tenantID, attentionOnly, "APPR-11-QA-ov-n1", "rejected")
	seedInvoiceAtStatus(t, super, tenantID, attentionOnly, "APPR-11-QA-ov-n2", "failed")
	seedInvoiceAtStatus(t, super, tenantID, attentionOnly, "APPR-11-QA-ov-n3", "failed")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- every per-entity assertion below is vacuous otherwise", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[awaitingOnly]; row.AwaitingApproval != 2 || row.NeedsAttention != 0 {
		t.Errorf("awaiting-only entity: AwaitingApproval = %d (want 2), NeedsAttention = %d (want 0)", row.AwaitingApproval, row.NeedsAttention)
	}
	if row := byEntity[attentionOnly]; row.NeedsAttention != 3 || row.AwaitingApproval != 0 {
		t.Errorf("attention-only entity: NeedsAttention = %d (want 3), AwaitingApproval = %d (want 0)", row.NeedsAttention, row.AwaitingApproval)
	}
	if got.Totals.AwaitingApproval != 2 || got.Totals.NeedsAttention != 3 {
		t.Errorf("Totals.AwaitingApproval = %d (want 2), Totals.NeedsAttention = %d (want 3) -- the two overlays must not track each other",
			got.Totals.AwaitingApproval, got.Totals.NeedsAttention)
	}
}

// --- QA adversarial coverage (Mode B): the approval-rejected arm ------------

// Only 'rejected' flags. approval_runs.state is open/approved/rejected/cancelled,
// and the arm reads the newest run's state with `=`, so the other three must leave
// the draft alone -- including 'cancelled', which no other test puts newest.
// One entity per state, so a zero names WHICH state failed rather than a total.
func TestStoreRollup_OnlyARejectedNewestRunFlagsTheDraft(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-QA-states tenant")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-2 * time.Hour)
	t1 := t0.Add(time.Hour)

	cases := []struct {
		newest string
		want   int
	}{
		{"open", 0},
		{"approved", 0},
		{"cancelled", 0},
		{"rejected", 1},
	}
	entities := map[string]string{}
	for _, tc := range cases {
		e := seedEntity(t, super, tenantID, "APPR-11-02-QA-states "+tc.newest)
		entities[tc.newest] = e
		inv := seedInvoiceWithViolations(t, super, tenantID, e, "APPR-11-02-QA-st-"+tc.newest, "draft", `[]`)
		// A rejection underneath every case, so each row differs ONLY in its newest state.
		seedApprovalRunAt(t, super, tenantID, inv, versionID, "rejected", t0)
		seedApprovalRunAt(t, super, tenantID, inv, versionID, tc.newest, t1)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != len(cases) {
		t.Fatalf("Clients = %d rows, want %d -- the per-entity assertions below are vacuous otherwise", len(got.Clients), len(cases))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	for _, tc := range cases {
		if row := byEntity[entities[tc.newest]]; row.NeedsAttention != tc.want {
			t.Errorf("newest run %q: NeedsAttention = %d, want %d", tc.newest, row.NeedsAttention, tc.want)
		}
	}
}

// EXISTS, not a join: two rejected runs on one draft still count it once. A join
// would report 2 and inflate the badge against a real approval history.
func TestStoreRollup_TwoRejectedRunsCountTheDraftOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-QA-twice tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-02-QA-twice entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-2 * time.Hour)

	inv := seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-twice-1", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, inv, versionID, "rejected", t0)
	seedApprovalRunAt(t, super, tenantID, inv, versionID, "rejected", t0.Add(time.Hour))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1 -- two rejected runs on one draft are one invoice", row.NeedsAttention)
	}
	if got.Totals.Counts.Draft != 1 {
		t.Fatalf("Counts.Draft = %d, want 1 -- the fixture did not land", got.Totals.Counts.Draft)
	}
}

// The arm reads status at query time, not at rejection time: a rejected draft that
// was promoted to validated leaves the overlay, and coming back to draft rejoins it.
// The run row never moves.
func TestStoreRollup_ApprovalRejectedArmFollowsTheCurrentStatus(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-QA-roundtrip tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-02-QA-roundtrip entity")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	inv := seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-rt-1", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, inv, versionID, "rejected", time.Now().Add(-time.Hour))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	setStatus := func(status string) {
		t.Helper()
		if _, err := super.Exec(ctx, `UPDATE invoices SET status = $1 WHERE id = $2`, status, inv); err != nil {
			t.Fatalf("set invoice status %q: %v", status, err)
		}
	}
	attention := func(step string) int {
		t.Helper()
		got, err := store.Rollup(c)
		if err != nil {
			t.Fatalf("Rollup (%s): %v", step, err)
		}
		return got.Totals.NeedsAttention
	}

	if n := attention("as draft"); n != 1 {
		t.Fatalf("NeedsAttention as draft = %d, want 1 -- the rest of this test proves nothing otherwise", n)
	}
	setStatus("validated")
	if n := attention("after promotion"); n != 0 {
		t.Errorf("NeedsAttention as validated = %d, want 0 (the run did not move; the status did)", n)
	}
	setStatus("draft")
	if n := attention("back to draft"); n != 1 {
		t.Errorf("NeedsAttention back at draft = %d, want 1 (the same run flags it again)", n)
	}
}

// The three original arms are untouched by the widening, on a tenant proven to hold
// ZERO approval_runs -- so a fourth arm that fired on run-less rows would show up here.
func TestStoreRollup_OriginalArmsHoldWithNoApprovalRowsAtAll(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-QA-noruns tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-02-QA-noruns entity")

	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-nr-rejected", "rejected", `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-nr-failed", "failed", `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-nr-errordraft", "draft",
		`[{"rule_key":"x","severity":"error","message":"x"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-nr-cleandraft", "draft", `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-QA-nr-validated", "validated", `[]`)

	if n := countApprovalRuns(t, super, tenantID); n != 0 {
		t.Fatalf("fixture holds %d approval_runs, want 0 -- this test's premise is gone", n)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Totals.NeedsAttention != 3 {
		t.Errorf("Totals.NeedsAttention = %d, want 3 (rejected + unresolved failed + error draft; the clean draft and the validated row must not count)",
			got.Totals.NeedsAttention)
	}
}

// The overlays both read approval_runs now, so their independence needs restating
// where it is newly at risk: an approval-rejected DRAFT under an active policy is
// needs_attention's alone, and a validated invoice with an open run is
// awaiting_approval's alone. Asserted in both directions, per entity.
func TestStoreRollup_ApprovalRejectedDraftIsNotAwaitingApproval(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-11-02-QA-indep tenant")
	sentBackEntity := seedEntity(t, super, tenantID, "APPR-11-02-QA-indep sent-back")
	awaitingEntity := seedEntity(t, super, tenantID, "APPR-11-02-QA-indep awaiting")
	_, versionID := seedApprovalPolicy(t, super, tenantID, true)

	t0 := time.Now().Add(-time.Hour)

	sentBack := seedInvoiceWithViolations(t, super, tenantID, sentBackEntity, "APPR-11-02-QA-ind-draft", "draft", `[]`)
	seedApprovalRunAt(t, super, tenantID, sentBack, versionID, "rejected", t0)

	awaiting := seedInvoiceWithViolations(t, super, tenantID, awaitingEntity, "APPR-11-02-QA-ind-validated", "validated", `[]`)
	seedApprovalRunAt(t, super, tenantID, awaiting, versionID, "open", t0)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2 -- the per-entity assertions below are vacuous otherwise", len(got.Clients))
	}
	byEntity := map[string]Client{}
	for _, cl := range got.Clients {
		byEntity[cl.EntityID] = cl
	}
	if row := byEntity[sentBackEntity]; row.NeedsAttention != 1 || row.AwaitingApproval != 0 {
		t.Errorf("sent-back entity: NeedsAttention = %d (want 1), AwaitingApproval = %d (want 0)", row.NeedsAttention, row.AwaitingApproval)
	}
	if row := byEntity[awaitingEntity]; row.AwaitingApproval != 1 || row.NeedsAttention != 0 {
		t.Errorf("awaiting entity: AwaitingApproval = %d (want 1), NeedsAttention = %d (want 0)", row.AwaitingApproval, row.NeedsAttention)
	}
	if got.Totals.NeedsAttention != 1 || got.Totals.AwaitingApproval != 1 {
		t.Errorf("Totals.NeedsAttention = %d (want 1), Totals.AwaitingApproval = %d (want 1)", got.Totals.NeedsAttention, got.Totals.AwaitingApproval)
	}
}
