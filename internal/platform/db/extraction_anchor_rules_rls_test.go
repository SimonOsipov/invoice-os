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
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
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

// L-02: invoice_app now holds INSERT and its own-tenant row lands. Reads back the row rather
// than only checking "no error" — that would also pass if the WITH CHECK silently rewrote a
// column.
func TestRLS_ExtractionAnchorRulesAppInsertsItsOwnTenantsRule(t *testing.T) {
	h := requireHarness(t)

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = $1`, id)
	}()

	fingerprint := earFingerprint()
	err := earAsApp(t, h.tenantA, earInsert, id, h.tenantA, fingerprint, earField, earRuleBody, 1)
	if failIfUndefinedAnchorRules(t, "own-tenant INSERT as invoice_app", err) {
		return
	}
	if err != nil {
		t.Fatalf("invoice_app INSERT of its own tenant's rule: want success, got: %v", err)
	}

	var gotTenant, gotFingerprint, gotField string
	if e := h.super.QueryRow(context.Background(),
		`SELECT tenant_id::text, layout_fingerprint, field_name FROM extraction_anchor_rules WHERE id = $1`, id,
	).Scan(&gotTenant, &gotFingerprint, &gotField); e != nil {
		t.Fatalf("read back the row invoice_app inserted: %v", e)
	}
	if gotTenant != h.tenantA || gotFingerprint != fingerprint || gotField != earField {
		t.Errorf("row read back = (%s, %s, %s), want (%s, %s, %s)",
			gotTenant, gotFingerprint, gotField, h.tenantA, fingerprint, earField)
	}
}

// L-05: seq is the recency order, not created_at — created_at defaults to now(), which is
// transaction-constant, so two rows written together tie on it exactly while nextval
// separates them. Both halves are asserted: created_at equality is what makes seqB > seqA
// prove seq ordering rather than accidentally agreeing with a clock-ordered read.
func TestRLS_ExtractionAnchorRulesSeqOrdersWithinOneTransaction(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	idA, idB := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, []string{idA, idB})
	}()

	fingerprint := earFingerprint()
	var seqA, seqB int64
	var createdAtA, createdAtB time.Time
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, earInsert, idA, h.tenantA, fingerprint, earField, earRuleBody, 1); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, earInsert, idB, h.tenantA, fingerprint, earField, earRuleBody, 1); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT seq, created_at FROM extraction_anchor_rules WHERE id = $1`, idA).
			Scan(&seqA, &createdAtA); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT seq, created_at FROM extraction_anchor_rules WHERE id = $1`, idB).
			Scan(&seqB, &createdAtB)
	})
	if err != nil {
		t.Fatalf("two INSERTs for one fingerprint in one transaction: %v — until Migration A grants "+
			"INSERT and adds seq, this fails closed", err)
	}

	// Strict, not seqB == seqA+1: a concurrent test in this package can consume a nextval.
	if !(seqB > seqA) {
		t.Fatalf("seq = %d then %d, want strictly increasing", seqA, seqB)
	}
	if !createdAtA.Equal(createdAtB) {
		t.Fatalf("created_at = %v then %v, want identical — the fixture no longer ties on the clock, "+
			"so seqB > seqA above no longer proves seq (not created_at) orders the rows", createdAtA, createdAtB)
	}
}

// L-06: without USAGE on the sequence, invoice_app's INSERT fails 42501 "permission denied
// for sequence" even holding the table grant — extraction_field_corrections (EFC-05) and
// audit_log.sql:79 are the precedent.
func TestRLS_ExtractionAnchorRulesAppHoldsSequenceUsage(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var got bool
	err := h.super.QueryRow(ctx,
		`SELECT has_sequence_privilege('invoice_app', 'public.extraction_anchor_rules_seq_seq', 'USAGE')`,
	).Scan(&got)
	if failIfUndefinedAnchorRules(t, "has_sequence_privilege(invoice_app, extraction_anchor_rules_seq_seq, USAGE)", err) {
		return
	}
	if err != nil {
		t.Fatalf("has_sequence_privilege(invoice_app, extraction_anchor_rules_seq_seq, USAGE): %v", err)
	}
	if !got {
		t.Error("invoice_app holds no USAGE on extraction_anchor_rules_seq_seq — every INSERT fails " +
			"42501 (permission denied for sequence); GRANT INSERT on the table does not carry it")
	}
}

// R-05, still true after Migration A: no UPDATE and no DELETE grant. Rules are append-only —
// nothing may edit or remove a stored rule.
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
	// Named `got`, not `version`: TestRuleSetV2_DetectionCommandBaseline greps the repo
	// for any identifier ending in `version` pinned to 1, and carves out no path here.
	var got int
	if err := h.super.QueryRow(context.Background(),
		`SELECT rule_schema_version FROM extraction_anchor_rules WHERE id = $1`, rowA).Scan(&got); err != nil {
		t.Fatalf("read rule_schema_version for %s: %v", rowA, err)
	}
	if got != 1 {
		t.Errorf("rule_schema_version = %d after a refused UPDATE, want the seeded 1", got)
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
// column, and AnchorRulesFor treats an unknown version as an error rather than a silent skip.
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

// L-07: the recency index leads with tenant, then fingerprint, then the write order, and
// supersedes the old index — which must be gone. Both facts come from the same pg_index
// read: a query that stopped matching extraction_anchor_rules at all could not pass the
// "new index exists with these columns" half either.
func TestRLS_ExtractionAnchorRulesFingerprintIndexLeadsWithTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	const (
		newIdx = "extraction_anchor_rules_tenant_fingerprint_seq_idx"
		oldIdx = "extraction_anchor_rules_tenant_fingerprint_idx"
	)

	got := map[string][]string{}
	rows, err := h.super.Query(ctx,
		`SELECT c.relname,
		        (SELECT array_agg(att.attname ORDER BY k.ord)
		           FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
		           JOIN pg_attribute att
		             ON att.attrelid = x.indrelid AND att.attnum = k.attnum)
		   FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid
		  WHERE x.indrelid = 'public.extraction_anchor_rules'::regclass
		    AND c.relname IN ($1, $2)`, newIdx, oldIdx)
	if failIfUndefinedAnchorRules(t, "read pg_index for extraction_anchor_rules", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_index for extraction_anchor_rules: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var cols []string
		if e := rows.Scan(&name, &cols); e != nil {
			t.Fatalf("scan pg_index row: %v", e)
		}
		got[name] = cols
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_index rows: %v", e)
	}

	if cols, ok := got[newIdx]; !ok {
		t.Errorf("no index named %s on extraction_anchor_rules; got %v", newIdx, got)
	} else if want := []string{"tenant_id", "layout_fingerprint", "seq"}; !reflect.DeepEqual(cols, want) {
		t.Errorf("%s columns = %v, want %v — tenant_id must lead, seq is the recency order", newIdx, cols, want)
	}
	if _, ok := got[oldIdx]; ok {
		t.Errorf("%s still exists; Migration A supersedes and drops it", oldIdx)
	}
}

// R-12: the uniqueness is declared as a named CONSTRAINT, the form every sibling table uses.
// Postgres accepts an FK against a bare unique index too (measured, PG18.6), so what a bare
// index actually costs is the pg_constraint row and the stable name — not the FK. PG18 stores
// NOT NULLs as contype 'n', so the contype filter is mandatory.
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

// L-01 (folds L-04): least privilege, and the catalog oracle L-02 needs. Asked as the
// superuser on purpose — information_schema.role_table_grants shows only the current role's
// own grants, so the "reader holds nothing" half cannot be proven from the app pool.
func TestRLS_ExtractionAnchorRulesGrantIsSelectAndInsert(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
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
				"exactly SELECT, INSERT to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// earMigrationGlob is a suffix, not a timestamp: this subtask's migration is scaffolded
// fresh in every worktree, so its filename is not predictable.
const earMigrationGlob = "*_extraction_learned_rule_writer.sql"

// earMigrationStatements returns one goose section ("Up" or "Down") of the shipped
// migration, read from migrations.FS. Naive splitter — correct for this migration, which
// is plain DDL and GRANT with no function body.
func earMigrationStatements(t *testing.T, section string) []string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, earMigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file — Migration A of "+
			"this subtask is not applied yet", earMigrationGlob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	body := string(raw)
	up := strings.Index(body, gooseUp)
	down := strings.Index(body, gooseDown)
	if up < 0 || down < 0 || down < up {
		t.Fatalf("%s: want both %q and %q, in that order (up=%d down=%d)", matches[0], gooseUp, gooseDown, up, down)
	}
	var piece string
	switch section {
	case "Up":
		piece = body[up+len(gooseUp) : down]
	case "Down":
		piece = body[down+len(gooseDown):]
	default:
		t.Fatalf("unknown goose section %q", section)
	}

	var stripped []string
	for _, line := range strings.Split(piece, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		stripped = append(stripped, line)
	}
	var out []string
	for _, s := range strings.Split(strings.Join(stripped, "\n"), ";") {
		if s := strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// assertEarShapeBeforeDown pins the post-Migration-A shape, the exact inverse of what the
// L-11 assertions demand after the Down.
func assertEarShapeBeforeDown(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()

	var insert bool
	if err := tx.QueryRow(ctx,
		`SELECT has_table_privilege('invoice_app', 'public.extraction_anchor_rules', 'INSERT')`,
	).Scan(&insert); err != nil {
		t.Fatalf("has_table_privilege before the Down: %v", err)
	}
	if !insert {
		t.Fatal("invoice_app holds no INSERT before the Down — Migration A has not been applied, so this test proves nothing")
	}

	var seqCol, seqRel, newIdx int
	if err := tx.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM information_schema.columns
		          WHERE table_schema = 'public' AND table_name = 'extraction_anchor_rules'
		            AND column_name = 'seq'),
		        (SELECT count(*) FROM pg_class
		          WHERE relkind = 'S' AND relname = 'extraction_anchor_rules_seq_seq'),
		        (SELECT count(*) FROM pg_indexes
		          WHERE schemaname = 'public'
		            AND indexname = 'extraction_anchor_rules_tenant_fingerprint_seq_idx')`,
	).Scan(&seqCol, &seqRel, &newIdx); err != nil {
		t.Fatalf("snapshot the shape before the Down: %v", err)
	}
	if seqCol != 1 || seqRel != 1 || newIdx != 1 {
		t.Fatalf("before the Down: seq column=%d, sequence=%d, seq index=%d, want 1/1/1 — "+
			"Migration A has not been applied, so this test proves nothing", seqCol, seqRel, newIdx)
	}
}

