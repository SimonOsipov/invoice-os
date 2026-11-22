// Storage suite for erp_source_documents and the two columns the ERP ingest adds to shipped ERP tables
// (invoices.erp_connection_id, erp_connections.ingest_epoch, pinned by erpShape).
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const esdMigrationGlob = "*_erp_source_documents.sql"

const esdTable = "erp_source_documents"

const esdInsertSQL = `INSERT INTO erp_source_documents
	(tenant_id, entity_id, connection_id, source_doc_id, source_revision, state, invoice_id, failure, content_hash, hash_version, claimed_number)
	VALUES ($1, $2, $3, $4, 'r1', $5, $6, $7, $8, $9, $10)`

var esdHash = strings.Repeat("a", 64)

type esdExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// esdRow is one document row; the zero value is a valid pending row with a fresh source_doc_id.
type esdRow struct {
	doc, state                    string
	invoice, failure, hash, claim *string
	version                       *int
}

func esdArgs(tenant, entity, conn string, r esdRow) []any {
	if r.doc == "" {
		r.doc = "doc-" + uuid.NewString()
	}
	if r.state == "" {
		r.state = "pending"
	}
	return []any{tenant, entity, conn, r.doc, r.state, r.invoice, r.failure, r.hash, r.version, r.claim}
}

func esdInsert(q esdExecer, tenant, entity, conn string, r esdRow) error {
	_, err := q.Exec(context.Background(), esdInsertSQL, esdArgs(tenant, entity, conn, r)...)
	return err
}

// failIfEsdUndefined turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfEsdUndefined(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the erp_source_documents migration is not applied yet: %v", what, c, err)
	}
}

// esdWantErr is erpWantErr with a message that names this migration while it is absent.
func esdWantErr(t *testing.T, err error, code, constraint, what string) {
	t.Helper()
	failIfEsdUndefined(t, what, err)
	erpWantErr(t, err, code, constraint, what)
}

func requireEsdTable(t *testing.T, q erpQuerier) {
	t.Helper()
	var reg *string
	if err := q.QueryRow(context.Background(), `SELECT to_regclass('public.erp_source_documents')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass(public.erp_source_documents): %v", err)
	}
	if reg == nil {
		t.Fatal("public.erp_source_documents does not exist — the erp_source_documents migration is not applied yet")
	}
}

func esdSeed(t *testing.T, tenant, entity, conn string, r esdRow) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), esdInsertSQL+` RETURNING id::text`, esdArgs(tenant, entity, conn, r)...).Scan(&id)
	failIfEsdUndefined(t, "seed erp_source_documents", err)
	if err != nil {
		t.Fatalf("seed erp_source_documents: %v", err)
	}
	return id
}

func esdConn(t *testing.T, tenant, entity string) string {
	t.Helper()
	return erpSeedConn(t, tenant, entity, "fake", erpStr("k-"+uuid.NewString()), nil, "pending")
}

func esdInvoice(t *testing.T, tenant, entity string) string {
	t.Helper()
	var id string
	if err := h.super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id::text`,
		tenant, entity, "ESD-"+uuid.NewString()[:8]).Scan(&id); err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	return id
}

type esdWorld struct{ tenant, entity, conn string }

