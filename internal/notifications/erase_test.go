package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/accountevents"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

const rebuildWarn = "erasure: the address registered again; its vendor contact is rebuilt"

type restoreCall struct {
	email, first, last string
	st                 ResendState
}

type recErase struct {
	mu         sync.Mutex
	calls      []string
	fn         func(n int, who string) error
	state      ResendState // answered by ContactState
	restoreErr error       // returned by RestoreContact
	stateErr   error       // returned by ContactState
	events     []string    // "state", "erase" and "restore", in call order
	restored   []restoreCall
}

func (r *recErase) event(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recErase) ContactState(context.Context, string) (ResendState, error) {
	r.event("state")
	return r.state, r.stateErr
}

func (r *recErase) RestoreContact(_ context.Context, email, first, last string, st ResendState) error {
	r.event("restore")
	r.mu.Lock()
	r.restored = append(r.restored, restoreCall{email, first, last, st})
	err := r.restoreErr
	r.mu.Unlock()
	return err
}

func (r *recErase) record(who string) error {
	r.mu.Lock()
	r.calls = append(r.calls, who)
	n, fn := len(r.calls), r.fn
	r.mu.Unlock()
	if fn != nil {
		return fn(n, who)
	}
	return nil
}

func (r *recErase) EraseContact(_ context.Context, email string) error {
	r.event("erase")
	return r.record(email)
}
func (r *recErase) DeleteUser(_ context.Context, id string) error { return r.record(id) }

func (r *recErase) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type recBlog struct {
	mu    sync.Mutex
	calls int
}

func (r *recBlog) SyncBlog(context.Context, BlogSubscriber) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return nil
}

func (r *recBlog) ContactUnsubscribed(context.Context, string) (bool, error) { return false, nil }

type eraseRig struct {
	t         *testing.T
	e         env
	hs, rs    *recErase
	acct      *recErase
	sink      *qaLogSink
	w         *EraseWorker
	uid, mail string
}

// track removes every row and job that carries the email or the user id at cleanup.
func (r *eraseRig) track(email, uid string) {
	r.t.Cleanup(func() {
		ctx := context.Background()
		for _, c := range []struct {
			q    string
			args []any
		}{
			{`DELETE FROM river_job WHERE lower(btrim(args->>'email')) = $1 OR args->>'user_id' = $2`, []any{email, uid}},
			{`DELETE FROM person_erasures WHERE user_id = $1::uuid`, []any{uid}},
			{`DELETE FROM registrant_handoffs WHERE user_id = $1 OR email = $2`, []any{uid, email}},
			{`DELETE FROM contacts WHERE email = $1`, []any{email}},
			{`DELETE FROM account_known_devices WHERE user_id::text = $1`, []any{uid}},
			{`DELETE FROM account_event_handoffs WHERE user_id::text = $1 OR lower(btrim(email)) = $2`, []any{uid, email}},
			{`DELETE FROM blog_subscribers WHERE email = $1`, []any{email}},
			{`DELETE FROM blog_subscription_requests WHERE email = $1`, []any{email}},
		} {
			if _, err := r.e.admin.Exec(ctx, c.q, c.args...); err != nil {
				r.t.Errorf("cleanup: %v", err)
			}
		}
	})
}

func newEraseRig(t *testing.T) *eraseRig {
	t.Helper()
	e := newEnv(t)
	r := &eraseRig{t: t, e: e, hs: &recErase{}, rs: &recErase{}, acct: &recErase{}, sink: &qaLogSink{},
		uid: uuid.NewString(), mail: fmt.Sprintf("era-%s@corp.example", uuid.NewString()[:8])}
	r.track(r.mail, r.uid)
	r.w = &EraseWorker{Pool: e.app, River: e.store.river, HubSpot: r.hs, Resend: r.rs, Account: r.acct, Mode: ModeReal, Logger: r.sink.logger()}
	return r
}

// person returns another user id and email, tracked for cleanup.
func (r *eraseRig) person() (uid, email string) {
	uid, email = uuid.NewString(), fmt.Sprintf("oth-%s@corp.example", uuid.NewString()[:8])
	r.track(email, uid)
	return
}

func (r *eraseRig) enqueue(uid, email string) int64 {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.e.app.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := EnqueueErasure(ctx, tx, r.e.store.river, uid, email); err != nil {
		_ = tx.Rollback(ctx)
		r.t.Fatalf("enqueue: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		r.t.Fatal(err)
	}
	var gen int64
	if err := r.e.admin.QueryRow(ctx, `SELECT generation FROM person_erasures WHERE user_id = $1`, uid).Scan(&gen); err != nil {
		r.t.Fatal(err)
	}
	return gen
}

// start records the erasure of the rig's person.
func (r *eraseRig) start() int64 { return r.enqueue(r.uid, r.mail) }

func (r *eraseRig) work(gen int64) error {
	return r.workCtx(context.Background(), gen)
}

func (r *eraseRig) workCtx(ctx context.Context, gen int64) error {
	return r.w.Work(ctx, &river.Job[EraseArgs]{Args: EraseArgs{UserID: r.uid, Generation: gen}})
}

type rowState struct {
	Email                                *string
	Gen                                  int64
	Local, Account, HubSpot, Resend, Fin bool
	Superseded                           bool
	Mode                                 *string
}

func (r *eraseRig) row(uid string) rowState {
	r.t.Helper()
	var s rowState
	err := r.e.admin.QueryRow(context.Background(), `
		SELECT email, generation, local_erased_at IS NOT NULL, account_erased_at IS NOT NULL, hubspot_erased_at IS NOT NULL,
		       resend_erased_at IS NOT NULL, finished_at IS NOT NULL, superseded_at IS NOT NULL, vendor_mode
		FROM person_erasures WHERE user_id = $1`, uid).Scan(&s.Email, &s.Gen, &s.Local, &s.Account, &s.HubSpot, &s.Resend, &s.Fin, &s.Superseded, &s.Mode)
	if err != nil {
		r.t.Fatalf("read erasure row: %v", err)
	}
	return s
}

func (r *eraseRig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.e.admin.Exec(context.Background(), q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *eraseRig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.e.admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (r *eraseRig) insertJob(args river.JobArgs) int64 {
	r.t.Helper()
	res, err := r.e.store.river.Insert(context.Background(), args, nil)
	if err != nil {
		r.t.Fatalf("insert job: %v", err)
	}
	return res.Job.ID
}

func (r *eraseRig) jobExists(id int64) bool {
	return r.count(`SELECT count(*) FROM river_job WHERE id = $1`, id) == 1
}

func (r *eraseRig) setState(id int64, state string) {
	r.t.Helper()
	switch state {
	case "running":
		r.exec(`UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now(), attempted_by = ARRAY['t'] WHERE id = $1`, id)
	default:
		r.exec(`UPDATE river_job SET state = $2::river_job_state, finalized_at = now() WHERE id = $1`, id, state)
	}
}

func (r *eraseRig) contact(email string, uid *string, version int) {
	r.t.Helper()
	r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at, version) VALUES ($1, $2::uuid, 'Adaeze', 'Obi', now(), $3)`, email, uid, version)
}

func (r *eraseRig) handoff(uid, email string) {
	r.t.Helper()
	r.exec(`INSERT INTO registrant_handoffs (user_id, email, display_name, workspace_name, consent_text, consent_at) VALUES ($1, $2, '', '', '', '')`, uid, email)
}

func (r *eraseRig) blog(email string, version int) {
	r.t.Helper()
	r.exec(`INSERT INTO blog_subscribers (email, category_ids, frequency, confirmed_at, version, resend_delivered_at) VALUES ($1, '{a}', 'weekly', now(), $2, now())`, email, version)
}

func (r *eraseRig) blogRequest(email string) {
	r.t.Helper()
	r.exec(`INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at) VALUES ($1, '{a}', 'weekly', sha256(gen_random_uuid()::text::bytea), now() + interval '1 hour')`, email)
}

// failAll makes every step fail so a test sees the sweep alone.
func (r *eraseRig) failAll() {
	boom := errors.New("boom")
	r.acct.fn = func(int, string) error { return boom }
	r.hs.fn = func(int, string) error { return boom }
	r.rs.fn = func(int, string) error { return boom }
}

func (r *eraseRig) fourJobs(uid, email string) []int64 {
	return []int64{
		r.insertJob(DeliverArgs{Email: " " + strings.ToUpper(email) + " ", Destination: "hubspot", Version: 1}),
		r.insertJob(DemoDealArgs{Email: email, Name: "Adaeze Obi"}),
		r.insertJob(RegistrantArgs{UserID: uid, Email: "other-" + email, DisplayName: "Adaeze Obi"}),
		r.insertJob(BlogSyncArgs{Email: email, Version: 1}),
	}
}

func TestEnqueueErasure_RecordsTheRowAndOneJob(t *testing.T) {
	r := newEraseRig(t)
	r.start()
	if gen := r.row(r.uid).Gen; gen != 1 {
		t.Fatalf("generation = %d", gen)
	}
	s := r.row(r.uid)
	if s.Email == nil || *s.Email != r.mail || s.Local || s.Account || s.HubSpot || s.Resend || s.Fin {
		t.Fatalf("row = %+v", s)
	}
	var raw []byte
	if err := r.e.admin.QueryRow(context.Background(), `SELECT args FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' = $1`, r.uid).Scan(&raw); err != nil {
		t.Fatalf("job: %v", err)
	}
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	if len(args) != 2 || args["user_id"] != r.uid || args["generation"] != float64(1) {
		t.Fatalf("args = %s", raw)
	}
}

