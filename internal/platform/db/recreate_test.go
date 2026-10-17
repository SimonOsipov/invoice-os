package db_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	outOfOrderFile = "20261009061443_invitation_registration.sql"
	renameOld      = "20261016190930_customer_status.sql"
	renameNew      = "20261016190931_customer_status.sql"
	orphanVersion  = 20260101000000
)

// dsnForDB returns dsn pointed at database name.
func dsnForDB(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// newScratchDB creates an empty database fast03_<hex> and returns superuser
// and migrator DSNs for it. The shared invoice_os is never touched.
func newScratchDB(t *testing.T) (superDSN, migDSN string) {
	t.Helper()
	baseSuper, baseMig := requireProvisionDSNs(t)
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random name: %v", err)
	}
	name := "fast03_" + hex.EncodeToString(b[:])
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, baseSuper)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	return dsnForDB(t, baseSuper, name), dsnForDB(t, baseMig, name)
}

// migratedScratchDB returns a Bootstrapped scratch database migrated from fsys.
func migratedScratchDB(t *testing.T, fsys fs.FS) (superDSN, migDSN string) {
	t.Helper()
	superDSN, migDSN = newScratchDB(t)
	bootstrapAndMigrate(t, superDSN, migDSN, fsys)
	return superDSN, migDSN
}

func bootstrapAndMigrate(t *testing.T, superDSN, migDSN string, fsys fs.FS) {
	t.Helper()
	ctx := context.Background()
	if err := db.Bootstrap(ctx, superDSN, devRolePasswords(), dbsql.FS); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := db.MigrateUp(ctx, migDSN, fsys); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
}

func fsWithout(fsys fs.FS, name string) fstest.MapFS {
	requireNotNewest(fsys, name)
	return copyFS(fsys, func(n string) (string, bool) { return n, n != name })
}

func fsRenamed(fsys fs.FS, oldName, newName string) fstest.MapFS {
	requireFile(fsys, oldName)
	return copyFS(fsys, func(n string) (string, bool) {
		if n == oldName {
			return newName, true
		}
		return n, true
	})
}

// requireFile panics when a fixture's named migration is gone, so a later
// squash or rename fails here instead of leaving a drift fixture with no drift.
func requireFile(fsys fs.FS, name string) {
	if _, err := fs.Stat(fsys, name); err != nil {
		panic(fmt.Sprintf("fixture migration %s is missing from the FS: %v", name, err))
	}
}

// requireNotNewest also panics when name is the newest file: dropping it
// leaves no applied version above a gap, so it is not out of order.
func requireNotNewest(fsys fs.FS, name string) {
	requireFile(fsys, name)
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() > name {
			return
		}
	}
	panic(fmt.Sprintf("fixture migration %s is the newest file; dropping it is not out of order", name))
}

func copyFS(fsys fs.FS, f func(name string) (string, bool)) fstest.MapFS {
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		to, ok := f(e.Name())
		if !ok {
			continue
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			panic(err)
		}
		out[to] = &fstest.MapFile{Data: body}
	}
	return out
}