// esdNewWorld makes a throwaway tenant with one client and one connection; the tenant delete cleans up.
func esdNewWorld(t *testing.T, label string) esdWorld {
	t.Helper()
	tenant := erpTenant(t)
	// Registered after the tenant's own cleanup, so it runs first and the RESTRICT FK to invoices never blocks it.
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM erp_source_documents WHERE tenant_id = $1`, tenant)
	})
	entity := erpEntity(t, tenant, "esd-"+label)
	return esdWorld{tenant, entity, esdConn(t, tenant, entity)}
}

func esdCount(t *testing.T, table, tenant string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenant)
}

// ---- shape ----------------------------------------------------------------

var esdShape = map[string]erpCol{
	"tenant_id":                  {"uuid", true, ""},
	"id":                         {"uuid", true, "gen_random_uuid()"},
	"entity_id":                  {"uuid", true, ""},
	"connection_id":              {"uuid", true, ""},
	"source_doc_id":              {"text", true, ""},
	"source_revision":            {"text", true, ""},
	"content_hash":               {"text", false, ""},
	"hash_version":               {"smallint", false, ""},
	"invoice_id":                 {"uuid", false, ""},
	"state":                      {"text", true, "'pending'::text"},
	"failure":                    {"text", false, ""},
	"claimed_number":             {"text", false, ""},
	"differs_from_erp":           {"boolean", true, "false"},
	"changed_after_clearance_at": {"timestamp with time zone", false, ""},
	"deleted_in_erp_at":          {"timestamp with time zone", false, ""},
	"last_job_id":                {"bigint", false, ""},
	"created_at":                 {"timestamp with time zone", true, "now()"},
	"updated_at":                 {"timestamp with time zone", true, "now()"},
}

// The NOT NULLs are derived from esdShape, as erpConstraintsWant does (PG18 names each one).
var esdNamedConstraints = []string{
	"erp_source_documents_pk", "erp_source_documents_tenant_id_id_uq", "erp_source_documents_connection_doc_uq",
	"erp_source_documents_connection_invoice_uq", "erp_source_documents_state_check",
	"erp_source_documents_invoice_iff_taken", "erp_source_documents_failure_check", "erp_source_documents_claim_check",
	"erp_source_documents_hash_check", "erp_source_documents_hash_version_check",
	"erp_source_documents_tenant_entity_fk", "erp_source_documents_tenant_connection_fk",
	"erp_source_documents_tenant_invoice_fk", "erp_source_documents_tenant_id_fkey",
}

var esdIndexes = []string{
	"erp_source_documents_claim_idx", "erp_source_documents_connection_doc_uq", "erp_source_documents_connection_invoice_uq",
	"erp_source_documents_invoice_idx", "erp_source_documents_pk", "erp_source_documents_tenant_id_id_uq",
}

func esdConstraintDefs(q erpQuerier, table string, defs []struct {
	name       string
	has, lacks []string
}) []string {
	var problems []string
	for _, d := range defs {
		var def string
		if err := q.QueryRow(context.Background(),
			`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`,
			"public."+table, d.name).Scan(&def); err != nil {
			problems = append(problems, d.name+": read definition: "+err.Error())
			continue
		}
		for _, frag := range d.has {
			if !strings.Contains(def, frag) {
				problems = append(problems, d.name+" = "+def+", want it to contain "+frag)
			}
		}
		for _, frag := range d.lacks {
			if strings.Contains(def, frag) {
				problems = append(problems, d.name+" = "+def+", want it not to contain "+frag)
			}
		}
	}
	return problems
}

func esdIndexDef(q erpQuerier, name string) (string, error) {
	var def string
	err := q.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`, name).Scan(&def)
	return def, err
}

