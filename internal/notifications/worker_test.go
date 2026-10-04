package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"
)

// qaHoldFor bounds every channel wait, so a broken worker fails the test instead of hanging it.
const qaHoldFor = 10 * time.Second

// qaLogSink collects slog records as "msg k=v ..." text.
type qaLogSink struct {
	mu    sync.Mutex
	lines []qaLogLine
}

type qaLogLine struct {
	Level slog.Level
	Text  string
}

type qaSinkHandler struct {
	sink  *qaLogSink
	attrs []slog.Attr
}

func (h qaSinkHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h qaSinkHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	add := func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%s", a.Key, a.Value.Resolve().String())
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.lines = append(h.sink.lines, qaLogLine{Level: r.Level, Text: b.String()})
	return nil
}

func (h qaSinkHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return qaSinkHandler{sink: h.sink, attrs: append(slices.Clone(h.attrs), as...)}
}

func (h qaSinkHandler) WithGroup(string) slog.Handler { return h }

func (s *qaLogSink) logger() *slog.Logger { return slog.New(qaSinkHandler{sink: s}) }

func (s *qaLogSink) snapshot() []qaLogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

func (s *qaLogSink) at(level slog.Level) []qaLogLine {
	var out []qaLogLine
	for _, l := range s.snapshot() {
		if l.Level == level {
			out = append(out, l)
		}
	}
	return out
}

// qaRequireNoLeak fails when any line of any sink, or the error text, holds a needle.
func qaRequireNoLeak(t *testing.T, err error, sinks []*qaLogSink, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if n == "" {
			t.Fatal("leak needle is empty; the check would be vacuous")
		}
		n = strings.ToLower(n)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), n) {
			t.Errorf("worker error %q holds %q", err, n)
		}
		for _, s := range sinks {
			for _, l := range s.snapshot() {
				if strings.Contains(strings.ToLower(l.Text), n) {
					t.Errorf("log line %q holds %q", l.Text, n)
				}
			}
		}
	}
}

type qaRecHubSpot struct {
	mu    sync.Mutex
	calls []Contact
	fn    func(n int, c Contact) error
}

func (r *qaRecHubSpot) Upsert(_ context.Context, c Contact) error {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	n, fn := len(r.calls), r.fn
	r.mu.Unlock()
	if fn != nil {
		return fn(n, c)
	}
	return nil
}

func (r *qaRecHubSpot) got() []Contact {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

type qaResendCall struct {
	C     Contact
	OptIn bool
}

type qaRecResend struct {
	mu    sync.Mutex
	calls []qaResendCall
	fn    func(n int, c Contact, optIn bool) error
}

func (r *qaRecResend) Sync(_ context.Context, c Contact, optIn bool) error {
	r.mu.Lock()
	r.calls = append(r.calls, qaResendCall{c, optIn})
	n, fn := len(r.calls), r.fn
	r.mu.Unlock()
	if fn != nil {
		return fn(n, c, optIn)
	}
	return nil
}

func (r *qaRecResend) got() []qaResendCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// qaBlockOnce parks call number n until release closes; entered signals the park.
type qaBlockOnce struct {
	n       int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// qaNewBlockOnce releases the park and waits for r's async attempts when the test ends, so a
// failed test leaves no goroutine logging into a finished test.
func qaNewBlockOnce(t *testing.T, r *qaRig, n int) *qaBlockOnce {
	b := &qaBlockOnce{n: n, entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { b.open(); r.async.Wait() })
	return b
}

func (b *qaBlockOnce) open() { b.once.Do(func() { close(b.release) }) }

func (b *qaBlockOnce) wait(call int) error {
	if call != b.n {
		return nil
	}
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return nil
	case <-time.After(qaHoldFor):
		return errors.New("test: block never released")
	}
}

func (b *qaBlockOnce) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(qaHoldFor):
		t.Fatal("the worker never reached the vendor client")
	}
}

type qaRig struct {
	e      env
	hs     *qaRecHubSpot
	rs     *qaRecResend
	w      *DeliverWorker
	async  sync.WaitGroup
	worker *qaLogSink // the worker's own logger
	river  *qaLogSink // River's logger
}

func qaNewRig(t *testing.T, mode Mode) *qaRig {
	t.Helper()
	e := newEnv(t)
	r := &qaRig{e: e, hs: &qaRecHubSpot{}, rs: &qaRecResend{}, worker: &qaLogSink{}, river: &qaLogSink{}}
	r.w = &DeliverWorker{Pool: e.app, HubSpot: r.hs, Resend: r.rs, Mode: mode, Logger: r.worker.logger()}
	return r
}

func (r *qaRig) sinks() []*qaLogSink { return []*qaLogSink{r.worker, r.river} }

func (r *qaRig) jobRow(t *testing.T, email, dest string, version int64) *rivertype.JobRow {
	t.Helper()
	var id int64
	err := r.e.app.QueryRow(context.Background(), `
		SELECT id FROM river_job
		WHERE kind = 'contact_deliver' AND args->>'email' = $1 AND args->>'destination' = $2
		  AND (args->>'version')::bigint = $3
		ORDER BY id DESC LIMIT 1`, email, dest, version).Scan(&id)
	if err != nil {
		t.Fatalf("find %s@%d job for %s: %v", dest, version, email, err)
	}
	row, err := r.e.store.river.JobGet(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return row
}

// freshJob inserts a job with the same args as a finished one, as a later delivery would.
func (r *qaRig) freshJob(t *testing.T, args DeliverArgs) *rivertype.JobRow {
	t.Helper()
	res, err := r.e.store.river.Insert(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("insert %+v: %v", args, err)
	}
	if res.UniqueSkippedAsDuplicate {
		t.Fatalf("insert %+v was skipped as a duplicate", args)
	}
	return res.Job
}

// workHeld runs one attempt in a transaction that stays open until commit: River's own job
// update is not visible to anyone else until then, like a job that is still running.
func (r *qaRig) workHeld(t *testing.T, row *rivertype.JobRow) (*rivertest.WorkResult, error, func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := r.e.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	cfg := &river.Config{Logger: r.river.logger()}
	res, werr := rivertest.NewWorker(t, riverpgxv5.New(nil), cfg, r.w).WorkJob(ctx, t, tx, row)
	return res, werr, func() {
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit: %v", err)
		}
	}
}

