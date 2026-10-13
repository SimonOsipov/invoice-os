// RLS, grant and constraint suite for `customers` and the eight NRS address columns on
// business_entities. Rows are seeded per test through h.super, never in harness.seed(), so a
// missing table fails only these cases. A rejected statement poisons its transaction, so
// each one runs in its own db.WithinTenantTx.
package db_test

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const csInsert = `INSERT INTO customers (id, tenant_id, tin) VALUES ($1, $2, $3)`

// requireCustomersTable makes a missing migration an assertion failure, not a driver error.
func requireCustomersTable(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT to_regclass('public.customers')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass('public.customers'): %v", err)
	}
	if reg == nil {
		t.Fatal("public.customers does not exist: the buyer_directory migration is not applied")
	}
}

// seedCustomer inserts one row as the superuser (BYPASSRLS) and returns its id.
func seedCustomer(t *testing.T, tenantID, tin string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(), csInsert, id, tenantID, tin); err != nil {
		t.Fatalf("seed customers (%s, %s): %v", tenantID, tin, err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, id)
	})
	return id
}

// csAsApp runs one statement as invoice_app under tenantID and returns its error.
func csAsApp(tenantID, sql string, args ...any) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, sql, args...)
		return e
	})
}

// csAssertPGCode names the constraint too: a CHECK renamed or widened trips a different one.
func csAssertPGCode(t *testing.T, err error, wantCode, wantConstraint, what string) {
	t.Helper()
	if got := pgCode(err); got != wantCode {
		t.Fatalf("%s returned SQLSTATE %q (%v), want %q", what, got, err, wantCode)
	}
	if wantConstraint == "" {
		return
	}
	if got := pgConstraint(err); got != wantConstraint {
		t.Errorf("%s tripped constraint %q, want %q", what, got, wantConstraint)
	}
}

func csRowCount(t *testing.T, id string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM customers WHERE id = $1`, id)
}

// csInsertCleanup deletes an app-inserted row by id once the test ends.
func csInsertCleanup(t *testing.T, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM customers WHERE id = ANY($1)`, ids)
	})
}

func TestRLS_CustomersTwoTenantsSameTINSeeOnlyTheirOwn(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	a1 := seedCustomer(t, h.tenantA, "12345678-0001")
	a2 := seedCustomer(t, h.tenantA, "RN-100")
	b1 := seedCustomer(t, h.tenantB, "12345678-0001")

	wantA := []string{a1, a2}
	sort.Strings(wantA)
	for _, c := range []struct {
		tenant string
		want   []string
	}{
		{h.tenantA, wantA},
		{h.tenantB, []string{b1}},
	} {
		err := db.WithinTenantTx(ctx, h.app, c.tenant, func(tx pgx.Tx) error {
			rows, e := tx.Query(ctx, `SELECT id::text FROM customers ORDER BY id`)
			if e != nil {
				return e
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					return e
				}
				got = append(got, id)
			}
			if e := rows.Err(); e != nil {
				return e
			}
			if len(got) == 0 {
				t.Errorf("tenant %s sees no customers, want %d", c.tenant, len(c.want))
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("tenant %s sees ids %v, want exactly %v", c.tenant, got, c.want)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("tenant %s read: %v", c.tenant, err)
		}
	}
}

func TestRLS_CustomersCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	csInsertCleanup(t, crossID, ownID)

	err := csAsApp(h.tenantA, csInsert, crossID, h.tenantB, "12345678-0001")
	csAssertPGCode(t, err, "42501", "", "an insert of tenant B's row scoped to tenant A")
	if n := csRowCount(t, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant insert = %d, want 0", n)
	}

	// Control: the same statement for the scoped tenant lands, so the refusal is RLS.
	if err := csAsApp(h.tenantA, csInsert, ownID, h.tenantA, "12345678-0001"); err != nil {
		t.Fatalf("own-tenant insert through the same statement: want success, got: %v", err)
	}
	if n := csRowCount(t, ownID); n != 1 {
		t.Errorf("rows after the own-tenant insert = %d, want 1", n)
	}
}

