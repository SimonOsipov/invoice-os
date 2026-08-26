// Adversarial siblings of audit_number_test.go (AUDIT-11-03): the failure modes the
// acceptance set does not reach -- a skipped OncePerJob closure, real statement counting
// at the wire, a number that moves between the two hops, and a hostile number.
package submission_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// --- the skipped closure -----------------------------------------------------------------

// TestAuditNumberAdversarial_SkippedOncePerJobClosureMakesNoLookup pins the lookup INSIDE
// queue.OncePerJob's closure. Hoisting it one line up -- still inside case Accepted, still
// in the same tx, still terminal-only -- keeps every acceptance case green: the value, the
// key set, the rollback and the per-hop count are all unchanged. What changes is a hop whose
// marker already exists: OncePerJob skips fn entirely, so a lookup inside it must never run,
// and a lookup outside it turns a silent no-op into a query -- and, with a failing port, into
// a returned error that sends River back around a job it must simply ack.
//
// The marker is "job:<id>" per TENANT and shared across both workers (see workPollExhaustion),
// so submitting and polling under the SAME River job id in one tenant is enough to reach the
// skip branch with the job row still 'pending'.
func TestAuditNumberAdversarial_SkippedOncePerJobClosureMakesNoLookup(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	// The collision is the fixture. Assert it really happened before reading anything else:
	// if the closure DID run, the state below moves off "pending" and the case is vacuous.
	arrange := func(t *testing.T, port submission.InvoicePort) (tenantID, idemKey string, workErr error) {
		t.Helper()
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		t.Cleanup(cleanup)

		const sharedJobID = 77
		adapter, jobID, idemKey := anSubmitToPendingAt(t, f, tenantID, invoiceID, sharedJobID)
		adapter.pollQueue = []scriptedOutcome{anAccepted("SKIPPED")}

		pw := &submission.PollWorker{Pool: f.app, Adapter: adapter, InvoicePort: port, Queue: newQueueClient(f.app)}
		workErr = pw.Work(ctx, newPollJob(sharedJobID, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		}))
		return tenantID, idemKey, workErr
	}

	t.Run("no_lookup_and_no_row", func(t *testing.T) {
		port := &anCountingInvoicePort{}
		tenantID, idemKey, err := arrange(t, port)
		if err != nil {
			t.Fatalf("poll hop whose OncePerJob marker already exists: %v, want nil", err)
		}
		if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "pending" {
			t.Fatalf("submission_jobs.state = %q, want %q -- the closure was NOT skipped, so this case "+
				"is not exercising the skip branch at all", wj.state, "pending")
		}
		if _, number := port.counts(); number != 0 {
			t.Errorf("InvoicePort.Number call count on a hop whose closure was skipped = %d, want 0 -- "+
				"the lookup has been hoisted out of the queue.OncePerJob closure", number)
		}
		if n := auditCount(t, f, tenantID, "submission.accepted"); n != 0 {
			t.Errorf("submission.accepted audit rows = %d, want 0 -- a skipped closure writes nothing", n)
		}
	})

	t.Run("a_failing_port_cannot_fail_the_hop", func(t *testing.T) {
		_, _, err := arrange(t, anFailingNumberPort{})
		if err != nil {
			t.Errorf("poll hop whose closure was skipped returned %v, want nil -- the number lookup ran "+
				"outside queue.OncePerJob, so a lookup failure now fails a hop that has nothing to do", err)
		}
	})
}

// --- real statement counting -------------------------------------------------------------

// anTracer records every SQL statement pgx sends on a pooled connection.
type anTracer struct {
	mu  sync.Mutex
	sql []string
}

func (tr *anTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.sql = append(tr.sql, data.SQL)
	tr.mu.Unlock()
	return ctx
}

func (tr *anTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// reads returns every SELECT this hop sent against invoices. A terminal hop already sent one
// before this story (markTerminal's `SELECT status ... FOR UPDATE`), so the count Core AC 6
// caps is numberReads below; reads is what the failure messages print.
func (tr *anTracer) reads() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, s := range tr.sql {
		flat := strings.Join(strings.Fields(s), " ")
		if strings.HasPrefix(strings.ToUpper(flat), "SELECT") && strings.Contains(flat, "FROM invoices") {
			out = append(out, flat)
		}
	}
	return out
}

