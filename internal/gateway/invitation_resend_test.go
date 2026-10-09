package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	wantResendUnavailable = "invitation resend is unavailable"
	wantTokenRequired     = "token is required"
	invResendLimitMsg     = "invitation-resend: limit reached"
	invResendCapMsg       = "invitation-resend: gotrue email send rate limit"
	invResendIP           = "203.0.113.9"
	otherInviteToken      = "Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8Rx8R"
	gtInstanceMailCap     = `{"code":429,"error_code":"over_email_send_rate_limit","msg":"email rate limit exceeded"}`
	resendSent            = `{"status":"sent"}`
	resendHeld            = `{"status":"held"}`
)

func unconfirmedInvite() InvitationPreview {
	p := liveInvite
	p.Account = "unconfirmed"
	return p
}

func invitePreviewing(account string) *recordingPreviewer {
	p := liveInvite
	p.Account = account
	return previewing(p, nil)
}

// newInviteResend builds the handler with production-sized limits and enforcement on.
func newInviteResend(authURL *url.URL, p *recordingPreviewer, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	perAddress, perIP := resendThrottles(time.Now)
	return InvitationResendHandler(authURL, testClient(), perAddress, perIP, true, log, p.preview)
}

func serveInviteResend(h http.Handler, method, body, realIP string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/auth/invitation/resend", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func requireInviteAnswer(t *testing.T, rec *httptest.ResponseRecorder, status int, body, what string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("%s: status %d, body %q; want %d %s", what, rec.Code, rec.Body.String(), status, body)
	}
}

func jsonError(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

func TestInvitationResend_GoTrueOKIsSent(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	p := invitePreviewing("unconfirmed")
	log, buf := captureLog()
	h := newInviteResend(fake.URL, p, log)

	rec := serveInviteResend(h, http.MethodPost, `{"token":"`+inviteToken+`","email":"evil@x.test"}`, "")

	requireInviteAnswer(t, rec, http.StatusOK, resendSent, "resend")
	if got := p.calls(); len(got) != 1 || got[0] != inviteToken {
		t.Errorf("previewer saw %v, want the token once", got)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/resend" {
		t.Errorf("GoTrue call = %s %s, want POST /resend", calls[0].Method, calls[0].Path)
	}
	var sent map[string]string
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("GoTrue body %q is not a JSON object of strings: %v", calls[0].Body, err)
	}
	if want := map[string]string{"type": "signup", "email": inviteAddress}; !maps.Equal(sent, want) {
		t.Errorf("GoTrue body = %v, want %v (the preview's address; a body email is ignored)", sent, want)
	}
	if n := warnCount(t, buf); n != 0 {
		t.Errorf("a real send logged %d WARN lines: %s", n, buf.String())
	}
}

func TestInvitationResend_CooldownIsHeld(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusTooManyRequests, gtOverEmailSendRateLimit)
	log, buf := captureLog()
	h := newInviteResend(fake.URL, invitePreviewing("unconfirmed"), log)

	rec := serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), "")

	requireInviteAnswer(t, rec, http.StatusOK, resendHeld, "resend inside the cooldown")
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1", n)
	}
	// The cooldown is the expected answer, not a fault.
	if n := warnCount(t, buf); n != 0 {
		t.Errorf("a cooldown hold logged %d WARN lines: %s", n, buf.String())
	}
}