func (r *qaRig) work(t *testing.T, row *rivertype.JobRow) (*rivertest.WorkResult, error) {
	t.Helper()
	res, err, commit := r.workHeld(t, row)
	commit()
	return res, err
}

// workAsync runs one attempt in a goroutine; read the outcome from the channel.
type qaAttempt struct {
	res *rivertest.WorkResult
	err error
}

func (r *qaRig) workAsync(t *testing.T, row *rivertype.JobRow) <-chan qaAttempt {
	t.Helper()
	out := make(chan qaAttempt, 1)
	r.async.Add(1)
	go func() {
		defer r.async.Done()
		res, err, commit := r.workHeld(t, row)
		commit()
		out <- qaAttempt{res, err}
	}()
	return out
}

func qaAwaitAttempt(t *testing.T, ch <-chan qaAttempt) qaAttempt {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(qaHoldFor):
		t.Fatal("the attempt never returned")
		return qaAttempt{}
	}
}

type qaDeliveryState struct {
	Version                 int64
	HubSpotAt, ResendAt, In *time.Time
	Mode                    *string
	First, Last             *string
}

func (r *qaRig) delivery(t *testing.T, email string) qaDeliveryState {
	t.Helper()
	var d qaDeliveryState
	err := r.e.app.QueryRow(context.Background(), `
		SELECT version, hubspot_delivered_at, resend_delivered_at, resend_opt_in_sent_at, delivery_mode, first_name, last_name
		FROM contacts WHERE email = $1`, email).Scan(&d.Version, &d.HubSpotAt, &d.ResendAt, &d.In, &d.Mode, &d.First, &d.Last)
	if err != nil {
		t.Fatalf("read delivery state of %s: %v", email, err)
	}
	return d
}

func qaRegistrant(t *testing.T, e env, email, name, workspace, consent string) {
	t.Helper()
	if err := e.store.Registrant(context.Background(), RegistrantIntake{
		UserID: uuid.NewString(), Email: email, DisplayName: name, WorkspaceName: workspace, ConsentText: consent,
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
}

func qaRequireCompleted(t *testing.T, res *rivertest.WorkResult, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: err = %v, want nil", what, err)
	}
	if res.EventKind != river.EventKindJobCompleted || res.Job.State != rivertype.JobStateCompleted {
		t.Fatalf("%s: event %q, job state %q, want the job completed", what, res.EventKind, res.Job.State)
	}
}

// qaRequireRetryable: the job failed and River will run it again. A first failure is retried
// within a second, which River records as available rather than retryable.
func qaRequireRetryable(t *testing.T, res *rivertest.WorkResult, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want the job to fail", what)
	}
	retried := res.Job.State == rivertype.JobStateRetryable || res.Job.State == rivertype.JobStateAvailable
	if res.EventKind != river.EventKindJobFailed || !retried || res.Job.FinalizedAt != nil || len(res.Job.Errors) == 0 {
		t.Fatalf("%s: event %q, job state %q, finalized %v, %d error(s), want a failed job River will retry",
			what, res.EventKind, res.Job.State, res.Job.FinalizedAt, len(res.Job.Errors))
	}
}

// qaIntakeWhileRunning runs an intake while a job's River update is held open. An intake that
// blocks on that job's row (a unique key that ignores the version) fails instead of hanging.
func qaIntakeWhileRunning(t *testing.T, intake func(context.Context, *Store) error, e env) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- intake(context.Background(), e.store) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("intake while the job runs: %v", err)
		}
	case <-time.After(qaHoldFor):
		t.Fatal("the intake blocked on the running job: its insert must not collide with the version being worked")
	}
}

func qaHasTag(c Contact, tag string) bool { return slices.Contains(c.Tags, tag) }

func TestDeliver_HubSpotRecordsDelivery(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "dh")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "Analytical Engines", "")

	before := dbNow(t, r.e)
	res, err := r.work(t, r.jobRow(t, email, "hubspot", 1))
	after := dbNow(t, r.e)
	qaRequireCompleted(t, res, err, "hubspot job")

	calls := r.hs.got()
	if len(calls) != 1 {
		t.Fatalf("HubSpot.Upsert called %d times, want 1", len(calls))
	}
	c := calls[0]
	if c.Email != email || c.FirstName != "Ada" || c.LastName != "Lovelace" || c.Company != "Analytical Engines" {
		t.Errorf("Upsert contact = %+v, want %s / Ada / Lovelace / Analytical Engines", c, email)
	}
	if !slices.Equal(c.Tags, []string{"registered"}) || c.MarketingEligible {
		t.Errorf("Upsert tags = %v, marketing eligible = %v, want [registered], false", c.Tags, c.MarketingEligible)
	}
	d := r.delivery(t, email)
	requireSetBetween(t, "hubspot_delivered_at", d.HubSpotAt, before, after)
	if d.Mode == nil || *d.Mode != "real" {
		t.Errorf("delivery_mode = %q, want real", deref(d.Mode))
	}
	if d.ResendAt != nil || d.Version != 1 || len(r.rs.got()) != 0 {
		t.Errorf("resend_delivered_at = %v, version = %d, Sync calls = %d; a HubSpot job must touch neither Resend nor the version", d.ResendAt, d.Version, len(r.rs.got()))
	}
}