// execSuper runs sql on the scratch database as superuser.
func execSuper(t *testing.T, dsn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func scalarSuper[T any](t *testing.T, dsn, sql string, args ...any) T {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	var v T
	if err := conn.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

func recreate(t *testing.T, superDSN string, fsys fs.FS, logger *slog.Logger) (bool, error) {
	t.Helper()
	return db.RecreateStuckSchema(context.Background(), superDSN, fsys, logger)
}

func mustRecreate(t *testing.T, superDSN string, fsys fs.FS, want bool) {
	t.Helper()
	got, err := recreate(t, superDSN, fsys, nil)
	if err != nil {
		t.Fatalf("RecreateStuckSchema: %v", err)
	}
	if got != want {
		t.Fatalf("RecreateStuckSchema = %v, want %v", got, want)
	}
}

func publicObjectCounts(t *testing.T, dsn string) (relations, routines int) {
	t.Helper()
	relations = scalarSuper[int](t, dsn, `SELECT count(*) FROM pg_class WHERE relnamespace = 'public'::regnamespace`)
	routines = scalarSuper[int](t, dsn, `SELECT count(*) FROM pg_proc WHERE pronamespace = 'public'::regnamespace`)
	return relations, routines
}

func addCanary(t *testing.T, dsn, schema string) {
	t.Helper()
	execSuper(t, dsn, `CREATE TABLE `+schema+`.fast03_canary (id int)`)
	execSuper(t, dsn, `INSERT INTO `+schema+`.fast03_canary VALUES (1)`)
}

func canaryRows(t *testing.T, dsn, schema string) int {
	t.Helper()
	return scalarSuper[int](t, dsn, `SELECT count(*) FROM `+schema+`.fast03_canary`)
}

func canaryExists(t *testing.T, dsn string) bool {
	t.Helper()
	return scalarSuper[bool](t, dsn, `SELECT to_regclass('public.fast03_canary') IS NOT NULL`)
}

func ledgerExists(t *testing.T, dsn string) bool {
	t.Helper()
	return scalarSuper[bool](t, dsn, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`)
}

func insertLedgerVersion(t *testing.T, dsn string, v int64) {
	t.Helper()
	execSuper(t, dsn, `INSERT INTO public.goose_db_version (version_id, is_applied) VALUES ($1, true)`, v)
}

func sqlFileCount(t *testing.T) int {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(names)
}

func TestProvisionRecreateLedgerDrift(t *testing.T) {
	cases := []struct {
		name                     string
		files, ledger            []int64
		wantMissing, wantOrphans []int64
		wantStuck                bool
	}{
		{"matched sets", []int64{1, 2, 3}, []int64{1, 2, 3}, nil, nil, false},
		{"one file below the max with no row", []int64{1, 2, 3}, []int64{1, 3}, []int64{2}, nil, true},
		{"two such files given unsorted", []int64{5, 3, 1, 2}, []int64{1, 5}, []int64{2, 3}, nil, true},
		{"orphan below the max, every file applied", []int64{1, 3}, []int64{1, 2, 3}, nil, []int64{2}, true},
		{"orphan above every file, every file applied", []int64{1, 2}, []int64{1, 2, 9}, nil, []int64{9}, true},
		{"rename to a newer version", []int64{1, 2, 4}, []int64{1, 2, 3}, nil, []int64{3}, true},
		{"rename to an older version", []int64{1, 2, 4, 5}, []int64{1, 3, 4, 5}, []int64{2}, []int64{3}, true},
		{"normal pending file above the max", []int64{1, 2, 3}, []int64{1, 2}, nil, nil, false},
		{"fork shape, orphan from production and no pending file", []int64{1, 2, 3}, []int64{1, 2, 3, 7}, nil, []int64{7}, true},
		{"several orphans come back ascending", []int64{1}, []int64{9, 1, 7, 8, 6, 5, 4}, nil, []int64{4, 5, 6, 7, 8, 9}, true},
		{"empty ledger", []int64{1, 2}, nil, nil, nil, false},
		{"versions 0 and -1 ignored", []int64{1, 2}, []int64{0, -1, 1, 2}, nil, nil, false},
		{"ledger of only 0 and -1", []int64{1, 2}, []int64{0, -1}, nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			missing, orphans, stuck := db.LedgerDriftForTest(c.files, c.ledger)
			if !slices.Equal(missing, c.wantMissing) || !slices.Equal(orphans, c.wantOrphans) || stuck != c.wantStuck {
				t.Errorf("got missing=%v orphans=%v stuck=%v, want missing=%v orphans=%v stuck=%v",
					missing, orphans, stuck, c.wantMissing, c.wantOrphans, c.wantStuck)
			}
		})
	}
}

func TestProvisionRecreateOutOfOrderFile(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)
	addCanary(t, superDSN, "auth")
	execSuper(t, superDSN, `CREATE SCHEMA fast03_other`)
	addCanary(t, superDSN, "fast03_other")

	mustRecreate(t, superDSN, migrations.FS, true)

	if n := canaryRows(t, superDSN, "fast03_other"); n != 1 {
		t.Errorf("non-public schema canary rows = %d, want 1", n)
	}
	if rel, rtn := publicObjectCounts(t, superDSN); rel != 0 || rtn != 0 {
		t.Errorf("public holds %d relations and %d routines, want 0 and 0", rel, rtn)
	}
	if ledgerExists(t, superDSN) {
		t.Error("goose_db_version survived the recreate")
	}
	if owner := scalarSuper[string](t, superDSN, `SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'public'`); owner != "pg_database_owner" {
		t.Errorf("public owner = %q, want pg_database_owner", owner)
	}
	if !scalarSuper[bool](t, superDSN, `SELECT has_schema_privilege('public', 'public', 'USAGE')`) {
		t.Error("PUBLIC lacks USAGE on public")
	}
	if n := canaryRows(t, superDSN, "auth"); n != 1 {
		t.Errorf("auth canary rows = %d, want 1", n)
	}
}

func TestProvisionRecreateRenamedMigration(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, migrations.FS)
	addCanary(t, superDSN, "auth")

	mustRecreate(t, superDSN, fsRenamed(migrations.FS, renameOld, renameNew), true)

	if rel, rtn := publicObjectCounts(t, superDSN); rel != 0 || rtn != 0 {
		t.Errorf("public holds %d relations and %d routines, want 0 and 0", rel, rtn)
	}
	if n := canaryRows(t, superDSN, "auth"); n != 1 {
		t.Errorf("auth canary rows = %d, want 1", n)
	}
}

func TestProvisionRecreateThenMigrateRestoresSchema(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	aclBefore := scalarSuper[string](t, superDSN, `SELECT nspacl::text FROM pg_namespace WHERE nspname = 'public'`)

	mustRecreate(t, superDSN, migrations.FS, true)
	bootstrapAndMigrate(t, superDSN, migDSN, migrations.FS)

	if got := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version WHERE version_id > 0`); got != sqlFileCount(t) {
		t.Errorf("goose_db_version holds %d versions, want %d", got, sqlFileCount(t))
	}
	const hook = `public.custom_access_token_hook(jsonb)`
	if !scalarSuper[bool](t, superDSN, `SELECT to_regprocedure('`+hook+`') IS NOT NULL`) {
		t.Fatal("custom_access_token_hook(jsonb) is missing after the re-migrate")
	}
	if !scalarSuper[bool](t, superDSN, `SELECT has_function_privilege('supabase_auth_admin', '`+hook+`', 'EXECUTE')`) {
		t.Error("supabase_auth_admin lacks EXECUTE on the hook")
	}
	if aclAfter := scalarSuper[string](t, superDSN, `SELECT nspacl::text FROM pg_namespace WHERE nspname = 'public'`); aclAfter != aclBefore {
		t.Errorf("public nspacl = %s, want %s", aclAfter, aclBefore)
	}
}

func TestProvisionRecreateNoDriftIsNoOp(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, migrations.FS)
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)
	execSuper(t, superDSN, `INSERT INTO public.fast03_canary VALUES (1)`)

	var buf bytes.Buffer
	got, err := recreate(t, superDSN, migrations.FS, slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil || got {
		t.Fatalf("RecreateStuckSchema = %v, %v, want false, nil", got, err)
	}
	if n := canaryRows(t, superDSN, "public"); n != 1 {
		t.Errorf("canary rows = %d, want 1", n)
	}
	if buf.Len() != 0 {
		t.Errorf("logged %q on a database without drift", buf.String())
	}
}

func TestProvisionRecreateOrphanOnlyRecreates(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	insertLedgerVersion(t, superDSN, orphanVersion)
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)

	mustRecreate(t, superDSN, migrations.FS, true)

	if canaryExists(t, superDSN) {
		t.Error("canary table survived the recreate")
	}
	if ledgerExists(t, superDSN) {
		t.Error("goose_db_version survived the recreate")
	}
	bootstrapAndMigrate(t, superDSN, migDSN, migrations.FS)
	mustRecreate(t, superDSN, migrations.FS, false)
}