// Only the 60 s cooldown wording is "held": the instance mail cap sent nothing and no recent mail exists.
func TestInvitationResend_OnlyTheCooldownIsHeld(t *testing.T) {
	rows := []struct {
		name      string
		status    int
		body      string
		transport bool
		warnMsg   string
	}{
		{"429 instance mail cap", http.StatusTooManyRequests, gtInstanceMailCap, false, invResendCapMsg},
		{"429 over_request_rate_limit", http.StatusTooManyRequests, gtOverRequestRateLimit, false, "invitation-resend: gotrue resend failed"},
		{"429 with an empty msg", http.StatusTooManyRequests, `{"code":429,"error_code":"over_email_send_rate_limit","msg":""}`, false, ""},
		{"429 cooldown wording under another error_code", http.StatusTooManyRequests, `{"code":429,"error_code":"over_request_rate_limit","msg":"For security purposes, you can only request this after 42 seconds."}`, false, "invitation-resend: gotrue resend failed"},
		{"302", http.StatusFound, `{}`, false, "invitation-resend: gotrue resend failed"},
		{"400", http.StatusBadRequest, `{"code":400,"error_code":"email_address_not_authorized","msg":"Email address is not authorized"}`, false, "invitation-resend: gotrue resend failed"},
		{"403", http.StatusForbidden, `{"code":403,"error_code":"not_admin","msg":"forbidden"}`, false, "invitation-resend: gotrue resend failed"},
		{"500", http.StatusInternalServerError, gtInternal, false, "invitation-resend: gotrue resend failed"},
		{"transport error", http.StatusOK, `{}`, true, "invitation-resend: gotrue unreachable"},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			var fake *fakeGoTrue
			if !c.transport {
				fake = newFakeGoTrue(t, c.status, c.body)
				authURL = fake.URL
			}
			log, buf := captureLog()
			h := newInviteResend(authURL, invitePreviewing("unconfirmed"), log)

			rec := serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), "")

			requireInviteAnswer(t, rec, http.StatusBadGateway, jsonError(wantResendUnavailable), c.name)
			if strings.Contains(rec.Body.String(), "sent") || strings.Contains(rec.Body.String(), "held") {
				t.Errorf("body %q claims a send or a hold", rec.Body.String())
			}
			if fake != nil && len(fake.Calls()) != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", len(fake.Calls()))
			}
			if lines := recordsNamed(t, buf, c.warnMsg); c.warnMsg != "" && (len(lines) != 1 || lines[0]["level"] != "WARN") {
				t.Errorf("want one WARN %q line, got %d: %s", c.warnMsg, len(lines), buf.String())
			}
		})
	}
}

func TestInvitationResend_BadTokenNeverReachesGoTrue(t *testing.T) {
	rows := []struct {
		name, body     string
		result         func(string) (InvitationPreview, error)
		status         int
		msg            string
		previewerCalls int
	}{
		{"empty token", tokenBody(""), nil, http.StatusBadRequest, wantTokenRequired, 0},
		{"malformed JSON", `{`, nil, http.StatusBadRequest, msgInvalidBody, 0},
		{"oversized body", `{"token":"` + strings.Repeat("A", 2048) + `"}`, nil, http.StatusBadRequest, msgInvalidBody, 0},
		{"token of the wrong type", `{"token":12345}`, nil, http.StatusBadRequest, msgInvalidBody, 0},
		{"shapeless token", tokenBody("short"), nil, http.StatusNotFound, wantInviteNotValid, 0},
		{"well-formed unknown token", tokenBody(inviteToken), func(string) (InvitationPreview, error) { return InvitationPreview{}, ErrInvitationNotValid }, http.StatusNotFound, wantInviteNotValid, 1},
		{"lookup fails", tokenBody(inviteToken), func(string) (InvitationPreview, error) {
			return InvitationPreview{}, errors.New("tenancy answered 500")
		}, http.StatusBadGateway, wantLookupUnavailable, 1},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, `{}`)
			p := &recordingPreviewer{result: c.result}
			if c.result == nil {
				p.result = func(string) (InvitationPreview, error) { return unconfirmedInvite(), nil }
			}

			rec := serveInviteResend(newInviteResend(fake.URL, p, nil), http.MethodPost, c.body, "")

			requireInviteAnswer(t, rec, c.status, jsonError(c.msg), c.name)
			if n := len(p.calls()); n != c.previewerCalls {
				t.Errorf("previewer saw %d calls, want %d", n, c.previewerCalls)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

func TestInvitationResend_NonPostIs405(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, `{}`)
			p := invitePreviewing("unconfirmed")

			rec := serveInviteResend(newInviteResend(fake.URL, p, nil), method, tokenBody(inviteToken), "")

			requireInviteAnswer(t, rec, http.StatusMethodNotAllowed, jsonError("method not allowed"), method)
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
			if len(p.calls()) != 0 || len(fake.Calls()) != 0 {
				t.Errorf("previewer saw %d calls and GoTrue %d, want none", len(p.calls()), len(fake.Calls()))
			}
		})
	}
}

