package gateway

import (
	"context"
	"encoding/json"
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
	resetTimingMsg      = "reset-request: timing"
	resetLimitMsg       = "reset-request: limit reached"
	resetRateLimitMsg   = "reset-request: gotrue email send rate limit"
	resetFailedMsg      = "reset-request: gotrue recover failed"
	resetUnreachableMsg = "reset-request: gotrue unreachable"
)

// newReset builds the handler with fresh production-sized limits and enforcement on.
func newReset(authURL *url.URL, client *http.Client, floor time.Duration, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	perAddress, perIP := resendThrottles(time.Now)
	return RequestPasswordResetHandler(authURL, client, floor, perAddress, perIP, true, log)
}

// serveReset posts body (with X-Real-IP when realIP is set) and times ServeHTTP.
func serveReset(ctx context.Context, h http.Handler, body, realIP string) (*httptest.ResponseRecorder, time.Duration) {
	req := httptest.NewRequest(http.MethodPost, "/auth/request-password-reset", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	return rec, time.Since(start)
}

func callsTo(f *fakeGoTrue, path string) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Path == path {
			n++
		}
	}
	return n
}

func TestRequestPasswordReset_PostsTheEmailToRecover(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)

	rec, _ := serveReset(t.Context(), newReset(fake.URL, testClient(), 0, nil), `{"email":"  Ada@Corp.example "}`, "")

	requireAccepted(t, rec, "reset")
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/recover" {
		t.Errorf("GoTrue call = %s %s, want POST /recover", calls[0].Method, calls[0].Path)
	}
	var sent map[string]string
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("GoTrue body %q is not a JSON object of strings: %v", calls[0].Body, err)
	}
	if want := map[string]string{"email": "Ada@Corp.example"}; !maps.Equal(sent, want) {
		t.Errorf("GoTrue body = %v, want %v (no type key)", sent, want)
	}
}

func TestRequestPasswordReset_EveryUpstreamAnswerIsTheSame202(t *testing.T) {
	rows := []struct {
		name    string
		status  int
		body    string
		warnMsg string
		closed  bool
	}{
		{name: "200 empty", status: http.StatusOK, body: `{}`},
		{name: "429 over_email_send_rate_limit", status: http.StatusTooManyRequests, body: gtOverEmailSendRateLimit, warnMsg: resetRateLimitMsg},
		{name: "429 over_request_rate_limit", status: http.StatusTooManyRequests, body: gtOverRequestRateLimit, warnMsg: resetFailedMsg},
		{name: "500", status: http.StatusInternalServerError, body: gtInternal, warnMsg: resetFailedMsg},
		{name: "403", status: http.StatusForbidden, body: `{"code":403,"error_code":"not_admin","msg":"forbidden"}`, warnMsg: resetFailedMsg},
		{name: "400 email_address_not_authorized", status: http.StatusBadRequest, body: `{"code":400,"error_code":"email_address_not_authorized","msg":"Email address is not authorized"}`, warnMsg: resetFailedMsg},
		{name: "refused connection", closed: true, warnMsg: resetUnreachableMsg},
	}
	var first *httptest.ResponseRecorder
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			auth, fake := closedURL(t), (*fakeGoTrue)(nil)
			if !c.closed {
				fake = newFakeGoTrue(t, c.status, c.body)
				auth = fake.URL
			}
			log, buf := captureLog()

			rec, _ := serveReset(t.Context(), newReset(auth, testClient(), 0, log), resendBody("ada@corp.example"), "")

			requireAccepted(t, rec, c.name)
			if first == nil {
				first = rec
			}
			requireSameAnswer(t, c.name, rec, first)
			if fake != nil {
				calls := fake.Calls()
				if len(calls) != 1 || calls[0].Path != "/recover" {
					t.Errorf("GoTrue calls = %v, want one POST /recover", calls)
				}
			}
			wantWarns := 0
			if c.warnMsg != "" {
				wantWarns = 1
			}
			if n := warnCount(t, buf); n != wantWarns {
				t.Fatalf("%d WARN lines, want %d: %s", n, wantWarns, buf.String())
			}
			if c.warnMsg == "" {
				return
			}
			lines := recordsNamed(t, buf, c.warnMsg)
			if len(lines) != 1 {
				t.Fatalf("%d %q lines, want 1: %s", len(lines), c.warnMsg, buf.String())
			}
			if !c.closed && lines[0]["upstream_status"] != float64(c.status) {
				t.Errorf("upstream_status = %v, want %d", lines[0]["upstream_status"], c.status)
			}
		})
	}
}

