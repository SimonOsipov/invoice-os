package db_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const devHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

// Runbook U1 statements, run verbatim; subtask 07 copies them into the runbook byte for byte.
const (
	setHouseRowSQL       = `INSERT INTO public.house_company (tenant_id) VALUES ('<tenant-id>');`
	renameHouseTenantSQL = `UPDATE public.tenants SET name = 'ASComply' WHERE id = '<tenant-id>';`
)

func requireHouseTable(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regclass('public.house_company')::text`).Scan(&reg); err != nil {
		t.Fatalf("read to_regclass: %v", err)
	}
	if reg == nil {
		t.Fatal("public.house_company does not exist: Migration A is not applied")
	}
}

// adoptHouseTenant returns the committed house tenant, writing the dev house row only when the table is empty.
func adoptHouseTenant(t *testing.T) string {
	t.Helper()
	requireHouseTable(t)
	ctx := context.Background()
	read := func() (string, error) {
		var id string
		err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
		return id, err
	}
	id, err := read()
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := h.super.Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := h.super.Exec(ctx,
			`INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		id, err = read()
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return id
}

// rollBackHouseCompany runs Migration A's Down. The returned func and t.Cleanup re-apply the Up.
func rollBackHouseCompany(t *testing.T) (reapply func()) {
	t.Helper()
	ctx := context.Background()
	provider, version := staffMigrationProvider(t), houseMigrationVersion(t)
	applied := true
	apply := func() error {
		if applied {
			return nil
		}
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			return err
		}
		applied = true
		return nil
	}
	t.Cleanup(func() {
		if err := apply(); err != nil {
			t.Errorf("re-apply Migration A: %v", err)
		}
	})
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back Migration A: %v", err)
	}
	applied = false
	return func() {
		t.Helper()
		if err := apply(); err != nil {
			t.Fatalf("re-apply Migration A: %v", err)
		}
	}
}

// refusedBy runs sql under a savepoint and asserts the SQLSTATE and the constraint it names.
func refusedBy(t *testing.T, tx pgx.Tx, what, code, constraint, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `SAVEPOINT refusal`); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err := tx.Exec(ctx, sql, args...)
	if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT refusal`); rerr != nil {
		t.Fatalf("rollback to savepoint: %v", rerr)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: want SQLSTATE %s on %s, got %v", what, code, constraint, err)
		return
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Errorf("%s: want SQLSTATE %s on %s, got %s on %q (%s)", what, code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func txExec(t *testing.T, tx pgx.Tx, sql string, args ...any) pgconn.CommandTag {
	t.Helper()
	tag, err := tx.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return tag
}

// hookInTx runs the hook as supabase_auth_admin inside a superuser tx, with no tenant setting.
func hookInTx(t *testing.T, tx pgx.Tx, userID string) map[string]any {
	t.Helper()
	ctx := context.Background()
	raw, _ := hookEvent(t, userID)
	txExec(t, tx, `SET LOCAL ROLE supabase_auth_admin`)
	var out []byte
	err := tx.QueryRow(ctx, `select public.custom_access_token_hook($1::jsonb)`, string(raw)).Scan(&out)
	txExec(t, tx, `RESET ROLE`)
	if err != nil {
		t.Fatalf("call custom_access_token_hook as supabase_auth_admin: %v", err)
	}
	return outputClaims(t, decodeJSON(t, out))
}

// isHouseMemberAsMigrator asks as invoice_migrator with app.current_tenant unset.
func isHouseMemberAsMigrator(t *testing.T, userID string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var setting string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('app.current_tenant', true), '')`).Scan(&setting); err != nil {
		t.Fatalf("read app.current_tenant: %v", err)
	}
	if setting != "" {
		t.Fatalf("app.current_tenant = %q, want unset", setting)
	}
	var got bool
	if err := tx.QueryRow(ctx, `SELECT public.is_house_member($1)`, userID).Scan(&got); err != nil {
		t.Fatalf("invoice_migrator calls is_house_member: %v", err)
	}
	return got
}

func plantStaffClaims(c map[string]any) {
	md := c["app_metadata"].(map[string]any)
	md["staff"], md["rules_role"] = true, true
}

