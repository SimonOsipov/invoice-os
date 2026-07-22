// M5-01-04 (task-214): tests for `app_exchange`, the tenant-owned, APPEND-ONLY evidence log
// that records every submission attempt against the APP — request and response verbatim,
// including attempts that never reached the wire. Written BEFORE the migration exists —
// every case here is RED against SQLSTATE 42P01 undefined_table. The table the Executor will
// add (task-214 Implementation Plan, "Exact DDL — Migration C"):
//
//	app_exchange: id uuid PK DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL
//	    REFERENCES tenants(id) ON DELETE CASCADE, submission_job_id uuid NOT NULL,
//	    invoice_id uuid NOT NULL, operation text NOT NULL CHECK IN (submit, poll),
//	    outcome text NOT NULL CHECK IN (sent, blocked_rate_limit, skipped_already_cleared,
//	    transform_failed), attempt int NOT NULL CHECK (>= 1), request_body text NULL,
//	    request_headers jsonb NOT NULL DEFAULT '{}', response_body text NULL,
//	    response_headers jsonb NOT NULL DEFAULT '{}', http_status int NULL (no range CHECK),
//	    latency_ms int NULL CHECK (IS NULL OR >= 0), truncated boolean NOT NULL DEFAULT false,
//	    encoding_coerced boolean NOT NULL DEFAULT false, adapter / adapter_version text NOT
//	    NULL CHECK (char_length > 0), occurred_at timestamptz NOT NULL DEFAULT now();
//	    CONSTRAINT app_exchange_job_fk FOREIGN KEY (tenant_id, submission_job_id, invoice_id)
//	        REFERENCES submission_jobs (tenant_id, id, invoice_id) ON DELETE RESTRICT;
//	    ALTER COLUMN request_body / response_body SET COMPRESSION lz4;
//	    INDEX (tenant_id, invoice_id) and (tenant_id, submission_job_id);
//	    the verbatim M2-06 FORCE-RLS `tenant_isolation` policy, and
//	    GRANT SELECT, INSERT TO invoice_app — append-only BY GRANT, nothing at all to
//	    invoice_tenant_reader.
//
// Five things shape the cases below:
//
//   - APPEND-ONLY BY GRANT, not by trigger. invoice_app holds SELECT + INSERT and nothing
//     else, so every UPDATE and DELETE it attempts is refused at the GRANT layer (42501
//     insufficient_privilege) BEFORE the RLS policy's USING clause is evaluated. That is the
//     `invoice_status_history` precedent verbatim (20260714111246_invoice_status_history.sql
//     :16-20, M4-01 disposition D10: grant-enforced, "the idempotency_keys precedent...
//     Deliberately NO owner-proof trigger — that extra hardening belongs to audit_log"), and
//     it is the closest shipped analogue: a tenant-owned, FK-attached, invoice-child evidence
//     log with exactly this SELECT+INSERT grant. AE-08/AE-09 are the behavioural half of that
//     claim; AE-07 is the catalog half.
//
//     The HONEST CEILING of grant-only enforcement, stated plainly because a test suite that
//     implies more than it proves is worse than none: it does NOT restrain the table OWNER.
//     invoice_migrator holds implicit full DML, and FORCE ROW LEVEL SECURITY binds it to the
//     tenant PREDICATE, not to whether it may UPDATE or DELETE at all. AE-05 depends on
//     exactly that (it needs a role whose UPDATE reaches RLS). Operationally the residual is
//     small: with no app.current_tenant set, a migrator `DELETE FROM app_exchange` matches
//     ZERO rows (predicate → NULL → fail-closed), which is consistent with retention being a
//     deliberate operational act rather than an accident.
//
//   - AE-05 runs as h.mig ON PURPOSE, and it is the ONLY case here that does an UPDATE
//     expecting an RLS answer. Run as h.app it would prove nothing: with no UPDATE grant the
//     42501 comes from the ACL layer, making it a pure duplicate of AE-08 that says nothing
//     about the policy's WITH CHECK. invoice_status_history_rls_test.go:163-171 documents this
//     exact trap for the twin table and, for that reason, drops the own-row-reassignment case
//     entirely. Here it is kept but re-roled: as the owner (implicit UPDATE, bound by FORCE)
//     the refusal is a genuine row-level-security violation, and AE-05 asserts the ERROR
//     MESSAGE to prove which layer answered — "new row violates row-level security policy",
//     never "permission denied".
//
//   - The 3-column FK is the whole point of the table's referential design:
//     (tenant_id, submission_job_id, invoice_id) → submission_jobs (tenant_id, id, invoice_id).
//     One constraint buys three guarantees — same-tenant job existence (AE-11), agreement
//     between the evidence row's denormalised invoice_id and the job's (AE-12), and
//     (transitively, via submission_jobs' own composite FK) same-tenant invoice existence. It
//     is also directional armour: once evidence exists, the job's invoice_id can no longer be
//     re-pointed (AE-13) and the job cannot be deleted (AE-14).
//
//   - The log records ATTEMPTS, not responses. No CHECK ties `outcome` to response-column
//     nullability, and there is no range CHECK on http_status. AE-18 (never reached the wire:
//     transform_failed, request_body NULL too) and AE-19 (left the wire, nothing came back:
//     sent, request_body NON-NULL) are deliberately distinct on BOTH axes — different outcome
//     AND opposite request_body constraints — so neither can be satisfied by whatever makes
//     the other pass.
//
//   - The bodies are size-capped in Go (SafeBody, M5-01-05), never by a DB CHECK: a
//     schema-level cap would hard-reject an over-size evidence write exactly when something
//     has gone wrong. AE-21 therefore proves the storage path carries a full 256 KiB body
//     byte-identically, and AE-20 proves both body columns really are lz4.
//
// Spec-to-test map (Test Specs table, task-214 — spec # → AE-nn, same order):
//
//	AE-01 TestRLS_AppExchangeCrossTenantSelectRefused
//	AE-02 TestRLS_AppExchangeCrossTenantInsertRefused
//	AE-03 TestRLS_AppExchangeMissingContextFailsClosed
//	AE-04 TestRLS_AppExchangeOwnerInsertRefusedUnderForce
//	AE-05 TestRLS_AppExchangeOwnRowReassignmentRefused
//	AE-06 TestRLS_AppExchangeOwnTenantInsertSucceedsWithDefaults
//	AE-07 TestRLS_AppExchangeGrantMatrixIsSelectInsertOnly
//	AE-08 TestRLS_AppExchangeAppUpdateRefused
//	AE-09 TestRLS_AppExchangeAppDeleteRefused
//	AE-10 TestRLS_AppExchangeReaderSelectRefused
//	AE-11 TestRLS_AppExchangeCrossTenantJobRefRejected
//	AE-12 TestRLS_AppExchangeInvoiceIdMustMatchItsJob
//	AE-13 TestRLS_AppExchangeJobInvoiceRepointBlockedByEvidence
//	AE-14 TestRLS_AppExchangeJobDeleteRestrictedByEvidence
//	AE-15 TestRLS_AppExchangeOperationAndOutcomeChecks
//	AE-16 TestRLS_AppExchangeAttemptAtLeastOne
//	AE-17 TestRLS_AppExchangeLatencyNonNegative
//	AE-18 TestRLS_AppExchangePreWireAttemptStoresWithNullResponse
//	AE-19 TestRLS_AppExchangeSentWithNoResponseStores
//	AE-20 TestRLS_AppExchangeBodyColumnsUseLz4
//	AE-21 TestRLS_AppExchangeLargeBodyRoundTripsByteIdentical
//
// Every negative assertion is paired with a positive half or a mutation-verify re-read as the
// superuser, so no case can pass against a table that simply refuses everything or is empty.
// Each rejected statement gets its OWN db.WithinTenantTx — a failed statement poisons the
// surrounding transaction (idempotency_keys_rls_test.go:445-446).
//
// Rows are seeded per-test (seedAppExchange below, on top of seedBusinessEntity
// business_entities_rls_test.go:49 → seedInvoice invoices_rls_test.go:83 → seedSubmissionJob
// submission_jobs_rls_test.go:154), NOT in the shared harness.seed() in rls_harness_test.go —
// that runs in TestMain before every test in the package, so a missing app_exchange table
// would break the ENTIRE suite instead of failing only these AE cases.
//
// CLEANUP ORDERING IS LOAD-BEARING HERE, in a way it is not for the M4-01 tables: the
// evidence FK is ON DELETE RESTRICT, so an app_exchange row must be deleted BEFORE the
// submission_jobs row it references (and that before its invoice, whose own FK is likewise
// RESTRICT). Go's LIFO `defer` gives that for free provided the exchange row is seeded AFTER
// its job — the natural order — so every case below simply registers each cleanup with
// `defer` immediately, in seed order. Registering the defer BEFORE any assertion that can
// t.Fatalf is the other half of the rule (the IK-06 bug): a case that Goexits mid-way must
// still tear down what it created, or it leaves rows that break sibling cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS ./internal/platform/db/...`
// (.github/workflows/ci.yml) and `make test-rls` both pick these up with no workflow edit.
// Every case calls requireHarness(t), which SKIPS when the per-role DATABASE_* URLs are unset
// so a bare `go test ./...` stays green with no DB — note that under the CI gate
// (scripts/ci/rls-test-gate.sh) a SKIP is itself a failure, so no case here may add a t.Skip
// of its own.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_AppExchange ./internal/platform/db/...
//
// (A worktree running the compose DB on an alternate host port must substitute it in all four
// DSNs — e.g. `DEV_DB_PORT=5433 make test-rls`, since Makefile:32 defaults to 5432.)
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// aeBodyChunk is exactly 64 BYTES of VALID UTF-8: 62 ASCII characters plus one two-byte 'é'.
// Both properties are load-bearing for AE-21. Postgres `text` cannot store a NUL byte
// (22021 "null character not permitted") and rejects any byte sequence that is not valid
// UTF-8 (22021 `invalid byte sequence for encoding "UTF8"`) — verified live against this
// worktree's DB. A test that built its "256 KiB byte string" from arbitrary bytes would fail
// at the INSERT for that reason and never exercise the TOAST/compression path at all. (Those
// two failure modes are precisely the condition `encoding_coerced` exists to record: the
// writer must coerce such a body before it can be stored, and flag that it did.) The multi-
// byte rune is included on purpose so the round-trip covers encoding, not just length.
const aeBodyChunk = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZé"

