// Storage suite for erp_field_mappings and erp_lookups: shape, RLS, grants, constraints, cascades, and the
// mapping_fingerprint column the same migration adds to erp_source_documents.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const empMigrationGlob = "*_erp_mappings.sql"

const (
	efmTable = "erp_field_mappings"
	elkTable = "erp_lookups"
)

var empTables = []string{efmTable, elkTable}

const efmInsertSQL = `INSERT INTO erp_field_mappings (tenant_id, entity_id, system, target, source_path)
	VALUES ($1, $2, $3, $4, $5)`

const elkInsertSQL = `INSERT INTO erp_lookups
	(tenant_id, entity_id, system, external_key, kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

// failIfEmpUndefined turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfEmpUndefined(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the erp_mappings migration is not applied yet: %v", what, c, err)
	}
}

func empWantErr(t *testing.T, err error, code, constraint, what string) {
	t.Helper()
	failIfEmpUndefined(t, what, err)
	erpWantErr(t, err, code, constraint, what)
}

func requireEmpTables(t *testing.T, q erpQuerier) {
	t.Helper()
	for _, table := range empTables {
		var reg *string
		if err := q.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(public.%s): %v", table, err)
		}
		if reg == nil {
			t.Fatalf("public.%s does not exist — the erp_mappings migration is not applied yet", table)
		}
	}
}

func empRand() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:12] }

// efmRow is one valid mapping row; a test changes the one field it attacks.
type efmRow struct{ system, target, path string }

func newEfm() efmRow { return efmRow{"fake", "f_" + empRand(), "description"} }

func (r efmRow) insert(q esdExecer, tenant, entity string) error {
	_, err := q.Exec(context.Background(), efmInsertSQL, tenant, entity, r.system, r.target, r.path)
	return err
}

func (r efmRow) seed(t *testing.T, tenant, entity string) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), efmInsertSQL+` RETURNING id::text`, tenant, entity, r.system, r.target, r.path).Scan(&id)
	failIfEmpUndefined(t, "seed erp_field_mappings", err)
	if err != nil {
		t.Fatalf("seed erp_field_mappings: %v", err)
	}
	return id
}

// elkRow is one lookup row; newElk returns a valid one of the kind with a fresh erp_key.
type elkRow struct {
	system, company, kind, key                            string
	taxCategory, exemption, hsn, service, tin, state, lga *string
}

func newElk(kind string) elkRow {
	r := elkRow{system: "fake", company: "fake-co-1", kind: kind, key: "k-" + empRand()}
	switch kind {
	case "tax_code":
		r.taxCategory = erpStr("T1")
	case "item":
		r.hsn = erpStr("8471.30")
	default:
		r.tin = erpStr("12345678-0001")
	}
	return r
}

// elkWith returns a valid row of kind after f has changed it.
func elkWith(kind string, f func(r *elkRow)) elkRow {
	r := newElk(kind)
	f(&r)
	return r
}

func (r elkRow) args(tenant, entity string) []any {
	return []any{tenant, entity, r.system, r.company, r.kind, r.key, r.taxCategory, r.exemption, r.hsn, r.service, r.tin, r.state, r.lga}
}

func (r elkRow) insert(q esdExecer, tenant, entity string) error {
	_, err := q.Exec(context.Background(), elkInsertSQL, r.args(tenant, entity)...)
	return err
}

func (r elkRow) seed(t *testing.T, tenant, entity string) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), elkInsertSQL+` RETURNING id::text`, r.args(tenant, entity)...).Scan(&id)
	failIfEmpUndefined(t, "seed erp_lookups", err)
	if err != nil {
		t.Fatalf("seed erp_lookups: %v", err)
	}
	return id
}

// empTbl lets one test body run over both tables: a valid row with a fresh key each call.
type empTbl struct {
	name   string
	insert func(q esdExecer, tenant, entity string) error
	seed   func(t *testing.T, tenant, entity string) string
}

var empTbls = []empTbl{
	{efmTable,
		func(q esdExecer, tenant, entity string) error { return newEfm().insert(q, tenant, entity) },
		func(t *testing.T, tenant, entity string) string { return newEfm().seed(t, tenant, entity) }},
	{elkTable,
		func(q esdExecer, tenant, entity string) error { return newElk("customer").insert(q, tenant, entity) },
		func(t *testing.T, tenant, entity string) string { return newElk("customer").seed(t, tenant, entity) }},
}

type empWorld struct{ tenant, entity string }

// empNewWorld makes a throwaway tenant with one client; the tenant delete cleans up.
func empNewWorld(t *testing.T, label string) empWorld {
	t.Helper()
	tenant := erpTenant(t)
	return empWorld{tenant, erpEntity(t, tenant, "emp-"+label)}
}

// ---- shape ----------------------------------------------------------------

var empShapes = map[string]map[string]erpCol{
	efmTable: {
		"tenant_id":   {"uuid", true, ""},
		"id":          {"uuid", true, "gen_random_uuid()"},
		"entity_id":   {"uuid", true, ""},
		"system":      {"text", true, ""},
		"target":      {"text", true, ""},
		"source_path": {"text", true, ""},
		"created_at":  {"timestamp with time zone", true, "now()"},
		"updated_at":  {"timestamp with time zone", true, "now()"},
	},
	elkTable: {
		"tenant_id":      {"uuid", true, ""},
		"id":             {"uuid", true, "gen_random_uuid()"},
		"entity_id":      {"uuid", true, ""},
		"system":         {"text", true, ""},
		"external_key":   {"text", true, ""},
		"kind":           {"text", true, ""},
		"erp_key":        {"text", true, ""},
		"tax_category":   {"text", false, ""},
		"exemption_code": {"text", false, ""},
		"hsn_code":       {"text", false, ""},
		"service_code":   {"text", false, ""},
		"tin":            {"text", false, ""},
		"state":          {"text", false, ""},
		"lga":            {"text", false, ""},
		"created_at":     {"timestamp with time zone", true, "now()"},
		"updated_at":     {"timestamp with time zone", true, "now()"},
	},
}

// The NOT NULLs are derived from empShapes (PG18 names each one).
var empNamedConstraints = map[string][]string{
	efmTable: {
		"erp_field_mappings_pk", "erp_field_mappings_tenant_id_id_uq", "erp_field_mappings_client_system_target_uq",
		"erp_field_mappings_system_check", "erp_field_mappings_target_check", "erp_field_mappings_path_check",
		"erp_field_mappings_tenant_entity_fk", "erp_field_mappings_tenant_id_fkey",
	},
	elkTable: {
		"erp_lookups_pk", "erp_lookups_tenant_id_id_uq", "erp_lookups_client_company_kind_key_uq",
		"erp_lookups_system_check", "erp_lookups_external_key_check", "erp_lookups_kind_check", "erp_lookups_key_check",
		"erp_lookups_values_check", "erp_lookups_tin_check",
		"erp_lookups_tenant_entity_fk", "erp_lookups_tenant_id_fkey",
	},
}

// The unique constraints are the only indexes besides the primary key.
var empIndexes = map[string][]string{
	efmTable: {"erp_field_mappings_client_system_target_uq", "erp_field_mappings_pk", "erp_field_mappings_tenant_id_id_uq"},
	elkTable: {"erp_lookups_client_company_kind_key_uq", "erp_lookups_pk", "erp_lookups_tenant_id_id_uq"},
}

func empConstraintsWant(table string) []string {
	want := slices.Clone(empNamedConstraints[table])
	for col, c := range empShapes[table] {
		if c.notNull {
			want = append(want, table+"_"+col+"_not_null")
		}
	}
	sort.Strings(want)
	return want
}

func empNames(q erpQuerier, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(context.Background(), sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// empShapeProblems compares both live tables to the Design: columns, constraint names and definitions, indexes.
func empShapeProblems(q erpQuerier) []string {
	problems := erpShapeProblemsOf(q, empShapes)
	for _, table := range empTables {
		cons, err := empNames(q, `SELECT conname FROM pg_constraint WHERE conrelid = to_regclass($1) ORDER BY conname`, "public."+table)
		if err != nil {
			problems = append(problems, table+": read pg_constraint: "+err.Error())
			continue
		}
		if want := empConstraintsWant(table); !slices.Equal(cons, want) {
			problems = append(problems, "constraints of "+table+" = "+strings.Join(cons, ",")+"\n  want "+strings.Join(want, ","))
		}
		idx, err := empNames(q, `SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1 ORDER BY indexname`, table)
		if err != nil {
			problems = append(problems, table+": read pg_indexes: "+err.Error())
			continue
		}
		if !slices.Equal(idx, empIndexes[table]) {
			problems = append(problems, "indexes of "+table+" = "+strings.Join(idx, ",")+", want "+strings.Join(empIndexes[table], ","))
		}
	}
	type def = struct {
		name       string
		has, lacks []string
	}
	fk := func(table string) []def {
		return []def{
			{table + "_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}, nil},
			// Composite: RI checks bypass RLS, so only this FK refuses another tenant's client.
			{table + "_tenant_entity_fk",
				[]string{"FOREIGN KEY (tenant_id, entity_id)", "REFERENCES business_entities(tenant_id, id)", "ON DELETE CASCADE"}, nil},
			{table + "_pk", []string{"PRIMARY KEY (id)"}, nil},
			{table + "_tenant_id_id_uq", []string{"UNIQUE (tenant_id, id)"}, nil},
		}
	}
	problems = append(problems, esdConstraintDefs(q, efmTable, append(fk(efmTable), def{
		"erp_field_mappings_client_system_target_uq", []string{"UNIQUE (tenant_id, entity_id, system, target)"}, nil}))...)
	problems = append(problems, esdConstraintDefs(q, elkTable, append(fk(elkTable), def{
		"erp_lookups_client_company_kind_key_uq", []string{"UNIQUE (tenant_id, entity_id, system, external_key, kind, erp_key)"}, nil}))...)
	return problems
}

func TestRLS_ErpMappingTablesShape(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	for _, p := range empShapeProblems(h.super) {
		t.Error(p)
	}
}

// ---- RLS ------------------------------------------------------------------

func TestRLS_ErpMappingTablesForceRLSAndPolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEmpTables(t, h.super)

	for _, table := range empTables {
		var enabled, forced bool
		if err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass($1)`, "public."+table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("read pg_class for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s relrowsecurity/relforcerowsecurity = %v/%v, want true/true", table, enabled, forced)
		}
		rows, err := h.super.Query(ctx,
			`SELECT policyname, cmd, roles::text[], coalesce(qual, '') FROM pg_policies WHERE schemaname = 'public' AND tablename = $1`, table)
		if err != nil {
			t.Fatalf("query pg_policies for %s: %v", table, err)
		}
		type policy struct {
			cmd   string
			roles []string
			qual  string
		}
		got := map[string]policy{}
		for rows.Next() {
			var name string
			var p policy
			if e := rows.Scan(&name, &p.cmd, &p.roles, &p.qual); e != nil {
				rows.Close()
				t.Fatalf("scan pg_policies row of %s: %v", table, e)
			}
			got[name] = p
		}
		rows.Close()
		iso, ok := got["tenant_isolation"]
		if len(got) != 1 || !ok {
			t.Errorf("%s policies = %v, want exactly one, tenant_isolation", table, got)
			continue
		}
		if iso.cmd != "ALL" || strings.Join(iso.roles, ",") != "public" {
			t.Errorf("%s tenant_isolation cmd/roles = %s/%v, want ALL/[public] (no TO clause)", table, iso.cmd, iso.roles)
		}
		for _, guc := range []string{"app.current_tenant", "app.entity_scope"} {
			if !strings.Contains(iso.qual, guc) {
				t.Errorf("%s tenant_isolation qual = %q, want a comparison against %s", table, iso.qual, guc)
			}
		}
	}
}

