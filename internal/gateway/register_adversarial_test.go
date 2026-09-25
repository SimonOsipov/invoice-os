package gateway

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// A literal, not maxRegisterBodyBytes: a test that reads the constant moves with it.
const registerCap = 4 << 10

func TestRegister_OversizedBody400NoUpstreamCall(t *testing.T) {
	// Positive pair: a body just under the cap reaches GoTrue.
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	under := registerBody(regEmail, strings.Repeat("p", registerCap-100))
	if len(under) >= registerCap {
		t.Fatalf("fixture is %d bytes, want under %d", len(under), registerCap)
	}
	requirePending202(t, doRegister(t, fake.URL, nil, under))
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("under the cap: GoTrue saw %d calls, want 1", n)
	}

	fake = newFakeGoTrue(t, http.StatusOK, gtNewUser)
	rec := doRegister(t, fake.URL, nil, registerBody(regEmail, strings.Repeat("p", registerCap+1)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over the cap: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("over the cap: GoTrue saw %d calls, want 0", n)
	}
}

// GoTrue behind a proxy can answer HTML, garbage or a huge body; none of it reaches the caller.
func TestRegister_GarbageOrHugeGoTrueBodies(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	for _, c := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantError  string // "" = the 202 body
	}{
		{"400 non-JSON", http.StatusBadRequest, "<html>bad request</html>", http.StatusBadGateway, "registration is unavailable"},
		{"502 HTML from a proxy", http.StatusBadGateway, "<html>Bad Gateway</html>", http.StatusBadGateway, "registration is unavailable"},
		{"400 one MiB of garbage", http.StatusBadRequest, huge, http.StatusBadGateway, "registration is unavailable"},
		{"429 non-JSON", http.StatusTooManyRequests, "slow down", http.StatusTooManyRequests, "too many requests"},
		{"200 one MiB body", http.StatusOK, `{"id":"` + huge + `"}`, http.StatusAccepted, ""},
		{"200 non-JSON", http.StatusOK, "ok", http.StatusAccepted, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)

			rec := doRegister(t, fake.URL, nil, registerBody(regEmail, regPassword))

			if c.wantError == "" {
				requirePending202(t, rec)
				return
			}
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d: %.200s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if got := errorBody(t, rec); got != c.wantError {
				t.Errorf("error = %q, want %q", got, c.wantError)
			}
		})
	}
}

// Only the named validation codes pass GoTrue's msg through; an unknown code's msg never reaches the caller.
func TestRegister_UnknownErrorCodeIs502WithoutMsg(t *testing.T) {
	const secret = "internal-detail-7Q"
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake := newFakeGoTrue(t, status, `{"code":`+strconv.Itoa(status)+`,"error_code":"bad_json","msg":"`+secret+`"}`)
			log, buf := captureLog()

			rec := doRegister(t, fake.URL, log, registerBody(regEmail, regPassword))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, rec); got != "registration is unavailable" {
				t.Errorf("error = %q, want %q", got, "registration is unavailable")
			}
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "bad_json") {
				t.Errorf("response carries GoTrue's error: %s", rec.Body.String())
			}
			if !strings.Contains(buf.String(), `"error_code":"bad_json"`) {
				t.Errorf("log does not name the unknown error_code: %s", buf.String())
			}
		})
	}
}

func TestRegister_GoTrueRedirectIs502(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusFound, "")

	rec := doRegister(t, fake.URL, nil, registerBody(regEmail, regPassword))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
}

func TestRegister_EveryAnswerIsJSON(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"202", http.StatusOK, gtNewUser},
		{"400", http.StatusBadRequest, gtValidationFailed},
		{"429", http.StatusTooManyRequests, gtOverRequestRateLimit},
		{"502", http.StatusInternalServerError, gtInternal},
		{"503", http.StatusUnprocessableEntity, gtSignupDisabled},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := doRegister(t, newFakeGoTrue(t, c.status, c.body).URL, nil, registerBody(regEmail, regPassword))
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Errorf("body is not JSON: %s", rec.Body.String())
			}
		})
	}
}

// The address and password never reach a log line on any branch.
func TestRegister_LogsNeverCarryCredentials(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int // 0 = unreachable
		body   string
	}{
		{"200", http.StatusOK, gtNewUser},
		{"429 over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit},
		{"400 email_address_invalid", http.StatusBadRequest, gtEmailAddressInvalid},
		{"500", http.StatusInternalServerError, gtInternal},
		{"unreachable", 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.status != 0 {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}
			log, buf := captureLog()

			doRegister(t, authURL, log, registerBody(regEmail, regPassword))

			if c.status != http.StatusOK && c.status != http.StatusBadRequest && buf.Len() == 0 {
				t.Fatal("no log line: the assertion below has nothing to read")
			}
			for _, s := range []string{regEmail, regPassword} {
				if strings.Contains(buf.String(), s) {
					t.Errorf("log carries %q: %s", s, buf.String())
				}
			}
		})
	}
}

// The token is decoded from the query once and JSON-encoded, never spliced into a string.
func TestVerify_TokenWithSpecialCharactersIsJSONEncoded(t *testing.T) {
	const token = `a"b\c&d=e+f/g%h}{,`
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)

	rec := doVerify(t, fake.URL, siteURL(t), nil, url.Values{"token": {token}, "type": {"signup"}}.Encode())

	if loc := rec.Header().Get("Location"); loc != siteURLValue+"/?verified=1" {
		t.Errorf("Location = %q, want %q", loc, siteURLValue+"/?verified=1")
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("verify body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"type": "signup", "token_hash": token}; !maps.Equal(sent, want) {
		t.Errorf("verify body = %v, want exactly %v", sent, want)
	}
}

