// audit_number_test.go: AUDIT-11-03 Mode A. The acceptance tests for "every submission
// audit writer carries invoice_number", authored RED before any branch emits the key.
//
// Package submission_test, reusing submit_worker_test.go / worker_poll_test.go's fixture,
// seeds, doubles and read-back helpers rather than declaring a second set.
//
// Run: export the four DSNs and `go test ./internal/submission/ -p 1`. A bare run with only
// DEV_DB_PORT set skips every case here and still prints ok (CF-6).
package submission_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// auditNumberKey is the ONE spelling, settled by AUDIT-11-01 and verbatim from
// invoices.invoice_number.
const auditNumberKey = "invoice_number"

// anBranchCount floors anBranches. SEVEN branches call recordVerdictAudit /
// recordFailureAudit -- four in SubmitWorker.Work, three in PollWorker.Work. The story's
// prose says six and is wrong; a six-row table would drop a branch and still satisfy every
// assertion inside every loop below.
const anBranchCount = 7

const (
	anSubmitBranches = 4
	anPollBranches   = 3
)

// anBranch is one audit-writing branch: how to drive it, and the payload key set it wrote
// before this story.
type anBranch struct {
	label string
	// worker is "submit" or "poll" -- the poll half is the one with no in-scope invoice
	// fact, so a SubmitWorker-only fix passes the submit half and silently misses it.
	worker string
	// site names the branch by function and case, never by line number (CF-11).
	site     string
	event    string
	baseKeys []string
	// drive runs the real worker(s) and returns the submission_jobs.id its audit row names.
	drive func(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string
}

func anBranches() []anBranch {
	return []anBranch{
		{
			label: "submit_transform_failed", worker: "submit",
			site:     "SubmitWorker.Work, Transform-failed branch",
			event:    "submission.failed",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome", "failure_kind"},
			drive: func(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
				return workTransformFailure(t, f, tenantID, invoiceID).id
			},
		},
		{
			label: "submit_accepted", worker: "submit",
			site:     "SubmitWorker.Work, case Accepted",
			event:    "submission.accepted",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome", "reference"},
			drive:    anDriveSubmitAccepted,
		},
		{
			label: "submit_rejected", worker: "submit",
			site:     "SubmitWorker.Work, case Rejected",
			event:    "submission.rejected",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome"},
			drive:    anDriveSubmitRejected,
		},
		{
			label: "submit_dead_letter", worker: "submit",
			site:     "SubmitWorker.Work, case Retryable final-attempt dead-letter",
			event:    "submission.failed",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome", "failure_kind"},
			drive: func(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
				return workRetryExhaustion(t, f, tenantID, invoiceID).id
			},
		},
		{
			label: "poll_accepted", worker: "poll",
			site:     "PollWorker.Work, case Accepted",
			event:    "submission.accepted",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome", "reference"},
			drive:    anDrivePollAccepted,
		},
		{
			label: "poll_rejected", worker: "poll",
			site:     "PollWorker.Work, case Rejected",
			event:    "submission.rejected",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome"},
			drive:    anDrivePollRejected,
		},
		{
			label: "poll_dead_letter", worker: "poll",
			site:     "PollWorker.Work, case Retryable final-attempt dead-letter",
			event:    "submission.failed",
			baseKeys: []string{"invoice_id", "submission_job_id", "outcome", "failure_kind"},
			drive: func(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
				return workPollExhaustion(t, f, tenantID, invoiceID, 1, 20).id
			},
		},
	}
}

// anRequireTable fails when the table lost a row, and when the requested subset is short --
// a filtered loop over fewer branches passes vacuously for the ones it dropped.
func anRequireTable(t *testing.T, all []anBranch, subset []anBranch, wantSubset int) {
	t.Helper()
	if len(all) != anBranchCount {
		t.Fatalf("anBranches holds %d rows, want %d -- a short table passes every assertion below vacuously", len(all), anBranchCount)
	}
	if len(subset) != wantSubset {
		t.Fatalf("selected %d branches, want %d -- the filter dropped a branch, so the loop below proves less than it claims", len(subset), wantSubset)
	}
}

func anSelect(worker string) []anBranch {
	var out []anBranch
	for _, b := range anBranches() {
		if b.worker == worker {
			out = append(out, b)
		}
	}
	return out
}

// --- drives ---------------------------------------------------------------------------

func anIdemKey(invoiceID string) string { return "req-" + uuid.NewString() + ":" + invoiceID }

func anAccepted(tag string) scriptedOutcome {
	return scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-AN-" + tag, CSID: "CSID-AN", QRPayload: "QR-AN"},
		evidence: submission.Evidence{ReachedWire: true},
	}
}

