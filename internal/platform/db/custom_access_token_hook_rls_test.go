package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// authAdminPool connects as supabase_auth_admin, the role GoTrue's hook dispatcher uses.
// A missing URL fails rather than skips: the rls job sets it.
func authAdminPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_AUTH_ADMIN_URL")
	if url == "" {
		t.Fatal("DATABASE_AUTH_ADMIN_URL is unset; the hook tests need the supabase_auth_admin login")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect supabase_auth_admin: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedHookMembership(t *testing.T, tenantID, userID, status string) {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1, $2, $3, 'admin', $4)`,
		id, tenantID, userID, status,
	); err != nil {
		t.Fatalf("seed membership (%s): %v", status, err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	})
}

// hookEvent builds a CustomAccessTokenInput as supabase/auth v2.197.0 marshals it
// (internal/hooks/v0hooks: metadata, user_id, claims, authentication_method).
func hookEvent(t *testing.T, userID string) (event []byte, claims map[string]any) {
	t.Helper()
	claims = map[string]any{
		"iss":           "http://auth:9999",
		"sub":           userID,
		"aud":           []any{"authenticated"},
		"exp":           json.Number("1790000000"),
		"iat":           json.Number("1789996400"),
		"email":         "hook-user@example.test",
		"phone":         "",
		"app_metadata":  map[string]any{"provider": "email", "providers": []any{"email"}},
		"user_metadata": map[string]any{"full_name": "Hook User"},
		"role":          "authenticated",
		"aal":           "aal1",
		"amr":           []any{map[string]any{"method": "password", "timestamp": json.Number("1789996400")}},
		"session_id":    uuid.NewString(),
		"is_anonymous":  false,
	}
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"uuid":       uuid.NewString(),
			"time":       "2026-09-24T10:00:00Z",
			"name":       "custom-access-token",
			"ip_address": "10.0.0.1",
		},
		"user_id":               userID,
		"claims":                claims,
		"authentication_method": "password",
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return raw, claims
}

// callHook runs the hook exactly as GoTrue's pg-functions dispatcher does.
func callHook(t *testing.T, pool *pgxpool.Pool, event []byte) map[string]any {
	t.Helper()
	var out []byte
	if err := pool.QueryRow(context.Background(),
		`select public.custom_access_token_hook($1::jsonb)`, string(event),
	).Scan(&out); err != nil {
		t.Fatalf("call custom_access_token_hook as supabase_auth_admin: %v", err)
	}
	return decodeJSON(t, out)
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

// roundTrip normalises a Go value through JSON so it compares equal to a decoded one.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return decodeJSON(t, raw)
}

func outputClaims(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	c, ok := out["claims"].(map[string]any)
	if !ok {
		t.Fatalf("output has no claims object: %v", out)
	}
	return c
}

func TestRLS_CustomAccessTokenHookProjectsSingleActiveTenant(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	// Another user's active row in B: a hook that ignores user_id would project B.
	seedHookMembership(t, h.tenantB, uuid.NewString(), "active")

	event, in := hookEvent(t, userID)
	want := roundTrip(t, in)
	if len(want) != 14 {
		t.Fatalf("input claims = %d keys, want the 14 GoTrue v2.197.0 emits", len(want))
	}

	got := outputClaims(t, callHook(t, auth, event))
	md, ok := got["app_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("output claims.app_metadata missing: %v", got)
	}
	if md["tenant_id"] != h.tenantA {
		t.Errorf("claims.app_metadata.tenant_id = %v, want %s (text)", md["tenant_id"], h.tenantA)
	}

	delete(md, "tenant_id")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claims other than tenant_id changed\n got: %v\nwant: %v", got, want)
	}
}

func TestRLS_CustomAccessTokenHookLeavesClaimsWithoutActiveMembership(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, status := range []string{"", "invited", "suspended"} {
		name := status
		if name == "" {
			name = "no_row"
		}
		t.Run(name, func(t *testing.T) {
			userID := uuid.NewString()
			if status != "" {
				seedHookMembership(t, h.tenantA, userID, status)
			}
			event, in := hookEvent(t, userID)
			want := roundTrip(t, in)
			if len(want) == 0 {
				t.Fatal("input claims empty")
			}
			if got := outputClaims(t, callHook(t, auth, event)); !reflect.DeepEqual(got, want) {
				t.Errorf("claims changed\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

func TestRLS_CustomAccessTokenHookLeavesClaimsForTwoActiveTenants(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	seedHookMembership(t, h.tenantB, userID, "active")

	event, in := hookEvent(t, userID)
	want := roundTrip(t, in)
	got := outputClaims(t, callHook(t, auth, event))
	md, ok := got["app_metadata"].(map[string]any)
	if !ok || len(md) == 0 {
		t.Fatalf("output claims.app_metadata missing or empty: %v", got)
	}
	if v, has := md["tenant_id"]; has {
		t.Errorf("two active tenants projected tenant_id = %v, want no key", v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claims changed\n got: %v\nwant: %v", got, want)
	}
}

func TestRLS_CustomAccessTokenHookReturnsClaimsEnvelope(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name   string
		tenant string
	}{{"projected", h.tenantA}, {"unchanged", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			if tc.tenant != "" {
				seedHookMembership(t, tc.tenant, userID, "active")
			}
			event, _ := hookEvent(t, userID)
			out := callHook(t, auth, event)
			keys := make([]string, 0, len(out))
			for k := range out {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, []string{"claims"}) {
				t.Errorf("top-level keys = %v, want [claims]", keys)
			}
		})
	}
}

func TestRLS_AuthAdminCannotReadMembershipsDirectly(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	// Positive pair: the same login does reach the user's tenant through the hook.
	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	event, _ := hookEvent(t, userID)
	md, _ := outputClaims(t, callHook(t, auth, event))["app_metadata"].(map[string]any)
	if md["tenant_id"] != h.tenantA {
		t.Fatalf("hook as supabase_auth_admin projected %v, want %s", md["tenant_id"], h.tenantA)
	}

	for _, tc := range []struct{ name, sql string }{
		{"select_user_id", `SELECT user_id FROM public.memberships LIMIT 1`},
		{"select_email", `SELECT email FROM public.memberships LIMIT 1`},
		{"insert", `INSERT INTO public.memberships (tenant_id, user_id, role) VALUES ('` + h.tenantA + `', gen_random_uuid(), 'admin')`},
		{"update", `UPDATE public.memberships SET status = 'active' WHERE false`},
		{"delete", `DELETE FROM public.memberships WHERE false`},
		{"select_tenants", `SELECT id FROM public.tenants LIMIT 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.Exec(ctx, tc.sql)
			if code := pgCode(err); code != "42501" {
				t.Errorf("%s as supabase_auth_admin: SQLSTATE %q, want 42501: %v", tc.sql, code, err)
			}
		})
	}
}

