package db_test

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// seedStaff inserts a staff_members row as the superuser; the cleanup tolerates a dropped table.
func seedStaff(t *testing.T, userID string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO public.staff_members (user_id) VALUES ($1)`, userID); err != nil {
		t.Fatalf("seed staff_members row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, userID)
	})
}

// hookRun calls the hook as supabase_auth_admin on a golden-shaped event that edit may change.
// It returns the output claims and the input claims.
func hookRun(t *testing.T, auth *pgxpool.Pool, userID string, edit func(claims map[string]any)) (got, in map[string]any) {
	t.Helper()
	raw, _ := hookEvent(t, userID)
	if edit != nil {
		raw = editEvent(t, raw, func(_, c map[string]any) { edit(c) })
	}
	return outputClaims(t, callHook(t, auth, raw)), outputClaims(t, decodeJSON(t, raw))
}

func appMetadataOf(t *testing.T, claims map[string]any) map[string]any {
	t.Helper()
	md, ok := claims["app_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("claims.app_metadata is not an object: %v", claims)
	}
	return md
}

func staffMigrationProvider(t *testing.T) *goose.Provider {
	t.Helper()
	sqlDB, err := sql.Open("pgx", os.Getenv("DATABASE_MIGRATION_URL"))
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	return provider
}

// AC-1. Guard: fails if a grant to one of these roles is added.
func TestRLS_StaffMembersRefusesAppReaderAndAuthAdmin(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	seedStaff(t, uuid.NewString())
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n == 0 {
		t.Fatal("staff_members is empty, so a refusal could not be told from an empty table")
	}

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{
		{"invoice_app", h.app},
		{"invoice_tenant_reader", h.reader},
		{"supabase_auth_admin", authAdminPool(t)},
	} {
		for _, stmt := range []struct{ verb, sql string }{
			{"SELECT", `SELECT user_id FROM public.staff_members`},
			{"INSERT", `INSERT INTO public.staff_members (user_id) VALUES (gen_random_uuid())`},
			{"UPDATE", `UPDATE public.staff_members SET created_at = now()`},
			{"DELETE", `DELETE FROM public.staff_members`},
		} {
			_, err := role.pool.Exec(ctx, stmt.sql)
			if code := pgCode(err); code != sqlstateInsufficientPrivilege {
				t.Errorf("%s %s on staff_members: SQLSTATE %q, want %s: %v", role.name, stmt.verb, code, sqlstateInsufficientPrivilege, err)
			}
		}
	}
}

// AC-2.
func TestRLS_StaffMembersHookReaderReadsUserIDOnly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	userID := uuid.NewString()
	seedStaff(t, userID)

	// Each statement gets its own transaction: a refusal aborts it.
	asHookReader := func(stmt string, args ...any) (pgx.Rows, func(), error) {
		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		done := func() { _ = tx.Rollback(ctx) }
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
			done()
			t.Fatalf("SET LOCAL ROLE auth_hook_reader: %v", err)
		}
		rows, err := tx.Query(ctx, stmt, args...)
		return rows, done, err
	}

	rows, done, err := asHookReader(`SELECT user_id::text FROM public.staff_members WHERE user_id = $1`, userID)
	if err != nil {
		t.Errorf("auth_hook_reader SELECT user_id: %v", err)
	} else {
		var seen []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan user_id: %v", err)
			}
			seen = append(seen, id)
		}
		if err := rows.Err(); err != nil {
			t.Errorf("auth_hook_reader SELECT user_id: %v", err)
		} else if !reflect.DeepEqual(seen, []string{userID}) {
			t.Errorf("auth_hook_reader reads user_ids %v, want [%s]", seen, userID)
		}
		rows.Close()
	}
	done()

	for _, c := range []struct{ what, sql string }{
		{"SELECT created_at", `SELECT created_at FROM public.staff_members`},
		{"INSERT", `INSERT INTO public.staff_members (user_id) VALUES (gen_random_uuid())`},
	} {
		rows, done, err := asHookReader(c.sql)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		done()
		if code := pgCode(err); code != sqlstateInsufficientPrivilege {
			t.Errorf("auth_hook_reader %s: SQLSTATE %q, want %s: %v", c.what, code, sqlstateInsufficientPrivilege, err)
		}
	}
}

// AC-3. Guard: fails if the primary key is dropped.
func TestRLS_StaffMembersOwnerGrantIsIdempotent(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	userID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, userID)
	})

	for i, wantAffected := range []int64{1, 0} {
		tag, err := h.mig.Exec(ctx,
			`INSERT INTO public.staff_members (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
		if err != nil {
			t.Fatalf("migrator insert %d: %v", i+1, err)
		}
		if tag.RowsAffected() != wantAffected {
			t.Errorf("migrator insert %d affected %d rows, want %d", i+1, tag.RowsAffected(), wantAffected)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, userID); n != 1 {
		t.Errorf("staff_members rows for the user = %d, want 1", n)
	}
	// A plain duplicate must fail on the key, not pass silently.
	_, err := h.mig.Exec(ctx, `INSERT INTO public.staff_members (user_id) VALUES ($1)`, userID)
	if code := pgCode(err); code != "23505" {
		t.Errorf("plain duplicate insert: SQLSTATE %q, want 23505: %v", code, err)
	}
}

// AC-4.
func TestRLS_CustomAccessTokenHookProjectsStaffBesideTheTenant(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name       string
		membership bool
	}{
		{"one_active_membership", true},
		{"no_membership", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			seedStaff(t, userID)
			if tc.membership {
				seedHookMembership(t, h.tenantA, userID, "active")
			}

			got, want := hookRun(t, auth, userID, nil)
			wantMD := appMetadataOf(t, want)
			wantMD["staff"] = true
			if tc.membership {
				wantMD["tenant_id"] = h.tenantA
			}

			gotMD := appMetadataOf(t, got)
			if gotMD["staff"] != true {
				t.Errorf("app_metadata.staff = %v, want true: %v", gotMD["staff"], gotMD)
			}
			if _, has := gotMD["tenant_id"]; has != tc.membership {
				t.Errorf("app_metadata has tenant_id = %v, want %v: %v", has, tc.membership, gotMD)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("claims\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// AC-5.
func TestRLS_CustomAccessTokenHookStaffWithoutAppMetadata(t *testing.T) {
	requireHarness(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedStaff(t, userID)
	got, want := hookRun(t, auth, userID, func(c map[string]any) { delete(c, "app_metadata") })
	if len(want) == 0 {
		t.Fatal("input claims empty")
	}

	if md, ok := got["app_metadata"].(map[string]any); !ok || !reflect.DeepEqual(md, map[string]any{"staff": true}) {
		t.Errorf("app_metadata = %v, want {staff: true}", got["app_metadata"])
	}
	delete(got, "app_metadata")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("other claims changed\n got: %v\nwant: %v", got, want)
	}
}

// AC-6.
func TestRLS_CustomAccessTokenHookStripsAnUnearnedStaffClaim(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	// Another user's row: a strip that skipped on any row would keep the claim.
	seedStaff(t, uuid.NewString())
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n == 0 {
		t.Fatal("staff_members is empty")
	}

	for name, forged := range map[string]any{"bool_true": true, "string_true": "true", "number_1": 1} {
		t.Run(name, func(t *testing.T) {
			got, in := hookRun(t, auth, uuid.NewString(), func(c map[string]any) {
				c["app_metadata"].(map[string]any)["staff"] = forged
			})
			if _, planted := appMetadataOf(t, in)["staff"]; !planted {
				t.Fatal("the input carries no staff claim to strip")
			}

			md := appMetadataOf(t, got)
			if v, has := md["staff"]; has {
				t.Errorf("app_metadata.staff = %v survived, want it removed", v)
			}
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
			delete(appMetadataOf(t, in), "staff")
			if !reflect.DeepEqual(got, in) {
				t.Errorf("claims other than staff changed\n got: %v\nwant: %v", got, in)
			}
		})
	}
}

// AC-7. Guard: fails if the strip branch sets staff:false.
func TestRLS_CustomAccessTokenHookAddsNoKeyForANonStaffUser(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name       string
		membership bool
	}{
		{"no_membership", false},
		{"one_active_membership", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			if tc.membership {
				seedHookMembership(t, h.tenantA, userID, "active")
			}
			got, want := hookRun(t, auth, userID, nil)
			if len(appMetadataOf(t, want)) == 0 {
				t.Fatal("input app_metadata empty")
			}
			if tc.membership {
				appMetadataOf(t, want)["tenant_id"] = h.tenantA
			}

			if v, has := appMetadataOf(t, got)["staff"]; has {
				t.Errorf("app_metadata.staff = %v added for a non-staff user", v)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("claims\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// AC-8. Guard: fails if provisioning inserts a staff row.
func TestRLS_ProvisionWorkspaceGrantsNoStaff(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	cleanupTenant(t, tenantID)
	a := newProvisionArgs(tenantID)
	before := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`)

	if err := provisionAs(ctx, h.app, tenantID, a); err != nil {
		t.Fatalf("provision_workspace as invoice_app: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n != before {
		t.Errorf("staff_members rows = %d after provisioning, want %d", n, before)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, a.userID); n != 0 {
		t.Errorf("staff_members rows for the provisioned user = %d, want 0", n)
	}
	md := appMetadataOf(t, mustHookClaims(t, auth, a.userID))
	if md["tenant_id"] != tenantID {
		t.Fatalf("app_metadata.tenant_id = %v, want the provisioned %s (the workspace exists)", md["tenant_id"], tenantID)
	}
	if v, has := md["staff"]; has {
		t.Errorf("app_metadata.staff = %v for a provisioned user, want absent", v)
	}
}

func mustHookClaims(t *testing.T, auth *pgxpool.Pool, userID string) map[string]any {
	t.Helper()
	got, _ := hookRun(t, auth, userID, nil)
	return got
}

// staffFootprint counts the staff_members table, its hook policy and auth_hook_reader's column grants on it.
func staffFootprint(t *testing.T) (tables, policies, grants int) {
	t.Helper()
	if err := h.super.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = 'staff_members'),
		        (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'staff_members'
		          AND policyname = 'staff_hook_lookup'),
		        (SELECT count(*) FROM information_schema.column_privileges
		          WHERE table_name = 'staff_members' AND grantee = 'auth_hook_reader')`,
	).Scan(&tables, &policies, &grants); err != nil {
		t.Fatalf("read staff_members footprint: %v", err)
	}
	return
}

// AC-9. Drives the shipped Down and Up through goose for the staff version.
func TestRLS_StaffMembersDownRestoresTheTenantOnlyHook(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()
	version := staffMigrationVersion(t)
	provider := staffMigrationProvider(t)

	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	seedStaff(t, userID)

	checkProjection := func(stage string, wantStaff bool) {
		t.Helper()
		md := appMetadataOf(t, mustHookClaims(t, auth, userID))
		if md["tenant_id"] != h.tenantA {
			t.Errorf("%s: app_metadata.tenant_id = %v, want %s", stage, md["tenant_id"], h.tenantA)
		}
		if got := md["staff"] == true; got != wantStaff {
			t.Errorf("%s: app_metadata.staff = %v, want present=%v", stage, md["staff"], wantStaff)
		}
	}
	checkFootprint := func(stage string, want int) {
		t.Helper()
		if tables, policies, grants := staffFootprint(t); tables != want || policies != want || grants != want {
			t.Errorf("%s: table=%d policy=%d column grants=%d, want %d each", stage, tables, policies, grants, want)
		}
	}

	checkFootprint("before Down", 1)
	checkProjection("before Down", true)

	staffApplied := true
	t.Cleanup(func() {
		if !staffApplied {
			if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil {
				t.Errorf("restore the staff migration: %v", err)
			}
		}
	})
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the staff migration: %v", err)
	}
	staffApplied = false
	checkFootprint("after Down", 0)
	checkProjection("after Down", false)

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the staff migration: %v", err)
	}
	staffApplied = true
	seedStaff(t, userID)
	checkFootprint("after Up", 1)
	checkProjection("after Up", true)
}

// AC-11.
func TestRLS_CustomAccessTokenHookStaffStepIsTotalOverNonObjectAppMetadata(t *testing.T) {
	requireHarness(t)
	auth := authAdminPool(t)

	for name, appMetadata := range map[string]any{"json_null": nil, "string": "x"} {
		t.Run(name+"/staff", func(t *testing.T) {
			userID := uuid.NewString()
			seedStaff(t, userID)
			got, want := hookRun(t, auth, userID, func(c map[string]any) { c["app_metadata"] = appMetadata })
			if len(want) == 0 {
				t.Fatal("input claims empty")
			}
			if md, ok := got["app_metadata"].(map[string]any); !ok || !reflect.DeepEqual(md, map[string]any{"staff": true}) {
				t.Errorf("app_metadata = %v, want {staff: true}", got["app_metadata"])
			}
		})
		t.Run(name+"/non_staff", func(t *testing.T) {
			got, want := hookRun(t, auth, uuid.NewString(), func(c map[string]any) { c["app_metadata"] = appMetadata })
			if len(want) == 0 {
				t.Fatal("input claims empty")
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("claims changed\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// AC-12. Replays the memberships round-trip tests' cleanup, then reads the hook.
func TestRLS_StaffProjectionSurvivesTheMembershipsRoundTrip(t *testing.T) {
	requireHarness(t)
	auth := authAdminPool(t)
	ctx := context.Background()
	provider := staffMigrationProvider(t)
	membershipsV := membershipsMigrationVersion(t)

	t.Run("round_trip", func(t *testing.T) {
		reapplyHookMigrationOnCleanup(t, provider)
		t.Cleanup(func() {
			if _, err := provider.Up(context.Background()); err != nil {
				t.Errorf("restore memberships schema: %v", err)
			}
		})
		if _, err := provider.ApplyVersion(ctx, membershipsV, false); err != nil {
			t.Fatalf("roll back the memberships identity migration: %v", err)
		}
		if _, err := provider.ApplyVersion(ctx, membershipsV, true); err != nil {
			t.Fatalf("re-apply the memberships identity migration: %v", err)
		}
	})

	userID := uuid.NewString()
	seedStaff(t, userID)
	if v := appMetadataOf(t, mustHookClaims(t, auth, userID))["staff"]; v != true {
		t.Errorf("app_metadata.staff = %v after the round-trip cleanup, want true", v)
	}
}

// Boundary. Guard: fails if the EXISTS ignores user_id.
func TestRLS_CustomAccessTokenHookStaffRowForAnotherUser(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)

	staffUser, other := uuid.NewString(), uuid.NewString()
	seedStaff(t, staffUser)
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, staffUser); n != 1 {
		t.Fatalf("staff row for user A = %d, want 1", n)
	}

	got, want := hookRun(t, auth, other, nil)
	md := appMetadataOf(t, got)
	if v, has := md["staff"]; has {
		t.Errorf("user B app_metadata.staff = %v from user A's row, want absent", v)
	}
	if md["provider"] != "email" {
		t.Errorf("user B app_metadata.provider = %v, want email kept", md["provider"])
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claims\n got: %v\nwant: %v", got, want)
	}
}