func TestEnqueueErasure_NormalisesTheEmailAndCanonicalisesTheUserID(t *testing.T) {
	r := newEraseRig(t)
	r.enqueue(strings.ToUpper(r.uid), " "+strings.ToUpper(r.mail)+" ")
	if s := r.row(r.uid); s.Email == nil || *s.Email != r.mail {
		t.Fatalf("row = %+v", s)
	}
	if n := r.count(`SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' = $1`, r.uid); n != 1 {
		t.Fatalf("lower-case user id jobs = %d", n)
	}
}

func TestEnqueueErasure_RefusesBadInput(t *testing.T) {
	r := newEraseRig(t)
	ctx := context.Background()
	for _, c := range []struct{ uid, email string }{
		{"not-a-uuid", r.mail}, {r.uid, ""}, {r.uid, "   "}, {r.uid, "ab"},
	} {
		tx, err := r.e.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = EnqueueErasure(ctx, tx, r.e.store.river, c.uid, c.email)
		if _, qerr := tx.Exec(ctx, `SELECT 1`); qerr != nil {
			t.Errorf("%+v: the refusal left the caller's transaction aborted: %v", c, qerr)
		}
		_ = tx.Rollback(ctx)
		if err == nil {
			t.Fatalf("%+v accepted", c)
		}
		for _, in := range []string{c.uid, c.email} {
			if strings.TrimSpace(in) != "" && strings.Contains(err.Error(), in) {
				t.Errorf("error %q holds the input", err)
			}
		}
	}
	if n := r.count(`SELECT count(*) FROM person_erasures WHERE user_id = $1::uuid`, r.uid); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestEnqueueErasure_EveryCallQueuesItsOwnJob(t *testing.T) {
	r := newEraseRig(t)
	r.start()
	r.start()
	if s := r.row(r.uid); s.Gen != 2 {
		t.Fatalf("generation = %d", s.Gen)
	}
	q := `SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND state = 'available' AND args->>'user_id' = $1 AND (args->>'generation')::int = $2`
	if r.count(q, r.uid, 1) != 1 || r.count(q, r.uid, 2) != 1 {
		t.Fatal("want one available job for each generation")
	}
	r.exec(`UPDATE person_erasures SET local_erased_at = now(), account_erased_at = now(), hubspot_erased_at = now(), resend_erased_at = now(),
		vendor_mode = 'real', superseded_at = now(), finished_at = now(), email = NULL WHERE user_id = $1`, r.uid)
	r.start()
	s := r.row(r.uid)
	if s.Fin || s.Email == nil || *s.Email != r.mail || s.Gen != 3 || r.count(q, r.uid, 3) != 1 {
		t.Fatalf("row after a re-request = %+v", s)
	}
	if s.Local || s.Account || s.HubSpot || s.Resend || s.Superseded || s.Mode != nil {
		t.Fatalf("a re-request kept a stamp: %+v", s)
	}
}

func TestErase_SweepRemovesTheQueuedJobsOfThePerson(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	mine := r.fourJobs(r.uid, r.mail)
	ouid, omail := r.person()
	others := r.fourJobs(ouid, omail)
	r.failAll()
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	for _, id := range mine {
		if r.jobExists(id) {
			t.Errorf("job %d of the person is left", id)
		}
	}
	for _, id := range others {
		if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'available'`, id) != 1 {
			t.Errorf("job %d of another person was touched", id)
		}
	}
}

// A finished job row is locked by the test, so the sweep's DELETE blocks after its cancel ran.
func TestErase_SweepCancelsThenDeletesInOneTransaction(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	queued := r.insertJob(BlogSyncArgs{Email: r.mail, Version: 1})
	finished := r.insertJob(BlogSyncArgs{Email: r.mail, Version: 2})
	r.setState(finished, "completed")
	r.failAll()
	ctx := context.Background()
	hold, err := r.e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(ctx)
	if _, err := hold.Exec(ctx, `SELECT 1 FROM river_job WHERE id = $1 FOR UPDATE`, finished); err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { res <- r.work(gen) }()
	deadline := time.Now().Add(qaHoldFor)
	for r.count(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'DELETE FROM river_job%'`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sweep never reached its job delete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'available'`, queued) != 1 {
		t.Fatal("the cancel was visible before the sweep committed")
	}
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-res
	if r.jobExists(queued) || r.jobExists(finished) {
		t.Fatal("a job is left after the sweep")
	}
}

func TestErase_SweepDeletesTheLocalCopies(t *testing.T) {
	for _, withAccount := range []bool{true, false} {
		t.Run(fmt.Sprintf("contact_has_user_id=%v", withAccount), func(t *testing.T) {
			r := newEraseRig(t)
			gen := r.start()
			ouid, omail := r.person()
			var cuid *string
			if withAccount {
				cuid = &r.uid
			}
			r.handoff(r.uid, r.mail)
			r.contact(r.mail, cuid, 1)
			r.blog(r.mail, 1)
			r.blogRequest(r.mail)
			r.handoff(ouid, omail)
			r.contact(omail, &ouid, 1)
			r.blog(omail, 1)
			r.blogRequest(omail)
			r.failAll()
			_ = r.work(gen)
			for _, q := range []string{
				`SELECT count(*) FROM registrant_handoffs WHERE email = $1`, `SELECT count(*) FROM contacts WHERE email = $1`,
				`SELECT count(*) FROM blog_subscribers WHERE email = $1`, `SELECT count(*) FROM blog_subscription_requests WHERE email = $1`,
			} {
				if n := r.count(q, r.mail); n != 0 {
					t.Errorf("%s: %d left", q, n)
				}
				if n := r.count(q, omail); n != 1 {
					t.Errorf("%s: another person has %d", q, n)
				}
			}
		})
	}
}

func TestErase_RunningJobIsMarkedThenRemovedByTheFinalSweep(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	id := r.insertJob(DeliverArgs{Email: r.mail, Destination: "resend", Version: 1})
	r.setState(id, "running")
	if err := r.work(gen); err == nil {
		t.Fatal("attempt 1: the final sweep must fail while the job runs")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'running' AND metadata ? 'cancel_attempted_at'`, id) != 1 {
		t.Fatal("the running job is not marked")
	}
	r.setState(id, "cancelled")
	if err := r.work(gen); err == nil {
		t.Fatal("attempt 2: the final sweep removes the row and must fail")
	}
	if r.jobExists(id) {
		t.Fatal("the finished job survived the final sweep")
	}
	if s := r.row(r.uid); s.HubSpot || s.Resend || s.Fin {
		t.Fatalf("vendor stamps after attempt 2 = %+v", s)
	}
	if err := r.work(gen); err != nil {
		t.Fatalf("attempt 3: %v", err)
	}
	if s := r.row(r.uid); !s.Fin || len(r.hs.got()) != 3 {
		t.Fatalf("row = %+v, hubspot calls = %d", s, len(r.hs.got()))
	}
}

func TestErase_RunningIntakeJobIsMarkedNotDeleted(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	id := r.insertJob(RegistrantArgs{UserID: r.uid, Email: "x-" + r.mail})
	r.setState(id, "running")
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'running' AND metadata ? 'cancel_attempted_at'`, id) != 1 {
		t.Fatal("the running intake job is not marked")
	}
}

func TestErase_ReRequestDuringARunIsExecuted(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	newMail := "new-" + r.mail
	r.track(newMail, r.uid)
	r.hs.fn = func(n int, _ string) error {
		if n == 1 {
			r.enqueue(r.uid, newMail)
		}
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatalf("job 1: %v", err)
	}
	if s := r.row(r.uid); s.Gen != 2 || s.HubSpot || s.Resend || s.Fin {
		t.Fatalf("row = %+v", s)
	}
	warns := r.sink.at(slog.LevelWarn)
	if len(warns) != 1 || warns[0].Text != "erasure: re-requested during the run; the newer job runs it" {
		t.Fatalf("warns = %+v", warns)
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' = $1 AND (args->>'generation')::int = 2`, r.uid) != 1 {
		t.Fatal("no job for generation 2")
	}
	if err := r.work(2); err != nil {
		t.Fatalf("job 2: %v", err)
	}
	if s := r.row(r.uid); !s.Fin || s.Email != nil {
		t.Fatalf("row = %+v", s)
	}
	if got := r.hs.got(); len(got) != 2 || got[1] != newMail {
		t.Fatalf("hubspot calls = %v", got)
	}
	if got := r.rs.got(); len(got) != 1 || got[0] != newMail {
		t.Fatalf("resend calls = %v", got)
	}
}

