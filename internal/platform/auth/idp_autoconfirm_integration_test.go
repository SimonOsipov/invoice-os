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
	register := gateway.RegisterHandler(authURL, idpHTTP, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
}
