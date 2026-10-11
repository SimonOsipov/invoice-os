// RLS, grant, constraint, history-cause and migration suite for `custom_rule_revisions`
// (ENGI-14-01). Rows are seeded per test through h.super, never in harness.seed(), so a
// missing table fails only these cases. A rejected statement poisons its transaction, so
// each one runs in its own db.WithinTenantTx.
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

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const crrInsert = `INSERT INTO custom_rule_revisions
	(id, tenant_id, rule_id, revision, entity_id, type, target, severity, message, enabled, created_by)
	VALUES ($1, $2, $3, $4, $5, 'required', 'invoice.number', $6, $7, true, 'qa')`

// The audit_log_entity_for definition the new migration replaces; its Down must restore it.
const crrResolverBaseline = "20260903055829_audit_log_entity_for_anchor_learned.sql"

// crrRequireTable turns a missing migration into an assertion failure, not a driver error.
func crrRequireTable(t *testing.T, q querier) {
	t.Helper()
	var reg *string
	if err := q.QueryRow(context.Background(),
		`SELECT to_regclass('public.custom_rule_revisions')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass('public.custom_rule_revisions'): %v", err)
	}
	if reg == nil {
		t.Fatal("public.custom_rule_revisions does not exist: the custom_rules migration is not applied")
	}
}

// seedCRR inserts one revision as the superuser (BYPASSRLS) and returns its id and rule id.
func seedCRR(t *testing.T, tenantID string, entityID any, revision int) (id, ruleID string) {
	t.Helper()
	id, ruleID = uuid.NewString(), uuid.NewString()
	if _, err := h.super.Exec(context.Background(), crrInsert,
		id, tenantID, ruleID, revision, entityID, "error", "must be present"); err != nil {
		t.Fatalf("seed custom_rule_revisions (%s, revision %d): %v", tenantID, revision, err)
	}
	crrCleanup(t, id)
	return id, ruleID
}

// crrCleanup deletes rows by id as the superuser once the test ends; app-inserted rows too.
func crrCleanup(t *testing.T, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM custom_rule_revisions WHERE id = ANY($1)`, ids)
	})
}

// crrAsApp runs one statement as invoice_app under tenantID and returns its error.
func crrAsApp(tenantID, sql string, args ...any) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, sql, args...)
		return e
	})
}