func requireNoKeys(t *testing.T, md map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, has := md[k]; has {
			t.Errorf("app_metadata.%s = %v, want absent", k, v)
		}
	}
}

// AC-1.
func TestRLS_HouseCompanyStartsEmpty(t *testing.T) {
	requireHarness(t)
	reapply := rollBackHouseCompany(t)
	reapply()

	if n := mustCount(t, h.super, `SELECT count(*) FROM public.house_company`); n != 0 {
		t.Errorf("house_company rows after the Up = %d, want 0: production's row is written by hand", n)
	}
}

// AC-1.
func TestRLS_HouseCompanyHoldsAtMostOneRow(t *testing.T) {
	requireHarness(t)
	requireHouseTable(t)
	tx := revokeProbeTx(t)
	txExec(t, tx, `DELETE FROM public.house_company`)
	txExec(t, tx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, h.tenantA)
	if n := mustCount(t, tx, `SELECT count(*) FROM public.house_company`); n != 1 {
		t.Fatalf("house_company rows = %d, want the one just inserted", n)
	}

	refusedBy(t, tx, "second row, another tenant", "23505", "house_company_pkey",
		`INSERT INTO public.house_company (tenant_id) VALUES ($1)`, h.tenantB)
	refusedBy(t, tx, "singleton = false", "23514", "house_company_singleton_check",
		`INSERT INTO public.house_company (singleton, tenant_id) VALUES (false, $1)`, h.tenantB)
	if n := mustCount(t, tx, `SELECT count(*) FROM public.house_company`); n != 1 {
		t.Errorf("house_company rows after the refusals = %d, want 1", n)
	}
}

// AC-1.
func TestRLS_HouseCompanyRefusesAnUnknownTenant(t *testing.T) {
	requireHarness(t)
	requireHouseTable(t)
	tx := revokeProbeTx(t)
	txExec(t, tx, `DELETE FROM public.house_company`)
	txExec(t, tx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, h.tenantA)
	txExec(t, tx, `DELETE FROM public.house_company`)

	refusedBy(t, tx, "tenant that does not exist", "23503", "house_company_tenant_id_fkey",
		`INSERT INTO public.house_company (tenant_id) VALUES ($1)`, uuid.NewString())
	if n := mustCount(t, tx, `SELECT count(*) FROM public.house_company`); n != 0 {
		t.Errorf("house_company rows after the refusal = %d, want 0", n)
	}
}