func TestRequestPasswordReset_BadRequestsAre400AtOnce(t *testing.T) {
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

			rec, elapsed := serveReset(ctx, newReset(fake.URL, testClient(), time.Hour, nil), c.body, "")

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

func TestRequestPasswordReset_EmailAtTheByteCapIsSent(t *testing.T) {
	email := strings.Repeat("a", 254-len("@corp.example")) + "@corp.example"
	if len(email) != 254 {
		t.Fatalf("fixture is %d bytes, want 254", len(email))
	}
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)

	rec, _ := serveReset(t.Context(), newReset(fake.URL, testClient(), 0, nil), resendBody(email), "")

	requireAccepted(t, rec, "254-byte email")
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1", n)
	}
}

func TestRequestPasswordReset_GoTrueValidationFailedIs400AtOnce(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusBadRequest, gtValidationFailed)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	rec, elapsed := serveReset(ctx, newReset(fake.URL, testClient(), time.Hour, nil), resendBody("not-an-address"), "")

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

func TestRequestPasswordReset_EveryNon400AnswerWaitsTheFloor(t *testing.T) {
	const floor = 300 * time.Millisecond
	const addr = "ada@corp.example"
	type build func(t *testing.T, log *slog.Logger) (http.Handler, *fakeGoTrue)
	viaFake := func(status int, body string) build {
		return func(t *testing.T, log *slog.Logger) (http.Handler, *fakeGoTrue) {
			f := newFakeGoTrue(t, status, body)
			return newReset(f.URL, testClient(), floor, log), f
		}
	}
	rows := []struct {
		name      string
		build     build
		wantCalls int
	}{
		{"GoTrue 200", viaFake(http.StatusOK, `{}`), 1},
		{"429 cooldown", viaFake(http.StatusTooManyRequests, gtOverEmailSendRateLimit), 1},
		{"500", viaFake(http.StatusInternalServerError, gtInternal), 1},
		{"over the address limit", func(t *testing.T, log *slog.Logger) (http.Handler, *fakeGoTrue) {
			f := newFakeGoTrue(t, http.StatusOK, `{}`)
			perAddress, perIP := resendThrottles(time.Now)
			for range ResendPerAddress {
				perAddress.Reserve(addr)
			}
			return RequestPasswordResetHandler(f.URL, testClient(), floor, perAddress, perIP, true, log), f
		}, 0},
		{"refused connection", func(t *testing.T, log *slog.Logger) (http.Handler, *fakeGoTrue) {
			return newReset(closedURL(t), testClient(), floor, log), nil
		}, 0},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			log, buf := captureLog()
			h, fake := c.build(t, log)

			rec, elapsed := serveReset(t.Context(), h, resendBody(addr), "")

			requireAccepted(t, rec, c.name)
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
			if fake != nil {
				if n := len(fake.Calls()); n != c.wantCalls {
					t.Errorf("GoTrue saw %d calls, want %d", n, c.wantCalls)
				}
			}
			lines := recordsNamed(t, buf, resetTimingMsg)
			if len(lines) != 1 || lines[0]["level"] != "INFO" {
				t.Errorf("%q lines = %v, want one at INFO: %s", resetTimingMsg, lines, buf.String())
			}
		})
	}
}