func crrCount(t *testing.T, id string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM custom_rule_revisions WHERE id = $1`, id)
}

func crrReadRechecked(t *testing.T, id string) *string {
	t.Helper()
	var v *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT rechecked_at::text FROM custom_rule_revisions WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read rechecked_at for %s: %v", id, err)
	}
	return v
}

func TestRLS_CustomRuleRevisionsTwoTenantsSeeOnlyTheirOwn(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	a1, _ := seedCRR(t, h.tenantA, nil, 1)
	a2, _ := seedCRR(t, h.tenantA, nil, 1)
	b1, _ := seedCRR(t, h.tenantB, nil, 1)

	wantA := []string{a1, a2}
	sort.Strings(wantA)
	for _, c := range []struct {
		tenant string
		want   []string
	}{
		{h.tenantA, wantA},
		{h.tenantB, []string{b1}},
	} {
		var got []string
		err := db.WithinTenantTx(ctx, h.app, c.tenant, func(tx pgx.Tx) error {
			rows, e := tx.Query(ctx, `SELECT id::text FROM custom_rule_revisions ORDER BY id`)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					return e
				}
				got = append(got, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("tenant %s read: %v", c.tenant, err)
		}
		if len(got) == 0 {
			t.Errorf("tenant %s sees no revisions, want %d", c.tenant, len(c.want))
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tenant %s sees ids %v, want exactly %v", c.tenant, got, c.want)
		}
	}
}

func TestRLS_CustomRuleRevisionsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	crossID, ownID := uuid.NewString(), uuid.NewString()
	crrCleanup(t, crossID, ownID)

	err := crrAsApp(h.tenantA, crrInsert, crossID, h.tenantB, uuid.NewString(), 1, nil, "error", "m")
	csAssertPGCode(t, err, "42501", "", "an insert of tenant B's row scoped to tenant A")
	if n := crrCount(t, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant insert = %d, want 0", n)
	}

	// Control: the same statement for the scoped tenant lands, so the refusal is RLS.
	if err := crrAsApp(h.tenantA, crrInsert, ownID, h.tenantA, uuid.NewString(), 1, nil, "error", "m"); err != nil {
		t.Fatalf("own-tenant insert through the same statement: want success, got: %v", err)
	}
	if n := crrCount(t, ownID); n != 1 {
		t.Errorf("rows after the own-tenant insert = %d, want 1", n)
	}
}

func TestRLS_CustomRuleRevisionsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	rowA, _ := seedCRR(t, h.tenantA, nil, 1)

	err := crrAsApp(h.tenantA,
		`UPDATE custom_rule_revisions SET rechecked_at = now(), tenant_id = $1 WHERE id = $2`, h.tenantB, rowA)
	csAssertPGCode(t, err, "42501", "", "reassigning a row's tenant_id while marking it rechecked")

	var owner string
	if e := h.super.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM custom_rule_revisions WHERE id = $1`, rowA).Scan(&owner); e != nil {
		t.Fatalf("read tenant_id for %s: %v", rowA, e)
	}
	if owner != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %s, want the seeded %s", owner, h.tenantA)
	}
	if v := crrReadRechecked(t, rowA); v != nil {
		t.Errorf("rechecked_at after the refused statement = %s, want NULL: the statement is atomic", *v)
	}

	// Control: marking the same row rechecked, alone, succeeds.
	if err := crrAsApp(h.tenantA, `UPDATE custom_rule_revisions SET rechecked_at = now() WHERE id = $1`, rowA); err != nil {
		t.Fatalf("rechecked_at-only update of the same row: want success, got: %v", err)
	}
	if v := crrReadRechecked(t, rowA); v == nil {
		t.Error("rechecked_at after the control update is NULL, want a timestamp")
	}
}

