// ENGI-02-01: the 22 invoices and 9 line_items NRS columns. Every DB test leads with
// requireNRSColumns, so a schema without them fails on a named assertion, not on SQLSTATE 42703.
package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const nrsMigrationGlob = "*_invoices_nrs_fields.sql"

// nrsCol is one new column: its table, information_schema type and the value pair the
// round-trip test writes then overwrites (text form; the cast follows the type).
type nrsCol struct {
	table, name, dataType string
	precision, scale      int // numeric only
	ins, upd              string
}

var nrsInvoiceCols = func() []nrsCol {
	var cols []nrsCol
	text := func(names ...string) {
		for _, n := range names {
			cols = append(cols, nrsCol{table: "invoices", name: n, dataType: "text", ins: n + "-v1", upd: n + "-v2"})
		}
	}
	text("invoice_kind", "tax_currency_code", "payment_status")
	cols = append(cols,
		nrsCol{table: "invoices", name: "due_date", dataType: "date", ins: "2026-10-01", upd: "2026-11-02"},
		nrsCol{table: "invoices", name: "tax_point_date", dataType: "date", ins: "2026-10-03", upd: "2026-11-04"},
		nrsCol{table: "invoices", name: "issue_time", dataType: "time without time zone", ins: "14:30:00", upd: "09:05:07"},
	)
	for _, party := range []string{"supplier", "buyer"} {
		for _, f := range []string{"email", "telephone", "street", "city", "postal_zone", "country", "state", "lga"} {
			text(party + "_" + f)
		}
	}
	return cols
}()

var nrsLineCols = func() []nrsCol {
	var cols []nrsCol
	for _, n := range []string{"tax_category", "hsn_code", "isic_code", "product_category", "service_category", "sellers_item_identification", "price_unit"} {
		cols = append(cols, nrsCol{table: "line_items", name: n, dataType: "text", ins: n + "-v1", upd: n + "-v2"})
	}
	return append(cols,
		nrsCol{table: "line_items", name: "tax_percent", dataType: "numeric", precision: 14, scale: 2, ins: "7.50", upd: "5.00"},
		nrsCol{table: "line_items", name: "base_quantity", dataType: "numeric", precision: 14, scale: 3, ins: "2.500", upd: "3.250"},
	)
}()

func nrsAllCols() []nrsCol { return append(slices.Clone(nrsInvoiceCols), nrsLineCols...) }

func nrsNames(cols []nrsCol) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.name
	}
	return out
}