func TestRLS_CustomersOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	rowA := seedCustomer(t, h.tenantA, "12345678-0001")

	err := csAsApp(h.tenantA, `UPDATE customers SET tenant_id = $1 WHERE id = $2`, h.tenantB, rowA)
	csAssertPGCode(t, err, "42501", "", "reassigning a row's tenant_id to another tenant")

	var owner string
	if e := h.super.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM customers WHERE id = $1`, rowA).Scan(&owner); e != nil {
		t.Fatalf("read tenant_id for %s: %v", rowA, e)
	}
	if owner != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %s, want the seeded %s", owner, h.tenantA)
	}
}

// FORCE subjects the migrator (the table owner) to the policy: with no GUC it matches nothing.
func TestRLS_CustomersOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	id := uuid.NewString()
	okID := uuid.NewString()
	csInsertCleanup(t, id, okID)

	_, err := h.mig.Exec(ctx, csInsert, id, h.tenantA, "12345678-0001")
	csAssertPGCode(t, err, "42501", "", "the migrator inserting with no tenant GUC")
	if n := csRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused owner insert = %d, want 0", n)
	}

	// Control: scoped to the row's own tenant the migrator may insert, so the refusal above
	// is the forced policy and not a missing privilege.
	err = db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, csInsert, okID, h.tenantA, "12345678-0002")
		return e
	})
	if err != nil {
		t.Fatalf("the migrator inserting its own tenant's row under the GUC: want success, got: %v", err)
	}
	if n := csRowCount(t, okID); n != 1 {
		t.Errorf("rows after the scoped owner insert = %d, want 1", n)
	}
}

func TestRLS_CustomersMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	rowA := seedCustomer(t, h.tenantA, "12345678-0001")
	rowB := seedCustomer(t, h.tenantB, "12345678-0001")

	// Control: the zero below means nothing unless the rows are there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM customers WHERE id = ANY($1)`, []string{rowA, rowB}); n != 2 {
		t.Fatalf("superuser sees %d of the two seeded rows, want 2", n)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without a tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&n); err != nil {
		t.Fatalf("count with no tenant set: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant unset = %d, want 0", n)
	}
}

func TestRLS_CustomersForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.customers'::regclass`,
	).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class for customers: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("customers relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}

	type policy struct {
		roles []string
		cmd   string
		qual  string
	}
	got := map[string]policy{}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'customers'`)
	if err != nil {
		t.Fatalf("query pg_policies for customers: %v", err)
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
		t.Fatalf("policies on customers = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	p, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on customers; got %v", got)
	}
	if p.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want ALL", p.cmd)
	}
	if !reflect.DeepEqual(p.roles, []string{"public"}) {
		t.Errorf("tenant_isolation roles = %v, want [public]: a TO clause leaves unnamed roles unbound", p.roles)
	}
	if p.qual == "" {
		t.Error("tenant_isolation carries an empty USING; it would admit every row")
	}
}

func TestRLS_CustomersUniqueTINPerTenant(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	const tin = "12345678-0001"
	seedCustomer(t, h.tenantA, tin)

	dupID := uuid.NewString()
	otherID := uuid.NewString()
	csInsertCleanup(t, dupID, otherID)

	err := csAsApp(h.tenantA, csInsert, dupID, h.tenantA, tin)
	csAssertPGCode(t, err, "23505", "customers_tenant_tin_uq", "a repeated (tenant, tin)")
	if n := csRowCount(t, dupID); n != 0 {
		t.Errorf("rows after the refused duplicate = %d, want 0", n)
	}

	if err := csAsApp(h.tenantB, csInsert, otherID, h.tenantB, tin); err != nil {
		t.Fatalf("the same TIN under tenant B: want success, got: %v", err)
	}
	if n := csRowCount(t, otherID); n != 1 {
		t.Errorf("rows after tenant B's insert of the same TIN = %d, want 1", n)
	}
}

func TestRLS_CustomersTINShapeChecked(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	var probes []string
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM customers WHERE id = ANY($1)`, probes)
	})

	// Each shape breaks one anchor, quantifier or class of the pattern.
	for _, tin := range []string{
		"1234", " 12345678-0001", "rn-1",
		"12345678-0001x", "x12345678-0001", "12345678-0001\n",
		"123456789-0001", "1234567-0001", "12345678-00001", "12345678-000",
		"12345678_0001", "12345678-ABCD", "ABCDEFGH-0001", "١٢٣٤٥٦٧٨-٠٠٠١",
		"RN-", "RN-1x", "RN1", "xRN-1",
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := csAsApp(h.tenantA, csInsert, id, h.tenantA, tin)
		csAssertPGCode(t, err, "23514", "customers_tin_check", "a row with tin "+tin)
		if n := csRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused tin %q = %d, want 0", tin, n)
		}
	}

	for _, tin := range []string{"12345678-0001", "RN-123", "RN-0"} {
		id := uuid.NewString()
		probes = append(probes, id)
		if err := csAsApp(h.tenantA, csInsert, id, h.tenantA, tin); err != nil {
			t.Fatalf("a row with the well-formed tin %q: want success, got: %v", tin, err)
		}
		if n := csRowCount(t, id); n != 1 {
			t.Errorf("rows after the accepted tin %q = %d, want 1", tin, n)
		}
	}

	nullID := uuid.NewString()
	probes = append(probes, nullID)
	err := csAsApp(h.tenantA, csInsert, nullID, h.tenantA, nil)
	csAssertPGCode(t, err, "23502", "", "a row with a NULL tin")
	if got := earColumnName(err); got != "tin" {
		t.Errorf("the not-null violation names column %q, want tin", got)
	}
}