func TestErase_StaleGenerationWritesNothing(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.exec(`UPDATE person_erasures SET local_erased_at = now(), account_erased_at = now(), hubspot_erased_at = now(), resend_erased_at = now() WHERE user_id = $1`, r.uid)
	ctx := context.Background()
	lock, err := r.e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:hubspot:' || $1::text, 0))`, r.mail); err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { res <- r.work(gen) }()
	time.Sleep(300 * time.Millisecond)
	r.exec(`UPDATE person_erasures SET generation = 2 WHERE user_id = $1`, r.uid)
	_ = lock.Commit(ctx)
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("Work = %v", err)
		}
	case <-time.After(qaHoldFor):
		t.Fatal("Work never returned")
	}
	if s := r.row(r.uid); s.Fin || s.Email == nil {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_ReRequestAfterTheFinishGetsItsOwnJob(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now(), attempted_by = ARRAY['t'] WHERE kind = 'person_erase' AND args->>'user_id' = $1`, r.uid)
	newMail := "new-" + r.mail
	r.track(newMail, r.uid)
	gen2 := r.enqueue(r.uid, newMail)
	if gen2 != 2 || r.count(`SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND state = 'available' AND args->>'user_id' = $1 AND (args->>'generation')::int = 2`, r.uid) != 1 {
		t.Fatalf("generation %d without its own available job", gen2)
	}
	if err := r.work(gen2); err != nil {
		t.Fatal(err)
	}
	if s := r.row(r.uid); !s.Fin || s.Email != nil {
		t.Fatalf("row = %+v", s)
	}
	for name, c := range map[string]*recErase{"hubspot": r.hs, "resend": r.rs} {
		if got := c.got(); len(got) != 2 || got[1] != newMail {
			t.Errorf("%s calls = %v", name, got)
		}
	}
	if got := r.acct.got(); len(got) != 2 {
		t.Errorf("account calls = %v", got)
	}
}

func TestErase_AnOlderJobReturnsWithoutACall(t *testing.T) {
	r := newEraseRig(t)
	r.start()
	r.start()
	key := func() string {
		s := r.row(r.uid)
		return fmt.Sprintf("%s %+v", *s.Email, s.Gen) + fmt.Sprint(s.Local, s.Account, s.HubSpot, s.Resend, s.Fin)
	}
	before := key()
	if err := r.work(1); err != nil {
		t.Fatal(err)
	}
	if len(r.hs.got())+len(r.rs.got())+len(r.acct.got()) != 0 || key() != before {
		t.Fatal("an older job acted")
	}
}

func TestErase_ANewAccountKeepsItsLocalRows(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.exec(`UPDATE person_erasures SET account_erased_at = now() WHERE user_id = $1`, r.uid)
	buid := uuid.NewString()
	r.contact(r.mail, &buid, 1)
	r.handoff(buid, r.mail)
	r.handoff(r.uid, r.mail)
	r.blog(r.mail, 1)
	job := r.insertJob(DeliverArgs{Email: r.mail, Destination: "hubspot", Version: 9})
	intake := r.insertJob(RegistrantArgs{UserID: r.uid, Email: r.mail})
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if got := r.hs.got(); len(got) != 1 || got[0] != r.mail {
		t.Fatalf("hubspot calls = %v", got)
	}
	if got := r.rs.got(); len(got) != 1 || got[0] != r.mail {
		t.Fatalf("resend calls = %v", got)
	}
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1 AND user_id = $2::uuid`, r.mail, buid) != 1 ||
		r.count(`SELECT count(*) FROM registrant_handoffs WHERE user_id = $1`, buid) != 1 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1`, r.mail) != 1 || !r.jobExists(job) {
		t.Fatal("the new account lost a row or job")
	}
	if r.count(`SELECT count(*) FROM registrant_handoffs WHERE user_id = $1`, r.uid) != 0 || r.jobExists(intake) {
		t.Fatal("the person's own hand-off or intake job is left")
	}
	if s := r.row(r.uid); !s.Fin || !s.Superseded || s.Email != nil {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_ADemoRowWithoutAnAccountDoesNotSupersede(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.hs.fn = func(n int, _ string) error {
		if n == 1 {
			r.exec(`INSERT INTO contacts (email, demo_requested_at) VALUES ($1, now())`, r.mail)
		}
		return nil
	}
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if s := r.row(r.uid); s.HubSpot || s.Resend || s.Fin {
		t.Fatalf("row = %+v", s)
	}
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 0 {
		t.Fatal("the demo row survived")
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if s := r.row(r.uid); !s.Fin || s.Superseded {
		t.Fatalf("row = %+v", s)
	}
}

// rebuilt runs an erase during which another account registers the address.
func rebuilt(t *testing.T) (*eraseRig, string) {
	return rebuiltWith(t, ResendState{})
}

// rebuiltWith also gives the Resend contact the state the erase finds. The new account ticked
// marketing and its opt_in is recorded as sent.
func rebuiltWith(t *testing.T, st ResendState) (*eraseRig, string) {
	r := newEraseRig(t)
	r.rs.state = st
	gen := r.start()
	buid := uuid.NewString()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at, marketing_consented_at, marketing_consent_text, version, hubspot_delivered_at, resend_delivered_at, resend_opt_in_sent_at)
			VALUES ($1, $2::uuid, 'Bola', 'Eze', now(), now(), 'ok', 3, now(), now(), now())`, r.mail, buid)
		r.blog(r.mail, 2)
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	return r, buid
}

