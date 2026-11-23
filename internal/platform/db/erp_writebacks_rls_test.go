// Storage suite for erp_writebacks: the append-only attempt log of the NRS-result write-back to the ERP.
package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const ewbMigrationGlob = "*_erp_writebacks.sql"

const ewbTable = "erp_writebacks"

const ewbInsertSQL = `INSERT INTO erp_writebacks
	(tenant_id, entity_id, connection_id, source_document_id, invoice_id, result_version, outcome, attempt, state, reason, written_revision, new_revision, river_job_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

// The attempt reasons by state, as the design's erp_writebacks_reason_check.
var (
	ewbFailedReasons  = []string{"not_found", "unsupported", "revision_conflict", "rate_limited", "vendor"}
	ewbSkippedReasons = []string{"disconnected", "nothing_writable", "superseded", "deleted_in_erp"}
)

// ewbRow is one attempt row; the zero value is a valid succeeded row for result 1, attempt 1, outcome accepted.
type ewbRow struct {
	version, attempt *int
	outcome, state   string
	reason           *string
	written, newRev  *string
}

func ewbInt(n int) *int { return &n }

func ewbArgs(w ewbWorld, tenant string, r ewbRow) []any {
	if r.outcome == "" {
		r.outcome = "accepted"
	}
	if r.state == "" {
		r.state = "succeeded"
	}
	if r.version == nil {
		r.version = ewbInt(1)
	}
	if r.attempt == nil {
		r.attempt = ewbInt(1)
	}
	return []any{tenant, w.entity, w.conn, w.doc, w.invoice, *r.version, r.outcome, *r.attempt, r.state, r.reason, r.written, r.newRev, int64(1)}
}

func ewbInsert(q esdExecer, tenant string, w ewbWorld, r ewbRow) error {
	_, err := q.Exec(context.Background(), ewbInsertSQL, ewbArgs(w, tenant, r)...)
	return err
}

// failIfEwbUndefined turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfEwbUndefined(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the erp_writebacks migration is not applied yet: %v", what, c, err)
	}
}

// ewbWantErr is erpWantErr with a message that names this migration while it is absent.
func ewbWantErr(t *testing.T, err error, code, constraint, what string) {
	t.Helper()
	failIfEwbUndefined(t, what, err)
	erpWantErr(t, err, code, constraint, what)
}

func requireEwbTable(t *testing.T, q erpQuerier) {
	t.Helper()
	var reg *string
	if err := q.QueryRow(context.Background(), `SELECT to_regclass('public.erp_writebacks')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass(public.erp_writebacks): %v", err)
	}
	if reg == nil {
		t.Fatal("public.erp_writebacks does not exist — the erp_writebacks migration is not applied yet")
	}
}

// ewbWorld is one client of a tenant with the connection, invoice and source document a write-back row points at.
type ewbWorld struct{ tenant, entity, conn, doc, invoice string }

