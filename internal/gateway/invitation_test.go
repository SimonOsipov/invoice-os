package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/cryptotest"
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

// postTenancy decodes a 404 body too; the preview must still read it as "not valid", never as the invite it names.
func TestHTTPInvitationPreviewer_404BodyIsNeverAnInvite(t *testing.T) {
	for _, body := range []string{
		`{"workspace":"` + inviteWorkspace + `","role":"` + inviteRole + `","email":"` + inviteAddress + `"}`,
		`404 page not found`,
		``,
	} {
		t.Run(body, func(t *testing.T) {
			stub := newTenancyStub(t, http.StatusNotFound, body)

			got, err := NewHTTPInvitationPreviewer(stub.URL, &http.Client{}, inviteGatewayToken)(t.Context(), inviteToken)

			if !errors.Is(err, ErrInvitationNotValid) || got != (InvitationPreview{}) {
				t.Errorf("preview = (%+v, %v), want (zero, ErrInvitationNotValid)", got, err)
			}
		})
	}
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

// A second valid-shape token (43 base64url characters) that differs from inviteToken.
var otherInviteToken = strings.Repeat("Zk8", 14) + "Z"

var base64url43 = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

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

// recordingRegistrations answers each claim from claim and records every claim and release.
type recordingRegistrations struct {
	mu                sync.Mutex
	claimed, released []string
	claim             func(token string) (string, bool, error)
	release           func(ctx context.Context, token string) error // nil releases cleanly
}

func claiming(email string, first bool, err error) *recordingRegistrations {
	return &recordingRegistrations{claim: func(string) (string, bool, error) { return email, first, err }}
}

func (r *recordingRegistrations) registrations() InvitationRegistrations {
	return InvitationRegistrations{
		Claim: func(_ context.Context, token string) (string, bool, error) {
			r.mu.Lock()
			r.claimed = append(r.claimed, token)
			r.mu.Unlock()
			return r.claim(token)
		},
		Release: func(ctx context.Context, token string) error {
			r.mu.Lock()
			r.released = append(r.released, token)
			r.mu.Unlock()
			if r.release != nil {
				return r.release(ctx, token)
			}
			return nil
		},
	}
}

func (r *recordingRegistrations) claims() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.claimed)
}

func (r *recordingRegistrations) releases() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.released)
}

func inviteeHandler(authURL *url.URL, floor time.Duration, perIP *SignInThrottle, p *recordingPreviewer, r *recordingRegistrations) http.Handler {
	log, _ := captureLog()
	return InvitationRegisterHandler(authURL, testClient(), floor, perIP, true, log, p.preview, r.registrations())
}

// newDroppingGoTrue records the request, then closes the connection without answering.
func newDroppingGoTrue(t *testing.T) *fakeGoTrue {
	t.Helper()
	f := &fakeGoTrue{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, gotrueCall{r.Method, r.URL.Path, b})
		f.mu.Unlock()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

// signupBody decodes the body of the first /signup call the fake saw.
func signupBody(t *testing.T, f *fakeGoTrue) map[string]any {
	t.Helper()
	calls := f.Calls()
	if len(calls) == 0 || calls[0].Path != "/signup" {
		t.Fatalf("GoTrue saw %+v, want a /signup call first", calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("signup body %q is not JSON: %v", calls[0].Body, err)
	}
	return sent
}

// warnRecords returns the WARN records of a captureLog buffer whose msg is msg.
func warnRecords(buf *bytes.Buffer, msg string) []map[string]any {
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["level"] == "WARN" && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func TestInvitationRegister_SignsUpWithAPasswordNobodyKeeps(t *testing.T) {
	const bodyPassword = "body-pw"
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	r := claiming("e@x.test", true, nil)
	h := inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r)

	for _, token := range []string{inviteToken, otherInviteToken} {
		rec, _ := postInvitee(h, inviteeBody(token, bodyPassword, map[string]string{"email": inviteeEmailFromBody}))
		requirePending202(t, rec)
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("GoTrue saw %d calls, want 2 (one signup per token): %+v", len(calls), calls)
	}
	var passwords []string
	for _, c := range calls {
		var sent map[string]any
		if c.Path != "/signup" || json.Unmarshal(c.Body, &sent) != nil {
			t.Fatalf("GoTrue call = %s %q, want a /signup with a JSON body", c.Path, c.Body)
		}
		if keys := slices.Sorted(maps.Keys(sent)); !slices.Equal(keys, []string{"data", "email", "password"}) {
			t.Errorf("signup body keys = %v, want exactly [data email password]: %s", keys, c.Body)
		}
		if sent["email"] != "e@x.test" {
			t.Errorf("signup email = %v, want the claimed address, not the body's", sent["email"])
		}
		if !reflect.DeepEqual(sent["data"], map[string]any{"invited": true}) {
			t.Errorf("signup data = %v, want exactly {\"invited\":true}", sent["data"])
		}
		pw, _ := sent["password"].(string)
		raw, err := base64.RawURLEncoding.DecodeString(pw)
		if !base64url43.MatchString(pw) || err != nil || len(raw) != 32 {
			t.Errorf("signup password %q is not 32 bytes of unpadded base64url (43 characters)", pw)
		}
		if pw == bodyPassword {
			t.Error("signup password is the body's password")
		}
		passwords = append(passwords, pw)
	}
	if passwords[0] == passwords[1] {
		t.Errorf("both signups carry the password %q: it is not generated per request", passwords[0])
	}
	if got := r.claims(); !slices.Equal(got, []string{inviteToken, otherInviteToken}) {
		t.Errorf("claims = %v, want one per posted token", got)
	}
}

// Seeding crypto/rand twice gives the same password only if the password is read from it.
func TestInvitationRegister_PasswordComesFromCryptoRand(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	h := inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), claiming("e@x.test", true, nil))

	for range 2 {
		cryptotest.SetGlobalRandom(t, 7)
		rec, _ := postInvitee(h, tokenBody(inviteToken))
		requirePending202(t, rec)
	}

	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("GoTrue saw %d calls, want 2", len(calls))
	}
	var first, second struct {
		Password string `json:"password"`
	}
	if json.Unmarshal(calls[0].Body, &first) != nil || json.Unmarshal(calls[1].Body, &second) != nil || first.Password == "" {
		t.Fatalf("signup bodies %q and %q carry no password", calls[0].Body, calls[1].Body)
	}
	if first.Password != second.Password {
		t.Errorf("one crypto/rand seed gave two passwords (%q, %q): the password does not come from crypto/rand", first.Password, second.Password)
	}
}

