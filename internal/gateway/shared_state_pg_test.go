// gateway_session_revocations and gateway_throttle_counts are global (no tenant, no RLS).
// CI runs these in the RLS job, where DATABASE_GATEWAY_URL is set.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

var sharedStateTables = []string{"gateway_session_revocations", "gateway_throttle_counts"}

// requireSharedStateTables fails by name when the migration has not run.
func requireSharedStateTables(t *testing.T, d handoffDB) {
	t.Helper()
	for _, tbl := range sharedStateTables {
		var reg *string
		if err := d.super.QueryRow(context.Background(), `SELECT to_regclass('public.' || $1)::text`, tbl).Scan(&reg); err != nil {
			t.Fatalf("look up table %s: %v", tbl, err)
		}
		if reg == nil {
			t.Fatalf("table %s does not exist: the gateway_shared_limits migration has not run", tbl)
		}
	}
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ownSubject returns a random subject and deletes its row as the superuser on cleanup (D21).
func ownSubject(t *testing.T, d handoffDB) string {
	t.Helper()
	sub := "flake04-" + randHex(t, 8)
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_session_revocations WHERE subject = $1`, sub)
	})
	return sub
}

// ownThrottleRow returns a random scope and a 32-byte key; cleanup deletes the row by scope.
func ownThrottleRow(t *testing.T, d handoffDB) (string, []byte) {
	t.Helper()
	scope := "flake04-" + randHex(t, 8)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE scope = $1`, scope)
	})
	return scope, key
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestRLS_GatewaySharedState_OnlyTheGatewayRoleReachesTheTables(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()

	var who string
	if err := d.a.QueryRow(ctx, `SELECT current_user`).Scan(&who); err != nil || who != "invoice_gateway" {
		t.Fatalf("gateway pool runs as (%q, %v), want invoice_gateway", who, err)
	}

	sub := ownSubject(t, d)
	scope, key := ownThrottleRow(t, d)
	gateway := []struct {
		name, sql string
		args      []any
	}{
		{"revocations INSERT", `INSERT INTO gateway_session_revocations (subject, revoked_at) VALUES ($1, now())`, []any{sub}},
		{"revocations SELECT", `SELECT subject FROM gateway_session_revocations WHERE subject = $1`, []any{sub}},
		{"revocations UPDATE", `UPDATE gateway_session_revocations SET revoked_at = now() WHERE subject = $1`, []any{sub}},
		{"revocations DELETE", `DELETE FROM gateway_session_revocations WHERE subject = $1`, []any{sub}},
		{"throttle INSERT", `INSERT INTO gateway_throttle_counts (scope, key_hash, n, expires_at) VALUES ($1, $2, 1, now() + interval '1 minute')`, []any{scope, key}},
		{"throttle SELECT", `SELECT n FROM gateway_throttle_counts WHERE scope = $1 AND key_hash = $2`, []any{scope, key}},
		{"throttle UPDATE", `UPDATE gateway_throttle_counts SET n = n + 1 WHERE scope = $1 AND key_hash = $2`, []any{scope, key}},
		{"throttle DELETE", `DELETE FROM gateway_throttle_counts WHERE scope = $1 AND key_hash = $2`, []any{scope, key}},
	}
	for _, s := range gateway {
		rows, err := d.a.Query(ctx, s.sql, s.args...)
		if err != nil {
			t.Errorf("invoice_gateway %s: %v", s.name, err)
			continue
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Errorf("invoice_gateway %s: %v", s.name, err)
		}
	}

	for _, tbl := range sharedStateTables {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var gw, app bool
			if err := d.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_gateway', $1, $2), has_table_privilege('invoice_app', $1, $2)`, tbl, priv).Scan(&gw, &app); err != nil {
				t.Fatalf("has_table_privilege %s %s: %v", tbl, priv, err)
			}
			if !gw {
				t.Errorf("invoice_gateway lacks %s on %s", priv, tbl)
			}
			if app {
				t.Errorf("invoice_app holds %s on %s, want nothing", priv, tbl)
			}
		}
		var grants []string
		if err := d.super.QueryRow(ctx, `SELECT coalesce(array_agg(coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type ORDER BY coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type), '{}')
			FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee
			WHERE c.oid = to_regclass('public.' || $1) AND a.grantee <> c.relowner`, tbl).Scan(&grants); err != nil {
			t.Fatalf("aclexplode %s: %v", tbl, err)
		}
		wantGrants := []string{"invoice_gateway:DELETE", "invoice_gateway:INSERT", "invoice_gateway:SELECT", "invoice_gateway:UPDATE"}
		if !slices.Equal(grants, wantGrants) {
			t.Errorf("%s grants beyond the owner = %v, want exactly %v (no reader, app, PUBLIC or extra privilege)", tbl, grants, wantGrants)
		}
		_, err := d.app.Exec(ctx, `SELECT * FROM `+tbl)
		if code := sqlState(err); code != "42501" {
			t.Errorf("invoice_app SELECT on %s = %v (SQLSTATE %q), want permission denied 42501", tbl, err, code)
		}
	}
}