func anRejected() scriptedOutcome {
	return scriptedOutcome{
		result:   submission.Rejected{Reasons: []submission.Reason{{Code: "L01", Message: "bad TIN"}}},
		evidence: submission.Evidence{ReachedWire: true},
	}
}

func anPending() scriptedOutcome {
	return scriptedOutcome{
		result:   submission.Pending{Ref: "an-r1", PollAfter: time.Now().Add(time.Hour)},
		evidence: submission.Evidence{ReachedWire: true},
	}
}

func anDriveSubmitAccepted(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	idemKey := anIdemKey(invoiceID)
	w := newTestWorker(f.app, newScriptedAdapter(anAccepted("SUBMIT")))
	if err := w.Work(context.Background(), newSubmitJob(1, 1, 8, submission.SubmitArgs{
		TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey,
	})); err != nil {
		t.Fatalf("submit to Accepted: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Fatalf("submission_jobs.state = %q, want %q -- the branch under test did not run", wj.state, "accepted")
	}
	return wj.id
}

func anDriveSubmitRejected(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	idemKey := anIdemKey(invoiceID)
	w := newTestWorker(f.app, newScriptedAdapter(anRejected()))
	if err := w.Work(context.Background(), newSubmitJob(1, 1, 8, submission.SubmitArgs{
		TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey,
	})); err != nil {
		t.Fatalf("submit to Rejected: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "rejected" {
		t.Fatalf("submission_jobs.state = %q, want %q -- the branch under test did not run", wj.state, "rejected")
	}
	return wj.id
}

// anSubmitToPending drives the submit hop to Pending so a poll hop has a job to resume, and
// hands back the adapter so the caller can script the poll outcome.
func anSubmitToPending(t *testing.T, f *effectsFixture, tenantID, invoiceID string) (*scriptedAdapter, string, string) {
	t.Helper()
	idemKey := anIdemKey(invoiceID)
	adapter := newScriptedAdapter(anPending())
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(context.Background(), newSubmitJob(1, 1, 8, submission.SubmitArgs{
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

func anDrivePollAccepted(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
	adapter.pollQueue = []scriptedOutcome{anAccepted("POLL")}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(context.Background(), newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Accepted: %v", err)
	}
	if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "accepted" {
		t.Fatalf("submission_jobs.state = %q, want %q -- the branch under test did not run", wj.state, "accepted")
	}
	return jobID
}

func anDrivePollRejected(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
	adapter.pollQueue = []scriptedOutcome{anRejected()}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(context.Background(), newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Rejected: %v", err)
	}
	if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "rejected" {
		t.Fatalf("submission_jobs.state = %q, want %q -- the branch under test did not run", wj.state, "rejected")
	}
	return jobID
}

// --- read-back ---------------------------------------------------------------------------

// anAuditRow is one audit_log row plus both payload-derived columns.
type anAuditRow struct {
	rows      int
	raw       string
	payload   map[string]any
	invoiceID *string // GENERATED ALWAYS from payload->>'invoice_id'
	entityID  *string // filled by the audit_log_entity_on_insert trigger
}

func anReadAuditRow(t *testing.T, f *effectsFixture, tenantID, event string) anAuditRow {
	t.Helper()
	ctx := context.Background()
	var r anAuditRow
	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&r.rows); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT payload::text, invoice_id::text, entity_id::text
			   FROM audit_log WHERE event = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, event,
		).Scan(&r.raw, &r.invoiceID, &r.entityID)
	}); err != nil {
		t.Fatalf("read audit_log row (tenant=%s event=%s): %v", tenantID, event, err)
	}
	if err := json.Unmarshal([]byte(r.raw), &r.payload); err != nil {
		t.Fatalf("unmarshal audit_log payload for %q: %v", event, err)
	}
	return r
}

// anRequireOneRow fails when the drive wrote no row: every assertion after that would read
// some other fixture's row, or none.
func anRequireOneRow(t *testing.T, r anAuditRow, b anBranch) {
	t.Helper()
	if r.rows != 1 {
		t.Fatalf("%s (%s): the tenant holds %d %s audit rows, want exactly 1 -- the fixture did not drive the branch",
			b.event, b.site, r.rows, b.event)
	}
}

// anNumber reports payload[invoice_number] and whether the key is present at all, so an
// ABSENT key and a present blank read differently.
func anNumber(payload map[string]any) (string, bool) {
	v, ok := payload[auditNumberKey]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

func anKeys(payload map[string]any) []string {
	out := make([]string, 0, len(payload))
	for k := range payload {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// anMustInvoiceNumber reads invoices.invoice_number back out of the table, so no assertion
// below compares the payload against a literal the test itself wrote.
func anMustInvoiceNumber(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	ctx := context.Background()
	var n string
	if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id = $1`, invoiceID).Scan(&n)
	}); err != nil {
		t.Fatalf("read invoices.invoice_number for %s: %v", invoiceID, err)
	}
	return n
}

func anMustInvoiceEntityID(t *testing.T, f *effectsFixture, tenantID, invoiceID string) string {
	t.Helper()
	ctx := context.Background()
	var e string
	if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT entity_id::text FROM invoices WHERE id = $1`, invoiceID).Scan(&e)
	}); err != nil {
		t.Fatalf("read invoices.entity_id for %s: %v", invoiceID, err)
	}
	return e
}

// anCarriesTheNumber is the shared body of the two AC-1 tests: drive the branch, read the
// row back, and require the payload value to equal the invoice's own stored number.
func anCarriesTheNumber(t *testing.T, worker string, wantBranches int) {
	f := requireExchangeDB(t)
	all := anBranches()
	sel := anSelect(worker)
	anRequireTable(t, all, sel, wantBranches)

	for _, b := range sel {
		t.Run(b.label, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()

			want := anMustInvoiceNumber(t, f, tenantID, invoiceID)
			if want == "" {
				t.Fatalf("fixture invoice %s carries a blank invoice_number; the comparison below would be vacuous", invoiceID)
			}

			jobID := b.drive(t, f, tenantID, invoiceID)
			row := anReadAuditRow(t, f, tenantID, b.event)
			anRequireOneRow(t, row, b)

			if got, ok := row.payload["submission_job_id"]; !ok || got != jobID {
				t.Fatalf("%s (%s): payload submission_job_id = %v, want %q -- this is not the row the drive wrote",
					b.event, b.site, got, jobID)
			}
			got, present := anNumber(row.payload)
			if !present {
				t.Fatalf("%s (%s): payload has no %q key; payload = %s, want %q",
					b.event, b.site, auditNumberKey, row.raw, want)
			}
			if got != want {
				t.Errorf("%s (%s): payload %q = %q, want %q (invoices.invoice_number read back)",
					b.event, b.site, auditNumberKey, got, want)
			}
		})
	}
}

// AC-1: SubmitWorker's four audit branches each carry the invoice's own number, taken from
// the Canonical tx1 already fetched.
func TestAuditNumber_SubmitWorkerVerdictsCarryTheNumber(t *testing.T) {
	anCarriesTheNumber(t, "submit", anSubmitBranches)
}

// AC-1: PollWorker's three terminal branches carry it too. This worker holds NO invoice fact
// at all today -- it is the half a SubmitWorker-only fix silently misses, and the half where
// a wrong implementation writes a blank into an immutable row.
func TestAuditNumber_PollWorkerVerdictsCarryTheNumber(t *testing.T) {
	anCarriesTheNumber(t, "poll", anPollBranches)
}

// AC-5: audit_log rows are immutable, so a blank frozen now is permanent. Every branch's
// payload must be non-blank wherever invoices.invoice_number is.
func TestAuditNumber_SubmissionNumberIsNeverBlank(t *testing.T) {
	f := requireExchangeDB(t)
	all := anBranches()
	anRequireTable(t, all, all, anBranchCount)

	for _, b := range all {
		t.Run(b.label, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()

			column := anMustInvoiceNumber(t, f, tenantID, invoiceID)
			if column == "" {
				t.Fatalf("fixture invoices.invoice_number is blank; this case asserts the non-blank half")
			}

			b.drive(t, f, tenantID, invoiceID)
			row := anReadAuditRow(t, f, tenantID, b.event)
			anRequireOneRow(t, row, b)

			got, present := anNumber(row.payload)
			if !present {
				t.Fatalf("%s (%s): payload has no %q key; payload = %s", b.event, b.site, auditNumberKey, row.raw)
			}
			if got == "" {
				t.Errorf("%s (%s): payload %q is the empty string while invoices.invoice_number is %q -- "+
					"a blank default is indistinguishable from no number and cannot be repaired afterwards; payload = %s",
					b.event, b.site, auditNumberKey, column, row.raw)
			}
		})
	}
}

