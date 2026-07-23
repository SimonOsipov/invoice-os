// M5-01-03 (task-213): tests for the `submission_jobs` table, the mutable
// per-submission-cycle job record. Written BEFORE the migration exists — every case here
// is RED against SQLSTATE 42P01 undefined_table. The table the Executor will add
// (task-213 Implementation Plan, "Exact DDL — Migration B"):
//
//	submission_jobs: id uuid PK DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL
//	    REFERENCES tenants(id) ON DELETE CASCADE, invoice_id uuid NOT NULL,
//	    idempotency_key text NOT NULL CHECK (char_length > 0 AND <= 255), adapter /
//	    adapter_version text NOT NULL CHECK (char_length > 0), state text NOT NULL
//	    DEFAULT 'queued' CHECK IN (queued, submitting, pending, accepted, rejected,
//	    failed), attempts int NOT NULL DEFAULT 0 CHECK (>= 0), next_poll_at timestamptz,
//	    last_error text, river_job_id bigint (soft link, NO FK), created_at / updated_at
//	    timestamptz NOT NULL DEFAULT now();
//	    CONSTRAINT submission_jobs_tenant_id_invoice_uq UNIQUE (tenant_id, id, invoice_id),
//	    CONSTRAINT submission_jobs_tenant_invoice_fk FOREIGN KEY (tenant_id, invoice_id)
//	        REFERENCES invoices (tenant_id, id) ON DELETE RESTRICT;
//	    UNIQUE INDEX submission_jobs_tenant_idem_uq (tenant_id, idempotency_key),
//	    INDEX submission_jobs_tenant_invoice_idx (tenant_id, invoice_id),
//	    a BEFORE UPDATE trigger maintaining updated_at, the verbatim M2-06 FORCE-RLS
//	    `tenant_isolation` policy, and GRANT SELECT, INSERT, UPDATE TO invoice_app
//	    (no DELETE; nothing at all to invoice_tenant_reader).
//
// Five things about this table differ from its M4-01 siblings and shape the cases below:
//
//   - The FK to `invoices` is COMPOSITE — (tenant_id, invoice_id) REFERENCES
//     invoices (tenant_id, id) — not a bare invoice_id → invoices(id). Postgres runs
//     referential-integrity checks with RLS bypassed, so a single-column FK would silently
//     accept a cross-tenant invoice; that is exactly the D8 residual
//     line_items_rls_test.go:430-477 (LI-RLS-12) DOCUMENTS as accepted for its table. Here
//     it is DEFENDED: SJ-07 asserts 23503 where LI-RLS-12 asserts success. If someone
//     "simplifies" the FK back to one column, SJ-07 is the case that goes red.
//   - `updated_at` is TRIGGER-maintained, not writer-set — the first app-owned table in
//     this repo with one (River's vendored tables maintain theirs writer-side via
//     DEFAULT CURRENT_TIMESTAMP and are not an imitable precedent). SJ-13 therefore issues
//     an UPDATE that does NOT name updated_at and requires it to move anyway, while
//     created_at stays put; a writer-maintained implementation passes neither half.
//   - The idempotency_key CHECK bound must MATCH the M2-08 ledger's own
//     (migrations/20260707193000_river_and_idempotency.sql:394 — note the plan text cites
//     392, which is off by two). SJ-10 proves the bound behaviourally on both sides: the
//     longest key submission_jobs accepts is also accepted by idempotency_keys, so a job
//     can never hold a key its own ledger row would reject at enqueue time (M5-04).
//   - A resubmission after rejection is a NEW ROW, never a reset of the old one (Core
//     AC-1). SJ-12 pins that: two rows for the SAME invoice with DIFFERENT keys must both
//     persist. The uniqueness guard is (tenant_id, idempotency_key) — SJ-11 — and
//     deliberately NOT (tenant_id, invoice_id), which would make resubmission impossible.
//   - The state vocabulary is the job's own — queued, submitting, pending, accepted,
//     rejected, failed, dead_lettered — in a column named `state`, never `status`. It
//     shares four names with invoices.status but is a different column on a different
//     table; SJ-08 inserts and reads back all seven so a copy-pasted invoice CHECK (which
//     has no `submitting` / `pending` / `dead_lettered`) fails loudly. `dead_lettered` is
//     M5-04-01's SECOND RED WAVE addition (Decision [dead-letter-state]) — a job River
//     itself discards, kept distinct from `failed` so M7-04 can list/re-drive by state and
//     M8-08 can alert on DLQ size without conflating "discarded by the queue" with "a real
//     terminal failure".
//
// Cross-tenant refusal is the load-bearing category: SJ-01/02/03/04 each assert a DISTINCT
// isolation failure mode — SELECT visibility, INSERT rejection, GUC-unset fail-closed, and
// the FORCE-bound owner path — and must not be collapsed into one "RLS works" case.
//
// Spec-to-test map (Test Specs table, task-213):
//
//	SJ-01 TestRLS_SubmissionJobsCrossTenantSelectRefused
//	SJ-02 TestRLS_SubmissionJobsCrossTenantInsertRefused
//	SJ-03 TestRLS_SubmissionJobsMissingContextFailsClosed
//	SJ-04 TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce
//	SJ-05 TestRLS_SubmissionJobsOwnRowReassignmentRefused
//	SJ-06 TestRLS_SubmissionJobsOwnTenantInsertSucceedsWithDefaults
//	SJ-07 TestRLS_SubmissionJobsCrossTenantInvoiceRefRejected
//	SJ-08 TestRLS_SubmissionJobsStateCheck
//	SJ-09 TestRLS_SubmissionJobsAttemptsNonNegative
//	SJ-10 TestRLS_SubmissionJobsIdempotencyKeyBounds
//	SJ-11 TestRLS_SubmissionJobsIdempotencyKeyUniquePerTenant
//	SJ-12 TestRLS_SubmissionJobsResubmissionCreatesSecondRow
//	SJ-13 TestRLS_SubmissionJobsUpdatedAtBumpedByTrigger
//	SJ-14 TestRLS_SubmissionJobsGrantMatrix
//	SJ-15 TestRLS_SubmissionJobsAppDeleteRefused
//	SJ-16 TestRLS_SubmissionJobsReaderSelectRefused
//	SJ-17 TestRLS_SubmissionJobsInvoiceDeleteRestricted
//	SJ-18 TestRLS_SubmissionJobsTenantIdInvoiceUniqueConstraintExists
//
// SJ-18 is a QA addition (not in task-213's original Test Specs table): none of SJ-01..17
// would catch a regression of submission_jobs_tenant_id_invoice_uq from three columns back
// to two, or to a bare CREATE UNIQUE INDEX (no pg_constraint row at all) — the failure would
// surface only when M5-01-04's migration can't find its FK target. Modeled on
// TestRLS_InvoicesTenantIdIdUniqueConstraintExists (invoices_fiscal_rls_test.go:484).
//
// Every negative assertion is paired with a positive half or a mutation-verify re-read as
// the superuser, so no case can pass against a table that simply refuses everything or is
// empty. Each rejected statement gets its OWN db.WithinTenantTx — a failed statement
// poisons the surrounding transaction (idempotency_keys_rls_test.go:445-446).
//
// Rows are seeded per-test (seedSubmissionJob below, reusing seedBusinessEntity from
// business_entities_rls_test.go and seedInvoice from invoices_rls_test.go for parents),
// NOT in the shared harness.seed() in rls_harness_test.go — that runs in TestMain before
// every test in the package, so a missing submission_jobs table would break the ENTIRE
// suite instead of failing only these SJ cases. Cleanup defers are registered BEFORE any
// assertion that can t.Fatalf, so the failure path leaks nothing into sibling cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS
// ./internal/platform/db/...` (.github/workflows/ci.yml) and `make test-rls` both pick these
// up with no workflow edit. Every case calls requireHarness(t), which SKIPS when the
// per-role DATABASE_* URLs are unset so a bare `go test ./...` stays green with no DB — note
// that under the CI gate (scripts/ci/rls-test-gate.sh) a SKIP is itself a failure, so no case
// here may add a t.Skip of its own.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_SubmissionJobs ./internal/platform/db/...
//
// (A worktree running the compose DB on an alternate host port must substitute it in all
// four DSNs — e.g. `DEV_DB_PORT=5433 make test-rls`, since Makefile:32 defaults to 5432.)
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

