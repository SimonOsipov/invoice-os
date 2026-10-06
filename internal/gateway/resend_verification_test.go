package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	resendAccepted  = `{"status":"accepted"}`
	resendTimingMsg = "resend-verification: timing"
	resendLimitMsg  = "resend-verification: limit reached"
)

type gtAnswer struct {
	status int
	body   string
}

// sequenceGoTrue answers the nth call with answers[n] and repeats the last answer after that.
func sequenceGoTrue(t *testing.T, answers ...gtAnswer) *fakeGoTrue {
	t.Helper()
	f := &fakeGoTrue{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		a := answers[min(len(f.calls), len(answers)-1)]
		f.calls = append(f.calls, gotrueCall{r.Method, r.URL.Path, b})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	f.URL = u
	return f
}

func resendThrottles(now func() time.Time) (perAddress, perIP *SignInThrottle) {
	return NewSignInThrottle(ResendPerAddress, ResendMaxKeys, ResendWindow, now),
		NewSignInThrottle(ResendPerIP, ResendMaxKeys, ResendWindow, now)
}

// newResend builds the handler with fresh production-sized limits and enforcement on.
func newResend(authURL *url.URL, client *http.Client, floor time.Duration, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	perAddress, perIP := resendThrottles(time.Now)
	return ResendVerificationHandler(authURL, client, floor, perAddress, perIP, true, log)
}

func resendBody(email string) string {
	b, _ := json.Marshal(map[string]string{"email": email})
	return string(b)
}

// serveResend posts body (with X-Real-IP when realIP is set) and times ServeHTTP.
func serveResend(ctx context.Context, h http.Handler, body, realIP string) (*httptest.ResponseRecorder, time.Duration) {
	req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	return rec, time.Since(start)
}

func requireAccepted(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != resendAccepted {
		t.Fatalf("%s: status %d, body %q; want 202 %s", what, rec.Code, rec.Body.String(), resendAccepted)
	}
}

func headerNames(rec *httptest.ResponseRecorder) []string {
	return slices.Sorted(maps.Keys(rec.Header()))
}

// requireSameAnswer fails unless got equals want in status, body bytes and header names.
func requireSameAnswer(t *testing.T, what string, got, want *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code != want.Code || got.Body.String() != want.Body.String() || !slices.Equal(headerNames(got), headerNames(want)) {
		t.Errorf("%s: status %d, body %q, headers %v; want status %d, body %q, headers %v",
			what, got.Code, got.Body.String(), headerNames(got), want.Code, want.Body.String(), headerNames(want))
	}
}

// logRecords decodes every JSON record in buf; numbers stay float64.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func recordsNamed(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func warnCount(t *testing.T, buf *bytes.Buffer) int {
	t.Helper()
	n := 0
	for _, rec := range logRecords(t, buf) {
		if rec["level"] == "WARN" {
			n++
		}
	}
	return n
}

// requireLimitLine requires exactly one limit-reached record with these attributes.
func requireLimitLine(t *testing.T, buf *bytes.Buffer, msg, limit, source string, enforced bool) {
	t.Helper()
	lines := recordsNamed(t, buf, msg)
	if len(lines) != 1 {
		t.Fatalf("%d %q lines, want 1: %s", len(lines), msg, buf.String())
	}
	rec := lines[0]
	if rec["level"] != "WARN" || rec["limit"] != limit || rec["key_source"] != source || rec["enforced"] != enforced {
		t.Errorf("%q record = %v; want level WARN, limit=%s, key_source=%s, enforced=%v", msg, rec, limit, source, enforced)
	}
}

func businessAddress(i int) string { return fmt.Sprintf("user-%d@corp.example", i) }

func TestResendVerification_PostsSignupTypeToGoTrue(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newResend(fake.URL, testClient(), 0, nil)

	rec, _ := serveResend(t.Context(), h, `{"email":"  Ada@Corp.example "}`, "")

	requireAccepted(t, rec, "resend")
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
	if want := map[string]string{"type": "signup", "email": "Ada@Corp.example"}; !maps.Equal(sent, want) {
		t.Errorf("GoTrue body = %v, want %v", sent, want)
	}
}

