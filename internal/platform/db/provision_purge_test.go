// Suite for the demo purge's wiring into db.Provision. Authored test-first,
// before the wiring exists, so every case fails on its own assertion rather
// than on a missing symbol.
//
// DB-backed cases are env-gated via requireProvisionDSNs (provision_test.go);
// the two source-scan cases need no database.
package db_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// productionShapedProvisionConfig is the field shape cmd/gateway/main.go
// constructs on the persistent environment: ENVIRONMENT forks verbatim as
// "development", RAILWAY_ENVIRONMENT_NAME is "production" and GATEWAY_DB_RESET
// is unset, so the purge runs where Reset refuses.
func productionShapedProvisionConfig(superDSN, migDSN string) db.ProvisionConfig {
	return db.ProvisionConfig{
		Environment:            "development",
		BootstrapFlag:          "true",
		RailwayEnvironmentName: "production",
		ResetFlag:              "",
		SuperuserDSN:           superDSN,
		MigrationDSN:           migDSN,
		Passwords:              devRolePasswords(),
		BootstrapFS:            dbsql.FS,
		MigrationsFS:           migrations.FS,
		SeedFS:                 dbsql.FS,
	}
}

// restoreCuratedDemoState re-seeds in cleanup, matching every other DB-backed
// case in this package that disturbs the shared demo fixtures.
func restoreCuratedDemoState(t *testing.T, superDSN string) {
	t.Helper()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state: %v", err)
		}
	})
}

// seedBaseline converges the demo tenants before a case plants residue under
// one of them: earlier cases in this package empty the schema, so the tenant
// rows are not there by default.
func seedBaseline(t *testing.T, superDSN string) {
	t.Helper()
	if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
		t.Fatalf("test setup: seed baseline: %v", err)
	}
}

// plantDemoResidue inserts one business_entities row under the demo tenant and
// returns its tin. In a shape where Reset refuses, only the purge removes it.
func plantDemoResidue(t *testing.T, pool *pgxpool.Pool, label string) string {
	t.Helper()
	tin := "66666666-" + uuid.NewString()[:4]
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3)`,
		demoTenantID, "purge probe "+label, tin,
	); err != nil {
		t.Fatalf("plant demo residue (precondition): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, tin)
	})
	return tin
}

func countEntityTIN(t *testing.T, pool *pgxpool.Pool, tenantID, tin string) int {
	t.Helper()
	return mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, tenantID, tin)
}

// ---- DB-backed: the purge runs inside Provision ----------------------------

// TestProvisionPurgesBeforeSeeding (AC-1): residue under a demo tenant is gone
// after one Provision call, and the curated portfolio is back — which is only
// true if Seed runs AFTER the purge rather than before it.
func TestProvisionPurgesBeforeSeeding(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	tin := plantDemoResidue(t, pool, "purges-before-seeding")

	if err := db.Provision(ctx, productionShapedProvisionConfig(superDSN, migDSN)); err != nil {
		t.Fatalf("Provision (production shape): %v", err)
	}

	if n := countEntityTIN(t, pool, demoTenantID, tin); n != 0 {
		t.Errorf("demo residue (tin=%s) count after Provision = %d, want 0 — Provision must run the purge", tin, n)
	}
	if entities := fetchDemoBusinessEntities(t, pool, demoTenantID); len(entities) != 10 {
		t.Errorf("curated business_entities after Provision = %d, want 10 — Seed must run after the purge, not before it", len(entities))
	}
}

// TestProvisionPurgeRunsAgainstThePersistentEnvironmentName (AC-5): the purge
// fires on the environment name Reset is built to refuse, and reaches only the
// allowlisted tenants — a non-demo probe survives, so no TRUNCATE fired.
func TestProvisionPurgeRunsAgainstThePersistentEnvironmentName(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	if cfg.ResetWillRun() {
		t.Fatal("test setup: ResetWillRun() is true for the production shape, so this case would not isolate the purge")
	}

	probeTenant := newNonDemoProbeTenant(t, pool, "persistent-env")
	probeTIN := "55555555-" + uuid.NewString()[:4]
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3)`,
		probeTenant, "non-demo probe", probeTIN,
	); err != nil {
		t.Fatalf("insert non-demo probe row (precondition): %v", err)
	}
	demoTIN := plantDemoResidue(t, pool, "persistent-env")

	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (RailwayEnvironmentName=production, ResetFlag unset): %v", err)
	}

	if n := countEntityTIN(t, pool, probeTenant, probeTIN); n != 1 {
		t.Errorf("non-demo probe row count after Provision = %d, want 1 — the purge must not reach a tenant outside db.DemoTenants", n)
	}
	if n := countEntityTIN(t, pool, demoTenantID, demoTIN); n != 0 {
		t.Errorf("demo residue count after Provision = %d, want 0 — the purge's only gate is BootstrapEnabled, so 'production' must not stop it", n)
	}
}

