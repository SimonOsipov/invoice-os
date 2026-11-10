// Tests for public.public_check_ai_cost_today(), the check's AI spend for the UTC day (SECAUD-05-05).
// Reads run as invoice_app inside a rolled-back transaction; superuser seeds are deleted by id.
package db_test

import (
	"context"
	"io/fs"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	publicSpendFn       = "public.public_check_ai_cost_today()"
	publicSpendPolicy   = "public_check_spend"
	publicSpendIndex    = "ai_calls_public_check_day"
	publicSpendMidnight = `date_trunc('day', now(), 'UTC')`
)

// publicSpendReadSQL yields NULL as the text 'NULL' so a NULL result fails the parse, not the scan.
const publicSpendReadSQL = `SELECT coalesce(` + publicSpendFn + `::text, 'NULL')`

type spendQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	aiExecer
}

func publicSpend(t *testing.T, q spendQuerier) *big.Rat {
	t.Helper()
	var s string
	if err := q.QueryRow(context.Background(), publicSpendReadSQL).Scan(&s); err != nil {
		t.Fatalf("read %s: %v", publicSpendFn, err)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("%s = %q, want a number (0 on an empty day, never NULL)", publicSpendFn, s)
	}
	return r
}

func ratOf(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad number %q", s)
	}
	return r
}

// wantSpendDelta fails unless now-before is exactly want (numeric text, no float).
func wantSpendDelta(t *testing.T, what string, now, before *big.Rat, want string) {
	t.Helper()
	if d := new(big.Rat).Sub(now, before); d.Cmp(ratOf(t, want)) != 0 {
		t.Errorf("%s: spend moved by %s, want exactly %s", what, d.FloatString(6), want)
	}
}

// publicSpendInsert writes one NULL-tenant row; at is a SQL timestamptz expression, cost numeric text.
func publicSpendInsert(t *testing.T, e aiExecer, cost, at string) {
	t.Helper()
	if _, err := e.Exec(context.Background(),
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at)
		 VALUES (NULL, 'ai', 'public-check', 'test/model', 10, 5, $1::text::numeric, 120, 1, 'ok', `+at+`)`, cost); err != nil {
		t.Fatalf("INSERT of a NULL-tenant row (cost %s at %s): %v", cost, at, err)
	}
}

// publicSpendSeed commits one row as the superuser (tenant "" is NULL) and deletes it at test end.
func publicSpendSeed(t *testing.T, tenant, cost string) {
	t.Helper()
	id := uuid.NewString()
	deleteAICallOnCleanup(t, id)
	var tid any
	if tenant != "" {
		tid = tenant
	}
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO ai_calls (id, tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
		 VALUES ($1, $2, 'ai', 'public-check', 'test/model', 10, 5, $3::text::numeric, 120, 1, 'ok')`, id, tid, cost); err != nil {
		t.Fatalf("seed an ai_calls row (tenant %q, cost %s): %v", tenant, cost, err)
	}
}

func TestRLS_PublicCheckAICostTodaySumsTodaysPublicRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	before := publicSpend(t, tx)
	publicSpendInsert(t, tx, "0.25", "now()")
	publicSpendInsert(t, tx, "0.5", "now()")
	wantSpendDelta(t, "two rows of 0.25 and 0.5 today", publicSpend(t, tx), before, "0.75")

	// Pairs the sum with a refusal: the rows the function counts stay unreadable to the app role.
	if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE tenant_id IS NULL`); n != 0 {
		t.Errorf("invoice_app reads %d NULL-tenant rows directly, want 0 (only the function sums them)", n)
	}
}

func TestRLS_PublicCheckAICostTodayIsZeroWithNoRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM ai_calls WHERE tenant_id IS NULL AND created_at >= `+publicSpendMidnight); err != nil {
		t.Fatalf("clear today's NULL-tenant rows: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_app`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_app: %v", err)
	}
	if got := publicSpend(t, tx); got.Sign() != 0 {
		t.Errorf("%s on an empty day = %s, want 0", publicSpendFn, got.FloatString(6))
	}
}

