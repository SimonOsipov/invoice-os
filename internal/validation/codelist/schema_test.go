package codelist

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbTestPools skips unless both DSNs are set (same gate as internal/validation/schema_test.go).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("codelist db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL")
	}
	ctx := context.Background()
	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser: %v", err)
	}
	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)
	return s, a
}

// newListName returns a unique list name and registers superuser cleanup of both tables.
func newListName(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	list := "t-" + uuid.NewString()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"nrs_codes", "nrs_code_list_syncs"} {
			// 42P01: the table is absent, so there is nothing to clean up.
			if _, err := super.Exec(ctx, `DELETE FROM `+table+` WHERE list = $1`, list); err != nil && sqlState(err) != "42P01" {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	return list
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func wantState(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: succeeded, want SQLSTATE %s", what, want)
	}
	if got := sqlState(err); got != want {
		t.Fatalf("%s: SQLSTATE %q (%v), want %s", what, got, err, want)
	}
}

func TestSchema_CodeListTablesAreGlobal(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	want := map[string]map[string]string{
		"nrs_codes": {"list": "text", "code": "text", "entries": "jsonb"},
		"nrs_code_list_syncs": {
			"id": "uuid", "list": "text", "synced_at": "timestamp with time zone",
			"entry_count": "integer", "added": "ARRAY", "removed": "ARRAY", "changed": "ARRAY",
		},
	}
	for table, cols := range want {
		var rls bool
		if err := super.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass($1)`, "public."+table,
		).Scan(&rls); err != nil {
			t.Fatalf("%s: not found in pg_class: %v", table, err)
		}
		if rls {
			t.Errorf("%s: row level security is on, want off", table)
		}

		rows, err := super.Query(ctx,
			`SELECT column_name, data_type, is_nullable FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = $1`, table)
		if err != nil {
			t.Fatalf("%s: read columns: %v", table, err)
		}
		got := map[string]string{}
		for rows.Next() {
			var name, typ, nullable string
			if err := rows.Scan(&name, &typ, &nullable); err != nil {
				t.Fatalf("%s: scan: %v", table, err)
			}
			got[name] = typ
			if nullable != "NO" {
				t.Errorf("%s.%s: nullable, want NOT NULL", table, name)
			}
		}
		rows.Close()
		if len(got) == 0 {
			t.Fatalf("%s: no columns found", table)
		}
		if _, ok := got["tenant_id"]; ok {
			t.Errorf("%s: has tenant_id, want a global table", table)
		}
		for name, typ := range cols {
			if got[name] != typ {
				t.Errorf("%s.%s: type %q, want %q", table, name, got[name], typ)
			}
		}
		if len(got) != len(cols) {
			t.Errorf("%s: columns %v, want exactly %v", table, got, cols)
		}
	}
}

func TestSchema_AppWritesCodes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	list := newListName(t, super)

	if _, err := app.Exec(ctx,
		`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, 'A', '[{"a":1}]')`, list); err != nil {
		t.Fatalf("app INSERT: %v", err)
	}
	var entries string
	if err := app.QueryRow(ctx,
		`SELECT entries::text FROM nrs_codes WHERE list = $1 AND code = 'A'`, list).Scan(&entries); err != nil {
		t.Fatalf("app SELECT: %v", err)
	}
	if entries != `[{"a": 1}]` {
		t.Fatalf("entries = %s, want [{\"a\": 1}]", entries)
	}
	tag, err := app.Exec(ctx,
		`UPDATE nrs_codes SET entries = '[{"a":2}]' WHERE list = $1 AND code = 'A'`, list)
	if err != nil {
		t.Fatalf("app UPDATE entries: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("UPDATE entries affected %d rows, want 1", tag.RowsAffected())
	}
	tag, err = app.Exec(ctx, `DELETE FROM nrs_codes WHERE list = $1 AND code = 'A'`, list)
	if err != nil {
		t.Fatalf("app DELETE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("DELETE affected %d rows, want 1", tag.RowsAffected())
	}
}

func TestSchema_AppCannotRewriteCodeOrList(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	list := newListName(t, super)

	if _, err := super.Exec(ctx,
		`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, 'A', '[{"a":1}]')`, list); err != nil {
		t.Fatalf("seed nrs_codes: %v", err)
	}
	_, err := app.Exec(ctx, `UPDATE nrs_codes SET code = 'x' WHERE list = $1`, list)
	wantState(t, "app UPDATE code", err, "42501")
	_, err = app.Exec(ctx, `UPDATE nrs_codes SET list = 'y' WHERE list = $1`, list)
	wantState(t, "app UPDATE list", err, "42501")

	var code string
	if err := super.QueryRow(ctx, `SELECT code FROM nrs_codes WHERE list = $1`, list).Scan(&code); err != nil {
		t.Fatalf("row gone after refused updates: %v", err)
	}
	if code != "A" {
		t.Fatalf("code = %q, want A", code)
	}
}

func TestSchema_SyncLogIsInsertOnlyForApp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	list := newListName(t, super)

	var id string
	if err := app.QueryRow(ctx,
		`INSERT INTO nrs_code_list_syncs (list, entry_count, added, removed, changed)
		 VALUES ($1, 3, '{A,B}', '{}', '{}') RETURNING id::text`, list).Scan(&id); err != nil {
		t.Fatalf("app INSERT sync row: %v", err)
	}
	var n int
	if err := app.QueryRow(ctx,
		`SELECT entry_count FROM nrs_code_list_syncs WHERE id = $1::uuid`, id).Scan(&n); err != nil {
		t.Fatalf("app SELECT sync row: %v", err)
	}
	if n != 3 {
		t.Fatalf("entry_count = %d, want 3", n)
	}
	_, err := app.Exec(ctx,
		`INSERT INTO nrs_code_list_syncs (id, list, entry_count, added, removed, changed)
		 VALUES ($1::uuid, $2, 1, '{}', '{}', '{}')`, id, list)
	wantState(t, "app INSERT duplicate sync id", err, "23505")
	_, err = app.Exec(ctx, `UPDATE nrs_code_list_syncs SET entry_count = 4 WHERE id = $1::uuid`, id)
	wantState(t, "app UPDATE sync row", err, "42501")
	_, err = app.Exec(ctx, `DELETE FROM nrs_code_list_syncs WHERE id = $1::uuid`, id)
	wantState(t, "app DELETE sync row", err, "42501")

	if err := super.QueryRow(ctx,
		`SELECT entry_count FROM nrs_code_list_syncs WHERE id = $1::uuid`, id).Scan(&n); err != nil {
		t.Fatalf("sync row gone after refused DELETE: %v", err)
	}
	if n != 3 {
		t.Fatalf("entry_count = %d after refused UPDATE, want 3", n)
	}
}

func TestSchema_RefusesBlankAndMalformedRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	list := newListName(t, super)

	const insert = `INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, $3::jsonb)`
	// A valid row first: the refusals below must come from the CHECK, not a missing table.
	if _, err := app.Exec(ctx, insert, list, "A", `[{"a":1}]`); err != nil {
		t.Fatalf("valid INSERT: %v", err)
	}

	cases := []struct {
		name, list, code, entries string
		want                      string
	}{
		{"blank code", list, "", `[{"a":1}]`, "23514"},
		{"blank list", "", "B", `[{"a":1}]`, "23514"},
		{"entries object", list, "C", `{}`, "23514"},
		{"entries empty array", list, "D", `[]`, "23514"},
		{"duplicate list and code", list, "A", `[{"a":2}]`, "23505"},
		{"entries json null", list, "E", `null`, "23514"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := app.Exec(ctx, insert, c.list, c.code, c.entries)
			wantState(t, c.name, err, c.want)
		})
	}
	t.Run("entries SQL null", func(t *testing.T) {
		_, err := app.Exec(ctx, insert, list, "F", nil)
		wantState(t, "entries NULL", err, "23502")
	})

	const sync = `INSERT INTO nrs_code_list_syncs (list, entry_count, added, removed, changed)
		VALUES ($1, $2, '{}', '{}', '{}')`
	if _, err := app.Exec(ctx, sync, list, 1); err != nil {
		t.Fatalf("valid sync INSERT: %v", err)
	}
	for _, c := range []struct {
		name  string
		list  string
		count int
	}{
		{"sync entry_count zero", list, 0},
		{"sync entry_count negative", list, -1},
		{"sync blank list", "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := app.Exec(ctx, sync, c.list, c.count)
			wantState(t, c.name, err, "23514")
		})
	}
	t.Run("sync NULL changed", func(t *testing.T) {
		_, err := app.Exec(ctx, `INSERT INTO nrs_code_list_syncs (list, entry_count, added, removed, changed)
			VALUES ($1, 1, '{}', '{}', NULL)`, list)
		wantState(t, "changed NULL", err, "23502")
	})

	var n int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM nrs_codes WHERE list = $1`, list).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("nrs_codes rows for list = %d, want 1 (only the valid row)", n)
	}
	if err := super.QueryRow(ctx, `SELECT count(*) FROM nrs_code_list_syncs WHERE list = $1`, list).Scan(&n); err != nil {
		t.Fatalf("count syncs: %v", err)
	}
	if n != 1 {
		t.Fatalf("nrs_code_list_syncs rows for list = %d, want 1 (only the valid row)", n)
	}
}

// The key is (list, code): a code repeats across lists, and a list holds many codes.
func TestSchema_KeyIsListAndCode(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	listA, listB := newListName(t, super), newListName(t, super)

	const insert = `INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{"a":1}]')`
	for _, r := range [][2]string{{listA, "A"}, {listA, "B"}, {listB, "A"}} {
		if _, err := app.Exec(ctx, insert, r[0], r[1]); err != nil {
			t.Fatalf("INSERT (%s, %s): %v", r[0], r[1], err)
		}
	}
}
