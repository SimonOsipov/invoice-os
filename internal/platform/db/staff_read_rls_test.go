package db_test

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/jackc/pgx/v5/pgconn"

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

// The empty-set case is the positive half: an empty owned set equals an empty named set.
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

	// Go byte order, as for namedSigs: the database collation sorts submission_job( after submission_jobs(.
	sort.Strings(owned)
	var namedSigs []string
	for _, fn := range named {
		namedSigs = append(namedSigs, fn.sig)
	}
	sort.Strings(namedSigs)
	if !reflect.DeepEqual(owned, namedSigs) {
		t.Errorf("functions owned by staff_reader = %v, public.staff_read_%% functions = %v, want the same set", owned, namedSigs)
	}
}

// The applied body, not the migration text: a later CREATE OR REPLACE supersedes the file.
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

// Every staff_read_ function, so a later function falls under it with no edit.
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

// One REPEATABLE READ snapshot: other packages create tenants while this runs.
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

// The newer id sorts last by id, so only created_at DESC puts it first. A tie of six is not
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

// No tenant is set on the call, so the role decides nothing.
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

// A tenant set on the connection neither narrows a staff read nor admits a customer member.
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

	// Column sets per table: tenants is the staff_read_tenants migration's, the next three the tenant-detail migration's, the last two the submission-jobs migration's; audit_log gains id, event, actor and payload from the audit-search migration.
	for table, cols := range map[string][]string{
		"public.tenants":                {"created_at", "id", "kind", "name"},
		"public.memberships":            {"created_at", "display_name", "email", "role", "status", "tenant_id", "user_id"},
		"public.invoices":               {"created_at", "id", "invoice_number", "status", "tenant_id"},
		"public.audit_log":              {"actor", "created_at", "event", "id", "payload", "tenant_id"},
		"public.submission_jobs":        {"attempts", "created_at", "id", "invoice_id", "last_error", "poll_ref", "state", "tenant_id", "updated_at"},
		"public.invoice_status_history": {"actor", "changed_at", "from_status", "id", "invoice_id", "tenant_id", "to_status"},
	} {
		var want []string
		for _, c := range cols {
			want = append(want, c+"/SELECT")
		}
		if got := sortedCopy(grants[table]); !reflect.DeepEqual(got, sortedCopy(want)) {
			t.Errorf("staff_reader grants on %s = %v, want exactly %v", table, got, sortedCopy(want))
		}
		if ps := policies[table]; len(ps) != 1 || ps[0].name != "staff_read_lookup" || ps[0].cmd != "SELECT" || ps[0].qual != "true" || ps[0].roles != "{staff_reader}" {
			t.Errorf("staff_reader policies on %s = %+v, want exactly staff_read_lookup FOR SELECT TO staff_reader USING (true)", table, ps)
		}
	}

	wantAudit := []string{"actor/SELECT", "created_at/SELECT", "event/SELECT", "id/SELECT", "payload/SELECT", "rule_set_version_id/SELECT", "subject_id/SELECT", "tenant_id/SELECT"}
	if got := sortedCopy(grants["public.staff_audit_log"]); !reflect.DeepEqual(got, wantAudit) {
		t.Errorf("staff_reader grants on staff_audit_log = %v, want exactly %v", got, wantAudit)
	}
	if ps := policies["public.staff_audit_log"]; len(ps) != 0 {
		t.Errorf("staff_audit_log has staff_reader policies %v, want none", ps)
	}
}

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

// Passes before the migration too: it pins that the new policy leaves the reader's alone.
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
	"memberships": {"tenant_id", func(t *testing.T) {
		seedCommittedMembership(t, h.tenantB, "reviewer", "active")
	}},
	"invoices": {"tenant_id", func(t *testing.T) {
		entity := seedEntity(t, h.tenantB)
		seedInvoiceAt(t, h.tenantB, entity, uuid.NewString(), "ISO-"+uuid.NewString()[:8], "draft", time.Now().UTC())
	}},
	"submission_jobs": {"tenant_id", func(t *testing.T) {
		seedJobAt(t, h.tenantB, tenantBInvoice(t), "queued", time.Now().UTC(), nil, nil)
	}},
	"invoice_status_history": {"tenant_id", func(t *testing.T) {
		seedHistoryAt(t, h.tenantB, tenantBInvoice(t), strPtr("draft"), "validated", "staff-read-isolation", time.Now().UTC())
	}},
	// audit_log is append-only: the row stays.
	"audit_log": {"tenant_id", func(t *testing.T) {
		if _, err := h.super.Exec(context.Background(),
			`INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1, 'staff-read-isolation', 'staff_read.seed')`, h.tenantB); err != nil {
			t.Fatalf("seed a tenant-B audit row: %v", err)
		}
	}},
}

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
	// The loop below covers only tables with a policy: a seed entry with none proves nothing.
	for name := range staffReaderSeeds {
		if len(policies["public."+name]) == 0 {
			t.Errorf("staffReaderSeeds has an entry for %s and the table has no staff_reader policy, so its isolation is not exercised", name)
		}
	}

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

// The house Down drops is_house_member and its Up re-grants invoice_migrator only.
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

// Rolls back every staff_read_ migration, newest first, so a later one cannot hold the grants under test.
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

const (
	staffReadActivitySig = "public.staff_read_tenant_activity(uuid)"
	staffReadMembersSig  = "public.staff_read_tenant_members(uuid, uuid)"
	staffReadInvoicesSig = "public.staff_read_tenant_invoices(uuid, uuid)"

	// staffTenantInvoiceLimit is the LIMIT in staff_read_tenant_invoices; no Go constant carries it yet.
	staffTenantInvoiceLimit = 10
)

var staffDetailSigs = []string{staffReadActivitySig, staffReadMembersSig, staffReadInvoicesSig}

// requireStaffDetailFunctions fails by name while the tenant-detail migration is not applied.
func requireStaffDetailFunctions(t *testing.T) {
	t.Helper()
	for _, sig := range staffDetailSigs {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Fatalf("function %s not found: the staff_read_tenant_detail migration is not applied", sig)
		}
	}
}

type rowQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// seedDetailTenant commits a tenant and deletes its invoices and entities first on cleanup: invoices.entity_id is RESTRICT.
func seedDetailTenant(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	seedCommittedTenant(t, id, name, "firm", time.Now().UTC().Truncate(time.Second))
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE tenant_id = $1`, id)
		_, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE tenant_id = $1`, id)
	})
	return id
}

func seedEntity(t *testing.T, tenant string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenant, "entity-"+id[:8]); err != nil {
		t.Fatalf("seed a business entity: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id) })
	return id
}

func seedInvoiceAt(t *testing.T, tenant, entity, id, number, status string, createdAt time.Time) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, tenant, entity, number, status, createdAt); err != nil {
		t.Fatalf("seed invoice %s: %v", number, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id) })
}