// numberReads is the subset that reads the number -- the statement this story adds.
func (tr *anTracer) numberReads() []string {
	var out []string
	for _, s := range tr.reads() {
		if strings.Contains(s, "invoice_number") {
			out = append(out, s)
		}
	}
	return out
}

func (tr *anTracer) reset() {
	tr.mu.Lock()
	tr.sql = nil
	tr.mu.Unlock()
}

// anTracedPool is a second invoice_app pool with a query tracer attached, so the counts below
// come off the wire rather than off a test double's own counter -- a double that forgot to
// increment, or a second lookup issued by anything other than the port, is invisible to
// anCountingInvoicePort and visible here.
func anTracedPool(t *testing.T) (*pgxpool.Pool, *anTracer) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL is unset, yet the fixture built -- refusing to count statements against nothing")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	tr := &anTracer{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open traced pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, tr
}

// TestAuditNumberAdversarial_PollHopIssuesAtMostOneInvoiceRead counts the invoices reads a
// single poll hop actually sends, rather than trusting the fake port's counter (AC-3, Core
// AC 6). Terminal hops pay exactly one; a superseded hop and a still-pending hop pay none.
func TestAuditNumberAdversarial_PollHopIssuesAtMostOneInvoiceRead(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	pool, tr := anTracedPool(t)

	build := func(adapter submission.Adapter) *submission.PollWorker {
		return &submission.PollWorker{Pool: pool, Adapter: adapter, InvoicePort: testInvoicePort{}, Queue: newQueueClient(pool)}
	}

	t.Run("superseded_hop_reads_invoices_zero_times", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()
		jobID := seedTerminalJob(t, f, tenantID, invoiceID, anIdemKey(invoiceID))

		tr.reset()
		if err := build(newScriptedAdapter()).Work(ctx, newPollJob(30, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 5,
		})); err != nil {
			t.Fatalf("superseded poll hop: %v", err)
		}
		if got := tr.reads(); len(got) != 0 {
			t.Errorf("superseded hop sent %d SELECT(s) against invoices, want 0 -- it returns before tx2: %v", len(got), got)
		}
	})

	t.Run("still_pending_hop_reads_invoices_zero_times", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()
		adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
		adapter.pollQueue = []scriptedOutcome{anPending()}

		tr.reset()
		if err := build(adapter).Work(ctx, newPollJob(31, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		})); err != nil {
			t.Fatalf("still-pending poll hop: %v", err)
		}
		if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "pending" {
			t.Fatalf("submission_jobs.state = %q, want %q -- this hop was not the non-terminal one", wj.state, "pending")
		}
		if got := tr.reads(); len(got) != 0 {
			t.Errorf("still-pending hop sent %d SELECT(s) against invoices, want 0 -- a poll chain is "+
				"unbounded, so one read here is one read per hop forever: %v", len(got), got)
		}
	})

	t.Run("terminal_hop_reads_invoices_exactly_once", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()
		adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
		adapter.pollQueue = []scriptedOutcome{anAccepted("TRACED")}

		tr.reset()
		if err := build(adapter).Work(ctx, newPollJob(32, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		})); err != nil {
			t.Fatalf("terminal poll hop: %v", err)
		}
		if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "accepted" {
			t.Fatalf("submission_jobs.state = %q, want %q -- this hop was not the terminal one", wj.state, "accepted")
		}
		got := tr.numberReads()
		if len(got) != 1 {
			t.Fatalf("terminal hop sent %d invoice_number read(s), want exactly 1 (Core AC 6); every invoices "+
				"read this hop sent: %v", len(got), tr.reads())
		}
		if strings.Contains(got[0], "*") || strings.Count(got[0], ",") != 0 {
			t.Errorf("the invoice_number read was %q, want a single-column SELECT -- the plan Core AC 6 was "+
				"signed off against is an index-only equality on one column", got[0])
		}
	})
}

