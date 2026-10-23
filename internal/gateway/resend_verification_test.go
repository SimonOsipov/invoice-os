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
	"sync"
	"sync/atomic"
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

func resendThrottle(now func() time.Time) *SignInThrottle {
	return NewSignInThrottle("resend-address", ResendPerAddress, ResendMaxKeys, ResendWindow, now)
}

// newResend builds the handler with fresh production-sized limits and enforcement on.
func newResend(authURL *url.URL, client *http.Client, floor time.Duration, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return ResendVerificationHandler(authURL, client, floor, resendThrottle(time.Now), true, log)
}

func resendBody(email string) string {
	b, _ := json.Marshal(map[string]string{"email": email})
	return string(b)
}

// serveResend posts body and times ServeHTTP.
func serveResend(ctx context.Context, h http.Handler, body string) (*httptest.ResponseRecorder, time.Duration) {
	req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
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
func requireLimitLine(t *testing.T, buf *bytes.Buffer, msg, limit string, enforced bool) {
	t.Helper()
	lines := recordsNamed(t, buf, msg)
	if len(lines) != 1 {
		t.Fatalf("%d %q lines, want 1: %s", len(lines), msg, buf.String())
	}
	rec := lines[0]
	if rec["level"] != "WARN" || rec["limit"] != limit || rec["enforced"] != enforced {
		t.Errorf("%q record = %v; want level WARN, limit=%s, enforced=%v", msg, rec, limit, enforced)
	}
}

func TestResendVerification_PostsSignupTypeToGoTrue(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newResend(fake.URL, testClient(), 0, nil)

	rec, _ := serveResend(t.Context(), h, `{"email":"  Ada@Corp.example "}`)

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

			rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, log), resendBody("ada@corp.example"))

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
		{"255 bytes in 128 runes", resendBody(strings.Repeat("é", 127) + "x"), "invalid email address"},
		{"non-string email", `{"email":5}`, "invalid request body"},
		{"array body", `[]`, "invalid request body"},
		{"null body", `null`, "email is required"},
		{"unicode whitespace email", `{"email":"\u00a0\u3000"}`, "email is required"},
	}
	if len(rows[1].body) != 1025 || len(email255) != 255 {
		t.Fatal("fixture sizes are off")
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, `{}`)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			rec, elapsed := serveResend(ctx, newResend(fake.URL, testClient(), time.Hour, nil), c.body)

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
	for name, email := range map[string]string{
		"254 ASCII bytes":        strings.Repeat("a", 254-len("@corp.example")) + "@corp.example",
		"254 bytes in 127 runes": strings.Repeat("é", 127),
	} {
		t.Run(name, func(t *testing.T) {
			if len(email) != 254 {
				t.Fatalf("fixture is %d bytes, want 254", len(email))
			}
			fake := newFakeGoTrue(t, http.StatusOK, `{}`)

			rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, nil), resendBody(email))

			requireAccepted(t, rec, name)
			if n := len(fake.Calls()); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
			}
		})
	}
}

func TestResendVerification_GoTrueValidationFailedIs400AtOnce(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusBadRequest, gtValidationFailed)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	rec, elapsed := serveResend(ctx, newResend(fake.URL, testClient(), time.Hour, nil), resendBody("not-an-address"))

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
	if rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 0, nil), resendBody("ada@corp.example")); rec.Code != http.StatusAccepted || len(fake.Calls()) != 1 {
		t.Errorf("control POST: status %d, %d GoTrue calls; want 202 and 1", rec.Code, len(fake.Calls()))
	}
}