func TestInvitationRegister_GeneratedPasswordIsNeverEchoedOrLogged(t *testing.T) {
	cases := []struct {
		name       string
		fake       func(t *testing.T) *fakeGoTrue
		wantLogged bool // the case must log something, or the check below proves nothing
	}{
		{"200", func(t *testing.T) *fakeGoTrue { return newFakeGoTrue(t, http.StatusOK, gtNewUser) }, false},
		{"user_already_exists", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusUnprocessableEntity, gtUserAlreadyExists)
		}, false},
		{"over_email_send_rate_limit", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusTooManyRequests, gtOverEmailSendRateLimit)
		}, true},
		{"500", func(t *testing.T) *fakeGoTrue { return newFakeGoTrue(t, http.StatusInternalServerError, gtInternal) }, true},
		{"unreachable", newDroppingGoTrue, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := c.fake(t)
			log, buf := captureLog()
			r := claiming("e@x.test", true, nil)
			h := InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview, r.registrations())

			rec, _ := postInvitee(h, inviteeBody(inviteToken, "body-pw", nil))

			pw, _ := signupBody(t, fake)["password"].(string)
			if pw == "" {
				t.Fatal("the signup carried no password: nothing to look for")
			}
			if c.wantLogged && buf.Len() == 0 {
				t.Fatal("no log line: the check below proves nothing")
			}
			if strings.Contains(rec.Body.String(), pw) || strings.Contains(buf.String(), pw) {
				t.Errorf("the generated password is in the answer or the log: %s %s", rec.Body.String(), buf.String())
			}
			for name, values := range rec.Header() {
				if strings.Contains(strings.Join(values, ","), pw) {
					t.Errorf("the generated password is in response header %s", name)
				}
			}
		})
	}
}

func TestInvitationRegister_SecondRegistrationOfATokenCreatesNothing(t *testing.T) {
	const floor = 200 * time.Millisecond
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	first, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), claiming("e@x.test", true, nil)), tokenBody(inviteToken))
	requirePending202(t, first)
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("control: the first registration made %d GoTrue calls, want 1", n)
	}

	r := claiming("e@x.test", false, nil)
	rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, freshRegisterLimit(), previewing(liveInvite, nil), r), tokenBody(inviteToken))

	requirePending202(t, rec)
	if rec.Body.String() != first.Body.String() || rec.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
		t.Errorf("repeat = %q (%s), want the first registration's %q (%s)",
			rec.Body.String(), rec.Header().Get("Content-Type"), first.Body.String(), first.Header().Get("Content-Type"))
	}
	if elapsed < floor {
		t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("GoTrue saw %d calls, want only the first registration's", n)
	}
	if got := r.claims(); !slices.Equal(got, []string{inviteToken}) {
		t.Errorf("claims = %v, want exactly the posted token", got)
	}
}

