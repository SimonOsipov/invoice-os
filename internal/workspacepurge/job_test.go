package workspacepurge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/workspacepurge/purgeargs"
)

func purgeJob(tenantID string) *river.Job[purgeargs.Args] {
	return &river.Job[purgeargs.Args]{JobRow: &rivertype.JobRow{ID: 1}, Args: purgeargs.Args{TenantID: tenantID}}
}

func (r *rig) worker() *PurgeWorker { return &PurgeWorker{Purger: r.purger()} }

func TestPurgeWorker_PurgesAStartedTenant(t *testing.T) {
	r := newRig(t)
	a := r.start(r.tenant())
	admin, mail := r.user(), r.mail("job")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 2)
	b, bUser, bMail := r.due(r.tenant()), r.user(), r.mail("b") // due, but the job names A only
	r.member(b, bUser, "admin", "active", &bMail, "")

	if err := r.worker().Work(context.Background(), purgeJob(a)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if r.tenantRows(a) != 0 || r.memberRows(a) != 0 || r.objects(a) != 0 {
		t.Errorf("A's tenant, member and object counts = %d, %d, %d; want 0, 0, 0", r.tenantRows(a), r.memberRows(a), r.objects(a))
	}
	if r.erasures(admin) != 1 {
		t.Error("the admin of A has no erasure row")
	}
	recs := r.logs.find(slog.LevelInfo, infoPurged)
	if len(recs) != 1 || recs[0].attrs["tenant_id"] != a || recs[0].attrs["members"] != "1" || recs[0].attrs["objects"] != "2" {
		t.Errorf("%q records = %+v, want one with tenant_id %s, members 1, objects 2", infoPurged, recs, a)
	}
	if r.tenantRows(b) != 1 || r.erasures(bUser) != 0 || r.marker(b) != nil {
		t.Error("due tenant B was touched by the job for A")
	}

	// Another started tenant, and a second run of A's job.
	s, sUser, sMail := r.start(r.tenant()), r.user(), r.mail("s")
	r.member(s, sUser, "admin", "active", &sMail, "")
	if err := r.worker().Work(context.Background(), purgeJob(a)); err != nil {
		t.Fatalf("second Work for the purged tenant: %v", err)
	}
	if r.erasures(admin) != 1 || r.jobs(eraseKind, admin) != 1 || len(r.staff(a)) != 1 {
		t.Errorf("after a second run: erasure rows %d, erase jobs %d, staff rows %d; want 1, 1, 1", r.erasures(admin), r.jobs(eraseKind, admin), len(r.staff(a)))
	}
	if r.tenantRows(s) != 1 || r.erasures(sUser) != 0 || r.marker(s) == nil || len(r.staff(s)) != 0 {
		t.Error("started tenant S was touched by the jobs for A")
	}
}

func TestPurgeWorker_ATenantNotStartedIsUntouched(t *testing.T) {
	r := newRig(t)
	scheduled, unscheduled := r.tenant(), r.tenant()
	r.scheduleAt(scheduled, "now()")
	users := map[string]string{}
	for _, tn := range []string{scheduled, unscheduled} {
		users[tn] = r.user()
		mail := r.mail("idle")
		r.member(tn, users[tn], "admin", "active", &mail, "")
		r.put(tn, 1)
	}
	for name, tn := range map[string]string{"scheduled, not due, not started": scheduled, "unscheduled": unscheduled} {
		if err := r.worker().Work(context.Background(), purgeJob(tn)); err != nil {
			t.Fatalf("Work on the %s tenant: %v", name, err)
		}
		if r.tenantRows(tn) != 1 || r.memberRows(tn) != 1 || r.objects(tn) != 1 || r.marker(tn) != nil {
			t.Errorf("the %s tenant lost a row or object, or got a marker", name)
		}
		if r.erasures(users[tn]) != 0 || len(r.staff(tn)) != 0 {
			t.Errorf("the %s tenant has an erasure or staff row", name)
		}
	}

	if err := r.worker().Work(context.Background(), purgeJob(uuid.NewString())); err != nil {
		t.Errorf("Work on a tenant that does not exist: %v, want nil", err)
	}

	// Control: the same worker purges a started tenant, so the untouched tenants above are a decision, not a dead worker.
	// It runs on a nil Logger, so Work falls back to the default one.
	prev := slog.Default()
	slog.SetDefault(r.logs.logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	s := r.start(r.tenant())
	w := r.worker()
	w.Purger.Logger = nil
	if err := w.Work(context.Background(), purgeJob(s)); err != nil {
		t.Fatalf("Work on the started tenant: %v", err)
	}
	if r.tenantRows(s) != 0 {
		t.Error("control: the started tenant is still there after Work")
	}
	if recs := r.logs.find(slog.LevelInfo, infoPurged); len(recs) != 1 || recs[0].attrs["tenant_id"] != s {
		t.Errorf("%q records on the default logger = %+v, want one for %s", infoPurged, recs, s)
	}
}

func TestPurgeWorker_ABadIDCancelsTheJob(t *testing.T) {
	r := newRig(t)
	a := r.start(r.tenant())

	err := r.worker().Work(context.Background(), purgeJob("x"))
	var cancel *rivertype.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("Work(tenant_id \"x\") = %v, want a river.JobCancel error", err)
	}
	if r.tenantRows(a) != 1 {
		t.Error("control: a bad id removed a started tenant")
	}
	if err := r.worker().Work(context.Background(), purgeJob(a)); err != nil || r.tenantRows(a) != 0 {
		t.Errorf("control: a valid id did not purge A: err = %v, tenant rows = %d", err, r.tenantRows(a))
	}
}

func TestPurgeWorker_AFailedStepLeavesTheTenantStartedForTheTicker(t *testing.T) {
	r := newRig(t)
	a := r.start(r.tenant())
	admin, mail := r.user(), r.mail("fail")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 2)
	fails := 0
	r.store.failDelete = func([]string) bool { // the first batch fails once
		fails++
		return fails == 1
	}

	err := r.worker().Work(context.Background(), purgeJob(a))
	requireStepErr(t, err, a, "files")
	var cancel *rivertype.JobCancelError
	if errors.As(err, &cancel) {
		t.Error("a failed step cancels the job; it must stay retryable by the ticker")
	}
	recs := r.logs.find(slog.LevelError, errTenantMsg)
	if len(recs) != 1 || recs[0].attrs["tenant_id"] != a || recs[0].attrs["step"] != "files" || !strings.Contains(recs[0].attrs["error"], "forced delete failure") {
		t.Fatalf("%q records = %+v, want one with tenant_id %s, step files and the cause", errTenantMsg, recs, a)
	}
	if r.tenantRows(a) != 1 || r.marker(a) == nil {
		t.Fatalf("after the failed step: tenant rows = %d, marker = %v; want the tenant present and started", r.tenantRows(a), r.marker(a))
	}

	if err := r.purger().RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if r.tenantRows(a) != 0 || r.objects(a) != 0 {
		t.Errorf("after RunOnce: tenant rows = %d, objects = %d; want 0, 0", r.tenantRows(a), r.objects(a))
	}
}