// sjAdapter / sjAdapterVersion are the non-empty adapter identifiers every seed and probe
// insert supplies. Both columns are NOT NULL with a char_length > 0 CHECK, so they cannot
// be omitted; their VALUES carry no meaning for any assertion here (the adapter registry
// itself is M5-02's problem).
const (
	sjAdapter        = "firs-app"
	sjAdapterVersion = "v1"
)

// failIfUndefinedSubmissionJobs turns the pre-migration failure mode into an explicit,
// self-explaining message instead of a raw driver error, following the tenants_kind_test.go
// (:63-66) / invoices_fiscal_rls_test.go (:93-101) precedent. Returns true when it fired.
func failIfUndefinedSubmissionJobs(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the submission_jobs migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedSubmissionJob inserts one submission_jobs row for tenantID/invoiceID/idempotencyKey
// as the superuser (BYPASSRLS, so seeding needs neither tenant context nor an INSERT grant)
// and returns its id plus a cleanup func. Scoped per-test — see the package doc comment
// above for why this must NOT move into the shared harness.seed(). Follows the repo's
// seedX convention (seedInvoice invoices_rls_test.go:83, seedLineItem
// line_items_rls_test.go:73, seedBusinessEntity business_entities_rls_test.go:49),
// including the 42P01 early-fatal branch that names the missing migration.
func seedSubmissionJob(t *testing.T, tenantID, invoiceID, idempotencyKey string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, tenantID, invoiceID, idempotencyKey, sjAdapter, sjAdapterVersion,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed submission_jobs: undefined_table (42P01) — submission_jobs migration not applied yet: %v", err)
		}
		t.Fatalf("seed submission_jobs: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE id = $1`, id)
	}
}

// sjKey builds a per-case unique idempotency key. Keys are unique per (tenant_id, key), and
// the harness seeds fresh random tenants per run, but a stable prefix keeps failure messages
// readable while the uuid suffix keeps reruns collision-free.
func sjKey(prefix string) string { return prefix + "-" + uuid.NewString() }