// seedAuditAt appends an audit row; audit_log is append-only, so the row stays.
func seedAuditAt(t *testing.T, tenant string, at time.Time) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, created_at) VALUES ($1, 'staff-read-detail', 'staff_read.seed', $2)`, tenant, at); err != nil {
		t.Fatalf("seed an audit row: %v", err)
	}
}

func seedMemberAt(t *testing.T, tenant, role, status string, createdAt time.Time, display, email *string) string {
	t.Helper()
	return seedMemberAsUser(t, tenant, uuid.NewString(), role, status, createdAt, display, email)
}

func seedMemberAsUser(t *testing.T, tenant, user, role, status string, createdAt time.Time, display, email *string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role, status, display_name, email, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, tenant, user, role, status, display, email, createdAt); err != nil {
		t.Fatalf("seed a %s %s membership: %v", status, role, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id) })
	return user
}

func strPtr(s string) *string { return &s }

type tenantActivityRow struct {
	tenant          string
	active, invoice int64
	last            *time.Time
}

func collectTenantActivity(t *testing.T, q rowQuerier, actor string) []tenantActivityRow {
	t.Helper()
	// SELECT * scans by position: pins the result column order and count.
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_tenant_activity($1::uuid)`, actor)
	if err != nil {
		t.Fatalf("staff_read_tenant_activity as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tenantActivityRow, error) {
		var x tenantActivityRow
		return x, r.Scan(&x.tenant, &x.active, &x.invoice, &x.last)
	})
	if err != nil {
		t.Fatalf("collect staff_read_tenant_activity rows: %v", err)
	}
	return got
}

type tenantMemberRow struct {
	user, role, status string
	display, email     *string
}

func collectTenantMembers(t *testing.T, q rowQuerier, actor string, tenant any) []tenantMemberRow {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_tenant_members($1::uuid, $2::uuid)`, actor, tenant)
	if err != nil {
		t.Fatalf("staff_read_tenant_members as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tenantMemberRow, error) {
		var x tenantMemberRow
		return x, r.Scan(&x.user, &x.role, &x.status, &x.display, &x.email)
	})
	if err != nil {
		t.Fatalf("collect staff_read_tenant_members rows: %v", err)
	}
	return got
}

type tenantInvoiceRow struct {
	id, number, status string
	createdAt          time.Time
}

func collectTenantInvoices(t *testing.T, q rowQuerier, actor string, tenant any) []tenantInvoiceRow {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_tenant_invoices($1::uuid, $2::uuid)`, actor, tenant)
	if err != nil {
		t.Fatalf("staff_read_tenant_invoices as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tenantInvoiceRow, error) {
		var x tenantInvoiceRow
		return x, r.Scan(&x.id, &x.number, &x.status, &x.createdAt)
	})
	if err != nil {
		t.Fatalf("collect staff_read_tenant_invoices rows: %v", err)
	}
	return got
}

func TestRLS_StaffReadTenantDetailFunctionsExist(t *testing.T) {
	requireHarness(t)
	for _, sig := range staffDetailSigs {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Errorf("function %s not found, want it created by the staff_read_tenant_detail migration", sig)
		}
	}
	// The generic catalog tests iterate every staff_read_ function: they must now see all four.
	names := map[string]bool{}
	for _, fn := range staffReadFunctions(t) {
		names[fn.name] = true
	}
	for _, name := range []string{"staff_read_tenants", "staff_read_tenant_activity", "staff_read_tenant_members", "staff_read_tenant_invoices"} {
		if !names[name] {
			t.Errorf("the staff_read_ catalog scan does not see %s", name)
		}
	}
}

func TestRLS_StaffReadTenantActivityCountsActiveMembersInvoicesAndLastAudit(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	busy, quiet := seedDetailTenant(t, "activity busy"), seedDetailTenant(t, "activity quiet")
	for _, m := range []struct{ role, status string }{
		{"admin", "active"}, {"reviewer", "active"}, {"preparer", "suspended"}, {"preparer", "invited"},
	} {
		seedMemberAt(t, busy, m.role, m.status, t0, nil, nil)
	}
	entity := seedEntity(t, busy)
	for i := 0; i < 3; i++ {
		seedInvoiceAt(t, busy, entity, uuid.NewString(), fmt.Sprintf("ACT-%d", i), "draft", t0)
	}
	seedAuditAt(t, busy, t0)
	seedAuditAt(t, busy, t0.Add(time.Minute))

	got := collectTenantActivity(t, h.app, staff)
	if len(got) == 0 {
		t.Fatal("staff_read_tenant_activity returned no rows to an active house member")
	}
	byTenant := map[string][]tenantActivityRow{}
	for _, r := range got {
		byTenant[r.tenant] = append(byTenant[r.tenant], r)
	}
	for _, id := range []string{busy, quiet} {
		if len(byTenant[id]) != 1 {
			t.Fatalf("tenant %s appears in %d rows, want exactly 1", id, len(byTenant[id]))
		}
	}
	if r := byTenant[busy][0]; r.active != 2 || r.invoice != 3 || r.last == nil || !r.last.Equal(t0.Add(time.Minute)) {
		t.Errorf("busy tenant = active %d, invoices %d, last %v, want 2, 3, %v", r.active, r.invoice, r.last, t0.Add(time.Minute))
	}
	if r := byTenant[quiet][0]; r.active != 0 || r.invoice != 0 || r.last != nil {
		t.Errorf("quiet tenant = active %d, invoices %d, last %v, want 0, 0, NULL", r.active, r.invoice, r.last)
	}
}

// One REPEATABLE READ snapshot: other packages create tenants while this runs.
func TestRLS_StaffReadTenantActivityReturnsOneRowPerTenant(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	ctx := context.Background()
	staff := seedHouseMember(t, "reviewer", "active")
	seedDetailTenant(t, "activity one row")

	tx, err := h.super.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	want := mustCount(t, tx, `SELECT count(*) FROM tenants`)
	txExec(t, tx, `SET LOCAL ROLE invoice_app`)
	got := collectTenantActivity(t, tx, staff)

	if want == 0 || len(got) != want {
		t.Errorf("staff_read_tenant_activity returned %d rows, superuser count(*) FROM tenants = %d, want equal and non-zero", len(got), want)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.tenant] {
			t.Errorf("tenant %s repeats in the result", r.tenant)
		}
		seen[r.tenant] = true
	}
}

