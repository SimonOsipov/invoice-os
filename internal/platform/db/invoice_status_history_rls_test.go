// M4-01-04 (task-95): tests for the `invoice_status_history` tenant-owned, APPEND-ONLY
// table, written BEFORE the migration exists (RED against SQLSTATE 42P01
// undefined_table). The table the Executor will add (Simon Vault "M4-01 Invoice Spine
// Migrations" §System Design #4):
//
//	invoice_status_history: id uuid PK, tenant_id uuid NOT NULL REFERENCES tenants(id)
//	    ON DELETE CASCADE, invoice_id uuid NOT NULL REFERENCES invoices(id) ON DELETE
//	    CASCADE, from_status text NULLABLE (CHECK from_status IS NULL OR IN the 7-state
//	    set — the first transition has no predecessor), to_status text NOT NULL (CHECK
//	    the 7-state set), actor text NOT NULL (CHECK char_length(actor) > 0 — the
//	    audit_log.actor precedent, a GoTrue subject uuid or 'system'), changed_at
//	    timestamptz NOT NULL DEFAULT now() — verbatim M2-06 FORCE-RLS `tenant_isolation`
//	    policy, APPEND-ONLY GRANT SELECT/INSERT only (no UPDATE/DELETE) TO invoice_app —
//	    the idempotency_keys precedent (grants only, NO owner-proof trigger, unlike
//	    audit_log — D10).
//
// ISH-RLS-01..06 attack the same isolation guarantees M2-07 (rls_test.go) proves for the
// tenants/rls_fixture shape and M4-01-01/02/03 transplant onto real tables, applied here
// to invoice_status_history — MINUS the own-row-reassignment case: that case is an
// UPDATE, and this table grants invoice_app NO UPDATE privilege at all (see ISH-RLS-07),
// so any UPDATE — reassignment or not — fails identically at the GRANT layer before RLS
// is even reached. ISH-RLS-07..13 are table-specific: the two append-only grant proofs
// (UPDATE/DELETE both refused at 42501, the idempotency_keys pattern — no owner-proof
// trigger per D10), the from_status/to_status/actor CHECKs, the invoice_id CASCADE, and
// the D8 cross-tenant dangling-reference residual (documented, not defended — see the
// story's QA-Verify disposition [2]).
//
// A critical distinction runs through ISH-RLS-03 and ISH-RLS-07: because invoice_app has
// NO UPDATE grant on this table (SELECT/INSERT only — the append-only design), EVERY
// UPDATE attempt — cross-tenant (03) or same-tenant/own-row (07) — is refused at the
// GRANT layer (SQLSTATE 42501 insufficient_privilege), raised before Postgres even
// evaluates the RLS policy's USING clause. This differs from the "UPDATE affects zero
// rows, no error" pattern the other three (grant-bearing) M4-01 tables exhibit for a
// cross-tenant UPDATE (see TestRLS_InvoicesCrossTenantUpdateAffectsZeroRows) — there the
// UPDATE statement itself succeeds (the grant exists) but RLS filters the target row
// out, so zero rows match and no error is raised. Here the statement never gets that
// far, so ISH-RLS-03 asserts 42501 instead of RowsAffected()==0.
//
// Rows are seeded per-test (seedStatusHistory below, reusing seedBusinessEntity from
// business_entities_rls_test.go and seedInvoice from invoices_rls_test.go for parent
// rows), NOT in the shared harness.seed() in rls_harness_test.go — that runs in TestMain
// before every test in the package, so a missing invoice_status_history table would
// break the ENTIRE suite instead of failing only these ISH-RLS cases.
//
// Spec-to-test map (Test Specs table, M4-01 story / task-95):
//
//	ISH-RLS-01 TestRLS_InvoiceStatusHistoryCrossTenantSelectRefused
//	ISH-RLS-02 TestRLS_InvoiceStatusHistoryCrossTenantInsertRefused
//	ISH-RLS-03 TestRLS_InvoiceStatusHistoryCrossTenantUpdateRefused
//	ISH-RLS-04 TestRLS_InvoiceStatusHistoryMissingContextFailsClosed
//	ISH-RLS-05 TestRLS_InvoiceStatusHistoryOwnTenantInsertSucceedsWithDefaults
//	ISH-RLS-06 TestRLS_InvoiceStatusHistoryOwnerInsertRefusedUnderForce
//	ISH-RLS-07 TestRLS_InvoiceStatusHistoryAppendOnlyUpdateRefused
//	ISH-RLS-08 TestRLS_InvoiceStatusHistoryAppendOnlyDeleteRefused
//	ISH-RLS-09 TestRLS_InvoiceStatusHistoryFromStatusNullAccepted
//	ISH-RLS-10 TestRLS_InvoiceStatusHistoryToStatusCheck
//	ISH-RLS-11 TestRLS_InvoiceStatusHistoryActorCheck
//	ISH-RLS-12 TestRLS_InvoiceStatusHistoryInvoiceDeleteCascades
//	ISH-RLS-13 TestRLS_InvoiceStatusHistoryCrossTenantDanglingInvoiceRef
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
//	go test -count=1 -run TestRLS_InvoiceStatusHistory ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedStatusHistory inserts one invoice_status_history row for tenantID/invoiceID as
// the superuser (BYPASSRLS, so seeding needs no tenant context), with a fixed valid
// from/to/actor triple (NULL -> 'validated', actor 'system'), and returns its id plus a
// cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed().
func seedStatusHistory(t *testing.T, tenantID, invoiceID string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1, $2, $3, NULL, 'validated', 'system')`,
		id, tenantID, invoiceID,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed invoice_status_history: undefined_table (42P01) — invoice_status_history migration not applied yet: %v", err)
		}
		t.Fatalf("seed invoice_status_history: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, id)
	}
}

// ISH-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's invoice_status_history row; B's is invisible (filtered out, not an error).
func TestRLS_InvoiceStatusHistoryCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "ISH-01 B Corp")
	defer cleanupEntityB()

	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-01-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "ISH-01-B")
	defer cleanupInvoiceB()

	_, cleanupA := seedStatusHistory(t, h.tenantA, invoiceA)
	defer cleanupA()
	_, cleanupB := seedStatusHistory(t, h.tenantB, invoiceB)
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// ISH-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_InvoiceStatusHistoryCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "ISH-02 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "ISH-02-B")
	defer cleanupInvoiceB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'validated', 'system')`,
			h.tenantB, invoiceB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// ISH-RLS-03: a cross-tenant UPDATE is refused — but NOT with the "affects zero rows, no
