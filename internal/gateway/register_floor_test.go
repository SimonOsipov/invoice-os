package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const timingMsg = "registration: signup timing"

// serveFloor serves one register request and returns the answer and how long ServeHTTP took.
func serveFloor(t *testing.T, authURL *url.URL, floor time.Duration, log *slog.Logger, body string) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	RegisterHandler(authURL, testClient(), floor, freshRegisterLimit(), true, log, noPendingInvite).ServeHTTP(rec, req)
	return rec, time.Since(start)
}

// slowGoTrue answers status and body after delay.
func slowGoTrue(t *testing.T, delay time.Duration, status int, body string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u
}

// timingLines returns the decoded "registration: signup timing" records in buf.
func timingLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec["msg"] == timingMsg {
			out = append(out, rec)
		}
	}
	return out
}

// intAttr reads a whole-number JSON attribute.
func intAttr(t *testing.T, rec map[string]any, key string) int64 {
	t.Helper()
	n, ok := rec[key].(json.Number)
	if !ok {
		t.Fatalf("%s = %v (%T), want a JSON number: %v", key, rec[key], rec[key], rec)
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want an integer: %v", key, n, err)
	}
	return v
}

var pendingOutcomes = []struct {
	name   string
	status int
	body   string
}{
	{"200 new account", http.StatusOK, gtNewUser},
	{"200 confirmed address", http.StatusOK, gtSanitizedUser},
	{"422 user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists},
	{"422 email_exists", http.StatusUnprocessableEntity, gtEmailExists},
	{"429 over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit},
	{"500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey},
}

func TestRegister_EveryPendingOutcomeWaitsTheMinimum(t *testing.T) {
	const floor = 150 * time.Millisecond
	for _, c := range pendingOutcomes {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)

			rec, elapsed := serveFloor(t, fake.URL, floor, nil, registerBody(regEmail, regPassword))

			if len(fake.Calls()) != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", len(fake.Calls()))
			}
			requirePending202(t, rec)
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
		})
	}
}

func TestRegister_NonValidationRefusalsWaitTheMinimum(t *testing.T) {
	const floor = 150 * time.Millisecond
	for _, c := range []struct {
		name       string
		status     int // GoTrue's; 0 = unreachable
		body       string
		wantStatus int
		wantError  string
	}{
		{"signup_disabled", http.StatusUnprocessableEntity, gtSignupDisabled, http.StatusServiceUnavailable, "registration is closed"},
		{"bare 429", http.StatusTooManyRequests, gtOverRequestRateLimit, http.StatusTooManyRequests, "too many requests"},
		{"unknown code", http.StatusInternalServerError, gtInternal, http.StatusBadGateway, "registration is unavailable"},
		{"unreachable", 0, "", http.StatusBadGateway, "registration is unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.status != 0 {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}

			rec, elapsed := serveFloor(t, authURL, floor, nil, registerBody(regEmail, regPassword))

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if got := errorBody(t, rec); got != c.wantError {
				t.Errorf("error = %q, want %q", got, c.wantError)
			}
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
		})
	}
}

func TestRegister_ValidationRefusalsDoNotWait(t *testing.T) {
	const floor = 3 * time.Second
	for _, c := range []struct {
		name      string
		body      string
		status    int // GoTrue's; 0 = never reached
		gtBody    string
		wantCalls int
	}{
		{"malformed JSON", `{"email":`, 0, "", 0},
		{"empty password", registerBody(regEmail, ""), 0, "", 0},
		{"free-mail", registerBody("x@gmail.com", regPassword), 0, "", 0},
		{"validation_failed", registerBody(regEmail, regPassword), http.StatusBadRequest, gtValidationFailed, 1},
		{"weak_password", registerBody(regEmail, regPassword), http.StatusUnprocessableEntity, gtWeakPassword, 1},
		{"email_address_invalid", registerBody(regEmail, regPassword), http.StatusBadRequest, gtEmailAddressInvalid, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, max(c.status, http.StatusOK), c.gtBody)
			log, buf := captureLog()

			rec, elapsed := serveFloor(t, fake.URL, floor, log, c.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if n := len(fake.Calls()); n != c.wantCalls {
				t.Errorf("GoTrue saw %d calls, want %d", n, c.wantCalls)
			}
			if elapsed >= time.Second {
				t.Errorf("answered after %v, want no wait (minimum %v)", elapsed, floor)
			}
			if n := len(timingLines(t, buf)); n != 0 {
				t.Errorf("a 400 logged %d %q lines, want none: %s", n, timingMsg, buf.String())
			}
		})
	}
}