func TestRLS_OnlyAuthAdminCanExecuteCustomAccessTokenHook(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	event, _ := hookEvent(t, uuid.NewString())
	if out := callHook(t, auth, event); len(out) == 0 {
		t.Fatal("hook as supabase_auth_admin returned an empty object")
	}

	for _, tc := range []struct {
		role string
		pool *pgxpool.Pool
	}{{"invoice_app", h.app}, {"invoice_tenant_reader", h.reader}} {
		t.Run(tc.role, func(t *testing.T) {
			var out []byte
			err := tc.pool.QueryRow(ctx, `select public.custom_access_token_hook($1::jsonb)`, string(event)).Scan(&out)
			if code := pgCode(err); code != "42501" {
				t.Errorf("EXECUTE as %s: SQLSTATE %q, want 42501 (permission denied): %v", tc.role, code, err)
			}
		})
	}
}

// requireHookPolicy asserts auth_hook_lookup exists and targets only auth_hook_reader,
// so the isolation checks below run against the migrated schema.
func requireHookPolicy(t *testing.T) {
	t.Helper()
	var roles []string
	var cmd string
	err := h.super.QueryRow(context.Background(),
		`SELECT roles::text[], cmd FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = 'memberships' AND policyname = 'auth_hook_lookup'`,
	).Scan(&roles, &cmd)
	if err == pgx.ErrNoRows {
		t.Fatal("policy auth_hook_lookup on public.memberships does not exist")
	}
	if err != nil {
		t.Fatalf("read auth_hook_lookup: %v", err)
	}
	if !reflect.DeepEqual(roles, []string{"auth_hook_reader"}) || cmd != "SELECT" {
		t.Fatalf("auth_hook_lookup roles=%v cmd=%s, want [auth_hook_reader] SELECT", roles, cmd)
	}
}