func TestRLS_StaffReadTenantMembersReturnsOneTenantInOrder(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x, y := seedDetailTenant(t, "members x"), seedDetailTenant(t, "members y")

	// Newest first, so insertion order is not the expected order.
	suspended := seedMemberAt(t, x, "preparer", "suspended", t0.Add(2*time.Second), nil, strPtr("sam@example.test"))
	invited := seedMemberAt(t, x, "reviewer", "invited", t0.Add(time.Second), strPtr("Ivy Invited"), strPtr("ivy@example.test"))
	active := seedMemberAt(t, x, "admin", "active", t0, strPtr("Ada Active"), strPtr("ada@example.test"))
	other := seedMemberAt(t, y, "admin", "active", t0, strPtr("Yolanda Other"), strPtr("yo@example.test"))

	got := collectTenantMembers(t, h.app, staff, x)
	want := []tenantMemberRow{
		{active, "admin", "active", strPtr("Ada Active"), strPtr("ada@example.test")},
		{invited, "reviewer", "invited", strPtr("Ivy Invited"), strPtr("ivy@example.test")},
		{suspended, "preparer", "suspended", nil, strPtr("sam@example.test")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staff_read_tenant_members(X) = %+v, want %+v", got, want)
	}
	for _, r := range got {
		if r.user == other {
			t.Errorf("tenant Y's member %s appears in tenant X's result", other)
		}
	}
}

// Both detail functions, so a NULL or unknown tenant is refused by no error and no row.
func TestRLS_StaffReadTenantMembersOfNoTenantIsEmpty(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x := seedDetailTenant(t, "no tenant control")
	seedMemberAt(t, x, "admin", "active", t0, nil, nil)
	seedInvoiceAt(t, x, seedEntity(t, x), uuid.NewString(), "NOT-1", "draft", t0)

	if n := len(collectTenantMembers(t, h.app, staff, x)); n != 1 {
		t.Fatalf("control: members of a seeded tenant = %d rows, want 1", n)
	}
	if n := len(collectTenantInvoices(t, h.app, staff, x)); n != 1 {
		t.Fatalf("control: invoices of a seeded tenant = %d rows, want 1", n)
	}
	for _, tenant := range []any{nil, uuid.NewString()} {
		if n := len(collectTenantMembers(t, h.app, staff, tenant)); n != 0 {
			t.Errorf("members of tenant %v = %d rows, want 0", tenant, n)
		}
		if n := len(collectTenantInvoices(t, h.app, staff, tenant)); n != 0 {
			t.Errorf("invoices of tenant %v = %d rows, want 0", tenant, n)
		}
	}
}

func TestRLS_StaffReadTenantInvoicesReturnsTheTenNewest(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x, y := seedDetailTenant(t, "invoices x"), seedDetailTenant(t, "invoices y")
	entityX, entityY := seedEntity(t, x), seedEntity(t, y)

	// Larger tied id first: heap order must not equal the expected order.
	tied := []string{uuid.NewString(), uuid.NewString()}
	sort.Strings(tied)
	statuses := []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"} // invoices.status CHECK
	var want []tenantInvoiceRow
	for i := 8; i >= 0; i-- {
		id, num, st, at := uuid.NewString(), fmt.Sprintf("INV-%02d", i), statuses[i%len(statuses)], t0.Add(time.Duration(i)*time.Second)
		seedInvoiceAt(t, x, entityX, id, num, st, at)
		if i >= 1 {
			want = append(want, tenantInvoiceRow{id, num, st, at})
		}
	}
	tiedRows := []tenantInvoiceRow{
		{tied[0], "INV-TIE-A", "validated", t0.Add(10 * time.Second)},
		{tied[1], "INV-TIE-B", "accepted", t0.Add(10 * time.Second)},
	}
	seedInvoiceAt(t, x, entityX, tied[1], "INV-TIE-B", "accepted", t0.Add(10*time.Second))
	seedInvoiceAt(t, x, entityX, tied[0], "INV-TIE-A", "validated", t0.Add(10*time.Second))
	seedInvoiceAt(t, y, entityY, uuid.NewString(), "INV-Y", "draft", t0.Add(20*time.Second))
	want = append(tiedRows, want...)

	got := collectTenantInvoices(t, h.app, staff, x)
	if len(got) != staffTenantInvoiceLimit {
		t.Fatalf("staff_read_tenant_invoices(X) = %d rows, want %d (staffTenantInvoiceLimit)", len(got), staffTenantInvoiceLimit)
	}
	// want is built newest first from offsets 8..1 (offset 0 is the oldest and is cut).
	for i := range got {
		got[i].createdAt = got[i].createdAt.UTC()
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staff_read_tenant_invoices(X) =\n%+v\nwant the tied pair by ascending id, then offsets 8..1:\n%+v", got, want)
	}
	for _, r := range got {
		if r.number == "INV-Y" || r.number == "INV-00" {
			t.Errorf("result holds %s, want tenant Y's invoice and the oldest of X absent", r.number)
		}
	}
}

func staffDetailFootprintOf(t *testing.T) (functions, policies, columnGrants int) {
	t.Helper()
	if err := h.super.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM unnest($1::text[]) s WHERE to_regprocedure(s) IS NOT NULL),
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND policyname = 'staff_read_lookup'
		         AND tablename IN ('memberships', 'invoices', 'audit_log')),
		       (SELECT count(*) FROM pg_attribute a, aclexplode(a.attacl) x
		         WHERE a.attrelid IN ('public.memberships'::regclass, 'public.invoices'::regclass, 'public.audit_log'::regclass)
		           AND NOT a.attisdropped AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader'))`,
		staffDetailSigs).Scan(&functions, &policies, &columnGrants); err != nil {
		t.Fatalf("read the staff_read_tenant_detail footprint: %v", err)
	}
	return functions, policies, columnGrants
}

// staffDetailMigrationVersion is the one migration that creates staff_read_tenant_activity.
func staffDetailMigrationVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if bytes.Contains(raw, []byte("CREATE FUNCTION public.staff_read_tenant_activity")) {
			v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
			if err != nil {
				t.Fatalf("parse goose version out of %q: %v", f, err)
			}
			return v
		}
	}
	t.Fatal("no migration holds CREATE FUNCTION public.staff_read_tenant_activity")
	return 0
}

// Rolls back the detail migration and every newer staff_read_ one, newest first: the staff_read_tenants footprint must survive.
func TestRLS_StaffReadTenantDetailDownAndUp(t *testing.T) {
	requireHarness(t)
	version := staffDetailMigrationVersion(t)
	var versions []int64
	for _, v := range staffReadMigrationVersions(t) {
		if v >= version {
			versions = append(versions, v)
		}
	}
	house := adoptHouseTenant(t)
	member := seedHouseMember(t, "reviewer", "active")
	if f, p, g := staffDetailFootprintOf(t); f != 3 || p != 3 || g != 18 {
		t.Fatalf("before the Down: %d functions, %d staff_read_lookup policies, %d column grants, want 3, 3, 18", f, p, g)
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), versions...)
	if f, p, g := staffDetailFootprintOf(t); f != 0 || p != 0 || g != 0 {
		t.Errorf("after the Down: %d functions, %d staff_read_lookup policies, %d column grants, want 0, 0, 0", f, p, g)
	}
	if f := readStaffReadFootprint(t); !f.function || !f.policy || !f.isHouseMemberExecute || f.tenantColumnGrants != 4 {
		t.Errorf("after the Down: SUPP-02 footprint = %+v, want staff_read_tenants, its policy, is_house_member EXECUTE and 4 tenants column grants", f)
	}
	if got := collectStaffTenantRows(t, h.app, member); len(got) == 0 {
		t.Error("after the Down: staff_read_tenants returned no rows to an active house member")
	}

	reapply()
	if f, p, g := staffDetailFootprintOf(t); f != 3 || p != 3 || g != 18 {
		t.Errorf("after the Up: %d functions, %d staff_read_lookup policies, %d column grants, want 3, 3, 18", f, p, g)
	}
	var found bool
	for _, r := range collectTenantMembers(t, h.app, member, house) {
		found = found || r.user == member
	}
	if !found {
		t.Error("after the Up: staff_read_tenant_members of the house tenant does not list the house member")
	}
}