// ewbTenant makes a throwaway tenant. Its cleanup runs before the tenant delete: both tables hold RESTRICT-side FKs to invoices.
func ewbTenant(t *testing.T) string {
	t.Helper()
	tenant := erpTenant(t)
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM erp_writebacks WHERE tenant_id = $1`, tenant)
		_, _ = h.super.Exec(context.Background(), `DELETE FROM erp_source_documents WHERE tenant_id = $1`, tenant)
	})
	return tenant
}

func ewbClient(t *testing.T, tenant, label string) ewbWorld {
	t.Helper()
	entity := erpEntity(t, tenant, "ewb-"+label)
	conn := esdConn(t, tenant, entity)
	inv := esdInvoice(t, tenant, entity)
	doc := esdSeed(t, tenant, entity, conn, esdRow{state: "drafted", invoice: &inv})
	return ewbWorld{tenant, entity, conn, doc, inv}
}

func ewbNewWorld(t *testing.T, label string) ewbWorld {
	t.Helper()
	return ewbClient(t, ewbTenant(t), label)
}

func ewbSeed(t *testing.T, w ewbWorld, r ewbRow) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), ewbInsertSQL+` RETURNING id::text`, ewbArgs(w, w.tenant, r)...).Scan(&id)
	failIfEwbUndefined(t, "seed erp_writebacks", err)
	if err != nil {
		t.Fatalf("seed erp_writebacks: %v", err)
	}
	return id
}

func ewbCount(t *testing.T, table, tenant string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenant)
}

// ---- shape ----------------------------------------------------------------

var ewbShape = map[string]erpCol{
	"tenant_id":          {"uuid", true, ""},
	"id":                 {"uuid", true, "gen_random_uuid()"},
	"entity_id":          {"uuid", true, ""},
	"connection_id":      {"uuid", true, ""},
	"source_document_id": {"uuid", true, ""},
	"invoice_id":         {"uuid", true, ""},
	"result_version":     {"integer", true, ""},
	"outcome":            {"text", true, ""},
	"attempt":            {"integer", true, ""},
	"state":              {"text", true, ""},
	"reason":             {"text", false, ""},
	"written_revision":   {"text", false, ""},
	"new_revision":       {"text", false, ""},
	"river_job_id":       {"bigint", true, ""},
	"created_at":         {"timestamp with time zone", true, "now()"},
}

// The NOT NULLs are derived from ewbShape (PG18 names each one).
var ewbNamedConstraints = []string{
	"erp_writebacks_pk", "erp_writebacks_result_version_check", "erp_writebacks_attempt_check",
	"erp_writebacks_outcome_check", "erp_writebacks_state_check", "erp_writebacks_reason_check",
	"erp_writebacks_tenant_entity_fk", "erp_writebacks_tenant_connection_fk",
	"erp_writebacks_tenant_document_fk", "erp_writebacks_tenant_invoice_fk", "erp_writebacks_tenant_id_fkey",
}

var ewbIndexes = []string{"erp_writebacks_document_idx", "erp_writebacks_pk"}

// ewbShapeProblems compares the live table to the Design: columns, constraint names and definitions, indexes.
func ewbShapeProblems(q erpQuerier) []string {
	ctx := context.Background()
	var problems []string

	rows, err := q.Query(ctx, `SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`, ewbTable)
	if err != nil {
		return append(problems, "read information_schema.columns: "+err.Error())
	}
	got := map[string]erpCol{}
	for rows.Next() {
		var name, typ, nullable string
		var def *string
		if e := rows.Scan(&name, &typ, &nullable, &def); e != nil {
			rows.Close()
			return append(problems, "scan column: "+e.Error())
		}
		c := erpCol{typ: typ, notNull: nullable == "NO"}
		if def != nil {
			c.def = *def
		}
		got[name] = c
	}
	rows.Close()
	if len(got) == 0 {
		return append(problems, "information_schema.columns returned no column for erp_writebacks")
	}
	if !reflect.DeepEqual(got, ewbShape) {
		problems = append(problems, "columns of erp_writebacks differ from the Design: got "+fmtCols(got)+" want "+fmtCols(ewbShape))
	}

	cons, err := q.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = 'public.erp_writebacks'::regclass ORDER BY conname`)
	if err != nil {
		return append(problems, "read pg_constraint: "+err.Error())
	}
	var gotCons []string
	for cons.Next() {
		var n string
		if e := cons.Scan(&n); e != nil {
			cons.Close()
			return append(problems, "scan conname: "+e.Error())
		}
		gotCons = append(gotCons, n)
	}
	cons.Close()
	wantCons := slices.Clone(ewbNamedConstraints)
	for col, c := range ewbShape {
		if c.notNull {
			wantCons = append(wantCons, ewbTable+"_"+col+"_not_null")
		}
	}
	sort.Strings(wantCons)
	if !slices.Equal(gotCons, wantCons) {
		problems = append(problems, "constraints of erp_writebacks = "+strings.Join(gotCons, ",")+"\n  want "+strings.Join(wantCons, ","))
	}

	problems = append(problems, esdConstraintDefs(q, ewbTable, []struct {
		name       string
		has, lacks []string
	}{
		{"erp_writebacks_pk", []string{"PRIMARY KEY (id)"}, nil},
		{"erp_writebacks_result_version_check", []string{"result_version > 0"}, nil},
		{"erp_writebacks_attempt_check", []string{"attempt > 0"}, nil},
		{"erp_writebacks_outcome_check", []string{"'accepted'", "'rejected'", "'failed'"}, nil},
		{"erp_writebacks_state_check", []string{"'succeeded'", "'failed'", "'skipped'"}, nil},
		{"erp_writebacks_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}, nil},
		{"erp_writebacks_tenant_entity_fk",
			[]string{"FOREIGN KEY (tenant_id, entity_id)", "REFERENCES business_entities(tenant_id, id)", "ON DELETE CASCADE"}, nil},
		// NO ACTION, as erp_source_documents_tenant_connection_fk: the tenant cascade still passes.
		{"erp_writebacks_tenant_connection_fk",
			[]string{"FOREIGN KEY (tenant_id, connection_id)", "REFERENCES erp_connections(tenant_id, id)"}, []string{"ON DELETE", "ON UPDATE"}},
		{"erp_writebacks_tenant_document_fk",
			[]string{"FOREIGN KEY (tenant_id, source_document_id)", "REFERENCES erp_source_documents(tenant_id, id)"}, []string{"ON DELETE", "ON UPDATE"}},
		{"erp_writebacks_tenant_invoice_fk",
			[]string{"FOREIGN KEY (tenant_id, invoice_id)", "REFERENCES invoices(tenant_id, id)", "ON DELETE RESTRICT"}, nil},
	})...)

	idx, err := q.Query(ctx, `SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'erp_writebacks' ORDER BY indexname`)
	if err != nil {
		return append(problems, "read pg_indexes: "+err.Error())
	}
	var gotIdx []string
	for idx.Next() {
		var n string
		if e := idx.Scan(&n); e != nil {
			idx.Close()
			return append(problems, "scan indexname: "+e.Error())
		}
		gotIdx = append(gotIdx, n)
	}
	idx.Close()
	if !slices.Equal(gotIdx, ewbIndexes) {
		problems = append(problems, "indexes of erp_writebacks = "+strings.Join(gotIdx, ",")+", want "+strings.Join(ewbIndexes, ","))
	}
	if def, err := esdIndexDef(q, "erp_writebacks_document_idx"); err != nil {
		problems = append(problems, "read erp_writebacks_document_idx: "+err.Error())
	} else if !strings.Contains(def, "(tenant_id, source_document_id, result_version)") {
		problems = append(problems, "erp_writebacks_document_idx = "+def+", want it on (tenant_id, source_document_id, result_version)")
	}
	return problems
}

