package tenancy

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func ids(a ...string) *ClientAccess { return &ClientAccess{IDs: a} }

func (w invWorld) create(emails []string, role string, access *ClientAccess) ([]IssuedInvite, error) {
	return w.store.CreateInvitations(w.adminCtx(), emails, role, access)
}

func (w invWorld) inviteFlag(t *testing.T, id string) bool {
	t.Helper()
	var all bool
	if err := w.super.QueryRow(context.Background(), `SELECT all_clients FROM invitations WHERE id = $1`, id).Scan(&all); err != nil {
		t.Fatalf("read all_clients of %s: %v", id, err)
	}
	return all
}

func (w invWorld) requireInvite(t *testing.T, who, id string, wantAll bool, wantIDs ...string) {
	t.Helper()
	got := inviteClientRows(t, w.super, id)
	want := sorted(wantIDs...)
	if all := w.inviteFlag(t, id); all != wantAll || !slices.Equal(got, want) {
		t.Errorf("%s: flag=%v rows=%v, want flag=%v rows=%v", who, all, got, wantAll, want)
	}
}

func TestInvitations_CreateStoresTheClientSet(t *testing.T) {
	w := newInvWorld(t, "Create Set Tenant", "Ada Obi")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)

	first, err := w.create([]string{"a@x.test", "b@x.test"}, "preparer", ids(e2, e1))
	if err != nil || len(first) != 2 {
		t.Fatalf("create with a set: %d invites, err %v", len(first), err)
	}
	for _, inv := range first {
		w.requireInvite(t, "set "+inv.Email, inv.ID, false, e1, e2)
	}

	got, err := w.create([]string{"c@x.test", "d@x.test"}, "reviewer", &ClientAccess{All: true})
	if err != nil || len(got) != 2 {
		t.Fatalf("create with all: %d invites, err %v", len(got), err)
	}
	for _, inv := range got {
		w.requireInvite(t, "all "+inv.Email, inv.ID, true)
	}

	for name, access := range map[string]*ClientAccess{"nil": nil, "an empty set": ids()} {
		email := name[:2] + "@x.test"
		got, err = w.create([]string{email}, "preparer", access)
		if err != nil || len(got) != 1 {
			t.Fatalf("create with %s: %d invites, err %v", name, len(got), err)
		}
		w.requireInvite(t, name, got[0].ID, false)
	}
	// The first batch keeps its rows after the later batches.
	for _, inv := range first {
		w.requireInvite(t, "first batch "+inv.Email, inv.ID, false, e1, e2)
	}
	if n := countRows(t, w, "invitation_clients"); n != 4 {
		t.Errorf("invitation_clients rows = %d, want 4 (two invites x two clients)", n)
	}
}

func countRows(t *testing.T, w invWorld, table string) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, w.tenant).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestInvitations_ReissueReplacesTheClientSet(t *testing.T) {
	w := newInvWorld(t, "Reissue Set Tenant", "Ada Obi")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)

	first, err := w.create([]string{"a@x.test"}, "preparer", ids(e1))
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	id := first[0].ID
	w.requireInvite(t, "first", id, false, e1)

	steps := []struct {
		name   string
		role   string
		access *ClientAccess
		all    bool
		ids    []string
	}{
		{"another set", "preparer", ids(e2), false, []string{e2}},
		{"all clients", "reviewer", &ClientAccess{All: true}, true, nil},
		{"a set clears the flag", "reviewer", ids(e1, e2), false, []string{e1, e2}},
		{"no key", "reviewer", nil, false, nil},
		{"all again", "preparer", &ClientAccess{All: true}, true, nil},
		{"no key clears the flag", "preparer", nil, false, nil},
	}
	for _, s := range steps {
		got, err := w.create([]string{"a@x.test"}, s.role, s.access)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %d invites, err %v", s.name, len(got), err)
		}
		if got[0].ID != id {
			t.Errorf("%s: invite id %s, want %s unchanged", s.name, got[0].ID, id)
		}
		w.requireInvite(t, s.name, id, s.all, s.ids...)
	}
	if n := len(invRows(t, w.super, w.tenant, "status = 'pending'")); n != 1 {
		t.Errorf("pending invites = %d, want 1", n)
	}
}