func TestInvitationRegister_TokenIsTheOnlyRequiredField(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		claims     int
		msg        string // the 400 text; "" means "token is required"
	}{
		{"empty object", `{}`, 400, 0, ""},
		{"empty token", `{"token":""}`, 400, 0, ""},
		{"null body", `null`, 400, 0, ""},
		{"password without a token", `{"password":"body-pw"}`, 400, 0, ""},
		{"token that is not a string", `{"token":123}`, 400, 0, "invalid request body"},
		{"body past the size cap", `{"token":"` + inviteToken + `","pad":"` + strings.Repeat("a", 2*maxRegisterBodyBytes) + `"}`, 400, 0, "invalid request body"},
		{"token only", tokenBody(inviteToken), 202, 1, ""},
		{"empty password", `{"token":"` + inviteToken + `","password":""}`, 202, 1, ""},
		{"unknown keys", `{"token":"` + inviteToken + `","email":"` + inviteeEmailFromBody + `","extra":{"a":[1]}}`, 202, 1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming("e@x.test", true, nil)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r), c.body)

			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body.String())
			}
			if c.status == 400 {
				want := cmp.Or(c.msg, "token is required")
				if got := errorBody(t, rec); got != want {
					t.Errorf("error = %q, want %q", got, want)
				}
			} else if sent := signupBody(t, fake); sent["email"] != "e@x.test" {
				t.Errorf("signup email = %v, want the claimed address", sent["email"])
			}
			if got := len(r.claims()); got != c.claims {
				t.Errorf("claims = %d, want %d", got, c.claims)
			}
			if want := c.claims; len(fake.Calls()) != want {
				t.Errorf("GoTrue saw %d calls, want %d", len(fake.Calls()), want)
			}
		})
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
			RegisterHandler(authURL, testClient(), 0, freshRegisterLimit(), true, log, noPendingInvite).ServeHTTP(reg, regReq)

			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: regEmail, Account: "none"}, nil)
			inv, _ := postInvitee(inviteeHandler(authURL, 0, freshRegisterLimit(), p, claiming(regEmail, true, nil)), tokenBody(inviteToken))

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
	const floor = 150 * time.Millisecond
	for _, c := range []struct{ account, want string }{
		{"confirmed", msgAccountExists},
		{"unconfirmed", msgAccountUnconfirmed},
	} {
		t.Run(c.account, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			perIP := freshRegisterLimit()
			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: c.account}, nil)

			for range 2 {
				rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, perIP, p, claiming(inviteAddress, true, nil)), inviteeBody(inviteToken, "pw-123456", nil))
				if rec.Code != http.StatusConflict || errorBody(t, rec) != c.want {
					t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), c.want)
				}
				if strings.Contains(rec.Body.String(), inviteAddress) {
					t.Errorf("409 body carries the address: %s", rec.Body.String())
				}
				if elapsed < floor {
					t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
				}
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}

			fresh := previewing(liveInvite, nil)
			for i := range RegisterPerIP {
				rec, _ := postInvitee(inviteeHandler(fake.URL, 0, perIP, fresh, claiming(inviteAddress, true, nil)), inviteeBody(inviteToken, "pw-123456", nil))
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

// A preview refusal takes the register budget check as the 202 path does: a spent IP gets the uniform 202 after the floor, not the 409.
func TestInvitationRegister_PreviewRefusalTakesTheBudgetCheck(t *testing.T) {
	const floor = 150 * time.Millisecond
	for _, account := range []string{"confirmed", "unconfirmed"} {
		t.Run(account, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			perIP := NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
			perIP.Reserve("192.0.2.1")
			r := claiming(inviteAddress, true, nil)
			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: account}, nil)
			log, _ := captureLog()
			req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken)))
			req.RemoteAddr = "192.0.2.1:5555"
			rec := httptest.NewRecorder()
			start := time.Now()

			InvitationRegisterHandler(fake.URL, testClient(), floor, perIP, true, log, p.preview, r.registrations()).ServeHTTP(rec, req)

			requirePending202(t, rec)
			if elapsed := time.Since(start); elapsed < floor {
				t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
			}
			if len(r.claims()) != 0 || len(fake.Calls()) != 0 {
				t.Errorf("claims = %d, GoTrue calls = %d, want 0 and 0", len(r.claims()), len(fake.Calls()))
			}
		})
	}
}

func TestInvitationRegister_PreviewRefusalOnASpentIPIsNotEnforcedWhenOff(t *testing.T) {
	const floor = 150 * time.Millisecond
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	perIP := NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
	perIP.Reserve("192.0.2.1")
	r := claiming(inviteAddress, true, nil)
	p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: "confirmed"}, nil)
	log, _ := captureLog()
	req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken)))
	req.RemoteAddr = "192.0.2.1:5555"
	rec := httptest.NewRecorder()
	start := time.Now()

	InvitationRegisterHandler(fake.URL, testClient(), floor, perIP, false, log, p.preview, r.registrations()).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict || errorBody(t, rec) != msgAccountExists {
		t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), msgAccountExists)
	}
	if elapsed := time.Since(start); elapsed < floor {
		t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
	}
	if len(r.claims()) != 0 || len(fake.Calls()) != 0 {
		t.Errorf("claims = %d, GoTrue calls = %d, want 0 and 0", len(r.claims()), len(fake.Calls()))
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

				rec, elapsed := postInvitee(inviteeHandler(fake.URL, floor, freshRegisterLimit(), p, claiming(inviteAddress, true, nil)), inviteeBody(inviteToken, "pw-123456", nil))

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
					RegisterHandler(fake.URL, testClient(), 0, perIP, true, log, noPendingInvite).ServeHTTP(rec,
						httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))))
					return rec
				}
				rec, _ := postInvitee(inviteeHandler(fake.URL, 0, perIP, previewing(liveInvite, nil), claiming(inviteAddress, true, nil)), inviteeBody(inviteToken, "pw-123456", nil))
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

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), claiming(inviteAddress, true, nil)), inviteeBody(inviteToken, "pw-123456", nil))

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
		claims int
		logged bool
	}{
		{"token names no usable invite", inviteeBody(inviteToken, "pw-123456", nil), ErrInvitationNotValid, 404, wantInviteNotValid, 1, false},
		{"claim error", inviteeBody(inviteToken, "pw-123456", nil), errors.New("tenancy answered 500"), 502, wantLookupUnavailable, 1, true},
		{"empty token", inviteeBody("", "pw-123456", nil), nil, 400, "token is required", 0, false},
		{"malformed token", inviteeBody(inviteToken[:42], "pw-123456", nil), nil, 404, wantInviteNotValid, 0, false},
		{"malformed body", `{`, nil, 400, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming("e@x.test", true, c.result)
			log, buf := captureLog()
			h := InvitationRegisterHandler(fake.URL, testClient(), floor, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview, r.registrations())

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
			if got := len(r.claims()); got != c.claims {
				t.Errorf("claims = %d, want %d", got, c.claims)
			}
			if got := r.releases(); len(got) != 0 {
				t.Errorf("releases = %v, want none: nothing was claimed", got)
			}
			if elapsed > 100*time.Millisecond {
				t.Errorf("answered after %v, want at once (floor %v)", elapsed, floor)
			}
			if strings.Contains(buf.String(), inviteToken) || strings.Contains(buf.String(), "pw-123456") {
				t.Errorf("a log line carries the token or the password: %s", buf.String())
			}
			if c.logged && buf.Len() == 0 {
				t.Error("no log line on a claim error: the secret check above proves nothing")
			}
		})
	}
}

