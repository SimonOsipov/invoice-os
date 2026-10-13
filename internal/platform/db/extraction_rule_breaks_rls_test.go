// RLS, grant and constraint suite for `extraction_rule_breaks`. The catalog test asserts the
// table exists; every other case degrades to a 42P01 message through failIfUndefinedRuleBreaks.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
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
		`SELECT id FROM rule_set_versions WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`).Scan(&id); err != nil {
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
		t.Fatal("extraction_rule_breaks does not exist — the extraction_rule_breaks migration is not applied")
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

// ERB-11 (AC-1, AC-3): constraint names and rules a caller can trip. Omitting a NOT NULL
// column names it; an unknown rule-set version trips its own FK; id and created_at default.
func TestRLS_ExtractionRuleBreaksReferencesAndRequiredColumns(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	jobA := jobFor(t, h.tenantA, "ERB-11/a.pdf")
	version := activeRuleSetVersion(t)
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_rule_breaks WHERE extraction_job_id = $1`, jobA)
	})

	var id string
	var fresh bool
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO extraction_rule_breaks (tenant_id, extraction_job_id, field_name, rule_set_version_id, rule_key, message)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id::text, created_at > now() - interval '1 minute' AND created_at <= now()`,
			h.tenantA, jobA, erbField, version, erbRuleKey, erbMessage).Scan(&id, &fresh)
	})
	if failIfUndefinedRuleBreaks(t, "insert without id and created_at", err) {
		return
	}
	if err != nil {
		t.Fatalf("insert relying on the id and created_at defaults: want success, got: %v", err)
	}
	if _, perr := uuid.Parse(id); perr != nil || !fresh {
		t.Errorf("defaults: id = %q (parse err %v), created_at within the last minute = %v; want a uuid and true", id, perr, fresh)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertRuleBreak(ctx, tx, uuid.NewString(), h.tenantA, jobA, "subtotal", uuid.NewString(), erbRuleKey, erbMessage)
	})
	if err == nil {
		t.Fatal("insert naming a rule_set_versions id that does not exist succeeded, want FK violation (23503)")
	}
	if code, name := pgCode(err), pgConstraint(err); code != "23503" || name != "extraction_rule_breaks_rule_set_version_id_fkey" {
		t.Errorf("unknown rule-set version: SQLSTATE %q on %q, want 23503 on extraction_rule_breaks_rule_set_version_id_fkey: %v", code, name, err)
	}

	for _, col := range []string{"extraction_job_id", "field_name", "rule_set_version_id", "rule_key", "message"} {
		args := map[string]any{
			"extraction_job_id": jobA, "field_name": "total", "rule_set_version_id": version,
			"rule_key": "required-" + col, "message": erbMessage,
		}
		delete(args, col)
		cols, ph, vals := []string{"tenant_id"}, []string{"$1"}, []any{h.tenantA}
		for _, c := range []string{"extraction_job_id", "field_name", "rule_set_version_id", "rule_key", "message"} {
			if v, ok := args[c]; ok {
				cols, ph, vals = append(cols, c), append(ph, "$"+string(rune('1'+len(vals)))), append(vals, v)
			}
		}
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, "INSERT INTO extraction_rule_breaks ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(ph, ", ")+")", vals...)
			return e
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != col {
			t.Errorf("insert without %s: got %v, want SQLSTATE 23502 naming column %s", col, err, col)
		}
	}
}

func erbMigrationSection(t *testing.T, section string) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, "*_extraction_rule_breaks.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS files matching *_extraction_rule_breaks.sql = %v (err %v), want exactly 1", matches, err)
	}
	b, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	raw := string(b)
	up, ok := auditEntityUpOf(raw)
	if !ok {
		t.Fatalf("%s: want %q before %q", matches[0], gooseUp, gooseDown)
	}
	if section == "Up" {
		return up
	}
	return raw[strings.Index(raw, gooseDown)+len(gooseDown):]
}