// esdShapeProblems compares the live table to the Design: columns, constraint names and definitions, indexes.
func esdShapeProblems(q erpQuerier) []string {
	ctx := context.Background()
	var problems []string

	rows, err := q.Query(ctx, `SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`, esdTable)
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
		return append(problems, "information_schema.columns returned no column for erp_source_documents")
	}
	if !reflect.DeepEqual(got, esdShape) {
		problems = append(problems, "columns of erp_source_documents differ from the Design: got "+fmtCols(got)+" want "+fmtCols(esdShape))
	}

	cons, err := q.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = 'public.erp_source_documents'::regclass ORDER BY conname`)
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
	wantCons := slices.Clone(esdNamedConstraints)
	for col, c := range esdShape {
		if c.notNull {
			wantCons = append(wantCons, esdTable+"_"+col+"_not_null")
		}
	}
	sort.Strings(wantCons)
	if !slices.Equal(gotCons, wantCons) {
		problems = append(problems, "constraints of erp_source_documents = "+strings.Join(gotCons, ",")+"\n  want "+strings.Join(wantCons, ","))
	}

	problems = append(problems, esdConstraintDefs(q, esdTable, []struct {
		name       string
		has, lacks []string
	}{
		{"erp_source_documents_pk", []string{"PRIMARY KEY (id)"}, nil},
		{"erp_source_documents_tenant_id_id_uq", []string{"UNIQUE (tenant_id, id)"}, nil},
		{"erp_source_documents_connection_doc_uq", []string{"UNIQUE (connection_id, source_doc_id)"}, nil},
		{"erp_source_documents_connection_invoice_uq", []string{"UNIQUE (connection_id, invoice_id)"}, nil},
		{"erp_source_documents_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}, nil},
		{"erp_source_documents_tenant_entity_fk",
			[]string{"FOREIGN KEY (tenant_id, entity_id)", "REFERENCES business_entities(tenant_id, id)", "ON DELETE CASCADE"}, nil},
		// NO ACTION, as erp_connections_tenant_grant_fk: the tenant cascade still passes.
		{"erp_source_documents_tenant_connection_fk",
			[]string{"FOREIGN KEY (tenant_id, connection_id)", "REFERENCES erp_connections(tenant_id, id)"}, []string{"ON DELETE", "ON UPDATE"}},
		{"erp_source_documents_tenant_invoice_fk",
			[]string{"FOREIGN KEY (tenant_id, invoice_id)", "REFERENCES invoices(tenant_id, id)", "ON DELETE RESTRICT"}, nil},
	})...)

	idx, err := q.Query(ctx, `SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'erp_source_documents' ORDER BY indexname`)
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
	if !slices.Equal(gotIdx, esdIndexes) {
		problems = append(problems, "indexes of erp_source_documents = "+strings.Join(gotIdx, ",")+", want "+strings.Join(esdIndexes, ","))
	}
	if def, err := esdIndexDef(q, "erp_source_documents_invoice_idx"); err != nil {
		problems = append(problems, "read erp_source_documents_invoice_idx: "+err.Error())
	} else {
		for _, frag := range []string{"(tenant_id, invoice_id)", "WHERE (invoice_id IS NOT NULL)"} {
			if !strings.Contains(def, frag) {
				problems = append(problems, "erp_source_documents_invoice_idx = "+def+", want it to contain "+frag)
			}
		}
	}
	if def, err := esdIndexDef(q, "erp_source_documents_claim_idx"); err != nil {
		problems = append(problems, "read erp_source_documents_claim_idx: "+err.Error())
	} else {
		for _, frag := range []string{"(entity_id, claimed_number)", "WHERE (claimed_number IS NOT NULL)"} {
			if !strings.Contains(def, frag) {
				problems = append(problems, "erp_source_documents_claim_idx = "+def+", want it to contain "+frag)
			}
		}
	}
	return problems
}

// invErpProblems checks invoices.erp_connection_id, its composite FK and its partial index.
func invErpProblems(q erpQuerier) []string {
	ctx := context.Background()
	var problems []string
	var typ, nullable string
	var def *string
	err := q.QueryRow(ctx, `SELECT data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'invoices' AND column_name = 'erp_connection_id'`).Scan(&typ, &nullable, &def)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		problems = append(problems, "invoices.erp_connection_id does not exist")
	case err != nil:
		problems = append(problems, "read invoices.erp_connection_id: "+err.Error())
	case typ != "uuid" || nullable != "YES" || def != nil:
		problems = append(problems, "invoices.erp_connection_id is "+typ+" nullable="+nullable+" default set="+boolStr(def != nil)+", want a nullable uuid with no default")
	}
	problems = append(problems, esdConstraintDefs(q, "invoices", []struct {
		name       string
		has, lacks []string
	}{
		{"invoices_tenant_erp_connection_fk",
			[]string{"FOREIGN KEY (tenant_id, erp_connection_id)", "REFERENCES erp_connections(tenant_id, id)", "ON DELETE RESTRICT"}, nil},
	})...)
	if idx, err := esdIndexDef(q, "invoices_erp_connection_id_idx"); err != nil {
		problems = append(problems, "read invoices_erp_connection_id_idx: "+err.Error())
	} else {
		for _, frag := range []string{"(erp_connection_id)", "WHERE (erp_connection_id IS NOT NULL)"} {
			if !strings.Contains(idx, frag) {
				problems = append(problems, "invoices_erp_connection_id_idx = "+idx+", want it to contain "+frag)
			}
		}
	}
	return problems
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func TestRLS_ErpSourceDocumentsShape(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	for _, p := range esdShapeProblems(h.super) {
		t.Error(p)
	}
}

func TestRLS_InvoicesErpConnectionColumn(t *testing.T) {
	h := requireHarness(t)
	for _, p := range invErpProblems(h.super) {
		t.Error(p)
	}

	// RESTRICT, not CASCADE or SET NULL: a connection an invoice came from cannot be deleted.
	w := esdNewWorld(t, "invcol")
	inv := esdInvoice(t, w.tenant, w.entity)
	_, err := h.super.Exec(context.Background(), `UPDATE invoices SET erp_connection_id = $1 WHERE id = $2`, w.conn, inv)
	failIfEsdUndefined(t, "set invoices.erp_connection_id", err)
	if err != nil {
		t.Fatalf("set invoices.erp_connection_id to the own connection: %v", err)
	}
	_, err = h.super.Exec(context.Background(), `DELETE FROM erp_connections WHERE id = $1`, w.conn)
	esdWantErr(t, err, "23001", "invoices_tenant_erp_connection_fk", "deleting a connection an invoice points at")
}

// ---- RLS ------------------------------------------------------------------

func TestRLS_ErpSourceDocumentsForceRLSAndPolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.erp_source_documents')`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, cmd, roles::text[], coalesce(qual, '') FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_source_documents'`)
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

