// M4-02-01 (task-96): tests for internal/invoice's Store CRUD surface,
// written BEFORE the real implementation exists (RED against the
// not-implemented stub bodies in store.go). Store.Create/Get/List/Update
// wrap db.WithinRequestTenantTx (System Design table, M4-02-01 story) — RLS
// scopes tenant, so no manual `WHERE tenant_id` appears in any assertion
// query run through the app-role pool; the superuser pool is used only for
// seeding/reading-back rows out-of-band (bypasses RLS, so it needs no
// tenant context).
//
// Spec-to-test map (Test Specs table, M4-02-01 story / task-96):
//
//	INV-STORE-01 TestStoreCreate_PersistsDraftUnderCallerTenant
//	INV-STORE-02 TestStoreCreate_LineItemsGetSystemOrdinals
//	INV-STORE-03 TestStoreCreate_WritesExactlyOneCreatedAuditActorIsSubject
//	INV-STORE-04 TestStoreCreate_WritesGenesisHistoryRow
//	INV-STORE-05 TestStoreCreate_StoreInvalidContentPersistsUnrejected
//	INV-STORE-06 TestStoreCreate_DuplicateNumberRejectedAtomically
//	INV-STORE-07 TestStoreCreate_AtomicityRollsBackOnLaterInTxFailure
//	INV-STORE-08 TestStoreGet_HydratesLineItemsOrdered
//	INV-STORE-09 TestStoreGet_CrossTenantNotFound
//	INV-STORE-10 TestStoreList_TenantScopedAndPaginated
//	INV-STORE-11 TestStoreUpdate_PartialAndAudit_AllNilRejected
//	INV-STORE-12 TestStoreCrossTenant_UpdateGetListRefused
//	INV-STORE-13 TestStoreCreate_NonExistentEntityIDRejected
//
// TestRLS_InvoicesStoreChildWritesTenantScoped lives in
// cross_tenant_integration_test.go.
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- shared DB-test harness (mirrors internal/portfolio/portfolio_test.go's
// dbTestPools/seedEntity/auditCount/auditActor idiom, adapted to the
// invoices/line_items/invoice_status_history shape) ------------------------

// dbTestPools returns the superuser (seed) and app-role (Store) pools for
// the invoice db-integration suite below, or skips the test when the
// per-role DSNs are unset — copied from internal/portfolio/portfolio_test.go
// (~line 731), itself copied from internal/tenancy/tenancy_test.go, the same
// env gate `make test-rls`/`make test-audit` use.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("invoice db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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
// CASCADEs away every business_entities/invoices/line_items/
// invoice_status_history row scoped to it (confirmed by M4-01-04's
// TestRLS_InvoiceStatusHistoryTenantDeleteCascades), so per-test cleanup
// never has to unwind child rows by hand — audit_log rows are the one
// exception (bare uuid tenant_id, no FK) but are harmless leftovers since
// every test tenant id is a fresh uuid.
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
// alongside the tenant-cascade above; harmless once the tenant is gone).
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

// seedInvoice inserts one invoices row directly (bypassing Store.Create,
// which is itself under test) as the superuser, for specs that exercise
// Get/List/Update against a known-good row without depending on Create's
// own correctness.
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

// mustCount runs a `SELECT count(*) ...` query as the superuser (bypasses
// RLS) and fails the test if the query itself errors.
func mustCount(t *testing.T, super *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// auditCount counts audit_log rows for tenantID+event, scoped via
// db.WithinTenantTx (FORCE RLS) — mirrors
// internal/portfolio/portfolio_test.go's auditCount helper.
func auditCount(t *testing.T, pool *pgxpool.Pool, tenantID, event string) int {
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

// auditActor returns the actor of the most recent audit_log row for
// tenantID+event.
func auditActor(t *testing.T, pool *pgxpool.Pool, tenantID, event string) string {
	t.Helper()
	ctx := context.Background()
	var actor string
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&actor)
	}); err != nil {
		t.Fatalf("read audit_log actor: %v", err)
	}
	return actor
}