func TestRLS_CustomersBuyerTypeChecked(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	const insertType = `INSERT INTO customers (id, tenant_id, tin, buyer_type) VALUES ($1, $2, $3, $4)`
	var probes []string
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM customers WHERE id = ANY($1)`, probes)
	})

	for i, bt := range []string{"b2b", "B2X", "B2B ", ""} {
		id := uuid.NewString()
		probes = append(probes, id)
		tin := "12345678-010" + string(rune('0'+i))
		err := csAsApp(h.tenantA, insertType, id, h.tenantA, tin, bt)
		csAssertPGCode(t, err, "23514", "customers_buyer_type_check", "a row with buyer_type "+bt)
		if n := csRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused buyer_type %q = %d, want 0", bt, n)
		}
	}

	for i, bt := range []any{"B2B", "B2G", "B2C", nil} {
		id := uuid.NewString()
		probes = append(probes, id)
		tin := "12345678-020" + string(rune('0'+i))
		if err := csAsApp(h.tenantA, insertType, id, h.tenantA, tin, bt); err != nil {
			t.Fatalf("a row with buyer_type %v: want success, got: %v", bt, err)
		}
		if n := csRowCount(t, id); n != 1 {
			t.Errorf("rows after the accepted buyer_type %v = %d, want 1", bt, n)
		}
	}
}

func TestRLS_CustomersAppInsertsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	var id, edited string
	var createdSet bool
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO customers (tenant_id, tin) VALUES ($1, $2)
			 RETURNING id::text, edited_fields::text, created_at IS NOT NULL`,
			h.tenantA, "12345678-0001").Scan(&id, &edited, &createdSet)
	})
	if err != nil {
		t.Fatalf("invoice_app's own-tenant insert: want success, got: %v", err)
	}
	csInsertCleanup(t, id)

	if id == "" {
		t.Error("insert returned an empty id, want a generated uuid")
	}
	if edited != "{}" {
		t.Errorf("edited_fields = %s, want {}", edited)
	}
	if !createdSet {
		t.Error("created_at is NULL, want the now() default")
	}
}

func TestRLS_CustomersGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", true},
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
			`SELECT has_table_privilege($1, 'public.customers', $2)`, c.role, c.priv,
		).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%q, customers, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, customers, %q) = %v, want %v: the grant is exactly "+
				"SELECT, INSERT, UPDATE to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

func TestRLS_CustomersDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	rowA := seedCustomer(t, h.tenantA, "12345678-0001")

	err := csAsApp(h.tenantA, `DELETE FROM customers WHERE id = $1`, rowA)
	csAssertPGCode(t, err, "42501", "", "invoice_app deleting its own tenant's row")

	if n := csRowCount(t, rowA); n != 1 {
		t.Errorf("rows after the refused delete = %d, want 1", n)
	}
}