func erbExecSection(t *testing.T, ctx context.Context, tx pgx.Tx, section string) {
	t.Helper()
	if _, err := tx.Exec(ctx, erbMigrationSection(t, section)); err != nil {
		t.Fatalf("migration %s section failed: %v", section, err)
	}
}

func erbStrings(t *testing.T, ctx context.Context, tx pgx.Tx, sql string) []string {
	t.Helper()
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %q: %v", sql, err)
	}
	return out
}

func erbReasonCheckDef(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	var def string
	if err := tx.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_field_results'::regclass AND conname = $1`,
		efrReasonCodeCheck).Scan(&def); err != nil {
		t.Fatalf("read %s: %v", efrReasonCodeCheck, err)
	}
	return def
}

// ERB-12 (AC-3, AC-4): the shipped Down, executed for real in a rolled-back owner transaction,
// drops the table and puts the four-code CHECK back under the same name.
func TestRLS_ExtractionRuleBreaksMigrationDownDropsTheTableAndRestoresFourCodes(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.extraction_rule_breaks') IS NOT NULL`).Scan(&present); err != nil || !present {
		t.Fatalf("extraction_rule_breaks present before the Down = %v (err %v); the case would pass vacuously", present, err)
	}
	if got := erbReasonCheckDef(t, ctx, tx); got != efrFiveCodeReasonCheck {
		t.Fatalf("reason CHECK before the Down:\n got: %s\nwant: %s", got, efrFiveCodeReasonCheck)
	}

	erbExecSection(t, ctx, tx, "Down")

	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.extraction_rule_breaks') IS NOT NULL`).Scan(&present); err != nil || present {
		t.Errorf("extraction_rule_breaks present after the Down = %v (err %v), want false", present, err)
	}
	if got := erbReasonCheckDef(t, ctx, tx); got != efrFourCodeReasonCheck {
		t.Errorf("reason CHECK after the Down:\n got: %s\nwant: %s", got, efrFourCodeReasonCheck)
	}
}

