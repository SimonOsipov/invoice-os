package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const nonDemoTenant = "fa570300-0000-4000-8000-000000000001"

var fileVersionRE = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// recreateConfig is a pr-452 preview: every gate of the recreate on.
func recreateConfig(superDSN, migDSN string, fsys fs.FS, logger *slog.Logger) db.ProvisionConfig {
	return db.ProvisionConfig{
		Environment:            "development",
		BootstrapFlag:          "true",
		RailwayEnvironmentName: "pr-452",
		ResetFlag:              "true",
		SuperuserDSN:           superDSN,
		MigrationDSN:           migDSN,
		Passwords:              devRolePasswords(),
		BootstrapFS:            dbsql.FS,
		MigrationsFS:           fsys,
		SeedFS:                 dbsql.FS,
		Logger:                 logger,
	}
}

func provisionWithin(t *testing.T, cfg db.ProvisionConfig) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return db.Provision(ctx, cfg)
}

func mustProvision(t *testing.T, label string, cfg db.ProvisionConfig) {
	t.Helper()
	if err := provisionWithin(t, cfg); err != nil {
		t.Fatalf("%s: Provision: %v", label, err)
	}
}

func fileVersionSet(t *testing.T, fsys fs.FS) []int64 {
	t.Helper()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var out []int64
	for _, e := range entries {
		if m := fileVersionRE.FindStringSubmatch(e.Name()); m != nil {
			v, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				t.Fatalf("parse %s: %v", e.Name(), err)
			}
			out = append(out, v)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		t.Fatal("no migration files found")
	}
	return out
}

