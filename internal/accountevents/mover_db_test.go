package accountevents

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	evSignedIn       = "account.signed_in"
	evSignInFailed   = "account.sign_in_failed"
	evResetRequested = "account.password_reset_requested"
	evResetCompleted = "account.password_reset_completed"
	fullBatchMsg     = "account-event-handoff: full batch"
)

// tick runs one mover tick with the env's River client.
func (e *env) tick(t *testing.T, log *slog.Logger) error {
	t.Helper()
	return MoveHandoffs(e.app, e.rc, log)(context.Background())
}

func mustTick(t *testing.T, e *env, log *slog.Logger) {
	t.Helper()
	if err := e.tick(t, log); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// AC 2: one row for a member of two workspaces becomes two jobs; a suspended third workspace gets none.
func TestMover_OneJobPerActiveWorkspace(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.tenant(t), e.tenant(t), e.tenant(t)
	u := e.user()
	e.member(t, a, u, "active", e.email("u"))
	e.member(t, b, u, "active", e.email("u"))
	e.member(t, c, u, "suspended", e.email("u"))
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	e.enqueue(t, evSignedIn, u, e.email("u"), e.tag+"-203.0.113.7", uaChromeMac, at)

	mustTick(t, e, slog.Default())

	jobs := e.jobs(t, u)
	if len(jobs) == 0 {
		t.Fatalf("no account_audit job for the member after the tick")
	}
	if got, want := tenantsOf(jobs), sorted(a, b); !slices.Equal(got, want) {
		t.Errorf("job tenants = %v, want exactly %v (one per active workspace, none for the suspended one)", got, want)
	}
	for _, j := range jobs {
		if j.UserID != u || j.Event != evSignedIn || j.IP != e.tag+"-203.0.113.7" || j.Device != "Chrome on macOS" {
			t.Errorf("job = %+v, want user %s, event %s, the row's IP and device %q", j, u, evSignedIn, "Chrome on macOS")
		}
		if j.OccurredAt != at.Format(time.RFC3339) {
			t.Errorf("job occurred_at = %q, want the row's time %q (RFC 3339, UTC)", j.OccurredAt, at.Format(time.RFC3339))
		}
	}
	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want 0", n)
	}
}

// AC 2: a failed attempt carries only an email; the mover matches it to the member in any case.
func TestMover_ResolvesAFailedAttemptByEmail(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	u := e.user()
	e.member(t, a, u, "active", "Ada-"+e.tag+"@Example.com")
	e.enqueue(t, evSignInFailed, nil, "ada-"+e.tag+"@example.com", e.tag+"-ip", uaChromeMac, time.Now())

	mustTick(t, e, slog.Default())

	jobs := e.jobs(t, u)
	if len(jobs) != 1 {
		t.Fatalf("%d job(s) for the member, want exactly 1", len(jobs))
	}
	if j := jobs[0]; j.UserID != u || j.TenantID != a || j.Event != evSignInFailed {
		t.Errorf("job = %+v, want user %s, tenant %s, event %s", j, u, a, evSignInFailed)
	}
}

// AC 3: an unknown address makes no job and one INFO line that names the event and holds no detail.
func TestMover_UnknownAddressBecomesALogLineOnly(t *testing.T) {
	e := newEnv(t)
	addr := "nobody-" + e.tag + "@example.com"
	ip, ua := e.tag+"-198.51.100.9", "Mozilla/5.0 uniqueagent-"+e.tag
	e.enqueue(t, evSignInFailed, nil, addr, ip, ua, time.Now())

	log, records, raw := capture()
	mustTick(t, e, log)

	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want the unknown-address row deleted", n)
	}
	if n := e.mustCount(t, `SELECT count(*) FROM river_job WHERE kind = 'account_audit' AND args->>'ip' = $1`, ip); n != 0 {
		t.Errorf("%d job(s) for an unknown address, want 0", n)
	}
	var named []map[string]any
	for _, r := range atLevel(records(), "INFO") {
		if mentions(r, evSignInFailed) {
			named = append(named, r)
		}
	}
	if len(named) != 1 {
		t.Errorf("%d INFO line(s) naming %s, want exactly 1; log:\n%s", len(named), evSignInFailed, raw())
	}
	for _, secret := range []string{addr, "nobody-" + e.tag, ip, "uniqueagent-" + e.tag} {
		if strings.Contains(raw(), secret) {
			t.Errorf("log holds %q, want no email, IP or user agent:\n%s", secret, raw())
		}
	}

	// Control: the same tick path makes a job for a member, so "no job" above is not a dead mover.
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("ctl"))
	e.enqueue(t, evSignInFailed, nil, e.email("ctl"), e.tag+"-ctl", ua, time.Now())
	mustTick(t, e, slog.Default())
	if n := len(e.jobs(t, u)); n != 1 {
		t.Errorf("control member got %d job(s), want 1", n)
	}
}