func TestErase_ANewAccountIsRebuiltAfterBothVendorSteps(t *testing.T) {
	r, buid := rebuilt(t)
	if len(r.hs.got()) != 1 || len(r.rs.got()) != 1 {
		t.Fatal("the vendors were not erased")
	}
	if r.count(`SELECT count(*) FROM contacts WHERE user_id = $1::uuid AND hubspot_delivered_at IS NULL AND resend_delivered_at IS NULL AND version = 3`, buid) != 1 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1 AND resend_delivered_at IS NULL AND version = 2`, r.mail) != 1 {
		t.Fatal("the stamps were not cleared with versions kept")
	}
	q := `SELECT count(*) FROM river_job WHERE state = 'available' AND kind = $1 AND args->>'email' = $2 AND args->>'destination' IS NOT DISTINCT FROM $3 AND (args->>'version')::int = $4`
	if r.count(q, "contact_deliver", r.mail, "hubspot", 3) != 1 || r.count(q, "contact_deliver", r.mail, "resend", 3) != 1 ||
		r.count(q, "blog_subscriber_sync", r.mail, nil, 2) != 1 {
		t.Fatal("rebuild jobs missing")
	}
	n := 0
	for _, l := range r.sink.at(slog.LevelWarn) {
		if l.Text == rebuildWarn {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("rebuild warnings = %d", n)
	}
}

func TestErase_TheRebuildSends(t *testing.T) {
	r, buid := rebuilt(t)
	hs, rs, bl := &qaRecHubSpot{}, &qaRecResend{}, &recBlog{}
	dw := &DeliverWorker{Pool: r.e.app, HubSpot: hs, Resend: rs, Mode: ModeReal, Logger: r.sink.logger()}
	bw := &BlogSyncWorker{Pool: r.e.app, Resend: bl, Mode: ModeReal, Logger: r.sink.logger()}
	ctx := context.Background()
	for _, d := range []string{"hubspot", "resend"} {
		if err := dw.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{Email: r.mail, Destination: d, Version: 3}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Work(ctx, &river.Job[BlogSyncArgs]{Args: BlogSyncArgs{Email: r.mail, Version: 2}}); err != nil {
		t.Fatal(err)
	}
	if h := hs.got(); len(h) != 1 || h[0].FirstName != "Bola" || h[0].LastName != "Eze" {
		t.Fatalf("hubspot = %+v", h)
	}
	if s := rs.got(); len(s) != 1 || s[0].C.FirstName != "Bola" || !s[0].OptIn {
		t.Fatalf("resend = %+v", s)
	}
	if bl.calls != 1 {
		t.Fatalf("blog syncs = %d", bl.calls)
	}
	if r.count(`SELECT count(*) FROM contacts WHERE user_id = $1::uuid AND hubspot_delivered_at IS NOT NULL AND resend_delivered_at IS NOT NULL`, buid) != 1 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1 AND resend_delivered_at IS NOT NULL`, r.mail) != 1 {
		t.Fatal("stamps not set again")
	}
}

func TestErase_ADeliveryDuringTheVendorCallIsNotLost(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	buid := uuid.NewString()
	r.contact(r.mail, &buid, 1)
	hs := &qaRecHubSpot{}
	dw := &DeliverWorker{Pool: r.e.app, HubSpot: hs, Resend: &qaRecResend{}, Mode: ModeReal, Logger: r.sink.logger()}
	delivered := make(chan error, 1)
	r.hs.fn = func(n int, _ string) error {
		if n != 1 {
			return nil
		}
		go func() {
			delivered <- dw.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{Email: r.mail, Destination: "hubspot", Version: 1}})
		}()
		time.Sleep(500 * time.Millisecond)
		if len(hs.got()) != 0 {
			t.Error("the delivery ran during the erase call")
		}
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(qaHoldFor):
		t.Fatal("the delivery never ran")
	}
	if len(hs.got()) != 1 || r.count(`SELECT count(*) FROM contacts WHERE email = $1 AND hubspot_delivered_at IS NOT NULL`, r.mail) != 1 {
		t.Fatalf("upserts = %d", len(hs.got()))
	}
	if err := dw.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{Email: r.mail, Destination: "hubspot", Version: 1}}); err != nil || len(hs.got()) != 1 {
		t.Fatalf("the rebuild job made a call: %v", err)
	}
}

func TestErase_NoRebuildWithoutAnotherAccount(t *testing.T) {
	for _, demoRow := range []bool{false, true} {
		t.Run(fmt.Sprintf("demo_row=%v", demoRow), func(t *testing.T) {
			r := newEraseRig(t)
			gen := r.start()
			r.hs.fn = func(n int, _ string) error {
				if n == 1 && demoRow {
					r.exec(`INSERT INTO contacts (email, demo_requested_at) VALUES ($1, now())`, r.mail)
				}
				return nil
			}
			if err := r.work(gen); (err != nil) != demoRow {
				t.Fatalf("Work = %v with demo_row=%v", err, demoRow)
			}
			if n := r.count(`SELECT count(*) FROM river_job WHERE kind IN ('contact_deliver', 'blog_subscriber_sync') AND args->>'email' = $1`, r.mail); n != 0 {
				t.Fatalf("%d jobs queued", n)
			}
			for _, l := range r.sink.snapshot() {
				if l.Text == rebuildWarn {
					t.Fatal("rebuild warning logged")
				}
			}
		})
	}
}