// Cross-tenant refusal: a customer member reads no staff function, even for its own tenant with that tenant set.
func TestRLS_StaffReadTenantDetailRefusesACustomerAndIgnoresTheSetTenant(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x := seedDetailTenant(t, "customer refusal")
	customer := seedMemberAt(t, x, "admin", "active", t0, nil, nil)
	seedInvoiceAt(t, x, seedEntity(t, x), uuid.NewString(), "REF-1", "draft", t0)

	calls := []struct {
		fn   string
		call func(actor string) (int, error)
	}{
		{"staff_read_tenant_activity", func(actor string) (int, error) {
			return withTenantRows(t, x, `SELECT * FROM public.staff_read_tenant_activity($1::uuid)`, actor)
		}},
		{"staff_read_tenant_members", func(actor string) (int, error) {
			return withTenantRows(t, x, `SELECT * FROM public.staff_read_tenant_members($1::uuid, $2::uuid)`, actor, x)
		}},
		{"staff_read_tenant_invoices", func(actor string) (int, error) {
			return withTenantRows(t, x, `SELECT * FROM public.staff_read_tenant_invoices($1::uuid, $2::uuid)`, actor, x)
		}},
	}
	for _, c := range calls {
		if n, err := c.call(staff); err != nil || n == 0 {
			t.Errorf("%s control: staff with tenant X set = %d rows, err %v, want rows and no error", c.fn, n, err)
		}
		n, err := c.call(customer)
		if n != 0 {
			t.Errorf("%s: customer member of X with X set got %d rows, want 0", c.fn, n)
		}
		requireNotStaffRefusal(t, c.fn+" as a customer member", err, c.fn)
	}

	// A tenant set on the connection does not narrow a staff read to that tenant.
	other := seedDetailTenant(t, "customer refusal other")
	seedMemberAt(t, other, "admin", "active", t0, nil, nil)
	n, err := withTenantRows(t, x, `SELECT * FROM public.staff_read_tenant_members($1::uuid, $2::uuid)`, staff, other)
	if err != nil || n != 1 {
		t.Errorf("staff reading another tenant's members with X set = %d rows, err %v, want 1 and no error", n, err)
	}
}

