package db_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const staffReadTenantsSig = "public.staff_read_tenants(uuid)"

// staffReadMigrationVersions lists every migration that creates a staff_read_ function, newest first.
func staffReadMigrationVersions(t *testing.T) []int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	var versions []int64
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !bytes.Contains(raw, []byte("CREATE FUNCTION public.staff_read_")) {
			continue
		}
		v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("parse goose version out of %q: %v", f, err)
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	return versions
}

type staffReadFn struct {
	sig, name, owner string
	secdef           bool
	config           []string
	argTypes         []string
	firstArgName     string
	publicExecute    bool
}

// staffReadFunctions reads every public.staff_read_% function from the catalog.
func staffReadFunctions(t *testing.T) []staffReadFn {
	t.Helper()
	rows, err := h.super.Query(context.Background(), `
		SELECT p.oid::regprocedure::text, p.proname, pg_get_userbyid(p.proowner), p.prosecdef, coalesce(p.proconfig, '{}'),
		       ARRAY(SELECT format_type(o, NULL) FROM unnest(string_to_array(p.proargtypes::text, ' ')::oid[]) WITH ORDINALITY u(o, n) ORDER BY n),
		       coalesce(p.proargnames[1], ''),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
		FROM pg_proc p
		WHERE p.pronamespace = 'public'::regnamespace AND p.proname LIKE 'staff\_read\_%'
		ORDER BY p.proname, p.oid`)
	if err != nil {
		t.Fatalf("read staff_read_ functions: %v", err)
	}
	fns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffReadFn, error) {
		var f staffReadFn
		return f, r.Scan(&f.sig, &f.name, &f.owner, &f.secdef, &f.config, &f.argTypes, &f.firstArgName, &f.publicExecute)
	})
	if err != nil {
		t.Fatalf("collect staff_read_ functions: %v", err)
	}
	return fns
}

func requireStaffReadFunctions(t *testing.T) []staffReadFn {
	t.Helper()
	fns := staffReadFunctions(t)
	if len(fns) == 0 {
		t.Fatal("no public.staff_read_ function exists, want at least one: the staff_read_tenants migration is not applied")
	}
	return fns
}

func requireStaffReadTenants(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, staffReadTenantsSig).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure: %v", err)
	}
	if reg == nil {
		t.Fatalf("function %s not found: the staff_read_tenants migration is not applied", staffReadTenantsSig)
	}
}

// staffReadCall calls fn with $1 as the actor and NULL for every later argument.
func staffReadCall(fn staffReadFn) string {
	parts := []string{"$1::uuid"}
	for _, ty := range fn.argTypes[1:] {
		parts = append(parts, "NULL::"+ty)
	}
	return fmt.Sprintf("SELECT * FROM public.%s(%s)", pgx.Identifier{fn.name}.Sanitize(), strings.Join(parts, ", "))
}

// AC-1.
func TestRLS_EveryStaffReadFunctionIsDefinerOwnedByStaffReader(t *testing.T) {
	requireHarness(t)
	fns := requireStaffReadFunctions(t)
	for _, fn := range fns {
		if !fn.secdef {
			t.Errorf("%s: prosecdef = false, want SECURITY DEFINER", fn.sig)
		}
		if fn.owner != "staff_reader" {
			t.Errorf("%s: owner = %q, want staff_reader", fn.sig, fn.owner)
		}
		if !reflect.DeepEqual(fn.config, []string{`search_path=""`}) {
			t.Errorf("%s: proconfig = %q, want [search_path=\"\"]", fn.sig, fn.config)
		}
		if len(fn.argTypes) == 0 || fn.argTypes[0] != "uuid" || fn.firstArgName != "p_actor" {
			t.Errorf("%s: first argument = %q %v, want p_actor uuid", fn.sig, fn.firstArgName, fn.argTypes)
		}
		if fn.publicExecute {
			t.Errorf("%s: PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked", fn.sig)
		}
		for role, want := range map[string]bool{"invoice_app": true, "invoice_tenant_reader": false, "supabase_auth_admin": false} {
			var has bool
			if err := h.super.QueryRow(context.Background(), `SELECT has_function_privilege($1, $2::regprocedure, 'EXECUTE')`, role, fn.sig).Scan(&has); err != nil {
				t.Fatalf("has_function_privilege %s on %s: %v", role, fn.sig, err)
			}
			if has != want {
				t.Errorf("%s: %s holds EXECUTE = %v, want %v", fn.sig, role, has, want)
			}
		}
	}
}

