package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Copied from internal/tenancy/accept.go (msgInviteNotValid); the gateway does not import tenancy.
const wantInviteNotValid = "this invite is no longer valid"

// From Design § API contracts: the 502 text of both gateway routes.
const wantLookupUnavailable = "invitation lookup is unavailable"

const (
	// 43 base64url characters, the shape tenancy mints; "Qz9" appears in no log or error text.
	inviteToken        = "Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Qz9Q"
	inviteGatewayToken = "gw-token-for-the-invitation-tests"
	inviteWorkspace    = "Obi Partners"
	inviteRole         = "reviewer"
	inviteAddress      = "tunde@obi.test"
)

var liveInvite = InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress}

// recordingPreviewer answers every token with one fixed result and records the tokens it saw.
type recordingPreviewer struct {
	mu     sync.Mutex
	seen   []string
	result func(token string) (InvitationPreview, error)
}

func previewing(p InvitationPreview, err error) *recordingPreviewer {
	return &recordingPreviewer{result: func(string) (InvitationPreview, error) { return p, err }}
}

func (r *recordingPreviewer) preview(_ context.Context, token string) (InvitationPreview, error) {
	r.mu.Lock()
	r.seen = append(r.seen, token)
	r.mu.Unlock()
	return r.result(token)
}

func (r *recordingPreviewer) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func tokenBody(token string) string {
	b, _ := json.Marshal(map[string]string{"token": token})
	return string(b)
}

func serveInvitation(h http.Handler, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/auth/invitation", strings.NewReader(body)))
	return rec
}

func requireStringMap(t *testing.T, rec *httptest.ResponseRecorder, want map[string]string) {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q is not a JSON object of strings: %v", rec.Body.String(), err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("body = %s, want exactly %v", rec.Body.String(), want)
	}
}

func TestInvitationHandler_Contract(t *testing.T) {
	over1KiB := strings.Repeat(" ", 1025-len(tokenBody(inviteToken))) + tokenBody(inviteToken)
	exactly1KiB := strings.Repeat(" ", 1024-len(tokenBody(inviteToken))) + tokenBody(inviteToken)
	if len(over1KiB) != 1025 || len(exactly1KiB) != 1024 {
		t.Fatalf("fixtures are %d and %d bytes, want 1025 and 1024", len(over1KiB), len(exactly1KiB))
	}
	live := map[string]string{"workspace": inviteWorkspace, "role": inviteRole, "email": inviteAddress}
	cases := []struct {
		name, method, body string
		result             func(string) (InvitationPreview, error)
		status             int
		want               map[string]string
		previewerCalls     int
		warn               bool
	}{
		{"live invite", "POST", tokenBody(inviteToken), func(string) (InvitationPreview, error) { return liveInvite, nil }, 200, live, 1, false},
		{"body of exactly 1 KiB", "POST", exactly1KiB, func(string) (InvitationPreview, error) { return liveInvite, nil }, 200, live, 1, false},
		{"not valid", "POST", tokenBody(inviteToken), func(string) (InvitationPreview, error) { return InvitationPreview{}, ErrInvitationNotValid }, 404, map[string]string{"error": wantInviteNotValid}, 1, false},
		{"previewer error", "POST", tokenBody(inviteToken), func(string) (InvitationPreview, error) {
			return InvitationPreview{}, errors.New("tenancy answered 500")
		}, 502, map[string]string{"error": wantLookupUnavailable}, 1, true},
		{"malformed JSON", "POST", `{`, nil, 400, map[string]string{"error": msgInvalidBody}, 0, false},
		{"over 1 KiB", "POST", over1KiB, nil, 400, map[string]string{"error": msgInvalidBody}, 0, false},
		{"GET", "GET", "", nil, 405, map[string]string{"error": "method not allowed"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &recordingPreviewer{result: c.result}
			if c.result == nil {
				p.result = func(string) (InvitationPreview, error) { return liveInvite, nil }
			}
			log, buf := captureLog()

			rec := serveInvitation(InvitationHandler(p.preview, log), c.method, c.body)

			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body.String())
			}
			requireStringMap(t, rec, c.want)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if c.method == "GET" {
				if got := rec.Header().Get("Allow"); got != "POST" {
					t.Errorf("Allow = %q, want POST", got)
				}
			}
			if got := len(p.calls()); got != c.previewerCalls {
				t.Errorf("previewer calls = %d, want %d", got, c.previewerCalls)
			}
			if c.warn {
				if !strings.Contains(buf.String(), `"level":"WARN"`) {
					t.Errorf("no WARN line on a previewer error: %q", buf.String())
				}
			}
			if strings.Contains(buf.String(), inviteToken) {
				t.Errorf("a log line carries the token: %s", buf.String())
			}
		})
	}
}

