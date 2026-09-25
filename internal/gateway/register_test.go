package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// GoTrue v2.197.0 fixtures. Errors use the initial-API-version HTTPError shape
// {"code","error_code","msg"} (internal/api/apierrors/apierrors.go, HTTPError).
const (
	// internal/api/signup.go, Signup: sendJSON(w, 200, user) with autoconfirm off.
	gtNewUser = `{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","aud":"authenticated","role":"authenticated","email":"new@corp.example","phone":"","confirmation_sent_at":"2026-09-24T10:00:00Z","app_metadata":{"provider":"email","providers":["email"]},"user_metadata":{},"identities":[{"identity_id":"0d9e8f7a-6b5c-4d3e-2f1a-0b9c8d7e6f5a","id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","user_id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","provider":"email"}],"created_at":"2026-09-24T10:00:00Z","updated_at":"2026-09-24T10:00:00Z","is_anonymous":false}`
	// internal/api/signup.go, UserExistsError branch: sendJSON(w, 200, sanitizeUser(...)) for a confirmed address.
	gtSanitizedUser = `{"id":"c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f","aud":"authenticated","role":"","email":"known@corp.example","phone":"","confirmation_sent_at":"2026-09-24T10:00:00Z","app_metadata":{"provider":"email","providers":["email"]},"user_metadata":{},"identities":[],"created_at":"2026-09-24T10:00:00Z","updated_at":"2026-09-24T10:00:00Z","is_anonymous":false}`
	// internal/api/mail.go, validateSentWithinFrequencyLimit; msg from internal/api/errors.go, generateFrequencyLimitErrorMessage.
	gtOverEmailSendRateLimit = `{"code":429,"error_code":"over_email_send_rate_limit","msg":"For security purposes, you can only request this after 42 seconds."}`
	// internal/api/signup.go, UserExistsError branch under Mailer.Autoconfirm.
	gtUserAlreadyExists = `{"code":422,"error_code":"user_already_exists","msg":"User already registered"}`
	// internal/api/mail.go (NewUnprocessableEntityError(ErrorCodeEmailExists, DuplicateEmailMsg)); msg from internal/api/errors.go.
	gtEmailExists = `{"code":422,"error_code":"email_exists","msg":"A user with this email address has already been registered"}`
	// internal/api/mail.go, validateEmail: format check failure.
	gtValidationFailed = `{"code":400,"error_code":"validation_failed","msg":"Unable to validate email address: invalid format"}`
	// internal/api/errors.go, handleResponseErrorWeakPassword (initial version); msg from internal/api/password.go.
	gtWeakPassword = `{"code":422,"error_code":"weak_password","msg":"Password should be at least 6 characters.","weak_password":{"reasons":["length"]}}`
	// internal/api/mail.go, sendEmail: validateclient.ErrInvalidEmailAddress branch.
	gtEmailAddressInvalid = `{"code":400,"error_code":"email_address_invalid","msg":"Email address \"new@corp.example\" is invalid"}`
	// internal/api/signup.go, Signup: config.DisableSignup.
	gtSignupDisabled = `{"code":422,"error_code":"signup_disabled","msg":"Signups not allowed for this instance"}`
	// internal/api/middleware.go, limitHandler.
	gtOverRequestRateLimit = `{"code":429,"error_code":"over_request_rate_limit","msg":"Request rate limit reached"}`
	// internal/api/apierrors/apierrors.go, NewInternalServerError; error_id set for 5xx in internal/api/errors.go.
	gtInternal = `{"code":500,"error_code":"unexpected_failure","msg":"Internal server error","error_id":"req-1"}`
	// Measured on v2.197.0: the loser of two concurrent signups for one address.
	gtDuplicateKey = `{"code":"23505","message":"duplicate key value violates unique constraint \"users_email_partial_key\""}`
	// A 5xx carrying some other SQLSTATE.
	gtOtherSQLState = `{"code":"40001","message":"could not serialize access due to concurrent update"}`
	// internal/api/verify.go, verifyTokenHash: expired or unknown email link.
	gtOTPExpired = `{"code":403,"error_code":"otp_expired","msg":"Email link is invalid or has expired"}`
)