// noOpCase asserts RecreateStuckSchema returns false and keeps the canary.
func noOpCase(t *testing.T, superDSN string) {
	t.Helper()
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)
	mustRecreate(t, superDSN, migrations.FS, false)
	if !canaryExists(t, superDSN) {
		t.Error("canary table was dropped by a no-op call")
	}
}

func TestProvisionRecreateNoLedgerIsNoOp(t *testing.T) {
	superDSN, _ := newScratchDB(t)
	if err := db.Bootstrap(context.Background(), superDSN, devRolePasswords(), dbsql.FS); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	noOpCase(t, superDSN)
}

func TestProvisionRecreateEmptyLedgerIsNoOp(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, migrations.FS)
	execSuper(t, superDSN, `DELETE FROM public.goose_db_version`)
	noOpCase(t, superDSN)
}

func TestProvisionRecreateVersionZeroLedgerIsNoOp(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.DownTo(context.Background(), 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if n := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version WHERE version_id > 0`); n != 0 {
		t.Fatalf("ledger holds %d positive versions after DownTo(0), want 0", n)
	}
	noOpCase(t, superDSN)
}

func TestProvisionRecreateLogsOneLine(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	insertLedgerVersion(t, superDSN, orphanVersion)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	if got, err := recreate(t, superDSN, migrations.FS, logger); err != nil || !got {
		t.Fatalf("RecreateStuckSchema = %v, %v, want true, nil", got, err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %d records, want 1: %q", len(lines), buf.String())
	}
	var rec struct {
		Level   string
		Missing []int64
		Orphans []int64
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	if rec.Level != "WARN" || !slices.Equal(rec.Missing, []int64{20261009061443}) || !slices.Equal(rec.Orphans, []int64{orphanVersion}) {
		t.Errorf("record = %+v, want WARN missing=[20261009061443] orphans=[%d]", rec, orphanVersion)
	}
}

func TestProvisionRecreateEmptyMigrationSetRefused(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)

	got, err := recreate(t, superDSN, fstest.MapFS{}, nil)
	if err == nil || got {
		t.Fatalf("RecreateStuckSchema = %v, %v, want false and an error", got, err)
	}
	if !canaryExists(t, superDSN) {
		t.Error("canary table was dropped despite the empty migration set")
	}
}

func TestProvisionRecreateDDLFailureRollsBack(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	execSuper(t, superDSN, `CREATE TABLE public.fast03_canary (id int)`)
	rowsBefore := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version`)
	execSuper(t, superDSN, `CREATE SCHEMA fast03_guard`)
	execSuper(t, superDSN, `CREATE FUNCTION fast03_guard.deny() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fast03 guard'; END $$`)
	execSuper(t, superDSN, `CREATE EVENT TRIGGER fast03_deny ON ddl_command_start WHEN TAG IN ('CREATE SCHEMA') EXECUTE FUNCTION fast03_guard.deny()`)

	got, err := recreate(t, superDSN, migrations.FS, nil)
	if err == nil || got {
		t.Fatalf("RecreateStuckSchema = %v, %v, want false and an error", got, err)
	}
	if !canaryExists(t, superDSN) {
		t.Error("canary table was dropped; the DROP was not rolled back")
	}
	if n := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version`); n != rowsBefore {
		t.Errorf("goose_db_version rows = %d, want %d", n, rowsBefore)
	}
}

func TestProvisionRecreateReleasesLockAndConnection(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	baseSuper, _ := requireProvisionDSNs(t)
	name := strings.TrimPrefix(func() string { u, _ := url.Parse(superDSN); return u.Path }(), "/")
	ctx := context.Background()

	counter, err := pgx.Connect(ctx, baseSuper)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer counter.Close(ctx)
	count := func() int {
		var n int
		if err := counter.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = $1`, name).Scan(&n); err != nil {
			t.Fatalf("count activity: %v", err)
		}
		return n
	}
	before := count()

	check := func(label string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for count() != before && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if n := count(); n != before {
			t.Errorf("%s: %d connections to the scratch database, want %d", label, n, before)
		}
		probe, err := pgx.Connect(ctx, superDSN)
		if err != nil {
			t.Fatalf("%s: connect probe: %v", label, err)
		}
		defer probe.Close(ctx)
		var ok bool
		if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, db.BootstrapAdvisoryLockKey).Scan(&ok); err != nil || !ok {
			t.Errorf("%s: advisory lock not free (ok=%v, err=%v)", label, ok, err)
		}
	}

	if got, err := recreate(t, superDSN, fstest.MapFS{}, nil); err == nil || got {
		t.Fatalf("empty migration set: got %v, %v, want false and an error", got, err)
	}
	check("after an empty-migration-set refusal")

	execSuper(t, superDSN, `CREATE SCHEMA fast03_guard`)
	execSuper(t, superDSN, `CREATE FUNCTION fast03_guard.deny() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fast03 guard'; END $$`)
	execSuper(t, superDSN, `CREATE EVENT TRIGGER fast03_deny ON ddl_command_start WHEN TAG IN ('CREATE SCHEMA') EXECUTE FUNCTION fast03_guard.deny()`)
	if got, err := recreate(t, superDSN, migrations.FS, nil); err == nil || got {
		t.Fatalf("DDL failure: got %v, %v, want false and an error", got, err)
	}
	check("after a DDL failure")
	execSuper(t, superDSN, `DROP EVENT TRIGGER fast03_deny`)

	mustRecreate(t, superDSN, migrations.FS, true)
	check("after a stuck recreate")
	bootstrapAndMigrate(t, superDSN, migDSN, migrations.FS)
	mustRecreate(t, superDSN, migrations.FS, false)
	check("after a not-stuck call")
}

