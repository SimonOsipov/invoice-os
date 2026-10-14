package staffjobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func TestRLS_StaffJobsListReadsEveryTenant(t *testing.T) {
	super, app := dbPools(t)
	ctx := staffCtx(houseMember(t, super))
	tenantA, _ := customerTenant(t, super)
	tenantB, _ := customerTenant(t, super)
	now := time.Now().UTC().Truncate(time.Second)
	queuedA := seedJob(t, super, tenantA, seedInvoice(t, super, tenantA, "SJ-A1", "queued"), "queued", now, nil)
	deadA := seedJob(t, super, tenantA, seedInvoice(t, super, tenantA, "SJ-A2", "failed"), "dead_lettered", now.Add(-time.Minute), nil)
	queuedB := seedJob(t, super, tenantB, seedInvoice(t, super, tenantB, "SJ-B1", "queued"), "queued", now.Add(-2*time.Minute), nil)
	store := NewStore(app, nil, nil)

	ids := func(jobs []Job) map[string]bool {
		out := map[string]bool{}
		for _, j := range jobs {
			out[j.ID] = true
		}
		return out
	}

	all, err := store.List(ctx, nil, nil)
	if err != nil {
		t.Fatalf("List(nil, nil): %v", err)
	}
	got := ids(all)
	for _, want := range []string{queuedA, deadA, queuedB} {
		if !got[want] {
			t.Errorf("List(nil, nil) misses job %s of a seeded tenant", want)
		}
	}

	state := "queued"
	tenantAID := uuid.MustParse(tenantA)
	filtered, err := store.List(ctx, &state, &tenantAID)
	if err != nil {
		t.Fatalf("List(queued, tenantA): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != queuedA {
		t.Fatalf("List(queued, tenantA) = %+v, want only job %s", filtered, queuedA)
	}
	j := filtered[0]
	if j.TenantID != tenantA || j.InvoiceNumber != "SJ-A1" || j.InvoiceStatus != "queued" || j.State != "queued" || j.Attempts != 3 || j.LastError != nil || j.Acknowledged {
		t.Errorf("job fields = %+v", j)
	}
}

func TestRLS_StaffJobsGetReadsTheHistory(t *testing.T) {
	super, app := dbPools(t)
	ctx := staffCtx(houseMember(t, super))
	tenant, _ := customerTenant(t, super)
	invoice := seedInvoice(t, super, tenant, "SJ-H1", "failed")
	lastErr := "mock APP timed out in flight"
	job := seedJob(t, super, tenant, invoice, "dead_lettered", time.Now().UTC(), &lastErr)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	str := func(s string) *string { return &s }
	// Inserted out of order: the read must sort by changed_at.
	seedHistory(t, super, tenant, invoice, str("validated"), "queued", base.Add(2*time.Minute))
	seedHistory(t, super, tenant, invoice, nil, "draft", base)
	seedHistory(t, super, tenant, invoice, str("queued"), "failed", base.Add(3*time.Minute))
	seedHistory(t, super, tenant, invoice, str("draft"), "validated", base.Add(time.Minute))

	d, err := NewStore(app, nil, nil).Get(ctx, job)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if d.Job.ID != job || d.Job.State != "dead_lettered" || d.Job.InvoiceNumber != "SJ-H1" || d.Job.TenantID != tenant ||
		d.Job.LastError == nil || *d.Job.LastError != lastErr {
		t.Errorf("job = %+v", d.Job)
	}
	var tos []string
	for _, s := range d.History {
		tos = append(tos, s.ToStatus)
	}
	want := []string{"draft", "validated", "queued", "failed"}
	if len(tos) != len(want) {
		t.Fatalf("history = %v, want %v", tos, want)
	}
	for i := range want {
		if tos[i] != want[i] {
			t.Fatalf("history = %v, want %v", tos, want)
		}
	}
	if d.History[0].FromStatus != nil || d.History[0].Actor != "system" {
		t.Errorf("first step = %+v, want a nil from_status and actor system", d.History[0])
	}
}

func TestRLS_StaffJobsGetMalformedIdIsNotFound(t *testing.T) {
	super, app := dbPools(t)
	ctx := staffCtx(houseMember(t, super))
	store := NewStore(app, nil, nil)
	if _, err := store.Get(ctx, "x"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get(x) = %v, want ErrJobNotFound", err)
	}
	if _, err := store.Get(ctx, uuid.NewString()); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get(unknown uuid) = %v, want ErrJobNotFound", err)
	}
}

