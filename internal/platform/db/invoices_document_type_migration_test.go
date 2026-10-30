// Suite for invoices.document_type and invoices.original_invoice_id (CRDDBTNOTE-01-01).
package db_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const invoicesDocTypeGlob = "*_invoices_document_type.sql"

var invoicesDocTypeConstraints = []string{
	"invoices_document_type_check", "invoices_document_type_not_null",
	"invoices_original_invoice_fk", "invoices_original_matches_type",
}

const invoicesDocTypeIndex = "invoices_original_invoice_idx"

// invoicesDocTypeMigration returns the migration's Up or Down body, comments stripped.
func invoicesDocTypeMigration(t *testing.T, section string) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, invoicesDocTypeGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", invoicesDocTypeGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want 1 -- scaffold it with `make migrate-create name=invoices_document_type`",
			len(matches), invoicesDocTypeGlob, matches)
	}
	return auditEntityStripComments(auditEntitySectionOf(t, matches[0], section))
}

// requireInvoicesDocTypeColumns fails on a named assertion, not on a raw 42703 from later SQL.
func requireInvoicesDocTypeColumns(t *testing.T, q querier) {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'invoices'
		    AND column_name IN ('document_type', 'original_invoice_id')`).Scan(&n); err != nil {
		t.Fatalf("read invoices columns: %v", err)
	}
	if n != 2 {
		t.Fatalf("invoices has %d of the columns document_type and original_invoice_id, want 2 (is the migration applied? run `make migrate-up`)", n)
	}
}

// invoicesDocTypeFixture seeds an entity in tenant and registers a one-statement cleanup of
// every invoice numbered with the returned prefix: a note and its original go together.
func invoicesDocTypeFixture(t *testing.T, tenant string) (entity, prefix string) {
	t.Helper()
	entity, cleanupEntity := seedBusinessEntity(t, tenant, "DT Corp")
	t.Cleanup(cleanupEntity)
	prefix = "DT-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE invoice_number LIKE $1`, prefix+"%")
	})
	return entity, prefix
}

func invoicesDocTypeOriginal(t *testing.T, tenant, entity, number string) string {
	t.Helper()
	id := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
		id, tenant, entity, number)
	return id
}

// invoicesDocTypeInsertAsApp runs one INSERT as invoice_app bound to tenant.
func invoicesDocTypeInsertAsApp(tenant, entity, number, docType string, original any) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
			 VALUES ($1, $2, $3, $4, $5)`, tenant, entity, number, docType, original)
		return err
	})
}

func wantPgError(t *testing.T, what string, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: err %v, want SQLSTATE %s from %s", what, err, code, constraint)
		return
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Errorf("%s: SQLSTATE %s constraint %q, want %s from %s", what, pgErr.Code, pgErr.ConstraintName, code, constraint)
	}
}

// AC 1.
func TestRLS_InvoicesDocumentType_DefaultsToCommercialInvoice(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	ctx := context.Background()
	entity, prefix := invoicesDocTypeFixture(t, h.tenantA)

	var docType string
	var original *string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3)
			 RETURNING document_type, original_invoice_id::text`,
			h.tenantA, entity, prefix).Scan(&docType, &original)
	})
	if err != nil {
		t.Fatalf("INSERT without the new columns: %v", err)
	}
	if docType != "commercial_invoice" {
		t.Errorf("document_type default = %q, want commercial_invoice", docType)
	}
	if original != nil {
		t.Errorf("original_invoice_id default = %q, want NULL", *original)
	}
}

// AC 1. Each bad type carries a tenant-A original, so matches_type cannot be the constraint that fires.
func TestRLS_InvoicesDocumentType_CheckRefusesAnUnknownType(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	entity, prefix := invoicesDocTypeFixture(t, h.tenantA)
	original := invoicesDocTypeOriginal(t, h.tenantA, entity, prefix+"-orig")

	// Controls first: a refusal means nothing if the valid types are also refused.
	for i, ok := range []struct {
		docType string
		orig    any
	}{{"commercial_invoice", nil}, {"credit_note", original}, {"debit_note", original}} {
		if err := invoicesDocTypeInsertAsApp(h.tenantA, entity, fmt.Sprintf("%s-ok%d", prefix, i), ok.docType, ok.orig); err != nil {
			t.Fatalf("control INSERT of %s: %v", ok.docType, err)
		}
	}
	for i, bad := range []string{"refund", "Credit_Note", ""} {
		err := invoicesDocTypeInsertAsApp(h.tenantA, entity, fmt.Sprintf("%s-bad%d", prefix, i), bad, original)
		wantPgError(t, fmt.Sprintf("INSERT document_type %q", bad), err, "23514", "invoices_document_type_check")
	}
}

