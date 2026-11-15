package auth_test

import (
	"context"
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
	if err != nil || status != http.StatusSeeOther || location != siteURL+"/?verified=1" {
		t.Fatalf("set-password POST: status %d, Location %q, err %v; want 303 %s/?verified=1", status, location, err, siteURL)
	}

	requireSignIn(t, base, u, p3, http.StatusOK, "")
	requireSignIn(t, base, u, p1, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, base, u, p2, http.StatusBadRequest, "invalid_credentials")
}

// setPasswordFromLink opens the mailed link with state, requires the set-password form, posts password and requires the verified redirect.
func setPasswordFromLink(t *testing.T, link, state, password string) (action string, values url.Values, location string) {
	t.Helper()
	action, values = confirmFormWithState(t, link, state)
	if parsed, err := url.Parse(action); err != nil || parsed.Path != "/auth/invitation/password" {
		t.Fatalf("the mailed link's form posts to %s, want /auth/invitation/password", action)
	}
	if !values.Has("password") {
		t.Fatalf("the mailed link's form has no password field; inputs: %v", values)
	}
	values.Set("password", password)
	status, location, err := postForm(action, values)
	if err != nil || status != http.StatusSeeOther || location != siteURL+"/?verified=1" {
		t.Fatalf("set-password POST: status %d, Location %q, err %v; want 303 %s/?verified=1", status, location, err, siteURL)
	}
	return action, values, location
}

func requireConfirmed(t *testing.T, email string, want bool) {
	t.Helper()
	var confirmed bool
	if err := superConn(t).QueryRow(context.Background(), `SELECT email_confirmed_at IS NOT NULL FROM auth.users WHERE email = $1`, email).Scan(&confirmed); err != nil {
		t.Fatalf("read the user: %v", err)
	}
	if confirmed != want {
		t.Errorf("email_confirmed_at set = %v, want %v", confirmed, want)
	}
}

func TestIdP_AttackerFirstLosesToTheOwnersSetPassword(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	attacker := registrant(t, gw)
	pN := "pw-owner-" + uuid.NewString()
	if status, body, err := register(gw, attacker.email, "pw-owner-form-"+uuid.NewString()); err != nil || status != http.StatusAccepted {
		t.Fatalf("the owner's registration: status %d, body %s, err %v; want 202", status, body, err)
	}

	setPasswordFromLink(t, confirmationLink(t, attacker.email), newState(t), pN)

	requireSignIn(t, base, attacker, attacker.password, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, base, attacker, pN, http.StatusOK, "")
}

// A squat made at GoTrue directly, as the gateway did before it discarded the password.
func TestIdP_APreFixSquatLosesItsPasswordAtTheOwnersConfirm(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	squat := idpUser{email: "idp-mail-" + uuid.NewString() + "@example.test", password: "pw-squat-" + uuid.NewString()}
	t.Cleanup(func() {
		_, _ = superConn(t).Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, squat.email)
	})
	squat.register(t, base)
	if status, body, err := register(gw, squat.email, "pw-owner-form-"+uuid.NewString()); err != nil || status != http.StatusAccepted {
		t.Fatalf("the owner's registration: status %d, body %s, err %v; want 202", status, body, err)
	}
	pN := "pw-owner-" + uuid.NewString()

	setPasswordFromLink(t, confirmationLink(t, squat.email), newState(t), pN)

	requireSignIn(t, base, squat, squat.password, http.StatusBadRequest, "invalid_credentials")
	requireSignIn(t, base, squat, pN, http.StatusOK, "")
}

func TestIdP_OwnerFirstKeepsTheSetPasswordAgainstALaterRegistration(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	owner := registrant(t, gw)
	pA := "pw-attacker-" + uuid.NewString()
	if status, body, err := register(gw, owner.email, pA); err != nil || status != http.StatusAccepted {
		t.Fatalf("the attacker's registration: status %d, body %s, err %v; want 202", status, body, err)
	}
	pN := "pw-owner-" + uuid.NewString()

	setPasswordFromLink(t, confirmationLink(t, owner.email), newState(t), pN)

	requireSignIn(t, base, owner, pN, http.StatusOK, "")
	requireSignIn(t, base, owner, pA, http.StatusBadRequest, "invalid_credentials")
}

