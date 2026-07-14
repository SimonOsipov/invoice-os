// M4-01-03 (task-94): tests for the `line_items` tenant-owned child table, written
// BEFORE the migration exists (RED against SQLSTATE 42P01 undefined_table). The
// table the Executor will add (Simon Vault "M4-01 Invoice Spine Migrations" §System
// Design #3):
//
//	line_items: id uuid PK (the stable line id the no-duplicate-line-items CEL rule
//	    keys on), tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
//	    invoice_id uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE (lines are
//	    inseparable from their invoice), line_no integer NOT NULL (system-assigned
//	    ordinal), UNIQUE (invoice_id, line_no). MBS-content columns (description,
//	    quantity numeric(14,3), unit_price/line_total/line_tax numeric(14,2)) are
//	    NULLABLE and un-CHECKed — store-invalid, so M4-04 can report violations
//	    instead of the schema hard-rejecting an invalid import. created_at
//	    timestamptz NOT NULL DEFAULT now() — verbatim M2-06 FORCE-RLS
//	    `tenant_isolation` policy, GRANT SELECT/INSERT/UPDATE (no DELETE) TO
//	    invoice_app.
//
// Each of LI-RLS-01..07 attacks the same guarantees M2-07 (rls_test.go) proves for
// the tenants/rls_fixture shape and M4-01-01/M4-01-02 (import_batches_rls_test.go /
// invoices_rls_test.go) transplant onto real tables, applied here to line_items.
// LI-RLS-08..12 are table-specific: the UNIQUE (invoice_id, line_no) ordinal guard,
// the invoice_id CASCADE, the store-invalid guarantee, and the D8 cross-tenant
// dangling-reference residual (documented, not defended — see the story's
// QA-Verify disposition [2]).
//
// Rows are seeded per-test (seedLineItem below, reusing seedBusinessEntity from
// business_entities_rls_test.go and seedInvoice from invoices_rls_test.go for parent
// rows), NOT in the shared harness.seed() in rls_harness_test.go — that runs in
// TestMain before every test in the package, so a missing line_items table would
// break the ENTIRE suite instead of failing only these LI-RLS cases.
//
// Spec-to-test map (Test Specs table, M4-01 story / task-94):
//
//	LI-RLS-01 TestRLS_LineItemsCrossTenantSelectRefused
//	LI-RLS-02 TestRLS_LineItemsCrossTenantInsertRefused
//	LI-RLS-03 TestRLS_LineItemsCrossTenantUpdateAffectsZeroRows
//	LI-RLS-04 TestRLS_LineItemsMissingContextFailsClosed
//	LI-RLS-05 TestRLS_LineItemsOwnTenantInsertSucceedsWithDefaults
//	LI-RLS-06 TestRLS_LineItemsOwnerInsertRefusedUnderForce
//	LI-RLS-07 TestRLS_LineItemsOwnRowReassignmentRefused
//	LI-RLS-08 TestRLS_LineItemsUniqueLineNoDuplicateRejected
//	LI-RLS-09 TestRLS_LineItemsSameLineNoDifferentInvoiceAllowed
//	LI-RLS-10 TestRLS_LineItemsInvoiceDeleteCascades
//	LI-RLS-11 TestRLS_LineItemsStoreInvalidNullContentSucceeds
//	LI-RLS-12 TestRLS_LineItemsCrossTenantDanglingInvoiceRef
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
//	go test -count=1 -run TestRLS_LineItems ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedLineItem inserts one line_items row for tenantID/invoiceID/lineNo as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus
// a cleanup func. Scoped per-test — see the package doc comment above for why this
// must NOT move into the shared harness.seed().
func seedLineItem(t *testing.T, tenantID, invoiceID string, lineNo int) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO line_items (id, tenant_id, invoice_id, line_no) VALUES ($1, $2, $3, $4)`,
		id, tenantID, invoiceID, lineNo,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed line_items: undefined_table (42P01) — line_items migration not applied yet: %v", err)
		}
		t.Fatalf("seed line_items: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, id)
	}
}

// LI-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's line_items row; B's is invisible (filtered out, not an error).
func TestRLS_LineItemsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-01 B Corp")
	defer cleanupEntityB()

	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-01-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-01-B")
	defer cleanupInvoiceB()

	_, cleanupA := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupA()
	_, cleanupB := seedLineItem(t, h.tenantB, invoiceB, 1)
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// LI-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_LineItemsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-02 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-02-B")
	defer cleanupInvoiceB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1)`,
			h.tenantB, invoiceB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// LI-RLS-03: an UPDATE that targets another tenant's rows affects zero rows and
