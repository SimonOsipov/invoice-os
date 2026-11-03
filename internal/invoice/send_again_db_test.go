package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// saRig is one firm tenant with a Submitter over an insert-only River client.
type saRig struct {
	super, app *pgxpool.Pool
	q          *queue.Client
	sub        *Submitter
	tenant     string
	entity     string
	ctx        context.Context
}

func newSARig(t *testing.T, label string) *saRig {
	t.Helper()
	super, app := dbTestPools(t)
	r := &saRig{super: super, app: app, q: newInsertOnlyQueueClient(t, app)}
	r.sub = NewSubmitter(NewStore(app), r.q)
	r.tenant = seedTenant(t, super, label+" tenant")
	r.entity = seedEntity(t, super, r.tenant, label+" entity")
	r.ctx = tenantCtx(r.tenant)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = super.Exec(c, `DELETE FROM app_exchange WHERE tenant_id = $1`, r.tenant)
		_, _ = super.Exec(c, `DELETE FROM idempotency_keys WHERE tenant_id = $1`, r.tenant)
		_, _ = super.Exec(c, `DELETE FROM river_job WHERE args->>'tenant_id' = $1`, r.tenant)
	})
	return r
}

// failedInvoice seeds a failed invoice; kind "" leaves failure_kind NULL.
func (r *saRig) failedInvoice(t *testing.T, number, kind string) string {
	t.Helper()
	id := seedInvoiceAtStatus(t, r.super, r.tenant, r.entity, number, StatusFailed)
	if kind != "" {
		if _, err := r.super.Exec(context.Background(), `UPDATE invoices SET failure_kind = $2 WHERE id = $1`, id, kind); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// saJob describes a seeded submission job. riverState "" seeds no River row;
// noRiverID keeps river_job_id NULL even when a River row exists.
type saJob struct {
	state      string
	riverState string
	pollRef    bool
	attempts   int
	key        string
	createdAt  time.Time
}

type saJobRef struct {
	id, key string
	riverID int64
	args    submission.SubmitArgs
}

func (r *saRig) job(t *testing.T, invoiceID string, j saJob) saJobRef {
	t.Helper()
	ctx := context.Background()
	ref := saJobRef{id: uuid.NewString(), key: j.key}
	if ref.key == "" {
		ref.key = "send-again-" + ref.id
	}
	if j.createdAt.IsZero() {
		j.createdAt = time.Now().UTC()
	}
	var pollRef *string
	var next *time.Time
	if j.pollRef {
		p, n := "ref-1", time.Now().UTC().Add(time.Hour)
		pollRef, next = &p, &n
	}
	if _, err := r.super.Exec(ctx,
		`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, attempts, poll_ref, next_poll_at, created_at)
		 VALUES ($1, $2, $3, $4, 'mock', 'v1', $5, $6, $7, $8, $9)`,
		ref.id, r.tenant, invoiceID, ref.key, j.state, j.attempts, pollRef, next, j.createdAt); err != nil {
		t.Fatalf("seed a %s job: %v", j.state, err)
	}
	ref.args = submission.SubmitArgs{TenantID: r.tenant, InvoiceID: invoiceID, IdempotencyKey: ref.key}
	if j.riverState != "" {
		ref.riverID = r.riverRow(t, ref.args, j.riverState)
		if _, err := r.super.Exec(ctx, `UPDATE submission_jobs SET river_job_id = $2 WHERE id = $1`, ref.id, ref.riverID); err != nil {
			t.Fatal(err)
		}
	}
	return ref
}

// riverRow inserts a submission_submit row through River, then forces its state.
func (r *saRig) riverRow(t *testing.T, args submission.SubmitArgs, state string) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := r.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := r.q.River().InsertTx(ctx, tx, args, nil)
	if err != nil || res.UniqueSkippedAsDuplicate {
		t.Fatalf("seed river row: err=%v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE river_job SET state = $2::river_job_state, attempt = 1,
		        attempted_at = CASE WHEN $2 IN ('running', 'completed', 'discarded') THEN now() END,
		        finalized_at = CASE WHEN $2 IN ('completed', 'discarded', 'cancelled') THEN now() END
		  WHERE id = $1`, res.Job.ID, state); err != nil {
		t.Fatalf("force river row to %s: %v", state, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return res.Job.ID
}

// saSnap is every row a send again may write.
type saSnap struct {
	Status, Kind     string
	History, Audit   int
	Jobs, Keys       int
	JobState, JobKey string
	Attempts         int
	RiverJobID       int64
	Rivers           string
}

func (r *saRig) snap(t *testing.T, invoiceID string) saSnap {
	t.Helper()
	ctx := context.Background()
	var s saSnap
	if err := r.super.QueryRow(ctx,
		`SELECT status, COALESCE(failure_kind, ''),
		        (SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1),
		        (SELECT count(*) FROM audit_log WHERE event = 'invoice.transitioned' AND payload->>'id' = $1::text),
		        (SELECT count(*) FROM submission_jobs WHERE invoice_id = $1),
		        (SELECT count(*) FROM idempotency_keys WHERE tenant_id = $2)
		   FROM invoices WHERE id = $1`, invoiceID, r.tenant,
	).Scan(&s.Status, &s.Kind, &s.History, &s.Audit, &s.Jobs, &s.Keys); err != nil {
		t.Fatalf("snapshot invoice: %v", err)
	}
	err := r.super.QueryRow(ctx,
		`SELECT state, idempotency_key, attempts, COALESCE(river_job_id, 0) FROM submission_jobs
		  WHERE invoice_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, invoiceID,
	).Scan(&s.JobState, &s.JobKey, &s.Attempts, &s.RiverJobID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("snapshot job: %v", err)
	}
	rows, err := r.super.Query(ctx, `SELECT id::text || ':' || state::text FROM river_job WHERE args->>'invoice_id' = $1 ORDER BY id`, invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		s.Rivers += a + ","
	}
	return s
}

type saRiverRow struct {
	State, Kind string
	Args        submission.SubmitArgs
}

func (r *saRig) readRiver(t *testing.T, id int64) (saRiverRow, bool) {
	t.Helper()
	var row saRiverRow
	var raw []byte
	err := r.super.QueryRow(context.Background(), `SELECT state::text, kind, args FROM river_job WHERE id = $1`, id).Scan(&row.State, &row.Kind, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &row.Args); err != nil {
		t.Fatal(err)
	}
	return row, true
}

func (r *saRig) fix() supportFix {
	return supportFix{r.super, r.app, NewStore(r.app), r.tenant, r.entity}
}

// refuses asserts SendAgain fails with want and writes nothing.
func (r *saRig) refuses(t *testing.T, invoiceID string, want error) {
	t.Helper()
	before := r.snap(t, invoiceID)
	inv, err := r.sub.SendAgain(r.ctx, invoiceID)
	if !errors.Is(err, want) {
		t.Fatalf("SendAgain = %v, want %v", err, want)
	}
	if inv.ID != "" {
		t.Errorf("a refused SendAgain returned the invoice %+v", inv)
	}
	if after := r.snap(t, invoiceID); after != before {
		t.Errorf("a refused SendAgain wrote:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSendAgain_NeverAcknowledgedGoesBackToQueued(t *testing.T) {
	r := newSARig(t, "SA never ack")
	inv := r.failedInvoice(t, "SA-NA", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})

	got, err := r.sub.SendAgain(r.ctx, inv)
	if err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	if got.ID != inv || got.Status != StatusQueued {
		t.Errorf("returned invoice id=%q status=%q, want %q and queued", got.ID, got.Status, inv)
	}
	if s := r.snap(t, inv); s.Status != "queued" || s.Kind != "" {
		t.Errorf("stored status=%q failure_kind=%q, want queued and NULL", s.Status, s.Kind)
	}
}

func TestSendAgain_PayloadNotBuiltWithAFailedJobGoesBackToQueued(t *testing.T) {
	r := newSARig(t, "SA pnb")
	inv := r.failedInvoice(t, "SA-PNB", "payload_not_built")
	r.job(t, inv, saJob{state: "failed", riverState: "cancelled"})

	got, err := r.sub.SendAgain(r.ctx, inv)
	if err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	if got.Status != StatusQueued {
		t.Errorf("returned status = %q, want queued", got.Status)
	}
	if s := r.snap(t, inv); s.Status != "queued" || s.Kind != "" {
		t.Errorf("stored status=%q failure_kind=%q, want queued and NULL", s.Status, s.Kind)
	}
}

func TestSendAgain_ReusesTheJobRowAndItsKey(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		job        saJob
	}{
		{"never_acknowledged", "never_acknowledged", saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"}},
		{"payload_not_built", "payload_not_built", saJob{state: "failed", riverState: "cancelled"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newSARig(t, "SA reuse "+c.name)
			inv := r.failedInvoice(t, "SA-RE", c.kind)
			job := r.job(t, inv, c.job)
			before := r.snap(t, inv)

			if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
				t.Fatalf("SendAgain: %v", err)
			}
			after := r.snap(t, inv)
			if after.Jobs != 1 || after.Jobs != before.Jobs || after.Keys != before.Keys {
				t.Errorf("submission_jobs %d->%d, idempotency_keys %d->%d, want both unchanged", before.Jobs, after.Jobs, before.Keys, after.Keys)
			}
			if after.JobKey != job.key || after.JobState != "queued" || after.Attempts != 0 {
				t.Errorf("job key=%q state=%q attempts=%d, want %q, queued, 0", after.JobKey, after.JobState, after.Attempts, job.key)
			}
			var id string
			if err := r.super.QueryRow(context.Background(), `SELECT id FROM submission_jobs WHERE invoice_id = $1`, inv).Scan(&id); err != nil || id != job.id {
				t.Errorf("job row id = %q (%v), want the original %q", id, err, job.id)
			}
			if after.RiverJobID == 0 || after.RiverJobID == job.riverID {
				t.Fatalf("river_job_id = %d, want a new id (was %d)", after.RiverJobID, job.riverID)
			}
			row, ok := r.readRiver(t, after.RiverJobID)
			if !ok || row.State != "available" || row.Kind != "submission_submit" || row.Args != job.args {
				t.Errorf("new river row = %+v (found %v), want an available submission_submit with args %+v", row, ok, job.args)
			}
			if _, ok := r.readRiver(t, job.riverID); ok {
				t.Errorf("the old river row %d is still there", job.riverID)
			}
		})
	}
}

func TestSendAgain_UsesTheNewestJobOfTheInvoice(t *testing.T) {
	r := newSARig(t, "SA newest")
	inv := r.failedInvoice(t, "SA-NEW", "never_acknowledged")
	older := r.job(t, inv, saJob{state: "failed", key: "k1-" + uuid.NewString(), createdAt: time.Now().UTC().Add(-time.Hour)})
	newer := r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded", key: "k2-" + uuid.NewString()})

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	var newID int64
	var state string
	if err := r.super.QueryRow(context.Background(), `SELECT state, COALESCE(river_job_id, 0) FROM submission_jobs WHERE id = $1`, newer.id).Scan(&state, &newID); err != nil {
		t.Fatal(err)
	}
	row, ok := r.readRiver(t, newID)
	if state != "queued" || !ok || row.Args.IdempotencyKey != newer.key {
		t.Errorf("newer job state=%q river=%+v (found %v), want queued with key %q", state, row, ok, newer.key)
	}
	var oldState string
	var oldRiver *int64
	var oldAttempts int
	if err := r.super.QueryRow(context.Background(), `SELECT state, river_job_id, attempts FROM submission_jobs WHERE id = $1`, older.id).Scan(&oldState, &oldRiver, &oldAttempts); err != nil {
		t.Fatal(err)
	}
	if oldState != "failed" || oldRiver != nil || oldAttempts != 0 {
		t.Errorf("older job = state %q river %v attempts %d, want untouched", oldState, oldRiver, oldAttempts)
	}
}

func TestSendAgain_RecordsTheCallerAsActor(t *testing.T) {
	r := newSARig(t, "SA actor")
	inv := r.failedInvoice(t, "SA-ACT", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	before := r.snap(t, inv)

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	after := r.snap(t, inv)
	if after.History != before.History+1 || after.Audit != before.Audit+1 {
		t.Fatalf("history %d->%d, audit %d->%d, want one new row each", before.History, after.History, before.Audit, after.Audit)
	}
	f := r.fix()
	if from, to, actor := f.lastHistory(t, inv); from != "failed" || to != "queued" || actor != memberSubject {
		t.Errorf("history = %s->%s by %q, want failed->queued by %q", from, to, actor, memberSubject)
	}
	if actor, number := f.lastAudit(t, inv); actor != memberSubject || number != "SA-ACT" {
		t.Errorf("audit actor=%q invoice_number=%q, want %q and SA-ACT", actor, number, memberSubject)
	}
}

func TestSendAgain_RefusesAcknowledgedNoVerdict(t *testing.T) {
	r := newSARig(t, "SA anv")
	inv := r.failedInvoice(t, "SA-ANV", "acknowledged_no_verdict")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, pollRef: true, riverState: "discarded"})
	r.refuses(t, inv, ErrNotResendable)
}

func TestSendAgain_RefusesANullKind(t *testing.T) {
	r := newSARig(t, "SA null kind")
	inv := r.failedInvoice(t, "SA-NULL", "")
	r.job(t, inv, saJob{state: "cancelled", riverState: "cancelled"})
	r.refuses(t, inv, ErrNotResendable)
}

func TestSendAgain_RefusesWithoutAResendableJob(t *testing.T) {
	cases := []struct {
		name string
		job  *saJob
	}{
		{"no job row", nil},
		{"poll_ref set", &saJob{state: "dead_lettered", attempts: 8, pollRef: true, riverState: "discarded"}},
		{"job queued", &saJob{state: "queued", riverState: "available"}},
		{"job submitting", &saJob{state: "submitting", riverState: "running"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSARig(t, "SA nojob "+c.name)
			inv := r.failedInvoice(t, "SA-NJ", "never_acknowledged")
			if c.job != nil {
				r.job(t, inv, *c.job)
			}
			r.refuses(t, inv, ErrNotResendable)
		})
	}
}

func TestSendAgain_RefusesAMismatchedKindAndJobState(t *testing.T) {
	cases := []struct{ name, kind, jobState string }{
		{"payload_not_built with a dead_lettered job", "payload_not_built", "dead_lettered"},
		{"never_acknowledged with a failed job", "never_acknowledged", "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSARig(t, "SA mismatch")
			inv := r.failedInvoice(t, "SA-MM", c.kind)
			r.job(t, inv, saJob{state: c.jobState, attempts: 1, riverState: "discarded"})
			r.refuses(t, inv, ErrNotResendable)
		})
	}
}