// aeBodySize is the 256 KiB cap SafeBody (M5-01-05) enforces in Go. Nothing in the schema
// caps a body — a DB CHECK would hard-reject an over-size evidence write exactly when
// something has gone wrong — so this is the size the storage path must carry intact.
const aeBodySize = 256 * 1024

// failIfUndefinedAppExchange turns the pre-migration failure mode into an explicit,
// self-explaining message instead of a raw driver error or a misleading "want 42501, got
// 42P01", following the failIfUndefinedSubmissionJobs (submission_jobs_rls_test.go:138) /
// tenants_kind_test.go:63-66 precedent. Returns true when it fired.
func failIfUndefinedAppExchange(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the app_exchange migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// pgMessage extracts the primary error message from err, or "" if err does not wrap a
// *pgconn.PgError. Companion to pgCode (tenants_kind_test.go:34): AE-05 needs to prove WHICH
// layer refused an UPDATE, and both layers answer with the same SQLSTATE 42501 — only the
// message distinguishes an RLS WITH CHECK violation from a missing table privilege.
func pgMessage(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return ""
}

// aeMarker builds a per-case unique `adapter` value. app_exchange has no natural business key
// (no idempotency_key like submission_jobs), so a probe INSERT that MIGHT succeed when it
// should not still needs a handle for cleanup — the adapter column, whose only constraint is
// char_length > 0, carries one. The uuid suffix keeps reruns collision-free.
func aeMarker(prefix string) string { return prefix + "-" + uuid.NewString() }

// seedAppExchange inserts one app_exchange row for tenantID/jobID/invoiceID as the superuser
// (BYPASSRLS, so seeding needs neither tenant context nor an INSERT grant) and returns its id
// plus a cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed(), and for why its cleanup must run before the
// referenced job's (ON DELETE RESTRICT). Follows the repo's seedX convention including the
// 42P01 early-fatal branch that names the missing migration.
func seedAppExchange(t *testing.T, tenantID, jobID, invoiceID string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO app_exchange
		     (id, tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
		 VALUES ($1, $2, $3, $4, 'submit', 'sent', 1, $5, $6)`,
		id, tenantID, jobID, invoiceID, sjAdapter, sjAdapterVersion,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed app_exchange: undefined_table (42P01) — app_exchange migration not applied yet: %v", err)
		}
		t.Fatalf("seed app_exchange: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE id = $1`, id)
	}
}