func TestInvitationHandler_TokenLengthCap(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		calls      int
	}{
		{"empty token", tokenBody(""), 404, 0},
		{"no token key", `{}`, 404, 0},
		{"43-char token", tokenBody(inviteToken), 200, 1},
		{"256 bytes, not a token", tokenBody(strings.Repeat("a", maxVerifyTokenBytes)), 404, 0},
		{"257 bytes", tokenBody(strings.Repeat("a", maxVerifyTokenBytes+1)), 404, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := previewing(liveInvite, nil)
			log, _ := captureLog()

			rec := serveInvitation(InvitationHandler(p.preview, log), "POST", c.body)

			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body.String())
			}
			if c.status == 404 {
				requireStringMap(t, rec, map[string]string{"error": wantInviteNotValid})
			}
			if got := len(p.calls()); got != c.calls {
				t.Errorf("previewer calls = %d, want %d", got, c.calls)
			}
		})
	}
}

// tenancyStub records each request and answers status and body.
type tenancyStub struct {
	URL      *url.URL
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	location string // sent as Location when set
}

func newTenancyStub(t *testing.T, status int, body string) *tenancyStub {
	t.Helper()
	s := &tenancyStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, string(b))
		location := s.location
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub url: %v", err)
	}
	s.URL = u
	return s
}

func (s *tenancyStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func TestHTTPInvitationPreviewer_Wire(t *testing.T) {
	// Copied from internal/tenancy/accept.go's 200 body: {workspace, role, email}.
	const liveBody = `{"workspace":"` + inviteWorkspace + `","role":"` + inviteRole + `","email":"` + inviteAddress + `"}`

	t.Run("200 names the invite", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusOK, liveBody)

		got, err := NewHTTPInvitationPreviewer(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteToken)

		if err != nil || got != liveInvite {
			t.Fatalf("preview = (%+v, %v), want (%+v, nil)", got, err, liveInvite)
		}
		if stub.count() != 1 {
			t.Fatalf("tenancy saw %d requests, want 1", stub.count())
		}
		r := stub.requests[0]
		if r.Method != http.MethodPost || r.URL.Path != "/internal/invitations/preview" {
			t.Errorf("request = %s %s, want POST /internal/invitations/preview", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("X-Gateway-Token"); got != inviteGatewayToken {
			t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
		}
		for _, h := range []string{"X-User-ID", "X-User-Email", "X-User-Role", "X-Tenant-ID"} {
			if v := r.Header.Values(h); len(v) != 0 {
				t.Errorf("%s = %q, want no identity header on the preview", h, v)
			}
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil || !maps.Equal(sent, map[string]string{"token": inviteToken}) {
			t.Errorf("body = %s (decode err %v), want exactly {\"token\":<token>}", stub.bodies[0], err)
		}
	})

	t.Run("404 is not valid", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusNotFound, `{"error":"`+wantInviteNotValid+`"}`)

		_, err := NewHTTPInvitationPreviewer(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteToken)

		if !errors.Is(err, ErrInvitationNotValid) {
			t.Fatalf("err = %v, want ErrInvitationNotValid", err)
		}
		if stub.count() != 1 {
			t.Errorf("tenancy saw %d requests, want 1", stub.count())
		}
	})
}

