// Adversarial coverage for the demo-tenant purge: near-miss tenant ids, two
// boots racing, PurgeResult's treatment of tables that lost nothing, a
// superuser DSN that turns out not to be one, and a package-wide scan for
// unscoped deletes.
package db_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// nearMissTenantID differs from the demo tenant 11111111-... in its last digit.
const nearMissTenantID = "11111111-1111-1111-1111-111111111112"

// insufficientPrivilege is what a non-superuser gets for SET session_replication_role;
// invalidTextRepresentation is what a tenant id that is not a uuid raises.
const (
	insufficientPrivilege     = "42501"
	invalidTextRepresentation = "22P02"
)

// createTenantForTest inserts a throwaway tenant and removes it, with anything
// planted under it, on cleanup.
func createTenantForTest(t *testing.T, pool *pgxpool.Pool, id, name string) {
	t.Helper()
	execSetup(t, pool,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm') ON CONFLICT (id) DO NOTHING`,
		id, name)
	t.Cleanup(func() { dropTenantRows(t, pool, id) })
}

// demoRowTotal is the number of demo-tenant rows across every purged table.
func demoRowTotal(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	total := 0
	for _, tbl := range db.PurgeTablesForTest {
		total += countFor(t, pool, tbl, db.DemoTenants)
	}
	return total
}

// TestPurgeScopeHoldsAgainstNearMissTenantIDs: ANY($1) compares uuids, not
// text, so neither a one-character neighbour nor a differently-cased spelling
// is a way past the allowlist.
func TestPurgeScopeHoldsAgainstNearMissTenantIDs(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	t.Run("a tenant one character away from a demo id survives", func(t *testing.T) {
		reseedOnCleanup(t, superDSN)
		createTenantForTest(t, pool, nearMissTenantID, "Purge near-miss (test)")
		plantWitnessRows(t, pool, nearMissTenantID)

		before := map[string][]string{}
		for _, tbl := range db.PurgeTablesForTest {
			before[tbl] = snapshotTable(t, pool, tbl, nearMissTenantID)
			if len(before[tbl]) == 0 {
				t.Fatalf("test setup: the near-miss tenant holds no %s row, so its survival cannot be witnessed", tbl)
			}
		}

		if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
			t.Fatalf("PurgeDemoTenants: %v", err)
		}

		for _, tbl := range db.PurgeTablesForTest {
			if after := snapshotTable(t, pool, tbl, nearMissTenantID); !reflect.DeepEqual(before[tbl], after) {
				t.Errorf("%s rows of the tenant one character away from a demo id changed across the purge\nbefore: %v\nafter:  %v", tbl, before[tbl], after)
			}
		}
	})

	t.Run("an id that is only a prefix of a demo id is rejected, not matched", func(t *testing.T) {
		truncated := db.DemoTenants[0][:len(db.DemoTenants[0])-1]
		before := demoRowTotal(t, pool)
		if before == 0 {
			t.Fatal("test setup: no demo rows, so 'nothing was deleted' would hold vacuously")
		}

		conn, err := pgx.Connect(ctx, superDSN)
		if err != nil {
			t.Fatalf("test setup: connect: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		_, err = db.PurgeWithinForTest(ctx, tx, []string{truncated})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != invalidTextRepresentation {
			t.Fatalf("purgeWithin over a truncated tenant id returned %v, want SQLSTATE %s — a prefix must be rejected as a value the scope cannot bind, never quietly matched or quietly skipped", err, invalidTextRepresentation)
		}
		if after := demoRowTotal(t, pool); after != before {
			t.Errorf("demo rows = %d after the rejected purge, want %d", after, before)
		}
	})

	t.Run("an upper-cased allowlist still reaches the same demo rows", func(t *testing.T) {
		upper := make([]string, len(db.DemoTenants))
		for i, id := range db.DemoTenants {
			upper[i] = strings.ToUpper(id)
		}

		conn, err := pgx.Connect(ctx, superDSN)
		if err != nil {
			t.Fatalf("test setup: connect: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin: %v", err)
		}
		// Rolled back: the mutation must never reach disk.
		defer func() { _ = tx.Rollback(ctx) }()

		// A tenant id whose hex digits include letters: for the all-digit ids,
		// upper-casing is a no-op and would prove nothing.
		lettered := ""
		for _, id := range db.DemoTenants {
			if strings.ToUpper(id) != id {
				lettered = id
				break
			}
		}
		if lettered == "" {
			t.Fatal("no demo tenant id contains a hex letter, so upper-casing the allowlist changes nothing to observe")
		}
		// Planted inside the rolled-back tx: the seed leaves this tenant empty.
		execSetup(t, tx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,'purge-case-probe')`, lettered)

		countLettered := func() int {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM idempotency_keys WHERE tenant_id::text = $1`, lettered).Scan(&n); err != nil {
				t.Fatalf("count rows of %s: %v", lettered, err)
			}
			return n
		}
		if countLettered() == 0 {
			t.Fatal("test setup: the planted row is not there, so the case comparison proves nothing")
		}

		if _, err := db.PurgeWithinForTest(ctx, tx, upper); err != nil {
			t.Fatalf("purgeWithin over an upper-cased allowlist: %v", err)
		}
		if after := countLettered(); after != 0 {
			t.Errorf("an upper-cased allowlist left %d row(s) of %s, want 0 — the scope would be comparing text, so the four literals' casing silently decides what a deploy deletes", after, lettered)
		}
	})
}