// AE-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees only A's
// app_exchange row; B's are invisible (filtered out, not an error). The UNFILTERED count is
// the load-bearing half — the two tenant_id-filtered counts would still come out right if RLS
// did nothing at all and the WHERE clause happened to do the narrowing. B gets TWO rows so an
// accidental "sees everything" (3) is distinguishable from the correct answer (1).
func TestRLS_AppExchangeCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "AE-01 B Corp")
	defer cleanupEntityB()

	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-01-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "AE-01-B")
	defer cleanupInvoiceB()

	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-01-A"))
	defer cleanupJobA()
	jobB, cleanupJobB := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("AE-01-B"))
	defer cleanupJobB()

	// Seeded AFTER their jobs, so LIFO defer deletes the evidence FIRST — the job FK is
	// ON DELETE RESTRICT and a job cleanup that ran first would silently fail and leak.
	_, cleanupExA := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupExA()
	_, cleanupExB1 := seedAppExchange(t, h.tenantB, jobB, invoiceB)
	defer cleanupExB1()
	_, cleanupExB2 := seedAppExchange(t, h.tenantB, jobB, invoiceB)
	defer cleanupExB2()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM app_exchange`); n != 1 {
			t.Errorf("unfiltered count under A's RLS = %d, want 1 (A's own row only; B seeded 2 more)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// AE-02: a cross-tenant INSERT — a row named for tenant B, referencing B's real job and
// invoice, written from a tx scoped to tenant A — is refused by the policy's WITH CHECK,
// SQLSTATE 42501. B's genuine job/invoice are used on purpose so the FK is satisfiable and
// the ONLY thing wrong with the row is its tenant. Paired with the same statement shape
// succeeding for A's own tenant, so the 42501 is isolation and not a blanket write refusal.
func TestRLS_AppExchangeCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-02 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "AE-02 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-02-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "AE-02-B")
	defer cleanupInvoiceB()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-02-A"))
	defer cleanupJobA()
	jobB, cleanupJobB := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("AE-02-B"))
	defer cleanupJobB()

	crossMarker := aeMarker("AE-02-CROSS")
	ownMarker := aeMarker("AE-02-OWN")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM app_exchange WHERE adapter IN ($1, $2)`, crossMarker, ownMarker)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantB, jobB, invoiceB, crossMarker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE adapter = $1`, crossMarker); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the identical statement shape succeeds for A's own tenant.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobA, invoiceA, ownMarker, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("own-tenant INSERT of the same shape: want success, got: %v", err)
	}
}

// AE-03: a missing app.current_tenant GUC fails closed. With no context set, the isolation
// predicate's nullif(...)::uuid is NULL, the comparison is never true, and the connection sees
// nothing — a full read of every tenant's evidence is the failure mode this guards. Paired
// with a positive re-read of the SAME row under a proper tenant tx, so "zero rows" cannot be
// an artefact of an empty table.
func TestRLS_AppExchangeMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-03 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-03-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-03"))
	defer cleanupJobA()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupEx()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM app_exchange`)
	if failIfUndefinedAppExchange(t, "SELECT with no tenant context", err) {
		return
	}
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("app_exchange visible with no tenant set = %d, want 0", n)
	}

	// The row genuinely exists and is genuinely readable — with the GUC set.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); got != 1 {
			t.Errorf("seeded row visible WITH tenant context = %d, want 1 (the zero above must come "+
				"from the missing GUC, not from an empty table)", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (positive half): %v", err)
	}
}

// AE-04: the table OWNER (invoice_migrator) is bound by the policy under FORCE ROW LEVEL
// SECURITY exactly like the `tenants` template — a cross-tenant INSERT is refused even for the
// owner, SQLSTATE 42501. Without the FORCE line the owner would bypass the policy entirely and
// this case is the only one that notices. Paired with the owner's own-tenant INSERT succeeding,
// so the refusal is isolation rather than a missing owner privilege.
func TestRLS_AppExchangeOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-04 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "AE-04 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-04-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "AE-04-B")
	defer cleanupInvoiceB()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-04-A"))
	defer cleanupJobA()
	jobB, cleanupJobB := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("AE-04-B"))
	defer cleanupJobB()

	crossMarker := aeMarker("AE-04-CROSS")
	ownMarker := aeMarker("AE-04-OWN")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM app_exchange WHERE adapter IN ($1, $2)`, crossMarker, ownMarker)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantB, jobB, invoiceB, crossMarker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "owner cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE adapter = $1`, crossMarker); n != 0 {
		t.Errorf("rows after the owner's refused cross-tenant INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the owner CAN write inside its own tenant scope.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobA, invoiceA, ownMarker, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("owner own-tenant INSERT: want success, got: %v", err)
	}
}