func TestRLS_CustomRuleRevisionsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	rowA, _ := seedCRR(t, h.tenantA, nil, 1)
	rowB, _ := seedCRR(t, h.tenantB, nil, 1)
	insID := uuid.NewString()
	crrCleanup(t, insID)

	// Control: the zero below means nothing unless the rows are there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM custom_rule_revisions WHERE id = ANY($1)`,
		[]string{rowA, rowB}); n != 2 {
		t.Fatalf("superuser sees %d of the two seeded rows, want 2", n)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without a tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM custom_rule_revisions`).Scan(&n); err != nil {
		t.Fatalf("count with no tenant set: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant unset = %d, want 0", n)
	}

	_, err = tx.Exec(ctx, crrInsert, insID, h.tenantA, uuid.NewString(), 1, nil, "error", "m")
	csAssertPGCode(t, err, "42501", "", "an insert with no tenant GUC")

	// A pooled connection reads the GUC as '' after a tenant tx ends: that must be no tenant,
	// not a uuid cast error.
	tx2, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin for the empty-GUC case: %v", err)
	}
	defer func() { _ = tx2.Rollback(context.Background()) }()
	if _, err := tx2.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
		t.Fatalf("set an empty app.current_tenant: %v", err)
	}
	if err := tx2.QueryRow(ctx, `SELECT count(*) FROM custom_rule_revisions`).Scan(&n); err != nil {
		t.Fatalf("count with an empty tenant GUC: want zero rows and no error, got: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant = '' = %d, want 0", n)
	}
	_, err = tx2.Exec(ctx, crrInsert, insID, h.tenantA, uuid.NewString(), 1, nil, "error", "m")
	csAssertPGCode(t, err, "42501", "", "an insert with an empty tenant GUC")
}

// FORCE subjects the migrator (the table owner) to the policy: with no GUC it matches nothing.
func TestRLS_CustomRuleRevisionsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	id, okID := uuid.NewString(), uuid.NewString()
	crrCleanup(t, id, okID)

	_, err := h.mig.Exec(ctx, crrInsert, id, h.tenantA, uuid.NewString(), 1, nil, "error", "m")
	csAssertPGCode(t, err, "42501", "", "the migrator inserting with no tenant GUC")
	if n := crrCount(t, id); n != 0 {
		t.Errorf("rows after the refused owner insert = %d, want 0", n)
	}

	// Control: scoped to the row's own tenant the migrator may insert, so the refusal above
	// is the forced policy and not a missing privilege.
	err = db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, crrInsert, okID, h.tenantA, uuid.NewString(), 1, nil, "error", "m")
		return e
	})
	if err != nil {
		t.Fatalf("the migrator inserting its own tenant's row under the GUC: want success, got: %v", err)
	}
	if n := crrCount(t, okID); n != 1 {
		t.Errorf("rows after the scoped owner insert = %d, want 1", n)
	}
}

func TestRLS_CustomRuleRevisionsForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.custom_rule_revisions'::regclass`,
	).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class for custom_rule_revisions: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("custom_rule_revisions relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}

	type policy struct {
		roles []string
		cmd   string
		qual  string
	}
	got := map[string]policy{}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'custom_rule_revisions'`)
	if err != nil {
		t.Fatalf("query pg_policies for custom_rule_revisions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var p policy
		if e := rows.Scan(&name, &p.roles, &p.cmd, &p.qual); e != nil {
			t.Fatalf("scan pg_policies row: %v", e)
		}
		got[name] = p
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_policies rows: %v", e)
	}

	if len(got) != 1 {
		t.Fatalf("policies on custom_rule_revisions = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	p, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on custom_rule_revisions; got %v", got)
	}
	if p.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want ALL", p.cmd)
	}
	if !reflect.DeepEqual(p.roles, []string{"public"}) {
		t.Errorf("tenant_isolation roles = %v, want [public]: a TO clause leaves unnamed roles unbound", p.roles)
	}
	if !strings.Contains(p.qual, "app.current_tenant") || !strings.Contains(p.qual, "tenant_id") {
		t.Errorf("tenant_isolation USING = %q, want a tenant_id predicate on app.current_tenant", p.qual)
	}
}

func TestRLS_CustomRuleRevisionsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false}, // table-level; the one writable column is granted below
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		if err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.custom_rule_revisions', $2)`, c.role, c.priv,
		).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%q, custom_rule_revisions, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, custom_rule_revisions, %q) = %v, want %v", c.role, c.priv, got, c.want)
		}
	}

	rows, err := h.super.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'custom_rule_revisions'`)
	if err != nil {
		t.Fatalf("query custom_rule_revisions columns: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if e := rows.Scan(&c); e != nil {
			t.Fatalf("scan column name: %v", e)
		}
		cols = append(cols, c)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate columns: %v", e)
	}
	if !slices.Contains(cols, "rechecked_at") || len(cols) < 10 {
		t.Fatalf("custom_rule_revisions columns = %v, want the full design list including rechecked_at", cols)
	}

	for _, col := range cols {
		var appUpdate, appSelect, appInsert, readerAny bool
		if err := h.super.QueryRow(ctx,
			`SELECT has_column_privilege('invoice_app', 'public.custom_rule_revisions', $1, 'UPDATE'),
			        has_column_privilege('invoice_app', 'public.custom_rule_revisions', $1, 'SELECT'),
			        has_column_privilege('invoice_app', 'public.custom_rule_revisions', $1, 'INSERT'),
			        has_column_privilege('invoice_tenant_reader', 'public.custom_rule_revisions', $1, 'SELECT')
			     OR has_column_privilege('invoice_tenant_reader', 'public.custom_rule_revisions', $1, 'INSERT')
			     OR has_column_privilege('invoice_tenant_reader', 'public.custom_rule_revisions', $1, 'UPDATE')`,
			col).Scan(&appUpdate, &appSelect, &appInsert, &readerAny); err != nil {
			t.Fatalf("has_column_privilege on %s: %v", col, err)
		}
		if want := col == "rechecked_at"; appUpdate != want {
			t.Errorf("invoice_app UPDATE on column %s = %v, want %v: only rechecked_at is writable", col, appUpdate, want)
		}
		if !appSelect || !appInsert {
			t.Errorf("invoice_app SELECT/INSERT on column %s = %v/%v, want true/true", col, appSelect, appInsert)
		}
		if readerAny {
			t.Errorf("invoice_tenant_reader holds a privilege on column %s, want none", col)
		}
	}
}

func TestRLS_CustomRuleRevisionsContentIsImmutable(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	rowA, _ := seedCRR(t, h.tenantA, nil, 1)

	for _, c := range []struct{ what, sql string }{
		{"updating message", `UPDATE custom_rule_revisions SET message = 'x' WHERE id = $1`},
		{"updating enabled", `UPDATE custom_rule_revisions SET enabled = false WHERE id = $1`},
		{"deleting the row", `DELETE FROM custom_rule_revisions WHERE id = $1`},
	} {
		csAssertPGCode(t, crrAsApp(h.tenantA, c.sql, rowA), "42501", "", "invoice_app "+c.what)
	}

	var msg string
	var enabled bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT message, enabled FROM custom_rule_revisions WHERE id = $1`, rowA).Scan(&msg, &enabled); err != nil {
		t.Fatalf("read the row back (a refused DELETE must leave it): %v", err)
	}
	if msg != "must be present" || !enabled {
		t.Errorf("row after the refused writes = (%q, %v), want (%q, true)", msg, enabled, "must be present")
	}
}