// withTenantRows runs one statement as invoice_app with tenant set and returns the row count.
func withTenantRows(t *testing.T, tenant, sql string, args ...any) (int, error) {
	t.Helper()
	var n int
	var callErr error
	if err := db.WithinTenantTx(context.Background(), h.app, tenant, func(tx pgx.Tx) error {
		n, callErr = ruleDraftTry(t, tx, sql, args...)
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
	return n, callErr
}

func TestRLS_StaffReadTenantMembersTiesBreakByUserIdAscending(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x := seedDetailTenant(t, "members tie")

	users := make([]string, 6)
	for i := range users {
		users[i] = uuid.NewString()
	}
	sort.Strings(users)
	// Largest id first, so heap order is the reverse of the expected order.
	for i := len(users) - 1; i >= 0; i-- {
		seedMemberAsUser(t, x, users[i], "preparer", "active", t0, nil, nil)
	}

	got := collectTenantMembers(t, h.app, staff, x)
	if len(got) != len(users) {
		t.Fatalf("staff_read_tenant_members(X) = %d rows, want %d", len(got), len(users))
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.user)
	}
	if !reflect.DeepEqual(ids, users) {
		t.Errorf("members with equal created_at = %v, want ascending user_id %v", ids, users)
	}
}

// invoices.status CHECK values: invoice_count must count every one.
var staffDetailInvoiceStatuses = []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"}

func TestRLS_StaffReadTenantActivityCountsEveryInvoiceStatusAndKeepsTenantsApart(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	all, noisy, inactive := seedDetailTenant(t, "activity all statuses"), seedDetailTenant(t, "activity noisy"), seedDetailTenant(t, "activity inactive only")

	entity := seedEntity(t, all)
	for i, st := range staffDetailInvoiceStatuses {
		seedInvoiceAt(t, all, entity, uuid.NewString(), fmt.Sprintf("ALL-%d", i), st, t0)
	}
	seedMemberAt(t, all, "admin", "active", t0, nil, nil)
	seedAuditAt(t, all, t0)

	// A later audit row, more members and more invoices on another tenant must not move the first tenant's numbers.
	noisyEntity := seedEntity(t, noisy)
	for i := 0; i < 5; i++ {
		seedInvoiceAt(t, noisy, noisyEntity, uuid.NewString(), fmt.Sprintf("NOISY-%d", i), "accepted", t0)
		seedMemberAt(t, noisy, "preparer", "active", t0, nil, nil)
	}
	seedAuditAt(t, noisy, t0.Add(time.Hour))

	seedMemberAt(t, inactive, "admin", "invited", t0, nil, nil)
	seedMemberAt(t, inactive, "admin", "suspended", t0, nil, nil)

	got := collectTenantActivity(t, h.app, staff)
	byTenant := map[string]tenantActivityRow{}
	for _, r := range got {
		byTenant[r.tenant] = r
	}
	if r := byTenant[all]; r.active != 1 || r.invoice != int64(len(staffDetailInvoiceStatuses)) || r.last == nil || !r.last.Equal(t0) {
		t.Errorf("tenant with every invoice status = active %d, invoices %d, last %v, want 1, %d, %v", r.active, r.invoice, r.last, len(staffDetailInvoiceStatuses), t0)
	}
	if r := byTenant[noisy]; r.active != 5 || r.invoice != 5 || r.last == nil || !r.last.Equal(t0.Add(time.Hour)) {
		t.Errorf("noisy tenant = active %d, invoices %d, last %v, want 5, 5, %v", r.active, r.invoice, r.last, t0.Add(time.Hour))
	}
	if r, ok := byTenant[inactive]; !ok || r.active != 0 || r.invoice != 0 || r.last != nil {
		t.Errorf("tenant with only invited and suspended members = %+v (present %v), want one row of 0, 0, NULL", r, ok)
	}
}

func TestRLS_StaffReadTenantInvoicesLimitBoundaryAndEmptyTenants(t *testing.T) {
	requireHarness(t)
	requireStaffDetailFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	exact, few, bare := seedDetailTenant(t, "invoices exactly the limit"), seedDetailTenant(t, "invoices few"), seedDetailTenant(t, "invoices none")
	memberless := seedDetailTenant(t, "members none")
	seedMemberAt(t, bare, "admin", "active", t0, nil, nil)
	seedInvoiceAt(t, memberless, seedEntity(t, memberless), uuid.NewString(), "MEMBERLESS-1", "draft", t0)

	for _, c := range []struct {
		tenant string
		n      int
	}{{exact, staffTenantInvoiceLimit}, {few, 3}} {
		entity := seedEntity(t, c.tenant)
		for i := 0; i < c.n; i++ {
			seedInvoiceAt(t, c.tenant, entity, uuid.NewString(), fmt.Sprintf("B-%02d", i), "draft", t0.Add(time.Duration(i)*time.Second))
		}
		got := collectTenantInvoices(t, h.app, staff, c.tenant)
		if len(got) != c.n {
			t.Fatalf("a tenant with %d invoices returned %d rows, want all %d", c.n, len(got), c.n)
		}
		for i, r := range got {
			if want := fmt.Sprintf("B-%02d", c.n-1-i); r.number != want {
				t.Errorf("a tenant with %d invoices: row %d = %s, want %s (newest first)", c.n, i, r.number, want)
			}
		}
	}

	if n := len(collectTenantInvoices(t, h.app, staff, bare)); n != 0 {
		t.Errorf("invoices of a tenant with a member and no invoice = %d rows, want 0", n)
	}
	if n := len(collectTenantMembers(t, h.app, staff, memberless)); n != 0 {
		t.Errorf("members of a tenant with an invoice and no member = %d rows, want 0", n)
	}
	if n := len(collectTenantMembers(t, h.app, staff, bare)); n != 1 {
		t.Errorf("control: members of the tenant with one member = %d rows, want 1", n)
	}
	if n := len(collectTenantInvoices(t, h.app, staff, memberless)); n != 1 {
		t.Errorf("control: invoices of the tenant with one invoice = %d rows, want 1", n)
	}
}

// Runtime check of the detail tables, submission_jobs and invoice_status_history: the catalog test pins the column sets, this reads and writes as staff_reader.
func TestRLS_StaffReaderReadsOnlyGrantedColumnsAndWritesNothingOnTheDetailTables(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	x := seedDetailTenant(t, "staff reader writes")
	seedMemberAt(t, x, "admin", "active", t0, nil, nil)
	srwInvoice := uuid.NewString()
	seedInvoiceAt(t, x, seedEntity(t, x), srwInvoice, "SRW-1", "draft", t0)
	seedAuditAt(t, x, t0)
	seedJobAt(t, x, srwInvoice, "queued", t0, nil, nil)
	seedHistoryAt(t, x, srwInvoice, nil, "draft", "srw", t0)

	tx := revokeProbeTx(t)
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)
	txExec(t, tx, `SET LOCAL ROLE staff_reader`)
	for _, table := range []string{"memberships", "invoices", "audit_log", "submission_jobs", "invoice_status_history"} {
		if n := mustCount(t, tx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, x); n == 0 {
			t.Fatalf("control: staff_reader reads no %s row of the seeded tenant", table)
		}
	}

	try := func(sql string) (int64, error) {
		txExec(t, tx, `SAVEPOINT w`)
		tag, err := tx.Exec(ctx, sql, x)
		txExec(t, tx, `ROLLBACK TO SAVEPOINT w`)
		return tag.RowsAffected(), err
	}
	for _, c := range []struct{ what, sql string }{
		{"read memberships.id", `SELECT id FROM memberships WHERE tenant_id = $1`},
		{"read invoices.entity_id", `SELECT entity_id FROM invoices WHERE tenant_id = $1`},
		{"read audit_log.entity_id", `SELECT entity_id FROM audit_log WHERE tenant_id = $1`},
		{"read submission_jobs.idempotency_key", `SELECT idempotency_key FROM submission_jobs WHERE tenant_id = $1`},
		{"read invoice_status_history.cause_rule_set_version_id", `SELECT cause_rule_set_version_id FROM invoice_status_history WHERE tenant_id = $1`},
	} {
		_, err := try(c.sql)
		assertPgRefusal(t, "staff_reader "+c.what, err, sqlstateInsufficientPrivilege, "permission denied for table")
	}
	// Positive pair of the entity_id refusal: the four columns the audit-search migration grants read fine.
	if n := mustCount(t, tx, `SELECT count(*) FROM (SELECT id, event, actor, payload FROM audit_log WHERE tenant_id = $1) r`, x); n == 0 {
		t.Error("staff_reader reads no audit_log row through id, event, actor and payload of the seeded tenant")
	}
	for _, c := range []struct{ what, sql string }{
		{"INSERT memberships", `INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'admin', 'active')`},
		{"UPDATE memberships", `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1`},
		{"DELETE memberships", `DELETE FROM memberships WHERE tenant_id = $1`},
		{"UPDATE invoices", `UPDATE invoices SET status = 'failed' WHERE tenant_id = $1`},
		{"DELETE invoices", `DELETE FROM invoices WHERE tenant_id = $1`},
		{"INSERT audit_log", `INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1, 'x', 'staff_read.write')`},
		{"UPDATE audit_log", `UPDATE audit_log SET created_at = now() WHERE tenant_id = $1`},
		{"DELETE audit_log", `DELETE FROM audit_log WHERE tenant_id = $1`},
		{"INSERT submission_jobs", `INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version) VALUES ($1, gen_random_uuid(), 'srw', 'mock', 'v1')`},
		{"UPDATE submission_jobs", `UPDATE submission_jobs SET state = 'failed' WHERE tenant_id = $1`},
		{"DELETE submission_jobs", `DELETE FROM submission_jobs WHERE tenant_id = $1`},
		{"INSERT invoice_status_history", `INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, gen_random_uuid(), 'draft', 'srw')`},
		{"UPDATE invoice_status_history", `UPDATE invoice_status_history SET to_status = 'failed' WHERE tenant_id = $1`},
		{"DELETE invoice_status_history", `DELETE FROM invoice_status_history WHERE tenant_id = $1`},
	} {
		n, err := try(c.sql)
		if err == nil && n != 0 {
			t.Errorf("staff_reader %s changed %d rows, want a refusal or 0 rows", c.what, n)
		}
		var pgErr *pgconn.PgError
		if err != nil && (!errors.As(err, &pgErr) || pgErr.Code != sqlstateInsufficientPrivilege) {
			t.Errorf("staff_reader %s failed with %v, want SQLSTATE %s", c.what, err, sqlstateInsufficientPrivilege)
		}
	}
}

// staff_read_submission_jobs, staff_read_submission_job and staff_read_invoice_status_history.

const (
	staffReadJobsSig    = "public.staff_read_submission_jobs(uuid, text, uuid)"
	staffReadJobSig     = "public.staff_read_submission_job(uuid, uuid)"
	staffReadHistorySig = "public.staff_read_invoice_status_history(uuid, uuid, uuid)"

	// staffJobsLimit is the LIMIT in staff_read_submission_jobs; no Go constant carries it yet.
	staffJobsLimit = 200
)

var staffJobSigs = []string{staffReadJobsSig, staffReadJobSig, staffReadHistorySig}

// requireStaffJobFunctions fails by name while the submission-jobs migration is not applied.
func requireStaffJobFunctions(t *testing.T) {
	t.Helper()
	for _, sig := range staffJobSigs {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Fatalf("function %s not found: the staff_read_submission_jobs migration is not applied", sig)
		}
	}
}