// TestPurgeConcurrentBootsDeleteEachRowExactlyOnce: two gateways booting
// against one database run the purge at the same time. Either both commit or
// one gives up on lock_timeout, but the rows must be counted once in total and
// none may survive.
func TestPurgeConcurrentBootsDeleteEachRowExactlyOnce(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("test setup: Seed: %v", err)
	}
	plantWitnessRows(t, pool, demoTenantID)

	before := demoRowTotal(t, pool)
	if before == 0 {
		t.Fatal("test setup: no demo rows to race over")
	}

	results := make([]db.PurgeResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = db.PurgeDemoTenants(ctx, superDSN)
		}(i)
	}
	wg.Wait()

	// Concurrency may cost one purge its transaction; it may not cost it half.
	tolerated := map[string]bool{lockNotAvailable: true, "40001": true, "40P01": true}
	succeeded, deleted := 0, int64(0)
	for i, err := range errs {
		if err == nil {
			succeeded++
			deleted += results[i].Rows
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || !tolerated[pgErr.Code] {
			t.Fatalf("concurrent PurgeDemoTenants %d failed with %v — a racing boot may lose its lock, but any other failure means the purge is unsafe to run twice at once", i, err)
		}
	}
	if succeeded == 0 {
		t.Fatal("both concurrent purges failed — one boot must be able to complete")
	}
	if deleted != int64(before) {
		t.Errorf("the concurrent purges reported %d row(s) deleted in total, want %d — a row counted twice means two transactions each believed they removed it", deleted, before)
	}
	for _, tbl := range db.PurgeTablesForTest {
		if n := countFor(t, pool, tbl, db.DemoTenants); n != 0 {
			t.Errorf("%s still holds %d demo row(s) after two concurrent purges, want 0", tbl, n)
		}
	}
}

// TestPurgeResultOmitsTablesThatLostNothing pins the reporting contract: a
// table that lost no rows is absent from ByTable rather than present with 0,
// and Rows is exactly the sum of what is present.
func TestPurgeResultOmitsTablesThatLostNothing(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("test setup: Seed: %v", err)
	}

	before := map[string]int{}
	var empty, populated []string
	for _, tbl := range db.PurgeTablesForTest {
		before[tbl] = countFor(t, pool, tbl, db.DemoTenants)
		if before[tbl] == 0 {
			empty = append(empty, tbl)
		} else {
			populated = append(populated, tbl)
		}
	}
	if len(empty) == 0 {
		t.Fatal("test setup: every purged table holds demo rows, so 'a table that lost nothing is omitted' has no case to prove")
	}
	if len(populated) == 0 {
		t.Fatal("test setup: no purged table holds demo rows, so the counts below would all be zero")
	}

	res, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}

	for _, tbl := range empty {
		if n, ok := res.ByTable[tbl]; ok {
			t.Errorf("ByTable names %s with %d — a table that lost no rows must be absent, not reported as zero", tbl, n)
		}
	}
	for _, tbl := range populated {
		if got := res.ByTable[tbl]; got != int64(before[tbl]) {
			t.Errorf("ByTable[%s] = %d, want %d (the rows it held)", tbl, got, before[tbl])
		}
	}
	var sum int64
	for tbl, n := range res.ByTable {
		if n <= 0 {
			t.Errorf("ByTable[%s] = %d, want a positive count", tbl, n)
		}
		if before[tbl] == 0 {
			t.Errorf("ByTable names %s, which is no purged table that held rows", tbl)
		}
		sum += n
	}
	if res.Rows != sum {
		t.Errorf("PurgeResult.Rows = %d, want %d (the sum of ByTable)", res.Rows, sum)
	}
	if res.Duration <= 0 {
		t.Errorf("PurgeResult.Duration = %v, want a measured duration", res.Duration)
	}
}