// raises no error — B's row is simply invisible to a tx scoped to A.
func TestRLS_LineItemsCrossTenantUpdateAffectsZeroRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-03 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-03-B")
	defer cleanupInvoiceB()
	_, cleanupLine := seedLineItem(t, h.tenantB, invoiceB, 1)
	defer cleanupLine()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE line_items SET description = 'hacked' WHERE tenant_id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("cross-tenant UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant UPDATE (expected 0 rows): %v", err)
	}
}

// LI-RLS-04: a missing app.current_tenant GUC fails closed — with no context set,
// the isolation predicate is false for every row and the connection sees nothing.
func TestRLS_LineItemsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM line_items`); n != 0 {
		t.Errorf("line_items visible with no tenant set = %d, want 0", n)
	}
}

// LI-RLS-05: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK and the
// tenants(id)/invoices(id) FKs coexist for a same-tenant write, the row becomes
// visible, and the content columns actually default to NULL (nothing named on
// INSERT) while created_at is populated as designed.
func TestRLS_LineItemsOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-05 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-05-INV")
	defer cleanupInvoiceA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1) RETURNING id`,
			h.tenantA, invoiceA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			description, quantity, unitPrice, lineTotal, lineTax *string
			createdAt                                            string
		)
		if e := tx.QueryRow(ctx,
			`SELECT description, quantity::text, unit_price::text, line_total::text, line_tax::text, created_at::text
			 FROM line_items WHERE id = $1`,
			id,
		).Scan(&description, &quantity, &unitPrice, &lineTotal, &lineTax, &createdAt); e != nil {
			return e
		}
		if description != nil {
			t.Errorf("description default = %q, want NULL", *description)
		}
		if quantity != nil {
			t.Errorf("quantity default = %q, want NULL", *quantity)
		}
		if unitPrice != nil {
			t.Errorf("unit_price default = %q, want NULL", *unitPrice)
		}
		if lineTotal != nil {
			t.Errorf("line_total default = %q, want NULL", *lineTotal)
		}
		if lineTax != nil {
			t.Errorf("line_tax default = %q, want NULL", *lineTax)
		}
		if createdAt == "" {
			t.Errorf("created_at default = empty, want a populated timestamp")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}
}

// LI-RLS-06: the table OWNER (invoice_migrator) is bound by the policy under FORCE
// exactly like the `tenants` template — a cross-tenant INSERT is refused even for
// the owner, SQLSTATE 42501.
func TestRLS_LineItemsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-06 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-06-B")
	defer cleanupInvoiceB()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1)`,
			h.tenantB, invoiceB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// LI-RLS-07: reassigning an OWN, visible row to another tenant is refused. This is
// the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_LineItemsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-07 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-07-A")
	defer cleanupInvoiceA()
	_, cleanupLine := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupLine()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE line_items SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}

// LI-RLS-08: the unique guard UNIQUE (invoice_id, line_no) rejects a second row with
// the same ordinal within one invoice, SQLSTATE 23505.
func TestRLS_LineItemsUniqueLineNoDuplicateRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-08 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-08-A")
	defer cleanupInvoiceA()

	_, cleanupFirst := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupFirst()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1)`,
			h.tenantA, invoiceA,
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate (invoice_id, line_no) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate (invoice_id, line_no): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
}

