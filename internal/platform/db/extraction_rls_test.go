// RLS, grant and constraint suites for `extraction_jobs` and `extraction_field_results`.
// failIfUndefinedExtractionJobs / failIfUndefinedFieldCorrections turn a not-yet-migrated
// table into a self-explaining 42P01 message instead of a raw driver error — the shape every
// case here degrades to if run against a DB missing that table's migration.
//
// Rows are seeded per test, never in harness.seed(): a missing table must fail only these
// cases, not the whole package. Each rejected statement gets its own db.WithinTenantTx,
// because a failed statement poisons the surrounding transaction.
//
// Run: `DEV_DB_PORT=5434 make test-rls`. requireHarness skips without the four per-role
// DSNs, and a skip is itself a failure under scripts/ci/rls-test-gate.sh — so no case here
// adds a t.Skip of its own.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// The provenance stamp every insert must supply: both columns are NOT NULL with a
// char_length > 0 CHECK. Their values carry no meaning for any assertion here.
const (
	ejExtractor        = "mock"
	ejExtractorVersion = "v1"
)

// failIfUndefinedExtractionJobs turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedExtractionJobs(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_jobs migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedExtractionJob inserts one row as the superuser (BYPASSRLS, so seeding needs neither
// tenant context nor an INSERT grant) and returns its id plus a cleanup func.
func seedExtractionJob(t *testing.T, tenantID, documentID string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, documentID, ejExtractor, ejExtractorVersion,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_jobs: undefined_table (42P01) — extraction_jobs migration not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_jobs: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = $1`, id)
	}
}

// EJ-01: the catalog half of the isolation posture. ENABLE alone would let the owner bypass
// the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ExtractionJobsForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.extraction_jobs'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedExtractionJobs(t, "read pg_class for extraction_jobs", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for extraction_jobs: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("extraction_jobs relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
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
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'extraction_jobs'`)
	if err != nil {
		t.Fatalf("query pg_policies for extraction_jobs: %v", err)
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
		t.Errorf("policies on extraction_jobs = %d (%v), want exactly 1 (tenant_isolation) — this "+
			"table has no cross-tenant enumeration reader", len(got), got)
	}

	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatal("no tenant_isolation policy on extraction_jobs — the migration is not applied yet")
	}
	if strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation roles = %v, want [public] (no TO clause — it must bind every role)", iso.roles)
	}
	if iso.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want %q (its USING must double as the INSERT WITH CHECK)", iso.cmd, "ALL")
	}
	if !strings.Contains(iso.qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC", iso.qual)
	}
}

// EJ-02: cross-tenant SELECT is refused. The unfiltered count is the load-bearing half — a
// tenant_id-filtered query would come out right even if RLS did nothing.
func TestRLS_ExtractionJobsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EJ-02/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EJ-02/b.pdf")
	defer cleanupDocB()

	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB1, cleanupJobB1 := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB1()
	jobB2, cleanupJobB2 := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB2()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		// By id, not by tenant_id: a tenant_id predicate would prove nothing about RLS.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, jobA); n != 1 {
			t.Errorf("A's own job visible to A = %d, want 1", n)
		}
		for _, id := range []string{jobB1, jobB2} {
			if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 0 {
				t.Errorf("B's job %s visible to A = %d, want 0", id, n)
			}
		}
		// RLS is the only thing narrowing this one: B seeded two more rows.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// EJ-03: an INSERT naming tenant B while scoped to A is refused with 42501 and lands no row.
// The positive half stops this passing against a policy written USING (false).
func TestRLS_ExtractionJobsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EJ-03/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EJ-03/b.pdf")
	defer cleanupDocB()

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_jobs WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			crossID, h.tenantB, docB, ejExtractor, ejExtractorVersion)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the same statement shape succeeds for A's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			ownID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
		return e
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// EJ-04: with app.current_tenant unset the isolation predicate is NULL, so the connection
// reads nothing and writes nothing. The positive re-read stops "zero rows" being an
// artefact of an empty table.
func TestRLS_ExtractionJobsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-04/a.pdf")
	defer cleanupDoc()
	jobID, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	strayID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = $1`, strayID)
	}()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs`); n != 0 {
		t.Errorf("extraction_jobs visible with no tenant set = %d, want 0", n)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		strayID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
	if err == nil {
		t.Fatal("INSERT with no tenant context succeeded, want RLS WITH CHECK violation (SQLSTATE 42501)")
	}
	assertRLSViolation(t, err)

	// The row genuinely exists and is genuinely readable — with the GUC set.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, jobID); n != 1 {
			t.Errorf("seeded row visible WITH tenant context = %d, want 1 (the zero above must come "+
				"from the missing GUC, not from an empty table)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (positive half): %v", err)
	}
}

// EJ-05: the table owner (invoice_migrator) is bound by the policy too. This is the case
// only FORCE ROW LEVEL SECURITY makes pass; ENABLE alone lets the owner straight through.
func TestRLS_ExtractionJobsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EJ-05/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EJ-05/b.pdf")
	defer cleanupDocB()

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_jobs WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			crossID, h.tenantB, docB, ejExtractor, ejExtractorVersion)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half: the owner can write inside its own tenant scope, so the 42501 above is
	// isolation and not a missing owner privilege.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			ownID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
		return e
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: want success, got: %v", err)
	}
}

// EJ-06: reassigning an own, visible row to another tenant is refused. Catches a policy
// whose USING stopped being applied as the UPDATE WITH CHECK.
func TestRLS_ExtractionJobsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-06/a.pdf")
	defer cleanupDoc()
	jobID, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE extraction_jobs SET tenant_id = $1 WHERE id = $2`, h.tenantB, jobID)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "own-row tenant reassignment", err) {
		return
	}
	assertRLSViolation(t, err)

	var stillTenant string
	if err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM extraction_jobs WHERE id = $1`, jobID).
		Scan(&stillTenant); err != nil {
		t.Fatalf("read back tenant_id after the refused UPDATE: %v", err)
	}
	if stillTenant != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %q, want unchanged %q", stillTenant, h.tenantA)
	}

	// Positive half, own tx: an in-tenant column UPDATE on the same row succeeds.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE extraction_jobs SET state = 'extracting' WHERE id = $1`, jobID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("in-tenant UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("in-tenant UPDATE of state: want success, got: %v", err)
	}
}

// EJ-07: the state CHECK admits exactly the extraction vocabulary. All five are inserted AND
// read back, so a CHECK that silently coerced would not pass. "submitting" is the pointed
// negative: it belongs to submission_jobs, so a copy-pasted CHECK fails here.
func TestRLS_ExtractionJobsStateCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-07/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	for _, state := range []string{"queued", "extracting", "succeeded", "failed", "dead_lettered"} {
		id := uuid.NewString()
		probes = append(probes, id)
		var got string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, state)
				 VALUES ($1, $2, $3, $4, $5, $6) RETURNING state`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, state).Scan(&got)
		})
		if failIfUndefinedExtractionJobs(t, "INSERT state "+state, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with state %q: want success (it is one of the five legal states), got: %v", state, err)
			continue
		}
		if got != state {
			t.Errorf("state round-trip = %q, want %q", got, state)
		}
	}

	// "submitting" and "extracted" are the near misses; each gets its own tx so one
	// unexpected acceptance cannot poison the rest.
	for _, bogus := range []string{"submitting", "extracted"} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, state)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, bogus)
			return e
		})
		if err == nil {
			t.Errorf("INSERT with state %q succeeded, want CHECK violation (SQLSTATE 23514)", bogus)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with state %q: SQLSTATE = %q, want 23514 (check_violation): %v", bogus, code, err)
		}
	}
}

// EJ-08: attempts is bounded below. A retry counter that could go negative would make the
// worker's backoff arithmetic silently wrong rather than loud.
func TestRLS_ExtractionJobsAttemptsNonNegative(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-08/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	negID := uuid.NewString()
	probes = append(probes, negID)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, attempts)
			 VALUES ($1, $2, $3, $4, $5, -1)`,
			negID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "INSERT attempts = -1", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with attempts = -1 succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT with attempts = -1: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, negID); n != 0 {
		t.Errorf("rows after the refused negative-attempts INSERT = %d, want 0", n)
	}

	// Positive half: the boundary value 0 and an ordinary positive count both round-trip,
	// so the CHECK bounds the column without pinning it to one value.
	for _, want := range []int{0, 7} {
		id := uuid.NewString()
		probes = append(probes, id)
		var got int
		if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, attempts)
				 VALUES ($1, $2, $3, $4, $5, $6) RETURNING attempts`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, want).Scan(&got)
		}); err != nil {
			t.Errorf("INSERT with attempts = %d: want success, got: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("attempts round-trip = %d, want %d", got, want)
		}
	}
}

// EJ-09: the provenance stamp cannot be blank. NOT NULL alone would let "" through, and an
// unattributable row is one nothing can later trace to the extractor that wrote it.
func TestRLS_ExtractionJobsExtractorNotBlank(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-09/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what      string
		extractor string
		version   string
	}{
		{"blank extractor", "", ejExtractorVersion},
		{"blank extractor_version", ejExtractor, ""},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
				 VALUES ($1, $2, $3, $4, $5)`,
				id, h.tenantA, docA, c.extractor, c.version)
			return e
		})
		if failIfUndefinedExtractionJobs(t, "INSERT "+c.what, err) {
			return
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.what, code, err)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused %s INSERT = %d, want 0", c.what, n)
		}
	}

	// Positive half: both stamps non-empty is accepted, so the 23514s are about blankness.
	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			okID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
		return e
	}); err != nil {
		t.Fatalf("INSERT with both stamps non-empty: want success, got: %v", err)
	}
}

// EJ-10: the composite FK is the whole point. Referential checks run with RLS bypassed, so a
// bare document_id -> documents(id) FK would accept another tenant's document. Attempted as
// the superuser on purpose, so no policy is in the way and the FK is what refuses.
func TestRLS_ExtractionJobsCrossTenantDocumentRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EJ-10/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EJ-10/b.pdf")
	defer cleanupDocB()

	danglingID := uuid.NewString()
	okID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_jobs WHERE id IN ($1, $2)`, danglingID, okID)
	}()

	_, err := h.super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		danglingID, h.tenantA, docB, ejExtractor, ejExtractorVersion)
	if failIfUndefinedExtractionJobs(t, "cross-tenant document reference", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT of a tenant-A job pointing at tenant B's document succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) — a single-column document_id FK would let " +
			"this through, which is the bug the composite FK exists to close")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant document reference: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, danglingID); n != 0 {
		t.Errorf("rows after the refused cross-tenant document reference = %d, want 0", n)
	}

	// Positive half: A's own document is accepted by the very same FK.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		okID, h.tenantA, docA, ejExtractor, ejExtractorVersion); err != nil {
		t.Fatalf("same-tenant document reference: want success, got: %v", err)
	}
}

// EJ-11: 23001 exactly, not 23503. An explicit ON DELETE RESTRICT is checked at the DELETE;
// an implicit NO ACTION defers and answers 23503 — accepting both would let that downgrade
// pass. The constraint name proves which FK refused.
func TestRLS_ExtractionJobsDocumentDeleteRestricted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	referenced, cleanupReferenced := seedDocument(t, h.tenantA, "EJ-11/referenced.pdf")
	defer cleanupReferenced()
	unreferenced, cleanupUnreferenced := seedDocument(t, h.tenantA, "EJ-11/unreferenced.pdf")
	defer cleanupUnreferenced() // no-op once the positive half removes it
	jobID, cleanupJob := seedExtractionJob(t, h.tenantA, referenced)
	defer cleanupJob() // LIFO: clears the RESTRICT before the document cleanup runs

	_, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, referenced)
	if err == nil {
		t.Fatal("deleting a document an extraction_jobs row still cites succeeded, want " +
			"restrict_violation (SQLSTATE 23001)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("delete a cited document: SQLSTATE = %q, want 23001 (restrict_violation) — 23503 means "+
			"the FK was written ON DELETE NO ACTION, not RESTRICT: %v", code, err)
	}
	if name := pgConstraint(err); name != "extraction_jobs_tenant_document_fk" {
		t.Errorf("delete a cited document: constraint = %q, want %q", name, "extraction_jobs_tenant_document_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM documents WHERE id = $1`, referenced); n != 1 {
		t.Errorf("document rows after the refused DELETE = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, jobID); n != 1 {
		t.Errorf("extraction_jobs rows after the refused DELETE = %d, want 1", n)
	}

	// Positive half: an uncited document deletes cleanly, so the refusal is about the
	// reference and not a blanket one.
	ct, err := h.super.Exec(ctx, `DELETE FROM documents WHERE id = $1`, unreferenced)
	if err != nil {
		t.Fatalf("delete an uncited document: want success, got: %v", err)
	}
	if ct.RowsAffected() != 1 {
		t.Errorf("delete of an uncited document affected %d rows, want 1", ct.RowsAffected())
	}
}

// EJ-12: updated_at is trigger-maintained, not writer-set. The UPDATE names only state, yet
// updated_at must move; created_at must not. Read as the superuser so the comparison is a
// pure property of the trigger, unentangled from RLS.
func TestRLS_ExtractionJobsUpdatedAtBumpedByTrigger(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-12/a.pdf")
	defer cleanupDoc()
	jobID, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var beforeCreated, beforeUpdated time.Time
	var beforeState string
	err := h.super.QueryRow(ctx,
		`SELECT created_at, updated_at, state FROM extraction_jobs WHERE id = $1`, jobID,
	).Scan(&beforeCreated, &beforeUpdated, &beforeState)
	if failIfUndefinedExtractionJobs(t, "read timestamps before the UPDATE", err) {
		return
	}
	if err != nil {
		t.Fatalf("read timestamps before the UPDATE: %v", err)
	}
	if beforeState != "queued" {
		t.Fatalf("seeded state = %q, want %q — the transition below assumes a fresh row", beforeState, "queued")
	}

	// Deliberately does not name updated_at. now() is the transaction timestamp, so this
	// separate transaction is guaranteed a later value than the seeding one.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE extraction_jobs SET state = 'extracting' WHERE id = $1`, jobID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("UPDATE state (not naming updated_at): %v", err)
	}

	var afterCreated, afterUpdated time.Time
	var afterState string
	if err := h.super.QueryRow(ctx,
		`SELECT created_at, updated_at, state FROM extraction_jobs WHERE id = $1`, jobID,
	).Scan(&afterCreated, &afterUpdated, &afterState); err != nil {
		t.Fatalf("read timestamps after the UPDATE: %v", err)
	}
	if afterState != "extracting" {
		t.Errorf("state after the UPDATE = %q, want %q (the UPDATE itself must have landed)", afterState, "extracting")
	}
	if !afterUpdated.After(beforeUpdated) {
		t.Errorf("updated_at after an UPDATE that does not name it = %s, want strictly later than %s — "+
			"the BEFORE UPDATE trigger is missing or not firing", afterUpdated, beforeUpdated)
	}
	if !afterCreated.Equal(beforeCreated) {
		t.Errorf("created_at after the UPDATE = %s, want unchanged %s", afterCreated, beforeCreated)
	}
}

