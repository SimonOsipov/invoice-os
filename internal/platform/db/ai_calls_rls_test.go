// Tests for `ai_calls`, the append-only per-tenant record of AI calls (SUPP-09-02).
// Tenants and rows are seeded per test through the superuser pool, never in harness.seed(),
// so a missing table fails only these cases. Run with `make test-rls`.
package db_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type aiExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// aiRow is one ai_calls insert; cost is numeric text, cast server-side.
type aiRow struct {
	client, purpose, model string
	inTokens, outTokens    int64
	cost                   string
	latencyMS              int64
	attempts               int
	outcome                string
}

func aiValidRow() aiRow {
	return aiRow{
		client: "ai", purpose: "extract", model: "test/model",
		inTokens: 10, outTokens: 5, cost: "0.001", latencyMS: 120, attempts: 1, outcome: "ok",
	}
}

const aiInsertSQL = `INSERT INTO ai_calls
	(tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
	VALUES ($1, $2, $3, $4, $5, $6, $7::text::numeric, $8, $9, $10)`

func aiInsert(ctx context.Context, e aiExecer, tenantID string, r aiRow) error {
	_, err := e.Exec(ctx, aiInsertSQL, tenantID, r.client, r.purpose, r.model,
		r.inTokens, r.outTokens, r.cost, r.latencyMS, r.attempts, r.outcome)
	return err
}

func failIfUndefinedAICalls(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) - the ai_calls migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// aiTenants seeds n throwaway tenants and removes them (and their rows) at test end.
func aiTenants(t *testing.T, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
		if _, err := h.super.Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, 'ai_calls throwaway tenant')`, ids[i]); err != nil {
			t.Fatalf("seed throwaway tenant: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE tenant_id = $1`, id)
			_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
		}
	})
	return ids
}

// seedAICall inserts one valid row as the superuser and returns its id.
func seedAICall(t *testing.T, tenantID string) string {
	t.Helper()
	r := aiValidRow()
	var id string
	err := h.super.QueryRow(context.Background(),
		aiInsertSQL+` RETURNING id::text`, tenantID, r.client, r.purpose, r.model,
		r.inTokens, r.outTokens, r.cost, r.latencyMS, r.attempts, r.outcome).Scan(&id)
	if failIfUndefinedAICalls(t, "seed ai_calls", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed ai_calls: %v", err)
	}
	return id
}

func TestRLS_AICallsColumnsAreExactlyTheRecordFields(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'ai_calls'`)
	if err != nil {
		t.Fatalf("read information_schema.columns: %v", err)
	}
	defer rows.Close()
	types := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		types[name] = typ
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if len(types) == 0 {
		t.Fatal("information_schema lists no columns for ai_calls - the migration is not applied yet")
	}

	want := []string{"id", "tenant_id", "client", "purpose", "model", "input_tokens", "output_tokens",
		"cost", "latency_ms", "attempts", "outcome", "created_at"}
	got := make([]string, 0, len(types))
	for name := range types {
		got = append(got, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ai_calls columns = %v, want exactly %v (no content column)", got, want)
	}
	for col, typ := range map[string]string{
		"cost": "numeric", "latency_ms": "bigint", "input_tokens": "bigint", "output_tokens": "bigint",
		"id": "uuid", "tenant_id": "uuid", "client": "text", "purpose": "text", "model": "text",
		"outcome": "text", "attempts": "integer", "created_at": "timestamp with time zone",
	} {
		if types[col] != typ {
			t.Errorf("ai_calls.%s data_type = %q, want %q", col, types[col], typ)
		}
	}
}

func TestRLS_AICallsTokensAboveInt32AreStored(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	var in, out int64
	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
			 VALUES ($1, 'ai', 'extract', 'm', 3000000000, 3000000000, 0, 1, 1, 'ok')
			 RETURNING input_tokens, output_tokens`, t1).Scan(&in, &out)
	})
	if failIfUndefinedAICalls(t, "INSERT tokens above int32", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT tokens above int32: %v", err)
	}
	if in != 3000000000 || out != 3000000000 {
		t.Errorf("tokens read back = %d/%d, want 3000000000/3000000000", in, out)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t1); n != 1 {
		t.Errorf("rows for the tenant = %d, want 1", n)
	}
}

