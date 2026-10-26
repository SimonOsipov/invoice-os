package notifications

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

const eraseByIDKind = "person_erase_by_id" // EraseByIDArgs.Kind

// byIDWorker is the by-id worker on the rig's pool, with the rig's account recorder.
func (r *eraseRig) byIDWorker() *EraseByIDWorker {
	return &EraseByIDWorker{Pool: r.e.app, River: r.e.store.river, Account: r.acct, Logger: r.sink.logger()}
}

func (r *eraseRig) workByID(w *EraseByIDWorker, uid string) error {
	return w.Work(context.Background(), &river.Job[EraseByIDArgs]{Args: EraseByIDArgs{UserID: uid}})
}

func (r *eraseRig) byIDJobs(uid string) int {
	return r.count(`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'user_id' = $2`, eraseByIDKind, uid)
}

// seedCopies gives a person a hand-off, a contact keyed by user id, a live intake job and a finished one.
func (r *eraseRig) seedCopies(uid, email string) (live, done int64) {
	r.handoff(uid, email)
	r.contact(email, &uid, 1)
	live = r.insertJob(RegistrantArgs{UserID: uid, Email: email})
	done = r.insertJob(RegistrantArgs{UserID: uid, Email: email})
	r.setState(done, "completed")
	return live, done
}

func (r *eraseRig) copies(uid string) (handoffs, contacts int) {
	return r.count(`SELECT count(*) FROM registrant_handoffs WHERE user_id = $1`, uid),
		r.count(`SELECT count(*) FROM contacts WHERE user_id::text = $1`, uid)
}

func TestEnqueueErasureByID_QueuesInTheCallersTx(t *testing.T) {
	r := newEraseRig(t)
	ctx := context.Background()
	enqueue := func(uid string, commit bool) error {
		tx, err := r.e.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = EnqueueErasureByID(ctx, tx, r.e.store.river, uid)
		if err != nil || !commit {
			_ = tx.Rollback(ctx)
			return err
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			t.Fatal(cerr)
		}
		return nil
	}

	if err := enqueue(r.uid, true); err != nil {
		t.Fatalf("EnqueueErasureByID: %v", err)
	}
	if n := r.byIDJobs(r.uid); n != 1 {
		t.Fatalf("jobs after commit = %d, want 1", n)
	}
	if keys := r.count(`SELECT count(*) FROM river_job, jsonb_object_keys(args) k WHERE kind = $1 AND args->>'user_id' = $2 AND k <> 'user_id'`, eraseByIDKind, r.uid); keys != 0 {
		t.Errorf("args hold %d keys besides user_id; the job must carry no email", keys)
	}

	if err := enqueue(r.uid, false); err != nil {
		t.Fatalf("EnqueueErasureByID in a rolled-back tx: %v", err)
	}
	if n := r.byIDJobs(r.uid); n != 1 {
		t.Errorf("jobs after a rolled-back enqueue = %d, want still 1", n)
	}

	// Empty InsertOpts: no uniqueness, every committed call queues its own job.
	if err := enqueue(r.uid, true); err != nil {
		t.Fatal(err)
	}
	if n := r.byIDJobs(r.uid); n != 2 {
		t.Errorf("jobs after a second committed enqueue = %d, want 2", n)
	}

	for _, bad := range []string{"not-a-uuid", ""} {
		if err := enqueue(bad, true); err == nil {
			t.Errorf("EnqueueErasureByID(%q) = nil, want an error", bad)
		}
		if n := r.byIDJobs(bad); n != 0 {
			t.Errorf("jobs for %q = %d, want 0", bad, n)
		}
	}
}

func TestEraseByID_RemovesTheUserIDKeyedCopiesAndTheAccount(t *testing.T) {
	r := newEraseRig(t)
	other, otherMail := r.person()
	live, done := r.seedCopies(r.uid, r.mail)
	otherLive, otherDone := r.seedCopies(other, otherMail)
	running := r.insertJob(RegistrantArgs{UserID: r.uid, Email: r.mail})
	r.setState(running, "running")
	if h, c := r.copies(r.uid); h != 1 || c != 1 {
		t.Fatalf("control: seeded copies = %d hand-offs, %d contacts", h, c)
	}

	if err := r.workByID(r.byIDWorker(), r.uid); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if h, c := r.copies(r.uid); h != 0 || c != 0 {
		t.Errorf("person's copies left: %d hand-offs, %d contacts", h, c)
	}
	if r.jobExists(live) || r.jobExists(done) {
		t.Errorf("person's jobs left: live %v, finished %v", r.jobExists(live), r.jobExists(done))
	}
	// A running job cannot be deleted; cancelling marks it.
	if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'running' AND metadata ? 'cancel_attempted_at'`, running) != 1 {
		t.Error("the running job was not marked cancelled")
	}
	if h, c := r.copies(other); h != 1 || c != 1 {
		t.Errorf("another person's copies = %d hand-offs, %d contacts, want 1 and 1", h, c)
	}
	if !r.jobExists(otherLive) || !r.jobExists(otherDone) {
		t.Error("another person's jobs were removed")
	}
	if r.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state <> 'available'`, otherLive) != 0 {
		t.Error("another person's live job was cancelled")
	}
	if got := r.acct.got(); len(got) != 1 || got[0] != r.uid {
		t.Errorf("account deletes = %v, want exactly the person's id once", got)
	}
}

