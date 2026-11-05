// Schema tests for APSC-01's migration: per-policy active index, version scope columns,
// policy order position. Run: `make test-rls DEV_DB_PORT=5467`.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	scopesMigrationGlob = "*_approval_policy_scopes.sql"
	sealGuardMigration  = "20260809210326_approval_policies.sql"
	versionsOneActive   = "approval_policy_versions_one_active"
)

// scTenant seeds a fresh firm tenant, so position and active-slot counts start from zero.
func scTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, "APSC-01 "+id); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

// scInsertPolicy inserts a policy with no position, as invoice_app under tenantID or as the
// superuser, and returns its id and the position it reads back with.
func scInsertPolicy(t *testing.T, app bool, tenantID string) (id string, position int) {
	t.Helper()
	const q = `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id, position`
	name := apName("pos")
	var err error
	if app {
		err = db.WithinTenantTx(context.Background(), h.app, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), q, tenantID, name).Scan(&id, &position)
		})
	} else {
		err = h.super.QueryRow(context.Background(), q, tenantID, name).Scan(&id, &position)
	}
	if err != nil {
		t.Fatalf("insert policy with no position (app=%v): %v", app, err)
	}
	return id, position
}

type scopeRow struct {
	Types    []string
	Entities []string
	Roles    []string
	Lower    *string
	Upper    *string
	Currency *string
}

func strp(s string) *string { return &s }

// scInsertVersion inserts a draft version for a fresh policy as invoice_app and reads the scopes
// back. Zero-value slices and nil band columns are written as given, not omitted.
func scInsertVersion(t *testing.T, tenantID string, in scopeRow) (scopeRow, error) {
	t.Helper()
	ctx := context.Background()
	policyID, cleanup := seedApprovalPolicy(t, tenantID, apName("scope-policy"))
	defer cleanup()

	var out scopeRow
	err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO approval_policy_versions
			   (tenant_id, policy_id, version, scope_document_types, scope_entity_ids,
			    scope_workflow_role_ids, scope_amount_lower, scope_amount_upper, scope_amount_currency)
			 VALUES ($1, $2, 1, $3::text[], $4::text[]::uuid[], $5::text[]::uuid[],
			         $6::text::numeric, $7::text::numeric, $8)
			 RETURNING scope_document_types, scope_entity_ids::text[], scope_workflow_role_ids::text[],
			           scope_amount_lower::text, scope_amount_upper::text, scope_amount_currency`,
			tenantID, policyID, nonNil(in.Types), nonNil(in.Entities), nonNil(in.Roles),
			in.Lower, in.Upper, in.Currency,
		).Scan(&out.Types, &out.Entities, &out.Roles, &out.Lower, &out.Upper, &out.Currency)
	})
	return out, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// AC 2: each scope is optional and any combination is one row.
func TestRLS_ApprovalPolicyVersionsScopesAcceptEveryCombination(t *testing.T) {
	requireHarness(t)
	tenantID := scTenant(t)
	e1, e2, r1 := uuid.NewString(), uuid.NewString(), uuid.NewString()

	cases := []struct {
		name string
		in   scopeRow
	}{
		{"document types", scopeRow{Types: []string{"commercial_invoice", "credit_note"}}},
		{"entities", scopeRow{Entities: []string{e1, e2}}},
		{"roles", scopeRow{Roles: []string{r1}}},
		{"band", scopeRow{Lower: strp("5.00"), Upper: strp("50.00"), Currency: strp("NGN")}},
		{"lower only", scopeRow{Lower: strp("5.00"), Currency: strp("NGN")}},
		{"upper only", scopeRow{Upper: strp("5.00"), Currency: strp("USD")}},
		{"all four", scopeRow{Types: []string{"debit_note"}, Entities: []string{e1}, Roles: []string{r1},
			Lower: strp("1.00"), Upper: strp("2.00"), Currency: strp("NGN")}},
		{"document types and band", scopeRow{Types: []string{"credit_note", "debit_note"},
			Lower: strp("0.01"), Upper: strp("9999999999.99"), Currency: strp("NGN")}},
		{"none", scopeRow{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := scInsertVersion(t, tenantID, c.in)
			if err != nil {
				t.Fatalf("insert version with %s: %v, want success", c.name, err)
			}
			want := c.in
			want.Types, want.Entities, want.Roles = nonNil(want.Types), nonNil(want.Entities), nonNil(want.Roles)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read back %+v, want %+v", got, want)
			}
		})
	}
}

// AC 3: the CHECK refuses an unknown document type and a NULL element.
func TestRLS_ApprovalPolicyVersionsScopeDocumentTypesCheckRejects(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	const check = "approval_policy_versions_scope_document_types_check"

	policyID, cleanup := seedApprovalPolicy(t, tenantID, apName("doc-types"))
	defer cleanup()
	for _, c := range []struct{ name, expr string }{
		{"unknown type", `ARRAY['invoice']::text[]`},
		{"NULL element", `ARRAY[NULL]::text[]`},
		{"one valid and one unknown", `ARRAY['credit_note', 'receipt']::text[]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx,
					`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, scope_document_types)
					 VALUES ($1, $2, 1, `+c.expr+`)`, tenantID, policyID)
				return e
			})
			if err == nil {
				t.Fatalf("insert with %s succeeded, want 23514 on %s", c.name, check)
			}
			if code := pgCode(err); code != "23514" {
				t.Fatalf("insert with %s: SQLSTATE = %q, want 23514: %v", c.name, code, err)
			}
			if got := pgConstraint(err); got != check {
				t.Errorf("constraint = %q, want %q", got, check)
			}
		})
	}

	got, err := scInsertVersion(t, tenantID, scopeRow{Types: []string{"commercial_invoice", "credit_note", "debit_note"}})
	if err != nil {
		t.Fatalf("control: all three valid types: %v, want success", err)
	}
	if len(got.Types) != 3 {
		t.Errorf("control: read back %v, want the three types", got.Types)
	}
}

