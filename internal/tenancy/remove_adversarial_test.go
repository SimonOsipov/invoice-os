package tenancy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestRemoveMember_InvitedRowIsRemovable(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 invited", "Ada Obi")
	inv := w.addMember(t, "reviewer", "invited", "Grace Eze")

	got, err := w.store.RemoveMember(w.adminCtx(), inv)
	if err != nil {
		t.Fatalf("RemoveMember of an invited row: %v", err)
	}
	if got.Status != "removed" || got.Email != nil {
		t.Errorf("returned status=%q email-nil=%v, want removed and nil", got.Status, got.Email == nil)
	}
	if p := auditPayload(t, w.app, w.tenant, "membership.removed", inv); p["from"] != "invited" {
		t.Errorf("audit from = %v, want invited", p["from"])
	}
}

func TestRemoveMember_AdminRemovesSelfWhenAnotherAdminIsActive(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 self", "Ada Obi")
	other := w.addMember(t, "admin", "active", "Zed Okoro")

	got, err := w.store.RemoveMember(w.adminCtx(), w.admin)
	if err != nil {
		t.Fatalf("self-removal beside a second active admin: %v", err)
	}
	if got.Status != "removed" || mustStatus(t, w.super, other) != "active" {
		t.Errorf("self status=%q other status=%q, want removed and active", got.Status, mustStatus(t, w.super, other))
	}
	if got := auditActor(t, w.app, w.tenant, "membership.removed"); got != w.admin {
		t.Errorf("audit actor = %q, want the removing admin themself", got)
	}
	if _, err := w.store.ListMemberships(w.adminCtx()); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("the removed admin's next request: err = %v, want db.ErrNotActiveMember", err)
	}
}

func TestRemoveMember_RemovedAdminIsNotAnotherActiveAdmin(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 removed admin", "Ada Obi")
	seedIdentityMembership(t, w.super, w.tenant, uuid.NewString(), "admin", "removed", strp("Gone Admin"), nil)

	if _, err := w.store.RemoveMember(w.adminCtx(), w.admin); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("err = %v, want ErrLastActiveAdmin: a removed admin must not count as a second admin", err)
	}
	if mustStatus(t, w.super, w.admin) != "active" {
		t.Error("the refused remove changed the sole active admin")
	}
}

func TestRemoveMember_AdminOfAnotherTenantReachesNothing(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 victim", "Ada Obi")
	other := newInvWorld(t, "DEL-02-02 attacker", "Zed Okoro")
	m := w.addMember(t, "reviewer", "active", "Grace Eze")
	before := imageOf(t, w.super, w.tenant, m)

	// Own tenant claim, foreign target: the row is invisible under RLS.
	if _, err := other.store.RemoveMember(other.adminCtx(), m); !errors.Is(err, ErrMembershipNotFound) {
		t.Errorf("foreign admin, own tenant claim: err = %v, want ErrMembershipNotFound", err)
	}
	// Forged claim for the victim tenant: no membership row there, the seam refuses.
	forged := w.as(other.admin)
	if _, err := w.store.RemoveMember(forged, m); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("foreign admin, forged tenant claim: err = %v, want db.ErrNotActiveMember", err)
	}
	if imageOf(t, w.super, w.tenant, m) != before {
		t.Error("a cross-tenant admin changed the victim row")
	}
	if n := auditCount(t, w.app, w.tenant, "membership.removed"); n != 0 {
		t.Errorf("membership.removed rows in the victim tenant = %d, want 0", n)
	}
	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Errorf("control: the victim tenant's own admin: %v", err)
	}
}

func TestRemoveMember_RepeatAndConcurrentRemovesWriteOneAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	for round := range 6 {
		w := invWorld{super: super, app: app, store: store, tenant: seedTenant(t, super, "DEL-02-02 same target")}
		a := w.addMember(t, "admin", "active", "Ada Obi")
		b := w.addMember(t, "admin", "active", "Zed Okoro")
		m, _ := w.grantedMember(t, "Grace Eze")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = store.RemoveMember(w.as(a), m) }()
		go func() { defer wg.Done(); <-start; _, errs[1] = store.RemoveMember(w.as(b), m) }()
		close(start)
		wg.Wait()

		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: concurrent removes of one member = %v, want both 200", round, errs)
		}
		if n := w.auditCountOf(t, "membership.removed"); n != 1 {
			t.Fatalf("round %d: membership.removed rows = %d, want 1", round, n)
		}
		if _, err := store.RemoveMember(w.as(a), m); err != nil {
			t.Fatalf("round %d: repeat remove: %v", round, err)
		}
		if n := w.auditCountOf(t, "membership.removed"); n != 1 {
			t.Fatalf("round %d: after a repeat, membership.removed rows = %d, want 1", round, n)
		}
	}
}

