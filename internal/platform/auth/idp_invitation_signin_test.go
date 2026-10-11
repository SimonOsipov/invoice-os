package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// inviteHandoffCode returns the exchange code in the invitee's set-password redirect.
func inviteHandoffCode(t *testing.T, location string) string {
	t.Helper()
	code, ok := strings.CutPrefix(location, siteURL+"/?verified=1&handoff=")
	if !ok || code == "" {
		t.Fatalf("set-password redirect = %q, want %s/?verified=1&handoff=<code>", location, siteURL)
	}
	return code
}

// exchanged redeems code with state and requires 200 with both tokens.
func exchanged(t *testing.T, gw, code, state string) (string, string) {
	t.Helper()
	status, session := exchange(t, gw, code, state)
	access, _ := session["access_token"].(string)
	refresh, _ := session["refresh_token"].(string)
	if status != http.StatusOK || access == "" || refresh == "" {
		t.Fatalf("exchange: status %d, body %v; want 200 with both tokens", status, session)
	}
	return access, refresh
}

// passwordHash reads the account's stored hash: the random invite password is unknowable, so only its replacement is observable.
func passwordHash(t *testing.T, email string) string {
	t.Helper()
	var hash string
	if err := superConn(t).QueryRow(context.Background(), `SELECT encrypted_password FROM auth.users WHERE email = $1`, email).Scan(&hash); err != nil {
		t.Fatalf("read the password hash: %v", err)
	}
	return hash
}

func TestIdP_SetPasswordSignsTheInviteeInToTheJoinRoutes(t *testing.T) {
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

	access, _ := exchanged(t, w.gw, inviteHandoffCode(t, location), state)
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the exchanged token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
	requireSignIn(t, w.base, u, u.password, http.StatusOK, "")
	requireSignIn(t, w.base, u, "not-"+u.password, http.StatusBadRequest, "invalid_credentials")
}

func TestIdP_SetPasswordSessionRefreshes(t *testing.T) {
	w := newInviteWorld(t, "logfix11b-")
	state := newState(t)

	w.register(t)
	_, _, location := w.setPasswordWithState(t, "pw-"+uuid.NewString(), state)
	_, refresh := exchanged(t, w.gw, inviteHandoffCode(t, location), state)

	status, next := postJSON(t, w.base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	if got, _ := next["access_token"].(string); status != http.StatusOK || got == "" {
		t.Fatalf("refresh grant with the exchanged token: status %d, body %v; want 200", status, next)
	}
}

func TestIdP_SetPasswordLeavesExactlyTheHandedOffSession(t *testing.T) {
	w := newInviteWorld(t, "logfix11c-")
	state := newState(t)

	w.register(t)
	_, _, location := w.setPasswordWithState(t, "pw-"+uuid.NewString(), state)
	if n := sessionRows(t, w.email); n != 1 {
		t.Fatalf("session rows right after the set-password POST = %d, want 1 (the verify session ended, the grant's remains)", n)
	}
	access, _ := exchanged(t, w.gw, inviteHandoffCode(t, location), state)

	var sessionID string
	if err := superConn(t).QueryRow(context.Background(),
		`SELECT s.id::text FROM auth.sessions s JOIN auth.users u ON u.id = s.user_id WHERE u.email = $1`, w.email).Scan(&sessionID); err != nil {
		t.Fatalf("read the session row: %v", err)
	}
	if sid, _ := jwtPart(t, access, 1)["session_id"].(string); sid == "" || sid != sessionID {
		t.Errorf("exchanged token session_id = %q, want the surviving row %q", sid, sessionID)
	}
}

func TestIdP_SetPasswordTwiceSignsInOnceThenFails(t *testing.T) {
	w := newInviteWorld(t, "logfix11d-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	s1, s2 := newState(t), newState(t)
	p2 := "pw2-" + uuid.NewString()

	w.register(t)
	action, values, location := w.setPasswordWithState(t, u.password, s1)
	code := inviteHandoffCode(t, location)

	values.Set("state", s2)
	values.Set("password", p2)
	status, second, err := postForm(action, values)
	if err != nil || status != http.StatusSeeOther || second != siteURL+"/?verify=failed" {
		t.Fatalf("replayed set-password form: status %d, Location %q, err %v; want 303 %s/?verify=failed", status, second, err, siteURL)
	}

	exchanged(t, w.gw, code, s1)
	if status, body := exchange(t, w.gw, code, s1); status != http.StatusBadRequest {
		t.Errorf("second exchange of the first code: status %d, body %v; want 400", status, body)
	}
	requireSignIn(t, w.base, u, u.password, http.StatusOK, "")
	requireSignIn(t, w.base, u, p2, http.StatusBadRequest, "invalid_credentials")
}

// One test gateway binds the mailed link's host, so each world runs in its own subtest.
func TestIdP_SetPasswordOnAnotherDeviceSignsInThere(t *testing.T) {
	s1, s2 := newState(t), newState(t)

	t.Run("other device's state", func(t *testing.T) {
		w := newInviteWorld(t, "logfix11e-")
		w.register(t)
		_, _, location := w.setPasswordWithState(t, "pw-"+uuid.NewString(), s2)
		if status, body := exchange(t, w.gw, inviteHandoffCode(t, location), s1); status != http.StatusBadRequest {
			t.Fatalf("exchange with the other device's state: status %d, body %v; want 400", status, body)
		}
	})

	t.Run("own state", func(t *testing.T) {
		w := newInviteWorld(t, "logfix11f-")
		w.register(t)
		_, _, location := w.setPasswordWithState(t, "pw-"+uuid.NewString(), s2)
		exchanged(t, w.gw, inviteHandoffCode(t, location), s2)
	})
}

func TestIdP_SetPasswordWithoutAStateSetsThePasswordOnly(t *testing.T) {
	w := newInviteWorld(t, "logfix11g-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}

	w.register(t)
	w.setPasswordFromMail(t, u.password)
	requireSignIn(t, w.base, u, u.password, http.StatusOK, "")
}