// EJ-13: extraction_jobs_tenant_id_id_uq is a real pg_constraint row over exactly
// (tenant_id, id) in that order — a bare CREATE UNIQUE INDEX produces no pg_constraint row
// and cannot be a composite-FK target. contype is filtered because PG18 also stores NOT NULL
// constraints here, as contype "n".
func TestRLS_ExtractionJobsTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	n, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_jobs'::regclass
		    AND contype = 'u' AND conname = 'extraction_jobs_tenant_id_id_uq'`)
	if failIfUndefinedExtractionJobs(t, "query pg_constraint for extraction_jobs_tenant_id_id_uq", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_constraint for extraction_jobs_tenant_id_id_uq: %v", err)
	}
	if n != 1 {
		t.Fatalf("UNIQUE constraints on extraction_jobs named extraction_jobs_tenant_id_id_uq = %d, want 1 — "+
			"not found; the migration is not applied yet, or it declared a bare CREATE UNIQUE INDEX "+
			"(no pg_constraint row, unusable as a composite-FK target)", n)
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname
		   FROM pg_constraint c
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		  WHERE c.conrelid = 'public.extraction_jobs'::regclass
		    AND c.contype = 'u' AND c.conname = 'extraction_jobs_tenant_id_id_uq'
		  ORDER BY k.ord`)
	if err != nil {
		t.Fatalf("query the constraint's columns: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if e := rows.Scan(&col); e != nil {
			t.Fatalf("scan constraint column: %v", e)
		}
		cols = append(cols, col)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate constraint columns: %v", e)
	}

	want := []string{"tenant_id", "id"}
	if len(cols) != len(want) {
		t.Fatalf("extraction_jobs_tenant_id_id_uq columns = %v, want %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("extraction_jobs_tenant_id_id_uq column %d = %q, want %q (order is load-bearing: "+
				"a composite FK must name (tenant_id, id) in this order)", i+1, cols[i], want[i])
		}
	}
}

// EJ-14: every non-primary index leads with tenant_id, so no lookup path can plan across
// tenants. The primary key is on (id) and is excluded on purpose — including it would fail
// against a correct migration. The non-empty check comes first: an empty list satisfies
// every assertion inside the loop.
func TestRLS_ExtractionJobsEveryIndexLeadsWithTenantId(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT i.relname, a.attname
		   FROM pg_index x
		   JOIN pg_class i ON i.oid = x.indexrelid
		   JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = x.indkey[0]
		  WHERE x.indrelid = 'public.extraction_jobs'::regclass
		    AND NOT x.indisprimary
		  ORDER BY i.relname`)
	if failIfUndefinedExtractionJobs(t, "query pg_index for extraction_jobs", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_index for extraction_jobs: %v", err)
	}
	defer rows.Close()

	firstCol := map[string]string{}
	for rows.Next() {
		var index, col string
		if e := rows.Scan(&index, &col); e != nil {
			t.Fatalf("scan pg_index row: %v", e)
		}
		firstCol[index] = col
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_index rows: %v", e)
	}

	if len(firstCol) == 0 {
		t.Fatalf("non-primary indexes on extraction_jobs = 0, want 2 — the migration is not applied "+
			"yet, or it shipped neither extraction_jobs_tenant_id_id_uq nor "+
			"extraction_jobs_tenant_document_idx: %v", firstCol)
	}
	for _, want := range []string{"extraction_jobs_tenant_id_id_uq", "extraction_jobs_tenant_document_idx"} {
		if _, ok := firstCol[want]; !ok {
			t.Errorf("no index named %q on extraction_jobs; got %v", want, firstCol)
		}
	}
	for index, col := range firstCol {
		if col != "tenant_id" {
			t.Errorf("index %q leads with %q, want tenant_id — a lookup path that does not lead with "+
				"tenant_id plans across tenants", index, col)
		}
	}
}

// EJ-15: least privilege, asked as the superuser on purpose —
// information_schema.role_table_grants shows only the current role's own grants, so the
// "reader holds nothing" half cannot be proven from the app pool.
func TestRLS_ExtractionJobsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
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
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.extraction_jobs', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedExtractionJobs(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, extraction_jobs, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, extraction_jobs, %q) = %v, want %v — the grant is exactly "+
				"SELECT, INSERT, UPDATE to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// L-08: layout_fingerprint is nullable, but any non-NULL value must be non-empty and no
// longer than 128 bytes.
func TestRLS_ExtractionJobsLayoutFingerprintBounds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "L-08/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what        string
		fingerprint any // nil encodes SQL NULL
		wantErr     bool
	}{
		{"an empty layout_fingerprint", "", true},
		{"a 129-character layout_fingerprint", strings.Repeat("a", 129), true},
		{"NULL", nil, false},
		{"a 67-byte v1: fingerprint", "v1:" + strings.Repeat("a", 64), false},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_fingerprint)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, c.fingerprint)
			return e
		})
		if !c.wantErr {
			if err != nil {
				t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514: %v", c.what, code, err)
			continue
		}
		if got := pgConstraint(err); got != "extraction_jobs_layout_fingerprint_check" {
			t.Errorf("INSERT with %s tripped constraint %q, want extraction_jobs_layout_fingerprint_check", c.what, got)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused %s = %d, want 0", c.what, n)
		}
	}
}

// L-09: layout_anchors is nullable, but any non-NULL value must be a JSON array — a JSON
// object, a JSON scalar and the JSON null literal (jsonb 'null', distinct from SQL NULL) are
// all refused.
func TestRLS_ExtractionJobsLayoutAnchorsMustBeAnArray(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "L-09/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what    string
		anchors any // nil encodes SQL NULL; a Go string is cast ::jsonb
		wantErr bool
	}{
		{"a JSON object", `{}`, true},
		{"a JSON string scalar", `"x"`, true},
		{"the JSON null literal", `null`, true},
		{"NULL", nil, false},
		{"an empty JSON array", `[]`, false},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_anchors)
				 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, c.anchors)
			return e
		})
		if !c.wantErr {
			if err != nil {
				t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514: %v", c.what, code, err)
			continue
		}
		if got := pgConstraint(err); got != "extraction_jobs_layout_anchors_check" {
			t.Errorf("INSERT with %s tripped constraint %q, want extraction_jobs_layout_anchors_check", c.what, got)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused %s = %d, want 0", c.what, n)
		}
	}
}

// QA (Mode B): L-08 pins the 129-char rejection but never the 128-char acceptance, so a
// migration that shrank the ceiling to, say, 64 would still pass it in full.
func TestRLS_ExtractionJobsLayoutFingerprintAcceptsTheOneTwentyEightCeiling(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "L-08/ceiling.pdf")
	defer cleanupDoc()

	at128 := "v1:" + strings.Repeat("a", 125)
	if len(at128) != 128 {
		t.Fatalf("test setup: the at-ceiling fingerprint is %d chars, want 128", len(at128))
	}

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = $1`, id)
	}()
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_fingerprint)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			id, h.tenantA, docA, ejExtractor, ejExtractorVersion, at128)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "INSERT with a 128-char layout_fingerprint", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT with a 128-char layout_fingerprint: want success (the ceiling is inclusive), got: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 1 {
		t.Errorf("rows after the 128-char layout_fingerprint = %d, want 1", n)
	}
}

// EJ-16: two jobs for the same (tenant_id, document_id) both persist. Pins D-6: a unique
// index here would turn a tolerated duplicate into a runtime 23505 on a path where nothing
// is ready to render it.
func TestRLS_ExtractionJobsNoUniqueOnDocument(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-16/a.pdf")
	defer cleanupDoc()
	firstID, cleanupFirst := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupFirst()

	secondID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = $1`, secondID)
	}()

	// Through the app path, where a 23505 would actually surface at runtime.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			secondID, h.tenantA, docA, ejExtractor, ejExtractorVersion)
		return e
	}); err != nil {
		if code := pgCode(err); code == "23505" {
			t.Fatalf("a second extraction job for the same document was refused with 23505 — there must "+
				"be no UNIQUE over (tenant_id, document_id): %v", err)
		}
		t.Fatalf("second extraction job for the same document: want success, got: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		for _, id := range []string{firstID, secondID} {
			if n := mustCount(t, tx, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 1 {
				t.Errorf("job %s visible after the second insert = %d, want 1", id, n)
			}
		}
		if n := mustCount(t, tx,
			`SELECT count(*) FROM extraction_jobs WHERE document_id = $1`, docA); n != 2 {
			t.Errorf("jobs for the same document = %d, want 2", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify both jobs persist: %v", err)
	}
}

// EJ-17: the state CHECK admits EXACTLY the five values, read off the catalog. EJ-07 probes
// two near misses by hand, so it cannot see a sixth value added alongside the five.
func TestRLS_ExtractionJobsStateCheckIsExactlyFiveValues(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT DISTINCT c.conname, pg_get_constraintdef(c.oid)
		   FROM pg_constraint c
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		  WHERE c.conrelid = 'public.extraction_jobs'::regclass
		    AND c.contype = 'c' AND a.attname = 'state'`)
	if failIfUndefinedExtractionJobs(t, "query the state CHECK definition", err) {
		return
	}
	if err != nil {
		t.Fatalf("query the state CHECK definition: %v", err)
	}
	defer rows.Close()

	var defs []string
	for rows.Next() {
		var name, def string
		if e := rows.Scan(&name, &def); e != nil {
			t.Fatalf("scan constraint definition: %v", e)
		}
		defs = append(defs, def)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate constraint definitions: %v", e)
	}
	if len(defs) != 1 {
		t.Fatalf("CHECK constraints over extraction_jobs.state = %d (%v), want exactly 1 — a second one "+
			"would make the value set below only half the story", len(defs), defs)
	}

	got := regexp.MustCompile(`'([^']*)'::text`).FindAllStringSubmatch(defs[0], -1)
	var values []string
	for _, m := range got {
		values = append(values, m[1])
	}
	if len(values) == 0 {
		t.Fatalf("no quoted values parsed out of %q — the parse is broken, so the comparison below "+
			"would pass vacuously", defs[0])
	}
	sort.Strings(values)

	want := []string{"dead_lettered", "extracting", "failed", "queued", "succeeded"}
	if !slices.Equal(values, want) {
		t.Errorf("extraction_jobs.state CHECK admits %v, want exactly %v — the vocabulary is closed, and "+
			"a sixth value is a state the worker has no branch for\ndefinition: %s", values, want, defs[0])
	}
}

// EJ-18: an UPDATE that names another tenant's row by id touches nothing. EJ-06 covers
// reassigning an OWN row; this covers reaching a row RLS should have hidden from the scan.
func TestRLS_ExtractionJobsCrossTenantUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EJ-18/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EJ-18/b.pdf")
	defer cleanupDocB()

	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	// Both ids in one predicate: RLS is the only thing that can exclude B's row, and the
	// statement cannot reach any row this test did not seed.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx,
			`UPDATE extraction_jobs SET state = 'failed' WHERE id = ANY($1)`,
			[]string{jobA, jobB})
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("UPDATE naming both tenants' rows affected %d, want 1 (A's own row only)", ct.RowsAffected())
		}
		return nil
	})
	if failIfUndefinedExtractionJobs(t, "cross-tenant UPDATE", err) {
		return
	}
	if err != nil {
		t.Fatalf("cross-tenant UPDATE: want silent no-op on B's row, got: %v", err)
	}

	for _, c := range []struct{ id, want string }{
		{jobA, "failed"}, // positive half: the statement did land where it was allowed
		{jobB, "queued"}, // B's row never moved
	} {
		var state string
		if e := h.super.QueryRow(ctx, `SELECT state FROM extraction_jobs WHERE id = $1`, c.id).Scan(&state); e != nil {
			t.Fatalf("read back state for %s: %v", c.id, e)
		}
		if state != c.want {
			t.Errorf("state of %s after A's UPDATE = %q, want %q", c.id, state, c.want)
		}
	}
}