func TestInvitations_CreateRefusesClientAccessInHouse(t *testing.T) {
	w := inHouseWorld(t)
	for name, access := range map[string]*ClientAccess{"an empty set": ids(), "all": {All: true}} {
		if _, err := w.create([]string{"a@x.test"}, "preparer", access); !errors.Is(err, ErrClientAccessNotFirm) {
			t.Errorf("%s: err = %v, want ErrClientAccessNotFirm", name, err)
		}
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Errorf("invites after the refusals = %d, want 0", n)
	}
	if n := w.auditCountOf(t, "invitation.sent"); n != 0 {
		t.Errorf("invitation.sent rows after the refusals = %d, want 0", n)
	}
	if n := countRows(t, w, "invitation_clients"); n != 0 {
		t.Errorf("invitation_clients rows = %d, want 0", n)
	}

	// Control: no choice still invites in an in-house workspace.
	if got, err := w.create([]string{"a@x.test"}, "preparer", nil); err != nil || len(got) != 1 {
		t.Fatalf("create with no choice: %d invites, err %v", len(got), err)
	}
	if n := w.auditCountOf(t, "invitation.sent"); n != 1 {
		t.Errorf("invitation.sent rows after the control = %d, want 1", n)
	}
}

func TestInvitations_CreateRefusesAForeignClient(t *testing.T) {
	w := newInvWorld(t, "Foreign Client A", "Ada Obi")
	b := newInvWorld(t, "Foreign Client B", "Bea Eze")
	e1 := seedEntity(t, w.super, w.tenant)
	x := seedEntity(t, b.super, b.tenant)

	for name, access := range map[string]*ClientAccess{
		"a foreign client among valid ones": ids(e1, x),
		"a foreign client alone":            ids(x),
		"an unknown uuid":                   ids(uuid.NewString()),
	} {
		if _, err := w.create([]string{"a@x.test", "b@x.test"}, "preparer", access); !errors.Is(err, ErrUnknownClient) {
			t.Errorf("%s: err = %v, want ErrUnknownClient", name, err)
		}
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Errorf("invites after the refusals = %d, want 0", n)
	}
	if n := w.auditCountOf(t, "invitation.sent"); n != 0 {
		t.Errorf("invitation.sent rows after the refusals = %d, want 0", n)
	}
	if n := countRows(t, w, "invitation_clients"); n != 0 {
		t.Errorf("invitation_clients rows = %d, want 0", n)
	}
	if n := countRows(t, b, "invitation_clients"); n != 0 {
		t.Errorf("the other tenant's invitation_clients rows = %d, want 0", n)
	}

	// A refused batch spends no daily-limit slot: all 20 fit afterwards.
	if got, err := w.create(addrs("fit", 20), "preparer", nil); err != nil || len(got) != 20 {
		t.Fatalf("20-invite batch after the refusals: %d invites, err %v", len(got), err)
	}
}

func TestInvitations_CreateRefusalKeepsTheReissuedInvitesSet(t *testing.T) {
	w := newInvWorld(t, "Refused Reissue", "Ada Obi")
	b := newInvWorld(t, "Refused Reissue Other", "Bea Eze")
	e1 := seedEntity(t, w.super, w.tenant)
	x := seedEntity(t, b.super, b.tenant)
	first, err := w.create([]string{"a@x.test"}, "preparer", ids(e1))
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	before := pendingRow(t, w.super, w.tenant, "a@x.test")

	if _, err := w.create([]string{"a@x.test"}, "reviewer", ids(x)); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("reissue with a foreign client: err = %v, want ErrUnknownClient", err)
	}
	after := pendingRow(t, w.super, w.tenant, "a@x.test")
	if after.ID != first[0].ID || after.Role != "preparer" || string(after.Hash) != string(before.Hash) {
		t.Errorf("the pending invite changed in a refused reissue: %+v -> %+v", before, after)
	}
	w.requireInvite(t, "the old set", first[0].ID, false, e1)
	if n := w.auditCountOf(t, "invitation.sent"); n != 1 {
		t.Errorf("invitation.sent rows = %d, want only the first", n)
	}
}

