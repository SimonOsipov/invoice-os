package db_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func setToken(t *testing.T, tenant, invitation, token string) (bool, error) {
	t.Helper()
	return db.SetInvitationToken(context.Background(), os.Getenv("DATABASE_MIGRATION_URL"),
		uuid.MustParse(tenant), uuid.MustParse(invitation), token)
}

func tokenHashOf(t *testing.T, id string) []byte {
	t.Helper()
	var got []byte
	if err := h.super.QueryRow(context.Background(), `SELECT token_hash FROM invitations WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	return got
}

func TestRLS_SetInvitationTokenReplacesThePendingHash(t *testing.T) {
	requireHarness(t)
	tenant := newNamedTenant(t, "Obi Partners")
	oldToken, newTok, acceptedToken := newToken(t), newToken(t), newToken(t)
	pending := seedAcceptInvite(t, tenant, "reviewer", invitedAddress, oldToken)
	accepted := seedAcceptInvite(t, tenant, "preparer", "bola@obi.test", acceptedToken)
	setInviteState(t, accepted, `status = 'accepted'`)

	// Control: the old hash names the pending invite before the write.
	if got := lookupAs(t, "", oldToken); len(got) != 1 || got[0].id != pending {
		t.Fatalf("control: invitation_by_token(old) = %+v, want exactly the pending invite %s", got, pending)
	}

	replaced, err := setToken(t, tenant, pending, newTok)
	if err != nil || !replaced {
		t.Fatalf("SetInvitationToken(pending) = (%v, %v), want (true, nil)", replaced, err)
	}

	got := lookupAs(t, "", newTok)
	if want := (lookupRow{id: pending, tenantID: tenant, workspace: "Obi Partners", role: "reviewer", email: invitedAddress}); len(got) != 1 || got[0] != want {
		t.Errorf("invitation_by_token(new) = %+v, want exactly [%+v]", got, want)
	}
	if got := lookupAs(t, "", oldToken); len(got) != 0 {
		t.Errorf("invitation_by_token(old) = %+v, want no row: the old hash must be replaced", got)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE token_hash = $1`, hashOf(oldToken)); n != 0 {
		t.Errorf("rows still holding the old hash = %d, want 0", n)
	}
	if !bytes.Equal(tokenHashOf(t, pending), hashOf(newTok)) {
		t.Errorf("token_hash is not sha256 of the new token")
	}

	before := tokenHashOf(t, accepted)
	replaced, err = setToken(t, tenant, accepted, newToken(t))
	if err != nil || replaced {
		t.Errorf("SetInvitationToken(accepted) = (%v, %v), want (false, nil)", replaced, err)
	}
	if !bytes.Equal(tokenHashOf(t, accepted), before) || !bytes.Equal(before, hashOf(acceptedToken)) {
		t.Error("an accepted invite's token_hash changed")
	}

	replaced, err = setToken(t, tenant, uuid.NewString(), newToken(t))
	if err != nil || replaced {
		t.Errorf("SetInvitationToken(unknown id) = (%v, %v), want (false, nil)", replaced, err)
	}
}

// A pending invite is reachable only under its own tenant id: another tenant's id changes nothing.
func TestRLS_SetInvitationTokenRefusesAnotherTenant(t *testing.T) {
	requireHarness(t)
	owner := newNamedTenant(t, "Obi Partners")
	other := newNamedTenant(t, "Ade Holdings")
	ownerToken := newToken(t)
	ownerInvite := seedAcceptInvite(t, owner, "reviewer", invitedAddress, ownerToken)
	otherInvite := seedAcceptInvite(t, other, "preparer", "bola@ade.test", newToken(t))
	otherBefore := tokenHashOf(t, otherInvite)
	ownerBefore := tokenHashOf(t, ownerInvite)

	// Control: the owner's own tenant id does replace the hash, so the refusals below are the tenant id.
	if replaced, err := setToken(t, owner, ownerInvite, newToken(t)); err != nil || !replaced {
		t.Fatalf("control: SetInvitationToken(own tenant) = (%v, %v), want (true, nil)", replaced, err)
	}
	if bytes.Equal(tokenHashOf(t, ownerInvite), ownerBefore) {
		t.Fatal("control: the owner's hash did not change")
	}

	intruder := newToken(t)
	replaced, err := setToken(t, other, ownerInvite, intruder)
	if err != nil || replaced {
		t.Errorf("SetInvitationToken(other tenant, owner's invite) = (%v, %v), want (false, nil)", replaced, err)
	}
	if bytes.Equal(tokenHashOf(t, ownerInvite), hashOf(intruder)) {
		t.Error("the owner's invite took the token another tenant's id supplied")
	}
	if got := lookupAs(t, "", intruder); len(got) != 0 {
		t.Errorf("invitation_by_token(intruder token) = %+v, want no row", got)
	}

	replaced, err = setToken(t, owner, otherInvite, newToken(t))
	if err != nil || replaced {
		t.Errorf("SetInvitationToken(owner tenant, other's invite) = (%v, %v), want (false, nil)", replaced, err)
	}
	if !bytes.Equal(tokenHashOf(t, otherInvite), otherBefore) {
		t.Error("another tenant's invite hash changed")
	}
}