func TestRLS_ErpWritebacksShape(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	for _, p := range ewbShapeProblems(h.super) {
		t.Error(p)
	}
}

// ---- RLS ------------------------------------------------------------------

func TestRLS_ErpWritebacksForceRLSAndPolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEwbTable(t, h.super)

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.erp_writebacks')`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, cmd, roles::text[], coalesce(qual, '') FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_writebacks'`)
	if err != nil {
		t.Fatalf("query pg_policies: %v", err)
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
			t.Fatalf("scan pg_policies row: %v", e)
		}
		got[name] = p
	}
	rows.Close()
	iso, ok := got["tenant_isolation"]
	if len(got) != 1 || !ok {
		t.Fatalf("policies = %v, want exactly one, tenant_isolation", got)
	}
	if iso.cmd != "ALL" || strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation cmd/roles = %s/%v, want ALL/[public] (no TO clause)", iso.cmd, iso.roles)
	}
	for _, guc := range []string{"app.current_tenant", "app.entity_scope"} {
		if !strings.Contains(iso.qual, guc) {
			t.Errorf("tenant_isolation qual = %q, want a comparison against %s", iso.qual, guc)
		}
	}
}

func TestRLS_ErpWritebacksCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	a, b := ewbNewWorld(t, "xa"), ewbNewWorld(t, "xb")
	ra := ewbSeed(t, a, ewbRow{})
	rb := ewbSeed(t, b, ewbRow{})

	if got := erpAppIDs(t, a.tenant, nil, ewbTable); !slices.Equal(got, []string{ra}) {
		t.Errorf("rows visible to A = %v, want only its own [%s]", got, ra)
	}
	if got := erpAppIDs(t, b.tenant, nil, ewbTable); !slices.Equal(got, []string{rb}) {
		t.Errorf("rows visible to B = %v, want only its own [%s]", got, rb)
	}

	// B's own client, connection, document and invoice, so only the policy can refuse the row.
	err := erpApp(a.tenant, nil, func(tx pgx.Tx) error { return ewbInsert(tx, a.tenant, a, ewbRow{attempt: ewbInt(2)}) })
	failIfEwbUndefined(t, "control INSERT in the own tenant", err)
	if err != nil {
		t.Fatalf("control INSERT in the own tenant: %v", err)
	}
	err = erpApp(a.tenant, nil, func(tx pgx.Tx) error { return ewbInsert(tx, b.tenant, b, ewbRow{attempt: ewbInt(2)}) })
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, ewbTable) {
		t.Errorf("insert refusal message = %q, want the erp_writebacks RLS WITH CHECK violation", msg)
	}
	if n := ewbCount(t, ewbTable, b.tenant); n != 1 {
		t.Errorf("rows of B after the refused INSERT = %d, want its 1", n)
	}
}

func TestRLS_ErpWritebacksClientScopeHidesOtherClients(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	tenant := ewbTenant(t)
	x, y := ewbClient(t, tenant, "scope-x"), ewbClient(t, tenant, "scope-y")
	rx := ewbSeed(t, x, ewbRow{})
	ry := ewbSeed(t, y, ewbRow{})
	both := sortedIDs(rx, ry)

	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"{" + x.entity + "}", []string{rx}},
		{"{" + y.entity + "}", []string{ry}},
		{"{" + x.entity + "," + y.entity + "}", both},
		{"*", both},
		{"", both},
		{"{}", nil},
	} {
		scope := c.scope
		if got := erpAppIDs(t, tenant, &scope, ewbTable); !slices.Equal(got, c.want) {
			t.Errorf("scope %q: visible write-backs = %v, want %v", c.scope, got, c.want)
		}
	}

	// The policy's WITH CHECK keeps a limited member from writing a row of another client.
	scope := "{" + x.entity + "}"
	err := erpApp(tenant, &scope, func(tx pgx.Tx) error { return ewbInsert(tx, tenant, x, ewbRow{attempt: ewbInt(2)}) })
	failIfEwbUndefined(t, "control INSERT inside the scope", err)
	if err != nil {
		t.Fatalf("control INSERT inside the scope: %v", err)
	}
	assertRLSViolation(t, erpApp(tenant, &scope, func(tx pgx.Tx) error { return ewbInsert(tx, tenant, y, ewbRow{attempt: ewbInt(2)}) }))
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_writebacks WHERE entity_id = $1`, y.entity); n != 1 {
		t.Errorf("rows of the out-of-scope client after the refused INSERT = %d, want its 1", n)
	}
}