// L-11: the Down restores the previous shape exactly — the dropped index returns, the
// sequence and its grant disappear, and the INSERT grant is gone. Executed for real, inside
// one invoice_migrator transaction that is always rolled back, so it leaves nothing behind.
func TestRLS_ExtractionAnchorRulesMigrationDownRestoresTheShape(t *testing.T) {
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	// The Up half is not replayed: the suite runs against a migrated DB, so a second
	// ADD COLUMN raises 42701. It is still parsed, so a file that loses its Up marker fails
	// here rather than silently testing nothing.
	if up := earMigrationStatements(t, "Up"); len(up) == 0 {
		t.Fatal("Up body is empty")
	}

	// Before-snapshot: without it the assertions below pass on a DB where Migration A never
	// landed, and a Down that does nothing reads as a Down that restores the shape.
	assertEarShapeBeforeDown(t, ctx, tx)

	down := earMigrationStatements(t, "Down")
	if len(down) == 0 {
		t.Fatal("Down body is empty")
	}
	for i, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("Down statement %d failed: %v\n%s", i+1, err, s)
		}
	}

	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
		var got bool
		if err := tx.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', 'public.extraction_anchor_rules', $1)`, priv,
		).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege after the round-trip: %v", err)
		}
		if want := priv == "SELECT"; got != want {
			t.Errorf("after Up+Down, has_table_privilege(invoice_app, extraction_anchor_rules, %s) = %v, want %v",
				priv, got, want)
		}
	}

	var colCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'extraction_anchor_rules' AND column_name = 'seq'`,
	).Scan(&colCount); err != nil {
		t.Fatalf("count the seq column: %v", err)
	}
	if colCount != 0 {
		t.Error("extraction_anchor_rules.seq survives the Down, want dropped")
	}

	var seqCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM pg_class WHERE relkind = 'S' AND relname = 'extraction_anchor_rules_seq_seq'`,
	).Scan(&seqCount); err != nil {
		t.Fatalf("count the sequence relation: %v", err)
	}
	if seqCount != 0 {
		t.Error("extraction_anchor_rules_seq_seq survives the Down, want dropped")
	}

	// Both index names in the same query: the old one must be back, the new one gone.
	idxCols := map[string][]string{}
	rows, err := tx.Query(ctx,
		`SELECT c.relname,
		        (SELECT array_agg(att.attname ORDER BY k.ord)
		           FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
		           JOIN pg_attribute att
		             ON att.attrelid = x.indrelid AND att.attnum = k.attnum)
		   FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid
		  WHERE x.indrelid = 'public.extraction_anchor_rules'::regclass
		    AND c.relname IN ('extraction_anchor_rules_tenant_fingerprint_idx',
		                       'extraction_anchor_rules_tenant_fingerprint_seq_idx')`)
	if err != nil {
		t.Fatalf("read pg_index after the round-trip: %v", err)
	}
	for rows.Next() {
		var name string
		var cols []string
		if e := rows.Scan(&name, &cols); e != nil {
			t.Fatalf("scan pg_index row: %v", e)
		}
		idxCols[name] = cols
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_index rows: %v", e)
	}
	if want := []string{"tenant_id", "layout_fingerprint"}; !reflect.DeepEqual(idxCols["extraction_anchor_rules_tenant_fingerprint_idx"], want) {
		t.Errorf("extraction_anchor_rules_tenant_fingerprint_idx columns after the round-trip = %v, want %v",
			idxCols["extraction_anchor_rules_tenant_fingerprint_idx"], want)
	}
	if _, ok := idxCols["extraction_anchor_rules_tenant_fingerprint_seq_idx"]; ok {
		t.Error("extraction_anchor_rules_tenant_fingerprint_seq_idx survives the Down, want dropped")
	}
}

