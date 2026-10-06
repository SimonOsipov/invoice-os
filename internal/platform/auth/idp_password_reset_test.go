package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// recoverySubject is idp-up.sh's GOTRUE_MAILER_SUBJECTS_RECOVERY for idp-mail.
const recoverySubject = "Reset your ASComply password"

// idp-up.sh points idp-mail's recovery link at the test gateway on localhost, like linkPrefix.
var resetLinkPrefix = fmt.Sprintf("http://localhost:%d/auth/reset-password?token=", verifyPort)

func requestReset(t *testing.T, gw, email string) (int, string, time.Duration) {
	t.Helper()
	return postMailRequest(t, gw+"/auth/request-password-reset", email)
}

// recoveryLink returns the action link of the one mail with the recovery subject among the address's total mails.
func recoveryLink(t *testing.T, email string, total int) string {
	t.Helper()
	var links []string
	for _, msg := range mailsFor(t, email, total) {
		if msg.Subject != recoverySubject {
			continue
		}
		link, err := actionLink(msg.HTML)
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, link)
	}
	if len(links) != 1 {
		t.Fatalf("%s has %d mails with the subject %q among %d, want exactly 1", email, len(links), recoverySubject, total)
	}
	return links[0]
}

// backdateRecovery moves the last recovery mail d into the past.
func backdateRecovery(t *testing.T, email string, d time.Duration) {
	t.Helper()
	if _, err := superConn(t).Exec(context.Background(),
		`UPDATE auth.users SET recovery_sent_at = now() - make_interval(secs => $2) WHERE email = $1`, email, d.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, query, email string) int {
	t.Helper()
	var n int
	if err := superConn(t).QueryRow(context.Background(), query, email).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func recoveryTokenRows(t *testing.T, email string) int {
	return countRows(t, `SELECT count(*) FROM auth.one_time_tokens ot JOIN auth.users u ON u.id = ot.user_id
		WHERE u.email = $1 AND ot.token_type = 'recovery_token'`, email)
}

func sessionRows(t *testing.T, email string) int {
	return countRows(t, `SELECT count(*) FROM auth.sessions s JOIN auth.users u ON u.id = s.user_id WHERE u.email = $1`, email)
}

// confirmedRegistrant registers and confirms through the mailed link.
func confirmedRegistrant(t *testing.T, gw string) idpUser {
	t.Helper()
	u := registrant(t, gw)
	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	return u
}

func requestResetAccepted(t *testing.T, gw, email string) {
	t.Helper()
	status, body, _ := requestReset(t, gw, email)
	requireResendAccepted(t, "reset request for "+email, status, body)
}

// submitReset posts the page's form with password and returns the redirect target.
func submitReset(t *testing.T, action string, values url.Values, password string) string {
	t.Helper()
	v := url.Values{}
	for k, vs := range values {
		v[k] = append([]string(nil), vs...)
	}
	v.Set("password", password)
	status, location, err := postForm(action, v)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	if status != http.StatusSeeOther {
		t.Fatalf("submitting the reset form: status %d, want 303", status)
	}
	return location
}

func requireSignIn(t *testing.T, base string, u idpUser, password string, wantStatus int, wantCode string) {
	t.Helper()
	status, body := signIn(t, base, idpUser{email: u.email, password: password})
	if status != wantStatus || (wantCode != "" && body["error_code"] != wantCode) {
		t.Errorf("sign in with %q: status %d, body %v; want %d %s", password, status, body, wantStatus, wantCode)
	}
}

// Five account states answer the same bytes, none earlier than the floor; it pins the request handler through real GoTrue.
func TestIdP_ResetRequestAnswersAlikeForEveryAccountState(t *testing.T) {
	base := idpMailURL(t)
	const floor = 400 * time.Millisecond
	gw, logs := startGateway(t, base, floor, nil)
	unknownAddress := func() string { return "idp-mail-" + uuid.NewString() + "@example.test" }

	type answer struct {
		name, body string
		elapsed    time.Duration
	}
	var answers []answer
	record := func(name, email string) {
		t.Helper()
		status, body, elapsed := requestReset(t, gw, email)
		requireResendAccepted(t, name, status, body)
		answers = append(answers, answer{name, body, elapsed})
	}

	record("unknown address", unknownAddress())

	unconfirmed := registrant(t, gw)
	if n := mailCount(t, unconfirmed.email); n != 1 {
		t.Fatalf("mailpit holds %d mails after registering, want 1", n)
	}
	record("unconfirmed registrant", unconfirmed.email)
	if n := mailCount(t, unconfirmed.email); n != 2 {
		t.Errorf("mailpit holds %d mails after a reset request, want 2 (registration and recovery)", n)
	}
	record("unconfirmed registrant inside the cooldown", unconfirmed.email)
	if n := strings.Count(logs.String(), "reset-request: gotrue email send rate limit"); n != 1 {
		t.Errorf("%d email-send-rate-limit lines in the gateway log, want 1: %s", n, logs.String())
	}
	if n := mailCount(t, unconfirmed.email); n != 2 {
		t.Errorf("mailpit holds %d mails after a request inside the cooldown, want still 2", n)
	}

	confirmed := confirmedRegistrant(t, gw)
	record("confirmed registrant", confirmed.email)
	if n := mailCount(t, confirmed.email); n != 2 {
		t.Errorf("mailpit holds %d mails for a confirmed registrant, want 2 (confirmation and recovery)", n)
	}

	overLimit := unknownAddress()
	for i := 1; i <= 4; i++ {
		record(fmt.Sprintf("unknown address over its limit, request %d", i), overLimit)
	}
	if n := strings.Count(logs.String(), "reset-request: limit reached"); n != 1 || !strings.Contains(logs.String(), `"limit":"address"`) {
		t.Errorf("want exactly one limit=address line for the fourth request, got %d: %s", n, logs.String())
	}

	for _, a := range answers {
		if a.body != answers[0].body {
			t.Errorf("%s body = %q, want byte-equal to the unknown-address body %q", a.name, a.body, answers[0].body)
		}
		if a.elapsed < floor {
			t.Errorf("%s answered after %v, want no earlier than %v", a.name, a.elapsed, floor)
		}
	}
}

func TestIdP_ResetMailIsBrandedAndLinksToThePage(t *testing.T) {
	gw, _ := startGateway(t, idpMailURL(t), 0, nil)
	r := confirmedRegistrant(t, gw)
	requestResetAccepted(t, gw, r.email)

	msgs := mailsFor(t, r.email, 2)
	var recovery []mailpitMessage
	for _, m := range msgs {
		if m.Subject == recoverySubject {
			recovery = append(recovery, m)
		}
	}
	if len(recovery) != 1 {
		t.Fatalf("%d of %d mails have the subject %q, want exactly 1", len(recovery), len(msgs), recoverySubject)
	}
	html := recovery[0].HTML
	if html == "" {
		t.Fatal("the recovery mail has no HTML body")
	}

	var srcs []string
	for _, m := range imgSrcRe.FindAllStringSubmatch(html, -1) {
		srcs = append(srcs, m[1])
	}
	if len(srcs) == 0 {
		t.Fatalf("recovery mail holds no <img src>: %s", html)
	}
	for _, src := range srcs {
		if src != accountmail.LogoURL {
			t.Errorf("<img src> = %q, want accountmail.LogoURL %q", src, accountmail.LogoURL)
		}
	}
	if !strings.Contains(html, "Password reset") {
		t.Error("recovery mail lacks the eyebrow \"Password reset\"")
	}

	link, err := actionLink(html)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, resetLinkPrefix) {
		t.Errorf("mailed link = %q, want the configured absolute %s…", link, resetLinkPrefix)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse the mailed link %q: %v", link, err)
	}
	if q := parsed.Query(); q.Get("type") != "recovery" || q.Get("token") == "" {
		t.Errorf("mailed link query = %v, want type=recovery and a token", q)
	}
}

// Opening the link spends nothing; the form ends every session and the new password replaces the old.
func TestIdP_ResetLinkOpensAPageThenTheFormEndsEverySession(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	r := confirmedRegistrant(t, gw)
	oldPassword := r.password
	const newPassword = "pw-new-after-reset"

	status, session := signIn(t, base, r)
	rt0, _ := session["refresh_token"].(string)
	if status != http.StatusOK || rt0 == "" {
		t.Fatalf("sign in before the reset: status %d, body %v", status, session)
	}
	requestResetAccepted(t, gw, r.email)
	link := recoveryLink(t, r.email, 2)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodGet} {
		open(t, link, method)
	}
	if n := recoveryTokenRows(t, r.email); n != 1 {
		t.Fatalf("auth.one_time_tokens holds %d recovery rows after opening the link, want 1", n)
	}
	requireSignIn(t, base, r, oldPassword, http.StatusOK, "")

	action, values := confirmForm(t, link)
	if got := submitReset(t, action, values, newPassword); got != siteURL+"/?reset=1" {
		t.Fatalf("reset redirect = %q, want %s/?reset=1", got, siteURL)
	}
	if n := sessionRows(t, r.email); n != 0 {
		t.Errorf("auth.sessions holds %d rows for the user after the reset, want 0", n)
	}
	requireSignIn(t, base, r, newPassword, http.StatusOK, "")
	requireSignIn(t, base, r, oldPassword, http.StatusBadRequest, "invalid_credentials")
	if status, body := postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": rt0}); status < 400 || status >= 500 {
		t.Errorf("refresh grant with a token from before the reset: status %d, body %v; want 4xx", status, body)
	}
}