// seedJobAt commits a submission job and deletes it on cleanup, before the invoice it references (RESTRICT).
func seedJobAt(t *testing.T, tenant, invoice, state string, createdAt time.Time, pollRef, lastError *string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, attempts, last_error, poll_ref, created_at)
		 VALUES ($1, $2, $3, $4, 'mock', 'v1', $5, 3, $6, $7, $8)`,
		id, tenant, invoice, "staff-read-"+id, state, lastError, pollRef, createdAt); err != nil {
		t.Fatalf("seed a %s submission job: %v", state, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE id = $1`, id) })
	return id
}

// seedHistoryAt commits a status-history row; the invoice's cleanup cascades it away.
func seedHistoryAt(t *testing.T, tenant, invoice string, from *string, to, actor string, changedAt time.Time) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		tenant, invoice, from, to, actor, changedAt); err != nil {
		t.Fatalf("seed a status-history row %s: %v", actor, err)
	}
}

// seedJobInvoice commits an entity and an invoice at status under tenant and returns the invoice id.
func seedJobInvoice(t *testing.T, tenant, number, status string) string {
	t.Helper()
	id := uuid.NewString()
	seedInvoiceAt(t, tenant, seedEntity(t, tenant), id, number, status, time.Now().UTC().Truncate(time.Second))
	return id
}

func tenantBInvoice(t *testing.T) string {
	t.Helper()
	return seedJobInvoice(t, h.tenantB, "ISO-"+uuid.NewString()[:8], "draft")
}

type staffJobRow struct {
	id, tenant, tenantName, invoice, number, invoiceStatus, state string
	attempts                                                      int32
	lastError                                                     *string
	acknowledged                                                  bool
	createdAt, updatedAt                                          time.Time
}

func scanStaffJob(r pgx.CollectableRow) (staffJobRow, error) {
	var x staffJobRow
	// Scans by position: pins the result column order and count.
	return x, r.Scan(&x.id, &x.tenant, &x.tenantName, &x.invoice, &x.number, &x.invoiceStatus,
		&x.state, &x.attempts, &x.lastError, &x.acknowledged, &x.createdAt, &x.updatedAt)
}