// ERB-13 (AC-1, AC-2, AC-3, AC-4): the shipped Down then Up recreates the whole contract.
// The live-DB cases above see a schema migrated before the run and cannot notice an edit to
// the migration file; this one executes the file.
func TestRLS_ExtractionRuleBreaksReplayedMigrationHoldsTheContract(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	// Registered before the transaction so the tx rolls back first (cleanups run last-in-first).
	jobA := jobFor(t, h.tenantA, "ERB-13/a.pdf")
	version := activeRuleSetVersion(t)
	tx := migratorTx(t, ctx)

	erbExecSection(t, ctx, tx, "Down")
	erbExecSection(t, ctx, tx, "Up")

	wantColumns := []string{
		"id uuid NOT NULL DEFAULT gen_random_uuid()",
		"tenant_id uuid NOT NULL",
		"extraction_job_id uuid NOT NULL",
		"field_name text NOT NULL",
		"rule_set_version_id uuid NOT NULL",
		"rule_key text NOT NULL",
		"message text NOT NULL",
		"created_at timestamp with time zone NOT NULL DEFAULT now()",
	}
	gotColumns := erbStrings(t, ctx, tx,
		`SELECT a.attname||' '||format_type(a.atttypid, a.atttypmod)||CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
		        ||COALESCE(' DEFAULT '||pg_get_expr(d.adbin, d.adrelid), '')
		   FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE a.attrelid = 'public.extraction_rule_breaks'::regclass AND a.attnum > 0 AND NOT a.attisdropped
		  ORDER BY a.attnum`)
	if !slices.Equal(gotColumns, wantColumns) {
		t.Errorf("columns after Down/Up:\n got: %q\nwant: %q", gotColumns, wantColumns)
	}

	wantConstraints := []string{
		"extraction_rule_breaks_field_name_check CHECK (((char_length(field_name) > 0) AND (char_length(field_name) <= 128)))",
		"extraction_rule_breaks_message_check CHECK ((char_length(message) > 0))",
		"extraction_rule_breaks_one_per_rule UNIQUE (tenant_id, extraction_job_id, field_name, rule_key)",
		"extraction_rule_breaks_pkey PRIMARY KEY (id)",
		"extraction_rule_breaks_rule_key_check CHECK ((char_length(rule_key) > 0))",
		"extraction_rule_breaks_rule_set_version_id_fkey FOREIGN KEY (rule_set_version_id) REFERENCES rule_set_versions(id)",
		"extraction_rule_breaks_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE",
		"extraction_rule_breaks_tenant_job_fk FOREIGN KEY (tenant_id, extraction_job_id) REFERENCES extraction_jobs(tenant_id, id) ON DELETE CASCADE",
	}
	gotConstraints := erbStrings(t, ctx, tx,
		`SELECT conname||' '||pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_rule_breaks'::regclass AND contype IN ('c', 'u', 'p', 'f')
		  ORDER BY conname`)
	if !slices.Equal(gotConstraints, wantConstraints) {
		t.Errorf("constraints after Down/Up:\n got: %q\nwant: %q", gotConstraints, wantConstraints)
	}

	var enabled, forced bool
	if err := tx.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.extraction_rule_breaks'::regclass`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("relrowsecurity/relforcerowsecurity after Down/Up = %v/%v, want true/true", enabled, forced)
	}
	policies := erbStrings(t, ctx, tx,
		`SELECT policyname||'|'||cmd||'|'||qual FROM pg_policies WHERE tablename = 'extraction_rule_breaks'`)
	if len(policies) != 1 || !strings.HasPrefix(policies[0], "tenant_isolation|ALL|(tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid") {
		t.Errorf("policies after Down/Up = %q, want exactly tenant_isolation on ALL comparing tenant_id with the nullif tenant GUC", policies)
	}

	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"invoice_app", "SELECT", true}, {"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false}, {"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false}, {"invoice_app", "REFERENCES", false}, {"invoice_app", "TRIGGER", false},
		{"invoice_tenant_reader", "SELECT", false}, {"invoice_tenant_reader", "INSERT", false},
	} {
		var got bool
		if err := tx.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.extraction_rule_breaks', $2)`, c.role, c.priv).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("%s %s on extraction_rule_breaks after Down/Up = %v, want %v", c.role, c.priv, got, c.want)
		}
	}

	if got := erbReasonCheckDef(t, ctx, tx); got != efrFiveCodeReasonCheck {
		t.Errorf("reason CHECK after Down/Up:\n got: %s\nwant: %s", got, efrFiveCodeReasonCheck)
	}

	// Behaviour of the replayed policy, as the owner under FORCE. Last: the duplicate aborts the tx.
	setTenant := func(id string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, id); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
	}
	setTenant(h.tenantA)
	if err := insertRuleBreak(ctx, tx, uuid.NewString(), h.tenantA, jobA, erbField, version, erbRuleKey, erbMessage); err != nil {
		t.Fatalf("insert into the replayed table: %v", err)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks`); n != 1 {
		t.Errorf("rows visible to tenant A after Down/Up = %d, want 1", n)
	}
	setTenant(h.tenantB)
	if n := mustCount(t, tx, `SELECT count(*) FROM extraction_rule_breaks`); n != 0 {
		t.Errorf("rows visible to tenant B after Down/Up = %d, want 0", n)
	}
	setTenant(h.tenantA)
	err := insertRuleBreak(ctx, tx, uuid.NewString(), h.tenantA, jobA, erbField, version, erbRuleKey, erbMessage)
	if code, name := pgCode(err), pgConstraint(err); code != "23505" || name != erbOnePerRule {
		t.Errorf("duplicate on the replayed table: SQLSTATE %q on %q, want 23505 on %s", code, name, erbOnePerRule)
	}
}