func TestIdP_SpentTamperedAndExpiredResetLinksFail(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)

	t.Run("spent", func(t *testing.T) {
		s := confirmedRegistrant(t, gw)
		requestResetAccepted(t, gw, s.email)
		action, values := confirmForm(t, recoveryLink(t, s.email, 2))
		const newPassword = "pw-new-spent"

		if got := submitReset(t, action, values, newPassword); got != siteURL+"/?reset=1" {
			t.Fatalf("first submit = %q, want %s/?reset=1", got, siteURL)
		}
		if got := submitReset(t, action, values, "pw-other-spent"); got != siteURL+"/?reset=failed" {
			t.Errorf("second submit = %q, want %s/?reset=failed", got, siteURL)
		}
		requireSignIn(t, base, s, newPassword, http.StatusOK, "")
	})

	t.Run("tampered_then_expired", func(t *testing.T) {
		e := confirmedRegistrant(t, gw)
		requestResetAccepted(t, gw, e.email)
		action, values := confirmForm(t, recoveryLink(t, e.email, 2))

		tampered := url.Values{}
		for k, vs := range values {
			tampered[k] = vs
		}
		tampered.Set("token", values.Get("token")+"x")
		if got := submitReset(t, action, tampered, "pw-new-tampered"); got != siteURL+"/?reset=failed" {
			t.Errorf("tampered submit = %q, want %s/?reset=failed", got, siteURL)
		}
		if n := recoveryTokenRows(t, e.email); n != 1 {
			t.Errorf("auth.one_time_tokens holds %d recovery rows after a tampered submit, want the real one to remain (1)", n)
		}
		requireSignIn(t, base, e, e.password, http.StatusOK, "")

		backdateRecovery(t, e.email, 25*time.Hour)
		if got := submitReset(t, action, values, "pw-new-expired"); got != siteURL+"/?reset=failed" {
			t.Errorf("expired submit = %q, want %s/?reset=failed", got, siteURL)
		}
		requireSignIn(t, base, e, e.password, http.StatusOK, "")
	})
}

// D28: a reset from the mailbox of an address registered and never confirmed takes the account back.
func TestIdP_ResetTakesBackAnUnconfirmedAccount(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	h := registrant(t, gw)
	attackerPassword := h.password
	const victimPassword = "pw-victim-reset"

	requireSignIn(t, base, h, attackerPassword, http.StatusBadRequest, "email_not_confirmed")
	requestResetAccepted(t, gw, h.email)
	action, values := confirmForm(t, recoveryLink(t, h.email, 2))
	if got := submitReset(t, action, values, victimPassword); got != siteURL+"/?reset=1" {
		t.Fatalf("reset redirect = %q, want %s/?reset=1", got, siteURL)
	}

	if !emailConfirmed(t, h.email) {
		t.Error("the account is not confirmed after the reset")
	}
	if n := sessionRows(t, h.email); n != 0 {
		t.Errorf("auth.sessions holds %d rows for the user after the reset, want 0", n)
	}
	requireSignIn(t, base, h, victimPassword, http.StatusOK, "")
	requireSignIn(t, base, h, attackerPassword, http.StatusBadRequest, "invalid_credentials")
}
