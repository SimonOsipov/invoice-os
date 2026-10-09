// RLS, grant and constraint suite for `extraction_rule_breaks` (ENGI-18-01). Red until the
// migration lands: the catalog test asserts the table exists, and every other case degrades to
// a 42P01 message through failIfUndefinedRuleBreaks.
package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	erbFieldNameCheck = "extraction_rule_breaks_field_name_check"
	erbRuleKeyCheck   = "extraction_rule_breaks_rule_key_check"
	erbMessageCheck   = "extraction_rule_breaks_message_check"
	erbTenantJobFK    = "extraction_rule_breaks_tenant_job_fk"
	erbOnePerRule     = "extraction_rule_breaks_one_per_rule"
)

const (
	erbField   = "total"
	erbRuleKey = "vat-standard-rate"
	erbMessage = "VAT is not 7.5% of the subtotal"
)

const erbInsert = `INSERT INTO extraction_rule_breaks
	(id, tenant_id, extraction_job_id, field_name, rule_set_version_id, rule_key, message)
	VALUES ($1, $2, $3, $4, $5, $6, $7)`

func failIfUndefinedRuleBreaks(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_rule_breaks migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// activeRuleSetVersion is the version every rule break names; the table FKs rule_set_versions.
func activeRuleSetVersion(t *testing.T) string {
	t.Helper()
	var id string
	if err := h.super.QueryRow(context.Background(),
		`SELECT id FROM rule_set_versions WHERE is_active`).Scan(&id); err != nil {
		t.Fatalf("read the active rule_set_versions row: %v", err)
	}
	return id
}

func insertRuleBreak(ctx context.Context, tx pgx.Tx, id, tenantID, jobID, field, versionID, ruleKey, message string) error {
	_, err := tx.Exec(ctx, erbInsert, id, tenantID, jobID, field, versionID, ruleKey, message)
	return err
}

// seedRuleBreak inserts one row as the superuser (BYPASSRLS, no grant needed).
func seedRuleBreak(t *testing.T, tenantID, jobID, field, ruleKey string) (id string, cleanup func()) {
	t.Helper()
	id = uuid.NewString()
	if _, err := h.super.Exec(context.Background(), erbInsert,
		id, tenantID, jobID, field, activeRuleSetVersion(t), ruleKey, erbMessage); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_rule_breaks: undefined_table (42P01) — migration not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_rule_breaks: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_rule_breaks WHERE id = $1`, id)
	}
}

// jobFor seeds a document and an extraction job for tenantID.
func jobFor(t *testing.T, tenantID, key string) string {
	t.Helper()
	doc, cleanupDoc := seedDocument(t, tenantID, key)
	t.Cleanup(cleanupDoc)
	job, cleanupJob := seedExtractionJob(t, tenantID, doc)
	t.Cleanup(cleanupJob)
	return job
}

// ERB-01 (AC-1, AC-2): the table exists, FORCE RLS binds the owner, tenant_isolation is the
// one policy, and invoice_app holds SELECT and INSERT and nothing else.
func TestRLS_ExtractionRuleBreaksTableIsTenantIsolatedAndAppendOnlyByGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var exists bool
	if err := h.super.QueryRow(ctx,
		`SELECT to_regclass('public.extraction_rule_breaks') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("to_regclass(extraction_rule_breaks): %v", err)
	}
	if !exists {
		t.Fatal("extraction_rule_breaks does not exist — the ENGI-18-01 migration is not applied")
	}

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class
		  WHERE oid = 'public.extraction_rule_breaks'::regclass`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}

	rows, err := h.super.Query(ctx,
		`SELECT policyname, qual FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = 'extraction_rule_breaks'`)
	if err != nil {
		t.Fatalf("query pg_policies: %v", err)
	}
	defer rows.Close()
	policies := map[string]string{}
	for rows.Next() {
		var name, qual string
		if err := rows.Scan(&name, &qual); err != nil {
			t.Fatalf("scan policy: %v", err)
		}
		policies[name] = qual
	}
	if len(policies) != 1 {
		t.Fatalf("policies on extraction_rule_breaks = %v, want exactly tenant_isolation", policies)
	}
	if qual, ok := policies["tenant_isolation"]; !ok || !strings.Contains(qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q (present=%v), want a comparison against app.current_tenant", qual, ok)
	}

	for _, c := range []struct {
		priv string
		want bool
	}{
		{"SELECT", true}, {"INSERT", true},
		{"UPDATE", false}, {"DELETE", false}, {"TRUNCATE", false}, {"REFERENCES", false}, {"TRIGGER", false},
	} {
		var got bool
		if err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', 'public.extraction_rule_breaks', $1)`, c.priv).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(invoice_app, %s): %v", c.priv, err)
		}
		if got != c.want {
			t.Errorf("invoice_app %s on extraction_rule_breaks = %v, want %v", c.priv, got, c.want)
		}
	}
}

