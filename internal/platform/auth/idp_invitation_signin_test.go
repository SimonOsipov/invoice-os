package auth_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// passwordHash reads the account's stored hash: the random invite password is unknowable, so only its replacement is observable.
func passwordHash(t *testing.T, email string) string {
	t.Helper()
	var hash string
	if err := superConn(t).QueryRow(context.Background(), `SELECT encrypted_password FROM auth.users WHERE email = $1`, email).Scan(&hash); err != nil {
		t.Fatalf("read the password hash: %v", err)
	}
	return hash
}

func TestIdP_SetPasswordThenSignInAdmitsTheInviteeToTheJoinRoutes(t *testing.T) {
	w := newInviteWorld(t, "logfix11a-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	state := newState(t)

	w.register(t)
	requireSignIn(t, w.base, u, u.password, http.StatusBadRequest, "invalid_credentials")
	randomHash := passwordHash(t, w.email)
	_, _, location := w.setPasswordWithState(t, u.password, state)
	if after := passwordHash(t, w.email); after == "" || after == randomHash {
		t.Fatalf("password hash after the set-password POST equals the invite's random one (%q); want it replaced", after)
	}

	if location != siteURL+"/?verified=1" {
		t.Fatalf("set-password redirect = %q, want %s/?verified=1", location, siteURL)
	}
	access := accessToken(t, w.base, u)
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the signed-in token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
	requireSignIn(t, w.base, u, "not-"+u.password, http.StatusBadRequest, "invalid_credentials")
}

func TestIdP_SetPasswordTwiceConfirmsOnceThenFails(t *testing.T) {
	w := newInviteWorld(t, "logfix11d-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	s1, s2 := newState(t), newState(t)
	p2 := "pw2-" + uuid.NewString()

	w.register(t)
	action, values, location := w.setPasswordWithState(t, u.password, s1)
	if location != siteURL+"/?verified=1" {
		t.Fatalf("first set-password redirect = %q, want %s/?verified=1", location, siteURL)
	}

	values.Set("state", s2)
	values.Set("password", p2)
	status, second, err := postForm(action, values)
	if err != nil || status != http.StatusSeeOther || second != siteURL+"/?verify=failed" {
		t.Fatalf("replayed set-password form: status %d, Location %q, err %v; want 303 %s/?verify=failed", status, second, err, siteURL)
	}

	if n := sessionRows(t, w.email); n != 0 {
		t.Errorf("%d session rows after the set-password POSTs, want 0", n)
	}
	requireSignIn(t, w.base, u, u.password, http.StatusOK, "")
	requireSignIn(t, w.base, u, p2, http.StatusBadRequest, "invalid_credentials")
}

func TestIdP_SetPasswordWithoutAStateSetsThePasswordOnly(t *testing.T) {
	w := newInviteWorld(t, "logfix11g-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}

	w.register(t)
	w.setPasswordFromMail(t, u.password)
	requireSignIn(t, w.base, u, u.password, http.StatusOK, "")
}