func TestRLS_AICallsTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	id := seedAICall(t, t1)
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE id = $1`, id); n != 1 {
		t.Fatalf("seeded rows before the tenant delete = %d, want 1", n)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, t1); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE id = $1`, id); n != 0 {
		t.Errorf("rows after the tenant delete = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

func TestRLS_AICallsUnknownTenantRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ghost := uuid.NewString()
	err := db.WithinTenantTx(ctx, h.app, ghost, func(tx pgx.Tx) error {
		return aiInsert(ctx, tx, ghost, aiValidRow())
	})
	if failIfUndefinedAICalls(t, "INSERT for an unknown tenant", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT for a tenant not in tenants succeeded, want foreign_key_violation (23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("INSERT for an unknown tenant: SQLSTATE = %q, want 23503: %v", code, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, ghost); n != 0 {
		t.Errorf("rows for the unknown tenant = %d, want 0", n)
	}
}

func TestRLS_AICallsRowSecurityEnabledAndForced(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class
		  WHERE oid = to_regclass('public.ai_calls')`).Scan(&enabled, &forced)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("pg_class has no ai_calls - the migration is not applied yet")
	}
	if err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled {
		t.Error("relrowsecurity = false, want true")
	}
	if !forced {
		t.Error("relforcerowsecurity = false, want true (the owner must be bound too)")
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_policies WHERE tablename = 'ai_calls' AND policyname = 'tenant_isolation'`); n != 1 {
		t.Errorf("tenant_isolation policies on ai_calls = %d, want 1", n)
	}
}

func TestRLS_AICallsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	ts := aiTenants(t, 2)
	t1, t2 := ts[0], ts[1]

	own := seedAICall(t, t1)
	seedAICall(t, t2)
	seedAICall(t, t2)

	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE id = $1`, own); n != 1 {
			t.Errorf("own row visible to T1 = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t2); n != 0 {
			t.Errorf("T2 rows visible to T1 = %d, want 0", n)
		}
		// Only RLS narrows this one: T2 holds two more rows.
		if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls`); n != 1 {
			t.Errorf("unfiltered count under T1 = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

func TestRLS_AICallsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	ts := aiTenants(t, 2)
	t1, t2 := ts[0], ts[1]

	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		return aiInsert(ctx, tx, t2, aiValidRow())
	})
	if failIfUndefinedAICalls(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t2); n != 0 {
		t.Errorf("T2 rows after the refused INSERT = %d, want 0", n)
	}

	// Positive half: the same statement shape succeeds for T1's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		return aiInsert(ctx, tx, t1, aiValidRow())
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: %v", err)
	}
}

func TestRLS_AICallsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]
	id := seedAICall(t, t1)

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls`); n != 0 {
		t.Errorf("ai_calls visible with no tenant set = %d, want 0", n)
	}
	assertRLSViolation(t, aiInsert(ctx, tx, t1, aiValidRow()))

	// A connection that never set the GUC (current_setting missing_ok) and one that set it to ''.
	fresh, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("fresh app connection: %v", err)
	}
	defer fresh.Close(ctx)
	if n := mustCount(t, fresh, `SELECT count(*) FROM ai_calls`); n != 0 {
		t.Errorf("ai_calls visible on a fresh connection with no tenant = %d, want 0", n)
	}
	assertRLSViolation(t, aiInsert(ctx, fresh, t1, aiValidRow()))
	etx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer etx.Rollback(ctx)
	if _, err := etx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
		t.Fatalf("set empty tenant: %v", err)
	}
	if n := mustCount(t, etx, `SELECT count(*) FROM ai_calls`); n != 0 {
		t.Errorf("ai_calls visible with an empty tenant = %d, want 0", n)
	}

	// The zero above comes from the missing GUC, not an empty table.
	if err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE id = $1`, id); n != 1 {
			t.Errorf("seeded row visible WITH tenant context = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (positive half): %v", err)
	}
}