func TestDeliver_ResendRecordsDelivery(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "dr")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "Analytical Engines", "")

	before := dbNow(t, r.e)
	res, err := r.work(t, r.jobRow(t, email, "resend", 1))
	after := dbNow(t, r.e)
	qaRequireCompleted(t, res, err, "resend job")

	calls := r.rs.got()
	if len(calls) != 1 {
		t.Fatalf("Resend.Sync called %d times, want 1", len(calls))
	}
	if calls[0].C.Email != email || !qaHasTag(calls[0].C, "registered") || calls[0].C.FirstName != "Ada" {
		t.Errorf("Sync contact = %+v, want %s, tagged registered, first name Ada", calls[0].C, email)
	}
	d := r.delivery(t, email)
	requireSetBetween(t, "resend_delivered_at", d.ResendAt, before, after)
	if d.Mode == nil || *d.Mode != "real" {
		t.Errorf("delivery_mode = %q, want real", deref(d.Mode))
	}
	if d.HubSpotAt != nil || len(r.hs.got()) != 0 {
		t.Errorf("hubspot_delivered_at = %v, Upsert calls = %d; a Resend job must not touch HubSpot", d.HubSpotAt, len(r.hs.got()))
	}
}

func TestDeliver_DeliveredDestinationIsSkipped(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "skip")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")
	if _, err := r.e.app.Exec(context.Background(),
		`UPDATE contacts SET hubspot_delivered_at = now(), delivery_mode = 'real' WHERE email = $1`, email); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	was := r.delivery(t, email)

	res, err := r.work(t, r.jobRow(t, email, "hubspot", 1))
	qaRequireCompleted(t, res, err, "stale hubspot job")
	if n := len(r.hs.got()); n != 0 {
		t.Fatalf("HubSpot.Upsert called %d times for a delivered destination, want 0", n)
	}
	now := r.delivery(t, email)
	requireSameTime(t, "hubspot_delivered_at", now.HubSpotAt, was.HubSpotAt)

	// The same worker delivers once the time is clear: the skip above is the delivery time, not a dead worker.
	if _, err := r.e.app.Exec(context.Background(), `UPDATE contacts SET hubspot_delivered_at = NULL WHERE email = $1`, email); err != nil {
		t.Fatalf("clear delivered: %v", err)
	}
	res, err = r.work(t, r.freshJob(t, DeliverArgs{Email: email, Destination: "hubspot", Version: 1}))
	qaRequireCompleted(t, res, err, "later hubspot job")
	if n := len(r.hs.got()); n != 1 {
		t.Fatalf("HubSpot.Upsert called %d times after the time was cleared, want 1", n)
	}
}

func TestDeliver_OutageRetriesThenArrives(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "outage")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")
	r.hs.fn = func(n int, _ Contact) error {
		if n < 3 {
			return &DeliveryError{Status: 503}
		}
		return nil
	}

	row := r.jobRow(t, email, "hubspot", 1)
	for n := 1; n <= 2; n++ {
		res, err := r.work(t, row)
		qaRequireRetryable(t, res, err, fmt.Sprintf("attempt %d during the outage", n))
		var de *DeliveryError
		if !errors.As(err, &de) || de.Status != 503 {
			t.Errorf("attempt %d err = %v, want the client's 503 DeliveryError", n, err)
		}
		if res.Job.Attempt != n {
			t.Errorf("attempt %d ran as attempt %d", n, res.Job.Attempt)
		}
		if d := r.delivery(t, email); d.HubSpotAt != nil {
			t.Fatalf("attempt %d: hubspot_delivered_at = %v, want NULL while the client fails", n, d.HubSpotAt)
		}
		row = res.Job
	}

	before := dbNow(t, r.e)
	res, err := r.work(t, row)
	after := dbNow(t, r.e)
	qaRequireCompleted(t, res, err, "attempt 3 after the outage")
	if res.Job.Attempt != 3 || len(r.hs.got()) != 3 {
		t.Errorf("attempt = %d, client calls = %d, want 3 and 3", res.Job.Attempt, len(r.hs.got()))
	}
	d := r.delivery(t, email)
	requireSetBetween(t, "hubspot_delivered_at", d.HubSpotAt, before, after)
	if d.Mode == nil || *d.Mode != "real" {
		t.Errorf("delivery_mode = %q, want real", deref(d.Mode))
	}
}

func TestDeliver_FailureLeavesDeliveryUnset(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "fail")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")
	r.hs.fn = func(int, Contact) error { return &DeliveryError{Status: 500} }

	res, err := r.work(t, r.jobRow(t, email, "hubspot", 1))
	qaRequireRetryable(t, res, err, "failing hubspot job")
	if n := len(r.hs.got()); n != 1 {
		t.Fatalf("HubSpot.Upsert called %d times, want 1", n)
	}
	d := r.delivery(t, email)
	if d.HubSpotAt != nil || d.Mode != nil || d.Version != 1 {
		t.Errorf("hubspot_delivered_at = %v, delivery_mode = %q, version = %d, want NULL, NULL, 1 after a failed delivery", d.HubSpotAt, deref(d.Mode), d.Version)
	}
}