// AC-2.
func TestRLS_HouseCompanyGrantsReachOnlyTheHookReader(t *testing.T) {
	requireHarness(t)
	adoptHouseTenant(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT column_name, privilege_type FROM information_schema.column_privileges
		  WHERE table_schema = 'public' AND table_name = 'house_company' AND grantee = 'auth_hook_reader'`)
	if err != nil {
		t.Fatalf("read auth_hook_reader column privileges: %v", err)
	}
	type grant struct{ Column, Privilege string }
	var got []grant
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.Column, &g.Privilege); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, g)
	}
	rows.Close()
	if want := []grant{{"tenant_id", "SELECT"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("auth_hook_reader column privileges = %v, want exactly %v", got, want)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM information_schema.table_privileges
		  WHERE table_schema = 'public' AND table_name = 'house_company' AND grantee = 'auth_hook_reader'`); n != 0 {
		t.Errorf("auth_hook_reader table-level privileges = %d, want 0", n)
	}

	for _, role := range []string{"invoice_app", "invoice_tenant_reader", "supabase_auth_admin", "PUBLIC"} {
		n := mustCount(t, h.super,
			`SELECT (SELECT count(*) FROM information_schema.column_privileges WHERE table_schema = 'public' AND table_name = 'house_company' AND grantee = $1)
			      + (SELECT count(*) FROM information_schema.table_privileges  WHERE table_schema = 'public' AND table_name = 'house_company' AND grantee = $1)`, role)
		if n != 0 {
			t.Errorf("%s holds %d privileges on house_company, want 0", role, n)
		}
	}

	var rls, force bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.house_company'::regclass`).Scan(&rls, &force); err != nil {
		t.Fatalf("read relrowsecurity: %v", err)
	}
	if !rls || force {
		t.Errorf("house_company relrowsecurity=%v relforcerowsecurity=%v, want true and false", rls, force)
	}
	var policies []string
	pr, err := h.super.Query(ctx,
		`SELECT policyname || '/' || cmd || '/' || roles::text || '/' || qual FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = 'house_company'`)
	if err != nil {
		t.Fatalf("read pg_policies: %v", err)
	}
	policies, err = pgx.CollectRows(pr, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect pg_policies: %v", err)
	}
	if want := []string{"house_hook_lookup/SELECT/{auth_hook_reader}/true"}; !reflect.DeepEqual(policies, want) {
		t.Errorf("house_company policies = %v, want exactly %v", policies, want)
	}

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"invoice_app", h.app}, {"invoice_tenant_reader", h.reader}, {"supabase_auth_admin", authAdminPool(t)}} {
		_, err := role.pool.Exec(ctx, `SELECT tenant_id FROM public.house_company`)
		assertPgRefusal(t, role.name+" SELECT on house_company", err, sqlstateInsufficientPrivilege, "permission denied for table house_company")
	}

	// Positive pair: the hook reader sees the row, through tenant_id only.
	tx := revokeProbeTx(t)
	txExec(t, tx, `SET LOCAL ROLE auth_hook_reader`)
	if n := mustCount(t, tx, `SELECT count(*) FROM (SELECT tenant_id FROM public.house_company) x`); n != 1 {
		t.Errorf("auth_hook_reader reads %d house rows, want 1", n)
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT singleton::text FROM public.house_company`).Scan(&id)
	assertPgRefusal(t, "auth_hook_reader SELECT singleton", err, sqlstateInsufficientPrivilege, "permission denied for table house_company")
}

// AC-3.
func TestRLS_IsHouseMemberIsDefinerOwnedByHookReader(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	const sig = "public.is_house_member(uuid)"
	var (
		secdef    bool
		owner     string
		config    []string
		grantees  []string
		publicAny bool
	)
	err := h.super.QueryRow(ctx, `
		SELECT p.prosecdef, pg_get_userbyid(p.proowner), coalesce(p.proconfig, '{}'),
		       coalesce((SELECT array_agg(pg_get_userbyid(a.grantee) || '=' || a.privilege_type ORDER BY 1)
		                 FROM aclexplode(p.proacl) a WHERE a.grantee <> 0), '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
		FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, sig,
	).Scan(&secdef, &owner, &config, &grantees, &publicAny)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("%s does not exist", sig)
	}
	if err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}
	if !secdef {
		t.Error("prosecdef = false, want SECURITY DEFINER")
	}
	if owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", owner)
	}
	if !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", config)
	}
	if publicAny {
		t.Error("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	for role, want := range map[string]bool{
		"invoice_migrator": true, "invoice_app": false, "invoice_tenant_reader": false, "supabase_auth_admin": false,
	} {
		var has bool
		if err := h.super.QueryRow(ctx, `SELECT has_function_privilege($1, $2, 'EXECUTE')`, role, sig).Scan(&has); err != nil {
			t.Fatalf("has_function_privilege %s: %v", role, err)
		}
		if has != want {
			t.Errorf("%s holds EXECUTE on %s = %v, want %v", role, sig, has, want)
		}
	}
}

// AC-3. The migrator-owned rule functions run with no tenant, which is why the check lives in a hook-reader function.
func TestRLS_IsHouseMemberReadsWithoutATenantSetting(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)

	cases := []struct {
		name   string
		tenant string
		status string // "" = no membership
		want   bool
	}{
		{"active_house_member", house, "active", true},
		{"suspended_house_member", house, "suspended", false},
		{"invited_house_member", house, "invited", false},
		{"customer_member", h.tenantA, "active", false},
		{"stranger", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := uuid.NewString()
			if tc.status != "" {
				seedHookMembership(t, tc.tenant, user, tc.status)
			}
			if got := isHouseMemberAsMigrator(t, user); got != tc.want {
				t.Errorf("is_house_member = %v, want %v", got, tc.want)
			}
		})
	}
}

