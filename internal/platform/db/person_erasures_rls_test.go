// person_erasures is global: no tenant, no RLS. The guarantees here are grants and CHECKs.
// Rows are seeded through the superuser pool; each refused statement runs on its own implicit tx.
package db_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	erasuresTable         = "person_erasures"
	erasuresMigrationGlob = "*_person_erasures.sql"
)

// erasuresRequireTable fails when the migration is not applied, so no case passes on a missing table.
func erasuresRequireTable(t *testing.T) *harness {
	t.Helper()
	hh := requireHarness(t)
	var reg *string
	if err := hh.super.QueryRow(context.Background(), `SELECT to_regclass('public.person_erasures')::text`).Scan(&reg); err != nil {
		t.Fatalf("read to_regclass %s: %v", erasuresTable, err)
	}
	if reg == nil {
		t.Fatalf("table public.%s not found: the person_erasures migration is not applied", erasuresTable)
	}
	return hh
}

// erasuresID is a fresh user id; its row is removed on cleanup.
func erasuresID(t *testing.T) uuid.UUID {
	t.Helper()
	hh := requireHarness(t)
	id := uuid.New()
	t.Cleanup(func() {
		_, _ = hh.super.Exec(context.Background(), `DELETE FROM public.person_erasures WHERE user_id = $1`, id)
	})
	return id
}

// erasuresInsert inserts a row naming user_id plus cols/vals; the caller picks the connection.
func erasuresInsert(c contactsConn, id uuid.UUID, cols []string, vals []any) error {
	all := append([]string{"user_id"}, cols...)
	args := append([]any{id}, vals...)
	ph := make([]string, len(all))
	for i := range all {
		ph[i] = "$" + string(rune('1'+i))
	}
	_, err := c.Exec(context.Background(),
		"INSERT INTO public.person_erasures ("+strings.Join(all, ", ")+") VALUES ("+strings.Join(ph, ", ")+")", args...)
	return err
}

func erasuresCount(t *testing.T, id uuid.UUID) int {
	t.Helper()
	return mustCount(t, requireHarness(t).super, `SELECT count(*) FROM public.person_erasures WHERE user_id = $1`, id)
}

// erasuresWantOK inserts a row that must be accepted and checks that it landed.
func erasuresWantOK(t *testing.T, c contactsConn, label string, cols []string, vals []any) {
	t.Helper()
	id := erasuresID(t)
	if err := erasuresInsert(c, id, cols, vals); err != nil {
		t.Errorf("%s: INSERT refused: %v, want accepted", label, err)
		return
	}
	if n := erasuresCount(t, id); n != 1 {
		t.Errorf("%s: row count after accepted INSERT = %d, want 1", label, n)
	}
}

// erasuresWantCheck inserts a row that must fail a CHECK (23514) and leave no row.
func erasuresWantCheck(t *testing.T, c contactsConn, label string, cols []string, vals []any) {
	t.Helper()
	id := erasuresID(t)
	if code := pgCode(erasuresInsert(c, id, cols, vals)); code != pgCheckViolation {
		t.Errorf("%s: INSERT got SQLSTATE %q, want %s (CHECK violation)", label, code, pgCheckViolation)
	}
	if n := erasuresCount(t, id); n != 0 {
		t.Errorf("%s: row count after refused INSERT = %d, want 0", label, n)
	}
}

// --- AC-1: grants --------------------------------------------------------------------