func collectStaffJobs(t *testing.T, q rowQuerier, actor string, state, tenant any) []staffJobRow {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_submission_jobs($1::uuid, $2::text, $3::uuid)`, actor, state, tenant)
	if err != nil {
		t.Fatalf("staff_read_submission_jobs as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, scanStaffJob)
	if err != nil {
		t.Fatalf("collect staff_read_submission_jobs rows: %v", err)
	}
	return got
}

func collectStaffJob(t *testing.T, q rowQuerier, actor, id string) []staffJobRow {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_submission_job($1::uuid, $2::uuid)`, actor, id)
	if err != nil {
		t.Fatalf("staff_read_submission_job as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, scanStaffJob)
	if err != nil {
		t.Fatalf("collect staff_read_submission_job rows: %v", err)
	}
	return got
}

type staffStepRow struct {
	from      *string
	to, actor string
	changedAt time.Time
}

func collectStaffSteps(t *testing.T, q rowQuerier, actor, tenant, invoice string) []staffStepRow {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT * FROM public.staff_read_invoice_status_history($1::uuid, $2::uuid, $3::uuid)`, actor, tenant, invoice)
	if err != nil {
		t.Fatalf("staff_read_invoice_status_history as invoice_app: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffStepRow, error) {
		var x staffStepRow
		return x, r.Scan(&x.from, &x.to, &x.actor, &x.changedAt)
	})
	if err != nil {
		t.Fatalf("collect staff_read_invoice_status_history rows: %v", err)
	}
	return got
}

// jobsFutureBase is newer than any leftover job row, so the 200 cap keeps the seeded rows.
func jobsFutureBase() time.Time { return time.Now().UTC().Truncate(time.Second).Add(time.Hour) }

func jobIDs(rows []staffJobRow) []string {
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	return ids
}

func TestRLS_StaffReadSubmissionJobFunctionsExist(t *testing.T) {
	requireHarness(t)
	for _, sig := range staffJobSigs {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Errorf("function %s not found, want it created by the staff_read_submission_jobs migration", sig)
		}
	}
	// The generic catalog tests iterate every staff_read_ function: they must now see the three.
	names := map[string]bool{}
	for _, fn := range staffReadFunctions(t) {
		names[fn.name] = true
	}
	for _, name := range []string{"staff_read_submission_jobs", "staff_read_submission_job", "staff_read_invoice_status_history"} {
		if !names[name] {
			t.Errorf("the staff_read_ catalog scan does not see %s", name)
		}
	}
}

func TestRLS_StaffReadSubmissionJobsListsEveryTenantNewestFirst(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	base := jobsFutureBase()
	x, y := seedDetailTenant(t, "jobs list x"), seedDetailTenant(t, "jobs list y")
	invX, invY := seedJobInvoice(t, x, "JOB-X", "failed"), seedJobInvoice(t, y, "JOB-Y", "queued")

	oldX := seedJobAt(t, x, invX, "dead_lettered", base.Add(1*time.Second), nil, strPtr("adapter down"))
	newY := seedJobAt(t, y, invY, "queued", base.Add(3*time.Second), strPtr("poll-ref-1"), nil)
	midX := seedJobAt(t, x, invX, "submitting", base.Add(2*time.Second), nil, nil)

	got := collectStaffJobs(t, h.app, staff, nil, nil)
	if len(got) == 0 {
		t.Fatal("staff_read_submission_jobs returned no rows to an active house member")
	}
	for i := 1; i < len(got); i++ {
		if got[i].createdAt.After(got[i-1].createdAt) {
			t.Fatalf("row %d (%s) is newer than row %d (%s), want created_at descending", i, got[i].createdAt, i-1, got[i-1].createdAt)
		}
	}
	if want := []string{newY, midX, oldX}; !reflect.DeepEqual(jobIDs(got)[:len(want)], want) {
		t.Fatalf("first rows = %v, want the seeded jobs of both tenants newest first %v", jobIDs(got), want)
	}

	byID := map[string]staffJobRow{}
	for _, r := range got {
		byID[r.id] = r
	}
	for _, c := range []struct {
		id                                string
		tenant, name, invoice, number, st string
		state                             string
		ack                               bool
		lastError                         *string
		at                                time.Time
	}{
		{oldX, x, "jobs list x", invX, "JOB-X", "failed", "dead_lettered", false, strPtr("adapter down"), base.Add(1 * time.Second)},
		{newY, y, "jobs list y", invY, "JOB-Y", "queued", "queued", true, nil, base.Add(3 * time.Second)},
		{midX, x, "jobs list x", invX, "JOB-X", "failed", "submitting", false, nil, base.Add(2 * time.Second)},
	} {
		r := byID[c.id]
		if r.tenant != c.tenant || r.tenantName != c.name || r.invoice != c.invoice || r.number != c.number || r.invoiceStatus != c.st ||
			r.state != c.state || r.attempts != 3 || r.acknowledged != c.ack || !r.createdAt.Equal(c.at) || r.updatedAt.IsZero() ||
			!reflect.DeepEqual(r.lastError, c.lastError) {
			t.Errorf("job %s = %+v, want tenant %s %q, invoice %s %q %s, state %s, attempts 3, acknowledged %v, last_error %v, created %s",
				c.id, r, c.tenant, c.name, c.invoice, c.number, c.st, c.state, c.ack, c.lastError, c.at)
		}
	}
}

func TestRLS_StaffReadSubmissionJobsFiltersByStateAndTenant(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	base := jobsFutureBase()
	x, y := seedDetailTenant(t, "jobs filter x"), seedDetailTenant(t, "jobs filter y")
	invX, invY := seedJobInvoice(t, x, "FLT-X", "failed"), seedJobInvoice(t, y, "FLT-Y", "failed")

	deadX := seedJobAt(t, x, invX, "dead_lettered", base.Add(1*time.Second), nil, nil)
	queuedX := seedJobAt(t, x, invX, "queued", base.Add(2*time.Second), nil, nil)
	deadY := seedJobAt(t, y, invY, "dead_lettered", base.Add(3*time.Second), nil, nil)
	queuedY := seedJobAt(t, y, invY, "queued", base.Add(4*time.Second), nil, nil)

	seeded := map[string]bool{deadX: true, queuedX: true, deadY: true, queuedY: true}
	onlySeeded := func(rows []staffJobRow) []string {
		ids := []string{}
		for _, r := range rows {
			if seeded[r.id] {
				ids = append(ids, r.id)
			}
		}
		return ids
	}

	byState := collectStaffJobs(t, h.app, staff, "dead_lettered", nil)
	if want := []string{deadY, deadX}; !reflect.DeepEqual(onlySeeded(byState), want) {
		t.Errorf("state filter dead_lettered returned seeded jobs %v, want %v", onlySeeded(byState), want)
	}
	for _, r := range byState {
		if r.state != "dead_lettered" {
			t.Errorf("state filter dead_lettered returned job %s in state %s", r.id, r.state)
		}
	}

	byTenant := collectStaffJobs(t, h.app, staff, nil, x)
	if want := []string{queuedX, deadX}; !reflect.DeepEqual(jobIDs(byTenant), want) {
		t.Errorf("tenant filter X returned %v, want exactly %v", jobIDs(byTenant), want)
	}

	both := collectStaffJobs(t, h.app, staff, "dead_lettered", x)
	if want := []string{deadX}; !reflect.DeepEqual(jobIDs(both), want) {
		t.Errorf("state dead_lettered and tenant X returned %v, want exactly %v", jobIDs(both), want)
	}

	none := collectStaffJobs(t, h.app, staff, "accepted", x)
	if len(none) != 0 {
		t.Errorf("state accepted and tenant X returned %v, want no row", jobIDs(none))
	}
}

func TestRLS_StaffReadSubmissionJobsCapsAtTwoHundred(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	base := jobsFutureBase()
	z := seedDetailTenant(t, "jobs cap")
	inv := seedJobInvoice(t, z, "CAP-1", "queued")

	n := staffJobsLimit + 1
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, created_at)
		 SELECT $1::uuid, $2::uuid, 'staff-read-cap-' || g, 'mock', 'v1', 'queued', $3::timestamptz + g * interval '1 second'
		   FROM generate_series(1, $4::int) g`, z, inv, base, n); err != nil {
		t.Fatalf("seed %d jobs: %v", n, err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE tenant_id = $1`, z)
	})
	var oldest, newest string
	if err := h.super.QueryRow(context.Background(),
		`SELECT (SELECT id::text FROM submission_jobs WHERE tenant_id = $1 ORDER BY created_at LIMIT 1),
		        (SELECT id::text FROM submission_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1)`, z).Scan(&oldest, &newest); err != nil {
		t.Fatalf("read the oldest and newest seeded job: %v", err)
	}

	for name, got := range map[string][]staffJobRow{
		"no filter":     collectStaffJobs(t, h.app, staff, nil, nil),
		"tenant filter": collectStaffJobs(t, h.app, staff, nil, z),
	} {
		if len(got) != staffJobsLimit {
			t.Errorf("%s: %d jobs seeded in one tenant returned %d rows, want %d", name, n, len(got), staffJobsLimit)
			continue
		}
		if got[0].id != newest {
			t.Errorf("%s: first row = %s, want the newest job %s", name, got[0].id, newest)
		}
		for _, id := range jobIDs(got) {
			if id == oldest {
				t.Errorf("%s: the oldest job %s is in the capped result, want it cut", name, oldest)
			}
		}
	}
}

func TestRLS_StaffReadSubmissionJobReturnsOneJobOrNoRow(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	base := jobsFutureBase()
	x, y := seedDetailTenant(t, "job one x"), seedDetailTenant(t, "job one y")
	invX, invY := seedJobInvoice(t, x, "ONE-X", "failed"), seedJobInvoice(t, y, "ONE-Y", "queued")
	want := seedJobAt(t, x, invX, "dead_lettered", base, strPtr("poll-ref-2"), strPtr("gave up"))
	other := seedJobAt(t, y, invY, "queued", base.Add(time.Second), nil, nil)

	got := collectStaffJob(t, h.app, staff, want)
	if len(got) != 1 {
		t.Fatalf("staff_read_submission_job(%s) = %d rows, want exactly 1", want, len(got))
	}
	r := got[0]
	if r.id != want || r.tenant != x || r.tenantName != "job one x" || r.invoice != invX || r.number != "ONE-X" || r.invoiceStatus != "failed" ||
		r.state != "dead_lettered" || r.attempts != 3 || !r.acknowledged || !r.createdAt.Equal(base) ||
		r.lastError == nil || *r.lastError != "gave up" {
		t.Errorf("staff_read_submission_job = %+v, want job %s of tenant X (%s), dead_lettered, acknowledged, last_error %q", r, want, x, "gave up")
	}
	if n := len(collectStaffJob(t, h.app, staff, other)); n != 1 {
		t.Errorf("control: the job of another tenant returned %d rows, want 1", n)
	}
	if n := len(collectStaffJob(t, h.app, staff, uuid.NewString())); n != 0 {
		t.Errorf("a random job id returned %d rows, want 0", n)
	}
}

func TestRLS_StaffReadInvoiceStatusHistoryReadsOneInvoiceOldestFirst(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	tenantX, tenantOther := seedDetailTenant(t, "history x"), seedDetailTenant(t, "history other")
	invX, invY := seedJobInvoice(t, tenantX, "HIS-X", "failed"), seedJobInvoice(t, tenantX, "HIS-Y", "validated")

	// Inserted newest first, so heap order is the reverse of the expected order.
	seedHistoryAt(t, tenantX, invX, strPtr("queued"), "failed", "x-3", t0.Add(3*time.Minute))
	seedHistoryAt(t, tenantX, invX, strPtr("draft"), "queued", "x-2", t0.Add(2*time.Minute))
	seedHistoryAt(t, tenantX, invX, nil, "draft", "x-1", t0.Add(1*time.Minute))
	seedHistoryAt(t, tenantX, invY, nil, "draft", "y-1", t0.Add(1*time.Minute))
	seedHistoryAt(t, tenantX, invY, strPtr("draft"), "validated", "y-2", t0.Add(2*time.Minute))
	// X's id filed under another tenant: a dangling cross-tenant row the tenant predicate must keep out.
	seedHistoryAt(t, tenantOther, invX, nil, "draft", "other-1", t0.Add(4*time.Minute))

	got := collectStaffSteps(t, h.app, staff, tenantX, invX)
	want := []staffStepRow{
		{nil, "draft", "x-1", t0.Add(1 * time.Minute)},
		{strPtr("draft"), "queued", "x-2", t0.Add(2 * time.Minute)},
		{strPtr("queued"), "failed", "x-3", t0.Add(3 * time.Minute)},
	}
	if len(got) == 0 {
		t.Fatal("staff_read_invoice_status_history returned no rows to an active house member")
	}
	for i := range got {
		got[i].changedAt = got[i].changedAt.UTC()
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("history of invoice X in tenant X =\n%+v\nwant X's three steps oldest first:\n%+v", got, want)
	}

	if other := collectStaffSteps(t, h.app, staff, tenantOther, invX); len(other) != 1 || other[0].actor != "other-1" {
		t.Errorf("history of invoice X under the other tenant = %+v, want only its own row other-1", other)
	}
	if n := len(collectStaffSteps(t, h.app, staff, tenantX, uuid.NewString())); n != 0 {
		t.Errorf("history of an unknown invoice returned %d rows, want 0", n)
	}
}

type staffJobsFootprint struct{ functions, policies, columnGrants int }

func readStaffJobsFootprint(t *testing.T) staffJobsFootprint {
	t.Helper()
	var f staffJobsFootprint
	if err := h.super.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM unnest($1::text[]) s WHERE to_regprocedure(s) IS NOT NULL),
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND policyname = 'staff_read_lookup'
		         AND tablename IN ('submission_jobs', 'invoice_status_history')),
		       (SELECT count(*) FROM pg_attribute a, aclexplode(a.attacl) x
		         WHERE a.attrelid IN ('public.submission_jobs'::regclass, 'public.invoice_status_history'::regclass)
		           AND NOT a.attisdropped AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader'))`,
		staffJobSigs).Scan(&f.functions, &f.policies, &f.columnGrants); err != nil {
		t.Fatalf("read the staff_read_submission_jobs footprint: %v", err)
	}
	return f
}

// staffJobsMigrationVersion is the one migration that creates staff_read_submission_jobs.
func staffJobsMigrationVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if bytes.Contains(raw, []byte("CREATE FUNCTION public.staff_read_submission_jobs")) {
			v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
			if err != nil {
				t.Fatalf("parse goose version out of %q: %v", f, err)
			}
			return v
		}
	}
	t.Fatal("no migration holds CREATE FUNCTION public.staff_read_submission_jobs")
	return 0
}