// invitationHandlers guards the same in main; the handler must not panic when built directly with half a pair.
func TestInvitationRegister_NilClaimOrReleaseIsNotConfigured(t *testing.T) {
	for name, strip := range map[string]func(*InvitationRegistrations){
		"Claim nil":   func(r *InvitationRegistrations) { r.Claim = nil },
		"Release nil": func(r *InvitationRegistrations) { r.Release = nil },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming("e@x.test", true, nil)
			regs := r.registrations()
			strip(&regs)
			log, _ := captureLog()

			rec, _ := postInvitee(InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview, regs), tokenBody(inviteToken))

			if rec.Code != http.StatusServiceUnavailable || errorBody(t, rec) != "registration is not configured" {
				t.Errorf("answer = %d %s, want 503 registration is not configured", rec.Code, rec.Body.String())
			}
			if n := len(fake.Calls()) + len(r.claims()) + len(r.releases()); n != 0 {
				t.Errorf("GoTrue, claim and release saw %d calls, want 0", n)
			}
		})
	}
}

func TestInvitationRegister_OnlyPostClaims(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	r := claiming("e@x.test", true, nil)
	h := inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken))))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, rec.Code)
		}
	}
	if n := len(fake.Calls()) + len(r.claims()); n != 0 {
		t.Errorf("GoTrue and claim saw %d calls, want 0", n)
	}
	rec, _ := postInvitee(h, tokenBody(inviteToken))
	requirePending202(t, rec)
}

func TestInvitationRegister_SharesTheRegisterBudget(t *testing.T) {
	const floor = 150 * time.Millisecond
	perIP := NewSignInThrottle("register", 3, RegisterMaxKeys, RegisterWindow, time.Now)
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log, _ := captureLog()
	postRegister := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		RegisterHandler(fake.URL, testClient(), 0, perIP, true, log, noPendingInvite).ServeHTTP(rec,
			httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody(regEmail, regPassword))))
		return rec
	}
	r := &recordingRegistrations{claim: func(token string) (string, bool, error) { return "e@x.test", token == inviteToken, nil }}
	h := inviteeHandler(fake.URL, floor, perIP, previewing(liveInvite, nil), r)

	requirePending202(t, postRegister())
	first, firstElapsed := postInvitee(h, tokenBody(inviteToken))
	repeat, repeatElapsed := postInvitee(h, tokenBody(otherInviteToken))

	requirePending202(t, first)
	requirePending202(t, repeat)
	if firstElapsed < floor || repeatElapsed < floor {
		t.Errorf("invitee answers after %v and %v, want each no earlier than the %v floor", firstElapsed, repeatElapsed, floor)
	}
	if n := len(fake.Calls()); n != 2 {
		t.Fatalf("GoTrue saw %d calls, want 2 (the /auth/register signup and the first invitee signup)", n)
	}
	requirePending202(t, postRegister())
	if n := len(fake.Calls()); n != 2 {
		t.Errorf("GoTrue saw %d calls after the budget was spent on invite registrations, want still 2: /auth/register reached GoTrue", n)
	}
}

// The free-mail refusal belongs to /auth/register; an invite names an address an admin chose.
func TestInvitationRegister_FreeMailRule(t *testing.T) {
	const (
		freeMail     = "ada@gmail.com"
		expiredToken = "Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9Ex9E"
		spentToken   = "Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9Sp9S"
	)
	newRegs := func() *recordingRegistrations {
		return &recordingRegistrations{claim: func(token string) (string, bool, error) {
			if token == inviteToken {
				return freeMail, true, nil
			}
			return "", false, ErrInvitationNotValid
		}}
	}
	p := newRegs()

	t.Run("a live invite to a free-mail address is signed up", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), p), tokenBody(inviteToken))
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
			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), p), tokenBody(token))
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
	if seen := p.claims(); len(seen) != 3 {
		t.Errorf("claims = %v, want one for each of the three invitee posts", seen)
	}
}

