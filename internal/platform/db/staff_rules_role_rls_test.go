package db_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

func rulesRoleMigrationVersion(t *testing.T) int64 {
	t.Helper()
	return migrationVersion(t, "*_staff_rules_role.sql")
}

// rulesRoleOf reads the column through to_jsonb, so a missing column is an assertion failure, not a 42703.
func rulesRoleOf(t *testing.T, userID string) (value, present bool) {
	t.Helper()
	var raw []byte
	if err := h.super.QueryRow(context.Background(),
		`SELECT to_jsonb(s) -> 'rules_role' FROM public.staff_members s WHERE s.user_id = $1`, userID,
	).Scan(&raw); err != nil {
		t.Fatalf("read the staff row of %s: %v", userID, err)
	}
	return string(raw) == "true", raw != nil
}

// requireRulesRoleFalse asserts the staff row exists with an explicit false, the positive half of a negative check.
func requireRulesRoleFalse(t *testing.T, userID string) {
	t.Helper()
	if v, present := rulesRoleOf(t, userID); !present || v {
		t.Errorf("staff_members.rules_role of %s = (present=%v, value=%v), want present and false", userID, present, v)
	}
}

// seedRulesStaff seeds a staff row with rules_role true. A missing column fails the test here and the
// test goes on, so the hook assertions are still reached.
func seedRulesStaff(t *testing.T, userID string) {
	t.Helper()
	seedStaff(t, userID)
	_, err := h.super.Exec(context.Background(),
		`UPDATE public.staff_members SET rules_role = true WHERE user_id = $1`, userID)
	switch {
	case err == nil:
	case pgCode(err) == "42703":
		t.Errorf("staff_members.rules_role does not exist: %v", err)
	default:
		t.Fatalf("set rules_role true: %v", err)
	}
}