// A wait added on top of the 600 ms upstream would answer at 1100 ms or later.
func TestRegister_SlowUpstreamIsNotDelayedFurther(t *testing.T) {
	const upstream, floor = 600 * time.Millisecond, 500 * time.Millisecond
	authURL := slowGoTrue(t, upstream, http.StatusOK, gtNewUser)

	rec, elapsed := serveFloor(t, authURL, floor, nil, registerBody(regEmail, regPassword))

	requirePending202(t, rec)
	if elapsed < upstream {
		t.Fatalf("answered after %v, before the %v upstream: the fake did not delay", elapsed, upstream)
	}
	if elapsed >= time.Second {
		t.Errorf("answered after %v, want about %v: the wait was added to the upstream time", elapsed, upstream)
	}
}

func TestRegister_ClientGoneEndsTheWaitWithoutWriting(t *testing.T) {
	answered := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gtNewUser)
		answered <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	authURL, _ := url.Parse(srv.URL)

	// Control: a client that stays gets an answer, so the empty recorder below means the handler wrote nothing.
	ctl, _ := serveFloor(t, authURL, 150*time.Millisecond, nil, registerBody(regEmail, regPassword))
	requirePending202(t, ctl)
	if ctl.Header().Get("Content-Type") == "" {
		t.Fatal("control answer has no Content-Type")
	}
	<-answered

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// GoTrue answers at once; the client leaves 50 ms later, well inside the 3 s wait.
	go func() {
		<-answered
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))).WithContext(ctx)

	start := time.Now()
	RegisterHandler(authURL, testClient(), 3*time.Second, freshRegisterLimit(), true, slog.New(slog.DiscardHandler), noPendingInvite).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed >= time.Second {
		t.Errorf("returned after %v, want the wait to end when the client leaves", elapsed)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("wrote body %q after the client left", rec.Body.String())
	}
	if h := rec.Header(); len(h) != 0 {
		t.Errorf("set headers %v after the client left", h)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the recorder's untouched default 200", rec.Code)
	}
}

func TestRegister_TimingLineLevels(t *testing.T) {
	for _, c := range []struct {
		name    string
		floor   time.Duration
		authURL func(t *testing.T) *url.URL
		level   string // "" = not asserted
	}{
		{"faster than the minimum is INFO", 300 * time.Millisecond, func(t *testing.T) *url.URL {
			return newFakeGoTrue(t, http.StatusOK, gtNewUser).URL
		}, "INFO"},
		{"slower than the minimum is WARN", 50 * time.Millisecond, func(t *testing.T) *url.URL {
			return slowGoTrue(t, 200*time.Millisecond, http.StatusOK, gtNewUser)
		}, "WARN"},
		{"unreachable still logs", 50 * time.Millisecond, closedURL, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			log, buf := captureLog()

			serveFloor(t, c.authURL(t), c.floor, log, registerBody(regEmail, regPassword))

			lines := timingLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("%d %q lines, want exactly 1: %s", len(lines), timingMsg, buf.String())
			}
			rec := lines[0]
			if c.level != "" && rec["level"] != c.level {
				t.Errorf("level = %v, want %s", rec["level"], c.level)
			}
			upstream := intAttr(t, rec, "upstream_ms")
			if got := intAttr(t, rec, "min_ms"); got != c.floor.Milliseconds() {
				t.Errorf("min_ms = %d, want %d", got, c.floor.Milliseconds())
			}
			switch c.level {
			case "INFO":
				if upstream >= c.floor.Milliseconds() {
					t.Errorf("upstream_ms = %d, want below min_ms %d", upstream, c.floor.Milliseconds())
				}
			case "WARN":
				if upstream < 200 {
					t.Errorf("upstream_ms = %d, want at least the 200 ms the upstream slept", upstream)
				}
			}
			for _, s := range []string{regEmail, regPassword} {
				if strings.Contains(buf.String(), s) {
					t.Errorf("log carries %q: %s", s, buf.String())
				}
			}
		})
	}
}