// AC-1. The empty-set case is the positive half: an empty owned set equals an empty named set.
func TestRLS_StaffReaderOwnsOnlyStaffReadFunctions(t *testing.T) {
	requireHarness(t)
	named := requireStaffReadFunctions(t)

	rows, err := h.super.Query(context.Background(),
		`SELECT p.pronamespace::regnamespace::text, p.proname, p.oid::regprocedure::text
		   FROM pg_proc p WHERE pg_get_userbyid(p.proowner) = 'staff_reader' ORDER BY 3`)
	if err != nil {
		t.Fatalf("read functions owned by staff_reader: %v", err)
	}
	var owned []string
	for rows.Next() {
		var schema, name, sig string
		if err := rows.Scan(&schema, &name, &sig); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !strings.HasPrefix(name, "staff_read_") {
			t.Errorf("staff_reader owns %s, want only staff_read_ functions", sig)
		}
		owned = append(owned, sig)
	}
	rows.Close()

	var namedSigs []string
	for _, fn := range named {
		namedSigs = append(namedSigs, fn.sig)
	}
	sort.Strings(namedSigs)
	if !reflect.DeepEqual(owned, namedSigs) {
		t.Errorf("functions owned by staff_reader = %v, public.staff_read_%% functions = %v, want the same set", owned, namedSigs)
	}
}

// AC-2. The applied body, not the migration text: a later CREATE OR REPLACE supersedes the file.
func TestRLS_EveryStaffReadFunctionChecksHouseMembershipFirst(t *testing.T) {
	requireHarness(t)
	fns := requireStaffReadFunctions(t)
	begin := regexp.MustCompile(`(?i)\bBEGIN\b`)
	for _, fn := range fns {
		var src string
		if err := h.super.QueryRow(context.Background(), `SELECT prosrc FROM pg_proc WHERE oid = $1::regprocedure`, fn.sig).Scan(&src); err != nil {
			t.Fatalf("read prosrc of %s: %v", fn.sig, err)
		}
		loc := begin.FindStringIndex(src)
		if loc == nil {
			t.Errorf("%s: body has no BEGIN:\n%s", fn.sig, src)
			continue
		}
		guard := regexp.MustCompile(`(?is)^\s*IF\s+NOT\s+public\.is_house_member\(p_actor\)\s+THEN\s+` +
			`RAISE\s+EXCEPTION\s+'` + regexp.QuoteMeta(fn.name) + `: actor is not staff'\s+` +
			`USING\s+ERRCODE\s*=\s*'insufficient_privilege'\s*;\s*END\s+IF\s*;`)
		if !guard.MatchString(src[loc[1]:]) {
			t.Errorf("%s: first statement after BEGIN is not the house-membership guard:\n%s", fn.sig, src)
		}
	}
}