func TestRLS_ErpWritebacksNoTenantSeesNothing(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEwbTable(t, h.super)
	w := ewbNewWorld(t, "noguc")
	ewbSeed(t, w, ewbRow{})
	if n := ewbCount(t, ewbTable, w.tenant); n != 1 {
		t.Fatalf("superuser sees %d rows, want 1", n)
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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM erp_writebacks`)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("SELECT with no tenant context (empty GUC %v): %v", emptyGUC, err)
		}
		if n != 0 {
			t.Errorf("rows visible with no tenant set (empty GUC %v) = %d, want 0", emptyGUC, n)
		}
		err = ewbInsert(tx, w.tenant, w, ewbRow{attempt: ewbInt(2)})
		_ = tx.Rollback(ctx)
		assertRLSViolation(t, err)
	}
}

// FORCE subjects the migrator (the table owner) to the policy.
func TestRLS_ErpWritebacksOwnerIsBoundByForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEwbTable(t, h.super)
	a, b := ewbNewWorld(t, "owner-a"), ewbNewWorld(t, "owner-b")
	ewbSeed(t, a, ewbRow{})

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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM erp_writebacks`)
		if err != nil {
			t.Fatalf("owner SELECT (GUC %q): %v", guc, err)
		}
		return n
	}
	if n := count(a.tenant); n != 1 {
		t.Fatalf("owner bound to the row's own tenant sees %d rows, want 1", n)
	}
	if n := count(b.tenant); n != 0 {
		t.Errorf("owner bound to tenant B sees %d of A's rows, want 0 (FORCE ROW LEVEL SECURITY)", n)
	}
	if n := count(""); n != 0 {
		t.Errorf("owner with no tenant context sees %d rows, want 0", n)
	}
}

// ---- constraints ----------------------------------------------------------