func TestInvitationResend_AccountStateGate(t *testing.T) {
	rows := []struct {
		name, account string
		goTrue        gtAnswer
		status        int
		body          string
		goTrueCalls   int
	}{
		{"confirmed", "confirmed", gtAnswer{200, `{}`}, http.StatusConflict, jsonError("account_exists"), 0},
		{"none", "none", gtAnswer{200, `{}`}, http.StatusConflict, jsonError("account_missing"), 0},
		{"unknown and GoTrue 200", "unknown", gtAnswer{200, `{}`}, http.StatusOK, `{"status":"maybe"}`, 1},
		{"unknown and cooldown 429", "unknown", gtAnswer{429, gtOverEmailSendRateLimit}, http.StatusOK, resendHeld, 1},
		{"unconfirmed and GoTrue 200", "unconfirmed", gtAnswer{200, `{}`}, http.StatusOK, resendSent, 1},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.goTrue.status, c.goTrue.body)

			rec := serveInviteResend(newInviteResend(fake.URL, invitePreviewing(c.account), nil), http.MethodPost, tokenBody(inviteToken), "")

			requireInviteAnswer(t, rec, c.status, c.body, c.name)
			if n := len(fake.Calls()); n != c.goTrueCalls {
				t.Errorf("GoTrue saw %d calls, want %d", n, c.goTrueCalls)
			}
		})
	}
}

func TestInvitationResend_SharesTheResendBudgets(t *testing.T) {
	fake := sequenceGoTrue(t, gtAnswer{200, `{}`})
	log, buf := captureLog()
	perAddress, perIP := resendThrottles(time.Now)
	invite := InvitationResendHandler(fake.URL, testClient(), perAddress, perIP, true, log, invitePreviewing("unconfirmed").preview)
	anonymous := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)

	for i := 1; i <= ResendPerAddress; i++ {
		rec := serveInviteResend(invite, http.MethodPost, tokenBody(inviteToken), invResendIP)
		requireInviteAnswer(t, rec, http.StatusOK, resendSent, "invite resend")
	}
	rec := serveInviteResend(invite, http.MethodPost, tokenBody(inviteToken), invResendIP)
	requireInviteAnswer(t, rec, http.StatusTooManyRequests, jsonError("too many requests"), "fourth invite resend")
	requireLimitLine(t, buf, invResendLimitMsg, "address", "header", true)
	if n := len(fake.Calls()); n != ResendPerAddress {
		t.Errorf("GoTrue saw %d calls after the budget ran out, want %d", n, ResendPerAddress)
	}

	// The anonymous route answers 202 for every outcome; the proof it was refused is that GoTrue saw no fourth call.
	anon, _ := serveResend(t.Context(), anonymous, resendBody(inviteAddress), invResendIP)
	requireAccepted(t, anon, "anonymous resend for the same address")
	if n := len(fake.Calls()); n != ResendPerAddress {
		t.Errorf("GoTrue saw %d calls after the anonymous resend, want %d: the invite route spent a budget the anonymous route does not share", n, ResendPerAddress)
	}
}

func TestInvitationResend_CooldownHoldsAreRefunded(t *testing.T) {
	const presses = 8
	fake := sequenceGoTrue(t, gtAnswer{200, `{}`}, gtAnswer{429, gtOverEmailSendRateLimit})
	log, buf := captureLog()
	h := newInviteResend(fake.URL, invitePreviewing("unconfirmed"), log)

	for i := 1; i <= presses; i++ {
		want := resendHeld
		if i == 1 {
			want = resendSent
		}
		rec := serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP)
		requireInviteAnswer(t, rec, http.StatusOK, want, fmt.Sprintf("press %d", i))
	}
	if n := len(fake.Calls()); n != presses {
		t.Errorf("GoTrue saw %d calls, want %d: a hold spent budget", n, presses)
	}
	if lines := recordsNamed(t, buf, invResendLimitMsg); len(lines) != 0 {
		t.Errorf("%d limit lines, want none: %s", len(lines), buf.String())
	}
}