// No outcome of the signup writes the token, the body's password or the generated one to a log line.
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
			h := InvitationRegisterHandler(authURL, testClient(), 0, perIP, true, log, previewing(liveInvite, nil).preview, claiming("e@x.test", true, nil).registrations())
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
	t.Run("control: a well-formed token reaches the claim", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		r := claiming("e@x.test", true, nil)

		rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r), tokenBody(inviteToken))

		requirePending202(t, rec)
		if n := len(r.claims()); n != 1 {
			t.Errorf("claims = %d, want 1", n)
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
			r := claiming("e@x.test", true, nil)
			p := previewing(liveInvite, nil)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), p, r), tokenBody(token))

			if rec.Code != 404 {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, rec); got != wantInviteNotValid {
				t.Errorf("error = %q, want %q", got, wantInviteNotValid)
			}
			if n := len(p.calls()); n != 0 {
				t.Errorf("previewer calls = %d, want 0", n)
			}
			if n := len(r.claims()); n != 0 {
				t.Errorf("claims = %d, want 0", n)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

func TestInvitationRegister_ReleasesTheClaimWhenNoAccountWasCreated(t *testing.T) {
	cases := []struct {
		name string
		fake func(t *testing.T) *fakeGoTrue
		want int
	}{
		{"over_email_send_rate_limit", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusTooManyRequests, gtOverEmailSendRateLimit)
		}, 202},
		{"validation_failed", func(t *testing.T) *fakeGoTrue { return newFakeGoTrue(t, http.StatusBadRequest, gtValidationFailed) }, 400},
		{"email_address_invalid", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusBadRequest, gtEmailAddressInvalid)
		}, 400},
		{"weak_password", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusUnprocessableEntity, gtWeakPassword)
		}, 400},
		{"signup_disabled", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusUnprocessableEntity, gtSignupDisabled)
		}, 503},
		{"over_request_rate_limit", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusTooManyRequests, gtOverRequestRateLimit)
		}, 429},
		{"500 without 23505", func(t *testing.T) *fakeGoTrue { return newFakeGoTrue(t, http.StatusInternalServerError, gtInternal) }, 502},
		{"500 with another SQLSTATE", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusInternalServerError, gtOtherSQLState)
		}, 502},
		{"422 carrying SQLSTATE 23505", func(t *testing.T) *fakeGoTrue {
			return newFakeGoTrue(t, http.StatusUnprocessableEntity, gtDuplicateKey)
		}, 502},
		{"502 from GoTrue", func(t *testing.T) *fakeGoTrue { return newFakeGoTrue(t, http.StatusBadGateway, ``) }, 502},
		{"unreachable", func(t *testing.T) *fakeGoTrue { return &fakeGoTrue{URL: closedURL(t)} }, 502},
		{"connection dropped", newDroppingGoTrue, 502},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := c.fake(t)
			rec := httptest.NewRecorder()
			r := claiming("e@x.test", true, nil)
			answeredAtRelease := false
			r.release = func(context.Context, string) error {
				answeredAtRelease = rec.Body.Len() > 0
				return nil
			}
			log, _ := captureLog()
			h := InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview, r.registrations())

			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken))))

			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if got := r.claims(); !slices.Equal(got, []string{inviteToken}) {
				t.Errorf("claims = %v, want exactly the posted token", got)
			}
			if got := r.releases(); !slices.Equal(got, []string{inviteToken}) {
				t.Errorf("releases = %v, want exactly the claimed token, once", got)
			}
			if !answeredAtRelease {
				t.Error("the release ran before the answer was written")
			}
		})
	}
}

func TestInvitationRegister_KeepsTheClaimWhenAnAccountMayExist(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"200 new account", http.StatusOK, gtNewUser, http.StatusAccepted},
		{"200 confirmed address", http.StatusOK, gtSanitizedUser, http.StatusConflict},
		{"user_already_exists", http.StatusUnprocessableEntity, gtUserAlreadyExists, http.StatusConflict},
		{"email_exists", http.StatusUnprocessableEntity, gtEmailExists, http.StatusConflict},
		{"500 SQLSTATE 23505", http.StatusInternalServerError, gtDuplicateKey, http.StatusAccepted},
		{"500 carrying user_already_exists", http.StatusInternalServerError, gtUserAlreadyExists, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, c.status, c.body)
			r := claiming("e@x.test", true, nil)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r), tokenBody(inviteToken))

			if c.want == http.StatusAccepted {
				requirePending202(t, rec)
			} else if rec.Code != c.want || errorBody(t, rec) != msgAccountExists {
				t.Errorf("answer = %d %s, want %d %s", rec.Code, rec.Body.String(), c.want, msgAccountExists)
			}
			if n := len(fake.Calls()); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1: the case proves nothing without a sign-up", n)
			}
			if got := r.releases(); len(got) != 0 {
				t.Errorf("releases = %v, want none: an account may exist", got)
			}
		})
	}
}

// The preview refusal spends no claim and calls no GoTrue; any other state claims once.
func TestInvitationRegister_PreviewRefusalComesBeforeTheClaim(t *testing.T) {
	cases := []struct {
		account string
		want    int
		wantMsg string
	}{
		{"confirmed", http.StatusConflict, msgAccountExists},
		{"unconfirmed", http.StatusConflict, msgAccountUnconfirmed},
		{"none", http.StatusAccepted, ""},
		{"unknown", http.StatusAccepted, ""},
		{"", http.StatusAccepted, ""},
	}
	for _, c := range cases {
		t.Run("account "+c.account, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming(inviteAddress, true, nil)
			p := previewing(InvitationPreview{Workspace: inviteWorkspace, Role: inviteRole, Email: inviteAddress, Account: c.account}, nil)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), p, r), tokenBody(inviteToken))

			wantClaims, wantCalls := 0, 0
			if c.want == http.StatusAccepted {
				requirePending202(t, rec)
				wantClaims, wantCalls = 1, 1
			} else if rec.Code != c.want || errorBody(t, rec) != c.wantMsg {
				t.Errorf("answer = %d %s, want %d %s", rec.Code, rec.Body.String(), c.want, c.wantMsg)
			}
			if got := len(r.claims()); got != wantClaims {
				t.Errorf("claims = %d, want %d", got, wantClaims)
			}
			if got := len(r.releases()); got != 0 {
				t.Errorf("releases = %d, want 0", got)
			}
			if got := len(fake.Calls()); got != wantCalls {
				t.Errorf("GoTrue calls = %d, want %d", got, wantCalls)
			}
		})
	}
}