func TestHTTPInvitationPreviewer_ErrorsCarryNoSecret(t *testing.T) {
	second := newTenancyStub(t, http.StatusOK, `{}`)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, second.URL.String()+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)
	redirectURL, err := url.Parse(redirecting.URL)
	if err != nil {
		t.Fatalf("parse redirecting url: %v", err)
	}

	cases := []struct {
		name       string
		base       *url.URL
		wantStatus string
	}{
		{"500", newTenancyStub(t, http.StatusInternalServerError, `{"error":"boom"}`).URL, "500"},
		{"307 is not followed", redirectURL, "307"},
		{"unreachable", closedURL(t), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A default client follows a 307: the previewer itself must refuse to.
			_, err := NewHTTPInvitationPreviewer(c.base, &http.Client{}, inviteGatewayToken)(t.Context(), inviteToken)

			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			if errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("err = %v, want it distinct from ErrInvitationNotValid", err)
			}
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				t.Errorf("err wraps a *url.Error, which carries the URL: %v", err)
			}
			for _, secret := range []string{inviteToken, inviteGatewayToken, c.base.Host, c.base.String(), "/internal/invitations/preview"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error text %q holds %q", err.Error(), secret)
				}
			}
			if c.wantStatus != "" && !strings.Contains(err.Error(), c.wantStatus) {
				t.Errorf("error text %q does not name the status %s", err.Error(), c.wantStatus)
			}
		})
	}
	if n := second.count(); n != 0 {
		t.Errorf("the redirect target saw %d request(s), want 0", n)
	}
}

func TestHTTPInvitationPreviewer_GivesUpAfterTheDeadline(t *testing.T) {
	old := invitationPreviewTimeout
	invitationPreviewTimeout = 150 * time.Millisecond
	t.Cleanup(func() { invitationPreviewTimeout = old })

	release := make(chan struct{})
	var started atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	// No client timeout: only the previewer's own bound can end the call.
	preview := NewHTTPInvitationPreviewer(base, &http.Client{}, inviteGatewayToken)

	// The outer bound only makes a missing previewer deadline fail fast instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err = preview(ctx, inviteToken)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("err = nil, want a deadline error")
	}
	if started.Load() != 1 {
		t.Fatalf("the stub saw %d requests, want 1: the call never reached it, so the deadline proves nothing", started.Load())
	}
	if elapsed > 3*time.Second {
		t.Errorf("gave up after %v, want near the %v bound", elapsed, invitationPreviewTimeout)
	}
	if strings.Contains(err.Error(), inviteToken) || strings.Contains(err.Error(), base.Host) {
		t.Errorf("error text %q holds the token or the URL", err.Error())
	}

	log, _ := captureLog()
	start = time.Now()
	rec := httptest.NewRecorder()
	InvitationHandler(preview, log).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/invitation", strings.NewReader(tokenBody(inviteToken))))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("handler status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	requireStringMap(t, rec, map[string]string{"error": wantLookupUnavailable})
	if time.Since(start) > 3*time.Second {
		t.Errorf("handler answered after %v, want near the bound", time.Since(start))
	}
}

const inviteeEmailFromBody = "evil@x.test"

func inviteeBody(token, password string, extra map[string]string) string {
	m := map[string]string{"token": token, "password": password}
	maps.Copy(m, extra)
	b, _ := json.Marshal(m)
	return string(b)
}

func postInvitee(h http.Handler, body string) (*httptest.ResponseRecorder, time.Duration) {
	req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	return rec, time.Since(start)
}

func inviteeHandler(authURL *url.URL, floor time.Duration, perIP *SignInThrottle, p *recordingPreviewer) http.Handler {
	log, _ := captureLog()
	return InvitationRegisterHandler(authURL, testClient(), floor, perIP, true, log, p.preview)
}