// EJ-19: which columns a row may omit. Probed as the superuser so NOT NULL is what refuses
// and no policy is in the way. The nullable half stops this being a blanket "everything is
// required", which would be a different (and wrong) schema.
func TestRLS_ExtractionJobsColumnNullabilityAndDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-19/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	// Each statement names its columns exactly once: the four required values are supplied
	// positionally, and the column under test is the one carrying NULL.
	for _, c := range []struct {
		col  string
		cols string
		vals string
	}{
		{"tenant_id", "id, tenant_id, document_id, extractor, extractor_version", "$1, NULL, $2, $3, $4"},
		{"document_id", "id, document_id, tenant_id, extractor, extractor_version", "$1, NULL, $2, $3, $4"},
		{"extractor", "id, extractor, tenant_id, document_id, extractor_version", "$1, NULL, $2, $3, $4"},
		{"extractor_version", "id, extractor_version, tenant_id, document_id, extractor", "$1, NULL, $2, $3, $4"},
		{"state", "id, state, tenant_id, document_id, extractor, extractor_version", "$1, NULL, $2, $3, $4, $5"},
		{"attempts", "id, attempts, tenant_id, document_id, extractor, extractor_version", "$1, NULL, $2, $3, $4, $5"},
		{"created_at", "id, created_at, tenant_id, document_id, extractor, extractor_version", "$1, NULL, $2, $3, $4, $5"},
		{"updated_at", "id, updated_at, tenant_id, document_id, extractor, extractor_version", "$1, NULL, $2, $3, $4, $5"},
	} {
		col := c.col
		id := uuid.NewString()
		probes = append(probes, id)
		args := []any{id, h.tenantA, docA, ejExtractor, ejExtractorVersion}
		if strings.Count(c.vals, "$") == 4 {
			// tenant_id/document_id/extractor/extractor_version each drop their own value.
			switch col {
			case "tenant_id":
				args = []any{id, docA, ejExtractor, ejExtractorVersion}
			case "document_id":
				args = []any{id, h.tenantA, ejExtractor, ejExtractorVersion}
			case "extractor":
				args = []any{id, h.tenantA, docA, ejExtractorVersion}
			case "extractor_version":
				args = []any{id, h.tenantA, docA, ejExtractor}
			}
		}
		_, err := h.super.Exec(ctx,
			`INSERT INTO extraction_jobs (`+c.cols+`) VALUES (`+c.vals+`)`, args...)
		if failIfUndefinedExtractionJobs(t, "INSERT NULL "+col, err) {
			return
		}
		if err == nil {
			t.Errorf("INSERT with %s = NULL succeeded, want not_null_violation (SQLSTATE 23502)", col)
			continue
		}
		if code := pgCode(err); code != "23502" {
			t.Errorf("INSERT with %s = NULL: SQLSTATE = %q, want 23502 (not_null_violation): %v", col, code, err)
		}
	}

	// The two genuinely optional columns, plus the defaults a bare insert must produce.
	bare := uuid.NewString()
	probes = append(probes, bare)
	var lastError *string
	var riverJobID *int64
	var state string
	var attempts int
	var created, updated time.Time
	if err := h.super.QueryRow(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING last_error, river_job_id, state, attempts, created_at, updated_at`,
		bare, h.tenantA, docA, ejExtractor, ejExtractorVersion,
	).Scan(&lastError, &riverJobID, &state, &attempts, &created, &updated); err != nil {
		t.Fatalf("bare INSERT naming only the required columns: want success, got: %v", err)
	}
	if lastError != nil {
		t.Errorf("last_error on a bare insert = %q, want NULL — a job that has not failed has no error", *lastError)
	}
	if riverJobID != nil {
		t.Errorf("river_job_id on a bare insert = %d, want NULL — the row exists before the job is enqueued", *riverJobID)
	}
	if state != "queued" || attempts != 0 {
		t.Errorf("bare insert defaults = state %q / attempts %d, want %q / 0", state, attempts, "queued")
	}
	if !created.Equal(updated) {
		t.Errorf("created_at %s and updated_at %s differ on insert, want equal — both default to the same "+
			"transaction now(), and EJ-12's strictly-later assertion depends on it", created, updated)
	}

	// The optional columns accept a value, and attempts is bounded below only.
	filled := uuid.NewString()
	probes = append(probes, filled)
	var gotErr string
	var gotRiver int64
	var gotAttempts int
	if err := h.super.QueryRow(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version,
		     last_error, river_job_id, attempts)
		 VALUES ($1, $2, $3, $4, $5, 'boom', 4242, 2147483647)
		 RETURNING last_error, river_job_id, attempts`,
		filled, h.tenantA, docA, ejExtractor, ejExtractorVersion,
	).Scan(&gotErr, &gotRiver, &gotAttempts); err != nil {
		t.Fatalf("INSERT filling the optional columns: want success, got: %v", err)
	}
	if gotErr != "boom" || gotRiver != 4242 || gotAttempts != 2147483647 {
		t.Errorf("optional columns round-tripped as %q/%d/%d, want %q/%d/%d",
			gotErr, gotRiver, gotAttempts, "boom", 4242, 2147483647)
	}
}

// EJ-20: the runtime consequence of the grant matrix. EJ-15 reads has_table_privilege; this
// issues the DELETE the app would issue, so a future GRANT DELETE shows up as a behaviour
// change and not only as a catalog diff.
func TestRLS_ExtractionJobsAppHoldsNoDeletePath(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EJ-20/a.pdf")
	defer cleanupDoc()
	jobID, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM extraction_jobs WHERE id = $1`, jobID)
		return e
	})
	if failIfUndefinedExtractionJobs(t, "app DELETE", err) {
		return
	}
	if err == nil {
		t.Fatal("invoice_app deleted an extraction_jobs row, want permission denied (SQLSTATE 42501) — " +
			"the demo purge runs as superuser precisely because no request path may delete here")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app DELETE: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, jobID); n != 1 {
		t.Errorf("rows after the refused DELETE = %d, want 1", n)
	}

	// Positive half, own tx: the same role CAN update the same row, so the 42501 is about
	// DELETE and not about the row being unreachable.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE extraction_jobs SET state = 'extracting' WHERE id = $1`, jobID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("in-tenant UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("in-tenant UPDATE by the same role: want success, got: %v", err)
	}
}

// shippedDownStatements returns the statements of one migration's real `-- +goose Down`
// block, read from the embedded FS.
func shippedDownStatements(t *testing.T, glob string) []string {
	t.Helper()

	matches, err := fs.Glob(migrations.FS, glob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file", glob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	_, after, found := strings.Cut(string(raw), "-- +goose Down")
	if !found {
		t.Fatalf("%s has no `-- +goose Down` marker", matches[0])
	}

	// Comments come out line-wise BEFORE the split: a prose semicolon inside one would
	// otherwise cut the block mid-sentence and leave the tail glued to the next statement.
	var body []string
	for _, line := range strings.Split(after, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			body = append(body, l)
		}
	}
	var stmts []string
	for _, s := range strings.Split(strings.Join(body, " "), ";") {
		if s := strings.TrimSpace(s); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}

// EJ-21: the Down is complete. DROP TABLE takes the policy, indexes, constraints, grants and
// trigger with it, but NOT the trigger function — and a reversibility round-trip cannot see
// the leak, because the Up re-runs CREATE OR REPLACE FUNCTION over the survivor. The shipped
// Down text is executed for real, inside a transaction that is always rolled back.
func TestRLS_ExtractionJobsDownDropsTableAndFunction(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	stmts := shippedDownStatements(t, "*_extraction_jobs.sql")
	if len(stmts) != 2 {
		t.Fatalf("parsed %d statement(s) out of the Down block (%v), want 2 — DROP TABLE and DROP FUNCTION",
			len(stmts), stmts)
	}

	const fn = "extraction_jobs_touch_updated_at"
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_proc WHERE proname = $1`, fn); n != 1 {
		t.Fatalf("pg_proc rows named %s before the Down = %d, want 1 — the assertion below would pass "+
			"vacuously against a function that was never there", fn, n)
	}

	// The migrator owns both objects, so it is the identity goose runs the Down as.
	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		if e := tx.Rollback(context.Background()); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Errorf("rollback the Down probe: %v", e)
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}

	// goose unwinds newest-first, so every dependent table's own Down has already run by
	// the time this one does. Reproduce that order rather than dropping the children by
	// hand — a later child migration only has to be listed here.
	for _, child := range []string{"*_extraction_field_results.sql", "*_extraction_field_corrections.sql"} {
		for _, s := range shippedDownStatements(t, child) {
			if _, err := tx.Exec(ctx, s); err != nil {
				t.Fatalf("execute the shipped Down statement %q from %s: %v", s, child, err)
			}
		}
	}

	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("execute the shipped Down statement %q: %v — a 2BP01 here means a new table "+
				"references extraction_jobs and its own migration's Down belongs in the child list above", s, err)
		}
	}

	var stillThere *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.extraction_jobs')::text`).Scan(&stillThere); err != nil {
		t.Fatalf("to_regclass after the Down: %v", err)
	}
	if stillThere != nil {
		t.Errorf("extraction_jobs still exists after the Down, want dropped")
	}
	var fnRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname = $1`, fn).Scan(&fnRows); err != nil {
		t.Fatalf("count pg_proc after the Down: %v", err)
	}
	if fnRows != 0 {
		t.Errorf("%s survives the Down (%d row(s)), want 0 — DROP TABLE does not take the trigger "+
			"function with it, so the Down must drop it explicitly", fn, fnRows)
	}
}

// ---------------------------------------------------------------------------
// extraction_field_results (EXTR-01-02). Same RED-first shape as the block above:
// every case here fails 42P01 until Migration B lands.
// ---------------------------------------------------------------------------

// The names every rejection case asserts. Postgres aborts on the FIRST violated CHECK in
// constraint-OID order, so SQLSTATE 23514 alone does not say which constraint refused —
// and a case asserting only the code would silently stop testing its subject if the DDL
// order changed.
const (
	efrRegionComplete  = "extraction_field_results_region_complete"
	efrBboxNormalised  = "extraction_field_results_bbox_normalised"
	efrPageCheck       = "extraction_field_results_page_check"
	efrFieldNameCheck  = "extraction_field_results_field_name_check"
	efrValueCheck      = "extraction_field_results_value_check"
	efrReasonCodeCheck = "extraction_field_results_reason_code_check"
	efrTenantJobFK     = "extraction_field_results_tenant_job_fk"
	efrTenantIDIDUq    = "extraction_field_results_tenant_id_id_uq"
	efrTenantJobIdx    = "extraction_field_results_tenant_job_idx"
)

// The field_name/value pair supplied whenever neither column is the subject.
const (
	efrField = "total_amount"
	efrValue = "100.00"
)

// The canonical INSERT. Every column the constraint cases vary is bound, so one statement
// shape serves them all and no case can differ by accident.
const efrInsert = `INSERT INTO extraction_field_results
	(id, tenant_id, extraction_job_id, field_name, value, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1, reason_code)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

// efrPtr returns a pointer to v. A nil pointer is how these cases write SQL NULL.
func efrPtr[T any](v T) *T { return &v }

// efrRegion is the five all-or-nothing region columns. The zero value is all-NULL, which
// _region_complete accepts.
type efrRegion struct {
	page           *int
	x0, y0, x1, y1 *float64
}

// efrBox is the canonical complete region: page 1 and a box well inside [0,1].
func efrBox() efrRegion {
	return efrRegion{page: efrPtr(1), x0: efrPtr(0.1), y0: efrPtr(0.2), x1: efrPtr(0.3), y1: efrPtr(0.4)}
}

// failIfUndefinedFieldResults turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedFieldResults(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_field_results migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedFieldResult inserts one row as the superuser (BYPASSRLS, so seeding needs neither
// tenant context nor an INSERT grant) and returns its id plus a cleanup func. Seed the
// document and then the extraction job first: this row's composite FK needs both.
func seedFieldResult(t *testing.T, tenantID, jobID, fieldName string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, value)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, jobID, fieldName, efrValue,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_field_results: undefined_table (42P01) — extraction_field_results "+
				"migration not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_field_results: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, id)
	}
}

// insertFieldResult issues the canonical INSERT on tx and returns the driver error
// unchanged, so each caller asserts its own SQLSTATE and constraint name.
func insertFieldResult(ctx context.Context, tx pgx.Tx, id, tenantID, jobID, fieldName string,
	value *string, r efrRegion, reasonCode *string) error {
	_, err := tx.Exec(ctx, efrInsert,
		id, tenantID, jobID, fieldName, value, r.page, r.x0, r.y0, r.x1, r.y1, reasonCode)
	return err
}

// EFR-01: the catalog half of the isolation posture. ENABLE alone would let the owner
// bypass the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ExtractionFieldResultsForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.extraction_field_results'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedFieldResults(t, "read pg_class for extraction_field_results", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for extraction_field_results: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("extraction_field_results relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
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
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'extraction_field_results'`)
	if err != nil {
		t.Fatalf("query pg_policies for extraction_field_results: %v", err)
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
		t.Errorf("policies on extraction_field_results = %d (%v), want exactly 1 (tenant_isolation) — "+
			"this table has no cross-tenant enumeration reader", len(got), got)
	}

	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatal("no tenant_isolation policy on extraction_field_results — the migration is not applied yet")
	}
	if strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation roles = %v, want [public] (no TO clause — it must bind every role)", iso.roles)
	}
	if iso.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want %q (its USING must double as the INSERT WITH CHECK)", iso.cmd, "ALL")
	}
	if !strings.Contains(iso.qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC", iso.qual)
	}
}

// EFR-02: cross-tenant SELECT is refused. The unfiltered count is the load-bearing half — a
// tenant_id-filtered query would come out right even if RLS did nothing.
func TestRLS_ExtractionFieldResultsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFR-02/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFR-02/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	resultA, cleanupResultA := seedFieldResult(t, h.tenantA, jobA, efrField)
	defer cleanupResultA()
	resultB1, cleanupResultB1 := seedFieldResult(t, h.tenantB, jobB, efrField)
	defer cleanupResultB1()
	resultB2, cleanupResultB2 := seedFieldResult(t, h.tenantB, jobB, "vendor_name")
	defer cleanupResultB2()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		// By id, not by tenant_id: a tenant_id predicate would prove nothing about RLS.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, resultA); n != 1 {
			t.Errorf("A's own field result visible to A = %d, want 1", n)
		}
		for _, id := range []string{resultB1, resultB2} {
			if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, id); n != 0 {
				t.Errorf("B's field result %s visible to A = %d, want 0", id, n)
			}
		}
		// RLS is the only thing narrowing this one: B seeded two more rows.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_results`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// EFR-03: an INSERT naming tenant B while scoped to A is refused with 42501 and lands no