func TestRegister_NoTimingLineAtZeroMinimum(t *testing.T) {
	for _, c := range pendingOutcomes {
		t.Run(c.name, func(t *testing.T) {
			authURL := newFakeGoTrue(t, c.status, c.body).URL

			// Control: the same answer at a non-zero minimum does log the line.
			ctlLog, ctlBuf := captureLog()
			serveFloor(t, authURL, 50*time.Millisecond, ctlLog, registerBody(regEmail, regPassword))
			if n := len(timingLines(t, ctlBuf)); n != 1 {
				t.Fatalf("control at 50 ms logged %d %q lines, want 1: %s", n, timingMsg, ctlBuf.String())
			}

			log, buf := captureLog()
			rec, _ := serveFloor(t, authURL, 0, log, registerBody(regEmail, regPassword))

			requirePending202(t, rec)
			if n := len(timingLines(t, buf)); n != 0 {
				t.Errorf("minimum 0 logged %d %q lines, want none: %s", n, timingMsg, buf.String())
			}
		})
	}
}

// An hour-long minimum makes a wrongly held 400 end at the 2 s request deadline with nothing written.
func TestRegister_FreeMailVariantsStayPromptUnderALargeMinimum(t *testing.T) {
	for _, email := range []string{"x@gmail.com", "X@GMAIL.COM", "x@gmail.com.", "x@mail.gmail.com", " x@outlook.com"} {
		t.Run(email, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(email, regPassword))).WithContext(ctx)

			start := time.Now()
			RegisterHandler(fake.URL, testClient(), time.Hour, freshRegisterLimit(), true, slog.New(slog.DiscardHandler), noPendingInvite).ServeHTTP(rec, req)
			elapsed := time.Since(start)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (after %v): %s", rec.Code, elapsed, rec.Body.String())
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

// One shared handler: each request waits from its own entry, not from a timer another request started.
func TestRegister_ConcurrentRequestsWaitIndependently(t *testing.T) {
	const floor = 300 * time.Millisecond
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log, buf := captureLog()
	h := RegisterHandler(fake.URL, testClient(), floor, freshRegisterLimit(), true, log, noPendingInvite)
	serve := func(rec *httptest.ResponseRecorder) time.Duration {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword)))
		start := time.Now()
		h.ServeHTTP(rec, req)
		return time.Since(start)
	}

	t.Run("simultaneous requests do not queue", func(t *testing.T) {
		const n = 6
		recs := make([]*httptest.ResponseRecorder, n)
		elapsed := make([]time.Duration, n)
		var wg sync.WaitGroup
		start := time.Now()
		for i := range n {
			recs[i] = httptest.NewRecorder()
			wg.Go(func() { elapsed[i] = serve(recs[i]) })
		}
		wg.Wait()
		wall := time.Since(start)

		for i := range n {
			requirePending202(t, recs[i])
			if elapsed[i] < floor {
				t.Errorf("request %d answered after %v, want no earlier than %v", i, elapsed[i], floor)
			}
		}
		if wall >= n*floor/2 {
			t.Errorf("%d requests took %v in all, want about one minimum (%v): the waits queued", n, wall, floor)
		}
	})

	t.Run("a late request waits its own minimum", func(t *testing.T) {
		first, second := httptest.NewRecorder(), httptest.NewRecorder()
		var wg sync.WaitGroup
		wg.Go(func() { serve(first) })
		time.Sleep(floor / 2)
		got := serve(second)
		wg.Wait()

		requirePending202(t, second)
		if got < floor {
			t.Errorf("late request answered after %v, want no earlier than %v from its own start", got, floor)
		}
	})

	if n := len(timingLines(t, buf)); n != 8 {
		t.Errorf("%d %q lines for 8 requests, want 8: %s", n, timingMsg, buf.String())
	}
}