func TestSendAgain_JobWithNoRiverJobID(t *testing.T) {
	r := newSARig(t, "SA no river id")
	inv := r.failedInvoice(t, "SA-NR", "payload_not_built")
	job := r.job(t, inv, saJob{state: "failed", key: "demo-seed:" + uuid.NewString()})

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	s := r.snap(t, inv)
	if s.Status != "queued" || s.JobState != "queued" || s.RiverJobID == 0 {
		t.Fatalf("after = invoice %q, job %q, river_job_id %d; want queued, queued, a new id", s.Status, s.JobState, s.RiverJobID)
	}
	if row, ok := r.readRiver(t, s.RiverJobID); !ok || row.State != "available" || row.Args != job.args {
		t.Errorf("new river row = %+v (found %v), want an available row with args %+v", row, ok, job.args)
	}
}

// recordingAdapter accepts every submission and records the idempotency key it saw.
type recordingAdapter struct {
	mu   sync.Mutex
	keys []string
}

func (*recordingAdapter) Name() string    { return "mock" }
func (*recordingAdapter) Version() string { return "v1" }
func (*recordingAdapter) Transform(context.Context, submission.Canonical) (submission.Wire, error) {
	return submission.Wire(`{}`), nil
}
func (a *recordingAdapter) Submit(_ context.Context, _ submission.Wire, key string) (submission.Result, submission.Evidence) {
	a.mu.Lock()
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	status, latency := 200, 5
	return submission.Accepted{IRN: "IRN-" + uuid.NewString(), CSID: "CSID", QRPayload: "QR"},
		submission.Evidence{ReachedWire: true, HTTPStatus: &status, LatencyMS: &latency, ResponseHeaders: http.Header{}}
}
func (*recordingAdapter) Poll(context.Context, submission.Ref) (submission.Result, submission.Evidence) {
	return submission.Retryable{Err: errors.New("unused")}, submission.Evidence{}
}