func TestInvitationRegister_SignsUpTheInvitedAddress(t *testing.T) {
	const floor = 200 * time.Millisecond
	p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: "ada@gmail.com"}, nil)
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)

	rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, freshRegisterLimit(), p),
		inviteeBody(inviteToken, "pw-123456", map[string]string{"email": inviteeEmailFromBody}))

	requirePending202(t, rec)
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than %v", elapsed, floor)
	}
	if got := p.calls(); len(got) != 1 || got[0] != inviteToken {
		t.Errorf("previewer saw %v, want exactly the posted token", got)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Path != "/signup" {
		t.Fatalf("GoTrue saw %+v, want exactly one /signup call", calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"email": "ada@gmail.com", "password": "pw-123456"}; !maps.Equal(sent, want) {
		t.Errorf("signup body = %s, want exactly %v (the invited address, no data key)", calls[0].Body, want)
	}
}

func TestInvitationRegister_MapsGoTrueLikeRegister(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		unreachable bool
		want        int
	}{
		{"200 new account", http.StatusOK, gtNewUser, false, 202},
		{"200 confirmed address", http.StatusOK, gtSanitizedUser, false, 202},
		{"user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists, false, 202},
		{"email_exists", http.StatusUnprocessableEntity, gtEmailExists, false, 202},
		{"over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit, false, 202},
		{"500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey, false, 202},
		{"weak_password", http.StatusUnprocessableEntity, gtWeakPassword, false, 400},
		{"validation_failed", http.StatusBadRequest, gtValidationFailed, false, 400},
		{"email_address_invalid", http.StatusBadRequest, gtEmailAddressInvalid, false, 400},
		{"signup_disabled", http.StatusUnprocessableEntity, gtSignupDisabled, false, 503},
		{"over_request_rate_limit", http.StatusTooManyRequests, gtOverRequestRateLimit, false, 429},
		{"500 unexpected_failure", http.StatusInternalServerError, gtInternal, false, 502},
		{"502 from GoTrue", http.StatusBadGateway, ``, false, 502},
		{"unreachable", 0, ``, true, 502},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if !c.unreachable {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}
			log, _ := captureLog()
			reg := httptest.NewRecorder()
			regReq := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword)))
			RegisterHandler(authURL, testClient(), 0, freshRegisterLimit(), true, log, noPendingInvite).ServeHTTP(reg, regReq)

			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: regEmail}, nil)
			inv, _ := postInvitee(inviteeHandler(authURL, 0, freshRegisterLimit(), p), inviteeBody(inviteToken, regPassword, nil))

			if reg.Code != c.want {
				t.Fatalf("control: /auth/register = %d, want %d: %s", reg.Code, c.want, reg.Body.String())
			}
			if inv.Code != reg.Code || inv.Body.String() != reg.Body.String() {
				t.Errorf("invitee = %d %s, want what /auth/register gives: %d %s", inv.Code, inv.Body.String(), reg.Code, reg.Body.String())
			}
			if got, want := inv.Header().Get("Content-Type"), reg.Header().Get("Content-Type"); got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}
		})
	}
}

func TestInvitationRegister_RefusesBeforeGoTrue(t *testing.T) {
	const floor = 2 * time.Second
	cases := []struct {
		name   string
		body   string
		result error
		status int
		want   string // "" accepts any non-empty error message
	}{
		{"token names no usable invite", inviteeBody(inviteToken, "pw-123456", nil), ErrInvitationNotValid, 404, wantInviteNotValid},
		{"previewer error", inviteeBody(inviteToken, "pw-123456", nil), errors.New("tenancy answered 500"), 502, wantLookupUnavailable},
		{"empty token", inviteeBody("", "pw-123456", nil), nil, 400, ""},
		{"empty password", inviteeBody(inviteToken, "", nil), nil, 400, ""},
		{"malformed body", `{`, nil, 400, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			p := previewing(liveInvite, c.result)
			log, buf := captureLog()
			h := InvitationRegisterHandler(fake.URL, testClient(), floor, freshRegisterLimit(), true, log, p.preview)

			rec, elapsed := postInvitee(h, c.body)

			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body.String())
			}
			if got := errorBody(t, rec); got == "" || (c.want != "" && got != c.want) {
				t.Errorf("error = %q, want %q", got, c.want)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
			if elapsed > 100*time.Millisecond {
				t.Errorf("answered after %v, want at once (floor %v)", elapsed, floor)
			}
			if strings.Contains(buf.String(), inviteToken) || strings.Contains(buf.String(), "pw-123456") {
				t.Errorf("a log line carries the token or the password: %s", buf.String())
			}
			if c.status == 502 && buf.Len() == 0 {
				t.Error("no log line on a previewer error: the secret check above proves nothing")
			}
		})
	}
}