func strPtr(s string) *string { return &s }

// --- INV-STORE-01..13 ------------------------------------------------------

// INV-STORE-01: Create persists a draft invoice under the caller's tenant.
func TestStoreCreate_PersistsDraftUnderCallerTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-01 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-01 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-STORE-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.ID == "" {
		t.Fatal("Create: ID is empty, want a generated id")
	}
	if inv.Status != StatusDraft {
		t.Errorf("Create: status = %q, want %q", inv.Status, StatusDraft)
	}
	if inv.EntityID != entityID {
		t.Errorf("Create: entity id = %q, want %q", inv.EntityID, entityID)
	}
	if inv.InvoiceNumber != "INV-STORE-01" {
		t.Errorf("Create: invoice number = %q, want %q", inv.InvoiceNumber, "INV-STORE-01")
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoices WHERE id = $1 AND tenant_id = $2 AND status = 'draft'`,
		inv.ID, tenantID,
	); n != 1 {
		t.Errorf("invoices rows for the created id under its tenant = %d, want 1", n)
	}
}

// INV-STORE-02: line_items get system-assigned ordinals 1..N by
// CreateInput.LineItems' array position, sharing the invoice's tenant_id.
func TestStoreCreate_LineItemsGetSystemOrdinals(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-02 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-02 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	descA, descB, descC := "Widget A", "Widget B", "Widget C"
	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-STORE-02",
		LineItems: []LineItemInput{
			{Description: &descA},
			{Description: &descB},
			{Description: &descC},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rows, err := super.Query(ctx,
		`SELECT line_no, description, tenant_id, invoice_id FROM line_items WHERE invoice_id = $1 ORDER BY line_no ASC`,
		inv.ID,
	)
	if err != nil {
		t.Fatalf("query line_items: %v", err)
	}
	defer rows.Close()

	var lineNos []int
	var descs, tenantIDs, invoiceIDs []string
	for rows.Next() {
		var lineNo int
		var desc, tid, iid string
		if err := rows.Scan(&lineNo, &desc, &tid, &iid); err != nil {
			t.Fatalf("scan line_items row: %v", err)
		}
		lineNos = append(lineNos, lineNo)
		descs = append(descs, desc)
		tenantIDs = append(tenantIDs, tid)
		invoiceIDs = append(invoiceIDs, iid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate line_items: %v", err)
	}

	if len(lineNos) != 3 {
		t.Fatalf("line_items rows for invoice = %d, want 3", len(lineNos))
	}
	wantDescs := []string{"Widget A", "Widget B", "Widget C"}
	for i, wantLineNo := range []int{1, 2, 3} {
		if lineNos[i] != wantLineNo {
			t.Errorf("line_items[%d].line_no = %d, want %d (system-assigned by array position)", i, lineNos[i], wantLineNo)
		}
		if descs[i] != wantDescs[i] {
			t.Errorf("line_items[%d].description = %q, want %q", i, descs[i], wantDescs[i])
		}
		if tenantIDs[i] != tenantID {
			t.Errorf("line_items[%d].tenant_id = %q, want %q", i, tenantIDs[i], tenantID)
		}
		if invoiceIDs[i] != inv.ID {
			t.Errorf("line_items[%d].invoice_id = %q, want %q", i, invoiceIDs[i], inv.ID)
		}
	}
}

// INV-STORE-03: Create writes exactly one "invoice.created" audit row, actor
// == the caller's Subject.
func TestStoreCreate_WritesExactlyOneCreatedAuditActorIsSubject(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-03 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-03 entity")

	store := NewStore(app)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.created"
	before := auditCount(t, app, tenantID, event)

	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-STORE-03"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != subject {
		t.Errorf("audit actor = %q, want %q", actor, subject)
	}
}

// INV-STORE-04: Create writes the genesis invoice_status_history row
// (from_status NULL -> to_status 'draft', actor == the caller's Subject),
// [D5].
func TestStoreCreate_WritesGenesisHistoryRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-04 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-04 entity")

	store := NewStore(app)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-STORE-04"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != 1 {
		t.Fatalf("invoice_status_history rows for created invoice = %d, want exactly 1 (genesis row)", n)
	}

	var fromStatus *string
	var toStatus, actor string
	if err := super.QueryRow(ctx,
		`SELECT from_status, to_status, actor FROM invoice_status_history WHERE invoice_id = $1`, inv.ID,
	).Scan(&fromStatus, &toStatus, &actor); err != nil {
		t.Fatalf("read genesis history row: %v", err)
	}
	if fromStatus != nil {
		t.Errorf("genesis history from_status = %q, want NULL", *fromStatus)
	}
	if toStatus != "draft" {
		t.Errorf("genesis history to_status = %q, want %q", toStatus, "draft")
	}
	if actor != subject {
		t.Errorf("genesis history actor = %q, want %q", actor, subject)
	}
}

// INV-STORE-05: store-invalid content (negative money, NULL currency, blank
// content) persists un-rejected (AC-6); values round-trip unchanged via the
// ::text cast, [D13].
func TestStoreCreate_StoreInvalidContentPersistsUnrejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-05 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-05 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-STORE-05",
		Subtotal:      strPtr("-5.00"),
		Currency:      nil,
		SupplierName:  strPtr(""),
	})
	if err != nil {
		t.Fatalf("Create (store-invalid content): want success (AC-6, no content CHECK), got: %v", err)
	}
	if inv.Subtotal == nil || *inv.Subtotal != "-5.00" {
		t.Errorf("Create returned subtotal = %v, want %q", inv.Subtotal, "-5.00")
	}
	if inv.Currency != nil {
		t.Errorf("Create returned currency = %q, want nil", *inv.Currency)
	}
	if inv.SupplierName == nil || *inv.SupplierName != "" {
		t.Errorf("Create returned supplier_name = %v, want empty string (blank content stored, not rejected)", inv.SupplierName)
	}

	var subtotal, supplierName string
	var currency *string
	if err := super.QueryRow(ctx,
		`SELECT subtotal::text, currency, supplier_name FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&subtotal, &currency, &supplierName); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if subtotal != "-5.00" {
		t.Errorf("subtotal read back = %q, want %q", subtotal, "-5.00")
	}
	if currency != nil {
		t.Errorf("currency read back = %q, want NULL", *currency)
	}
	if supplierName != "" {
		t.Errorf("supplier_name read back = %q, want empty string", supplierName)
	}
}