// unbuildableAdapter fails Transform, so the worker fails the invoice as payload_not_built.
type unbuildableAdapter struct{ recordingAdapter }

func (*unbuildableAdapter) Transform(context.Context, submission.Canonical) (submission.Wire, error) {
	return nil, errors.New("mock could not build the payload")
}

func (r *saRig) work(t *testing.T, adapter submission.Adapter, args submission.SubmitArgs, riverID int64) error {
	t.Helper()
	w := &submission.SubmitWorker{
		Pool: r.app, Adapter: adapter, InvoicePort: NewStore(r.app),
		Limiter: submission.NewRateLimiter(), RateLimit: 1000, Logger: slog.Default(),
	}
	job := &river.Job[submission.SubmitArgs]{JobRow: &rivertype.JobRow{ID: riverID, Attempt: 1, MaxAttempts: 8}, Args: args}
	return w.Work(context.Background(), job)
}

func TestSendAgain_WorkerSubmitsWithTheOriginalKey(t *testing.T) {
	r := newSARig(t, "SA worker key")
	inv := r.failedInvoice(t, "SA-WK", "never_acknowledged")
	job := r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded", key: "orig-key-" + uuid.NewString()})

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	s := r.snap(t, inv)
	if s.RiverJobID == 0 {
		t.Fatal("SendAgain left no river_job_id")
	}
	adapter := &recordingAdapter{}
	if err := r.work(t, adapter, job.args, s.RiverJobID); err != nil {
		t.Fatalf("Work on the sent-again job: %v", err)
	}
	if len(adapter.keys) != 1 || adapter.keys[0] != job.key {
		t.Errorf("adapter saw keys %v, want exactly [%s]", adapter.keys, job.key)
	}
	var id, state string
	if err := r.super.QueryRow(context.Background(), `SELECT id, state FROM submission_jobs WHERE invoice_id = $1`, inv).Scan(&id, &state); err != nil {
		t.Fatalf("read the invoice's job (want exactly one row): %v", err)
	}
	if id != job.id || state != "accepted" {
		t.Errorf("job id=%q state=%q, want the original %q and accepted", id, state, job.id)
	}
	if after := r.snap(t, inv); after.Jobs != 1 || after.Status != "accepted" {
		t.Errorf("jobs=%d invoice=%q, want 1 and accepted", after.Jobs, after.Status)
	}
}

