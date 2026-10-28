package tenancy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const roleChangedEvent = "membership.role_changed"

func (w invWorld) setRole(caller, target, role string) (MemberAccess, error) {
	return w.store.SetMemberRole(w.as(caller), target, role)
}

func (w invWorld) storedRole(t *testing.T, user string) string {
	t.Helper()
	return imageOf(t, w.super, w.tenant, user).Role
}

func (w invWorld) activeAdmins(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND role = 'admin' AND status = 'active'`, w.tenant).Scan(&n); err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	return n
}

// failMembershipUpdate makes any UPDATE of the user's row raise, so a call that must write nothing proves it.
func failMembershipUpdate(t *testing.T, super *pgxpool.Pool, user string) {
	t.Helper()
	name := "qa_no_update_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(context.Background(), fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced update failure' USING ERRCODE = 'check_violation'; END; $$`, name)); err != nil {
		t.Fatalf("create the update-failure function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name))
	})
	if _, err := super.Exec(context.Background(), fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE UPDATE ON memberships FOR EACH ROW
		 WHEN (OLD.user_id = %s::uuid) EXECUTE FUNCTION %s()`, name, quoteLiteral(user), name)); err != nil {
		t.Fatalf("create the update-failure trigger: %v", err)
	}
}

func TestSetMemberRole_AdminChangesAMembersRole(t *testing.T) {
	w := newInvWorld(t, "role change "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")

	for _, step := range []string{"reviewer", "admin", "preparer"} {
		got, err := w.setRole(w.admin, prep, step)
		if err != nil {
			t.Fatalf("set %s: %v", step, err)
		}
		if got.UserID != prep || got.Role != step || got.Status != "active" {
			t.Errorf("answer after %s = %s/%s/%s, want %s/%s/active", step, got.UserID, got.Role, got.Status, prep, step)
		}
		if stored := w.storedRole(t, prep); stored != step {
			t.Errorf("stored role = %q, want %q", stored, step)
		}
		requireSameAsList(t, w, prep, got)
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 3 {
		t.Errorf("%s rows = %d, want 3", roleChangedEvent, n)
	}
}

func TestSetMemberRole_NonAdminRefusedAlikeForEveryTarget(t *testing.T) {
	w := newInvWorld(t, "role nonadmin A "+uuid.NewString()[:8], "Ada Admin")
	b := newInvWorld(t, "role nonadmin B "+uuid.NewString()[:8], "Bea Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	member := w.addMember(t, "preparer", "active", "Mo Member")
	bMember := b.addMember(t, "reviewer", "active", "Bo Member")
	before := snapshot(t, w.super, w.tenant, b.tenant)
	memberBefore, bBefore := imageOf(t, w.super, w.tenant, member), imageOf(t, w.super, b.tenant, bMember)

	for _, caller := range []string{prep, rev} {
		for name, target := range map[string]string{
			"an unknown uuid": uuid.NewString(), "another tenant's member": bMember,
			"self": caller, "the admin": w.admin, "a member": member,
		} {
			if _, err := w.setRole(caller, target, "admin"); !errors.Is(err, ErrRoleChangeNotPermitted) {
				t.Errorf("caller %s, target %s: err = %v, want ErrRoleChangeNotPermitted", caller, name, err)
			}
		}
	}
	if snapshot(t, w.super, w.tenant, b.tenant) != before ||
		imageOf(t, w.super, w.tenant, member) != memberBefore || imageOf(t, w.super, b.tenant, bMember) != bBefore {
		t.Error("a refused role change wrote to a tenant")
	}
	if prepRole, revRole := w.storedRole(t, prep), w.storedRole(t, rev); prepRole != "preparer" || revRole != "reviewer" {
		t.Errorf("callers' roles = %s/%s, want unchanged", prepRole, revRole)
	}
	if n := w.auditCountOf(t, roleChangedEvent) + b.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, roleChangedEvent)
	}
	if _, err := w.setRole(w.admin, member, "reviewer"); err != nil {
		t.Errorf("control: admin sets the same member's role: %v", err)
	}
}

func TestSetMemberRole_SuspendedAdminCallerIsNotActiveMember(t *testing.T) {
	w := newInvWorld(t, "role suspended "+uuid.NewString()[:8], "Ada Admin")
	susp := w.addMember(t, "admin", "suspended", "Sue Pend")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	before := snapshot(t, w.super, w.tenant)

	for name, caller := range map[string]string{"a suspended admin": susp, "no membership at all": uuid.NewString()} {
		for tname, target := range map[string]string{"a member": prep, "an unknown uuid": uuid.NewString()} {
			_, err := w.setRole(caller, target, "reviewer")
			if !errors.Is(err, db.ErrNotActiveMember) {
				t.Errorf("%s, %s: err = %v, want db.ErrNotActiveMember", name, tname, err)
			}
		}
	}
	if snapshot(t, w.super, w.tenant) != before || w.storedRole(t, prep) != "preparer" {
		t.Error("a refused role change wrote to the tenant")
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, roleChangedEvent)
	}
	if _, err := w.setRole(w.admin, prep, "reviewer"); err != nil {
		t.Errorf("control: an active admin sets the role: %v", err)
	}
}

func TestSetMemberRole_UnknownRoleRefused(t *testing.T) {
	w := newInvWorld(t, "role unknown "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	before := imageOf(t, w.super, w.tenant, prep)

	for _, role := range []string{"owner", "", "Admin", "ADMIN", " admin", "admin "} {
		if _, err := w.setRole(w.admin, prep, role); !errors.Is(err, ErrInvalidRole) {
			t.Errorf("role %q: err = %v, want ErrInvalidRole", role, err)
		}
	}
	// The vocabulary check precedes the caller check (A6).
	if _, err := w.setRole(rev, uuid.NewString(), "owner"); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("non-admin caller, role owner: err = %v, want ErrInvalidRole", err)
	}
	if imageOf(t, w.super, w.tenant, prep) != before {
		t.Error("a refused role changed the row")
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, roleChangedEvent)
	}
	if _, err := w.setRole(w.admin, prep, "reviewer"); err != nil {
		t.Errorf("control: a valid role: %v", err)
	}
}

func TestSetMemberRole_InvitedAndRemovedTargetsRefused(t *testing.T) {
	w := newInvWorld(t, "role invited removed "+uuid.NewString()[:8], "Ada Admin")
	invited := w.addMember(t, "preparer", "invited", "Ivy Invited")
	removed := w.removedRow(t, "Rem Oved")
	invBefore, remBefore := imageOf(t, w.super, w.tenant, invited), imageOf(t, w.super, w.tenant, removed)

	// "preparer" is the stored role: the status guards precede the same-role no-op (A6).
	for _, role := range []string{"reviewer", "preparer"} {
		if _, err := w.setRole(w.admin, invited, role); !errors.Is(err, ErrInvitedNotTransitionable) {
			t.Errorf("invited, %s: err = %v, want ErrInvitedNotTransitionable", role, err)
		}
		if _, err := w.setRole(w.admin, removed, role); !errors.Is(err, ErrRemovedNotTransitionable) {
			t.Errorf("removed, %s: err = %v, want ErrRemovedNotTransitionable", role, err)
		}
	}
	if imageOf(t, w.super, w.tenant, invited) != invBefore || imageOf(t, w.super, w.tenant, removed) != remBefore {
		t.Error("a refused role change wrote to the row")
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, roleChangedEvent)
	}
	if _, err := w.setRole(w.admin, w.addMember(t, "preparer", "active", "Pia"), "reviewer"); err != nil {
		t.Errorf("control: an active member: %v", err)
	}
}

func TestSetMemberRole_UnknownOrCrossTenantIsNotFound(t *testing.T) {
	w := newInvWorld(t, "role not found "+uuid.NewString()[:8], "Ada Admin")
	other := newInvWorld(t, "role not found other "+uuid.NewString()[:8], "Zed Okoro")
	cross := other.addMember(t, "reviewer", "active", "Tunde Bello")
	before := imageOf(t, w.super, other.tenant, cross)

	for name, target := range map[string]string{"unknown": uuid.NewString(), "cross-tenant": cross} {
		if _, err := w.setRole(w.admin, target, "preparer"); !errors.Is(err, ErrMembershipNotFound) {
			t.Errorf("%s: err = %v, want ErrMembershipNotFound", name, err)
		}
	}
	if imageOf(t, w.super, other.tenant, cross) != before {
		t.Error("a cross-tenant role change wrote to the other tenant's row")
	}
	if n := other.auditCountOf(t, roleChangedEvent) + w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, roleChangedEvent)
	}
	if _, err := w.setRole(w.admin, w.addMember(t, "reviewer", "active", "Rex"), "preparer"); err != nil {
		t.Errorf("control: a member of the caller's tenant: %v", err)
	}
}

func TestSetMemberRole_LastActiveAdminDemotionRefused(t *testing.T) {
	w := newInvWorld(t, "role last admin "+uuid.NewString()[:8], "Ada Admin")
	w.addMember(t, "admin", "suspended", "Sue Pend")
	giveClients(t, w.super, w.tenant, w.admin, seedEntity(t, w.super, w.tenant))
	before := imageOf(t, w.super, w.tenant, w.admin)
	if before.Clients != 1 {
		t.Fatalf("fixture: admin holds %d client rows, want 1", before.Clients)
	}

	for _, role := range []string{"preparer", "reviewer"} {
		if _, err := w.setRole(w.admin, w.admin, role); !errors.Is(err, ErrLastActiveAdmin) {
			t.Errorf("demote to %s: err = %v, want ErrLastActiveAdmin", role, err)
		}
	}
	if imageOf(t, w.super, w.tenant, w.admin) != before {
		t.Error("the refused demotion changed the sole admin or its client rows")
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", roleChangedEvent, n)
	}
	other := w.addMember(t, "admin", "active", "Bo Admin")
	if _, err := w.setRole(w.admin, other, "reviewer"); err != nil {
		t.Errorf("control: demoting a non-last admin: %v", err)
	}
}

func TestSetMemberRole_SuspendedAdminTargetDemotable(t *testing.T) {
	w := newInvWorld(t, "role suspended target "+uuid.NewString()[:8], "Ada Admin")
	susp := w.addMember(t, "admin", "suspended", "Sue Pend")

	got, err := w.setRole(w.admin, susp, "reviewer")
	if err != nil {
		t.Fatalf("set reviewer on a suspended admin: %v", err)
	}
	if got.Role != "reviewer" || got.Status != "suspended" {
		t.Errorf("answer = %s/%s, want reviewer/suspended", got.Role, got.Status)
	}
	if w.storedRole(t, susp) != "reviewer" || w.activeAdmins(t) != 1 {
		t.Errorf("stored role = %q, active admins = %d, want reviewer and 1", w.storedRole(t, susp), w.activeAdmins(t))
	}
}

func TestSetMemberRole_ConcurrentDemotionsOfTheLastTwoAdmins(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	for round := range 8 {
		w := invWorld{super: super, app: app, store: store, tenant: seedTenant(t, super, "role concurrent demotions")}
		x, y := w.addMember(t, "admin", "active", "X Admin"), w.addMember(t, "admin", "active", "Y Admin")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = store.SetMemberRole(w.as(x), y, "preparer") }()
		go func() { defer wg.Done(); <-start; _, errs[1] = store.SetMemberRole(w.as(y), x, "preparer") }()
		close(start)
		wg.Wait()

		oks := 0
		for i, err := range errs {
			switch {
			case err == nil:
				oks++
			case errors.Is(err, ErrLastActiveAdmin), errors.Is(err, ErrRoleChangeNotPermitted):
			default:
				t.Fatalf("round %d: errs[%d] = %v, want nil, ErrLastActiveAdmin or ErrRoleChangeNotPermitted (a deadlock is a defect)", round, i, err)
			}
		}
		if oks != 1 {
			t.Fatalf("round %d: results = %v, want exactly one success", round, errs)
		}
		var admins, active int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FILTER (WHERE role = 'admin'), count(*) FILTER (WHERE role = 'admin' AND status = 'active')
			   FROM memberships WHERE tenant_id = $1`, w.tenant).Scan(&admins, &active); err != nil {
			t.Fatalf("round %d: count admins: %v", round, err)
		}
		if admins != 1 || active != 1 {
			t.Fatalf("round %d: admins = %d / %d active, want 1 / 1", round, admins, active)
		}
	}
}

