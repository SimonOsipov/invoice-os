package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// tenant plants a tenant, removed at cleanup through admin (the cascade takes its memberships and invitations).
func (r *eraseRig) tenant() string {
	r.t.Helper()
	id := uuid.NewString()
	r.exec(`INSERT INTO tenants (id, name, kind) VALUES ($1, 'Erase invitations test', 'firm')`, id)
	r.t.Cleanup(func() {
		if _, err := r.e.admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id); err != nil {
			r.t.Errorf("cleanup tenant: %v", err)
		}
	})
	return id
}

func (r *eraseRig) member(tenant, uid, status string) {
	r.t.Helper()
	r.exec(`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1, $2, $3, 'admin', $4)`,
		uuid.NewString(), tenant, uid, status)
}

func (r *eraseRig) invite(tenant, email, status string) string {
	r.t.Helper()
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		r.t.Fatal(err)
	}
	return r.inviteWithHash(tenant, email, status, tok)
}

func (r *eraseRig) inviteWithHash(tenant, email, status string, hash []byte) string {
	r.t.Helper()
	id := uuid.NewString()
	r.exec(`INSERT INTO invitations (id, tenant_id, role, invitee_email, status, token_hash, expires_at, invited_by)
		VALUES ($1, $2, 'admin', $3, $4, $5, now() + interval '7 days', $6)`,
		id, tenant, email, status, hash, uuid.NewString())
	return id
}

func (r *eraseRig) invited(id string) bool {
	r.t.Helper()
	return r.count(`SELECT count(*) FROM invitations WHERE id = $1`, id) == 1
}

func (r *eraseRig) wantInvites(what string, ids map[string]string, want map[string]bool) {
	r.t.Helper()
	for label, id := range ids {
		if got := r.invited(id); got != want[label] {
			r.t.Errorf("%s: invitation %q present = %v, want %v", what, label, got, want[label])
		}
	}
}

func (r *eraseRig) invitationCount() int {
	return r.count(`SELECT count(*) FROM invitations WHERE lower(invitee_email) = $1`, r.mail)
}

func TestErase_SweepDeletesTheInvitations(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	r.member(t1, r.uid, "removed")
	other := uuid.NewString()
	r.member(t1, other, "active")
	otherMail := "oth-" + uuid.NewString()[:8] + "@corp.example"
	ids := map[string]string{
		"accepted": r.invite(t1, r.mail, "accepted"),
		"pending":  r.invite(t1, r.mail, "pending"),
		"revoked":  r.invite(t1, r.mail, "revoked"),
		"other":    r.invite(t1, otherMail, "accepted"),
	}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	r.wantInvites("after the erase", ids, map[string]bool{"other": true})
	if !r.row(r.uid).Fin {
		t.Error("erasure row not finished")
	}
}

func TestErase_AnAcceptedInvitationInAnotherWorkspaceStays(t *testing.T) {
	r := newEraseRig(t)
	t1, t2 := r.tenant(), r.tenant()
	r.member(t1, r.uid, "removed")
	own := r.invite(t1, r.mail, "accepted")
	foreign := r.invite(t2, r.mail, "accepted")

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	r.wantInvites("after the erase", map[string]string{"own": own, "foreign": foreign}, map[string]bool{"foreign": true})
}

func TestErase_AnInvitationToAnAddressAnotherAccountHoldsStays(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	r.member(t1, r.uid, "removed")
	ids := map[string]string{
		"accepted": r.invite(t1, r.mail, "accepted"),
		"pending":  r.invite(t1, r.mail, "pending"),
		"revoked":  r.invite(t1, r.mail, "revoked"),
	}
	buid := uuid.NewString()
	r.contact(r.mail, &buid, 1)

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	r.wantInvites("after the erase", ids, map[string]bool{"pending": true, "revoked": true})
	if n := r.count(`SELECT count(*) FROM invitations WHERE id = ANY($1) AND lower(invitee_email) = $2`,
		[]string{ids["pending"], ids["revoked"]}, r.mail); n != 2 {
		t.Errorf("kept rows holding the address = %d, want 2", n)
	}
}

func TestErase_PendingInvitationsInEveryWorkspaceGo(t *testing.T) {
	r := newEraseRig(t)
	t1, t2 := r.tenant(), r.tenant()
	ids := map[string]string{"t1": r.invite(t1, r.mail, "pending"), "t2": r.invite(t2, r.mail, "pending")}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	r.wantInvites("after the erase", ids, nil)
}