func TestRLS_ErpSourceDocumentsCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)
	a, b := esdNewWorld(t, "xa"), esdNewWorld(t, "xb")
	ra := esdSeed(t, a.tenant, a.entity, a.conn, esdRow{})
	rb := esdSeed(t, b.tenant, b.entity, b.conn, esdRow{})

	if got := erpAppIDs(t, a.tenant, nil, esdTable); !slices.Equal(got, []string{ra}) {
		t.Errorf("rows visible to A = %v, want only its own [%s]", got, ra)
	}
	if got := erpAppIDs(t, b.tenant, nil, esdTable); !slices.Equal(got, []string{rb}) {
		t.Errorf("rows visible to B = %v, want only its own [%s]", got, rb)
	}

	// B's own client and connection, so only the policy can refuse the row.
	err := erpApp(a.tenant, nil, func(tx pgx.Tx) error { return esdInsert(tx, a.tenant, a.entity, a.conn, esdRow{}) })
	failIfEsdUndefined(t, "control INSERT in the own tenant", err)
	if err != nil {
		t.Fatalf("control INSERT in the own tenant: %v", err)
	}
	err = erpApp(a.tenant, nil, func(tx pgx.Tx) error { return esdInsert(tx, b.tenant, b.entity, b.conn, esdRow{}) })
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, esdTable) {
		t.Errorf("insert refusal message = %q, want the erp_source_documents RLS WITH CHECK violation", msg)
	}
	if n := esdCount(t, esdTable, b.tenant); n != 1 {
		t.Errorf("rows of B after the refused INSERT = %d, want its 1", n)
	}

	// Moving an own row to B trips WITH CHECK; B's row is invisible to A, so an UPDATE of it matches nothing.
	err = erpApp(a.tenant, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE erp_source_documents SET tenant_id = $1 WHERE id = $2`, b.tenant, ra)
		return e
	})
	assertRLSViolation(t, err)
	var n int64
	err = erpApp(a.tenant, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE erp_source_documents SET state = 'held' WHERE id = $1`, rb)
		n = tag.RowsAffected()
		return e
	})
	if err != nil || n != 0 {
		t.Errorf("A updating B's row: rows = %d, err = %v, want 0 and nil", n, err)
	}
	if got := mustCount(t, h.super, `SELECT count(*) FROM erp_source_documents WHERE id = $1 AND state = 'pending' AND tenant_id = $2`, rb, b.tenant); got != 1 {
		t.Errorf("B's row after A's UPDATE no longer pending in B: %d matches, want 1", got)
	}
}

func TestRLS_ErpSourceDocumentsClientScopeHidesOtherClients(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	a := esdNewWorld(t, "scope-x")
	y := erpEntity(t, a.tenant, "esd-scope-y")
	connY := esdConn(t, a.tenant, y)
	rx := esdSeed(t, a.tenant, a.entity, a.conn, esdRow{})
	ry := esdSeed(t, a.tenant, y, connY, esdRow{})
	both := sortedIDs(rx, ry)

	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"{" + a.entity + "}", []string{rx}},
		{"{" + y + "}", []string{ry}},
		{"{" + a.entity + "," + y + "}", both},
		{"*", both},
		{"", both},
		{"{}", nil},
	} {
		scope := c.scope
		if got := erpAppIDs(t, a.tenant, &scope, esdTable); !slices.Equal(got, c.want) {
			t.Errorf("scope %q: visible documents = %v, want %v", c.scope, got, c.want)
		}
	}

	// The policy's WITH CHECK keeps a limited member from writing a document of another client.
	scope := "{" + a.entity + "}"
	err := erpApp(a.tenant, &scope, func(tx pgx.Tx) error { return esdInsert(tx, a.tenant, a.entity, a.conn, esdRow{}) })
	failIfEsdUndefined(t, "control INSERT inside the scope", err)
	if err != nil {
		t.Fatalf("control INSERT inside the scope: %v", err)
	}
	assertRLSViolation(t, erpApp(a.tenant, &scope, func(tx pgx.Tx) error { return esdInsert(tx, a.tenant, y, connY, esdRow{}) }))
	var n int64
	err = erpApp(a.tenant, &scope, func(tx pgx.Tx) error {
		tag, e := tx.Exec(context.Background(), `UPDATE erp_source_documents SET state = 'held' WHERE id = $1`, ry)
		n = tag.RowsAffected()
		return e
	})
	if err != nil || n != 0 {
		t.Errorf("UPDATE of an out-of-scope document: rows = %d, err = %v, want 0 and nil", n, err)
	}
}