// error" pattern the other three (grant-bearing) M4-01 tables exhibit (see
// TestRLS_InvoicesCrossTenantUpdateAffectsZeroRows and its line_items/import_batches
// siblings). invoice_app has NO UPDATE grant on invoice_status_history at all (SELECT/
// INSERT only — the append-only design, ISH-RLS-07/08), so the UPDATE statement itself
// is refused at the GRANT layer (SQLSTATE 42501 insufficient_privilege) before Postgres
// ever evaluates which rows the RLS policy would let it see. Targets tenant B's row
// (which A cannot even see) to prove the refusal fires regardless of the target's
// visibility.
func TestRLS_InvoiceStatusHistoryCrossTenantUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "ISH-03 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "ISH-03-B")
	defer cleanupInvoiceB()
	_, cleanupHistory := seedStatusHistory(t, h.tenantB, invoiceB)
	defer cleanupHistory()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoice_status_history SET actor = 'hacked' WHERE tenant_id = $1`, h.tenantB)
		return e
	})
	if err == nil {
		t.Fatal("cross-tenant UPDATE on invoice_status_history succeeded, want permission denied (SQLSTATE 42501, no UPDATE grant)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("cross-tenant UPDATE on invoice_status_history: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
}

// ISH-RLS-04: a missing app.current_tenant GUC fails closed — with no context set, the
// isolation predicate is false for every row and the connection sees nothing.
func TestRLS_InvoiceStatusHistoryMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM invoice_status_history`); n != 0 {
		t.Errorf("invoice_status_history visible with no tenant set = %d, want 0", n)
	}
}