// ERB-02: unfiltered counts, so RLS is the only predicate. Tenant B holds a row too, so A's
// count of 1 is not "the table has one row".
func TestRLS_ExtractionRuleBreaksCrossTenantSelectReturnsNothing(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-02/a.pdf")
	jobB := jobFor(t, h.tenantB, "ERB-02/b.pdf")
	breakA, cleanupA := seedRuleBreak(t, h.tenantA, jobA, erbField, erbRuleKey)
	defer cleanupA()
	breakB, cleanupB := seedRuleBreak(t, h.tenantB, jobB, erbField, erbRuleKey)
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, breakA); n != 0 {
			t.Errorf("A's rule break visible to B = %d, want 0", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks`); n != 1 {
			t.Errorf("unfiltered count under B = %d, want 1 (B's own row only)", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, breakB); n != 1 {
			t.Errorf("B's own rule break visible to B = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx(B): %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks`); n != 1 {
			t.Errorf("unfiltered count under A = %d, want 1 (A's own row only)", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, breakA); n != 1 {
			t.Errorf("A's own rule break visible to A = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx(A): %v", err)
	}
}

// ERB-03: an INSERT naming tenant A while scoped to B is refused by the policy's WITH CHECK.
func TestRLS_ExtractionRuleBreaksCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-03/a.pdf")
	jobB := jobFor(t, h.tenantB, "ERB-03/b.pdf")
	version := activeRuleSetVersion(t)
	crossID, ownID := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_rule_breaks WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return insertRuleBreak(ctx, tx, crossID, h.tenantA, jobA, erbField, version, erbRuleKey, erbMessage)
	})
	if failIfUndefinedRuleBreaks(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half: the same statement shape succeeds for B's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return insertRuleBreak(ctx, tx, ownID, h.tenantB, jobB, erbField, version, erbRuleKey, erbMessage)
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// ERB-04: tenant B's own tenant_id with tenant A's job id passes the policy and fails the
// composite FK, so a single-column job FK would let it through.
func TestRLS_ExtractionRuleBreaksForeignJobRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-04/a.pdf")
	jobB := jobFor(t, h.tenantB, "ERB-04/b.pdf")
	version := activeRuleSetVersion(t)
	danglingID, okID := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_rule_breaks WHERE id IN ($1, $2)`, danglingID, okID)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return insertRuleBreak(ctx, tx, danglingID, h.tenantB, jobA, erbField, version, erbRuleKey, erbMessage)
	})
	if failIfUndefinedRuleBreaks(t, "foreign-job INSERT", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT naming tenant A's job under tenant B succeeded, want FK violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("foreign-job INSERT: SQLSTATE = %q, want 23503: %v", code, err)
	}
	if name := pgConstraint(err); name != erbTenantJobFK {
		t.Errorf("foreign-job INSERT: constraint = %q, want %q", name, erbTenantJobFK)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, danglingID); n != 0 {
		t.Errorf("rows after the refused foreign-job INSERT = %d, want 0", n)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return insertRuleBreak(ctx, tx, okID, h.tenantB, jobB, erbField, version, erbRuleKey, erbMessage)
	}); err != nil {
		t.Fatalf("B's own job reference: want success, got: %v", err)
	}
}

// ERB-05: no app.current_tenant, no rows and no writes. The seeded row proves the zero is RLS.
func TestRLS_ExtractionRuleBreaksNoTenantFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-05/a.pdf")
	_, cleanup := seedRuleBreak(t, h.tenantA, jobA, erbField, erbRuleKey)
	defer cleanup()
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks`); n < 1 {
		t.Fatalf("superuser sees %d rows after seeding one, want >= 1", n)
	}

	if n := mustCount(t, h.app, `SELECT count(*) FROM extraction_rule_breaks`); n != 0 {
		t.Errorf("app SELECT with no tenant context = %d rows, want 0", n)
	}

	insertID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_rule_breaks WHERE id = $1`, insertID)
	}()
	_, err := h.app.Exec(ctx, erbInsert,
		insertID, h.tenantA, jobA, erbField, activeRuleSetVersion(t), erbRuleKey, erbMessage)
	if failIfUndefinedRuleBreaks(t, "no-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
}

// ERB-06: FORCE makes the owner subject to the policy; ENABLE alone would let it read every row.
func TestRLS_ExtractionRuleBreaksForceBindsTheOwner(t *testing.T) {
	h := requireHarness(t)

	jobA := jobFor(t, h.tenantA, "ERB-06/a.pdf")
	id, cleanup := seedRuleBreak(t, h.tenantA, jobA, erbField, erbRuleKey)
	defer cleanup()
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, id); n != 1 {
		t.Fatalf("superuser sees %d rows for the seeded id, want 1", n)
	}

	if n := mustCount(t, h.mig, `SELECT count(*) FROM extraction_rule_breaks`); n != 0 {
		t.Errorf("migrator (owner) SELECT with no tenant context = %d rows, want 0 (FORCE ROW LEVEL SECURITY)", n)
	}
}

// ERB-07: each CHECK refuses its own bad value and names itself; the boundary value passes.
func TestRLS_ExtractionRuleBreaksChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-07/a.pdf")
	version := activeRuleSetVersion(t)
	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_rule_breaks WHERE id = ANY($1)`, probes)
	}()
	insert := func(field, ruleKey, message string) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertRuleBreak(ctx, tx, id, h.tenantA, jobA, field, version, ruleKey, message)
		})
	}

	// Positive half first: 128 characters is the longest legal field_name.
	err := insert(strings.Repeat("f", 128), erbRuleKey, erbMessage)
	if failIfUndefinedRuleBreaks(t, "INSERT with a 128-char field_name", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT with a 128-char field_name: want success, got: %v", err)
	}

	for _, c := range []struct {
		what, field, ruleKey, message, constraint string
	}{
		{"empty rule_key", erbField, "", erbMessage, erbRuleKeyCheck},
		{"empty message", erbField, erbRuleKey, "", erbMessageCheck},
		{"empty field_name", "", erbRuleKey, erbMessage, erbFieldNameCheck},
		{"129-char field_name", strings.Repeat("f", 129), erbRuleKey, erbMessage, erbFieldNameCheck},
	} {
		err := insert(c.field, c.ruleKey, c.message)
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514: %v", c.what, code, err)
			continue
		}
		if name := pgConstraint(err); name != c.constraint {
			t.Errorf("INSERT with %s: constraint = %q, want %q", c.what, name, c.constraint)
		}
	}
}