// TestProvisionPurgeSkippedWhenBootstrapGuardOff (AC-4): with the guard off the
// purge does not run, and the poison superuser DSN proves no superuser
// connection was opened at all.
func TestProvisionPurgeSkippedWhenBootstrapGuardOff(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	seedBaseline(t, superDSN)

	tin := plantDemoResidue(t, pool, "guard-off")

	cfg := productionShapedProvisionConfig(superuserPoisonDSN, migDSN)
	cfg.BootstrapFlag = "false"

	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision with the bootstrap guard off: %v — a guard-off boot must never dial the superuser DSN", err)
	}
	if n := countEntityTIN(t, pool, demoTenantID, tin); n != 1 {
		t.Errorf("demo residue count after a guard-off Provision = %d, want 1 — the purge must stay under the bootstrap guard", n)
	}
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeSkipped {
		t.Errorf("db.DemoPurgeOutcome = %q after a guard-off Provision, want %q", got, db.DemoPurgeSkipped)
	}
}

// TestProvisionPurgeOutcomeIsTrueOnSuccess (AC-1): the outcome var reads "true"
// after a successful purge.
func TestProvisionPurgeOutcomeIsTrueOnSuccess(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	if err := db.Provision(context.Background(), productionShapedProvisionConfig(superDSN, migDSN)); err != nil {
		t.Fatalf("Provision (production shape): %v", err)
	}
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeRan {
		t.Errorf("db.DemoPurgeOutcome = %q after a successful Provision, want %q", got, db.DemoPurgeRan)
	}
}

// holdAccessExclusive holds an ACCESS EXCLUSIVE lock on table until the test
// ends, so the purge's SET LOCAL lock_timeout fires against it.
func holdAccessExclusive(t *testing.T, dsn, table string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("test setup: connect lock holder: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("test setup: begin lock holder tx: %v", err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("test setup: lock %s: %v", table, err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
	})
}

// TestProvisionPurgeFailureStillSeedsAndReturnsNil (AC-2, AC-3): a purge that
// cannot take its locks leaves Provision's return nil, the seed done and the
// database byte-identical. invitations is the lock target because the seed
// never writes it — locking a seed-written table would hang Seed, which has no
// lock_timeout of its own.
func TestProvisionPurgeFailureStillSeedsAndReturnsNil(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)

	// Converge first, so Seed's own upserts cannot move the counts below.
	seedBaseline(t, superDSN)

	idx := slices.Index(db.PurgeTablesForTest, "invitations")
	if idx <= 0 {
		t.Fatalf("invitations sits at index %d of the purge order; the rollback witness needs tables deleted before it", idx)
	}
	earlier := db.PurgeTablesForTest[:idx]
	before := make(map[string]int, len(earlier))
	total := 0
	for _, tbl := range earlier {
		before[tbl] = countFor(t, pool, tbl, db.DemoTenants)
		total += before[tbl]
	}
	if total == 0 {
		t.Fatal("the demo tenants own no rows in the tables purged before invitations — the rollback witness would be vacuous")
	}

	holdAccessExclusive(t, superDSN, "invitations")

	if err := db.Provision(ctx, productionShapedProvisionConfig(superDSN, migDSN)); err != nil {
		t.Fatalf("Provision returned %v — a purge failure must not abort the boot", err)
	}
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeErrored {
		t.Errorf("db.DemoPurgeOutcome = %q after a failed purge, want %q", got, db.DemoPurgeErrored)
	}
	for _, tbl := range earlier {
		if after := countFor(t, pool, tbl, db.DemoTenants); after != before[tbl] {
			t.Errorf("%s holds %d demo row(s) after the failed purge, want %d — the transaction did not roll back whole", tbl, after, before[tbl])
		}
	}
	if entities := fetchDemoBusinessEntities(t, pool, demoTenantID); len(entities) != 10 {
		t.Errorf("curated business_entities after a failed purge = %d, want 10 — Seed must still run", len(entities))
	}
}