// AC 3: a member who is only suspended, invited or removed gets no job.
func TestMover_InactiveMemberGetsNoJob(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.tenant(t), e.tenant(t), e.tenant(t)
	u := e.user()
	e.member(t, a, u, "suspended", e.email("in"))
	e.member(t, b, u, "removed", e.email("in"))
	e.member(t, c, u, "invited", e.email("in"))
	e.enqueue(t, evSignedIn, u, e.email("in"), e.tag+"-inactive", uaChromeMac, time.Now())

	mustTick(t, e, slog.Default())

	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want the row deleted", n)
	}
	if jobs := e.jobs(t, u); len(jobs) != 0 {
		t.Errorf("jobs for an inactive member = %+v, want none", jobs)
	}

	// Control: once the member is active in one workspace, the same event makes a job.
	e.mustExec(t, `UPDATE memberships SET status = 'active' WHERE tenant_id = $1 AND user_id = $2`, a, u)
	e.enqueue(t, evSignedIn, u, e.email("in"), e.tag+"-active", uaChromeMac, time.Now())
	mustTick(t, e, slog.Default())
	if jobs := e.jobs(t, u); len(jobs) != 1 || jobs[0].TenantID != a {
		t.Errorf("jobs after activation = %+v, want one for tenant %s", jobs, a)
	}
}

// seedBatch inserts n rows for one member, newest-inserted first, with strictly rising occurred_at.
func seedBatch(t *testing.T, e *env, n int, user string) {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	for i := n - 1; i >= 0; i-- {
		e.enqueue(t, evSignedIn, user, nil, fmt.Sprintf("%s-%03d", e.tag, i), uaChromeMac, base.Add(time.Duration(i)*time.Second))
	}
}

// AC 4: 101 rows, 100 per tick, oldest first.
func TestMover_TakesTheOldest100(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("batch"))
	seedBatch(t, e, 101, u)

	mustTick(t, e, slog.Default())
	if n := e.outbox(t); n != 1 {
		t.Fatalf("%d outbox row(s) after tick 1, want 1", n)
	}
	var left string
	if err := e.admin.QueryRow(context.Background(), `SELECT ip FROM account_event_handoffs`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%s-%03d", e.tag, 100); left != want {
		t.Errorf("row left after tick 1 has ip %q, want the newest %q", left, want)
	}
	jobs := e.jobs(t, u)
	if len(jobs) != 100 {
		t.Fatalf("%d job(s) after tick 1, want 100", len(jobs))
	}
	for _, j := range jobs {
		if j.IP == left {
			t.Errorf("the newest row %q was moved in tick 1", left)
		}
	}

	mustTick(t, e, slog.Default())
	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) after tick 2, want 0", n)
	}
	if n := len(e.jobs(t, u)); n != 101 {
		t.Errorf("%d job(s) after tick 2, want 101", n)
	}
}