// --- adversarial coverage (QA, Mode B) --------------------------------------------------

// A-01: `?` on a jsonb ARRAY tests ELEMENT membership, so ["label","relation","shape"] passes
// all three key tests. Only jsonb_typeof(rule) = 'object' refuses it — R-07's [] and "x"
// probes are killed by the key tests too, which left that clause unpinned.
func TestRLS_ExtractionAnchorRulesRejectsAnArrayNamingTheThreeKeys(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	id := uuid.NewString()
	probes = append(probes, id)
	err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField,
		`["label","relation","shape"]`, 1)
	earAssertPGCode(t, err, "23514", "extraction_anchor_rules_rule_check",
		"a jsonb array whose ELEMENTS are the three key names")
	if n := earRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused array body = %d, want 0", n)
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := earAsSuper(t, earInsert, okID, h.tenantA, earFingerprint(), earField, earRuleBody, 1); err != nil {
		t.Fatalf("a well-formed object body: want success, got: %v", err)
	}
}

// A-02: where the floor stops. `?` is key EXISTENCE, so a null-valued key satisfies it, and
// the CHECK reads no value at all. Both bodies below LAND — ParseRule and AnchorRulesFor's
// read-time error own them. Pinning that keeps a future tightening of the CHECK from
// silently moving the boundary.
func TestRLS_ExtractionAnchorRulesAdmitsBodiesOnlyParseRuleCanRefuse(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what string
		rule string
	}{
		{"three null-valued keys", `{"label":null,"relation":null,"shape":null}`},
		{"three keys of the wrong types", `{"label":1,"relation":"x","shape":[]}`},
		{"an unknown shape and relation kind", `{"label":"x","relation":{"kind":"nope"},"shape":"nope"}`},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		if err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField, c.rule, 1); err != nil {
			t.Errorf("a rule body with %s: want the CHECK to ADMIT it (ParseRule owns the values), got: %v", c.what, err)
			continue
		}
		if n := earRowCount(t, id); n != 1 {
			t.Errorf("rows after admitting %s = %d, want 1", c.what, n)
		}
	}

	// Negative half: the floor is still a floor — drop one key and the same shape is refused.
	badID := uuid.NewString()
	probes = append(probes, badID)
	err := earAsSuper(t, earInsert, badID, h.tenantA, earFingerprint(), earField, `{"label":null,"relation":null}`, 1)
	earAssertPGCode(t, err, "23514", "extraction_anchor_rules_rule_check", "a body with two null-valued keys and no shape")
}