// AE-05: reassigning an OWN, visible row to another tenant is refused by the policy's WITH
// CHECK (42501, "new row violates row-level security policy") and the row's tenant_id is
// unchanged. This is the case that catches a policy copy-paste regression where the USING
// clause stopped being applied as the UPDATE's WITH CHECK and only validated fresh INSERTs.
//
// IT RUNS AS h.mig (the table owner), NOT h.app, and the role difference is the entire point.
// invoice_app has NO UPDATE grant on this append-only table, so as h.app the 42501 would come
// from the ACL layer before RLS was ever consulted — making this a pure duplicate of AE-08 and
// proving nothing about the policy. invoice_status_history_rls_test.go:163-171 documents that
// exact trap for the twin table (and drops its own-row-reassignment case for it). The owner
// holds implicit UPDATE and is bound by FORCE ROW LEVEL SECURITY, so its UPDATE reaches — and
// is refused by — the policy itself. The message assertion below is what pins WHICH layer
// answered: both layers use SQLSTATE 42501, only the text distinguishes them.
func TestRLS_AppExchangeOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-05 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-05-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-05"))
	defer cleanupJobA()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupEx()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE app_exchange SET tenant_id = $1 WHERE id = $2`, h.tenantB, exchangeID)
		return e
	})
	if failIfUndefinedAppExchange(t, "owner own-row tenant reassignment", err) {
		return
	}
	assertRLSViolation(t, err)

	msg := strings.ToLower(pgMessage(err))
	if !strings.Contains(msg, "row-level security") {
		t.Errorf("owner reassignment error message = %q, want one mentioning \"row-level security policy\" — "+
			"a \"permission denied\" message would mean the refusal came from the GRANT layer (as it "+
			"would for invoice_app, which holds no UPDATE), leaving the policy's WITH CHECK unproven",
			pgMessage(err))
	}
	if strings.Contains(msg, "permission denied") {
		t.Errorf("owner reassignment error message = %q, want an RLS violation, not an ACL refusal — "+
			"the owner must hold UPDATE for this case to test the policy at all", pgMessage(err))
	}

	// Mutation-verify as the superuser: the row must still belong to A.
	var stillTenant string
	if err := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM app_exchange WHERE id = $1`, exchangeID).
		Scan(&stillTenant); err != nil {
		t.Fatalf("read back tenant_id after the refused UPDATE: %v", err)
	}
	if stillTenant != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %q, want unchanged %q", stillTenant, h.tenantA)
	}

	// Positive half, own tx: an in-tenant column UPDATE on the very same row, by the very same
	// role, succeeds — so the 42501 above is about the tenant_id change specifically and not
	// about the owner being unable to update this row. (That the owner CAN update an
	// append-only table at all is the acknowledged ceiling of grant-only enforcement; see the
	// package doc comment. invoice_app cannot — AE-08.)
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE app_exchange SET truncated = true WHERE id = $1`, exchangeID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("owner in-tenant UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("owner in-tenant UPDATE: want success, got: %v", err)
	}
}

// AE-06: a positive own-tenant INSERT naming only the columns with no default succeeds, and
// every DEFAULT is load-bearing rather than merely present in the DDL: request_headers and
// response_headers both '{}', truncated and encoding_coerced both false, occurred_at
// populated, and the four optional columns (request_body, response_body, http_status,
// latency_ms) genuinely NULL.
//
// The second and third phases prove the AC-4 claim that truncated and encoding_coerced are TWO
// INDEPENDENT booleans, both NOT NULL: each is set true with the other left false and read back
// (a merged single flag, or a column wired to the wrong one, fails here), and an explicit NULL
// into either is rejected with 23502 (a nullable column with DEFAULT false would pass every
// other case in this file).
func TestRLS_AppExchangeOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-06 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-06-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-06"))
	defer cleanupJobA()

	marker := aeMarker("AE-06")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5) RETURNING id`,
			h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
		).Scan(&id)
	})
	if failIfUndefinedAppExchange(t, "own-tenant INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			reqHeaders, respHeaders     string
			truncated, encodingCoerced  bool
			occurredAt                  time.Time
			requestBody, responseBody   *string
			httpStatus, latencyMs       *int
			gotJob, gotInvoice, gotOper string
		)
		if e := tx.QueryRow(ctx,
			`SELECT request_headers::text, response_headers::text, truncated, encoding_coerced, occurred_at,
			        request_body, response_body, http_status, latency_ms,
			        submission_job_id::text, invoice_id::text, operation
			   FROM app_exchange WHERE id = $1`, id,
		).Scan(&reqHeaders, &respHeaders, &truncated, &encodingCoerced, &occurredAt,
			&requestBody, &responseBody, &httpStatus, &latencyMs,
			&gotJob, &gotInvoice, &gotOper); e != nil {
			return e
		}
		if reqHeaders != "{}" {
			t.Errorf("request_headers default = %q, want %q", reqHeaders, "{}")
		}
		if respHeaders != "{}" {
			t.Errorf("response_headers default = %q, want %q", respHeaders, "{}")
		}
		if truncated {
			t.Error("truncated default = true, want false")
		}
		if encodingCoerced {
			t.Error("encoding_coerced default = true, want false")
		}
		if occurredAt.IsZero() {
			t.Error("occurred_at default is the zero time, want a populated timestamp")
		}
		if requestBody != nil {
			t.Errorf("request_body default = %q, want NULL", *requestBody)
		}
		if responseBody != nil {
			t.Errorf("response_body default = %q, want NULL", *responseBody)
		}
		if httpStatus != nil {
			t.Errorf("http_status default = %d, want NULL", *httpStatus)
		}
		if latencyMs != nil {
			t.Errorf("latency_ms default = %d, want NULL", *latencyMs)
		}
		if gotJob != jobA {
			t.Errorf("submission_job_id round-trip = %q, want %q", gotJob, jobA)
		}
		if gotInvoice != invoiceA {
			t.Errorf("invoice_id round-trip = %q, want %q", gotInvoice, invoiceA)
		}
		if gotOper != "submit" {
			t.Errorf("operation round-trip = %q, want %q", gotOper, "submit")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}

	// The two flags are INDEPENDENT: each can be true while the other is false.
	for _, c := range []struct {
		name                       string
		truncated, encodingCoerced bool
	}{
		{"truncated only", true, false},
		{"encoding_coerced only", false, true},
		{"both", true, true},
	} {
		var flagID string
		if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO app_exchange
				     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
				      truncated, encoding_coerced, adapter, adapter_version)
				 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5, $6, $7) RETURNING id`,
				h.tenantA, jobA, invoiceA, c.truncated, c.encodingCoerced, marker, sjAdapterVersion,
			).Scan(&flagID)
		}); err != nil {
			t.Fatalf("INSERT with %s: want success, got: %v", c.name, err)
		}

		var gotTruncated, gotCoerced bool
		if err := h.super.QueryRow(ctx,
			`SELECT truncated, encoding_coerced FROM app_exchange WHERE id = $1`, flagID,
		).Scan(&gotTruncated, &gotCoerced); err != nil {
			t.Fatalf("read back flags for %s: %v", c.name, err)
		}
		if gotTruncated != c.truncated || gotCoerced != c.encodingCoerced {
			t.Errorf("%s: (truncated, encoding_coerced) = (%v, %v), want (%v, %v) — the two flags are "+
				"independent (a size-capped body and a lossily-encoded body are different facts) and "+
				"must not be merged into one column",
				c.name, gotTruncated, gotCoerced, c.truncated, c.encodingCoerced)
		}
	}

	// Both flags are NOT NULL: an explicit NULL is rejected (23502 not_null_violation). Each
	// rejected statement gets its own tx.
	for _, col := range []string{"truncated", "encoding_coerced"} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO app_exchange
				     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
				      `+col+`, adapter, adapter_version)
				 VALUES ($1, $2, $3, 'submit', 'sent', 1, NULL, $4, $5)`,
				h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
			)
			return e
		})
		if err == nil {
			t.Fatalf("INSERT with %s = NULL succeeded, want not_null_violation (SQLSTATE 23502)", col)
		}
		if code := pgCode(err); code != "23502" {
			t.Fatalf("INSERT with %s = NULL: SQLSTATE = %q, want 23502 (not_null_violation): %v", col, code, err)
		}
	}
}

// AE-07: the catalog half of least privilege — the table is APPEND-ONLY BY GRANT. invoice_app
// holds exactly SELECT + INSERT: no UPDATE, no DELETE, no TRUNCATE, no REFERENCES; and
// invoice_tenant_reader holds nothing at all (it is the one cross-tenant enumeration identity,
// and its only grant repo-wide is tenants/SELECT). AE-08/AE-09/AE-10 are the behavioural half;
// both are needed, since a privilege granted but never exercised would otherwise sit unnoticed,
// and a behavioural refusal alone cannot prove the reader was never granted anything. Asked as
// the SUPERUSER on purpose: information_schema.role_table_grants shows only the current role's
// own grants, so the "reader holds nothing" claim cannot be proven from the app pool
// (idempotency_keys_rls_test.go:118-126).
func TestRLS_AppExchangeGrantMatrixIsSelectInsertOnly(t *testing.T) {
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
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.app_exchange', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedAppExchange(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, app_exchange, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, app_exchange, %q) = %v, want %v — the grant is exactly "+
				"`GRANT SELECT, INSERT ON app_exchange TO invoice_app` (append-only by grant, the "+
				"invoice_status_history precedent) and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// AE-08: append-only, behavioural half. invoice_app has no UPDATE grant, so an UPDATE of its
// OWN, visible row is refused at the GRANT layer (42501 insufficient_privilege) before RLS is
// evaluated — the same shape invoice_status_history_rls_test.go:289 proves for the twin table,
// and distinct in cause from AE-05's policy-level 42501 on the same statement as the owner. The
// superuser read-back proves the row survived untouched; the SELECT that follows is the
// positive half, showing the SAME row is reachable by the SAME role so the refusal is
// specifically about UPDATE and not about the row being invisible.
func TestRLS_AppExchangeAppUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-08 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-08-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-08"))
	defer cleanupJobA()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupEx()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE app_exchange SET truncated = true WHERE id = $1`, exchangeID)
		return e
	})
	if failIfUndefinedAppExchange(t, "app-role UPDATE", err) {
		return
	}
	if err == nil {
		t.Fatal("app-role UPDATE of an own, visible app_exchange row succeeded, want permission denied " +
			"(SQLSTATE 42501) — the table is append-only by grant, invoice_app holds no UPDATE")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role UPDATE on app_exchange: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	// Mutation-verify as the superuser: nothing moved.
	var truncated bool
	if err := h.super.QueryRow(ctx, `SELECT truncated FROM app_exchange WHERE id = $1`, exchangeID).
		Scan(&truncated); err != nil {
		t.Fatalf("read back truncated after the refused UPDATE: %v", err)
	}
	if truncated {
		t.Error("truncated after the refused UPDATE = true, want unchanged false")
	}

	// Positive half: the same role can still READ the same row.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); n != 1 {
			t.Errorf("app-role SELECT of the row it could not update = %d, want 1 (the 42501 must be "+
				"about UPDATE, not about the row being invisible)", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("app-role SELECT (positive half): %v", err)
	}
}

// AE-09: append-only, the DELETE half. invoice_app has no DELETE grant, so even a same-tenant
// DELETE of a row it can otherwise see is refused at the GRANT layer (42501) and the row must
// survive — evidence of a submission attempt is never removed by the application. Paired with
// the same role reading the same row afterwards.
func TestRLS_AppExchangeAppDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-09 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-09-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-09"))
	defer cleanupJobA()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupEx()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM app_exchange WHERE id = $1`, exchangeID)
		return e
	})
	if failIfUndefinedAppExchange(t, "app-role DELETE", err) {
		return
	}
	if err == nil {
		t.Fatal("app-role DELETE on app_exchange succeeded, want permission denied (SQLSTATE 42501) — " +
			"the table is append-only by grant, invoice_app holds no DELETE")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on app_exchange: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); n != 1 {
		t.Errorf("rows after the refused DELETE = %d, want 1 (the evidence row must survive)", n)
	}

	// Positive half: the same role can still READ the same row.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); n != 1 {
			t.Errorf("app-role SELECT of the row it could not delete = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("app-role SELECT (positive half): %v", err)
	}
}