func TestResendVerification_EveryUpstreamAnswerIsTheSame202(t *testing.T) {
	rows := []struct {
		name    string
		status  int
		body    string
		warnMsg string
	}{
		{"200 empty", http.StatusOK, `{}`, ""},
		{"200 message_id", http.StatusOK, `{"message_id":"m"}`, ""},
		{"429 over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit, "resend-verification: gotrue email send rate limit"},
		{"429 over_request_rate_limit", http.StatusTooManyRequests, gtOverRequestRateLimit, "resend-verification: gotrue resend failed"},
		{"500", http.StatusInternalServerError, gtInternal, "resend-verification: gotrue resend failed"},
		{"403", http.StatusForbidden, `{"code":403,"error_code":"not_admin","msg":"forbidden"}`, "resend-verification: gotrue resend failed"},
		{"400 email_address_not_authorized", http.StatusBadRequest, `{"code":400,"error_code":"email_address_not_authorized","msg":"Email address is not authorized"}`, "resend-verification: gotrue resend failed"},
	}
	var first *httptest.ResponseRecorder
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			log, buf := captureLog()

			rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, log), resendBody("ada@corp.example"), "")

			requireAccepted(t, rec, c.name)
			if first == nil {
				first = rec
			}
			requireSameAnswer(t, c.name, rec, first)
			if n := len(fake.Calls()); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
			}
			wantWarns := 0
			if c.warnMsg != "" {
				wantWarns = 1
			}
			if n := warnCount(t, buf); n != wantWarns {
				t.Fatalf("%d WARN lines, want %d: %s", n, wantWarns, buf.String())
			}
			if c.warnMsg != "" {
				lines := recordsNamed(t, buf, c.warnMsg)
				if len(lines) != 1 {
					t.Fatalf("%d %q lines, want 1: %s", len(lines), c.warnMsg, buf.String())
				}
				if got := lines[0]["upstream_status"]; got != float64(c.status) {
					t.Errorf("upstream_status = %v, want %d", got, c.status)
				}
			}
		})
	}
}

func TestResendVerification_BadRequestsAre400AtOnce(t *testing.T) {
	email255 := strings.Repeat("a", 255-len("@corp.example")) + "@corp.example"
	rows := []struct{ name, body, msg string }{
		{"malformed JSON", `{`, "invalid request body"},
		{"body over 1 KiB", `{"email":"` + strings.Repeat("a", 1013) + `"}`, "invalid request body"},
		{"empty email", `{"email":""}`, "email is required"},
		{"whitespace email", `{"email":"  "}`, "email is required"},
		{"255-byte email", resendBody(email255), "invalid email address"},
	}
	if len(rows[1].body) != 1025 || len(email255) != 255 {
		t.Fatal("fixture sizes are off")
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, `{}`)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			rec, elapsed := serveResend(ctx, newResend(fake.URL, testClient(), time.Hour, nil), c.body, "")

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d after %v, want 400: %s", rec.Code, elapsed, rec.Body.String())
			}
			if got := errorBody(t, rec); got != c.msg {
				t.Errorf("error = %q, want %q", got, c.msg)
			}
			if elapsed >= time.Second {
				t.Errorf("answered after %v, want no wait", elapsed)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want none", n)
			}
		})
	}
}

func TestResendVerification_EmailAtTheByteCapIsSent(t *testing.T) {
	email := strings.Repeat("a", 254-len("@corp.example")) + "@corp.example"
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)

	rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, nil), resendBody(email), "")

	requireAccepted(t, rec, "254-byte email")
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1", n)
	}
}

func TestResendVerification_GoTrueValidationFailedIs400AtOnce(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusBadRequest, gtValidationFailed)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	rec, elapsed := serveResend(ctx, newResend(fake.URL, testClient(), time.Hour, nil), resendBody("not-an-address"), "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d after %v, want 400: %s", rec.Code, elapsed, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "invalid email address" {
		t.Errorf("error = %q, want %q", got, "invalid email address")
	}
	if elapsed >= time.Second {
		t.Errorf("answered after %v, want no wait", elapsed)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1 (the format check is GoTrue's)", n)
	}
}

func TestResendVerification_NonPostIs405AtOnce(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newResend(fake.URL, testClient(), time.Hour, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/auth/resend-verification", nil).WithContext(ctx)

			start := time.Now()
			h.ServeHTTP(rec, req)
			elapsed := time.Since(start)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
			if elapsed >= time.Second {
				t.Errorf("answered after %v, want no wait", elapsed)
			}
		})
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want none", n)
	}

	// Control: the same fake is reached by a POST, so the zero above is not a dead fake.
	if rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, nil), resendBody("ada@corp.example"), ""); rec.Code != http.StatusAccepted || len(fake.Calls()) != 1 {
		t.Errorf("control POST: status %d, %d GoTrue calls; want 202 and 1", rec.Code, len(fake.Calls()))
	}
}