func TestRequestPasswordReset_ClientGoneWritesNothing(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newReset(fake.URL, testClient(), time.Hour, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/request-password-reset", strings.NewReader(resendBody("ada@corp.example"))).WithContext(ctx)

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

func TestRequestPasswordReset_SharesTheResendLimits(t *testing.T) {
	// pair builds both handlers over one fake; shared passes one throttle pair to both.
	pair := func(fake *fakeGoTrue, log *slog.Logger, shared bool) (resend, reset http.Handler) {
		perAddress, perIP := resendThrottles(time.Now)
		resend = ResendVerificationHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)
		if !shared {
			perAddress, perIP = resendThrottles(time.Now)
		}
		return resend, RequestPasswordResetHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)
	}
	for _, shared := range []bool{true, false} {
		name := "shared instances"
		if !shared {
			name = "separate instances"
		}
		t.Run(name, func(t *testing.T) {
			// The first three requests (resend or reset per letter) spend the address; the fourth is a reset.
			for _, mix := range []string{"SST", "TTS"} {
				t.Run("address "+mix, func(t *testing.T) {
					fake := newFakeGoTrue(t, http.StatusOK, `{}`)
					log, buf := captureLog()
					resend, reset := pair(fake, log, shared)
					spellings := []string{"ada@corp.example", " Ada@Corp.example", " ADA@corp.example "}
					var first *httptest.ResponseRecorder
					for i, kind := range mix {
						h, serve := resend, serveResend
						if kind == 'T' {
							h, serve = reset, serveReset
						}
						rec, _ := serve(t.Context(), h, resendBody(spellings[i]), "")
						if first == nil {
							first = rec
							requireAccepted(t, first, "first request")
						}
					}
					if n := len(fake.Calls()); n != 3 {
						t.Fatalf("GoTrue saw %d calls after the mix %s, want 3", n, mix)
					}
					if got, want := callsTo(fake, "/recover"), strings.Count(mix, "T"); got != want {
						t.Fatalf("%d calls to /recover after the mix %s, want %d", got, mix, want)
					}

					fourth, _ := serveReset(t.Context(), reset, resendBody(" ADA@corp.example"), "")

					requireSameAnswer(t, "fourth mail-link request", fourth, first)
					if !shared {
						if n := len(fake.Calls()); n != 4 {
							t.Errorf("GoTrue saw %d calls, want 4: separate counts must not refuse", n)
						}
						if n := len(recordsNamed(t, buf, resetLimitMsg)); n != 0 {
							t.Errorf("%d limit lines, want 0: %s", n, buf.String())
						}
						return
					}
					if n := len(fake.Calls()); n != 3 {
						t.Errorf("GoTrue saw %d calls, want 3 (the fourth refused)", n)
					}
					requireLimitLine(t, buf, resetLimitMsg, "address", "remote_addr", true)
				})
			}
			t.Run("ip", func(t *testing.T) {
				fake := newFakeGoTrue(t, http.StatusOK, `{}`)
				log, buf := captureLog()
				resend, reset := pair(fake, log, shared)
				var first *httptest.ResponseRecorder
				// The AC names the numbers: ten sent, the eleventh refused.
				for i := range 10 {
					serve := func() (*httptest.ResponseRecorder, time.Duration) {
						if i%2 == 1 {
							return serveReset(t.Context(), reset, resendBody(businessAddress(i)), "203.0.113.7")
						}
						return serveResend(t.Context(), resend, resendBody(businessAddress(i)), "203.0.113.7")
					}
					rec, _ := serve()
					if first == nil {
						first = rec
					}
				}
				if n := len(fake.Calls()); n != 10 {
					t.Fatalf("GoTrue saw %d calls after 10 requests, want 10", n)
				}

				eleventh, _ := serveReset(t.Context(), reset, resendBody(businessAddress(10)), "203.0.113.7")

				requireSameAnswer(t, "eleventh mail-link request", eleventh, first)
				if !shared {
					if n := len(fake.Calls()); n != 11 {
						t.Errorf("GoTrue saw %d calls, want 11: separate counts must not refuse", n)
					}
					if n := len(recordsNamed(t, buf, resetLimitMsg)); n != 0 {
						t.Errorf("%d limit lines, want 0: %s", n, buf.String())
					}
					return
				}
				if n := len(fake.Calls()); n != 10 {
					t.Errorf("GoTrue saw %d calls, want 10 (the eleventh refused)", n)
				}
				requireLimitLine(t, buf, resetLimitMsg, "ip", "header", true)
			})
		})
	}
}

func TestRequestPasswordReset_CooldownRefusalsAreRefunded(t *testing.T) {
	ok, cooldown := gtAnswer{http.StatusOK, `{}`}, gtAnswer{http.StatusTooManyRequests, gtOverEmailSendRateLimit}
	fake := sequenceGoTrue(t, ok, cooldown, cooldown, ok, ok, ok)
	log, buf := captureLog()
	perAddress, perIP := resendThrottles(time.Now)
	h := RequestPasswordResetHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)

	for range 6 {
		rec, _ := serveReset(t.Context(), h, resendBody("ada@corp.example"), "")
		requireAccepted(t, rec, "reset")
	}

	if n := len(fake.Calls()); n != 5 {
		t.Errorf("GoTrue saw %d calls, want 5 (the two cooldown answers refunded)", n)
	}
	requireLimitLine(t, buf, resetLimitMsg, "address", "remote_addr", true)
}

func TestRequestPasswordReset_UnenforcedLimitOnlyLogs(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()
	perAddress, perIP := resendThrottles(time.Now)
	h := RequestPasswordResetHandler(fake.URL, testClient(), 0, perAddress, perIP, false, log)

	for range ResendPerAddress + 1 {
		rec, _ := serveReset(t.Context(), h, resendBody("ada@corp.example"), "")
		requireAccepted(t, rec, "reset")
	}

	if n := len(fake.Calls()); n != ResendPerAddress+1 {
		t.Errorf("GoTrue saw %d calls, want %d", n, ResendPerAddress+1)
	}
	requireLimitLine(t, buf, resetLimitMsg, "address", "remote_addr", false)
}