func TestRLS_ErpMappingTablesCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEmpTables(t, h.super)
	a, b := empNewWorld(t, "xa"), empNewWorld(t, "xb")

	for _, tb := range empTbls {
		ra := tb.seed(t, a.tenant, a.entity)
		rb := tb.seed(t, b.tenant, b.entity)

		if got := erpAppIDs(t, a.tenant, nil, tb.name); !slices.Equal(got, []string{ra}) {
			t.Errorf("%s rows visible to A = %v, want only its own [%s]", tb.name, got, ra)
		}
		if got := erpAppIDs(t, b.tenant, nil, tb.name); !slices.Equal(got, []string{rb}) {
			t.Errorf("%s rows visible to B = %v, want only its own [%s]", tb.name, got, rb)
		}

		// B's own client, so only the policy can refuse the row.
		err := erpApp(a.tenant, nil, func(tx pgx.Tx) error { return tb.insert(tx, a.tenant, a.entity) })
		failIfEmpUndefined(t, tb.name+" control INSERT in the own tenant", err)
		if err != nil {
			t.Fatalf("%s control INSERT in the own tenant: %v", tb.name, err)
		}
		err = erpApp(a.tenant, nil, func(tx pgx.Tx) error { return tb.insert(tx, b.tenant, b.entity) })
		assertRLSViolation(t, err)
		if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, tb.name) {
			t.Errorf("%s insert refusal message = %q, want its RLS WITH CHECK violation", tb.name, msg)
		}
		if n := esdCount(t, tb.name, b.tenant); n != 1 {
			t.Errorf("%s rows of B after the refused INSERT = %d, want its 1", tb.name, n)
		}

		// Moving an own row to B trips WITH CHECK; B's row is invisible to A, so an UPDATE of it matches nothing.
		err = erpApp(a.tenant, nil, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE `+tb.name+` SET tenant_id = $1 WHERE id = $2`, b.tenant, ra)
			return e
		})
		assertRLSViolation(t, err)
		var n int64
		err = erpApp(a.tenant, nil, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE `+tb.name+` SET updated_at = now() - interval '1 day' WHERE id = $1`, rb)
			n = tag.RowsAffected()
			return e
		})
		if err != nil || n != 0 {
			t.Errorf("%s A updating B's row: rows = %d, err = %v, want 0 and nil", tb.name, n, err)
		}
		if got := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE id = $1 AND tenant_id = $2 AND updated_at > now() - interval '1 hour'`, rb, b.tenant); got != 1 {
			t.Errorf("%s B's row after A's UPDATE: %d unchanged matches in B, want 1", tb.name, got)
		}
		if got := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE id = $1 AND tenant_id = $2`, ra, a.tenant); got != 1 {
			t.Errorf("%s A's row after the refused move: %d matches in A, want 1", tb.name, got)
		}
	}
}