func TestResendVerification_TransportErrorsAreTheSame202(t *testing.T) {
	const floor = 300 * time.Millisecond
	baseline, _ := serveResend(t.Context(), newResend(newFakeGoTrue(t, http.StatusOK, `{}`).URL, testClient(), 0, nil), resendBody("ada@corp.example"), "")
	requireAccepted(t, baseline, "GoTrue 200")

	rows := []struct {
		name   string
		auth   *url.URL
		client *http.Client
	}{
		{"refused connection", closedURL(t), testClient()},
		{"client timeout", slowGoTrue(t, 500*time.Millisecond, http.StatusOK, `{}`), &http.Client{Timeout: 100 * time.Millisecond}},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			log, buf := captureLog()

			rec, elapsed := serveResend(t.Context(), newResend(c.auth, c.client, floor, log), resendBody("ada@corp.example"), "")

			requireSameAnswer(t, c.name, rec, baseline)
			requireAccepted(t, rec, c.name)
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
			if n := len(recordsNamed(t, buf, "resend-verification: gotrue unreachable")); n != 1 {
				t.Errorf("%d unreachable WARN lines, want 1: %s", n, buf.String())
			}
		})
	}
}

func TestResendVerification_EveryNon400AnswerWaitsTheFloor(t *testing.T) {
	const floor = 300 * time.Millisecond
	const addr = "ada@corp.example"
	spentAddress := func() *SignInThrottle {
		perAddress, _ := resendThrottles(time.Now)
		for range ResendPerAddress {
			perAddress.Reserve(addr)
		}
		return perAddress
	}
	type row struct {
		name      string
		build     func(t *testing.T) (h http.Handler, fake *fakeGoTrue)
		wantCalls int
	}
	viaFake := func(status int, body string) func(*testing.T) (http.Handler, *fakeGoTrue) {
		return func(t *testing.T) (http.Handler, *fakeGoTrue) {
			f := newFakeGoTrue(t, status, body)
			return newResend(f.URL, testClient(), floor, nil), f
		}
	}
	for _, c := range []row{
		{"GoTrue 200", viaFake(http.StatusOK, `{}`), 1},
		{"429 cooldown", viaFake(http.StatusTooManyRequests, gtOverEmailSendRateLimit), 1},
		{"500", viaFake(http.StatusInternalServerError, gtInternal), 1},
		{"over the address limit", func(t *testing.T) (http.Handler, *fakeGoTrue) {
			f := newFakeGoTrue(t, http.StatusOK, `{}`)
			_, perIP := resendThrottles(time.Now)
			return ResendVerificationHandler(f.URL, testClient(), floor, spentAddress(), perIP, true, slog.New(slog.DiscardHandler)), f
		}, 0},
		{"refused connection", func(t *testing.T) (http.Handler, *fakeGoTrue) {
			return newResend(closedURL(t), testClient(), floor, nil), nil
		}, 0},
		{"client timeout", func(t *testing.T) (http.Handler, *fakeGoTrue) {
			return newResend(slowGoTrue(t, 500*time.Millisecond, http.StatusOK, `{}`), &http.Client{Timeout: 100 * time.Millisecond}, floor, nil), nil
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, fake := c.build(t)

			rec, elapsed := serveResend(t.Context(), h, resendBody(addr), "")

			requireAccepted(t, rec, c.name)
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
			if fake != nil {
				if n := len(fake.Calls()); n != c.wantCalls {
					t.Errorf("GoTrue saw %d calls, want %d", n, c.wantCalls)
				}
			}
		})
	}
}

func TestResendVerification_SlowUpstreamIsNotDelayedFurther(t *testing.T) {
	const floor, upstream = 100 * time.Millisecond, 500 * time.Millisecond
	auth := slowGoTrue(t, upstream, http.StatusOK, `{}`)
	log, buf := captureLog()

	rec, elapsed := serveResend(t.Context(), newResend(auth, testClient(), floor, log), resendBody("ada@corp.example"), "")

	requireAccepted(t, rec, "slow upstream")
	if elapsed < upstream-50*time.Millisecond || elapsed >= upstream+200*time.Millisecond {
		t.Errorf("answered after %v, want about the upstream's %v", elapsed, upstream)
	}
	lines := recordsNamed(t, buf, resendTimingMsg)
	if len(lines) != 1 || lines[0]["level"] != "WARN" {
		t.Errorf("timing lines = %v, want one at WARN: %s", lines, buf.String())
	}
}