func rulesRoleColumnCount(t *testing.T) int {
	t.Helper()
	return mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'staff_members' AND column_name = 'rules_role'`)
}

// restoreRulesRoleOnCleanup leaves the database at head after a test that rolls the migration back.
func restoreRulesRoleOnCleanup(t *testing.T, provider *goose.Provider, version int64) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
			t.Errorf("restore the rules-role migration: %v", err)
		}
	})
}

// AC-1.
func TestRLS_StaffRulesRoleDefaultsFalse(t *testing.T) {
	requireHarness(t)
	reapplyStaffMigration(t)
	ctx := context.Background()

	var dataType, nullable, dflt string
	if err := h.super.QueryRow(ctx,
		`SELECT data_type, is_nullable, coalesce(column_default, '') FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'staff_members' AND column_name = 'rules_role'`,
	).Scan(&dataType, &nullable, &dflt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read the rules_role column: %v", err)
	} else if errors.Is(err, pgx.ErrNoRows) {
		t.Error("staff_members has no rules_role column")
	} else if dataType != "boolean" || nullable != "NO" || dflt != "false" {
		t.Errorf("rules_role is %s nullable=%s default=%q, want boolean NOT NULL DEFAULT false", dataType, nullable, dflt)
	}

	userID := uuid.NewString()
	seedStaff(t, userID)
	requireRulesRoleFalse(t, userID)

	_, err := h.super.Exec(ctx, `UPDATE public.staff_members SET rules_role = NULL WHERE user_id = $1`, userID)
	if code := pgCode(err); code != "23502" {
		t.Errorf("UPDATE rules_role = NULL: SQLSTATE %q, want 23502: %v", code, err)
	}
}

// AC-1. A row that predates the Up reads false and earns no claim.
func TestRLS_StaffRulesRoleExistingRowReadsFalseAfterUp(t *testing.T) {
	requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	if n := rulesRoleColumnCount(t); n != 1 {
		t.Errorf("rules_role columns at head = %d, want 1", n)
	}
	version, provider := rulesRoleMigrationVersion(t), staffMigrationProvider(t)
	restoreRulesRoleOnCleanup(t, provider, version)
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the rules-role migration: %v", err)
	}
	if n := rulesRoleColumnCount(t); n != 0 {
		t.Fatalf("rules_role columns after the Down = %d, want 0", n)
	}

	userID := uuid.NewString()
	seedStaff(t, userID)
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("apply the rules-role migration: %v", err)
	}

	requireRulesRoleFalse(t, userID)
	md := appMetadataOf(t, mustHookClaims(t, auth, userID))
	if md["staff"] != true {
		t.Errorf("app_metadata.staff = %v, want true: %v", md["staff"], md)
	}
	if v, has := md["rules_role"]; has {
		t.Errorf("app_metadata.rules_role = %v for a row that predates the Up, want absent", v)
	}
}

// AC-2.
func TestRLS_CustomAccessTokenHookProjectsTheRulesRole(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedRulesStaff(t, userID)
	seedHookMembership(t, h.tenantA, userID, "active")

	got, in := hookRun(t, auth, userID, nil)
	wantMD := appMetadataOf(t, in)
	wantMD["tenant_id"], wantMD["staff"], wantMD["rules_role"] = h.tenantA, true, true

	if !reflect.DeepEqual(appMetadataOf(t, got), wantMD) {
		t.Errorf("app_metadata\n got: %v\nwant: %v", got["app_metadata"], wantMD)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("claims\n got: %v\nwant: %v", got, in)
	}
}

// AC-2. The negative key is paired with the row's explicit false.
func TestRLS_CustomAccessTokenHookStaffWithoutRulesRoleAddsNoRulesKey(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	userID := uuid.NewString()
	seedStaff(t, userID)
	requireRulesRoleFalse(t, userID)
	seedHookMembership(t, h.tenantA, userID, "active")

	got, in := hookRun(t, auth, userID, nil)
	wantMD := appMetadataOf(t, in)
	wantMD["tenant_id"], wantMD["staff"] = h.tenantA, true

	if v, has := appMetadataOf(t, got)["rules_role"]; has {
		t.Errorf("app_metadata.rules_role = %v for a staff row with rules_role false, want absent", v)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("claims\n got: %v\nwant: %v", got, in)
	}
}

// AC-2. The rules step never changes which tenant projects.
func TestRLS_CustomAccessTokenHookRulesRoleKeepsTheTenantRules(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		name        string
		memberships []string
		wantTenant  string
	}{
		{"zero_active", nil, ""},
		{"one_active", []string{h.tenantA}, h.tenantA},
		{"two_active", []string{h.tenantA, h.tenantB}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.NewString()
			seedRulesStaff(t, userID)
			for _, tenant := range tc.memberships {
				seedHookMembership(t, tenant, userID, "active")
			}

			got, _ := hookRun(t, auth, userID, func(c map[string]any) {
				c["app_metadata"].(map[string]any)["tenant_id"] = "forged-tenant"
			})
			md := appMetadataOf(t, got)
			if md["staff"] != true || md["rules_role"] != true {
				t.Errorf("app_metadata staff=%v rules_role=%v, want both true: %v", md["staff"], md["rules_role"], md)
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

// AC-3. A staff row that does not earn rules_role strips an incoming one; so does no row at all.
func TestRLS_CustomAccessTokenHookStripsAnUnearnedRulesRole(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	// Another user's rules row: a strip that skipped on any row would keep the claim.
	seedRulesStaff(t, uuid.NewString())
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members`); n == 0 {
		t.Fatal("staff_members is empty")
	}

	forgeries := map[string]any{
		"bool_true": true, "string_true": "true", "number_1": 1, "bool_false": false,
		"json_null": nil, "object": map[string]any{}, "array": []any{true},
	}
	for _, row := range []string{"no_staff_row", "staff_row_rules_false"} {
		for name, forged := range forgeries {
			t.Run(row+"/"+name, func(t *testing.T) {
				userID := uuid.NewString()
				if row == "staff_row_rules_false" {
					seedStaff(t, userID)
					requireRulesRoleFalse(t, userID)
				}
				got, in := hookRun(t, auth, userID, func(c map[string]any) {
					c["app_metadata"].(map[string]any)["rules_role"] = forged
				})
				inMD := appMetadataOf(t, in)
				if _, planted := inMD["rules_role"]; !planted {
					t.Fatal("the input carries no rules_role to strip")
				}

				if v, has := appMetadataOf(t, got)["rules_role"]; has {
					t.Errorf("app_metadata.rules_role = %v survived, want it removed", v)
				}
				delete(inMD, "rules_role")
				if row == "staff_row_rules_false" {
					inMD["staff"] = true
				}
				if !reflect.DeepEqual(got, in) {
					t.Errorf("claims other than rules_role changed\n got: %v\nwant: %v", got, in)
				}
			})
		}
	}
}