func TestResendVerification_TransportErrorsAreTheSame202(t *testing.T) {
	const floor = 300 * time.Millisecond
	baseline, _ := serveResend(t.Context(), newResend(newFakeGoTrue(t, http.StatusOK, `{}`).URL, testClient(), 0, nil), resendBody("ada@corp.example"))
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

			rec, elapsed := serveResend(t.Context(), newResend(c.auth, c.client, floor, log), resendBody("ada@corp.example"))

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
		perAddress := resendThrottle(time.Now)
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
			return ResendVerificationHandler(f.URL, testClient(), floor, spentAddress(), true, slog.New(slog.DiscardHandler)), f
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

			rec, elapsed := serveResend(t.Context(), h, resendBody(addr))

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
	const floor, upstream = 300 * time.Millisecond, 500 * time.Millisecond
	auth := slowGoTrue(t, upstream, http.StatusOK, `{}`)
	log, buf := captureLog()

	rec, elapsed := serveResend(t.Context(), newResend(auth, testClient(), floor, log), resendBody("ada@corp.example"))

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

	rec, _ := serveResend(t.Context(), newResend(fake.URL, testClient(), 300*time.Millisecond, log), resendBody("ada@corp.example"))

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
	h := ResendVerificationHandler(fake.URL, testClient(), 0, resendThrottle(clock.Now), true, log)
	post := func(email string) *httptest.ResponseRecorder {
		rec, _ := serveResend(t.Context(), h, resendBody(email))
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
	requireLimitLine(t, buf, resendLimitMsg, "address", true)

	clock.Advance(time.Hour)
	post("ada@corp.example")
	post("bob@corp.example")
	if n := len(fake.Calls()); n != 6 {
		t.Errorf("GoTrue saw %d calls after the window, want 6 (ada and bob allowed again)", n)
	}
}

func TestResendVerification_CooldownRefusalsAreRefunded(t *testing.T) {
	ok, cooldown := gtAnswer{http.StatusOK, `{}`}, gtAnswer{http.StatusTooManyRequests, gtOverEmailSendRateLimit}
	run := func(t *testing.T, perAddress *SignInThrottle, log *slog.Logger, n int, answers ...gtAnswer) *fakeGoTrue {
		t.Helper()
		fake := sequenceGoTrue(t, answers...)
		h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, true, log)
		for range n {
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"))
			requireAccepted(t, rec, "resend")
		}
		return fake
	}

	t.Run("address count", func(t *testing.T) {
		log, buf := captureLog()
		fake := run(t, resendThrottle(time.Now), log, 6, ok, cooldown, cooldown, ok, ok, ok)
		if n := len(fake.Calls()); n != 5 {
			t.Errorf("GoTrue saw %d calls, want 5 (the two cooldown answers refunded)", n)
		}
		requireLimitLine(t, buf, resendLimitMsg, "address", true)
	})
	t.Run("address count control: 500 is not refunded", func(t *testing.T) {
		log, buf := captureLog()
		fake := run(t, resendThrottle(time.Now), log, 6, ok, gtAnswer{http.StatusInternalServerError, gtInternal}, gtAnswer{http.StatusInternalServerError, gtInternal}, ok, ok, ok)
		if n := len(fake.Calls()); n != 3 {
			t.Errorf("GoTrue saw %d calls, want 3 (500 counts, the fourth is refused)", n)
		}
		if n := len(recordsNamed(t, buf, resendLimitMsg)); n != 3 {
			t.Errorf("%d limit lines, want 3 (requests 4 to 6): %s", n, buf.String())
		}
	})
}

func TestResendVerification_LogsCarryNoAddress(t *testing.T) {
	const email = "leak-probe@corp.example"
	const floor = 50 * time.Millisecond
	echo := func(code string) string {
		return `{"code":0,"error_code":"` + code + `","msg":"for ` + email + `","email":"` + email + `"}`
	}
	log, buf := captureLog()
	run := func(auth *url.URL, perAddress *SignInThrottle) {
		t.Helper()
		serveResend(t.Context(), ResendVerificationHandler(auth, testClient(), floor, perAddress, true, log), resendBody(email))
	}

	run(newFakeGoTrue(t, http.StatusOK, echo("ok")).URL, resendThrottle(time.Now))
	run(newFakeGoTrue(t, http.StatusTooManyRequests, echo("over_email_send_rate_limit")).URL, resendThrottle(time.Now))
	run(newFakeGoTrue(t, http.StatusInternalServerError, echo("unexpected_failure")).URL, resendThrottle(time.Now))
	run(closedURL(t), resendThrottle(time.Now))
	a := resendThrottle(time.Now)
	for range ResendPerAddress {
		a.Reserve(email)
	}
	run(newFakeGoTrue(t, http.StatusOK, `{}`).URL, a)

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
	for _, secret := range []string{"leak-probe"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log carries %q: %s", secret, buf.String())
		}
	}
}