func TestRLS_ErpMappingTablesClientScopeHidesOtherClients(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "scope-x")
	x, y := w.entity, erpEntity(t, w.tenant, "emp-scope-y")

	for _, tb := range empTbls {
		// Two rows on X and one on Y: a scope that leaked all or none of Y's differs from the right answer.
		x1, x2 := tb.seed(t, w.tenant, x), tb.seed(t, w.tenant, x)
		y1 := tb.seed(t, w.tenant, y)
		all := sortedIDs(x1, x2, y1)

		for _, c := range []struct {
			scope string
			want  []string
		}{
			{"{" + x + "}", sortedIDs(x1, x2)},
			{"{" + y + "}", []string{y1}},
			{"{" + x + "," + y + "}", all},
			{"*", all},
			{"", all},
			{"{}", nil},
		} {
			scope := c.scope
			if got := erpAppIDs(t, w.tenant, &scope, tb.name); !slices.Equal(got, c.want) {
				t.Errorf("%s scope %q: visible rows = %v, want %v", tb.name, c.scope, got, c.want)
			}
		}

		// WITH CHECK keeps a limited member from writing, or moving a row, to another client.
		scope := "{" + x + "}"
		err := erpApp(w.tenant, &scope, func(tx pgx.Tx) error { return tb.insert(tx, w.tenant, x) })
		failIfEmpUndefined(t, tb.name+" control INSERT inside the scope", err)
		if err != nil {
			t.Fatalf("%s control INSERT inside the scope: %v", tb.name, err)
		}
		assertRLSViolation(t, erpApp(w.tenant, &scope, func(tx pgx.Tx) error { return tb.insert(tx, w.tenant, y) }))
		assertRLSViolation(t, erpApp(w.tenant, &scope, func(tx pgx.Tx) error {
			_, e := tx.Exec(context.Background(), `UPDATE `+tb.name+` SET entity_id = $1 WHERE id = $2`, y, x1)
			return e
		}))
		var n int64
		err = erpApp(w.tenant, &scope, func(tx pgx.Tx) error {
			tag, e := tx.Exec(context.Background(), `UPDATE `+tb.name+` SET updated_at = now() WHERE id = $1`, y1)
			n = tag.RowsAffected()
			return e
		})
		if err != nil || n != 0 {
			t.Errorf("%s UPDATE of an out-of-scope row: rows = %d, err = %v, want 0 and nil", tb.name, n, err)
		}
	}
}

