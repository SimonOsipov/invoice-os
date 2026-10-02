package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// reapplyMigration rolls the migration matching glob back and forward from the embedded SQL, so a
// test reads the shipped file and not a database migrated earlier.
func reapplyMigration(t *testing.T, glob string) {
	t.Helper()
	ctx := context.Background()
	provider, version := staffMigrationProvider(t), migrationVersion(t, glob)
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil && !errors.Is(err, goose.ErrNotApplied) {
		t.Fatalf("roll back %s: %v", glob, err)
	}
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("apply %s: %v", glob, err)
	}
}

// resetStaffMigration drops the table, forgets the ledger row and applies the Up again. It does not
// run the Down, so a Down or Up left broken by an earlier run cannot block it.
func resetStaffMigration(t *testing.T, provider *goose.Provider, version int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.super.Exec(ctx, `DROP TABLE IF EXISTS public.staff_members`); err != nil {
		t.Fatalf("drop staff_members: %v", err)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM goose_db_version WHERE version_id = $1`, version); err != nil {
		t.Fatalf("forget the staff migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("apply the staff migration: %v", err)
	}
}

func reapplyStaffMigration(t *testing.T) {
	t.Helper()
	resetStaffMigration(t, staffMigrationProvider(t), staffMigrationVersion(t))
}

// AC-1. Guard: fails if a grant to one of these roles is added.
func TestRLS_StaffMembersRefusesAppReaderAndAuthAdmin(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
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

// Guard: fails if GrantStaff drops ON CONFLICT or writes a second row.
func TestRLS_GrantStaffIsIdempotent(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	ctx := context.Background()
	userID := uuid.New()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, userID)
	})
	count := func() int {
		return mustCount(t, h.super, `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, userID)
	}
	if n := count(); n != 0 {
		t.Fatalf("staff_members rows for the user before the grant = %d, want 0", n)
	}

	for i := 1; i <= 2; i++ {
		if err := db.GrantStaff(ctx, os.Getenv("DATABASE_MIGRATION_URL"), userID); err != nil {
			t.Fatalf("GrantStaff call %d: %v", i, err)
		}
		if n := count(); n != 1 {
			t.Errorf("staff_members rows for the user after call %d = %d, want 1", i, n)
		}
	}
}