// nonSuperuserDSN creates a throwaway LOGIN role with full table privileges but
// no superuser attribute, and returns a DSN for it built from superDSN.
func nonSuperuserDSN(t *testing.T, pool *pgxpool.Pool, superDSN string) string {
	t.Helper()
	const role = "purge_not_superuser_test"
	const pw = "purge_not_superuser_test"

	drop := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DROP OWNED BY `+role)
		_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+role)
	}
	drop()
	execSetup(t, pool, `CREATE ROLE `+role+` LOGIN PASSWORD '`+pw+`'`)
	t.Cleanup(drop)
	execSetup(t, pool, `GRANT USAGE ON SCHEMA public TO `+role)
	execSetup(t, pool, `GRANT ALL ON ALL TABLES IN SCHEMA public TO `+role)

	var isSuper bool
	if err := pool.QueryRow(context.Background(),
		`SELECT rolsuper FROM pg_roles WHERE rolname = $1`, role).Scan(&isSuper); err != nil {
		t.Fatalf("test setup: read %s attributes: %v", role, err)
	}
	if isSuper {
		t.Fatalf("test setup: %s is a superuser, so it cannot stand in for a DSN that is not one", role)
	}

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("test setup: parse superuser dsn: %v", err)
	}
	u.User = url.UserPassword(role, pw)
	return u.String()
}

// TestPurgeWithoutSuperuserRollsBackCompletely: the production DSN named
// "superuser" has never been exercised with session_replication_role, which is
// superuser-only. If it turns out not to be one, the purge must fail whole —
// no table half-emptied.
func TestPurgeWithoutSuperuserRollsBackCompletely(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	plantWitnessRows(t, pool, demoTenantID)

	before := map[string]int{}
	for _, tbl := range db.PurgeTablesForTest {
		before[tbl] = countFor(t, pool, tbl, db.DemoTenants)
		if before[tbl] == 0 {
			t.Fatalf("test setup: %s holds no demo rows, so 'it was not partially purged' holds vacuously", tbl)
		}
	}

	_, err := db.PurgeDemoTenants(ctx, nonSuperuserDSN(t, pool, superDSN))
	if err == nil {
		t.Fatal("PurgeDemoTenants succeeded on a connection with no superuser attribute — session_replication_role is superuser-only, so it cannot have opened the audit_log bypass")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != insufficientPrivilege {
		t.Errorf("PurgeDemoTenants failed with %v, want SQLSTATE %s — the failure must name the missing privilege, not read to an operator as something transient", err, insufficientPrivilege)
	}
	for _, tbl := range db.PurgeTablesForTest {
		if after := countFor(t, pool, tbl, db.DemoTenants); after != before[tbl] {
			t.Errorf("%s holds %d demo row(s) after the failed purge, want %d — the transaction did not roll back whole", tbl, after, before[tbl])
		}
	}
}

// deleteStmtRE matches DELETE ... FROM across any whitespace, so a literal that
// wraps the keywords over two lines cannot slip past the scan.
var deleteStmtRE = regexp.MustCompile(`(?is)\bdelete\b\s+\bfrom\b`)

// stringLiteralsIn returns every string literal in src, parsed rather than
// grepped so a mention inside a comment is never counted.
func stringLiteralsIn(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, v)
		}
		return true
	})
	return out
}

// deleteLiteralsMatching filters stringLiteralsIn down to the literals re matches.
func deleteLiteralsMatching(t *testing.T, filename, src string, re *regexp.Regexp) []string {
	t.Helper()
	var out []string
	for _, lit := range stringLiteralsIn(t, filename, src) {
		if re.MatchString(lit) {
			out = append(out, lit)
		}
	}
	return out
}

// packageDeleteLiterals returns every DELETE literal in the package's non-test
// sources, keyed by file.
func packageDeleteLiterals(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	out := map[string][]string{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		if lits := deleteLiteralsMatching(t, name, string(src), deleteStmtRE); len(lits) > 0 {
			out[name] = lits
		}
	}
	if scanned == 0 {
		t.Fatal("scanned 0 non-test source files — the walk found nothing, which reads exactly like a clean package")
	}
	return out
}

// TestPurgePackageEmitsNoUnscopedDeleteStatement widens obligation 2 from
// demopurge.go to every non-test source in the package (AC-10 says "every
// DELETE the package can emit") and tolerates whitespace between the keywords.
// A delete added here that is legitimately not tenant-scoped is a decision this
// test is the place to record.
func TestPurgePackageEmitsNoUnscopedDeleteStatement(t *testing.T) {
	byFile := packageDeleteLiterals(t)

	total := 0
	var files []string
	for f, lits := range byFile {
		total += len(lits)
		files = append(files, f)
	}
	sort.Strings(files)
	if total == 0 {
		t.Errorf("no source file in this package holds a DELETE literal — the purge emits no statement at all, so scanning proves nothing")
	}
	for _, f := range files {
		for _, lit := range byFile[f] {
			if !strings.Contains(lit, tenantPredicate) {
				t.Errorf("%s holds an unscoped DELETE literal %q — every statement this package can emit must carry %q in the same literal", f, lit, tenantPredicate)
			}
		}
	}

	t.Run("control needle spanning a line break", func(t *testing.T) {
		fixture := "package p\n\nconst wrapped = `DELETE\nFROM invoices`\n"
		got := deleteLiteralsMatching(t, "fixture.go", fixture, deleteStmtRE)
		if len(got) != 1 {
			t.Fatalf("the whitespace-tolerant scan found %d DELETE literal(s) in the fixture, want 1 — it cannot find a planted violation, so a clean report from it means nothing", len(got))
		}
		// The narrower scan misses this shape; that gap is why this test exists.
		if narrow := deleteLiteralsIn(t, "fixture.go", fixture); len(narrow) != 0 {
			t.Fatalf("deleteLiteralsIn now sees a line-wrapped DELETE (%v) — fold this case back into TestPurgeHasNoUnscopedDeleteStatement and drop the duplication", narrow)
		}
	})
}