// A preview that fails answers as the preview route does, before any claim or GoTrue call.
func TestInvitationRegister_PreviewFailureAnswersBeforeTheClaim(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		msg  string
	}{
		{"token names no invite", ErrInvitationNotValid, http.StatusNotFound, wantInviteNotValid},
		{"tenancy answers 500", errors.New("tenancy answered 500"), http.StatusBadGateway, wantLookupUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming(inviteAddress, true, nil)
			p := previewing(InvitationPreview{}, c.err)

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, freshRegisterLimit(), p, r), tokenBody(inviteToken))

			if rec.Code != c.want || errorBody(t, rec) != c.msg {
				t.Errorf("answer = %d %s, want %d %s", rec.Code, rec.Body.String(), c.want, c.msg)
			}
			if got := len(p.calls()); got != 1 {
				t.Errorf("previewer calls = %d, want 1: the case proves nothing without a preview", got)
			}
			if got := len(r.claims()); got != 0 {
				t.Errorf("claims = %d, want 0", got)
			}
			if got := len(fake.Calls()); got != 0 {
				t.Errorf("GoTrue calls = %d, want 0", got)
			}
		})
	}
}

func TestInvitationRegister_PerIPRefusalReleasesTheClaim(t *testing.T) {
	const floor = 200 * time.Millisecond
	spent := func() *SignInThrottle {
		perIP := NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
		perIP.Reserve("192.0.2.1")
		return perIP
	}
	post := func(h http.Handler) (*httptest.ResponseRecorder, time.Duration) {
		req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken)))
		req.RemoteAddr = "192.0.2.1:5555"
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, req)
		return rec, time.Since(start)
	}

	t.Run("enforced", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		r := claiming("e@x.test", true, nil)
		log, _ := captureLog()

		rec, elapsed := post(InvitationRegisterHandler(fake.URL, testClient(), floor, spent(), true, log, previewing(liveInvite, nil).preview, r.registrations()))

		requirePending202(t, rec)
		if elapsed < floor {
			t.Errorf("answered after %v, want no earlier than the %v floor", elapsed, floor)
		}
		if n := len(fake.Calls()); n != 0 {
			t.Errorf("GoTrue saw %d calls, want 0", n)
		}
		if got := r.releases(); !slices.Equal(got, []string{inviteToken}) {
			t.Errorf("releases = %v, want the claimed token once: no account was created", got)
		}
	})

	t.Run("not enforced, the sign-up proceeds and the claim stays", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
		r := claiming("e@x.test", true, nil)
		log, _ := captureLog()

		rec, _ := post(InvitationRegisterHandler(fake.URL, testClient(), 0, spent(), false, log, previewing(liveInvite, nil).preview, r.registrations()))

		requirePending202(t, rec)
		if n := len(fake.Calls()); n != 1 {
			t.Errorf("GoTrue saw %d calls, want 1", n)
		}
		if got := r.releases(); len(got) != 0 {
			t.Errorf("releases = %v, want none", got)
		}
	})
}

func TestInvitationRegister_ReleaseFailureIsLoggedAndTheAnswerStands(t *testing.T) {
	run := func(release func(context.Context, string) error) (*httptest.ResponseRecorder, *bytes.Buffer, *recordingRegistrations) {
		fake := newFakeGoTrue(t, http.StatusTooManyRequests, gtOverEmailSendRateLimit)
		r := claiming("e@x.test", true, nil)
		r.release = release
		log, buf := captureLog()
		h := InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview, r.registrations())
		rec, _ := postInvitee(h, tokenBody(inviteToken))
		return rec, buf, r
	}
	control, _, _ := run(nil)
	requirePending202(t, control)

	rec, buf, r := run(func(context.Context, string) error { return errors.New("invitation release: tenancy unreachable") })

	requirePending202(t, rec)
	if rec.Body.String() != control.Body.String() {
		t.Errorf("answer = %q, want the answer without the failure: %q", rec.Body.String(), control.Body.String())
	}
	if got := r.releases(); len(got) != 1 {
		t.Fatalf("releases = %v, want one attempt", got)
	}
	warns := warnRecords(buf, "invitation: release failed")
	if len(warns) != 1 {
		t.Fatalf("WARN `invitation: release failed` lines = %d, want 1: %s", len(warns), buf.String())
	}
	var attrs []string
	for k := range warns[0] {
		if k != "time" && k != "level" && k != "msg" {
			attrs = append(attrs, k)
		}
	}
	if !slices.Equal(attrs, []string{"error"}) || warns[0]["error"] != "invitation release: tenancy unreachable" {
		t.Errorf("the WARN carries %v = %v, want only the error", attrs, warns[0])
	}
	if strings.Contains(buf.String(), inviteToken) {
		t.Errorf("a log line carries the token: %s", buf.String())
	}
}

// A client that went away must not leave the claim spent: the release outlives the request's context.
func TestInvitationRegister_ReleaseOutlivesTheRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, gtOverEmailSendRateLimit)
	}))
	t.Cleanup(srv.Close)
	authURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	r := claiming("e@x.test", true, nil)
	var releaseCtxErr error
	r.release = func(ctx context.Context, _ string) error {
		releaseCtxErr = ctx.Err()
		return nil
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/invitation/register", strings.NewReader(tokenBody(inviteToken)))

	inviteeHandler(authURL, 0, freshRegisterLimit(), previewing(liveInvite, nil), r).ServeHTTP(httptest.NewRecorder(), req)

	if ctx.Err() == nil {
		t.Fatal("the request context was never cancelled: the case proves nothing")
	}
	if got := r.releases(); !slices.Equal(got, []string{inviteToken}) {
		t.Fatalf("releases = %v, want the claimed token once", got)
	}
	if releaseCtxErr != nil {
		t.Errorf("the release ran under a cancelled context: %v", releaseCtxErr)
	}
}