const (
	regEmail     = "new@corp.example"
	regPassword  = "Corr3ct-Horse-Battery"
	verifyToken  = "verify-token-hash-value-q7"
	sessionAT    = "eyJhbGciOiJFUzI1NiJ9.access-token-value.sig"
	sessionRT    = "refresh-token-value-4q2x"
	siteURLValue = "https://site.example"
)

// internal/api/verify.go, Verify (POST): 200 with a session for a signup token_hash.
var gtSession = `{"access_token":"` + sessionAT + `","token_type":"bearer","expires_in":3600,"expires_at":1790000000,"refresh_token":"` + sessionRT + `","user":{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","email":"new@corp.example","email_confirmed_at":"2026-09-24T10:05:00Z"}}`

type gotrueCall struct {
	Method, Path string
	Body         []byte
}

// fakeGoTrue answers every request with status and body, recording each call.
type fakeGoTrue struct {
	URL   *url.URL
	mu    sync.Mutex
	calls []gotrueCall
}

func newFakeGoTrue(t *testing.T, status int, body string) *fakeGoTrue {
	t.Helper()
	f := &fakeGoTrue{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, gotrueCall{r.Method, r.URL.Path, b})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

func (f *fakeGoTrue) Calls() []gotrueCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// closedURL is an address nothing listens on.
func closedURL(t *testing.T) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	srv.Close()
	return u
}

func testClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func siteURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(siteURLValue)
	if err != nil {
		t.Fatalf("parse site url: %v", err)
	}
	return u
}

func registerBody(email, password string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(b)
}

func doRegister(t *testing.T, authURL *url.URL, log *slog.Logger, body string) *httptest.ResponseRecorder {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	RegisterHandler(authURL, testClient(), log).ServeHTTP(rec, req)
	return rec
}

func doVerify(t *testing.T, authURL, site *url.URL, log *slog.Logger, query string) *httptest.ResponseRecorder {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/verify?"+query, nil)
	VerifyHandler(authURL, site, testClient(), log).ServeHTTP(rec, req)
	return rec
}

// errorBody decodes an error response and requires the flat {"error": <string>} envelope.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if keys := slices.Sorted(maps.Keys(m)); !slices.Equal(keys, []string{"error"}) {
		t.Fatalf("error body keys = %v, want exactly [error]: %s", keys, rec.Body.String())
	}
	s, ok := m["error"].(string)
	if !ok {
		t.Fatalf("error is %T, want string", m["error"])
	}
	return s
}

// logHasValue reports whether any JSON log record has a top-level attribute equal to v.
func logHasValue(buf *bytes.Buffer, v int) bool {
	for line := range strings.Lines(buf.String()) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		for _, a := range rec {
			if a == float64(v) || a == strconv.Itoa(v) {
				return true
			}
		}
	}
	return false
}

func requirePending202(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if !maps.Equal(m, map[string]any{"status": "verification_pending"}) {
		t.Fatalf("body = %s, want exactly {\"status\":\"verification_pending\"}", rec.Body.String())
	}
}

func TestRegister_Pending202AndOnlySignupCalled(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	rec := doRegister(t, fake.URL, nil, registerBody(regEmail, regPassword))

	requirePending202(t, rec)
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want exactly 1: %+v", len(calls), calls)
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/signup" {
		t.Errorf("GoTrue saw %s %s, want POST /signup", calls[0].Method, calls[0].Path)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"email": regEmail, "password": regPassword}; !maps.Equal(sent, want) {
		t.Errorf("signup body = %v, want exactly %v", sent, want)
	}
}