// AC 4: a band names its currency and at least one bound.
func TestRLS_ApprovalPolicyVersionsScopeAmountNeedsCurrency(t *testing.T) {
	requireHarness(t)
	tenantID := scTenant(t)
	const check = "approval_policy_versions_scope_amount_currency_check"

	for _, c := range []struct {
		name string
		in   scopeRow
	}{
		{"lower without currency", scopeRow{Lower: strp("5.00")}},
		{"upper without currency", scopeRow{Upper: strp("5.00")}},
		{"currency without bounds", scopeRow{Currency: strp("NGN")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := scInsertVersion(t, tenantID, c.in)
			if err == nil {
				t.Fatalf("insert %s succeeded, want 23514 on %s", c.name, check)
			}
			if code := pgCode(err); code != "23514" {
				t.Fatalf("insert %s: SQLSTATE = %q, want 23514: %v", c.name, code, err)
			}
			if got := pgConstraint(err); got != check {
				t.Errorf("constraint = %q, want %q", got, check)
			}
		})
	}

	for _, c := range []struct {
		name string
		in   scopeRow
	}{
		{"lower only with currency", scopeRow{Lower: strp("5.00"), Currency: strp("NGN")}},
		{"upper only with currency", scopeRow{Upper: strp("5.00"), Currency: strp("NGN")}},
	} {
		if _, err := scInsertVersion(t, tenantID, c.in); err != nil {
			t.Errorf("control %s: %v, want success", c.name, err)
		}
	}
}

// AC 4: lower above upper is refused; equal bounds are accepted.
func TestRLS_ApprovalPolicyVersionsScopeAmountLowerAboveUpperRefused(t *testing.T) {
	requireHarness(t)
	tenantID := scTenant(t)
	const check = "approval_policy_versions_scope_amount_order_check"

	_, err := scInsertVersion(t, tenantID, scopeRow{Lower: strp("50000000.00"), Upper: strp("5000000.00"), Currency: strp("NGN")})
	if err == nil {
		t.Fatalf("lower above upper succeeded, want 23514 on %s", check)
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("lower above upper: SQLSTATE = %q, want 23514: %v", code, err)
	}
	if got := pgConstraint(err); got != check {
		t.Errorf("constraint = %q, want %q", got, check)
	}

	if _, err := scInsertVersion(t, tenantID, scopeRow{Lower: strp("5000000.00"), Upper: strp("5000000.00"), Currency: strp("NGN")}); err != nil {
		t.Errorf("boundary lower = upper: %v, want success", err)
	}
}