func TestSendAgain_RefusesAResolvedOutsideInvoice(t *testing.T) {
	r := newSARig(t, "SA resolved outside")
	inv := r.failedInvoice(t, "SA-KEPT", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	if _, err := r.super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'filed by hand' WHERE id = $1`, inv); err != nil {
		t.Fatal(err)
	}
	r.refuses(t, inv, ErrResolvedOutside)
}

func TestSendAgain_RefusesANoteThatFailsItsChecks(t *testing.T) {
	seed := func(t *testing.T, noteTotal string) (*saRig, string) {
		r := newSARig(t, "SA note")
		o := seedInvoice(t, r.super, r.tenant, r.entity, "SA-O")
		ncAccepted(t, r.super, o, "IRN-SA-O", "100.00")
		n := csNote(t, r.super, r.tenant, r.entity, "SA-N", "credit_note", o, "failed", csAmount(noteTotal))
		csNote(t, r.super, r.tenant, r.entity, "SA-S", "credit_note", o, "accepted", csAmount("60.00"))
		if _, err := r.super.Exec(context.Background(), `UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, n); err != nil {
			t.Fatal(err)
		}
		r.job(t, n, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
		return r, n
	}
	t.Run("over the original's remaining amount", func(t *testing.T) {
		r, n := seed(t, "60.00")
		r.refuses(t, n, ErrNoteChecksFailed)
	})
	t.Run("a note that fits is sent again", func(t *testing.T) {
		r, n := seed(t, "40.00")
		if _, err := r.sub.SendAgain(r.ctx, n); err != nil {
			t.Fatalf("SendAgain of a note that fits: %v", err)
		}
		if s := r.snap(t, n); s.Status != "queued" {
			t.Errorf("status = %q, want queued", s.Status)
		}
	})
}