// AE-10: invoice_tenant_reader — the one cross-tenant enumeration identity — holds NO grant on
// app_exchange at all, so a bare SELECT fails at the GRANT layer (42501) before RLS is even
// evaluated. Submission evidence is never exposed to it. No other case here connects as
// h.reader, so a future migration that widened the GRANT would slip through unnoticed without
// this one. Paired with the app role reading the very same row, so the refusal is provably
// about the reader's privileges rather than an empty or unreadable table.
func TestRLS_AppExchangeReaderSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-10 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-10-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-10"))
	defer cleanupJobA()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, jobA, invoiceA)
	defer cleanupEx()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM app_exchange`).Scan(&n)
	if failIfUndefinedAppExchange(t, "reader SELECT", err) {
		return
	}
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on app_exchange succeeded, want permission denied " +
			"(SQLSTATE 42501) — the reader must hold no grant on this table")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on app_exchange: SQLSTATE = %q, want 42501 "+
			"(insufficient_privilege): %v", code, err)
	}

	// Positive half: the role that IS granted SELECT can read the very same row.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if got := mustCount(t, tx, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); got != 1 {
			t.Errorf("app-role SELECT of the seeded row = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("app-role SELECT (positive half): %v", err)
	}
}

// AE-11: the 3-column FK's first guarantee — same-tenant job existence. As tenant A, an
// evidence row whose submission_job_id (and invoice_id) belong to tenant B is rejected with
// 23503 foreign_key_violation. RLS does NOT catch this: the row's own tenant_id = A passes the
// policy's WITH CHECK, and Postgres runs referential-integrity checks with RLS BYPASSED — only
// the composite (tenant_id, submission_job_id, invoice_id) → submission_jobs (tenant_id, id,
// invoice_id) constraint refuses it. A bare submission_job_id → submission_jobs(id) FK would
// accept this row. The positive half — the identical statement with A's OWN job — proves the
// refusal is about the tenant mismatch and not a blanket rejection.
func TestRLS_AppExchangeCrossTenantJobRefRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-11 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "AE-11 B Corp")
	defer cleanupEntityB()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-11-A")
	defer cleanupInvoiceA()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "AE-11-B")
	defer cleanupInvoiceB()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-11-A"))
	defer cleanupJobA()
	jobB, cleanupJobB := seedSubmissionJob(t, h.tenantB, invoiceB, sjKey("AE-11-B"))
	defer cleanupJobB()

	crossMarker := aeMarker("AE-11-CROSS")
	ownMarker := aeMarker("AE-11-OWN")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM app_exchange WHERE adapter IN ($1, $2)`, crossMarker, ownMarker)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobB, invoiceB, crossMarker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "evidence row referencing another tenant's job", err) {
		return
	}
	if err == nil {
		t.Fatal("evidence row referencing another tenant's submission job succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) — the FK is composite on (tenant_id, " +
			"submission_job_id, invoice_id); a bare submission_job_id FK would accept this")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant job reference: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE adapter = $1`, crossMarker); n != 0 {
		t.Errorf("rows after the refused cross-tenant job reference = %d, want 0", n)
	}

	// Positive half, own tx: the same shape with A's own job succeeds.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobA, invoiceA, ownMarker, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("evidence row referencing A's OWN job: want success, got: %v", err)
	}
}

// AE-12: the 3-column FK's second guarantee — the evidence row's denormalised invoice_id must
// AGREE with its job's. Same tenant, real job, real invoice, but the wrong pairing: a job
// created for invoice X, with the evidence row claiming invoice Y. Rejected with 23503. Two
// independent single-column FKs (job → submission_jobs, invoice → invoices) would each be
// individually satisfied here and let the two invoice_ids drift apart silently — which is the
// whole reason the constraint is composite. The positive half is the same statement with the
// job's real invoice.
func TestRLS_AppExchangeInvoiceIdMustMatchItsJob(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-12 A Corp")
	defer cleanupEntityA()
	invoiceX, cleanupInvoiceX := seedInvoice(t, h.tenantA, entityA, "AE-12-X")
	defer cleanupInvoiceX()
	invoiceY, cleanupInvoiceY := seedInvoice(t, h.tenantA, entityA, "AE-12-Y")
	defer cleanupInvoiceY()
	jobX, cleanupJobX := seedSubmissionJob(t, h.tenantA, invoiceX, sjKey("AE-12"))
	defer cleanupJobX()

	mismatchMarker := aeMarker("AE-12-MISMATCH")
	matchMarker := aeMarker("AE-12-MATCH")
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM app_exchange WHERE adapter IN ($1, $2)`, mismatchMarker, matchMarker)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobX, invoiceY, mismatchMarker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "evidence row whose invoice_id disagrees with its job's", err) {
		return
	}
	if err == nil {
		t.Fatal("evidence row whose invoice_id disagrees with its job's succeeded, want " +
			"foreign_key_violation (SQLSTATE 23503) — the composite FK is what keeps the " +
			"denormalised invoice_id honest; two single-column FKs would both be satisfied here")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("invoice_id/job disagreement: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE adapter = $1`, mismatchMarker); n != 0 {
		t.Errorf("rows after the refused mismatched INSERT = %d, want 0", n)
	}

	// Positive half, own tx: the job's REAL invoice is accepted.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5)`,
			h.tenantA, jobX, invoiceX, matchMarker, sjAdapterVersion,
		)
		return e
	}); err != nil {
		t.Fatalf("evidence row with the job's real invoice: want success, got: %v", err)
	}
}