// INV-STORE-06: a duplicate (tenant, entity, invoice_number) is rejected
// atomically (AC-5) — the second Create's failure leaves the invoices and
// audit_log counts unchanged vs. right after the first Create.
func TestStoreCreate_DuplicateNumberRejectedAtomically(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-06 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-06 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const number = "INV-STORE-06-DUP"
	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	const event = "invoice.created"
	invoicesBefore := mustCount(t, super,
		`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2 AND invoice_number = $3`,
		tenantID, entityID, number,
	)
	auditBefore := auditCount(t, app, tenantID, event)

	_, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number})
	if !errors.Is(err, ErrDuplicateNumber) {
		t.Fatalf("second Create (duplicate tenant,entity,number) err = %v, want ErrDuplicateNumber", err)
	}

	if n := mustCount(t, super,
		`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2 AND invoice_number = $3`,
		tenantID, entityID, number,
	); n != invoicesBefore {
		t.Errorf("invoices rows for (tenant,entity,number) after failed duplicate Create = %d, want unchanged %d", n, invoicesBefore)
	}
	if n := auditCount(t, app, tenantID, event); n != auditBefore {
		t.Errorf("audit_log rows for %s after failed duplicate Create = %d, want unchanged %d", event, n, auditBefore)
	}
}

// PAR-03 (M4-06-02): manual-path DB backstop, state-blind (Core AC#3/AC#4,
// M4-06 Store-Level Duplicate Rule). Extends INV-STORE-06
// (TestStoreCreate_DuplicateNumberRejectedAtomically, directly above) with
// the state-blind half the M4-06 story adds: the unique index rejects a
// duplicate Create regardless of the ALREADY-STORED sibling row's own
// lifecycle state, not merely against a fresh draft.
//
//   - (a) mirrors INV-STORE-06 itself (a fresh draft sibling): first Create
//     succeeds, an identical second Create -> ErrDuplicateNumber,
//     superuser read-back exactly 1 row.
//   - (b) the NEW case: a superuser-seeded NON-draft stored row (status
//     'accepted', bypassing the state machine -- a fixture concern, per
//     the story's own note) still backstops a manual Create for the same
//     (entity, number) -> ErrDuplicateNumber, proving the index enforces
//     uniqueness independent of the stored row's status.
func TestStoreCreate_DuplicateRejectedRegardlessOfStoredRowState(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	store := NewStore(app)

	t.Run("draft sibling backstop (PAR-03a)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "PAR-03a tenant")
		entityID := seedEntity(t, super, tenantID, "PAR-03a entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		const number = "INV-M"
		if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number}); err != nil {
			t.Fatalf("first Create: %v", err)
		}
		if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number}); !errors.Is(err, ErrDuplicateNumber) {
			t.Fatalf("second Create err = %v, want ErrDuplicateNumber", err)
		}
		if n := mustCount(t, super,
			`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2 AND invoice_number = $3`,
			tenantID, entityID, number,
		); n != 1 {
			t.Errorf("rows for (tenant,entity,%q) = %d, want exactly 1", number, n)
		}
	})

	t.Run("non-draft stored row backstop (PAR-03b)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "PAR-03b tenant")
		entityID := seedEntity(t, super, tenantID, "PAR-03b entity")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		const number = "INV-M2"
		invID := seedInvoice(t, super, tenantID, entityID, number)
		if _, err := super.Exec(ctx,
			`UPDATE invoices SET status = $1 WHERE id = $2`, string(StatusAccepted), invID,
		); err != nil {
			t.Fatalf("seed status=accepted: %v", err)
		}

		if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number}); !errors.Is(err, ErrDuplicateNumber) {
			t.Fatalf("Create against an accepted stored row: err = %v, want ErrDuplicateNumber -- the index backstops regardless of the stored row's state (PAR-03b, Core AC#3/AC#4)", err)
		}
		if n := mustCount(t, super,
			`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2 AND invoice_number = $3`,
			tenantID, entityID, number,
		); n != 1 {
			t.Errorf("rows for (tenant,entity,%q) = %d, want exactly 1 (the pre-seeded accepted row, no duplicate inserted)", number, n)
		}
	})
}