func TestInvitationRegister_SharesTheRegisterBudget(t *testing.T) {
	const floor = 150 * time.Millisecond
	perIP := NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log, _ := captureLog()
	reg := httptest.NewRecorder()
	RegisterHandler(fake.URL, testClient(), 0, perIP, true, log, noPendingInvite).ServeHTTP(reg,
		httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))))
	requirePending202(t, reg)
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("control: /auth/register made %d GoTrue calls, want 1", n)
	}

	p := previewing(liveInvite, nil)
	rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, perIP, p), inviteeBody(inviteToken, "pw-123456", nil))

	requirePending202(t, rec)
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want only the /auth/register signup", n)
	}
}

// The free-mail refusal belongs to /auth/register; an invite names an address an admin chose.
func TestInvitationRegister_FreeMailRule(t *testing.T) {
	const (
		freeMail     = "ada@gmail.com"
		expiredToken = "Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9E"
		spentToken   = "Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9S"
	)
	p := &recordingPreviewer{result: func(token string) (InvitationPreview, error) {
		if token == inviteToken {
			return InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: freeMail}, nil
		}
		return InvitationPreview{}, ErrInvitationNotValid
	}}

	t.Run("a live invite to a free-mail address is signed up", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), p), inviteeBody(inviteToken, "pw-123456", nil))
		requirePending202(t, rec)
		calls := fake.Calls()
		if len(calls) != 1 || calls[0].Path != "/signup" || !strings.Contains(string(calls[0].Body), `"email":"`+freeMail+`"`) {
			t.Fatalf("GoTrue saw %+v, want one /signup for %s", calls, freeMail)
		}
	})

	t.Run("/auth/register still refuses the same address", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		rec := doRegister(t, fake.URL, nil, registerBody(freeMail, "pw-123456"))
		requireFreeMailRefused(t, fake, rec)
	})

	for _, token := range []string{expiredToken, spentToken} {
		t.Run("an expired or spent invite answers 404 without GoTrue: "+token[:2], func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), p), inviteeBody(token, "pw-123456", nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, rec); got != wantInviteNotValid {
				t.Errorf("error = %q, want %q", got, wantInviteNotValid)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
	if seen := p.calls(); len(seen) != 3 {
		t.Errorf("previewer saw %v, want one call for each of the three invitee posts", seen)
	}
}

// No outcome of the signup writes the token or the password to a log line.
func TestInvitationRegister_LogsCarryNoTokenOrPassword(t *testing.T) {
	const password = "Zq-pw-7788-marker"
	cases := []struct {
		name        string
		status      int
		body        string
		unreachable bool
		spent       bool
		wantLogged  bool // the case must log something, or the secret check proves nothing
	}{
		{"unreachable GoTrue", 0, ``, true, false, true},
		{"500 unexpected_failure", http.StatusInternalServerError, gtInternal, false, false, true},
		{"over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit, false, false, true},
		{"500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey, false, false, true},
		{"budget spent", http.StatusOK, gtNewUser, false, true, true},
		{"200 new account", http.StatusOK, gtNewUser, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if !c.unreachable {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}
			perIP := freshRegisterLimit()
			if c.spent {
				perIP = NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
				perIP.Reserve("192.0.2.1")
			}
			log, buf := captureLog()
			h := InvitationRegisterHandler(authURL, testClient(), 0, perIP, true, log, previewing(liveInvite, nil).preview)
			req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(inviteeBody(inviteToken, password, nil)))
			req.RemoteAddr = "192.0.2.1:5555"
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if c.wantLogged && buf.Len() == 0 {
				t.Fatal("no log line: the secret check below proves nothing")
			}
			for _, secret := range []string{inviteToken, password} {
				if strings.Contains(buf.String(), secret) || strings.Contains(rec.Body.String(), secret) {
					t.Errorf("a log line or the answer carries %q: %s %s", secret, buf.String(), rec.Body.String())
				}
			}
		})
	}
}