// requireNRSColumns fails with the missing column names when the migration has not run.
func requireNRSColumns(t *testing.T, ctx context.Context) {
	t.Helper()
	var missing []string
	for _, tbl := range []struct {
		name string
		cols []nrsCol
	}{{"invoices", nrsInvoiceCols}, {"line_items", nrsLineCols}} {
		have := map[string]bool{}
		rows, err := h.super.Query(ctx,
			`SELECT column_name FROM information_schema.columns
			   WHERE table_schema = 'public' AND table_name = $1 AND column_name = ANY($2)`,
			tbl.name, nrsNames(tbl.cols))
		if err != nil {
			t.Fatalf("read %s columns: %v", tbl.name, err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatalf("scan column name: %v", err)
			}
			have[n] = true
		}
		rows.Close()
		for _, c := range tbl.cols {
			if !have[c.name] {
				missing = append(missing, tbl.name+"."+c.name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d of 31 NRS columns do not exist yet: %v", len(missing), missing)
	}
}

// nrsQuerier is satisfied by a pool and by a tx, so one assertion runs on the live schema
// and on the schema a Down-then-Up leaves inside a rolled-back tx.
type nrsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type colShape struct {
	dataType, nullable string
	def                *string
	precision, scale   *int
}

func nrsShapes(t *testing.T, ctx context.Context, q nrsQuerier, table string) map[string]colShape {
	t.Helper()
	rows, err := q.Query(ctx,
		`SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale
		   FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`, table)
	if err != nil {
		t.Fatalf("read %s shapes: %v", table, err)
	}
	defer rows.Close()
	out := map[string]colShape{}
	for rows.Next() {
		var n string
		var s colShape
		if err := rows.Scan(&n, &s.dataType, &s.nullable, &s.def, &s.precision, &s.scale); err != nil {
			t.Fatalf("scan shape: %v", err)
		}
		out[n] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s shapes: %v", table, err)
	}
	return out
}

func assertNRSShapes(t *testing.T, ctx context.Context, q nrsQuerier, table string, cols []nrsCol) {
	t.Helper()
	shapes := nrsShapes(t, ctx, q, table)
	if len(shapes) < 10 {
		t.Fatalf("%s reports only %d column(s); the walk is broken", table, len(shapes))
	}
	for _, c := range cols {
		s, ok := shapes[c.name]
		if !ok {
			t.Errorf("%s.%s is missing", table, c.name)
			continue
		}
		if s.dataType != c.dataType {
			t.Errorf("%s.%s data_type = %q, want %q", table, c.name, s.dataType, c.dataType)
		}
		if s.nullable != "YES" {
			t.Errorf("%s.%s is_nullable = %q, want YES", table, c.name, s.nullable)
		}
		if s.def != nil {
			t.Errorf("%s.%s has default %q, want none", table, c.name, *s.def)
		}
		if c.precision != 0 {
			if s.precision == nil || *s.precision != c.precision || s.scale == nil || *s.scale != c.scale {
				t.Errorf("%s.%s numeric(%v,%v), want numeric(%d,%d)", table, c.name, deref(s.precision), deref(s.scale), c.precision, c.scale)
			}
		}
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestRLS_InvoicesNRSFields_InvoiceColumnsHaveTheirTypes(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)
	if len(nrsInvoiceCols) != 22 {
		t.Fatalf("spec lists %d invoice columns, want 22", len(nrsInvoiceCols))
	}
	assertNRSShapes(t, ctx, h.super, "invoices", nrsInvoiceCols)
}

func TestRLS_InvoicesNRSFields_LineColumnsHaveTheirTypes(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)
	if len(nrsLineCols) != 9 {
		t.Fatalf("spec lists %d line columns, want 9", len(nrsLineCols))
	}
	assertNRSShapes(t, ctx, h.super, "line_items", nrsLineCols)
}

func TestRLS_InvoicesNRSFields_AddNoCheckAndNoColumnGrant(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)
	assertNoNRSCheckOrColumnACL(t, ctx, h.super)
}

func assertNoNRSCheckOrColumnACL(t *testing.T, ctx context.Context, q nrsQuerier) {
	t.Helper()
	for _, tbl := range []struct {
		name string
		cols []nrsCol
	}{{"invoices", nrsInvoiceCols}, {"line_items", nrsLineCols}} {
		// Control: the CHECK query sees invoices.status's pre-existing CHECK, so an empty
		// result for the new columns is not an artefact of the query.
		if tbl.name == "invoices" {
			var n int
			if err := q.QueryRow(ctx,
				`SELECT count(*) FROM pg_constraint c JOIN pg_attribute a
				   ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
				  WHERE c.conrelid = 'public.invoices'::regclass AND c.contype = 'c' AND a.attname = 'status'`).Scan(&n); err != nil || n == 0 {
				t.Fatalf("control: invoices.status CHECK not seen by the query (n=%d, err=%v)", n, err)
			}
		}
		rows, err := q.Query(ctx,
			`SELECT a.attname, c.conname FROM pg_constraint c JOIN pg_attribute a
			   ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
			  WHERE c.conrelid = $1::regclass AND c.contype = 'c' AND a.attname = ANY($2)`,
			"public."+tbl.name, nrsNames(tbl.cols))
		if err != nil {
			t.Fatalf("read %s CHECKs: %v", tbl.name, err)
		}
		for rows.Next() {
			var col, con string
			if err := rows.Scan(&col, &con); err != nil {
				t.Fatalf("scan CHECK: %v", err)
			}
			t.Errorf("%s.%s is under CHECK %s, want none", tbl.name, col, con)
		}
		rows.Close()

		acl, err := q.Query(ctx,
			`SELECT attname, attacl IS NULL FROM pg_attribute
			  WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped AND attname = ANY($2)`,
			"public."+tbl.name, nrsNames(tbl.cols))
		if err != nil {
			t.Fatalf("read %s attacl: %v", tbl.name, err)
		}
		seen := 0
		for acl.Next() {
			var col string
			var isNull bool
			if err := acl.Scan(&col, &isNull); err != nil {
				t.Fatalf("scan attacl: %v", err)
			}
			seen++
			if !isNull {
				t.Errorf("%s.%s carries a column-level ACL", tbl.name, col)
			}
		}
		acl.Close()
		if seen != len(tbl.cols) {
			t.Errorf("attacl walk saw %d %s columns, want %d", seen, tbl.name, len(tbl.cols))
		}
	}
}

func nrsPlaceholder(c nrsCol, n int) string {
	switch c.dataType {
	case "date":
		return fmt.Sprintf("$%d::text::date", n)
	case "time without time zone":
		return fmt.Sprintf("$%d::text::time", n)
	case "numeric":
		return fmt.Sprintf("$%d::text::numeric", n)
	}
	return fmt.Sprintf("$%d::text", n)
}

func nrsInsert(t *testing.T, ctx context.Context, tx pgx.Tx, table string, baseCols []string, baseArgs []any, cols []nrsCol, val func(nrsCol) string) {
	t.Helper()
	names, args, ph := slices.Clone(baseCols), slices.Clone(baseArgs), []string{}
	for i := range baseCols {
		ph = append(ph, fmt.Sprintf("$%d", i+1))
	}
	for _, c := range cols {
		names = append(names, c.name)
		args = append(args, val(c))
		ph = append(ph, nrsPlaceholder(c, len(args)))
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, table,
		strings.Join(names, ", "), strings.Join(ph, ", ")), args...); err != nil {
		t.Fatalf("app INSERT into %s with every new column: %v", table, err)
	}
}

func nrsUpdateEach(t *testing.T, ctx context.Context, tx pgx.Tx, table, id string, cols []nrsCol, val func(nrsCol) string) {
	t.Helper()
	for _, c := range cols {
		tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = %s WHERE id = $1`, table, c.name, nrsPlaceholder(c, 2)), id, val(c))
		if err != nil {
			t.Errorf("app UPDATE %s.%s: %v", table, c.name, err)
			continue
		}
		if tag.RowsAffected() != 1 {
			t.Errorf("app UPDATE %s.%s changed %d rows, want 1", table, c.name, tag.RowsAffected())
		}
	}
}

func nrsReadBack(t *testing.T, ctx context.Context, tx pgx.Tx, table, id string, cols []nrsCol, val func(nrsCol) string, phase string) {
	t.Helper()
	sel := make([]string, len(cols))
	got := make([]*string, len(cols))
	dest := make([]any, len(cols))
	for i, c := range cols {
		sel[i] = c.name + "::text"
		dest[i] = &got[i]
	}
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, strings.Join(sel, ", "), table), id).Scan(dest...); err != nil {
		t.Fatalf("app SELECT %s after %s: %v", table, phase, err)
	}
	for i, c := range cols {
		if got[i] == nil || *got[i] != val(c) {
			t.Errorf("%s.%s after %s = %v, want %q", table, c.name, phase, derefStr(got[i]), val(c))
		}
	}
}

func derefStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestRLS_InvoicesNRSFields_AppRoleWritesAndReadsEveryNewColumn(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, "ENGI-02-01 Corp")
	defer cleanupEntity()
	invID, lineID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invID) })

	ins := func(c nrsCol) string { return c.ins }
	upd := func(c nrsCol) string { return c.upd }

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		nrsInsert(t, ctx, tx, "invoices", []string{"id", "tenant_id", "entity_id", "invoice_number"},
			[]any{invID, h.tenantA, entity, "ENGI-02-01"}, nrsInvoiceCols, ins)
		nrsInsert(t, ctx, tx, "line_items", []string{"id", "tenant_id", "invoice_id", "line_no"},
			[]any{lineID, h.tenantA, invID, 1}, nrsLineCols, ins)
		nrsReadBack(t, ctx, tx, "invoices", invID, nrsInvoiceCols, ins, "INSERT")
		nrsReadBack(t, ctx, tx, "line_items", lineID, nrsLineCols, ins, "INSERT")

		nrsUpdateEach(t, ctx, tx, "invoices", invID, nrsInvoiceCols, upd)
		nrsUpdateEach(t, ctx, tx, "line_items", lineID, nrsLineCols, upd)
		nrsReadBack(t, ctx, tx, "invoices", invID, nrsInvoiceCols, upd, "UPDATE")
		nrsReadBack(t, ctx, tx, "line_items", lineID, nrsLineCols, upd, "UPDATE")
		return nil
	})
	if err != nil {
		t.Fatalf("tenant tx: %v", err)
	}
}

func TestRLS_InvoicesNRSFields_OtherTenantCannotUpdateANewColumn(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, "ENGI-02-01 Cross Corp")
	defer cleanupEntity()
	invID, cleanupInv := seedInvoice(t, h.tenantA, entity, "ENGI-02-01-X")
	defer cleanupInv()

	const upd = `UPDATE invoices SET buyer_state = 'NG-LA' WHERE id = $1`
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, upd, invID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("tenant B's UPDATE of buyer_state changed %d rows of tenant A's invoice, want 0", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant B tx: %v", err)
	}
	var got *string
	if err := h.super.QueryRow(ctx, `SELECT buyer_state FROM invoices WHERE id = $1`, invID).Scan(&got); err != nil {
		t.Fatalf("superuser read: %v", err)
	}
	if got != nil {
		t.Errorf("buyer_state = %q after tenant B's UPDATE, want NULL", *got)
	}

	// Control: the same statement in the owning tenant lands, so the 0 above is RLS, not a no-op.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, upd, invID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Errorf("tenant A's own UPDATE of buyer_state changed %d rows, want 1", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant A tx: %v", err)
	}
}

func TestRLS_InvoicesNRSFields_LegacyRowReadsNull(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, "ENGI-02-01 Legacy Corp")
	defer cleanupEntity()
	invID, cleanupInv := seedInvoice(t, h.tenantA, entity, "ENGI-02-01-L")
	defer cleanupInv()
	lineID, cleanupLine := seedLineItem(t, h.tenantA, invID, 1)
	defer cleanupLine()
	if _, err := h.super.Exec(ctx, `UPDATE line_items SET description = 'legacy' WHERE id = $1`, lineID); err != nil {
		t.Fatalf("name description on the legacy line: %v", err)
	}

	for _, tc := range []struct {
		table, id, anchor string
		cols              []nrsCol
	}{
		{"invoices", invID, "invoice_number", nrsInvoiceCols},
		{"line_items", lineID, "description", nrsLineCols},
	} {
		var raw []byte
		if err := h.super.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(r) FROM %s r WHERE id = $1`, tc.table), tc.id).Scan(&raw); err != nil {
			t.Fatalf("read %s row: %v", tc.table, err)
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("decode %s row: %v", tc.table, err)
		}
		if row[tc.anchor] == nil {
			t.Fatalf("%s.%s is NULL on the legacy row; the NULL checks below would be vacuous", tc.table, tc.anchor)
		}
		for _, c := range tc.cols {
			v, present := row[c.name]
			if !present {
				t.Errorf("%s.%s is absent from the row", tc.table, c.name)
			} else if v != nil {
				t.Errorf("%s.%s = %v on a row that named only pre-existing columns, want NULL", tc.table, c.name, v)
			}
		}
	}
}