func TestRLS_ErpMappingTablesNoTenantSeesNothing(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "noguc")

	for _, tb := range empTbls {
		tb.seed(t, w.tenant, w.entity)
		if n := esdCount(t, tb.name, w.tenant); n != 1 {
			t.Fatalf("%s superuser sees %d rows, want 1", tb.name, n)
		}
		// A pooled connection keeps app.current_tenant = '' after a tenant tx, so a policy
		// without nullif() raises 22P02 there instead of returning zero rows.
		for _, emptyGUC := range []bool{false, true} {
			tx, err := h.app.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if emptyGUC {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
					_ = tx.Rollback(ctx)
					t.Fatalf("set_config: %v", err)
				}
			}
			n, err := scanCount(ctx, tx, `SELECT count(*) FROM `+tb.name)
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("%s SELECT with no tenant context (empty GUC %v): %v", tb.name, emptyGUC, err)
			}
			if n != 0 {
				t.Errorf("%s rows visible with no tenant set (empty GUC %v) = %d, want 0", tb.name, emptyGUC, n)
			}
			err = tb.insert(tx, w.tenant, w.entity)
			_ = tx.Rollback(ctx)
			assertRLSViolation(t, err)
		}
	}
}

// FORCE subjects the migrator (the table owner) to the policy.
func TestRLS_ErpMappingTablesOwnerIsBoundByForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEmpTables(t, h.super)
	a, b := empNewWorld(t, "owner-a"), empNewWorld(t, "owner-b")

	for _, tb := range empTbls {
		tb.seed(t, a.tenant, a.entity)
		count := func(guc string) int {
			tx, err := h.mig.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if guc != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, guc); err != nil {
					t.Fatalf("set_config: %v", err)
				}
			}
			n, err := scanCount(ctx, tx, `SELECT count(*) FROM `+tb.name)
			if err != nil {
				t.Fatalf("%s owner SELECT (GUC %q): %v", tb.name, guc, err)
			}
			return n
		}
		if n := count(a.tenant); n != 1 {
			t.Fatalf("%s owner bound to the row's own tenant sees %d rows, want 1", tb.name, n)
		}
		if n := count(b.tenant); n != 0 {
			t.Errorf("%s owner bound to tenant B sees %d of A's rows, want 0 (FORCE ROW LEVEL SECURITY)", tb.name, n)
		}
		if n := count(""); n != 0 {
			t.Errorf("%s owner with no tenant context sees %d rows, want 0", tb.name, n)
		}
	}
}

// ---- constraints ----------------------------------------------------------

