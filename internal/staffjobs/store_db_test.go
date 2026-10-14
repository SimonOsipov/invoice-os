package staffjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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
	store := NewStore(app)

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

	d, err := NewStore(app).Get(ctx, job)
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
	store := NewStore(app)
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
	store := NewStore(app)
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