// Intake lands while the client runs: the update misses the new version; the version 2 job delivers.
func TestDeliver_FactAddedBeforeUpdateIsDeliveredByTheNewJob(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "before")
	qaRegistrant(t, r.e, email, "Zelda Quuxington", "Zeta Holdings", "")
	gate := qaNewBlockOnce(t, r, 1)
	r.hs.fn = func(n int, _ Contact) error { return gate.wait(n) }

	running := r.workAsync(t, r.jobRow(t, email, "hubspot", 1))
	gate.awaitEntered(t)
	qaIntakeWhileRunning(t, func(ctx context.Context, s *Store) error {
		return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Zelda Quuxington", Company: "Zeta Holdings", ConsentText: consentText})
	}, r.e)
	gate.open()

	a := qaAwaitAttempt(t, running)
	qaRequireCompleted(t, a.res, a.err, "attempt 1 after the version moved")
	d := r.delivery(t, email)
	if d.Version != 2 || d.HubSpotAt != nil {
		t.Fatalf("version = %d, hubspot_delivered_at = %v, want 2 and NULL: the stale attempt must not record a delivery", d.Version, d.HubSpotAt)
	}

	before := dbNow(t, r.e)
	res, err := r.work(t, r.jobRow(t, email, "hubspot", 2))
	after := dbNow(t, r.e)
	qaRequireCompleted(t, res, err, "version 2 job")
	calls := r.hs.got()
	if len(calls) != 2 {
		t.Fatalf("HubSpot.Upsert called %d times, want 2", len(calls))
	}
	if first := calls[0]; qaHasTag(first, "demo request") || first.MarketingEligible {
		t.Errorf("first call = %+v, want the facts as read before the intake", first)
	}
	if second := calls[1]; !qaHasTag(second, "demo request") || !qaHasTag(second, "registered") || !second.MarketingEligible {
		t.Errorf("version 2 call = %+v, want registered + demo request and marketing eligible", second)
	}
	d = r.delivery(t, email)
	requireSetBetween(t, "hubspot_delivered_at", d.HubSpotAt, before, after)

	qaRequireNoLeak(t, a.err, r.sinks(), email, "Zelda", "Quuxington", "Zeta Holdings")
}

// A job queued for an older version is dropped before any vendor call: the newer job delivers.
func TestDeliver_StaleVersionJobMakesNoCall(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "stale")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	if err := r.e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Ada Lovelace"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}

	for _, dest := range []string{"hubspot", "resend"} {
		res, err := r.work(t, r.jobRow(t, email, dest, 1))
		qaRequireCompleted(t, res, err, dest+" version 1 job")
	}
	if n, m := len(r.hs.got()), len(r.rs.got()); n != 0 || m != 0 {
		t.Fatalf("client calls = hubspot %d, resend %d, want none from a stale job", n, m)
	}

	res, err := r.work(t, r.jobRow(t, email, "hubspot", 2))
	qaRequireCompleted(t, res, err, "hubspot version 2 job")
	res, err = r.work(t, r.jobRow(t, email, "resend", 2))
	qaRequireCompleted(t, res, err, "resend version 2 job")
	if n, m := len(r.hs.got()), len(r.rs.got()); n != 1 || m != 1 {
		t.Fatalf("client calls = hubspot %d, resend %d, want 1 each from the current job", n, m)
	}
}