func TestProvisionRecreateUnreachableDSNFailsBounded(t *testing.T) {
	start := time.Now()
	got, err := recreate(t, "postgres://postgres:postgres@127.0.0.1:1/none?sslmode=disable", migrations.FS, nil)
	if err == nil || got {
		t.Fatalf("RecreateStuckSchema = %v, %v, want false and an error", got, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s, want under 10s", d)
	}
}

func TestProvisionScratchDBUsesDevPasswords(t *testing.T) {
	superDSN, _ := newScratchDB(t)
	_, baseMig := requireProvisionDSNs(t)
	if err := db.Bootstrap(context.Background(), superDSN, devRolePasswords(), dbsql.FS); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := attemptLogin(t, baseMig); err != nil {
		t.Errorf("invoice_migrator login on the shared DSN failed after a scratch Bootstrap: %v", err)
	}
}

func TestProvisionRecreateLockTimeoutFailsBounded(t *testing.T) {
	superDSN, _ := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	ctx := context.Background()
	holder, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer holder.Close(ctx)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE public.goose_db_version IN ACCESS SHARE MODE`); err != nil {
		t.Fatalf("lock: %v", err)
	}

	start := time.Now()
	got, err := recreate(t, superDSN, migrations.FS, nil)
	var pgErr *pgconn.PgError
	if err == nil || got || !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("RecreateStuckSchema = %v, %v, want false and a 55P03 lock timeout", got, err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %s, want under 20s", d)
	}
	_ = tx.Rollback(ctx)
	if !ledgerExists(t, superDSN) {
		t.Error("public was dropped despite the lock timeout")
	}
	probe, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatalf("connect probe: %v", err)
	}
	defer probe.Close(ctx)
	var ok bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, db.BootstrapAdvisoryLockKey).Scan(&ok); err != nil || !ok {
		t.Errorf("advisory lock not free (ok=%v, err=%v)", ok, err)
	}
}