func TestRLS_CustomRuleRevisionsAppMarksRechecked(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	rowA, _ := seedCRR(t, h.tenantA, nil, 1)
	if v := crrReadRechecked(t, rowA); v != nil {
		t.Fatalf("seeded rechecked_at = %s, want NULL (pending)", *v)
	}

	update := func(tenant string) int64 {
		var n int64
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE custom_rule_revisions SET rechecked_at = now() WHERE id = $1`, rowA)
			n = tag.RowsAffected()
			return e
		})
		if err != nil {
			t.Fatalf("tenant %s marking %s rechecked: %v", tenant, rowA, err)
		}
		return n
	}

	if n := update(h.tenantB); n != 0 {
		t.Errorf("tenant B marking A's row: affected %d, want 0", n)
	}
	if v := crrReadRechecked(t, rowA); v != nil {
		t.Errorf("rechecked_at after tenant B's attempt = %s, want NULL", *v)
	}
	if n := update(h.tenantA); n != 1 {
		t.Errorf("tenant A marking its own pending row: affected %d, want 1", n)
	}
	if v := crrReadRechecked(t, rowA); v == nil {
		t.Error("rechecked_at after tenant A's update is NULL, want a timestamp")
	}
}

func TestRLS_CustomRuleRevisionsSeverityChecked(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	var probes []string
	crrInsertSeverity := func(sev string) (string, error) {
		id := uuid.NewString()
		probes = append(probes, id)
		return id, crrAsApp(h.tenantA, crrInsert, id, h.tenantA, uuid.NewString(), 1, nil, sev, "m")
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM custom_rule_revisions WHERE id = ANY($1)`, probes)
	})

	for _, sev := range []string{"info", ""} {
		id, err := crrInsertSeverity(sev)
		csAssertPGCode(t, err, "23514", "custom_rule_revisions_severity_check", "a row with severity "+sev)
		if n := crrCount(t, id); n != 0 {
			t.Errorf("rows after the refused severity %q = %d, want 0", sev, n)
		}
	}
	for _, sev := range []string{"error", "warning"} {
		id, err := crrInsertSeverity(sev)
		if err != nil {
			t.Fatalf("a row with severity %q: want success, got: %v", sev, err)
		}
		if n := crrCount(t, id); n != 1 {
			t.Errorf("rows after the accepted severity %q = %d, want 1", sev, n)
		}
	}
}

