package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// swapMailAlerts installs a fresh gate on now and restores the old one on cleanup.
func swapMailAlerts(t *testing.T, now func() time.Time) {
	t.Helper()
	old := mailAlerts
	mailAlerts = &mailAlertGate{last: map[error]time.Time{}, now: now}
	t.Cleanup(func() { mailAlerts = old })
}

func alertValue(e *sentry.Event) string {
	if len(e.Exception) == 0 {
		return ""
	}
	return e.Exception[len(e.Exception)-1].Value
}

type mailRoute struct {
	name   string
	status int
	serve  func(t *testing.T, authURL *url.URL) *httptest.ResponseRecorder
}

func mailRoutes() []mailRoute {
	log := slog.New(slog.DiscardHandler)
	return []mailRoute{
		{"register", http.StatusAccepted, func(t *testing.T, u *url.URL) *httptest.ResponseRecorder {
			return doRegisterWith(t, RegisterHandler(u, testClient(), 0, log, noPendingInvite, nil, false, nil), registerBody(regEmail, regPassword))
		}},
		{"invitation register", http.StatusAccepted, func(t *testing.T, u *url.URL) *httptest.ResponseRecorder {
			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: regEmail, Account: "none"}, nil)
			rec, _ := postInvitee(inviteeHandler(u, 0, p, claiming(regEmail, true, nil)), tokenBody(inviteToken))
			return rec
		}},
		{"resend", http.StatusAccepted, func(t *testing.T, u *url.URL) *httptest.ResponseRecorder {
			rec, _ := serveResend(t.Context(), newResend(u, testClient(), 0, nil), resendBody("ada@corp.example"))
			return rec
		}},
		{"reset", http.StatusAccepted, func(t *testing.T, u *url.URL) *httptest.ResponseRecorder {
			h := RequestPasswordResetHandler(u, testClient(), 0, resendThrottle(time.Now), true, log, nil)
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"))
			return rec
		}},
		{"invitation resend", http.StatusBadGateway, func(t *testing.T, u *url.URL) *httptest.ResponseRecorder {
			return serveInviteResend(newInviteResend(u, invitePreviewing("unconfirmed"), nil), http.MethodPost, tokenBody(inviteToken))
		}},
	}
}

func TestMailAlert_InstanceCapOnEveryMailRoute(t *testing.T) {
	for _, c := range mailRoutes() {
		t.Run(c.name, func(t *testing.T) {
			app, rec, want := sentrytest.Boot(t, "gateway")
			_ = app
			swapMailAlerts(t, time.Now)
			fake := newFakeGoTrue(t, http.StatusTooManyRequests, gtInstanceMailCap)

			got := c.serve(t, fake.URL)

			if got.Code != c.status {
				t.Errorf("status = %d, want %d: %s", got.Code, c.status, got.Body.String())
			}
			if v := alertValue(rec.One(t, want)); v != "identity provider mail cap reached" {
				t.Errorf("exception = %q", v)
			}
		})
	}
}

func TestMailAlert_RegisterCapKeepsThe202AndTheFloor(t *testing.T) {
	const floor = 150 * time.Millisecond
	_, rec, want := sentrytest.Boot(t, "gateway")
	swapMailAlerts(t, time.Now)
	fresh, _ := serveFloor(t, newFakeGoTrue(t, http.StatusOK, gtNewUser).URL, 0, nil, registerBody("new@corp.example", regPassword))

	got, elapsed := serveFloor(t, newFakeGoTrue(t, http.StatusTooManyRequests, gtInstanceMailCap).URL, floor, nil, registerBody(regEmail, regPassword))

	if got.Code != http.StatusAccepted || got.Body.String() != fresh.Body.String() || !strings.Contains(got.Body.String(), "verification_pending") {
		t.Errorf("answer = %d %q, want the new-address answer %d %q", got.Code, got.Body.String(), fresh.Code, fresh.Body.String())
	}
	if elapsed < floor {
		t.Errorf("elapsed %v, want at least %v", elapsed, floor)
	}
	if v := alertValue(rec.One(t, want)); v != "identity provider mail cap reached" {
		t.Errorf("exception = %q", v)
	}
}

func TestMailAlert_CooldownCapturesNothing(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	swapMailAlerts(t, time.Now)
	for _, c := range mailRoutes() {
		fake := newFakeGoTrue(t, http.StatusTooManyRequests, gtOverEmailSendRateLimit)
		c.serve(t, fake.URL)
	}
	rec.None(t)
}