func TestResendVerification_UnenforcedLimitOnlyLogs(t *testing.T) {
	t.Run("address", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, `{}`)
		log, buf := captureLog()
		h := ResendVerificationHandler(fake.URL, testClient(), 0, resendThrottle(time.Now), false, log)
		for range ResendPerAddress + 1 {
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"))
			requireAccepted(t, rec, "resend")
		}
		if n := len(fake.Calls()); n != ResendPerAddress+1 {
			t.Errorf("GoTrue saw %d calls, want %d", n, ResendPerAddress+1)
		}
		requireLimitLine(t, buf, resendLimitMsg, "address", false)
	})
	t.Run("a cooldown answer over a limit is not refunded", func(t *testing.T) {
		ok, cooldown := gtAnswer{http.StatusOK, `{}`}, gtAnswer{http.StatusTooManyRequests, gtOverEmailSendRateLimit}
		fake := sequenceGoTrue(t, ok, ok, cooldown, ok)
		log, buf := captureLog()
		perAddress := NewSignInThrottle("resend-address", 2, ResendMaxKeys, ResendWindow, time.Now)
		h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, false, log)
		for range 4 {
			rec, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"))
			requireAccepted(t, rec, "resend")
		}
		if n := len(fake.Calls()); n != 4 {
			t.Errorf("GoTrue saw %d calls, want 4", n)
		}
		if n := len(recordsNamed(t, buf, resendLimitMsg)); n != 2 {
			t.Errorf("%d limit lines, want 2 (requests 3 and 4): the cooldown answer to request 3 must not make room: %s", n, buf.String())
		}
	})
}

// A cap of two and three requests: the third logs a limit line only when no earlier answer returned its count.
// Every GoTrue 4xx is refunded (no mail goes out); 2xx, 5xx and transport errors keep the count.
func TestResendVerification_OnlyA4xxAnswerIsRefunded(t *testing.T) {
	viaFake := func(status int, body string) func(*testing.T) (*url.URL, *http.Client) {
		return func(t *testing.T) (*url.URL, *http.Client) { return newFakeGoTrue(t, status, body).URL, testClient() }
	}
	rows := []struct {
		name      string
		build     func(*testing.T) (*url.URL, *http.Client)
		wantLimit int
	}{
		{"429 cooldown (control)", viaFake(http.StatusTooManyRequests, gtOverEmailSendRateLimit), 0},
		{"200", viaFake(http.StatusOK, `{}`), 1},
		{"429 over_request_rate_limit", viaFake(http.StatusTooManyRequests, gtOverRequestRateLimit), 0},
		{"500", viaFake(http.StatusInternalServerError, gtInternal), 1},
		{"302 redirect", viaFake(http.StatusFound, `{}`), 1},
		{"399 below the 4xx range", viaFake(399, `{}`), 1},
		{"499 top of the 4xx range", viaFake(499, `{}`), 0},
		{"403", viaFake(http.StatusForbidden, `{"code":403,"error_code":"not_admin","msg":"forbidden"}`), 0},
		{"400 email_address_not_authorized", viaFake(http.StatusBadRequest, `{"code":400,"error_code":"email_address_not_authorized","msg":"no"}`), 0},
		{"400 validation_failed", viaFake(http.StatusBadRequest, gtValidationFailed), 0},
		{"refused connection", func(t *testing.T) (*url.URL, *http.Client) { return closedURL(t), testClient() }, 1},
		{"client timeout", func(t *testing.T) (*url.URL, *http.Client) {
			return slowGoTrue(t, 300*time.Millisecond, http.StatusOK, `{}`), &http.Client{Timeout: 50 * time.Millisecond}
		}, 1},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			auth, client := c.build(t)
			log, buf := captureLog()
			perAddress := NewSignInThrottle("resend-address", 2, ResendMaxKeys, ResendWindow, time.Now)
			h := ResendVerificationHandler(auth, client, 0, perAddress, true, log)

			for range 3 {
				serveResend(t.Context(), h, resendBody("ada@corp.example"))
			}

			lines := recordsNamed(t, buf, resendLimitMsg)
			if len(lines) != c.wantLimit {
				t.Fatalf("%d limit lines after 3 requests against a cap of 2, want %d: %s", len(lines), c.wantLimit, buf.String())
			}
			if c.wantLimit == 1 && lines[0]["limit"] != "address" {
				t.Errorf("limit = %v, want address", lines[0]["limit"])
			}
		})
	}
}

