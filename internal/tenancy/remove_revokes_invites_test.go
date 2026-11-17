package tenancy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// reinviteWorld is the story's scenario: addr joined as a preparer, then an admin invited the
// same address again as admin, so a second pending invite sits next to an active member.
type reinviteWorld struct {
	invWorld
	user, addr string
	second     IssuedInvite
}

func newReinviteWorld(t *testing.T, tenantName string) reinviteWorld {
	t.Helper()
	w := newInvWorld(t, tenantName, "Ada Obi")
	rw := reinviteWorld{invWorld: w, user: uuid.NewString(), addr: joinAddr("grace")}
	first := mustIssue(t, w.adminCtx(), w.store, []string{rw.addr}, "preparer")[0]
	if _, _, _, err := w.store.AcceptInvitation(tenantless(rw.user, rw.addr), first.Token, nil); err != nil {
		t.Fatalf("fixture: first accept: %v", err)
	}
	rw.second = mustIssue(t, w.adminCtx(), w.store, []string{rw.addr}, "admin")[0]
	if rw.second.ID == first.ID {
		t.Fatal("fixture: the second invite reused the accepted invite's row")
	}
	return rw
}

func (rw reinviteWorld) caller() context.Context { return tenantless(rw.user, rw.addr) }

func (rw reinviteWorld) remove(t *testing.T) {
	t.Helper()
	if _, err := rw.store.RemoveMember(rw.adminCtx(), rw.user); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func memberAddr(id string) string { return id + "@members.test" }

func inviteStatus(t *testing.T, w invWorld, id string) string {
	t.Helper()
	rows := invRows(t, w.super, w.tenant, "id = $2", id)
	if len(rows) != 1 {
		t.Fatalf("invitation %s: %d rows, want 1", id, len(rows))
	}
	return rows[0].Status
}

func requireInviteStatus(t *testing.T, w invWorld, id, want string) {
	t.Helper()
	if got := inviteStatus(t, w, id); got != want {
		t.Errorf("invitation %s status = %q, want %q", id, got, want)
	}
}

func sortedPayloadKeys(p map[string]any) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// revokedIDs returns invitation_id of each invitation.revoked row of the tenant, sorted.
func revokedIDs(t *testing.T, w invWorld) []string {
	t.Helper()
	var ids []string
	for _, p := range auditPayloads(t, w.super, w.tenant, "invitation.revoked") {
		id, _ := p["invitation_id"].(string)
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func requireNoRevokeAudit(t *testing.T, w invWorld) {
	t.Helper()
	if ids := revokedIDs(t, w); len(ids) != 0 {
		t.Errorf("invitation.revoked rows = %v, want none", ids)
	}
}

func setMemberEmail(t *testing.T, w invWorld, user string, email *string) {
	t.Helper()
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET email = $2 WHERE tenant_id = $1 AND user_id = $3`, w.tenant, email, user); err != nil {
		t.Fatalf("set member email: %v", err)
	}
}

func requireRemoved(t *testing.T, w invWorld, user string) {
	t.Helper()
	rows := membersOf(t, w.super, user)
	if len(rows) != 1 || rows[0].Status != "removed" {
		t.Errorf("memberships of %s = %+v, want one removed row", user, rows)
	}
}

func TestRemoveMember_RevokesTheReinviteOfARemovedMember(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 reinvite")

	rw.remove(t)

	requireInviteStatus(t, rw.invWorld, rw.second.ID, "revoked")
	if _, _, role, err := rw.store.AcceptInvitation(rw.caller(), rw.second.Token, nil); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("accept of the old invite = role %q, err %v; want ErrInvitationNotValid", role, err)
	}
	rows := membersOf(t, rw.super, rw.user)
	if len(rows) != 1 || rows[0].Status != "removed" || rows[0].Role != "preparer" {
		t.Errorf("memberships = %+v, want one removed preparer row", rows)
	}
}

func TestRemoveMember_RevokedInviteIsDeadOnEveryPath(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 dead paths")
	if _, err := rw.store.PreviewInvitation(context.Background(), rw.second.Token); err != nil {
		t.Fatalf("control: preview before the removal: %v", err)
	}
	if _, first, err := rw.store.ClaimInvitationRegistration(context.Background(), rw.second.Token); err != nil || !first {
		t.Fatalf("control: claim before the removal = first %v, err %v; want a first claim", first, err)
	}

	rw.remove(t)

	if p, err := rw.store.PreviewInvitation(context.Background(), rw.second.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("preview = %+v, err %v; want ErrInvitationNotValid", p, err)
	}
	if _, _, _, err := rw.store.AcceptInvitationByID(rw.caller(), rw.second.ID, nil); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("accept by id err = %v, want ErrInvitationNotValid", err)
	}
	if email, first, err := rw.store.ClaimInvitationRegistration(context.Background(), rw.second.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("claim = %q, first %v, err %v; want ErrInvitationNotValid", email, first, err)
	}
	requireRemoved(t, rw.invWorld, rw.user)
}

func TestRemoveMember_RevokesAnInviteWhoseAddressDiffersOnlyInCase(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 case", "Ada Obi")
	m := w.addMember(t, "preparer", "active", "Grace Eze")
	inv := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: strings.ToUpper(memberAddr(m))})

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	requireInviteStatus(t, w, inv.ID, "revoked")
}

func TestRemoveMember_RevokesAnInviteWithPaddedAddress(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 padded", "Ada Obi")
	x := w.addMember(t, "preparer", "active", "Grace Eze")
	y := w.addMember(t, "preparer", "active", "Hope Eze")
	padded := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "  " + strings.ToUpper(memberAddr(x)) + " "})
	setMemberEmail(t, w, y, strp(" "+strings.ToUpper(memberAddr(y))+" "))
	storedPadded := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: memberAddr(y)})

	for _, id := range []string{x, y} {
		if _, err := w.store.RemoveMember(w.adminCtx(), id); err != nil {
			t.Fatalf("RemoveMember %s: %v", id, err)
		}
	}

	requireInviteStatus(t, w, padded.ID, "revoked")
	requireInviteStatus(t, w, storedPadded.ID, "revoked")
}

func TestRemoveMember_RevokesAnExpiredPendingInvite(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 expired")
	setExpiry(t, rw.super, rw.second.ID, time.Now().Add(-time.Hour))

	rw.remove(t)

	requireInviteStatus(t, rw.invWorld, rw.second.ID, "revoked")
	if ids := revokedIDs(t, rw.invWorld); !slices.Equal(ids, []string{rw.second.ID}) {
		t.Errorf("invitation.revoked rows name %v, want only %s", ids, rw.second.ID)
	}
}

func TestRemoveMember_RevokesEveryCaseVariant(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 variants", "Ada Obi")
	m := w.addMember(t, "preparer", "active", "Grace Eze")
	lower := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: memberAddr(m), role: "reviewer"})
	upper := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: strings.ToUpper(memberAddr(m)), role: "admin"})

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	requireInviteStatus(t, w, lower.ID, "revoked")
	requireInviteStatus(t, w, upper.ID, "revoked")
	want := []string{lower.ID, upper.ID}
	slices.Sort(want)
	if ids := revokedIDs(t, w); !slices.Equal(ids, want) {
		t.Fatalf("invitation.revoked rows name %v, want one per invite %v", ids, want)
	}
	for _, p := range auditPayloads(t, w.super, w.tenant, "invitation.revoked") {
		wantRole := map[string]string{lower.ID: "reviewer", upper.ID: "admin"}[p["invitation_id"].(string)]
		if p["role"] != wantRole {
			t.Errorf("payload %v: role = %v, want %s (the invite's own role)", p, p["role"], wantRole)
		}
	}
}

func TestRemoveMember_RevokesTheInviteOfASuspendedMember(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 suspended", "Ada Obi")
	m := w.addMember(t, "reviewer", "suspended", "Grace Eze")
	inv := mustIssue(t, w.adminCtx(), w.store, []string{memberAddr(m)}, "preparer")[0]

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	requireInviteStatus(t, w, inv.ID, "revoked")
	ps := auditPayloads(t, w.super, w.tenant, "membership.removed")
	if len(ps) != 1 || ps[0]["from"] != "suspended" {
		t.Errorf("membership.removed payloads = %v, want one with from suspended", ps)
	}
}

func TestRemoveMember_RevokesTheInviteOfAnAdminTarget(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 admin target", "Ada Obi")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	inv := mustIssue(t, w.adminCtx(), w.store, []string{memberAddr(b)}, "reviewer")[0]

	if _, err := w.store.RemoveMember(w.adminCtx(), b); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	requireInviteStatus(t, w, inv.ID, "revoked")
	ps := auditPayloads(t, w.super, w.tenant, "invitation.revoked")
	if len(ps) != 1 || ps[0]["role"] != "reviewer" {
		t.Errorf("invitation.revoked payloads = %v, want one with the invite's role reviewer, not admin", ps)
	}
}

func TestRemoveMember_LeavesTheSameAddressInAnotherWorkspace(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 guard here")
	w2 := newInvWorld(t, "SECAUD-27 guard elsewhere", "Chi Okoro")
	other := mustIssue(t, w2.adminCtx(), w2.store, []string{rw.addr}, "reviewer")[0]

	rw.remove(t)

	requireInviteStatus(t, rw.invWorld, rw.second.ID, "revoked")
	rows := invRows(t, w2.super, w2.tenant, "id = $2", other.ID)
	if len(rows) != 1 || rows[0].Status != "pending" || !slices.Equal(rows[0].Hash, other.TokenHash) {
		t.Errorf("other workspace invite = %+v, want pending with its token hash unchanged", rows)
	}
	if _, err := rw.store.PreviewInvitation(context.Background(), other.Token); err != nil {
		t.Errorf("preview of the other workspace's invite: %v", err)
	}
	requireNoRevokeAudit(t, w2)
}

func TestRemoveMember_LeavesAnotherAddressesInviteUsable(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 other address", "Ada Obi")
	m := w.addMember(t, "preparer", "active", "Grace Eze")
	mine := mustIssue(t, w.adminCtx(), w.store, []string{memberAddr(m)}, "reviewer")[0]
	other := mustIssue(t, w.adminCtx(), w.store, []string{joinAddr("someone")}, "reviewer")[0]

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	requireInviteStatus(t, w, mine.ID, "revoked")
	rows := invRows(t, w.super, w.tenant, "id = $2", other.ID)
	if len(rows) != 1 || rows[0].Status != "pending" || !slices.Equal(rows[0].Hash, other.TokenHash) {
		t.Errorf("other address invite = %+v, want pending with its token hash unchanged", rows)
	}
	if _, err := w.store.PreviewInvitation(context.Background(), other.Token); err != nil {
		t.Errorf("preview of the other address's invite: %v", err)
	}
}

func TestRemoveMember_AuditsEachRevokedInvite(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 audit")

	rw.remove(t)

	ps := auditPayloads(t, rw.super, rw.tenant, "invitation.revoked")
	if len(ps) != 1 {
		t.Fatalf("invitation.revoked rows = %d, want 1", len(ps))
	}
	if keys := sortedPayloadKeys(ps[0]); !slices.Equal(keys, []string{"invitation_id", "role"}) {
		t.Errorf("invitation.revoked keys = %v, want exactly [invitation_id role]", keys)
	}
	if ps[0]["invitation_id"] != rw.second.ID || ps[0]["role"] != "admin" {
		t.Errorf("invitation.revoked payload = %v, want the second invite and role admin", ps[0])
	}
	if got := auditActor(t, rw.app, rw.tenant, "invitation.revoked"); got != rw.admin {
		t.Errorf("invitation.revoked actor = %q, want the removing admin %q", got, rw.admin)
	}
	removed := auditPayloads(t, rw.super, rw.tenant, "membership.removed")
	if len(removed) != 1 || !slices.Equal(sortedPayloadKeys(removed[0]), []string{"from", "user_id"}) {
		t.Errorf("membership.removed payloads = %v, want one with exactly [from user_id]", removed)
	}
}

func TestRemoveMember_RefusedRemovalKeepsTheInvitePending(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 refused", "Ada Obi")
	inv := mustIssue(t, w.adminCtx(), w.store, []string{memberAddr(w.admin)}, "preparer")[0]

	if _, err := w.store.RemoveMember(w.adminCtx(), w.admin); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("RemoveMember of the only admin err = %v, want ErrLastActiveAdmin", err)
	}

	requireInviteStatus(t, w, inv.ID, "pending")
	requireNoRevokeAudit(t, w)

	// Positive pair: once a second admin exists the same removal revokes the invite.
	w.addMember(t, "admin", "active", "Bola Eze")
	if _, err := w.store.RemoveMember(w.adminCtx(), w.admin); err != nil {
		t.Fatalf("RemoveMember with a second admin: %v", err)
	}
	requireInviteStatus(t, w, inv.ID, "revoked")
}

func TestRemoveMember_FailedAuditKeepsTheInvitePending(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 failed audit")
	drop := failAudit(t, rw.super, rw.tenant, "membership.removed")

	if _, err := rw.store.RemoveMember(rw.adminCtx(), rw.user); err == nil {
		t.Fatal("RemoveMember succeeded although its membership.removed audit fails")
	}

	requireInviteStatus(t, rw.invWorld, rw.second.ID, "pending")
	requireNoRevokeAudit(t, rw.invWorld)
	if rows := membersOf(t, rw.super, rw.user); len(rows) != 1 || rows[0].Status != "active" {
		t.Errorf("memberships = %+v, want the one row still active", rows)
	}

	// Positive pair: with the audit healthy the retry revokes the invite.
	drop()
	rw.remove(t)
	requireInviteStatus(t, rw.invWorld, rw.second.ID, "revoked")
}

func TestRemoveMember_RevokesWithoutTheIdentityLock(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 no lock")
	holdIdentityLock(t, rw.super, rw.user)
	ctx, cancel := context.WithTimeout(rw.adminCtx(), 5*time.Second)
	defer cancel()

	if _, err := rw.store.RemoveMember(ctx, rw.user); err != nil {
		t.Fatalf("RemoveMember while another session holds the target's identity lock: %v", err)
	}

	requireInviteStatus(t, rw.invWorld, rw.second.ID, "revoked")
	requireRemoved(t, rw.invWorld, rw.user)
}

func TestRemoveMember_NullEmailRevokesNothing(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 null email", "Ada Obi")
	m := w.addMember(t, "preparer", "active", "Grace Eze")
	setMemberEmail(t, w, m, nil)
	inv := mustIssue(t, w.adminCtx(), w.store, []string{joinAddr("someone")}, "reviewer")[0]

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember of a NULL-email member: %v", err)
	}

	requireRemoved(t, w, m)
	requireInviteStatus(t, w, inv.ID, "pending")
	requireNoRevokeAudit(t, w)
}

func TestRemoveMember_BlankEmailRevokesNothing(t *testing.T) {
	w := newInvWorld(t, "SECAUD-27 blank email", "Ada Obi")
	m := w.addMember(t, "preparer", "active", "Grace Eze")
	setMemberEmail(t, w, m, strp("   "))
	blank := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "  "})

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember of a blank-email member: %v", err)
	}

	requireRemoved(t, w, m)
	requireInviteStatus(t, w, blank.ID, "pending")
	requireNoRevokeAudit(t, w)
}

func TestRemoveMember_OldInviteAnswersTheExisting404(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 404")
	mux := realAcceptMux(rw.store)
	rw.remove(t)

	byToken := apiDo(mux, rw.caller(), "POST", "/v1/invitations/accept", `{"token":"`+rw.second.Token+`"}`)
	byID := apiDo(mux, rw.caller(), "POST", "/v1/invitations/"+rw.second.ID+"/accept", "")

	for name, rec := range map[string]string{"token": byToken.Body.String(), "id": byID.Body.String()} {
		if !strings.Contains(rec, "this invite is no longer valid") {
			t.Errorf("accept by %s body = %s, want the no-longer-valid sentence", name, rec)
		}
	}
	if byToken.Code != 404 || byID.Code != 404 {
		t.Errorf("accept statuses = token %d, id %d; want 404 for both", byToken.Code, byID.Code)
	}
	if byToken.Body.String() != byID.Body.String() {
		t.Errorf("bodies differ: token %q, id %q; the join must not tell the cases apart", byToken.Body, byID.Body)
	}
}

func TestRemoveMember_MineNoLongerListsTheInvite(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 mine")
	w2 := newInvWorld(t, "SECAUD-27 mine elsewhere", "Chi Okoro")
	elsewhere := mustIssue(t, w2.adminCtx(), w2.store, []string{rw.addr}, "reviewer")[0]
	mux := realAcceptMux(rw.store)

	rw.remove(t)

	got := getMine(t, mux, rw.caller(), "/v1/invitations/mine", "")
	var ids []string
	for _, inv := range got.Invitations {
		ids = append(ids, inv.ID)
	}
	if !got.Removed || !slices.Contains(ids, elsewhere.ID) {
		t.Errorf("mine = %s, want removed true and the other workspace's invite listed", got.Body)
	}
	if slices.Contains(ids, rw.second.ID) {
		t.Errorf("mine lists the revoked invite %s: %s", rw.second.ID, got.Body)
	}
}

func TestRemoveMember_AdminCanInviteTheRemovedPersonAgain(t *testing.T) {
	rw := newReinviteWorld(t, "SECAUD-27 invite again")
	rw.remove(t)

	again := mustIssue(t, rw.adminCtx(), rw.store, []string{rw.addr}, "reviewer")[0]
	if again.ID == rw.second.ID {
		t.Errorf("the new invite reused the revoked row %s", again.ID)
	}
	requireInviteStatus(t, rw.invWorld, again.ID, "pending")

	// The removed row carries the address again, so only the early return on a removed row spares the new invite.
	setMemberEmail(t, rw.invWorld, rw.user, strp(rw.addr))
	if _, err := rw.store.RemoveMember(rw.adminCtx(), rw.user); err != nil {
		t.Fatalf("repeat RemoveMember: %v", err)
	}

	requireInviteStatus(t, rw.invWorld, again.ID, "pending")
	if ids := revokedIDs(t, rw.invWorld); !slices.Equal(ids, []string{rw.second.ID}) {
		t.Errorf("invitation.revoked rows name %v, want only the first removal's %s", ids, rw.second.ID)
	}
	_, _, role, err := rw.store.AcceptInvitation(rw.caller(), again.Token, nil)
	if err != nil || role != "reviewer" {
		t.Fatalf("accept of the new invite = role %q, err %v; want reviewer, nil", role, err)
	}
	rows := membersOf(t, rw.super, rw.user)
	if len(rows) != 1 || rows[0].Status != "active" || rows[0].Role != "reviewer" {
		t.Errorf("memberships = %+v, want one active reviewer row", rows)
	}
}
