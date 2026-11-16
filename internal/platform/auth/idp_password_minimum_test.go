package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// passwordMin is GOTRUE_PASSWORD_MIN_LENGTH in sidecar/auth/Dockerfile.
const passwordMin = 10

// pwOfLen builds a password at run time so no literal reaches the repo.
func pwOfLen(n int) string { return uuid.NewString()[:n] }

func userCount(t *testing.T, email string) int {
	return countRows(t, `SELECT count(*) FROM auth.users WHERE email = $1`, email)
}

func TestIdP_RegisterRefusesAPasswordOneUnderTheMinimum(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	register := registerHandler(authURL, true)
	wantErr := fmt.Sprintf("Password should be at least %d characters.", passwordMin)
	newEmail := func() string {
		e := "idp-" + uuid.NewString() + "@example.test"
		t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, e) })
		return e
	}

	fresh := newEmail()
	status, body := serveJSON(t, register, "/auth/register", map[string]string{"email": fresh, "password": pwOfLen(passwordMin - 1)})
	if status != http.StatusBadRequest || fields(t, body)["error"] != wantErr {
		t.Errorf("register one under the minimum: status %d, error %q; want 400 %q", status, fields(t, body)["error"], wantErr)
	}
	if n := userCount(t, fresh); n != 0 {
		t.Errorf("auth.users rows after a refused register = %d, want 0", n)
	}

	u := idpUser{email: newEmail(), password: pwOfLen(passwordMin)}
	if status, _ := serveJSON(t, register, "/auth/register", map[string]string{"email": u.email, "password": u.password}); status != http.StatusAccepted {
		t.Fatalf("register at the minimum: status %d, want 202", status)
	}
	h := newHandoff(t, base)
	h.code(t, u, newState(t))

	// GoTrue checks strength before the user lookup, so an existing address answers as a new one.
	status, body = serveJSON(t, register, "/auth/register", map[string]string{"email": u.email, "password": pwOfLen(passwordMin - 1)})
	if status != http.StatusBadRequest || fields(t, body)["error"] != wantErr {
		t.Errorf("repeat register one under the minimum: status %d, error %q; want 400 %q", status, fields(t, body)["error"], wantErr)
	}

	// The production shape does not forward the password, so GoTrue never sees the short one.
	short := idpUser{email: newEmail(), password: pwOfLen(passwordMin - 1)}
	if status, _ := serveJSON(t, registerHandler(authURL, false), "/auth/register", map[string]string{"email": short.email, "password": short.password}); status != http.StatusAccepted {
		t.Fatalf("non-forwarding register one under the minimum: status %d, want 202", status)
	}
	if status, out := signIn(t, base, short); status != http.StatusBadRequest || out["error_code"] != "invalid_credentials" {
		t.Errorf("sign in with the unforwarded password: status %d, error_code %v; want 400 invalid_credentials", status, out["error_code"])
	}
}

func TestIdP_AShortPasswordSetBeforeTheMinimumStillSignsIn(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	short := pwOfLen(passwordMin - 1)
	hash, err := bcrypt.GenerateFromPassword([]byte(short), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `UPDATE auth.users SET encrypted_password = $2 WHERE id = $1`, u.id, string(hash))
	u.password = short

	status, body := signIn(t, base, u)
	if status != http.StatusOK {
		t.Fatalf("sign in with the old short password: status %d, error_code %v; want 200", status, body["error_code"])
	}
	// weak_password.reasons proves the live minimum is above the old password length.
	weak, _ := body["weak_password"].(map[string]any)
	reasons, _ := weak["reasons"].([]any)
	if !slices.Contains(reasons, any("length")) {
		t.Errorf("weak_password.reasons = %v, want it to contain length", reasons)
	}

	h := newHandoff(t, base)
	s := newState(t)
	status, out := h.redeem(t, h.code(t, u, s), s)
	if status != http.StatusOK {
		t.Fatalf("exchange: status %d, error %q; want 200", status, field(t, out, "error"))
	}
	if m := fields(t, out); len(m) != 2 || m["access_token"] == "" || m["refresh_token"] == "" {
		t.Errorf("exchange body keys %v, want exactly access_token and refresh_token", keys(m))
	}

	requireSignIn(t, base, u, short+"x", http.StatusBadRequest, "invalid_credentials")
}

func TestIdP_ResetTakesAPasswordOfExactlyTheMinimum(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	r := confirmedRegistrant(t, gw)
	requestResetAccepted(t, gw, r.email)
	action, values := confirmForm(t, recoveryLink(t, r.email, 2))

	under := url.Values{}
	for k, vs := range values {
		under[k] = append([]string(nil), vs...)
	}
	under.Set("password", pwOfLen(passwordMin-1))
	status, _, err := postForm(action, under)
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("reset one under the minimum: status %d, err %v; want 400", status, err)
	}
	if n := recoveryTokenRows(t, r.email); n != 1 {
		t.Errorf("recovery token rows after the refused post = %d, want 1", n)
	}

	ten := pwOfLen(passwordMin)
	if got := submitReset(t, action, values, ten); got != siteURL+"/?reset=1" {
		t.Fatalf("reset at the minimum = %q, want %s/?reset=1", got, siteURL)
	}
	requireSignIn(t, base, r, ten, http.StatusOK, "")
}

func TestIdP_RegistrantSetPasswordTakesAPasswordOfExactlyTheMinimum(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	action, values := confirmForm(t, confirmationLink(t, u.email))
	if parsed, err := url.Parse(action); err != nil || parsed.Path != "/auth/invitation/password" {
		t.Fatalf("the confirmation form posts to %s, want /auth/invitation/password", action)
	}

	values.Set("password", pwOfLen(passwordMin-1))
	status, _, err := postForm(action, values)
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("set-password one under the minimum: status %d, err %v; want 400", status, err)
	}
	requireConfirmed(t, u.email, false)

	ten := pwOfLen(passwordMin)
	values.Set("password", ten)
	status, location, err := postForm(action, values)
	if err != nil || status != http.StatusSeeOther || location != siteURL+"/?verified=1" {
		t.Fatalf("set-password at the minimum: status %d, Location %q, err %v; want 303 %s/?verified=1", status, location, err, siteURL)
	}
	requireSignIn(t, base, u, ten, http.StatusOK, "")
}