func TestIdP_ASquattedInviteeSetsThePasswordAndTheSquatterStaysOut(t *testing.T) {
	var squat idpUser
	w := newInviteWorldFor(t, "secaud07-"+uuid.NewString()+"@example.com", func(w inviteWorld) {
		squat = idpUser{email: w.email, password: "pw-squat-" + uuid.NewString()}
		squat.register(t, w.base)
	})
	squatLink := confirmationLink(t, w.email)
	backdateCooldown(t, w.email)
	w.requireResend(t, "resend after the cooldown", http.StatusOK, inviteResendSent)
	var link string
	for _, l := range confirmationLinks(t, w.email, 2) {
		if l != squatLink {
			link = l
		}
	}
	if link == "" {
		t.Fatal("the resend mailed no new link")
	}
	if strings.Contains(link, "invite=1") {
		t.Errorf("the resend link %q carries invite=1; the squatter's sign-up metadata set no invited flag", link)
	}
	pN, state := "pw-invitee-"+uuid.NewString(), newState(t)

	setPasswordFromLink(t, link, state, pN)

	requireSignIn(t, w.base, squat, squat.password, http.StatusBadRequest, "invalid_credentials")
	invitee := idpUser{email: w.email, password: pN}
	requireSignIn(t, w.base, invitee, pN, http.StatusOK, "")
	if code, body := w.accept(tenantlessCaller(t, w, invitee)); code != http.StatusOK {
		t.Errorf("accept: status %d, body %s; want 200", code, body)
	}
}

func TestIdP_ASpentSignupLinkCannotResetTheChosenPassword(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	p1, p2 := "pw-first-"+uuid.NewString(), "pw-second-"+uuid.NewString()
	action, values, _ := setPasswordFromLink(t, confirmationLink(t, u.email), newState(t), p1)
	values.Set("password", p2)

	status, location, err := postForm(action, values)

	if err != nil || status != http.StatusSeeOther || location != siteURL+"/?verify=failed" {
		t.Errorf("replaying the spent link: status %d, Location %q, err %v; want 303 %s/?verify=failed", status, location, err, siteURL)
	}
	requireSignIn(t, base, u, p1, http.StatusOK, "")
	requireSignIn(t, base, u, p2, http.StatusBadRequest, "invalid_credentials")
}

// "Forgot password" is the stated recovery of a registrant who holds no known password.
func TestIdP_ARegistrantWithoutAKnownPasswordRecoversThroughReset(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	spendToken := func(t *testing.T, u idpUser) {
		link := confirmationLink(t, u.email)
		parsed, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		status, body := postJSON(t, base+"/verify", map[string]string{"type": "signup", "token_hash": parsed.Query().Get("token")})
		if status != http.StatusOK {
			t.Fatalf("POST /verify with the signup token: status %d, body %v; want 200", status, body)
		}
	}
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T, u idpUser)
	}{
		{"link never opened", func(t *testing.T, u idpUser) { requireConfirmed(t, u.email, false) }},
		{"signup token spent with no password set", spendToken},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := registrant(t, gw)
			c.setUp(t, u)
			requireSignIn(t, base, u, u.password, http.StatusBadRequest, "invalid_credentials")
			requestResetAccepted(t, gw, u.email)
			action, values := confirmForm(t, recoveryLink(t, u.email, 2))
			pN := "pw-recovered-" + uuid.NewString()

			if got := submitReset(t, action, values, pN); got != siteURL+"/?reset=1" {
				t.Fatalf("reset redirect = %q, want %s/?reset=1", got, siteURL)
			}

			requireSignIn(t, base, u, pN, http.StatusOK, "")
			requireConfirmed(t, u.email, true)
		})
	}
}