// AC-3. Boundary: a rules row is keyed on user_id, so another user's row grants and keeps nothing.
func TestRLS_CustomAccessTokenHookRulesRoleRowForAnotherUser(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	rulesUser, other := uuid.NewString(), uuid.NewString()
	seedRulesStaff(t, rulesUser)
	if v, present := rulesRoleOf(t, rulesUser); !present || !v {
		t.Fatalf("user B rules_role = (present=%v, value=%v), want present and true", present, v)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, other); n != 0 {
		t.Fatalf("staff rows for user A = %d, want 0", n)
	}

	for name, edit := range map[string]func(map[string]any){
		"clean_input": nil,
		"planted_input": func(c map[string]any) {
			md := c["app_metadata"].(map[string]any)
			md["staff"], md["rules_role"] = true, true
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := hookRun(t, auth, other, edit)
			md := appMetadataOf(t, got)
			for _, key := range []string{"staff", "rules_role"} {
				if v, has := md[key]; has {
					t.Errorf("user A app_metadata.%s = %v from user B's row, want absent", key, v)
				}
			}
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
		})
	}
}

// AC-3.
func TestRLS_CustomAccessTokenHookRulesRoleForANullOrAbsentUserID(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	seedRulesStaff(t, uuid.NewString())
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
				md := c["app_metadata"].(map[string]any)
				md["staff"], md["rules_role"] = true, true
			})
			md := appMetadataOf(t, outputClaims(t, callHook(t, auth, event)))
			for _, key := range []string{"staff", "rules_role"} {
				if v, has := md[key]; has {
					t.Errorf("app_metadata.%s = %v survived a %s user_id", key, v, name)
				}
			}
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
		})
	}
}

// AC-4. A non-object app_metadata is replaced, never extended or errored on.
// ceiling: an array app_metadata errors in the tenant step (22P02, since the staff migration); GoTrue sends an object.
func TestRLS_CustomAccessTokenHookRulesRoleOverNonObjectAppMetadata(t *testing.T) {
	requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)

	for name, appMetadata := range map[string]any{
		"absent": "<absent>", "json_null": nil, "string": "x", "number": 7, "bool": true,
	} {
		edit := func(c map[string]any) {
			if s, isStr := appMetadata.(string); isStr && s == "<absent>" {
				delete(c, "app_metadata")
				return
			}
			c["app_metadata"] = appMetadata
		}
		t.Run(name+"/rules_role", func(t *testing.T) {
			userID := uuid.NewString()
			seedRulesStaff(t, userID)
			got, _ := hookRun(t, auth, userID, edit)
			if md, ok := got["app_metadata"].(map[string]any); !ok || !reflect.DeepEqual(md, map[string]any{"staff": true, "rules_role": true}) {
				t.Errorf("app_metadata = %v, want {staff: true, rules_role: true}", got["app_metadata"])
			}
		})
		t.Run(name+"/staff_only", func(t *testing.T) {
			userID := uuid.NewString()
			seedStaff(t, userID)
			requireRulesRoleFalse(t, userID)
			got, _ := hookRun(t, auth, userID, edit)
			if md, ok := got["app_metadata"].(map[string]any); !ok || !reflect.DeepEqual(md, map[string]any{"staff": true}) {
				t.Errorf("app_metadata = %v, want {staff: true}", got["app_metadata"])
			}
		})
	}
}

// AC-5. The ACL is read directly; a refusal alone could be RLS's own 42501.
func TestRLS_StaffRulesRoleRefusesEveryNonOwnerWrite(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedStaff(t, userID)
	requireRulesRoleFalse(t, userID)

	stmts := []struct{ verb, sql string }{
		{"UPDATE rules_role", `UPDATE public.staff_members SET rules_role = true`},
		{"INSERT rules_role", `INSERT INTO public.staff_members (user_id, rules_role) VALUES (gen_random_uuid(), true)`},
		{"SELECT rules_role", `SELECT rules_role FROM public.staff_members`},
	}
	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{
		{"invoice_app", h.app},
		{"invoice_tenant_reader", h.reader},
		{"supabase_auth_admin", authAdminPool(t)},
	} {
		for _, s := range stmts {
			_, err := role.pool.Exec(ctx, s.sql)
			if code := pgCode(err); code != sqlstateInsufficientPrivilege {
				t.Errorf("%s %s: SQLSTATE %q, want %s: %v", role.name, s.verb, code, sqlstateInsufficientPrivilege, err)
			}
		}
	}

	for _, s := range stmts[:2] {
		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("SET LOCAL ROLE auth_hook_reader: %v", err)
		}
		_, err = tx.Exec(ctx, s.sql)
		_ = tx.Rollback(ctx)
		if code := pgCode(err); code != sqlstateInsufficientPrivilege {
			t.Errorf("auth_hook_reader %s: SQLSTATE %q, want %s: %v", s.verb, code, sqlstateInsufficientPrivilege, err)
		}
	}
	requireRulesRoleFalse(t, userID)
}