// AC 5: the band has the type of invoices.total, ids are uuid[].
func TestRLS_ApprovalPolicyVersionsScopeColumnTypes(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	typeOf := func(table, col string) (typ string, notNull, found bool) {
		err := h.super.QueryRow(ctx,
			`SELECT format_type(a.atttypid, a.atttypmod), a.attnotnull
			   FROM pg_attribute a
			  WHERE a.attrelid = to_regclass('public.' || $1) AND a.attname = $2 AND NOT a.attisdropped`,
			table, col).Scan(&typ, &notNull)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, false
		}
		if err != nil {
			t.Fatalf("read type of %s.%s: %v", table, col, err)
		}
		return typ, notNull, true
	}

	total, _, ok := typeOf("invoices", "total")
	if !ok || total != "numeric(14,2)" {
		t.Fatalf("invoices.total type = %q (found %v), want numeric(14,2)", total, ok)
	}
	for _, c := range []struct {
		col     string
		want    string
		notNull bool
	}{
		{"scope_document_types", "text[]", true},
		{"scope_entity_ids", "uuid[]", true},
		{"scope_workflow_role_ids", "uuid[]", true},
		{"scope_amount_lower", total, false},
		{"scope_amount_upper", total, false},
		{"scope_amount_currency", "text", false},
	} {
		got, notNull, found := typeOf("approval_policy_versions", c.col)
		if !found {
			t.Errorf("approval_policy_versions.%s does not exist", c.col)
			continue
		}
		if got != c.want || notNull != c.notNull {
			t.Errorf("approval_policy_versions.%s = (%s, not null %v), want (%s, not null %v)",
				c.col, got, notNull, c.want, c.notNull)
		}
	}
}

// AC 6: the trigger numbers a tenant's policies 1.. and counts soft-deleted rows.
func TestRLS_ApprovalPoliciesPositionAssignedPerTenant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a, b := scTenant(t), scTenant(t)

	_, p1 := scInsertPolicy(t, true, a)
	second, p2 := scInsertPolicy(t, true, a)
	if err := db.WithinTenantTx(ctx, h.app, a, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, second)
		return e
	}); err != nil {
		t.Fatalf("soft-delete the second policy: %v", err)
	}
	_, p3 := scInsertPolicy(t, true, a)
	_, pb := scInsertPolicy(t, true, b)

	if got := []int{p1, p2, p3}; !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("tenant A positions = %v, want [1 2 3] (a soft-deleted row still counts)", got)
	}
	if pb != 1 {
		t.Errorf("tenant B first position = %d, want 1 (positions restart per tenant)", pb)
	}
}

// AC 6: a BYPASSRLS insert sees every tenant's rows, so the trigger must filter by tenant itself.
func TestRLS_ApprovalPoliciesPositionAssignedPerTenantAsSuperuser(t *testing.T) {
	requireHarness(t)
	a, b := scTenant(t), scTenant(t)

	for range 3 {
		scInsertPolicy(t, false, b)
	}
	if _, p := scInsertPolicy(t, false, a); p != 1 {
		t.Fatalf("tenant A first position = %d, want 1", p)
	}
	if _, p := scInsertPolicy(t, false, a); p != 2 {
		t.Errorf("tenant A second position as superuser = %d, want 2 (not 4: tenant B holds 1..3)", p)
	}
	if _, p := scInsertPolicy(t, true, a); p != 3 {
		t.Errorf("tenant A third position as invoice_app = %d, want 3", p)
	}
}

// AC 6: two concurrent inserts in one tenant get distinct positions and neither fails.
func TestRLS_ApprovalPoliciesPositionDistinctUnderConcurrentInserts(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	const q = `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING position`

	begin := func() (pgx.Tx, int) {
		tx, err := h.app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("read pid: %v", err)
		}
		return tx, pid
	}

	tx1, pid1 := begin()
	tx2, _ := begin()
	var pos1 int
	if err := tx1.QueryRow(ctx, q, tenantID, apName("c1")).Scan(&pos1); err != nil {
		t.Fatalf("tx1 insert: %v", err)
	}

	type result struct {
		pos int
		err error
	}
	done := make(chan result, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var r result
		r.err = tx2.QueryRow(ctx, q, tenantID, apName("c2")).Scan(&r.pos)
		done <- r
	}()
	defer wg.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		if err := h.super.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid1).Scan(&blocked); err != nil {
			t.Fatalf("read blocked backends: %v", err)
		}
		if blocked > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("tx2 insert returned (pos %d, err %v) while tx1 held its insert uncommitted, want it blocked", r.pos, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("tx2 never blocked behind tx1")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("tx2 insert after tx1 committed: %v (SQLSTATE %s), want success", r.err, pgCode(r.err))
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}
	if got := []int{pos1, r.pos}; !slices.Equal(got, []int{1, 2}) {
		t.Errorf("positions = %v, want [1 2]", got)
	}
}

