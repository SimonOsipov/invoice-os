package tenancy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// joinAddr is a per-test address, so a list holds only this test's invites.
func joinAddr(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] + "@obi.test"
}

// newJoinWorld is tenant "Obi Partners" with one live invite to a fresh address.
func newJoinWorld(t *testing.T, role string) (acceptWorld, string) {
	t.Helper()
	addr := joinAddr("tunde")
	w := newInvWorld(t, "Obi Partners", "Ada Obi")
	return acceptWorld{invWorld: w, invite: mustIssue(t, w.adminCtx(), w.store, []string{addr}, role)[0]}, addr
}

func setExpiry(t *testing.T, super *pgxpool.Pool, id string, at time.Time) {
	t.Helper()
	if _, err := super.Exec(context.Background(), `UPDATE invitations SET expires_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
}

// holdIdentityLock holds the user's identity advisory key; the returned func releases it.
func holdIdentityLock(t *testing.T, super *pgxpool.Pool, user string) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := super.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1::text, 0))`, user); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1::text, 0))`, user); err != nil {
			t.Errorf("unlock: %v", err)
		}
		conn.Release()
	}
	t.Cleanup(release)
	if n := identityLockRequests(t, super, user, true); n != 1 {
		t.Fatalf("pg_locks filter sees %d granted locks on the key, want the test's own 1", n)
	}
	return release
}

type joinResult struct {
	tenant Tenant
	err    error
}

// raceJoins starts one AcceptInvitationByID per id while the identity lock is held,
// waits until each is queued on it, releases, and returns the results.
func raceJoins(t *testing.T, w invWorld, user, addr string, ids ...string) []joinResult {
	t.Helper()
	release := holdIdentityLock(t, w.super, user)
	ch := make(chan joinResult, len(ids))
	for _, id := range ids {
		go func() {
			tenant, _, _, err := w.store.AcceptInvitationByID(tenantless(user, addr), id)
			ch <- joinResult{tenant, err}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for identityLockRequests(t, w.super, user, false) < len(ids) {
		select {
		case r := <-ch:
			t.Fatalf("a join returned while the identity lock was held (tenant %+v, err %v)", r.tenant, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pg_locks shows %d ungranted requests on the identity key after 5 s, want %d", identityLockRequests(t, w.super, user, false), len(ids))
		}
		time.Sleep(25 * time.Millisecond)
	}
	release()
	var out []joinResult
	for range ids {
		select {
		case r := <-ch:
			out = append(out, r)
		case <-time.After(15 * time.Second):
			t.Fatal("a join did not return after the lock was released")
		}
	}
	return out
}

func TestJoin_ListNamesEveryWorkspaceThatInvitedTheAddress(t *testing.T) {
	addr := joinAddr("tunde")
	obi := newInvWorld(t, "Obi Partners", "Ada Obi")
	eze := newInvWorld(t, "Eze Ltd", "Bola Eze")
	third := newInvWorld(t, "Third Co", "") // no display name: the inviter falls back to the email
	now := time.Now()
	iObi := mustIssue(t, obi.adminCtx(), obi.store, []string{addr}, "reviewer")[0]
	iEze := mustIssue(t, eze.adminCtx(), eze.store, []string{addr}, "preparer")[0]
	iThird := mustIssue(t, third.adminCtx(), third.store, []string{addr}, "admin")[0]
	// Distinct expiries, inserted out of order: three items tell an expiry order from an insertion order.
	setExpiry(t, obi.super, iObi.ID, now.Add(72*time.Hour))
	setExpiry(t, obi.super, iEze.ID, now.Add(24*time.Hour))
	setExpiry(t, obi.super, iThird.ID, now.Add(48*time.Hour))
	// Another address's invite must stay out.
	mustIssue(t, obi.adminCtx(), obi.store, []string{joinAddr("someone-else")}, "reviewer")

	got, err := obi.store.MyPendingInvitations(tenantless(uuid.NewString(), addr))
	if err != nil {
		t.Fatalf("MyPendingInvitations: %v", err)
	}
	type want struct {
		id, workspace, role, inviter string
		expires                      time.Time
	}
	wants := []want{
		{iEze.ID, "Eze Ltd", "preparer", "Bola Eze", now.Add(24 * time.Hour)},
		{iThird.ID, "Third Co", "admin", third.admin + "@members.test", now.Add(48 * time.Hour)},
		{iObi.ID, "Obi Partners", "reviewer", "Ada Obi", now.Add(72 * time.Hour)},
	}
	if len(got) != len(wants) {
		t.Fatalf("invites = %+v, want %d", got, len(wants))
	}
	for i, w := range wants {
		g := got[i]
		if g.ID != w.id || g.Workspace != w.workspace || g.Role != w.role {
			t.Errorf("item %d = %+v, want id %s workspace %q role %q", i, g, w.id, w.workspace, w.role)
		}
		if g.Inviter == nil || *g.Inviter != w.inviter {
			t.Errorf("item %d inviter = %v, want %q", i, g.Inviter, w.inviter)
		}
		near(t, "item "+g.Workspace+" expiry", g.ExpiresAt, w.expires, 5*time.Second)
	}
}

func TestJoin_ListUsesTheNormalisedHeaderEmail(t *testing.T) {
	addr := joinAddr("tunde")
	a := newInvWorld(t, "Obi Partners", "Ada Obi")
	b := newInvWorld(t, "Eze Ltd", "Bola Eze")
	c := newInvWorld(t, "Third Co", "Chi Okoro")
	ids := map[string]bool{
		mustIssue(t, a.adminCtx(), a.store, []string{addr}, "reviewer")[0].ID: true,
		mustIssue(t, b.adminCtx(), b.store, []string{addr}, "reviewer")[0].ID: true,
		mustIssue(t, c.adminCtx(), c.store, []string{addr}, "reviewer")[0].ID: true,
	}
	live := mustIssue(t, a.adminCtx(), a.store, []string{joinAddr("accept")}, "reviewer")[0]
	store, tr := tracedStore(t)

	padded := " " + strings.ToUpper(addr) + " "
	before := tr.count()
	got, err := store.MyPendingInvitations(tenantless(uuid.NewString(), padded))
	if err != nil {
		t.Fatalf("list as %q: %v", padded, err)
	}
	if len(got) != len(ids) {
		t.Fatalf("list as %q = %+v, want the %d invites", padded, got, len(ids))
	}
	for _, g := range got {
		if !ids[g.ID] {
			t.Errorf("list as %q holds %s, an invite of another address", padded, g.ID)
		}
	}
	if tr.count() == before {
		t.Fatal("the traced pool saw no statement for a valid list, so a zero count below proves nothing")
	}

	for _, bad := range []string{"not-an-address", "", "   ", "Tunde <" + addr + ">", "a@b@c.test"} {
		t.Run("list as "+bad, func(t *testing.T) {
			before := tr.count()
			got, err := store.MyPendingInvitations(tenantless(uuid.NewString(), bad))
			if err != nil || len(got) != 0 || got == nil {
				t.Errorf("list = %+v, err %v; want a non-nil empty list and no error", got, err)
			}
			if n := tr.count() - before; n != 0 {
				t.Errorf("sent %d statements, want none", n)
			}
		})
		t.Run("accept as "+bad, func(t *testing.T) {
			before := tr.count()
			_, _, _, err := store.AcceptInvitationByID(tenantless(uuid.NewString(), bad), live.ID)
			if !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("err = %v, want ErrInvitationNotValid", err)
			}
			if n := tr.count() - before; n != 0 {
				t.Errorf("sent %d statements, want none", n)
			}
		})
	}
}

func TestJoin_AcceptByIdJoinsWithTheInvitedRole(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()

	// An upper-case subject and a padded address: the rows hold the canonical forms.
	tenant, subject, role, err := w.store.AcceptInvitationByID(tenantless(strings.ToUpper(user), " "+strings.ToUpper(addr)+" "), w.invite.ID)
	if err != nil {
		t.Fatalf("AcceptInvitationByID: %v", err)
	}
	if tenant != (Tenant{ID: w.tenant, Name: "Obi Partners", Kind: "firm"}) {
		t.Errorf("tenant = %+v, want {%s Obi Partners firm}", tenant, w.tenant)
	}
	if subject != user || role != "reviewer" {
		t.Errorf("subject, role = %q, %q; want %q, reviewer", subject, role, user)
	}

	rows := membersOf(t, w.super, user)
	if len(rows) != 1 {
		t.Fatalf("memberships = %+v, want exactly one", rows)
	}
	if m := rows[0]; m.Tenant != w.tenant || m.Role != "reviewer" || m.Status != "active" || m.DisplayName != nil || m.Email == nil || *m.Email != addr {
		t.Errorf("membership = %+v, want (%s, reviewer, active, no name, %s)", m, w.tenant, addr)
	}
	requireStatus(t, w, "accepted")

	audits := acceptedAudits(t, w.super, w.tenant)
	if len(audits) != 1 {
		t.Fatalf("invitation.accepted rows = %d, want exactly 1", len(audits))
	}
	if audits[0].Actor != user || len(audits[0].Payload) != 2 || audits[0].Payload["invitation_id"] != w.invite.ID || audits[0].Payload["role"] != "reviewer" {
		t.Errorf("audit = %+v, want actor %s, exactly {invitation_id: %s, role: reviewer}", audits[0], user, w.invite.ID)
	}

	me := auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: w.tenant})
	rec := apiDo(MeHandler(w.store.Me, nil), me, http.MethodGet, "/v1/me", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), w.tenant) || !strings.Contains(rec.Body.String(), `"reviewer"`) {
		t.Errorf("/v1/me as the new identity = %d %s, want 200 naming the tenant and role", rec.Code, rec.Body)
	}
}

func TestJoin_AnotherAddressInAnyTenantIsNeitherListedNorAccepted(t *testing.T) {
	addr := joinAddr("tunde")
	t1 := newInvWorld(t, "Obi Partners", "Ada Obi")
	t2 := newInvWorld(t, "Eze Ltd", "Bola Eze")
	i1 := mustIssue(t, t1.adminCtx(), t1.store, []string{addr}, "reviewer")[0]
	i2 := mustIssue(t, t2.adminCtx(), t2.store, []string{addr}, "preparer")[0]
	unknown := uuid.NewString()

	for _, other := range []string{joinAddr("other"), strings.Replace(addr, "@", "x@", 1), "x" + addr} {
		t.Run(other, func(t *testing.T) {
			user := uuid.NewString()
			ctx := tenantless(user, other)
			got, err := t1.store.MyPendingInvitations(ctx)
			if err != nil || len(got) != 0 || got == nil {
				t.Errorf("list = %+v, err %v; want a non-nil empty list and no error", got, err)
			}
			for _, id := range []string{i1.ID, i2.ID, unknown} {
				if _, _, _, err := t1.store.AcceptInvitationByID(ctx, id); !errors.Is(err, ErrInvitationNotValid) {
					t.Errorf("accept %s: err = %v, want ErrInvitationNotValid", id, err)
				}
			}
			if rows := membersOf(t, t1.super, user); len(rows) != 0 {
				t.Errorf("memberships = %+v, want none", rows)
			}
		})
	}

	for tenant, inv := range map[string]IssuedInvite{t1.tenant: i1, t2.tenant: i2} {
		if rows := invRows(t, t1.super, tenant, "id = $2", inv.ID); len(rows) != 1 || rows[0].Status != "pending" {
			t.Errorf("invite %s rows = %+v, want one pending", inv.ID, rows)
		}
	}
	// Control: the addressee sees both, so the empty lists above are not an empty world.
	got, err := t1.store.MyPendingInvitations(tenantless(uuid.NewString(), addr))
	if err != nil || len(got) != 2 {
		t.Errorf("the addressee's list = %+v, err %v; want both invites", got, err)
	}
}

func TestJoin_ExpiredInviteIsNotListedAndNotAccepted(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()
	ctx := tenantless(user, addr)

	got, err := w.store.MyPendingInvitations(ctx)
	if err != nil || len(got) != 1 || got[0].ID != w.invite.ID {
		t.Fatalf("a live invite lists as %+v, err %v; want exactly it", got, err)
	}
	setExpiry(t, w.super, w.invite.ID, time.Now().Add(-time.Second))

	got, err = w.store.MyPendingInvitations(ctx)
	if err != nil || len(got) != 0 {
		t.Errorf("list after expiry = %+v, err %v; want empty", got, err)
	}
	if _, _, _, err := w.store.AcceptInvitationByID(ctx, w.invite.ID); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("accept after expiry: err = %v, want ErrInvitationNotValid", err)
	}
	requireNothingWritten(t, w, user, 0)
}

func TestJoin_AMemberGetsAlreadyMember(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	other := newInvWorld(t, "Other Firm", "Bola Eze")
	user := uuid.NewString()
	seedIdentityMembership(t, w.super, other.tenant, user, "admin", "suspended", nil, strp(addr))
	ctx := tenantless(user, addr) // a suspended-only member holds a tenant-less token

	_, _, _, err := w.store.AcceptInvitationByID(ctx, w.invite.ID)
	if !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("err = %v, want ErrAlreadyMember", err)
	}
	requireNothingWritten(t, w, user, 1)
	if m := membersOf(t, w.super, user)[0]; m.Tenant != other.tenant || m.Role != "admin" || m.Status != "suspended" {
		t.Errorf("membership changed to %+v", m)
	}

	if got, err := w.store.MyPendingInvitations(ctx); err != nil || len(got) != 0 {
		t.Errorf("a suspended member's list = %+v, err %v; want empty", got, err)
	}

	// A member probing an invite of another address learns nothing: not valid answers before member.
	probe := tenantless(user, joinAddr("probe"))
	if _, _, _, err := w.store.AcceptInvitationByID(probe, w.invite.ID); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("a member's probe of another address: err = %v, want ErrInvitationNotValid", err)
	}
}

func TestJoin_AnActiveMemberListsNothing(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	other := newInvWorld(t, "Other Firm", "Bola Eze")
	user := uuid.NewString()
	seedIdentityMembership(t, w.super, other.tenant, user, "admin", "active", nil, strp(addr))

	if got, err := w.store.MyPendingInvitations(tenantless(user, addr)); err != nil || len(got) != 0 {
		t.Errorf("an active member's list = %+v, err %v; want empty", got, err)
	}
}

func TestJoin_TenantBearingCallerSendsNoStatement(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	b := newInvWorld(t, "Other Firm", "Bola Eze")
	user := uuid.NewString()
	seedIdentityMembership(t, w.super, b.tenant, user, "admin", "active", nil, strp(addr))
	store, tr := tracedStore(t)
	bearing := auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: b.tenant, Email: addr})

	before := tr.count()
	if _, err := store.MyPendingInvitations(bearing); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("list: err = %v, want ErrAlreadyMember", err)
	}
	if _, _, _, err := store.AcceptInvitationByID(bearing, w.invite.ID); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("accept: err = %v, want ErrAlreadyMember", err)
	}
	if n := tr.count() - before; n != 0 {
		t.Errorf("a tenant-bearing caller sent %d statements, want none", n)
	}
	requireNothingWritten(t, w, user, 1)

	// Control: the same address as a tenant-less caller reaches the pool.
	before = tr.count()
	if got, err := store.MyPendingInvitations(tenantless(uuid.NewString(), addr)); err != nil || len(got) != 1 {
		t.Fatalf("control list = %+v, err %v; want the invite", got, err)
	}
	if tr.count() == before {
		t.Error("the traced pool saw no statement for a tenant-less list, so a zero count above proves nothing")
	}
}

func TestJoin_NoCallerOrANonUuidSubjectSendsNoStatement(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	store, tr := tracedStore(t)

	for name, ctx := range map[string]context.Context{
		"no caller":             context.Background(),
		"subject is not a uuid": tenantless("not-a-uuid", addr),
	} {
		t.Run(name, func(t *testing.T) {
			before := tr.count()
			if _, err := store.MyPendingInvitations(ctx); !errors.Is(err, db.ErrNoTenant) {
				t.Errorf("list: err = %v, want db.ErrNoTenant", err)
			}
			if _, _, _, err := store.AcceptInvitationByID(ctx, w.invite.ID); !errors.Is(err, db.ErrNoTenant) {
				t.Errorf("accept: err = %v, want db.ErrNoTenant", err)
			}
			if n := tr.count() - before; n != 0 {
				t.Errorf("sent %d statements, want none", n)
			}
		})
	}
	requireStatus(t, w, "pending")

	before := tr.count()
	if _, err := store.MyPendingInvitations(tenantless(uuid.NewString(), addr)); err != nil {
		t.Fatalf("control list: %v", err)
	}
	if tr.count() == before {
		t.Error("the traced pool saw no statement for a valid list, so a zero count above proves nothing")
	}
}

func TestJoin_AFailedAuditRollsTheJoinBack(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()
	drop := failAcceptAudit(t, w.super, w.tenant)

	_, _, _, err := w.store.AcceptInvitationByID(tenantless(user, addr), w.invite.ID)
	if err == nil || !strings.Contains(err.Error(), "forced audit failure") {
		t.Fatalf("err = %v, want the forced audit failure", err)
	}
	for _, s := range []error{ErrInvitationNotValid, ErrAlreadyMember} {
		if errors.Is(err, s) {
			t.Errorf("err = %v, want the audit failure, not the refusal %v", err, s)
		}
	}
	requireNothingWritten(t, w, user, 0)

	drop()
	if _, _, _, err := w.store.AcceptInvitationByID(tenantless(user, addr), w.invite.ID); err != nil {
		t.Errorf("accept after the trigger is gone: %v", err)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships after the retry = %d, want 1", got)
	}
}

func TestJoin_TwoInvitesJoinOnceUnderRace(t *testing.T) {
	addr := joinAddr("tunde")
	t1 := newInvWorld(t, "Obi Partners", "Ada Obi")
	t2 := newInvWorld(t, "Eze Ltd", "Bola Eze")
	i1 := mustIssue(t, t1.adminCtx(), t1.store, []string{addr}, "reviewer")[0]
	i2 := mustIssue(t, t2.adminCtx(), t2.store, []string{addr}, "preparer")[0]
	user := uuid.NewString()

	ok, member := 0, 0
	for _, r := range raceJoins(t, t1, user, addr, i1.ID, i2.ID) {
		switch {
		case r.err == nil && (r.tenant.ID == t1.tenant || r.tenant.ID == t2.tenant):
			ok++
		case errors.Is(r.err, ErrAlreadyMember):
			member++
		default:
			t.Errorf("unexpected result: tenant %+v, err %v", r.tenant, r.err)
		}
	}
	if ok != 1 || member != 1 {
		t.Errorf("successes/already-member = %d/%d, want 1/1", ok, member)
	}
	if got := len(membersOf(t, t1.super, user)); got != 1 {
		t.Errorf("memberships = %d, want 1", got)
	}
	if n := len(acceptedAudits(t, t1.super, t1.tenant)) + len(acceptedAudits(t, t1.super, t2.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func TestJoin_SameInviteTwoConcurrentAcceptsJoinOnce(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()

	ok, refused := 0, 0
	for _, r := range raceJoins(t, w.invWorld, user, addr, w.invite.ID, w.invite.ID) {
		switch {
		case r.err == nil && r.tenant.ID == w.tenant:
			ok++
		// The loser re-reads the invite under the lock: it is spent.
		case errors.Is(r.err, ErrInvitationNotValid):
			refused++
		default:
			t.Errorf("unexpected result: tenant %+v, err %v", r.tenant, r.err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("successes/refusals = %d/%d, want 1/1", ok, refused)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships = %d, want 1", got)
	}
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func TestJoin_TokenLinkAfterJoinByIdIsNoLongerValid(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()
	ctx := tenantless(user, addr)

	if _, _, _, err := w.store.AcceptInvitationByID(ctx, w.invite.ID); err != nil {
		t.Fatalf("join by id: %v", err)
	}
	if _, _, _, err := w.store.AcceptInvitation(ctx, w.invite.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("token accept after a join by id: err = %v, want ErrInvitationNotValid", err)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships = %d, want 1", got)
	}
	requireStatus(t, w, "accepted")
}

func TestJoin_JoinByIdAfterTokenLinkIsNoLongerValid(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	user := uuid.NewString()
	ctx := tenantless(user, addr)

	if _, _, _, err := w.store.AcceptInvitation(ctx, w.invite.Token); err != nil {
		t.Fatalf("token accept: %v", err)
	}
	// Not valid answers before member, though the caller now holds the membership.
	if _, _, _, err := w.store.AcceptInvitationByID(ctx, w.invite.ID); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("join by id after a token accept: err = %v, want ErrInvitationNotValid", err)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships = %d, want 1", got)
	}
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func setInviteStatus(t *testing.T, super *pgxpool.Pool, id, status string) {
	t.Helper()
	if _, err := super.Exec(context.Background(), `UPDATE invitations SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func TestJoin_RevokedOrAcceptedInviteIsNeitherListedNorAccepted(t *testing.T) {
	for _, state := range []string{"revoked", "accepted"} {
		t.Run(state, func(t *testing.T) {
			w, addr := newJoinWorld(t, "reviewer")
			user := uuid.NewString()
			ctx := tenantless(user, addr)
			if got, err := w.store.MyPendingInvitations(ctx); err != nil || len(got) != 1 {
				t.Fatalf("a live invite lists as %+v, err %v; want exactly it", got, err)
			}
			setInviteStatus(t, w.super, w.invite.ID, state)

			if got, err := w.store.MyPendingInvitations(ctx); err != nil || len(got) != 0 {
				t.Errorf("list of a %s invite = %+v, err %v; want empty", state, got, err)
			}
			if _, _, _, err := w.store.AcceptInvitationByID(ctx, w.invite.ID); !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("accept of a %s invite: err = %v, want ErrInvitationNotValid", state, err)
			}
			if got := len(membersOf(t, w.super, user)); got != 0 {
				t.Errorf("memberships = %d, want 0", got)
			}
			if n := len(acceptedAudits(t, w.super, w.tenant)); n != 0 {
				t.Errorf("invitation.accepted rows = %d, want 0", n)
			}
			requireStatus(t, w, state)
		})
	}
}