// ERB-08: one row per (job, field, rule_key). A second rule on the same field and the same
// rule on a second field are both legal, so the key is not narrower than the design says.
func TestRLS_ExtractionRuleBreaksOnePerRule(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-08/a.pdf")
	version := activeRuleSetVersion(t)
	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_rule_breaks WHERE id = ANY($1)`, probes)
	}()
	insert := func(field, ruleKey string) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertRuleBreak(ctx, tx, id, h.tenantA, jobA, field, version, ruleKey, erbMessage)
		})
	}

	err := insert(erbField, erbRuleKey)
	if failIfUndefinedRuleBreaks(t, "first INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("first INSERT: want success, got: %v", err)
	}

	err = insert(erbField, erbRuleKey)
	if err == nil {
		t.Fatal("second INSERT of the same (job, field, rule_key) succeeded, want unique violation (23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate INSERT: SQLSTATE = %q, want 23505: %v", code, err)
	}
	if name := pgConstraint(err); name != erbOnePerRule {
		t.Errorf("duplicate INSERT: constraint = %q, want %q", name, erbOnePerRule)
	}

	if err := insert(erbField, "a-different-rule"); err != nil {
		t.Errorf("a different rule_key on the same field: want success, got: %v", err)
	}
	if err := insert("subtotal", erbRuleKey); err != nil {
		t.Errorf("the same rule_key on a different field: want success, got: %v", err)
	}
}

// ERB-09: deleting the job takes its rule breaks with it.
func TestRLS_ExtractionRuleBreaksCascadeWithTheJob(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	doc, cleanupDoc := seedDocument(t, h.tenantA, "ERB-09/a.pdf")
	defer cleanupDoc()
	job, cleanupJob := seedExtractionJob(t, h.tenantA, doc)
	defer cleanupJob()
	otherJob := jobFor(t, h.tenantA, "ERB-09/other.pdf")

	_, c1 := seedRuleBreak(t, h.tenantA, job, erbField, erbRuleKey)
	defer c1()
	_, c2 := seedRuleBreak(t, h.tenantA, job, "subtotal", erbRuleKey)
	defer c2()
	kept, c3 := seedRuleBreak(t, h.tenantA, otherJob, erbField, erbRuleKey)
	defer c3()

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE extraction_job_id = $1`, job); n != 2 {
		t.Fatalf("rule breaks for the job before the delete = %d, want 2", n)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM extraction_jobs WHERE id = $1`, job); err != nil {
		t.Fatalf("delete the job: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE extraction_job_id = $1`, job); n != 0 {
		t.Errorf("rule breaks for the deleted job = %d, want 0 (ON DELETE CASCADE)", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, kept); n != 1 {
		t.Errorf("another job's rule break after the delete = %d, want 1", n)
	}
}