func TestErase_AVendorStepHoldsItsLock(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	free := func(dest string) bool {
		var ok bool
		err := pgx.BeginFunc(context.Background(), r.e.admin, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT pg_try_advisory_xact_lock(hashtextextended('contact_deliver:' || $1::text || ':' || $2::text, 0))`, dest, r.mail).Scan(&ok)
		})
		if err != nil {
			t.Error(err)
		}
		return ok
	}
	r.hs.fn = func(int, string) error {
		if free("hubspot") {
			t.Error("hubspot lock free during the call")
		}
		return nil
	}
	r.rs.fn = func(int, string) error {
		if free("resend") {
			t.Error("resend lock free during the call")
		}
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if !free("hubspot") || !free("resend") {
		t.Fatal("a lock outlived the erase")
	}
}

func TestErase_SweepDeletesTheFinishedJobsOfThePerson(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	ouid, omail := r.person()
	var mine, others []int64
	for _, st := range []string{"completed", "cancelled", "discarded"} {
		for _, id := range r.fourJobs(r.uid, r.mail) {
			r.setState(id, st)
			mine = append(mine, id)
		}
		for _, id := range r.fourJobs(ouid, omail) {
			r.setState(id, st)
			others = append(others, id)
		}
	}
	r.failAll()
	_ = r.work(gen)
	for _, id := range mine {
		if r.jobExists(id) {
			t.Errorf("finished job %d is left", id)
		}
	}
	for _, id := range others {
		if !r.jobExists(id) {
			t.Errorf("finished job %d of another person was deleted", id)
		}
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' = $1`, r.uid) != 1 {
		t.Fatal("the erase job row was touched")
	}
}

func TestErase_SweepRunsBeforeAnyCall(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.contact(r.mail, &r.uid, 1)
	r.insertJob(DemoDealArgs{Email: r.mail})
	r.acct.fn = func(n int, _ string) error {
		if n != 1 {
			return nil
		}
		if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 0 ||
			r.count(`SELECT count(*) FROM river_job WHERE kind = 'demo_deal' AND args->>'email' = $1 AND state NOT IN ('completed', 'cancelled', 'discarded')`, r.mail) != 0 ||
			!r.row(r.uid).Local {
			t.Error("the sweep had not committed at the first account call")
		}
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
}

func testSweepWaits(t *testing.T, dest string) {
	r := newEraseRig(t)
	gen := r.start()
	r.contact(r.mail, &r.uid, 1)
	ctx := context.Background()
	lock, err := r.e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:' || $1::text || ':' || $2::text, 0))`, dest, r.mail); err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { res <- r.work(gen) }()
	time.Sleep(400 * time.Millisecond)
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 1 {
		t.Fatal("the sweep deleted a row while the lock was held")
	}
	if len(r.acct.got()) != 0 {
		t.Fatal("a call ran while the lock was held")
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-res:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(qaHoldFor):
		t.Fatal("the sweep never completed")
	}
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 0 {
		t.Fatal("the sweep left the row")
	}
}

func TestErase_SweepWaitsForTheHubSpotLock(t *testing.T) { testSweepWaits(t, "hubspot") }
func TestErase_SweepWaitsForTheResendLock(t *testing.T)  { testSweepWaits(t, "resend") }

func TestErase_DeliveryAfterTheSweepMakesNoCall(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.contact(r.mail, &r.uid, 1)
	r.failAll()
	_ = r.work(gen)
	hs := &qaRecHubSpot{}
	dw := &DeliverWorker{Pool: r.e.app, HubSpot: hs, Resend: &qaRecResend{}, Mode: ModeReal, Logger: r.sink.logger()}
	if err := dw.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{Email: r.mail, Destination: "hubspot", Version: 1}}); err != nil || len(hs.got()) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(hs.got()))
	}
}