// RI checks bypass RLS, so only the composite FK refuses another tenant's client, for any role.
func TestRLS_ErpMappingTablesRefuseAnotherTenantsClient(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	a, b := empNewWorld(t, "fk-a"), empNewWorld(t, "fk-b")

	for _, tb := range empTbls {
		if err := tb.insert(h.super, a.tenant, a.entity); err != nil {
			failIfEmpUndefined(t, tb.name+" control INSERT", err)
			t.Fatalf("%s control INSERT with the own client: %v", tb.name, err)
		}
		for _, role := range []struct {
			name string
			run  func() error
		}{
			{"superuser", func() error { return tb.insert(h.super, a.tenant, b.entity) }},
			{"invoice_app bound to A", func() error {
				return erpApp(a.tenant, nil, func(tx pgx.Tx) error { return tb.insert(tx, a.tenant, b.entity) })
			}},
		} {
			empWantErr(t, role.run(), "23503", tb.name+"_tenant_entity_fk", role.name+" inserting a "+tb.name+" row for another tenant's client")
		}
		if n := esdCount(t, tb.name, a.tenant); n != 1 {
			t.Errorf("%s rows of A after the refused INSERTs = %d, want only the control", tb.name, n)
		}
	}
}

func TestRLS_ErpMappingTablesCascadeWithTheClient(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "casc-x")
	x, y := w.entity, erpEntity(t, w.tenant, "emp-casc-y")

	byEntity := func(table, entity string) int {
		return mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1 AND entity_id = $2`, w.tenant, entity)
	}
	for _, tb := range empTbls {
		tb.seed(t, w.tenant, x)
		tb.seed(t, w.tenant, x)
		tb.seed(t, w.tenant, y)
		if byEntity(tb.name, x) != 2 || byEntity(tb.name, y) != 1 {
			t.Fatalf("%s seeded rows on X/Y = %d/%d, want 2/1", tb.name, byEntity(tb.name, x), byEntity(tb.name, y))
		}
	}
	if _, err := h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE tenant_id = $1 AND id = $2`, w.tenant, x); err != nil {
		t.Fatalf("delete the client: %v", err)
	}
	for _, tb := range empTbls {
		if n := byEntity(tb.name, x); n != 0 {
			t.Errorf("%s rows of the deleted client = %d, want 0", tb.name, n)
		}
		if n := byEntity(tb.name, y); n != 1 {
			t.Errorf("%s rows of the other client = %d, want its 1", tb.name, n)
		}
	}
}

func TestRLS_ErpFieldMappingsOneRowPerClientSystemTarget(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "efm-uq")
	x, y := w.entity, erpEntity(t, w.tenant, "emp-efm-uq-y")
	base := efmRow{"fake", "line_description", "description"}
	base.seed(t, w.tenant, x)

	// A different path does not make a second row for the same target.
	dup := base
	dup.path = "other.path"
	empWantErr(t, dup.insert(h.super, w.tenant, x), "23505", "erp_field_mappings_client_system_target_uq", "the same client, system and target again")
	for _, c := range []struct {
		name   string
		entity string
		row    efmRow
	}{
		{"another client, same system and target", y, base},
		{"another system, same client and target", x, efmRow{"other", "line_description", "description"}},
		{"another target, same client and system", x, efmRow{"fake", "line_quantity", "qty"}},
	} {
		if err := c.row.insert(h.super, w.tenant, c.entity); err != nil {
			t.Errorf("%s: want accepted, got %v", c.name, err)
		}
	}
	if n := esdCount(t, efmTable, w.tenant); n != 4 {
		t.Errorf("rows after the cases = %d, want the first and the 3 accepted", n)
	}
}

func TestRLS_ErpLookupsOneRowPerClientCompanyKindKey(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "elk-uq")
	x, y := w.entity, erpEntity(t, w.tenant, "emp-elk-uq-y")
	row := func(system, company, kind, key string) elkRow {
		r := newElk(kind)
		r.system, r.company, r.key = system, company, key
		return r
	}
	row("fake", "fake-co-1", "customer", "c1").seed(t, w.tenant, x)

	// Different values do not make a second row for the same key.
	dup := row("fake", "fake-co-1", "customer", "c1")
	dup.tin = erpStr("RN-1")
	empWantErr(t, dup.insert(h.super, w.tenant, x), "23505", "erp_lookups_client_company_kind_key_uq", "the same client, system, company, kind and key again")
	for _, c := range []struct {
		name   string
		entity string
		row    elkRow
	}{
		{"another company", x, row("fake", "fake-co-2", "customer", "c1")},
		{"another kind", x, row("fake", "fake-co-1", "item", "c1")},
		{"another key", x, row("fake", "fake-co-1", "customer", "c2")},
		{"another client", y, row("fake", "fake-co-1", "customer", "c1")},
		{"another system", x, row("other", "fake-co-1", "customer", "c1")},
	} {
		if err := c.row.insert(h.super, w.tenant, c.entity); err != nil {
			t.Errorf("%s: want accepted, got %v", c.name, err)
		}
	}
	if n := esdCount(t, elkTable, w.tenant); n != 6 {
		t.Errorf("rows after the cases = %d, want the first and the 5 accepted", n)
	}
}

