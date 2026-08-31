// enqueue_adversarial_test.go: the negative, edge and concurrency specs for the enqueue seam,
// plus the oracle for the retirement note. Shares enqueue_db_test.go's helpers and
// store_db_test.go's single skip site.
package extraction_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// eqBadUUID is the SQLSTATE the uuid cast raises on a blank tenant id.
const eqBadUUID = "22P02"

// eqEnqueueErr is eqEnqueue for the calls that must fail: it hands the error back rather than
// ending the test on it.
func eqEnqueueErr(t *testing.T, ctx context.Context, c *queue.Client, txTenant, argTenant, documentID string) (bool, error) {
	t.Helper()
	var skipped bool
	err := db.WithinTenantTx(ctx, stRequire(t).app, txTenant, func(tx pgx.Tx) error {
		var e error
		skipped, e = extraction.EnqueueExtraction(ctx, tx, c, argTenant, documentID)
		return e
	})
	return skipped, err
}

// A blank tenant is refused before either row lands. Nothing upstream of the seam parses the
// tenant as a uuid, so the type system is not what closes this.
func TestRLS_EnqueueExtractionRefusesABlankTenant(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	skipped, err := eqEnqueueErr(t, ctx, wkInsertClient(t), tenantID, "", documentID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != eqBadUUID {
		t.Fatalf("EnqueueExtraction with a blank tenant returned %v, want SQLSTATE %s: idempotency_keys.tenant_id is uuid, so the cast refuses the blank before the policy is ever consulted", err, eqBadUUID)
	}
	if skipped {
		t.Errorf("the refused call reported skipped=true; a caller reading that as a duplicate drops the error")
	}
	if got := eqJobRows(t, ctx, tenantID); len(got) != 0 {
		t.Errorf("tenant %s owns %d river_job row(s) after the refused call, want 0", tenantID, len(got))
	}
	if n := eqKeyCount(t, ctx, tenantID); n != 0 {
		t.Errorf("tenant %s owns %d idempotency_keys row(s) after the refused call, want 0", tenantID, n)
	}
}

// A blank document is NOT refused: the key is "extract:", non-empty, so queue.EnqueueTx's own
// blank-key guard never fires and the first blank call burns that key for the tenant for good.
// The seam is string-in by design; validating the document id is the caller's (EXTR-09-02's).
func TestRLS_EnqueueExtractionAcceptsABlankDocumentAndBurnsOneKeyForever(t *testing.T) {
	ctx := t.Context()
	tenantID, _ := wkFixture(t, ctx)
	c := wkInsertClient(t)

	if skipped, err := eqEnqueueErr(t, ctx, c, tenantID, tenantID, ""); err != nil || skipped {
		t.Fatalf("EnqueueExtraction with a blank document returned (skipped=%v, %v), want (false, nil): the seam does not validate the document id", skipped, err)
	}
	r := eqOneJob(t, ctx, tenantID)
	if r.argsDocument != "" {
		t.Errorf("river_job %d carries args.document_id %q, want the blank the caller passed", r.id, r.argsDocument)
	}
	if !eqKeyExists(t, ctx, tenantID, eqKey("")) {
		t.Errorf("no idempotency_keys row for tenant %s under key %q; the blank document keyed on something else", tenantID, eqKey(""))
	}

	// The cost: every later blank-document call for this tenant is swallowed as a duplicate.
	if skipped, err := eqEnqueueErr(t, ctx, c, tenantID, tenantID, ""); err != nil || !skipped {
		t.Errorf("the second blank-document call returned (skipped=%v, %v), want (true, nil)", skipped, err)
	}
	if got := eqJobRows(t, ctx, tenantID); len(got) != 1 {
		t.Errorf("tenant %s owns %d river_job row(s) after two blank-document calls, want 1", tenantID, len(got))
	}
}

// The seam does not prove the document belongs to the tenant -- there is no read and no foreign
// key on the path. It fails closed anyway: the worker re-establishes the JOB's tenant, so the
// foreign document is unreadable and the job dies without the body ever being opened.
// EXTR-09-02 owns the 404; this pins that the gap is not also a leak.
func TestRLS_EnqueueExtractionCannotReachAForeignTenantsDocument(t *testing.T) {
	ctx := t.Context()
	tenantA, _ := wkFixture(t, ctx)
	tenantB, documentB := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	c := wkClient(t, wkWorker(t, ext, op), extraction.QueueName)

	if skipped, err := eqEnqueueErr(t, ctx, c, tenantA, tenantA, documentB); err != nil || skipped {
		t.Fatalf("EnqueueExtraction for tenant %s over tenant %s's document returned (skipped=%v, %v), want (false, nil): the seam checks no ownership", tenantA, tenantB, skipped, err)
	}
	id := eqOneJob(t, ctx, tenantA).id
	wkStart(t, c)

	wkAwaitRiverState(t, id, "discarded", wkFastBudget,
		"a job naming a document its tenant cannot read must die, not extract")

	if n := op.count(); n != 0 {
		t.Errorf("OpenDocument was called %d time(s) for a foreign tenant's document, want 0", n)
	}
	if n := ext.count(); n != 0 {
		t.Errorf("the extractor ran %d time(s) over a foreign tenant's document, want 0", n)
	}
	if got := eqJobRows(t, ctx, tenantB); len(got) != 0 {
		t.Errorf("tenant %s owns %d river_job row(s), want 0: tenant %s's call must not enqueue under it", tenantB, len(got), tenantA)
	}
}

// eqAwaitLockWait blocks until some backend is waiting on an ungranted lock, so the racer below
// is proven contended rather than assumed to be. Two goroutines released from one channel can
// run end to end in sequence, which would assert nothing a serial call does not.
func eqAwaitLockWait(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(wkFastBudget)
	for time.Now().Before(deadline) {
		var n int
		if err := stRequire(t).super.QueryRow(ctx,
			`SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&n); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no backend waited on a lock within %s; the second enqueue never contended with the first, so this spec would prove only what the serial idempotency spec proves", wkFastBudget)
}

// eqHoldOpen begins a tenant transaction and enqueues on it WITHOUT committing, so a second
// enqueue can be observed blocking. db.WithinTenantTx cannot hand back an open tx, so the GUC it
// sets is set here by hand -- the seam's only precondition.
func eqHoldOpen(t *testing.T, ctx context.Context, c *queue.Client, tenantID, documentID string) pgx.Tx {
	t.Helper()
	tx, err := stRequire(t).app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holding transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("scope the holding transaction to tenant %s: %v", tenantID, err)
	}
	skipped, err := extraction.EnqueueExtraction(ctx, tx, c, tenantID, documentID)
	if err != nil || skipped {
		t.Fatalf("the holding EnqueueExtraction returned (skipped=%v, %v), want (false, nil)", skipped, err)
	}
	return tx
}

// queue.EnqueueTx's doc comment claims a key and its job always share one fate under a
// concurrent duplicate. Proven through THIS seam, and both ways: the loser skips when the holder
// commits, and inserts when the holder rolls back. Neither outcome leaves a key without a job.
func TestRLS_EnqueueExtractionConcurrentDuplicatesShareOneFate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commit      bool
		wantSkipped bool
	}{
		{name: "the holder commits, so the loser skips", commit: true, wantSkipped: true},
		{name: "the holder rolls back, so the loser inserts", commit: false, wantSkipped: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tenantID, documentID := wkFixture(t, ctx)
			c := wkInsertClient(t)

			held := eqHoldOpen(t, ctx, c, tenantID, documentID)

			type outcome struct {
				skipped bool
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				var skipped bool
				err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
					var e error
					skipped, e = extraction.EnqueueExtraction(ctx, tx, c, tenantID, documentID)
					return e
				})
				done <- outcome{skipped: skipped, err: err}
			}()

			eqAwaitLockWait(t, ctx)
			select {
			case got := <-done:
				t.Fatalf("the second enqueue returned (skipped=%v, %v) while the first transaction was still open; it never blocked on the key, so the fates below are not shared", got.skipped, got.err)
			default:
			}

			if tc.commit {
				if err := held.Commit(ctx); err != nil {
					t.Fatalf("commit the holding transaction: %v", err)
				}
			} else if err := held.Rollback(ctx); err != nil {
				t.Fatalf("roll back the holding transaction: %v", err)
			}

			var got outcome
			select {
			case got = <-done:
			case <-time.After(wkFastBudget):
				t.Fatalf("the second enqueue was still blocked %s after the first transaction resolved", wkFastBudget)
			}
			if got.err != nil {
				t.Fatalf("the second enqueue failed: %v", got.err)
			}
			if got.skipped != tc.wantSkipped {
				t.Errorf("the second enqueue reported skipped=%v, want %v", got.skipped, tc.wantSkipped)
			}

			if rows := eqJobRows(t, ctx, tenantID); len(rows) != 1 {
				t.Errorf("tenant %s owns %d river_job row(s), want exactly 1", tenantID, len(rows))
			}
			if n := eqKeyCount(t, ctx, tenantID); n != 1 {
				t.Errorf("tenant %s owns %d idempotency_keys row(s), want exactly 1: a key without its job is the fate this spec forbids", tenantID, n)
			}
		})
	}
}

// AC-5's oracle. The retirement note is the only record of why EXTR-01's absence ban is gone;
// without a test, a reword that drops the story, the reason or the replacement is invisible.
func TestTheRetirementNoteNamesItsStoryReasonAndReplacement(t *testing.T) {
	const file = "reachability_test.go"
	const guard = "TestExtractionExposesExactlyOneEnqueueSeam"

	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var note string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == guard && fd.Doc != nil {
			note = fd.Doc.Text()
		}
	}
	if strings.TrimSpace(note) == "" {
		t.Fatalf("%s carries no doc comment in %s; every containment check below would pass vacuously on the empty string", guard, file)
	}

	for _, want := range []struct{ token, why string }{
		{"EXTR-09", "the story that opened the seam"},
		{"EXTR-01", "the AC #6/#7 absence ban this retires"},
		{"critical-fork", "the gate at which the user sanctioned it"},
		{"EnqueueExtraction", "what replaced the absence"},
		{"enqueue.go", "where the replacement lives"},
	} {
		if !strings.Contains(note, want.token) {
			t.Errorf("the retirement note on %s does not name %q (%s)", guard, want.token, want.why)
		}
	}
}