func TestResendVerification_ClientGoneWritesNothing(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newResend(fake.URL, testClient(), time.Hour, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", strings.NewReader(resendBody("ada@corp.example"))).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler still waiting 3 s after the client left")
	}

	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1: the client must leave during the wait, not before it", n)
	}
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 || rec.Code != http.StatusOK {
		t.Errorf("wrote status %d, headers %v, body %q after the client left", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestResendVerification_TimingLineNamesTheRoute(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()

	rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 300*time.Millisecond, log), resendBody("ada@corp.example"), "")

	requireAccepted(t, rec, "resend")
	lines := recordsNamed(t, buf, resendTimingMsg)
	if len(lines) != 1 || lines[0]["level"] != "INFO" {
		t.Fatalf("%q lines = %v, want one at INFO: %s", resendTimingMsg, lines, buf.String())
	}
	if n := len(recordsNamed(t, buf, timingMsg)); n != 0 {
		t.Errorf("%d %q lines on the resend route, want 0", n, timingMsg)
	}
}

func TestResendVerification_PerAddressLimit(t *testing.T) {
	clock := newTestClock()
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()
	perAddress, perIP := resendThrottles(clock.Now)
	h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)
	post := func(email string) *httptest.ResponseRecorder {
		rec, _ := serveResend(t.Context(), h, resendBody(email), "")
		return rec
	}

	first := post("ada@corp.example")
	requireAccepted(t, first, "first request")
	post(" Ada@Corp.example")
	post("ADA@corp.example ")
	if n := len(fake.Calls()); n != 3 {
		t.Fatalf("GoTrue saw %d calls after 3 requests, want 3", n)
	}
	post("bob@corp.example")
	fourth := post("ada@corp.example")

	requireSameAnswer(t, "fourth request for one address", fourth, first)
	if n := len(fake.Calls()); n != 4 {
		t.Errorf("GoTrue saw %d calls, want 4 (3 for ada, 1 for bob; ada's fourth refused)", n)
	}
	requireLimitLine(t, buf, resendLimitMsg, "address", "remote_addr", true)

	clock.Advance(time.Hour)
	post("ada@corp.example")
	post("bob@corp.example")
	if n := len(fake.Calls()); n != 6 {
		t.Errorf("GoTrue saw %d calls after the window, want 6 (ada and bob allowed again)", n)
	}
}

func TestResendVerification_CooldownRefusalsAreRefunded(t *testing.T) {
	ok, cooldown := gtAnswer{http.StatusOK, `{}`}, gtAnswer{http.StatusTooManyRequests, gtOverEmailSendRateLimit}
	run := func(t *testing.T, perAddress, perIP *SignInThrottle, log *slog.Logger, n int, answers ...gtAnswer) *fakeGoTrue {
		t.Helper()
		fake := sequenceGoTrue(t, answers...)
		h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)
		for range n {
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"), "")
			requireAccepted(t, rec, "resend")
		}
		return fake
	}

	t.Run("address count", func(t *testing.T) {
		log, buf := captureLog()
		perAddress, perIP := resendThrottles(time.Now)
		fake := run(t, perAddress, perIP, log, 6, ok, cooldown, cooldown, ok, ok, ok)
		if n := len(fake.Calls()); n != 5 {
			t.Errorf("GoTrue saw %d calls, want 5 (the two cooldown answers refunded)", n)
		}
		requireLimitLine(t, buf, resendLimitMsg, "address", "remote_addr", true)
	})
	t.Run("address count control: 500 is not refunded", func(t *testing.T) {
		log, buf := captureLog()
		perAddress, perIP := resendThrottles(time.Now)
		fake := run(t, perAddress, perIP, log, 6, ok, gtAnswer{http.StatusInternalServerError, gtInternal}, gtAnswer{http.StatusInternalServerError, gtInternal}, ok, ok, ok)
		if n := len(fake.Calls()); n != 3 {
			t.Errorf("GoTrue saw %d calls, want 3 (500 counts, the fourth is refused)", n)
		}
		if n := len(recordsNamed(t, buf, resendLimitMsg)); n != 3 {
			t.Errorf("%d limit lines, want 3 (requests 4 to 6): %s", n, buf.String())
		}
	})
	t.Run("ip count", func(t *testing.T) {
		log, buf := captureLog()
		perAddress := NewSignInThrottle(100, ResendMaxKeys, ResendWindow, time.Now)
		perIP := NewSignInThrottle(2, ResendMaxKeys, ResendWindow, time.Now)
		fake := run(t, perAddress, perIP, log, 5, ok, cooldown, cooldown, ok, ok)
		if n := len(fake.Calls()); n != 4 {
			t.Errorf("GoTrue saw %d calls, want 4 (the two cooldown answers refunded; the fifth refused)", n)
		}
		requireLimitLine(t, buf, resendLimitMsg, "ip", "remote_addr", true)
	})
}