// A-03: the 128 ceiling on both text columns. R-09 pins only the > 0 half, so a migration
// that dropped the ceiling passed every case. 128 must land and 129 must not, or the ceiling
// is somewhere else than it reads.
func TestRLS_ExtractionAnchorRulesTextCeilingsAreOneTwentyEight(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	at128 := "v1:" + strings.Repeat("a", 125)
	if len(at128) != 128 {
		t.Fatalf("test setup: the at-ceiling fingerprint is %d chars, want 128", len(at128))
	}
	over := strings.Repeat("a", 129)

	for _, c := range []struct {
		what        string
		fingerprint string
		field       string
		constraint  string // "" = the row must land
	}{
		{"a 128-char layout_fingerprint", at128, earField, ""},
		{"a 129-char layout_fingerprint", over, earField, "extraction_anchor_rules_layout_fingerprint_check"},
		{"a 128-char field_name", earFingerprint(), strings.Repeat("f", 128), ""},
		{"a 129-char field_name", earFingerprint(), over, "extraction_anchor_rules_field_name_check"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := earAsSuper(t, earInsert, id, h.tenantA, c.fingerprint, c.field, earRuleBody, 1)
		if c.constraint == "" {
			if err != nil {
				t.Errorf("%s: want the row to land at the ceiling, got: %v", c.what, err)
				continue
			}
			if n := earRowCount(t, id); n != 1 {
				t.Errorf("rows after %s = %d, want 1", c.what, n)
			}
			continue
		}
		earAssertPGCode(t, err, "23514", c.constraint, c.what)
		if n := earRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused %s = %d, want 0", c.what, n)
		}
	}
}