// AC-3.
func TestRLS_AppCannotExecuteIsHouseMember(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	member := uuid.NewString()
	seedHookMembership(t, house, member, "active")

	if !isHouseMemberAsMigrator(t, member) {
		t.Fatal("control: invoice_migrator gets false for an active house member")
	}
	var got bool
	err := h.app.QueryRow(context.Background(), `SELECT public.is_house_member($1)`, member).Scan(&got)
	assertPgRefusal(t, "invoice_app executes is_house_member", err, sqlstateInsufficientPrivilege, "permission denied for function is_house_member")
}

// AC-4.
func TestRLS_CustomAccessTokenHookProjectsStaffForAnActiveHouseMember(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	user := uuid.NewString()
	seedHookMembership(t, house, user, "active")

	got, want := hookRun(t, auth, user, nil)
	md := appMetadataOf(t, want)
	md["tenant_id"], md["staff"] = house, true

	if gotMD := appMetadataOf(t, got); gotMD["staff"] != true || gotMD["tenant_id"] != house {
		t.Errorf("app_metadata staff=%v tenant_id=%v, want true and %s", gotMD["staff"], gotMD["tenant_id"], house)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claims\n got: %v\nwant: %v", got, want)
	}
}

// AC-4. The active house member is the control: a strip that always fires would fail it.
func TestRLS_CustomAccessTokenHookStripsStaffForAnInactiveHouseMember(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)

	for _, tc := range []struct {
		status    string
		wantStaff bool
	}{{"active", true}, {"suspended", false}, {"invited", false}} {
		t.Run(tc.status, func(t *testing.T) {
			user := uuid.NewString()
			seedHookMembership(t, house, user, tc.status)
			got, in := hookRun(t, auth, user, plantStaffClaims)
			if _, planted := appMetadataOf(t, in)["staff"]; !planted {
				t.Fatal("the input carries no staff claim to strip")
			}

			md := appMetadataOf(t, got)
			if (md["staff"] == true) != tc.wantStaff {
				t.Errorf("app_metadata.staff = %v, want present=%v", md["staff"], tc.wantStaff)
			}
			requireNoKeys(t, md, "rules_role")
			if !tc.wantStaff {
				requireNoKeys(t, md, "staff")
				delete(appMetadataOf(t, in), "staff")
				delete(appMetadataOf(t, in), "rules_role")
				if !reflect.DeepEqual(got, in) {
					t.Errorf("claims other than staff and rules_role changed\n got: %v\nwant: %v", got, in)
				}
			}
		})
	}
}

// AC-4.
func TestRLS_CustomAccessTokenHookStripsStaffForACustomerMember(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	staff := uuid.NewString()
	seedHookMembership(t, house, staff, "active")
	if md := appMetadataOf(t, mustHookClaims(t, auth, staff)); md["staff"] != true {
		t.Fatalf("control: the house member's app_metadata.staff = %v, want true", md["staff"])
	}

	user := uuid.NewString()
	seedHookMembership(t, h.tenantA, user, "active")
	got, _ := hookRun(t, auth, user, plantStaffClaims)

	md := appMetadataOf(t, got)
	requireNoKeys(t, md, "staff", "rules_role")
	if md["tenant_id"] != h.tenantA {
		t.Errorf("app_metadata.tenant_id = %v, want %s", md["tenant_id"], h.tenantA)
	}
}

// AC-4.
func TestRLS_CustomAccessTokenHookStripsRulesRoleForAHouseMember(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	user := uuid.NewString()
	seedHookMembership(t, house, user, "active")

	got, in := hookRun(t, auth, user, func(c map[string]any) {
		c["app_metadata"].(map[string]any)["rules_role"] = true
	})
	if _, planted := appMetadataOf(t, in)["rules_role"]; !planted {
		t.Fatal("the input carries no rules_role to strip")
	}

	md := appMetadataOf(t, got)
	if md["staff"] != true {
		t.Errorf("app_metadata.staff = %v, want true", md["staff"])
	}
	requireNoKeys(t, md, "rules_role")
}