func TestRemoveMemberHandler_MalformedIDsAnswer404WithoutTouchingTheStore(t *testing.T) {
	id := &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	member := uuid.NewString()
	calls := 0
	remove := func(_ context.Context, u string) (MemberAccess, error) {
		calls++
		if u != member {
			t.Errorf("store got %q, want the canonical lower-case id", u)
		}
		return MemberAccess{Membership: Membership{UserID: u, Status: "removed"}}, nil
	}
	for _, bad := range []string{"1", "not-a-uuid", "%27%3B%20drop%20table%20memberships%3B--", "00000000-0000-0000-0000-00000000000g", "%00"} {
		rec := removeViaMux(t, remove, nil, id, bad)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "membership not found") {
			t.Errorf("path %q: %d %s, want 404 membership not found", bad, rec.Code, rec.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("store called %d times for malformed ids, want 0", calls)
	}
	var rec *httptest.ResponseRecorder = removeViaMux(t, remove, nil, id, strings.ToUpper(member))
	if rec.Code != http.StatusOK || calls != 1 {
		t.Errorf("control: upper-case uuid = %d with %d store calls, want 200 and 1", rec.Code, calls)
	}
}

// SetRoleMembers shares the named rows and RemoveMember locks the target and the admins
// in user_id order. A PUT that shares in another order deadlocks against the remove.
// A held lock on the target queues the PUT first; it then takes the target and waits on the
// admin the remove already holds.
func TestRemoveMember_RolePutNamingAnAdminAndTheTargetDoesNotDeadlock(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	// A physical-order scan, whatever the planner prefers on this table size.
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["enable_indexscan"] = "off"
	cfg.ConnConfig.RuntimeParams["enable_bitmapscan"] = "off"
	scan, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(scan.Close)
	store, roles := NewStore(app), approval.NewStore(scan, nil, nil)

	tenant := seedTenant(t, super, "DEL-02-02 lock order")
	ids := []string{uuid.NewString(), uuid.NewString()}
	slices.Sort(ids)
	admin, target := ids[0], ids[1]
	// The larger id goes in first, so a physical-order scan reaches it before the admin.
	seedMembership(t, super, tenant, target, "preparer", "active")
	seedMembership(t, super, tenant, admin, "admin", "active")
	seedRoleStaffedBy(t, super, tenant, "approver")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: admin, Role: "authenticated", TenantID: tenant})

	hold, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer hold.Rollback(ctx) //nolint:errcheck
	if _, err := hold.Exec(ctx, `SELECT 1 FROM memberships WHERE tenant_id = $1 AND user_id = $2 FOR UPDATE`, tenant, target); err != nil {
		t.Fatalf("hold the target row: %v", err)
	}
	waiting := func(want int) {
		t.Helper()
		for range 200 {
			var n int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&n); err != nil {
				t.Fatalf("read waiters: %v", err)
			}
			if n >= want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("fewer than %d sessions are waiting on a lock", want)
	}

	var wg sync.WaitGroup
	var putErr, rmErr error
	wg.Add(2)
	go func() { defer wg.Done(); _, putErr = roles.SetRoleMembers(c, "approver", []string{admin, target}) }()
	waiting(1)
	go func() { defer wg.Done(); _, rmErr = store.RemoveMember(c, target) }()
	waiting(2)
	if err := hold.Commit(ctx); err != nil {
		t.Fatalf("release the target row: %v", err)
	}
	wg.Wait()

	if rmErr != nil {
		t.Errorf("RemoveMember: %v", rmErr)
	}
	if putErr != nil && !errors.Is(putErr, approval.ErrValidation) {
		t.Errorf("SetRoleMembers: %v, want nil or ErrValidation", putErr)
	}
	if img := imageOf(t, super, tenant, target); img.Status != "removed" || img.Roles != 0 {
		t.Errorf("target = %+v, want removed with no grants", img)
	}
}
