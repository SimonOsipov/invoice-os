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
	"testing"

	"github.com/google/uuid"
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

// seedTenant inserts one throwaway tenants row (kind 'firm') as the
// superuser and registers a cleanup that deletes it. A tenants delete
// CASCADEs away every business_entities/invoices row scoped to it, so
// per-test cleanup never has to unwind child rows by hand.
func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 3 {
		t.Fatalf("Clients = %d rows, want 3", len(got.Clients))
	}

	var wantCounts Counts
	var wantNeeds int
	for _, c := range got.Clients {
		wantCounts.Draft += c.Counts.Draft
		wantCounts.Validated += c.Counts.Validated
		wantCounts.Queued += c.Counts.Queued
		wantCounts.Submitted += c.Counts.Submitted
		wantCounts.Accepted += c.Counts.Accepted
		wantCounts.Rejected += c.Counts.Rejected
		wantCounts.Failed += c.Counts.Failed
		wantNeeds += c.NeedsAttention
	}
	if got.Totals.Counts != wantCounts {
		t.Errorf("Totals.Counts = %+v, want %+v (element-wise sum of Clients)", got.Totals.Counts, wantCounts)
	}
	if got.Totals.NeedsAttention != wantNeeds {
		t.Errorf("Totals.NeedsAttention = %d, want %d (sum of Clients' needs_attention)", got.Totals.NeedsAttention, wantNeeds)
	}

	wantExact := Counts{Draft: 1, Validated: 1, Rejected: 1, Accepted: 1, Failed: 2}
	if got.Totals.Counts != wantExact {
		t.Errorf("Totals.Counts = %+v, want %+v (known seeded totals: 1 draft, 1 validated, 1 rejected, 1 accepted, 2 failed)", got.Totals.Counts, wantExact)
	}
	if got.Totals.NeedsAttention != 3 { // 1 rejected + 2 failed; draft/validated/accepted never count
		t.Errorf("Totals.NeedsAttention = %d, want 3 (1 rejected + 2 failed)", got.Totals.NeedsAttention)
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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

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