// AC 2: RI checks skip RLS, so only the composite FK refuses another tenant's original.
func TestRLS_InvoicesDocumentType_OriginalInAnotherTenantRefused(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	entityA, prefix := invoicesDocTypeFixture(t, h.tenantA)
	entityB, prefixB := invoicesDocTypeFixture(t, h.tenantB)
	originalA := invoicesDocTypeOriginal(t, h.tenantA, entityA, prefix+"-origA")
	originalB := invoicesDocTypeOriginal(t, h.tenantB, entityB, prefixB+"-origB")

	err := invoicesDocTypeInsertAsApp(h.tenantA, entityA, prefix+"-cross", "credit_note", originalB)
	wantPgError(t, "tenant-A credit_note naming a tenant-B original", err, "23503", "invoices_original_invoice_fk")
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE invoice_number = $1`, prefix+"-cross"); n != 0 {
		t.Errorf("rows after the refused cross-tenant note = %d, want 0", n)
	}

	if err := invoicesDocTypeInsertAsApp(h.tenantA, entityA, prefix+"-same", "credit_note", originalA); err != nil {
		t.Fatalf("control: tenant-A credit_note naming a tenant-A original: %v", err)
	}
}

// AC 2: ON DELETE NO ACTION. The lone delete fails; the one-statement tenant delete does not.
func TestRLS_InvoicesDocumentType_DeletingAnOriginalWithANoteRefused(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	tenant := ptNewTenant(t)
	entity := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'DT delete')`, entity, tenant)
	original := invoicesDocTypeOriginal(t, tenant, entity, "DT-del-orig")
	note := uuid.NewString()
	execSetup(t, h.super,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, $3, 'DT-del-note', 'credit_note', $4)`, note, tenant, entity, original)

	_, err := h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, original)
	wantPgError(t, "DELETE of an original that has a note", err, "23503", "invoices_original_invoice_fk")
	if n := ptRows(t, "invoices", tenant); n != 2 {
		t.Fatalf("invoices after the refused delete = %d, want 2", n)
	}

	if _, err := h.super.Exec(context.Background(), `DELETE FROM invoices WHERE tenant_id = $1`, tenant); err != nil {
		t.Errorf("one-statement DELETE of the note and its original: %v", err)
	}
	if n := ptRows(t, "invoices", tenant); n != 0 {
		t.Errorf("invoices after the one-statement delete = %d, want 0", n)
	}
}

// AC 3.
func TestRLS_InvoicesDocumentType_TypeAndOriginalMustAgree(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	entity, prefix := invoicesDocTypeFixture(t, h.tenantA)
	original := invoicesDocTypeOriginal(t, h.tenantA, entity, prefix+"-orig")

	for i, ok := range []struct {
		docType string
		orig    any
	}{{"commercial_invoice", nil}, {"credit_note", original}, {"debit_note", original}} {
		if err := invoicesDocTypeInsertAsApp(h.tenantA, entity, fmt.Sprintf("%s-ok%d", prefix, i), ok.docType, ok.orig); err != nil {
			t.Fatalf("control INSERT of %s: %v", ok.docType, err)
		}
	}
	for i, bad := range []struct {
		docType string
		orig    any
	}{{"credit_note", nil}, {"debit_note", nil}, {"commercial_invoice", original}} {
		err := invoicesDocTypeInsertAsApp(h.tenantA, entity, fmt.Sprintf("%s-bad%d", prefix, i), bad.docType, bad.orig)
		wantPgError(t, fmt.Sprintf("INSERT %s with original %v", bad.docType, bad.orig), err, "23514", "invoices_original_matches_type")
	}
}

// AC 7 (Decision 40).
func TestRLS_InvoicesDocumentType_PurgeTenantRemovesANoteWithItsOriginal(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	tenant := ptNewTenant(t)
	entity := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'DT purge')`, entity, tenant)
	original := invoicesDocTypeOriginal(t, tenant, entity, "DT-purge-orig")
	execSetup(t, h.super,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-purge-note', 'credit_note', $3)`, tenant, entity, original)
	if n := ptRows(t, "invoices", tenant); n != 2 {
		t.Fatalf("invoices before the purge = %d, want 2", n)
	}

	if got := ptPurge(t, tenant); got != 1 {
		t.Errorf("purge_tenant returned %d, want 1", got)
	}
	if n := ptRows(t, "invoices", tenant); n != 0 {
		t.Errorf("invoices after purge_tenant = %d, want 0", n)
	}
}