// SJ-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees only A's
// submission_jobs row; B's is invisible (filtered out, not an error). The unfiltered count
// is the load-bearing half — the two tenant_id-filtered counts would still come out right
// if RLS did nothing at all and the WHERE clause happened to do the narrowing.
func TestRLS_SubmissionJobsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "SJ-01 B Corp")
	defer cleanupEntityB()

	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-01-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "SJ-01-B")
	defer cleanupInvoiceB()

	_, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-01-A"))
	defer cleanupJobA()
	_, cleanupJobB1 := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("SJ-01-B1"))
	defer cleanupJobB1()
	_, cleanupJobB2 := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("SJ-01-B2"))
	defer cleanupJobB2()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		// RLS is the ONLY thing narrowing this one: B seeded two more rows.
		if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// SJ-02: a cross-tenant INSERT — a row named for tenant B while scoped to A — is refused
// with a WITH CHECK violation, SQLSTATE 42501, and no row lands. The positive half (an
// own-tenant INSERT of the same shape succeeding, in its OWN tx because the refusal above
// poisoned the first) is what stops this case passing against a table that refuses every
// write, e.g. one whose policy was written `USING (false)`.
func TestRLS_SubmissionJobsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-02 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "SJ-02 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-02-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "SJ-02-B")
	defer cleanupInvoiceB()

	crossKey := sjKey("SJ-02-CROSS")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key = $2`, h.tenantB, crossKey)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantB, invoiceB, crossKey, sjAdapter, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedSubmissionJobs(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	// Mutation-verify: the refusal must have left nothing behind.
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_jobs WHERE idempotency_key = $1`, crossKey); n != 0 {
		t.Errorf("rows with the cross-tenant key after the refused INSERT = %d, want 0", n)
	}

	// Positive half, in its own tx: the very same statement shape succeeds for A's own
	// tenant, so the 42501 above is isolation and not a blanket write refusal.
	ownKey := sjKey("SJ-02-OWN")
	var ownID string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE idempotency_key = $1`, ownKey)
	}()
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			h.tenantA, invoiceA, ownKey, sjAdapter, sjAdapterVersion,
		).Scan(&ownID)
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// SJ-03: a missing app.current_tenant GUC fails closed. With no context set, the isolation
// predicate's nullif(...)::uuid is NULL, the comparison is never true, and the connection
// sees nothing — a full read is the failure mode this guards. Paired with a positive
// re-read of the SAME row under a proper tenant tx, so "zero rows" cannot be an artefact of
// an empty table.
func TestRLS_SubmissionJobsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-03 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-03-A")
	defer cleanupInvoiceA()
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-03"))
	defer cleanupJob()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs`); n != 0 {
		t.Errorf("submission_jobs visible with no tenant set = %d, want 0", n)
	}

	// The row genuinely exists and is genuinely readable — with the GUC set.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs WHERE id = $1`, jobID); n != 1 {
			t.Errorf("seeded row visible WITH tenant context = %d, want 1 (the zero above must "+
				"come from the missing GUC, not from an empty table)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (positive half): %v", err)
	}
}

// SJ-04: the table OWNER (invoice_migrator) is bound by the policy under FORCE ROW LEVEL
// SECURITY exactly like the `tenants` template — a cross-tenant INSERT is refused even for
// the owner, SQLSTATE 42501. Without the FORCE line the owner would bypass the policy
// entirely and this case is the only one that notices. Paired with the owner's own-tenant
// INSERT succeeding, so the refusal is isolation rather than a missing owner privilege.
func TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-04 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "SJ-04 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-04-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "SJ-04-B")
	defer cleanupInvoiceB()

	crossKey := sjKey("SJ-04-CROSS")
	ownKey := sjKey("SJ-04-OWN")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE idempotency_key IN ($1, $2)`, crossKey, ownKey)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantB, invoiceB, crossKey, sjAdapter, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedSubmissionJobs(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_jobs WHERE idempotency_key = $1`, crossKey); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half: the owner CAN write in its own tenant scope.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantA, invoiceA, ownKey, sjAdapter, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: want success, got: %v", err)
	}
}

// SJ-05: reassigning an OWN, visible row to another tenant is refused (42501) and the row's
// tenant_id is unchanged. This is the case that catches a per-table policy copy-paste
// regression where the USING clause stopped being applied as the UPDATE's WITH CHECK and
// only validated fresh INSERTs. The positive half — an ordinary in-tenant UPDATE of `state`
// on the same row succeeding — proves the app can update this row at all, so the 42501 is
// about the tenant_id change specifically.
func TestRLS_SubmissionJobsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-05 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-05-A")
	defer cleanupInvoiceA()
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-05"))
	defer cleanupJob()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE submission_jobs SET tenant_id = $1 WHERE id = $2`, h.tenantB, jobID)
		return e
	})
	if failIfUndefinedSubmissionJobs(t, "own-row tenant reassignment", err) {
		return
	}
	assertRLSViolation(t, err)

	// Mutation-verify as the superuser: the row must still belong to A.
	var stillTenant string
	if err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM submission_jobs WHERE id = $1`, jobID).
		Scan(&stillTenant); err != nil {
		t.Fatalf("read back tenant_id after the refused UPDATE: %v", err)
	}
	if stillTenant != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %q, want unchanged %q", stillTenant, h.tenantA)
	}

	// Positive half, own tx: an in-tenant column UPDATE on the very same row succeeds.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE submission_jobs SET state = 'submitting' WHERE id = $1`, jobID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("in-tenant state UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("in-tenant state UPDATE: want success, got: %v", err)
	}
}