// AC-4.
func TestRLS_CustomAccessTokenHookProjectsStaffBesideTheTenant(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	// Another user's row: a strip that skipped on any row would keep the claim.
	seedStaff(t, uuid.NewString())
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n == 0 {
		t.Fatal("staff_members is empty")
	}

	for name, forged := range map[string]any{
		"bool_true": true, "string_true": "true", "number_1": 1,
		"bool_false": false, "json_null": nil, "object": map[string]any{"is": true}, "array": []any{true},
	} {
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
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
	useShippedGuard(t)
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
	reapplyStaffMigration(t)
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
			resetStaffMigration(t, provider, version)
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
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	for name, appMetadata := range map[string]any{
		"json_null": nil, "string": "x", "number": 7, "bool": true,
	} {
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
		// The tenant step joins a scalar and the tenant object into an array, and #- into an array errors.
		t.Run(name+"/non_staff_with_membership", func(t *testing.T) {
			userID := uuid.NewString()
			seedHookMembership(t, h.tenantA, userID, "active")
			got, _ := hookRun(t, auth, userID, func(c map[string]any) { c["app_metadata"] = appMetadata })
			want := roundTrip(t, map[string]any{"app_metadata": []any{appMetadata, map[string]any{"tenant_id": h.tenantA}}})
			if !reflect.DeepEqual(got["app_metadata"], want["app_metadata"]) {
				t.Errorf("app_metadata\n got: %v\nwant: %v", got["app_metadata"], want["app_metadata"])
			}
		})
	}
}

// AC-12. Replays the memberships round-trip tests' cleanup, then reads the hook.
func TestRLS_StaffProjectionSurvivesTheMembershipsRoundTrip(t *testing.T) {
	requireHarness(t)
	reapplyStaffMigration(t)
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
	reapplyStaffMigration(t)
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

// A write grant alone can hide behind RLS's own 42501, so the ACL is read directly.
func TestRLS_StaffMembersGrantsReachOnlyTheOwnerAndTheHookReader(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	ctx := context.Background()

	var owner string
	if err := h.super.QueryRow(ctx,
		`SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'staff_members'`,
	).Scan(&owner); err != nil {
		t.Fatalf("read the staff_members owner: %v", err)
	}
	if owner != "invoice_migrator" {
		t.Fatalf("staff_members owner = %q, want invoice_migrator", owner)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.table_privileges
		  WHERE table_schema = 'public' AND table_name = 'staff_members' AND grantee = $1`, owner); n == 0 {
		t.Fatal("the view lists no privilege for the owner, so an empty read below proves nothing")
	}

	collect := func(sql string) []string {
		t.Helper()
		rows, err := h.super.Query(ctx, sql, owner)
		if err != nil {
			t.Fatalf("read privileges: %v", err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("collect privileges: %v", err)
		}
		return got
	}
	if got := collect(`SELECT grantee || ':' || privilege_type FROM information_schema.table_privileges
	                    WHERE table_schema = 'public' AND table_name = 'staff_members' AND grantee <> $1 ORDER BY 1`); len(got) != 0 {
		t.Errorf("table-level privileges beyond the owner: %v", got)
	}
	want := []string{"auth_hook_reader:user_id:SELECT"}
	if got := collect(`SELECT grantee || ':' || column_name || ':' || privilege_type FROM information_schema.column_privileges
	                    WHERE table_schema = 'public' AND table_name = 'staff_members' AND grantee <> $1 ORDER BY 1`); !reflect.DeepEqual(got, want) {
		t.Errorf("column privileges beyond the owner = %v, want %v", got, want)
	}
}

// The staff step runs after the tenant step: it never changes which tenant projects.
func TestRLS_CustomAccessTokenHookStaffStepKeepsTheTenantRules(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name        string
		memberships map[string]string
		wantTenant  string
	}{
		{"two_active", map[string]string{h.tenantA: "active", h.tenantB: "active"}, ""},
		{"suspended_only", map[string]string{h.tenantA: "suspended"}, ""},
		{"no_membership", nil, ""},
		{"one_active_one_suspended", map[string]string{h.tenantA: "active", h.tenantB: "suspended"}, h.tenantA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			seedStaff(t, userID)
			for tenant, status := range tc.memberships {
				seedHookMembership(t, tenant, userID, status)
			}

			got, _ := hookRun(t, auth, userID, func(c map[string]any) {
				c["app_metadata"].(map[string]any)["tenant_id"] = "forged-tenant"
			})
			md := appMetadataOf(t, got)
			if md["staff"] != true {
				t.Errorf("app_metadata.staff = %v, want true: %v", md["staff"], md)
			}
			if tc.wantTenant == "" {
				if v, has := md["tenant_id"]; has {
					t.Errorf("app_metadata.tenant_id = %v, want absent", v)
				}
			} else if md["tenant_id"] != tc.wantTenant {
				t.Errorf("app_metadata.tenant_id = %v, want %s", md["tenant_id"], tc.wantTenant)
			}
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
		})
	}
}

// A user_id that matches no row, null or absent, earns no staff and keeps no forged one.
func TestRLS_CustomAccessTokenHookStripsStaffForANullOrAbsentUserID(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	seedStaff(t, uuid.NewString())
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n == 0 {
		t.Fatal("staff_members is empty")
	}

	for name, edit := range map[string]func(e, _ map[string]any){
		"null":   func(e, _ map[string]any) { e["user_id"] = nil },
		"absent": func(e, _ map[string]any) { delete(e, "user_id") },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := hookEvent(t, uuid.NewString())
			event := editEvent(t, raw, func(e, c map[string]any) {
				edit(e, c)
				c["app_metadata"].(map[string]any)["staff"] = true
			})
			md := appMetadataOf(t, outputClaims(t, callHook(t, auth, event)))
			if v, has := md["staff"]; has {
				t.Errorf("app_metadata.staff = %v survived a %s user_id", v, name)
			}
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
		})
	}
}

// Deleting the row cuts the account off at its next token, forged claim included.
func TestRLS_CustomAccessTokenHookStopsProjectingStaffWhenTheRowIsDeleted(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedStaff(t, userID)
	if v := appMetadataOf(t, mustHookClaims(t, auth, userID))["staff"]; v != true {
		t.Fatalf("app_metadata.staff = %v with the row present, want true", v)
	}

	if _, err := h.super.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete the staff row: %v", err)
	}
	got, _ := hookRun(t, auth, userID, func(c map[string]any) {
		c["app_metadata"].(map[string]any)["staff"] = true
	})
	if v, has := appMetadataOf(t, got)["staff"]; has {
		t.Errorf("app_metadata.staff = %v after the row was deleted, want absent", v)
	}
}
