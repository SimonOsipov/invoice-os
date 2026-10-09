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

var liveInvite = InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: "none"}

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
	live := map[string]string{"workspace": inviteWorkspace, "role": inviteRole, "email": inviteAddress, "account": "none"}
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
}

func newTenancyStub(t *testing.T, status int, body string) *tenancyStub {
	t.Helper()
	s := &tenancyStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, string(b))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
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
	// Copied from internal/tenancy/accept.go's 200 body: {workspace, role, email, account}.
	const liveBody = `{"workspace":"` + inviteWorkspace + `","role":"` + inviteRole + `","email":"` + inviteAddress + `","account":"none"}`

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

	t.Run("account", func(t *testing.T) {
		for _, c := range []struct{ name, field, want string }{
			{"confirmed", `,"account":"confirmed"`, "confirmed"},
			{"unconfirmed", `,"account":"unconfirmed"`, "unconfirmed"},
			{"none", `,"account":"none"`, "none"},
			{"unrecognised value", `,"account":"bogus"`, "unknown"},
			{"other case", `,"account":"Confirmed"`, "unknown"},
			{"null", `,"account":null`, "unknown"},
			{"missing", ``, "unknown"},
		} {
			stub := newTenancyStub(t, http.StatusOK, `{"workspace":"`+inviteWorkspace+`","role":"`+inviteRole+`","email":"`+inviteAddress+`"`+c.field+`}`)

			got, err := NewHTTPInvitationPreviewer(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteToken)

			if err != nil || got.Account != c.want {
				t.Errorf("%s: Account = %q (err %v), want %q", c.name, got.Account, err, c.want)
			}
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
		inviteWant  int // 0 means the same as want
	}{
		{"200 new account", http.StatusOK, gtNewUser, false, 202, 0},
		{"200 confirmed address", http.StatusOK, gtSanitizedUser, false, 202, 409},
		{"user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists, false, 202, 409},
		{"email_exists", http.StatusUnprocessableEntity, gtEmailExists, false, 202, 409},
		{"over_email_send_rate_limit", http.StatusTooManyRequests, gtOverEmailSendRateLimit, false, 202, 0},
		{"500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey, false, 202, 0},
		{"weak_password", http.StatusUnprocessableEntity, gtWeakPassword, false, 400, 0},
		{"validation_failed", http.StatusBadRequest, gtValidationFailed, false, 400, 0},
		{"email_address_invalid", http.StatusBadRequest, gtEmailAddressInvalid, false, 400, 0},
		{"signup_disabled", http.StatusUnprocessableEntity, gtSignupDisabled, false, 503, 0},
		{"over_request_rate_limit", http.StatusTooManyRequests, gtOverRequestRateLimit, false, 429, 0},
		{"500 unexpected_failure", http.StatusInternalServerError, gtInternal, false, 502, 0},
		{"502 from GoTrue", http.StatusBadGateway, ``, false, 502, 0},
		{"unreachable", 0, ``, true, 502, 0},
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
			RegisterHandler(authURL, testClient(), 0, freshRegisterLimit(), true, log).ServeHTTP(reg, regReq)

			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: regEmail, Account: "none"}, nil)
			inv, _ := postInvitee(inviteeHandler(authURL, 0, freshRegisterLimit(), p), inviteeBody(inviteToken, regPassword, nil))

			if reg.Code != c.want {
				t.Fatalf("control: /auth/register = %d, want %d: %s", reg.Code, c.want, reg.Body.String())
			}
			if c.inviteWant != 0 {
				if inv.Code != c.inviteWant || errorBody(t, inv) != msgAccountExists {
					t.Errorf("invitee = %d %s, want %d %s", inv.Code, inv.Body.String(), c.inviteWant, msgAccountExists)
				}
			} else if inv.Code != reg.Code || inv.Body.String() != reg.Body.String() {
				t.Errorf("invitee = %d %s, want what /auth/register gives: %d %s", inv.Code, inv.Body.String(), reg.Code, reg.Body.String())
			}
			if got, want := inv.Header().Get("Content-Type"), reg.Header().Get("Content-Type"); got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}
		})
	}
}