// SJ-06: a positive own-tenant INSERT naming only the five NOT NULL columns without a
// default succeeds, and every DEFAULT is load-bearing rather than merely present in the
// DDL: state 'queued', attempts 0, created_at/updated_at populated, and the three optional
// columns (next_poll_at, last_error, river_job_id) genuinely NULL. river_job_id being NULL
// at creation is the shape M5-04 relies on — the River job id is stitched in AFTER enqueue,
// and there is deliberately no FK to river_job (River prunes its own rows).
func TestRLS_SubmissionJobsOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-06 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-06-A")
	defer cleanupInvoiceA()

	key := sjKey("SJ-06")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE idempotency_key = $1`, key)
	}()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			h.tenantA, invoiceA, key, sjAdapter, sjAdapterVersion,
		).Scan(&id)
	})
	if failIfUndefinedSubmissionJobs(t, "own-tenant INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			state                  string
			attempts               int
			createdAt, updatedAt   time.Time
			nextPollAt             *time.Time
			lastError              *string
			riverJobID             *int64
			gotInvoice, gotAdapter string
		)
		if e := tx.QueryRow(ctx,
			`SELECT state, attempts, created_at, updated_at, next_poll_at, last_error, river_job_id,
			        invoice_id::text, adapter
			   FROM submission_jobs WHERE id = $1`, id,
		).Scan(&state, &attempts, &createdAt, &updatedAt, &nextPollAt, &lastError, &riverJobID,
			&gotInvoice, &gotAdapter); e != nil {
			return e
		}
		if state != "queued" {
			t.Errorf("state default = %q, want %q", state, "queued")
		}
		if attempts != 0 {
			t.Errorf("attempts default = %d, want 0", attempts)
		}
		if createdAt.IsZero() {
			t.Error("created_at default is the zero time, want a populated timestamp")
		}
		if updatedAt.IsZero() {
			t.Error("updated_at default is the zero time, want a populated timestamp")
		}
		if nextPollAt != nil {
			t.Errorf("next_poll_at default = %v, want NULL", *nextPollAt)
		}
		if lastError != nil {
			t.Errorf("last_error default = %q, want NULL", *lastError)
		}
		if riverJobID != nil {
			t.Errorf("river_job_id default = %d, want NULL (stitched in after enqueue, no FK)", *riverJobID)
		}
		if gotInvoice != invoiceA {
			t.Errorf("invoice_id round-trip = %q, want %q", gotInvoice, invoiceA)
		}
		if gotAdapter != sjAdapter {
			t.Errorf("adapter round-trip = %q, want %q", gotAdapter, sjAdapter)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}
}

// SJ-07: the COMPOSITE FK is the whole point. As tenant A, a job whose invoice_id belongs
// to tenant B is rejected with 23503 foreign_key_violation — the row's own tenant_id = A
// passes the policy's WITH CHECK, so RLS does NOT catch this; only
// (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id) does. Postgres runs RI checks
// with RLS bypassed, which is exactly why a bare invoice_id → invoices(id) FK would accept
// this row (the accepted D8 residual line_items_rls_test.go:430 documents for its table).
// The positive half — the identical statement with A's OWN invoice succeeding — proves the
// 23503 is about tenant mismatch, not about the FK rejecting everything.
func TestRLS_SubmissionJobsCrossTenantInvoiceRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-07 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "SJ-07 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-07-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "SJ-07-B")
	defer cleanupInvoiceB()

	danglingKey := sjKey("SJ-07-DANGLING")
	okKey := sjKey("SJ-07-OK")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE idempotency_key IN ($1, $2)`, danglingKey, okKey)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantA, invoiceB, danglingKey, sjAdapter, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedSubmissionJobs(t, "cross-tenant invoice reference", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT of a tenant-A job pointing at tenant B's invoice succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) from submission_jobs_tenant_invoice_fk — a " +
			"single-column invoice_id FK would let this through, which is the bug the composite " +
			"FK exists to close")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant invoice reference: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM submission_jobs WHERE idempotency_key = $1`, danglingKey); n != 0 {
		t.Errorf("rows after the refused cross-tenant invoice reference = %d, want 0", n)
	}

	// Positive half, own tx: A's own invoice is accepted by the very same FK.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantA, invoiceA, okKey, sjAdapter, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("same-tenant invoice reference: want success, got: %v", err)
	}
}

// SJ-08: the state CHECK admits exactly the job's own seven-value vocabulary and nothing
// else. All seven are inserted AND read back (a CHECK that silently coerced or a column
// that dropped the value would pass a rejection-only test), and out-of-vocabulary values
// are refused with 23514 — both an unrelated word ('bogus') and, more pointedly, the
// near-miss typo 'dead_letter' (missing the trailing -ed), so the CHECK is proven to match
// the vocabulary exactly rather than a permissive prefix/substring test. The six inherited
// from M5-01-03 are deliberately NOT the invoice's seven
// (draft/validated/queued/submitted/accepted/rejected/failed): a copy-pasted
// invoices_status_check would reject 'submitting' and 'pending' here and this case is what
// catches it. `dead_lettered` (M5-04-01, SECOND RED WAVE — the migration widened this
// CHECK from six values to seven, same DROP+re-ADD-under-the-same-name idiom as
// app_exchange's `connection_failed`) is the seventh; RED against SQLSTATE 23514 until the
// Executor ships it.
func TestRLS_SubmissionJobsStateCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-08 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-08-A")
	defer cleanupInvoiceA()

	keyPrefix := "SJ-08-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE idempotency_key LIKE $1`, keyPrefix+"%")
	}()

	// Every legal state inserts and reads back verbatim. Each gets its own tx so one
	// unexpected rejection cannot poison the rest. `dead_lettered` is the M5-04-01 SECOND
	// RED WAVE addition — RED against 23514 until the Executor widens the CHECK.
	for _, state := range []string{"queued", "submitting", "pending", "accepted", "rejected", "failed", "dead_lettered"} {
		key := keyPrefix + "-" + state
		var got string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
				 VALUES ($1, $2, $3, $4, $5, $6) RETURNING state`,
				h.tenantA, invoiceA, key, sjAdapter, sjAdapterVersion, state,
			).Scan(&got)
		})
		if failIfUndefinedSubmissionJobs(t, "INSERT state "+state, err) {
			return
		}
		if err != nil {
			t.Errorf("INSERT with state %q: want success (it is one of the seven legal states), got: %v", state, err)
			continue
		}
		if got != state {
			t.Errorf("state round-trip = %q, want %q", got, state)
		}
	}

	// And values outside the vocabulary are refused, each in its own tx so one unexpected
	// acceptance cannot poison the rest. 'dead_letter' is the load-bearing case: the near
	// miss (missing the trailing -ed) proves the widened CHECK matches the vocabulary
	// exactly rather than a permissive prefix/substring test that would let the typo slide
	// through as a distinct, silently-accepted eighth value.
	for _, bogus := range []string{"bogus", "dead_letter"} {
		key := keyPrefix + "-" + bogus
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				h.tenantA, invoiceA, key, sjAdapter, sjAdapterVersion, bogus,
			)
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

// SJ-09: attempts is a non-negative counter. -1 is refused with 23514; 0 (the default) and
// a positive value both insert and read back, so the CHECK bounds the column from below
// without pinning it to a single value. A retry counter that could go negative would make
// the M5-04 backoff arithmetic silently wrong rather than loud.
func TestRLS_SubmissionJobsAttemptsNonNegative(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-09 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-09-A")
	defer cleanupInvoiceA()

	keyPrefix := "SJ-09-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE idempotency_key LIKE $1`, keyPrefix+"%")
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, attempts)
			 VALUES ($1, $2, $3, $4, $5, -1)`,
			h.tenantA, invoiceA, keyPrefix+"-neg", sjAdapter, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedSubmissionJobs(t, "INSERT attempts = -1", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with attempts = -1 succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT with attempts = -1: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// Positive half: the boundary value 0 and an ordinary positive count both round-trip.
	for _, want := range []int{0, 7} {
		key := keyPrefix + "-ok-" + strings.Repeat("x", want+1)
		var got int
		if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, attempts)
				 VALUES ($1, $2, $3, $4, $5, $6) RETURNING attempts`,
				h.tenantA, invoiceA, key, sjAdapter, sjAdapterVersion, want,
			).Scan(&got)
		}); err != nil {
			t.Errorf("INSERT with attempts = %d: want success, got: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("attempts round-trip = %d, want %d", got, want)
		}
	}
}

