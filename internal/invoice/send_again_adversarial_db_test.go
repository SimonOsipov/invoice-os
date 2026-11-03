package invoice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// A double click: two sends race for one failed invoice; exactly one wins.
func TestSendAgain_TwoConcurrentSendsOneWins(t *testing.T) {
	r := newSARig(t, "SA race")
	inv := r.failedInvoice(t, "SA-RACE", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	before := r.snap(t, inv)

	const n = 2
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = r.sub.SendAgain(r.ctx, inv)
		}()
	}
	close(start)
	wg.Wait()

	wins, stale := 0, 0
	for _, err := range errs {
		var se StatusError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &se) && se.Have == StatusQueued:
			stale++
		default:
			t.Errorf("a concurrent SendAgain = %v, want nil or StatusError{queued}", err)
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("wins=%d stale=%d, want 1 and 1", wins, stale)
	}
	after := r.snap(t, inv)
	if after.History != before.History+1 || after.Audit != before.Audit+1 || after.Jobs != 1 {
		t.Errorf("history %d->%d audit %d->%d jobs %d, want one new row each and one job", before.History, after.History, before.Audit, after.Audit, after.Jobs)
	}
	var live int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE args->>'invoice_id' = $1`, inv).Scan(&live); err != nil || live != 1 {
		t.Errorf("river rows for the invoice = %d (%v), want exactly the one new row", live, err)
	}
}

// A stale page: the invoice is queued by an earlier send, so a second send is refused and writes nothing.
func TestSendAgain_AStaleSecondSendIsRefused(t *testing.T) {
	r := newSARig(t, "SA stale")
	inv := r.failedInvoice(t, "SA-STALE", "payload_not_built")
	r.job(t, inv, saJob{state: "failed", riverState: "cancelled"})
	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("first SendAgain: %v", err)
	}
	before := r.snap(t, inv)
	_, err := r.sub.SendAgain(r.ctx, inv)
	var se StatusError
	if !errors.As(err, &se) || se.Have != StatusQueued {
		t.Fatalf("second SendAgain = %v, want StatusError{queued}", err)
	}
	if after := r.snap(t, inv); after != before {
		t.Errorf("the stale send wrote:\nbefore %+v\nafter  %+v", before, after)
	}
}

// The job points at a River row that is gone: the delete is skipped, the send goes out.
func TestSendAgain_ADanglingRiverJobIDIsIgnored(t *testing.T) {
	r := newSARig(t, "SA dangling")
	inv := r.failedInvoice(t, "SA-DANGLE", "never_acknowledged")
	job := r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	if _, err := r.super.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, job.riverID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sub.SendAgain(r.ctx, inv); err != nil {
		t.Fatalf("SendAgain with a dangling river_job_id: %v", err)
	}
	if s := r.snap(t, inv); s.Status != "queued" || s.RiverJobID == 0 || s.RiverJobID == job.riverID {
		t.Errorf("after: status %q river_job_id %d (was %d), want queued and a new id", s.Status, s.RiverJobID, job.riverID)
	}
}

// A live River row with the same args that the job row does not name: the insert is a duplicate, so the send is busy.
func TestSendAgain_ADuplicateRiverRowIsBusy(t *testing.T) {
	r := newSARig(t, "SA duplicate")
	inv := r.failedInvoice(t, "SA-DUP", "payload_not_built")
	job := r.job(t, inv, saJob{state: "failed"})
	if r.riverRow(t, job.args, "available") == 0 {
		t.Fatal("seed an available river row")
	}
	r.refuses(t, inv, ErrResendBusy)
}

// The real Store.CallerRole decides: a preparer, a suspended reviewer and a non-member get 403 and nothing moves.
func TestSendAgain_RealStore_OnlyAnApproverReachesTheJob(t *testing.T) {
	r := newSARig(t, "SA roles")
	inv := r.failedInvoice(t, "SA-ROLE", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	store := NewStore(r.app)

	preparer, suspended, stranger, reviewer, admin := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedMembership(t, r.super, r.tenant, preparer, "preparer")
	seedMembershipWithStatus(t, r.super, r.tenant, suspended, "reviewer", "suspended")
	seedMembership(t, r.super, r.tenant, reviewer, "reviewer")
	seedMembership(t, r.super, r.tenant, admin, "admin")

	post := func(subject, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+id+"/send-again", nil)
		req.SetPathValue("id", id)
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: r.tenant}))
		rec := httptest.NewRecorder()
		SendAgainHandler(r.sub.SendAgain, store.CallerRole, nil).ServeHTTP(rec, req)
		return rec
	}

	before := r.snap(t, inv)
	var bodies []string
	for name, subject := range map[string]string{"preparer": preparer, "suspended reviewer": suspended, "non-member": stranger} {
		for _, id := range []string{inv, uuid.NewString()} {
			rec := post(subject, id)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s on %s: status %d, want 403 (body=%s)", name, id, rec.Code, rec.Body.String())
			}
			bodies = append(bodies, rec.Body.String())
		}
	}
	if len(bodies) != 6 {
		t.Fatalf("collected %d refusal bodies, want 6", len(bodies))
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Errorf("403 bodies differ: %q vs %q", b, bodies[0])
		}
	}
	if after := r.snap(t, inv); after != before {
		t.Errorf("a refused send wrote:\nbefore %+v\nafter  %+v", before, after)
	}

	if rec := post(reviewer, inv); rec.Code != http.StatusOK {
		t.Fatalf("reviewer: status %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if from, to, actor := r.fix().lastHistory(t, inv); from != "failed" || to != "queued" || actor != reviewer {
		t.Errorf("history = %s->%s by %q, want failed->queued by the reviewer %q", from, to, actor, reviewer)
	}

	inv2 := r.failedInvoice(t, "SA-ROLE2", "payload_not_built")
	r.job(t, inv2, saJob{state: "failed", riverState: "cancelled"})
	if rec := post(admin, inv2); rec.Code != http.StatusOK {
		t.Fatalf("admin: status %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// The kind guard stands on its own: a job that looks resendable (state and no poll_ref) does not make an
// acknowledged_no_verdict or NULL-kind invoice resendable.
func TestSendAgain_TheKindGuardRefusesAResendableLookingJob(t *testing.T) {
	cases := []struct{ name, kind, jobState string }{
		{"acknowledged_no_verdict with a dead_lettered job", "acknowledged_no_verdict", "dead_lettered"},
		{"acknowledged_no_verdict with a failed job", "acknowledged_no_verdict", "failed"},
		{"NULL kind with a dead_lettered job", "", "dead_lettered"},
		{"NULL kind with a failed job", "", "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newSARig(t, "SA kind guard")
			inv := r.failedInvoice(t, "SA-KG", c.kind)
			r.job(t, inv, saJob{state: c.jobState, attempts: 8, riverState: "discarded"})
			r.refuses(t, inv, ErrNotResendable)
		})
	}
}

// With the handler's pre-read bypassed, SendAgain itself refuses a suspended or absent member (the tenant
// seam's ErrNotActiveMember), and an active reviewer still goes through.
func TestSendAgain_SuspendedAndNonMemberAreRefusedWithoutTheHandler(t *testing.T) {
	r := newSARig(t, "SA in-tx roles")
	inv := r.failedInvoice(t, "SA-INTX", "never_acknowledged")
	r.job(t, inv, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})
	suspended, stranger, reviewer := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedMembershipWithStatus(t, r.super, r.tenant, suspended, "reviewer", "suspended")
	seedMembership(t, r.super, r.tenant, reviewer, "reviewer")
	as := func(subject string) context.Context {
		return auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: r.tenant})
	}

	before := r.snap(t, inv)
	for name, subject := range map[string]string{"suspended reviewer": suspended, "non-member": stranger} {
		if _, err := r.sub.SendAgain(as(subject), inv); !errors.Is(err, db.ErrNotActiveMember) {
			t.Errorf("%s: SendAgain = %v, want db.ErrNotActiveMember", name, err)
		}
	}
	if after := r.snap(t, inv); after != before {
		t.Errorf("a refused SendAgain wrote:\nbefore %+v\nafter  %+v", before, after)
	}
	if _, err := r.sub.SendAgain(as(reviewer), inv); err != nil {
		t.Fatalf("SendAgain as an active reviewer = %v, want nil", err)
	}
}