func TestInvitations_CreateAcceptsAnArchivedClient(t *testing.T) {
	w := newInvWorld(t, "Archived Client Tenant", "Ada Obi")
	e1 := seedEntity(t, w.super, w.tenant)
	if _, err := w.super.Exec(context.Background(), `UPDATE business_entities SET status = 'archived' WHERE id = $1`, e1); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, err := w.create([]string{"a@x.test"}, "preparer", ids(e1))
	if err != nil || len(got) != 1 {
		t.Fatalf("create with an archived client: %d invites, err %v", len(got), err)
	}
	w.requireInvite(t, "the invite", got[0].ID, false, e1)

	user := uuid.NewString()
	if _, _, _, err := w.store.AcceptInvitation(tenantless(user, "a@x.test"), got[0].Token, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	w.requireStored(t, "the member", user, false, e1)
}

func TestInvitations_CreateThenAcceptCopiesTheChoice(t *testing.T) {
	w := newInvWorld(t, "Create Accept Tenant", "Ada Obi")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	set, err := w.create([]string{"s@x.test"}, "reviewer", ids(e1, e2))
	if err != nil {
		t.Fatalf("invite with a set: %v", err)
	}
	every, err := w.create([]string{"e@x.test"}, "preparer", &ClientAccess{All: true})
	if err != nil {
		t.Fatalf("invite with all: %v", err)
	}
	none, err := w.create([]string{"n@x.test"}, "preparer", nil)
	if err != nil {
		t.Fatalf("invite with no choice: %v", err)
	}

	for _, c := range []struct {
		inv   IssuedInvite
		all   bool
		wants []string
	}{{set[0], false, []string{e1, e2}}, {every[0], true, nil}, {none[0], false, nil}} {
		user := uuid.NewString()
		if _, _, _, err := w.store.AcceptInvitation(tenantless(user, c.inv.Email), c.inv.Token, nil); err != nil {
			t.Fatalf("accept %s: %v", c.inv.Email, err)
		}
		w.requireStored(t, c.inv.Email, user, c.all, c.wants...)
	}
}

func TestInvitations_ALaterInviteDoesNotInheritAnOlderInvitesSet(t *testing.T) {
	w := newInvWorld(t, "Later Invite Tenant", "Ada Obi")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	accept := func(inv IssuedInvite) string {
		t.Helper()
		user := uuid.NewString()
		if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inv.Email), inv.Token, nil); err != nil {
			t.Fatalf("accept %s: %v", inv.Email, err)
		}
		return user
	}

	// Revoked, then invited again with no choice: the new member gets nothing.
	old, err := w.create([]string{"r@x.test"}, "preparer", ids(e1))
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	if err := w.store.RevokeInvitation(w.adminCtx(), old[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	fresh, err := w.create([]string{"r@x.test"}, "preparer", nil)
	if err != nil || fresh[0].ID == old[0].ID {
		t.Fatalf("re-invite after the revoke: %v, same id %v", err, len(fresh) == 1 && fresh[0].ID == old[0].ID)
	}
	w.requireStored(t, "member of the re-invite", accept(fresh[0]), false)
	w.requireInvite(t, "the revoked invite keeps its rows", old[0].ID, false, e1)

	// Reissued: the old link is dead and the new one carries the new set.
	first, err := w.create([]string{"s@x.test"}, "preparer", ids(e1))
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	second, err := w.create([]string{"s@x.test"}, "preparer", ids(e2))
	if err != nil {
		t.Fatalf("reissue: %v", err)
	}
	stale := uuid.NewString()
	if _, _, _, err := w.store.AcceptInvitation(tenantless(stale, "s@x.test"), first[0].Token, nil); !errors.Is(err, ErrInvitationNotValid) {
		t.Fatalf("the replaced link err = %v, want ErrInvitationNotValid", err)
	}
	if got := len(membersOf(t, w.super, stale)); got != 0 {
		t.Errorf("the replaced link left %d memberships, want 0", got)
	}
	w.requireStored(t, "member of the reissue", accept(second[0]), false, e2)

	// An admin reissue with no choice drops the earlier set.
	if _, err := w.create([]string{"t@x.test"}, "reviewer", ids(e1)); err != nil {
		t.Fatalf("invite: %v", err)
	}
	adm, err := w.create([]string{"t@x.test"}, "admin", nil)
	if err != nil {
		t.Fatalf("admin reissue: %v", err)
	}
	w.requireInvite(t, "admin reissue", adm[0].ID, false)
	w.requireStored(t, "the admin member", accept(adm[0]), false)
}

func TestInvitations_ABatchGivesNewAndReissuedInvitesTheSameSet(t *testing.T) {
	w := newInvWorld(t, "Mixed Batch Tenant", "Ada Obi")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	pending, err := w.create([]string{"a@x.test"}, "preparer", ids(e1))
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}

	got, err := w.create([]string{"a@x.test", "b@x.test", "c@x.test"}, "reviewer", ids(e2))
	if err != nil || len(got) != 3 {
		t.Fatalf("mixed batch: %d invites, err %v", len(got), err)
	}
	if got[0].ID != pending[0].ID {
		t.Errorf("the pending address got id %s, want %s reissued", got[0].ID, pending[0].ID)
	}
	for _, inv := range got {
		w.requireInvite(t, inv.Email, inv.ID, false, e2)
	}
	if n := countRows(t, w, "invitation_clients"); n != 3 {
		t.Errorf("invitation_clients rows = %d, want 3 (the old row replaced, not kept)", n)
	}
}

