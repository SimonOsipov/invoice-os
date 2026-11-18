package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var resetT0 = time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)

func resetShareAt(perHour, perDay, perIPHour, perIPDay int, c *mailClock) *ResetMail {
	return NewResetMail("reset-mail", perHour, perDay, perIPHour, perIPDay, c.Now)
}

// prodReset is a ResetMail with the production constants.
func prodReset(c *mailClock) *ResetMail {
	return resetShareAt(ResetMailPerHour, ResetMailPerDay, ResetMailPerIPHour, ResetMailPerIPDay, c)
}

func testIP(i int) string { return fmt.Sprintf("198.51.100.%d", i) }

// postResetFrom posts one reset request from the local-posture client remote (host only).
func postResetFrom(ctx_ *testing.T, h http.Handler, remote, email string) *httptest.ResponseRecorder {
	ctx_.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/request-password-reset", strings.NewReader(resendBody(email)))
	req.Header.Set("Content-Type", "application/json")
	if strings.Contains(remote, ":") {
		remote = "[" + remote + "]"
	}
	req.RemoteAddr = remote + ":4321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func resetHandlerOn(u *url.URL, per *SignInThrottle, reset *ResetMail) http.Handler {
	return RequestPasswordResetHandler(u, testClient(), 0, per, true, slog.New(slog.DiscardHandler), nil, reset)
}

func TestResetMail_HourWindowHoldsThreeThenRefuses(t *testing.T) {
	c := newMailClock(resetT0)
	m := prodReset(c)
	for i := 1; i <= 4; i++ {
		h, spent, err := m.Hold(testIP(i))
		if i < 4 {
			if spent != "" || err != nil || h.m == nil {
				t.Fatalf("Hold %d = (%+v, %q, %v), want a non-zero hold", i, h, spent, err)
			}
			continue
		}
		if spent != "hour" || err != nil || h.m != nil {
			t.Fatalf("Hold 4 = (%+v, %q, %v), want (zero, hour, nil)", h, spent, err)
		}
	}
}

func TestResetMail_DayWindowRefusesTheTwentyFirst(t *testing.T) {
	c := newMailClock(resetT0)
	m := prodReset(c)
	for i := 1; i <= 20; i++ {
		if _, spent, err := m.Hold(testIP(i)); spent != "" || err != nil {
			t.Fatalf("Hold %d = (%q, %v), want admitted", i, spent, err)
		}
		if i%3 == 0 {
			c.Add(61 * time.Minute)
		}
	}
	for i := 21; i <= 22; i++ {
		if _, spent, err := m.Hold(testIP(i)); spent != "day" || err != nil {
			t.Fatalf("Hold %d = (%q, %v), want (day, nil)", i, spent, err)
		}
	}
}

func TestResetMail_OneIPHoldsOncePerHour(t *testing.T) {
	m := resetShareAt(10, 100, ResetMailPerIPHour, ResetMailPerIPDay, newMailClock(resetT0))
	if _, spent, err := m.Hold("198.51.100.7"); spent != "" || err != nil {
		t.Fatalf("first Hold = (%q, %v)", spent, err)
	}
	if _, spent, _ := m.Hold("198.51.100.7"); spent != "ip-hour" {
		t.Fatalf("second Hold = %q, want ip-hour", spent)
	}
	if _, spent, err := m.Hold("198.51.100.8"); spent != "" || err != nil {
		t.Fatalf("other IP Hold = (%q, %v)", spent, err)
	}
}

func TestResetMail_OneIPHoldsThreeTimesPer24Hours(t *testing.T) {
	c := newMailClock(resetT0)
	m := resetShareAt(100, 100, ResetMailPerIPHour, ResetMailPerIPDay, c)
	const ip = "198.51.100.7"
	for i := 1; i <= 3; i++ {
		if _, spent, err := m.Hold(ip); spent != "" || err != nil {
			t.Fatalf("Hold %d = (%q, %v)", i, spent, err)
		}
		c.Add(61 * time.Minute)
	}
	if _, spent, _ := m.Hold(ip); spent != "ip-day" {
		t.Fatalf("4th Hold in 24 h = %q, want ip-day", spent)
	}
	c.Set(resetT0.Add(24 * time.Hour))
	if _, spent, err := m.Hold(ip); spent != "" || err != nil {
		t.Fatalf("Hold 24 h after the first = (%q, %v), want admitted", spent, err)
	}
}

func TestResetMail_AnIPRefusalSpendsNoShare(t *testing.T) {
	m := resetShareAt(2, 100, 1, 100, newMailClock(resetT0))
	m.Hold("198.51.100.1")
	if _, spent, _ := m.Hold("198.51.100.1"); spent != "ip-hour" {
		t.Fatalf("repeat Hold = %q, want ip-hour", spent)
	}
	if _, spent, err := m.Hold("198.51.100.2"); spent != "" || err != nil {
		t.Fatalf("IP B Hold = (%q, %v): the refused Hold took a share", spent, err)
	}
	if _, spent, _ := m.Hold("198.51.100.3"); spent != "hour" {
		t.Fatalf("IP C Hold = %q, want hour (two holds spent it)", spent)
	}
}