// ISH-RLS-05: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK and the
// tenants(id)/invoices(id) FKs coexist for a same-tenant write, the row becomes visible,
// and from_status/changed_at actually default as designed (NULL / populated) when
// from_status is not named on INSERT.
func TestRLS_InvoiceStatusHistoryOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-05 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-05-A")
	defer cleanupInvoiceA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'draft', 'system') RETURNING id`,
			h.tenantA, invoiceA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			fromStatus *string
			changedAt  string
		)
		if e := tx.QueryRow(ctx,
			`SELECT from_status, changed_at::text FROM invoice_status_history WHERE id = $1`,
			id,
		).Scan(&fromStatus, &changedAt); e != nil {
			return e
		}
		if fromStatus != nil {
			t.Errorf("from_status default = %q, want NULL", *fromStatus)
		}
		if changedAt == "" {
			t.Errorf("changed_at default = empty, want a populated timestamp")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}
}

// ISH-RLS-06: the table OWNER (invoice_migrator) is bound by the policy under FORCE
// exactly like the `tenants` template — a cross-tenant INSERT is refused even for the
// owner, SQLSTATE 42501.
func TestRLS_InvoiceStatusHistoryOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "ISH-06 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "ISH-06-B")
	defer cleanupInvoiceB()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'validated', 'system')`,
			h.tenantB, invoiceB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// ISH-RLS-07: append-only. invoice_app has NO UPDATE grant on invoice_status_history
// (SELECT/INSERT only — the idempotency_keys precedent, D10). An UPDATE of the app's
// OWN, visible row is refused at the GRANT layer, SQLSTATE 42501, before RLS is even
// evaluated — distinct from the RLS-policy 42501 that ISH-RLS-02/06 raise on a WITH
// CHECK violation. The row must survive untouched.
func TestRLS_InvoiceStatusHistoryAppendOnlyUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-07 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-07-A")
	defer cleanupInvoiceA()
	id, cleanupHistory := seedStatusHistory(t, h.tenantA, invoiceA)
	defer cleanupHistory()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoice_status_history SET actor = 'hacked' WHERE tenant_id = $1`, h.tenantA)
		return e
	})
	if err == nil {
		t.Fatal("app-role UPDATE of an own, visible invoice_status_history row succeeded, want permission denied (SQLSTATE 42501, no UPDATE grant)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role UPDATE on invoice_status_history: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	var actor string
	if err := h.super.QueryRow(ctx, `SELECT actor FROM invoice_status_history WHERE id = $1`, id).Scan(&actor); err != nil {
		t.Fatalf("read back actor after refused UPDATE: %v", err)
	}
	if actor != "system" {
		t.Errorf("actor after refused UPDATE = %q, want unchanged %q", actor, "system")
	}
}

// ISH-RLS-08: append-only. invoice_app has NO DELETE grant on invoice_status_history —
// even a same-tenant DELETE on a row the app can otherwise see is refused at the GRANT
// layer, SQLSTATE 42501, and the row must survive.
func TestRLS_InvoiceStatusHistoryAppendOnlyDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-08 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-08-A")
	defer cleanupInvoiceA()
	id, cleanupHistory := seedStatusHistory(t, h.tenantA, invoiceA)
	defer cleanupHistory()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM invoice_status_history WHERE tenant_id = $1`, h.tenantA)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on invoice_status_history succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on invoice_status_history: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoice_status_history WHERE id = $1`, id); n != 1 {
		t.Errorf("row count after refused DELETE = %d, want 1 (row must survive)", n)
	}
}

// ISH-RLS-09: from_status accepts NULL — the initial transition (NULL -> 'draft') has
// no predecessor state.
func TestRLS_InvoiceStatusHistoryFromStatusNullAccepted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-09 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-09-A")
	defer cleanupInvoiceA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
			 VALUES ($1, $2, NULL, 'draft', 'system') RETURNING id`,
			h.tenantA, invoiceA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert with from_status = NULL (initial transition): want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, id)
	}()

	var fromStatus *string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT from_status FROM invoice_status_history WHERE id = $1`, id).Scan(&fromStatus)
	})
	if err != nil {
		t.Fatalf("read back from_status: %v", err)
	}
	if fromStatus != nil {
		t.Errorf("from_status read back = %q, want NULL", *fromStatus)
	}
}

// ISH-RLS-10: the `to_status` CHECK rejects a value outside the 7-state lifecycle set
// (23514) and accepts each of the 7 canonical states, round-tripping correctly.
func TestRLS_InvoiceStatusHistoryToStatusCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-10 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-10-A")
	defer cleanupInvoiceA()

	// A bogus to_status is rejected.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'bogus', 'system')`,
			h.tenantA, invoiceA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with to_status = 'bogus' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with to_status = 'bogus': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// Each of the 7 canonical states is accepted and round-trips.
	for _, want := range []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"} {
		var id string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, $3, 'system') RETURNING id`,
				h.tenantA, invoiceA, want,
			).Scan(&id)
		})
		if err != nil {
			t.Fatalf("insert with to_status = %q: want success, got: %v", want, err)
		}
		defer func(rowID string) {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, rowID)
		}(id)

		var got string
		err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT to_status FROM invoice_status_history WHERE id = $1`, id).Scan(&got)
		})
		if err != nil {
			t.Fatalf("read back to_status for %q: %v", want, err)
		}
		if got != want {
			t.Errorf("to_status read back = %q, want %q", got, want)
		}
	}
}

