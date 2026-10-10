package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	inviteResendSent = `{"status":"sent"}`
	inviteResendHeld = `{"status":"held"}`
)

func (w inviteWorld) resendInvite(t *testing.T) (int, string) {
	t.Helper()
	return postGW(t, w.gw+"/auth/invitation/resend", map[string]string{"token": w.token})
}

func (w inviteWorld) requireResend(t *testing.T, what string, status int, body string) {
	t.Helper()
	gotStatus, gotBody := w.resendInvite(t)
	if gotStatus != status || strings.TrimSpace(gotBody) != body {
		t.Fatalf("%s: status %d, body %q; want %d %s", what, gotStatus, gotBody, status, body)
	}
}

func requireMails(t *testing.T, email string, want int, what string) {
	t.Helper()
	if n := mailCount(t, email); n != want {
		t.Fatalf("%s: mailpit holds %d mails for %s, want %d", what, n, email, want)
	}
}

// Real GoTrue answers 429 inside the 60 s cooldown and 200 after it; the token route turns that into held and sent.
func TestIdP_InviteResendTellsSentFromHeld(t *testing.T) {
	w := newInviteWorld(t, "resend09-")
	u := idpUser{email: w.email}
	w.register(t)
	registrationLink := confirmationLink(t, u.email)

	w.requireResend(t, "resend inside the cooldown", http.StatusOK, inviteResendHeld)
	requireMails(t, u.email, 1, "after a held resend")

	backdateCooldown(t, u.email)
	w.requireResend(t, "resend after the cooldown", http.StatusOK, inviteResendSent)
	requireMails(t, u.email, 2, "after a real resend")

	// A second tab's press meets the same cooldown.
	w.requireResend(t, "resend again", http.StatusOK, inviteResendHeld)
	requireMails(t, u.email, 2, "after a second held resend")

	var newest string
	for _, l := range confirmationLinks(t, u.email, 2) {
		if l != registrationLink {
			newest = l
		}
	}
	if newest == "" {
		t.Fatal("the resend mailed the registration link again, want a new one")
	}
	submit := func(link string) string {
		action, values := confirmForm(t, link)
		values.Set("password", "pw-"+uuid.NewString())
		_, location, err := postForm(action, values)
		if err != nil {
			t.Fatalf("POST %s: %v", action, err)
		}
		return location
	}
	if got := submit(registrationLink); got != siteURL+"/?verify=failed" {
		t.Errorf("the registration link = %q, want %s/?verify=failed", got, siteURL)
	}
	if got := submit(newest); got != siteURL+"/?verified=1" {
		t.Errorf("the newest link = %q, want %s/?verified=1", got, siteURL)
	}
}

// Another device confirmed the account: the page must learn that, and no mail goes out.
func TestIdP_InviteResendForAConfirmedAccountIs409(t *testing.T) {
	w := newInviteWorld(t, "resend09c-")
	u := idpUser{email: w.email}
	w.register(t)
	w.setPasswordFromMail(t, "pw-"+uuid.NewString())

	w.requireResend(t, "resend for a confirmed account", http.StatusConflict, `{"error":"account_exists"}`)
	requireMails(t, u.email, 1, "after a refused resend")
}

// Without the auth.users read the preview reads unknown: a 200 from GoTrue may be a confirmed account's silent no-op.
func TestIdP_InviteResendWithoutTheStateGrantIsMaybeNotSent(t *testing.T) {
	revoke := func(t *testing.T) {
		t.Helper()
		exec(t, superConn(t), `REVOKE USAGE ON SCHEMA auth FROM auth_hook_reader`)
		t.Cleanup(func() {
			if _, err := db.GrantAccountStateRead(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL")); err != nil {
				t.Errorf("re-run the grant: %v", err)
			}
		})
	}

	t.Run("confirmed elsewhere", func(t *testing.T) {
		w := newInviteWorld(t, "resend09m-")
		u := idpUser{email: w.email}
		w.register(t)
		revoke(t)
		w.setPasswordFromMail(t, "pw-"+uuid.NewString())

		w.requireResend(t, "resend without the state grant", http.StatusOK, `{"status":"maybe"}`)
		requireMails(t, u.email, 1, "after a maybe")
	})

	t.Run("unconfirmed inside the cooldown", func(t *testing.T) {
		w := newInviteWorld(t, "resend09h-")
		w.register(t)
		revoke(t)

		w.requireResend(t, "resend inside the cooldown without the state grant", http.StatusOK, inviteResendHeld)
		requireMails(t, w.email, 1, "after a held resend")
	})
}