// Intake lands after the update committed but while River still holds the job running.
func TestDeliver_FactAddedAfterUpdateIsQueued(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "after")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")
	row := r.jobRow(t, email, "hubspot", 1)
	if _, err := r.e.admin.Exec(context.Background(),
		`UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now(), attempted_by = ARRAY['qa']::text[] WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("mark the job running: %v", err)
	}

	res, err, commit := r.workHeld(t, row)
	if err != nil {
		t.Fatalf("version 1 attempt: %v", err)
	}
	if n := len(r.hs.got()); n != 1 {
		t.Fatalf("HubSpot.Upsert called %d times, want 1", n)
	}
	if d := r.delivery(t, email); d.HubSpotAt == nil || d.Version != 1 {
		t.Fatalf("hubspot_delivered_at = %v, version = %d, want the version 1 delivery recorded", d.HubSpotAt, d.Version)
	}

	qaIntakeWhileRunning(t, func(ctx context.Context, s *Store) error {
		return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Ada Lovelace", ConsentText: consentText})
	}, r.e)
	if keys := jobKeys(t, jobsFor(t, r.e, email)); !slices.Contains(keys, "hubspot@2") {
		t.Fatalf("jobs = %v, want a hubspot@2 job queued next to the running version 1 job", keys)
	}
	commit()
	if res.Job.State != rivertype.JobStateCompleted {
		t.Fatalf("version 1 job state = %q, want completed", res.Job.State)
	}

	res2, err := r.work(t, r.jobRow(t, email, "hubspot", 2))
	qaRequireCompleted(t, res2, err, "version 2 job")
	calls := r.hs.got()
	if len(calls) != 2 || !qaHasTag(calls[1], "demo request") || !calls[1].MarketingEligible {
		t.Fatalf("HubSpot.Upsert calls = %+v, want a second call carrying demo request and consent", calls)
	}
	if d := r.delivery(t, email); d.HubSpotAt == nil || d.Version != 2 {
		t.Errorf("hubspot_delivered_at = %v, version = %d, want the version 2 delivery recorded", d.HubSpotAt, d.Version)
	}
}

func TestDeliver_OptInSentOnce(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "optin")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)

	res, err := r.work(t, r.jobRow(t, email, "resend", 1))
	qaRequireCompleted(t, res, err, "first resend job")
	d1 := r.delivery(t, email)
	if d1.In == nil || d1.ResendAt == nil {
		t.Fatalf("resend_opt_in_sent_at = %v, resend_delivered_at = %v, want both set after the opt-in", d1.In, d1.ResendAt)
	}

	if err := r.e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Ada Lovelace"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	res, err = r.work(t, r.jobRow(t, email, "resend", 2))
	qaRequireCompleted(t, res, err, "second resend job")

	calls := r.rs.got()
	if len(calls) != 2 || !calls[0].OptIn || calls[1].OptIn {
		t.Fatalf("Sync sendOptIn = %+v, want [true false]", calls)
	}
	d2 := r.delivery(t, email)
	requireSameTime(t, "resend_opt_in_sent_at", d2.In, d1.In)
	if d2.ResendAt == nil || !d2.ResendAt.After(*d1.ResendAt) {
		t.Errorf("resend_delivered_at = %v, want the second delivery recorded after %v", d2.ResendAt, d1.ResendAt)
	}
}

// The opt-in is sent and recorded, then the guarded update misses: the version 2 job must not send it again.
func TestDeliver_OptInIsNotResentAfterAVersionMiss(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "optinretry")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	gate := qaNewBlockOnce(t, r, 1)
	r.rs.fn = func(n int, _ Contact, _ bool) error { return gate.wait(n) }

	running := r.workAsync(t, r.jobRow(t, email, "resend", 1))
	gate.awaitEntered(t)
	qaIntakeWhileRunning(t, func(ctx context.Context, s *Store) error {
		return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Ada Lovelace"})
	}, r.e)
	gate.open()

	a := qaAwaitAttempt(t, running)
	qaRequireCompleted(t, a.res, a.err, "attempt 1 after the version moved")
	d := r.delivery(t, email)
	if d.In == nil || d.ResendAt != nil {
		t.Fatalf("resend_opt_in_sent_at = %v, resend_delivered_at = %v, want the opt-in recorded and no delivery", d.In, d.ResendAt)
	}

	res, err := r.work(t, r.jobRow(t, email, "resend", 2))
	qaRequireCompleted(t, res, err, "version 2 job")
	calls := r.rs.got()
	if len(calls) != 2 || !calls[0].OptIn || calls[1].OptIn {
		t.Fatalf("Sync sendOptIn = %+v, want [true false]: the version 2 job must not send the opt-in again", calls)
	}
}

// qaNotDoneFor is how long a delivery that waits on the per-person lock must stay pending.
const qaNotDoneFor = 300 * time.Millisecond

func qaRequireStillWaiting(t *testing.T, ch <-chan qaAttempt, vendorCalls func() int, what string) {
	t.Helper()
	select {
	case a := <-ch:
		t.Fatalf("%s finished (err = %v) while the earlier version was inside the vendor call", what, a.err)
	case <-time.After(qaNotDoneFor):
	}
	if n := vendorCalls(); n != 1 {
		t.Fatalf("vendor calls = %d while the earlier version is parked, want 1: %s must wait", n, what)
	}
}

// Two workers, one person: the version 2 job waits for the version 1 job, then reads the opt-in it recorded.
func TestDeliver_OptInIsSentOnceWhenTwoVersionsRunTogether(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "optinrace")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	gate := qaNewBlockOnce(t, r, 1)
	r.rs.fn = func(n int, _ Contact, _ bool) error { return gate.wait(n) }

	first := r.workAsync(t, r.jobRow(t, email, "resend", 1))
	gate.awaitEntered(t)
	qaIntakeWhileRunning(t, func(ctx context.Context, s *Store) error {
		return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Ada Lovelace"})
	}, r.e)
	second := r.workAsync(t, r.jobRow(t, email, "resend", 2))
	qaRequireStillWaiting(t, second, func() int { return len(r.rs.got()) }, "version 2 job")

	gate.open()
	a := qaAwaitAttempt(t, first)
	qaRequireCompleted(t, a.res, a.err, "version 1 job")
	b := qaAwaitAttempt(t, second)
	qaRequireCompleted(t, b.res, b.err, "version 2 job")

	calls := r.rs.got()
	opts := 0
	for _, c := range calls {
		if c.OptIn {
			opts++
		}
	}
	if len(calls) != 2 || opts != 1 || !calls[0].OptIn {
		t.Fatalf("Sync calls = %+v, want two with the opt-in on the first only", calls)
	}
}

// Two workers, one person: the version 2 call lands after the version 1 call, so the vendor ends current.
func TestDeliver_StaleCallLandingLastDoesNotLeaveTheVendorStale(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "landrace")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")
	gate := qaNewBlockOnce(t, r, 1)
	var mu sync.Mutex
	var landed []Contact
	r.hs.fn = func(n int, c Contact) error {
		err := gate.wait(n)
		mu.Lock()
		landed = append(landed, c)
		mu.Unlock()
		return err
	}

	first := r.workAsync(t, r.jobRow(t, email, "hubspot", 1))
	gate.awaitEntered(t)
	qaIntakeWhileRunning(t, func(ctx context.Context, s *Store) error {
		return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Ada Lovelace"})
	}, r.e)
	second := r.workAsync(t, r.jobRow(t, email, "hubspot", 2))
	qaRequireStillWaiting(t, second, func() int { return len(r.hs.got()) }, "version 2 job")

	gate.open()
	a := qaAwaitAttempt(t, first)
	qaRequireCompleted(t, a.res, a.err, "version 1 job")
	b := qaAwaitAttempt(t, second)
	qaRequireCompleted(t, b.res, b.err, "version 2 job")

	mu.Lock()
	defer mu.Unlock()
	if len(landed) != 2 {
		t.Fatalf("vendor calls landed = %d, want 2", len(landed))
	}
	if qaHasTag(landed[0], "demo request") || !qaHasTag(landed[1], "demo request") {
		t.Fatalf("landed = %+v, want version 1 first and version 2 (registered + demo request) last", landed)
	}
	if d := r.delivery(t, email); d.Version != 2 || d.HubSpotAt == nil {
		t.Errorf("version = %d, hubspot_delivered_at = %v, want a delivery recorded at version 2", d.Version, d.HubSpotAt)
	}
}

// The lock is per person and destination: a parked HubSpot call for one person holds up neither
// that person's Resend job nor another person's HubSpot job.
func TestDeliver_TheLockIsPerPersonAndDestination(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	ada := uniqueEmail(t, r.e, "lockada")
	bob := uniqueEmail(t, r.e, "lockbob")
	qaRegistrant(t, r.e, ada, "Ada Lovelace", "", consentText)
	qaRegistrant(t, r.e, bob, "Bob Babbage", "", "")
	gate := qaNewBlockOnce(t, r, 1)
	r.hs.fn = func(n int, _ Contact) error { return gate.wait(n) }

	parked := r.workAsync(t, r.jobRow(t, ada, "hubspot", 1))
	gate.awaitEntered(t)
	otherDest := r.workAsync(t, r.jobRow(t, ada, "resend", 1))
	otherPerson := r.workAsync(t, r.jobRow(t, bob, "hubspot", 1))

	a := qaAwaitAttempt(t, otherDest)
	qaRequireCompleted(t, a.res, a.err, "same person, other destination")
	b := qaAwaitAttempt(t, otherPerson)
	qaRequireCompleted(t, b.res, b.err, "other person, same destination")
	if d := r.delivery(t, ada); d.ResendAt == nil || d.HubSpotAt != nil {
		t.Errorf("resend_delivered_at = %v, hubspot_delivered_at = %v, want Resend delivered and HubSpot still parked", d.ResendAt, d.HubSpotAt)
	}

	gate.open()
	c := qaAwaitAttempt(t, parked)
	qaRequireCompleted(t, c.res, c.err, "parked job")
}

func TestDeliver_UntickedRegistrantNeverOptsIn(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "noopt")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", "")

	res, err := r.work(t, r.jobRow(t, email, "resend", 1))
	qaRequireCompleted(t, res, err, "resend job")
	calls := r.rs.got()
	if len(calls) != 1 {
		t.Fatalf("Sync called %d times, want 1: every registrant reaches Resend", len(calls))
	}
	if calls[0].OptIn {
		t.Error("Sync sendOptIn = true for a registrant who did not tick")
	}
	if d := r.delivery(t, email); d.In != nil || d.ResendAt == nil {
		t.Errorf("resend_opt_in_sent_at = %v, resend_delivered_at = %v, want NULL and set", d.In, d.ResendAt)
	}
}

func TestDeliver_UntickedDemoBookerNeverReachesResend(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	plain := uniqueEmail(t, r.e, "demoplain")
	ticked := uniqueEmail(t, r.e, "demotick")
	ctx := context.Background()
	if err := r.e.store.DemoRequest(ctx, DemoIntake{Email: plain, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	if err := r.e.store.DemoRequest(ctx, DemoIntake{Email: ticked, Name: "Grace Hopper", Company: "Navy", ConsentText: consentText}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	requireJobs(t, r.e, plain, "hubspot@1")
	requireJobs(t, r.e, ticked, "hubspot@1", "resend@1")

	// The unticked booker still reaches HubSpot, so the worker is alive.
	res, err := r.work(t, r.jobRow(t, plain, "hubspot", 1))
	qaRequireCompleted(t, res, err, "unticked hubspot job")
	if n := len(r.hs.got()); n != 1 {
		t.Fatalf("HubSpot.Upsert called %d times, want 1", n)
	}

	forced := r.freshJob(t, DeliverArgs{Email: plain, Destination: "resend", Version: 1})
	res, err = r.work(t, forced)
	qaRequireCompleted(t, res, err, "forced resend job")
	if n := len(r.rs.got()); n != 0 {
		t.Fatalf("Sync called %d times for an unticked demo booker, want 0", n)
	}
	if d := r.delivery(t, plain); d.ResendAt != nil || d.In != nil {
		t.Errorf("resend_delivered_at = %v, resend_opt_in_sent_at = %v, want NULL, NULL", d.ResendAt, d.In)
	}

	// A ticked booker does reach Resend and is opted in.
	res, err = r.work(t, r.jobRow(t, ticked, "resend", 1))
	qaRequireCompleted(t, res, err, "ticked resend job")
	calls := r.rs.got()
	if len(calls) != 1 || !calls[0].OptIn || calls[0].C.Email != ticked {
		t.Errorf("Sync calls = %+v, want one opt-in sync for %s", calls, ticked)
	}
}

func TestDeliver_FakeModeRecordsFake(t *testing.T) {
	prev := http.DefaultTransport
	http.DefaultTransport = failTransport{t}
	t.Cleanup(func() { http.DefaultTransport = prev })

	e := newEnv(t)
	sink := &qaLogSink{}
	r := &qaRig{e: e, worker: sink, river: &qaLogSink{}}
	r.w = &DeliverWorker{Pool: e.app, HubSpot: FakeHubSpot{}, Resend: FakeResend{}, Mode: ModeFake, Logger: sink.logger()}
	email := uniqueEmail(t, e, "fake")
	qaRegistrant(t, e, email, "Ada Lovelace", "", consentText)

	for _, dest := range []string{"hubspot", "resend"} {
		res, err := r.work(t, r.jobRow(t, email, dest, 1))
		qaRequireCompleted(t, res, err, dest+" job in fake mode")
	}
	d := r.delivery(t, email)
	if d.HubSpotAt == nil || d.ResendAt == nil {
		t.Fatalf("hubspot_delivered_at = %v, resend_delivered_at = %v, want both recorded in fake mode", d.HubSpotAt, d.ResendAt)
	}
	if d.Mode == nil || *d.Mode != "fake" {
		t.Errorf("delivery_mode = %q, want fake", deref(d.Mode))
	}
}

func TestDeliver_PermanentRejectionLogsError(t *testing.T) {
	cases := []struct {
		dest      string
		status    int
		wantLevel slog.Level
	}{
		{"hubspot", 400, slog.LevelError},
		{"hubspot", 401, slog.LevelError},
		{"hubspot", 409, slog.LevelError},
		{"resend", 422, slog.LevelError},
		{"hubspot", 408, slog.LevelWarn},
		{"hubspot", 429, slog.LevelWarn},
		{"hubspot", 503, slog.LevelWarn},
		{"resend", 500, slog.LevelWarn},
		{"resend", 0, slog.LevelWarn},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_%d", c.dest, c.status), func(t *testing.T) {
			r := qaNewRig(t, ModeReal)
			email := uniqueEmail(t, r.e, "log")
			qaRegistrant(t, r.e, email, "Zelda Quuxington", "Zeta Holdings", consentText)
			fail := func() error { return &DeliveryError{Status: c.status} }
			r.hs.fn = func(int, Contact) error { return fail() }
			r.rs.fn = func(int, Contact, bool) error { return fail() }

			res, err := r.work(t, r.jobRow(t, email, c.dest, 1))
			qaRequireRetryable(t, res, err, "rejected job")
			if d := r.delivery(t, email); d.HubSpotAt != nil || d.ResendAt != nil {
				t.Errorf("a rejected delivery was recorded: hubspot %v, resend %v", d.HubSpotAt, d.ResendAt)
			}

			status := regexp.MustCompile(fmt.Sprintf(`\b%d\b`, c.status))
			var hit []qaLogLine
			for _, l := range r.worker.at(c.wantLevel) {
				if strings.Contains(l.Text, c.dest) && status.MatchString(l.Text) {
					hit = append(hit, l)
				}
			}
			if len(hit) == 0 {
				t.Errorf("no %s line naming %q and status %d; worker lines: %+v", c.wantLevel, c.dest, c.status, r.worker.snapshot())
			}
			if c.wantLevel == slog.LevelWarn {
				if errs := r.worker.at(slog.LevelError); len(errs) != 0 {
					t.Errorf("a transient %d logged at ERROR: %+v", c.status, errs)
				}
			}
			qaRequireNoLeak(t, err, r.sinks(), email, "Zelda", "Quuxington", "Zeta Holdings")
		})
	}
}

// A blank name is stored as an empty string and reaches the real clients as no property at all.
func TestDeliver_BlankNamesAreStoredEmptyAndDropped(t *testing.T) {
	e := newEnv(t)
	blank := uniqueEmail(t, e, "blank")
	named := uniqueEmail(t, e, "named")
	qaRegistrant(t, e, blank, "", "", "")
	qaRegistrant(t, e, named, "Ada Lovelace", "Analytical Engines", "")
	r := &qaRig{e: e, worker: &qaLogSink{}, river: &qaLogSink{}}
	if d := r.delivery(t, blank); d.First == nil || *d.First != "" || d.Last == nil || *d.Last != "" {
		t.Fatalf("blank registrant names = %q / %q, want '' (not NULL): the case under test is gone", deref(d.First), deref(d.Last))
	}

	hsVendor := newVendor(t, hubspotRoutes(200, 201))
	rsVendor := newVendor(t, resendAbsent.respond)
	r.w = &DeliverWorker{Pool: e.app, HubSpot: hubspotAt(hsVendor, nil), Resend: resendAt(rsVendor, nil), Mode: ModeReal, Logger: r.worker.logger()}

	for _, email := range []string{blank, named} {
		for _, dest := range []string{"hubspot", "resend"} {
			res, err := r.work(t, r.jobRow(t, email, dest, 1))
			qaRequireCompleted(t, res, err, dest+" job for "+email)
		}
	}

	var patches []seenReq
	for _, s := range hsVendor.calls() {
		if s.Method == http.MethodPatch {
			patches = append(patches, s)
		}
	}
	if len(patches) != 2 {
		t.Fatalf("HubSpot PATCH requests = %d, want 2", len(patches))
	}
	for _, s := range patches {
		props := jsonProps(t, s)
		isBlank := strings.Contains(s.URI, "blank-")
		for _, k := range []string{"firstname", "lastname", "company"} {
			if _, has := props[k]; has == isBlank {
				t.Errorf("%s: property %q present = %v, want %v (props %v)", s.label(), k, has, !isBlank, props)
			}
		}
		if props["ascomply_contact_tags"] != ";registered" {
			t.Errorf("%s: tags = %q, want %q", s.label(), props["ascomply_contact_tags"], ";registered")
		}
	}

	var creates []map[string]string
	for _, s := range rsVendor.calls() {
		if s.Method == http.MethodPost && s.path() == "/contacts" {
			var body map[string]string
			if err := json.Unmarshal(s.Body, &body); err != nil {
				t.Fatalf("Resend create body %q: %v", s.Body, err)
			}
			creates = append(creates, body)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("Resend create requests = %d, want 2", len(creates))
	}
	for _, body := range creates {
		isBlank := strings.HasPrefix(body["email"], "blank-")
		for _, k := range []string{"first_name", "last_name"} {
			if _, has := body[k]; has == isBlank {
				t.Errorf("Resend create for %s: %q present = %v, want %v", body["email"], k, has, !isBlank)
			}
		}
	}
}

// A Sync that fails after the intake asked for the opt-in must leave the opt-in unsent, so the retry sends it.
func TestDeliver_FailedSyncDoesNotMarkTheOptIn(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "optinfail")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	r.rs.fn = func(n int, _ Contact, _ bool) error {
		if n == 1 {
			return &DeliveryError{Status: 503}
		}
		return nil
	}

	res, err := r.work(t, r.jobRow(t, email, "resend", 1))
	qaRequireRetryable(t, res, err, "resend job with a failing Sync")
	d := r.delivery(t, email)
	if d.In != nil || d.ResendAt != nil {
		t.Fatalf("resend_opt_in_sent_at = %v, resend_delivered_at = %v, want NULL, NULL: the opt-in never went out", d.In, d.ResendAt)
	}

	res, err = r.work(t, res.Job)
	qaRequireCompleted(t, res, err, "retry")
	calls := r.rs.got()
	if len(calls) != 2 || !calls[0].OptIn || !calls[1].OptIn {
		t.Fatalf("Sync sendOptIn = %+v, want [true true]: the retry owes the opt-in the first attempt never sent", calls)
	}
	if d := r.delivery(t, email); d.In == nil || d.ResendAt == nil {
		t.Errorf("resend_opt_in_sent_at = %v, resend_delivered_at = %v, want both set after the retry", d.In, d.ResendAt)
	}
}

// A contact that is gone completes the job instead of retrying it for three weeks.
func TestDeliver_MissingContactCompletesWithoutACall(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "gone")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	hsRow, rsRow := r.jobRow(t, email, "hubspot", 1), r.jobRow(t, email, "resend", 1)
	if _, err := r.e.admin.Exec(context.Background(), `DELETE FROM contacts WHERE email = $1`, email); err != nil {
		t.Fatalf("delete the contact: %v", err)
	}

	for _, row := range []*rivertype.JobRow{hsRow, rsRow} {
		res, err := r.work(t, row)
		qaRequireCompleted(t, res, err, "job for a deleted contact")
	}
	if len(r.hs.got()) != 0 || len(r.rs.got()) != 0 {
		t.Errorf("clients called %d + %d times for a deleted contact, want 0 + 0", len(r.hs.got()), len(r.rs.got()))
	}
}

// A destination this build does not know is cancelled: no retry, no client call, no row change.
func TestDeliver_UnknownDestinationIsCancelled(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "bogus")
	qaRegistrant(t, r.e, email, "Ada Lovelace", "", consentText)
	was := r.delivery(t, email)

	res, err := r.work(t, r.freshJob(t, DeliverArgs{Email: email, Destination: "mailchimp", Version: 1}))
	if res.EventKind != river.EventKindJobCancelled || res.Job.State != rivertype.JobStateCancelled {
		t.Fatalf("err = %v, event %q, job state %q, want the job cancelled", err, res.EventKind, res.Job.State)
	}
	if len(r.hs.got()) != 0 || len(r.rs.got()) != 0 {
		t.Errorf("clients called %d + %d times for an unknown destination, want 0 + 0", len(r.hs.got()), len(r.rs.got()))
	}
	now := r.delivery(t, email)
	if now.HubSpotAt != nil || now.ResendAt != nil || now.In != nil || now.Version != was.Version {
		t.Errorf("row after the cancelled job = %+v, want it untouched (%+v)", now, was)
	}

	// The known destinations still run, so the cancel above is the destination's fault.
	res, err = r.work(t, r.jobRow(t, email, "hubspot", 1))
	qaRequireCompleted(t, res, err, "hubspot job")
}

// A failure that is not a *DeliveryError has no status: WARN with status 0, never ERROR, never the email.
func TestDeliver_UntypedClientErrorWarnsAndRetries(t *testing.T) {
	r := qaNewRig(t, ModeReal)
	email := uniqueEmail(t, r.e, "untyped")
	qaRegistrant(t, r.e, email, "Zelda Quuxington", "Zeta Holdings", "")
	r.hs.fn = func(int, Contact) error { return errors.New("dial tcp 10.0.0.1:443: connect: connection refused") }

	res, err := r.work(t, r.jobRow(t, email, "hubspot", 1))
	qaRequireRetryable(t, res, err, "hubspot job with a refused connection")
	warns := r.worker.at(slog.LevelWarn)
	if len(warns) == 0 || !strings.Contains(warns[0].Text, "hubspot") {
		t.Fatalf("WARN lines = %+v, want one naming hubspot", warns)
	}
	if errs := r.worker.at(slog.LevelError); len(errs) != 0 {
		t.Errorf("an untyped failure logged at ERROR: %+v", errs)
	}
	qaRequireNoLeak(t, nil, r.sinks(), email, "Zelda", "Quuxington", "Zeta Holdings")
}