// row. The positive half stops this passing against a policy written USING (false).
func TestRLS_ExtractionFieldResultsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFR-03/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFR-03/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, crossID, h.tenantB, jobB, efrField,
			efrPtr(efrValue), efrRegion{}, nil)
	})
	if failIfUndefinedFieldResults(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the same statement shape succeeds for A's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, ownID, h.tenantA, jobA, efrField,
			efrPtr(efrValue), efrRegion{}, nil)
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// EFR-04: with app.current_tenant unset the isolation predicate is NULL, so the connection
// reads nothing and writes nothing. The positive re-read stops "zero rows" being an
// artefact of an empty table.
func TestRLS_ExtractionFieldResultsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-04/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()
	resultID, cleanupResult := seedFieldResult(t, h.tenantA, jobA, efrField)
	defer cleanupResult()

	strayID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, strayID)
	}()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_results`); n != 0 {
		t.Errorf("extraction_field_results visible with no tenant set = %d, want 0", n)
	}
	err = insertFieldResult(ctx, tx, strayID, h.tenantA, jobA, efrField, efrPtr(efrValue), efrRegion{}, nil)
	if err == nil {
		t.Fatal("INSERT with no tenant context succeeded, want RLS WITH CHECK violation (SQLSTATE 42501)")
	}
	assertRLSViolation(t, err)

	// The row genuinely exists and is genuinely readable — with the GUC set.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, resultID); n != 1 {
			t.Errorf("seeded row visible WITH tenant context = %d, want 1 (the zero above must come "+
				"from the missing GUC, not from an empty table)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (positive half): %v", err)
	}
}

// EFR-05: the table owner (invoice_migrator) is bound by the policy too. This is the case
// only FORCE ROW LEVEL SECURITY makes pass; ENABLE alone lets the owner straight through.
func TestRLS_ExtractionFieldResultsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFR-05/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFR-05/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, crossID, h.tenantB, jobB, efrField,
			efrPtr(efrValue), efrRegion{}, nil)
	})
	if failIfUndefinedFieldResults(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the owner's own-tenant INSERT succeeds, so the 42501 above is
	// the policy binding the owner and not a missing privilege.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, ownID, h.tenantA, jobA, efrField,
			efrPtr(efrValue), efrRegion{}, nil)
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: want success, got: %v", err)
	}
}

// EFR-06: the composite FK is the whole point. Referential checks run with RLS bypassed, so
// a bare extraction_job_id FK would accept another tenant's job. Attempted as the superuser
// on purpose, so no policy is in the way and the FK is what refuses.
func TestRLS_ExtractionFieldResultsCrossTenantJobRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFR-06/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFR-06/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	danglingID := uuid.NewString()
	okID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id IN ($1, $2)`, danglingID, okID)
	}()

	_, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, value)
		 VALUES ($1, $2, $3, $4, $5)`,
		danglingID, h.tenantA, jobB, efrField, efrValue)
	if failIfUndefinedFieldResults(t, "cross-tenant job reference", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT of a tenant-A field result pointing at tenant B's job succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) — a single-column extraction_job_id FK would let " +
			"this through, which is the bug the composite FK exists to close")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant job reference: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != efrTenantJobFK {
		t.Errorf("cross-tenant job reference: constraint = %q, want %q", name, efrTenantJobFK)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, danglingID); n != 0 {
		t.Errorf("rows after the refused cross-tenant job reference = %d, want 0", n)
	}

	// Positive half: A's own job is accepted by the very same FK.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, value)
		 VALUES ($1, $2, $3, $4, $5)`,
		okID, h.tenantA, jobA, efrField, efrValue); err != nil {
		t.Fatalf("same-tenant job reference: want success, got: %v", err)
	}
}

// EFR-07: the reason vocabulary. All four codes AND NULL insert and read back, so a CHECK
// that silently coerced would not pass. "low_confidence" is the pointed negative: it is the
// obvious fifth code an extractor would invent, and the review screen has no string for it.
func TestRLS_ExtractionFieldResultsReasonCodeCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-07/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()

	for _, code := range []*string{
		efrPtr("unreadable"), efrPtr("ambiguous"), efrPtr("inconsistent"), efrPtr("missing"), nil,
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		var got *string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO extraction_field_results
				   (id, tenant_id, extraction_job_id, field_name, value, reason_code)
				 VALUES ($1, $2, $3, $4, $5, $6) RETURNING reason_code`,
				id, h.tenantA, jobA, efrField, efrValue, code).Scan(&got)
		})
		if failIfUndefinedFieldResults(t, "INSERT reason_code", err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with reason_code %v: want success (NULL and the four codes are legal), got: %v", code, err)
			continue
		}
		switch {
		case code == nil && got != nil:
			t.Errorf("reason_code round-trip = %q, want NULL (there is no none sentinel)", *got)
		case code != nil && (got == nil || *got != *code):
			t.Errorf("reason_code round-trip = %v, want %q", got, *code)
		}
	}

	// The near miss, its own tx: a fifth code costs a migration by design.
	bogusID := uuid.NewString()
	probes = append(probes, bogusID)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, bogusID, h.tenantA, jobA, efrField,
			efrPtr(efrValue), efrRegion{}, efrPtr("low_confidence"))
	})
	if err == nil {
		t.Fatal("INSERT with reason_code \"low_confidence\" succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT with reason_code \"low_confidence\": SQLSTATE = %q, want 23514: %v", code, err)
	}
	if name := pgConstraint(err); name != efrReasonCodeCheck {
		t.Errorf("INSERT with reason_code \"low_confidence\": constraint = %q, want %q", name, efrReasonCodeCheck)
	}
}

// EFR-08: the reason CHECK admits EXACTLY the four values, read off the catalog. EFR-07
// probes one near miss by hand, so it cannot see a FIFTH code added alongside the four.
func TestRLS_ExtractionFieldResultsReasonCodeIsExactlyFourValues(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT DISTINCT c.conname, pg_get_constraintdef(c.oid)
		   FROM pg_constraint c
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		  WHERE c.conrelid = 'public.extraction_field_results'::regclass
		    AND c.contype = 'c' AND a.attname = 'reason_code'`)
	if failIfUndefinedFieldResults(t, "query the reason_code CHECK definition", err) {
		return
	}
	if err != nil {
		t.Fatalf("query the reason_code CHECK definition: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var name, def string
		if e := rows.Scan(&name, &def); e != nil {
			t.Fatalf("scan constraint definition: %v", e)
		}
		got[name] = def
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate constraint definitions: %v", e)
	}
	if len(got) != 1 {
		t.Fatalf("CHECK constraints over extraction_field_results.reason_code = %d (%v), want exactly 1 — "+
			"a second one would make the comparison below only half the story", len(got), got)
	}

	def, ok := got[efrReasonCodeCheck]
	if !ok {
		t.Fatalf("the reason_code CHECK is named %v, want %q — the auto-name is deterministic and the "+
			"assertion below is keyed on it", got, efrReasonCodeCheck)
	}

	// The rendered text, exactly. Hand-sampling values cannot see a fifth code added
	// alongside the four; this can.
	const want = `CHECK (((reason_code IS NULL) OR (reason_code = ANY ` +
		`(ARRAY['unreadable'::text, 'ambiguous'::text, 'inconsistent'::text, 'missing'::text]))))`
	if def != want {
		t.Errorf("reason_code CHECK definition:\n got: %s\nwant: %s", def, want)
	}
}

// EFR-09: a region is all five columns or none of them — a half-written box points nowhere.
// All ten half-written combinations are enumerated: sampling two of them would leave a
// dropped IS NOT NULL conjunct alive.
func TestRLS_ExtractionFieldResultsRegionAllOrNothing(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-09/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()

	insert := func(what string, r efrRegion) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertFieldResult(ctx, tx, id, h.tenantA, jobA, efrField, efrPtr(efrValue), r, nil)
		})
	}

	// Positive halves first: both complete forms are accepted, so the ten refusals below
	// are about half-writing and not about the columns being unusable.
	for _, c := range []struct {
		what string
		r    efrRegion
	}{
		{"all five region columns set", efrBox()},
		{"all five region columns NULL", efrRegion{}},
	} {
		err := insert(c.what, c.r)
		if failIfUndefinedFieldResults(t, "INSERT with "+c.what, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
		}
	}

	// The five columns, each addressable, so every conjunct of the CHECK is load-bearing.
	cols := []struct {
		name  string
		set   func(*efrRegion)
		clear func(*efrRegion)
	}{
		{"page", func(r *efrRegion) { r.page = efrPtr(1) }, func(r *efrRegion) { r.page = nil }},
		{"bbox_x0", func(r *efrRegion) { r.x0 = efrPtr(0.1) }, func(r *efrRegion) { r.x0 = nil }},
		{"bbox_y0", func(r *efrRegion) { r.y0 = efrPtr(0.2) }, func(r *efrRegion) { r.y0 = nil }},
		{"bbox_x1", func(r *efrRegion) { r.x1 = efrPtr(0.3) }, func(r *efrRegion) { r.x1 = nil }},
		{"bbox_y1", func(r *efrRegion) { r.y1 = efrPtr(0.4) }, func(r *efrRegion) { r.y1 = nil }},
	}

	type halfWritten struct {
		what string
		r    efrRegion
	}
	var cases []halfWritten
	for i := range cols {
		r := efrBox()
		cols[i].clear(&r)
		cases = append(cases, halfWritten{cols[i].name + " NULL, the other four set", r})
	}
	for i := range cols {
		var r efrRegion
		cols[i].set(&r)
		cases = append(cases, halfWritten{cols[i].name + " set, the other four NULL", r})
	}
	if len(cases) != 10 {
		t.Fatalf("built %d half-written combinations, want 10 — an empty or short list would satisfy "+
			"every assertion in the loop below vacuously", len(cases))
	}

	for _, c := range cases {
		err := insert(c.what, c.r)
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514) on %s", c.what, efrRegionComplete)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.what, code, err)
			continue
		}
		if name := pgConstraint(err); name != efrRegionComplete {
			t.Errorf("INSERT with %s: constraint = %q, want %q — another CHECK fired, so this case is "+
				"not testing its subject", c.what, name, efrRegionComplete)
		}
	}
}

// EFR-10: the box is normalised to [0,1] with x0 <= x1 and y0 <= y1. One rejection per
// conjunct, each with page set so _region_complete cannot fire instead.
func TestRLS_ExtractionFieldResultsBboxNormalised(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-10/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()

	insert := func(r efrRegion) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertFieldResult(ctx, tx, id, h.tenantA, jobA, efrField, efrPtr(efrValue), r, nil)
		})
	}
	box := func(x0, y0, x1, y1 float64) efrRegion {
		return efrRegion{page: efrPtr(1), x0: efrPtr(x0), y0: efrPtr(y0), x1: efrPtr(x1), y1: efrPtr(y1)}
	}

	for _, c := range []struct {
		what string
		r    efrRegion
	}{
		{"the full page (0,0,1,1)", box(0, 0, 1, 1)},
		{"an interior box (0.1,0.2,0.3,0.4)", box(0.1, 0.2, 0.3, 0.4)},
	} {
		err := insert(c.r)
		if failIfUndefinedFieldResults(t, "INSERT "+c.what, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT %s: want success (the bounds are inclusive), got: %v", c.what, err)
		}
	}

	rejects := []struct {
		what string
		r    efrRegion
	}{
		{"bbox_x0 below 0", box(-0.0001, 0.2, 0.3, 0.4)},
		{"bbox_x1 above 1", box(0.1, 0.2, 1.0001, 0.4)},
		{"bbox_x0 greater than bbox_x1", box(0.9, 0.2, 0.1, 0.4)},
		{"bbox_y0 below 0", box(0.1, -0.0001, 0.3, 0.4)},
		{"bbox_y1 above 1", box(0.1, 0.2, 0.3, 1.0001)},
		{"bbox_y0 greater than bbox_y1", box(0.1, 0.9, 0.3, 0.2)},
	}
	if len(rejects) != 6 {
		t.Fatalf("built %d rejection cases, want 6 — one per conjunct of %s", len(rejects), efrBboxNormalised)
	}
	for _, c := range rejects {
		err := insert(c.r)
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514) on %s", c.what, efrBboxNormalised)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.what, code, err)
			continue
		}
		if name := pgConstraint(err); name != efrBboxNormalised {
			t.Errorf("INSERT with %s: constraint = %q, want %q — another CHECK fired, so this case is "+
				"not testing its subject", c.what, name, efrBboxNormalised)
		}
	}
}

// EFR-11: the literal unconverted-PDF-point case. A US-Letter box in points fails at write
// time rather than rendering off-screen two stories later.
func TestRLS_ExtractionFieldResultsAbsoluteCoordinateRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-11/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	absID := uuid.NewString()
	okID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id IN ($1, $2)`, absID, okID)
	}()

	absolute := efrRegion{page: efrPtr(1), x0: efrPtr(72.0), y0: efrPtr(720.0), x1: efrPtr(540.0), y1: efrPtr(750.0)}
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, absID, h.tenantA, jobA, efrField, efrPtr(efrValue), absolute, nil)
	})
	if failIfUndefinedFieldResults(t, "INSERT an absolute-point box", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT of the absolute-point box (72,720,540,750) succeeded, want CHECK violation " +
			"(SQLSTATE 23514) — normalisation is a contract, not a convention")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("absolute-point box: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != efrBboxNormalised {
		t.Errorf("absolute-point box: constraint = %q, want %q", name, efrBboxNormalised)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, absID); n != 0 {
		t.Errorf("rows after the refused absolute-point INSERT = %d, want 0", n)
	}

	// Positive half: the same box divided by a 612x792 page is accepted.
	normalised := efrRegion{
		page: efrPtr(1),
		x0:   efrPtr(72.0 / 612.0), y0: efrPtr(720.0 / 792.0),
		x1: efrPtr(540.0 / 612.0), y1: efrPtr(750.0 / 792.0),
	}
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, okID, h.tenantA, jobA, efrField, efrPtr(efrValue), normalised, nil)
	}); err != nil {
		t.Fatalf("the same box normalised against a 612x792 page: want success, got: %v", err)
	}
}

// EFR-12: the bbox inequalities are non-strict, so a zero-area box is LEGAL. Pins the
// decision against a later story tightening <= to <: a collapsed box is undrawable, not
// wrong, and rejecting it would dead-letter a whole extraction over one field.
func TestRLS_ExtractionFieldResultsZeroAreaBoxAccepted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-12/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, id)
	}()

	var page int
	var x0, y0, x1, y1 float64
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO extraction_field_results
			   (id, tenant_id, extraction_job_id, field_name, value, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1)
			 VALUES ($1, $2, $3, $4, $5, 1, 0.5, 0.5, 0.5, 0.5)
			 RETURNING page, bbox_x0, bbox_y0, bbox_x1, bbox_y1`,
			id, h.tenantA, jobA, efrField, efrValue).Scan(&page, &x0, &y0, &x1, &y1)
	})
	if failIfUndefinedFieldResults(t, "INSERT a zero-area box", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT of the zero-area box (0.5,0.5,0.5,0.5): want success, got: %v — the CHECK is "+
			"non-strict on purpose (x0 <= x1, y0 <= y1)", err)
	}
	if page != 1 || x0 != 0.5 || y0 != 0.5 || x1 != 0.5 || y1 != 0.5 {
		t.Errorf("zero-area box round-trip = page %d (%v,%v,%v,%v), want page 1 (0.5,0.5,0.5,0.5)",
			page, x0, y0, x1, y1)
	}
}