// AC-4. Rolled back: the committed house row stays.
func TestRLS_CustomAccessTokenHookMakesNobodyStaffWithAnEmptyHouseTable(t *testing.T) {
	requireHarness(t)
	requireHouseTable(t)
	tx := revokeProbeTx(t)
	user := uuid.NewString()
	txExec(t, tx, `DELETE FROM public.house_company`)
	txExec(t, tx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, h.tenantA)
	txExec(t, tx, `INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, h.tenantA, user)

	if md := appMetadataOf(t, hookInTx(t, tx, user)); md["staff"] != true {
		t.Fatalf("control: with the house row, app_metadata.staff = %v, want true", md["staff"])
	}
	txExec(t, tx, `DELETE FROM public.house_company`)

	md := appMetadataOf(t, hookInTx(t, tx, user))
	requireNoKeys(t, md, "staff")
	if md["tenant_id"] != h.tenantA {
		t.Errorf("app_metadata.tenant_id = %v, want the tenant step unchanged (%s)", md["tenant_id"], h.tenantA)
	}
}

// AC-4.
func TestRLS_CustomAccessTokenHookStripsStaffForANullOrAbsentUserID(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	member := uuid.NewString()
	seedHookMembership(t, house, member, "active")
	if md := appMetadataOf(t, mustHookClaims(t, auth, member)); md["staff"] != true {
		t.Fatalf("control: the house member's app_metadata.staff = %v, want true", md["staff"])
	}

	for name, edit := range map[string]func(e map[string]any){
		"null":   func(e map[string]any) { e["user_id"] = nil },
		"absent": func(e map[string]any) { delete(e, "user_id") },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := hookEvent(t, uuid.NewString())
			event := editEvent(t, raw, func(e, c map[string]any) {
				edit(e)
				plantStaffClaims(c)
			})
			md := appMetadataOf(t, outputClaims(t, callHook(t, auth, event)))
			requireNoKeys(t, md, "staff", "rules_role")
			if md["provider"] != "email" {
				t.Errorf("app_metadata.provider = %v, want email kept", md["provider"])
			}
		})
	}
}

// AC-4. No array case: the unchanged tenant step raises 22P02 on an array app_metadata before the house step runs.
func TestRLS_CustomAccessTokenHookHouseStepIsTotalOverNonObjectAppMetadata(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)

	for name, appMetadata := range map[string]any{"json_null": nil, "string": "x", "number": 7, "bool": true} {
		t.Run(name, func(t *testing.T) {
			user := uuid.NewString()
			seedHookMembership(t, house, user, "active")
			got, _ := hookRun(t, auth, user, func(c map[string]any) { c["app_metadata"] = appMetadata })

			md, ok := got["app_metadata"].(map[string]any)
			if !ok || md["staff"] != true {
				t.Errorf("app_metadata = %v, want an object with staff true", got["app_metadata"])
			}
		})
	}
}

// AC-4. Provisioning joins no house tenant; the house row exists, so an absent claim is not an empty-table artefact.
func TestRLS_ProvisionWorkspaceGrantsNoStaff(t *testing.T) {
	requireHarness(t)
	adoptHouseTenant(t)
	useShippedGuard(t)
	auth := authAdminPool(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	cleanupTenant(t, tenantID)
	a := newProvisionArgs(tenantID)
	before := mustCount(t, h.super, `SELECT count(*) FROM public.house_company`)

	if err := provisionAs(ctx, h.app, tenantID, a); err != nil {
		t.Fatalf("provision_workspace as invoice_app: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM public.house_company`); n != before {
		t.Errorf("house_company rows = %d after provisioning, want %d", n, before)
	}
	md := appMetadataOf(t, mustHookClaims(t, auth, a.userID))
	if md["tenant_id"] != tenantID {
		t.Fatalf("app_metadata.tenant_id = %v, want the provisioned %s", md["tenant_id"], tenantID)
	}
	requireNoKeys(t, md, "staff")
}

// AC-5.
func TestRLS_CustomAccessTokenHookKeepsStaffForAHouseMemberOfACustomer(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	user := uuid.NewString()
	seedHookMembership(t, house, user, "active")
	seedHookMembership(t, h.tenantA, user, "active")

	got, _ := hookRun(t, auth, user, func(c map[string]any) {
		c["app_metadata"].(map[string]any)["tenant_id"] = "forged-tenant"
	})

	md := appMetadataOf(t, got)
	if md["staff"] != true {
		t.Errorf("app_metadata.staff = %v, want true", md["staff"])
	}
	requireNoKeys(t, md, "tenant_id")
}