// SJ-10: the idempotency_key CHECK bound is (0, 255] — an empty key and a 256-character key
// are both refused with 23514, and exactly 255 characters is accepted. The final half is
// the one that matters for AC-3: the SAME 255-character key is then written to the M2-08
// `idempotency_keys` ledger successfully, proving behaviourally (not by comparing constraint
// text) that a key this table accepts is always acceptable to the ledger. If someone widens
// this column's bound to, say, 512, that last INSERT starts failing and the drift is caught
// at the point where it would otherwise let M5-04 enqueue a job whose ledger row cannot be
// written. The ledger's own bound is pinned separately by
// TestRLS_IdempotencyKeysKeyLengthCheck (idempotency_keys_rls_test.go:448).
func TestRLS_SubmissionJobsIdempotencyKeyBounds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-10 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-10-A")
	defer cleanupInvoiceA()

	maxKey := strings.Repeat("m", 255)
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key IN ($2, $3, $4)`,
			h.tenantA, "", strings.Repeat("k", 256), maxKey)
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, maxKey)
	}()

	// Both rejection boundaries, each in its own tx.
	for _, c := range []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"256-character key", strings.Repeat("k", 256)},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
				 VALUES ($1, $2, $3, $4, $5)`,
				h.tenantA, invoiceA, c.key, sjAdapter, sjAdapterVersion,
			)
			return e
		})
		if failIfUndefinedSubmissionJobs(t, "INSERT with "+c.name, err) {
			return
		}
		if err == nil {
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514)", c.name)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.name, code, err)
		}
	}

	// The accepted maximum: exactly 255 characters round-trips, proving the bound is
	// (0, 255] and not something narrower.
	var gotLen int
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5) RETURNING char_length(idempotency_key)`,
			h.tenantA, invoiceA, maxKey, sjAdapter, sjAdapterVersion,
		).Scan(&gotLen)
	}); err != nil {
		t.Fatalf("INSERT with a 255-character key: want success (the CHECK's inclusive upper bound), got: %v", err)
	}
	if gotLen != 255 {
		t.Errorf("stored idempotency_key length = %d, want 255 (the column must not truncate)", gotLen)
	}

	// AC-3, the cross-table half: the longest key this table accepts must also be
	// acceptable to the M2-08 ledger, or M5-04 could create a job it can never enqueue.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, h.tenantA, maxKey)
		return e
	}); err != nil {
		t.Fatalf("the 255-character key accepted by submission_jobs was REJECTED by idempotency_keys: %v "+
			"— the two CHECK bounds have drifted (M5-01-03 AC-3: a key accepted here must always be "+
			"acceptable to the ledger)", err)
	}
}

// SJ-11: (tenant_id, idempotency_key) is unique. A second row with the same key under the
// same tenant is refused with 23505 — even pointing at a DIFFERENT invoice, which is what
// makes this a per-tenant key guard rather than a per-invoice one. The positive half is the
// tenant scoping: the identical key under tenant B is accepted, so the index is
// (tenant_id, key) and not a global unique on the key alone.
func TestRLS_SubmissionJobsIdempotencyKeyUniquePerTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-11 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "SJ-11 B Corp")
	defer cleanupEntityB()
	invoiceA1, cleanupInvoiceA1 := seedInvoice(t, h.tenantA, entityA, "SJ-11-A1")
	defer cleanupInvoiceA1()
	invoiceA2, cleanupInvoiceA2 := seedInvoice(t, h.tenantA, entityA, "SJ-11-A2")
	defer cleanupInvoiceA2()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "SJ-11-B")
	defer cleanupInvoiceB()

	key := sjKey("SJ-11")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE idempotency_key = $1`, key)
	}()

	_, cleanupFirst := seedSubmissionJob(t, h.tenantA, invoiceA1, key)
	defer cleanupFirst()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5)`,
			h.tenantA, invoiceA2, key, sjAdapter, sjAdapterVersion,
		)
		return e
	})
	if err == nil {
		t.Fatal("second row with a duplicate (tenant_id, idempotency_key) succeeded, want " +
			"unique_violation (SQLSTATE 23505) from submission_jobs_tenant_idem_uq")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate (tenant_id, idempotency_key): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}

	// Positive half, own tx: the SAME key under a different tenant is fine.
	var bID string
	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			h.tenantB, invoiceB, key, sjAdapter, sjAdapterVersion,
		).Scan(&bID)
	}); err != nil {
		t.Fatalf("the same idempotency_key under tenant B: want success (the guard is per tenant), got: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM submission_jobs WHERE idempotency_key = $1`, key); n != 2 {
		t.Errorf("rows holding this key across both tenants = %d, want 2 (one per tenant)", n)
	}
}