type invoicesDocTypeSnapshot struct {
	constraints map[string]string // name -> contype + definition
	indexes     map[string]string // name -> indexdef
	columns     map[string]string // name -> type, nullability, default
}

func invoicesDocTypeSnap(t *testing.T, ctx context.Context, tx pgx.Tx) invoicesDocTypeSnapshot {
	t.Helper()
	read := func(sql string) map[string]string {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("snapshot query: %v", err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			out[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot rows: %v", err)
		}
		return out
	}
	return invoicesDocTypeSnapshot{
		constraints: read(`SELECT conname, contype::text || ' ' || pg_get_constraintdef(oid) || ' ' || confdeltype::text
		                     FROM pg_constraint WHERE conrelid = 'public.invoices'::regclass`),
		indexes: read(`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'invoices'`),
		columns: read(`SELECT column_name, data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
		                 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'invoices'`),
	}
}

func invoicesDocTypeAdded(after, before map[string]string) []string {
	var out []string
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// AC 1, 2, 4, 5: the shipped Up and Down bodies themselves.
func TestRLS_InvoicesDocumentType_AddsExactlyItsConstraintsAndIndex(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	down := invoicesDocTypeMigration(t, "Down")
	up := invoicesDocTypeMigration(t, "Up")
	tx := migratorTx(t, ctx) // rolled back on cleanup
	requireInvoicesDocTypeColumns(t, tx)

	before := invoicesDocTypeSnap(t, ctx, tx)
	wantCols := map[string]string{
		"document_type":       "text NO 'commercial_invoice'::text",
		"original_invoice_id": "uuid YES ",
	}
	for name, want := range wantCols {
		if got := before.columns[name]; got != want {
			t.Errorf("column %s = %q, want %q", name, got, want)
		}
	}
	wantFK := "f FOREIGN KEY (tenant_id, original_invoice_id) REFERENCES invoices(tenant_id, id) a"
	if got := before.constraints["invoices_original_invoice_fk"]; got != wantFK {
		t.Errorf("invoices_original_invoice_fk = %q, want %q", got, wantFK)
	}

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}
	afterDown := invoicesDocTypeSnap(t, ctx, tx)
	if got, want := invoicesDocTypeAdded(afterDown.columns, before.columns), []string{"document_type", "original_invoice_id"}; !slices.Equal(got, want) {
		t.Errorf("columns the Down removed = %v, want %v", got, want)
	}
	if got := invoicesDocTypeAdded(before.columns, afterDown.columns); len(got) != 0 {
		t.Errorf("columns the Down added = %v, want none", got)
	}
	if got := invoicesDocTypeAdded(afterDown.constraints, before.constraints); !slices.Equal(got, invoicesDocTypeConstraints) {
		t.Errorf("constraints the Down removed = %v, want %v", got, invoicesDocTypeConstraints)
	}
	if got := invoicesDocTypeAdded(before.constraints, afterDown.constraints); len(got) != 0 {
		t.Errorf("constraints the Down added = %v, want none", got)
	}
	if got := invoicesDocTypeAdded(afterDown.indexes, before.indexes); !slices.Equal(got, []string{invoicesDocTypeIndex}) {
		t.Errorf("indexes the Down removed = %v, want [%s]", got, invoicesDocTypeIndex)
	}
	if got := invoicesDocTypeAdded(before.indexes, afterDown.indexes); len(got) != 0 {
		t.Errorf("indexes the Down added = %v, want none", got)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	afterUp := invoicesDocTypeSnap(t, ctx, tx)
	for what, pair := range map[string][2]map[string]string{
		"constraints": {afterUp.constraints, before.constraints},
		"indexes":     {afterUp.indexes, before.indexes},
		"columns":     {afterUp.columns, before.columns},
	} {
		if len(pair[0]) != len(pair[1]) {
			t.Errorf("%s after Down/Up = %d, want %d", what, len(pair[0]), len(pair[1]))
		}
		for k, want := range pair[1] {
			if got := pair[0][k]; got != want {
				t.Errorf("%s %s after Down/Up = %q, want %q", what, k, got, want)
			}
		}
	}
	if !strings.Contains(afterUp.indexes[invoicesDocTypeIndex], "original_invoice_id IS NOT NULL") {
		t.Errorf("%s is not partial on original_invoice_id IS NOT NULL: %q", invoicesDocTypeIndex, afterUp.indexes[invoicesDocTypeIndex])
	}
}

func invoicesDocTypeExecAsApp(tenant, sql string, args ...any) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// AC 1, 2, 3: the invariants hold on UPDATE and on an explicit NULL type, not only on INSERT.
func TestRLS_InvoicesDocumentType_UpdateAndNullCannotBreakTheInvariants(t *testing.T) {
	h := requireHarness(t)
	requireInvoicesDocTypeColumns(t, h.super)
	entityA, prefix := invoicesDocTypeFixture(t, h.tenantA)
	entityB, prefixB := invoicesDocTypeFixture(t, h.tenantB)
	origA1 := invoicesDocTypeOriginal(t, h.tenantA, entityA, prefix+"-o1")
	origA2 := invoicesDocTypeOriginal(t, h.tenantA, entityA, prefix+"-o2")
	origB := invoicesDocTypeOriginal(t, h.tenantB, entityB, prefixB+"-oB")
	note := uuid.NewString()
	execSetup(t, h.super,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, $3, $4, 'credit_note', $5)`, note, h.tenantA, entityA, prefix+"-note", origA1)

	err := invoicesDocTypeExecAsApp(h.tenantA,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type) VALUES ($1, $2, $3, NULL)`,
		h.tenantA, entityA, prefix+"-null")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != "document_type" {
		t.Errorf("INSERT with an explicit NULL document_type: %v, want SQLSTATE 23502 on document_type", err)
	}

	for _, c := range []struct {
		what, sql, code, constraint string
		arg                         any
	}{
		{"promote a plain invoice to credit_note", `UPDATE invoices SET document_type = 'credit_note' WHERE id = $1`, "23514", "invoices_original_matches_type", origA2},
		{"clear a note's original", `UPDATE invoices SET original_invoice_id = NULL WHERE id = $1`, "23514", "invoices_original_matches_type", note},
		{"turn a note into a commercial_invoice", `UPDATE invoices SET document_type = 'commercial_invoice' WHERE id = $1`, "23514", "invoices_original_matches_type", note},
		{"retype a note to refund", `UPDATE invoices SET document_type = 'refund' WHERE id = $1`, "23514", "invoices_document_type_check", note},
		{"repoint a note at a tenant-B original", `UPDATE invoices SET original_invoice_id = ` + "'" + origB + "'" + ` WHERE id = $1`, "23503", "invoices_original_invoice_fk", note},
	} {
		wantPgError(t, c.what, invoicesDocTypeExecAsApp(h.tenantA, c.sql, c.arg), c.code, c.constraint)
	}
	var kept, docType string
	if err := h.super.QueryRow(context.Background(),
		`SELECT original_invoice_id::text, document_type FROM invoices WHERE id = $1`, note).Scan(&kept, &docType); err != nil {
		t.Fatalf("read the note: %v", err)
	}
	if kept != origA1 || docType != "credit_note" {
		t.Errorf("note after the refused updates = (%s, %s), want (credit_note, %s)", docType, kept, origA1)
	}

	if err := invoicesDocTypeExecAsApp(h.tenantA, `UPDATE invoices SET original_invoice_id = $2 WHERE id = $1`, note, origA2); err != nil {
		t.Fatalf("control: repoint a note at another tenant-A original: %v", err)
	}
	// The original is only held by a note: deleting the note alone is allowed and keeps the original.
	if _, err := h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, note); err != nil {
		t.Fatalf("control: DELETE of a note alone: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = ANY($1)`, []string{origA1, origA2}); n != 2 {
		t.Errorf("originals after deleting their note = %d, want 2", n)
	}
}