// INV-STORE-07: Create's atomicity — a later in-tx write failing rolls back
// the WHOLE closure, including the earlier, already-executed writes. Two
// crafted-actor injections hit the SAME guarantee at two different points in
// the write order (invoices -> line_items -> genesis history ->
// audit.Record):
//
//   - (a) an empty Subject fails the genesis invoice_status_history INSERT's
//     `char_length(actor) > 0` CHECK (23514) — invoices/line_items were
//     already (transiently) written and must roll back too.
//   - (b) a 256-char Subject passes that CHECK (no upper bound there) but
//     then fails audit_log's `char_length(actor) <= 255` CHECK (23514) — by
//     this point invoices/line_items/history were all (transiently) written
//     and must ALL roll back.
//
// Both cases assert the SQLSTATE via pgCode (not assertRLSViolation, which
// checks 42501) and that zero rows exist across all four tables for the
// throwaway tenant used by that sub-case afterward.
func TestStoreCreate_AtomicityRollsBackOnLaterInTxFailure(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	assertNothingPersisted := func(t *testing.T, tenantID string) {
		t.Helper()
		if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("invoices rows for tenant after failed Create = %d, want 0 (whole tx rolled back)", n)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("line_items rows for tenant after failed Create = %d, want 0", n)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("invoice_status_history rows for tenant after failed Create = %d, want 0", n)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created'`, tenantID); n != 0 {
			t.Errorf("audit_log invoice.created rows for tenant after failed Create = %d, want 0", n)
		}
	}

	t.Run("empty actor fails genesis history CHECK (23514)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "INV-STORE-07a tenant")
		entityID := seedEntity(t, super, tenantID, "INV-STORE-07a entity")

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})

		desc := "line 1"
		_, err := store.Create(c, CreateInput{
			EntityID:      entityID,
			InvoiceNumber: "INV-STORE-07a",
			LineItems:     []LineItemInput{{Description: &desc}},
		})
		if err == nil {
			t.Fatal("Create with an empty-Subject actor succeeded, want a genesis invoice_status_history CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Create with an empty-Subject actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		assertNothingPersisted(t, tenantID)
	})

	t.Run("256-char actor passes history CHECK but fails audit_log CHECK (23514)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "INV-STORE-07b tenant")
		entityID := seedEntity(t, super, tenantID, "INV-STORE-07b entity")

		store := NewStore(app)
		longSubject := strings.Repeat("a", 256)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})

		desc := "line 1"
		_, err := store.Create(c, CreateInput{
			EntityID:      entityID,
			InvoiceNumber: "INV-STORE-07b",
			LineItems:     []LineItemInput{{Description: &desc}},
		})
		if err == nil {
			t.Fatal("Create with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Create with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		assertNothingPersisted(t, tenantID)
	})
}