// AC 4: a tick that takes 100 rows warns once; 99 rows is under the bound.
func TestMover_FullBatchLogsABacklogWarning(t *testing.T) {
	t.Run("100 rows", func(t *testing.T) {
		e := newEnv(t)
		a, u := e.tenant(t), e.user()
		e.member(t, a, u, "active", e.email("warn"))
		seedBatch(t, e, 100, u)

		log, records, raw := capture()
		mustTick(t, e, log)

		if n := len(e.jobs(t, u)); n != 100 {
			t.Fatalf("%d job(s), want 100", n)
		}
		warns := atLevel(records(), "WARN")
		if len(warns) != 1 {
			t.Fatalf("%d WARN line(s), want exactly 1; log:\n%s", len(warns), raw())
		}
		w := warns[0]
		if msg, _ := w["msg"].(string); !strings.Contains(msg, fullBatchMsg) {
			t.Errorf("WARN msg = %q, want it to contain %q", msg, fullBatchMsg)
		}
		hasSize := false
		for k, v := range w {
			if k == "time" || k == "level" || k == "msg" {
				continue
			}
			if f, ok := v.(float64); ok && f == 100 {
				hasSize = true
			}
			if s, ok := v.(string); ok && s == "100" {
				hasSize = true
			}
		}
		if !hasSize {
			t.Errorf("WARN line has no attribute with the batch size 100: %v", w)
		}
		if strings.Contains(raw(), e.email("warn")) || strings.Contains(raw(), e.tag+"-0") {
			t.Errorf("WARN log holds an address or IP:\n%s", raw())
		}
	})
	t.Run("101 rows", func(t *testing.T) {
		e := newEnv(t)
		a, u := e.tenant(t), e.user()
		e.member(t, a, u, "active", e.email("warn"))
		seedBatch(t, e, 101, u)

		log, records, raw := capture()
		mustTick(t, e, log)
		if warns := atLevel(records(), "WARN"); len(warns) != 1 {
			t.Fatalf("%d WARN line(s) on the full tick, want 1; log:\n%s", len(warns), raw())
		}
		mustTick(t, e, log)
		if warns := atLevel(records(), "WARN"); len(warns) != 1 {
			t.Errorf("%d WARN line(s) after the one-row tick, want still 1; log:\n%s", len(warns), raw())
		}
	})
	t.Run("99 rows", func(t *testing.T) {
		e := newEnv(t)
		a, u := e.tenant(t), e.user()
		e.member(t, a, u, "active", e.email("warn"))
		seedBatch(t, e, 99, u)

		log, records, raw := capture()
		mustTick(t, e, log)

		if n := len(e.jobs(t, u)); n != 99 {
			t.Fatalf("%d job(s), want 99", n)
		}
		if warns := atLevel(records(), "WARN"); len(warns) != 0 {
			t.Errorf("%d WARN line(s) for 99 rows, want none; log:\n%s", len(warns), raw())
		}
	})
}

// AC 2 and [d-wrong-turns]: a reset requested on one device and completed on another write
// two jobs for the same member, each with its own device and IP.
func TestMover_RequestAndCompletionOnTwoDevicesWriteTwoRows(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("two"))
	now := time.Now().UTC()
	e.enqueue(t, evResetRequested, nil, e.email("two"), e.tag+"-ipA", uaChromeMac, now.Add(-time.Minute))
	e.enqueue(t, evResetCompleted, u, nil, e.tag+"-ipB", uaSafariIPhone, now)

	mustTick(t, e, slog.Default())

	jobs := e.jobs(t, u)
	if len(jobs) != 2 {
		t.Fatalf("%d job(s), want 2: %+v", len(jobs), jobs)
	}
	byEvent := map[string]AuditArgs{}
	for _, j := range jobs {
		byEvent[j.Event] = j
	}
	req, done := byEvent[evResetRequested], byEvent[evResetCompleted]
	if req.UserID != u || req.IP != e.tag+"-ipA" || req.Device != "Chrome on macOS" {
		t.Errorf("requested job = %+v, want user %s, ip A, Chrome on macOS", req, u)
	}
	if done.UserID != u || done.IP != e.tag+"-ipB" || done.Device != "Safari on iOS" {
		t.Errorf("completed job = %+v, want user %s, ip B, Safari on iOS", done, u)
	}
}

// failingClient is a River client whose bundle lacks account_audit, so every insert of one fails.
func failingClient(t *testing.T, e *env) *river.Client[pgx.Tx] {
	t.Helper()
	workers := river.NewWorkers()
	river.AddWorker(workers, &otherWorker{})
	rc, err := river.NewClient(riverpgxv5.New(e.app), &river.Config{
		Workers: workers,
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
	})
	if err != nil {
		t.Fatalf("river client with a bundle: %v", err)
	}
	return rc
}

type otherArgs struct{}

func (otherArgs) Kind() string { return "account_events_test_other" }

type otherWorker struct {
	river.WorkerDefaults[otherArgs]
}

func (*otherWorker) Work(context.Context, *river.Job[otherArgs]) error { return nil }

