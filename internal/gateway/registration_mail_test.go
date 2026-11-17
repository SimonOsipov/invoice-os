package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// mailClock is a settable clock safe to read from a fake server's goroutine.
type mailClock struct {
	mu sync.Mutex
	t  time.Time
}

func newMailClock(t time.Time) *mailClock { return &mailClock{t: t} }
func (c *mailClock) Now() time.Time       { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *mailClock) Set(t time.Time)      { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
func (c *mailClock) Add(d time.Duration)  { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

func shareAt(perHour, perDay int, c *mailClock) *RegistrationMail {
	return NewRegistrationMail("registration-mail", perHour, perDay, c.Now)
}

// limiterGoTrue models GoTrue's mail limiter (ratelimit.IntervalLimiter at GoTrue v2.198.0): cap sends per fixed hour from boot,
// counting /signup, /resend and /recover. Past the cap it answers 429 over_email_send_rate_limit.
type limiterGoTrue struct {
	URL    *url.URL
	mu     sync.Mutex
	boot   time.Time
	now    func() time.Time
	cap    int
	counts map[int]map[string]int // GoTrue hour -> path -> sends
	last   map[string]int         // path -> last status
}

func newLimiterGoTrue(t *testing.T, boot time.Time, now func() time.Time, cap int) *limiterGoTrue {
	t.Helper()
	l := &limiterGoTrue{boot: boot, now: now, cap: cap, counts: map[int]map[string]int{}, last: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		l.mu.Lock()
		hour := int(l.now().Sub(l.boot) / time.Hour)
		if l.now().Before(l.boot) {
			hour = -1
		}
		if l.counts[hour] == nil {
			l.counts[hour] = map[string]int{}
		}
		total := 0
		for _, n := range l.counts[hour] {
			total += n
		}
		status, body := http.StatusOK, `{}`
		if total >= l.cap {
			status, body = http.StatusTooManyRequests, gtOverEmailSendRateLimit
		} else {
			l.counts[hour][r.URL.Path]++
		}
		l.last[r.URL.Path] = status
		l.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	l.URL = u
	return l
}

func (l *limiterGoTrue) sends(hour int, path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[hour][path]
}

func (l *limiterGoTrue) lastStatus(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last[path]
}

func TestRegistrationMail_HourWindowAdmitsTenThenRefuses(t *testing.T) {
	c := newMailClock(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC))
	m := shareAt(RegistrationMailPerHour, RegistrationMailPerDay, c)
	for i := 1; i <= 11; i++ {
		spent, err := m.Admit()
		want := ""
		if i == 11 {
			want = "hour"
		}
		if spent != want || err != nil {
			t.Fatalf("Admit %d = (%q, %v), want (%q, nil)", i, spent, err, want)
		}
	}
}

func TestRegistrationMail_DayWindowRefusesTheThirtyFirst(t *testing.T) {
	c := newMailClock(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC))
	m := shareAt(RegistrationMailPerHour, RegistrationMailPerDay, c)
	for i := 1; i <= RegistrationMailPerDay; i++ {
		if spent, err := m.Admit(); spent != "" || err != nil {
			t.Fatalf("Admit %d = (%q, %v), want admitted", i, spent, err)
		}
		if i%RegistrationMailPerHour == 0 {
			c.Add(61 * time.Minute)
		}
	}
	for i := 31; i <= 32; i++ {
		if spent, err := m.Admit(); spent != "day" || err != nil {
			t.Fatalf("Admit %d = (%q, %v), want (\"day\", nil): the refused day must refund the hour", i, spent, err)
		}
	}
}

func TestRegistrationMail_DayStartsAgainAtUTCMidnight(t *testing.T) {
	c := newMailClock(time.Date(2026, 11, 17, 23, 59, 0, 0, time.UTC))
	m := shareAt(100, 2, c)
	for range 2 {
		if spent, _ := m.Admit(); spent != "" {
			t.Fatalf("Admit before the day is spent = %q", spent)
		}
	}
	if spent, _ := m.Admit(); spent != "day" {
		t.Fatalf("third Admit = %q, want day", spent)
	}
	c.Set(time.Date(2026, 11, 18, 0, 0, 1, 0, time.UTC))
	if spent, err := m.Admit(); spent != "" || err != nil {
		t.Errorf("Admit after UTC midnight = (%q, %v), want admitted", spent, err)
	}
}

func TestRegistrationMail_NilAdmitsEverything(t *testing.T) {
	var m *RegistrationMail
	for range 100 {
		if spent, err := m.Admit(); spent != "" || err != nil {
			t.Fatalf("nil Admit = (%q, %v)", spent, err)
		}
		m.Refund()
	}
}

func TestRegistrationMail_ResetStillMailsAfterTheShareIsSpent(t *testing.T) {
	boot := time.Now()
	gt := newLimiterGoTrue(t, boot, time.Now, 30)
	log := slog.New(slog.DiscardHandler)
	share := NewRegistrationMail("registration-mail", RegistrationMailPerHour, RegistrationMailPerDay, time.Now)
	register := RegisterHandler(gt.URL, testClient(), 0, log, noPendingInvite, nil, false, share)
	for range 50 {
		doRegisterWith(t, register, registerBody(regEmail, regPassword))
	}
	reset := RequestPasswordResetHandler(gt.URL, testClient(), 0, resendThrottle(time.Now), true, log, nil)
	rec, _ := serveResend(t.Context(), reset, resendBody("ada@corp.example"))
	requireAccepted(t, rec, "reset after the share is spent")

	if n := gt.sends(0, "/signup"); n != RegistrationMailPerHour {
		t.Errorf("GoTrue counted %d /signup sends, want %d", n, RegistrationMailPerHour)
	}
	if got := gt.lastStatus("/recover"); got != http.StatusOK {
		t.Errorf("/recover answered %d, want 200 (send %d of 30)", got, RegistrationMailPerHour+1)
	}
}

func TestRegistrationMail_ShareWindowsStraddlingOneGoTrueHourLeaveTheReserve(t *testing.T) {
	tb := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := newMailClock(tb.Add(-59 * time.Minute))
	gt := newLimiterGoTrue(t, tb, c.Now, 30)
	log := slog.New(slog.DiscardHandler)
	share := NewRegistrationMail("registration-mail", RegistrationMailPerHour, RegistrationMailPerDay, c.Now)
	register := RegisterHandler(gt.URL, testClient(), 0, log, noPendingInvite, nil, false, share)
	reg := func() { doRegisterWith(t, register, registerBody(regEmail, regPassword)) }

	reg()
	c.Set(tb.Add(30 * time.Second))
	for range 9 {
		reg()
	}
	for i := range 40 {
		c.Set(tb.Add(61*time.Second + time.Duration(i)*85*time.Second))
		reg()
	}
	c.Set(tb.Add(59 * time.Minute))
	reset := RequestPasswordResetHandler(gt.URL, testClient(), 0, resendThrottle(c.Now), true, log, nil)
	rec, _ := serveResend(t.Context(), reset, resendBody("ada@corp.example"))
	requireAccepted(t, rec, "reset")

	signups := gt.sends(0, "/signup")
	if signups != 19 || signups > 2*RegistrationMailPerHour {
		t.Errorf("GoTrue counted %d /signup sends in its hour, want 19 (two share windows, at most %d)", signups, 2*RegistrationMailPerHour)
	}
	if got := gt.lastStatus("/recover"); got != http.StatusOK {
		t.Errorf("/recover answered %d, want 200", got)
	}
	if left := 30 - signups - 1; left < 10 {
		t.Errorf("%d sends left after the reset, want at least 10", left)
	}
}