// AC 6: position is NOT NULL.
func TestRLS_ApprovalPoliciesPositionCannotBeCleared(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)

	id, pos := scInsertPolicy(t, true, tenantID)
	if pos != 1 {
		t.Fatalf("fixture position = %d, want 1", pos)
	}
	err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE approval_policies SET position = NULL WHERE id = $1`, id)
		return e
	})
	if err == nil {
		t.Fatal("UPDATE position = NULL succeeded, want 23502")
	}
	if code := pgCode(err); code != "23502" {
		t.Errorf("UPDATE position = NULL: SQLSTATE = %q, want 23502: %v", code, err)
	}
}

// AC 7: live policies cannot share a position; a soft-deleted policy's position is free.
func TestRLS_ApprovalPoliciesPositionUniqueAmongLive(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	const excl = "approval_policies_live_position_excl"

	scInsertPolicy(t, false, tenantID)
	second, _ := scInsertPolicy(t, false, tenantID)

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policies (tenant_id, name, position) VALUES ($1, $2, 2)`, tenantID, apName("dup"))
	if err == nil {
		t.Fatalf("insert at a live position succeeded, want 23P01 on %s", excl)
	}
	if code := pgCode(err); code != "23P01" {
		t.Fatalf("insert at a live position: SQLSTATE = %q, want 23P01: %v", code, err)
	}
	if got := pgConstraint(err); got != excl {
		t.Errorf("constraint = %q, want %q", got, excl)
	}

	if _, err := h.super.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, second); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_policies (tenant_id, name, position) VALUES ($1, $2, 2)`, tenantID, apName("reuse")); err != nil {
		t.Errorf("control: insert at a soft-deleted policy's position: %v, want success", err)
	}
}

// AC 7: the constraint is deferrable, so a reorder swaps in one statement.
func TestRLS_ApprovalPoliciesPositionSwapInOneStatement(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)

	first, _ := scInsertPolicy(t, false, tenantID)
	second, _ := scInsertPolicy(t, false, tenantID)

	if _, err := h.super.Exec(ctx,
		`UPDATE approval_policies SET position = 3 - position WHERE tenant_id = $1 AND deleted_at IS NULL`, tenantID); err != nil {
		t.Fatalf("swap positions in one UPDATE: %v, want success", err)
	}
	read := func(id string) (p int) {
		if err := h.super.QueryRow(ctx, `SELECT position FROM approval_policies WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatalf("read position: %v", err)
		}
		return p
	}
	if got := []int{read(first), read(second)}; !slices.Equal(got, []int{2, 1}) {
		t.Errorf("positions after the swap = %v, want [2 1]", got)
	}
}

// --- migration body -----------------------------------------------------------------

func scopesMigrationSections(t *testing.T) (up, down string) {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, scopesMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", scopesMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1", len(matches), scopesMigrationGlob, matches)
	}
	return auditEntitySectionOf(t, matches[0], "Up"), auditEntitySectionOf(t, matches[0], "Down")
}

// scMigrationTx opens the rolled-back migrator tx, fails unless the migration is applied to the
// database (the tests below Down it first), and leaves every active version inactive so the Down
// guard counts only what the test seeds.
func scMigrationTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx := migratorTx(t, ctx)
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'approval_policies' AND column_name = 'position'`).Scan(&n); err != nil {
		t.Fatalf("read approval_policies.position presence: %v", err)
	}
	if n != 1 {
		t.Fatalf("approval_policies.position does not exist: the migration is not applied to this database")
	}
	scSetTenant(t, ctx, tx, "")
	for _, stmt := range []string{
		`ALTER TABLE approval_policy_versions NO FORCE ROW LEVEL SECURITY`,
		`UPDATE approval_policy_versions SET is_active = false WHERE is_active`,
		`ALTER TABLE approval_policy_versions FORCE ROW LEVEL SECURITY`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return tx
}

func scSetTenant(t *testing.T, ctx context.Context, tx pgx.Tx, tenantID string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("set app.current_tenant: %v", err)
	}
}

// scSealedActiveVersion inserts a sealed active version 1 for policyID.
func scSealedActiveVersion(t *testing.T, ctx context.Context, tx pgx.Tx, tenantID, policyID string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, sealed, is_active)
		 VALUES ($1, $2, 1, true, true) RETURNING id`, tenantID, policyID).Scan(&id); err != nil {
		t.Fatalf("seed sealed active version: %v", err)
	}
	return id
}

