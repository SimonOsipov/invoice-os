package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/migrations"
)

func hookMigrationVersion(t *testing.T) int64 {
	t.Helper()
	return migrationVersion(t, "*_custom_access_token_hook.sql")
}

// reapplyHookMigrationOnCleanup runs the hook migration's Down then Up after the test.
// A memberships Down/Up round-trip drops status, which strips auth_hook_reader's column grant.
func reapplyHookMigrationOnCleanup(t *testing.T, provider *goose.Provider) {
	t.Helper()
	v := hookMigrationVersion(t)
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := provider.ApplyVersion(ctx, v, false); err != nil {
			t.Errorf("roll back the hook migration: %v", err)
			return
		}
		if _, err := provider.ApplyVersion(ctx, v, true); err != nil {
			t.Errorf("re-apply the hook migration: %v", err)
		}
	})
}

// editEvent decodes a hookEvent, lets edit change it, and re-encodes it.
func editEvent(t *testing.T, raw []byte, edit func(event, claims map[string]any)) []byte {
	t.Helper()
	event := decodeJSON(t, raw)
	claims, ok := event["claims"].(map[string]any)
	if !ok {
		t.Fatalf("event has no claims object: %v", event)
	}
	edit(event, claims)
	out, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal edited event: %v", err)
	}
	return out
}

func TestRLS_CustomAccessTokenHookProjectsTheOneActiveAmongOtherStatuses(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name         string
		inA, inB     string
		wantTenantIs string
	}{
		{"active_A_invited_B", "active", "invited", "A"},
		{"invited_A_active_B", "invited", "active", "B"},
		{"suspended_A_active_B", "suspended", "active", "B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			seedHookMembership(t, h.tenantA, userID, tc.inA)
			seedHookMembership(t, h.tenantB, userID, tc.inB)
			want := map[string]string{"A": h.tenantA, "B": h.tenantB}[tc.wantTenantIs]

			event, _ := hookEvent(t, userID)
			md, ok := outputClaims(t, callHook(t, auth, event))["app_metadata"].(map[string]any)
			if !ok {
				t.Fatal("output claims.app_metadata missing")
			}
			if md["tenant_id"] != want {
				t.Errorf("tenant_id = %v, want tenant %s (%s)", md["tenant_id"], tc.wantTenantIs, want)
			}
		})
	}
}