func TestErase_CallsEveryStepOnceAndFinishes(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if got := r.acct.got(); len(got) != 1 || got[0] != r.uid {
		t.Fatalf("account calls = %v", got)
	}
	if len(r.hs.got()) != 1 || r.hs.got()[0] != r.mail || len(r.rs.got()) != 1 || r.rs.got()[0] != r.mail {
		t.Fatal("vendor calls wrong")
	}
	s := r.row(r.uid)
	if !s.Local || !s.Account || !s.HubSpot || !s.Resend || !s.Fin || s.Email != nil || s.Mode == nil || *s.Mode != "real" {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_RerunAfterAPartialFailureFinishes(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.hs.fn = func(n int, _ string) error {
		if n == 1 {
			return &DeliveryError{Status: 503}
		}
		return nil
	}
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if s := r.row(r.uid); !s.Account || !s.Resend || s.HubSpot || s.Fin {
		t.Fatalf("row = %+v", s)
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if len(r.hs.got()) != 2 || len(r.rs.got()) != 1 || len(r.acct.got()) != 1 || !r.row(r.uid).Fin {
		t.Fatal("the rerun repeated a finished step")
	}
}

func TestErase_EveryStepIsTriedWhenOneFails(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	boom := &gotrueadmin.StatusError{Status: 500}
	r.acct.fn = func(int, string) error { return boom }
	err := r.work(gen)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if s := r.row(r.uid); s.Account || !s.HubSpot || !s.Resend || len(r.hs.got()) != 1 || len(r.rs.got()) != 1 {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_AFailedSweepCallsNothing(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.contact(r.mail, &r.uid, 1)
	ctx := context.Background()
	lock, err := r.e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:hubspot:' || $1::text, 0))`, r.mail); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if err := r.workCtx(cctx, gen); err == nil {
		t.Fatal("want an error")
	}
	if len(r.acct.got())+len(r.hs.got())+len(r.rs.got()) != 0 || r.row(r.uid).Local ||
		r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 1 {
		t.Fatal("a failed sweep changed state or called out")
	}
}

func TestErase_ACopyThatCameBackRedoesTheVendors(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	var job int64
	r.rs.fn = func(n int, _ string) error {
		if n == 1 {
			r.exec(`INSERT INTO contacts (email, demo_requested_at) VALUES ($1, now())`, r.mail)
			job = r.insertJob(DeliverArgs{Email: r.mail, Destination: "hubspot", Version: 1})
		}
		return nil
	}
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 0 || r.jobExists(job) {
		t.Fatal("the copy survived")
	}
	if s := r.row(r.uid); s.HubSpot || s.Resend || s.Fin {
		t.Fatalf("row = %+v", s)
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if len(r.hs.got()) != 2 || len(r.rs.got()) != 2 || !r.row(r.uid).Fin {
		t.Fatal("the vendors were not run again")
	}
}

func TestErase_FinishedRowIsANoOp(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if err := r.work(gen); err != nil || len(r.hs.got()) != 1 || len(r.acct.got()) != 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestErase_MissingRowCancelsTheJob(t *testing.T) {
	r := newEraseRig(t)
	var jc *rivertype.JobCancelError
	if err := r.work(1); !errors.As(err, &jc) {
		t.Fatalf("err = %v", err)
	}
}

func TestErase_OffModeNeverStampsAVendor(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.w.HubSpot, r.w.Resend = nil, nil
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if s := r.row(r.uid); s.HubSpot || s.Resend || !s.Local || !s.Account || s.Fin {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_NoAccountClientNeverStampsTheAccount(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.w.Account = nil
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	if s := r.row(r.uid); s.Account || !s.HubSpot || !s.Resend || s.Fin {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_FakeModeRecordsFake(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.w.HubSpot, r.w.Resend, r.w.Mode = FakeHubSpot{}, FakeResend{}, ModeFake
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if s := r.row(r.uid); !s.Fin || s.Mode == nil || *s.Mode != "fake" {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_PermanentRejectionLogsError(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.hs.fn = func(int, string) error { return &DeliveryError{Status: 403} }
	if err := r.work(gen); err == nil {
		t.Fatal("want an error")
	}
	errs := r.sink.at(slog.LevelError)
	if len(errs) != 1 || errs[0].Text != "erasure: hubspot rejected the erase step=hubspot status=403" {
		t.Fatalf("errors = %+v", errs)
	}
}

func TestErase_TransientFailureLogsWarn(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.rs.fn = func(int, string) error { return &DeliveryError{} }
	r.acct.fn = func(int, string) error { return &gotrueadmin.StatusError{Status: 503} }
	var se *gotrueadmin.StatusError
	if err := r.work(gen); !errors.As(err, &se) {
		t.Fatalf("want the first failure (account), got %v", err)
	}
	var texts []string
	for _, l := range r.sink.at(slog.LevelWarn) {
		texts = append(texts, l.Text)
	}
	want := []string{"erasure: account failed step=account status=503", "erasure: resend failed step=resend status=0"}
	if strings.Join(texts, "|") != strings.Join(want, "|") || len(r.sink.at(slog.LevelError)) != 0 {
		t.Fatalf("warns = %v", texts)
	}
}

func TestErase_LogsAndErrorsCarryNoPersonalData(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.contact(r.mail, &r.uid, 1)
	r.insertJob(RegistrantArgs{UserID: r.uid, Email: r.mail, DisplayName: "Adaeze Obi"})
	fail := func(status int) func(int, string) error {
		return func(n int, _ string) error {
			if n == 1 {
				return &DeliveryError{Status: status}
			}
			return nil
		}
	}
	r.hs.fn, r.rs.fn = fail(403), fail(503)
	r.acct.fn = func(n int, _ string) error {
		if n == 1 {
			return &gotrueadmin.StatusError{Status: 500}
		}
		return nil
	}
	var errs []error
	for i := 0; i < 2; i++ {
		errs = append(errs, r.work(gen))
	}
	if errs[0] == nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	needles := []string{r.mail, strings.Split(r.mail, "@")[0], "Adaeze", "Obi", r.uid}
	for _, err := range errs {
		qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, needles...)
	}
}

func TestEraseArgs_InsertOpts(t *testing.T) {
	o := EraseArgs{}.InsertOpts()
	u := o.UniqueOpts
	if o.Queue != "" || u.ByArgs || u.ByPeriod != 0 || u.ByQueue || u.ByState != nil {
		t.Fatalf("opts = %+v", o)
	}
	if d := (&EraseWorker{}).Timeout(nil); d != 3*time.Minute {
		t.Fatalf("timeout = %v", d)
	}
}

func TestErase_WorkCanonicalisesTheUserID(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.uid = strings.ToUpper(r.uid)
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if got := r.acct.got(); len(got) != 1 || got[0] != strings.ToLower(r.uid) {
		t.Fatalf("account calls = %v", got)
	}
	if s := r.row(strings.ToLower(r.uid)); !s.Fin {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_SweepMatchesAnOddlyWrittenEmail(t *testing.T) {
	for _, mail := range []string{"élodie+news@corp.example", "o'brien@corp.example"} {
		t.Run(mail, func(t *testing.T) {
			r := newEraseRig(t)
			r.mail = mail
			r.track(mail, r.uid)
			gen := r.enqueue(r.uid, " "+strings.ToUpper(mail)+"\t")
			padded := r.insertJob(DeliverArgs{Email: " " + strings.ToUpper(mail) + " ", Destination: "hubspot", Version: 1})
			upper := r.insertJob(BlogSyncArgs{Email: strings.ToUpper(mail), Version: 1})
			r.failAll()
			_ = r.work(gen)
			if r.jobExists(upper) || r.jobExists(padded) {
				t.Error("a job that carries the address is left")
			}
		})
	}
}

func TestErase_SweepCancelsEveryNonFinalState(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	var ids []int64
	for _, st := range []string{"available", "pending", "retryable", "scheduled"} {
		id := r.insertJob(BlogSyncArgs{Email: r.mail, Version: int64(len(ids) + 1)})
		if st != "available" {
			r.exec(`UPDATE river_job SET state = $2::river_job_state, scheduled_at = now() + interval '1 hour' WHERE id = $1`, id, st)
		}
		ids = append(ids, id)
	}
	r.failAll()
	_ = r.work(gen)
	for _, id := range ids {
		if r.jobExists(id) {
			t.Errorf("job %d is left", id)
		}
	}
}

func TestErase_EachSignOfAnotherAccountSupersedes(t *testing.T) {
	other := uuid.NewString()
	for name, put := range map[string]func(r *eraseRig){
		"contacts row": func(r *eraseRig) { r.contact(r.mail, &other, 1) },
		"hand-off":     func(r *eraseRig) { r.handoff(other, r.mail) },
		"intake job":   func(r *eraseRig) { r.insertJob(RegistrantArgs{UserID: other, Email: r.mail}) },
		"running intake job": func(r *eraseRig) {
			r.setState(r.insertJob(RegistrantArgs{UserID: other, Email: r.mail}), "running")
		},
		"upper-case id": func(r *eraseRig) {
			r.insertJob(RegistrantArgs{UserID: strings.ToUpper(other), Email: "  " + strings.ToUpper(r.mail)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newEraseRig(t)
			gen := r.start()
			put(r)
			r.blog(r.mail, 1)
			emailJob := r.insertJob(DemoDealArgs{Email: r.mail})
			if err := r.work(gen); err != nil {
				t.Fatal(err)
			}
			if s := r.row(r.uid); !s.Fin || !s.Superseded {
				t.Fatalf("row = %+v", s)
			}
			if r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1`, r.mail) != 1 || !r.jobExists(emailJob) {
				t.Fatal("an email-keyed row or job of the other account was removed")
			}
		})
	}
}

func TestLogEraseFailure_Levels(t *testing.T) {
	for _, c := range []struct {
		status int
		level  slog.Level
	}{
		{0, slog.LevelWarn}, {399, slog.LevelWarn}, {400, slog.LevelError}, {403, slog.LevelError}, {408, slog.LevelWarn},
		{429, slog.LevelWarn}, {499, slog.LevelError}, {500, slog.LevelWarn}, {503, slog.LevelWarn},
	} {
		for name, err := range map[string]error{
			"vendor":  &DeliveryError{Status: c.status},
			"account": &gotrueadmin.StatusError{Status: c.status},
			"wrapped": fmt.Errorf("x: %w", &DeliveryError{Status: c.status}),
		} {
			var sink qaLogSink
			logEraseFailure(context.Background(), sink.logger(), "hubspot", err)
			ls := sink.snapshot()
			if len(ls) != 1 || ls[0].Level != c.level || !strings.HasSuffix(ls[0].Text, fmt.Sprintf("step=hubspot status=%d", c.status)) {
				t.Errorf("%s %d: lines = %+v", name, c.status, ls)
			}
		}
	}
}

func TestErase_EveryLogPathCarriesNoPersonalData(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	buid := uuid.NewString()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at) VALUES ($1, $2::uuid, 'Adaeze', 'Obi', now())`, r.mail, buid)
		r.blog(r.mail, 1)
		return nil
	}
	errs := []error{r.work(gen)}
	r.w.HubSpot, r.w.Resend, r.w.Account = nil, nil, nil
	r.exec(`UPDATE person_erasures SET account_erased_at = NULL, hubspot_erased_at = NULL, resend_erased_at = NULL, vendor_mode = NULL, superseded_at = NULL, finished_at = NULL, email = $2 WHERE user_id = $1`, r.uid, r.mail)
	errs = append(errs, r.work(gen))
	if len(r.sink.at(slog.LevelWarn)) < 4 {
		t.Fatalf("warn lines = %d; the paths did not run", len(r.sink.at(slog.LevelWarn)))
	}
	for _, err := range errs {
		qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.mail, strings.Split(r.mail, "@")[0], "Adaeze", "Obi", r.uid, buid)
	}
}

func TestErase_ACameBackLogCarriesNoPersonalData(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.rs.fn = func(n int, _ string) error {
		if n == 1 {
			r.exec(`INSERT INTO contacts (email, demo_requested_at, first_name, last_name) VALUES ($1, now(), 'Adaeze', 'Obi')`, r.mail)
		}
		return nil
	}
	err := r.work(gen)
	if err == nil || len(r.sink.at(slog.LevelWarn)) == 0 {
		t.Fatalf("err = %v, warns = %d", err, len(r.sink.at(slog.LevelWarn)))
	}
	qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.mail, strings.Split(r.mail, "@")[0], "Adaeze", "Obi", r.uid)
}

func TestErase_EachStepsErrorCarriesNoPersonalData(t *testing.T) {
	for _, step := range []string{"account", "hubspot", "resend"} {
		t.Run(step, func(t *testing.T) {
			r := newEraseRig(t)
			gen := r.start()
			bad := map[string]*recErase{"account": r.acct, "hubspot": r.hs, "resend": r.rs}[step]
			bad.fn = func(int, string) error { return &DeliveryError{Status: 503} }
			err := r.work(gen)
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v", err)
			}
			qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.mail, strings.Split(r.mail, "@")[0], r.uid)
		})
	}
}

func TestErase_TheCasingOfTheOwnUserIDDoesNotSupersede(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.insertJob(RegistrantArgs{UserID: strings.ToUpper(r.uid), Email: r.mail})
	r.blog(r.mail, 1)
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if s := r.row(r.uid); !s.Fin || s.Superseded || r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1`, r.mail) != 0 {
		t.Fatalf("row = %+v", s)
	}
}

func TestErase_TheHubSpotStepRebuildsOnlyHubSpot(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.exec(`UPDATE person_erasures SET resend_erased_at = now() WHERE user_id = $1`, r.uid)
	buid := uuid.NewString()
	r.hs.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at, hubspot_delivered_at, resend_delivered_at) VALUES ($1, $2::uuid, 'Bola', 'Eze', now(), now(), now())`, r.mail, buid)
		r.blog(r.mail, 1)
		return nil
	}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'contact_deliver' AND args->>'destination' = 'hubspot' AND args->>'email' = $1`, r.mail) != 1 ||
		r.count(`SELECT count(*) FROM river_job WHERE kind IN ('blog_subscriber_sync') AND args->>'email' = $1`, r.mail) != 0 ||
		r.count(`SELECT count(*) FROM river_job WHERE kind = 'contact_deliver' AND args->>'destination' = 'resend' AND args->>'email' = $1`, r.mail) != 0 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1 AND resend_delivered_at IS NOT NULL`, r.mail) != 1 {
		t.Fatal("the HubSpot step rebuilt more than the HubSpot contact")
	}
}

func TestErase_AFinishedIntakeJobOfAnotherUserDoesNotSupersede(t *testing.T) {
	for _, st := range []string{"completed", "cancelled", "discarded"} {
		t.Run(st, func(t *testing.T) {
			r := newEraseRig(t)
			gen := r.start()
			r.setState(r.insertJob(RegistrantArgs{UserID: uuid.NewString(), Email: r.mail}), st)
			r.blog(r.mail, 1)
			if err := r.work(gen); err != nil {
				t.Fatal(err)
			}
			if s := r.row(r.uid); !s.Fin || s.Superseded || r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1`, r.mail) != 0 {
				t.Fatalf("row = %+v", s)
			}
		})
	}
}

func TestErase_AHandoffOnlyAccountKeepsItsBlogSubscription(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.handoff(uuid.NewString(), r.mail)
	r.blog(r.mail, 4)
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'blog_subscriber_sync' AND state = 'available' AND args->>'email' = $1 AND (args->>'version')::int = 4`, r.mail) != 1 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1 AND version = 4 AND resend_delivered_at IS NULL`, r.mail) != 1 {
		t.Fatal("the blog subscription was not rebuilt at its version")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'contact_deliver' AND args->>'email' = $1`, r.mail) != 0 {
		t.Fatal("a contact_deliver was queued without a contacts row")
	}
	n := 0
	for _, l := range r.sink.at(slog.LevelWarn) {
		if l.Text == rebuildWarn {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rebuild warnings = %d", n)
	}
}

func TestErase_ABlogRowWithoutAnotherAccountIsNotRebuilt(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.rs.fn = func(n int, _ string) error {
		if n == 1 {
			r.blog(r.mail, 1)
		}
		return nil
	}
	if err := r.work(gen); err == nil {
		t.Fatal("want the came-back error")
	}
	for _, l := range r.sink.snapshot() {
		if l.Text == rebuildWarn {
			t.Fatal("rebuild warning logged without another account")
		}
	}
}

func (r *eraseRig) optInSent(buid string) bool {
	r.t.Helper()
	return r.count(`SELECT count(*) FROM contacts WHERE user_id = $1::uuid AND resend_opt_in_sent_at IS NOT NULL`, buid) == 1
}

func (r *eraseRig) blogRebuilt() bool {
	r.t.Helper()
	return r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1 AND resend_delivered_at IS NULL`, r.mail) == 1
}

const (
	topicMarketing = "topic-marketing"
	topicBlog      = "topic-blog"
)

func TestErase_TheRebuildReadsTheStateBeforeTheDeleteAndRestoresIt(t *testing.T) {
	st := ResendState{Unsubscribed: true, MarketingOut: true, OptedOut: []string{topicMarketing, topicBlog}}
	r, _ := rebuiltWith(t, st)
	if got := strings.Join(r.rs.events, ","); got != "state,erase,restore" {
		t.Fatalf("resend events = %s, want state,erase,restore", got)
	}
	if len(r.rs.restored) != 1 {
		t.Fatalf("restores = %d", len(r.rs.restored))
	}
	c := r.rs.restored[0]
	if c.email != r.mail || c.first != "Bola" || c.last != "Eze" || !c.st.Unsubscribed || len(c.st.OptedOut) != 2 {
		t.Fatalf("restore = %+v", c)
	}
}

func TestErase_AnUnsubscribedContactStaysUnsubscribed(t *testing.T) {
	r, buid := rebuiltWith(t, ResendState{Unsubscribed: true})
	if !r.optInSent(buid) {
		t.Error("the opt_in stamp was cleared for an unsubscribed contact")
	}
	if r.blogRebuilt() {
		t.Error("the blog subscription was re-queued for an unsubscribed contact")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'blog_subscriber_sync' AND state = 'available' AND args->>'email' = $1`, r.mail) != 0 {
		t.Error("a blog sync job was queued")
	}
}

func TestErase_AMarketingOptOutStaysOptedOut(t *testing.T) {
	r, buid := rebuiltWith(t, ResendState{MarketingOut: true, OptedOut: []string{topicMarketing}})
	if !r.optInSent(buid) {
		t.Error("the opt_in stamp was cleared for a Marketing opt-out")
	}
	if !r.blogRebuilt() {
		t.Error("a Marketing opt-out stopped the blog rebuild")
	}
	if len(r.rs.restored) != 1 || r.rs.restored[0].st.Unsubscribed {
		t.Errorf("restores = %+v", r.rs.restored)
	}
}

func TestErase_ABlogOptOutIsNotRebuilt(t *testing.T) {
	r, buid := rebuiltWith(t, ResendState{OptedOut: []string{topicBlog}})
	if r.blogRebuilt() {
		t.Error("the blog subscription was re-queued over a Blog opt-out")
	}
	if r.optInSent(buid) {
		t.Error("a Blog opt-out kept the Marketing opt_in stamp")
	}
	if len(r.rs.restored) != 1 || len(r.rs.restored[0].st.OptedOut) != 1 {
		t.Errorf("restores = %+v", r.rs.restored)
	}
}

func TestErase_ACleanContactGetsItsMarketingOptInBack(t *testing.T) {
	r, buid := rebuilt(t)
	if len(r.rs.restored) != 0 {
		t.Errorf("restored a clean contact: %+v", r.rs.restored)
	}
	if r.optInSent(buid) {
		t.Fatal("the opt_in stamp survived the erase of the contact")
	}
	rs := &qaRecResend{}
	dw := &DeliverWorker{Pool: r.e.app, HubSpot: &qaRecHubSpot{}, Resend: rs, Mode: ModeReal, Logger: r.sink.logger()}
	if err := dw.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{Email: r.mail, Destination: "resend", Version: 3}}); err != nil {
		t.Fatal(err)
	}
	if got := rs.got(); len(got) != 1 || !got[0].OptIn {
		t.Fatalf("resend = %+v, want one sync with the opt_in", got)
	}
}

func TestErase_NothingIsRestoredWhenNoOtherAccountHoldsTheAddress(t *testing.T) {
	r := newEraseRig(t)
	r.rs.state = ResendState{Unsubscribed: true}
	gen := r.start()
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if len(r.rs.restored) != 0 {
		t.Fatalf("restores = %+v, want none", r.rs.restored)
	}
}

func TestErase_SweepDeletesTheFinishedEmailKeyedJobsEvenWhenAnotherAccountHoldsTheAddress(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.exec(`UPDATE person_erasures SET account_erased_at = now() WHERE user_id = $1`, r.uid)
	r.contact(r.mail, ptr(uuid.NewString()), 1)
	var finished []int64
	for _, st := range []string{"completed", "cancelled", "discarded"} {
		for _, id := range []int64{
			r.insertJob(DemoDealArgs{Email: r.mail, Name: "Adaeze Obi", Company: "Obi Ltd"}),
			r.insertJob(DeliverArgs{Email: r.mail, Destination: "resend", Version: 1}),
			r.insertJob(BlogSyncArgs{Email: r.mail, Version: 1}),
			r.insertJob(RegistrantArgs{UserID: uuid.NewString(), Email: r.mail}),
		} {
			r.setState(id, st)
			finished = append(finished, id)
		}
	}
	open := r.insertJob(DemoDealArgs{Email: r.mail, Name: "Bola Eze"})
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	for _, id := range finished {
		if r.jobExists(id) {
			t.Errorf("finished email-keyed job %d is left", id)
		}
	}
	if !r.jobExists(open) {
		t.Error("a non-final email-keyed job of the other account was removed")
	}
	if s := r.row(r.uid); !s.Fin || !s.Superseded {
		t.Fatalf("row = %+v", s)
	}
}

func ptr[T any](v T) *T { return &v }

func TestErase_ARetryAfterAFailedRestoreRestoresUnsubscribed(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	buid := uuid.NewString()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at, marketing_consented_at, marketing_consent_text, version, hubspot_delivered_at, resend_delivered_at, resend_opt_in_sent_at)
			VALUES ($1, $2::uuid, 'Bola', 'Eze', now(), now(), 'ok', 3, now(), now(), now())`, r.mail, buid)
		r.blog(r.mail, 2)
		return nil
	}
	r.rs.restoreErr = errors.New("boom")
	r.rs.state = ResendState{OptedOut: []string{topicBlog}}
	if err := r.work(gen); err == nil {
		t.Fatal("want the restore error")
	}
	r.rs.restoreErr = nil
	r.rs.state = ResendState{Missing: true}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	last := r.rs.restored[len(r.rs.restored)-1]
	if !last.st.Unsubscribed || last.email != r.mail {
		t.Fatalf("restore = %+v, want unsubscribed", last)
	}
	if !r.optInSent(buid) {
		t.Error("the opt_in stamp was cleared")
	}
	if r.blogRebuilt() {
		t.Error("the blog subscription was re-queued")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE kind = 'blog_subscriber_sync' AND state = 'available' AND args->>'email' = $1`, r.mail) != 0 {
		t.Error("a blog sync job was queued")
	}
}

func TestErase_AMissingContactThatWasNeverDeliveredStaysMissing(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, registered_at, version) VALUES ($1, $2::uuid, now(), 1)`, r.mail, uuid.NewString())
		return nil
	}
	r.rs.state = ResendState{Missing: true}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if len(r.rs.restored) != 0 {
		t.Fatalf("restores = %+v, want none", r.rs.restored)
	}
}

func TestErase_AStateReadFailureStopsBeforeTheDelete(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.rs.stateErr = &DeliveryError{Status: 503}
	var de *DeliveryError
	if err := r.work(gen); !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DeliveryError", err)
	}
	if len(r.rs.events) == 0 || strings.Contains(strings.Join(r.rs.events, ","), "erase") {
		t.Fatalf("resend events = %v, want the state read and no erase", r.rs.events)
	}
	if r.row(r.uid).Resend {
		t.Error("the resend step is stamped")
	}
}

func TestErase_AMissingContactWithNoResendDeliveryStampStaysMissing(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, registered_at, version, hubspot_delivered_at, resend_opt_in_sent_at) VALUES ($1, $2::uuid, now(), 1, now(), now())`, r.mail, uuid.NewString())
		return nil
	}
	r.rs.state = ResendState{Missing: true}
	if err := r.work(gen); err != nil {
		t.Fatal(err)
	}
	if len(r.rs.restored) != 0 {
		t.Fatalf("restores = %+v, want none", r.rs.restored)
	}
}

func TestErase_TheFailSafeLogsNoPersonalData(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	r.acct.fn = func(int, string) error {
		r.exec(`INSERT INTO contacts (email, user_id, first_name, last_name, registered_at, version, resend_delivered_at) VALUES ($1, $2::uuid, 'Bola', 'Eze', now(), 1, now())`, r.mail, uuid.NewString())
		return nil
	}
	r.rs.state = ResendState{Missing: true}
	err := r.work(gen)
	if err != nil {
		t.Fatal(err)
	}
	warns := 0
	for _, l := range r.sink.at(slog.LevelWarn) {
		if strings.Contains(l.Text, "state is unknown") {
			warns++
		}
	}
	if warns != 1 || len(r.rs.restored) != 1 || !r.rs.restored[0].st.Unsubscribed {
		t.Fatalf("warns = %d, restores = %+v", warns, r.rs.restored)
	}
	qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.mail, strings.Split(r.mail, "@")[0], "Bola", "Eze", r.uid)
}

// seedAccountEvents gives a person a known pair, an outbox row and a queued job of each account kind.
func (r *eraseRig) seedAccountEvents(uid, email string) (jobs []int64) {
	r.exec(`INSERT INTO account_known_devices (user_id, browser, os) VALUES ($1::uuid, 'Chrome', 'macOS')`, uid)
	r.exec(`INSERT INTO account_event_handoffs (event, user_id, email) VALUES ('account.signed_in', $1::uuid, $2)`, uid, email)
	r.exec(`INSERT INTO account_event_handoffs (event, email) VALUES ('account.sign_in_failed', $1)`, " "+strings.ToUpper(email)+" ")
	jobs = append(jobs, r.insertJob(accountevents.NewDeviceArgs{UserID: uid, Email: email}))
	jobs = append(jobs, r.insertJob(accountevents.AuditArgs{UserID: uid}))
	return jobs
}

func (r *eraseRig) accountRows(uid, email string) (pairs, outbox int) {
	return r.count(`SELECT count(*) FROM account_known_devices WHERE user_id::text = $1`, uid),
		r.count(`SELECT count(*) FROM account_event_handoffs WHERE user_id::text = $1 OR lower(btrim(email)) = $2`, uid, email)
}

func TestErase_SweepDeletesKnownDevicesAndAccountEventRows(t *testing.T) {
	r := newEraseRig(t)
	gen := r.start()
	ouid, omail := r.person()
	mine := r.seedAccountEvents(r.uid, r.mail)
	theirs := r.seedAccountEvents(ouid, omail)
	r.failAll()
	_ = r.work(gen)

	if p, o := r.accountRows(r.uid, r.mail); p != 0 || o != 0 {
		t.Errorf("person's pairs = %d, outbox rows = %d, want 0 and 0", p, o)
	}
	for _, id := range mine {
		if r.jobExists(id) && r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'cancelled'`, id) == 0 {
			t.Errorf("person's job %d is still live", id)
		}
	}
	if p, o := r.accountRows(ouid, omail); p != 1 || o != 2 {
		t.Errorf("another person's pairs = %d, outbox rows = %d, want 1 and 2", p, o)
	}
	for _, id := range theirs {
		if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'available'`, id) != 1 {
			t.Errorf("another person's job %d was touched", id)
		}
	}
}