func TestInvitationRegister_SecondRegistrationReleasesNothing(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget left", true: "budget spent"}[exhausted], func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
			r := claiming("e@x.test", false, nil)
			perIP := freshRegisterLimit()
			if exhausted {
				perIP = NewSignInThrottle("register", 1, RegisterMaxKeys, RegisterWindow, time.Now)
				perIP.Reserve("192.0.2.1")
			}

			rec, _ := postInvitee(inviteeHandler(fake.URL, 0, perIP, previewing(liveInvite, nil), r), tokenBody(inviteToken))

			requirePending202(t, rec)
			if got := r.claims(); !slices.Equal(got, []string{inviteToken}) {
				t.Errorf("claims = %v, want one", got)
			}
			if got := r.releases(); len(got) != 0 {
				t.Errorf("releases = %v, want none: this registration claimed nothing", got)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
}

// Deploy skew: an old tenancy answers an unrouted POST with a text/plain 404 (not the invite's JSON 404).
func TestInvitationRegister_UnroutedTenancyIsUnavailableNotInvalid(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	fake := newFakeGoTrue(t, http.StatusOK, gtNewUser)
	log, _ := captureLog()
	h := InvitationRegisterHandler(fake.URL, testClient(), 0, freshRegisterLimit(), true, log, previewing(liveInvite, nil).preview,
		NewHTTPInvitationRegistrations(base, &http.Client{}, inviteGatewayToken))

	rec, elapsed := postInvitee(h, tokenBody(inviteToken))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != wantLookupUnavailable {
		t.Errorf("error = %q, want %q", got, wantLookupUnavailable)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, []string{"/internal/invitations/register"}) {
		t.Errorf("tenancy saw %v, want exactly the claim: nothing was claimed, so nothing to release", paths)
	}
	if elapsed > time.Second {
		t.Errorf("answered after %v, want at once", elapsed)
	}
}

const (
	claimPath   = "/internal/invitations/register"
	releasePath = "/internal/invitations/release"
	claimedBody = `{"email":"e@x.test","first":true}`
)

func claimWith(base *url.URL) (string, bool, error) {
	return NewHTTPInvitationRegistrations(base, &http.Client{}, inviteGatewayToken).Claim(context.Background(), inviteToken)
}

func releaseWith(base *url.URL) error {
	return NewHTTPInvitationRegistrations(base, &http.Client{}, inviteGatewayToken).Release(context.Background(), inviteToken)
}

func TestHTTPInvitationRegistrations_Wire(t *testing.T) {
	requireOneAsk := func(t *testing.T, stub *tenancyStub, path string) {
		t.Helper()
		if stub.count() != 1 {
			t.Fatalf("tenancy saw %d requests, want 1", stub.count())
		}
		r := stub.requests[0]
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("X-Gateway-Token"); got != inviteGatewayToken {
			t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
		}
		for _, h := range []string{"X-User-ID", "X-User-Email", "X-User-Role", "X-Tenant-ID"} {
			if v := r.Header.Values(h); len(v) != 0 {
				t.Errorf("%s = %q, want no identity header", h, v)
			}
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil || !maps.Equal(sent, map[string]string{"token": inviteToken}) {
			t.Errorf("body = %s (decode err %v), want exactly {\"token\":<token>}", stub.bodies[0], err)
		}
	}
	redirecting := func(t *testing.T) (*tenancyStub, *tenancyStub) {
		second := newTenancyStub(t, http.StatusOK, claimedBody)
		first := newTenancyStub(t, http.StatusFound, ``)
		first.mu.Lock()
		first.location = second.URL.String() + "/elsewhere"
		first.mu.Unlock()
		return first, second
	}

	t.Run("claim 200 names the address and whether it is the first", func(t *testing.T) {
		for _, c := range []struct {
			body  string
			first bool
		}{{claimedBody, true}, {`{"email":"e@x.test","first":false}`, false}} {
			stub := newTenancyStub(t, http.StatusOK, c.body)

			email, first, err := claimWith(stub.URL)

			requireOneAsk(t, stub, claimPath)
			if err != nil || email != "e@x.test" || first != c.first {
				t.Errorf("claim of %s = (%q, %v, %v), want (e@x.test, %v, nil)", c.body, email, first, err, c.first)
			}
		}
	})
	t.Run("claim 404 with the invite's JSON error is not valid", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusNotFound, `{"error":"`+wantInviteNotValid+`"}`)

		_, _, err := claimWith(stub.URL)

		requireOneAsk(t, stub, claimPath)
		if !errors.Is(err, ErrInvitationNotValid) {
			t.Errorf("err = %v, want ErrInvitationNotValid", err)
		}
	})
	t.Run("claim 302 is an error and is not followed", func(t *testing.T) {
		first, second := redirecting(t)

		email, firstClaim, err := claimWith(first.URL)

		requireOneAsk(t, first, claimPath)
		if err == nil || email != "" || firstClaim {
			t.Errorf("claim = (%q, %v, %v), want (\"\", false, an error)", email, firstClaim, err)
		}
		if n := second.count(); n != 0 {
			t.Errorf("the redirect target saw %d request(s), want 0", n)
		}
	})
	for _, c := range []struct{ name, body string }{
		{"text/plain 404 from a tenancy without the route", `404 page not found`},
		{"404 with another JSON error", `{"error":"invalid request body"}`},
		{"404 with an empty body", ``},
		{"404 with the message outside an error key", `{"message":"` + wantInviteNotValid + `"}`},
		{"404 with the message and a trailing space", `{"error":"` + wantInviteNotValid + ` "}`},
		{"404 with the message in an array", `{"error":["` + wantInviteNotValid + `"]}`},
		{"404 with the message as a bare string", `"` + wantInviteNotValid + `"`},
		{"404 with a null body", `null`},
		{"404 with the message cut off", `{"error":"` + wantInviteNotValid + `"`},
		{"404 whose body overruns the read limit after the message", `{"error":"` + wantInviteNotValid + `","pad":"` + strings.Repeat("a", 2*maxPreviewResponseBytes) + `"}`},
	} {
		t.Run("claim "+c.name+" is an error, not an invalid invite", func(t *testing.T) {
			stub := newTenancyStub(t, http.StatusNotFound, c.body)

			_, _, err := claimWith(stub.URL)

			if err == nil || errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("err = %v, want an error distinct from ErrInvitationNotValid", err)
			}
		})
	}
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"200 without an address", 200, `{"first":true}`},
		{"200 with an empty address", 200, `{"email":"","first":true}`},
		{"200 without first", 200, `{"email":"e@x.test"}`},
		{"200 with a non-boolean first", 200, `{"email":"e@x.test","first":"yes"}`},
		{"200 with null first", 200, `{"email":"e@x.test","first":null}`},
		{"200 with an unreadable body", 200, `<html>not json`},
		{"200 with an empty body", 200, ``},
		{"400", 400, `{"error":"invalid request body"}`},
		{"401", 401, `{"error":"unauthorized"}`},
		{"500", 500, `{"error":"boom"}`},
		{"204", 204, ``},
	} {
		t.Run("claim "+c.name+" is an error", func(t *testing.T) {
			stub := newTenancyStub(t, c.status, c.body)

			email, first, err := claimWith(stub.URL)

			if err == nil || errors.Is(err, ErrInvitationNotValid) || email != "" || first {
				t.Errorf("claim = (%q, %v, %v), want (\"\", false, an error other than ErrInvitationNotValid)", email, first, err)
			}
		})
	}

	t.Run("release 204 is nil", func(t *testing.T) {
		stub := newTenancyStub(t, http.StatusNoContent, ``)

		err := releaseWith(stub.URL)

		requireOneAsk(t, stub, releasePath)
		if err != nil {
			t.Errorf("release = %v, want nil", err)
		}
	})
	t.Run("release 302 is an error and is not followed", func(t *testing.T) {
		first, second := redirecting(t)

		err := releaseWith(first.URL)

		requireOneAsk(t, first, releasePath)
		if err == nil {
			t.Error("release = nil, want an error")
		}
		if n := second.count(); n != 0 {
			t.Errorf("the redirect target saw %d request(s), want 0", n)
		}
	})
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"404", 404, `404 page not found`},
		{"404 with the invite's JSON error", 404, `{"error":"` + wantInviteNotValid + `"}`},
		{"500", 500, `{"error":"boom"}`},
		{"200", 200, `{}`},
		{"202", 202, ``},
	} {
		t.Run("release "+c.name+" is an error", func(t *testing.T) {
			stub := newTenancyStub(t, c.status, c.body)

			if err := releaseWith(stub.URL); err == nil {
				t.Errorf("release = nil on a %d, want an error: only 204 means released", c.status)
			}
		})
	}
}