// AC 8: the Up numbers existing policies by created_at, id; gives every version the catch-all
// scopes; and changes no is_active.
func TestRLS_ApprovalPolicyScopesMigrationBackfillsOrderAndKeepsTheActiveVersion(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	tx := scMigrationTx(t, ctx)
	up, down := scopesMigrationSections(t)

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down section: %v", err)
	}

	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	slices.Sort(ids)
	// pc has the smallest id but the latest created_at: created_at must win. pd is soft-deleted.
	pc, pa, pb, pd := ids[0], ids[1], ids[2], uuid.NewString()
	scSetTenant(t, ctx, tx, tenantID)
	for _, p := range []struct{ id, name, created string }{
		{pd, "pd", "2026-01-01T00:00:00Z"},
		{pa, "pa", "2026-02-01T00:00:00Z"},
		{pb, "pb", "2026-02-01T00:00:00Z"},
		{pc, "pc", "2026-03-01T00:00:00Z"},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO approval_policies (id, tenant_id, name, created_at) VALUES ($1, $2, $3, $4::timestamptz)`,
			p.id, tenantID, p.name, p.created); err != nil {
			t.Fatalf("seed policy %s: %v", p.name, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, pd); err != nil {
		t.Fatalf("soft-delete pd: %v", err)
	}
	activeVersion := scSealedActiveVersion(t, ctx, tx, tenantID, pa)
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1)`, tenantID, pb); err != nil {
		t.Fatalf("seed draft version: %v", err)
	}

	scSetTenant(t, ctx, tx, "")
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up section with a sealed version present and no tenant GUC: %v", err)
	}

	scSetTenant(t, ctx, tx, tenantID)
	rows, err := tx.Query(ctx, `SELECT name, position FROM approval_policies ORDER BY position`)
	if err != nil {
		t.Fatalf("read positions: %v", err)
	}
	var order []string
	var positions []int
	for rows.Next() {
		var name string
		var pos int
		if err := rows.Scan(&name, &pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		order, positions = append(order, name), append(positions, pos)
	}
	rows.Close()
	if !slices.Equal(order, []string{"pd", "pa", "pb", "pc"}) || !slices.Equal(positions, []int{1, 2, 3, 4}) {
		t.Errorf("policies by position = %v at %v, want [pd pa pb pc] at [1 2 3 4] (created_at, id)", order, positions)
	}

	var versions, catchAll int
	if err := tx.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE scope_document_types = '{}' AND scope_entity_ids = '{}'
		                           AND scope_workflow_role_ids = '{}' AND scope_amount_lower IS NULL
		                           AND scope_amount_upper IS NULL AND scope_amount_currency IS NULL)
		   FROM approval_policy_versions`).Scan(&versions, &catchAll); err != nil {
		t.Fatalf("read version scopes: %v", err)
	}
	if versions != 2 || catchAll != 2 {
		t.Errorf("versions = %d, catch-all versions = %d, want 2 and 2 (sealed and draft)", versions, catchAll)
	}

	var active []string
	arows, err := tx.Query(ctx, `SELECT id::text FROM approval_policy_versions WHERE is_active`)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	for arows.Next() {
		var id string
		if err := arows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		active = append(active, id)
	}
	arows.Close()
	if !slices.Equal(active, []string{activeVersion}) {
		t.Errorf("active versions = %v, want exactly [%s]", active, activeVersion)
	}
}

// AC 9: the Down refuses a workspace the old index cannot hold, with 55000 and not 23505.
func TestRLS_ApprovalPolicyScopesMigrationDownRefusesTwoActivePolicies(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	tx := scMigrationTx(t, ctx)
	_, down := scopesMigrationSections(t)

	scSetTenant(t, ctx, tx, tenantID)
	for _, name := range []string{"first", "second"} {
		var policyID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenantID, name).Scan(&policyID); err != nil {
			t.Fatalf("seed policy %s: %v", name, err)
		}
		scSealedActiveVersion(t, ctx, tx, tenantID, policyID)
	}
	scSetTenant(t, ctx, tx, "")

	spt, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, downErr := spt.Exec(ctx, down)
	_ = spt.Rollback(ctx)
	if downErr == nil {
		t.Fatal("Down succeeded with two active policies in one workspace, want SQLSTATE 55000")
	}
	if code := pgCode(downErr); code != "55000" {
		t.Fatalf("Down: SQLSTATE = %q, want 55000 (not 23505 from the index build): %v", code, downErr)
	}
	msg := pgMessage(downErr)
	if !strings.Contains(msg, "more than one active policy") || !strings.HasPrefix(msg, "1 workspace(s)") {
		t.Errorf("Down message = %q, want it to name the count (1 workspace(s)) and \"more than one active policy\"", msg)
	}
}

func scSealGuardOriginalBody(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(migrations.FS, sealGuardMigration)
	if err != nil {
		t.Fatalf("read %s: %v", sealGuardMigration, err)
	}
	src := string(b)
	i := strings.Index(src, "CREATE OR REPLACE FUNCTION approval_policy_versions_seal_guard")
	if i < 0 {
		t.Fatalf("%s: no seal guard definition", sealGuardMigration)
	}
	start := strings.Index(src[i:], "$$")
	end := strings.Index(src[i+start+2:], "$$")
	if start < 0 || end < 0 {
		t.Fatalf("%s: seal guard body has no $$ quotes", sealGuardMigration)
	}
	return src[i+start+2 : i+start+2+end]
}

// AC 9: with one active version per workspace the Down restores the old shape and the Up applies again.
func TestRLS_ApprovalPolicyScopesMigrationDownRestoresTheOldShape(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenantID := scTenant(t)
	tx := scMigrationTx(t, ctx)
	up, down := scopesMigrationSections(t)
	wantBody := scSealGuardOriginalBody(t)

	scSetTenant(t, ctx, tx, tenantID)
	var policyID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, 'only') RETURNING id`, tenantID).Scan(&policyID); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	scSealedActiveVersion(t, ctx, tx, tenantID, policyID)
	scSetTenant(t, ctx, tx, "")

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	indexDef := func() (d string) {
		t.Helper()
		if err := tx.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`, versionsOneActive).Scan(&d); err != nil {
			t.Fatalf("read indexdef: %v", err)
		}
		return d
	}
	guardBody := func() (b string) {
		t.Helper()
		if err := tx.QueryRow(ctx,
			`SELECT prosrc FROM pg_proc WHERE proname = 'approval_policy_versions_seal_guard'`).Scan(&b); err != nil {
			t.Fatalf("read seal guard body: %v", err)
		}
		return b
	}

	if !strings.Contains(indexDef(), "(tenant_id, policy_id) WHERE is_active") {
		t.Fatalf("before the Down, %s = %q, want it keyed (tenant_id, policy_id)", versionsOneActive, indexDef())
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down section with one active version: %v, want success", err)
	}

	if d := indexDef(); !strings.Contains(d, "(tenant_id) WHERE is_active") {
		t.Errorf("after the Down, %s = %q, want (tenant_id) WHERE is_active", versionsOneActive, d)
	}
	if n := count(`SELECT count(*) FROM pg_constraint WHERE conname = 'approval_policies_scope_check'`); n != 1 {
		t.Errorf("approval_policies_scope_check rows after the Down = %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
	                 AND ((table_name = 'approval_policies' AND column_name = 'position')
	                   OR (table_name = 'approval_policy_versions' AND column_name LIKE 'scope\_%'))`); n != 0 {
		t.Errorf("new columns after the Down = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM pg_constraint WHERE conname LIKE 'approval\_policy\_versions\_scope\_%'
	                  OR conname = 'approval_policies_live_position_excl'`); n != 0 {
		t.Errorf("new constraints after the Down = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM pg_trigger WHERE tgname = 'approval_policies_assign_position'`); n != 0 {
		t.Errorf("position trigger after the Down = %d, want 0", n)
	}
	if n := count(`SELECT count(*) FROM pg_proc WHERE proname = 'approval_policies_assign_position'`); n != 0 {
		t.Errorf("position function after the Down = %d, want 0", n)
	}
	if got := guardBody(); got != wantBody {
		t.Errorf("seal guard body after the Down differs from %s:\n got: %q\nwant: %q", sealGuardMigration, got, wantBody)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up section after its own Down: %v", err)
	}
	if d := indexDef(); !strings.Contains(d, "(tenant_id, policy_id) WHERE is_active") {
		t.Errorf("after the Up, %s = %q, want (tenant_id, policy_id) WHERE is_active", versionsOneActive, d)
	}
	if !strings.Contains(guardBody(), "scope_document_types") {
		t.Error("after the Up, the seal guard body does not name scope_document_types")
	}
	if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
	                 AND table_name = 'approval_policies' AND column_name = 'position'`); n != 1 {
		t.Errorf("approval_policies.position after the Up = %d, want 1", n)
	}
}