func TestRLS_ErpFieldMappingsChecks(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "efm-checks")
	with := func(f func(r *efmRow)) efmRow {
		r := newEfm()
		f(&r)
		return r
	}

	var accepted int
	for _, c := range []struct {
		name string
		row  efmRow
		want string // constraint that refuses; "" accepts
	}{
		{"empty system", with(func(r *efmRow) { r.system = "" }), "erp_field_mappings_system_check"},
		{"target with an upper-case letter", with(func(r *efmRow) { r.target = "Line_x" }), "erp_field_mappings_target_check"},
		{"target starting with a digit", with(func(r *efmRow) { r.target = "9x" }), "erp_field_mappings_target_check"},
		{"empty path", with(func(r *efmRow) { r.path = "" }), "erp_field_mappings_path_check"},
		{"path of 257 characters", with(func(r *efmRow) { r.path = strings.Repeat("p", 257) }), "erp_field_mappings_path_check"},

		{"valid row", newEfm(), ""},
		{"target with digits and underscores", with(func(r *efmRow) { r.target = "line_2_x" }), ""},
		{"path of 1 character", with(func(r *efmRow) { r.path = "p" }), ""},
		{"path of 256 characters", with(func(r *efmRow) { r.path = strings.Repeat("p", 256) }), ""},
	} {
		err := c.row.insert(h.super, w.tenant, w.entity)
		if c.want == "" {
			failIfEmpUndefined(t, c.name, err)
			if err != nil {
				t.Errorf("%s: want accepted, got %v", c.name, err)
			} else {
				accepted++
			}
			continue
		}
		empWantErr(t, err, "23514", c.want, c.name)
	}
	if n := esdCount(t, efmTable, w.tenant); n != accepted || n == 0 {
		t.Errorf("rows after the cases = %d, want the %d accepted", n, accepted)
	}
}

func TestRLS_ErpLookupsChecks(t *testing.T) {
	h := requireHarness(t)
	requireEmpTables(t, h.super)
	w := empNewWorld(t, "elk-checks")
	s := erpStr
	tax := func(f func(r *elkRow)) elkRow { return elkWith("tax_code", f) }
	item := func(f func(r *elkRow)) elkRow { return elkWith("item", f) }
	cust := func(f func(r *elkRow)) elkRow { return elkWith("customer", f) }
	long := strings.Repeat("k", 257)

	const (
		system   = "erp_lookups_system_check"
		external = "erp_lookups_external_key_check"
		kind     = "erp_lookups_kind_check"
		key      = "erp_lookups_key_check"
		values   = "erp_lookups_values_check"
		tin      = "erp_lookups_tin_check"
	)
	type tc struct {
		name string
		row  elkRow
		want string // constraint that refuses; "" accepts
	}
	cases := []tc{
		{"empty system", tax(func(r *elkRow) { r.system = "" }), system},

		{"empty external_key", tax(func(r *elkRow) { r.company = "" }), external},
		{"blank external_key", tax(func(r *elkRow) { r.company = " " }), external},
		{"external_key with a leading space", tax(func(r *elkRow) { r.company = " k" }), external},
		{"external_key with a trailing space", tax(func(r *elkRow) { r.company = "k " }), external},
		{"external_key of 257 characters", tax(func(r *elkRow) { r.company = long }), external},

		// 'vendor' also fits no branch of values_check; kind_check sorts first, so it names the failure.
		{"unknown kind", newElk("vendor"), kind},

		{"empty erp_key", tax(func(r *elkRow) { r.key = "" }), key},
		{"blank erp_key", tax(func(r *elkRow) { r.key = " " }), key},
		{"erp_key with a leading space", tax(func(r *elkRow) { r.key = " c1" }), key},
		{"erp_key with a trailing space", tax(func(r *elkRow) { r.key = "c1 " }), key},
		{"erp_key of 257 characters", tax(func(r *elkRow) { r.key = long }), key},

		{"tax_code with no tax_category", tax(func(r *elkRow) { r.taxCategory = nil }), values},
		{"tax_code with an hsn_code", tax(func(r *elkRow) { r.hsn = s("8471.30") }), values},
		{"tax_code with a service_code", tax(func(r *elkRow) { r.service = s("S1") }), values},
		{"tax_code with a tin", tax(func(r *elkRow) { r.tin = s("12345678-0001") }), values},
		{"tax_code with a state", tax(func(r *elkRow) { r.state = s("LA") }), values},
		{"tax_code with an lga", tax(func(r *elkRow) { r.lga = s("Ikeja") }), values},

		{"item with an hsn_code and a service_code", item(func(r *elkRow) { r.service = s("S1") }), values},
		{"item with neither code", item(func(r *elkRow) { r.hsn = nil }), values},
		{"item with a tax_category", item(func(r *elkRow) { r.taxCategory = s("T1") }), values},
		{"item with an exemption_code", item(func(r *elkRow) { r.exemption = s("E1") }), values},
		{"item with a tin", item(func(r *elkRow) { r.tin = s("12345678-0001") }), values},
		{"item with a state", item(func(r *elkRow) { r.state = s("LA") }), values},
		{"item with an lga", item(func(r *elkRow) { r.lga = s("Ikeja") }), values},

		{"customer with none of tin, state and lga", cust(func(r *elkRow) { r.tin = nil }), values},
		{"customer with a tax_category", cust(func(r *elkRow) { r.taxCategory = s("T1") }), values},
		{"customer with an exemption_code", cust(func(r *elkRow) { r.exemption = s("E1") }), values},
		{"customer with an hsn_code", cust(func(r *elkRow) { r.hsn = s("8471.30") }), values},
		{"customer with a service_code", cust(func(r *elkRow) { r.service = s("S1") }), values},
	}
	// Each shape of a bad tin, on a customer that is otherwise valid.
	for _, bad := range []string{"1234567897", "123456780001", "1234567-0001", "12345678-00001", "x12345678-0001", "12345678-0001x", "RN-", "rn-123", "RN-12a", ""} {
		cases = append(cases, tc{"customer with the tin '" + bad + "'", cust(func(r *elkRow) { r.tin = s(bad) }), tin})
	}
	cases = append(cases,
		tc{"tax_code", tax(func(*elkRow) {}), ""},
		tc{"tax_code with an exemption_code", tax(func(r *elkRow) { r.exemption = s("E1") }), ""},
		tc{"item with an hsn_code", item(func(*elkRow) {}), ""},
		tc{"item with a service_code", item(func(r *elkRow) { r.hsn, r.service = nil, s("S1") }), ""},
		tc{"customer with a tin", cust(func(*elkRow) {}), ""},
		tc{"customer with an RN- tin", cust(func(r *elkRow) { r.tin = s("RN-123") }), ""},
		tc{"customer with a state only", cust(func(r *elkRow) { r.tin, r.state = nil, s("LA") }), ""},
		tc{"customer with an lga only", cust(func(r *elkRow) { r.tin, r.lga = nil, s("Ikeja") }), ""},
		tc{"customer with a tin, a state and an lga", cust(func(r *elkRow) { r.state, r.lga = s("LA"), s("Ikeja") }), ""},
		tc{"external_key of 256 characters", tax(func(r *elkRow) { r.company = long[:256] }), ""},
		tc{"erp_key of 256 characters", tax(func(r *elkRow) { r.key = long[:256] }), ""},
		tc{"external_key and erp_key with inner spaces", tax(func(r *elkRow) { r.company, r.key = "fake co 1", "c 1" }), ""},
	)

	var accepted int
	for _, c := range cases {
		err := c.row.insert(h.super, w.tenant, w.entity)
		if c.want == "" {
			failIfEmpUndefined(t, c.name, err)
			if err != nil {
				t.Errorf("%s: want accepted, got %v", c.name, err)
			} else {
				accepted++
			}
			continue
		}
		empWantErr(t, err, "23514", c.want, c.name)
	}
	if n := esdCount(t, elkTable, w.tenant); n != accepted || n == 0 {
		t.Errorf("rows after the cases = %d, want the %d accepted", n, accepted)
	}
}