// TestProvisionPurgeAndResetTogetherAreHarmless (AC-1): the two destructive
// steps compose. Half A runs both through Provision in the pr-110 shape; half B
// runs them directly, because PurgeResult's row counts are not reachable
// through Provision.
func TestProvisionPurgeAndResetTogetherAreHarmless(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	// Reset excludes workflow_roles and Seed only upserts its own 14 keys, so a
	// stray key is removable by the purge and by nothing else in the sequence.
	strayKey := "stray-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx,
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Stray role')`,
		demoTenantID, strayKey,
	); err != nil {
		t.Fatalf("insert stray workflow_roles row (precondition): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM workflow_roles WHERE tenant_id = $1 AND key = $2`, demoTenantID, strayKey)
	})

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	cfg.RailwayEnvironmentName = "pr-110"
	cfg.ResetFlag = "true"
	if !cfg.ResetWillRun() {
		t.Fatal("test setup: ResetWillRun() is false for the pr-110 shape, so half A would exercise the purge alone")
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (pr-110 shape, reset and purge together): %v", err)
	}
	if entities := fetchDemoBusinessEntities(t, pool, demoTenantID); len(entities) != 10 {
		t.Errorf("curated business_entities after a reset+purge Provision = %d, want 10", len(entities))
	}
	if n := mustCount(t, pool,
		`SELECT count(*) FROM workflow_roles WHERE tenant_id = $1 AND key = $2`, demoTenantID, strayKey); n != 0 {
		t.Errorf("stray workflow_roles row count after a pr-110 Provision = %d, want 0 — Reset excludes the table, so only the purge removes it", n)
	}

	// Half B: the purge over tables Reset has just emptied must not error.
	seedBaseline(t, superDSN)
	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	res, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("PurgeDemoTenants over the tables Reset just emptied: %v", err)
	}

	var shared []string
	for _, tbl := range db.PurgeTablesForTest {
		if slices.Contains(resetTargetTables, tbl) {
			shared = append(shared, tbl)
		}
	}
	if len(shared) == 0 {
		t.Fatal("purgeTables and resetTables name no table in common — the assertion below would be vacuous")
	}
	for _, tbl := range shared {
		if n := res.ByTable[tbl]; n != 0 {
			t.Errorf("the purge deleted %d row(s) from %s, want 0 — Reset had already emptied it", n, tbl)
		}
	}
	// Paired positive: Reset excludes workflow_roles, so a zero here would mean
	// the purge did nothing at all rather than that Reset had done its job.
	if n := res.ByTable["workflow_roles"]; n == 0 {
		t.Error("the purge deleted 0 workflow_roles rows, want more than 0 — Reset excludes that table, so the seeded roles were there to delete")
	}
}

// ---- Source-scan: boot order and error posture -----------------------------

// provisionFuncDecl returns the package-level Provision declaration in src.
func provisionFuncDecl(t *testing.T, filename, src string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Provision" && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s declares no func Provision with a body — the scan below would prove nothing", filename)
	return nil
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// provisionCallOrder returns every call in Provision's body whose callee is in
// want, in source order.
func provisionCallOrder(t *testing.T, filename, src string, want []string) []string {
	t.Helper()
	type sited struct {
		pos  token.Pos
		name string
	}
	var found []sited
	ast.Inspect(provisionFuncDecl(t, filename, src).Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := calleeName(call.Fun); slices.Contains(want, name) {
			found = append(found, sited{call.Pos(), name})
		}
		return true
	})
	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	out := make([]string, 0, len(found))
	for _, f := range found {
		out = append(out, f.name)
	}
	return out
}

// TestProvisionPurgeOrderIsResetThenPurgeThenSeed (AC-1): the boot sequence is
// a property of the source, not of any one database's state. The control needle
// runs the same scanner over a fixture with the order swapped — a scanner that
// quietly stops matching reports nothing, which reads exactly like clean code.
func TestProvisionPurgeOrderIsResetThenPurgeThenSeed(t *testing.T) {
	const filename = "provision.go"
	want := []string{"Reset", "PurgeDemoTenants", "Seed"}

	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	got := provisionCallOrder(t, filename, string(src), want)
	if len(got) == 0 {
		t.Fatalf("%s: Provision calls none of %v — the scanner sees nothing, which reads exactly like a correct sequence", filename, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Provision's step order is %v, want %v", got, want)
	}

	t.Run("control needle", func(t *testing.T) {
		const fixture = `package p

func Provision() {
	Seed()
	PurgeDemoTenants()
	Reset()
}
`
		got := provisionCallOrder(t, "fixture.go", fixture, want)
		if len(got) != len(want) {
			t.Fatalf("the scanner found %d step call(s) in the fixture, want %d — it cannot see calls it should see", len(got), len(want))
		}
		if reflect.DeepEqual(got, want) {
			t.Fatalf("the scanner read the swapped fixture as %v — it cannot find a planted violation, so a clean report from it means nothing", got)
		}
	})
}

type provisionStep struct {
	name    string
	returns bool
}

// bodyReturns reports whether b returns directly, ignoring returns that belong
// to a nested function literal.
func bodyReturns(b *ast.BlockStmt) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		}
		return !found
	})
	return found
}