func ledgerVersionSet(t *testing.T, dsn string) []int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT version_id FROM public.goose_db_version WHERE version_id > 0 ORDER BY version_id`)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return out
}

func demoTenantCount(t *testing.T, dsn string) int {
	t.Helper()
	return scalarSuper[int](t, dsn, `SELECT count(*) FROM tenants WHERE id::text = ANY($1)`, db.DemoTenants)
}

type recreateRecord struct {
	Missing []int64 `json:"missing"`
	Orphans []int64 `json:"orphans"`
}

// logRecords decodes every JSON line in buf and returns all records plus the
// ones carrying both `missing` and `orphans` (the recreate line).
func logRecords(t *testing.T, buf *bytes.Buffer) (all int, recreates []recreateRecord) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		all++
		_, hasMissing := raw["missing"]
		_, hasOrphans := raw["orphans"]
		if hasMissing && hasOrphans {
			var rec recreateRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode recreate record %q: %v", line, err)
			}
			recreates = append(recreates, rec)
		}
	}
	return all, recreates
}

// requireNoRecreateRecord fails when buf holds a recreate record; it first
// requires some other record so an empty buffer cannot pass for "no record".
func requireNoRecreateRecord(t *testing.T, label string, buf *bytes.Buffer) {
	t.Helper()
	all, recreates := logRecords(t, buf)
	if all == 0 {
		t.Fatalf("%s: logger captured no record at all; the scanner cannot see a recreate line", label)
	}
	if len(recreates) != 0 {
		t.Errorf("%s: logged %d recreate record(s) %+v, want none", label, len(recreates), recreates)
	}
}

func newJSONLogger() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewJSONHandler(&buf, nil))
}

func TestProvisionRecreatesAfterOutOfOrderMigration(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))

	mustProvision(t, "pr-452 after an out-of-order file", recreateConfig(superDSN, migDSN, migrations.FS, nil))

	want := fileVersionSet(t, migrations.FS)
	if got := ledgerVersionSet(t, superDSN); !slices.Equal(got, want) {
		t.Errorf("ledger holds %d versions, want the %d file versions; first difference is in %v vs %v", len(got), len(want), got, want)
	}
	if n := demoTenantCount(t, superDSN); n != 5 {
		t.Errorf("demo tenants = %d, want 5", n)
	}
}

func TestProvisionRecreatesAfterRenamedMigration(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	renamed := fsRenamed(migrations.FS, renameOld, renameNew)

	mustProvision(t, "pr-452 after a rename", recreateConfig(superDSN, migDSN, renamed, nil))

	ledger := ledgerVersionSet(t, superDSN)
	if !slices.Contains(ledger, 20261016190931) {
		t.Error("ledger lacks 20261016190931")
	}
	if slices.Contains(ledger, 20261016190930) {
		t.Error("ledger still holds 20261016190930")
	}
}

func TestProvisionRecreateLogsOnceThenNothing(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	buf, logger := newJSONLogger()
	cfg := recreateConfig(superDSN, migDSN, migrations.FS, logger)

	mustProvision(t, "run 1", cfg)
	_, recreates := logRecords(t, buf)
	if len(recreates) != 1 {
		t.Fatalf("run 1 logged %d recreate records, want 1: %s", len(recreates), buf.String())
	}
	if !slices.Equal(recreates[0].Missing, []int64{20261009061443}) || len(recreates[0].Orphans) != 0 {
		t.Errorf("run 1 record = %+v, want missing=[20261009061443] orphans=[]", recreates[0])
	}

	buf.Reset()
	mustProvision(t, "run 2", cfg)
	requireNoRecreateRecord(t, "run 2", buf)
}

func TestProvisionRecreateNeverRunsOutsideAPreview(t *testing.T) {
	cases := []struct {
		name                        string
		env, bootstrap, railway, rs string
	}{
		{"production railway name", "development", "true", "production", "true"},
		{"pre-rename development name", "development", "true", "development", "true"},
		{"empty railway name", "development", "true", "", "true"},
		{"reset flag empty", "development", "true", "pr-452", ""},
		{"reset flag 1", "development", "true", "pr-452", "1"},
		{"bootstrap flag empty", "development", "", "pr-452", "true"},
		{"environment production", "production", "true", "production", "true"},
		{"environment production with a preview railway name", "production", "true", "pr-452", "true"},
		{"environment staging", "staging", "true", "pr-452", "true"},
		{"environment Development", "Development", "true", "pr-452", "true"},
		{"environment trailing space", "development ", "true", "pr-452", "true"},
		{"environment empty", "", "true", "pr-452", "true"},
		{"railway name Production", "development", "true", "Production", "true"},
		{"railway name PR-452", "development", "true", "PR-452", "true"},
		{"railway name pr-452x", "development", "true", "pr-452x", "true"},
		{"railway name xpr-452", "development", "true", "xpr-452", "true"},
		{"railway name pr- with no number", "development", "true", "pr-", "true"},
		{"railway name trailing space", "development", "true", "pr-452 ", "true"},
		{"railway name leading space", "development", "true", " pr-452", "true"},
		{"railway name trailing newline", "development", "true", "pr-452\n", "true"},
		{"reset flag True", "development", "true", "pr-452", "True"},
		{"reset flag trailing space", "development", "true", "pr-452", "true "},
		{"bootstrap flag True", "development", "True", "pr-452", "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
			addCanary(t, superDSN, "public")
			ledgerBefore := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version`)
			relBefore, rtnBefore := publicObjectCounts(t, superDSN)

			cfg := recreateConfig(superDSN, migDSN, migrations.FS, nil)
			cfg.Environment, cfg.BootstrapFlag, cfg.RailwayEnvironmentName, cfg.ResetFlag = c.env, c.bootstrap, c.railway, c.rs
			err := provisionWithin(t, cfg)

			if err == nil || !strings.Contains(err.Error(), "out-of-order") {
				t.Errorf("Provision error = %v, want one containing out-of-order", err)
			}
			if !canaryExists(t, superDSN) {
				t.Error("canary table was dropped")
			}
			if n := scalarSuper[int](t, superDSN, `SELECT count(*) FROM public.goose_db_version`); n != ledgerBefore {
				t.Errorf("goose_db_version rows = %d, want %d", n, ledgerBefore)
			}
			if rel, rtn := publicObjectCounts(t, superDSN); rel != relBefore || rtn != rtnBefore {
				t.Errorf("public holds %d relations and %d routines, want %d and %d", rel, rtn, relBefore, rtnBefore)
			}
		})
	}

	previews := []struct{ name, env, railway string }{
		{"control: pr-452 with every gate on recreates", "development", "pr-452"},
		{"repo-qualified preview name recreates", "development", "invoice-os-pr-452"},
		{"environment named like a preview recreates", "pr-452", "pr-452"},
	}
	for _, c := range previews {
		t.Run(c.name, func(t *testing.T) {
			superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
			addCanary(t, superDSN, "public")
			cfg := recreateConfig(superDSN, migDSN, migrations.FS, nil)
			cfg.Environment, cfg.RailwayEnvironmentName = c.env, c.railway

			mustProvision(t, c.name, cfg)

			if canaryExists(t, superDSN) {
				t.Error("canary table survived; the table above cannot tell a drop from no drop")
			}
		})
	}
}