func TestRLS_ErpSourceDocumentsMappingFingerprintCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "fp")
	id := esdSeed(t, w.tenant, w.entity, w.conn, esdRow{})

	const check = "erp_source_documents_mapping_fingerprint_check"
	set := func(v *string) error {
		_, err := h.super.Exec(ctx, `UPDATE erp_source_documents SET mapping_fingerprint = $1 WHERE id = $2`, v, id)
		return err
	}
	read := func() *string {
		var got *string
		if err := h.super.QueryRow(ctx, `SELECT mapping_fingerprint FROM erp_source_documents WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read mapping_fingerprint: %v", err)
		}
		return got
	}

	if err := set(nil); err != nil {
		failIfEmpUndefined(t, "set mapping_fingerprint", err)
		t.Fatalf("set mapping_fingerprint NULL: %v", err)
	}
	valid := strings.Repeat("0123456789abcdef", 4)
	if err := set(&valid); err != nil {
		t.Fatalf("control: 64 lowercase hex: want accepted, got %v", err)
	}
	for name, v := range map[string]string{
		"short text":       "abc",
		"63 hex":           valid[:63],
		"65 hex":           valid + "a",
		"64 upper-case":    strings.ToUpper(valid),
		"64 non-hex":       strings.Repeat("g", 64),
		"64 hex and space": valid[:63] + " ",
		"empty":            "",
	} {
		empWantErr(t, set(&v), "23514", check, "mapping_fingerprint "+name)
	}
	if got := read(); got == nil || *got != valid {
		t.Errorf("mapping_fingerprint after the refused updates = %v, want the valid value kept", got)
	}
	if err := set(nil); err != nil {
		t.Errorf("mapping_fingerprint back to NULL: want accepted, got %v", err)
	}
	if got := read(); got != nil {
		t.Errorf("mapping_fingerprint after NULL = %q, want NULL", *got)
	}
}

// ---- grants ---------------------------------------------------------------

// A missing privilege and a policy refusal are both 42501 on the wire, so the catalog answers here.
func TestRLS_ErpMappingTablesGrants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEmpTables(t, h.super)
	a, b := empNewWorld(t, "grants-a"), empNewWorld(t, "grants-b")

	for _, tb := range empTbls {
		for priv, want := range map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": false} {
			var got bool
			if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', $1, $2)`, "public."+tb.name, priv).Scan(&got); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", tb.name, priv, err)
			}
			if got != want {
				t.Errorf("has_table_privilege(invoice_app, %s, %s) = %v, want %v", tb.name, priv, got, want)
			}
		}
		// The probes miss REFERENCES, TRIGGER and any other grantee: read the ACL.
		acl, err := empNames(h.super,
			`SELECT coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee WHERE c.oid = to_regclass($1) AND a.grantee <> c.relowner ORDER BY 1`,
			"public."+tb.name)
		if err != nil {
			t.Fatalf("aclexplode %s: %v", tb.name, err)
		}
		if got, want := strings.Join(acl, ","), "invoice_app:DELETE,invoice_app:INSERT,invoice_app:SELECT,invoice_app:UPDATE"; got != want {
			t.Errorf("%s non-owner ACL = %q, want %q", tb.name, got, want)
		}

		own, other := tb.seed(t, a.tenant, a.entity), tb.seed(t, b.tenant, b.entity)
		var n, nOther int64
		err = erpApp(a.tenant, nil, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `DELETE FROM `+tb.name+` WHERE id = $1`, own)
			n = tag.RowsAffected()
			if e != nil {
				return e
			}
			tag, e = tx.Exec(ctx, `DELETE FROM `+tb.name+` WHERE id = $1`, other)
			nOther = tag.RowsAffected()
			return e
		})
		failIfEmpUndefined(t, tb.name+" invoice_app DELETE", err)
		if err != nil || n != 1 || nOther != 0 {
			t.Errorf("%s invoice_app deleting its own row and B's: rows = %d and %d, err = %v, want 1 and 0", tb.name, n, nOther, err)
		}
		if got := esdCount(t, tb.name, b.tenant); got != 1 {
			t.Errorf("%s rows of B after A's DELETE = %d, want its 1", tb.name, got)
		}
	}
}