// INV-STORE-08: Get hydrates line_items ordered by line_no, regardless of
// insertion order.
func TestStoreGet_HydratesLineItemsOrdered(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-08 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-08 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "INV-STORE-08")

	// Seed 3 line_items OUT of line_no order, so an unordered SELECT would
	// fail the ordering assertion below.
	for _, lineNo := range []int{3, 1, 2} {
		if _, err := super.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description) VALUES ($1, $2, $3, $4)`,
			tenantID, invoiceID, lineNo, fmt.Sprintf("Line %d", lineNo),
		); err != nil {
			t.Fatalf("seed line_items (line_no=%d): %v", lineNo, err)
		}
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Get(c, invoiceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.LineItems) != 3 {
		t.Fatalf("Get: len(LineItems) = %d, want 3", len(got.LineItems))
	}
	for i, want := range []int{1, 2, 3} {
		if got.LineItems[i].LineNo != want {
			t.Errorf("Get: LineItems[%d].LineNo = %d, want %d (ordered by line_no)", i, got.LineItems[i].LineNo, want)
		}
	}
}

// TestStoreGet_PopulatesRuleSetVersion (M4-09-01, task-182, Core AC #1/#2):
// Store.Get must resolve the human-facing rule_set_versions.version integer
// onto the transient Invoice.RuleSetVersion field -- a real row's
// rule_set_version_id when stamped, nil when it is NULL (never validated).
// Stamps rule_set_version_id directly via the superuser pool (any real
// rule_set_versions row satisfies the FK, mirrors gate_test.go's
// seedRuleSetVersionID -- this test does not need to run the real gate,
// only a stamped-vs-unstamped invoices row). wantVersion is read back from
// the DB itself (not hardcoded), so the assertion pins Get's resolved int
// to the actual rule_set_versions row's version, whatever seedRuleSetVersionID
// happened to pick -- see TestStoreGet_RuleSetVersionResolvesByID below for
// the stronger, multi-row proof that the subselect keys on the FK id and not
// merely "the only/first row".
func TestStoreGet_PopulatesRuleSetVersion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-01 tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-01 entity")
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Case 1: a stamped rule_set_version_id -> Get must resolve the
	// rule_set_versions row's own `version` int.
	stampedID := seedInvoice(t, super, tenantID, entityID, "M4-09-01-A")
	rsvID := seedRuleSetVersionID(t, super)
	var wantVersion int
	if err := super.QueryRow(ctx,
		`SELECT version FROM rule_set_versions WHERE id = $1`, rsvID,
	).Scan(&wantVersion); err != nil {
		t.Fatalf("read rule_set_versions.version: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rsvID, stampedID,
	); err != nil {
		t.Fatalf("stamp rule_set_version_id: %v", err)
	}

	got, err := store.Get(c, stampedID)
	if err != nil {
		t.Fatalf("Get(stamped): %v", err)
	}
	if got.RuleSetVersion == nil {
		t.Fatalf("Get(stamped).RuleSetVersion = nil, want %d (the stamped row's rule_set_versions.version)",
			wantVersion)
	}
	if *got.RuleSetVersion != wantVersion {
		t.Errorf("Get(stamped).RuleSetVersion = %d, want %d", *got.RuleSetVersion, wantVersion)
	}

	// Case 2: rule_set_version_id IS NULL (never validated) -> nil, not 0.
	unstampedID := seedInvoice(t, super, tenantID, entityID, "M4-09-01-B")

	got2, err := store.Get(c, unstampedID)
	if err != nil {
		t.Fatalf("Get(never-validated): %v", err)
	}
	if got2.RuleSetVersion != nil {
		t.Errorf("Get(never-validated).RuleSetVersion = %d, want nil (rule_set_version_id is NULL)",
			*got2.RuleSetVersion)
	}
}

// INV-STORE-09: Get on a cross-tenant id resolves to ErrNotFound (AC-6) — no
// leak.
func TestStoreGet_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-STORE-09 tenant A")
	tenantB := seedTenant(t, super, "INV-STORE-09 tenant B")
	entityB := seedEntity(t, super, tenantB, "INV-STORE-09 B entity")
	invoiceB := seedInvoice(t, super, tenantB, entityB, "INV-STORE-09-B")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	if _, err := store.Get(cA, invoiceB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(tenant B's invoice) as tenant A err = %v, want ErrNotFound", err)
	}
}

// INV-STORE-10: List returns only the caller's tenant's headers, paginated,
// with the correct total; an empty result is [] never nil.
func TestStoreList_TenantScopedAndPaginated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-STORE-10 tenant A")
	tenantB := seedTenant(t, super, "INV-STORE-10 tenant B")
	entityA := seedEntity(t, super, tenantA, "INV-STORE-10 A entity")
	entityB := seedEntity(t, super, tenantB, "INV-STORE-10 B entity")

	for i := 0; i < 3; i++ {
		seedInvoice(t, super, tenantA, entityA, fmt.Sprintf("INV-STORE-10-A-%d", i))
	}
	seedInvoice(t, super, tenantB, entityB, "INV-STORE-10-B-0")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, total, err := store.List(cA, ListFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("List (page 1): %v", err)
	}
	if total != 3 {
		t.Fatalf("List total = %d, want 3 (only tenant A's rows)", total)
	}
	if len(items) != 2 {
		t.Fatalf("List (limit=2) returned %d items, want 2", len(items))
	}
	for _, inv := range items {
		if inv.EntityID == entityB {
			t.Errorf("List (as A) leaked tenant B's invoice: %+v", inv)
		}
	}

	items2, total2, err := store.List(cA, ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List (page 2): %v", err)
	}
	if total2 != 3 {
		t.Errorf("List (page 2) total = %d, want 3", total2)
	}
	if len(items2) != 1 {
		t.Fatalf("List (offset=2, limit=2) returned %d items, want 1 (3 total, 2 already on page 1)", len(items2))
	}

	emptyTenant := seedTenant(t, super, "INV-STORE-10 empty tenant")
	cEmpty := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: emptyTenant})
	emptyItems, emptyTotal, err := store.List(cEmpty, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (empty tenant): %v", err)
	}
	if emptyTotal != 0 {
		t.Errorf("List (empty tenant) total = %d, want 0", emptyTotal)
	}
	if emptyItems == nil {
		t.Error("List (empty tenant) returned a nil slice, want [] (never null)")
	}
	if len(emptyItems) != 0 {
		t.Errorf("List (empty tenant) len = %d, want 0", len(emptyItems))
	}
}

// INV-STORE-11: Update applies only the provided content field(s) and writes
// exactly one "invoice.updated" audit row; an all-nil UpdateInput is
// rejected as ErrValidation with no tx and no audit row.
func TestStoreUpdate_PartialAndAudit_AllNilRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-11 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-STORE-11 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "INV-STORE-11")
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET currency = 'NGN', subtotal = 100.00 WHERE id = $1`, invoiceID,
	); err != nil {
		t.Fatalf("seed initial content: %v", err)
	}

	store := NewStore(app)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.updated"
	before := auditCount(t, app, tenantID, event)

	newTotal := "250.00"
	updated, err := store.Update(c, invoiceID, UpdateInput{Total: &newTotal})
	if err != nil {
		t.Fatalf("Update (partial, Total only): %v", err)
	}
	if updated.Total == nil || *updated.Total != newTotal {
		t.Errorf("Update: Total = %v, want %q", updated.Total, newTotal)
	}
	if updated.Currency == nil || *updated.Currency != "NGN" {
		t.Errorf("Update: Currency = %v, want unchanged %q (Update must not touch fields the caller left nil)", updated.Currency, "NGN")
	}
	if updated.Subtotal == nil || *updated.Subtotal != "100.00" {
		t.Errorf("Update: Subtotal = %v, want unchanged %q", updated.Subtotal, "100.00")
	}

	if after := auditCount(t, app, tenantID, event); after != before+1 {
		t.Errorf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != subject {
		t.Errorf("audit actor = %q, want %q", actor, subject)
	}

	beforeAllNil := auditCount(t, app, tenantID, event)
	if _, err := store.Update(c, invoiceID, UpdateInput{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Update(all-nil) err = %v, want ErrValidation", err)
	}
	if after := auditCount(t, app, tenantID, event); after != beforeAllNil {
		t.Errorf("audit_log rows for %s after all-nil Update = %d, want unchanged %d", event, after, beforeAllNil)
	}
}

// INV-STORE-12: Update/Get/List never see a cross-tenant row (RLS): tenant A
// cannot read or update tenant B's invoice; B's row is unaffected.
func TestStoreCrossTenant_UpdateGetListRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-STORE-12 tenant A")
	tenantB := seedTenant(t, super, "INV-STORE-12 tenant B")
	entityB := seedEntity(t, super, tenantB, "INV-STORE-12 B entity")
	invoiceB := seedInvoice(t, super, tenantB, entityB, "INV-STORE-12-B")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	if _, err := store.Get(cA, invoiceB); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(B's invoice) as tenant A err = %v, want ErrNotFound", err)
	}

	newTotal := "999.99"
	if _, err := store.Update(cA, invoiceB, UpdateInput{Total: &newTotal}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(B's invoice) as tenant A err = %v, want ErrNotFound", err)
	}

	items, _, err := store.List(cA, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (as A): %v", err)
	}
	for _, inv := range items {
		if inv.ID == invoiceB {
			t.Errorf("List (as A) leaked tenant B's invoice %s", invoiceB)
		}
	}

	var total *string
	if err := super.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, invoiceB).Scan(&total); err != nil {
		t.Fatalf("read back B's invoice: %v", err)
	}
	if total != nil {
		t.Errorf("B's invoice total after refused cross-tenant Update = %v, want unchanged NULL", *total)
	}
}

