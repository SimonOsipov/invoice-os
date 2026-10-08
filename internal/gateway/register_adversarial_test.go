package gateway

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	// The same cap holds when the padding rides in an answer field.
	answers := func(pad int) string {
		return registerBodyWithAnswers(regEmail, map[string]any{"workspace_name": strings.Repeat("a", pad), "display_name": "Ada"})
	}
	if len(answers(150)) >= registerCap || len(answers(registerCap)) <= registerCap {
		t.Fatalf("answer fixtures are %d and %d bytes, want under and over %d", len(answers(150)), len(answers(registerCap)), registerCap)
	}
	fake = newFakeGoTrue(t, http.StatusOK, gtNewUser)
	requirePending202(t, doRegister(t, fake.URL, nil, answers(150)))
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("answers under the cap: GoTrue saw %d calls, want 1", n)
	}
	fake = newFakeGoTrue(t, http.StatusOK, gtNewUser)
	rec = doRegister(t, fake.URL, nil, answers(registerCap))
	if rec.Code != http.StatusBadRequest || errorBody(t, rec) != "invalid request body" {
		t.Errorf("answers over the cap: %d %s, want 400 invalid request body", rec.Code, rec.Body.String())
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("answers over the cap: GoTrue saw %d calls, want 0", n)
	}
}

// Each case is the answer fields of a body; wantErr "" means 202 with wantData forwarded to GoTrue (nil: no data key).
func TestRegister_AnswerEdges(t *testing.T) {
	const (
		wsMsg   = "workspace_name must be 1 to 200 characters"
		dnMsg   = "display_name must be 1 to 200 characters"
		nulMsg  = "workspace_name must not contain a NUL byte"
		kindMsg = `kind must be "firm" or "in_house"`
		badBody = "invalid request body"
	)
	reg := func(w, d string, kind ...string) map[string]any {
		m := map[string]any{"workspace_name": w, "display_name": d}
		if len(kind) > 0 {
			m["kind"] = kind[0]
		}
		return m
	}
	wantReg := func(m map[string]any) map[string]any { return map[string]any{"registration": m} }
	acme := strings.Repeat("é", 200)
	cases := []struct {
		name     string
		fields   string
		wantErr  string
		wantData map[string]any
	}{
		{"number name", `"workspace_name":123,"display_name":"Ada"`, badBody, nil},
		{"bool name", `"workspace_name":true,"display_name":"Ada"`, badBody, nil},
		{"array name", `"workspace_name":["Acme"],"display_name":"Ada"`, badBody, nil},
		{"object display_name", `"workspace_name":"Acme","display_name":{"a":1}`, badBody, nil},
		{"number kind", `"workspace_name":"Acme","display_name":"Ada","kind":1`, badBody, nil},
		{"null name beside a display_name", `"workspace_name":null,"display_name":"Ada"`, wsMsg, nil},
		{"null display_name beside a name", `"workspace_name":"Acme","display_name":null`, dnMsg, nil},
		{"all three null is no answers", `"workspace_name":null,"display_name":null,"kind":null`, "", nil},
		{"null kind is an absent kind", `"workspace_name":"Acme","display_name":"Ada","kind":null`, "", wantReg(reg("Acme", "Ada"))},
		{"kind Firm", `"workspace_name":"Acme","display_name":"Ada","kind":"Firm"`, kindMsg, nil},
		{"kind FIRM", `"workspace_name":"Acme","display_name":"Ada","kind":"FIRM"`, kindMsg, nil},
		{"kind padded", `"workspace_name":"Acme","display_name":"Ada","kind":" firm"`, kindMsg, nil},
		{"kind in-house", `"workspace_name":"Acme","display_name":"Ada","kind":"in-house"`, kindMsg, nil},
		{"kind carrying NUL", `"workspace_name":"Acme","display_name":"Ada","kind":"firm\u0000"`, kindMsg, nil},
		{"201-rune display_name", `"workspace_name":"Acme","display_name":"` + strings.Repeat("a", 201) + `"`, dnMsg, nil},
		{"201 multi-byte runes", `"workspace_name":"` + strings.Repeat("é", 201) + `","display_name":"Ada"`, wsMsg, nil},
		{"NBSP-only name", `"workspace_name":"\u00a0\u2003","display_name":"Ada"`, wsMsg, nil},
		{"NUL-only name", `"workspace_name":"\u0000","display_name":"Ada"`, nulMsg, nil},
		{"NUL survives the trim", `"workspace_name":"  \u0000  ","display_name":"Ada"`, nulMsg, nil},
		{"both names bad: workspace first", `"workspace_name":"","display_name":""`, wsMsg, nil},
		{"unicode whitespace is trimmed", `"workspace_name":"\u00a0\u2003Acme\u3000","display_name":"\u0085Ada\u2009"`, "", wantReg(reg("Acme", "Ada"))},
		{"padding is not counted", `"workspace_name":"  ` + acme + `  ","display_name":"\t` + acme + `\n"`, "", wantReg(reg(acme, acme))},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			body := `{"email":"` + regEmail + `","password":"` + regPassword + `",` + c.fields + `}`

			rec := doRegister(t, fake.URL, nil, body)

			if c.wantErr != "" {
				if rec.Code != http.StatusBadRequest || errorBody(t, rec) != c.wantErr {
					t.Errorf("got %d %s, want 400 %q", rec.Code, rec.Body.String(), c.wantErr)
				}
				if n := len(fake.Calls()); n != 0 {
					t.Errorf("GoTrue saw %d calls, want 0", n)
				}
				return
			}
			requirePending202(t, rec)
			got := signupData(t, fake)
			if c.wantData == nil {
				if got != nil {
					t.Errorf("signup data = %v, want no data key", got)
				}
				return
			}
			if !reflect.DeepEqual(got, any(c.wantData)) {
				t.Errorf("signup data = %v, want %v", got, c.wantData)
			}
		})
	}
}