func TestRLS_AuthHookPolicyDoesNotWidenAppIsolation(t *testing.T) {
	h := requireHarness(t)
	requireHookPolicy(t)
	ctx := context.Background()

	seedHookMembership(t, h.tenantA, uuid.NewString(), "active")
	seedHookMembership(t, h.tenantB, uuid.NewString(), "active")

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships`); n == 0 {
			t.Error("app scoped to A sees no memberships, want A's row")
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id <> $1`, h.tenantA); n != 0 {
			t.Errorf("app scoped to A sees %d rows of other tenants, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

func TestRLS_MigratorDoesNotInheritAuthHookPolicy(t *testing.T) {
	h := requireHarness(t)
	requireHookPolicy(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	seedHookMembership(t, h.tenantB, uuid.NewString(), "active")

	// Positive pair: the policy does give the hook's owner the row.
	event, _ := hookEvent(t, userID)
	md, _ := outputClaims(t, callHook(t, auth, event))["app_metadata"].(map[string]any)
	if md["tenant_id"] != h.tenantA {
		t.Fatalf("hook projected %v, want %s", md["tenant_id"], h.tenantA)
	}

	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var who, guc string
	if err := tx.QueryRow(ctx,
		`SELECT current_user, coalesce(current_setting('app.current_tenant', true), '')`,
	).Scan(&who, &guc); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if who != "invoice_migrator" || guc != "" {
		t.Fatalf("session is %s with app.current_tenant=%q, want invoice_migrator with it unset", who, guc)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships`); n != 0 {
		t.Errorf("invoice_migrator without SET ROLE sees %d memberships, want 0", n)
	}
}

func TestRLS_CustomAccessTokenHookIsDefinerOwnedByHookReader(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	var (
		secdef bool
		config []string
		owner  string
		acl    []string
	)
	err := h.super.QueryRow(ctx,
		`SELECT p.prosecdef, coalesce(p.proconfig, '{}'), pg_get_userbyid(p.proowner), coalesce(p.proacl::text[], '{}')
		   FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		  WHERE n.nspname = 'public' AND p.proname = 'custom_access_token_hook'
		    AND pg_get_function_identity_arguments(p.oid) = 'event jsonb'`,
	).Scan(&secdef, &config, &owner, &acl)
	if err == pgx.ErrNoRows {
		t.Fatal("function public.custom_access_token_hook(event jsonb) does not exist")
	}
	if err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}
	if !secdef {
		t.Error("prosecdef = false, want SECURITY DEFINER")
	}
	// Postgres stores SET search_path = '' as the element search_path="".
	if !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %v, want [search_path=\"\"]", config)
	}
	if owner != "auth_hook_reader" {
		t.Errorf("owner = %s, want auth_hook_reader", owner)
	}
	if len(acl) == 0 {
		t.Fatal("proacl is empty (NULL means default PUBLIC EXECUTE)")
	}
	granted := false
	for _, item := range acl {
		if strings.HasPrefix(item, "=") {
			t.Errorf("proacl has a PUBLIC entry %q", item)
		}
		if strings.HasPrefix(item, "supabase_auth_admin=X/") {
			granted = true
		}
	}
	if !granted {
		t.Errorf("proacl %v has no supabase_auth_admin=X/ entry", acl)
	}

	rows, err := h.super.Query(ctx,
		`SELECT column_name || ':' || privilege_type FROM information_schema.column_privileges
		  WHERE grantee = 'auth_hook_reader' AND table_schema = 'public' AND table_name = 'memberships'
		  ORDER BY 1`)
	if err != nil {
		t.Fatalf("read column privileges: %v", err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect column privileges: %v", err)
	}
	want := []string{"status:SELECT", "tenant_id:SELECT", "user_id:SELECT"}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("auth_hook_reader column privileges on memberships = %v, want %v", cols, want)
	}
}