// AE-13: the FK is DIRECTIONAL ARMOUR — once evidence exists, the job's invoice_id can no
// longer be re-pointed. The 23503 is raised by app_exchange_job_fk's IMPLICIT ON UPDATE NO
// ACTION (confupdtype = 'a'): changing submission_jobs.invoice_id changes the referenced key of
// a row app_exchange still references. (Not, as the plan's prose says, by the job's own FK or
// unique constraint — those are unaffected by this UPDATE. The SQLSTATE is right, the
// attribution was not.) Measured live on the shipped submission_jobs → invoices FK of the same
// shape: repointing a referenced key raises 23503 foreign_key_violation, while an explicit
// ON DELETE RESTRICT raises 23001 — see AE-14. The positive half repoints an UNREFERENCED job
// with the same role in the same tenant scope, so the refusal is provably about the evidence.
func TestRLS_AppExchangeJobInvoiceRepointBlockedByEvidence(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-13 A Corp")
	defer cleanupEntityA()
	invoiceX, cleanupInvoiceX := seedInvoice(t, h.tenantA, entityA, "AE-13-X")
	defer cleanupInvoiceX()
	invoiceY, cleanupInvoiceY := seedInvoice(t, h.tenantA, entityA, "AE-13-Y")
	defer cleanupInvoiceY()
	withEvidence, cleanupWithEvidence := seedSubmissionJob(t, h.tenantA, invoiceX, sjKey("AE-13-EVID"))
	defer cleanupWithEvidence()
	// Seeded after its job so LIFO clears the evidence before the job it RESTRICTs.
	_, cleanupEx := seedAppExchange(t, h.tenantA, withEvidence, invoiceX)
	defer cleanupEx()
	withoutEvidence, cleanupWithoutEvidence := seedSubmissionJob(t, h.tenantA, invoiceX, sjKey("AE-13-BARE"))
	defer cleanupWithoutEvidence()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE submission_jobs SET invoice_id = $1 WHERE id = $2`, invoiceY, withEvidence)
		return e
	})
	if failIfUndefinedAppExchange(t, "repoint a job that has evidence", err) {
		return
	}
	if err == nil {
		t.Fatal("re-pointing the invoice_id of a submission job that already has app_exchange evidence " +
			"succeeded, want foreign_key_violation (SQLSTATE 23503) — the evidence FK's implicit " +
			"ON UPDATE NO ACTION is what freezes it")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("repoint of a job with evidence: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	// Mutation-verify as the superuser: the job still points at its original invoice.
	var stillInvoice string
	if err := h.super.QueryRow(ctx, `SELECT invoice_id::text FROM submission_jobs WHERE id = $1`, withEvidence).
		Scan(&stillInvoice); err != nil {
		t.Fatalf("read back invoice_id after the refused repoint: %v", err)
	}
	if stillInvoice != invoiceX {
		t.Errorf("job invoice_id after the refused repoint = %q, want unchanged %q", stillInvoice, invoiceX)
	}

	// Positive half, own tx: a job with NO evidence repoints cleanly for the same role in the
	// same tenant scope.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE submission_jobs SET invoice_id = $1 WHERE id = $2`, invoiceY, withoutEvidence)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("repoint of an evidence-free job affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("repoint of an evidence-free job: want success (the freeze must be about the evidence, "+
			"not a blanket refusal), got: %v", err)
	}
}

// AE-14: the evidence FK is ON DELETE RESTRICT — a submission job that has evidence cannot be
// deleted, so the record of what was sent can never be orphaned or silently destroyed.
//
// The SQLSTATE is 23001 restrict_violation, NOT the 23503 the plan's spec table names. An
// EXPLICIT ON DELETE RESTRICT is checked immediately at the DELETE and raises 23001; an
// implicit NO ACTION FK defers to end-of-statement and raises 23503. Measured live in this
// worktree against the shipped submission_jobs → invoices FK, which has exactly this shape:
// DELETE of a referenced invoice → 23001, UPDATE of the referenced key → 23503 (AE-13).
// Asserting 23503 here would PASS against a weaker NO ACTION FK — the opposite of what this
// case guards — which is the same trap submission_jobs_rls_test.go:1144-1150 documents.
//
// Runs as h.mig: invoice_app holds no DELETE on submission_jobs (SJ-15), so as h.app the
// refusal would come from the ACL layer and never reach the FK at all. The positive half
// deletes an evidence-free job with the same role in the same tenant scope.
func TestRLS_AppExchangeJobDeleteRestrictedByEvidence(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-14 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-14-A")
	defer cleanupInvoiceA()
	withEvidence, cleanupWithEvidence := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-14-EVID"))
	defer cleanupWithEvidence()
	exchangeID, cleanupEx := seedAppExchange(t, h.tenantA, withEvidence, invoiceA)
	defer cleanupEx() // runs FIRST (LIFO), clearing the RESTRICT before the job cleanup
	withoutEvidence, cleanupWithoutEvidence := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-14-BARE"))
	defer cleanupWithoutEvidence() // no-op once the positive half below removes it

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM submission_jobs WHERE id = $1`, withEvidence)
		return e
	})
	if failIfUndefinedAppExchange(t, "delete a job that has evidence", err) {
		return
	}
	if err == nil {
		t.Fatal("DELETE of a submission job referenced by an app_exchange row succeeded, want " +
			"restrict_violation (SQLSTATE 23001) — the evidence FK is ON DELETE RESTRICT")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("DELETE of a job with evidence: SQLSTATE = %q, want 23001 (restrict_violation) — an "+
			"explicit ON DELETE RESTRICT raises 23001, while a weaker implicit NO ACTION would raise "+
			"23503: %v", code, err)
	}

	// Mutation-verify: neither side was removed.
	if n := mustCount(t, h.super, `SELECT count(*) FROM submission_jobs WHERE id = $1`, withEvidence); n != 1 {
		t.Errorf("submission_jobs rows after the refused DELETE = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM app_exchange WHERE id = $1`, exchangeID); n != 1 {
		t.Errorf("app_exchange rows after the refused DELETE = %d, want 1", n)
	}

	// Positive half, own tx: a job with no evidence deletes cleanly for the same role in the
	// same tenant scope.
	if err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM submission_jobs WHERE id = $1`, withoutEvidence)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("DELETE of an evidence-free job affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("DELETE of an evidence-free job: want success (the RESTRICT must be about the "+
			"evidence, not a blanket refusal), got: %v", err)
	}
}

// AE-15: the `operation` and `outcome` CHECKs. Each rejects an out-of-set value (23514) and,
// critically, all EIGHT legal (operation × outcome) combinations insert successfully — the log
// records a poll that was blocked by the rate-limit gate exactly as readily as a submit that
// reached the wire, and no cross-column CHECK may quietly forbid a pairing. Each rejected
// statement gets its own tx (a failed statement poisons the surrounding transaction).
func TestRLS_AppExchangeOperationAndOutcomeChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-15 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-15-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-15"))
	defer cleanupJobA()

	marker := aeMarker("AE-15")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	for _, bad := range []struct{ operation, outcome string }{
		{"bogus", "sent"},
		{"submit", "bogus"},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO app_exchange
				     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
				 VALUES ($1, $2, $3, $4, $5, 1, $6, $7)`,
				h.tenantA, jobA, invoiceA, bad.operation, bad.outcome, marker, sjAdapterVersion,
			)
			return e
		})
		if failIfUndefinedAppExchange(t, "INSERT with an out-of-set operation/outcome", err) {
			return
		}
		if err == nil {
			t.Fatalf("INSERT with (operation, outcome) = (%q, %q) succeeded, want CHECK violation "+
				"(SQLSTATE 23514)", bad.operation, bad.outcome)
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("INSERT with (operation, outcome) = (%q, %q): SQLSTATE = %q, want 23514 "+
				"(check_violation): %v", bad.operation, bad.outcome, code, err)
		}
	}

	// All 2 x 4 legal combinations are accepted and round-trip.
	for _, operation := range []string{"submit", "poll"} {
		for _, outcome := range []string{"sent", "blocked_rate_limit", "skipped_already_cleared", "transform_failed"} {
			var id string
			if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx,
					`INSERT INTO app_exchange
					     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
					 VALUES ($1, $2, $3, $4, $5, 1, $6, $7) RETURNING id`,
					h.tenantA, jobA, invoiceA, operation, outcome, marker, sjAdapterVersion,
				).Scan(&id)
			}); err != nil {
				t.Fatalf("INSERT with (operation, outcome) = (%q, %q): want success, got: %v",
					operation, outcome, err)
			}

			var gotOperation, gotOutcome string
			if err := h.super.QueryRow(ctx,
				`SELECT operation, outcome FROM app_exchange WHERE id = $1`, id,
			).Scan(&gotOperation, &gotOutcome); err != nil {
				t.Fatalf("read back (operation, outcome) for (%q, %q): %v", operation, outcome, err)
			}
			if gotOperation != operation || gotOutcome != outcome {
				t.Errorf("(operation, outcome) round-trip = (%q, %q), want (%q, %q)",
					gotOperation, gotOutcome, operation, outcome)
			}
		}
	}
}