// AC 4: a job insert that fails rolls back the delete; every row stays, and a later good tick moves them.
func TestMover_FailedJobInsertKeepsTheRows(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("fail"))
	seedBatch(t, e, 5, u)
	if n := e.outbox(t); n != 5 {
		t.Fatalf("seeded %d row(s), want 5", n)
	}

	err := MoveHandoffs(e.app, failingClient(t, e), slog.Default())(context.Background())
	if err == nil {
		t.Fatal("tick with a failing job insert returned nil, want an error")
	}
	if n := e.outbox(t); n != 5 {
		t.Errorf("%d outbox row(s) after the failed tick, want all 5 kept", n)
	}
	if n := len(e.jobs(t, u)); n != 0 {
		t.Errorf("%d job(s) after the failed tick, want 0", n)
	}

	mustTick(t, e, slog.Default())
	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) after the good tick, want 0", n)
	}
	if n := len(e.jobs(t, u)); n != 5 {
		t.Errorf("%d job(s) after the good tick, want 5", n)
	}
}

// AC 2 and 3: a user id wins over an email; an email matches trimmed and in any case; a blank email matches nobody.
func TestMover_MembershipLookupRules(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(t), e.tenant(t)
	u1, u2 := e.user(), e.user()
	e.member(t, a, u1, "active", e.email("one"))
	e.member(t, b, u2, "active", "Two-"+e.tag+"@Corp.Example.Test")

	e.enqueue(t, evSignedIn, u1, e.email("two"), e.tag+"-both", uaChromeMac, time.Now().Add(-3*time.Second))
	e.enqueue(t, evSignInFailed, nil, "  TWO-"+e.tag+"@corp.example.test  ", e.tag+"-trim", uaChromeMac, time.Now().Add(-2*time.Second))
	e.enqueue(t, evSignInFailed, nil, "   ", e.tag+"-blank", uaChromeMac, time.Now().Add(-time.Second))

	mustTick(t, e, slog.Default())

	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want 0", n)
	}
	jobs := e.jobs(t, u1, u2)
	if len(jobs) == 0 {
		t.Fatal("no job after the tick")
	}
	byIP := map[string]AuditArgs{}
	for _, j := range jobs {
		byIP[j.IP] = j
	}
	if j, ok := byIP[e.tag+"-both"]; !ok || j.UserID != u1 || j.TenantID != a {
		t.Errorf("row with a user id and another member's email gave %+v, want user %s in tenant %s", j, u1, a)
	}
	if j, ok := byIP[e.tag+"-trim"]; !ok || j.UserID != u2 || j.TenantID != b {
		t.Errorf("row with a padded upper-case email gave %+v, want user %s in tenant %s", j, u2, b)
	}
	if _, ok := byIP[e.tag+"-blank"]; ok {
		t.Errorf("row with a blank email made a job: %+v", byIP[e.tag+"-blank"])
	}
	if len(jobs) != 2 {
		t.Errorf("%d job(s), want exactly 2: %+v", len(jobs), jobs)
	}
}

// AC 2 and 7: each of the six events makes a job under its own name; three blocked refusals in one second make three jobs.
func TestMover_EverySixEventsMakesAJobAndNoBlockedRowIsDropped(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("six"))
	at := time.Now().UTC().Add(-time.Minute)
	if len(Events) != 6 {
		t.Fatalf("Events has %d name(s), want 6", len(Events))
	}
	for i, ev := range Events {
		e.enqueue(t, ev, u, nil, fmt.Sprintf("%s-ev%d", e.tag, i), uaChromeMac, at.Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 2; i++ {
		e.enqueue(t, "account.sign_in_blocked", u, nil, fmt.Sprintf("%s-blk%d", e.tag, i), uaChromeMac, at)
	}

	mustTick(t, e, slog.Default())

	counts := map[string]int{}
	for _, j := range e.jobs(t, u) {
		counts[j.Event]++
	}
	for _, ev := range Events {
		want := 1
		if ev == "account.sign_in_blocked" {
			want = 3
		}
		if counts[ev] != want {
			t.Errorf("%d job(s) for %s, want %d", counts[ev], ev, want)
		}
	}
}

// AC 3: an unknown user id is dropped with one INFO line that holds neither the id nor the IP; an empty tick logs nothing.
func TestMover_UnknownUserIDIsDroppedAndAnEmptyTickIsSilent(t *testing.T) {
	e := newEnv(t)
	a, u, ghost := e.tenant(t), e.user(), e.user()
	e.member(t, a, u, "active", e.email("known"))

	log, records, raw := capture()
	mustTick(t, e, log)
	if len(records()) != 0 {
		t.Errorf("empty tick logged %s", raw())
	}

	e.enqueue(t, "account.signed_out", ghost, nil, e.tag+"-ghostip", uaChromeMac, time.Now().Add(-time.Second))
	e.enqueue(t, evSignedIn, u, nil, e.tag+"-knownip", uaChromeMac, time.Now())
	mustTick(t, e, log)

	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want 0", n)
	}
	jobs := e.jobs(t, u, ghost)
	if len(jobs) != 1 || jobs[0].UserID != u {
		t.Errorf("jobs = %+v, want exactly one, for the known member %s and none for the unknown id", jobs, u)
	}
	infos := atLevel(records(), "INFO")
	if len(infos) != 1 || !mentions(infos[0], "account.signed_out") {
		t.Errorf("INFO lines = %v, want exactly one naming account.signed_out", infos)
	}
	for _, secret := range []string{ghost, e.tag + "-ghostip", u} {
		if strings.Contains(raw(), secret) {
			t.Errorf("log holds %q:\n%s", secret, raw())
		}
	}
	if w := atLevel(records(), "WARN"); len(w) != 0 {
		t.Errorf("%d WARN line(s) for two rows, want none", len(w))
	}
}