// ERB-10 (AC-2): the app role can read and insert but not edit or remove a stored break.
func TestRLS_ExtractionRuleBreaksAppendOnly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-10/a.pdf")
	id, cleanup := seedRuleBreak(t, h.tenantA, jobA, erbField, erbRuleKey)
	defer cleanup()

	for _, c := range []struct{ what, sql string }{
		{"UPDATE", `UPDATE extraction_rule_breaks SET message = 'tampered' WHERE id = $1`},
		{"DELETE", `DELETE FROM extraction_rule_breaks WHERE id = $1`},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql, id)
			return e
		})
		if failIfUndefinedRuleBreaks(t, "app "+c.what, err) {
			return
		}
		if err == nil {
			t.Errorf("invoice_app ran %s on extraction_rule_breaks, want permission denied (42501)", c.what)
			continue
		}
		if code := pgCode(err); code != "42501" {
			t.Errorf("app %s: SQLSTATE = %q, want 42501: %v", c.what, code, err)
		}
	}

	var message string
	if err := h.super.QueryRow(ctx, `SELECT message FROM extraction_rule_breaks WHERE id = $1`, id).Scan(&message); err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if message != erbMessage {
		t.Errorf("message after the refused UPDATE = %q, want unchanged %q", message, erbMessage)
	}

	// Positive half: the same role can read the row, so the refusals are about UPDATE and DELETE.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks WHERE id = $1`, id); n != 1 {
			t.Errorf("app SELECT of the row = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}