// errCallName returns the callee of an assignment that binds err, or "".
func errCallName(stmt ast.Stmt) string {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return ""
	}
	bindsErr := false
	for _, lhs := range assign.Lhs {
		if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "err" {
			bindsErr = true
		}
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !bindsErr || !ok {
		return ""
	}
	return calleeName(call.Fun)
}

func isErrNotNil(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	x, okX := bin.X.(*ast.Ident)
	y, okY := bin.Y.(*ast.Ident)
	return okX && okY && x.Name == "err" && y.Name == "nil"
}

// provisionErrorSteps returns every error-checked call in Provision's body,
// paired with whether its error branch returns. Two shapes are recognised:
// `if err := F(...); err != nil {` and an assignment binding err followed by
// `if err != nil {`. A shape it cannot read is reported as nothing, so a purge
// written some third way fails this scan rather than passing it.
func provisionErrorSteps(t *testing.T, filename, src string) []provisionStep {
	t.Helper()
	var out []provisionStep
	var walk func(list []ast.Stmt)
	walk = func(list []ast.Stmt) {
		for i, stmt := range list {
			ifStmt, ok := stmt.(*ast.IfStmt)
			if !ok {
				if block, ok := stmt.(*ast.BlockStmt); ok {
					walk(block.List)
				}
				continue
			}
			if isErrNotNil(ifStmt.Cond) {
				name := ""
				if ifStmt.Init != nil {
					name = errCallName(ifStmt.Init)
				} else if i > 0 {
					name = errCallName(list[i-1])
				}
				if name != "" {
					out = append(out, provisionStep{name: name, returns: bodyReturns(ifStmt.Body)})
				}
			}
			walk(ifStmt.Body.List)
			switch e := ifStmt.Else.(type) {
			case *ast.BlockStmt:
				walk(e.List)
			case *ast.IfStmt:
				walk([]ast.Stmt{e})
			}
		}
	}
	walk(provisionFuncDecl(t, filename, src).Body.List)
	return out
}

// TestProvisionPurgeIsTheOnlyNonFatalStep (AC-2): exactly one error-checked call
// in Provision does not abort the boot, and it is the purge. Pins the asymmetry
// in both directions — a fatal purge, and a sibling that copies its shape.
func TestProvisionPurgeIsTheOnlyNonFatalStep(t *testing.T) {
	const filename = "provision.go"
	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	steps := provisionErrorSteps(t, filename, string(src))
	if len(steps) == 0 {
		t.Fatalf("%s: Provision has no error-checked call the scanner can read — a clean report here means nothing", filename)
	}

	var nonFatal []string
	for _, s := range steps {
		if !s.returns {
			nonFatal = append(nonFatal, s.name)
		}
	}
	if !reflect.DeepEqual(nonFatal, []string{"PurgeDemoTenants"}) {
		t.Errorf("Provision's non-fatal error-checked calls are %v out of %d checked, want exactly [PurgeDemoTenants]", nonFatal, len(steps))
	}

	t.Run("control needle", func(t *testing.T) {
		const fixture = `package p

func Provision() error {
	if err := Bootstrap(); err != nil {
		return err
	}
	res, err := PurgeDemoTenants()
	if err != nil {
		_ = res
	}
	if err := Seed(); err != nil {
		_ = err
	}
	return nil
}
`
		steps := provisionErrorSteps(t, "fixture.go", fixture)
		if len(steps) != 3 {
			t.Fatalf("the scanner read %d error-checked call(s) in the fixture, want 3 — it cannot see calls it should see", len(steps))
		}
		var nonFatal []string
		for _, s := range steps {
			if !s.returns {
				nonFatal = append(nonFatal, s.name)
			}
		}
		want := []string{"PurgeDemoTenants", "Seed"}
		if !reflect.DeepEqual(nonFatal, want) {
			t.Fatalf("the scanner reported %v as non-fatal in the fixture, want %v — it cannot find a planted second non-fatal step", nonFatal, want)
		}
	})
}