// ---- migration ------------------------------------------------------------

// empMigration returns the one migration file; the test fails until it exists.
func empMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, empMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", empMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1 — the erp_mappings migration is not written yet", empMigrationGlob, matches)
	}
	return matches[0]
}

// Runs on the migrator inside a transaction that is always rolled back, so no other test sees the tables vanish.
func TestRLS_ErpMappingTablesMigrationDownUp(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	migration := empMigration(t)
	requireEmpTables(t, h.super)

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	before := erpPublicTables(t, tx, "before the Down")
	if !esdHasColumn(t, tx, esdTable, "mapping_fingerprint") {
		t.Fatalf("erp_source_documents.mapping_fingerprint is not there before the Down")
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", migration, err)
	}
	after := erpPublicTables(t, tx, "after the Down")
	var dropped []string
	for _, table := range before {
		if !slices.Contains(after, table) {
			dropped = append(dropped, table)
		}
	}
	if !slices.Equal(dropped, empTables) || len(after) != len(before)-len(empTables) {
		t.Errorf("tables the Down dropped = %v (public tables %d -> %d), want exactly %v", dropped, len(before), len(after), empTables)
	}
	if esdHasColumn(t, tx, esdTable, "mapping_fingerprint") {
		t.Error("erp_source_documents.mapping_fingerprint is still there after the Down")
	}
	if !slices.Contains(after, esdTable) {
		t.Errorf("%s is gone after the Down, want it kept", esdTable)
	}
	if p := esdShapeProblems(tx, false); len(p) != 0 {
		t.Errorf("erp_source_documents after the Down no longer matches its own migration: %v", p)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	requireEmpTables(t, tx)
	for _, p := range empShapeProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, p := range esdShapeProblems(tx, true) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, table := range empTables {
		var forced bool
		var policies int
		var grants bool
		if err := tx.QueryRow(ctx,
			`SELECT c.relrowsecurity AND c.relforcerowsecurity, (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1), has_table_privilege('invoice_app', c.oid, 'SELECT, INSERT, UPDATE, DELETE') FROM pg_class c WHERE c.oid = to_regclass($2)`,
			table, "public."+table).Scan(&forced, &policies, &grants); err != nil || !forced || policies != 1 || !grants {
			t.Errorf("after Down then Up: %s FORCE RLS = %v, policies = %d, app grants = %v (err %v), want true, 1, true", table, forced, policies, grants, err)
		}
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := erpPublicTables(t, h.super, "after the rollback"); !slices.Equal(got, before) {
		t.Errorf("public tables after the rollback = %v, want the %v seen before the Down", got, before)
	}
	if !esdHasColumn(t, h.super, esdTable, "mapping_fingerprint") {
		t.Error("erp_source_documents.mapping_fingerprint is gone after the rollback")
	}
}