// The first type value decides; GoTrue is only ever asked for type signup.
func TestVerify_DuplicateTypeAndRedirectTo(t *testing.T) {
	const evil = "redirect_to=" + "https%3A%2F%2Fevil.example"
	for _, c := range []struct {
		name, query, wantLoc string
		wantCalls            int
	}{
		{"signup first", "token=" + verifyToken + "&type=signup&type=recovery&" + evil, siteURLValue + "/?verified=1", 1},
		{"recovery first", "token=" + verifyToken + "&type=recovery&type=signup&" + evil, siteURLValue + "/?verify=failed", 0},
		{"two redirect_to", "token=" + verifyToken + "&type=signup&" + evil + "&" + evil, siteURLValue + "/?verified=1", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtSession)

			rec := doVerify(t, fake.URL, siteURL(t), nil, c.query)

			if loc := rec.Header().Get("Location"); loc != c.wantLoc {
				t.Errorf("Location = %q, want %q", loc, c.wantLoc)
			}
			calls := fake.Calls()
			if len(calls) != c.wantCalls {
				t.Fatalf("GoTrue saw %d calls, want %d", len(calls), c.wantCalls)
			}
			for _, call := range calls {
				var sent map[string]any
				if err := json.Unmarshal(call.Body, &sent); err != nil || sent["type"] != "signup" {
					t.Errorf("GoTrue got %s, want type signup", call.Body)
				}
			}
		})
	}
}

func TestVerify_GoTrueRedirectIsFailure(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusSeeOther, "")

	rec := doVerify(t, fake.URL, siteURL(t), nil, "token="+verifyToken+"&type=signup")

	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != siteURLValue+"/?verify=failed" {
		t.Errorf("= %d %q, want 303 %q", rec.Code, loc, siteURLValue+"/?verify=failed")
	}
}

// A site URL with a path keeps it; one trailing slash is not doubled.
func TestVerify_SiteURLPathAndTrailingSlash(t *testing.T) {
	for _, c := range []struct{ site, want string }{
		{"https://site.example/", "https://site.example/?verified=1"},
		{"https://site.example/app", "https://site.example/app/?verified=1"},
		{"https://site.example/app/", "https://site.example/app/?verified=1"},
	} {
		t.Run(c.site, func(t *testing.T) {
			site, err := url.Parse(c.site)
			if err != nil {
				t.Fatal(err)
			}
			rec := doVerify(t, newFakeGoTrue(t, http.StatusOK, gtSession).URL, site, nil, "token="+verifyToken+"&type=signup")
			if loc := rec.Header().Get("Location"); loc != c.want {
				t.Errorf("Location = %q, want %q", loc, c.want)
			}
		})
	}
}

// A GoTrue session body larger than the read cap still verifies and is never echoed.
func TestVerify_HugeSessionBodyIsDiscarded(t *testing.T) {
	body := `{"access_token":"` + sessionAT + `","pad":"` + strings.Repeat("x", 1<<20) + `"}`
	fake := newFakeGoTrue(t, http.StatusOK, body)
	log, buf := captureLog()

	rec := doVerify(t, fake.URL, siteURL(t), log, "token="+verifyToken+"&type=signup")

	if loc := rec.Header().Get("Location"); loc != siteURLValue+"/?verified=1" {
		t.Errorf("Location = %q, want %q", loc, siteURLValue+"/?verified=1")
	}
	for _, out := range []string{rec.Body.String(), buf.String(), rec.Header().Get("Location")} {
		if strings.Contains(out, sessionAT) {
			t.Errorf("session value escaped: %.200s", out)
		}
	}
}

// Only GET verifies; any other method, HEAD included, is refused before GoTrue sees the token.
func TestVerify_NonGetIs405WithoutUpstreamCall(t *testing.T) {
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtSession)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/auth/verify?token="+verifyToken+"&type=signup", nil)

			VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
				t.Errorf("%s = %d Allow %q, want 405 Allow GET", method, rec.Code, rec.Header().Get("Allow"))
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("%s reached GoTrue %d times, want 0", method, n)
			}
		})
	}
}

// A refusal is a JSON answer and writes no log line.
func TestRegister_FreeMailRefusalIsSilentJSON(t *testing.T) {
	// Positive pair: the captured logger records a line on another branch.
	log, buf := captureLog()
	doRegister(t, closedURL(t), log, registerBody(regEmail, regPassword))
	if buf.Len() == 0 {
		t.Fatal("no log line on the unreachable branch: the silence assertion below proves nothing")
	}

	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log, buf = captureLog()

	rec := doRegister(t, fake.URL, log, registerBody("user@gmail.com", regPassword))

	requireFreeMailRefused(t, fake, rec)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if buf.Len() != 0 {
		t.Errorf("refusal wrote a log line: %s", buf.String())
	}
}

// Kills trimming the address before it is forwarded.
func TestRegister_PaddedBusinessAddressForwardedVerbatim(t *testing.T) {
	const email = " user@corp.example "
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	requirePending202(t, doRegister(t, fake.URL, nil, registerBody(email, regPassword)))

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	var sent struct{ Email string }
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
	}
	if sent.Email != email {
		t.Errorf("forwarded email = %q, want %q", sent.Email, email)
	}
}