func TestJoin_ANonUuidIdSendsNoStatement(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	store, tr := tracedStore(t)
	ctx := tenantless(uuid.NewString(), addr)
	for _, id := range []string{"", "x", w.invite.ID + "x", "' OR 1=1 --", w.invite.Token} {
		before := tr.count()
		if _, _, _, err := store.AcceptInvitationByID(ctx, id); !errors.Is(err, ErrInvitationNotValid) {
			t.Errorf("accept %q: err = %v, want ErrInvitationNotValid", id, err)
		}
		if n := tr.count() - before; n != 0 {
			t.Errorf("accept %q sent %d statements, want none", id, n)
		}
	}
	requireStatus(t, w, "pending")
	before := tr.count()
	if _, _, _, err := store.AcceptInvitationByID(ctx, w.invite.ID); err != nil {
		t.Fatalf("control accept: %v", err)
	}
	if tr.count() == before {
		t.Error("the traced pool saw no statement for a valid accept, so a zero count above proves nothing")
	}
}

func TestJoin_TwoCallersOneInviteJoinOnce(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	users := []string{uuid.NewString(), uuid.NewString()}
	ch := make(chan joinResult, len(users))
	start := make(chan struct{})
	for _, u := range users {
		go func() {
			<-start
			tenant, _, _, err := w.store.AcceptInvitationByID(tenantless(u, addr), w.invite.ID)
			ch <- joinResult{tenant, err}
		}()
	}
	close(start)
	ok, refused := 0, 0
	for range users {
		r := <-ch
		switch {
		case r.err == nil && r.tenant.ID == w.tenant:
			ok++
		case errors.Is(r.err, ErrInvitationNotValid):
			refused++
		default:
			t.Errorf("unexpected result: tenant %+v, err %v", r.tenant, r.err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("successes/refusals = %d/%d, want 1/1", ok, refused)
	}
	if n := len(membersOf(t, w.super, users[0])) + len(membersOf(t, w.super, users[1])); n != 1 {
		t.Errorf("memberships across both callers = %d, want 1", n)
	}
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func TestJoin_AuditMatchesTheTokenAccept(t *testing.T) {
	byID, addrA := newJoinWorld(t, "preparer")
	byToken, addrB := newJoinWorld(t, "preparer")
	userA, userB := uuid.NewString(), uuid.NewString()
	if _, _, _, err := byID.store.AcceptInvitationByID(tenantless(userA, addrA), byID.invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := byToken.store.AcceptInvitation(tenantless(userB, addrB), byToken.invite.Token); err != nil {
		t.Fatal(err)
	}
	a, b := acceptedAudits(t, byID.super, byID.tenant), acceptedAudits(t, byToken.super, byToken.tenant)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("audit rows by id / by token = %d / %d, want 1 / 1", len(a), len(b))
	}
	if a[0].Actor != userA || b[0].Actor != userB {
		t.Errorf("actors = %s, %s; want the joining subjects", a[0].Actor, b[0].Actor)
	}
	if len(a[0].Payload) != len(b[0].Payload) || a[0].Payload["role"] != b[0].Payload["role"] ||
		a[0].Payload["invitation_id"] != byID.invite.ID || b[0].Payload["invitation_id"] != byToken.invite.ID {
		t.Errorf("payloads differ: by id %v, by token %v", a[0].Payload, b[0].Payload)
	}
}

// The routes read the address from the caller context only; a victim's address in the body or query is ignored,
// and every refusal is the same bytes whatever made it.
func TestJoin_HandlersIgnoreBodyAndQueryAndRefuseAlike(t *testing.T) {
	w, addr := newJoinWorld(t, "reviewer")
	other := newInvWorld(t, "Eze Ltd", "Bola Eze")
	otherInv := mustIssue(t, other.adminCtx(), other.store, []string{addr}, "preparer")[0]
	// One tenant each: a second invite to the same address in a tenant revives the first row.
	dead := func(name string) IssuedInvite {
		x := newInvWorld(t, name, "Chi Okoro")
		return mustIssue(t, x.adminCtx(), x.store, []string{addr}, "reviewer")[0]
	}
	expired, revoked, accepted := dead("Dead A"), dead("Dead B"), dead("Dead C")
	setExpiry(t, w.super, expired.ID, time.Now().Add(-time.Minute))
	setInviteStatus(t, w.super, revoked.ID, "revoked")
	setInviteStatus(t, w.super, accepted.ID, "accepted")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/invitations/mine", InvitationsMineHandler(w.store.MyPendingInvitations, nil))
	mux.HandleFunc("POST /v1/invitations/{id}/accept", AcceptInvitationByIDHandler(w.store.AcceptInvitationByID, nil))
	attacker := uuid.NewString()
	atk := tenantless(attacker, joinAddr("attacker"))
	spoof := `{"email":"` + addr + `","tenant_id":"` + w.tenant + `"}`

	var bodies []string
	for name, c := range map[string]struct{ id, query string }{
		"unknown id":         {uuid.NewString(), ""},
		"live invite of A":   {w.invite.ID, "?email=" + addr},
		"other tenant":       {otherInv.ID, "?email=" + addr + "&tenant_id=" + other.tenant},
		"expired":            {expired.ID, ""},
		"revoked":            {revoked.ID, ""},
		"accepted":           {accepted.ID, ""},
		"not a uuid":         {"x", ""},
		"token as id":        {w.invite.Token, ""},
		"upper-case foreign": {strings.ToUpper(w.invite.ID), ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := apiDo(mux, atk, http.MethodPost, "/v1/invitations/"+c.id+"/accept"+c.query, spoof)
			assertErrorBody(t, rec, http.StatusNotFound, "this invite is no longer valid")
			bodies = append(bodies, rec.Body.String())
			for _, leak := range []string{"Obi Partners", "Eze Ltd", w.tenant, other.tenant, addr} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("refusal %s names %q", rec.Body, leak)
				}
			}
		})
	}
	if len(bodies) == 0 {
		t.Fatal("no refusal ran")
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("refusal bodies differ: %q vs %q", b, bodies[0])
		}
	}
	if rows := membersOf(t, w.super, attacker); len(rows) != 0 {
		t.Errorf("attacker memberships = %+v, want none", rows)
	}
	requireStatus(t, w, "pending")

	rec := apiDo(mux, atk, http.MethodGet, "/v1/invitations/mine?email="+addr, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"invitations":[]}` {
		t.Errorf("attacker list with a spoofed query = %d %s, want 200 empty", rec.Code, rec.Body)
	}

	// Positive controls: the addressee lists and joins; a member and a no-caller request get 409 / 401.
	user := uuid.NewString()
	rec = apiDo(mux, tenantless(user, addr), http.MethodGet, "/v1/invitations/mine", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), w.invite.ID) || !strings.Contains(rec.Body.String(), "Obi Partners") {
		t.Fatalf("addressee list = %d %s, want 200 naming the invite", rec.Code, rec.Body)
	}
	rec = apiDo(mux, tenantless(user, addr), http.MethodPost, "/v1/invitations/"+w.invite.ID+"/accept", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reviewer"`) {
		t.Fatalf("addressee accept = %d %s, want 200", rec.Code, rec.Body)
	}
	member := auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: w.tenant, Email: addr})
	assertErrorBody(t, apiDo(mux, member, http.MethodGet, "/v1/invitations/mine", ""), http.StatusConflict, "you already belong to a workspace")
	assertErrorBody(t, apiDo(mux, member, http.MethodPost, "/v1/invitations/"+otherInv.ID+"/accept", ""), http.StatusConflict, "you already belong to a workspace")
	assertErrorBody(t, apiDo(mux, context.Background(), http.MethodGet, "/v1/invitations/mine", ""), http.StatusUnauthorized, "unauthorized")
	assertErrorBody(t, apiDo(mux, context.Background(), http.MethodPost, "/v1/invitations/"+otherInv.ID+"/accept", ""), http.StatusUnauthorized, "unauthorized")
}

