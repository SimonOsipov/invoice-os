package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// noPendingInvite is the lookup of a database with no invites.
func noPendingInvite(context.Context, string) (bool, error) { return false, nil }

// registerHandler builds the gateway's register handler; pass forward to RegisterHandler's new parameter, never a constant.
func registerHandler(authURL *url.URL, forward bool) http.Handler {
	_ = forward
	return gateway.RegisterHandler(authURL, idpHTTP, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), noPendingInvite, nil)
}

// idp-es256 runs the fork's mail posture (autoconfirm on, SMTP blank), so this is the fork's
// registration then sign-in, and the repeat that e2e/api/registration.spec.ts asserts.
func TestIdP_AutoconfirmedRegistrationSignsInAndARepeatAnswers202(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := idpUser{email: "idp-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email)
	})
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	register := registerHandler(authURL, true)
	creds := map[string]string{"email": u.email, "password": u.password}

	if status, body := serveJSON(t, register, "/auth/register", creds); status != http.StatusAccepted {
		t.Fatalf("first register: status %d, body %s; want 202", status, body)
	}
	var confirmed bool
	if err := conn.QueryRow(context.Background(),
		`SELECT email_confirmed_at IS NOT NULL FROM auth.users WHERE email = $1`, u.email).Scan(&confirmed); err != nil {
		t.Fatalf("read auth.users: %v", err)
	}
	if !confirmed {
		t.Fatal("the registered user is unconfirmed; autoconfirm did not apply")
	}

	// The upstream answer the spec's comment names.
	status, body := postJSON(t, base+"/signup", creds)
	if status != http.StatusUnprocessableEntity || body["error_code"] != "user_already_exists" {
		t.Errorf("repeat /signup: status %d, error_code %v; want 422 user_already_exists", status, body["error_code"])
	}
	if status, body := serveJSON(t, register, "/auth/register", creds); status != http.StatusAccepted {
		t.Errorf("repeat register: status %d, body %s; want 202", status, body)
	}

	// No mail was sent, yet the password grant succeeds.
	h := newHandoff(t, base)
	h.code(t, u, newState(t))
	// A gateway that does not forward signs up with a random password, though GoTrue autoconfirms.
	v := idpUser{email: "idp-" + uuid.NewString() + "@example.test", password: u.password}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, v.email)
	})
	if status, body := serveJSON(t, registerHandler(authURL, false), "/auth/register", map[string]string{"email": v.email, "password": v.password}); status != http.StatusAccepted {
		t.Fatalf("non-forwarding register: status %d, body %s; want 202", status, body)
	}
	if status, body := signIn(t, base, v); status != http.StatusBadRequest || body["error_code"] != "invalid_credentials" {
		t.Errorf("sign in with the non-forwarded register password: status %d, error_code %v; want 400 invalid_credentials", status, body["error_code"])
	}
}