func nrsColumnList(t *testing.T, ctx context.Context, tx pgx.Tx, table string) []string {
	t.Helper()
	rows, err := tx.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out = append(out, c)
	}
	if len(out) < 10 {
		t.Fatalf("%s reports only %d column(s); the walk is broken", table, len(out))
	}
	return out
}

func TestRLS_InvoicesNRSFields_DownDropsExactlyTheAddedColumns(t *testing.T) {
	requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	matches, err := fs.Glob(migrations.FS, nrsMigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1: %v", len(matches), nrsMigrationGlob, matches, err)
	}
	up, down := auditEntitySectionOf(t, matches[0], "Up"), auditEntitySectionOf(t, matches[0], "Down")

	tx := migratorTx(t, ctx) // rolled back on cleanup
	if _, err := tx.Exec(ctx, `LOCK TABLE invoices, line_items IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock invoices, line_items: %v", err)
	}
	beforeInv, beforeLine := nrsColumnList(t, ctx, tx, "invoices"), nrsColumnList(t, ctx, tx, "line_items")

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}
	for _, tc := range []struct {
		table  string
		before []string
		added  []nrsCol
	}{{"invoices", beforeInv, nrsInvoiceCols}, {"line_items", beforeLine, nrsLineCols}} {
		after := nrsColumnList(t, ctx, tx, tc.table)
		for _, c := range tc.added {
			if slices.Contains(after, c.name) {
				t.Errorf("%s.%s survived the Down", tc.table, c.name)
			}
		}
		want := slices.DeleteFunc(slices.Clone(tc.before), func(c string) bool { return slices.Contains(nrsNames(tc.added), c) })
		if len(want) != len(tc.before)-len(tc.added) {
			t.Fatalf("%s: %d columns before, %d new, %d left: the new columns were not all present", tc.table, len(tc.before), len(tc.added), len(want))
		}
		if !slices.Equal(after, want) {
			t.Errorf("%s columns after Down = %v, want %v: the Down dropped a column it does not own", tc.table, after, want)
		}
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	for _, tc := range []struct {
		table  string
		before []string
	}{{"invoices", beforeInv}, {"line_items", beforeLine}} {
		got := slices.Sorted(slices.Values(nrsColumnList(t, ctx, tx, tc.table)))
		if want := slices.Sorted(slices.Values(tc.before)); !slices.Equal(got, want) {
			t.Errorf("%s columns after Down then Up = %v, want %v", tc.table, got, want)
		}
	}

	// The Up section, not the already-migrated DB, is what these see: types, nullable,
	// no default, no CHECK, no column ACL.
	assertNRSShapes(t, ctx, tx, "invoices", nrsInvoiceCols)
	assertNRSShapes(t, ctx, tx, "line_items", nrsLineCols)
	assertNoNRSCheckOrColumnACL(t, ctx, tx)
}

func TestRLS_InvoicesNRSFields_OtherTenantCannotReadOrWriteNewColumns(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, "ENGI-02-01 Read Corp")
	defer cleanupEntity()
	invID, cleanupInv := seedInvoice(t, h.tenantA, entity, "ENGI-02-01-R")
	defer cleanupInv()
	lineID, cleanupLine := seedLineItem(t, h.tenantA, invID, 1)
	defer cleanupLine()
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET buyer_email = 'a@example.test', due_date = '2026-10-01' WHERE id = $1`, invID); err != nil {
		t.Fatalf("seed invoice NRS values: %v", err)
	}
	if _, err := h.super.Exec(ctx, `UPDATE line_items SET tax_percent = 7.5, hsn_code = '8471' WHERE id = $1`, lineID); err != nil {
		t.Fatalf("seed line NRS values: %v", err)
	}

	const (
		readInv  = `SELECT count(*) FROM invoices WHERE id = $1 AND buyer_email IS NOT NULL AND due_date IS NOT NULL`
		readLine = `SELECT count(*) FROM line_items WHERE id = $1 AND tax_percent IS NOT NULL AND hsn_code IS NOT NULL`
	)
	count := func(tx pgx.Tx, q, id string) int {
		var n int
		if err := tx.QueryRow(ctx, q, id).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// Control: the owning tenant sees both rows, so the zeros below are RLS.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := count(tx, readInv, invID); n != 1 {
			t.Errorf("tenant A reads %d invoice(s) by new column, want 1", n)
		}
		if n := count(tx, readLine, lineID); n != 1 {
			t.Errorf("tenant A reads %d line(s) by new column, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant A tx: %v", err)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		if n := count(tx, readInv, invID); n != 0 {
			t.Errorf("tenant B reads %d of tenant A's invoice by a new column, want 0", n)
		}
		if n := count(tx, readLine, lineID); n != 0 {
			t.Errorf("tenant B reads %d of tenant A's line by a new column, want 0", n)
		}
		tag, err := tx.Exec(ctx, `UPDATE line_items SET tax_percent = 99.99 WHERE id = $1`, lineID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("tenant B's UPDATE of line_items.tax_percent changed %d rows, want 0", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant B tx: %v", err)
	}
	var pct string
	if err := h.super.QueryRow(ctx, `SELECT tax_percent::text FROM line_items WHERE id = $1`, lineID).Scan(&pct); err != nil || pct != "7.50" {
		t.Errorf("tax_percent = %q (err %v) after tenant B's UPDATE, want 7.50", pct, err)
	}
}

// The numeric columns round to their declared scale and refuse a value past their precision.
func TestRLS_InvoicesNRSFields_NumericColumnsRoundAtScaleAndRefuseOverflow(t *testing.T) {
	h := requireHarness(t)
	ctx := t.Context()
	requireNRSColumns(t, ctx)

	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, "ENGI-02-01 Numeric Corp")
	defer cleanupEntity()
	invID, cleanupInv := seedInvoice(t, h.tenantA, entity, "ENGI-02-01-N")
	defer cleanupInv()

	errRollback := errors.New("rollback")
	cases := []struct {
		col, in, want, wantCode string
	}{
		{"tax_percent", "7.555", "7.56", ""},
		{"tax_percent", "999999999999.99", "999999999999.99", ""},
		{"tax_percent", "1000000000000.00", "", "22003"},
		{"base_quantity", "1.2345", "1.235", ""},
		{"base_quantity", "99999999999.999", "99999999999.999", ""},
		{"base_quantity", "100000000000.000", "", "22003"},
	}
	for _, tc := range cases {
		var got string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			id := uuid.NewString()
			if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO line_items (id, tenant_id, invoice_id, line_no, %s) VALUES ($1, $2, $3, 1, $4::text::numeric)`, tc.col),
				id, h.tenantA, invID, tc.in); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s::text FROM line_items WHERE id = $1`, tc.col), id).Scan(&got); err != nil {
				return err
			}
			return errRollback
		})
		if tc.wantCode != "" {
			if code := pgCode(err); code != tc.wantCode {
				t.Errorf("%s = %s: SQLSTATE %q (err %v), want %s", tc.col, tc.in, code, err, tc.wantCode)
			}
			continue
		}
		if !errors.Is(err, errRollback) {
			t.Errorf("%s = %s: %v", tc.col, tc.in, err)
		} else if got != tc.want {
			t.Errorf("%s = %s reads back %q, want %q", tc.col, tc.in, got, tc.want)
		}
	}
}