func TestProvisionRecreateRunsBeforeBootstrapAndMigrate(t *testing.T) {
	const filename = "provision.go"
	want := []string{"RecreateStuckSchema", "Bootstrap", "MigrateUp"}

	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	got := provisionCallOrder(t, filename, string(src), want)
	if len(got) == 0 {
		t.Fatalf("%s: Provision calls none of %v; the scanner sees nothing", filename, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Provision's step order is %v, want %v", got, want)
	}
}

func TestProvisionRecreateKeepsAuthAndRestoresGrants(t *testing.T) {
	superDSN, migDSN := newScratchDB(t)
	cfg := recreateConfig(superDSN, migDSN, migrations.FS, nil)
	mustProvision(t, "normal boot", cfg)

	const hook = `public.custom_access_token_hook(jsonb)`
	aclBefore := scalarSuper[string](t, superDSN, `SELECT nspacl::text FROM pg_namespace WHERE nspname = 'public'`)
	addCanary(t, superDSN, "auth")
	addCanary(t, superDSN, "public")
	execSuper(t, superDSN, `INSERT INTO tenants (id, name) VALUES ($1, 'fast03 non-demo tenant')`, nonDemoTenant)
	if n := scalarSuper[int](t, superDSN, `SELECT count(*) FROM tenants WHERE id = $1`, nonDemoTenant); n != 1 {
		t.Fatalf("non-demo tenant rows before = %d, want 1", n)
	}
	execSuper(t, superDSN, `DELETE FROM public.goose_db_version WHERE version_id = $1`, int64(20261009061443))

	mustProvision(t, "boot after drift", cfg)

	if n := canaryRows(t, superDSN, "auth"); n != 1 {
		t.Errorf("auth canary rows = %d, want 1", n)
	}
	if canaryExists(t, superDSN) {
		t.Error("public canary table survived; Reset never drops a table, so only the recreate removes it")
	}
	if aclAfter := scalarSuper[string](t, superDSN, `SELECT nspacl::text FROM pg_namespace WHERE nspname = 'public'`); aclAfter != aclBefore {
		t.Errorf("public nspacl = %s, want %s", aclAfter, aclBefore)
	}
	if n := demoTenantCount(t, superDSN); n != 5 {
		t.Errorf("demo tenants = %d, want 5", n)
	}
	if n := scalarSuper[int](t, superDSN, `SELECT count(*) FROM tenants WHERE id = $1`, nonDemoTenant); n != 0 {
		t.Errorf("non-demo tenant rows after = %d, want 0", n)
	}
	if !scalarSuper[bool](t, superDSN, `SELECT has_function_privilege('supabase_auth_admin', '`+hook+`', 'EXECUTE')`) {
		t.Error("supabase_auth_admin lacks EXECUTE on the access-token hook")
	}
}

func TestProvisionHealthyPreviewRedeployDoesNotRecreate(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	addCanary(t, superDSN, "public")
	buf, logger := newJSONLogger()
	cfg := recreateConfig(superDSN, migDSN, migrations.FS, logger)

	mustProvision(t, "boot 1", cfg)
	mustProvision(t, "boot 2", cfg)

	if !canaryExists(t, superDSN) {
		t.Error("canary table was dropped on a healthy redeploy")
	}
	requireNoRecreateRecord(t, "healthy redeploy", buf)
}

func TestProvisionOrphanOnlyPreviewRecreatesOnce(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	insertLedgerVersion(t, superDSN, orphanVersion)
	addCanary(t, superDSN, "public")
	buf, logger := newJSONLogger()
	cfg := recreateConfig(superDSN, migDSN, migrations.FS, logger)

	mustProvision(t, "run 1", cfg)
	if canaryExists(t, superDSN) {
		t.Error("canary table survived the orphan-only recreate")
	}
	_, recreates := logRecords(t, buf)
	if len(recreates) != 1 {
		t.Fatalf("run 1 logged %d recreate records, want 1: %s", len(recreates), buf.String())
	}
	if !slices.Equal(recreates[0].Orphans, []int64{orphanVersion}) || len(recreates[0].Missing) != 0 {
		t.Errorf("run 1 record = %+v, want orphans=[%d] missing=[]", recreates[0], orphanVersion)
	}

	buf.Reset()
	mustProvision(t, "run 2", cfg)
	requireNoRecreateRecord(t, "run 2", buf)
}

func TestProvisionMigrateFailureAfterRecreateDoesNotRecreateAgain(t *testing.T) {
	superDSN, migDSN := migratedScratchDB(t, fsWithout(migrations.FS, outOfOrderFile))
	broken := copyFS(migrations.FS, func(n string) (string, bool) { return n, true })
	broken["20261231000000_broken.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1/0;\n\n-- +goose Down\nSELECT 1;\n")}

	err := provisionWithin(t, recreateConfig(superDSN, migDSN, broken, nil))
	if err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("Provision with a broken migration = %v, want an error from migrate", err)
	}
	if strings.Contains(err.Error(), "out-of-order") || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("Provision error = %v, want the broken file's division by zero after the recreate, not out-of-order", err)
	}

	addCanary(t, superDSN, "public")
	buf, logger := newJSONLogger()
	mustProvision(t, "good file set", recreateConfig(superDSN, migDSN, migrations.FS, logger))

	if !canaryExists(t, superDSN) {
		t.Error("canary table was dropped; the good boot recreated again")
	}
	requireNoRecreateRecord(t, "good file set", buf)
}