// RI checks bypass RLS, so only the composite FKs refuse another tenant's row, for any role.
func TestRLS_ErpWritebacksCompositeFKsRefuseAnotherTenantsRows(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	a, b := ewbNewWorld(t, "fk-a"), ewbNewWorld(t, "fk-b")

	if err := ewbInsert(h.super, a.tenant, a, ewbRow{}); err != nil {
		failIfEwbUndefined(t, "control INSERT", err)
		t.Fatalf("control INSERT with the own client, connection, document and invoice: %v", err)
	}
	roles := []struct {
		name string
		run  func(w ewbWorld) error
	}{
		{"superuser", func(w ewbWorld) error { return ewbInsert(h.super, a.tenant, w, ewbRow{attempt: ewbInt(2)}) }},
		{"invoice_app bound to A", func(w ewbWorld) error {
			return erpApp(a.tenant, nil, func(tx pgx.Tx) error { return ewbInsert(tx, a.tenant, w, ewbRow{attempt: ewbInt(2)}) })
		}},
	}
	own := func(edit func(w *ewbWorld)) ewbWorld { w := a; edit(&w); return w }
	for _, role := range roles {
		for _, c := range []struct {
			name string
			w    ewbWorld
			want string
		}{
			{"another tenant's client", own(func(w *ewbWorld) { w.entity = b.entity }), "erp_writebacks_tenant_entity_fk"},
			{"another tenant's connection", own(func(w *ewbWorld) { w.conn = b.conn }), "erp_writebacks_tenant_connection_fk"},
			{"another tenant's source document", own(func(w *ewbWorld) { w.doc = b.doc }), "erp_writebacks_tenant_document_fk"},
			{"another tenant's invoice", own(func(w *ewbWorld) { w.invoice = b.invoice }), "erp_writebacks_tenant_invoice_fk"},
		} {
			ewbWantErr(t, role.run(c.w), "23503", c.want, role.name+" inserting "+c.name)
		}
	}
	if n := ewbCount(t, ewbTable, a.tenant); n != 1 {
		t.Errorf("rows of A after the refused INSERTs = %d, want only the control", n)
	}
}

func TestRLS_ErpWritebacksChecks(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	w := ewbNewWorld(t, "checks")
	s := erpStr

	type refusal struct {
		name string
		row  ewbRow
		want []string // the constraint that refuses; PG reports the first by name, so two may name one case
	}
	reasonCk, stateCk := "erp_writebacks_reason_check", "erp_writebacks_state_check"
	cases := []refusal{
		{"unknown outcome", ewbRow{outcome: "x"}, []string{"erp_writebacks_outcome_check"}},
		// reason_check also refuses an unknown state and sorts before state_check.
		{"unknown state", ewbRow{state: "x"}, []string{stateCk, reasonCk}},
		{"result_version 0", ewbRow{version: ewbInt(0)}, []string{"erp_writebacks_result_version_check"}},
		{"negative result_version", ewbRow{version: ewbInt(-1)}, []string{"erp_writebacks_result_version_check"}},
		{"attempt 0", ewbRow{attempt: ewbInt(0)}, []string{"erp_writebacks_attempt_check"}},
		{"negative attempt", ewbRow{attempt: ewbInt(-1)}, []string{"erp_writebacks_attempt_check"}},
		{"succeeded with an empty reason", ewbRow{state: "succeeded", reason: s("")}, []string{reasonCk}},
		{"failed without a reason", ewbRow{state: "failed"}, []string{reasonCk}},
		{"failed with an empty reason", ewbRow{state: "failed", reason: s("")}, []string{reasonCk}},
		{"failed with an unknown reason", ewbRow{state: "failed", reason: s("x")}, []string{reasonCk}},
		{"skipped without a reason", ewbRow{state: "skipped"}, []string{reasonCk}},
		{"skipped with an empty reason", ewbRow{state: "skipped", reason: s("")}, []string{reasonCk}},
		{"skipped with an unknown reason", ewbRow{state: "skipped", reason: s("x")}, []string{reasonCk}},
	}
	// A succeeded row carries no reason; a failed row only a failed reason; a skipped row only a skipped one.
	for _, r := range append(slices.Clone(ewbFailedReasons), ewbSkippedReasons...) {
		cases = append(cases, refusal{"succeeded with " + r, ewbRow{state: "succeeded", reason: s(r)}, []string{reasonCk}})
	}
	for _, r := range ewbSkippedReasons {
		cases = append(cases, refusal{"failed with the skipped reason " + r, ewbRow{state: "failed", reason: s(r)}, []string{reasonCk}})
	}
	for _, r := range ewbFailedReasons {
		cases = append(cases, refusal{"skipped with the failed reason " + r, ewbRow{state: "skipped", reason: s(r)}, []string{reasonCk}})
	}
	for _, c := range cases {
		err := ewbInsert(h.super, w.tenant, w, c.row)
		failIfEwbUndefined(t, c.name, err)
		erpWantErr(t, err, "23514", "", c.name)
		if got := pgConstraint(err); !slices.Contains(c.want, got) {
			t.Errorf("%s tripped constraint %q, want one of %v", c.name, got, c.want)
		}
	}

	var accepted int
	ok := []struct {
		name string
		row  ewbRow
	}{
		{"succeeded accepted", ewbRow{state: "succeeded", outcome: "accepted"}},
		{"succeeded rejected", ewbRow{state: "succeeded", outcome: "rejected"}},
		{"succeeded failed outcome", ewbRow{state: "succeeded", outcome: "failed"}},
		{"succeeded with revisions", ewbRow{written: s("r1"), newRev: s("r2")}},
		{"later result and attempt", ewbRow{version: ewbInt(3), attempt: ewbInt(7)}},
	}
	for _, r := range ewbFailedReasons {
		ok = append(ok, struct {
			name string
			row  ewbRow
		}{"failed " + r, ewbRow{state: "failed", reason: s(r)}})
	}
	for _, r := range ewbSkippedReasons {
		ok = append(ok, struct {
			name string
			row  ewbRow
		}{"skipped " + r, ewbRow{state: "skipped", reason: s(r)}})
	}
	for _, c := range ok {
		if err := ewbInsert(h.super, w.tenant, w, c.row); err != nil {
			failIfEwbUndefined(t, c.name, err)
			t.Errorf("%s: want accepted, got %v", c.name, err)
		} else {
			accepted++
		}
	}
	if n := ewbCount(t, ewbTable, w.tenant); n != accepted || n == 0 {
		t.Errorf("rows after the cases = %d, want the %d accepted", n, accepted)
	}
}