// EFR-13: page is 1-based. All four bbox columns are set so _region_complete is satisfied
// and only _page_check can refuse; the constraint name is asserted because Postgres aborts
// on the first violated CHECK in creation order, not on the one this case aims at.
func TestRLS_ExtractionFieldResultsPageIsOneBased(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-13/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	zeroID := uuid.NewString()
	oneID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id IN ($1, $2)`, zeroID, oneID)
	}()

	zero := efrBox()
	zero.page = efrPtr(0)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, zeroID, h.tenantA, jobA, efrField, efrPtr(efrValue), zero, nil)
	})
	if failIfUndefinedFieldResults(t, "INSERT page = 0", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with page = 0 succeeded, want CHECK violation (SQLSTATE 23514) — pages are 1-based")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT page = 0: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != efrPageCheck {
		t.Errorf("INSERT page = 0: constraint = %q, want %q — %q fired instead, so this case is not "+
			"testing its subject", name, efrPageCheck, name)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, zeroID); n != 0 {
		t.Errorf("rows after the refused page = 0 INSERT = %d, want 0", n)
	}

	// Positive half: the same box on page 1 is accepted, so the refusal is about the page
	// number and not about the box.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, oneID, h.tenantA, jobA, efrField, efrPtr(efrValue), efrBox(), nil)
	}); err != nil {
		t.Fatalf("INSERT with page = 1 and the same box: want success, got: %v", err)
	}
}

// EFR-14: field_name is bounded at both ends. NOT NULL alone would let "" through, and an
// unbounded name is a review-screen column header nothing can render.
func TestRLS_ExtractionFieldResultsFieldNameBounds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-14/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()

	insert := func(fieldName string) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertFieldResult(ctx, tx, id, h.tenantA, jobA, fieldName, efrPtr(efrValue), efrRegion{}, nil)
		})
	}

	// Each rejection gets its own tx: a failed statement poisons the surrounding one.
	for _, c := range []struct {
		what      string
		fieldName string
	}{
		{"a blank field_name", ""},
		{"a 129-character field_name", strings.Repeat("f", 129)},
	} {
		err := insert(c.fieldName)
		if failIfUndefinedFieldResults(t, "INSERT "+c.what, err) {
			return
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.what, code, err)
			continue
		}
		if name := pgConstraint(err); name != efrFieldNameCheck {
			t.Errorf("INSERT with %s: constraint = %q, want %q", c.what, name, efrFieldNameCheck)
		}
	}

	// Positive half: the boundary length and an ordinary name are both accepted, so the
	// two refusals bound the column rather than pinning it.
	for _, c := range []struct {
		what      string
		fieldName string
	}{
		{"an ordinary field_name", efrField},
		{"a 128-character field_name", strings.Repeat("f", 128)},
	} {
		if err := insert(c.fieldName); err != nil {
			t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
		}
	}
}

// EFR-15: value is nullable but never blank. NULL means the extractor found nothing; "" is
// the same claim in a second encoding, which the review screen would render as a field that
// was read and came back empty.
func TestRLS_ExtractionFieldResultsValueNotBlank(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-15/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()

	insert := func(value *string) (string, error) {
		id := uuid.NewString()
		probes = append(probes, id)
		return id, db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertFieldResult(ctx, tx, id, h.tenantA, jobA, efrField, value, efrRegion{}, nil)
		})
	}

	blankID, err := insert(efrPtr(""))
	if failIfUndefinedFieldResults(t, "INSERT a blank value", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with a blank value succeeded, want CHECK violation (SQLSTATE 23514) — NULL is the " +
			"one encoding of nothing")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT a blank value: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != efrValueCheck {
		t.Errorf("INSERT a blank value: constraint = %q, want %q", name, efrValueCheck)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, blankID); n != 0 {
		t.Errorf("rows after the refused blank-value INSERT = %d, want 0", n)
	}

	// Positive half: NULL and a non-empty string both round-trip, so the 23514 is about
	// blankness and not about the column being unwritable.
	for _, c := range []struct {
		what  string
		value *string
	}{
		{"a NULL value", nil},
		{"a non-empty value", efrPtr(efrValue)},
	} {
		id, err := insert(c.value)
		if err != nil {
			t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
			continue
		}
		var got *string
		if e := h.super.QueryRow(ctx, `SELECT value FROM extraction_field_results WHERE id = $1`, id).
			Scan(&got); e != nil {
			t.Errorf("read back %s: %v", c.what, e)
			continue
		}
		switch {
		case c.value == nil && got != nil:
			t.Errorf("value round-trip for %s = %q, want NULL", c.what, *got)
		case c.value != nil && (got == nil || *got != *c.value):
			t.Errorf("value round-trip for %s = %v, want %q", c.what, got, *c.value)
		}
	}
}

// EFR-16: the runtime consequence of the grant matrix. EFR-17 reads has_table_privilege;
// this issues the statements the app would issue, so a future GRANT UPDATE or GRANT DELETE
// shows up as a behaviour change and not only as a catalog diff.
func TestRLS_ExtractionFieldResultsAppUpdateAndDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-16/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()
	resultID, cleanupResult := seedFieldResult(t, h.tenantA, jobA, efrField)
	defer cleanupResult()

	insertID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, insertID)
	}()

	// Each refused statement gets its own tx.
	for _, c := range []struct {
		what string
		sql  string
	}{
		{"UPDATE", `UPDATE extraction_field_results SET value = 'tampered' WHERE id = $1`},
		{"DELETE", `DELETE FROM extraction_field_results WHERE id = $1`},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql, resultID)
			return e
		})
		if failIfUndefinedFieldResults(t, "app "+c.what, err) {
			return
		}
		if err == nil {
			t.Errorf("invoice_app ran %s on extraction_field_results, want permission denied (SQLSTATE 42501) — "+
				"these rows are append-only by grant and a correction is a new row", c.what)
			continue
		}
		if code := pgCode(err); code != "42501" {
			t.Errorf("app %s: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", c.what, code, err)
		}
	}

	var value *string
	if err := h.super.QueryRow(ctx,
		`SELECT value FROM extraction_field_results WHERE id = $1`, resultID).Scan(&value); err != nil {
		t.Fatalf("read back the row after the refused UPDATE and DELETE: %v", err)
	}
	if value == nil || *value != efrValue {
		t.Errorf("value after the refused UPDATE = %v, want unchanged %q", value, efrValue)
	}

	// Positive half, own tx: the same role CAN insert, so the two 42501s are about UPDATE
	// and DELETE and not about the table being unreachable.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, insertID, h.tenantA, jobA, "vendor_name",
			efrPtr("ACME LTD"), efrRegion{}, nil)
	}); err != nil {
		t.Fatalf("INSERT by the same role: want success, got: %v", err)
	}
}

// EFR-17: least privilege, asked as the superuser on purpose —
// information_schema.role_table_grants shows only the current role's own grants, so the
// "reader holds nothing" half cannot be proven from the app pool. The two `true` rows make
// this notice a MISSING grant, not only a forbidden present one.
func TestRLS_ExtractionFieldResultsGrantMatrix(t *testing.T) {
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
			`SELECT has_table_privilege($1, 'public.extraction_field_results', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedFieldResults(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, extraction_field_results, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, extraction_field_results, %q) = %v, want %v — the grant is "+
				"exactly SELECT, INSERT to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// EFR-18: the composite-FK target. A bare CREATE UNIQUE INDEX leaves no pg_constraint row
// and cannot be referenced, so contype and the column order are both asserted. PG18 stores
// NOT NULLs in pg_constraint as contype 'n', which makes the filter mandatory.
func TestRLS_ExtractionFieldResultsTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	n, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.extraction_field_results'::regclass
		    AND contype = 'u' AND conname = 'extraction_field_results_tenant_id_id_uq'`)
	if failIfUndefinedFieldResults(t, "query pg_constraint for "+efrTenantIDIDUq, err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_constraint for %s: %v", efrTenantIDIDUq, err)
	}
	if n != 1 {
		t.Fatalf("UNIQUE constraints on extraction_field_results named %s = %d, want 1 — not found; the "+
			"migration is not applied yet, or it declared a bare CREATE UNIQUE INDEX (no pg_constraint "+
			"row, unusable as a composite-FK target)", efrTenantIDIDUq, n)
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname
		   FROM pg_constraint c
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		  WHERE c.conrelid = 'public.extraction_field_results'::regclass
		    AND c.contype = 'u' AND c.conname = 'extraction_field_results_tenant_id_id_uq'
		  ORDER BY k.ord`)
	if err != nil {
		t.Fatalf("query the constraint's columns: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if e := rows.Scan(&col); e != nil {
			t.Fatalf("scan constraint column: %v", e)
		}
		cols = append(cols, col)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate constraint columns: %v", e)
	}

	want := []string{"tenant_id", "id"}
	if len(cols) != len(want) {
		t.Fatalf("%s columns = %v, want %v", efrTenantIDIDUq, cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("%s column %d = %q, want %q (order is load-bearing: a composite FK must name "+
				"(tenant_id, id) in this order)", efrTenantIDIDUq, i+1, cols[i], want[i])
		}
	}
}

// EFR-19: every non-primary index leads with tenant_id, so no lookup path can plan across
// tenants. The primary key is on (id) and is excluded on purpose — including it would fail
// against a correct migration. The non-empty check comes first: an empty list satisfies
// every assertion inside the loop. Both expected indexes are named, so a later added index
// does not go red but a dropped one does.
func TestRLS_ExtractionFieldResultsEveryIndexLeadsWithTenantId(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT i.relname, a.attname
		   FROM pg_index x
		   JOIN pg_class i ON i.oid = x.indexrelid
		   JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = x.indkey[0]
		  WHERE x.indrelid = 'public.extraction_field_results'::regclass
		    AND NOT x.indisprimary
		  ORDER BY i.relname`)
	if failIfUndefinedFieldResults(t, "query pg_index for extraction_field_results", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_index for extraction_field_results: %v", err)
	}
	defer rows.Close()

	firstCol := map[string]string{}
	for rows.Next() {
		var index, col string
		if e := rows.Scan(&index, &col); e != nil {
			t.Fatalf("scan pg_index row: %v", e)
		}
		firstCol[index] = col
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_index rows: %v", e)
	}

	if len(firstCol) == 0 {
		t.Fatalf("non-primary indexes on extraction_field_results = 0, want 2 — the migration is not "+
			"applied yet, or it shipped neither %s nor %s: %v", efrTenantIDIDUq, efrTenantJobIdx, firstCol)
	}
	for _, want := range []string{efrTenantIDIDUq, efrTenantJobIdx} {
		if _, ok := firstCol[want]; !ok {
			t.Errorf("no index named %q on extraction_field_results; got %v", want, firstCol)
		}
	}
	for index, col := range firstCol {
		if col != "tenant_id" {
			t.Errorf("index %q leads with %q, want tenant_id — a lookup path that does not lead with "+
				"tenant_id plans across tenants", index, col)
		}
	}
}

// EFR-20: deleting a job takes its field results with it. Run as the superuser: the same
// DELETE as the owner with the GUC unset removes 0 rows and the children survive, because
// FORCE RLS hides the PARENT from the owner — correct behaviour, not a cascade failure.
// The sibling job's rows are the control.
func TestRLS_ExtractionFieldResultsJobDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-20/a.pdf")
	defer cleanupDoc()
	doomedJob, cleanupDoomed := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupDoomed() // no-op once the DELETE below removes it
	survivingJob, cleanupSurviving := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupSurviving()

	doomedChild, cleanupDoomedChild := seedFieldResult(t, h.tenantA, doomedJob, efrField)
	defer cleanupDoomedChild()
	survivingChild, cleanupSurvivingChild := seedFieldResult(t, h.tenantA, survivingJob, efrField)
	defer cleanupSurvivingChild()

	// Both children exist first, so the zero below cannot come from a seed that never landed.
	for _, id := range []string{doomedChild, survivingChild} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, id); n != 1 {
			t.Fatalf("field result %s before the DELETE = %d, want 1", id, n)
		}
	}

	ct, err := h.super.Exec(ctx, `DELETE FROM extraction_jobs WHERE id = $1`, doomedJob)
	if err != nil {
		t.Fatalf("delete the parent job: want success (the FK is ON DELETE CASCADE), got: %v", err)
	}
	if ct.RowsAffected() != 1 {
		t.Fatalf("delete of the parent job affected %d rows, want 1", ct.RowsAffected())
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_field_results WHERE id = $1`, doomedChild); n != 0 {
		t.Errorf("field results of the deleted job = %d, want 0 — a field result has no meaning without "+
			"its job", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_field_results WHERE id = $1`, survivingChild); n != 1 {
		t.Errorf("field results of the sibling job = %d, want 1 — the cascade must reach only the "+
			"deleted job's children", n)
	}
}

// EFR-21: this table has no updated_at and no trigger; SELECT and INSERT are the only
// grants, so there is nothing to touch. extraction_jobs carries both, and pinning their
// absence here stops a later story copying that shape in by reflex.
func TestRLS_ExtractionFieldResultsHasNoUpdatedAtColumnOrTrigger(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cols, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_attribute
		  WHERE attrelid = 'public.extraction_field_results'::regclass
		    AND attname = 'updated_at' AND NOT attisdropped`)
	if failIfUndefinedFieldResults(t, "query pg_attribute for extraction_field_results.updated_at", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_attribute for extraction_field_results.updated_at: %v", err)
	}
	if cols != 0 {
		t.Errorf("updated_at columns on extraction_field_results = %d, want 0 — the rows are append-only "+
			"by grant, so a maintained timestamp would never move", cols)
	}

	// The control: created_at IS there, so the zero above is a real absence and not a
	// broken catalog query.
	created, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_attribute
		  WHERE attrelid = 'public.extraction_field_results'::regclass
		    AND attname = 'created_at' AND NOT attisdropped`)
	if err != nil {
		t.Fatalf("query pg_attribute for extraction_field_results.created_at: %v", err)
	}
	if created != 1 {
		t.Fatalf("created_at columns on extraction_field_results = %d, want 1 — the query above would "+
			"report 0 for any column name, so its result proves nothing", created)
	}

	triggers, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_trigger
		  WHERE tgrelid = 'public.extraction_field_results'::regclass AND NOT tgisinternal`)
	if err != nil {
		t.Fatalf("query pg_trigger for extraction_field_results: %v", err)
	}
	if triggers != 0 {
		t.Errorf("non-internal triggers on extraction_field_results = %d, want 0", triggers)
	}
}

