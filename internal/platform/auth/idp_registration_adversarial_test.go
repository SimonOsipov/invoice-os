package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// register posts one registration and returns the gateway's status and body.
func register(gw, email, password string) (int, string, error) {
	body := strings.NewReader(`{"email":"` + email + `","password":"` + password + `"}`)
	resp, err := noRedirect.Post(gw+"/auth/register", "application/json", body)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// mailCount returns how many mails mailpit holds for exactly this address, after GoTrue's synchronous send has settled.
func mailCount(t *testing.T, email string) int {
	t.Helper()
	time.Sleep(time.Second)
	var found mailpitSearch
	getJSON(t, mailEnv(t, "MAILPIT_URL")+"/api/v1/search?query="+url.QueryEscape(`to:"`+email+`"`), &found)
	n := 0
	for _, m := range found.Messages {
		for _, to := range m.To {
			if to.Address == email {
				n++
			}
		}
	}
	return n
}

func emailConfirmed(t *testing.T, email string) bool {
	t.Helper()
	var confirmed bool
	if err := superConn(t).QueryRow(context.Background(),
		`SELECT email_confirmed_at IS NOT NULL FROM auth.users WHERE email = $1`, email).Scan(&confirmed); err != nil {
		t.Fatalf("read %s: %v", email, err)
	}
	return confirmed
}

func TestIdP_TamperedLinkFailsAndLeavesTheRealOneUsable(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))
	link := confirmationLink(t, u.email)

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	tok := q.Get("token")
	if len(tok) < 2 {
		t.Fatalf("mailed token %q is too short to tamper", tok)
	}
	flip := byte('0')
	if tok[len(tok)-1] == '0' {
		flip = '1'
	}
	q.Set("token", tok[:len(tok)-1]+string(flip))
	parsed.RawQuery = q.Encode()

	if got := follow(t, parsed.String()); got != siteURL+"/?verify=failed" {
		t.Errorf("tampered link = %q, want %s/?verify=failed", got, siteURL)
	}
	if emailConfirmed(t, u.email) {
		t.Fatal("a tampered token confirmed the account")
	}
	if got := follow(t, link); got != siteURL+"/?verified=1" {
		t.Errorf("the real link after a tampered attempt = %q, want %s/?verified=1", got, siteURL)
	}
}

func TestIdP_ExpiredLinkFails(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))
	link := confirmationLink(t, u.email)

	// GoTrue's default GOTRUE_MAILER_OTP_EXP is 24h, measured from confirmation_sent_at.
	if _, err := superConn(t).Exec(context.Background(),
		`UPDATE auth.users SET confirmation_sent_at = now() - interval '25 hours' WHERE email = $1`, u.email); err != nil {
		t.Fatal(err)
	}
	if got := follow(t, link); got != siteURL+"/?verify=failed" {
		t.Errorf("expired link = %q, want %s/?verify=failed", got, siteURL)
	}
	if emailConfirmed(t, u.email) {
		t.Error("an expired token confirmed the account")
	}
	if status, body := signIn(t, base, u); status != http.StatusBadRequest || body["error_code"] != "email_not_confirmed" {
		t.Errorf("password grant after an expired link: status %d, body %v; want 400 email_not_confirmed", status, body)
	}
}

// GoTrue answers a repeat within the minute 429 over_email_send_rate_limit and sends nothing.
func TestIdP_RepeatRegistrationIsAcceptedAndMailsOnce(t *testing.T) {
	base := idpMailURL(t)
	gw := startGateway(t, base)
	u := registrant(t, gw)
	if n := mailCount(t, u.email); n != 1 {
		t.Fatalf("control: mailpit holds %d mails after one registration, want 1", n)
	}

	status, body, err := register(gw, u.email, u.password)
	if err != nil || status != http.StatusAccepted || !strings.Contains(body, `"verification_pending"`) {
		t.Fatalf("repeat registration: status %d, body %s, err %v; want 202 verification_pending", status, body, err)
	}
	if n := mailCount(t, u.email); n != 1 {
		t.Errorf("mailpit holds %d mails after a repeat within the minute, want 1 (GoTrue rate-limits the resend)", n)
	}
}

// GoTrue answers a confirmed address 200 with a sanitized user and sends nothing.
func TestIdP_VerifiedAddressRegisteringAgainSendsNoMailAndKeepsThePassword(t *testing.T) {
	base := idpMailURL(t)
	gw := startGateway(t, base)
	u := registrant(t, gw)
	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q", got)
	}

	other := idpUser{email: u.email, password: "pw-other-" + u.password}
	status, body, err := register(gw, other.email, other.password)
	if err != nil || status != http.StatusAccepted || !strings.Contains(body, `"verification_pending"`) {
		t.Fatalf("registration of a verified address: status %d, body %s, err %v; want 202 verification_pending", status, body, err)
	}
	if n := mailCount(t, u.email); n != 1 {
		t.Errorf("mailpit holds %d mails, want 1: a verified address must get no second mail", n)
	}
	accessToken(t, base, u)
	if status, _ := signIn(t, base, other); status == http.StatusOK {
		t.Error("the second registration's password signs in; it replaced the verified account's password")
	}
}