func TestSetMemberRole_DemotionRacesASuspension(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	for round := range 8 {
		w := invWorld{super: super, app: app, store: store, tenant: seedTenant(t, super, "role demotion vs suspension")}
		a, b := w.addMember(t, "admin", "active", "A Admin"), w.addMember(t, "admin", "active", "B Admin")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = store.SetMemberRole(w.as(a), b, "reviewer") }()
		go func() { defer wg.Done(); <-start; _, errs[1] = store.SetMembershipStatus(w.as(b), a, "suspended") }()
		close(start)
		wg.Wait()

		oks := 0
		for i, err := range errs {
			switch {
			case err == nil:
				oks++
			case errors.Is(err, ErrLastActiveAdmin), errors.Is(err, ErrRoleChangeNotPermitted),
				errors.Is(err, ErrNotPermitted), errors.Is(err, db.ErrNotActiveMember):
			default:
				t.Fatalf("round %d: errs[%d] = %v, want nil or a last-admin / not-permitted refusal (a deadlock is a defect)", round, i, err)
			}
		}
		if oks != 1 {
			t.Fatalf("round %d: results = %v, want exactly one success", round, errs)
		}
		if n := w.activeAdmins(t); n != 1 {
			t.Fatalf("round %d: active admins = %d, want 1", round, n)
		}
	}
}