func TestInvitationResend_IPBudgetAndUnenforcedMode(t *testing.T) {
	newHandler := func(fake *fakeGoTrue, enforce bool, log *slog.Logger) http.Handler {
		perAddress := NewSignInThrottle("resend-address", 100, ResendMaxKeys, ResendWindow, time.Now)
		perIP := NewSignInThrottle("resend-ip", 2, ResendMaxKeys, ResendWindow, time.Now)
		return InvitationResendHandler(fake.URL, testClient(), perAddress, perIP, enforce, log, invitePreviewing("unconfirmed").preview)
	}

	t.Run("enforced", func(t *testing.T) {
		fake := sequenceGoTrue(t, gtAnswer{200, `{}`})
		log, buf := captureLog()
		h := newHandler(fake, true, log)
		for i := 0; i < 2; i++ {
			requireInviteAnswer(t, serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP), http.StatusOK, resendSent, "within the IP budget")
		}

		rec := serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP)

		requireInviteAnswer(t, rec, http.StatusTooManyRequests, jsonError("too many requests"), "third press from one IP")
		requireLimitLine(t, buf, invResendLimitMsg, "ip", "header", true)
		if n := len(fake.Calls()); n != 2 {
			t.Errorf("GoTrue saw %d calls, want 2", n)
		}
	})

	t.Run("not enforced", func(t *testing.T) {
		fake := sequenceGoTrue(t, gtAnswer{200, `{}`})
		log, buf := captureLog()
		h := newHandler(fake, false, log)
		for i := 1; i <= 3; i++ {
			requireInviteAnswer(t, serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP), http.StatusOK, resendSent, "unenforced press")
		}
		if n := len(fake.Calls()); n != 3 {
			t.Errorf("GoTrue saw %d calls, want 3: an unenforced limit refused", n)
		}
		requireLimitLine(t, buf, invResendLimitMsg, "ip", "header", false)
	})
}

// Wrong turn: the anonymous route answers a real send and a cooldown hold alike; the token route tells them apart.
func TestInvitationResend_OnlyTheTokenRouteTellsSentFromHeld(t *testing.T) {
	answers := []gtAnswer{{200, `{}`}, {429, gtOverEmailSendRateLimit}}

	anonFake := sequenceGoTrue(t, answers...)
	anon := newResend(anonFake.URL, testClient(), 0, nil)
	sentAnon, _ := serveResend(t.Context(), anon, resendBody(inviteAddress), "")
	heldAnon, _ := serveResend(t.Context(), anon, resendBody(inviteAddress), "")
	requireAccepted(t, sentAnon, "anonymous real send")
	requireSameAnswer(t, "anonymous cooldown hold", heldAnon, sentAnon)

	inviteFake := sequenceGoTrue(t, answers...)
	invite := newInviteResend(inviteFake.URL, invitePreviewing("unconfirmed"), nil)
	sentInvite := serveInviteResend(invite, http.MethodPost, tokenBody(inviteToken), "")
	heldInvite := serveInviteResend(invite, http.MethodPost, tokenBody(inviteToken), "")
	requireInviteAnswer(t, sentInvite, http.StatusOK, resendSent, "invite real send")
	requireInviteAnswer(t, heldInvite, http.StatusOK, resendHeld, "invite cooldown hold")
	if sentInvite.Body.String() == heldInvite.Body.String() {
		t.Error("the invite route answers a real send and a cooldown hold with the same body")
	}
	if n := len(anonFake.Calls()) + len(inviteFake.Calls()); n != 4 {
		t.Errorf("GoTrue saw %d calls in total, want 4", n)
	}
}