func TestRLS_AICallsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	ts := aiTenants(t, 2)
	t1, t2 := ts[0], ts[1]

	err := db.WithinTenantTx(ctx, h.mig, t1, func(tx pgx.Tx) error {
		return aiInsert(ctx, tx, t2, aiValidRow())
	})
	if failIfUndefinedAICalls(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t2); n != 0 {
		t.Errorf("T2 rows after the owner's refused INSERT = %d, want 0", n)
	}

	// Positive half: the owner can write in its own tenant scope.
	if err := db.WithinTenantTx(ctx, h.mig, t1, func(tx pgx.Tx) error {
		return aiInsert(ctx, tx, t1, aiValidRow())
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: %v", err)
	}
}

func TestRLS_AICallsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	ts := aiTenants(t, 2)
	t1, t2 := ts[0], ts[1]
	id := seedAICall(t, t1)

	err := db.WithinTenantTx(ctx, h.mig, t1, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE ai_calls SET tenant_id = $1 WHERE id = $2`, t2, id)
		return e
	})
	if failIfUndefinedAICalls(t, "own-row tenant reassignment", err) {
		return
	}
	assertRLSViolation(t, err)

	var still string
	if err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM ai_calls WHERE id = $1`, id).Scan(&still); err != nil {
		t.Fatalf("read back tenant_id: %v", err)
	}
	if still != t1 {
		t.Errorf("tenant_id after the refused reassignment = %q, want %q", still, t1)
	}

	// Positive half: the owner can update the same row in-tenant, so the 42501 is the policy.
	if err := db.WithinTenantTx(ctx, h.mig, t1, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE ai_calls SET outcome = 'changed' WHERE id = $1`, id)
		if e == nil && ct.RowsAffected() != 1 {
			t.Errorf("in-tenant UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return e
	}); err != nil {
		t.Fatalf("in-tenant UPDATE by the owner: %v", err)
	}
}

func TestRLS_AICallsOwnTenantInsertSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	want := aiValidRow()
	var id string
	var got aiRow
	var createdAt time.Time
	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		if e := aiInsert(ctx, tx, t1, want); e != nil {
			return e
		}
		return tx.QueryRow(ctx,
			`SELECT id::text, created_at, client, purpose, model, input_tokens, output_tokens,
			        cost::text, latency_ms, attempts, outcome
			   FROM ai_calls WHERE tenant_id = $1`, t1,
		).Scan(&id, &createdAt, &got.client, &got.purpose, &got.model, &got.inTokens, &got.outTokens,
			&got.cost, &got.latencyMS, &got.attempts, &got.outcome)
	})
	if failIfUndefinedAICalls(t, "own-tenant INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("own-tenant INSERT and read-back: %v", err)
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Errorf("id default = %q, want a uuid", id)
	}
	if createdAt.IsZero() || time.Since(createdAt).Abs() > 5*time.Minute {
		t.Errorf("created_at default = %v, want about now", createdAt)
	}
	if got != want {
		t.Errorf("row read back = %+v, want %+v", got, want)
	}
}

func TestRLS_AICallsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cases := []struct {
		role, priv string
		want       bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
	}
	if len(cases) == 0 {
		t.Fatal("no grant cases")
	}
	for _, c := range cases {
		var got bool
		err := h.super.QueryRow(ctx, `SELECT has_table_privilege($1, 'public.ai_calls', $2)`, c.role, c.priv).Scan(&got)
		if failIfUndefinedAICalls(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%s, ai_calls, %s) = %v, want %v", c.role, c.priv, got, c.want)
		}
	}
}

func TestRLS_AICallsAppUpdateAndDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]
	id := seedAICall(t, t1)

	for _, c := range []struct{ name, sql string }{
		{"UPDATE", `UPDATE ai_calls SET outcome = 'changed' WHERE id = $1`},
		{"DELETE", `DELETE FROM ai_calls WHERE id = $1`},
	} {
		err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql, id)
			return e
		})
		if err == nil {
			t.Errorf("app-role %s on ai_calls succeeded, want permission denied (42501)", c.name)
			continue
		}
		if code := pgCode(err); code != "42501" || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("app-role %s: got %v, want SQLSTATE 42501 permission denied (a missing grant, not RLS)", c.name, err)
		}
	}

	var outcome string
	if err := h.super.QueryRow(ctx, `SELECT outcome FROM ai_calls WHERE id = $1`, id).Scan(&outcome); err != nil {
		t.Fatalf("row must survive the refused UPDATE and DELETE: %v", err)
	}
	if outcome != "ok" {
		t.Errorf("outcome after the refused UPDATE = %q, want unchanged %q", outcome, "ok")
	}
}

func TestRLS_AICallsReaderSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]
	id := seedAICall(t, t1)

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM ai_calls`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on ai_calls succeeded, want permission denied (42501)")
	}
	if code := pgCode(err); code != "42501" || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("reader SELECT: got %v, want SQLSTATE 42501 permission denied", err)
	}

	// Positive half: the app role reads the same row.
	if err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM ai_calls WHERE id = $1`, id); got != 1 {
			t.Errorf("app-role SELECT of the seeded row = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("app-role SELECT: %v", err)
	}
}

func TestRLS_AICallsChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	mod := func(f func(*aiRow)) aiRow { r := aiValidRow(); f(&r); return r }
	reject := []struct {
		name string
		col  string // the CHECK that must refuse, named after its column
		row  aiRow
	}{
		{"client x", "client", mod(func(r *aiRow) { r.client = "x" })},
		{"purpose empty", "purpose", mod(func(r *aiRow) { r.purpose = "" })},
		{"purpose 65 chars", "purpose", mod(func(r *aiRow) { r.purpose = strings.Repeat("p", 65) })},
		{"outcome empty", "outcome", mod(func(r *aiRow) { r.outcome = "" })},
		{"outcome 65 chars", "outcome", mod(func(r *aiRow) { r.outcome = strings.Repeat("o", 65) })},
		{"model 129 chars", "model", mod(func(r *aiRow) { r.model = strings.Repeat("m", 129) })},
		{"model 129 multibyte chars", "model", mod(func(r *aiRow) { r.model = strings.Repeat("é", 129) })},
		{"input_tokens -1", "input_tokens", mod(func(r *aiRow) { r.inTokens = -1 })},
		{"output_tokens -1", "output_tokens", mod(func(r *aiRow) { r.outTokens = -1 })},
		{"cost -0.01", "cost", mod(func(r *aiRow) { r.cost = "-0.01" })},
		{"latency_ms -1", "latency_ms", mod(func(r *aiRow) { r.latencyMS = -1 })},
		{"attempts 0", "attempts", mod(func(r *aiRow) { r.attempts = 0 })},
	}
	accept := []struct {
		name string
		row  aiRow
	}{
		{"client jev", mod(func(r *aiRow) { r.client = "jev" })},
		{"purpose 64 chars", mod(func(r *aiRow) { r.purpose = strings.Repeat("p", 64) })},
		{"outcome 64 chars", mod(func(r *aiRow) { r.outcome = strings.Repeat("o", 64) })},
		{"model 128 chars", mod(func(r *aiRow) { r.model = strings.Repeat("m", 128) })},
		{"model empty", mod(func(r *aiRow) { r.model = "" })},
		{"model 128 multibyte chars", mod(func(r *aiRow) { r.model = strings.Repeat("é", 128) })},
		{"purpose 64 multibyte chars", mod(func(r *aiRow) { r.purpose = strings.Repeat("é", 64) })},
		{"attempts 1", mod(func(r *aiRow) { r.attempts = 1 })},
		{"cost 0", mod(func(r *aiRow) { r.cost = "0" })},
		{"input_tokens 0", mod(func(r *aiRow) { r.inTokens = 0 })},
		{"output_tokens 0", mod(func(r *aiRow) { r.outTokens = 0 })},
		{"latency_ms 0", mod(func(r *aiRow) { r.latencyMS = 0 })},
	}

	for _, c := range reject {
		err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error { return aiInsert(ctx, tx, t1, c.row) })
		if failIfUndefinedAICalls(t, "INSERT "+c.name, err) {
			return
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want check_violation (23514)", c.name)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514: %v", c.name, code, err)
		} else if !strings.Contains(pgConstraint(err), c.col) {
			t.Errorf("INSERT with %s: constraint = %q, want one naming %q", c.name, pgConstraint(err), c.col)
		}
	}
	for _, c := range accept {
		err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error { return aiInsert(ctx, tx, t1, c.row) })
		if failIfUndefinedAICalls(t, "INSERT "+c.name, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with %s: want success (a legal boundary), got: %v", c.name, err)
		}
	}

	// Only the boundary rows landed: no refused insert leaked through.
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t1); n != len(accept) {
		t.Errorf("rows after all inserts = %d, want %d (the boundary rows only)", n, len(accept))
	}
}

func TestRLS_AICallsCostIsExactNumeric(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	var got string
	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		r := aiValidRow()
		r.cost = "0.000123456"
		if e := aiInsert(ctx, tx, t1, r); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT cost::text FROM ai_calls WHERE tenant_id = $1`, t1).Scan(&got)
	})
	if failIfUndefinedAICalls(t, "INSERT cost 0.000123456", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT and read back cost: %v", err)
	}
	if got != "0.000123456" {
		t.Errorf("cost read back = %q, want %q", got, "0.000123456")
	}
}