// A-04: corrections are append-only, so several rules accumulate per field per layout and all
// produce candidates. The migration says so in prose; nothing asserted it, and a UNIQUE over
// (tenant_id, layout_fingerprint, field_name) added later would silently make the second
// correction overwrite the first.
func TestRLS_ExtractionAnchorRulesAcceptsSeveralRulesForOneField(t *testing.T) {
	h := requireHarness(t)

	fingerprint := earFingerprint()
	first, cleanupFirst := seedAnchorRule(t, h.tenantA, fingerprint, earField)
	defer cleanupFirst()
	second, cleanupSecond := seedAnchorRule(t, h.tenantA, fingerprint, earField)
	defer cleanupSecond()
	if first == second {
		t.Fatal("test setup: both seeds share an id, so the count below cannot distinguish them")
	}

	n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_anchor_rules
		  WHERE tenant_id = $1 AND layout_fingerprint = $2 AND field_name = $3`,
		h.tenantA, fingerprint, earField)
	if n != 2 {
		t.Errorf("rules stored for one (tenant, layout, field) = %d, want 2 — corrections are append-only", n)
	}

	// The only uniqueness the table declares is (tenant_id, id), so a duplicate id IS refused.
	dup := earAsSuper(t, earInsert, first, h.tenantA, fingerprint, earField, earRuleBody, 1)
	if got := pgCode(dup); got != "23505" {
		t.Errorf("re-inserting the same id returned SQLSTATE %q (%v), want 23505", got, dup)
	}
}

// A-05: rule_schema_version is int4 with a floor of 1 and no ceiling of its own. R-10 pins
// only the floor; a widening to bigint would pass it while changing what a reader must scan.
func TestRLS_ExtractionAnchorRulesSchemaVersionSpansInt4(t *testing.T) {
	h := requireHarness(t)

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_anchor_rules WHERE id = ANY($1)`, probes)
	}()

	for _, version := range []int{1, 2, 2147483647} {
		id := uuid.NewString()
		probes = append(probes, id)
		if err := earAsSuper(t, earInsert, id, h.tenantA, earFingerprint(), earField, earRuleBody, version); err != nil {
			t.Errorf("rule_schema_version %d: want success, got: %v", version, err)
			continue
		}
		if n := earRowCount(t, id); n != 1 {
			t.Errorf("rows after rule_schema_version %d = %d, want 1", version, n)
		}
	}

	// One past int4: the column type refuses it, not a CHECK, so there is no constraint name.
	// The value is inline because pgx refuses to encode it into an int4 parameter client-side,
	// which would prove the driver rather than the column.
	over := uuid.NewString()
	probes = append(probes, over)
	err := earAsSuper(t, `INSERT INTO extraction_anchor_rules
		    (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1, $2, $3, $4, $5::jsonb, 2147483648)`,
		over, h.tenantA, earFingerprint(), earField, earRuleBody)
	earAssertPGCode(t, err, "22003", "", "rule_schema_version one past int4")
}