func TestInvitationRegister_AnExistingAccountIsRefusedBeforeGoTrue(t *testing.T) {
	const floor = 2 * time.Second
	for _, c := range []struct{ account, want string }{
		{"confirmed", msgAccountExists},
		{"unconfirmed", msgAccountUnconfirmed},
	} {
		t.Run(c.account, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			perIP := freshRegisterLimit()
			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: c.account}, nil)

			for range 2 {
				rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, perIP, p), inviteeBody(inviteToken, "pw-123456", nil))
				if rec.Code != http.StatusConflict || errorBody(t, rec) != c.want {
					t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), c.want)
				}
				if strings.Contains(rec.Body.String(), inviteAddress) {
					t.Errorf("409 body carries the address: %s", rec.Body.String())
				}
				if elapsed > time.Second {
					t.Errorf("answered after %v, want at once (floor %v)", elapsed, floor)
				}
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}

			fresh := previewing(liveInvite, nil)
			for i := range RegisterPerIP {
				rec, _ := postInvitee(inviteeHandler(fake.URL, 0, perIP, fresh), inviteeBody(inviteToken, "pw-123456", nil))
				if rec.Code != http.StatusAccepted {
					t.Fatalf("request %d after the refusals = %d, want 202 from the full per-IP budget", i+1, rec.Code)
				}
			}
			if n := len(fake.Calls()); n != RegisterPerIP {
				t.Errorf("GoTrue saw %d calls, want %d", n, RegisterPerIP)
			}
		})
	}
}

func TestInvitationRegister_ExistingAccountWaitsTheFloor(t *testing.T) {
	const floor = 150 * time.Millisecond
	shapes := []struct {
		name   string
		status int
		body   string
	}{
		{"200 empty identities", http.StatusOK, gtSanitizedUser},
		{"user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists},
		{"email_exists", http.StatusUnprocessableEntity, gtEmailExists},
	}
	for _, account := range []string{"unknown", "none"} {
		for _, c := range shapes {
			t.Run(account+"/"+c.name, func(t *testing.T) {
				fake := newFakeGoTrue(t, c.status, c.body)
				p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: account}, nil)

				rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, freshRegisterLimit(), p), inviteeBody(inviteToken, "pw-123456", nil))

				if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"`+msgAccountExists+`"}` {
					t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), msgAccountExists)
				}
				if elapsed < floor {
					t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
				}
				if n := len(fake.Calls()); n != 1 {
					t.Errorf("GoTrue saw %d calls, want 1", n)
				}
			})
		}
	}
}

func TestInvitationRegister_RefundsTheSlotOnlyForAnExistingAccount(t *testing.T) {
	cases := []struct {
		name       string
		public     bool
		status     int
		body       string
		wantCalls  int
		wantStatus int
	}{
		{"200 empty identities", false, http.StatusOK, gtSanitizedUser, 2, http.StatusConflict},
		{"user_already_exists", false, http.StatusUnprocessableEntity, gtUserAlreadyExists, 2, http.StatusConflict},
		{"email_exists", false, http.StatusUnprocessableEntity, gtEmailExists, 2, http.StatusConflict},
		{"real signup spends the slot", false, http.StatusOK, gtNewUser, 1, http.StatusAccepted},
		{"public route: 200 empty identities still spends the slot", true, http.StatusOK, gtSanitizedUser, 1, http.StatusAccepted},
		{"public route: user_already_exists is refunded by the 4xx rule", true, http.StatusUnprocessableEntity, gtUserAlreadyExists, 2, http.StatusAccepted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			perIP := NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
			fake := newFakeGoTrue(t, c.status, c.body)
			post := func() *httptest.ResponseRecorder {
				if c.public {
					log, _ := captureLog()
					rec := httptest.NewRecorder()
					RegisterHandler(fake.URL, testClient(), 0, perIP, true, log).ServeHTTP(rec,
						httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))))
					return rec
				}
				rec, _ := postInvitee(inviteeHandler(fake.URL, 0, perIP, previewing(liveInvite, nil)), inviteeBody(inviteToken, "pw-123456", nil))
				return rec
			}

			first := post()
			if first.Code != c.wantStatus {
				t.Fatalf("first answer = %d %s, want %d", first.Code, first.Body.String(), c.wantStatus)
			}
			post()

			if n := len(fake.Calls()); n != c.wantCalls {
				t.Errorf("GoTrue saw %d calls after two posts on a budget of 1, want %d", n, c.wantCalls)
			}
		})
	}
}

func TestInvitationRegister_NewAccountShapesStay202(t *testing.T) {
	const autoconfirm = `{"access_token":"a.b.c","token_type":"bearer","expires_in":3600,"refresh_token":"r","user":{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","identities":[]}}`
	noIdentities := `{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"}`
	nullIdentities := `{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","identities":null}`
	for name, body := range map[string]string{"new user": gtNewUser, "autoconfirm token body": autoconfirm, "no identities key": noIdentities, "null identities": nullIdentities} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, body)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil)), inviteeBody(inviteToken, "pw-123456", nil))

			requirePending202(t, rec)
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
	RegisterHandler(fake.URL, testClient(), 0, perIP, true, log).ServeHTTP(reg,
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