// AE-16: `attempt` mirrors submission_jobs.attempts AT WRITE TIME, and a written attempt is
// always at least the first one — attempt = 0 is rejected (23514) while attempt = 1 is accepted
// and round-trips. The positive half matters: a CHECK miscoded as `attempt > 1` would reject 0
// too and pass a negative-only test.
func TestRLS_AppExchangeAttemptAtLeastOne(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-16 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-16-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-16"))
	defer cleanupJobA()

	marker := aeMarker("AE-16")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 0, $4, $5)`,
			h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "INSERT with attempt = 0", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with attempt = 0 succeeded, want CHECK violation (SQLSTATE 23514) — an evidence " +
			"row always records at least the first attempt")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT with attempt = 0: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// Positive half, own tx: attempt = 1 is accepted and round-trips.
	var id string
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5) RETURNING id`,
			h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
		).Scan(&id)
	}); err != nil {
		t.Fatalf("INSERT with attempt = 1: want success, got: %v", err)
	}

	var attempt int
	if err := h.super.QueryRow(ctx, `SELECT attempt FROM app_exchange WHERE id = $1`, id).Scan(&attempt); err != nil {
		t.Fatalf("read back attempt: %v", err)
	}
	if attempt != 1 {
		t.Errorf("attempt round-trip = %d, want 1", attempt)
	}
}

// AE-17: `latency_ms` is `IS NULL OR >= 0`. A negative latency is rejected (23514); NULL is
// accepted (the attempt that never got a response has no latency to record — AE-18/AE-19); and
// 0 is accepted too. The 0 case is what separates the real CHECK from a miscoded
// `CHECK (latency_ms IS NULL)`, which would also reject -1 and pass a NULL-plus-negative test.
func TestRLS_AppExchangeLatencyNonNegative(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-17 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-17-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-17"))
	defer cleanupJobA()

	marker := aeMarker("AE-17")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, latency_ms, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, -1, $4, $5)`,
			h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
		)
		return e
	})
	if failIfUndefinedAppExchange(t, "INSERT with latency_ms = -1", err) {
		return
	}
	if err == nil {
		t.Fatal("INSERT with latency_ms = -1 succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("INSERT with latency_ms = -1: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// Positive halves, each in its own tx: NULL (no response ever arrived) and 0 (a response
	// that arrived immediately) are both accepted and round-trip.
	for _, want := range []*int{nil, intPtr(0)} {
		var id string
		if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO app_exchange
				     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, latency_ms, adapter, adapter_version)
				 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5, $6) RETURNING id`,
				h.tenantA, jobA, invoiceA, want, marker, sjAdapterVersion,
			).Scan(&id)
		}); err != nil {
			t.Fatalf("INSERT with latency_ms = %v: want success, got: %v", want, err)
		}

		var got *int
		if err := h.super.QueryRow(ctx, `SELECT latency_ms FROM app_exchange WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read back latency_ms: %v", err)
		}
		switch {
		case want == nil && got != nil:
			t.Errorf("latency_ms round-trip = %d, want NULL", *got)
		case want != nil && got == nil:
			t.Errorf("latency_ms round-trip = NULL, want %d", *want)
		case want != nil && got != nil && *want != *got:
			t.Errorf("latency_ms round-trip = %d, want %d", *got, *want)
		}
	}
}

// intPtr is a local helper for AE-17's nil/0 table — a *int literal cannot be written inline.
func intPtr(v int) *int { return &v }

// AE-18: the log is a record of ATTEMPTS, not of responses. An attempt that never reached the
// wire — the transform failed before a request was even built — stores successfully with
// request_body, response_body, http_status and latency_ms ALL NULL, and reads back unchanged.
// Nothing in the schema may require a body or a response for a row to exist.
//
// Deliberately distinct from AE-19 on BOTH axes: different outcome ('transform_failed' vs
// 'sent') AND opposite request_body constraint (NULL here, non-NULL there). Neither case can be
// satisfied by whatever makes the other pass.
func TestRLS_AppExchangePreWireAttemptStoresWithNullResponse(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-18 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-18-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-18"))
	defer cleanupJobA()

	marker := aeMarker("AE-18")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
			      request_body, response_body, http_status, latency_ms, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'transform_failed', 1, NULL, NULL, NULL, NULL, $4, $5)
			 RETURNING id`,
			h.tenantA, jobA, invoiceA, marker, sjAdapterVersion,
		).Scan(&id)
	})
	if failIfUndefinedAppExchange(t, "pre-wire attempt INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("pre-wire attempt (outcome = 'transform_failed', nothing sent, nothing received): "+
			"want success — the log records attempts, not responses — got: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			outcome                   string
			requestBody, responseBody *string
			httpStatus, latencyMs     *int
		)
		if e := tx.QueryRow(ctx,
			`SELECT outcome, request_body, response_body, http_status, latency_ms
			   FROM app_exchange WHERE id = $1`, id,
		).Scan(&outcome, &requestBody, &responseBody, &httpStatus, &latencyMs); e != nil {
			return e
		}
		if outcome != "transform_failed" {
			t.Errorf("outcome round-trip = %q, want %q", outcome, "transform_failed")
		}
		if requestBody != nil {
			t.Errorf("request_body = %q, want NULL (nothing was ever built)", *requestBody)
		}
		if responseBody != nil {
			t.Errorf("response_body = %q, want NULL", *responseBody)
		}
		if httpStatus != nil {
			t.Errorf("http_status = %d, want NULL", *httpStatus)
		}
		if latencyMs != nil {
			t.Errorf("latency_ms = %d, want NULL", *latencyMs)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back the pre-wire attempt: %v", err)
	}
}