// AC-6. Drives the shipped Down and Up through goose.
func TestRLS_HouseCompanyDownRestoresTheRulesRoleHook(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	auth := authAdminPool(t)
	ctx := context.Background()
	user := uuid.NewString()
	seedHookMembership(t, house, user, "active")

	hookDef := func() string {
		t.Helper()
		var def string
		if err := h.super.QueryRow(ctx,
			`SELECT pg_get_functiondef('public.custom_access_token_hook(jsonb)'::regprocedure)`).Scan(&def); err != nil {
			t.Fatalf("read the hook body: %v", err)
		}
		return def
	}
	exists := func(sql string) bool {
		t.Helper()
		return mustCount(t, h.super, sql) == 1
	}
	const (
		tableThere = `SELECT count(*) FROM pg_class WHERE oid = to_regclass('public.house_company')`
		fnThere    = `SELECT count(*) FROM pg_proc WHERE oid = to_regprocedure('public.is_house_member(uuid)')`
	)

	if def := hookDef(); !strings.Contains(def, "is_house_member") || strings.Contains(def, "staff_members") {
		t.Errorf("before the Down the hook body should call is_house_member and not read staff_members:\n%s", def)
	}
	if md := appMetadataOf(t, mustHookClaims(t, auth, user)); md["staff"] != true {
		t.Errorf("before the Down: app_metadata.staff = %v, want true", md["staff"])
	}

	reapply := rollBackHouseCompany(t)
	if exists(tableThere) || exists(fnThere) {
		t.Errorf("after the Down: house_company present=%v, is_house_member present=%v, want both gone", exists(tableThere), exists(fnThere))
	}
	if def := hookDef(); !strings.Contains(def, "staff_members") || strings.Contains(def, "is_house_member") {
		t.Errorf("after the Down the hook body should read staff_members and not call is_house_member:\n%s", def)
	}

	reapply()
	if !exists(tableThere) || !exists(fnThere) {
		t.Fatalf("after the Up: house_company present=%v, is_house_member present=%v, want both", exists(tableThere), exists(fnThere))
	}
	if _, err := h.super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, house); err != nil {
		t.Fatalf("write the house row after the Up: %v", err)
	}
	if md := appMetadataOf(t, mustHookClaims(t, auth, user)); md["staff"] != true || md["tenant_id"] != house {
		t.Errorf("after the Up: app_metadata staff=%v tenant_id=%v, want true and %s", md["staff"], md["tenant_id"], house)
	}
}

// AC-10. The U1 statements run verbatim in a rolled-back tx, as the superuser does by hand.
func TestRLS_HouseRunbookStatementsNameTheHouseCompany(t *testing.T) {
	requireHarness(t)
	requireHouseTable(t)
	tx := revokeProbeTx(t)
	tenant, member := uuid.NewString(), uuid.NewString()
	txExec(t, tx, `DELETE FROM public.house_company`)
	txExec(t, tx, `INSERT INTO tenants (id, name) VALUES ($1, 'Pre-rename tenant')`, tenant)
	txExec(t, tx, `INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, tenant, member)

	if md := appMetadataOf(t, hookInTx(t, tx, member)); md["staff"] == true {
		t.Fatal("control: the member is staff before the house row is written")
	}
	for _, c := range []struct{ stmt, tag string }{
		{setHouseRowSQL, "INSERT 0 1"},
		{renameHouseTenantSQL, "UPDATE 1"},
	} {
		if got := txExec(t, tx, strings.Replace(c.stmt, "<tenant-id>", tenant, 1)).String(); got != c.tag {
			t.Errorf("runbook statement %q answered %q, want %q", c.stmt[:6], got, c.tag)
		}
	}

	var name string
	if err := tx.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, tenant).Scan(&name); err != nil {
		t.Fatalf("read the tenant name: %v", err)
	}
	if name != "ASComply" {
		t.Errorf("tenant name = %q, want ASComply", name)
	}
	if md := appMetadataOf(t, hookInTx(t, tx, member)); md["staff"] != true {
		t.Errorf("after the runbook: app_metadata.staff = %v, want true", md["staff"])
	}
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

func mustHookClaims(t *testing.T, auth *pgxpool.Pool, userID string) map[string]any {
	t.Helper()
	got, _ := hookRun(t, auth, userID, nil)
	return got
}