// AC-3.
func TestRLS_StaffReadTenantsRefusesEveryoneWithNoHouseRow(t *testing.T) {
	requireHarness(t)
	requireStaffReadTenants(t)
	house := adoptHouseTenant(t)
	tx := revokeProbeTx(t)
	member := seedMembershipTx(t, tx, house, "admin", "active")
	call := `SELECT * FROM public.staff_read_tenants($1::uuid)`

	ruleDraftAsApp(t, tx)
	if n, err := ruleDraftTry(t, tx, call, member); err != nil || n == 0 {
		t.Fatalf("control: staff_read_tenants for the active house member: %d rows, err %v, want rows and no error", n, err)
	}

	ruleDraftAsOwner(t, tx)
	txExec(t, tx, `DELETE FROM public.house_company`)
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`, house, member); n != 1 {
		t.Fatalf("the former house member's membership rows = %d, want 1", n)
	}
	ruleDraftAsApp(t, tx)

	n, err := ruleDraftTry(t, tx, call, member)
	if n != 0 {
		t.Errorf("refused call returned %d rows", n)
	}
	requireNotStaffRefusal(t, "empty house_company", err, "staff_read_tenants")
}

// AC-3. Every staff_read_ function, so a later function falls under it with no edit.
func TestRLS_EveryStaffReadFunctionRefusesANonStaffActor(t *testing.T) {
	requireHarness(t)
	fns := requireStaffReadFunctions(t)
	house := adoptHouseTenant(t)
	tx := revokeProbeTx(t)

	active := seedMembershipTx(t, tx, house, "reviewer", "active")
	actors := []struct {
		name string
		id   any
	}{
		{"customer_active_member", seedMembershipTx(t, tx, h.tenantA, "admin", "active")},
		{"suspended_house_member", seedMembershipTx(t, tx, house, "reviewer", "suspended")},
		{"invited_house_member", seedMembershipTx(t, tx, house, "reviewer", "invited")},
		{"stranger", uuid.NewString()},
		{"null_actor", nil},
	}
	ruleDraftAsApp(t, tx)

	for _, fn := range fns {
		if len(fn.argTypes) == 0 {
			t.Errorf("%s: no arguments, want p_actor uuid first", fn.sig)
			continue
		}
		call := staffReadCall(fn)
		// Control: the guard admits staff, so the refusals below are the guard and not a lost grant.
		if _, err := ruleDraftTry(t, tx, call, active); err != nil {
			if pgErr := ruleDraftPgErr(err); pgErr != nil && pgErr.Message == fn.name+": actor is not staff" {
				t.Errorf("%s: control: an active house member was refused by the guard", fn.sig)
			}
		}
		for _, a := range actors {
			n, err := ruleDraftTry(t, tx, call, a.id)
			if n != 0 {
				t.Errorf("%s as %s: refused call returned %d rows", fn.sig, a.name, n)
			}
			requireNotStaffRefusal(t, fn.sig+" as "+a.name, err, fn.name)
		}
	}
}

type staffTenantRow struct {
	id, name, kind string
	createdAt      time.Time
}

func seedCommittedTenant(t *testing.T, id, name, kind string, createdAt time.Time) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind, created_at) VALUES ($1, $2, $3, $4)`, id, name, kind, createdAt); err != nil {
		t.Fatalf("seed tenant %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
}

func collectStaffTenantRows(t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, member string) []staffTenantRow {
	t.Helper()
	// SELECT * scans by position: pins the result column order and count.
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_tenants($1::uuid)`, member)
	if err != nil {
		t.Fatalf("staff_read_tenants as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffTenantRow, error) {
		var x staffTenantRow
		return x, r.Scan(&x.id, &x.name, &x.kind, &x.createdAt)
	})
	if err != nil {
		t.Fatalf("collect staff_read_tenants rows: %v", err)
	}
	return got
}

// AC-4. One REPEATABLE READ snapshot: other packages create tenants while this runs.
func TestRLS_StaffReadTenantsReturnsEveryTenantToAHouseMember(t *testing.T) {
	requireHarness(t)
	requireStaffReadTenants(t)
	ctx := context.Background()
	member := seedHouseMember(t, "reviewer", "active")
	base := time.Now().UTC().Truncate(time.Second)
	firm, inHouse := uuid.NewString(), uuid.NewString()
	seedCommittedTenant(t, firm, "staff-read firm", "firm", base.Add(-2*time.Minute))
	seedCommittedTenant(t, inHouse, "staff-read in-house", "in_house", base.Add(-1*time.Minute))

	tx, err := h.super.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	want := mustCount(t, tx, `SELECT count(*) FROM tenants`)
	txExec(t, tx, `SET LOCAL ROLE invoice_app`)
	got := collectStaffTenantRows(t, tx, member)

	if len(got) != want {
		t.Errorf("staff_read_tenants returned %d rows, superuser count(*) FROM tenants = %d, want equal", len(got), want)
	}
	byID := map[string]staffTenantRow{}
	for _, r := range got {
		byID[r.id] = r
	}
	for _, w := range []staffTenantRow{
		{firm, "staff-read firm", "firm", base.Add(-2 * time.Minute)},
		{inHouse, "staff-read in-house", "in_house", base.Add(-1 * time.Minute)},
	} {
		r, ok := byID[w.id]
		if !ok {
			t.Errorf("tenant %s (%s) is missing from the result", w.id, w.name)
			continue
		}
		if r.name != w.name || r.kind != w.kind || !r.createdAt.Equal(w.createdAt) {
			t.Errorf("tenant %s = %+v, want name %q kind %q created_at %v", w.id, r, w.name, w.kind, w.createdAt)
		}
	}
	for i := 1; i < len(got); i++ {
		p, c := got[i-1], got[i]
		if p.createdAt.Before(c.createdAt) || (p.createdAt.Equal(c.createdAt) && p.id > c.id) {
			t.Errorf("rows %d,%d out of order: (%v, %s) before (%v, %s), want created_at DESC then id", i-1, i, p.createdAt, p.id, c.createdAt, c.id)
		}
	}
}

// AC-4. The newer id sorts last by id, so only created_at DESC puts it first. A tie of six is not
// ordered by chance: heap order of committed rows is not insertion order, and two rows could match it.
func TestRLS_StaffReadTenantsOrdersNewestFirstThenById(t *testing.T) {
	requireHarness(t)
	requireStaffReadTenants(t)
	member := seedHouseMember(t, "preparer", "active")
	var ids []string
	for i := 0; i < 7; i++ {
		ids = append(ids, uuid.NewString())
	}
	sort.Strings(ids)
	ties, newer := ids[:6], ids[6]
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for i := len(ties) - 1; i >= 0; i-- {
		seedCommittedTenant(t, ties[i], "order tie "+strconv.Itoa(i), "firm", t0)
	}
	seedCommittedTenant(t, newer, "order newer", "firm", t0.Add(time.Minute))

	seeded := map[string]bool{newer: true}
	for _, id := range ties {
		seeded[id] = true
	}
	var order []string
	for _, r := range collectStaffTenantRows(t, h.app, member) {
		if seeded[r.id] {
			order = append(order, r.id)
		}
	}
	if want := append([]string{newer}, ties...); !reflect.DeepEqual(order, want) {
		t.Errorf("order among the seeded tenants = %v, want the newer one, then the tied rows by ascending id: %v", order, want)
	}
}

// AC-4. No tenant is set on the call, so the role decides nothing.
func TestRLS_StaffReadTenantsAdmitsAHouseMemberOfAnyRole(t *testing.T) {
	requireHarness(t)
	requireStaffReadTenants(t)
	house := adoptHouseTenant(t)
	tx := revokeProbeTx(t)

	for _, role := range []string{"admin", "preparer", "reviewer"} {
		ruleDraftAsOwner(t, tx)
		member := seedMembershipTx(t, tx, house, role, "active")
		ruleDraftAsApp(t, tx)
		n, err := ruleDraftTry(t, tx, `SELECT * FROM public.staff_read_tenants($1::uuid)`, member)
		if err != nil || n == 0 {
			t.Errorf("%s house member: %d rows, err %v, want rows and no error", role, n, err)
		}
	}
}

// AC-4. A tenant set on the connection neither narrows a staff read nor admits a customer member.
func TestRLS_StaffReadTenantsIgnoresTheTenantSetOnTheConnection(t *testing.T) {
	requireHarness(t)
	requireStaffReadTenants(t)
	staff := seedHouseMember(t, "reviewer", "active")
	customer := seedCommittedMembership(t, h.tenantA, "admin", "active")

	var got []staffTenantRow
	err := db.WithinTenantTx(context.Background(), h.app, h.tenantA, func(tx pgx.Tx) error {
		got = collectStaffTenantRows(t, tx, staff)
		return nil
	})
	if err != nil {
		t.Fatalf("staff_read_tenants with tenant A set: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.id] = true
	}
	if len(got) == 0 || !seen[h.tenantA] || !seen[h.tenantB] {
		t.Errorf("staff_read_tenants with tenant A set returned %d rows, tenant A seen = %v, tenant B seen = %v, want both", len(got), seen[h.tenantA], seen[h.tenantB])
	}

	err = db.WithinTenantTx(context.Background(), h.app, h.tenantA, func(tx pgx.Tx) error {
		n, callErr := ruleDraftTry(t, tx, `SELECT * FROM public.staff_read_tenants($1::uuid)`, customer)
		if n != 0 {
			t.Errorf("refused call returned %d rows", n)
		}
		requireNotStaffRefusal(t, "customer member of tenant A with tenant A set", callErr, "staff_read_tenants")
		return nil
	})
	if err != nil {
		t.Fatalf("customer call with tenant A set: %v", err)
	}
}

// staffReaderColumnGrants maps "schema.table" to the staff_reader column grants on it, as "column/PRIVILEGE".
func staffReaderColumnGrants(t *testing.T) map[string][]string {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT c.relnamespace::regnamespace::text || '.' || c.relname, a.attname || '/' || x.privilege_type
		   FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid, aclexplode(a.attacl) x
		  WHERE x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader') AND a.attnum > 0 AND NOT a.attisdropped
		  ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("read staff_reader column grants: %v", err)
	}
	grants := map[string][]string{}
	for rows.Next() {
		var table, grant string
		if err := rows.Scan(&table, &grant); err != nil {
			t.Fatalf("scan: %v", err)
		}
		grants[table] = append(grants[table], grant)
	}
	rows.Close()
	return grants
}

type staffReaderPolicy struct{ name, cmd, qual, roles string }

// staffReaderPolicies maps "schema.table" to the policies that name staff_reader.
func staffReaderPolicies(t *testing.T) map[string][]staffReaderPolicy {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT schemaname || '.' || tablename, policyname, cmd, coalesce(qual, ''), roles::text
		   FROM pg_policies WHERE 'staff_reader' = ANY (roles) ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("read staff_reader policies: %v", err)
	}
	out := map[string][]staffReaderPolicy{}
	for rows.Next() {
		var table string
		var p staffReaderPolicy
		if err := rows.Scan(&table, &p.name, &p.cmd, &p.qual, &p.roles); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[table] = append(out[table], p)
	}
	rows.Close()
	return out
}

// AC-5.
func TestRLS_StaffReaderPrivilegesAreSelectColumnsWithAPolicy(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	tableLevel := collectStrings(t,
		`SELECT c.relnamespace::regnamespace::text || '.' || c.relname || '/' || x.privilege_type
		   FROM pg_class c, aclexplode(c.relacl) x
		  WHERE x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader') ORDER BY 1`)
	if len(tableLevel) != 0 {
		t.Errorf("staff_reader table-level privileges = %v, want none", tableLevel)
	}

	grants := staffReaderColumnGrants(t)
	if len(grants) == 0 {
		t.Fatal("staff_reader holds no column grant on any table, want at least tenants")
	}
	policies := staffReaderPolicies(t)

	for table, gs := range grants {
		for _, g := range gs {
			if !strings.HasSuffix(g, "/SELECT") {
				t.Errorf("%s: staff_reader column grant %q, want SELECT only", table, g)
			}
		}
		var rls bool
		if err := h.super.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, table).Scan(&rls); err != nil {
			t.Fatalf("read relrowsecurity of %s: %v", table, err)
		}
		switch ps := policies[table]; {
		case rls && len(ps) != 1:
			t.Errorf("%s has RLS and %d staff_reader policies %v, want exactly 1", table, len(ps), ps)
		case rls && (ps[0].cmd != "SELECT" || ps[0].qual != "true" || ps[0].roles != "{staff_reader}"):
			t.Errorf("%s policy = %+v, want FOR SELECT TO staff_reader USING (true)", table, ps[0])
		case !rls && len(ps) != 0:
			t.Errorf("%s has no RLS but staff_reader policies %v, want none", table, ps)
		}
	}
	for table, ps := range policies {
		if _, ok := grants[table]; !ok {
			t.Errorf("%s: staff_reader policies %v with no column grant on the table", table, ps)
		}
	}

	want := []string{"created_at/SELECT", "id/SELECT", "kind/SELECT", "name/SELECT"}
	if got := grants["public.tenants"]; !reflect.DeepEqual(got, want) {
		t.Errorf("staff_reader grants on tenants = %v, want exactly %v", got, want)
	}
}