// The gateway rebuilds the signup body: nothing but email, password and the validated answers reaches GoTrue.
func TestRegister_ClientSuppliedDataNeverReachesGoTrue(t *testing.T) {
	const forged = `"data":{"registration":{"workspace_name":"Evil","display_name":"E","kind":"firm"},"role":"admin"},` +
		`"app_metadata":{"role":"admin"},"phone":"+15550100","email_confirm":true,"channel":"sms"`
	for _, c := range []struct {
		name     string
		extra    string
		wantData any
	}{
		{"no answers", forged, nil},
		{"with answers", forged + `,"workspace_name":"Acme","display_name":"Ada"`,
			map[string]any{"registration": map[string]any{"workspace_name": "Acme", "display_name": "Ada"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

			requirePending202(t, doRegister(t, fake.URL, nil, `{"email":"`+regEmail+`","password":"`+regPassword+`",`+c.extra+`}`))

			calls := fake.Calls()
			if len(calls) != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
			}
			var sent map[string]any
			if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
				t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
			}
			wantKeys := []string{"email", "password"}
			if c.wantData != nil {
				wantKeys = []string{"data", "email", "password"}
			}
			if keys := slices.Sorted(maps.Keys(sent)); !slices.Equal(keys, wantKeys) {
				t.Errorf("signup body keys = %v, want %v", keys, wantKeys)
			}
			if !reflect.DeepEqual(sent["data"], c.wantData) {
				t.Errorf("signup data = %v, want %v", sent["data"], c.wantData)
			}
		})
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

			// With a minimum the timing line joins the log; a 400 logs none.
			floorLog, floorBuf := captureLog()
			serveFloor(t, authURL, 50*time.Millisecond, floorLog, registerBody(regEmail, regPassword))
			if c.status != http.StatusBadRequest && len(timingLines(t, floorBuf)) != 1 {
				t.Fatalf("no %q line at a 50 ms minimum: %s", timingMsg, floorBuf.String())
			}
			for _, s := range []string{regEmail, regPassword} {
				if strings.Contains(floorBuf.String(), s) {
					t.Errorf("log at a 50 ms minimum carries %q: %s", s, floorBuf.String())
				}
			}
		})
	}
}

// The token is decoded from the form once and JSON-encoded, never spliced into a string.
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

// Only POST verifies; every other method is refused, uncacheable, before GoTrue sees the token.
func TestVerify_NonPostIs405WithoutUpstreamCall(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtSession)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/auth/verify?token="+verifyToken+"&type=signup", nil)

			VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), nil, testHandoffStore()).ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
				t.Errorf("%s = %d Allow %q, want 405 Allow POST", method, rec.Code, rec.Header().Get("Allow"))
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s Cache-Control = %q, want no-store", method, got)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("%s reached GoTrue %d times, want 0", method, n)
			}
		})
	}
}

// The token is read from the form body only; the URL query never counts.
func TestVerify_QueryTokenIsIgnored(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/verify?token="+verifyToken+"&type=signup", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), nil, testHandoffStore()).ServeHTTP(rec, req)

	requireRedirect(t, rec, failedLocation)
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}

const formType = "application/x-www-form-urlencoded"

func multipartVerifyBody(t *testing.T) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{"token": verifyToken, "type": "signup"} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.String()
}