func TestRLS_PublicCheckAICostTodayStartsAtUTCMidnight(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	// UTC+14: the session's own day starts hours before the UTC one.
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Pacific/Kiritimati'`); err != nil {
		t.Fatalf("set time zone: %v", err)
	}

	before := publicSpend(t, tx)
	publicSpendInsert(t, tx, "7", publicSpendMidnight+` - interval '1 microsecond'`)
	wantSpendDelta(t, "a row 1 microsecond before 00:00 UTC", publicSpend(t, tx), before, "0")
	publicSpendInsert(t, tx, "3", publicSpendMidnight)
	wantSpendDelta(t, "plus a row at exactly 00:00 UTC", publicSpend(t, tx), before, "3")
}

func TestRLS_PublicCheckAICostTodayIgnoresTenantRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]
	publicSpendSeed(t, t1, "100")

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	got := publicSpend(t, tx)
	publicSpendInsert(t, tx, "0.25", "now()")
	// The positive half: a NULL row moves the sum, so the tenant's 100 is ignored, not the whole read.
	wantSpendDelta(t, "a tenant row of 100 beside a NULL row of 0.25", publicSpend(t, tx), got, "0.25")
	if got.Cmp(ratOf(t, "100")) >= 0 {
		t.Errorf("%s = %s with a tenant row of 100 today, want it to leave tenant rows out", publicSpendFn, got.FloatString(6))
	}
}