// INV-STORE-13: a non-existent entity_id maps to ErrValidation (23503,
// QA #4); nothing persists.
func TestStoreCreate_NonExistentEntityIDRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-STORE-13 tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	bogusEntityID := uuid.NewString()
	_, err := store.Create(c, CreateInput{EntityID: bogusEntityID, InvoiceNumber: "INV-STORE-13"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with a non-existent entity_id err = %v, want ErrValidation (23503 foreign_key_violation)", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("invoices rows for tenant after rejected Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("line_items rows for tenant after rejected Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created'`, tenantID); n != 0 {
		t.Errorf("audit_log invoice.created rows for tenant after rejected Create = %d, want 0", n)
	}
}

// M4-06-03 (QA Mode A, RED): closes the invoices->entity leg of the D8
// cross-tenant dangling-reference residual for Store.Create. This is the
// entity_id sibling of TestStoreCreate_CrossTenantImportBatchIDFKBypassesRLS
// (import_batch_test.go), which pins the SAME shape of leak for
// ImportBatchID and — per that test's own doc comment — is deliberately left
// OPEN as an accepted residual (no AC requires a same-tenant check there,
// and the only production caller always mints a fresh same-tenant batch id).
// EntityID is different: M4-06 requires it be closed, so unlike the
// import_batch leg, a caller-supplied EntityID belonging to a DIFFERENT
// tenant must be rejected as ErrValidation rather than silently accepted.
//
// RED today: the entity_id foreign_key_violation check runs with RLS
// bypassed (same mechanism TestRLS_InvoicesCrossTenantDanglingEntityRef,
// internal/platform/db/invoices_rls_test.go, pins at the raw-SQL layer), and
// Store.Create has no tenant-scoped ownership pre-check on EntityID, so this
// Create call SUCCEEDS (err == nil) instead of returning ErrValidation. The
// fix — a composite FK (tenant_id, entity_id) -> business_entities(tenant_id,
// id) plus a friendly tenant-scoped pre-check in Store.Create — lands in a
// later subtask; this test does not add it.
func TestStoreCreate_CrossTenantEntityIDRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "M4-06-03 tenant A")
	tenantB := seedTenant(t, super, "M4-06-03 tenant B")
	entityB := seedEntity(t, super, tenantB, "M4-06-03 B entity")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	_, err := store.Create(cA, CreateInput{EntityID: entityB, InvoiceNumber: "INV-XT-1"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (tenant A, entity_id = tenant B's entity) err = %v, want ErrValidation (M4-06 closes the invoices->entity leg of the D8 cross-tenant residual)", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantA); n != 0 {
		t.Errorf("invoices rows for tenant A after rejected cross-tenant Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created'`, tenantA); n != 0 {
		t.Errorf("audit_log invoice.created rows for tenant A after rejected cross-tenant Create = %d, want 0", n)
	}
}

// TestStoreCreate_CrossTenantEntityIDRejectedNoPartialLineItemsWrite (QA Mode
// B adversarial coverage, M4-06-03): closes a vacuous-assertion gap in
// TestStoreCreate_CrossTenantEntityIDRejected directly above. That test's own
// CreateInput carries NO LineItems, so its (pre-existing-pattern) zero-rows
// check on line_items would pass trivially even if the pre-check/FK backstop
// were broken -- there was never anything to write in the first place. This
// variant supplies a non-empty LineItems slice so the zero-rows assertion is
// actually discriminating: it proves the tenant-ownership pre-check rejects
// BEFORE the invoices INSERT starts the write sequence, so line_items (which
// would otherwise be written in step (2), right after the invoices row) never
// gets a single row either.
func TestStoreCreate_CrossTenantEntityIDRejectedNoPartialLineItemsWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "M4-06-03 tenant A (line items)")
	tenantB := seedTenant(t, super, "M4-06-03 tenant B (line items)")
	entityB := seedEntity(t, super, tenantB, "M4-06-03 B entity (line items)")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	descA, descB := "Widget A", "Widget B"
	_, err := store.Create(cA, CreateInput{
		EntityID:      entityB,
		InvoiceNumber: "INV-XT-LI-1",
		LineItems: []LineItemInput{
			{Description: &descA},
			{Description: &descB},
		},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (tenant A, entity_id = tenant B's entity, 2 line items) err = %v, want ErrValidation", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantA); n != 0 {
		t.Errorf("invoices rows for tenant A after rejected cross-tenant Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, tenantA); n != 0 {
		t.Errorf("line_items rows for tenant A after rejected cross-tenant Create = %d, want 0 (no partial write -- the pre-check rejects before the invoices INSERT even starts the write sequence)", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created'`, tenantA); n != 0 {
		t.Errorf("audit_log invoice.created rows for tenant A after rejected cross-tenant Create = %d, want 0", n)
	}
}