// paddedForm is a valid verify form of exactly n bytes.
func paddedForm(n int) string {
	head := "token=" + verifyToken + "&type=signup&pad="
	return head + strings.Repeat("x", n-len(head))
}

// Anything but a small urlencoded form with a good token is a failure notice and never reaches GoTrue.
func TestVerify_BadFormIsFailureWithoutUpstreamCall(t *testing.T) {
	mpType, mpBody := multipartVerifyBody(t)
	const good = "token=" + verifyToken + "&type=signup"
	for _, c := range []struct {
		name, contentType, body string
		wantCalls               int
	}{
		{"control: a good form", formType, good, 1},
		{"JSON body", "application/json", `{"token":"` + verifyToken + `","type":"signup"}`, 0},
		{"multipart body", mpType, mpBody, 0},
		{"no Content-Type", "", good, 0},
		{"malformed percent-escape", formType, "token=%zz&type=signup", 0},
		// ParseForm fails but still yields a valid token: only the error check refuses it.
		{"bad escape after a valid token", formType, good + "&x=%zz", 0},
		{"first token empty, second valid", formType, "token=&token=" + verifyToken + "&type=signup", 0},
		{"control: charset parameter on the Content-Type", formType + "; charset=utf-8", good, 1},
		{"empty token", formType, "token=&type=signup", 0},
		{"wrong type", formType, "token=" + verifyToken + "&type=recovery", 0},
		// 256 is maxVerifyTokenBytes.
		{"token at the cap", formType, "token=" + strings.Repeat("a", 256) + "&type=signup", 1},
		{"token over the cap", formType, "token=" + strings.Repeat("a", 257) + "&type=signup", 0},
		// 1024 is the 1 KiB body cap.
		{"body at the cap", formType, paddedForm(1024), 1},
		{"body over the cap", formType, paddedForm(1025), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtSession)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth/verify", strings.NewReader(c.body))
			if c.contentType != "" {
				req.Header.Set("Content-Type", c.contentType)
			}

			VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), nil, testHandoffStore()).ServeHTTP(rec, req)

			want := failedLocation
			if c.wantCalls == 1 {
				want = verifiedLocation
			}
			requireRedirect(t, rec, want)
			if n := len(fake.Calls()); n != c.wantCalls {
				t.Errorf("GoTrue saw %d calls, want %d", n, c.wantCalls)
			}
		})
	}
}

// With no site URL the handler is the not-configured 503, and a good form never reaches GoTrue.
func TestVerify_NilSiteURLIs503WithoutUpstreamCall(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)
	rec := httptest.NewRecorder()

	VerifyHandler(fake.URL, nil, testClient(), slog.New(slog.DiscardHandler), nil, testHandoffStore()).ServeHTTP(rec, verifyRequest(t.Context(), verifyQuery))

	if rec.Code != http.StatusServiceUnavailable || errorBody(t, rec) != "registration is not configured" {
		t.Errorf("answer = %d %s, want 503 registration is not configured", rec.Code, rec.Body.String())
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}

// The form wins over the URL query: a different token in the query is never the one GoTrue sees.
func TestVerify_FormWinsOverQuery(t *testing.T) {
	for _, c := range []struct {
		name, target, body string
		wantToken          string // "" = no GoTrue call
	}{
		{"other token in the query", "/auth/verify?token=query-token&type=signup", "token=" + verifyToken + "&type=signup", verifyToken},
		{"query type recovery, form signup", "/auth/verify?token=query-token&type=recovery", "token=" + verifyToken + "&type=signup", verifyToken},
		{"query type signup, form recovery", "/auth/verify?token=query-token&type=signup", "token=" + verifyToken + "&type=recovery", ""},
		{"query token, form token empty", "/auth/verify?token=query-token&type=signup", "token=&type=signup", ""},
		{"token only in the query, type in the form", "/auth/verify?token=query-token", "type=signup", ""},
		{"type only in the query, token in the form", "/auth/verify?type=signup", "token=" + verifyToken, ""},
		{"first form token wins", "/auth/verify", "token=" + verifyToken + "&token=second-token&type=signup", verifyToken},
		{"first form type wins, signup", "/auth/verify", "token=" + verifyToken + "&type=signup&type=recovery", verifyToken},
		{"first form type wins, recovery", "/auth/verify", "token=" + verifyToken + "&type=recovery&type=signup", ""},
		// A state in the URL mints no code: verifiedLocation is the no-code answer.
		{"state only in the query", "/auth/verify?state=" + vhState, "token=" + verifyToken + "&type=signup", verifyToken},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtSession)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, c.target, strings.NewReader(c.body))
			req.Header.Set("Content-Type", formType)

			VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), nil, testHandoffStore()).ServeHTTP(rec, req)

			calls := fake.Calls()
			if c.wantToken == "" {
				requireRedirect(t, rec, failedLocation)
				if len(calls) != 0 {
					t.Fatalf("GoTrue saw %d calls, want 0", len(calls))
				}
				return
			}
			requireRedirect(t, rec, verifiedLocation)
			if len(calls) != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
			}
			var sent map[string]string
			if err := json.Unmarshal(calls[0].Body, &sent); err != nil || sent["token_hash"] != c.wantToken || sent["type"] != "signup" {
				t.Errorf("GoTrue got %s, want token_hash %q type signup", calls[0].Body, c.wantToken)
			}
		})
	}
}