// Every non-400 branch reaches the wait, including those a quirky upstream selects.
func TestRegister_OddUpstreamAnswersStillWaitTheMinimum(t *testing.T) {
	const floor = 200 * time.Millisecond
	huge := strings.Repeat("x", 1<<20)
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"200 non-JSON", http.StatusOK, "ok"},
		{"200 empty", http.StatusOK, ""},
		{"200 one MiB", http.StatusOK, `{"id":"` + huge + `"}`},
		{"302 redirect", http.StatusFound, ""},
		{"502 HTML from a proxy", http.StatusBadGateway, "<html>Bad Gateway</html>"},
		{"500 other SQLSTATE", http.StatusInternalServerError, gtOtherSQLState},
		{"400 non-JSON", http.StatusBadRequest, "<html>bad request</html>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			log, buf := captureLog()

			rec, elapsed := serveFloor(t, fake.URL, floor, log, registerBody(regEmail, regPassword))

			if rec.Code == http.StatusBadRequest {
				t.Fatalf("status 400 from an upstream that gave no validation code: %s", rec.Body.String())
			}
			if elapsed < floor {
				t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
			}
			if n := len(timingLines(t, buf)); n != 1 {
				t.Errorf("%d %q lines, want 1: %s", n, timingMsg, buf.String())
			}
		})
	}
}

// The wait runs from handler entry, so a client timeout shorter than the minimum is still padded up to it.
func TestRegister_UpstreamTimeoutWaitsOutTheRestOfTheMinimum(t *testing.T) {
	const floor, clientTimeout = 500 * time.Millisecond, 100 * time.Millisecond
	authURL := slowGoTrue(t, 2*time.Second, http.StatusOK, gtNewUser)
	client := &http.Client{Timeout: clientTimeout}
	log, buf := captureLog()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword)))

	start := time.Now()
	RegisterHandler(authURL, client, floor, freshRegisterLimit(), true, log, noPendingInvite).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
	}
	if elapsed >= time.Second {
		t.Errorf("answered after %v, want about %v", elapsed, floor)
	}
	lines := timingLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("%d %q lines, want 1: %s", len(lines), timingMsg, buf.String())
	}
	if up := intAttr(t, lines[0], "upstream_ms"); up < clientTimeout.Milliseconds() || up >= floor.Milliseconds() {
		t.Errorf("upstream_ms = %d, want the client timeout (%d) up to the minimum (%d)", up, clientTimeout.Milliseconds(), floor.Milliseconds())
	}
	if lines[0]["level"] != "INFO" {
		t.Errorf("level = %v, want INFO: the client error came before the minimum", lines[0]["level"])
	}
}

// A client gone while GoTrue is still working takes the unreachable path; it too writes nothing.
func TestRegister_ClientGoneDuringTheUpstreamCallWritesNothing(t *testing.T) {
	authURL := slowGoTrue(t, 800*time.Millisecond, http.StatusOK, gtNewUser)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))).WithContext(ctx)

	start := time.Now()
	RegisterHandler(authURL, testClient(), 3*time.Second, freshRegisterLimit(), true, slog.New(slog.DiscardHandler), noPendingInvite).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed >= time.Second {
		t.Errorf("returned after %v, want the handler to end when the client leaves", elapsed)
	}
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 || rec.Code != http.StatusOK {
		t.Errorf("wrote status %d, headers %v, body %q after the client left", rec.Code, rec.Header(), rec.Body.String())
	}
}

// The line's attributes are exactly these two, whatever GoTrue's body echoes back.
func TestRegister_TimingLineCarriesOnlyTheTwoTimings(t *testing.T) {
	echo := func(code string) string {
		return `{"code":0,"error_code":"` + code + `","msg":"for ` + regEmail + ` password ` + regPassword + `","email":"` + regEmail + `"}`
	}
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"200 echoing the address", http.StatusOK, gtNewUser},
		{"429 rate limit echoing both", http.StatusTooManyRequests, echo("over_email_send_rate_limit")},
		{"422 signup_disabled echoing both", http.StatusUnprocessableEntity, echo("signup_disabled")},
		{"500 unknown code echoing both", http.StatusInternalServerError, echo("unexpected_failure")},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			log, buf := captureLog()

			serveFloor(t, fake.URL, 50*time.Millisecond, log, registerBody(regEmail, regPassword))

			lines := timingLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("%d %q lines, want 1: %s", len(lines), timingMsg, buf.String())
			}
			var keys []string
			for k := range lines[0] {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if want := []string{"level", "min_ms", "msg", "time", "upstream_ms"}; !slices.Equal(keys, want) {
				t.Errorf("timing line keys = %v, want %v", keys, want)
			}
			for _, s := range []string{regEmail, regPassword} {
				if strings.Contains(buf.String(), s) {
					t.Errorf("log carries %q: %s", s, buf.String())
				}
			}
		})
	}
}

