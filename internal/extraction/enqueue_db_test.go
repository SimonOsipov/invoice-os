// enqueue_db_test.go: the DB-backed specs for the sanctioned enqueue seam. Package
// extraction_test, so it shares store_db_test.go's TestMain, per-role pools and single skip
// site, and worker_db_test.go's fixtures and River harness.
package extraction_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// eqRLSViolation is the SQLSTATE idempotency_keys' tenant_isolation policy raises on an INSERT
// whose tenant_id is not app.current_tenant.
const eqRLSViolation = "42501"

var eqErrRollback = errors.New("enqueue suite: intentional rollback")

// eqKey spells the seam's business key here rather than calling into the seam: a key the test
// recomputes through the production helper would agree with it on any format at all.
func eqKey(documentID string) string { return "extract:" + documentID }

type eqJobRow struct {
	id           int64
	state        string
	queue        string
	kind         string
	argsTenant   string
	argsDocument string
	argsKey      string
}

// eqJobRows reads every river_job the tenant owns, as the superuser: river_job carries no
// tenant_id, so the payload is the only tenant handle it has.
func eqJobRows(t *testing.T, ctx context.Context, tenantID string) []eqJobRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT id, state::text, queue, kind,
		        coalesce(args->>'tenant_id', ''), coalesce(args->>'document_id', ''),
		        coalesce(args->>'idempotency_key', '')
		   FROM river_job WHERE args->>'tenant_id' = $1 ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("read river_job rows for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var out []eqJobRow
	for rows.Next() {
		var r eqJobRow
		if err := rows.Scan(&r.id, &r.state, &r.queue, &r.kind,
			&r.argsTenant, &r.argsDocument, &r.argsKey); err != nil {
			t.Fatalf("scan river_job row for tenant %s: %v", tenantID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate river_job rows for tenant %s: %v", tenantID, err)
	}
	return out
}

// eqOneJob returns the tenant's single river_job row, failing when there is any other number of
// them: every caller below reads a field off it.
func eqOneJob(t *testing.T, ctx context.Context, tenantID string) eqJobRow {
	t.Helper()
	got := eqJobRows(t, ctx, tenantID)
	if len(got) != 1 {
		t.Fatalf("tenant %s owns %d river_job row(s), want exactly 1", tenantID, len(got))
	}
	return got[0]
}

func eqKeyCount(t *testing.T, ctx context.Context, tenantID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count idempotency_keys for tenant %s: %v", tenantID, err)
	}
	return n
}

func eqKeyExists(t *testing.T, ctx context.Context, tenantID, key string) bool {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`,
		tenantID, key).Scan(&n); err != nil {
		t.Fatalf("look up idempotency key %q for tenant %s: %v", key, tenantID, err)
	}
	return n > 0
}

// eqEnqueue runs the seam inside a committing tenant transaction, the shape the request path
// gets from db.WithinRequestTenantTx.
func eqEnqueue(t *testing.T, ctx context.Context, c *queue.Client, tenantID, documentID string) bool {
	t.Helper()
	var skipped bool
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		var e error
		skipped, e = extraction.EnqueueExtraction(ctx, tx, c, tenantID, documentID)
		return e
	}); err != nil {
		t.Fatalf("EnqueueExtraction for document %s: %v", documentID, err)
	}
	return skipped
}

// --- specs ---------------------------------------------------------------------------

// AC-1 on the wire. The queue and max_attempts come from extractArgs.InsertOpts, which the seam
// must not override with an opts of its own.
func TestRLS_EnqueueExtractionInsertsOneJobOfTheRightKind(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	if skipped := eqEnqueue(t, ctx, wkInsertClient(t), tenantID, documentID); skipped {
		t.Fatalf("the first EnqueueExtraction for document %s reported skipped; the assertions below would read a row nothing inserted", documentID)
	}

	r := eqOneJob(t, ctx, tenantID)
	if r.kind != "extraction_extract" {
		t.Errorf("river_job %d carries kind %q, want %q", r.id, r.kind, "extraction_extract")
	}
	if r.queue != extraction.QueueName {
		t.Errorf("river_job %d landed on queue %q, want %q -- a non-nil opts overrides extractArgs.InsertOpts", r.id, r.queue, extraction.QueueName)
	}
	if r.argsTenant != tenantID {
		t.Errorf("river_job %d carries args.tenant_id %q, want %q", r.id, r.argsTenant, tenantID)
	}
	if r.argsDocument != documentID {
		t.Errorf("river_job %d carries args.document_id %q, want %q", r.id, r.argsDocument, documentID)
	}
	if r.argsKey != eqKey(documentID) {
		t.Errorf("river_job %d carries args.idempotency_key %q, want %q -- the key format is what makes the dedupe per-document", r.id, r.argsKey, eqKey(documentID))
	}

	if n := eqKeyCount(t, ctx, tenantID); n != 1 {
		t.Errorf("tenant %s owns %d idempotency_keys row(s), want 1", tenantID, n)
	}
	if !eqKeyExists(t, ctx, tenantID, eqKey(documentID)) {
		t.Errorf("no idempotency_keys row for tenant %s under key %q; the outbox recorded some other key, so the dedupe below keys on the wrong thing", tenantID, eqKey(documentID))
	}
}

// AC-2. idempotency_keys' PRIMARY KEY (tenant_id, key) is the ONLY dedupe layer here --
// river.UniqueOpts is set nowhere on extractArgs -- so the second call inserts neither the key
// nor the job.
func TestRLS_EnqueueExtractionIsIdempotentPerDocument(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	c := wkInsertClient(t)

	if skipped := eqEnqueue(t, ctx, c, tenantID, documentID); skipped {
		t.Fatalf("the first EnqueueExtraction for document %s reported skipped; a second call skipping proves nothing when the first one did too", documentID)
	}
	if skipped := eqEnqueue(t, ctx, c, tenantID, documentID); !skipped {
		t.Errorf("the second EnqueueExtraction for document %s returned skipped=false, want true", documentID)
	}

	if got := eqJobRows(t, ctx, tenantID); len(got) != 1 {
		t.Errorf("tenant %s owns %d river_job row(s) after two calls, want 1", tenantID, len(got))
	}
	if n := eqKeyCount(t, ctx, tenantID); n != 1 {
		t.Errorf("tenant %s owns %d idempotency_keys row(s) after two calls, want 1", tenantID, n)
	}
}

// AC-2's transactional half: the key and the job share the caller's transaction, so a rollback
// leaves neither behind and the document can be enqueued again.
func TestRLS_EnqueueExtractionRollsBackWithItsTransaction(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	c := wkInsertClient(t)

	var skipped bool
	err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		var e error
		skipped, e = extraction.EnqueueExtraction(ctx, tx, c, tenantID, documentID)
		if e != nil {
			return e
		}
		return eqErrRollback
	})
	if !errors.Is(err, eqErrRollback) {
		t.Fatalf("the transaction returned %v, want the planted rollback error; the enqueue never reached the rollback, so the empty counts below mean nothing", err)
	}
	if skipped {
		t.Fatalf("the seam reported skipped inside the rolled-back transaction; it inserted nothing, so there was nothing for the rollback to undo")
	}

	if got := eqJobRows(t, ctx, tenantID); len(got) != 0 {
		t.Errorf("tenant %s owns %d river_job row(s) after the rollback, want 0", tenantID, len(got))
	}
	if n := eqKeyCount(t, ctx, tenantID); n != 0 {
		t.Errorf("tenant %s owns %d idempotency_keys row(s) after the rollback, want 0", tenantID, n)
	}
}

// The permanent-dedupe DECISION, pinned so a later reader does not read it as a bug and "fix"
// it. invoice_app holds SELECT and INSERT on idempotency_keys and no DELETE, so a document
// whose extraction dead-letters can never be re-extracted through this seam. Re-extraction is
// EXTR-17's; do not widen the key here.
func TestRLS_EnqueueExtractionRefusesEvenAfterTheJobDeadLetters(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-09: the extractor always fails")
	c := wkClient(t, wkWorker(t, wkFailing(boom), wkNewOpener()), extraction.QueueName)

	if skipped := eqEnqueue(t, ctx, c, tenantID, documentID); skipped {
		t.Fatalf("the first EnqueueExtraction for document %s reported skipped; there is no job to dead-letter", documentID)
	}
	id := eqOneJob(t, ctx, tenantID).id
	wkStart(t, c)

	wkAwaitRiverState(t, id, "discarded", wkFastBudget,
		"an extractor that always fails must exhaust MaxAttempts, or the re-enqueue below runs against a job that is still retrying")
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, id), "dead_lettered")

	if skipped := eqEnqueue(t, ctx, c, tenantID, documentID); !skipped {
		t.Errorf("EnqueueExtraction for dead-lettered document %s returned skipped=false, want true: the dedupe is permanent by decision, not in-flight", documentID)
	}
	if got := eqJobRows(t, ctx, tenantID); len(got) != 1 {
		t.Errorf("tenant %s owns %d river_job row(s) after the re-enqueue, want 1", tenantID, len(got))
	}
	if n := eqKeyCount(t, ctx, tenantID); n != 1 {
		t.Errorf("tenant %s owns %d idempotency_keys row(s) after the re-enqueue, want 1", tenantID, n)
	}
}

// Fail-closed on tenant divergence: the outbox row is written under the transaction's
// app.current_tenant, so a tenantID that is not the transaction's is refused by the policy
// before either row lands.
func TestRLS_EnqueueExtractionRefusesATenantThatIsNotTheTxTenant(t *testing.T) {
	ctx := t.Context()
	tenantA, _ := wkFixture(t, ctx)
	tenantB, documentB := wkFixture(t, ctx)

	var skipped bool
	err := db.WithinTenantTx(ctx, stRequire(t).app, tenantA, func(tx pgx.Tx) error {
		var e error
		skipped, e = extraction.EnqueueExtraction(ctx, tx, wkInsertClient(t), tenantB, documentB)
		return e
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != eqRLSViolation {
		t.Fatalf("enqueueing tenant %s inside tenant %s's transaction returned %v, want SQLSTATE %s from idempotency_keys' tenant_isolation policy",
			tenantB, tenantA, err, eqRLSViolation)
	}
	if skipped {
		t.Errorf("the refused call reported skipped=true; a refusal is not a duplicate, and a caller that reads it as one drops the error")
	}

	for _, tenantID := range []string{tenantA, tenantB} {
		if got := eqJobRows(t, ctx, tenantID); len(got) != 0 {
			t.Errorf("tenant %s owns %d river_job row(s) after the refused call, want 0", tenantID, len(got))
		}
		if n := eqKeyCount(t, ctx, tenantID); n != 0 {
			t.Errorf("tenant %s owns %d idempotency_keys row(s) after the refused call, want 0", tenantID, n)
		}
	}
}

// AC-1 end to end: the seam's own job -- not a fixture built by hand -- is what the extraction
// worker fetches. wkEnqueue mints a uuid key and calls EnqueueTx directly, so every other
// worker spec would still pass if the seam enqueued onto the wrong queue or under the wrong
// kind.
func TestRLS_EnqueuedJobIsFetchedByTheExtractionWorker(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	c := wkClient(t, wkWorker(t, ext, op), extraction.QueueName)

	if skipped := eqEnqueue(t, ctx, c, tenantID, documentID); skipped {
		t.Fatalf("EnqueueExtraction for document %s reported skipped; there is no job for the worker to fetch", documentID)
	}
	id := eqOneJob(t, ctx, tenantID).id
	wkStart(t, c)

	wkAwaitRiverState(t, id, "completed", wkFastBudget,
		"a client configured for the extraction queue must fetch the seam's own job")

	// "past queued" spelled as the state it actually reaches: a row stuck in queued and a row
	// that was never minted both read as not-succeeded.
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, id), "succeeded")
	if n := ext.count(); n != 1 {
		t.Errorf("the extractor ran %d time(s), want 1", n)
	}
	if seen := op.first(t); seen.doc != documentID {
		t.Errorf("OpenDocument was asked for document %q, want the seam's %q", seen.doc, documentID)
	}
}