// A-06: invoice_tenant_reader holds no grant here, so its SELECT fails at the ACL layer
// before RLS is evaluated. R-14 asserts the same from the catalog; this is the behaviour.
// The app role reading the very same row is the positive half.
func TestRLS_ExtractionAnchorRulesReaderSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rowA, cleanupRow := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupRow()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM extraction_anchor_rules`).Scan(&n)
	if failIfUndefinedAnchorRules(t, "reader SELECT", err) {
		return
	}
	if err == nil {
		t.Fatalf("invoice_tenant_reader SELECT on extraction_anchor_rules returned %d rows, want permission "+
			"denied (42501) — the reader enumerates tenants and has no use for learned rules", n)
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on extraction_anchor_rules: SQLSTATE = %q, want 42501: %v", code, err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM extraction_anchor_rules WHERE id = $1`, rowA); got != 1 {
			t.Errorf("app-role SELECT of the seeded row = %d, want 1 — the refusal above must be the "+
				"reader's privileges, not an unreadable table", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("app read of the seeded row: %v", err)
	}
}

// A-07: relabelling a row into another tenant. The grant refuses it before the policy does,
// which is the stronger of the two — R-05 asserts UPDATE is refused at all, this asserts the
// row a cross-tenant move would have produced never appears.
func TestRLS_ExtractionAnchorRulesAppCannotRelabelARowsTenant(t *testing.T) {
	h := requireHarness(t)

	rowA, cleanupRow := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupRow()

	err := earAsApp(t, h.tenantA, `UPDATE extraction_anchor_rules SET tenant_id = $1 WHERE id = $2`, h.tenantB, rowA)
	if failIfUndefinedAnchorRules(t, "cross-tenant relabel as invoice_app", err) {
		return
	}
	if got := pgCode(err); got != "42501" {
		t.Fatalf("invoice_app moved a row to another tenant and got SQLSTATE %q (%v), want 42501", got, err)
	}

	var owner string
	if e := h.super.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM extraction_anchor_rules WHERE id = $1`, rowA).Scan(&owner); e != nil {
		t.Fatalf("read tenant_id for %s: %v", rowA, e)
	}
	if owner != h.tenantA {
		t.Errorf("tenant_id after the refused relabel = %s, want the seeded %s", owner, h.tenantA)
	}
}

// A-08: the composite-FK target, exercised rather than asserted. R-12 reads pg_constraint;
// this builds EXTR-14's child shape against (tenant_id, id) and proves the pair is enforced.
// It deliberately does NOT fail if the uniqueness were a bare index — Postgres accepts that as
// an FK target — which is why R-12 stays the oracle for the constraint form. The child is
// created and dropped here, never a committed migration: the rls_harness_test.go shape.
func TestRLS_ExtractionAnchorRulesUqCarriesACompositeForeignKey(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rowA, cleanupRow := seedAnchorRule(t, h.tenantA, earFingerprint(), earField)
	defer cleanupRow()

	defer func() {
		_, _ = h.mig.Exec(context.Background(), `DROP TABLE IF EXISTS ear_fk_probe`)
	}()
	if _, err := h.mig.Exec(ctx, `DROP TABLE IF EXISTS ear_fk_probe`); err != nil {
		t.Fatalf("drop any stale probe child: %v", err)
	}
	if _, err := h.mig.Exec(ctx, `CREATE TABLE ear_fk_probe (
		id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		tenant_id      uuid NOT NULL,
		anchor_rule_id uuid NOT NULL,
		FOREIGN KEY (tenant_id, anchor_rule_id)
			REFERENCES extraction_anchor_rules (tenant_id, id) ON DELETE CASCADE
	)`); err != nil {
		if failIfUndefinedAnchorRules(t, "create a child against (tenant_id, id)", err) {
			return
		}
		t.Fatalf("a composite FK against extraction_anchor_rules (tenant_id, id): %v", err)
	}

	// The matching pair lands.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO ear_fk_probe (tenant_id, anchor_rule_id) VALUES ($1, $2)`, h.tenantA, rowA); err != nil {
		t.Fatalf("a child row naming the right (tenant_id, id) pair: want success, got: %v", err)
	}

	// The mismatched pair does not: this is what the composite target buys over a bare
	// anchor_rule_id FK, which would accept another tenant's rule id.
	_, err := h.super.Exec(ctx,
		`INSERT INTO ear_fk_probe (tenant_id, anchor_rule_id) VALUES ($1, $2)`, h.tenantB, rowA)
	if got := pgCode(err); got != "23503" {
		t.Fatalf("a child row pairing tenant B with tenant A's rule returned SQLSTATE %q (%v), want 23503", got, err)
	}
}