func TestAccept_AnInHouseInviteGivesNoClientAccess(t *testing.T) {
	w := inHouseWorld(t)
	got, err := w.create([]string{"in@x.test"}, "preparer", nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("invite: %d, err %v", len(got), err)
	}
	user := uuid.NewString()
	if _, _, _, err := w.store.AcceptInvitation(tenantless(user, "in@x.test"), got[0].Token, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	w.requireStored(t, "the in-house member", user, false)
	requireAccess(t, "list as admin", w.listAs(t, w.admin)[user], "null")
}

func TestAccept_TheSameAddressInTwoWorkspacesCopiesEachOwnSet(t *testing.T) {
	a := newInvWorld(t, "Same Address A", "Ada Obi")
	b := newInvWorld(t, "Same Address B", "Bea Eze")
	ea, eb := seedEntity(t, a.super, a.tenant), seedEntity(t, b.super, b.tenant)
	ia, err := a.create([]string{"both@x.test"}, "preparer", ids(ea))
	if err != nil {
		t.Fatalf("invite from A: %v", err)
	}
	ib, err := b.create([]string{"both@x.test"}, "reviewer", ids(eb))
	if err != nil {
		t.Fatalf("invite from B: %v", err)
	}

	user := uuid.NewString()
	tenant, _, _, err := b.store.AcceptInvitation(tenantless(user, "both@x.test"), ib[0].Token, nil)
	if err != nil || tenant.ID != b.tenant {
		t.Fatalf("accept B's invite: tenant %s, err %v", tenant.ID, err)
	}
	b.requireStored(t, "the member of B", user, false, eb)
	if got := memberClientRows(t, a.super, a.tenant); got != 0 {
		t.Errorf("membership_clients rows in A = %d, want 0", got)
	}
	a.requireInvite(t, "A's invite", ia[0].ID, false, ea)
	if got := memberClientRows(t, b.super, b.tenant); got != 1 {
		t.Errorf("membership_clients rows in B = %d, want 1", got)
	}
}

func TestInvitations_AllClientsStoresNoRowsEvenWithAListedSet(t *testing.T) {
	w := newInvWorld(t, "All Wins Tenant", "Ada Obi")
	e1 := seedEntity(t, w.super, w.tenant)

	got, err := w.create([]string{"a@x.test"}, "preparer", &ClientAccess{All: true, IDs: []string{e1}})
	if err != nil || len(got) != 1 {
		t.Fatalf("create: %d invites, err %v", len(got), err)
	}
	w.requireInvite(t, "the invite", got[0].ID, true)
}