// AC-5.
func TestRLS_StaffReaderReadsEveryTenantAndWritesNone(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := revokeProbeTx(t)
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)
	txExec(t, tx, `SET LOCAL ROLE staff_reader`)

	rows, err := tx.Query(ctx, `SELECT id::text FROM tenants WHERE id = ANY ($1::uuid[])`, []string{h.tenantA, h.tenantB})
	if err != nil {
		t.Fatalf("staff_reader SELECT id FROM tenants: %v", err)
	}
	seen, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("staff_reader SELECT id FROM tenants: %v", err)
	}
	sort.Strings(seen)
	if want := []string{h.tenantA, h.tenantB}; !reflect.DeepEqual(seen, sortedCopy(want)) {
		t.Errorf("staff_reader sees tenants %v, want both %v", seen, sortedCopy(want))
	}

	for _, w := range []struct{ what, sql string }{
		{"INSERT", `INSERT INTO tenants (id, name) VALUES (gen_random_uuid(), 'staff_reader write probe')`},
		{"UPDATE", `UPDATE tenants SET name = 'staff_reader write probe' WHERE id = '` + h.tenantA + `'`},
		{"DELETE", `DELETE FROM tenants WHERE id = '` + h.tenantB + `'`},
	} {
		txExec(t, tx, `SAVEPOINT w`)
		_, err := tx.Exec(ctx, w.sql)
		txExec(t, tx, `ROLLBACK TO SAVEPOINT w`)
		assertPgRefusal(t, "staff_reader "+w.what+" on tenants", err, sqlstateInsufficientPrivilege, "permission denied for table tenants")
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// AC-7. Passes before the migration too: it pins that the new policy leaves the reader's alone.
func TestRLS_TenantEnumerateIsUnchanged(t *testing.T) {
	requireHarness(t)
	got := collectStrings(t, `SELECT cmd || '/' || roles::text || '/' || qual FROM pg_policies
		WHERE schemaname = 'public' AND tablename = 'tenants' AND policyname = 'tenant_enumerate'`)
	if want := []string{"SELECT/{invoice_tenant_reader}/true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tenant_enumerate = %v, want exactly %v", got, want)
	}
	for _, id := range []string{h.tenantA, h.tenantB} {
		if n := mustCount(t, h.reader, `SELECT count(*) FROM tenants WHERE id = $1`, id); n != 1 {
			t.Errorf("invoice_tenant_reader sees tenant %s = %d, want 1", id, n)
		}
	}
}