// AC-1: the payload is WIDENED, never rewritten. Set equality in BOTH directions across all
// seven branches -- a writer that replaced invoice_id with the number would NULL
// audit_log.invoice_id and audit_log.entity_id on every future row and still read fine.
func TestAuditNumber_SubmissionPayloadKeysAreOnlyWidened(t *testing.T) {
	f := requireExchangeDB(t)
	all := anBranches()
	anRequireTable(t, all, all, anBranchCount)

	for _, b := range all {
		t.Run(b.label, func(t *testing.T) {
			if len(b.baseKeys) == 0 {
				t.Fatalf("%s (%s): baseKeys is empty; set equality against an empty want proves nothing", b.event, b.site)
			}
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()

			b.drive(t, f, tenantID, invoiceID)
			row := anReadAuditRow(t, f, tenantID, b.event)
			anRequireOneRow(t, row, b)

			want := append(append([]string{}, b.baseKeys...), auditNumberKey)
			sort.Strings(want)
			got := anKeys(row.payload)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s (%s): payload keys = [%s], want [%s] (every pre-change key, plus exactly %q); payload = %s",
					b.event, b.site, strings.Join(got, ","), strings.Join(want, ","), auditNumberKey, row.raw)
			}
			if v, ok := row.payload["invoice_id"]; !ok || v != invoiceID {
				t.Errorf("%s (%s): payload invoice_id = %v, want %q unchanged -- both payload-derived columns address this key and nothing else",
					b.event, b.site, v, invoiceID)
			}
		})
	}
}