func TestHoldMinimum_Boundaries(t *testing.T) {
	const floor = 40 * time.Millisecond
	level := func(t *testing.T, upstream time.Duration) string {
		t.Helper()
		log, buf := captureLog()
		if !holdMinimum(t.Context(), log, timingMsg, time.Now().Add(-time.Hour), upstream, floor) {
			t.Fatal("holdMinimum = false for a live context")
		}
		lines := timingLines(t, buf)
		if len(lines) != 1 {
			t.Fatalf("%d lines, want 1: %s", len(lines), buf.String())
		}
		return lines[0]["level"].(string)
	}

	if got := level(t, floor-time.Nanosecond); got != "INFO" {
		t.Errorf("just below the minimum: level = %s, want INFO", got)
	}
	if got := level(t, floor); got != "WARN" {
		t.Errorf("exactly the minimum: level = %s, want WARN", got)
	}
	if got := level(t, floor+time.Nanosecond); got != "WARN" {
		t.Errorf("just above the minimum: level = %s, want WARN", got)
	}

	t.Run("a minimum already spent adds no wait", func(t *testing.T) {
		start := time.Now()
		holdMinimum(t.Context(), slog.New(slog.DiscardHandler), timingMsg, time.Now().Add(-time.Hour), time.Hour, floor)
		if elapsed := time.Since(start); elapsed >= floor {
			t.Errorf("waited %v with the minimum long past", elapsed)
		}
	})
	t.Run("a cancelled context reports false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if holdMinimum(ctx, slog.New(slog.DiscardHandler), timingMsg, time.Now(), 0, time.Hour) {
			t.Error("holdMinimum = true for a cancelled context")
		}
	})
	t.Run("zero and negative minimums neither log nor wait", func(t *testing.T) {
		for _, m := range []time.Duration{0, -time.Second} {
			log, buf := captureLog()
			start := time.Now()
			ok := holdMinimum(t.Context(), log, timingMsg, time.Now(), 0, m)
			if !ok || time.Since(start) >= floor || buf.Len() != 0 {
				t.Errorf("minimum %v: ok=%v after %v, log %q; want true at once with no line", m, ok, time.Since(start), buf.String())
			}
		}
	})
}

// serveFloorWith serves one register request through a handler whose lookup is p and times ServeHTTP.
func serveFloorWith(t *testing.T, authURL *url.URL, floor time.Duration, p *pendingProbe) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword)))
	start := time.Now()
	RegisterHandler(authURL, testClient(), floor, freshRegisterLimit(), true, slog.New(slog.DiscardHandler), p.lookup).ServeHTTP(rec, req)
	return rec, time.Since(start)
}

func TestRegister_InvitedAddressWaitsTheMinimum(t *testing.T) {
	const floor = 200 * time.Millisecond
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	p := probing(invited)

	rec, elapsed := serveFloorWith(t, fake.URL, floor, p)

	if n := len(p.calls()); n != 1 {
		t.Fatalf("lookup asked %d times, want 1", n)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Fatalf("GoTrue saw %d calls, want 0: the answer's timing would not be the invited path's", n)
	}
	requirePending202(t, rec)
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
	}
}

func TestRegister_LookupFailureWaitsTheMinimum(t *testing.T) {
	const floor = 200 * time.Millisecond
	p := probing(lookupErr)

	rec, elapsed := serveFloorWith(t, newFakeGoTrue(t, http.StatusOK, gtNewUser).URL, floor, p)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "registration is unavailable" {
		t.Errorf("error = %q, want %q", got, "registration is unavailable")
	}
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
	}
}