// A repeat or confirmed address must be indistinguishable from a new one.
func TestRegister_ExistingAccountLooksIdentical(t *testing.T) {
	baseline := doRegister(t, newFakeGoTrue(t, http.StatusOK, gtNewUser).URL, nil, registerBody(regEmail, regPassword))
	requirePending202(t, baseline)

	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"confirmed address, sanitized 200", http.StatusOK, gtSanitizedUser},
		{"unconfirmed repeat within 60s, 429 over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit},
		{"autoconfirm repeat, 422 user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists},
		{"422 email_exists", http.StatusUnprocessableEntity, gtEmailExists},
		{"concurrent duplicate, 500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			log, buf := captureLog()

			rec := doRegister(t, fake.URL, log, registerBody(regEmail, regPassword))

			if len(fake.Calls()) != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", len(fake.Calls()))
			}
			if rec.Code != baseline.Code {
				t.Errorf("status = %d, want %d (the new-account status)", rec.Code, baseline.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), baseline.Body.Bytes()) {
				t.Errorf("body = %q, want byte-equal to the new-account body %q", rec.Body.String(), baseline.Body.String())
			}
			if got, want := rec.Header().Get("Content-Type"), baseline.Header().Get("Content-Type"); got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}
			if c.status == http.StatusTooManyRequests || c.status == http.StatusInternalServerError {
				if n := strings.Count(buf.String(), `"level":"WARN"`); n != 1 {
					t.Errorf("WARN lines = %d, want exactly 1: %s", n, buf.String())
				}
				if strings.Contains(buf.String(), regEmail) {
					t.Errorf("log names the address: %s", buf.String())
				}
			}
		})
	}
}

func TestRegister_GoTrueErrorMapping(t *testing.T) {
	for _, c := range []struct {
		name       string
		status     int // GoTrue's; 0 = unreachable
		body       string
		wantStatus int
		wantError  string // "" = not asserted beyond the envelope
	}{
		{"validation_failed", http.StatusBadRequest, gtValidationFailed, http.StatusBadRequest, "Unable to validate email address: invalid format"},
		{"weak_password", http.StatusUnprocessableEntity, gtWeakPassword, http.StatusBadRequest, "Password should be at least 6 characters."},
		{"email_address_invalid", http.StatusBadRequest, gtEmailAddressInvalid, http.StatusBadRequest, `Email address "new@corp.example" is invalid`},
		{"signup_disabled", http.StatusUnprocessableEntity, gtSignupDisabled, http.StatusServiceUnavailable, "registration is closed"},
		{"over_request_rate_limit", http.StatusTooManyRequests, gtOverRequestRateLimit, http.StatusTooManyRequests, ""},
		{"500", http.StatusInternalServerError, gtInternal, http.StatusBadGateway, ""},
		{"500 other SQLSTATE", http.StatusInternalServerError, gtOtherSQLState, http.StatusBadGateway, ""},
		{"422 SQLSTATE 23505", http.StatusUnprocessableEntity, gtDuplicateKey, http.StatusBadGateway, ""},
		{"closed port", 0, "", http.StatusBadGateway, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			var fake *fakeGoTrue
			if c.status != 0 {
				fake = newFakeGoTrue(t, c.status, c.body)
				authURL = fake.URL
			}

			rec := doRegister(t, authURL, nil, registerBody(regEmail, regPassword))

			if fake != nil && len(fake.Calls()) != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", len(fake.Calls()))
			}
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			got := errorBody(t, rec)
			if c.wantError != "" && got != c.wantError {
				t.Errorf("error = %q, want %q", got, c.wantError)
			}
		})
	}
}