func TestResendVerification_PerIPLimit(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()
	h := newResend(fake.URL, testClient(), 0, log)
	var recs []*httptest.ResponseRecorder
	for i := range 11 {
		rec, _ := serveResend(t.Context(), h, resendBody(businessAddress(i)), "203.0.113.7")
		recs = append(recs, rec)
	}

	requireAccepted(t, recs[0], "first request")
	requireSameAnswer(t, "eleventh request", recs[10], recs[0])
	if n := len(fake.Calls()); n != 10 {
		t.Errorf("GoTrue saw %d calls, want 10", n)
	}
	requireLimitLine(t, buf, resendLimitMsg, "ip", "header", true)

	serveResend(t.Context(), h, resendBody(businessAddress(11)), "203.0.113.8")
	if n := len(fake.Calls()); n != 11 {
		t.Errorf("GoTrue saw %d calls after a request from another key, want 11", n)
	}
}

func TestResendVerification_IPRefusalSpendsNoAddressCount(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newResend(fake.URL, testClient(), 0, nil)
	for i := range ResendPerIP {
		serveResend(t.Context(), h, resendBody(businessAddress(i)), "203.0.113.7")
	}
	if n := len(fake.Calls()); n != ResendPerIP {
		t.Fatalf("GoTrue saw %d calls, want %d", n, ResendPerIP)
	}

	for range 3 {
		serveResend(t.Context(), h, resendBody("ada@corp.example"), "203.0.113.7")
	}
	if n := len(fake.Calls()); n != ResendPerIP {
		t.Fatalf("GoTrue saw %d calls after 3 refused requests, want still %d", n, ResendPerIP)
	}

	for range 3 {
		serveResend(t.Context(), h, resendBody("ada@corp.example"), "203.0.113.8")
	}
	if n := len(fake.Calls()); n != ResendPerIP+3 {
		t.Errorf("GoTrue saw %d calls, want %d: the refused requests spent ada's count", n, ResendPerIP+3)
	}
}

func TestClientKey(t *testing.T) {
	const remote = "192.0.2.1:5555"
	rows := []struct {
		name, header string
		absent       bool
		key, source  string
	}{
		{name: "plain IPv4", header: "203.0.113.7", key: "203.0.113.7", source: "header"},
		{name: "IPv4 with surrounding spaces", header: " 203.0.113.7 ", key: "203.0.113.7", source: "header"},
		{name: "IPv4-mapped IPv6", header: "::ffff:203.0.113.7", key: "203.0.113.7", source: "header"},
		{name: "IPv6 keyed by its /64", header: "2001:db8:1:2::1", key: "2001:db8:1:2::/64", source: "header"},
		{name: "IPv6 in the same /64", header: "2001:db8:1:2:ffff::9", key: "2001:db8:1:2::/64", source: "header"},
		{name: "IPv6 in another /64", header: "2001:db8:1:3::1", key: "2001:db8:1:3::/64", source: "header"},
		{name: "upper-case IPv6", header: "2001:DB8:1:2::1", key: "2001:db8:1:2::/64", source: "header"},
		{name: "zone eth0", header: "fe80::1%eth0", key: "fe80::/64", source: "header"},
		{name: "zone eth1", header: "fe80::1%eth1", key: "fe80::/64", source: "header"},
		{name: "IPv6 loopback", header: "::1", key: "::/64", source: "header"},
		{name: "absent header", absent: true, key: "192.0.2.1", source: "remote_addr"},
		{name: "empty header", header: "", key: "192.0.2.1", source: "remote_addr"},
		{name: "garbage", header: "garbage", key: "192.0.2.1", source: "remote_addr"},
		{name: "IPv4 with a port", header: "203.0.113.7:4444", key: "192.0.2.1", source: "remote_addr"},
		{name: "bracketed IPv6", header: "[2001:db8::1]", key: "192.0.2.1", source: "remote_addr"},
		{name: "list", header: "203.0.113.7, 10.0.0.1", key: "192.0.2.1", source: "remote_addr"},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", nil)
			req.RemoteAddr = remote
			if !c.absent {
				req.Header.Set("X-Real-IP", c.header)
			}

			key, source := clientKey(req)

			if key != c.key || source != c.source {
				t.Errorf("clientKey(X-Real-IP %q) = (%q, %q), want (%q, %q)", c.header, key, source, c.key, c.source)
			}
		})
	}
}