// SJ-12: Core AC-1's resubmission rule. A rejected job is never reset — the next attempt is
// a NEW row. Two rows for the SAME invoice with DIFFERENT keys must therefore both persist,
// each with its own id and its own state, and both must be visible under the tenant's RLS.
// This is the case that goes red if the uniqueness guard is ever "tightened" to
// (tenant_id, invoice_id), which would silently make resubmission impossible.
func TestRLS_SubmissionJobsResubmissionCreatesSecondRow(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-12 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-12-A")
	defer cleanupInvoiceA()

	firstKey := sjKey("SJ-12-ATTEMPT-1")
	secondKey := sjKey("SJ-12-ATTEMPT-2")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM submission_jobs WHERE idempotency_key IN ($1, $2)`, firstKey, secondKey)
	}()

	// Attempt 1, rejected by the authority.
	var firstID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
			 VALUES ($1, $2, $3, $4, $5, 'rejected') RETURNING id`,
			h.tenantA, invoiceA, firstKey, sjAdapter, sjAdapterVersion,
		).Scan(&firstID)
	})
	if failIfUndefinedSubmissionJobs(t, "first submission attempt", err) {
		return
	}
	if err != nil {
		t.Fatalf("first submission attempt: %v", err)
	}

	// Attempt 2 after the fix: same invoice, new key, new row.
	var secondID string
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			h.tenantA, invoiceA, secondKey, sjAdapter, sjAdapterVersion,
		).Scan(&secondID)
	}); err != nil {
		t.Fatalf("resubmission for the same invoice with a different key: want success (Core AC-1 — a "+
			"resubmission is a NEW row, never a reset), got: %v", err)
	}
	if secondID == firstID {
		t.Fatalf("resubmission reused id %q, want a distinct new row", firstID)
	}

	// BOTH rows persist, under the tenant's own RLS, and the first is untouched.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx,
			`SELECT count(*) FROM submission_jobs WHERE invoice_id = $1`, invoiceA); n != 2 {
			t.Errorf("submission_jobs rows for the resubmitted invoice = %d, want 2", n)
		}
		var firstState, secondState string
		if e := tx.QueryRow(ctx, `SELECT state FROM submission_jobs WHERE id = $1`, firstID).Scan(&firstState); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT state FROM submission_jobs WHERE id = $1`, secondID).Scan(&secondState); e != nil {
			return e
		}
		if firstState != "rejected" {
			t.Errorf("first attempt's state after resubmission = %q, want unchanged %q "+
				"(the old row must not be reset)", firstState, "rejected")
		}
		if secondState != "queued" {
			t.Errorf("resubmission's state = %q, want %q", secondState, "queued")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify both submission rows: %v", err)
	}
}

// SJ-13: updated_at is maintained by the trigger, not by the writer. The UPDATE below sets
// only `state` and never names updated_at, yet updated_at must move forward; created_at must
// not move at all. Both halves matter: a writer-maintained column fails the first, and a
// trigger that clumsily rewrote the whole row (or a created_at DEFAULT re-evaluated on
// UPDATE) fails the second. Read before/after as the SUPERUSER so the comparison is a pure
// property of the trigger, unentangled from RLS.
func TestRLS_SubmissionJobsUpdatedAtBumpedByTrigger(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-13 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-13-A")
	defer cleanupInvoiceA()
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-13"))
	defer cleanupJob()

	var beforeCreated, beforeUpdated time.Time
	var beforeState string
	if err := h.super.QueryRow(ctx,
		`SELECT created_at, updated_at, state FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&beforeCreated, &beforeUpdated, &beforeState); err != nil {
		t.Fatalf("read timestamps before the UPDATE: %v", err)
	}
	if beforeState != "queued" {
		t.Fatalf("seeded state = %q, want %q — the transition below assumes a fresh row", beforeState, "queued")
	}

	// Deliberately does NOT name updated_at. now() is the transaction timestamp, so this
	// separate transaction is guaranteed a later value than the seeding one.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE submission_jobs SET state = 'submitting' WHERE id = $1`, jobID)
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
		`SELECT created_at, updated_at, state FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&afterCreated, &afterUpdated, &afterState); err != nil {
		t.Fatalf("read timestamps after the UPDATE: %v", err)
	}

	if afterState != "submitting" {
		t.Errorf("state after the UPDATE = %q, want %q (the UPDATE itself must have landed)", afterState, "submitting")
	}
	if !afterUpdated.After(beforeUpdated) {
		t.Errorf("updated_at after an UPDATE that does not name it = %s, want strictly later than %s — "+
			"the BEFORE UPDATE trigger is missing or not firing", afterUpdated, beforeUpdated)
	}
	if !afterCreated.Equal(beforeCreated) {
		t.Errorf("created_at after the UPDATE = %s, want unchanged %s", afterCreated, beforeCreated)
	}
}

// SJ-14: the catalog half of least privilege. invoice_app holds exactly SELECT + INSERT +
// UPDATE — no DELETE, no TRUNCATE, no REFERENCES — and invoice_tenant_reader holds nothing
// at all (it is the one cross-tenant enumeration identity, and its only grant repo-wide is
// tenants/SELECT). SJ-15/SJ-16 are the behavioural half; both are needed, since a privilege
// granted but never exercised would otherwise sit unnoticed. Asked as the SUPERUSER on
// purpose: information_schema.role_table_grants shows only the current role's own grants,
// so the "reader holds nothing" claim cannot be proven from the app pool
// (idempotency_keys_rls_test.go:118-126).
func TestRLS_SubmissionJobsGrantMatrix(t *testing.T) {
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
			`SELECT has_table_privilege($1, 'public.submission_jobs', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedSubmissionJobs(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, submission_jobs, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, submission_jobs, %q) = %v, want %v — the grant is exactly "+
				"`GRANT SELECT, INSERT, UPDATE ON submission_jobs TO invoice_app` and nothing to "+
				"invoice_tenant_reader", c.role, c.priv, got, c.want)
		}
	}
}