func TestIsIdPMailCap_Classifies(t *testing.T) {
	rows := []struct {
		name string
		gt   gotrueError
		want bool
	}{
		{"cap msg", gotrueError{ErrorCode: "over_email_send_rate_limit", Msg: "email rate limit exceeded"}, true},
		{"cooldown msg", gotrueError{ErrorCode: "over_email_send_rate_limit", Msg: "For security purposes, you can only request this after 42 seconds."}, false},
		{"empty msg", gotrueError{ErrorCode: "over_email_send_rate_limit"}, true},
		{"other code", gotrueError{ErrorCode: "over_request_rate_limit", Msg: "x"}, false},
	}
	for _, c := range rows {
		if got := isIdPMailCap(c.gt); got != c.want {
			t.Errorf("%s: isIdPMailCap = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMailAlert_SpentShareAlerts(t *testing.T) {
	for _, route := range []string{"register", "resend"} {
		t.Run(route, func(t *testing.T) {
			_, rec, want := sentrytest.Boot(t, "gateway")
			swapMailAlerts(t, time.Now)
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			if route == "register" {
				doRegisterWith(t, registerWithShare(fake.URL, nil, nil, spentShare(t)), registerBody(regEmail, regPassword))
			} else {
				resend := ResendVerificationHandler(fake.URL, testClient(), 0, resendThrottle(time.Now), true, slog.New(slog.DiscardHandler), spentShare(t))
				serveResend(t.Context(), resend, resendBody("ada@corp.example"))
			}
			if v := alertValue(rec.One(t, want)); v != "registration mail share spent" {
				t.Errorf("exception = %q", v)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

func TestMailAlert_UnreadableShareDoesNotAlert(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	swapMailAlerts(t, time.Now)
	share := NewRegistrationMail("registration-mail", 1, 100, time.Now)
	share.Shared(deadPool(t), slog.New(slog.DiscardHandler))
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	got := doRegisterWith(t, registerWithShare(fake.URL, nil, nil, share), registerBody(regEmail, regPassword))

	if got.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", got.Code)
	}
	rec.None(t)
}

func TestMailAlert_OncePerWindowPerError(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	c := newMailClock(time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC))
	swapMailAlerts(t, c.Now)
	count := func(msg string) int {
		n := 0
		for _, e := range rec.Events() {
			if alertValue(e) == msg {
				n++
			}
		}
		return n
	}

	alertMail(t.Context(), ErrIdPMailCap)
	alertMail(t.Context(), ErrIdPMailCap)
	c.Add(5*time.Hour + 59*time.Minute)
	alertMail(t.Context(), ErrIdPMailCap)
	alertMail(t.Context(), ErrRegistrationMailSpent)
	if got := count(ErrIdPMailCap.Error()); got != 1 {
		t.Errorf("cap events before 6h = %d, want 1", got)
	}
	if got := count(ErrRegistrationMailSpent.Error()); got != 1 {
		t.Errorf("share events = %d, want 1 on its own gate", got)
	}
	c.Add(time.Minute)
	alertMail(t.Context(), ErrIdPMailCap)
	if got := count(ErrIdPMailCap.Error()); got != 2 {
		t.Errorf("cap events at 6h = %d, want 2", got)
	}
}

func TestMailAlert_EachEpisodeIsANewIssue(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	c := newMailClock(time.Date(2026, 11, 17, 5, 59, 0, 0, time.UTC))
	swapMailAlerts(t, c.Now)
	reg := func(h http.Handler) { doRegisterWith(t, h, registerBody(regEmail, regPassword)) }
	cap := RegisterHandler(newFakeGoTrue(t, http.StatusTooManyRequests, gtInstanceMailCap).URL, testClient(), 0, slog.New(slog.DiscardHandler), noPendingInvite, nil, false, nil)

	reg(cap)
	c.Set(time.Date(2026, 11, 17, 11, 59, 0, 0, time.UTC))
	reg(cap)
	reg(registerWithShare(newFakeGoTrue(t, http.StatusOK, gtNewUser).URL, nil, nil, spentShare(t)))

	var got [][]string
	for _, e := range rec.Events() {
		got = append(got, e.Fingerprint)
	}
	want := [][]string{
		{"gateway-mail-alert", "identity provider mail cap reached", "2026-11-17T00:00:00Z"},
		{"gateway-mail-alert", "identity provider mail cap reached", "2026-11-17T06:00:00Z"},
		{"gateway-mail-alert", "registration mail share spent", "2026-11-17T06:00:00Z"},
	}
	if len(got) != 3 {
		t.Fatalf("fingerprints = %q, want %q", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("fingerprint %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMailAlert_EventsCarryNoAddress(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	swapMailAlerts(t, time.Now)
	const addr, ip = "Ada.Lovelace@corp.example", "203.0.113.77"
	post := func(h http.Handler) {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(addr, regPassword)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	post(RegisterHandler(newFakeGoTrue(t, http.StatusTooManyRequests, gtInstanceMailCap).URL, testClient(), 0, slog.New(slog.DiscardHandler), noPendingInvite, nil, false, nil))
	post(registerWithShare(newFakeGoTrue(t, http.StatusOK, gtNewUser).URL, nil, nil, spentShare(t)))

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	sentrytest.AssertNoLeak(t, events)
	for _, e := range events {
		raw, _ := json.Marshal(e)
		for _, s := range []string{addr, strings.ToLower(addr), ip, regPassword} {
			if strings.Contains(string(raw), s) {
				t.Errorf("event carries %q: %s", s, raw)
			}
		}
	}
}