func TestSendAgain_RefusesEveryNonFailedStatus(t *testing.T) {
	r := newSARig(t, "SA statuses")
	seen := 0
	for _, st := range allStatuses {
		if st == StatusFailed {
			continue
		}
		seen++
		t.Run(string(st), func(t *testing.T) {
			inv := seedInvoiceAtStatus(t, r.super, r.tenant, r.entity, "SA-ST-"+string(st), st)
			r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
			before := r.snap(t, inv)
			_, err := r.sub.SendAgain(r.ctx, inv)
			var se StatusError
			if !errors.As(err, &se) || se.Have != st {
				t.Fatalf("SendAgain = %v, want StatusError{Have: %s}", err, st)
			}
			if after := r.snap(t, inv); after != before {
				t.Errorf("a refused SendAgain wrote:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
	if seen != 6 {
		t.Fatalf("covered %d statuses, want 6", seen)
	}
}

func TestSendAgain_CrossTenantIdIsNotFound(t *testing.T) {
	a := newSARig(t, "SA tenant A")
	b := newSARig(t, "SA tenant B")
	inv := b.failedInvoice(t, "SA-XT", "never_acknowledged")
	b.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	before := b.snap(t, inv)

	_, err := a.sub.SendAgain(a.ctx, inv)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SendAgain of tenant B's invoice as tenant A = %v, want ErrNotFound", err)
	}
	if after := b.snap(t, inv); after != before {
		t.Errorf("tenant B's rows changed:\nbefore %+v\nafter  %+v", before, after)
	}
	// Positive pair: the owner can send it again.
	if _, err := b.sub.SendAgain(b.ctx, inv); err != nil {
		t.Errorf("SendAgain as the owning tenant = %v, want nil", err)
	}
}

func TestSendAgain_UnknownAndMalformedIds(t *testing.T) {
	r := newSARig(t, "SA ids")
	if _, err := r.sub.SendAgain(r.ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown uuid = %v, want ErrNotFound", err)
	}
	if _, err := r.sub.SendAgain(r.ctx, "not-a-uuid"); !errors.Is(err, ErrValidation) {
		t.Errorf("malformed id = %v, want ErrValidation", err)
	}
}

func TestSendAgain_OutOfScopeClientIsNotFound(t *testing.T) {
	f := newCSFixture(t, "SA client scope")
	q := newInsertOnlyQueueClient(t, f.app)
	sub := NewSubmitter(f.store, q)
	r := &saRig{super: f.super, app: f.app, q: q, sub: sub, tenant: f.tenant, entity: f.entB}
	t.Cleanup(func() {
		_, _ = f.super.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'tenant_id' = $1`, f.tenant)
	})
	// invB belongs to client B; the limited reviewer holds client A only.
	inv := seedInvoiceAtStatus(t, f.super, f.tenant, f.entB, "SA-CS-B", StatusFailed)
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, inv); err != nil {
		t.Fatal(err)
	}
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	before := r.snap(t, inv)

	if _, err := sub.SendAgain(f.limited, inv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SendAgain outside the client set = %v, want ErrNotFound", err)
	}
	if after := r.snap(t, inv); after != before {
		t.Errorf("an out-of-scope SendAgain wrote:\nbefore %+v\nafter  %+v", before, after)
	}
	// Positive pair: an admin sees every client.
	if _, err := sub.SendAgain(f.admin, inv); err != nil {
		t.Errorf("SendAgain as an all-clients admin = %v, want nil", err)
	}
}

func TestSendAgain_LockedWorkspaceIsRefused(t *testing.T) {
	r := newSARig(t, "SA locked")
	inv := r.failedInvoice(t, "SA-LOCK", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	if _, err := r.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = gen_random_uuid() WHERE id = $1`, r.tenant); err != nil {
		t.Fatalf("schedule the deletion: %v", err)
	}
	r.refuses(t, inv, db.ErrWorkspaceLocked)
}

func TestSendAgain_ARunningRiverJobIsBusy(t *testing.T) {
	r := newSARig(t, "SA busy")
	inv := r.failedInvoice(t, "SA-BUSY", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "running"})
	r.refuses(t, inv, ErrResendBusy)
}

func TestSendAgain_FailsAgainThenSendsAgain(t *testing.T) {
	r := newSARig(t, "SA fails again")
	inv := r.failedInvoice(t, "SA-FA", "payload_not_built")
	job := r.job(t, inv, saJob{state: "failed", riverState: "cancelled"})

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("first SendAgain: %v", err)
	}
	// The worker fails the transform again; its error is a River cancel, not a test failure.
	_ = r.work(t, &unbuildableAdapter{}, job.args, r.snap(t, inv).RiverJobID)
	if s := r.snap(t, inv); s.Status != "failed" || s.Kind != "payload_not_built" || s.JobState != "failed" {
		t.Fatalf("after the failing run: invoice %q kind %q job %q; want failed, payload_not_built, failed", s.Status, s.Kind, s.JobState)
	}

	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("second SendAgain: %v", err)
	}
	if s := r.snap(t, inv); s.Status != "queued" || s.Kind != "" || s.JobState != "queued" || s.Jobs != 1 {
		t.Errorf("after the second SendAgain: invoice %q kind %q job %q jobs %d; want queued, NULL, queued, 1", s.Status, s.Kind, s.JobState, s.Jobs)
	}
}