// AC-1 (CF-4): BOTH payload-derived columns still fill once the sibling key is there.
// audit_log.invoice_id is a STORED generated column and audit_log.entity_id is written by
// the audit_log_entity_on_insert trigger; neither iterates the key set, so an invoice_id-only
// assertion would not see entity_id regress.
func TestAuditNumber_ScopedColumnsStillFillForSubmissionEvents(t *testing.T) {
	f := requireExchangeDB(t)
	all := anBranches()
	anRequireTable(t, all, all, anBranchCount)

	for _, b := range all {
		t.Run(b.label, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()

			b.drive(t, f, tenantID, invoiceID)
			row := anReadAuditRow(t, f, tenantID, b.event)
			anRequireOneRow(t, row, b)

			// Half the claim is "with the new key present" -- without this the case would
			// stay green against a payload this story never touched.
			if _, present := anNumber(row.payload); !present {
				t.Fatalf("%s (%s): payload has no %q key, so this case is not yet asserting what its name says; payload = %s",
					b.event, b.site, auditNumberKey, row.raw)
			}

			if row.invoiceID == nil {
				t.Errorf("%s (%s): audit_log.invoice_id is NULL with %q present; payload = %s", b.event, b.site, auditNumberKey, row.raw)
			} else if *row.invoiceID != invoiceID {
				t.Errorf("%s (%s): audit_log.invoice_id = %q, want %q", b.event, b.site, *row.invoiceID, invoiceID)
			}

			wantEntity := anMustInvoiceEntityID(t, f, tenantID, invoiceID)
			if row.entityID == nil {
				t.Errorf("%s (%s): audit_log.entity_id is NULL with %q present, which reads as a firm-wide claim; payload = %s",
					b.event, b.site, auditNumberKey, row.raw)
			} else if *row.entityID != wantEntity {
				t.Errorf("%s (%s): audit_log.entity_id = %q, want the invoice's own entity %q", b.event, b.site, *row.entityID, wantEntity)
			}
		})
	}
}