func TestJoin_AcceptNamesTheInviteNotTheFirstOfTheAddress(t *testing.T) {
	addr := joinAddr("tunde")
	t1 := newInvWorld(t, "Obi Partners", "Ada Obi")
	t2 := newInvWorld(t, "Eze Ltd", "Bola Eze")
	i1 := mustIssue(t, t1.adminCtx(), t1.store, []string{addr}, "reviewer")[0]
	i2 := mustIssue(t, t2.adminCtx(), t2.store, []string{addr}, "preparer")[0]
	setExpiry(t, t1.super, i1.ID, time.Now().Add(24*time.Hour)) // i1 lists first
	setExpiry(t, t1.super, i2.ID, time.Now().Add(48*time.Hour))
	user := uuid.NewString()

	tenant, _, role, err := t1.store.AcceptInvitationByID(tenantless(user, addr), i2.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if tenant.ID != t2.tenant || tenant.Name != "Eze Ltd" || role != "preparer" {
		t.Errorf("joined %+v as %q, want Eze Ltd as preparer", tenant, role)
	}
	rows := membersOf(t, t1.super, user)
	if len(rows) != 1 || rows[0].Tenant != t2.tenant || rows[0].Role != "preparer" {
		t.Errorf("memberships = %+v, want one in %s as preparer", rows, t2.tenant)
	}
	if got := invRows(t, t1.super, t1.tenant, "id = $2", i1.ID); len(got) != 1 || got[0].Status != "pending" {
		t.Errorf("the other invite = %+v, want pending", got)
	}
}