// staffReaderSeeds makes a tenant-B row exist in each table that has a staff_reader policy.
// A table with no entry fails TestRLS_AppStaysIsolatedOnEveryStaffReaderTable by name.
var staffReaderSeeds = map[string]struct {
	tenantColumn string
	seed         func(t *testing.T)
}{
	"tenants": {"id", func(t *testing.T) {
		if mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, h.tenantB) != 1 {
			t.Fatal("the harness did not seed tenant B")
		}
	}},
}

// AC-8.
func TestRLS_AppStaysIsolatedOnEveryStaffReaderTable(t *testing.T) {
	requireHarness(t)
	policies := staffReaderPolicies(t)
	if len(policies) == 0 {
		t.Fatal("no table has a staff_reader policy, want at least tenants")
	}
	var tables []string
	for table := range policies {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		name := strings.TrimPrefix(table, "public.")
		entry, ok := staffReaderSeeds[name]
		if !ok {
			t.Errorf("%s has a staff_reader policy and no entry in staffReaderSeeds: add one that seeds a tenant-B row", table)
			continue
		}
		entry.seed(t)
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = $1`, pgx.Identifier{"public", name}.Sanitize(), pgx.Identifier{entry.tenantColumn}.Sanitize())
		if n := mustCount(t, h.super, q, h.tenantB); n == 0 {
			t.Errorf("%s: no tenant-B row exists, so the isolation count proves nothing", table)
		}
		err := db.WithinTenantTx(context.Background(), h.app, h.tenantA, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, q, h.tenantB); n != 0 {
				t.Errorf("%s: invoice_app with tenant A set reads %d tenant-B rows, want 0", table, n)
			}
			return nil
		})
		if err != nil {
			t.Errorf("%s: read as tenant A: %v", table, err)
		}
		err = db.WithinTenantTx(context.Background(), h.app, h.tenantB, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, q, h.tenantB); n == 0 {
				t.Errorf("%s: control: invoice_app with tenant B set reads no tenant-B row", table)
			}
			return nil
		})
		if err != nil {
			t.Errorf("%s: read as tenant B: %v", table, err)
		}
	}
}

// AC-10. The house Down drops is_house_member and its Up re-grants invoice_migrator only.
func TestRLS_StaffReadSurvivesAHouseCompanyRoundTrip(t *testing.T) {
	requireHarness(t)
	house := adoptHouseTenant(t)
	member := seedCommittedMembership(t, house, "reviewer", "active")

	reapply := rollBackHouseCompany(t)
	reapply()
	// The house Up starts the table empty (production's row is written by hand).
	if _, err := h.super.Exec(context.Background(), `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, house); err != nil {
		t.Fatalf("write the house row after the Up: %v", err)
	}

	var has bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege('staff_reader', 'public.is_house_member(uuid)', 'EXECUTE')`).Scan(&has); err != nil {
		t.Fatalf("has_function_privilege staff_reader on is_house_member: %v", err)
	}
	if !has {
		t.Error("staff_reader holds no EXECUTE on is_house_member after the house round trip, want it restored")
	}
	requireStaffReadTenants(t)
	if got := collectStaffTenantRows(t, h.app, member); len(got) == 0 {
		t.Error("staff_read_tenants returned no rows to an active house member after the round trip")
	}
}

type staffReadFootprint struct {
	function, policy, isHouseMemberExecute bool
	tenantColumnGrants                     int
}

func readStaffReadFootprint(t *testing.T) staffReadFootprint {
	t.Helper()
	var f staffReadFootprint
	if err := h.super.QueryRow(context.Background(), `
		SELECT to_regprocedure($1) IS NOT NULL,
		       EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'public' AND tablename = 'tenants' AND policyname = 'staff_read_lookup'),
		       has_function_privilege('staff_reader', 'public.is_house_member(uuid)', 'EXECUTE'),
		       (SELECT count(*) FROM pg_attribute a, aclexplode(a.attacl) x
		         WHERE a.attrelid = 'public.tenants'::regclass AND NOT a.attisdropped
		           AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader'))`,
		staffReadTenantsSig).Scan(&f.function, &f.policy, &f.isHouseMemberExecute, &f.tenantColumnGrants); err != nil {
		t.Fatalf("read the staff_read_tenants footprint: %v", err)
	}
	return f
}