func TestRLS_ErpSourceDocumentsNoTenantSeesNothing(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "noguc")
	esdSeed(t, w.tenant, w.entity, w.conn, esdRow{})
	if n := esdCount(t, esdTable, w.tenant); n != 1 {
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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM erp_source_documents`)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("SELECT with no tenant context (empty GUC %v): %v", emptyGUC, err)
		}
		if n != 0 {
			t.Errorf("rows visible with no tenant set (empty GUC %v) = %d, want 0", emptyGUC, n)
		}
		err = esdInsert(tx, w.tenant, w.entity, w.conn, esdRow{})
		_ = tx.Rollback(ctx)
		assertRLSViolation(t, err)
	}
}

// FORCE subjects the migrator (the table owner) to the policy.
func TestRLS_ErpSourceDocumentsOwnerIsBoundByForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)
	a, b := esdNewWorld(t, "owner-a"), esdNewWorld(t, "owner-b")
	esdSeed(t, a.tenant, a.entity, a.conn, esdRow{})

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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM erp_source_documents`)
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
func TestRLS_ErpSourceDocumentsCompositeFKsRefuseAnotherTenantsRows(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	a, b := esdNewWorld(t, "fk-a"), esdNewWorld(t, "fk-b")
	invA, invB := esdInvoice(t, a.tenant, a.entity), esdInvoice(t, b.tenant, b.entity)

	if err := esdInsert(h.super, a.tenant, a.entity, a.conn, esdRow{state: "drafted", invoice: &invA}); err != nil {
		t.Fatalf("control INSERT with the own client, connection and invoice: %v", err)
	}
	roles := []struct {
		name string
		run  func(entity, conn string, r esdRow) error
	}{
		{"superuser", func(entity, conn string, r esdRow) error { return esdInsert(h.super, a.tenant, entity, conn, r) }},
		{"invoice_app bound to A", func(entity, conn string, r esdRow) error {
			return erpApp(a.tenant, nil, func(tx pgx.Tx) error { return esdInsert(tx, a.tenant, entity, conn, r) })
		}},
	}
	for _, role := range roles {
		for _, c := range []struct {
			name         string
			entity, conn string
			row          esdRow
			want         string
		}{
			{"another tenant's client", b.entity, a.conn, esdRow{}, "erp_source_documents_tenant_entity_fk"},
			{"another tenant's connection", a.entity, b.conn, esdRow{}, "erp_source_documents_tenant_connection_fk"},
			{"another tenant's invoice", a.entity, a.conn, esdRow{state: "drafted", invoice: &invB}, "erp_source_documents_tenant_invoice_fk"},
		} {
			esdWantErr(t, role.run(c.entity, c.conn, c.row), "23503", c.want, role.name+" inserting "+c.name)
		}
	}
	if n := esdCount(t, esdTable, a.tenant); n != 1 {
		t.Errorf("rows of A after the refused INSERTs = %d, want only the control", n)
	}
}

func TestRLS_InvoicesErpConnectionRefusesAnotherTenantsConnection(t *testing.T) {
	h := requireHarness(t)
	a, b := esdNewWorld(t, "invfk-a"), esdNewWorld(t, "invfk-b")
	inv := esdInvoice(t, a.tenant, a.entity)
	set := `UPDATE invoices SET erp_connection_id = $1 WHERE id = $2`

	_, err := h.super.Exec(context.Background(), set, b.conn, inv)
	esdWantErr(t, err, "23503", "invoices_tenant_erp_connection_fk", "pointing an invoice at another tenant's connection")
	if _, err := h.super.Exec(context.Background(), set, a.conn, inv); err != nil {
		t.Fatalf("pointing an invoice at its own tenant's connection: %v", err)
	}
	var got *string
	if err := h.super.QueryRow(context.Background(), `SELECT erp_connection_id::text FROM invoices WHERE id = $1`, inv).Scan(&got); err != nil || got == nil || *got != a.conn {
		t.Errorf("invoices.erp_connection_id = %v (err %v), want %s", got, err, a.conn)
	}
}