// SJ-15: the app never deletes a submission job — the row is mutable state but a permanent
// record of the attempt. invoice_app has no DELETE grant, so even a same-tenant DELETE of a
// row it can otherwise see and update is refused at the GRANT layer (42501) before RLS is
// evaluated, and the row survives untouched. The UPDATE that follows is the positive half:
// the SAME row is reachable and writable by the SAME role, so the 42501 is specifically
// about DELETE and not about the row being invisible.
func TestRLS_SubmissionJobsAppDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-15 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-15-A")
	defer cleanupInvoiceA()
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-15"))
	defer cleanupJob()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM submission_jobs WHERE id = $1`, jobID)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on submission_jobs succeeded, want permission denied (SQLSTATE 42501) — " +
			"the grant is SELECT/INSERT/UPDATE only")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on submission_jobs: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM submission_jobs WHERE id = $1`, jobID); n != 1 {
		t.Errorf("row count after the refused DELETE = %d, want 1 (the row must survive)", n)
	}

	// Positive half, own tx: the same role can still UPDATE the same row.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE submission_jobs SET last_error = 'sj-15' WHERE id = $1`, jobID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("UPDATE after the refused DELETE affected %d rows, want 1 (the row must be reachable)",
				ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("UPDATE after the refused DELETE: want success, got: %v", err)
	}
}

// SJ-16: invoice_tenant_reader holds no grant on submission_jobs at all, so a bare SELECT as
// that role fails at the GRANT layer (42501) before RLS is evaluated. This is the
// behavioural counterpart to SJ-14's catalog check — information_schema views hide grants
// belonging to other roles, so only the live refusal proves the table was never exposed to
// the cross-tenant enumeration identity. The app-role read of the same seeded row is the
// positive half: the table exists and is readable by the role that should read it.
func TestRLS_SubmissionJobsReaderSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-16 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "SJ-16-A")
	defer cleanupInvoiceA()
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("SJ-16"))
	defer cleanupJob()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM submission_jobs`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on submission_jobs succeeded, want permission denied " +
			"(SQLSTATE 42501) — the reader must hold no grant on this table")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on submission_jobs: SQLSTATE = %q, want 42501 "+
			"(insufficient_privilege): %v", code, err)
	}

	// Positive half: the role that IS granted SELECT can read the very same row.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM submission_jobs WHERE id = $1`, jobID); got != 1 {
			t.Errorf("app-role SELECT of the seeded row = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("app-role SELECT (positive half): %v", err)
	}
}