// EFR-22: the whole column list, in order, with its types, nullability and defaults. The
// four bbox columns are double precision by D-3 (numeric was rejected); nothing else pinned
// the type, so a swap changed no test. A DeepEqual over the ordered list also catches a
// column added, removed or renamed — including an updated_at that EFR-21 would see only as
// a name.
func TestRLS_ExtractionFieldResultsColumnShapeAndNullability(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	type column struct {
		name     string
		typ      string
		notNull  bool
		defaults string
	}
	want := []column{
		{"id", "uuid", true, "gen_random_uuid()"},
		{"tenant_id", "uuid", true, ""},
		{"extraction_job_id", "uuid", true, ""},
		{"field_name", "text", true, ""},
		{"value", "text", false, ""},
		{"page", "integer", false, ""},
		{"bbox_x0", "double precision", false, ""},
		{"bbox_y0", "double precision", false, ""},
		{"bbox_x1", "double precision", false, ""},
		{"bbox_y1", "double precision", false, ""},
		{"reason_code", "text", false, ""},
		{"created_at", "timestamp with time zone", true, "now()"},
		{"candidate_rank", "integer", true, "0"},
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		        coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		   FROM pg_attribute a
		   LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE a.attrelid = 'public.extraction_field_results'::regclass
		    AND a.attnum > 0 AND NOT a.attisdropped
		  ORDER BY a.attnum`)
	if failIfUndefinedFieldResults(t, "query pg_attribute for extraction_field_results", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_attribute for extraction_field_results: %v", err)
	}
	defer rows.Close()

	var got []column
	for rows.Next() {
		var c column
		if e := rows.Scan(&c.name, &c.typ, &c.notNull, &c.defaults); e != nil {
			t.Fatalf("scan pg_attribute row: %v", e)
		}
		got = append(got, c)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_attribute rows: %v", e)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extraction_field_results columns =\n%v\nwant\n%v", got, want)
	}

	// The catalog half above says NOT NULL; this half proves it refuses at write time.
	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id = ANY($1)`, probes)
	}()
	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-22/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	for _, c := range []struct {
		col  string
		cols string
		vals string
		args []any
	}{
		{"tenant_id", "id, tenant_id, extraction_job_id, field_name", "$1, NULL, $2, $3", nil},
		{"extraction_job_id", "id, extraction_job_id, tenant_id, field_name", "$1, NULL, $2, $3", nil},
		{"field_name", "id, field_name, tenant_id, extraction_job_id", "$1, NULL, $2, $3", nil},
		{"created_at", "id, created_at, tenant_id, extraction_job_id, field_name", "$1, NULL, $2, $3, $4", nil},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		args := []any{id, h.tenantA, jobA, efrField}
		switch c.col {
		case "tenant_id":
			args = []any{id, jobA, efrField}
		case "extraction_job_id":
			args = []any{id, h.tenantA, efrField}
		case "field_name":
			args = []any{id, h.tenantA, jobA}
		}
		_, err := h.super.Exec(ctx,
			`INSERT INTO extraction_field_results (`+c.cols+`) VALUES (`+c.vals+`)`, args...)
		if err == nil {
			t.Errorf("INSERT with %s = NULL succeeded, want not_null_violation (SQLSTATE 23502)", c.col)
			continue
		}
		if code := pgCode(err); code != "23502" {
			t.Errorf("INSERT with %s = NULL: SQLSTATE = %q, want 23502 (not_null_violation): %v", c.col, code, err)
		}
	}

	// The bare insert: every optional column may be omitted, and the two defaults fire.
	bare := uuid.NewString()
	probes = append(probes, bare)
	var value, reason *string
	var page *int
	var x0 *float64
	var created time.Time
	if err := h.super.QueryRow(ctx,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name)
		 VALUES ($1, $2, $3, $4)
		 RETURNING value, reason_code, page, bbox_x0, created_at`,
		bare, h.tenantA, jobA, efrField,
	).Scan(&value, &reason, &page, &x0, &created); err != nil {
		t.Fatalf("bare INSERT naming only the required columns: want success, got: %v", err)
	}
	if value != nil || reason != nil || page != nil || x0 != nil {
		t.Errorf("bare insert left value=%v reason_code=%v page=%v bbox_x0=%v, want all NULL",
			value, reason, page, x0)
	}
	if created.IsZero() {
		t.Error("created_at on a bare insert is the zero time, want the transaction's now()")
	}
}

// EXTR-05-01: candidate_rank never goes negative. 0 is the decided reading; 1..N are the
// surviving alternatives Reconcile keeps — a negative rank has no such meaning.
func TestRLS_ExtractionFieldResultsCandidateRankRejectsNegative(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EXTR-05-01/negative.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, id)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, candidate_rank)
			 VALUES ($1, $2, $3, $4, $5)`,
			id, h.tenantA, jobA, efrField, -1)
		return err
	})
	if failIfUndefinedFieldResults(t, "INSERT candidate_rank = -1", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with candidate_rank = -1 succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT candidate_rank = -1: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, id); n != 0 {
		t.Errorf("rows after the refused candidate_rank = -1 INSERT = %d, want 0", n)
	}
}

// EXTR-05-01: an INSERT naming none of the optional columns still gets candidate_rank = 0,
// so the pre-existing writer (which knows nothing of ranks) keeps working untouched.
func TestRLS_ExtractionFieldResultsCandidateRankDefaultsToZero(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EXTR-05-01/default.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, id)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name)
			 VALUES ($1, $2, $3, $4)`,
			id, h.tenantA, jobA, efrField)
		return err
	})
	if failIfUndefinedFieldResults(t, "INSERT naming only the required columns", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT naming only the required columns: want success, got: %v", err)
	}

	var rank int
	if err := h.super.QueryRow(ctx,
		`SELECT candidate_rank FROM extraction_field_results WHERE id = $1`, id,
	).Scan(&rank); err != nil {
		t.Fatalf("read back candidate_rank: %v", err)
	}
	if rank != 0 {
		t.Errorf("candidate_rank on an insert that never named it = %d, want 0", rank)
	}
}

// EXTR-05-01: candidate_rank is an ordinary column, not a new isolation seam — it rides the
// same tenant_id predicate as every other column on this table. B's non-zero-ranked row must
// stay invisible to A under SELECT, and A must not be able to plant a ranked row into B.
func TestRLS_ExtractionFieldResultsCandidateRankCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docB, cleanupDocB := seedDocument(t, h.tenantB, "EXTR-05-01/cross-b.pdf")
	defer cleanupDocB()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	rankedB := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, rankedB)
	}()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, candidate_rank)
		 VALUES ($1, $2, $3, $4, $5)`,
		rankedB, h.tenantB, jobB, efrField, 3,
	); err != nil {
		if failIfUndefinedFieldResults(t, "seed B's ranked row", err) {
			return
		}
		t.Fatalf("seed B's ranked row: %v", err)
	}

	crossInsertID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, crossInsertID)
	}()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		// SELECT half: B's ranked row must not surface even when the query names candidate_rank.
		if n := mustCount(t, tx,
			`SELECT count(*) FROM extraction_field_results WHERE id = $1 AND candidate_rank = 3`, rankedB,
		); n != 0 {
			t.Errorf("B's ranked row visible to A via candidate_rank filter = %d, want 0", n)
		}

		// INSERT half: A cannot plant a ranked row into B's tenant either.
		_, err := tx.Exec(ctx,
			`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, candidate_rank)
			 VALUES ($1, $2, $3, $4, $5)`,
			crossInsertID, h.tenantB, jobB, efrField, 2)
		return err
	}); err != nil {
		assertRLSViolation(t, err)
	} else {
		t.Fatal("cross-tenant INSERT naming candidate_rank succeeded, want RLS refusal")
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, crossInsertID); n != 0 {
		t.Errorf("rows after the refused cross-tenant ranked INSERT = %d, want 0", n)
	}
}

// EFR-23: NaN and ±Infinity never reach a bbox column. Postgres orders NaN above every
// float8, so a NaN that only ever met a `>= 0` conjunct would be stored; the ordering
// conjuncts are what refuse it. An extractor dividing by a zero page width produces exactly
// these values.
func TestRLS_ExtractionFieldResultsNonFiniteBboxRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-23/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	for _, bad := range []struct {
		label string
		v     float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	} {
		for _, col := range []string{"bbox_x0", "bbox_y0", "bbox_x1", "bbox_y1"} {
			r := efrBox()
			switch col {
			case "bbox_x0":
				r.x0 = efrPtr(bad.v)
			case "bbox_y0":
				r.y0 = efrPtr(bad.v)
			case "bbox_x1":
				r.x1 = efrPtr(bad.v)
			case "bbox_y1":
				r.y1 = efrPtr(bad.v)
			}
			id := uuid.NewString()
			err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
				return insertFieldResult(ctx, tx, id, h.tenantA, jobA, efrField,
					efrPtr(efrValue), r, nil)
			})
			if failIfUndefinedFieldResults(t, bad.label+" in "+col, err) {
				return
			}
			if code := pgCode(err); code != "23514" {
				t.Errorf("%s in %s: SQLSTATE = %q, want 23514 (check_violation): %v", bad.label, col, code, err)
				continue
			}
			if name := pgConstraint(err); name != efrBboxNormalised {
				t.Errorf("%s in %s was refused by %q, want %q", bad.label, col, name, efrBboxNormalised)
			}
		}
	}

	// The control: the same box with every column finite lands, so the twelve rejections
	// above are about the value and not about the statement shape.
	okID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_results WHERE id = $1`, okID)
	}()
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertFieldResult(ctx, tx, okID, h.tenantA, jobA, efrField, efrPtr(efrValue), efrBox(), nil)
	}); err != nil {
		t.Fatalf("the same INSERT with a finite box: want success, got: %v", err)
	}
}

// EFR-24: _bbox_normalised abstains whenever bbox_x0 IS NULL, so on its own it would let an
// out-of-range y0 through. _region_complete is what closes that door, and the two are only
// sound together. Relaxing _region_complete to allow a page-only region silently reopens it.
func TestRLS_ExtractionFieldResultsHalfWrittenRegionCannotSmuggleABadBox(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-24/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	for _, c := range []struct {
		label string
		r     efrRegion
	}{
		{"y0 = 5.0", efrRegion{page: efrPtr(1), x0: nil, y0: efrPtr(5.0), x1: efrPtr(0.5), y1: efrPtr(0.5)}},
		{"x1 = -3", efrRegion{page: efrPtr(1), x0: nil, y0: efrPtr(0.1), x1: efrPtr(-3.0), y1: efrPtr(0.5)}},
		{"y1 = 42", efrRegion{page: efrPtr(1), x0: nil, y0: efrPtr(0.1), x1: efrPtr(0.5), y1: efrPtr(42.0)}},
	} {
		id := uuid.NewString()
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertFieldResult(ctx, tx, id, h.tenantA, jobA, efrField, efrPtr(efrValue), c.r, nil)
		})
		if failIfUndefinedFieldResults(t, "half-written region with "+c.label, err) {
			return
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("bbox_x0 NULL with %s: SQLSTATE = %q, want 23514: %v", c.label, code, err)
			continue
		}
		if name := pgConstraint(err); name != efrRegionComplete {
			t.Errorf("bbox_x0 NULL with %s was refused by %q, want %q — _bbox_normalised short-circuits "+
				"on a NULL bbox_x0, so _region_complete is the only thing refusing this row",
				c.label, name, efrRegionComplete)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_results WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused half-written region (%s) = %d, want 0", c.label, n)
		}
	}
}

// EFR-25: one job may carry the same field_name twice. Q16 makes a correction a NEW row that
// supersedes the old one, so a unique index over (extraction_job_id, field_name) would break
// EXTR-14 before it is written. Nothing else pins the absence of that index.
func TestRLS_ExtractionFieldResultsRepeatedFieldNameAccepted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFR-25/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	first, second := uuid.NewString(), uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_results WHERE id = ANY($1)`, []string{first, second})
	}()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if e := insertFieldResult(ctx, tx, first, h.tenantA, jobA, efrField, efrPtr("100.00"), efrRegion{}, nil); e != nil {
			return e
		}
		return insertFieldResult(ctx, tx, second, h.tenantA, jobA, efrField, efrPtr("120.00"), efrRegion{}, nil)
	}); err != nil {
		if failIfUndefinedFieldResults(t, "two rows with the same field_name", err) {
			return
		}
		t.Fatalf("two rows with the same field_name on one job: want success (a correction supersedes "+
			"rather than replaces), got: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_field_results WHERE extraction_job_id = $1 AND field_name = $2`,
		jobA, efrField); n != 2 {
		t.Errorf("rows for (%s, %s) = %d, want 2 — both the superseded value and its correction", jobA, efrField, n)
	}

	// The catalog half: no unique index reaches field_name at all.
	n, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_index x
		   JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = ANY(x.indkey::int2[])
		  WHERE x.indrelid = 'public.extraction_field_results'::regclass
		    AND x.indisunique AND a.attname = 'field_name'`)
	if err != nil {
		t.Fatalf("query pg_index for a unique index over field_name: %v", err)
	}
	if n != 0 {
		t.Errorf("unique indexes covering field_name = %d, want 0 — a correction is a new row", n)
	}
}

// EFR-26: the RLS half of the write posture. invoice_app holds no UPDATE or DELETE grant
// (EFR-16), so the owner is the only role that can reach either statement — and FORCE RLS
// must still confine it to its own tenant. This is what would hold if a later story granted
// invoice_app UPDATE.
func TestRLS_ExtractionFieldResultsOwnerCrossTenantWriteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFR-26/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFR-26/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	ownRow, cleanupOwn := seedFieldResult(t, h.tenantA, jobA, efrField)
	defer cleanupOwn()
	otherRow, cleanupOther := seedFieldResult(t, h.tenantB, jobB, efrField)
	defer cleanupOther()

	// B's row is invisible to A, so both statements match nothing rather than erroring.
	for _, c := range []struct {
		what string
		sql  string
	}{
		{"UPDATE", `UPDATE extraction_field_results SET value = 'tampered' WHERE id = $1`},
		{"DELETE", `DELETE FROM extraction_field_results WHERE id = $1`},
	} {
		if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
			ct, e := tx.Exec(ctx, c.sql, otherRow)
			if e != nil {
				return e
			}
			if ct.RowsAffected() != 0 {
				t.Errorf("the owner's cross-tenant %s affected %d row(s), want 0", c.what, ct.RowsAffected())
			}
			return nil
		}); err != nil {
			if failIfUndefinedFieldResults(t, "owner cross-tenant "+c.what, err) {
				return
			}
			t.Fatalf("owner cross-tenant %s: %v", c.what, err)
		}
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_field_results WHERE id = $1 AND value = $2`, otherRow, efrValue); n != 1 {
		t.Errorf("B's row after A's owner-scoped UPDATE and DELETE = %d row(s) with the original value, want 1", n)
	}

	// Reassigning its own row into B is refused outright: the policy's USING doubles as the
	// WITH CHECK, so the NEW row is tested too.
	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE extraction_field_results SET tenant_id = $1 WHERE id = $2`, h.tenantB, ownRow)
		return e
	})
	assertRLSViolation(t, err)
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM extraction_field_results WHERE id = $1 AND tenant_id = $2`, ownRow, h.tenantA); n != 1 {
		t.Errorf("A's row after the refused reassignment is no longer A's, want 1 row still under tenant A (got %d)", n)
	}

	// The positive half: the same role CAN update its own row in its own tenant, so the
	// zeros above are RLS and not a missing privilege.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE extraction_field_results SET value = '1.00' WHERE id = $1`, ownRow)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("the owner's in-tenant UPDATE affected %d row(s), want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("owner in-tenant UPDATE: want success, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// extraction_field_corrections (EXTR-12-03). Same RED-first shape as the two blocks