// --- InvoicePort doubles for the cost / rollback claims ---------------------------------

// anCountingInvoicePort counts Canonical and Number calls. Number is declared here BEFORE
// InvoicePort grows it: an extra method on a test double compiles today and becomes the
// override once the interface widens.
type anCountingInvoicePort struct {
	testInvoicePort
	mu            sync.Mutex
	canonicalHits int
	numberHits    int
}

func (p *anCountingInvoicePort) Canonical(ctx context.Context, tx pgx.Tx, invoiceID string) (submission.Canonical, error) {
	p.mu.Lock()
	p.canonicalHits++
	p.mu.Unlock()
	return p.testInvoicePort.Canonical(ctx, tx, invoiceID)
}

func (p *anCountingInvoicePort) Number(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
	p.mu.Lock()
	p.numberHits++
	p.mu.Unlock()
	var n string
	err := tx.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id = $1`, invoiceID).Scan(&n)
	return n, err
}

func (p *anCountingInvoicePort) counts() (canonical, number int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canonicalHits, p.numberHits
}

var errANNumberLookup = errors.New("submission_test: invoice number lookup failed")

// anFailingNumberPort makes the PollWorker lookup fail. The FK from submission_jobs to
// invoices is ON DELETE RESTRICT, so a live poll chain can never outlive its invoice and
// this error is unreachable in production -- it has to be forced.
type anFailingNumberPort struct{ testInvoicePort }

func (anFailingNumberPort) Number(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
	return "", errANNumberLookup
}

// AC-2: the number reaches SubmitWorker's audit branches off the Canonical tx1 already
// fetched -- exactly one Canonical call, and no port lookup at all.
func TestAuditNumber_SubmitWorkerAddsNoInvoiceQuery(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	want := anMustInvoiceNumber(t, f, tenantID, invoiceID)
	port := &anCountingInvoicePort{}
	idemKey := anIdemKey(invoiceID)
	w := &submission.SubmitWorker{
		Pool:        f.app,
		Adapter:     newScriptedAdapter(anAccepted("NOQUERY")),
		InvoicePort: port,
		Limiter:     submission.NewRateLimiter(),
		RateLimit:   60,
		Queue:       newQueueClient(f.app),
	}
	if err := w.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{
		TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey,
	})); err != nil {
		t.Fatalf("submit to Accepted: %v", err)
	}

	row := anReadAuditRow(t, f, tenantID, "submission.accepted")
	got, present := anNumber(row.payload)
	if !present {
		t.Fatalf("submission.accepted payload has no %q key; payload = %s, want %q", auditNumberKey, row.raw, want)
	}
	if got != want {
		t.Errorf("submission.accepted payload %q = %q, want %q", auditNumberKey, got, want)
	}

	canonical, number := port.counts()
	if canonical != 1 {
		t.Errorf("InvoicePort.Canonical call count = %d, want exactly 1 -- SubmitWorker hydrates the invoice once, in tx1", canonical)
	}
	if number != 0 {
		t.Errorf("InvoicePort.Number call count = %d, want 0 -- SubmitWorker already holds the number on its Canonical; "+
			"a uniform fix that calls the port from both workers buys a statement it does not need", number)
	}
}

// AC-3: the poll-path lookup runs on terminal branches only. A lookup hoisted to the top of
// Work would cost one statement on every hop of an unbounded poll chain.
func TestAuditNumber_PollWorkerLooksUpOnlyOnTerminalBranches(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	build := func(adapter submission.Adapter, port submission.InvoicePort) *submission.PollWorker {
		return &submission.PollWorker{Pool: f.app, Adapter: adapter, InvoicePort: port, Queue: newQueueClient(f.app)}
	}

	t.Run("superseded_hop_makes_no_lookup", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		idemKey := anIdemKey(invoiceID)
		jobID := seedTerminalJob(t, f, tenantID, invoiceID, idemKey) // state='accepted'
		port := &anCountingInvoicePort{}
		adapter := newScriptedAdapter() // Poll must never fire
		if err := build(adapter, port).Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 5,
		})); err != nil {
			t.Fatalf("superseded poll hop: %v", err)
		}
		if _, number := port.counts(); number != 0 {
			t.Errorf("InvoicePort.Number call count on a superseded hop = %d, want 0 -- the short-circuit returns before tx2", number)
		}
	})

	t.Run("still_pending_hop_makes_no_lookup", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
		adapter.pollQueue = []scriptedOutcome{anPending()}
		port := &anCountingInvoicePort{}
		if err := build(adapter, port).Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		})); err != nil {
			t.Fatalf("still-pending poll hop: %v", err)
		}
		if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "pending" {
			t.Fatalf("submission_jobs.state = %q, want %q -- this hop was not the non-terminal one", wj.state, "pending")
		}
		if _, number := port.counts(); number != 0 {
			t.Errorf("InvoicePort.Number call count on a still-pending hop = %d, want 0 -- a poll chain is unbounded, "+
				"so a lookup here is one statement per hop forever", number)
		}
	})

	t.Run("terminal_hop_makes_exactly_one_lookup", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
		adapter.pollQueue = []scriptedOutcome{anAccepted("TERMINAL")}
		port := &anCountingInvoicePort{}
		if err := build(adapter, port).Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
		})); err != nil {
			t.Fatalf("terminal poll hop: %v", err)
		}
		if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "accepted" {
			t.Fatalf("submission_jobs.state = %q, want %q -- this hop was not the terminal one", wj.state, "accepted")
		}
		if _, number := port.counts(); number != 1 {
			t.Errorf("InvoicePort.Number call count on a terminal hop = %d, want exactly 1 -- PollWorker holds no invoice "+
				"fact of its own, so the number can only come through the port", number)
		}
	})
}

// AC-5 (D-10): a lookup failure is an error that rolls the closure back, like every other
// write in it. Substituting "" would freeze a blank into a row nothing can ever rewrite.
func TestAuditNumber_PollLookupFailureRollsBackTheClosure(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	adapter, jobID, idemKey := anSubmitToPending(t, f, tenantID, invoiceID)
	exchangesBefore := exCountRows(t, f, tenantID, jobID)

	adapter.pollQueue = []scriptedOutcome{anAccepted("ROLLBACK")}
	pw := &submission.PollWorker{
		Pool:        f.app,
		Adapter:     adapter,
		InvoicePort: anFailingNumberPort{},
		Queue:       newQueueClient(f.app),
	}
	err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
	}))
	if err == nil {
		t.Fatal("PollWorker.Work returned nil while the invoice-number lookup fails -- the terminal branch " +
			"either never asks the port for the number, or swallowed the error and wrote a blank")
	}
	if !errors.Is(err, errANNumberLookup) {
		t.Errorf("PollWorker.Work error = %v, want one wrapping errANNumberLookup -- the lookup error must travel with %%w", err)
	}

	if wj := wjRequire(t, f, tenantID, idemKey); wj.state != "pending" {
		t.Errorf("submission_jobs.state = %q, want unchanged %q -- the whole closure, including the OncePerJob marker, must have rolled back", wj.state, "pending")
	}
	if inv := wiRead(t, f, tenantID, invoiceID); inv.status != "submitted" {
		t.Errorf("invoice status = %q, want unchanged %q", inv.status, "submitted")
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 0 {
		t.Errorf("submission.accepted audit rows = %d, want 0 -- no row may survive a failed lookup", n)
	}
	if n := exCountRows(t, f, tenantID, jobID); n != exchangesBefore {
		t.Errorf("app_exchange rows for job %s = %d, want unchanged %d", jobID, n, exchangesBefore)
	}
}