// Rolls back this migration and any newer staff_read_ one, newest first: the older footprints must survive.
func TestRLS_StaffReadSubmissionJobsDownAndUp(t *testing.T) {
	requireHarness(t)
	version := staffJobsMigrationVersion(t)
	var versions []int64
	for _, v := range staffReadMigrationVersions(t) {
		if v >= version {
			versions = append(versions, v)
		}
	}
	member := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, "jobs down and up")
	job := seedJobAt(t, x, seedJobInvoice(t, x, "DU-1", "queued"), "queued", jobsFutureBase(), nil, nil)

	want := staffJobsFootprint{functions: 3, policies: 2, columnGrants: 16}
	if f := readStaffJobsFootprint(t); f != want {
		t.Fatalf("before the Down: footprint = %+v, want %+v", f, want)
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), versions...)
	if f := readStaffJobsFootprint(t); f != (staffJobsFootprint{}) {
		t.Errorf("after the Down: footprint = %+v, want no function, no policy and no column grant", f)
	}
	if f := readStaffReadFootprint(t); !f.function || !f.policy || !f.isHouseMemberExecute || f.tenantColumnGrants != 4 {
		t.Errorf("after the Down: SUPP-02 footprint = %+v, want staff_read_tenants, its policy, is_house_member EXECUTE and 4 tenants column grants", f)
	}
	if f, p, g := staffDetailFootprintOf(t); f != 3 || p != 3 || g != 14 {
		t.Errorf("after the Down: SUPP-05 footprint = %d functions, %d policies, %d column grants, want 3, 3, 14", f, p, g)
	}
	for _, table := range []string{"public.submission_jobs", "public.invoice_status_history"} {
		var app, reader bool
		if err := h.super.QueryRow(context.Background(),
			`SELECT has_table_privilege('invoice_app', $1, 'SELECT'), has_any_column_privilege('staff_reader', $1, 'SELECT')`, table).Scan(&app, &reader); err != nil {
			t.Fatalf("read privileges on %s: %v", table, err)
		}
		if !app || reader {
			t.Errorf("after the Down: %s invoice_app SELECT = %v, staff_reader column SELECT = %v, want true, false", table, app, reader)
		}
	}

	reapply()
	if f := readStaffJobsFootprint(t); f != want {
		t.Errorf("after the Up: footprint = %+v, want %+v", f, want)
	}
	var found bool
	for _, r := range collectStaffJobs(t, h.app, member, nil, x) {
		found = found || r.id == job
	}
	if !found {
		t.Error("after the Up: staff_read_submission_jobs does not list the seeded job")
	}
}

// Equal sort keys fall back to id ascending, so a read never reshuffles tied rows.
func TestRLS_StaffReadSubmissionJobsAndHistoryTieBreakByIdAscending(t *testing.T) {
	requireHarness(t)
	requireStaffJobFunctions(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, "tie break")
	inv := seedJobInvoice(t, x, "TIE-1", "queued")
	at := jobsFutureBase()

	ids := make([]string, 8)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	sort.Strings(ids)
	// Inserted newest id first, so heap order is the reverse of the expected order.
	for i := len(ids) - 1; i >= 0; i-- {
		if _, err := h.super.Exec(context.Background(),
			`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, created_at) VALUES ($1, $2, $3, $4, 'mock', 'v1', $5)`,
			ids[i], x, inv, "tie-"+ids[i], at); err != nil {
			t.Fatalf("seed tied job %s: %v", ids[i], err)
		}
		if _, err := h.super.Exec(context.Background(),
			`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, to_status, actor, changed_at) VALUES ($1, $2, $3, 'draft', $4, $5)`,
			ids[i], x, inv, fmt.Sprintf("tie-%d", i), at); err != nil {
			t.Fatalf("seed tied history row %s: %v", ids[i], err)
		}
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE tenant_id = $1`, x)
	})

	if got := jobIDs(collectStaffJobs(t, h.app, staff, nil, x)); !reflect.DeepEqual(got, ids) {
		t.Errorf("tied jobs = %v, want id ascending %v", got, ids)
	}
	var gotActors, wantActors []string
	for i, s := range collectStaffSteps(t, h.app, staff, x, inv) {
		gotActors = append(gotActors, s.actor)
		wantActors = append(wantActors, fmt.Sprintf("tie-%d", i))
	}
	if len(gotActors) != len(ids) || !reflect.DeepEqual(gotActors, wantActors) {
		t.Errorf("tied history actors = %v, want %d rows in id order %v", gotActors, len(ids), wantActors)
	}
}