func TestSetMemberRole_AdminDemotesSelfWhenAnotherAdminIsActive(t *testing.T) {
	w := newInvWorld(t, "role self demotion "+uuid.NewString()[:8], "Ada Admin")
	other := w.addMember(t, "admin", "active", "Bo Admin")

	got, err := w.setRole(w.admin, w.admin, "reviewer")
	if err != nil {
		t.Fatalf("self-demotion: %v", err)
	}
	if got.UserID != w.admin || got.Role != "reviewer" {
		t.Errorf("answer = %s/%s, want the caller as reviewer", got.UserID, got.Role)
	}
	if w.storedRole(t, w.admin) != "reviewer" || w.storedRole(t, other) != "admin" || mustStatus(t, w.super, other) != "active" {
		t.Error("stored roles after the self-demotion: want caller reviewer, other still an active admin")
	}
	if _, err := w.setRole(w.admin, other, "preparer"); !errors.Is(err, ErrRoleChangeNotPermitted) {
		t.Errorf("the demoted caller's next change: err = %v, want ErrRoleChangeNotPermitted", err)
	}
}

func TestSetMemberRole_FirmDemotionGivesEveryClient(t *testing.T) {
	w := newInvWorld(t, "role firm demotion "+uuid.NewString()[:8], "Ada Admin")
	b, c := w.addMember(t, "admin", "active", "Bo Admin"), w.addMember(t, "admin", "active", "Cy Admin")
	stale := []string{seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)}
	giveClients(t, w.super, w.tenant, b, stale...)
	giveClients(t, w.super, w.tenant, c, stale[0])
	if imageOf(t, w.super, w.tenant, b).Clients != 2 || imageOf(t, w.super, w.tenant, c).AllClients {
		t.Fatalf("fixture: want b with 2 client rows and c with all_clients false")
	}

	for _, tc := range []struct{ user, role string }{{b, "preparer"}, {c, "reviewer"}} {
		got, err := w.setRole(w.admin, tc.user, tc.role)
		if err != nil {
			t.Fatalf("demote %s to %s: %v", tc.user, tc.role, err)
		}
		w.requireStored(t, tc.role, tc.user, true)
		requireAccess(t, tc.role, wireRows(t, []MemberAccess{got})[tc.user], `"all"`)
		requireSameAsList(t, w, tc.user, got)
	}
}