func TestRLS_AICallsNullColumnsAndDuplicateIDRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	t1 := aiTenants(t, 1)[0]

	// A NULL passes a CHECK, so only NOT NULL refuses it.
	r := aiValidRow()
	args := []any{t1, r.client, r.purpose, r.model, r.inTokens, r.outTokens, r.cost, r.latencyMS, r.attempts, r.outcome}
	cols := []string{"tenant_id", "client", "purpose", "model", "input_tokens", "output_tokens", "cost", "latency_ms", "attempts", "outcome"}
	for i := 1; i < len(cols); i++ {
		a := append([]any(nil), args...)
		a[i] = nil
		_, err := h.super.Exec(ctx, aiInsertSQL, a...)
		if failIfUndefinedAICalls(t, "INSERT NULL "+cols[i], err) {
			return
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != cols[i] {
			t.Errorf("INSERT with NULL %s: got %v, want not_null_violation (23502) on %s", cols[i], err, cols[i])
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, t1); n != 0 {
		t.Errorf("rows after the NULL inserts = %d, want 0", n)
	}

	// Positive half and primary key: the same explicit id twice.
	const withID = `INSERT INTO ai_calls (id, tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
		VALUES ($1, $2, 'ai', 'p', 'm', 0, 0, 0, 0, 1, 'ok')`
	id := uuid.NewString()
	if _, err := h.super.Exec(ctx, withID, id, t1); err != nil {
		t.Fatalf("INSERT with an explicit id: %v", err)
	}
	if _, err := h.super.Exec(ctx, withID, id, t1); pgCode(err) != "23505" {
		t.Errorf("duplicate id INSERT: got %v, want unique_violation (23505)", err)
	}
}