func TestEraseByID_WritesNoErasureRowAndCallsNoVendor(t *testing.T) {
	r := newEraseRig(t)
	other, _ := r.person()
	r.handoff(r.uid, r.mail)
	// Email-keyed copies: no email-keyed step runs, so these stay.
	r.contact(r.mail, nil, 1)
	r.blog(r.mail, 1)
	byEmail := r.insertJob(RegistrantArgs{UserID: other, Email: r.mail})

	w := r.byIDWorker()
	if err := r.workByID(w, r.uid); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if h, _ := r.copies(r.uid); h != 0 {
		t.Fatal("control: the by-id job did not run its sweep")
	}
	if n := r.count(`SELECT count(*) FROM person_erasures WHERE user_id = $1::uuid`, r.uid); n != 0 {
		t.Errorf("person_erasures rows = %d, want 0", n)
	}
	if r.count(`SELECT count(*) FROM contacts WHERE email = $1`, r.mail) != 1 ||
		r.count(`SELECT count(*) FROM blog_subscribers WHERE email = $1`, r.mail) != 1 || !r.jobExists(byEmail) {
		t.Error("an email-keyed copy was removed")
	}
	// No field can reach a vendor.
	vendors := []reflect.Type{reflect.TypeOf((*HubSpotEraser)(nil)).Elem(), reflect.TypeOf((*ResendEraser)(nil)).Elem()}
	st := reflect.TypeOf(*w)
	for i := 0; i < st.NumField(); i++ {
		for _, v := range vendors {
			if ft := st.Field(i).Type; ft.Implements(v) {
				t.Errorf("EraseByIDWorker.%s implements %v", st.Field(i).Name, v)
			}
		}
	}
}

func TestEraseByID_WaitsWithoutAnAccountClient(t *testing.T) {
	r := newEraseRig(t)
	r.seedCopies(r.uid, r.mail)
	w := r.byIDWorker()
	w.Account = nil

	err := r.workByID(w, r.uid)
	if !errors.Is(err, errAccountOff) {
		t.Fatalf("Work without a client = %v, want errAccountOff", err)
	}
	if h, c := r.copies(r.uid); h != 0 || c != 0 {
		t.Errorf("local copies left: %d hand-offs, %d contacts; step 1 must finish before the wait", h, c)
	}
	warns := r.sink.at(slog.LevelWarn)
	if len(warns) != 1 || !strings.HasSuffix(warns[0].Text, "step="+stepAccount) || strings.Count(warns[0].Text, "=") != 1 {
		t.Errorf("WARN lines = %+v, want one that carries step=%s only", warns, stepAccount)
	}
	if len(r.acct.got()) != 0 {
		t.Fatal("control: the recorder was called before it was set")
	}

	w.Account = r.acct
	if err := r.workByID(w, r.uid); err != nil {
		t.Fatalf("Work with a client = %v", err)
	}
	if got := r.acct.got(); len(got) != 1 || got[0] != r.uid {
		t.Errorf("account deletes = %v, want the person's id once", got)
	}
}

func TestEraseByID_AnAccountFailureLogsTheStatusOnly(t *testing.T) {
	r := newEraseRig(t)
	r.seedCopies(r.uid, r.mail)
	r.acct.fn = func(int, string) error { return &gotrueadmin.StatusError{Status: 500} }

	err := r.workByID(r.byIDWorker(), r.uid)
	var se *gotrueadmin.StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("Work = %v, want the account's *StatusError{500}", err)
	}
	warns := r.sink.at(slog.LevelWarn)
	if len(warns) != 1 || !strings.HasSuffix(warns[0].Text, "step="+stepAccount+" status=500") || strings.Count(warns[0].Text, "=") != 2 {
		t.Fatalf("WARN lines = %+v, want one with exactly step=%s and status=500", warns, stepAccount)
	}
	if n := len(r.sink.at(slog.LevelError)); n != 0 {
		t.Errorf("ERROR lines = %d, want 0 for a 500", n)
	}
	qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.uid, r.mail)
}

func TestEraseByID_ABadIDCancelsTheJob(t *testing.T) {
	r := newEraseRig(t)
	err := r.workByID(r.byIDWorker(), "not-a-uuid")
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("Work with a bad id = %v, want river.JobCancel", err)
	}
	if len(r.acct.got()) != 0 {
		t.Error("the account was called for a bad id")
	}
}

func TestUsableEraseEmail_MatchesTheEnqueueRule(t *testing.T) {
	cases := []struct {
		name, in string
		want     bool
	}{
		{"empty", "", false},
		{"blank", "  ", false},
		{"two bytes", "a@", false},
		{"three bytes", "a@b", true},
		{"254 bytes", strings.Repeat("a", 254), true},
		{"255 bytes", strings.Repeat("a", 255), false},
		{"padded and cased", " Ada@X.test ", true},
		{"254 bytes in 127 runes", strings.Repeat("é", 127), true},
		{"256 bytes in 128 runes", strings.Repeat("é", 128), false},
	}
	for _, c := range cases {
		if got := UsableEraseEmail(c.in); got != c.want {
			t.Errorf("UsableEraseEmail(%s) = %v, want %v", c.name, got, c.want)
		}
	}

	t.Run("EnqueueErasure agrees", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		for _, c := range cases {
			tx, err := e.app.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = EnqueueErasure(ctx, tx, e.store.river, uuid.NewString(), c.in)
			_ = tx.Rollback(ctx)
			if (err == nil) != c.want {
				t.Errorf("EnqueueErasure(%s) error = %v, want accepted %v", c.name, err, c.want)
			}
		}
	})
}