// ISH-RLS-11: the `actor` CHECK rejects an empty string (23514) — actor must be a
// non-empty GoTrue subject uuid or 'system' (the audit_log.actor precedent).
func TestRLS_InvoiceStatusHistoryActorCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-11 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-11-A")
	defer cleanupInvoiceA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'draft', '')`,
			h.tenantA, invoiceA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with actor = '' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with actor = '': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
}

// ISH-RLS-12: invoice_id is ON DELETE CASCADE. Deleting the parent invoices row removes
// its invoice_status_history rows.
func TestRLS_InvoiceStatusHistoryInvoiceDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "ISH-12 A Corp")
	defer cleanupEntityA()
	// Unlike LI-RLS-10 (which safely discards the invoice cleanup because its own
	// DELETE runs right after seedLineItem succeeds), here seedStatusHistory is the
	// very next fallible call and — in the RED state — fails via t.Fatalf before this
	// test's own `DELETE FROM invoices` statement is ever reached. A discarded cleanup
	// would orphan invoiceA (and, since entity_id is ON DELETE RESTRICT, the deferred
	// cleanupEntityA above would then also silently fail against the still-referencing
	// invoice). So the invoice cleanup MUST be deferred immediately; the DELETE below
	// still exercises the CASCADE, and this deferred cleanup becomes a harmless
	// already-gone no-op afterward.
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "ISH-12-A")
	defer cleanupInvoiceA()
	historyID, cleanupHistory := seedStatusHistory(t, h.tenantA, invoiceA)
	defer cleanupHistory()

	if _, err := h.super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceA); err != nil {
		t.Fatalf("delete parent invoices row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoice_status_history WHERE id = $1`, historyID); n != 0 {
		t.Errorf("invoice_status_history rows after invoice delete = %d, want 0 (invoice_id ON DELETE CASCADE)", n)
	}
}

// ISH-RLS-13 (D8 cross-tenant dangling-ref, DOCUMENTING not defending): as tenant A,
// INSERT a status-history row whose invoice_id belongs to tenant B's invoices row. The
// FK check bypasses RLS (Postgres referential-integrity triggers run with elevated
// internal privilege), and the row's own tenant_id = A passes the WITH CHECK — so this
// SUCCEEDS. This pins the accepted D8 residual: tenant-owned→tenant-owned FKs are plain
// per-column FKs, not composite same-tenant FKs (story QA-Verify disposition [2]). The
// second half proves it is not a READ leak: a join from the history row to invoices
// under A's RLS returns ZERO parent rows — B's invoice row stays invisible to A, so the
// reference dangles from A's view rather than opening a window into B's data. If a
// future story adopts composite same-tenant FKs, this spec flips to expect 23503.
func TestRLS_InvoiceStatusHistoryCrossTenantDanglingInvoiceRef(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "ISH-13 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "ISH-13-B")
	defer cleanupInvoiceB()

	var historyID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'validated', 'system') RETURNING id`,
			h.tenantA, invoiceB,
		).Scan(&historyID)
	})
	if err != nil {
		t.Fatalf("insert status history with cross-tenant invoice_id (documenting D8 residual): want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, historyID)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		n := mustCount(t, tx,
			`SELECT count(*) FROM invoice_status_history h JOIN invoices i ON h.invoice_id = i.id WHERE h.id = $1`,
			historyID,
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