// AC 4: four movers racing on one outbox move each row exactly once.
func TestMover_ConcurrentTicksMoveEachRowOnce(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("race"))
	const rows = 60
	seedBatch(t, e, rows, u)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- e.tick(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent tick: %v", err)
		}
	}
	mustTick(t, e, slog.Default())

	if n := e.outbox(t); n != 0 {
		t.Errorf("%d outbox row(s) left, want 0", n)
	}
	jobs := e.jobs(t, u)
	seen := map[string]int{}
	for _, j := range jobs {
		seen[j.IP]++
	}
	if len(jobs) != rows || len(seen) != rows {
		t.Errorf("%d job(s) over %d distinct row(s), want %d each (no row moved twice, none lost)", len(jobs), len(seen), rows)
	}
}

// AC 5, 6 and [d-ua]: mover then worker. A member of two workspaces ends with exactly one row in each, none in a
// suspended third or in another member's; the raw user agent reaches no job; an empty IP is written as "Unknown IP".
func TestMoverThenWorker_OneRowInEachActiveWorkspace(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.tenant(t), e.tenant(t), e.tenant(t)
	u, other := e.user(), e.user()
	e.member(t, a, u, "active", e.email("mw"))
	e.member(t, b, u, "active", e.email("mw"))
	e.member(t, c, u, "suspended", e.email("mw"))
	e.member(t, a, other, "active", e.email("other"))
	rawUA := "Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Safari/537.36 uniqueagent-" + e.tag
	e.enqueue(t, evSignInFailed, nil, e.email("mw"), "", rawUA, time.Now())

	mustTick(t, e, slog.Default())
	jobs := e.jobs(t, u)
	if len(jobs) != 2 {
		t.Fatalf("%d job(s), want 2: %+v", len(jobs), jobs)
	}
	if n := e.mustCount(t, `SELECT count(*) FROM river_job WHERE kind = 'account_audit' AND args::text LIKE $1`, "%uniqueagent-"+e.tag+"%"); n != 0 {
		t.Errorf("%d job(s) hold the raw user agent, want 0", n)
	}
	mustTick(t, e, slog.Default())
	if again := e.jobs(t, u); len(again) != 2 {
		t.Errorf("%d job(s) after a second tick, want still 2", len(again))
	}
	for _, j := range jobs {
		if err := e.work(j); err != nil {
			t.Fatalf("Work: %v", err)
		}
	}

	for _, tn := range []string{a, b} {
		rows := e.rowsUnder(t, tn, u)
		if len(rows) != 1 {
			t.Fatalf("tenant %s holds %d row(s) for the member, want exactly 1", tn, len(rows))
		}
		if rows[0].Event != evSignInFailed || rows[0].Payload["ip_address"] != "Unknown IP" || rows[0].Payload["device"] != "Chrome on Windows" {
			t.Errorf("tenant %s row = %+v, want %s from Unknown IP on Chrome on Windows", tn, rows[0], evSignInFailed)
		}
	}
	if got := e.rowsUnder(t, c, u); len(got) != 0 {
		t.Errorf("suspended workspace holds %d row(s), want 0", len(got))
	}
	if got := e.rowsUnder(t, a, other); len(got) != 0 {
		t.Errorf("another member of tenant A holds %d row(s), want 0", len(got))
	}
	if n := e.auditEverywhere(t, u); n != 2 {
		t.Errorf("%d row(s) for the member across all tenants, want 2", n)
	}
}