// LI-RLS-09: the SAME line_no under a DIFFERENT invoice, same tenant, is allowed —
// the guard is per invoice, not global.
func TestRLS_LineItemsSameLineNoDifferentInvoiceAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-09 A Corp")
	defer cleanupEntityA()
	invoice1, cleanupInvoice1 := seedInvoice(t, h.tenantA, entityA, "LI-09-INV-1")
	defer cleanupInvoice1()
	invoice2, cleanupInvoice2 := seedInvoice(t, h.tenantA, entityA, "LI-09-INV-2")
	defer cleanupInvoice2()

	_, cleanupFirst := seedLineItem(t, h.tenantA, invoice1, 1)
	defer cleanupFirst()

	var secondID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1) RETURNING id`,
			h.tenantA, invoice2,
		).Scan(&secondID)
	})
	if err != nil {
		t.Fatalf("same line_no under a different invoice: want success, got: %v", err)
	}
	_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, secondID)
}

// LI-RLS-10: invoice_id is ON DELETE CASCADE. Deleting the parent invoices row
// removes its line_items — lines are an inseparable part of their invoice.
func TestRLS_LineItemsInvoiceDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-10 A Corp")
	defer cleanupEntityA()
	invoiceA, _ := seedInvoice(t, h.tenantA, entityA, "LI-10-A")
	lineID, cleanupLine := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupLine()

	if _, err := h.super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceA); err != nil {
		t.Fatalf("delete parent invoices row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM line_items WHERE id = $1`, lineID); n != 0 {
		t.Errorf("line_items rows after invoice delete = %d, want 0 (invoice_id ON DELETE CASCADE)", n)
	}
}

// LI-RLS-11: store-invalid. A line with description/quantity/unit_price all NULL
// INSERTs successfully because MBS-content columns carry no CHECK (D2): the
// import→validate→fix loop requires an invalid row be storable so M4-04 can later
// report *why* it is invalid.
func TestRLS_LineItemsStoreInvalidNullContentSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-11 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-11-A")
	defer cleanupInvoiceA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price)
			 VALUES ($1, $2, 1, NULL, NULL, NULL) RETURNING id`,
			h.tenantA, invoiceA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert with NULL description/quantity/unit_price: want success (store-invalid, no content CHECK), got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, id)
	}()

	var (
		description *string
		quantity    *string
		unitPrice   *string
	)
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT description, quantity::text, unit_price::text FROM line_items WHERE id = $1`, id,
		).Scan(&description, &quantity, &unitPrice)
	})
	if err != nil {
		t.Fatalf("read back invalid line item: %v", err)
	}
	if description != nil {
		t.Errorf("description read back = %q, want NULL", *description)
	}
	if quantity != nil {
		t.Errorf("quantity read back = %q, want NULL", *quantity)
	}
	if unitPrice != nil {
		t.Errorf("unit_price read back = %q, want NULL", *unitPrice)
	}
}

// LI-RLS-12 (D8 cross-tenant dangling-ref, DOCUMENTING not defending): as tenant A,
// INSERT a line_item whose invoice_id belongs to tenant B's invoices row. The FK
// check bypasses RLS (Postgres referential-integrity triggers run with elevated
// internal privilege), and the row's own tenant_id = A passes the WITH CHECK — so
// this SUCCEEDS. This pins the accepted D8 residual: tenant-owned→tenant-owned FKs
// are plain per-column FKs, not composite same-tenant FKs (story QA-Verify
// disposition [2]). The second half proves it is not a READ leak: a join from the
// line_item to invoices under A's RLS returns ZERO parent rows — B's invoice row
// stays invisible to A, so the reference dangles from A's view rather than opening a
// window into B's data. If a future story adopts composite same-tenant FKs, this
// spec flips to expect 23503.
func TestRLS_LineItemsCrossTenantDanglingInvoiceRef(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-12 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-12-B")
	defer cleanupInvoiceB()

	var lineID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1) RETURNING id`,
			h.tenantA, invoiceB,
		).Scan(&lineID)
	})
	if err != nil {
		t.Fatalf("insert line_item with cross-tenant invoice_id (documenting D8 residual): want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, lineID)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		n := mustCount(t, tx,
			`SELECT count(*) FROM line_items l JOIN invoices i ON l.invoice_id = i.id WHERE l.id = $1`,
			lineID,
		)
		if n != 0 {
			t.Errorf("join to cross-tenant parent invoice under A's RLS = %d rows, want 0 (no read leak)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (join check): %v", err)
	}
}

// (QA-added, least-privilege proof): invoice_tenant_reader has NO grant on
// line_items at all (unlike tenants, which grants it SELECT via tenant_enumerate —
// see the migration header: "Deliberately NO tenant_enumerate/invoice_tenant_reader
// policy"). A bare SELECT as that role must fail at the GRANT level (SQLSTATE 42501
// insufficient_privilege) before RLS is even evaluated — proving the table was never
// exposed to the one cross-tenant enumeration identity. None of LI-RLS-01..12 connect
// as h.reader, so a future migration that widened the GRANT would slip through
// unnoticed without this case (same guarantee TestRLS_InvoicesReaderHasNoGrant /
// TestRLS_ImportBatchesReaderHasNoGrant prove for their tables).
func TestRLS_LineItemsReaderHasNoGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM line_items`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on line_items succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on line_items: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
}