// SJ-17: the composite FK is ON DELETE RESTRICT, matching the invoices → business_entities
// disposition — a submitted invoice is a durable fiscal record and must not be silently
// destroyed out from under the evidence of its submission. Deleting a REFERENCED invoice
// raises 23503 and both rows survive; the positive half deletes an UNREFERENCED invoice with
// the same role in the same tenant scope, so the refusal is provably about the reference and
// not about the role being unable to delete invoices at all. Run as h.mig (the owner, bound
// by FORCE RLS like every other role) inside a real tenant tx rather than as the superuser,
// which bypasses RLS and would prove less.
func TestRLS_SubmissionJobsInvoiceDeleteRestricted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "SJ-17 A Corp")
	defer cleanupEntityA()
	referenced, cleanupReferenced := seedInvoice(t, h.tenantA, entityA, "SJ-17-REFERENCED")
	defer cleanupReferenced()
	unreferenced, cleanupUnreferenced := seedInvoice(t, h.tenantA, entityA, "SJ-17-UNREFERENCED")
	defer cleanupUnreferenced() // no-op once the positive half below removes it
	jobID, cleanupJob := seedSubmissionJob(t, h.tenantA, referenced, sjKey("SJ-17"))
	defer cleanupJob() // runs FIRST (LIFO), clearing the RESTRICT before the invoice cleanup

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, referenced)
		return e
	})
	// 23001 restrict_violation, NOT 23503 foreign_key_violation: an EXPLICIT ON DELETE
	// RESTRICT is checked immediately at the DELETE, while an implicit NO ACTION FK defers
	// to end-of-statement and raises 23503 (invoices_rls_test.go:645-648, and the shipped
	// siblings TestRLS_InvoicesEntityDeleteRestricted /
	// TestRLS_BusinessEntitiesEntityDeleteRestrictedForApp both assert 23001 for this exact
	// shape). Asserting 23503 here would PASS against a weaker NO ACTION FK — the opposite
	// of what this case guards.
	if err == nil {
		t.Fatal("DELETE of an invoice referenced by a submission_jobs row succeeded, want " +
			"restrict_violation (SQLSTATE 23001) — the FK is ON DELETE RESTRICT")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("DELETE of a referenced invoice: SQLSTATE = %q, want 23001 (restrict_violation): %v", code, err)
	}

	// Mutation-verify: neither side was removed.
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1`, referenced); n != 1 {
		t.Errorf("invoices rows after the refused DELETE = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM submission_jobs WHERE id = $1`, jobID); n != 1 {
		t.Errorf("submission_jobs rows after the refused DELETE = %d, want 1", n)
	}

	// Positive half, own tx: an invoice with no submission job deletes cleanly for the
	// same role in the same tenant scope.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, unreferenced)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("DELETE of an unreferenced invoice affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("DELETE of an unreferenced invoice: want success (the RESTRICT must be about the "+
			"reference, not a blanket refusal), got: %v", err)
	}
}

// SJ-18: submission_jobs_tenant_id_invoice_uq is a REAL pg_constraint row — not a bare
// index — with its three columns in exactly (tenant_id, id, invoice_id) order. The plan
// warns that this constraint must stay at three columns because M5-01-04's app_exchange FK
// binds to it, yet no other case here would notice a regression to the two-column
// (tenant_id, id) shape, or to a bare `CREATE UNIQUE INDEX` (which produces no pg_constraint
// row at all and so cannot be an FK target). Such a regression would surface only when
// M5-01-04's migration fails to find its FK target — far from its cause. Modeled on
// TestRLS_InvoicesTenantIdIdUniqueConstraintExists (invoices_fiscal_rls_test.go:484-537),
// which does exactly this for invoices_tenant_id_id_uq. contype is filtered explicitly:
// PG18 also stores NOT NULL constraints in pg_constraint as contype='n', of which this table
// has six, so an unfiltered lookup would assert almost nothing.
func TestRLS_SubmissionJobsTenantIdInvoiceUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	n, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.submission_jobs'::regclass
		    AND contype = 'u' AND conname = 'submission_jobs_tenant_id_invoice_uq'`)
	if failIfUndefinedSubmissionJobs(t, "query pg_constraint for submission_jobs_tenant_id_invoice_uq", err) {
		return
	}
	if err != nil {
		t.Fatalf("query pg_constraint for submission_jobs_tenant_id_invoice_uq: %v", err)
	}
	if n != 1 {
		t.Fatalf("UNIQUE constraints on submission_jobs named submission_jobs_tenant_id_invoice_uq = %d, "+
			"want 1 — constraint not found; the migration is not applied yet, or it declared a bare "+
			"CREATE UNIQUE INDEX (no pg_constraint row, unusable as M5-01-04's composite-FK target)", n)
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname
		   FROM pg_constraint c
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		  WHERE c.conrelid = 'public.submission_jobs'::regclass
		    AND c.contype = 'u' AND c.conname = 'submission_jobs_tenant_id_invoice_uq'
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

	want := []string{"tenant_id", "id", "invoice_id"}
	if len(cols) != len(want) {
		t.Fatalf("submission_jobs_tenant_id_invoice_uq columns = %v, want %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("submission_jobs_tenant_id_invoice_uq column %d = %q, want %q (order is load-bearing: "+
				"M5-01-04's composite FK must name (tenant_id, id, invoice_id) in this order)",
				i+1, cols[i], want[i])
		}
	}
}