func TestRLS_StaffJobsNonHouseActorIsNotStaff(t *testing.T) {
	super, app := dbPools(t)
	tenant, member := customerTenant(t, super)
	job := seedJob(t, super, tenant, seedInvoice(t, super, tenant, "SJ-N1", "queued"), "queued", time.Now().UTC(), nil)
	ctx := staffCtx(member)
	store := NewStore(app, nil, nil)
	if jobs, err := store.List(ctx, nil, nil); !errors.Is(err, db.ErrNotStaff) || len(jobs) != 0 {
		t.Errorf("List = %v, %v; want no rows and db.ErrNotStaff", jobs, err)
	}
	if d, err := store.Get(ctx, job); !errors.Is(err, db.ErrNotStaff) || d.Job.ID != "" {
		t.Errorf("Get = %+v, %v; want no job and db.ErrNotStaff", d, err)
	}
	if _, err := store.List(context.Background(), nil, nil); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("List without a staff context = %v, want db.ErrNotStaff", err)
	}
}

// redriveRig is a dead-lettered job over a failed invoice in a fresh customer tenant, driven by a house member.
type redriveRig struct {
	super, app *pgxpool.Pool
	rc         *river.Client[pgx.Tx]
	store      *Store
	ctx        context.Context
	tenant     string
	invoice    string
	job        string
	args       submission.SubmitArgs
}

// newRedriveRig seeds the job at state with the River row riverState (empty for no River row).
func newRedriveRig(t *testing.T, state, riverState string) *redriveRig {
	t.Helper()
	super, app := dbPools(t)
	r := &redriveRig{super: super, app: app, rc: insertOnlyRiver(t, super)}
	r.store = NewStore(app, invoice.NewStore(app), insertOnlyRiver(t, app))
	r.ctx = staffCtx(houseMember(t, super))
	r.tenant, _ = customerTenant(t, super)
	r.invoice = seedInvoice(t, super, r.tenant, "RD-"+r.tenant[:8], "failed")
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, r.invoice); err != nil {
		t.Fatal(err)
	}
	// The key is only known after the insert, and the River row's args carry it: seed the job first, then the row.
	r.job, r.args.IdempotencyKey = seedRedriveJob(t, super, r.tenant, r.invoice, state, nil, nil)
	r.args.TenantID, r.args.InvoiceID = r.tenant, r.invoice
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = super.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id = $1`, r.tenant)
		_, _ = super.Exec(ctx, `DELETE FROM app_exchange WHERE tenant_id = $1`, r.tenant)
		_, _ = super.Exec(ctx, `DELETE FROM river_job WHERE args->>'tenant_id' = $1`, r.tenant)
	})
	if riverState != "" {
		id := seedRiverRow(t, super, r.rc, r.args, riverState)
		r.setJob(t, `river_job_id = $2`, id)
	}
	return r
}

func (r *redriveRig) setJob(t *testing.T, set string, args ...any) {
	t.Helper()
	if _, err := r.super.Exec(context.Background(), `UPDATE submission_jobs SET `+set+` WHERE id = $1`, append([]any{r.job}, args...)...); err != nil {
		t.Fatalf("update job: %v", err)
	}
}

// riverRows lists "id:state" of every river_job row of the tenant, oldest first.
func riverRows(t *testing.T, super *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id::text || ':' || state::text FROM river_job WHERE args->>'tenant_id' = $1 ORDER BY id`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// redriveState is every row a Redrive may write.
type redriveState struct {
	JobState, InvoiceStatus   string
	Attempts                  int
	RiverJobID                int64
	FailureKind               string
	Rivers                    string
	AuditRows, StaffAuditRows int
	StatusHistoryRows         int
}