// ---- grants ---------------------------------------------------------------

// A missing privilege and a policy refusal are both 42501 on the wire, so the catalog answers here.
func TestRLS_ErpWritebacksGrants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEwbTable(t, h.super)

	for priv, want := range map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false, "TRUNCATE": false} {
		var got bool
		if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.erp_writebacks', $1)`, priv).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", priv, err)
		}
		if got != want {
			t.Errorf("has_table_privilege(invoice_app, erp_writebacks, %s) = %v, want %v", priv, got, want)
		}
	}
	// The probes miss REFERENCES, TRIGGER and any other grantee: read the ACL.
	rows, err := h.super.Query(ctx,
		`SELECT coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee WHERE c.oid = to_regclass('public.erp_writebacks') AND a.grantee <> c.relowner ORDER BY 1`)
	if err != nil {
		t.Fatalf("aclexplode: %v", err)
	}
	var acl []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			t.Fatalf("scan acl: %v", err)
		}
		acl = append(acl, g)
	}
	rows.Close()
	if got, want := strings.Join(acl, ","), "invoice_app:INSERT,invoice_app:SELECT"; got != want {
		t.Errorf("non-owner ACL = %q, want %q", got, want)
	}

	w := ewbNewWorld(t, "grants")
	id := ewbSeed(t, w, ewbRow{})
	for _, c := range []struct{ name, sql string }{
		{"UPDATE", `UPDATE erp_writebacks SET state = 'skipped', reason = 'superseded' WHERE id = $1`},
		{"DELETE", `DELETE FROM erp_writebacks WHERE id = $1`},
	} {
		err := erpApp(w.tenant, nil, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql, id)
			return e
		})
		ewbWantErr(t, err, "42501", "", "invoice_app running "+c.name+" on its own write-back")
		if msg := pgMessage(err); !strings.Contains(msg, "permission denied for table erp_writebacks") {
			t.Errorf("%s refusal message = %q, want the missing table privilege, not a policy", c.name, msg)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_writebacks WHERE id = $1 AND state = 'succeeded'`, id); n != 1 {
		t.Errorf("rows unchanged after the refused UPDATE and DELETE = %d, want 1", n)
	}
}

// ---- cascade --------------------------------------------------------------

// invoices, erp_connections and erp_source_documents are all referenced by the row, so the tenant delete must clear every table.
func TestRLS_ErpWritebacksCascadeWithTenant(t *testing.T) {
	h := requireHarness(t)
	requireEwbTable(t, h.super)
	a, b := ewbNewWorld(t, "casc-a"), ewbNewWorld(t, "casc-b")
	ewbSeed(t, a, ewbRow{})
	ewbSeed(t, b, ewbRow{})

	tables := []string{"business_entities", "erp_connections", "invoices", esdTable, ewbTable}
	for _, table := range tables {
		if n := esdCount(t, table, a.tenant); n == 0 {
			t.Fatalf("%s rows seeded for the tenant = 0, want some", table)
		}
	}
	if _, err := h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, a.tenant); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	for _, table := range tables {
		if n := esdCount(t, table, a.tenant); n != 0 {
			t.Errorf("%s rows after the tenant delete = %d, want 0", table, n)
		}
	}
	if n := ewbCount(t, ewbTable, b.tenant); n != 1 {
		t.Errorf("write-backs of the other tenant = %d, want 1", n)
	}
}

// ---- migration ------------------------------------------------------------

// ewbMigration returns the one migration file; the test fails until it exists.
func ewbMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, ewbMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", ewbMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1 — the erp_writebacks migration is not written yet", ewbMigrationGlob, matches)
	}
	return matches[0]
}

// Runs on the migrator inside a transaction that is always rolled back, so no other test sees the table vanish.
func TestRLS_ErpWritebacksMigrationDownUp(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	migration := ewbMigration(t)
	requireEwbTable(t, h.super)
	// Built before the Down: the re-applied Up is judged on how it behaves, not only on its catalog entries.
	wa, wb := ewbNewWorld(t, "du-a"), ewbNewWorld(t, "du-b")

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	before := erpPublicTables(t, tx, "before the Down")
	if !slices.Contains(before, ewbTable) {
		t.Fatalf("%s is not among the public tables before the Down: %v", ewbTable, before)
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
	if !slices.Equal(dropped, []string{ewbTable}) || len(after) != len(before)-1 {
		t.Errorf("tables the Down dropped = %v (public tables %d -> %d), want exactly [%s]", dropped, len(before), len(after), ewbTable)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	requireEwbTable(t, tx)
	for _, p := range ewbShapeProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	var forced bool
	var policies int
	var grants bool
	if err := tx.QueryRow(ctx,
		`SELECT c.relrowsecurity AND c.relforcerowsecurity, (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_writebacks'), has_table_privilege('invoice_app', c.oid, 'SELECT, INSERT') AND NOT has_table_privilege('invoice_app', c.oid, 'UPDATE') AND NOT has_table_privilege('invoice_app', c.oid, 'DELETE') FROM pg_class c WHERE c.oid = to_regclass('public.erp_writebacks')`,
	).Scan(&forced, &policies, &grants); err != nil || !forced || policies != 1 || !grants {
		t.Errorf("after Down then Up: FORCE RLS = %v, policies = %d, grants as designed = %v (err %v), want true, 1, true", forced, policies, grants, err)
	}

	ewbBehaviourOfReappliedUp(t, tx, wa, wb)

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := erpPublicTables(t, h.super, "after the rollback"); !slices.Equal(got, before) {
		t.Errorf("public tables after the rollback = %v, want the %v seen before the Down", got, before)
	}
}