// --- which number gets frozen --------------------------------------------------------------

// TestAuditNumberAdversarial_EachWorkerFreezesItsOwnReadOfTheNumber pins WHEN each worker
// reads the number, because audit_log is immutable and the two workers read at different
// moments by design: SubmitWorker uses the Canonical hydrated in tx1 (before the adapter
// call), PollWorker reads inside tx2 (after the verdict). Neither is wrong, but which one
// ships must be a decision, not an accident -- and a "fix" that made SubmitWorker re-read at
// audit time, or that stuffed the number into PollArgs (D-11), would flip one of these.
func TestAuditNumberAdversarial_EachWorkerFreezesItsOwnReadOfTheNumber(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	rename := func(t *testing.T, tenantID, invoiceID, to string) {
		t.Helper()
		if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE invoices SET invoice_number = $1 WHERE id = $2`, to, invoiceID)
			return e
		}); err != nil {
			t.Fatalf("rename invoice_number: %v", err)
		}
	}

	t.Run("submit_freezes_the_tx1_number", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()
		before := anMustInvoiceNumber(t, f, tenantID, invoiceID)
		const after = "RENAMED-BY-SUBMIT"

		// onSubmit fires between tx1 (where Canonical was hydrated) and tx2 (where the audit
		// row is written) -- the only window in which the two reads could disagree.
		adapter := newScriptedAdapter(anAccepted("FREEZE-SUBMIT"))
		adapter.onSubmit = func() { rename(t, tenantID, invoiceID, after) }
		w := &submission.SubmitWorker{
			Pool: f.app, Adapter: adapter, InvoicePort: testInvoicePort{},
			Limiter: submission.NewRateLimiter(), RateLimit: 60, Queue: newQueueClient(f.app),
		}
		if err := w.Work(ctx, newSubmitJob(40, 1, 8, submission.SubmitArgs{
			TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: anIdemKey(invoiceID),
		})); err != nil {
			t.Fatalf("submit to Accepted: %v", err)
		}
		if now := anMustInvoiceNumber(t, f, tenantID, invoiceID); now != after {
			t.Fatalf("invoices.invoice_number = %q, want %q -- the rename hook never fired, so this case proves nothing", now, after)
		}
		row := anReadAuditRow(t, f, tenantID, "submission.accepted")
		got, present := anNumber(row.payload)
		if !present {
			t.Fatalf("submission.accepted payload has no %q key; payload = %s", auditNumberKey, row.raw)
		}
		if got != before {
			t.Errorf("submission.accepted payload %q = %q, want %q -- SubmitWorker must reuse the Canonical "+
				"already fetched in tx1 (AC-2, zero extra statements), not re-read at audit time", auditNumberKey, got, before)
		}
	})

	t.Run("poll_freezes_the_verdict_time_number", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()
		before := anMustInvoiceNumber(t, f, tenantID, invoiceID)
		const after = "RENAMED-BEFORE-POLL"

		adapter, jobID, _ := anSubmitToPending(t, f, tenantID, invoiceID)
		rename(t, tenantID, invoiceID, after)
		adapter.pollQueue = []scriptedOutcome{anAccepted("FREEZE-POLL")}
		pw := newTestPollWorker(f.app, adapter)
		if err := pw.Work(ctx, newPollJob(41, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		})); err != nil {
			t.Fatalf("poll to Accepted: %v", err)
		}
		row := anReadAuditRow(t, f, tenantID, "submission.accepted")
		got, present := anNumber(row.payload)
		if !present {
			t.Fatalf("submission.accepted payload has no %q key; payload = %s", auditNumberKey, row.raw)
		}
		if got == before {
			t.Fatalf("submission.accepted payload %q = %q, the number as of the SUBMIT hop -- PollWorker is "+
				"carrying a stale value, which is what putting the number in PollArgs would produce (D-11)", auditNumberKey, got)
		}
		if got != after {
			t.Errorf("submission.accepted payload %q = %q, want %q read at verdict time", auditNumberKey, got, after)
		}
	})
}

// --- a hostile number ----------------------------------------------------------------------

// anHostileNumber carries JSON structural characters, a quote, a backslash and SQL wildcards.
// It is written through a bound parameter, read back through a bound parameter, and lands in
// jsonb -- any layer that concatenates instead of binding corrupts it here.
const anHostileNumber = `INV-"'\{}[]:,/<>&%_ 0001`

// TestAuditNumberAdversarial_AHostileNumberSurvivesBothWorkers drives the submit half and the
// poll half with a number full of JSON and SQL metacharacters. audit_log rows cannot be
// rewritten, so a value mangled on the way in is mangled forever.
func TestAuditNumberAdversarial_AHostileNumberSurvivesBothWorkers(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	for _, b := range []anBranch{anSelect("submit")[1], anSelect("poll")[2]} {
		b := b
		t.Run(b.label, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()
			if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE invoices SET invoice_number = $1 WHERE id = $2`, anHostileNumber, invoiceID)
				return e
			}); err != nil {
				t.Fatalf("seed the hostile invoice_number: %v", err)
			}
			if now := anMustInvoiceNumber(t, f, tenantID, invoiceID); now != anHostileNumber {
				t.Fatalf("invoices.invoice_number = %q, want %q -- the fixture itself lost the value", now, anHostileNumber)
			}

			b.drive(t, f, tenantID, invoiceID)
			row := anReadAuditRow(t, f, tenantID, b.event)
			anRequireOneRow(t, row, b)

			got, present := anNumber(row.payload)
			if !present {
				t.Fatalf("%s (%s): payload has no %q key; payload = %s", b.event, b.site, auditNumberKey, row.raw)
			}
			if got != anHostileNumber {
				t.Errorf("%s (%s): payload %q = %q, want %q byte for byte", b.event, b.site, auditNumberKey, got, anHostileNumber)
			}
			// The sibling keys and both payload-derived columns must be untouched: a number
			// that broke out of its own value would land as extra keys or a NULL invoice_id.
			if v, ok := row.payload["invoice_id"]; !ok || v != invoiceID {
				t.Errorf("%s (%s): payload invoice_id = %v, want %q -- the hostile number escaped its own value",
					b.event, b.site, v, invoiceID)
			}
			if row.invoiceID == nil || *row.invoiceID != invoiceID {
				t.Errorf("%s (%s): audit_log.invoice_id = %v, want %q", b.event, b.site, row.invoiceID, invoiceID)
			}
			want := append(append([]string{}, b.baseKeys...), auditNumberKey)
			if len(anKeys(row.payload)) != len(want) {
				t.Errorf("%s (%s): payload keys = %v, want %d keys: %s", b.event, b.site, anKeys(row.payload), len(want), row.raw)
			}
			// The stored jsonb must re-decode to the same string, not merely compare equal
			// after whatever escaping the driver applied on the way out.
			var reparsed map[string]any
			if err := json.Unmarshal([]byte(row.raw), &reparsed); err != nil {
				t.Fatalf("%s: stored payload is not valid JSON: %v (%s)", b.event, err, row.raw)
			}
			if reparsed[auditNumberKey] != anHostileNumber {
				t.Errorf("%s: re-decoded %q = %v, want %q", b.event, auditNumberKey, reparsed[auditNumberKey], anHostileNumber)
			}
		})
	}
}

// anSubmitToPendingAt is anSubmitToPending with the River job id as a parameter, so a caller
// can deliberately collide the submit and poll markers ("job:<id>" is per tenant, shared by
// both workers).
func anSubmitToPendingAt(t *testing.T, f *effectsFixture, tenantID, invoiceID string, riverJobID int64) (*scriptedAdapter, string, string) {
	t.Helper()
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(anPending())
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(context.Background(), newSubmitJob(riverJobID, 1, 8, submission.SubmitArgs{
		TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey,
	})); err != nil {
		t.Fatalf("submit to Pending: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "pending" {
		t.Fatalf("submission_jobs.state after a Pending submit = %q, want %q", wj.state, "pending")
	}
	return adapter, wj.id, idemKey
}
