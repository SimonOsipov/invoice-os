// RLS, grant and constraint suite for `extraction_anchor_rules`. Written before its migration
// exists, so every case fails with an explicit 42P01 until that migration lands.
//
// Rows are seeded per test, never in harness.seed(): a missing table must fail only these
// cases, not the whole package. Each rejected statement gets its own db.WithinTenantTx,
// because a failed statement poisons the surrounding transaction.
//
// Run: `DEV_DB_PORT=5433 make test-rls`. requireHarness skips without the four per-role DSNs,
// and a skip is itself a failure under scripts/ci/rls-test-gate.sh — so no case here adds a
// t.Skip of its own.
package db_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// earInsert names every column, so a test that means to violate one CHECK cannot silently
// trip a NOT NULL instead.
const earInsert = `INSERT INTO extraction_anchor_rules
	    (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
	 VALUES ($1, $2, $3, $4, $5::jsonb, $6)`

// earRuleBody is a body ParseRule accepts and the CHECK admits: all three required keys.
const earRuleBody = `{"label":"(?i)invoice\\s*no","relation":{"kind":"same_token","max_distance":0},"shape":"invoice_number"}`

// earField is an invoices column name; extraction.HeaderFields is the vocabulary.
const earField = "invoice_number"

// earFingerprint returns the "v1:<64 hex>" shape EXTR-04-03 produces, fresh per call.
func earFingerprint() string { return "v1:" + docHash() }

// failIfUndefinedAnchorRules turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedAnchorRules(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_anchor_rules migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedAnchorRule inserts one row as the superuser (BYPASSRLS, so seeding needs neither tenant
// context nor an INSERT grant) and returns its id plus a cleanup func.
func seedAnchorRule(t *testing.T, tenantID, fingerprint, fieldName string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx, earInsert,
		id, tenantID, fingerprint, fieldName, earRuleBody, 1,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_anchor_rules: undefined_table (42P01) — the migration is not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_anchor_rules: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = $1`, id)
	}
}

// earAsApp runs one statement as invoice_app under tenantID and returns its error.
func earAsApp(t *testing.T, tenantID, sql string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, sql, args...)
		return e
	})
}

// earAsSuper runs one statement as the superuser. The CHECK cases seed this way because
// invoice_app holds no INSERT grant and would be refused with 42501 before any CHECK ran.
func earAsSuper(t *testing.T, sql string, args ...any) error {
	t.Helper()
	_, err := h.super.Exec(context.Background(), sql, args...)
	return err
}

// earAssertPGCode names the constraint too: a CHECK renamed or widened trips a different one,
// and the SQLSTATE alone cannot tell those apart.
func earAssertPGCode(t *testing.T, err error, wantCode, wantConstraint, what string) {
	t.Helper()
	if failIfUndefinedAnchorRules(t, what, err) {
		return
	}
	if got := pgCode(err); got != wantCode {
		t.Fatalf("%s returned SQLSTATE %q (%v), want %q on %s", what, got, err, wantCode, wantConstraint)
	}
	if wantConstraint == "" {
		return
	}
	if got := pgConstraint(err); got != wantConstraint {
		t.Errorf("%s tripped constraint %q, want %q", what, got, wantConstraint)
	}
}

// earRowCount counts rows by id as the superuser, which sees past the policy.
func earRowCount(t *testing.T, id string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, id)
}

// R-01: the catalog half of the isolation posture. ENABLE alone would let the owner bypass
// the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ExtractionAnchorRulesForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.extraction_anchor_rules'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedAnchorRules(t, "read pg_class for extraction_anchor_rules", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for extraction_anchor_rules: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("extraction_anchor_rules relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
			"(ENABLE alone would let the migrator/owner bypass the policy)", enabled, forced)
	}

	type policy struct {
		roles []string
		cmd   string
		qual  string
	}
	got := map[string]policy{}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'extraction_anchor_rules'`)
	if err != nil {
		t.Fatalf("query pg_policies for extraction_anchor_rules: %v", err)
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
		t.Fatalf("policies on extraction_anchor_rules = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	p, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on extraction_anchor_rules; got %v", got)
	}
	if p.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want ALL — a per-command policy leaves the other commands unbound", p.cmd)
	}
	if !reflect.DeepEqual(p.roles, []string{"public"}) {
		t.Errorf("tenant_isolation roles = %v, want [public] — a TO clause would leave unnamed roles unbound", p.roles)
	}
	if p.qual == "" {
		t.Error("tenant_isolation carries an empty USING; it would admit every row")
	}
}