func TestErase_RerunAfterAFailedStepChangesNoInvitation(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	r.member(t1, r.uid, "removed")
	ids := map[string]string{
		"accepted": r.invite(t1, r.mail, "accepted"),
		"pending":  r.invite(t1, r.mail, "pending"),
	}
	r.failAll()
	gen := r.start()

	if err := r.work(gen); err == nil {
		t.Fatal("first Work = nil, want the failed step")
	}
	r.wantInvites("after the failed run", ids, nil)
	before := r.invitationCount()

	r.acct.fn, r.hs.fn, r.rs.fn = nil, nil, nil
	if err := r.work(gen); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if after := r.invitationCount(); after != before || after != 0 {
		t.Errorf("invitations: after the failed run %d, after the retry %d, want 0 both", before, after)
	}
	if !r.row(r.uid).Fin {
		t.Error("erasure row not finished after the retry")
	}
}

func TestErase_AnInvitationCreatedMidRunIsRemovedWithoutARerun(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	var late string
	r.acct.fn = func(int, string) error {
		late = r.invite(t1, r.mail, "pending")
		return nil
	}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if late == "" {
		t.Fatal("control: the hook did not run")
	}
	if r.invited(late) {
		t.Error("the invitation created mid-run is still there")
	}
	if len(r.hs.got()) != 1 || len(r.rs.got()) != 1 {
		t.Errorf("vendor calls hubspot %d, resend %d, want 1 each", len(r.hs.got()), len(r.rs.got()))
	}
}

func TestErase_NoInvitationMeansNoChange(t *testing.T) {
	r := newEraseRig(t)
	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !r.row(r.uid).Fin {
		t.Error("erasure row not finished")
	}
}

func TestErase_InvitationErasureLogsCarryNoPersonalData(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	r.member(t1, r.uid, "removed")
	r.invite(t1, r.mail, "accepted")
	r.invite(t1, r.mail, "pending")

	err := r.work(r.start())
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	qaRequireNoLeak(t, err, []*qaLogSink{r.sink}, r.mail, strings.Split(r.mail, "@")[0], r.uid)
}

func TestErase_AHoldThatEndsBetweenStepsDeletesTheKeptInvitation(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	pending := r.invite(t1, r.mail, "pending")
	buid := uuid.NewString()
	r.contact(r.mail, &buid, 1)
	r.acct.fn = func(int, string) error {
		r.exec(`DELETE FROM contacts WHERE email = $1`, r.mail)
		return nil
	}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if r.invited(pending) {
		t.Error("the pending invitation kept in step 1 is still there after the hold ended")
	}
}

func TestErase_AHoldThatStartsBetweenStepsKeepsALaterInvitation(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	early := r.invite(t1, r.mail, "pending")
	var late string
	r.acct.fn = func(int, string) error {
		buid := uuid.NewString()
		r.contact(r.mail, &buid, 1)
		late = r.invite(t1, r.mail, "pending")
		return nil
	}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if late == "" {
		t.Fatal("control: the hook did not run")
	}
	if r.invited(early) {
		t.Error("the pending invitation of step 1 (no hold) is still there")
	}
	if !r.invited(late) {
		t.Error("the invitation planted while another account holds the address was deleted")
	}
}

func TestEraseByID_LeavesInvitationsAlone(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	r.member(t1, r.uid, "removed")
	ids := map[string]string{"pending": r.invite(t1, r.mail, "pending"), "accepted": r.invite(t1, r.mail, "accepted")}
	r.seedCopies(r.uid, r.mail)

	if err := r.workByID(r.byIDWorker(), r.uid); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if h, _ := r.copies(r.uid); h != 0 {
		t.Fatal("control: the by-id job did not run its sweep")
	}
	r.wantInvites("after the by-id erase", ids, map[string]bool{"pending": true, "accepted": true})
}

func TestErase_ADeletedPendingInvitationsTokenIsInvalidAndTheAddressCanBeInvitedAgain(t *testing.T) {
	r := newEraseRig(t)
	t1 := r.tenant()
	token := "tok-" + uuid.NewString()
	hash := sha256.Sum256([]byte(token))
	r.inviteWithHash(t1, r.mail, "pending", hash[:])
	lookup := func() int {
		ctx := context.Background()
		var n int
		err := pgx.BeginFunc(ctx, r.e.app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM public.invitation_by_token($1)`, token).Scan(&n)
		})
		if err != nil {
			t.Fatalf("invitation_by_token: %v", err)
		}
		return n
	}
	if lookup() != 1 {
		t.Fatal("control: the token does not resolve before the erase")
	}

	if err := r.work(r.start()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := lookup(); n != 0 {
		t.Errorf("invitation_by_token rows after the erase = %d, want 0", n)
	}
	r.invite(t1, r.mail, "pending")
}