func TestRLS_CustomRuleRevisionsRevisionAndMessageChecked(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	var probes []string
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM custom_rule_revisions WHERE id = ANY($1)`, probes)
	})
	insert := func(revision int, message string) (string, error) {
		id := uuid.NewString()
		probes = append(probes, id)
		return id, crrAsApp(h.tenantA, crrInsert, id, h.tenantA, uuid.NewString(), revision, nil, "error", message)
	}

	id, err := insert(0, "m")
	csAssertPGCode(t, err, "23514", "custom_rule_revisions_revision_check", "a row with revision 0")
	if n := crrCount(t, id); n != 0 {
		t.Errorf("rows after the refused revision 0 = %d, want 0", n)
	}
	id, err = insert(1, "")
	csAssertPGCode(t, err, "23514", "custom_rule_revisions_message_check", "a row with an empty message")
	if n := crrCount(t, id); n != 0 {
		t.Errorf("rows after the refused empty message = %d, want 0", n)
	}

	// type and created_by carry the same non-empty check; crrInsert fixes both, so raw SQL here.
	for _, c := range []struct{ col, typ, by, check string }{
		{"type", "", "qa", "custom_rule_revisions_type_check"},
		{"created_by", "required", "", "custom_rule_revisions_created_by_check"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := crrAsApp(h.tenantA,
			`INSERT INTO custom_rule_revisions (id, tenant_id, rule_id, revision, type, target, severity, message, enabled, created_by)
			 VALUES ($1, $2, $3, 1, $4, 'invoice.number', 'error', 'm', true, $5)`,
			id, h.tenantA, uuid.NewString(), c.typ, c.by)
		csAssertPGCode(t, err, "23514", c.check, "a row with an empty "+c.col)
		if n := crrCount(t, id); n != 0 {
			t.Errorf("rows after the refused empty %s = %d, want 0", c.col, n)
		}
	}

	// Control: the same statement with revision 1 and a message lands.
	id, err = insert(1, "m")
	if err != nil {
		t.Fatalf("a row with revision 1 and a message: want success, got: %v", err)
	}
	if n := crrCount(t, id); n != 1 {
		t.Errorf("rows after the accepted insert = %d, want 1", n)
	}
}

func TestRLS_CustomRuleRevisionsUniqueRevisionPerRule(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)

	_, ruleID := seedCRR(t, h.tenantA, nil, 1)
	dupID, otherID := uuid.NewString(), uuid.NewString()
	crrCleanup(t, dupID, otherID)

	err := crrAsApp(h.tenantA, crrInsert, dupID, h.tenantA, ruleID, 1, nil, "error", "m")
	csAssertPGCode(t, err, "23505", "custom_rule_revisions_rule_revision_uq", "a repeated (tenant, rule_id, revision)")
	if n := crrCount(t, dupID); n != 0 {
		t.Errorf("rows after the refused duplicate = %d, want 0", n)
	}

	if err := crrAsApp(h.tenantB, crrInsert, otherID, h.tenantB, ruleID, 1, nil, "error", "m"); err != nil {
		t.Fatalf("the same rule_id and revision under tenant B: want success, got: %v", err)
	}
	if n := crrCount(t, otherID); n != 1 {
		t.Errorf("rows after tenant B's insert of the same rule_id = %d, want 1", n)
	}

	// A rule is its revisions: the next revision of the same rule_id must land, and so must
	// revision 1 of another rule in the same tenant.
	nextID, otherRuleID := uuid.NewString(), uuid.NewString()
	crrCleanup(t, nextID, otherRuleID)
	if err := crrAsApp(h.tenantA, crrInsert, otherRuleID, h.tenantA, uuid.NewString(), 1, nil, "error", "m"); err != nil {
		t.Fatalf("revision 1 of another rule_id under tenant A: want success, got: %v", err)
	}
	if err := crrAsApp(h.tenantA, crrInsert, nextID, h.tenantA, ruleID, 2, nil, "error", "m"); err != nil {
		t.Fatalf("revision 2 of the same rule_id: want success, got: %v", err)
	}
	if n := crrCount(t, nextID); n != 1 {
		t.Errorf("rows after revision 2 = %d, want 1", n)
	}
}

func TestRLS_CustomRuleRevisionsHistoryCauseColumn(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	var udt, nullable string
	var def *string
	err := h.super.QueryRow(ctx,
		`SELECT udt_name, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'invoice_status_history'
		    AND column_name = 'cause_custom_rule_revision_id'`).Scan(&udt, &nullable, &def)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("invoice_status_history.cause_custom_rule_revision_id does not exist")
	}
	if err != nil {
		t.Fatalf("read the cause column: %v", err)
	}
	if udt != "uuid" || nullable != "YES" || def != nil {
		t.Errorf("cause_custom_rule_revision_id = (%s, nullable %s, default %v), want (uuid, YES, none)", udt, nullable, def)
	}

	var refTable, refCol, col string
	var fks int
	rows, err := h.super.Query(ctx,
		`SELECT c.confrelid::regclass::text,
		        (SELECT string_agg(attname, ',') FROM pg_attribute WHERE attrelid = c.confrelid AND attnum = ANY(c.confkey)),
		        (SELECT string_agg(attname, ',') FROM pg_attribute WHERE attrelid = c.conrelid AND attnum = ANY(c.conkey))
		   FROM pg_constraint c
		  WHERE c.contype = 'f' AND c.conrelid = 'public.invoice_status_history'::regclass
		    AND c.confrelid = 'public.custom_rule_revisions'::regclass`)
	if err != nil {
		t.Fatalf("read the cause foreign key: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		fks++
		if e := rows.Scan(&refTable, &refCol, &col); e != nil {
			t.Fatalf("scan foreign key: %v", e)
		}
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate foreign keys: %v", e)
	}
	if fks != 1 || refTable != "custom_rule_revisions" || refCol != "id,tenant_id" || col != "cause_custom_rule_revision_id,tenant_id" {
		t.Errorf("foreign keys from invoice_status_history to custom_rule_revisions = %d (%s.%s <- %s), want 1 (custom_rule_revisions.id,tenant_id <- cause_custom_rule_revision_id,tenant_id)",
			fks, refTable, refCol, col)
	}

	// A row that names no cause reads NULL; the same insert with a real revision keeps it.
	entityID, cleanupEntity := seedBusinessEntity(t, h.tenantA, "crr-cause-entity")
	t.Cleanup(cleanupEntity)
	invoiceID, cleanupInvoice := seedInvoice(t, h.tenantA, entityID, "CRR-CAUSE-1")
	t.Cleanup(cleanupInvoice)
	revID, _ := seedCRR(t, h.tenantA, nil, 1)

	histID, cleanupHist := seedStatusHistory(t, h.tenantA, invoiceID)
	t.Cleanup(cleanupHist)
	var cause *string
	if e := h.super.QueryRow(ctx, `SELECT cause_custom_rule_revision_id::text FROM invoice_status_history WHERE id = $1`,
		histID).Scan(&cause); e != nil {
		t.Fatalf("read the cause of an existing history row: %v", e)
	}
	if cause != nil {
		t.Errorf("cause_custom_rule_revision_id of a row that named none = %s, want NULL", *cause)
	}

	insertHist := func(id, causeID string) error {
		_, e := h.super.Exec(ctx,
			`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor, cause_custom_rule_revision_id)
			 VALUES ($1, $2, $3, 'validated', 'draft', 'system', $4)`, id, h.tenantA, invoiceID, causeID)
		return e
	}
	okID, phantomID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = ANY($1)`, []string{okID, phantomID})
	})
	if e := insertHist(okID, revID); e != nil {
		t.Fatalf("a history row citing a real revision: want success, got: %v", e)
	}
	csAssertPGCode(t, insertHist(phantomID, uuid.NewString()), "23503", "", "a history row citing a revision that does not exist")
}