func TestInvitationResend_LogsCarryNoAddressTokenOrIP(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	post := func(h http.Handler) *httptest.ResponseRecorder {
		return serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP)
	}
	gt := func(status int, body string) *url.URL { return newFakeGoTrue(t, status, body).URL }

	post(newInviteResend(gt(200, `{}`), invitePreviewing("unconfirmed"), log))
	post(newInviteResend(gt(429, gtOverEmailSendRateLimit), invitePreviewing("unconfirmed"), log))
	post(newInviteResend(gt(429, gtInstanceMailCap), invitePreviewing("unconfirmed"), log))
	post(newInviteResend(gt(500, gtInternal), invitePreviewing("unconfirmed"), log))
	post(newInviteResend(closedURL(t), invitePreviewing("unconfirmed"), log))
	post(newInviteResend(gt(200, `{}`), previewing(InvitationPreview{}, errors.New("tenancy answered 500")), log))
	tight := InvitationResendHandler(gt(200, `{}`), testClient(),
		NewSignInThrottle("resend-address", 100, ResendMaxKeys, ResendWindow, time.Now),
		NewSignInThrottle("resend-ip", 1, ResendMaxKeys, ResendWindow, time.Now),
		true, log, invitePreviewing("unconfirmed").preview)
	post(tight)
	post(tight)

	for _, msg := range []string{invResendCapMsg, "invitation-resend: gotrue resend failed", "invitation-resend: gotrue unreachable", "invitation: lookup failed", invResendLimitMsg} {
		if len(recordsNamed(t, &buf, msg)) == 0 {
			t.Errorf("no %q line; the scan below would be vacuous: %s", msg, buf.String())
		}
	}
	for _, secret := range []string{inviteAddress, inviteToken, invResendIP} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("logs carry %q: %s", secret, buf.String())
		}
	}
}

// A request that holds no live, unconfirmed invite spends neither budget: 3x the per-IP limit of them leave a real press untouched.
func TestInvitationResend_RefusedRequestsSpendNoBudget(t *testing.T) {
	rows := []struct {
		name, body, account string
		wantStatus          int
	}{
		{"shapeless token", tokenBody("short"), "unconfirmed", http.StatusNotFound},
		{"empty token", tokenBody(""), "unconfirmed", http.StatusBadRequest},
		{"confirmed account", tokenBody(inviteToken), "confirmed", http.StatusConflict},
		{"missing account", tokenBody(inviteToken), "none", http.StatusConflict},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := sequenceGoTrue(t, gtAnswer{200, `{}`})
			perAddress, perIP := resendThrottles(time.Now)
			refused := InvitationResendHandler(fake.URL, testClient(), perAddress, perIP, true, slog.New(slog.DiscardHandler), invitePreviewing(c.account).preview)
			for i := 0; i < 3*ResendPerIP; i++ {
				if rec := serveInviteResend(refused, http.MethodPost, c.body, invResendIP); rec.Code != c.wantStatus {
					t.Fatalf("refused press %d = %d %s, want %d", i, rec.Code, rec.Body.String(), c.wantStatus)
				}
			}
			live := InvitationResendHandler(fake.URL, testClient(), perAddress, perIP, true, slog.New(slog.DiscardHandler), invitePreviewing("unconfirmed").preview)

			rec := serveInviteResend(live, http.MethodPost, tokenBody(inviteToken), invResendIP)

			requireInviteAnswer(t, rec, http.StatusOK, resendSent, "a live press after the refused ones")
			if n := len(fake.Calls()); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
			}
		})
	}
}

