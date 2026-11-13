package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The mailbox owner alone sets the password, on the page the confirmation mail opens.
func TestIdP_RegistrationPasswordsNeverSignIn(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	p1, p2, p3 := u.password, "pw-second-"+uuid.NewString(), "pw-chosen-"+uuid.NewString()
	if status, body, err := register(gw, u.email, p2); err != nil || status != http.StatusAccepted {
		t.Fatalf("second registration: status %d, body %s, err %v; want 202", status, body, err)
	}
	// invalid_credentials, not email_not_confirmed: GoTrue stored neither password.
	requireSignIn(t, base, u, p1, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, base, u, p2, http.StatusBadRequest, "invalid_credentials")

	state := newState(t)
	action, values := confirmFormWithState(t, confirmationLink(t, u.email), state)
	if parsed, err := url.Parse(action); err != nil || parsed.Path != "/auth/invitation/password" {
		t.Fatalf("the confirmation form posts to %s, want /auth/invitation/password", action)
	}
	if !values.Has("password") {
		t.Fatalf("the confirmation form has no password field; inputs: %v", values)
	}
	values.Set("password", p3)
	status, location, err := postForm(action, values)
	if err != nil || status != http.StatusSeeOther || !strings.HasPrefix(location, siteURL+"/?verified=1&handoff=") {
		t.Fatalf("set-password POST: status %d, Location %q, err %v; want 303 %s/?verified=1&handoff=<code>", status, location, err, siteURL)
	}

	requireSignIn(t, base, u, p3, http.StatusOK, "")
	requireSignIn(t, base, u, p1, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, base, u, p2, http.StatusBadRequest, "invalid_credentials")
}