func TestRLS_BusinessEntitiesNRSColumnsExistNullableText(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	want := []string{"email", "telephone", "street", "city", "postal_zone", "country", "state", "lga"}
	rows, err := h.super.Query(ctx,
		`SELECT column_name, data_type, is_nullable, column_default
		   FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'business_entities' AND column_name = ANY($1)`, want)
	if err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name, dataType, nullable string
		var def *string
		if e := rows.Scan(&name, &dataType, &nullable, &def); e != nil {
			t.Fatalf("scan column row: %v", e)
		}
		found[name] = true
		if dataType != "text" {
			t.Errorf("business_entities.%s data_type = %q, want text", name, dataType)
		}
		if nullable != "YES" {
			t.Errorf("business_entities.%s is_nullable = %q, want YES", name, nullable)
		}
		if def != nil {
			t.Errorf("business_entities.%s column_default = %q, want NULL", name, *def)
		}
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate column rows: %v", e)
	}
	if len(found) != len(want) {
		t.Fatalf("business_entities carries %d of the %d NRS columns %v, want all", len(found), len(want), want)
	}
}

func TestRLS_CustomersColumnsTypesAndDefaults(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	type col struct {
		udt, nullable, def string
	}
	want := map[string]col{
		"id":             {"uuid", "NO", "gen_random_uuid()"},
		"tenant_id":      {"uuid", "NO", ""},
		"tin":            {"text", "NO", ""},
		"name":           {"text", "YES", ""},
		"contact_person": {"text", "YES", ""},
		"buyer_type":     {"text", "YES", ""},
		"email":          {"text", "YES", ""},
		"telephone":      {"text", "YES", ""},
		"street":         {"text", "YES", ""},
		"city":           {"text", "YES", ""},
		"postal_zone":    {"text", "YES", ""},
		"country":        {"text", "YES", ""},
		"state":          {"text", "YES", ""},
		"lga":            {"text", "YES", ""},
		"edited_fields":  {"_text", "NO", "'{}'::text[]"},
		"created_at":     {"timestamptz", "NO", "now()"},
	}
	rows, err := h.super.Query(context.Background(),
		`SELECT column_name, udt_name, is_nullable, coalesce(column_default, '')
		   FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'customers'`)
	if err != nil {
		t.Fatalf("query information_schema.columns for customers: %v", err)
	}
	defer rows.Close()
	got := map[string]col{}
	for rows.Next() {
		var name string
		var c col
		if e := rows.Scan(&name, &c.udt, &c.nullable, &c.def); e != nil {
			t.Fatalf("scan column row: %v", e)
		}
		got[name] = c
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate column rows: %v", e)
	}
	if len(got) == 0 {
		t.Fatal("customers has no columns")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("customers columns (type, nullable, default) = %v, want %v", got, want)
	}
}

