package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// tenantlessCaller verifies the password grant's token and returns the tenant-less identity accept needs.
func tenantlessCaller(t *testing.T, w inviteWorld, u idpUser) auth.Identity {
	t.Helper()
	tok := accessToken(t, w.base, u)
	id, err := idpVerifier(t, w.base).Verify(context.Background(), tok)
	if err != nil || id.TenantID != "" {
		t.Fatalf("Verify the grant for %s: identity %+v, err %v; want a tenant-less identity", u.email, id, err)
	}
	return id
}

// Route 1: /auth/register for an invited address answers like any other and creates nothing.
func TestIdP_SignUpForAnInvitedAddressCreatesNoAccount(t *testing.T) {
	w := newInviteWorldAt(t, "resend2-01r1-", "example.com")
	conn := superConn(t)
	control := "resend2-01r1-ctl-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, control) })
	password := "pw-" + uuid.NewString()

	invitedStatus, invitedBody := postGW(t, w.gw+"/auth/register", map[string]string{"email": w.email, "password": password})
	controlStatus, controlBody := postGW(t, w.gw+"/auth/register", map[string]string{"email": control, "password": password})

	if invitedStatus != http.StatusAccepted || controlStatus != http.StatusAccepted {
		t.Fatalf("register: invited %d %s, control %d %s; want 202 for both", invitedStatus, invitedBody, controlStatus, controlBody)
	}
	if invitedBody != controlBody {
		t.Errorf("invited body %s differs from the control's %s", invitedBody, controlBody)
	}

	// GoTrue folds case on sign-up, so an upper-cased or padded form of the invited address must be refused too.
	for _, form := range []string{strings.ToUpper(w.email), "  " + w.email + " ", strings.ToUpper(w.email[:1]) + w.email[1:]} {
		status, body := postGW(t, w.gw+"/auth/register", map[string]string{"email": form, "password": password})
		if status != http.StatusAccepted || body != controlBody {
			t.Errorf("register %q: status %d, body %s; want 202 %s", form, status, body, controlBody)
		}
	}

	if n := mailCount(t, control); n != 1 {
		t.Fatalf("control mails = %d, want 1: the control proves a sign-up still sends", n)
	}
	if n := mailCount(t, w.email); n != 0 {
		t.Errorf("invited address mails = %d, want 0", n)
	}
	for _, c := range []struct {
		email string
		want  int
	}{{control, 1}, {w.email, 0}} {
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM auth.users WHERE email = $1`, c.email).Scan(&n); err != nil {
			t.Fatalf("count auth.users: %v", err)
		}
		if n != c.want {
			t.Errorf("auth.users rows for %s = %d, want %d", c.email, n, c.want)
		}
	}

	status, grant := signIn(t, w.base, idpUser{email: w.email, password: password})
	if status != http.StatusBadRequest || grant["error_code"] != "invalid_credentials" {
		t.Errorf("password grant for the invited address: status %d, body %v; want 400 invalid_credentials", status, grant)
	}
}

// Route 2: the link makes the account with a password nobody keeps; only the one chosen from the mail signs in.
func TestIdP_InviteLinkCreatesTheAccountWithoutAUsablePassword(t *testing.T) {
	w := newInviteWorldAt(t, "resend2-01r2-", "example.com")
	u := idpUser{email: w.email}
	pb, pn := "body-"+uuid.NewString(), "mail-"+uuid.NewString()

	status, body := postGW(t, w.gw+"/auth/invitation/register", map[string]string{"token": w.token, "password": pb})
	if status != http.StatusAccepted {
		t.Fatalf("invitee registration: status %d, body %s; want 202", status, body)
	}
	var invited *string
	if err := superConn(t).QueryRow(context.Background(), `SELECT raw_user_meta_data->>'invited' FROM auth.users WHERE email = $1`, w.email).Scan(&invited); err != nil {
		t.Fatalf("read the registered user: %v", err)
	}
	if invited == nil || *invited != "true" {
		t.Errorf("raw_user_meta_data.invited = %v, want true", invited)
	}

	w.setPasswordFromMail(t, pn)

	requireSignIn(t, w.base, u, pb, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, w.base, u, pn, http.StatusOK, "")
}

// A forwarded link: whoever registers first sends the one mail to the invitee, a later registration is refused as unconfirmed and sends none, and only the invitee's choice signs in.
func TestIdP_ForwardedLinkHolderNeverOwnsThePassword(t *testing.T) {
	w := newInviteWorldAt(t, "resend2-01fw-", "example.com")
	u := idpUser{email: w.email}
	ph, pi, pn := "holder-"+uuid.NewString(), "invitee-"+uuid.NewString(), "mail-"+uuid.NewString()
	registerURL := w.gw + "/auth/invitation/register"

	holderStatus, holderBody := postGW(t, registerURL, map[string]string{"token": w.token, "password": ph})
	if holderStatus != http.StatusAccepted {
		t.Fatalf("holder registration: status %d, body %s; want 202", holderStatus, holderBody)
	}
	if n := mailCount(t, w.email); n != 1 {
		t.Fatalf("mails after the holder's registration = %d, want 1", n)
	}
	// Backdated, GoTrue's 60 s send window no longer blocks a re-send, so a second mail can only come from a second GoTrue call.
	tag, err := superConn(t).Exec(context.Background(), `UPDATE auth.users SET confirmation_sent_at = confirmation_sent_at - interval '2 minutes' WHERE email = $1`, w.email)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate confirmation_sent_at: %v, rows %d; want 1", err, tag.RowsAffected())
	}

	inviteeStatus, inviteeBody := postGW(t, registerURL, map[string]string{"token": w.token, "password": pi})

	if inviteeStatus != http.StatusConflict || inviteeBody != `{"error":"account_unconfirmed"}`+"\n" {
		t.Errorf("second registration: status %d, body %q; want 409 account_unconfirmed", inviteeStatus, inviteeBody)
	}
	if n := mailCount(t, w.email); n != 1 {
		t.Fatalf("mails after the second registration = %d, want still 1", n)
	}
	w.setPasswordFromMail(t, pn)
	requireSignIn(t, w.base, u, ph, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, w.base, u, pi, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, w.base, u, pn, http.StatusOK, "")
	if code, body := w.accept(tenantlessCaller(t, w, idpUser{email: w.email, password: pn})); code != http.StatusOK {
		t.Errorf("accept: status %d, body %s; want 200", code, body)
	}
}

// GoTrue's /verify spends the token on the first POST; the replay must not reach PUT /user.
func TestIdP_SpentConfirmationLinkCannotResetTheChosenPassword(t *testing.T) {
	w := newInviteWorldAt(t, "resend2-01sp-", "example.com")
	u := idpUser{email: w.email}
	p1, p2 := "first-"+uuid.NewString(), "second-"+uuid.NewString()
	w.register(t)
	action, values := w.setPasswordFromMail(t, p1)
	values.Set("password", p2)

	status, location, err := postForm(action, values)

	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	if status != http.StatusSeeOther || location != siteURL+"/?verify=failed" {
		t.Errorf("replaying the spent token: status %d, Location %q; want 303 %s/?verify=failed", status, location, siteURL)
	}
	requireSignIn(t, w.base, u, p1, http.StatusOK, "")
	requireSignIn(t, w.base, u, p2, http.StatusBadRequest, "invalid_credentials")
}

// An address that confirmed an account before it was invited: the link registration answers 409, mails nothing, spends no claim and leaves the password alone.
func TestIdP_LinkRegistrationForAConfirmedAccountChangesNothing(t *testing.T) {
	email := "resend2-01cf-" + uuid.NewString() + "@example.com"
	u := idpUser{email: email}
	pa, pb := "own-"+uuid.NewString(), "body-"+uuid.NewString()
	w := newInviteWorldFor(t, email, func(w inviteWorld) {
		status, body := postGW(t, w.gw+"/auth/register", map[string]string{"email": email, "password": pa})
		if status != http.StatusAccepted {
			t.Fatalf("/auth/register before the invite: status %d, body %s; want 202", status, body)
		}
		if got := follow(t, confirmationLink(t, email)); got != siteURL+"/?verified=1" {
			t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
		}
	})
	requireSignIn(t, w.base, u, pa, http.StatusOK, "")
	before := mailCount(t, email)
	if before != 1 {
		t.Fatalf("mails before the link registration = %d, want 1", before)
	}

	status, body := postGW(t, w.gw+"/auth/invitation/register", map[string]string{"token": w.token, "password": pb})

	if status != http.StatusConflict || body != `{"error":"account_exists"}`+"\n" {
		t.Fatalf("link registration: status %d, body %q; want 409 account_exists", status, body)
	}
	if after := mailCount(t, email); after != before {
		t.Errorf("mails after the link registration = %d, want %d", after, before)
	}
	var claimed bool
	if err := superConn(t).QueryRow(context.Background(),
		`SELECT registered_token_hash IS NOT NULL FROM invitations WHERE tenant_id = $1 AND invitee_email = $2`, w.tenant, email).Scan(&claimed); err != nil {
		t.Fatalf("read the invitation: %v", err)
	}
	if claimed {
		t.Error("registered_token_hash is set: the preview refusal must come before the claim")
	}
	requireSignIn(t, w.base, u, pa, http.StatusOK, "")
	requireSignIn(t, w.base, u, pb, http.StatusBadRequest, "invalid_credentials")
}
