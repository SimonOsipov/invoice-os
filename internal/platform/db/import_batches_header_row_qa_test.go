package db_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Tenant B can neither read nor overwrite tenant A's header_row.
func TestRLS_ImportBatchesHeaderRowIsTenantScoped(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-HR-QA A Corp")
	defer cleanupEntityA()
	batchID, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()
	if _, err := h.super.Exec(ctx, `UPDATE import_batches SET header_row = 7 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("set header_row: %v", err)
	}

	var ownRead int
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, batchID).Scan(&ownRead)
	}); err != nil || ownRead != 7 {
		t.Fatalf("tenant A read = %d, %v; want 7 (positive control)", ownRead, err)
	}

	var crossRead int
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, batchID).Scan(&crossRead)
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("tenant B read = %d, %v; want pgx.ErrNoRows", crossRead, err)
	}

	var affected int64
	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE import_batches SET header_row = 9 WHERE id = $1`, batchID)
		affected = tag.RowsAffected()
		return e
	}); err != nil {
		t.Fatalf("tenant B UPDATE: %v", err)
	}
	if affected != 0 {
		t.Errorf("tenant B UPDATE affected %d row(s), want 0", affected)
	}
	var after int
	if err := h.super.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, batchID).Scan(&after); err != nil {
		t.Fatalf("read back header_row: %v", err)
	}
	if after != 7 {
		t.Errorf("header_row after tenant B UPDATE = %d, want 7", after)
	}
}

func importBatchesShape(t *testing.T, ctx context.Context, tx pgx.Tx) map[string][]string {
	t.Helper()
	queries := map[string]string{
		"columns": `SELECT column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
		              FROM information_schema.columns
		             WHERE table_schema = 'public' AND table_name = 'import_batches'`,
		"constraints": `SELECT conname || ' ' || pg_get_constraintdef(oid)
		                  FROM pg_constraint WHERE conrelid = 'public.import_batches'::regclass`,
		"indexes": `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'import_batches'`,
		"policies": `SELECT policyname || ' ' || cmd || ' ' || coalesce(qual, '') || ' ' || coalesce(with_check, '')
		               FROM pg_policies WHERE schemaname = 'public' AND tablename = 'import_batches'`,
		"grants": `SELECT grantee || ' ' || privilege_type FROM information_schema.role_table_grants
		            WHERE table_schema = 'public' AND table_name = 'import_batches'`,
	}
	out := map[string][]string{}
	for kind, q := range queries {
		rows, err := tx.Query(ctx, q)
		if err != nil {
			t.Fatalf("read import_batches %s: %v", kind, err)
		}
		vals, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("collect import_batches %s: %v", kind, err)
		}
		slices.Sort(vals)
		out[kind] = vals
	}
	for _, kind := range []string{"columns", "constraints", "indexes", "policies", "grants"} {
		if len(out[kind]) == 0 {
			t.Fatalf("import_batches reports no %s; the comparison would be vacuous", kind)
		}
	}
	return out
}

// The Down removes the column and its CHECK and nothing else: every other
// column, constraint, index, policy and grant is unchanged.
func TestImportBatchesHeaderRow_DownLeavesEverythingElseAsBefore(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx)

	before := importBatchesShape(t, ctx, tx)
	want := map[string][]string{}
	for kind, vals := range before {
		want[kind] = slices.DeleteFunc(slices.Clone(vals), func(v string) bool {
			return strings.HasPrefix(v, headerRowColumn+" ") || strings.HasPrefix(v, headerRowConstraint+" ")
		})
	}
	if len(want["columns"]) != len(before["columns"])-1 || len(want["constraints"]) != len(before["constraints"])-1 {
		t.Fatalf("header_row column or CHECK not found before the Down: %v", before)
	}

	if _, err := tx.Exec(ctx, headerRowSection(t, "Down")); err != nil {
		t.Fatalf("Down body: %v", err)
	}
	after := importBatchesShape(t, ctx, tx)
	for kind := range want {
		if !slices.Equal(after[kind], want[kind]) {
			t.Errorf("%s after Down = %v\nwant %v", kind, after[kind], want[kind])
		}
	}
}

// A batch written before the column existed reads NULL once the Up runs,
// and the replayed Up's CHECK refuses 0.
func TestImportBatchesHeaderRow_PreMigrationRowsReadNull(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-HR-QA pre-migration")
	// Registered before migratorTx so the rollback releases its table locks first.
	t.Cleanup(cleanupEntityA)

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, headerRowSection(t, "Down")); err != nil {
		t.Fatalf("Down body: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, h.tenantA); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2) RETURNING id`, h.tenantA, entityA,
	).Scan(&id); err != nil {
		t.Fatalf("insert pre-migration batch: %v", err)
	}
	if _, err := tx.Exec(ctx, headerRowSection(t, "Up")); err != nil {
		t.Fatalf("Up body over an existing row: %v", err)
	}

	var headerRow *int
	if err := tx.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, id).Scan(&headerRow); err != nil {
		t.Fatalf("read header_row: %v", err)
	}
	if headerRow != nil {
		t.Errorf("pre-migration header_row = %d, want NULL", *headerRow)
	}

	_, err := tx.Exec(ctx, `UPDATE import_batches SET header_row = 0 WHERE id = $1`, id)
	if pgCode(err) != "23514" || pgConstraint(err) != headerRowConstraint {
		t.Errorf("header_row = 0 after the replayed Up: %v; want 23514 on %s", err, headerRowConstraint)
	}
}