func TestRLS_CustomersTenantForeignKeyAndCascade(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	orphanID := uuid.NewString()
	csInsertCleanup(t, orphanID)
	_, err := h.super.Exec(ctx, csInsert, orphanID, uuid.NewString(), "12345678-0001")
	csAssertPGCode(t, err, "23503", "customers_tenant_id_fkey", "a customer for a tenant that does not exist")

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'customers cascade probe')`, tenantID); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID) })
	custID := seedCustomer(t, tenantID, "12345678-0001")
	if n := csRowCount(t, custID); n != 1 {
		t.Fatalf("seeded customers before the tenant delete = %d, want 1", n)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete the throwaway tenant: %v", err)
	}
	if n := csRowCount(t, custID); n != 0 {
		t.Errorf("customers left after their tenant was deleted = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

func TestRLS_CustomersAppUpdatesOnlyItsOwnRows(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	rowA := seedCustomer(t, h.tenantA, "12345678-0001")
	rowB := seedCustomer(t, h.tenantB, "12345678-0001")

	update := func(tenant, id string) (int64, error) {
		var n int64
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE customers SET name = 'edited' WHERE id = $1`, id)
			n = tag.RowsAffected()
			return e
		})
		return n, err
	}
	name := func(id string) *string {
		var v *string
		if e := h.super.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1`, id).Scan(&v); e != nil {
			t.Fatalf("read name for %s: %v", id, e)
		}
		return v
	}

	n, err := update(h.tenantA, rowA)
	if err != nil || n != 1 {
		t.Fatalf("tenant A updating its own row: affected %d, err %v, want 1 and nil", n, err)
	}
	if v := name(rowA); v == nil || *v != "edited" {
		t.Errorf("name of A's row after its own update = %v, want edited", v)
	}

	n, err = update(h.tenantA, rowB)
	if err != nil || n != 0 {
		t.Errorf("tenant A updating B's row: affected %d, err %v, want 0 and nil", n, err)
	}
	if v := name(rowB); v != nil {
		t.Errorf("name of B's row after A's update attempt = %q, want NULL", *v)
	}

	err = csAsApp(h.tenantA, `UPDATE customers SET buyer_type = 'B2X' WHERE id = $1`, rowA)
	csAssertPGCode(t, err, "23514", "customers_buyer_type_check", "an update to an invalid buyer_type")
}

func TestRLS_CustomersEditedFieldsAndCreatedAtNotNull(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)

	for _, col := range []string{"edited_fields", "created_at"} {
		id := uuid.NewString()
		csInsertCleanup(t, id)
		err := csAsApp(h.tenantA,
			`INSERT INTO customers (id, tenant_id, tin, `+col+`) VALUES ($1, $2, '12345678-0001', NULL)`, id, h.tenantA)
		csAssertPGCode(t, err, "23502", "", "a row with NULL "+col)
		if got := earColumnName(err); got != col {
			t.Errorf("the not-null violation names column %q, want %s", got, col)
		}
	}
}

// DownTo(0) in TestMigrateUpFromEmbedded drops business_entities with the rest, so it cannot
// see an over- or under-broad Down of this migration. ApplyVersion rolls back this one file.
func TestRLS_CustomersMigrationDownDropsExactlyItsObjects(t *testing.T) {
	h := requireHarness(t)
	requireCustomersTable(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, "*_buyer_directory.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS files matching *_buyer_directory.sql = %v (err %v), want exactly 1", matches, err)
	}
	version, err := strconv.ParseInt(strings.SplitN(matches[0], "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parse goose version out of %q: %v", matches[0], err)
	}

	sqlDB, err := sql.Open("pgx", os.Getenv("DATABASE_MIGRATION_URL"))
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore the buyer_directory schema after the Down/Up round-trip: %v", err)
		}
	})

	nrsCols := []string{"email", "telephone", "street", "city", "postal_zone", "country", "state", "lga"}
	state := func() (customers bool, nrs, other int) {
		if e := h.super.QueryRow(ctx, `SELECT to_regclass('public.customers') IS NOT NULL`).Scan(&customers); e != nil {
			t.Fatalf("read customers presence: %v", e)
		}
		if e := h.super.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE column_name = ANY($1)), count(*) FILTER (WHERE NOT column_name = ANY($1))
			   FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'business_entities'`,
			nrsCols).Scan(&nrs, &other); e != nil {
			t.Fatalf("count business_entities columns: %v", e)
		}
		return
	}

	has, nrs, otherBefore := state()
	if !has || nrs != len(nrsCols) || otherBefore == 0 {
		t.Fatalf("before Down: customers %v, NRS columns %d, other columns %d, want true, %d, >0", has, nrs, otherBefore, len(nrsCols))
	}
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the buyer_directory migration: %v", err)
	}
	has, nrs, other := state()
	if has || nrs != 0 || other != otherBefore {
		t.Errorf("after Down: customers %v, NRS columns %d, other columns %d, want false, 0, %d", has, nrs, other, otherBefore)
	}
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the buyer_directory migration: %v", err)
	}
	has, nrs, other = state()
	if !has || nrs != len(nrsCols) || other != otherBefore {
		t.Errorf("after re-Up: customers %v, NRS columns %d, other columns %d, want true, %d, %d", has, nrs, other, len(nrsCols), otherBefore)
	}
}