func TestRLS_PublicCheckAICostTodayIsTheSameInsideATenantTx(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	before := publicSpend(t, h.app)
	publicSpendSeed(t, "", "0.4")
	publicSpendSeed(t, t1, "9")

	noTenant := publicSpend(t, h.app)
	wantSpendDelta(t, "a committed NULL row of 0.4", noTenant, before, "0.4")
	if err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		if inTenant := publicSpend(t, tx); inTenant.Cmp(noTenant) != 0 {
			t.Errorf("spend inside the tenant tx = %s, want %s (the no-tenant read)", inTenant.FloatString(6), noTenant.FloatString(6))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

func TestRLS_PublicCheckAICostTodayGrantsAndOwner(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var owner, retType string
	var secdef bool
	var config []string
	if err := h.super.QueryRow(ctx,
		`SELECT pg_get_userbyid(proowner), prosecdef, coalesce(proconfig, '{}'), prorettype::regtype::text
		   FROM pg_proc WHERE oid = to_regprocedure($1)`, publicSpendFn).Scan(&owner, &secdef, &config, &retType); err != nil {
		t.Fatalf("read pg_proc for %s (function absent?): %v", publicSpendFn, err)
	}
	if owner != "auth_hook_reader" || !secdef || retType != "numeric" || !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("%s: owner=%q secdef=%v returns=%q config=%v, want auth_hook_reader, true, numeric, [search_path=\"\"]",
			publicSpendFn, owner, secdef, retType, config)
	}

	var publicExec bool
	if err := h.super.QueryRow(ctx,
		`SELECT p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
		   FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, publicSpendFn).Scan(&publicExec); err != nil {
		t.Fatalf("read the ACL of %s: %v", publicSpendFn, err)
	}
	if publicExec {
		t.Errorf("PUBLIC can EXECUTE %s, want it revoked", publicSpendFn)
	}
	if !mustBool(t, `SELECT has_function_privilege('invoice_app', to_regprocedure($1), 'EXECUTE')`, publicSpendFn) {
		t.Errorf("invoice_app cannot EXECUTE %s, want it granted", publicSpendFn)
	}
}

func mustBool(t *testing.T, sql string, args ...any) bool {
	t.Helper()
	var b bool
	if err := h.super.QueryRow(context.Background(), sql, args...).Scan(&b); err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	return b
}

func TestRLS_PublicCheckAICostTodayRefusedToOtherRoles(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// Positive half: the same call works as invoice_app, so a refusal below is the grant.
	_ = publicSpend(t, h.app)
	for _, c := range []struct {
		who  string
		pool spendQuerier
	}{{"invoice_tenant_reader", h.reader}, {"invoice_migrator", h.mig}} {
		var s string
		if err := c.pool.QueryRow(ctx, publicSpendReadSQL).Scan(&s); pgCode(err) != "42501" {
			t.Errorf("%s calling %s = %q, %v; want SQLSTATE 42501", c.who, publicSpendFn, s, err)
		}
	}
	for _, role := range []string{"staff_reader", "invoice_gateway"} {
		if mustBool(t, `SELECT has_function_privilege($1, to_regprocedure($2), 'EXECUTE')`, role, publicSpendFn) {
			t.Errorf("%s can EXECUTE %s, want invoice_app only", role, publicSpendFn)
		}
	}
}

func TestRLS_AuthHookReaderSeesNoTenantAICallsRow(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]
	publicSpendSeed(t, t1, "1")
	publicSpendSeed(t, "", "1")

	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
		t.Fatalf("SET LOCAL ROLE auth_hook_reader: %v", err)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE tenant_id IS NOT NULL`); n != 0 {
		t.Errorf("auth_hook_reader sees %d tenant rows of ai_calls, want 0", n)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE tenant_id IS NULL`); n < 1 {
		t.Errorf("auth_hook_reader sees %d NULL-tenant rows, want at least the seeded one", n)
	}
}

func TestRLS_AuthHookReaderReadsOnlyTheSpendColumnsOfAICalls(t *testing.T) {
	requireHarness(t)
	got := collectStrings(t,
		`SELECT column_name || ':' || privilege_type FROM information_schema.column_privileges
		  WHERE grantee = 'auth_hook_reader' AND table_schema = 'public' AND table_name = 'ai_calls' ORDER BY 1`)
	want := []string{"cost:SELECT", "created_at:SELECT", "tenant_id:SELECT"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("auth_hook_reader column privileges on ai_calls = %v, want exactly %v", got, want)
	}
}

type publicSpendFootprint struct {
	function, policies, indexes, hookColumnGrants int
	insertPolicies                                int
}

func readPublicSpendFootprint(t *testing.T) publicSpendFootprint {
	t.Helper()
	var f publicSpendFootprint
	if err := h.super.QueryRow(context.Background(), `
		SELECT (to_regprocedure($1) IS NOT NULL)::int,
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'ai_calls' AND policyname = $2)::int,
		       (SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'ai_calls' AND indexname = $3)::int,
		       (SELECT count(*) FROM information_schema.column_privileges
		         WHERE grantee = 'auth_hook_reader' AND table_schema = 'public' AND table_name = 'ai_calls')::int,
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'ai_calls' AND policyname = 'public_check_insert')::int`,
		publicSpendFn, publicSpendPolicy, publicSpendIndex).
		Scan(&f.function, &f.policies, &f.indexes, &f.hookColumnGrants, &f.insertPolicies); err != nil {
		t.Fatalf("read the public spend footprint: %v", err)
	}
	return f
}

// publicSpendMigrationVersion is the one migration that creates the function.
func publicSpendMigrationVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	var found []string
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), "CREATE FUNCTION public.public_check_ai_cost_today(") {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		t.Fatalf("migrations.FS holds %d migrations with CREATE FUNCTION public.public_check_ai_cost_today (%v), want exactly 1", len(found), found)
	}
	v, err := strconv.ParseInt(strings.SplitN(found[0], "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parse goose version out of %q: %v", found[0], err)
	}
	return v
}

func TestRLS_PublicCheckAICostTodayDownAndUp(t *testing.T) {
	requireHarness(t)
	version := publicSpendMigrationVersion(t)

	want := publicSpendFootprint{function: 1, policies: 1, indexes: 1, hookColumnGrants: 3, insertPolicies: 1}
	if before := readPublicSpendFootprint(t); before != want {
		t.Fatalf("before the Down: footprint = %+v, want %+v", before, want)
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), version)
	// public_check_insert belongs to the earlier migration and stays.
	if after, wantDown := readPublicSpendFootprint(t), (publicSpendFootprint{insertPolicies: 1}); after != wantDown {
		t.Errorf("after the Down: footprint = %+v, want %+v (function, policy, index and grant removed)", after, wantDown)
	}

	reapply()
	if again := readPublicSpendFootprint(t); again != want {
		t.Errorf("after the Up: footprint = %+v, want %+v", again, want)
	}
}