func TestRequestPasswordReset_LogsCarryNoAddressOrIP(t *testing.T) {
	const email, ip = "leak-probe@corp.example", "203.0.113.77"
	const floor = 50 * time.Millisecond
	echo := func(code string) string {
		return `{"code":0,"error_code":"` + code + `","msg":"for ` + email + `","email":"` + email + `"}`
	}
	log, buf := captureLog()
	run := func(auth *url.URL, perAddress, perIP *SignInThrottle) {
		t.Helper()
		serveReset(t.Context(), RequestPasswordResetHandler(auth, testClient(), floor, perAddress, perIP, true, log), resendBody(email), ip)
	}

	a, i := resendThrottles(time.Now)
	run(newFakeGoTrue(t, http.StatusOK, echo("ok")).URL, a, i)
	a, i = resendThrottles(time.Now)
	run(newFakeGoTrue(t, http.StatusTooManyRequests, echo("over_email_send_rate_limit")).URL, a, i)
	a, i = resendThrottles(time.Now)
	run(newFakeGoTrue(t, http.StatusInternalServerError, echo("unexpected_failure")).URL, a, i)
	a, i = resendThrottles(time.Now)
	run(closedURL(t), a, i)
	a, i = resendThrottles(time.Now)
	for range ResendPerAddress {
		a.Reserve(email)
	}
	run(newFakeGoTrue(t, http.StatusOK, `{}`).URL, a, i)
	a, i = resendThrottles(time.Now)
	for range ResendPerIP {
		i.Reserve(ip)
	}
	run(newFakeGoTrue(t, http.StatusOK, `{}`).URL, a, i)

	for _, msg := range []string{resetRateLimitMsg, resetFailedMsg, resetUnreachableMsg, resetLimitMsg, resetTimingMsg} {
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

func TestRequestPasswordReset_NonPostIs405AtOnce(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	h := newReset(fake.URL, testClient(), time.Hour, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/auth/request-password-reset", nil).WithContext(ctx)

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
	if rec, _ := serveReset(t.Context(), newReset(fake.URL, testClient(), 0, nil), resendBody("ada@corp.example"), ""); rec.Code != http.StatusAccepted || len(fake.Calls()) != 1 {
		t.Errorf("control POST: status %d, %d GoTrue calls; want 202 and 1", rec.Code, len(fake.Calls()))
	}
}

func TestRequestPasswordReset_ValidationFailedRefundsBothCounts(t *testing.T) {
	for _, limit := range []string{"address", "ip"} {
		t.Run(limit, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusBadRequest, gtValidationFailed)
			log, buf := captureLog()
			perAddress := NewSignInThrottle("resend-address", 2, ResendMaxKeys, ResendWindow, time.Now)
			perIP := NewSignInThrottle("resend-ip", 100, ResendMaxKeys, ResendWindow, time.Now)
			if limit == "ip" {
				perAddress, perIP = perIP, perAddress
			}
			h := RequestPasswordResetHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)

			for range 3 {
				rec, _ := serveReset(t.Context(), h, resendBody("ada@corp.example"), "")
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", rec.Code)
				}
			}

			if n := len(fake.Calls()); n != 3 {
				t.Errorf("GoTrue saw %d calls against a cap of 2, want 3: a validation_failed answer must refund", n)
			}
			if n := len(recordsNamed(t, buf, resetLimitMsg)); n != 0 {
				t.Errorf("%d limit lines, want 0: %s", n, buf.String())
			}
		})
	}
}

func TestRequestPasswordReset_FullMapsFailClosed(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, `{}`)
	log, buf := captureLog()
	perAddress := NewSignInThrottle("resend-address", ResendPerAddress, 1, ResendWindow, time.Now)
	perIP := NewSignInThrottle("resend-ip", ResendPerIP, 1, ResendWindow, time.Now)
	h := RequestPasswordResetHandler(fake.URL, testClient(), 0, perAddress, perIP, true, log)

	first, _ := serveReset(t.Context(), h, resendBody("ada@corp.example"), "203.0.113.7")
	newAddress, _ := serveReset(t.Context(), h, resendBody("bob@corp.example"), "203.0.113.7")
	newKey, _ := serveReset(t.Context(), h, resendBody("ada@corp.example"), "203.0.113.8")

	requireAccepted(t, first, "first request")
	requireSameAnswer(t, "new address on a full map", newAddress, first)
	requireSameAnswer(t, "new key on a full map", newKey, first)
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want 1: a full map must refuse new keys", n)
	}
	lines := recordsNamed(t, buf, resetLimitMsg)
	if len(lines) != 2 || lines[0]["limit"] != "address" || lines[1]["limit"] != "ip" {
		t.Errorf("limit lines = %v, want one limit=address then one limit=ip", lines)
	}
}