func TestRLS_PersonErasuresAppMayInsertSelectUpdate(t *testing.T) {
	hh := erasuresRequireTable(t)
	ctx := context.Background()
	id := erasuresID(t)

	if err := erasuresInsert(hh.app, id, []string{"email"}, []any{"ada@x.test"}); err != nil {
		t.Fatalf("app INSERT: %v", err)
	}
	var email string
	if err := hh.app.QueryRow(ctx, `SELECT email FROM public.person_erasures WHERE user_id = $1`, id).Scan(&email); err != nil {
		t.Fatalf("app SELECT: %v", err)
	}
	if email != "ada@x.test" {
		t.Fatalf("app read email %q, want ada@x.test", email)
	}
	tag, err := hh.app.Exec(ctx, `UPDATE public.person_erasures SET local_erased_at = now() WHERE user_id = $1`, id)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("app UPDATE local_erased_at: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}
	var stamped bool
	if err := hh.super.QueryRow(ctx,
		`SELECT local_erased_at IS NOT NULL FROM public.person_erasures WHERE user_id = $1`, id).Scan(&stamped); err != nil || !stamped {
		t.Fatalf("local_erased_at after UPDATE: set=%v err=%v, want set", stamped, err)
	}
}

func TestRLS_PersonErasuresAppMayNotDelete(t *testing.T) {
	hh := erasuresRequireTable(t)
	ctx := context.Background()
	id := erasuresID(t)
	if err := erasuresInsert(hh.super, id, []string{"email"}, []any{"ada@x.test"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if n := erasuresCount(t, id); n != 1 {
		t.Fatalf("seeded row count = %d, want 1", n)
	}

	_, err := hh.app.Exec(ctx, `DELETE FROM public.person_erasures WHERE user_id = $1`, id)
	if pgCode(err) != pgInsufficientPriv {
		t.Fatalf("app DELETE: got %v, want SQLSTATE %s (no DELETE grant)", err, pgInsufficientPriv)
	}
	_, err = hh.app.Exec(ctx, `TRUNCATE public.person_erasures`)
	if pgCode(err) != pgInsufficientPriv {
		t.Fatalf("app TRUNCATE: got %v, want SQLSTATE %s (no TRUNCATE grant)", err, pgInsufficientPriv)
	}
	if n := erasuresCount(t, id); n != 1 {
		t.Fatalf("row count after refused DELETE and TRUNCATE = %d, want 1", n)
	}
}

func TestRLS_PersonErasuresReaderMayNotSelect(t *testing.T) {
	hh := erasuresRequireTable(t)
	var n int
	err := hh.reader.QueryRow(context.Background(), `SELECT count(*) FROM public.person_erasures`).Scan(&n)
	if pgCode(err) != pgInsufficientPriv {
		t.Fatalf("reader SELECT: got %v, want SQLSTATE %s", err, pgInsufficientPriv)
	}
}

// --- AC-2: CHECKs --------------------------------------------------------------------

func TestRLS_PersonErasuresEmailIsTrimmedLowerCase(t *testing.T) {
	hh := erasuresRequireTable(t)
	email := []string{"email"}
	for _, bad := range []string{" Ada@X.test", "ada@x.test ", "Ada@x.test", "ab", strings.Repeat("a", 255)} {
		erasuresWantCheck(t, hh.app, "email "+bad[:min(len(bad), 12)], email, []any{bad})
	}
	for _, ok := range []string{"ada@x.test", "a@b", strings.Repeat("a", 254)} {
		erasuresWantOK(t, hh.app, "email "+ok[:min(len(ok), 12)], email, []any{ok})
	}
}

func TestRLS_PersonErasuresFinishedRowHoldsNoEmail(t *testing.T) {
	hh := erasuresRequireTable(t)
	now := time.Now()
	fin := []string{"email", "finished_at"}
	erasuresWantCheck(t, hh.app, "finished with email", fin, []any{"ada@x.test", now})
	erasuresWantCheck(t, hh.app, "unfinished with NULL email", fin, []any{nil, nil})
	erasuresWantOK(t, hh.app, "finished with NULL email", fin, []any{nil, now})
	erasuresWantOK(t, hh.app, "unfinished with email", fin, []any{"ada@x.test", nil})
}

func TestRLS_PersonErasuresVendorModeIsRealOrFake(t *testing.T) {
	hh := erasuresRequireTable(t)
	cols := []string{"email", "vendor_mode"}
	erasuresWantCheck(t, hh.app, "vendor_mode off", cols, []any{"ada@x.test", "off"})
	erasuresWantCheck(t, hh.app, "vendor_mode REAL", cols, []any{"ada@x.test", "REAL"})
	for _, mode := range []any{"real", "fake", nil} {
		erasuresWantOK(t, hh.app, "vendor_mode", cols, []any{"ada@x.test", mode})
	}
}

func TestRLS_PersonErasuresGenerationDefaultsToOne(t *testing.T) {
	hh := erasuresRequireTable(t)
	id := erasuresID(t)
	if err := erasuresInsert(hh.app, id, []string{"email"}, []any{"ada@x.test"}); err != nil {
		t.Fatalf("INSERT without generation: %v", err)
	}
	var gen int64
	if err := hh.super.QueryRow(context.Background(),
		`SELECT generation FROM public.person_erasures WHERE user_id = $1`, id).Scan(&gen); err != nil || gen != 1 {
		t.Fatalf("default generation = %d (err=%v), want 1", gen, err)
	}
	cols := []string{"email", "generation"}
	erasuresWantCheck(t, hh.app, "generation 0", cols, []any{"ada@x.test", 0})
	erasuresWantCheck(t, hh.app, "generation -1", cols, []any{"ada@x.test", -1})
	erasuresWantOK(t, hh.app, "generation 2", cols, []any{"ada@x.test", 2})
}

func TestRLS_PersonErasuresSupersededRowIsFinished(t *testing.T) {
	hh := erasuresRequireTable(t)
	now := time.Now()
	cols := []string{"email", "superseded_at", "finished_at"}
	erasuresWantCheck(t, hh.app, "superseded and unfinished", cols, []any{"ada@x.test", now, nil})
	erasuresWantOK(t, hh.app, "superseded and finished", cols, []any{nil, now, now})
	erasuresWantOK(t, hh.app, "not superseded, unfinished", cols, []any{"ada@x.test", nil, nil})
}

// --- AC-6: Down ----------------------------------------------------------------------

func erasuresDownSection(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, erasuresMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", erasuresMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), erasuresMigrationGlob, matches)
	}
	b, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	raw := string(b)
	if _, ok := auditEntityUpOf(raw); !ok {
		t.Fatalf("%s: want both %q and %q, in that order", matches[0], gooseUp, gooseDown)
	}
	return raw[strings.Index(raw, gooseDown)+len(gooseDown):]
}

func erasuresAppMayDelete(t *testing.T, ctx context.Context, tx pgx.Tx, table string) bool {
	t.Helper()
	var got bool
	if err := tx.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', $1, 'DELETE')`, "public."+table).Scan(&got); err != nil {
		t.Fatalf("read DELETE privilege on %s: %v", table, err)
	}
	return got
}

func TestRLS_PersonErasuresDownRevokesAndDrops(t *testing.T) {
	erasuresRequireTable(t)
	ctx := context.Background()
	down := erasuresDownSection(t)
	tx := migratorTx(t, ctx)
	// Roll back before the row cleanups run: their DELETE would block on the tx's table locks.
	defer func() { _ = tx.Rollback(ctx) }()

	for _, tbl := range []string{"contacts", "blog_subscribers"} {
		if !erasuresAppMayDelete(t, ctx, tx, tbl) {
			t.Fatalf("invoice_app lacks DELETE on %s before the Down: this case cannot tell 'Down revoked it' from 'never granted'", tbl)
		}
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}

	var reg *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.person_erasures')::text`).Scan(&reg); err != nil {
		t.Fatalf("read to_regclass after Down: %v", err)
	}
	if reg != nil {
		t.Errorf("after Down public.person_erasures still exists (%s)", *reg)
	}
	for _, tbl := range []string{"contacts", "blog_subscribers"} {
		if erasuresAppMayDelete(t, ctx, tx, tbl) {
			t.Errorf("after Down invoice_app still holds DELETE on %s", tbl)
		}
	}
}