func TestRLS_ErpSourceDocumentsOneRowPerConnectionDocument(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "uq-doc")
	conn2 := esdConn(t, w.tenant, w.entity)
	esdSeed(t, w.tenant, w.entity, w.conn, esdRow{doc: "doc-1"})

	err := esdInsert(h.super, w.tenant, w.entity, w.conn, esdRow{doc: "doc-1"})
	esdWantErr(t, err, "23505", "erp_source_documents_connection_doc_uq", "the same document on the same connection")
	if err := esdInsert(h.super, w.tenant, w.entity, conn2, esdRow{doc: "doc-1"}); err != nil {
		t.Errorf("the same source_doc_id on another connection: want accepted, got %v", err)
	}
}

func TestRLS_ErpSourceDocumentsOneDocumentPerInvoicePerConnection(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "uq-inv")
	conn2 := esdConn(t, w.tenant, w.entity)
	inv := esdInvoice(t, w.tenant, w.entity)
	esdSeed(t, w.tenant, w.entity, w.conn, esdRow{state: "linked", invoice: &inv})

	err := esdInsert(h.super, w.tenant, w.entity, w.conn, esdRow{state: "linked", invoice: &inv})
	esdWantErr(t, err, "23505", "erp_source_documents_connection_invoice_uq", "a second document for the same invoice on the same connection")
	if err := esdInsert(h.super, w.tenant, w.entity, conn2, esdRow{state: "linked", invoice: &inv}); err != nil {
		t.Errorf("the same invoice on another connection: want accepted, got %v", err)
	}
	// NULLs are distinct, so documents with no invoice never collide.
	for range 2 {
		if err := esdInsert(h.super, w.tenant, w.entity, w.conn, esdRow{}); err != nil {
			t.Errorf("a second document with no invoice on the connection: want accepted, got %v", err)
		}
	}
}

func TestRLS_ErpSourceDocumentsChecks(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "checks")
	inv := func() *string { id := esdInvoice(t, w.tenant, w.entity); return &id }
	s := erpStr
	one, zero, minusOne := 1, 0, -1
	hash63, hash65 := strings.Repeat("a", 63), strings.Repeat("a", 65)
	upper, nonHex := strings.Repeat("A", 64), strings.Repeat("g", 64)

	var accepted int
	for _, c := range []struct {
		name string
		row  esdRow
		want string // constraint that refuses; "" accepts
	}{
		{"unknown state", esdRow{state: "x"}, "erp_source_documents_state_check"},
		{"drafted without an invoice", esdRow{state: "drafted"}, "erp_source_documents_invoice_iff_taken"},
		{"linked without an invoice", esdRow{state: "linked"}, "erp_source_documents_invoice_iff_taken"},
		{"pending with an invoice", esdRow{state: "pending", invoice: inv()}, "erp_source_documents_invoice_iff_taken"},
		{"held with an invoice", esdRow{state: "held", invoice: inv()}, "erp_source_documents_invoice_iff_taken"},
		{"deleted with an invoice", esdRow{state: "deleted", invoice: inv()}, "erp_source_documents_invoice_iff_taken"},
		{"failed with an invoice", esdRow{state: "failed", failure: s("refused"), invoice: inv()}, "erp_source_documents_invoice_iff_taken"},
		{"failed without a failure", esdRow{state: "failed"}, "erp_source_documents_failure_check"},
		{"failed with an unknown failure", esdRow{state: "failed", failure: s("x")}, "erp_source_documents_failure_check"},
		{"failed with an empty failure", esdRow{state: "failed", failure: s("")}, "erp_source_documents_failure_check"},
		{"held with a failure", esdRow{state: "held", failure: s("refused")}, "erp_source_documents_failure_check"},
		{"pending with a failure", esdRow{state: "pending", failure: s("exhausted")}, "erp_source_documents_failure_check"},
		{"drafted with a failure", esdRow{state: "drafted", invoice: inv(), failure: s("refused")}, "erp_source_documents_failure_check"},
		{"hash of 63 hex", esdRow{hash: &hash63, version: &one}, "erp_source_documents_hash_check"},
		{"hash of 65 hex", esdRow{hash: &hash65, version: &one}, "erp_source_documents_hash_check"},
		{"upper-case hash", esdRow{hash: &upper, version: &one}, "erp_source_documents_hash_check"},
		{"non-hex hash", esdRow{hash: &nonHex, version: &one}, "erp_source_documents_hash_check"},
		{"hash without a hash_version", esdRow{hash: &esdHash}, "erp_source_documents_hash_version_check"},
		{"hash_version without a hash", esdRow{version: &one}, "erp_source_documents_hash_version_check"},
		{"hash_version 0", esdRow{hash: &esdHash, version: &zero}, "erp_source_documents_hash_version_check"},
		{"negative hash_version", esdRow{hash: &esdHash, version: &minusOne}, "erp_source_documents_hash_version_check"},

		{"pending", esdRow{state: "pending"}, ""},
		{"held", esdRow{state: "held"}, ""},
		{"deleted", esdRow{state: "deleted"}, ""},
		{"drafted with an invoice", esdRow{state: "drafted", invoice: inv()}, ""},
		{"linked with an invoice", esdRow{state: "linked", invoice: inv()}, ""},
		{"failed unmappable", esdRow{state: "failed", failure: s("unmappable")}, ""},
		{"failed refused", esdRow{state: "failed", failure: s("refused")}, ""},
		{"failed not_found", esdRow{state: "failed", failure: s("not_found")}, ""},
		{"failed no_mapping", esdRow{state: "failed", failure: s("no_mapping")}, ""},
		{"failed exhausted", esdRow{state: "failed", failure: s("exhausted")}, ""},
		{"hash with hash_version 1", esdRow{hash: &esdHash, version: &one}, ""},
	} {
		err := esdInsert(h.super, w.tenant, w.entity, w.conn, c.row)
		if c.want == "" {
			failIfEsdUndefined(t, c.name, err)
			if err != nil {
				t.Errorf("%s: want accepted, got %v", c.name, err)
			} else {
				accepted++
			}
			continue
		}
		esdWantErr(t, err, "23514", c.want, c.name)
	}
	if n := esdCount(t, esdTable, w.tenant); n != accepted || n == 0 {
		t.Errorf("rows after the cases = %d, want the %d accepted", n, accepted)
	}
}