// AC-9. Rolls back every staff_read_ migration, newest first, so a later one cannot hold the grants under test.
func TestRLS_StaffReadTenantsDownAndUp(t *testing.T) {
	requireHarness(t)
	versions := staffReadMigrationVersions(t)
	if len(versions) == 0 {
		t.Fatal("no staff_read_ migration found: no migration holds CREATE FUNCTION public.staff_read_")
	}
	member := seedHouseMember(t, "reviewer", "active")
	if f := readStaffReadFootprint(t); !f.function || !f.policy || !f.isHouseMemberExecute || f.tenantColumnGrants != 4 {
		t.Fatalf("before the Down: footprint = %+v, want function, policy, is_house_member EXECUTE and 4 tenants column grants", f)
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), versions...)
	if f := readStaffReadFootprint(t); f.function || f.policy || f.isHouseMemberExecute || f.tenantColumnGrants != 0 {
		t.Errorf("after the Down: footprint = %+v, want none of the function, the policy, the is_house_member EXECUTE and the column grants", f)
	}

	reapply()
	if f := readStaffReadFootprint(t); !f.function || !f.policy || !f.isHouseMemberExecute || f.tenantColumnGrants != 4 {
		t.Errorf("after the Up: footprint = %+v, want function, policy, is_house_member EXECUTE and 4 tenants column grants", f)
	}
	if got := collectStaffTenantRows(t, h.app, member); len(got) == 0 {
		t.Error("after the Up: staff_read_tenants returned no rows to an active house member")
	}
}