// above: every case here fails 42P01 until the migration lands.
// ---------------------------------------------------------------------------

// The names every rejection case asserts. SQLSTATE 23514 alone does not say which CHECK
// refused, so a case asserting only the code would silently stop testing its subject.
// Measured against PG18: when a row violates two CHECKs at once, the one Postgres reports is
// the alphabetically FIRST by constraint name, not the first declared -- reversing the DDL
// order changed nothing. So bbox_normalised outranks pointed_has_region outranks
// region_complete, whatever order the migration writes them in.
const (
	efcRegionComplete   = "extraction_field_corrections_region_complete"
	efcBboxNormalised   = "extraction_field_corrections_bbox_normalised"
	efcPointedHasRegion = "extraction_field_corrections_pointed_has_region"
	efcValueCheck       = "extraction_field_corrections_value_check"
	efcMethodCheck      = "extraction_field_corrections_method_check"
	efcTenantJobFK      = "extraction_field_corrections_tenant_job_fk"
)

// The field_name/value/method/actor set supplied whenever none of them is the subject.
// actor is a raw GoTrue subject, the convention audit_log.actor already follows.
const (
	efcField  = "total_amount"
	efcValue  = "212.50"
	efcMethod = "typed"
	efcActor  = "8f1c0d64-4c2e-4a1b-9d33-6f5f0f2c7a01"
)

// The canonical INSERT. Every column a constraint case varies is bound, so one statement
// shape serves them all and no case can differ by accident.
const efcInsert = `INSERT INTO extraction_field_corrections
	(id, tenant_id, extraction_job_id, field_name, value, method, page,
	 bbox_x0, bbox_y0, bbox_x1, bbox_y1, anchor_label, actor)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

// failIfUndefinedFieldCorrections turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedFieldCorrections(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the extraction_field_corrections migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// insertCorrection issues the canonical INSERT on tx and returns the driver error unchanged,
// so each caller asserts its own SQLSTATE and constraint name. The region reuses efrRegion:
// this table carries the same five columns.
func insertCorrection(ctx context.Context, tx pgx.Tx, id, tenantID, jobID, field, value, method string, r efrRegion) error {
	var noAnchor *string
	_, err := tx.Exec(ctx, efcInsert,
		id, tenantID, jobID, field, value, method, r.page, r.x0, r.y0, r.x1, r.y1, noAnchor, efcActor)
	return err
}

// seedFieldCorrection inserts one row as the superuser (BYPASSRLS, so seeding needs neither
// tenant context nor an INSERT grant). Seed the document and the extraction job first: this
// row's composite FK needs both.
func seedFieldCorrection(t *testing.T, tenantID, jobID, fieldName string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_corrections
		     (id, tenant_id, extraction_job_id, field_name, value, method, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, tenantID, jobID, fieldName, efcValue, efcMethod, efcActor,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed extraction_field_corrections: undefined_table (42P01) — "+
				"extraction_field_corrections migration not applied yet: %v", err)
		}
		t.Fatalf("seed extraction_field_corrections: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = $1`, id)
	}
}

// EFC-01: the catalog half of the isolation posture. ENABLE alone would let the owner bypass
// the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ExtractionFieldCorrectionsForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.extraction_field_corrections'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedFieldCorrections(t, "read pg_class for extraction_field_corrections", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for extraction_field_corrections: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("extraction_field_corrections relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
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
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'extraction_field_corrections'`)
	if err != nil {
		t.Fatalf("query pg_policies for extraction_field_corrections: %v", err)
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
		t.Errorf("policies on extraction_field_corrections = %d (%v), want exactly 1 (tenant_isolation) — "+
			"this table has no cross-tenant enumeration reader", len(got), got)
	}

	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatal("no tenant_isolation policy on extraction_field_corrections — the migration is not applied yet")
	}
	if strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation roles = %v, want [public] (no TO clause — it must bind every role)", iso.roles)
	}
	if iso.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want %q (its USING must double as the INSERT WITH CHECK)", iso.cmd, "ALL")
	}
	if !strings.Contains(iso.qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC", iso.qual)
	}
}

// EFC-02: cross-tenant SELECT is refused. The unfiltered count is the load-bearing half — a
// tenant_id-filtered query would come out right even if RLS did nothing.
func TestRLS_ExtractionFieldCorrectionsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFC-02/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFC-02/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	corrA, cleanupCorrA := seedFieldCorrection(t, h.tenantA, jobA, efcField)
	defer cleanupCorrA()
	corrB1, cleanupCorrB1 := seedFieldCorrection(t, h.tenantB, jobB, efcField)
	defer cleanupCorrB1()
	corrB2, cleanupCorrB2 := seedFieldCorrection(t, h.tenantB, jobB, "vendor_name")
	defer cleanupCorrB2()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		// By id, not by tenant_id: a tenant_id predicate would prove nothing about RLS.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_corrections WHERE id = $1`, corrA); n != 1 {
			t.Errorf("A's own correction visible to A = %d, want 1", n)
		}
		for _, id := range []string{corrB1, corrB2} {
			if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_corrections WHERE id = $1`, id); n != 0 {
				t.Errorf("B's correction %s visible to A = %d, want 0", id, n)
			}
		}
		// RLS is the only thing narrowing this one: B seeded two more rows.
		if n := mustCount(t, tx, `SELECT count(*) FROM extraction_field_corrections`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// EFC-03: an INSERT naming tenant B while scoped to A is refused with 42501 and lands no
// row. The positive half stops this passing against a policy written USING (false).
func TestRLS_ExtractionFieldCorrectionsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFC-03/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFC-03/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	crossID := uuid.NewString()
	ownID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_corrections WHERE id IN ($1, $2)`, crossID, ownID)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertCorrection(ctx, tx, crossID, h.tenantB, jobB, efcField, efcValue, efcMethod, efrRegion{})
	})
	if failIfUndefinedFieldCorrections(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_corrections WHERE id = $1`, crossID); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the same statement shape succeeds for A's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertCorrection(ctx, tx, ownID, h.tenantA, jobA, efcField, efcValue, efcMethod, efrRegion{})
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// EFC-04 (AC-2): a correction naming another tenant's job is refused by the composite FK.
// Attempted as the superuser so no policy stands in the way — a single-column
// extraction_job_id FK would let this through, which is what the composite FK exists to close.
func TestRLS_ExtractionFieldCorrectionsRejectAnotherTenantsJob(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDocA := seedDocument(t, h.tenantA, "EFC-04/a.pdf")
	defer cleanupDocA()
	docB, cleanupDocB := seedDocument(t, h.tenantB, "EFC-04/b.pdf")
	defer cleanupDocB()
	jobA, cleanupJobA := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJobA()
	jobB, cleanupJobB := seedExtractionJob(t, h.tenantB, docB)
	defer cleanupJobB()

	danglingID := uuid.NewString()
	okID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM extraction_field_corrections WHERE id IN ($1, $2)`, danglingID, okID)
	}()

	const stmt = `INSERT INTO extraction_field_corrections
	    (id, tenant_id, extraction_job_id, field_name, value, method, actor)
	 VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := h.super.Exec(ctx, stmt, danglingID, h.tenantA, jobB, efcField, efcValue, efcMethod, efcActor)
	if failIfUndefinedFieldCorrections(t, "cross-tenant job reference", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT of a tenant-A correction pointing at tenant B's job succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) — a single-column extraction_job_id FK would let " +
			"this through, which is the bug the composite FK exists to close")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant job reference: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != efcTenantJobFK {
		t.Errorf("cross-tenant job reference: constraint = %q, want %q", name, efcTenantJobFK)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_field_corrections WHERE id = $1`, danglingID); n != 0 {
		t.Errorf("rows after the refused cross-tenant job reference = %d, want 0", n)
	}

	// Positive half: A's own job is accepted by the very same FK.
	if _, err := h.super.Exec(ctx, stmt, okID, h.tenantA, jobA, efcField, efcValue, efcMethod, efcActor); err != nil {
		t.Fatalf("same-tenant job reference: want success, got: %v", err)
	}
}

// EFC-05 (AC-1): append-only is a GRANT, not a policy or a trigger. invoice_app's UPDATE and
// DELETE fail 42501 before RLS is ever consulted. The catalog half makes this a MISSING grant
// rather than only a refused statement, and the two `true` rows keep it from passing against a
// role that holds nothing at all.
func TestRLS_ExtractionFieldCorrectionsAreAppendOnlyByGrant(t *testing.T) {
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
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.extraction_field_corrections', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedFieldCorrections(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, extraction_field_corrections, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, extraction_field_corrections, %q) = %v, want %v — the grant is "+
				"exactly SELECT, INSERT to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}

	// seq is bigserial (house convention) rather than an identity column, and a bigserial
	// default calls nextval: SELECT, INSERT on the table alone leaves every invoice_app INSERT
	// failing 42501 "permission denied for sequence". Measured both ways against these roles;
	// audit_log.sql:79 is the precedent. The positive halves below would fail without naming
	// this as the cause.
	var seqUsage bool
	seqErr := h.super.QueryRow(ctx,
		`SELECT has_sequence_privilege('invoice_app', 'public.extraction_field_corrections_seq_seq', 'USAGE')`,
	).Scan(&seqUsage)
	if failIfUndefinedFieldCorrections(t, "has_sequence_privilege(invoice_app, USAGE)", seqErr) {
		return
	}
	if seqErr != nil {
		t.Fatalf("has_sequence_privilege(invoice_app, extraction_field_corrections_seq_seq, USAGE): %v", seqErr)
	}
	if !seqUsage {
		t.Error("invoice_app holds no USAGE on extraction_field_corrections_seq_seq — every INSERT " +
			"fails 42501 (permission denied for sequence); GRANT SELECT, INSERT does not carry it")
	}

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFC-05/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()
	corrID, cleanupCorr := seedFieldCorrection(t, h.tenantA, jobA, efcField)
	defer cleanupCorr()

	insertID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = $1`, insertID)
	}()

	// Each refused statement gets its own tx: a failed statement poisons the surrounding one.
	for _, c := range []struct {
		what string
		sql  string
	}{
		{"UPDATE", `UPDATE extraction_field_corrections SET value = 'tampered' WHERE id = $1`},
		{"DELETE", `DELETE FROM extraction_field_corrections WHERE id = $1`},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, c.sql, corrID)
			return e
		})
		if failIfUndefinedFieldCorrections(t, "app "+c.what, err) {
			return
		}
		if err == nil {
			t.Errorf("invoice_app ran %s on extraction_field_corrections, want permission denied "+
				"(SQLSTATE 42501) — a correction is superseded by a new row, never edited in place", c.what)
			continue
		}
		if code := pgCode(err); code != "42501" {
			t.Errorf("app %s: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", c.what, code, err)
		}
	}

	var value string
	if err := h.super.QueryRow(ctx,
		`SELECT value FROM extraction_field_corrections WHERE id = $1`, corrID).Scan(&value); err != nil {
		t.Fatalf("read back the row after the refused UPDATE and DELETE: %v", err)
	}
	if value != efcValue {
		t.Errorf("value after the refused UPDATE = %q, want unchanged %q", value, efcValue)
	}

	// Positive half, own tx: the same role CAN insert, so the two 42501s are about UPDATE and
	// DELETE and not about the table being unreachable.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return insertCorrection(ctx, tx, insertID, h.tenantA, jobA, "vendor_name", "ACME LTD", efcMethod, efrRegion{})
	}); err != nil {
		t.Fatalf("INSERT by the same role: want success, got: %v", err)
	}
}

// EFC-06 (AC-3): value cannot be empty and method is confined to four words. 'undone' is in
// the positive half because it re-asserts the extractor's own reading and carries a value like
// every other method — there is no value/method presence rule for it to satisfy.
func TestRLS_ExtractionFieldCorrectionsConstraintSet(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFC-06/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = ANY($1)`, probes)
	}()

	insert := func(value, method string) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertCorrection(ctx, tx, id, h.tenantA, jobA, efcField, value, method, efrRegion{})
		})
	}

	// Positive halves first, so the refusals below are about the values and not about the
	// columns being unusable. 'pointed' is absent: EFC-08 owns it, since it needs a box.
	for _, method := range []string{"typed", "chosen", "undone"} {
		err := insert(efcValue, method)
		if failIfUndefinedFieldCorrections(t, "INSERT method="+method, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT method=%q: want success, got: %v", method, err)
		}
	}

	for _, c := range []struct {
		what       string
		value      string
		method     string
		constraint string
	}{
		{"an empty value", "", efcMethod, efcValueCheck},
		// char_length counts a space, so the CHECK admits it and the field renders blank. Pinned
		// rather than left unspecified: tightening this to btrim() is a decision, not a detail.
		{"a whitespace-only value, which is NOT refused", " ", efcMethod, ""},
		{"method 'edited', the obvious fifth word", efcValue, "edited", efcMethodCheck},
		{"method 'Typed', wrong case", efcValue, "Typed", efcMethodCheck},
		{"an empty method", efcValue, "", efcMethodCheck},
	} {
		err := insert(c.value, c.method)
		if c.constraint == "" {
			if err != nil {
				t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514) on %s", c.what, c.constraint)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.what, code, err)
			continue
		}
		if name := pgConstraint(err); name != c.constraint {
			t.Errorf("INSERT with %s: constraint = %q, want %q — another CHECK fired, so this case is "+
				"not testing its subject", c.what, name, c.constraint)
		}
	}
}