func TestRLS_CustomAccessTokenHookWithoutAppMetadata(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	t.Run("active_creates_app_metadata", func(t *testing.T) {
		userID := uuid.NewString()
		seedHookMembership(t, h.tenantA, userID, "active")
		raw, _ := hookEvent(t, userID)
		event := editEvent(t, raw, func(_, c map[string]any) { delete(c, "app_metadata") })
		want := outputClaims(t, decodeJSON(t, event))

		got := outputClaims(t, callHook(t, auth, event))
		md, ok := got["app_metadata"].(map[string]any)
		if !ok {
			t.Fatalf("output claims.app_metadata missing: %v", got)
		}
		if !reflect.DeepEqual(md, map[string]any{"tenant_id": h.tenantA}) {
			t.Errorf("app_metadata = %v, want only tenant_id %s", md, h.tenantA)
		}
		delete(got, "app_metadata")
		if len(want) == 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("other claims changed\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("no_membership_adds_nothing", func(t *testing.T) {
		raw, _ := hookEvent(t, uuid.NewString())
		event := editEvent(t, raw, func(_, c map[string]any) { delete(c, "app_metadata") })
		want := outputClaims(t, decodeJSON(t, event))
		if len(want) == 0 {
			t.Fatal("input claims empty")
		}
		if got := outputClaims(t, callHook(t, auth, event)); !reflect.DeepEqual(got, want) {
			t.Errorf("claims changed\n got: %v\nwant: %v", got, want)
		}
	})
}

// An incoming tenant_id is replaced by the membership's: the hook re-derives on every issue.
func TestRLS_CustomAccessTokenHookOverwritesIncomingTenantID(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	raw, _ := hookEvent(t, userID)
	event := editEvent(t, raw, func(_, c map[string]any) {
		c["app_metadata"].(map[string]any)["tenant_id"] = h.tenantB
	})

	md, ok := outputClaims(t, callHook(t, auth, event))["app_metadata"].(map[string]any)
	if !ok {
		t.Fatal("output claims.app_metadata missing")
	}
	if md["tenant_id"] != h.tenantA {
		t.Errorf("tenant_id = %v, want the membership's %s over the incoming %s", md["tenant_id"], h.tenantA, h.tenantB)
	}
	if md["provider"] != "email" {
		t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
	}
}

// Only exactly one active membership may put a tenant in the token; an incoming one never survives.
func TestRLS_CustomAccessTokenHookStripsIncomingTenantIDWithoutOneActiveMembership(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name    string
		tenants []string
		want    string
	}{
		{"zero_memberships", nil, ""},
		{"two_active", []string{h.tenantA, h.tenantB}, ""},
		{"one_active", []string{h.tenantA}, h.tenantA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			for _, tenant := range tc.tenants {
				seedHookMembership(t, tenant, userID, "active")
			}
			raw, _ := hookEvent(t, userID)
			event := editEvent(t, raw, func(_, c map[string]any) {
				c["app_metadata"].(map[string]any)["tenant_id"] = "stale-tenant"
			})
			want := outputClaims(t, decodeJSON(t, event))
			delete(want["app_metadata"].(map[string]any), "tenant_id")
			if tc.want != "" {
				want["app_metadata"].(map[string]any)["tenant_id"] = tc.want
			}

			if got := outputClaims(t, callHook(t, auth, event)); !reflect.DeepEqual(got, want) {
				t.Errorf("claims\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// A non-uuid user_id fails the call, so GoTrue refuses the token (fail closed).
// A null or absent user_id matches no row and leaves the claims as issued.
func TestRLS_CustomAccessTokenHookMalformedUserID(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	// Positive pair: an active member exists, so a lookup that ignored user_id would project it.
	seedHookMembership(t, h.tenantA, uuid.NewString(), "active")

	for name, bad := range map[string]string{"invalid_text": "not-a-uuid", "invalid_empty": ""} {
		t.Run(name, func(t *testing.T) {
			raw, _ := hookEvent(t, uuid.NewString())
			event := editEvent(t, raw, func(e, _ map[string]any) { e["user_id"] = bad })
			var out []byte
			err := auth.QueryRow(ctx, `select public.custom_access_token_hook($1::jsonb)`, string(event)).Scan(&out)
			if code := pgCode(err); code != "22P02" {
				t.Errorf("user_id %q: SQLSTATE %q, want 22P02 (invalid uuid): out=%s err=%v", bad, code, out, err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		edit func(e, _ map[string]any)
	}{
		{"null", func(e, _ map[string]any) { e["user_id"] = nil }},
		{"absent", func(e, _ map[string]any) { delete(e, "user_id") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := hookEvent(t, uuid.NewString())
			event := editEvent(t, raw, tc.edit)
			want := outputClaims(t, decodeJSON(t, event))
			if len(want) == 0 {
				t.Fatal("input claims empty")
			}
			if got := outputClaims(t, callHook(t, auth, event)); !reflect.DeepEqual(got, want) {
				t.Errorf("claims changed\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// The lookup keys on event.user_id alone and returns one tenant id, nothing else.
func TestRLS_CustomAccessTokenHookRevealsOnlyTheNamedUsersTenant(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	victim, caller := uuid.NewString(), uuid.NewString()
	seedHookMembership(t, h.tenantB, victim, "active")
	seedHookMembership(t, h.tenantA, caller, "active")

	raw, in := hookEvent(t, caller)
	event := editEvent(t, raw, func(e, _ map[string]any) { e["user_id"] = victim })
	want := roundTrip(t, in)

	got := outputClaims(t, callHook(t, auth, event))
	md, ok := got["app_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("output claims.app_metadata missing: %v", got)
	}
	if md["tenant_id"] != h.tenantB {
		t.Errorf("tenant_id = %v, want the named user's %s", md["tenant_id"], h.tenantB)
	}
	delete(md, "tenant_id")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output carries more than one tenant id\n got: %v\nwant: %v", got, want)
	}
}

func TestRLS_AuthAdminCannotSetRoleToAuthHookReader(t *testing.T) {
	requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	for _, tc := range []struct {
		role, want string
	}{{"supabase_auth_admin", ""}, {"auth_hook_reader", "42501"}} {
		t.Run(tc.role, func(t *testing.T) {
			tx, err := auth.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback(ctx)
			_, err = tx.Exec(ctx, "SET LOCAL ROLE "+tc.role)
			if code := pgCode(err); code != tc.want {
				t.Errorf("SET LOCAL ROLE %s as supabase_auth_admin: SQLSTATE %q, want %q: %v", tc.role, code, tc.want, err)
			}
		})
	}
}

// STABLE plus an owner with no write privilege anywhere: the hook cannot write.
func TestRLS_CustomAccessTokenHookIsStableAndItsOwnerCannotWrite(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	var volatility string
	if err := h.super.QueryRow(ctx,
		`SELECT provolatile::text FROM pg_proc WHERE oid = 'public.custom_access_token_hook(jsonb)'::regprocedure`,
	).Scan(&volatility); err != nil {
		t.Fatalf("read provolatile: %v", err)
	}
	if volatility != "s" {
		t.Errorf("provolatile = %q, want s (STABLE)", volatility)
	}

	var cols, tables int
	if err := h.super.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM information_schema.column_privileges WHERE grantee = 'auth_hook_reader'),
		        (SELECT count(*) FROM information_schema.table_privileges  WHERE grantee = 'auth_hook_reader')`,
	).Scan(&cols, &tables); err != nil {
		t.Fatalf("read auth_hook_reader privileges: %v", err)
	}
	if cols != 3 {
		t.Errorf("auth_hook_reader holds %d column privileges, want the 3 SELECTs on memberships", cols)
	}
	if tables != 0 {
		t.Errorf("auth_hook_reader holds %d table-level privileges, want 0", tables)
	}
}

// Drives the shipped Down and Up through goose for this one version.
func TestRLS_CustomAccessTokenHookDownRemovesFunctionPolicyAndGrants(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	version := hookMigrationVersion(t)

	sqlDB, err := sql.Open("pgx", os.Getenv("DATABASE_MIGRATION_URL"))
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	footprint := func() (fn, policy, privs int) {
		t.Helper()
		if err := h.super.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM pg_proc WHERE proname = 'custom_access_token_hook'),
			        (SELECT count(*) FROM pg_policies WHERE policyname = 'auth_hook_lookup'),
			        (SELECT count(*) FROM information_schema.column_privileges WHERE grantee = 'auth_hook_reader')
			      + (SELECT count(*) FROM information_schema.table_privileges  WHERE grantee = 'auth_hook_reader')`,
		).Scan(&fn, &policy, &privs); err != nil {
			t.Fatalf("read hook footprint: %v", err)
		}
		return
	}

	if fn, policy, privs := footprint(); fn != 1 || policy != 1 || privs != 3 {
		t.Fatalf("before Down: function=%d policy=%d privileges=%d, want 1 1 3", fn, policy, privs)
	}

	needsUp := true
	t.Cleanup(func() {
		if needsUp {
			if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil {
				t.Errorf("restore the hook migration: %v", err)
			}
		}
	})
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		needsUp = false
		t.Fatalf("roll back the hook migration: %v", err)
	}
	if fn, policy, privs := footprint(); fn != 0 || policy != 0 || privs != 0 {
		t.Errorf("after Down: function=%d policy=%d privileges=%d, want 0 0 0", fn, policy, privs)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the hook migration: %v", err)
	}
	needsUp = false
	if fn, policy, privs := footprint(); fn != 1 || policy != 1 || privs != 3 {
		t.Errorf("after Up: function=%d policy=%d privileges=%d, want 1 1 3", fn, policy, privs)
	}
}