// R-02: reads are scoped both ways, by id and not by tenant_id. The bare count catches a
// policy that admits everything while the by-id lookups still agree.
func TestRLS_ExtractionAnchorRulesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rowA, cleanupA := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupA()
	rowB, cleanupB := seedAnchorRule(t, h.tenantB, earFingerprint(), earField)
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, rowA); n != 1 {
			t.Errorf("tenant A sees %d of its own rows, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, rowB); n != 0 {
			t.Errorf("tenant A sees %d of tenant B's rows, want 0", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules`); n != 1 {
			t.Errorf("an unfiltered count under tenant A returns %d rows, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A read: %v", err)
	}
}

// R-03: an unset app.current_tenant yields NULL, which matches no row.
func TestRLS_ExtractionAnchorRulesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rowA, cleanupRow := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupRow()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without a tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM extraction_anchor_rules`).Scan(&n)
	if failIfUndefinedAnchorRules(t, "count with no tenant set", err) {
		return
	}
	if err != nil {
		t.Fatalf("count with no tenant set: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant unset = %d, want 0 (row %s exists)", n, rowA)
	}
}

// R-04: no INSERT grant. The row is well-formed and OWN-TENANT, so the policy's WITH CHECK is
// perfectly satisfied and only the absent grant can refuse it.
//
// A missing grant and a WITH CHECK failure are BOTH 42501 and neither carries a constraint
// name, so the SQLSTATE alone cannot tell them apart — this case asserts the message shape
// too, and R-14 is the catalog oracle for the same claim.
func TestRLS_ExtractionAnchorRulesInsertRefusedForApp(t *testing.T) {
	h := requireHarness(t)

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = $1`, id)
	}()

	err := earAsApp(t, h.tenantA, earInsert, id, h.tenantA, earFingerprint(), earField, earRuleBody, 1)
	if failIfUndefinedAnchorRules(t, "own-tenant INSERT as invoice_app", err) {
		return
	}
	if got := pgCode(err); got != "42501" {
		t.Fatalf("invoice_app ran INSERT on extraction_anchor_rules and got SQLSTATE %q (%v), want 42501 "+
			"— the table holds no INSERT grant", got, err)
	}

	msg := pgMessage(err)
	if !strings.Contains(msg, "permission denied") {
		t.Errorf("the refusal message is %q, want one naming \"permission denied\" — an own-tenant row "+
			"cannot fail the policy, so a WITH CHECK message here means the grant is present and "+
			"something else refused", msg)
	}
	if strings.Contains(msg, "row-level security") {
		t.Errorf("the refusal message is %q — that is the policy refusing, not a missing grant; "+
			"invoice_app must hold no INSERT privilege at all", msg)
	}

	if n := earRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused INSERT = %d, want 0", n)
	}
}

// R-05: no UPDATE and no DELETE grant either. Rules are append-only and EXTR-14 ships the
// migration that widens this; nothing in EXTR-04 may edit or remove a stored rule.
func TestRLS_ExtractionAnchorRulesUpdateAndDeleteRefusedForApp(t *testing.T) {
	h := requireHarness(t)

	rowA, cleanupRow := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupRow()

	// Each rejected statement in its own transaction: a failed one poisons the surrounding tx.
	updErr := earAsApp(t, h.tenantA, `UPDATE extraction_anchor_rules SET rule_schema_version = 2 WHERE id = $1`, rowA)
	if failIfUndefinedAnchorRules(t, "UPDATE as invoice_app", updErr) {
		return
	}
	if got := pgCode(updErr); got != "42501" {
		t.Errorf("invoice_app ran UPDATE on extraction_anchor_rules and got SQLSTATE %q (%v), want 42501", got, updErr)
	}

	delErr := earAsApp(t, h.tenantA, `DELETE FROM extraction_anchor_rules WHERE id = $1`, rowA)
	if got := pgCode(delErr); got != "42501" {
		t.Errorf("invoice_app ran DELETE on extraction_anchor_rules and got SQLSTATE %q (%v), want 42501", got, delErr)
	}

	if n := earRowCount(t, rowA); n != 1 {
		t.Errorf("the seeded row after a refused UPDATE and DELETE = %d, want 1", n)
	}
	var version int
	if err := h.super.QueryRow(context.Background(),
		`SELECT rule_schema_version FROM extraction_anchor_rules WHERE id = $1`, rowA).Scan(&version); err != nil {
		t.Fatalf("read rule_schema_version for %s: %v", rowA, err)
	}
	if version != 1 {
		t.Errorf("rule_schema_version = %d after a refused UPDATE, want the seeded 1", version)
	}
}

// R-06: FORCE, behaviourally. The migrator OWNS the table, so without FORCE it reads every
// tenant's rules and every other case here still passes.
func TestRLS_ExtractionAnchorRulesOwnerIsForcedUnderThePolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rowA, cleanupA := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupA()
	rowB, cleanupB := seedAnchorRule(t, h.tenantB, earFingerprint(), earField)
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, rowB); n != 0 {
			t.Errorf("the owner under tenant A sees %d of tenant B's rows, want 0 — FORCE subjects the owner too", n)
		}
		// Positive half: the owner does read its own tenant, so the 0 above is the policy and
		// not a missing SELECT privilege.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, rowA); n != 1 {
			t.Errorf("the owner under tenant A sees %d of tenant A's rows, want 1", n)
		}
		return nil
	}); err != nil {
		if failIfUndefinedAnchorRules(t, "owner read under tenant A", err) {
			return
		}
		t.Fatalf("owner read under tenant A: %v", err)
	}
}

// R-07: rule is an OBJECT. A jsonb array or scalar carries no keys, so the three key checks
// would be vacuously false and the row would land as an unreadable body.
func TestRLS_ExtractionAnchorRulesRejectsANonObjectRule(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what string
		rule string
	}{
		{"a jsonb array", `[]`},
		{"a jsonb string scalar", `"x"`},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField, c.rule, 1)
		earAssertPGCode(t, err, "23514", "extraction_anchor_rules_rule_check", "a rule body that is "+c.what)
		if n := earRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused rule (%s) = %d, want 0", c.what, n)
		}
	}

	// Positive half: a well-formed object body is admitted, so the two rejections above are
	// the CHECK and not a broken statement.
	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := earAsSuper(t, earInsert, okID, h.tenantA, earFingerprint(), earField, earRuleBody, 1); err != nil {
		t.Fatalf("a well-formed object rule body: want success, got: %v", err)
	}
}

// R-08: all three keys are required. This is the ONE point where the CHECK is stronger than
// ParseRule — measured: ParseRule accepts a body with no `label` key, because the zero value
// compiles as the empty pattern and matches every token.
func TestRLS_ExtractionAnchorRulesRejectsARuleMissingAKey(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what string
		rule string
	}{
		{"only label", `{"label":"x"}`},
		{"no label — the body ParseRule wrongly accepts", `{"relation":{"kind":"same_token","max_distance":0},"shape":"date"}`},
		{"no relation", `{"label":"x","shape":"date"}`},
		{"no shape", `{"label":"x","relation":{"kind":"same_token","max_distance":0}}`},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField, c.rule, 1)
		earAssertPGCode(t, err, "23514", "extraction_anchor_rules_rule_check", "a rule body with "+c.what)
		if n := earRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused rule (%s) = %d, want 0", c.what, n)
		}
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := earAsSuper(t, earInsert, okID, h.tenantA, earFingerprint(), earField, earRuleBody, 1); err != nil {
		t.Fatalf("a body carrying all three keys: want success, got: %v", err)
	}
}

// R-09: an empty fingerprint or field name is a row nothing can ever look up — the read is
// keyed on both.
func TestRLS_ExtractionAnchorRulesRejectsAnEmptyFingerprintOrField(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what        string
		fingerprint string
		field       string
		constraint  string
	}{
		{"an empty layout_fingerprint", "", earField, "extraction_anchor_rules_layout_fingerprint_check"},
		{"an empty field_name", earFingerprint(), "", "extraction_anchor_rules_field_name_check"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := earAsSuper(t, earInsert, id, h.tenantA, c.fingerprint, c.field, earRuleBody, 1)
		earAssertPGCode(t, err, "23514", c.constraint, "a row with "+c.what)
		if n := earRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused row (%s) = %d, want 0", c.what, n)
		}
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := earAsSuper(t, earInsert, okID, h.tenantA, earFingerprint(), earField, earRuleBody, 1); err != nil {
		t.Fatalf("a non-empty fingerprint and field: want success, got: %v", err)
	}
}

// R-10: versions count from 1. A 0 is the unset int Go would write if the caller forgot the
// column, and EXTR-04-05 treats an unknown version as an error rather than a silent skip.
func TestRLS_ExtractionAnchorRulesRejectsSchemaVersionBelowOne(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, version := range []int{0, -1} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField, earRuleBody, version)
		earAssertPGCode(t, err, "23514", "extraction_anchor_rules_rule_schema_version_check",
			fmt.Sprintf("a row with rule_schema_version %d", version))
		if n := earRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused rule_schema_version %d = %d, want 0", version, n)
		}
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := earAsSuper(t, earInsert, okID, h.tenantA, earFingerprint(), earField, earRuleBody, 1); err != nil {
		t.Fatalf("rule_schema_version 1: want success, got: %v", err)
	}
}

// R-11: the only read this table serves is "what rules apply to a layout that looks like
// this". A fingerprint-leading index is not tenant-prunable and the policy would scan every
// tenant's rules first.
func TestRLS_ExtractionAnchorRulesFingerprintIndexLeadsWithTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var cols []string
	err := h.super.QueryRow(ctx,
		`SELECT (SELECT array_agg(att.attname ORDER BY k.ord)
		           FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
		           JOIN pg_attribute att
		             ON att.attrelid = x.indrelid AND att.attnum = k.attnum)
		   FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid
		  WHERE x.indrelid = 'public.extraction_anchor_rules'::regclass
		    AND c.relname = 'extraction_anchor_rules_tenant_fingerprint_idx'`,
	).Scan(&cols)
	if failIfUndefinedAnchorRules(t, "read pg_index for extraction_anchor_rules", err) {
		return
	}
	if err != nil {
		t.Fatalf("read extraction_anchor_rules_tenant_fingerprint_idx: %v", err)
	}
	if want := []string{"tenant_id", "layout_fingerprint"}; !reflect.DeepEqual(cols, want) {
		t.Errorf("extraction_anchor_rules_tenant_fingerprint_idx columns = %v, want %v — tenant_id must lead", cols, want)
	}
}

// R-12: the composite FK target must be a CONSTRAINT. A bare CREATE UNIQUE INDEX leaves no
// pg_constraint row and cannot be referenced, so EXTR-14's child would have no target. PG18
// stores NOT NULLs as contype 'n', so the contype filter is mandatory.
func TestRLS_ExtractionAnchorRulesTenantIdIdIsAConstraint(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var uq int
	err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_anchor_rules'::regclass
		    AND contype = 'u' AND conname = 'extraction_anchor_rules_tenant_id_id_uq'`).Scan(&uq)
	if failIfUndefinedAnchorRules(t, "read pg_constraint for extraction_anchor_rules", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_constraint for extraction_anchor_rules: %v", err)
	}
	if uq != 1 {
		t.Errorf("UNIQUE constraints named extraction_anchor_rules_tenant_id_id_uq = %d, want 1", uq)
	}
}