// (QA-added, least-privilege proof): invoice_app has NO DELETE grant on line_items —
// the migration grants only SELECT/INSERT/UPDATE (SIU, not SIUD — "no DELETE" in the
// header: "the fix loop (M4-05) edits rows in place — no DELETE"). Even a same-tenant
// DELETE on a row the app can otherwise see/update must be refused at the GRANT level
// (42501), never reaching RLS's policy evaluation, and the row must survive untouched.
// None of LI-RLS-01..12 exercise DELETE, so a future migration that widened the GRANT
// would slip through unnoticed without this case.
func TestRLS_LineItemsDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-DEL A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-DEL-A")
	defer cleanupInvoiceA()
	id, cleanupLine := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupLine()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM line_items WHERE tenant_id = $1`, h.tenantA)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on line_items succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on line_items: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM line_items WHERE id = $1`, id); n != 1 {
		t.Errorf("row count after refused DELETE = %d, want 1 (row must survive)", n)
	}
}

// (QA-added, cascade-is-FK-driven-not-grant-driven proof, belt-and-suspenders vs
// LI-RLS-10 and TestRLS_LineItemsDeleteRefused above): line_items grants invoice_app
// NO DELETE at all — proven above — so the app role can never directly remove a
// line_items row. LI-RLS-10 proves the CASCADE removes lines when the parent invoice
// is deleted, but does so via h.super, which BYPASSES every grant and every RLS
// policy, so it cannot distinguish "the CASCADE fired" from "the superuser can do
// anything anyway". This case re-proves the cascade using h.mig — the table OWNER,
// which is bound by FORCE RLS exactly like every other role (LI-RLS-06) and was never
// explicitly GRANTed DELETE on either table (ownership alone confers full privileges,
// same as invoice_migrator's implicit rights on every M4-01 table) — under a real
// tenant-scoped transaction. The line still disappears, proving the referential-action
// CASCADE is driven by the FK constraint itself, not by any DELETE grant on line_items.
func TestRLS_LineItemsCascadeDrivenByFKNotGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-CASC A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-CASC-A")
	defer cleanupInvoiceA() // no-op: DELETE-by-id after the tx below already removed it
	lineID, cleanupLine := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupLine() // no-op: DELETE-by-id after the cascade already removed it

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceA)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("owner (h.mig) tenant-scoped invoice DELETE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("owner-role (h.mig) tenant-scoped DELETE of the parent invoice: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM line_items WHERE id = $1`, lineID); n != 0 {
		t.Errorf("line_items rows after owner-driven cascade delete = %d, want 0 (CASCADE must fire regardless of the child table's own grants)", n)
	}
}

// (QA-added, unique guard belt-and-suspenders vs LI-RLS-08): LI-RLS-08 seeds its FIRST
// row via the superuser (BYPASSRLS) and only exercises the SECOND insert through an
// ordinary app-role tenant-context write. This case proves the guard holds when BOTH
// sides of the collision are ordinary same-tenant app-role writes end-to-end — ruling
// out any (implausible but unverified) path where the constraint only bites against
// superuser-seeded rows (same proof TestRLS_InvoicesUniqueGuardBothRowsViaTenantContext
// gives for invoices).
func TestRLS_LineItemsUniqueGuardBothRowsViaTenantContext(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-DUP2 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-DUP2-INV")
	defer cleanupInvoiceA()

	var firstID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1) RETURNING id`,
			h.tenantA, invoiceA,
		).Scan(&firstID)
	})
	if err != nil {
		t.Fatalf("first app-role tenant-context insert: want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM line_items WHERE id = $1`, firstID)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no) VALUES ($1, $2, 1)`,
			h.tenantA, invoiceA,
		)
		return e
	})
	if err == nil {
		t.Fatal("second app-role tenant-context insert with the same (invoice_id, line_no) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("second app-role tenant-context insert (duplicate ordinal): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
}

// (QA-added, unique guard belt-and-suspenders vs LI-RLS-08): the guard must also catch
// a collision created by an UPDATE, not just a fresh INSERT — renaming an existing
// line's line_no onto a sibling's, within the SAME invoice, is refused just like a
// duplicate INSERT would be (SQLSTATE 23505). Regression target: a future rewrite of
// the guard as an INSERT-only trigger instead of a true UNIQUE constraint would pass
// LI-RLS-08 but fail this case (same proof TestRLS_InvoicesUniqueGuardUpdateCollision
// gives for invoices).
func TestRLS_LineItemsUniqueGuardUpdateCollision(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-UPDDUP A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-UPDDUP-INV")
	defer cleanupInvoiceA()

	_, cleanupSibling := seedLineItem(t, h.tenantA, invoiceA, 1) // the TARGET ordinal
	defer cleanupSibling()
	victimID, cleanupVictim := seedLineItem(t, h.tenantA, invoiceA, 2) // the SOURCE, renamed below
	defer cleanupVictim()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE line_items SET line_no = 1 WHERE id = $1`, victimID)
		return e
	})
	if err == nil {
		t.Fatal("UPDATE renaming a line_no onto a sibling's within the same invoice succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("UPDATE collision on (invoice_id, line_no): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}

	// The victim row must be untouched by the refused UPDATE.
	var stillLineNo int
	if err := h.super.QueryRow(ctx, `SELECT line_no FROM line_items WHERE id = $1`, victimID).Scan(&stillLineNo); err != nil {
		t.Fatalf("read back victim line_no: %v", err)
	}
	if stillLineNo != 2 {
		t.Errorf("victim line_no after refused UPDATE = %d, want unchanged 2", stillLineNo)
	}
}

// (QA-added, isolation completeness — belt-and-suspenders vs LI-RLS-01): with TWO
// tenants' line_items coexisting, an UNFILTERED count (no WHERE tenant_id clause) as
// tenant A returns ONLY A's row count. LI-RLS-01 always filters by tenant_id in the
// query itself, which would still return the right count even if RLS silently did
// nothing and the query happened to filter correctly; this case removes that filter so
// RLS is the ONLY thing narrowing the result set (same proof
// TestRLS_ImportBatchesUnfilteredSelectSeesOnlyOwnTenant gives for import_batches).
func TestRLS_LineItemsUnfilteredCountSeesOnlyOwnTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "LI-ISO A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "LI-ISO B Corp")
	defer cleanupEntityB()

	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "LI-ISO-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "LI-ISO-B")
	defer cleanupInvoiceB()

	_, cleanupA := seedLineItem(t, h.tenantA, invoiceA, 1)
	defer cleanupA()
	_, cleanupB1 := seedLineItem(t, h.tenantB, invoiceB, 1)
	defer cleanupB1()
	_, cleanupB2 := seedLineItem(t, h.tenantB, invoiceB, 2)
	defer cleanupB2()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM line_items`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}