// The superuser bypasses RLS, so only the composite foreign keys can refuse a cross-tenant reference.
func TestRLS_CustomRuleRevisionsForeignEntityRefusedByCompositeFK(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	entityB, cleanup := seedBusinessEntity(t, h.tenantB, "crr-fk-entity-b")
	t.Cleanup(cleanup)

	id := uuid.NewString()
	crrCleanup(t, id)
	_, err := h.super.Exec(context.Background(), crrInsert,
		id, h.tenantA, uuid.NewString(), 1, entityB, "error", "must be present")
	csAssertPGCode(t, err, "23503", "", "tenant A revision naming tenant B's entity")
}

func TestRLS_CustomRuleRevisionsHistoryCitingForeignRevisionRefusedByCompositeFK(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()
	entityID, cleanupEntity := seedBusinessEntity(t, h.tenantA, "crr-fk-entity-a")
	t.Cleanup(cleanupEntity)
	invoiceID, cleanupInvoice := seedInvoice(t, h.tenantA, entityID, "CRR-FK-1")
	t.Cleanup(cleanupInvoice)
	revB, _ := seedCRR(t, h.tenantB, nil, 1)

	histID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoice_status_history WHERE id = $1`, histID)
	})
	_, err := h.super.Exec(ctx,
		`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor, cause_custom_rule_revision_id)
		 VALUES ($1, $2, $3, 'validated', 'draft', 'system', $4)`, histID, h.tenantA, invoiceID, revB)
	csAssertPGCode(t, err, "23503", "", "tenant A history row citing tenant B's revision")
}

// Purge runs under full FK enforcement: the revision must be deleted after the history row
// that cites it and before the entity it references.
func TestRLS_CustomRuleRevisionsPurgedWithForeignKeysEnforced(t *testing.T) {
	requireHarness(t)
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	crrRequireTable(t, pool)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	entityID, invoiceID, revID, histID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("cleanup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		execSetup(t, tx, `SET LOCAL session_replication_role = 'replica'`)
		execSetup(t, tx, `DELETE FROM invoice_status_history WHERE id = $1`, histID)
		execSetup(t, tx, `DELETE FROM custom_rule_revisions WHERE id = $1`, revID)
		execSetup(t, tx, `DELETE FROM invoices WHERE id = $1`, invoiceID)
		execSetup(t, tx, `DELETE FROM business_entities WHERE id = $1`, entityID)
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("cleanup: commit: %v", err)
		}
	})

	execSetup(t, pool, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'CRR purge witness')`,
		entityID, demoTenantID)
	execSetup(t, pool, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, 'CRR-PURGE-1')`,
		invoiceID, demoTenantID, entityID)
	execSetup(t, pool, crrInsert, revID, demoTenantID, uuid.NewString(), 1, entityID, "error", "m")
	execSetup(t, pool,
		`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor, cause_custom_rule_revision_id)
		 VALUES ($1, $2, $3, 'validated', 'draft', 'system', $4)`, histID, demoTenantID, invoiceID, revID)

	for _, tbl := range []string{"custom_rule_revisions", "invoice_status_history"} {
		if n := countFor(t, pool, tbl, db.DemoTenants); n == 0 {
			t.Fatalf("test setup: %s holds 0 demo-tenant rows before the purge", tbl)
		}
	}

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants with a revision and a history row citing it: %v", err)
	}
	for _, c := range []struct{ tbl, id string }{{"custom_rule_revisions", revID}, {"invoice_status_history", histID}} {
		if n := mustCount(t, pool, `SELECT count(*) FROM `+c.tbl+` WHERE id = $1`, c.id); n != 0 {
			t.Errorf("%s still holds the planted row after the purge, want 0", c.tbl)
		}
	}
}

// DownTo(0) in TestMigrateUpFromEmbedded drops every table with the rest, so it cannot see an
// over- or under-broad Down of this migration. The shipped Down runs in a rolled-back tx.
func TestRLS_CustomRuleRevisionsMigrationDownDropsExactlyItsObjects(t *testing.T) {
	h := requireHarness(t)
	crrRequireTable(t, h.super)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, "*_custom_rules.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS files matching *_custom_rules.sql = %v (err %v), want exactly 1", matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := auditEntitySectionOf(t, matches[0], "Down")
	baseline := auditEntitySectionOf(t, crrResolverBaseline, "Up")
	tx := migratorTx(t, ctx)

	strs := func(sql string) []string {
		t.Helper()
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if e := rows.Scan(&s); e != nil {
				t.Fatalf("scan %q: %v", sql, e)
			}
			out = append(out, s)
		}
		if e := rows.Err(); e != nil {
			t.Fatalf("iterate %q: %v", sql, e)
		}
		return out
	}
	tables := func() []string {
		return strs(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	}
	histCols := func() []string {
		return strs(`SELECT column_name FROM information_schema.columns
		              WHERE table_schema = 'public' AND table_name = 'invoice_status_history' ORDER BY column_name`)
	}
	fnDef := func() string {
		var d string
		if e := tx.QueryRow(ctx, `SELECT pg_get_functiondef('audit_log_entity_for(text,jsonb)'::regprocedure)`).Scan(&d); e != nil {
			t.Fatalf("read audit_log_entity_for: %v", e)
		}
		return d
	}
	exec := func(what, sql string) {
		t.Helper()
		if _, e := tx.Exec(ctx, sql); e != nil {
			t.Fatalf("%s: %v", what, e)
		}
	}
	without := func(in []string, drop string) []string {
		return slices.DeleteFunc(slices.Clone(in), func(s string) bool { return s == drop })
	}

	// Controls: the Up is applied, or every absence below would hold on an unmigrated DB.
	tablesBefore, colsBefore := tables(), histCols()
	if !slices.Contains(tablesBefore, "custom_rule_revisions") || !slices.Contains(colsBefore, "cause_custom_rule_revision_id") {
		t.Fatalf("before the Down: tables %v, invoice_status_history columns %v, want the new table and column", tablesBefore, colsBefore)
	}
	if !strings.Contains(fnDef(), "custom_rule.created") {
		t.Fatal("audit_log_entity_for does not name custom_rule.created before the Down: the Up did not replace the resolver")
	}

	exec("the shipped Down", down)

	if got := tables(); !reflect.DeepEqual(got, without(tablesBefore, "custom_rule_revisions")) {
		t.Errorf("tables after the Down = %v, want the earlier list minus custom_rule_revisions", got)
	}
	if got := histCols(); !reflect.DeepEqual(got, without(colsBefore, "cause_custom_rule_revision_id")) {
		t.Errorf("invoice_status_history columns after the Down = %v, want the earlier list minus the cause column", got)
	}
	if left := strs(`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'custom_rule_revisions'`); len(left) != 0 {
		t.Errorf("indexes left on custom_rule_revisions after the Down: %v", left)
	}

	downDef := fnDef()
	if strings.Contains(downDef, "custom_rule.") {
		t.Error("audit_log_entity_for still names custom_rule.* after the Down")
	}
	exec("the 20260903055829 Up body", baseline)
	if want := fnDef(); downDef != want {
		t.Errorf("audit_log_entity_for after the Down differs from the %s Up body:\n--- got\n%s\n--- want\n%s",
			crrResolverBaseline, downDef, want)
	}

	// Up then applies again.
	exec("re-applying the Up", up)
	if !slices.Contains(tables(), "custom_rule_revisions") || !slices.Contains(histCols(), "cause_custom_rule_revision_id") {
		t.Error("the Up did not recreate the table and the history column after the Down")
	}
	if !strings.Contains(fnDef(), "custom_rule.created") {
		t.Error("the Up did not restore the custom_rule branch of audit_log_entity_for")
	}
}