func TestResendVerification_LogsCarryNoAddressOrIP(t *testing.T) {
	const email, ip = "leak-probe@corp.example", "203.0.113.77"
	const floor = 50 * time.Millisecond
	echo := func(code string) string {
		return `{"code":0,"error_code":"` + code + `","msg":"for ` + email + `","email":"` + email + `"}`
	}
	log, buf := captureLog()
	run := func(auth *url.URL, perAddress, perIP *SignInThrottle) {
		t.Helper()
		serveResend(t.Context(), ResendVerificationHandler(auth, testClient(), floor, perAddress, perIP, true, log), resendBody(email), ip)
	}
	fresh := func() (*SignInThrottle, *SignInThrottle) { return resendThrottles(time.Now) }

	a, i := fresh()
	run(newFakeGoTrue(t, http.StatusOK, echo("ok")).URL, a, i)
	a, i = fresh()
	run(newFakeGoTrue(t, http.StatusTooManyRequests, echo("over_email_send_rate_limit")).URL, a, i)
	a, i = fresh()
	run(newFakeGoTrue(t, http.StatusInternalServerError, echo("unexpected_failure")).URL, a, i)
	a, i = fresh()
	run(closedURL(t), a, i)
	a, i = fresh()
	for range ResendPerAddress {
		a.Reserve(email)
	}
	run(newFakeGoTrue(t, http.StatusOK, `{}`).URL, a, i)
	a, i = fresh()
	for range ResendPerIP {
		i.Reserve(ip)
	}
	run(newFakeGoTrue(t, http.StatusOK, `{}`).URL, a, i)

	for _, msg := range []string{
		"resend-verification: gotrue email send rate limit",
		"resend-verification: gotrue resend failed",
		"resend-verification: gotrue unreachable",
		resendLimitMsg,
		resendTimingMsg,
	} {
		if len(recordsNamed(t, buf, msg)) == 0 {
			t.Errorf("no %q line: the outcome did not log, so the leak scan below proves nothing: %s", msg, buf.String())
		}
	}
	for _, secret := range []string{"leak-probe", ip} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log carries %q: %s", secret, buf.String())
		}
	}
}

func TestResendVerification_UnenforcedLimitOnlyLogs(t *testing.T) {
	t.Run("ip", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, `{}`)
		log, buf := captureLog()
		perAddress, perIP := resendThrottles(time.Now)
		h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, false, log)
		for i := range 11 {
			rec, _ := serveResend(t.Context(), h, resendBody(businessAddress(i)), "203.0.113.7")
			requireAccepted(t, rec, "resend")
		}
		if n := len(fake.Calls()); n != 11 {
			t.Errorf("GoTrue saw %d calls, want 11", n)
		}
		requireLimitLine(t, buf, resendLimitMsg, "ip", "header", false)
	})
	t.Run("address", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, `{}`)
		log, buf := captureLog()
		perAddress, perIP := resendThrottles(time.Now)
		h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, false, log)
		for range ResendPerAddress + 1 {
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"), "")
			requireAccepted(t, rec, "resend")
		}
		if n := len(fake.Calls()); n != ResendPerAddress+1 {
			t.Errorf("GoTrue saw %d calls, want %d", n, ResendPerAddress+1)
		}
		requireLimitLine(t, buf, resendLimitMsg, "address", "remote_addr", false)
	})
}