// AE-19: the wire-timeout case, the most common real submission failure. outcome = 'sent' with
// a NON-NULL request_body but response_body, http_status and latency_ms all NULL — the request
// left, no response ever arrived. It must store, which means NO CHECK may tie outcome = 'sent'
// to a response being present. A schema that added one would reject exactly the evidence that
// matters most.
//
// The mirror image of AE-18 (see its comment): different outcome, opposite request_body
// constraint. The non-NULL request_body is asserted by VALUE, not merely by presence, so a
// column silently dropping or truncating it is caught.
func TestRLS_AppExchangeSentWithNoResponseStores(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-19 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-19-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-19"))
	defer cleanupJobA()

	marker := aeMarker("AE-19")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	const requestBody = `{"irn":"AE-19","items":[{"desc":"widget","qty":1}]}`

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
			      request_body, response_body, http_status, latency_ms, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, NULL, NULL, NULL, $5, $6)
			 RETURNING id`,
			h.tenantA, jobA, invoiceA, requestBody, marker, sjAdapterVersion,
		).Scan(&id)
	})
	if failIfUndefinedAppExchange(t, "sent-with-no-response INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("outcome = 'sent' with a request body but no response (the wire-timeout case): want "+
			"success — no CHECK may tie outcome to response nullability — got: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			outcome               string
			gotRequest            string
			responseBody          *string
			httpStatus, latencyMs *int
		)
		if e := tx.QueryRow(ctx,
			`SELECT outcome, request_body, response_body, http_status, latency_ms
			   FROM app_exchange WHERE id = $1`, id,
		).Scan(&outcome, &gotRequest, &responseBody, &httpStatus, &latencyMs); e != nil {
			return e
		}
		if outcome != "sent" {
			t.Errorf("outcome round-trip = %q, want %q", outcome, "sent")
		}
		if gotRequest != requestBody {
			t.Errorf("request_body round-trip = %q, want %q (preserved verbatim as evidence)", gotRequest, requestBody)
		}
		if responseBody != nil {
			t.Errorf("response_body = %q, want NULL (nothing came back)", *responseBody)
		}
		if httpStatus != nil {
			t.Errorf("http_status = %d, want NULL (nothing came back)", *httpStatus)
		}
		if latencyMs != nil {
			t.Errorf("latency_ms = %d, want NULL (nothing came back)", *latencyMs)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back the sent-with-no-response row: %v", err)
	}
}

// AE-20: both body columns really are lz4-compressed — `ALTER TABLE ... ALTER COLUMN ... SET
// COMPRESSION lz4`, applied at table birth (compression only affects rows written afterwards).
//
// The value is read as attcompression::text. Measured live in this worktree: a column left at
// the DEFAULT reports the zero byte, which renders as an EMPTY STRING (length 0) — it is NOT
// 'p'. Only an explicit `SET COMPRESSION pglz` yields 'p'; lz4 yields 'l'. A negative written as
// `!= 'p'` would therefore pass against a table with no compression set at all, which is exactly
// the regression this case exists to catch.
//
// `adapter` is checked alongside as the discriminating control: it is a plain text column with
// no SET COMPRESSION, so it MUST report the default. Without it, a query bug returning 'l' for
// every column would pass.
func TestRLS_AppExchangeBodyColumnsUseLz4(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		column string
		want   string
		why    string
	}{
		{"request_body", "l", "SET COMPRESSION lz4"},
		{"response_body", "l", "SET COMPRESSION lz4"},
		{"adapter", "", "left at the default (the zero byte renders as an empty string, NOT 'p')"},
	} {
		var got string
		err := h.super.QueryRow(ctx,
			`SELECT attcompression::text
			   FROM pg_attribute
			  WHERE attrelid = 'public.app_exchange'::regclass AND attname = $1`, c.column,
		).Scan(&got)
		if failIfUndefinedAppExchange(t, "pg_attribute lookup for "+c.column, err) {
			return
		}
		if err != nil {
			t.Fatalf("pg_attribute.attcompression for app_exchange.%s: %v", c.column, err)
		}
		if got != c.want {
			t.Errorf("pg_attribute.attcompression for app_exchange.%s = %q, want %q (%s). "+
				"'l' = lz4, 'p' = explicit pglz, empty = default/InvalidCompressionMethod",
				c.column, got, c.want, c.why)
		}
	}
}

// AE-21: a full 256 KiB body round-trips BYTE-IDENTICAL through both body columns, proving the
// TOAST/lz4 storage path preserves the evidence rather than merely accepting it. 256 KiB is the
// cap SafeBody (M5-01-05) enforces in Go; the schema imposes none, deliberately.
//
// The payload is valid UTF-8 with no NUL — see aeBodyChunk for why that is not optional — and
// includes a multi-byte rune so the assertion covers encoding and not just length. Both the full
// string equality AND the exact byte length are asserted: equality alone would not distinguish a
// silent short-read if the comparison operand were also short, and length alone would not catch
// corrupted content.
func TestRLS_AppExchangeLargeBodyRoundTripsByteIdentical(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	if len(aeBodyChunk) != 64 {
		t.Fatalf("aeBodyChunk is %d bytes, want exactly 64 — the 256 KiB payload below is built by "+
			"repeating it and would no longer be exactly %d bytes", len(aeBodyChunk), aeBodySize)
	}
	body := strings.Repeat(aeBodyChunk, aeBodySize/len(aeBodyChunk))
	if len(body) != aeBodySize {
		t.Fatalf("constructed body is %d bytes, want %d", len(body), aeBodySize)
	}

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "AE-21 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "AE-21-A")
	defer cleanupInvoiceA()
	jobA, cleanupJobA := seedSubmissionJob(t, h.tenantA, invoiceA, sjKey("AE-21"))
	defer cleanupJobA()

	marker := aeMarker("AE-21")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM app_exchange WHERE adapter = $1`, marker)
	}()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app_exchange
			     (tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
			      request_body, response_body, http_status, latency_ms, adapter, adapter_version)
			 VALUES ($1, $2, $3, 'submit', 'sent', 1, $4, $5, 200, 1200, $6, $7)
			 RETURNING id`,
			h.tenantA, jobA, invoiceA, body, body, marker, sjAdapterVersion,
		).Scan(&id)
	})
	if failIfUndefinedAppExchange(t, "256 KiB body INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("INSERT of a %d-byte request_body and response_body: want success (nothing in the "+
			"schema caps a body — the cap lives in Go), got: %v", aeBodySize, err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var gotRequest, gotResponse string
		if e := tx.QueryRow(ctx,
			`SELECT request_body, response_body FROM app_exchange WHERE id = $1`, id,
		).Scan(&gotRequest, &gotResponse); e != nil {
			return e
		}
		for _, c := range []struct {
			column string
			got    string
		}{
			{"request_body", gotRequest},
			{"response_body", gotResponse},
		} {
			if len(c.got) != aeBodySize {
				t.Errorf("%s read back as %d bytes, want %d — a short read means the storage path "+
					"truncated the evidence", c.column, len(c.got), aeBodySize)
			}
			if c.got != body {
				t.Errorf("%s did not round-trip byte-identically (lengths: got %d, want %d)",
					c.column, len(c.got), len(body))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read back the 256 KiB bodies: %v", err)
	}
}