func TestRLS_ErpSourceDocumentsClaimOnlyWhilePending(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	w := esdNewWorld(t, "claim")
	claim := erpStr("N")
	inv := func() *string { id := esdInvoice(t, w.tenant, w.entity); return &id }

	if err := esdInsert(h.super, w.tenant, w.entity, w.conn, esdRow{state: "pending", claim: claim}); err != nil {
		failIfEsdUndefined(t, "pending with a claim", err)
		t.Errorf("pending with a claimed_number: want accepted, got %v", err)
	}
	for _, c := range []struct {
		name string
		row  esdRow
	}{
		{"drafted", esdRow{state: "drafted", invoice: inv(), claim: claim}},
		{"linked", esdRow{state: "linked", invoice: inv(), claim: claim}},
		{"held", esdRow{state: "held", claim: claim}},
		{"deleted", esdRow{state: "deleted", claim: claim}},
		{"failed", esdRow{state: "failed", failure: erpStr("refused"), claim: claim}},
	} {
		err := esdInsert(h.super, w.tenant, w.entity, w.conn, c.row)
		esdWantErr(t, err, "23514", "erp_source_documents_claim_check", c.name+" with a claimed_number")
	}
	if n := esdCount(t, esdTable, w.tenant); n != 1 {
		t.Errorf("rows after the cases = %d, want the 1 pending row", n)
	}
}

