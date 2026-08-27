// RLS, grant and constraint suite for `extraction_jobs`. Written before the migration
// exists, so every case here fails with an explicit 42P01 until it lands.
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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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