func TestSetMemberRole_InHouseDemotionLeavesClientAccessAlone(t *testing.T) {
	h := inHouseWorld(t)
	other := h.addMember(t, "admin", "active", "Bo Admin")

	got, err := h.setRole(h.admin, other, "preparer")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if got.Role != "preparer" || got.ClientAccess != nil {
		t.Errorf("answer = %s with client_access %v, want preparer with nil", got.Role, got.ClientAccess)
	}
	h.requireStored(t, "in-house demotion", other, false)
}

func TestSetMemberRole_NonAdminChangeKeepsTheClientSet(t *testing.T) {
	w := newInvWorld(t, "role keeps clients "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, prep, e1)
	giveClients(t, w.super, w.tenant, rev, e2)

	if _, err := w.setRole(w.admin, prep, "reviewer"); err != nil {
		t.Fatalf("preparer to reviewer: %v", err)
	}
	w.requireStored(t, "preparer to reviewer", prep, false, e1)
	// A promotion is not a demotion (A8).
	if _, err := w.setRole(w.admin, rev, "admin"); err != nil {
		t.Fatalf("reviewer to admin: %v", err)
	}
	w.requireStored(t, "reviewer to admin", rev, false, e2)
	if _, err := w.setRole(w.admin, prep, "preparer"); err != nil {
		t.Fatalf("reviewer back to preparer: %v", err)
	}
	w.requireStored(t, "reviewer to preparer", prep, false, e1)
}