// A missing privilege and a policy refusal are both 42501 on the wire, so the catalog answers here.
func TestRLS_ErpSourceDocumentsGrants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireEsdTable(t, h.super)

	for priv, want := range map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": false, "TRUNCATE": false} {
		var got bool
		if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.erp_source_documents', $1)`, priv).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", priv, err)
		}
		if got != want {
			t.Errorf("has_table_privilege(invoice_app, erp_source_documents, %s) = %v, want %v", priv, got, want)
		}
	}
	// The probes miss REFERENCES, TRIGGER and any other grantee: read the ACL.
	rows, err := h.super.Query(ctx,
		`SELECT coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a LEFT JOIN pg_roles r ON r.oid = a.grantee WHERE c.oid = to_regclass('public.erp_source_documents') AND a.grantee <> c.relowner ORDER BY 1`)
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
	if got, want := strings.Join(acl, ","), "invoice_app:INSERT,invoice_app:SELECT,invoice_app:UPDATE"; got != want {
		t.Errorf("non-owner ACL = %q, want %q", got, want)
	}

	w := esdNewWorld(t, "grants")
	id := esdSeed(t, w.tenant, w.entity, w.conn, esdRow{})
	err = erpApp(w.tenant, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM erp_source_documents WHERE id = $1`, id)
		return e
	})
	esdWantErr(t, err, "42501", "", "invoice_app deleting its own document")
	if msg := pgMessage(err); !strings.Contains(msg, "permission denied for table erp_source_documents") {
		t.Errorf("DELETE refusal message = %q, want the missing table privilege, not a policy", msg)
	}
	if n := esdCount(t, esdTable, w.tenant); n != 1 {
		t.Errorf("rows after the refused DELETE = %d, want 1", n)
	}
}

// ---- cascade --------------------------------------------------------------

// invoices and erp_connections hold the document, so the tenant delete must clear all four tables.
func TestRLS_ErpSourceDocumentsCascadeWithTenant(t *testing.T) {
	h := requireHarness(t)
	requireEsdTable(t, h.super)
	a, b := esdNewWorld(t, "casc-a"), esdNewWorld(t, "casc-b")
	inv := esdInvoice(t, a.tenant, a.entity)
	esdSeed(t, a.tenant, a.entity, a.conn, esdRow{state: "drafted", invoice: &inv})
	esdSeed(t, b.tenant, b.entity, b.conn, esdRow{})

	tables := []string{"business_entities", "erp_connections", "invoices", esdTable}
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
	if n := esdCount(t, esdTable, b.tenant); n != 1 {
		t.Errorf("documents of the other tenant = %d, want 1", n)
	}
}

// ---- migration ------------------------------------------------------------

func esdHasColumn(t *testing.T, q erpQuerier, table, column string) bool {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
		table, column).Scan(&n); err != nil {
		t.Fatalf("read information_schema.columns for %s.%s: %v", table, column, err)
	}
	return n == 1
}

// esdMigration returns the one migration file; the test fails until it exists.
func esdMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, esdMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", esdMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1 — the erp_source_documents migration is not written yet", esdMigrationGlob, matches)
	}
	return matches[0]
}

// Runs on the migrator inside a transaction that is always rolled back, so no other test sees the table vanish.
func TestRLS_ErpSourceDocumentsMigrationDownUp(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	migration := esdMigration(t)
	requireEsdTable(t, h.super)

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	before := erpPublicTables(t, tx, "before the Down")
	if !slices.Contains(before, esdTable) {
		t.Fatalf("%s is not among the public tables before the Down: %v", esdTable, before)
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
	if !slices.Equal(dropped, []string{esdTable}) || len(after) != len(before)-1 {
		t.Errorf("tables the Down dropped = %v (public tables %d -> %d), want exactly [%s]", dropped, len(before), len(after), esdTable)
	}
	if esdHasColumn(t, tx, "invoices", "erp_connection_id") {
		t.Error("invoices.erp_connection_id is still there after the Down")
	}
	if esdHasColumn(t, tx, "erp_connections", "ingest_epoch") {
		t.Error("erp_connections.ingest_epoch is still there after the Down")
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	requireEsdTable(t, tx)
	for _, p := range esdShapeProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, p := range invErpProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, p := range erpShapeProblems(tx) { // carries erp_connections.ingest_epoch
		t.Errorf("after Down then Up: %s", p)
	}
	var forced bool
	var policies int
	var grants bool
	if err := tx.QueryRow(ctx,
		`SELECT c.relrowsecurity AND c.relforcerowsecurity, (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_source_documents'), has_table_privilege('invoice_app', c.oid, 'SELECT, INSERT, UPDATE') AND NOT has_table_privilege('invoice_app', c.oid, 'DELETE') FROM pg_class c WHERE c.oid = to_regclass('public.erp_source_documents')`,
	).Scan(&forced, &policies, &grants); err != nil || !forced || policies != 1 || !grants {
		t.Errorf("after Down then Up: FORCE RLS = %v, policies = %d, grants as designed = %v (err %v), want true, 1, true", forced, policies, grants, err)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := erpPublicTables(t, h.super, "after the rollback"); !slices.Equal(got, before) {
		t.Errorf("public tables after the rollback = %v, want the %v seen before the Down", got, before)
	}
}