func (r *redriveRig) state(t *testing.T) redriveState {
	t.Helper()
	var s redriveState
	ctx := context.Background()
	q := func(sql string, dest ...any) {
		t.Helper()
		if err := r.super.QueryRow(ctx, sql, r.tenant).Scan(dest...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := r.super.QueryRow(ctx, `SELECT state, attempts, COALESCE(river_job_id, 0) FROM submission_jobs WHERE id = $1`, r.job).Scan(&s.JobState, &s.Attempts, &s.RiverJobID); err != nil {
		t.Fatal(err)
	}
	if err := r.super.QueryRow(ctx, `SELECT status, COALESCE(failure_kind, '') FROM invoices WHERE id = $1`, r.invoice).Scan(&s.InvoiceStatus, &s.FailureKind); err != nil {
		t.Fatal(err)
	}
	q(`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, &s.AuditRows)
	q(`SELECT count(*) FROM staff_audit_log WHERE tenant_id = $1`, &s.StaffAuditRows)
	q(`SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, &s.StatusHistoryRows)
	for _, row := range riverRows(t, r.super, r.tenant) {
		s.Rivers += row + ","
	}
	return s
}

// refuses asserts Redrive fails with want and writes nothing.
func (r *redriveRig) refuses(t *testing.T, want error) {
	t.Helper()
	before := r.state(t)
	d, err := r.store.Redrive(r.ctx, r.job, "APP back up")
	if !errors.Is(err, want) {
		t.Fatalf("Redrive = %v, want %v", err, want)
	}
	if d.Job.ID != "" {
		t.Errorf("a refused Redrive returned the job %+v", d.Job)
	}
	if after := r.state(t); after != before {
		t.Errorf("a refused Redrive wrote:\nbefore %+v\nafter  %+v", before, after)
	}
}

type riverRow struct {
	ID    int64
	State string
	Kind  string
	Args  submission.SubmitArgs
}

func (r *redriveRig) riverRow(t *testing.T, id int64) (riverRow, bool) {
	t.Helper()
	var row riverRow
	var raw []byte
	err := r.super.QueryRow(context.Background(), `SELECT id, state::text, kind, args FROM river_job WHERE id = $1`, id).Scan(&row.ID, &row.State, &row.Kind, &raw)
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

func TestRLS_RedriveRequeuesADeadLetteredJob(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	old := r.state(t).RiverJobID

	d, err := r.store.Redrive(r.ctx, r.job, "APP back up")
	if err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	got := r.state(t)
	if got.JobState != "queued" || got.Attempts != 0 || got.InvoiceStatus != "queued" || got.FailureKind != "" {
		t.Fatalf("after Redrive = %+v, want job queued, attempts 0, invoice queued, no failure_kind", got)
	}
	if got.RiverJobID == 0 || got.RiverJobID == old {
		t.Fatalf("river_job_id = %v, want a new id (was %d)", got.RiverJobID, old)
	}
	row, ok := r.riverRow(t, got.RiverJobID)
	if !ok || row.State != "available" || row.Kind != "submission_submit" || row.Args != r.args {
		t.Errorf("river row = %+v (found %v), want an available submission_submit with args %+v", row, ok, r.args)
	}
	if d.Job.ID != r.job || d.Job.State != "queued" || d.Job.InvoiceStatus != "queued" || d.Job.Attempts != 0 {
		t.Errorf("returned detail = %+v, want the job after commit", d.Job)
	}
	if len(d.History) == 0 || d.History[len(d.History)-1].ToStatus != "queued" {
		t.Errorf("returned history = %+v, want its last step to queued", d.History)
	}
}

// acceptAdapter accepts every submission.
type acceptAdapter struct{}

func (acceptAdapter) Name() string    { return "mock" }
func (acceptAdapter) Version() string { return "v1" }
func (acceptAdapter) Transform(context.Context, submission.Canonical) (submission.Wire, error) {
	return submission.Wire(`{}`), nil
}
func (acceptAdapter) Submit(context.Context, submission.Wire, string) (submission.Result, submission.Evidence) {
	status, latency := 200, 5
	return submission.Accepted{IRN: "IRN-REDRIVE", CSID: "CSID", QRPayload: "QR"},
		submission.Evidence{ReachedWire: true, HTTPStatus: &status, LatencyMS: &latency, ResponseHeaders: http.Header{}}
}
func (acceptAdapter) Poll(context.Context, submission.Ref) (submission.Result, submission.Evidence) {
	return submission.Retryable{Err: errors.New("unused")}, submission.Evidence{}
}

func TestRLS_RedrivenJobRunsTheNormalWorkerPath(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	newID := r.state(t).RiverJobID
	if newID == 0 {
		t.Fatal("Redrive left no river_job_id")
	}
	sw := &submission.SubmitWorker{
		Pool: r.app, Adapter: acceptAdapter{}, InvoicePort: invoice.NewStore(r.app),
		Limiter: submission.NewRateLimiter(), RateLimit: 1000, Logger: slog.Default(),
	}
	job := &river.Job[submission.SubmitArgs]{JobRow: &rivertype.JobRow{ID: newID, Attempt: 1, MaxAttempts: 8}, Args: r.args}
	if err := sw.Work(context.Background(), job); err != nil {
		t.Fatalf("Work on the re-driven job: %v", err)
	}
	got := r.state(t)
	if got.JobState != "accepted" || got.InvoiceStatus != "accepted" {
		t.Errorf("after Work = job %s, invoice %s; want accepted, accepted", got.JobState, got.InvoiceStatus)
	}
	var n int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'submission.accepted'`, r.tenant).Scan(&n); err != nil || n != 1 {
		t.Errorf("submission.accepted audit rows = %d (%v), want 1", n, err)
	}
}

func TestRLS_RedriveInsertsPastADiscardedRiverRow(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	old := r.state(t).RiverJobID
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); err != nil {
		t.Fatalf("Redrive past a discarded River row: %v", err)
	}
	got := r.state(t)
	if got.RiverJobID == 0 || got.RiverJobID == old {
		t.Fatalf("river_job_id = %v, want a new id (was %d)", got.RiverJobID, old)
	}
	if row, ok := r.riverRow(t, got.RiverJobID); !ok || row.State != "available" {
		t.Errorf("new river row = %+v (found %v), want available", row, ok)
	}
}

func TestRLS_RedriveDeletesACompletedRiverRowThenInserts(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "completed")
	old := r.state(t).RiverJobID
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); err != nil {
		t.Fatalf("Redrive past a completed River row: %v", err)
	}
	if _, ok := r.riverRow(t, old); ok {
		t.Errorf("the completed river row %d is still there", old)
	}
	got := r.state(t)
	if got.RiverJobID == 0 || got.RiverJobID == old {
		t.Fatalf("river_job_id = %v, want a new id (was %d)", got.RiverJobID, old)
	}
	if row, ok := r.riverRow(t, got.RiverJobID); !ok || row.State != "available" {
		t.Errorf("new river row = %+v (found %v), want available", row, ok)
	}
}

func TestRLS_RedriveOfARunningRiverRowIs409(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "running")
	r.refuses(t, ErrRiverRunning)
	if got := r.state(t); got.JobState != "dead_lettered" || got.InvoiceStatus != "failed" || got.Rivers == "" {
		t.Errorf("state = %+v, want the dead-lettered job, failed invoice and the running row kept", got)
	}
}

func TestRLS_RedriveRefusesWhenALiveRiverJobHoldsTheArgs(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	seedRiverRow(t, r.super, r.rc, r.args, "available")
	r.refuses(t, ErrRiverDuplicate)
}

func jobStatesOfTheCheck(t *testing.T, super *pgxpool.Pool) []string {
	t.Helper()
	var def string
	if err := super.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'submission_jobs_state_check'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(def, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestRLS_RedriveRefusesEveryOtherState(t *testing.T) {
	super, _ := dbPools(t)
	states := jobStatesOfTheCheck(t, super)
	seen := map[string]bool{}
	for _, s := range states {
		seen[s] = true
	}
	if len(states) < 2 || !seen["dead_lettered"] || !seen["queued"] {
		t.Fatalf("states of the CHECK = %v, want the set with dead_lettered and queued", states)
	}
	for _, state := range states {
		if state == "dead_lettered" {
			continue
		}
		t.Run(state, func(t *testing.T) {
			r := newRedriveRig(t, state, "discarded")
			r.refuses(t, ErrJobState)
		})
	}
}

func TestRLS_RedriveRefusesAnAcknowledgedJob(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	r.setJob(t, `poll_ref = 'ref-1', next_poll_at = now() + interval '1 hour'`)
	if _, err := r.super.Exec(context.Background(), `UPDATE invoices SET failure_kind = 'acknowledged_no_verdict' WHERE id = $1`, r.invoice); err != nil {
		t.Fatal(err)
	}
	r.refuses(t, ErrAcknowledged)
}

func TestRLS_RedriveRefusesAnInvoiceThatIsNotFailed(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	if _, err := r.super.Exec(context.Background(), `UPDATE invoices SET status = 'accepted', failure_kind = NULL WHERE id = $1`, r.invoice); err != nil {
		t.Fatal(err)
	}
	before := r.state(t)
	_, err := r.store.Redrive(r.ctx, r.job, "APP back up")
	var se invoice.StatusError
	if !errors.As(err, &se) || se.Have != invoice.StatusAccepted {
		t.Fatalf("Redrive = %v, want invoice.StatusError{Have: accepted}", err)
	}
	if after := r.state(t); after != before {
		t.Errorf("a refused Redrive wrote:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestRLS_RedriveRefusesAResolvedOutsideInvoice(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	if _, err := r.super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'filed by hand' WHERE id = $1`, r.invoice); err != nil {
		t.Fatal(err)
	}
	r.refuses(t, invoice.ErrResolvedOutside)
}

func (r *redriveRig) auditPayloads(t *testing.T, table, tenant string) []map[string]any {
	t.Helper()
	rows, err := r.super.Query(context.Background(),
		`SELECT payload FROM `+table+` WHERE tenant_id = $1 AND event = 'submission.job.redriven'`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRLS_RedriveWritesBothAuditRowsInTheSameTx(t *testing.T) {
	super, _ := dbPools(t)
	staff := uuid.NewString()
	seedMembership(t, super, adoptHouseTenant(t, super), staff)
	r := newRedriveRig(t, "dead_lettered", "discarded")
	r.ctx = staffCtx(staff)
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	want := map[string]any{"job_id": r.job, "invoice_id": r.invoice, "invoice_number": "RD-" + r.tenant[:8], "reason": "APP back up"}
	for _, table := range []string{"staff_audit_log", "audit_log"} {
		rows := r.auditPayloads(t, table, r.tenant)
		if len(rows) != 1 {
			t.Fatalf("%s has %d submission.job.redriven rows, want 1", table, len(rows))
		}
		if len(rows[0]) != len(want) {
			t.Errorf("%s payload = %v, want exactly the keys job_id, invoice_id, invoice_number, reason", table, rows[0])
		}
		for k, v := range want {
			if rows[0][k] != v {
				t.Errorf("%s payload[%s] = %v, want %v", table, k, rows[0][k], v)
			}
		}
	}
	var actor uuid.UUID
	var subject *uuid.UUID
	if err := super.QueryRow(context.Background(),
		`SELECT actor, subject_id FROM staff_audit_log WHERE tenant_id = $1 AND event = 'submission.job.redriven'`, r.tenant).Scan(&actor, &subject); err != nil {
		t.Fatal(err)
	}
	if actor.String() != staff || subject == nil || subject.String() != r.job {
		t.Errorf("staff_audit_log actor/subject = %v/%v, want %s/%s", actor, subject, staff, r.job)
	}
	var customerActor string
	if err := super.QueryRow(context.Background(),
		`SELECT actor FROM audit_log WHERE tenant_id = $1 AND event = 'submission.job.redriven'`, r.tenant).Scan(&customerActor); err != nil || customerActor != audit.SupportActor {
		t.Errorf("audit_log actor = %q (%v), want %q", customerActor, err, audit.SupportActor)
	}
	var leaks int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND payload::text LIKE '%' || $2 || '%'`, r.tenant, staff).Scan(&leaks); err != nil || leaks != 0 {
		t.Errorf("audit_log rows naming the staff uuid = %d (%v), want 0", leaks, err)
	}
}

func TestRLS_RedriveRollsBackWhenTheAuditWriteFails(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	boom := errors.New("audit down")
	calls := 0
	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	recordStaff = func(context.Context, pgx.Tx, audit.StaffEvent) error { calls++; return boom }
	before := r.state(t)
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); !errors.Is(err, boom) {
		t.Fatalf("Redrive = %v, want the audit error", err)
	}
	if calls != 1 {
		t.Errorf("recordStaff calls = %d, want 1: the audit write must run inside the change", calls)
	}
	if after := r.state(t); after != before {
		t.Errorf("a failed audit write left changes:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestRLS_RedriveOfAnotherTenantsJobWritesThatTenant(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	house := adoptHouseTenant(t, r.super)
	var houseBefore int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'submission.job.redriven'`, house).Scan(&houseBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Redrive(r.ctx, r.job, "APP back up"); err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	if got := len(r.auditPayloads(t, "audit_log", r.tenant)); got != 1 {
		t.Errorf("the job's tenant has %d submission.job.redriven rows, want 1", got)
	}
	var houseAfter int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'submission.job.redriven'`, house).Scan(&houseAfter); err != nil || houseAfter != houseBefore {
		t.Errorf("the house tenant's rows %d -> %d (%v), want unchanged", houseBefore, houseAfter, err)
	}
	newID := r.state(t).RiverJobID
	if newID == 0 {
		t.Fatal("no river_job_id after Redrive")
	}
	if row, ok := r.riverRow(t, newID); !ok || row.Args.TenantID != r.tenant {
		t.Errorf("new river row = %+v (found %v), want args tenant_id %s", row, ok, r.tenant)
	}
}

func TestRLS_RedriveNonHouseActorIsNotStaff(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	_, member := customerTenant(t, r.super)
	r.ctx = staffCtx(member)
	r.refuses(t, db.ErrNotStaff)
	r.ctx = context.Background()
	r.refuses(t, db.ErrNotStaff)
}

func TestRLS_ConcurrentRedrivesMakeOneRiverJob(t *testing.T) {
	r := newRedriveRig(t, "dead_lettered", "discarded")
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = r.store.Redrive(r.ctx, r.job, "APP back up")
		}()
	}
	close(start)
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrJobState):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("results = %v, want one success and one ErrJobState", errs)
	}
	got := r.state(t)
	var live int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE args->>'tenant_id' = $1 AND state = 'available'`, r.tenant).Scan(&live); err != nil || live != 1 {
		t.Errorf("available river rows = %d (%v) in %v, want 1", live, err, got.Rivers)
	}
	if got.AuditRows < 1 || len(r.auditPayloads(t, "audit_log", r.tenant)) != 1 || len(r.auditPayloads(t, "staff_audit_log", r.tenant)) != 1 {
		t.Errorf("audit rows: audit_log %d, staff_audit_log %d, want 1 and 1 submission.job.redriven",
			len(r.auditPayloads(t, "audit_log", r.tenant)), len(r.auditPayloads(t, "staff_audit_log", r.tenant)))
	}
}