// Every answer of a POST that reaches the handler, redirect or refusal, is uncacheable.
func TestVerify_EveryAnswerIsUncacheable(t *testing.T) {
	const good = "token=" + verifyToken + "&type=signup"
	for _, c := range []struct {
		name   string
		status int // GoTrue's; 0 = unreachable
		body   string
		form   string
		want   string
	}{
		{"verified", http.StatusOK, gtSession, good, verifiedLocation},
		{"refused by GoTrue", http.StatusForbidden, gtOTPExpired, good, failedLocation},
		{"GoTrue unreachable", 0, "", good, failedLocation},
		{"empty token", http.StatusOK, gtSession, "token=&type=signup", failedLocation},
		{"wrong type", http.StatusOK, gtSession, "token=" + verifyToken + "&type=recovery", failedLocation},
		{"malformed escape", http.StatusOK, gtSession, "token=%zz&type=signup", failedLocation},
		{"body over the cap", http.StatusOK, gtSession, paddedForm(1025), failedLocation},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.status != 0 {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}

			rec := doVerify(t, authURL, siteURL(t), nil, c.form)

			requireRedirect(t, rec, c.want)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// A refusal and an unreachable GoTrue each log one WARN; no log line carries the token.
func TestVerify_FailureLogsWarnWithoutTheToken(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int // 0 = unreachable
		body    string
		wantMsg string
	}{
		{"refused", http.StatusForbidden, gtOTPExpired, "verify: gotrue refused the link"},
		{"unreachable", 0, "", "verify: gotrue unreachable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.status != 0 {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}
			log, buf := captureLog()

			requireRedirect(t, doVerify(t, authURL, siteURL(t), log, verifyQuery), failedLocation)

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 || lines[0] == "" {
				t.Fatalf("log lines = %q, want exactly one", lines)
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatal(err)
			}
			if rec["level"] != "WARN" || rec["msg"] != c.wantMsg {
				t.Errorf("log = %v, want WARN %q", rec, c.wantMsg)
			}
			if strings.Contains(buf.String(), verifyToken) {
				t.Errorf("log carries the token: %s", buf.String())
			}
		})
	}
}

// Many clicks of one single-use token: the hand-offs equal the ?verified=1 answers, here exactly one.
func TestVerify_ConcurrentClicksHandOffOncePerVerifiedAnswer(t *testing.T) {
	const clicks = 8
	var spent atomic.Bool
	var verifyCalls atomic.Int32
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if spent.Swap(true) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(gtOTPExpired))
			return
		}
		_, _ = w.Write([]byte(coSession(coUser(coMetaFull))))
	}))
	t.Cleanup(gotrue.Close)
	authURL, err := url.Parse(gotrue.URL)
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecSink(nil)
	h := VerifyHandler(authURL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), sink, testHandoffStore())

	locations := make([]string, clicks)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range clicks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, verifyRequest(t.Context(), verifyQuery))
			if rec.Code != http.StatusSeeOther {
				t.Errorf("click %d = %d, want 303", i, rec.Code)
			}
			locations[i] = rec.Header().Get("Location")
		}()
	}
	close(start)
	wg.Wait()

	verified, failed := 0, 0
	for _, l := range locations {
		switch l {
		case verifiedLocation:
			verified++
		case failedLocation:
			failed++
		}
	}
	if verified != 1 || failed != clicks-1 {
		t.Fatalf("verified %d, failed %d of %d clicks, want 1 verified and the rest failed: %q", verified, failed, clicks, locations)
	}
	if n := verifyCalls.Load(); n != clicks {
		t.Errorf("GoTrue saw %d /verify calls, want %d", n, clicks)
	}
	requireOneHandOff(t, sink, coWant)
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.got()); n != 1 {
		t.Errorf("%d hand-offs after the clicks settled, want 1", n)
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

// The gateway trims only to classify; GoTrue receives the address as posted.
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