func TestSetMemberRole_AuditsInTheSameTransaction(t *testing.T) {
	w := newInvWorld(t, "role audit "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")

	if _, err := w.setRole(w.admin, prep, "reviewer"); err != nil {
		t.Fatalf("set: %v", err)
	}
	rows := auditPayloads(t, w.super, w.tenant, roleChangedEvent)
	if len(rows) != 1 {
		t.Fatalf("%s rows = %d, want 1", roleChangedEvent, len(rows))
	}
	keys := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"from", "to", "user_id"}) {
		t.Errorf("payload keys = %v, want exactly [from to user_id]", keys)
	}
	if rows[0]["from"] != "preparer" || rows[0]["to"] != "reviewer" || rows[0]["user_id"] != prep {
		t.Errorf("payload = %v, want from preparer, to reviewer, user_id %s", rows[0], prep)
	}
	if got := auditActor(t, w.app, w.tenant, roleChangedEvent); got != w.admin {
		t.Errorf("actor = %q, want the caller %q", got, w.admin)
	}

	// A non-HTTP caller's upper-case id is audited as the row's canonical id.
	if _, err := w.setRole(w.admin, strings.ToUpper(prep), "admin"); err != nil {
		t.Fatalf("set with an upper-case id: %v", err)
	}
	rows = auditPayloads(t, w.super, w.tenant, roleChangedEvent)
	if len(rows) != 2 || rows[1]["user_id"] != prep {
		t.Errorf("payloads = %v, want a second row with the canonical user_id %s", rows, prep)
	}
}

func TestSetMemberRole_FailedAuditRollsBack(t *testing.T) {
	w := newInvWorld(t, "role audit rollback "+uuid.NewString()[:8], "Ada Admin")
	m := w.addMember(t, "admin", "active", "Bo Admin")
	giveClients(t, w.super, w.tenant, m, seedEntity(t, w.super, w.tenant))
	before := imageOf(t, w.super, w.tenant, m)
	if before.Clients != 1 || before.AllClients {
		t.Fatalf("fixture: member = %+v, want 1 client row and all_clients false", before)
	}
	drop := failAudit(t, w.super, w.tenant, roleChangedEvent)

	if _, err := w.setRole(w.admin, m, "preparer"); pgCode(err) != "23514" {
		t.Fatalf("err = %v (SQLSTATE %q), want the forced audit failure 23514", err, pgCode(err))
	}
	if after := imageOf(t, w.super, w.tenant, m); after != before {
		t.Errorf("row after a failed audit = %+v, want rolled back to %+v", after, before)
	}

	drop()
	if _, err := w.setRole(w.admin, m, "preparer"); err != nil {
		t.Fatalf("retry after the trigger is gone: %v", err)
	}
	if after := imageOf(t, w.super, w.tenant, m); after.Role != "preparer" || !after.AllClients || after.Clients != 0 {
		t.Errorf("row after the retry = %+v, want preparer, all_clients, no client rows", after)
	}
}

func TestSetMemberRole_SameRoleIsANoOp(t *testing.T) {
	w := newInvWorld(t, "role same "+uuid.NewString()[:8], "Ada Admin")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	failMembershipUpdate(t, w.super, rev)
	failMembershipUpdate(t, w.super, w.admin)

	for _, tc := range []struct{ name, target, role string }{
		{"reviewer to reviewer", rev, "reviewer"},
		{"the sole admin to admin", w.admin, "admin"},
	} {
		got, err := w.setRole(w.admin, tc.target, tc.role)
		if err != nil {
			t.Fatalf("%s: %v (an UPDATE ran, or the call was refused)", tc.name, err)
		}
		if got.UserID != tc.target || got.Role != tc.role {
			t.Errorf("%s: answer = %s/%s, want the row as stored", tc.name, got.UserID, got.Role)
		}
		requireSameAsList(t, w, tc.target, got)
	}
	if n := w.auditCountOf(t, roleChangedEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", roleChangedEvent, n)
	}
	if _, err := w.setRole(w.admin, rev, "preparer"); err == nil {
		t.Error("control: a real change succeeded although the update trigger forbids it")
	}
}