// GoTrue mailed nothing on a 4xx, so it is refunded from both budgets; a 5xx or a transport error may have mailed, so it is not.
func TestInvitationResend_RefundsOnlyA4xx(t *testing.T) {
	const presses = 3 * ResendPerIP
	t.Run("400 is refunded", func(t *testing.T) {
		fake := sequenceGoTrue(t, gtAnswer{400, `{"code":400,"error_code":"email_address_not_authorized","msg":"no"}`})
		h := newInviteResend(fake.URL, invitePreviewing("unconfirmed"), nil)
		for i := 1; i <= presses; i++ {
			requireInviteAnswer(t, serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP), http.StatusBadGateway, jsonError(wantResendUnavailable), fmt.Sprintf("press %d", i))
		}
		if n := len(fake.Calls()); n != presses {
			t.Errorf("GoTrue saw %d calls, want %d: a 4xx kept its reservation", n, presses)
		}
	})
	t.Run("cooldown holds are refunded from the IP budget too", func(t *testing.T) {
		fake := sequenceGoTrue(t, gtAnswer{429, gtOverEmailSendRateLimit})
		h := newInviteResend(fake.URL, invitePreviewing("unconfirmed"), nil)
		for i := 1; i <= presses; i++ {
			requireInviteAnswer(t, serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP), http.StatusOK, resendHeld, fmt.Sprintf("press %d", i))
		}
	})
	for _, c := range []struct {
		name      string
		answer    gtAnswer
		transport bool
	}{
		{"500 keeps its reservation", gtAnswer{500, gtInternal}, false},
		{"a transport error keeps its reservation", gtAnswer{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			var fake *fakeGoTrue
			if !c.transport {
				fake = sequenceGoTrue(t, c.answer)
				authURL = fake.URL
			}
			h := newInviteResend(authURL, invitePreviewing("unconfirmed"), nil)
			for i := 1; i <= ResendPerAddress; i++ {
				requireInviteAnswer(t, serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP), http.StatusBadGateway, jsonError(wantResendUnavailable), fmt.Sprintf("press %d", i))
			}

			rec := serveInviteResend(h, http.MethodPost, tokenBody(inviteToken), invResendIP)

			requireInviteAnswer(t, rec, http.StatusTooManyRequests, jsonError("too many requests"), "press after the budget")
			if fake != nil && len(fake.Calls()) != ResendPerAddress {
				t.Errorf("GoTrue saw %d calls, want %d", len(fake.Calls()), ResendPerAddress)
			}
		})
	}
}

// A press refused for its IP spends no address count, and the address budget is keyed by the invite's address, not the client.
func TestInvitationResend_IPRefusalSpendsNoAddressAndBudgetsArePerAddress(t *testing.T) {
	const other = "198.51.100.4"
	fake := sequenceGoTrue(t, gtAnswer{200, `{}`})
	perAddress := NewSignInThrottle("resend-address", ResendPerAddress, ResendMaxKeys, ResendWindow, time.Now)
	perIP := NewSignInThrottle("resend-ip", 2, ResendMaxKeys, ResendWindow, time.Now)
	p := &recordingPreviewer{result: func(token string) (InvitationPreview, error) {
		inv := unconfirmedInvite()
		if token == otherInviteToken {
			inv.Email = "ada@obi.test"
		}
		return inv, nil
	}}
	h := InvitationResendHandler(fake.URL, testClient(), perAddress, perIP, true, slog.New(slog.DiscardHandler), p.preview)
	press := func(token, ip string) *httptest.ResponseRecorder {
		return serveInviteResend(h, http.MethodPost, tokenBody(token), ip)
	}

	requireInviteAnswer(t, press(inviteToken, invResendIP), http.StatusOK, resendSent, "press 1")
	requireInviteAnswer(t, press(inviteToken, invResendIP), http.StatusOK, resendSent, "press 2")
	requireInviteAnswer(t, press(inviteToken, invResendIP), http.StatusTooManyRequests, jsonError("too many requests"), "press 3: IP refused")
	// Address count is 2 if the refusal spent none; a third send from another client is the last one allowed.
	requireInviteAnswer(t, press(inviteToken, other), http.StatusOK, resendSent, "press 4: another client, address count 3")
	requireInviteAnswer(t, press(inviteToken, other), http.StatusTooManyRequests, jsonError("too many requests"), "press 5: address spent")
	requireInviteAnswer(t, press(otherInviteToken, "198.51.100.5"), http.StatusOK, resendSent, "press 6: a different address has its own budget")
	if n := len(fake.Calls()); n != 4 {
		t.Errorf("GoTrue saw %d calls, want 4", n)
	}
}