func TestResetMail_ARefundAfterUTCMidnightReturnsTheHoldsDay(t *testing.T) {
	c := newMailClock(time.Date(2026, 11, 17, 23, 59, 59, 0, time.UTC))
	m := resetShareAt(3, 1, 100, 100, c)
	first, spent, err := m.Hold(testIP(1))
	if spent != "" || err != nil {
		t.Fatalf("first Hold = (%q, %v)", spent, err)
	}
	c.Set(time.Date(2026, 11, 18, 0, 0, 1, 0, time.UTC))
	if _, spent, err := m.Hold(testIP(2)); spent != "" || err != nil {
		t.Fatalf("Hold on the new day = (%q, %v)", spent, err)
	}
	first.Refund()
	if _, spent, _ := m.Hold(testIP(3)); spent != "day" {
		t.Fatalf("Hold after the refund = %q, want day: the refund must hit the first day", spent)
	}
}

func TestResetMail_OtherMailRoutesIgnoreASpentResetShare(t *testing.T) {
	m := resetShareAt(1, 100, 100, 100, newMailClock(resetT0))
	m.Hold(testIP(1))
	if _, spent, _ := m.Hold(testIP(2)); spent != "hour" {
		t.Fatalf("setup: reset share not spent (%q)", spent)
	}
	for _, c := range mailRoutes() {
		if c.name == "reset" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			c.serve(t, fake.URL)
			if n := len(fake.Calls()); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
			}
		})
	}
}

func TestResetMail_RegisterAndResendSpendNoResetShare(t *testing.T) {
	c := newMailClock(resetT0)
	reset := resetShareAt(1, 100, 100, 100, c)
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log := slog.New(slog.DiscardHandler)
	share := NewRegistrationMail("registration-mail", RegistrationMailPerHour, RegistrationMailPerDay, c.Now)
	register := RegisterHandler(fake.URL, testClient(), 0, log, noPendingInvite, nil, false, share)
	resend := ResendVerificationHandler(fake.URL, testClient(), 0, resendThrottle(c.Now), true, log, share)
	for range 2 {
		doRegisterWith(t, register, registerBody(regEmail, regPassword))
		serveResend(t.Context(), resend, resendBody("ada@corp.example"))
	}
	if s, r := callsTo(fake, "/signup"), callsTo(fake, "/resend"); s != 2 || r != 2 {
		t.Fatalf("GoTrue saw %d /signup and %d /resend, want 2 and 2", s, r)
	}

	postResetFrom(t, resetHandlerOn(fake.URL, resendThrottle(c.Now), reset), testIP(1), "ada@corp.example")

	if n := callsTo(fake, "/recover"); n != 1 {
		t.Errorf("GoTrue saw %d /recover, want 1: register and resend must not spend the reset share", n)
	}
}

func TestResetMail_BothSharesStraddlingOneGoTrueHourLeaveTheInviteeReserve(t *testing.T) {
	tb := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := newMailClock(tb.Add(-59 * time.Minute))
	gt := newLimiterGoTrue(t, tb, c.Now, 30)
	log := slog.New(slog.DiscardHandler)
	share := NewRegistrationMail("registration-mail", RegistrationMailPerHour, RegistrationMailPerDay, c.Now)
	register := RegisterHandler(gt.URL, testClient(), 0, log, noPendingInvite, nil, false, share)
	resetShare := prodReset(c)
	resetH := resetHandlerOn(gt.URL, resendThrottle(c.Now), resetShare)
	ip := 0
	reg := func() { doRegisterWith(t, register, registerBody(regEmail, regPassword)) }
	rst := func() {
		ip++
		postResetFrom(t, resetH, testIP(ip), fmt.Sprintf("user%d@corp.example", ip))
	}

	reg()
	rst()
	c.Set(tb.Add(30 * time.Second))
	for range 9 {
		reg()
	}
	rst()
	rst()
	for i := range 40 {
		c.Set(tb.Add(61*time.Second + time.Duration(i)*85*time.Second))
		reg()
		rst()
	}
	c.Set(tb.Add(59 * time.Minute))
	rec := serveInviteResend(newInviteResend(gt.URL, invitePreviewing("unconfirmed"), nil), http.MethodPost, tokenBody(inviteToken))

	signups, recovers := gt.sends(0, "/signup"), gt.sends(0, "/recover")
	if total := signups + recovers; total > 2*(RegistrationMailPerHour+ResetMailPerHour) {
		t.Errorf("GoTrue's hour counted %d share sends (%d /signup, %d /recover), want at most %d", total, signups, recovers, 2*(RegistrationMailPerHour+ResetMailPerHour))
	}
	if rec.Code != http.StatusOK {
		t.Errorf("invitation resend = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if left := 30 - signups - recovers - gt.sends(0, "/resend"); left < 4 {
		t.Errorf("%d sends left in the hour, want at least 4", left)
	}
}