// AC-5. auth_hook_reader reads rules_role and user_id and nothing else.
func TestRLS_StaffRulesRoleHookReaderReadsTheColumn(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedRulesStaff(t, userID)

	asHookReader := func(stmt string, args ...any) (rows pgx.Rows, done func(), err error) {
		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		done = func() { _ = tx.Rollback(ctx) }
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
			done()
			t.Fatalf("SET LOCAL ROLE auth_hook_reader: %v", err)
		}
		rows, err = tx.Query(ctx, stmt, args...)
		return rows, done, err
	}

	rows, done, err := asHookReader(`SELECT rules_role FROM public.staff_members WHERE user_id = $1`, userID)
	if err != nil {
		t.Errorf("auth_hook_reader SELECT rules_role: %v", err)
	} else {
		got, err := pgx.CollectRows(rows, pgx.RowTo[bool])
		if err != nil {
			t.Errorf("auth_hook_reader SELECT rules_role: %v", err)
		} else if !reflect.DeepEqual(got, []bool{true}) {
			t.Errorf("auth_hook_reader reads rules_role %v, want [true]", got)
		}
	}
	done()

	for _, c := range []struct{ what, sql string }{
		{"SELECT created_at", `SELECT created_at FROM public.staff_members`},
		{"SELECT *", `SELECT * FROM public.staff_members`},
		{"SELECT user_id, rules_role, created_at", `SELECT user_id, rules_role, created_at FROM public.staff_members`},
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

// AC-7. Drives the shipped Down and Up through goose for the rules-role version.
func TestRLS_StaffRulesRoleDownRestoresTheStaffOnlyHook(t *testing.T) {
	h := requireHarness(t)
	reapplyStaffMigration(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedRulesStaff(t, userID)
	seedHookMembership(t, h.tenantA, userID, "active")

	check := func(stage string, wantRules bool) {
		t.Helper()
		md := appMetadataOf(t, mustHookClaims(t, auth, userID))
		if md["tenant_id"] != h.tenantA || md["staff"] != true {
			t.Errorf("%s: app_metadata tenant_id=%v staff=%v, want %s and true", stage, md["tenant_id"], md["staff"], h.tenantA)
		}
		if got := md["rules_role"] == true; got != wantRules {
			t.Errorf("%s: app_metadata.rules_role = %v, want present=%v", stage, md["rules_role"], wantRules)
		}
	}
	check("before Down", true)
	if n := rulesRoleColumnCount(t); n != 1 {
		t.Errorf("before Down: rules_role columns = %d, want 1", n)
	}
	if _, _, grants := staffFootprint(t); grants != 2 {
		t.Errorf("before Down: auth_hook_reader column grants = %d, want 2", grants)
	}

	version, provider := rulesRoleMigrationVersion(t), staffMigrationProvider(t)
	restoreRulesRoleOnCleanup(t, provider, version)
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the rules-role migration: %v", err)
	}
	check("after Down", false)
	if n := rulesRoleColumnCount(t); n != 0 {
		t.Errorf("after Down: rules_role columns = %d, want 0", n)
	}
	if tables, policies, grants := staffFootprint(t); tables != 1 || policies != 1 || grants != 1 {
		t.Errorf("after Down: table=%d policy=%d column grants=%d, want 1 each", tables, policies, grants)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the rules-role migration: %v", err)
	}
	requireRulesRoleFalse(t, userID)
	check("after Up", false)
	if _, err := h.super.Exec(ctx, `UPDATE public.staff_members SET rules_role = true WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("set rules_role true after the Up: %v", err)
	}
	check("after Up and set", true)
	if _, _, grants := staffFootprint(t); grants != 2 {
		t.Errorf("after Up: auth_hook_reader column grants = %d, want 2", grants)
	}
}