// EFC-07 (AC-4): a pointed correction supplies all five region columns, and the box is
// normalised. Both refusals carry method='pointed', so _pointed_has_region is satisfied and
// the constraint under test is the only one that can fire.
func TestRLS_ExtractionFieldCorrectionsPointedRegionIsAllOrNone(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFC-07/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = ANY($1)`, probes)
	}()

	insert := func(r efrRegion) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertCorrection(ctx, tx, id, h.tenantA, jobA, efcField, efcValue, "pointed", r)
		})
	}

	// The positive half: a complete, normalised box is accepted for a pointed correction.
	err := insert(efrBox())
	if failIfUndefinedFieldCorrections(t, "INSERT a complete pointed region", err) {
		return
	}
	if err != nil {
		t.Errorf("INSERT with all five region columns set: want success, got: %v", err)
	}

	// Each of the four bbox columns is addressable, so every conjunct of _region_complete's
	// second arm is load-bearing. page NULL is EFC-08's subject, not this one: dropping it
	// flips _pointed_has_region instead.
	var halfWritten []struct {
		what string
		r    efrRegion
	}
	for _, c := range []struct {
		name  string
		clear func(*efrRegion)
	}{
		{"bbox_x0", func(r *efrRegion) { r.x0 = nil }},
		{"bbox_y0", func(r *efrRegion) { r.y0 = nil }},
		{"bbox_x1", func(r *efrRegion) { r.x1 = nil }},
		{"bbox_y1", func(r *efrRegion) { r.y1 = nil }},
	} {
		r := efrBox()
		c.clear(&r)
		halfWritten = append(halfWritten, struct {
			what string
			r    efrRegion
		}{"page set, " + c.name + " NULL", r})
	}
	if len(halfWritten) != 4 {
		t.Fatalf("built %d half-written cases, want 4 — one per bbox column", len(halfWritten))
	}
	for _, c := range halfWritten {
		assertCorrectionCheckFires(t, insert(c.r), c.what, efcRegionComplete)
	}

	// The un-normalised half: an unconverted US-Letter box in PDF points, plus each bound.
	box := func(x0, y0, x1, y1 float64) efrRegion {
		return efrRegion{page: efrPtr(1), x0: efrPtr(x0), y0: efrPtr(y0), x1: efrPtr(x1), y1: efrPtr(y1)}
	}
	for _, c := range []struct {
		what string
		r    efrRegion
	}{
		{"an absolute-point box (72,720,540,750)", box(72, 720, 540, 750)},
		{"bbox_x0 below 0", box(-0.0001, 0.2, 0.3, 0.4)},
		{"bbox_x1 above 1", box(0.1, 0.2, 1.0001, 0.4)},
		{"bbox_x0 greater than bbox_x1", box(0.9, 0.2, 0.1, 0.4)},
		{"bbox_y0 below 0", box(0.1, -0.0001, 0.3, 0.4)},
		{"bbox_y1 above 1", box(0.1, 0.2, 0.3, 1.0001)},
		{"bbox_y0 greater than bbox_y1", box(0.1, 0.9, 0.3, 0.2)},
	} {
		assertCorrectionCheckFires(t, insert(c.r), c.what, efcBboxNormalised)
	}
}

// EFC-08 (AC-5): method and region presence agree in BOTH directions. The all-or-none region
// CHECK alone admits both shapes below — a settled-by-hand correction carrying a box, and a
// pointed one carrying none — which is why _pointed_has_region exists.
func TestRLS_ExtractionFieldCorrectionsMethodAndRegionMustAgree(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFC-08/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = ANY($1)`, probes)
	}()

	insert := func(method string, r efrRegion) error {
		id := uuid.NewString()
		probes = append(probes, id)
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return insertCorrection(ctx, tx, id, h.tenantA, jobA, efcField, efcValue, method, r)
		})
	}

	// The two legal shapes first: every refusal below is about the pairing, not the columns.
	err := insert("pointed", efrBox())
	if failIfUndefinedFieldCorrections(t, "INSERT pointed with a box", err) {
		return
	}
	if err != nil {
		t.Errorf("INSERT method='pointed' with a complete box: want success, got: %v", err)
	}
	if err := insert("typed", efrRegion{}); err != nil {
		t.Errorf("INSERT method='typed' with no region: want success, got: %v", err)
	}

	// Direction 1: a box on a method that never points at one. Both bbox_normalised and
	// region_complete are satisfied by efrBox(), so only _pointed_has_region can fire.
	for _, method := range []string{"typed", "chosen", "undone"} {
		assertCorrectionCheckFires(t, insert(method, efrBox()),
			"method='"+method+"' carrying a complete normalised box", efcPointedHasRegion)
	}

	// Direction 2: pointed with nothing to point at. All five region columns NULL satisfies
	// region_complete's first arm, so again only _pointed_has_region can fire.
	assertCorrectionCheckFires(t, insert("pointed", efrRegion{}),
		"method='pointed' with every region column NULL", efcPointedHasRegion)

	// A pointed correction carrying a bbox but no page violates region_complete AND
	// pointed_has_region at once; the name order above is what decides that the second is the
	// one reported. Measured, not assumed — the composition through `page` is the point, and
	// EFC-07 is where region_complete is asserted alone.
	noPage := efrBox()
	noPage.page = nil
	assertCorrectionCheckFires(t, insert("pointed", noPage),
		"method='pointed' with a bbox but no page", efcPointedHasRegion)
}

// assertCorrectionCheckFires asserts err is a CHECK violation raised by the named constraint.
// The name matters: SQLSTATE 23514 alone would not say which CHECK refused.
func assertCorrectionCheckFires(t *testing.T, err error, what, constraint string) {
	t.Helper()
	if err == nil {
		t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514) on %s", what, constraint)
		return
	}
	if code := pgCode(err); code != "23514" {
		t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", what, code, err)
		return
	}
	if name := pgConstraint(err); name != constraint {
		t.Errorf("INSERT with %s: constraint = %q, want %q — another CHECK fired, so this case is not "+
			"testing its subject", what, name, constraint)
	}
}

// EFC-09: the three bound CHECKs the AC suite leaves untouched — page is 1-based, field_name
// is 1..128, actor is 1..255. Each case varies one column and holds the rest legal, and each
// bound is probed on both sides, so an off-by-one in either direction reds.
func TestRLS_ExtractionFieldCorrectionsLengthAndPageBounds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "EFC-09/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_field_corrections WHERE id = ANY($1)`, probes)
	}()
	// page travels with method='pointed' and a complete box, so _pointed_has_region and
	// _region_complete are satisfied and page_check is the only CHECK left to fire.
	insert := func(field, actor string, page *int) error {
		id := uuid.NewString()
		probes = append(probes, id)
		method, r := efcMethod, efrRegion{}
		if page != nil {
			method, r = "pointed", efrBox()
			r.page = page
		}
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			var noAnchor *string
			_, err := tx.Exec(ctx, efcInsert, id, h.tenantA, jobA, field, efcValue, method,
				r.page, r.x0, r.y0, r.x1, r.y1, noAnchor, actor)
			return err
		})
	}

	// The accepted halves, first: every refusal below is about the bound, not the column.
	for _, c := range []struct {
		what  string
		field string
		actor string
		page  *int
	}{
		{"field_name at the 128-char ceiling", strings.Repeat("f", 128), efcActor, nil},
		{"actor at the 255-char ceiling", efcField, strings.Repeat("a", 255), nil},
		{"page 1, the lowest legal page", efcField, efcActor, efrPtr(1)},
	} {
		err := insert(c.field, c.actor, c.page)
		if failIfUndefinedFieldCorrections(t, "INSERT with "+c.what, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
		}
	}

	for _, c := range []struct {
		what       string
		field      string
		actor      string
		page       *int
		constraint string
	}{
		{"an empty field_name", "", efcActor, nil, "extraction_field_corrections_field_name_check"},
		{"a field_name one over the ceiling", strings.Repeat("f", 129), efcActor, nil, "extraction_field_corrections_field_name_check"},
		{"an empty actor", efcField, "", nil, "extraction_field_corrections_actor_check"},
		{"an actor one over the ceiling", efcField, strings.Repeat("a", 256), nil, "extraction_field_corrections_actor_check"},
		{"page 0, one below the lowest legal page", efcField, efcActor, efrPtr(0), "extraction_field_corrections_page_check"},
		{"a negative page", efcField, efcActor, efrPtr(-1), "extraction_field_corrections_page_check"},
	} {
		assertCorrectionCheckFires(t, insert(c.field, c.actor, c.page), c.what, c.constraint)
	}
}

// ---- EXTR-19-06: extraction_jobs.layout_tokens ------------------------------------------

// ltCap is the column's own serialised ceiling, in the chars char_length(jsonb::text) counts.
// Postgres renders a jsonb array with ", " and Go does not, so this is NOT len(json.Marshal).
const ltCap = 262144

// ltArrayRendering builds an n-element ASCII JSON array already in Postgres's own canonical
// rendering -- "[" + `"e"` joined by ", " + "]" -- measuring exactly want chars. Computed by
// padding the last element, so no fixture here is hand-counted.
func ltArrayRendering(t *testing.T, n, want int) string {
	t.Helper()
	elems := make([]string, n)
	for i := range elems {
		elems[i] = "x"
	}
	render := func() string {
		quoted := make([]string, n)
		for i, e := range elems {
			quoted[i] = `"` + e + `"`
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	if out := render(); len(out) > want {
		t.Fatalf("a %d-element array already renders as %d chars, over the %d wanted", n, len(out), want)
	} else {
		elems[n-1] += strings.Repeat("x", want-len(out))
	}
	out := render()
	if len(out) != want {
		t.Fatalf("the array fixture is %d chars, want exactly %d", len(out), want)
	}
	return out
}

// LT-8 (AC-7). The type conjunct and the size conjunct, each with its own case, plus the JSON
// null literal: json.Marshal of a nil slice is `null` and jsonb_typeof('null') is not 'array',
// so a writer that forgets to normalise nil dead-letters the job. Must-fail mutation: drop the
// jsonb_typeof conjunct -- the object case reds.
func TestRLS_ExtractionJobsLayoutTokensBounds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "LT-8/a.pdf")
	defer cleanupDoc()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM extraction_jobs WHERE id = ANY($1)`, probes)
	}()

	// Both sides of the boundary, computed rather than hand-counted: the accepted case must sit
	// exactly ON the cap, or nothing here guards it.
	atCap := ltArrayRendering(t, 500, ltCap)
	overCap := ltArrayRendering(t, 500, ltCap+1)
	atCapID := ""

	for _, c := range []struct {
		what    string
		tokens  any // nil encodes SQL NULL; a Go string is cast ::jsonb
		wantErr bool
	}{
		{"a JSON object", `{"a":1}`, true},
		{"a JSON string scalar", `"x"`, true},
		{"the JSON null literal", `null`, true},
		{"an array over the cap", overCap, true},
		{"a 300 KiB array", ltArrayRendering(t, 500, 307200), true},
		{"SQL NULL", nil, false},
		{"a 3-element array", `["Invoice No: X-1","Buyer: Honeywell Group","Total: 300.00"]`, false},
		{"an empty JSON array", `[]`, false},
		{"an array saturating the cap", atCap, false},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		if c.what == "an array saturating the cap" {
			atCapID = id
		}
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_tokens)
				 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
				id, h.tenantA, docA, ejExtractor, ejExtractorVersion, c.tokens)
			return e
		})
		if !c.wantErr {
			if err != nil {
				t.Errorf("INSERT with %s: want success, got: %v", c.what, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.what)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514: %v", c.what, code, err)
			continue
		}
		if got := pgConstraint(err); got != "extraction_jobs_layout_tokens_check" {
			t.Errorf("INSERT with %s tripped constraint %q, want extraction_jobs_layout_tokens_check", c.what, got)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, id); n != 0 {
			t.Errorf("rows after the refused %s = %d, want 0", c.what, n)
		}
	}

	// The accepted boundary case is only a boundary if Postgres re-renders it at the cap: the
	// fixture is written in PG's canonical form, and this is what proves that claim.
	if atCapID != "" {
		var rendered *int
		if err := h.super.QueryRow(ctx,
			`SELECT char_length(layout_tokens::text) FROM extraction_jobs WHERE id = $1`, atCapID).Scan(&rendered); err != nil {
			t.Fatalf("read back the saturating array: %v", err)
		}
		if rendered == nil || *rendered != ltCap {
			t.Errorf("the saturating array renders as %v chars, want exactly %d -- the fixture is not on the boundary the CHECK measures", rendered, ltCap)
		}
	}
}

// LT-9 (AC-8). Inheritance is a claim: a new column on an already-policied table is only
// covered because the policy is row-scoped and has no column list. This reads it.
func TestRLS_LayoutTokensAreNotReadableAcrossTenants(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "LT-9/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	const tokens = `["Invoice No: ASC-2026-0919","Total: NGN 4,300.00"]`
	if _, err := h.super.Exec(ctx,
		`UPDATE extraction_jobs SET layout_tokens = $2::jsonb WHERE id = $1`, jobA, tokens); err != nil {
		t.Fatalf("store layout_tokens on tenant A's job: %v -- without a stored value the zero below is not isolation", err)
	}

	read := func(tenantID string) []string {
		t.Helper()
		var out []string
		if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
			rows, e := tx.Query(ctx, `SELECT layout_tokens::text FROM extraction_jobs WHERE id = $1`, jobA)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var raw *string
				if e := rows.Scan(&raw); e != nil {
					return e
				}
				out = append(out, ltNullable(raw))
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("read layout_tokens as %s: %v", tenantID, err)
		}
		return out
	}

	// Control first: the same query under A returns the row, so B's zero is RLS and not a bad id.
	if got := read(h.tenantA); len(got) != 1 || !strings.Contains(got[0], "ASC-2026-0919") {
		t.Fatalf("tenant A read %v for its own job, want one row carrying its tokens", got)
	}
	if got := read(h.tenantB); len(got) != 0 {
		t.Errorf("tenant B read %d row(s) %v for tenant A's job, want zero rows -- a redacted NULL would still confirm the row exists", len(got), got)
	}
}

func ltNullable(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}