func TestRLS_GatewaySharedState_TablesAreGlobal(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()

	wantCols := map[string][]string{
		"gateway_session_revocations": {"revoked_at", "subject"},
		"gateway_throttle_counts":     {"expires_at", "key_hash", "n", "scope"},
	}
	wantPK := map[string][]string{
		"gateway_session_revocations": {"subject"},
		"gateway_throttle_counts":     {"scope", "key_hash"},
	}
	wantIdx := map[string]string{
		"gateway_session_revocations": "CREATE INDEX gateway_session_revocations_revoked_at ON public.gateway_session_revocations USING btree (revoked_at)",
		"gateway_throttle_counts":     "CREATE INDEX gateway_throttle_counts_expires_at ON public.gateway_throttle_counts USING btree (expires_at)",
	}
	wantTypes := map[string]string{
		"gateway_session_revocations": "revoked_at=timestamp with time zone,subject=text",
		"gateway_throttle_counts":     "expires_at=timestamp with time zone,key_hash=bytea,n=integer,scope=text",
	}
	for _, tbl := range sharedStateTables {
		var rls, force bool
		if err := d.super.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, tbl).Scan(&rls, &force); err != nil {
			t.Fatalf("pg_class %s: %v", tbl, err)
		}
		if rls || force {
			t.Errorf("%s has row-level security (enable %v, force %v), want none: the table is global", tbl, rls, force)
		}

		rows, err := d.super.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY column_name`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		rows.Close()
		if len(cols) == 0 {
			t.Fatalf("%s has no columns in information_schema", tbl)
		}
		if slices.Contains(cols, "tenant_id") {
			t.Errorf("%s has a tenant_id column, want none", tbl)
		}
		if !slices.Equal(cols, wantCols[tbl]) {
			t.Errorf("%s columns = %v, want %v", tbl, cols, wantCols[tbl])
		}

		var nullable []string
		if err := d.super.QueryRow(ctx, `SELECT coalesce(array_agg(column_name::text ORDER BY column_name), '{}') FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND is_nullable = 'YES'`, tbl).Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		if len(nullable) != 0 {
			t.Errorf("%s nullable columns = %v, want none", tbl, nullable)
		}

		var pk []string
		if err := d.super.QueryRow(ctx, `SELECT coalesce(array_agg(a.attname::text ORDER BY k.ord), '{}')
			FROM pg_index i
			CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			WHERE i.indrelid = to_regclass('public.' || $1) AND i.indisprimary`, tbl).Scan(&pk); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pk, wantPK[tbl]) {
			t.Errorf("%s primary key = %v, want %v", tbl, pk, wantPK[tbl])
		}

		var types string
		if err := d.super.QueryRow(ctx, `SELECT string_agg(column_name || '=' || data_type, ',' ORDER BY column_name) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`, tbl).Scan(&types); err != nil {
			t.Fatal(err)
		}
		if types != wantTypes[tbl] {
			t.Errorf("%s column types = %q, want %q", tbl, types, wantTypes[tbl])
		}

		var idxDefs []string
		if err := d.super.QueryRow(ctx, `SELECT coalesce(array_agg(indexdef ORDER BY indexname) FILTER (WHERE indexname NOT LIKE '%_pkey'), '{}') FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1`, tbl).Scan(&idxDefs); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(idxDefs, []string{wantIdx[tbl]}) {
			t.Errorf("%s secondary indexes = %v, want exactly [%s]", tbl, idxDefs, wantIdx[tbl])
		}
	}
}

func TestRLS_GatewaySharedState_ThrottleKeyIsAHashLength(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	const ins = `INSERT INTO gateway_throttle_counts (scope, key_hash, n, expires_at) VALUES ($1, $2, $3, now() + interval '1 minute')`

	scope, key := ownThrottleRow(t, d)
	if _, err := d.a.Exec(ctx, ins, scope, key, 1); err != nil {
		t.Fatalf("a 32-byte key_hash is refused: %v", err)
	}

	zeroScope, zeroKey := ownThrottleRow(t, d)
	if _, err := d.a.Exec(ctx, ins, zeroScope, zeroKey, 0); err != nil {
		t.Fatalf("n = 0 is refused: %v", err)
	}

	refused := []struct {
		name  string
		scope string
		key   []byte
		n     int
	}{
		{"31-byte key_hash", scope, key[:31], 1},
		{"33-byte key_hash", scope, append(slices.Clone(key), 0), 1},
		{"empty key_hash", scope, []byte{}, 1},
		{"empty scope", "", key, 1},
		{"negative n", scope + "-neg", key, -1},
	}
	for _, c := range refused {
		_, err := d.a.Exec(ctx, ins, c.scope, c.key, c.n)
		if code := sqlState(err); code != "23514" {
			t.Errorf("%s: err = %v (SQLSTATE %q), want check violation 23514", c.name, err, code)
		}
	}

	if _, err := d.a.Exec(ctx, ins, scope, key, 1); sqlState(err) != "23505" {
		t.Errorf("repeat (scope, key_hash) = %v, want unique violation 23505", err)
	}
}

func TestRLS_GatewaySharedState_RevocationRefusesAnEmptySubject(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	const ins = `INSERT INTO gateway_session_revocations (subject, revoked_at) VALUES ($1, now())`

	sub := ownSubject(t, d)
	if _, err := d.a.Exec(ctx, ins, sub); err != nil {
		t.Fatalf("a non-empty subject is refused: %v", err)
	}
	if _, err := d.a.Exec(ctx, ins, ""); sqlState(err) != "23514" {
		t.Errorf("empty subject: err = %v, want check violation 23514", err)
	}
	if _, err := d.a.Exec(ctx, ins, sub); sqlState(err) != "23505" {
		t.Errorf("repeat subject = %v, want unique violation 23505", err)
	}
}