func TestHTTPInvitationRegistrations_ErrorsCarryNoSecretAndGiveUpAtTheDeadline(t *testing.T) {
	second := newTenancyStub(t, http.StatusOK, claimedBody)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, second.URL.String()+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)
	redirectURL, err := url.Parse(redirecting.URL)
	if err != nil {
		t.Fatalf("parse redirecting url: %v", err)
	}
	ops := map[string]func(*url.URL) error{
		"claim": func(base *url.URL) error {
			_, _, err := claimWith(base)
			return err
		},
		"release": releaseWith,
	}

	for op, call := range ops {
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
			t.Run(op+" "+c.name, func(t *testing.T) {
				err := call(c.base)

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
				for _, secret := range []string{inviteToken, inviteGatewayToken, c.base.Host, c.base.String(), claimPath, releasePath} {
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
	if n := second.count(); n != 0 {
		t.Errorf("the redirect target saw %d request(s), want 0", n)
	}

	for op, call := range ops {
		t.Run(op+" gives up at the deadline", func(t *testing.T) {
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

			start := time.Now()
			err = call(base)
			elapsed := time.Since(start)

			if started.Load() != 1 {
				t.Fatalf("the stub saw %d requests, want 1: the call never reached it, so the deadline proves nothing", started.Load())
			}
			if err == nil {
				t.Fatal("err = nil, want a deadline error")
			}
			if elapsed > 3*time.Second {
				t.Errorf("gave up after %v, want near the %v bound", elapsed, invitationPreviewTimeout)
			}
			for _, secret := range []string{inviteToken, inviteGatewayToken, base.Host} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error text %q holds %q", err.Error(), secret)
				}
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

// A 200 that names no verdict is not "no invite": reading it as false would let register sign up an invited address.
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