// R-13: a learned rule has no meaning once its tenant is gone, so the FK cascades. RESTRICT
// here would raise 23503 on the tenant delete instead.
func TestRLS_ExtractionAnchorRulesCascadesWithItsTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	doomedTenant := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'extraction-anchor-rules throwaway tenant')`, doomedTenant); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, doomedTenant)
	}()

	doomedRow, _ := seedAnchorRule(t, doomedTenant, earFingerprint(), earField)
	survivingRow, cleanupSurviving := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupSurviving()

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, doomedTenant); err != nil {
		t.Fatalf("delete the tenant: %v — the anchor-rule FK must cascade, not restrict", err)
	}
	if n := earRowCount(t, doomedRow); n != 0 {
		t.Errorf("rows for the deleted tenant = %d, want 0", n)
	}
	if n := earRowCount(t, survivingRow); n != 1 {
		t.Errorf("tenant A's row = %d, want 1 — the cascade took more than its own tenant", n)
	}
}

// R-14: least privilege, and the catalog oracle R-04 needs. Asked as the superuser on purpose
// — information_schema.role_table_grants shows only the current role's own grants, so the
// "reader holds nothing" half cannot be proven from the app pool. The one `true` row makes
// this notice a MISSING grant, not only a forbidden present one.
func TestRLS_ExtractionAnchorRulesGrantIsSelectOnly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", false},
		{"invoice_app", "UPDATE", false},
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
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.extraction_anchor_rules', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedAnchorRules(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, extraction_anchor_rules, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, extraction_anchor_rules, %q) = %v, want %v — the grant is "+
				"exactly SELECT to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}