func TestRegister_BadBody400NoUpstreamCall(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"malformed JSON", `{"email":`},
		{"empty email", registerBody("", regPassword)},
		{"empty password", registerBody(regEmail, "")},
		{"missing email", `{"password":"` + regPassword + `"}`},
		{"missing password", `{"email":"` + regEmail + `"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

			rec := doRegister(t, fake.URL, nil, c.body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			} else if msg := errorBody(t, rec); msg == "" {
				t.Error("400 carries an empty error message")
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

func TestRegister_NeverLeaksUserID(t *testing.T) {
	const userID = "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"
	if !strings.Contains(gtNewUser, userID) {
		t.Fatal("fixture lost the user id this test looks for")
	}
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	rec := doRegister(t, fake.URL, nil, registerBody(regEmail, regPassword))

	requirePending202(t, rec)
	for _, needle := range []string{userID, "authenticated", "confirmation_sent_at", "identities"} {
		if strings.Contains(rec.Body.String(), needle) {
			t.Errorf("response body carries GoTrue's %q: %s", needle, rec.Body.String())
		}
	}
}

// AUTH_URL may carry a path prefix; both calls join under it.
func TestRegister_JoinsUnderAnAuthURLPrefix(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)
	base := fake.URL.JoinPath("auth", "v1")

	doRegister(t, base, nil, registerBody(regEmail, regPassword))
	doVerify(t, base, siteURL(t), nil, "token="+verifyToken+"&type=signup")

	var got []string
	for _, c := range fake.Calls() {
		got = append(got, c.Method+" "+c.Path)
	}
	if want := []string{"POST /auth/v1/signup", "POST /auth/v1/verify"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

func TestVerify_Success303ToSite(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)

	rec := doVerify(t, fake.URL, siteURL(t), nil, "token="+verifyToken+"&type=signup")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != siteURLValue+"/?verified=1" {
		t.Errorf("Location = %q, want %q", loc, siteURLValue+"/?verified=1")
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/verify" {
		t.Fatalf("GoTrue saw %+v, want exactly one POST /verify", calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("verify body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"type": "signup", "token_hash": verifyToken}; !maps.Equal(sent, want) {
		t.Errorf("verify body = %v, want exactly %v", sent, want)
	}
}

func TestVerify_Failure303(t *testing.T) {
	const good = "token=" + verifyToken + "&type=signup"
	for _, c := range []struct {
		name      string
		status    int // GoTrue's; 0 = unreachable
		body      string
		query     string
		wantCalls int
	}{
		{"403 otp_expired", http.StatusForbidden, gtOTPExpired, good, 1},
		{"404", http.StatusNotFound, `{"code":404,"error_code":"not_found","msg":"Not found"}`, good, 1},
		{"500", http.StatusInternalServerError, gtInternal, good, 1},
		{"unreachable", 0, "", good, 0},
		{"missing token", http.StatusOK, gtSession, "type=signup", 0},
		{"empty token", http.StatusOK, gtSession, "token=&type=signup", 0},
		{"type=recovery", http.StatusOK, gtSession, "token=" + verifyToken + "&type=recovery", 0},
		{"missing type", http.StatusOK, gtSession, "token=" + verifyToken, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			var fake *fakeGoTrue
			if c.status != 0 {
				fake = newFakeGoTrue(t, c.status, c.body)
				authURL = fake.URL
			}

			rec := doVerify(t, authURL, siteURL(t), nil, c.query)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303: %s", rec.Code, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != siteURLValue+"/?verify=failed" {
				t.Errorf("Location = %q, want %q", loc, siteURLValue+"/?verify=failed")
			}
			if fake != nil && len(fake.Calls()) != c.wantCalls {
				t.Errorf("GoTrue saw %d calls, want %d", len(fake.Calls()), c.wantCalls)
			}
		})
	}
}

// No open redirect: the target is AUTH_SITE_URL, never a query value.
func TestVerify_IgnoresRedirectTo(t *testing.T) {
	const evil = "https://evil.example/steal"
	for _, c := range []struct {
		name, body, want string
		status           int
	}{
		{"verified", gtSession, siteURLValue + "/?verified=1", http.StatusOK},
		{"failed", gtOTPExpired, siteURLValue + "/?verify=failed", http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)

			rec := doVerify(t, fake.URL, siteURL(t), nil,
				"token="+verifyToken+"&type=signup&redirect_to="+url.QueryEscape(evil))

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			loc := rec.Header().Get("Location")
			if loc != c.want {
				t.Errorf("Location = %q, want %q", loc, c.want)
			}
			if strings.Contains(loc, "evil.example") {
				t.Errorf("Location follows redirect_to: %q", loc)
			}
		})
	}
}

func TestVerify_NoTokenInLocationOrLogs(t *testing.T) {
	secrets := []string{verifyToken, sessionAT, sessionRT}
	for _, c := range []struct {
		name, body, want string
		status           int
		wantLog          int // a failure logs the upstream status
	}{
		{"verified with a session body", gtSession, siteURLValue + "/?verified=1", http.StatusOK, 0},
		{"failed", gtOTPExpired, siteURLValue + "/?verify=failed", http.StatusForbidden, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			log, buf := captureLog()

			rec := doVerify(t, fake.URL, siteURL(t), log, "token="+verifyToken+"&type=signup")

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			loc := rec.Header().Get("Location")
			if loc != c.want {
				t.Errorf("Location = %q, want %q", loc, c.want)
			}
			if c.wantLog != 0 && !logHasValue(buf, c.wantLog) {
				t.Errorf("no log attribute carries the upstream status %d: %q", c.wantLog, buf.String())
			}
			for _, s := range secrets {
				if strings.Contains(loc, s) {
					t.Errorf("Location carries %q", s)
				}
				if strings.Contains(buf.String(), s) {
					t.Errorf("log carries %q: %s", s, buf.String())
				}
				if strings.Contains(rec.Body.String(), s) {
					t.Errorf("response body carries %q", s)
				}
			}
		})
	}
}

// RegisterHandler takes no site URL; the register half of AUTH_SITE_URL unset is
// pinned at the seam, TestRegistrationHandlers_NotConfigured503 in cmd/gateway.
func TestRegistration_NotConfigured503(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtSession)

	rec := doVerify(t, fake.URL, nil, nil, "token="+verifyToken+"&type=signup")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("verify status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "registration is not configured" {
		t.Errorf("verify error = %q, want %q", got, "registration is not configured")
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}

const freeMailRefusal = "a business email address is required; personal email providers are not accepted"

// requireFreeMailRefused requires the D6 refusal and that GoTrue was never called.
func requireFreeMailRefused(t *testing.T, fake *fakeGoTrue, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != freeMailRefusal {
		t.Errorf("error = %q, want %q", got, freeMailRefusal)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}

func TestRegister_FreeMailRefused400NoUpstreamCall(t *testing.T) {
	for _, c := range []struct{ name, email string }{
		{"gmail", "user@gmail.com"},
		{"upper", "USER@GMAIL.COM"},
		{"whitespace", " user@gmail.com "},
		{"plus_tag", "user+tag@gmail.com"},
		{"subdomain", "user@mail.gmail.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			requireFreeMailRefused(t, fake, doRegister(t, fake.URL, nil, registerBody(c.email, regPassword)))
		})
	}
}

func TestRegister_EveryListedDomainRefused(t *testing.T) {
	if len(freeMailDomains) < 20 {
		t.Fatalf("freeMailDomains has %d entries, want >= 20", len(freeMailDomains))
	}
	for _, d := range freeMailDomains {
		t.Run(d, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			requireFreeMailRefused(t, fake, doRegister(t, fake.URL, nil, registerBody("reg@"+d, regPassword)))
		})
	}
}

// Kills a suffix match without the dot boundary, and forwarding a normalised address.
func TestRegister_BusinessAndLookalikeDomainsReachGoTrue(t *testing.T) {
	// Mixed case: a lower-case input cannot tell forwarded from normalised.
	for _, email := range []string{"user@corp.example", "user@gmai1.com", "user@evilgmail.com", "User@Corp.Example"} {
		t.Run(email, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

			requirePending202(t, doRegister(t, fake.URL, nil, registerBody(email, regPassword)))

			calls := fake.Calls()
			if len(calls) != 1 || calls[0].Path != "/signup" {
				t.Fatalf("GoTrue saw %+v, want exactly one /signup call", calls)
			}
			var sent struct{ Email string }
			if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
				t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
			}
			if sent.Email != email {
				t.Errorf("forwarded email = %q, want %q byte for byte", sent.Email, email)
			}
		})
	}
}

// Kills the free-mail branch placed above the empty-field check.
func TestRegister_EmptyFieldCheckPrecedesFreeMail(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	rec := doRegister(t, fake.URL, nil, registerBody("user@gmail.com", ""))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "email and password are required" {
		t.Errorf("error = %q, want %q", got, "email and password are required")
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}