// toggleGoTrue answers 429 over_email_send_rate_limit while cooldown is set and 200 after.
func toggleGoTrue(t *testing.T, cooldown *atomic.Bool) *fakeGoTrue {
	t.Helper()
	f := &fakeGoTrue{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, gotrueCall{Method: r.Method, Path: r.URL.Path})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if cooldown.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, gtOverEmailSendRateLimit)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	f.URL = u
	return f
}

func TestResendVerification_ConcurrentRequestsRaceTheReservations(t *testing.T) {
	const n = 40
	const email = "ada@corp.example"
	race := func(h http.Handler) []*httptest.ResponseRecorder {
		recs := make([]*httptest.ResponseRecorder, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { recs[i], _ = serveResend(t.Context(), h, resendBody(email)) })
		}
		wg.Wait()
		return recs
	}
	t.Run("one address admits exactly its cap", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, `{}`)

		recs := race(newResend(fake.URL, testClient(), 0, nil))

		if got := len(fake.Calls()); got != ResendPerAddress {
			t.Errorf("GoTrue saw %d calls from %d concurrent requests, want exactly %d", got, n, ResendPerAddress)
		}
		for i, rec := range recs {
			requireSameAnswer(t, fmt.Sprintf("request %d", i), rec, recs[0])
		}
	})
	t.Run("one address refunds leave exactly its cap", func(t *testing.T) {
		var cooldown atomic.Bool
		cooldown.Store(true)
		fake := toggleGoTrue(t, &cooldown)
		h := newResend(fake.URL, testClient(), 0, nil)

		race(h)
		refused := len(fake.Calls())
		if refused == 0 {
			t.Fatal("no request reached GoTrue, so nothing was refunded")
		}
		cooldown.Store(false)
		for range ResendPerAddress + 3 {
			serveResend(t.Context(), h, resendBody(email))
		}

		if got := len(fake.Calls()) - refused; got != ResendPerAddress {
			t.Errorf("GoTrue saw %d calls after the refunds, want exactly %d: a refund left the count off", got, ResendPerAddress)
		}
	})
}

func TestResendVerification_FullMapFailsClosed(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()
	perAddress := NewSignInThrottle("resend-address", ResendPerAddress, 1, ResendWindow, time.Now)
	h := ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, true, log)

	first, _ := serveResend(t.Context(), h, resendBody("ada@corp.example"))
	newAddress, _ := serveResend(t.Context(), h, resendBody("bob@corp.example"))

	requireAccepted(t, first, "first request")
	requireSameAnswer(t, "new address on a full map", newAddress, first)
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1: a full map must refuse new keys", n)
	}
	lines := recordsNamed(t, buf, resendLimitMsg)
	if len(lines) != 1 || lines[0]["limit"] != "address" {
		t.Errorf("limit lines = %v, want one limit=address", lines)
	}
}