func TestInvitation_MalformedTokenMakesNoTenancyCall(t *testing.T) {
	malformed := map[string]string{
		"short":        "abc",
		"44 chars":     inviteToken + "A",
		"42 chars":     inviteToken[:42],
		"bad alphabet": inviteToken[:42] + "+",
		"padded":       inviteToken[:42] + "=",
		"newline":      inviteToken + "\n",
		"non-ASCII":    inviteToken[:42] + "é",
	}
	t.Run("control: a well-formed token reaches the previewer", func(t *testing.T) {
		p := previewing(liveInvite, nil)
		log, _ := captureLog()

		rec := serveInvitation(InvitationHandler(p.preview, log), "POST", tokenBody(inviteToken))

		if n := len(p.calls()); rec.Code != 200 || n != 1 {
			t.Fatalf("status = %d with %d previewer call(s), want 200 and 1: %s", rec.Code, n, rec.Body.String())
		}
	})
	for name, token := range malformed {
		t.Run("preview "+name, func(t *testing.T) {
			p := previewing(liveInvite, nil)
			log, _ := captureLog()

			rec := serveInvitation(InvitationHandler(p.preview, log), "POST", tokenBody(token))

			if rec.Code != 404 {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			requireStringMap(t, rec, map[string]string{"error": wantInviteNotValid})
			if n := len(p.calls()); n != 0 {
				t.Errorf("previewer calls = %d, want 0", n)
			}
		})
		t.Run("register "+name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			p := previewing(liveInvite, nil)
			log, _ := captureLog()
			h := InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, p.preview)

			rec, _ := postInvitee(h, inviteeBody(token, "pw-123456", nil))

			if rec.Code != 404 {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, rec); got != wantInviteNotValid {
				t.Errorf("error = %q, want %q", got, wantInviteNotValid)
			}
			if n := len(p.calls()); n != 0 {
				t.Errorf("previewer calls = %d, want 0", n)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

func TestHTTPPendingInviteLookup_Wire(t *testing.T) {
	asks := func(t *testing.T, stub *tenancyStub) (bool, error) {
		t.Helper()
		// A default client follows redirects: the lookup itself must refuse to.
		return NewHTTPPendingInviteLookup(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteAddress)
	}
	requireOneAsk := func(t *testing.T, stub *tenancyStub) {
		t.Helper()
		if stub.count() != 1 {
			t.Fatalf("tenancy saw %d requests, want 1", stub.count())
		}
		r := stub.requests[0]
		if r.Method != http.MethodPost || r.URL.Path != "/internal/invitations/pending" {
			t.Errorf("request = %s %s, want POST /internal/invitations/pending", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("X-Gateway-Token"); got != inviteGatewayToken {
			t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil || !maps.Equal(sent, map[string]string{"email": inviteAddress}) {
			t.Errorf("body = %s (decode err %v), want exactly {\"email\":<address>}", stub.bodies[0], err)
		}
	}

	t.Run("pending true", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusOK, `{"pending":true}`)
		got, err := asks(t, stub)
		requireOneAsk(t, stub)
		if err != nil || !got {
			t.Errorf("lookup = (%v, %v), want (true, nil)", got, err)
		}
	})
	t.Run("pending false", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusOK, `{"pending":false}`)
		got, err := asks(t, stub)
		requireOneAsk(t, stub)
		if err != nil || got {
			t.Errorf("lookup = (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("302 is an error and is not followed", func(t *testing.T) {
		second := newTenancyStub(t, http.StatusOK, `{"pending":false}`)
		first := newTenancyStub(t, http.StatusFound, ``)
		first.mu.Lock()
		first.location = second.URL.String() + "/elsewhere"
		first.mu.Unlock()
		got, err := asks(t, first)
		requireOneAsk(t, first)
		if err == nil || got {
			t.Errorf("lookup = (%v, %v), want (false, an error)", got, err)
		}
		if n := second.count(); n != 0 {
			t.Errorf("the redirect target saw %d request(s), want 0", n)
		}
	})
}

func TestHTTPPendingInviteLookup_ErrorsCarryNoSecret(t *testing.T) {
	cases := []struct {
		name       string
		base       *url.URL
		wantStatus string
	}{
		{"500", newTenancyStub(t, http.StatusInternalServerError, `{"error":"boom"}`).URL, "500"},
		{"404: a tenancy without the route is not 'no invite'", newTenancyStub(t, http.StatusNotFound, `404 page not found`).URL, "404"},
		{"404 with the pending body", newTenancyStub(t, http.StatusNotFound, `{"pending":false}`).URL, "404"},
		{"400", newTenancyStub(t, http.StatusBadRequest, `{"error":"invalid request body"}`).URL, "400"},
		{"401", newTenancyStub(t, http.StatusUnauthorized, `{"pending":false}`).URL, "401"},
		{"403", newTenancyStub(t, http.StatusForbidden, `{"pending":false}`).URL, "403"},
		{"202 with the pending body", newTenancyStub(t, http.StatusAccepted, `{"pending":false}`).URL, "202"},
		{"204", newTenancyStub(t, http.StatusNoContent, ``).URL, "204"},
		{"unreachable", closedURL(t), ""},
		{"200 with an unreadable body", newTenancyStub(t, http.StatusOK, `<html>not json`).URL, ""},
		{"200 with an empty body", newTenancyStub(t, http.StatusOK, ``).URL, ""},
		{"200 with a truncated body", newTenancyStub(t, http.StatusOK, `{"pending":tr`).URL, ""},
		{"200 with a non-boolean verdict", newTenancyStub(t, http.StatusOK, `{"pending":"yes"}`).URL, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewHTTPPendingInviteLookup(c.base, &http.Client{}, inviteGatewayToken)(t.Context(), inviteAddress)

			if err == nil {
				t.Fatalf("lookup = (%v, nil), want an error", got)
			}
			if got {
				t.Errorf("lookup = (true, %v), want false with an error", err)
			}
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				t.Errorf("err wraps a *url.Error, which carries the URL: %v", err)
			}
			for _, secret := range []string{inviteAddress, inviteGatewayToken, c.base.Host, c.base.String(), "/internal/invitations/pending"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error text %q holds %q", err.Error(), secret)
				}
			}
			if c.wantStatus != "" && !strings.Contains(err.Error(), c.wantStatus) {
				t.Errorf("error text %q does not name the status %s", err.Error(), c.wantStatus)
			}
		})
	}
}

// A 200 that names no verdict is not "no invite": reading it as false would let register sign up an invited address (D6, D26).
func TestHTTPPendingInviteLookup_AnswerWithoutAVerdictIsAnError(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"pending":null}`, `{"status":"ok"}`} {
		t.Run(body, func(t *testing.T) {
			stub := newTenancyStub(t, http.StatusOK, body)

			got, err := NewHTTPPendingInviteLookup(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteAddress)

			if stub.count() != 1 {
				t.Fatalf("tenancy saw %d requests, want 1", stub.count())
			}
			if err == nil || got {
				t.Errorf("lookup = (%v, %v), want (false, an error)", got, err)
			}
		})
	}
}

func TestHTTPPendingInviteLookup_GivesUpAfterTheDeadline(t *testing.T) {
	old := invitationPreviewTimeout
	invitationPreviewTimeout = 150 * time.Millisecond
	t.Cleanup(func() { invitationPreviewTimeout = old })

	release := make(chan struct{})
	var started atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	// No client timeout: only the lookup's own bound can end the call.
	lookup := NewHTTPPendingInviteLookup(base, &http.Client{}, inviteGatewayToken)

	// The outer bound only makes a missing lookup deadline fail fast instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err = lookup(ctx, inviteAddress)
	elapsed := time.Since(start)

	if started.Load() != 1 {
		t.Fatalf("the stub saw %d requests, want 1: the call never reached it, so the deadline proves nothing", started.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a \"timed out\" error", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("gave up after %v, want near the %v bound", elapsed, invitationPreviewTimeout)
	}
	if strings.Contains(err.Error(), inviteAddress) || strings.Contains(err.Error(), base.Host) {
		t.Errorf("error text %q holds the address or the URL", err.Error())
	}
}