// ewbBehaviourOfReappliedUp drives the table the migration's own Up just made, as its owner bound by FORCE:
// the live-table tests cannot see a change to the migration file. Each case runs in a rolled-back savepoint.
func ewbBehaviourOfReappliedUp(t *testing.T, tx pgx.Tx, a, b ewbWorld) {
	t.Helper()
	ctx := context.Background()
	bind := func(q pgx.Tx, tenant, scope string) {
		t.Helper()
		if _, err := q.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true), set_config('app.entity_scope', $2, true)`, tenant, scope); err != nil {
			t.Fatalf("bind the tenant: %v", err)
		}
	}
	try := func(fn func(sp pgx.Tx) error) error {
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		defer func() { _ = sp.Rollback(ctx) }()
		return fn(sp)
	}
	bind(tx, a.tenant, "")
	s := erpStr
	const reasonCk = "erp_writebacks_reason_check"

	for _, c := range []struct {
		name string
		row  ewbRow
	}{
		{"succeeded", ewbRow{}},
		{"failed vendor", ewbRow{state: "failed", reason: s("vendor")}},
		{"skipped superseded", ewbRow{state: "skipped", reason: s("superseded")}},
	} {
		if err := try(func(sp pgx.Tx) error { return ewbInsert(sp, a.tenant, a, c.row) }); err != nil {
			t.Errorf("re-applied Up refused a valid %s row: %v", c.name, err)
		}
	}
	for _, c := range []struct {
		name, constraint string
		row              ewbRow
	}{
		{"unknown outcome", "erp_writebacks_outcome_check", ewbRow{outcome: "x"}},
		{"unknown state", "", ewbRow{state: "x"}},
		{"result_version 0", "erp_writebacks_result_version_check", ewbRow{version: ewbInt(0)}},
		{"attempt 0", "erp_writebacks_attempt_check", ewbRow{attempt: ewbInt(0)}},
		{"succeeded with a reason", reasonCk, ewbRow{reason: s("vendor")}},
		{"failed without a reason", reasonCk, ewbRow{state: "failed"}},
		{"failed with a skipped reason", reasonCk, ewbRow{state: "failed", reason: s("superseded")}},
		{"failed with deleted_in_erp", reasonCk, ewbRow{state: "failed", reason: s("deleted_in_erp")}},
		{"skipped without a reason", reasonCk, ewbRow{state: "skipped"}},
		{"skipped with a failed reason", reasonCk, ewbRow{state: "skipped", reason: s("vendor")}},
	} {
		err := try(func(sp pgx.Tx) error { return ewbInsert(sp, a.tenant, a, c.row) })
		if c.constraint == "" { // reason_check also refuses an unknown state and sorts before state_check
			ewbWantErr(t, err, "23514", "", "re-applied Up, "+c.name)
			if got := pgConstraint(err); got != "erp_writebacks_state_check" && got != reasonCk {
				t.Errorf("re-applied Up, %s tripped %q", c.name, got)
			}
			continue
		}
		ewbWantErr(t, err, "23514", c.constraint, "re-applied Up, "+c.name)
	}

	own := func(edit func(w *ewbWorld)) ewbWorld { w := a; edit(&w); return w }
	for _, c := range []struct {
		w          ewbWorld
		constraint string
	}{
		{own(func(w *ewbWorld) { w.entity = b.entity }), "erp_writebacks_tenant_entity_fk"},
		{own(func(w *ewbWorld) { w.conn = b.conn }), "erp_writebacks_tenant_connection_fk"},
		{own(func(w *ewbWorld) { w.doc = b.doc }), "erp_writebacks_tenant_document_fk"},
		{own(func(w *ewbWorld) { w.invoice = b.invoice }), "erp_writebacks_tenant_invoice_fk"},
	} {
		err := try(func(sp pgx.Tx) error { return ewbInsert(sp, a.tenant, c.w, ewbRow{}) })
		ewbWantErr(t, err, "23503", c.constraint, "re-applied Up, a row on another tenant's "+c.constraint)
	}

	// The policy: own tenant only, then the client scope, and nothing without a tenant.
	if err := try(func(sp pgx.Tx) error {
		if err := ewbInsert(sp, a.tenant, a, ewbRow{}); err != nil {
			return err
		}
		for _, c := range []struct {
			name, tenant, scope string
			want                int
		}{
			{"own tenant, every client", a.tenant, "", 1},
			{"own tenant, scope *", a.tenant, "*", 1},
			{"own tenant, its client", a.tenant, "{" + a.entity + "}", 1},
			{"own tenant, another client", a.tenant, "{" + b.entity + "}", 0},
			{"another tenant", b.tenant, "", 0},
			{"no tenant", "", "", 0},
		} {
			bind(sp, c.tenant, c.scope)
			var n int
			if err := sp.QueryRow(ctx, `SELECT count(*) FROM erp_writebacks`).Scan(&n); err != nil {
				return err
			}
			if n != c.want {
				t.Errorf("re-applied Up, %s: rows visible = %d, want %d", c.name, n, c.want)
			}
		}
		return nil
	}); err != nil {
		t.Errorf("re-applied Up, the policy cases: %v", err)
	}
	for _, c := range []struct{ name, bound, row string }{{"no tenant bound", "", a.tenant}, {"another tenant's tenant_id", a.tenant, b.tenant}} {
		err := try(func(sp pgx.Tx) error {
			bind(sp, c.bound, "")
			return ewbInsert(sp, c.row, a, ewbRow{})
		})
		assertRLSViolation(t, err)
		if err == nil {
			t.Errorf("re-applied Up accepted an INSERT with %s", c.name)
		}
	}
}
