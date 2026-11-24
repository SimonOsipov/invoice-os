package erp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// intuitStub is a recording token endpoint.
type intuitStub struct {
	mu     sync.Mutex
	reqs   []*http.Request
	forms  []url.Values
	bodies [][]byte // JSON requests only
	status int
	body   string
	delay  time.Duration
}

func (s *intuitStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	if r.Header.Get("Content-Type") == "application/json" {
		raw, _ = io.ReadAll(r.Body)
	}
	_ = r.ParseForm()
	s.mu.Lock()
	s.bodies = append(s.bodies, raw)
	s.reqs = append(s.reqs, r)
	s.forms = append(s.forms, r.PostForm)
	status, body, delay := s.status, s.body, s.delay
	s.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *intuitStub) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }

const stubOKBody = `{"access_token":"a-%s","refresh_token":"r-%s","expires_in":3600,"token_type":"bearer"}`

func stubTokens() string {
	id := uuid.NewString()
	return strings.ReplaceAll(stubOKBody, "%s", id)
}

func quickBooksOver(t *testing.T, cfg intuitConfig, s *intuitStub, timeout time.Duration) *QuickBooks {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	redirect, _ := url.Parse("https://gw.example/erp/callback")
	return newQuickBooksAt(redirect, cfg,
		intuitEndpoints{auth: intuitAuthURL, token: srv.URL + "/token", revoke: srv.URL + "/revoke", api: srv.URL},
		&http.Client{Timeout: timeout})
}

func testIntuitConfig() intuitConfig {
	return intuitConfig{clientID: "v-" + uuid.NewString(), clientSecret: "v-" + uuid.NewString()}
}

func TestQuickBooks_AuthorizeBuildsTheIntuitConsentURL(t *testing.T) {
	cfg := testIntuitConfig()
	redirect, _ := url.Parse("https://gw.example/erp/callback")
	q := NewQuickBooks(redirect, cfg)
	state := "v-" + uuid.NewString()
	res, err := q.Authorize(context.Background(), AuthorizeRequest{State: state})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	u, err := url.Parse(res.ConsentURL)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != "https://appcenter.intuit.com/connect/oauth2" {
		t.Fatalf("consent URL = %q (%v)", res.ConsentURL, err)
	}
	want := url.Values{
		"client_id":     {cfg.clientID},
		"redirect_uri":  {redirect.String()},
		"response_type": {"code"},
		"scope":         {"com.intuit.quickbooks.accounting"},
		"state":         {state},
	}
	got := u.Query()
	if len(got) != len(want) {
		t.Errorf("query = %v, want exactly the keys %v", got, want)
	}
	for k, v := range want {
		if got.Get(k) != v[0] {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v[0])
		}
	}
}

func TestQuickBooks_AuthorizeSendsTheRedirectAsRegistered(t *testing.T) {
	const reg = "https://gateway-nightly-123.africa-south1.run.app/erp/callback"
	redirect, err := url.Parse(reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewQuickBooks(redirect, testIntuitConfig()).Authorize(context.Background(), AuthorizeRequest{State: "v-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.ConsentURL)
	if got := u.Query().Get("redirect_uri"); got != reg {
		t.Errorf("redirect_uri = %q, want %q", got, reg)
	}
}

func TestQuickBooks_AuthorizeMakesNoCallAndNeedsAState(t *testing.T) {
	s := &intuitStub{status: 200, body: "{}"}
	q := quickBooksOver(t, testIntuitConfig(), s, time.Second)
	if _, err := q.Authorize(context.Background(), AuthorizeRequest{State: ""}); err == nil {
		t.Error("empty state: want an error")
	}
	if _, err := q.Authorize(context.Background(), AuthorizeRequest{State: "v-" + uuid.NewString()}); err != nil {
		t.Errorf("Authorize: %v", err)
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests, want 0", n)
	}
}

func TestQuickBooks_ExchangePostsTheCode(t *testing.T) {
	cfg := testIntuitConfig()
	s := &intuitStub{status: 200, body: stubTokens()}
	q := quickBooksOver(t, cfg, s, 5*time.Second)
	code := "v-" + uuid.NewString()
	if _, err := q.Exchange(context.Background(), ExchangeRequest{Code: code, RealmID: "4620816365"}); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("%d requests, want 1", s.count())
	}
	r, form := s.reqs[0], s.forms[0]
	if r.Method != http.MethodPost || r.URL.Path != "/token" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != code || form.Get("redirect_uri") != q.redirect.String() {
		t.Errorf("form = %v", form)
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != cfg.clientID || pass != cfg.clientSecret {
		t.Errorf("Authorization header does not decode to the client id and secret (ok %v)", ok)
	}
}

func TestQuickBooks_ExchangeReturnsTheRealmAndARefreshGrant(t *testing.T) {
	access, refresh := "a-"+uuid.NewString(), "r-"+uuid.NewString()
	body, _ := json.Marshal(map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600, "token_type": "bearer"})
	s := &intuitStub{status: 200, body: string(body)}
	q := quickBooksOver(t, testIntuitConfig(), s, 5*time.Second)
	res, err := q.Exchange(context.Background(), ExchangeRequest{Code: "v-" + uuid.NewString(), RealmID: "4620816365"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalKey != "4620816365" || res.Grant.Kind != GrantOAuthRefresh {
		t.Errorf("key %q kind %q", res.ExternalKey, res.Grant.Kind)
	}
	if res.Grant.ExpiresAt == nil || !near(*res.Grant.ExpiresAt, time.Now().Add(time.Hour), 5*time.Second) {
		t.Errorf("ExpiresAt = %v, want about now + 1h", res.Grant.ExpiresAt)
	}
	var tok qbToken
	if err := json.Unmarshal(res.Grant.Secret, &tok); err != nil || tok.Access != access || tok.Refresh != refresh {
		t.Errorf("secret does not hold both tokens (err %v)", err)
	}
}

func TestQuickBooks_ExchangeRefusesABadRealmBeforeAnyCall(t *testing.T) {
	rows := []struct {
		name, code, realm string
		ok                bool
	}{
		{"empty code", "", "4620816365", false},
		{"empty realm", "c", "", false},
		{"non-digit", "c", "12a4", false},
		{"65 digits", "c", strings.Repeat("7", 65), false},
		{"64 digits", "c", strings.Repeat("7", 64), true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := &intuitStub{status: 200, body: stubTokens()}
			q := quickBooksOver(t, testIntuitConfig(), s, 5*time.Second)
			_, err := q.Exchange(context.Background(), ExchangeRequest{Code: r.code, RealmID: r.realm})
			want := 0
			if r.ok {
				want = 1
			}
			if (err == nil) != r.ok || s.count() != want {
				t.Errorf("err %v, %d requests, want ok=%v and %d requests", err, s.count(), r.ok, want)
			}
		})
	}
}

func TestQuickBooks_ExchangeFailures(t *testing.T) {
	rows := []struct {
		name   string
		status int
		body   string
		delay  time.Duration
	}{
		{"400", 400, `{"error":"invalid_grant"}`, 0},
		{"500", 500, "upstream down", 0},
		{"no refresh token", 200, `{"access_token":"x","expires_in":3600}`, 0},
		{"no expiry", 200, `{"access_token":"x","refresh_token":"y"}`, 0},
		{"timeout", 200, stubTokens(), 300 * time.Millisecond},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := &intuitStub{status: r.status, body: r.body, delay: r.delay}
			q := quickBooksOver(t, testIntuitConfig(), s, 50*time.Millisecond)
			if _, err := q.Exchange(context.Background(), ExchangeRequest{Code: "c", RealmID: "123"}); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestQuickBooks_ExchangeErrorsQuoteNothing(t *testing.T) {
	cfg := testIntuitConfig()
	code, realm := "v-"+uuid.NewString(), "9"+strings.Repeat("3", 15)
	replies := []struct {
		status int
		body   string
	}{
		{400, `{"error":"invalid_grant","error_description":"leak-` + uuid.NewString() + `"}`},
		{500, "leak-" + uuid.NewString()},
		{200, `{"access_token":"leak-` + uuid.NewString() + `"}`},
	}
	for _, rp := range replies {
		s := &intuitStub{status: rp.status, body: rp.body}
		q := quickBooksOver(t, cfg, s, 5*time.Second)
		_, err := q.Exchange(context.Background(), ExchangeRequest{Code: code, RealmID: realm})
		if err == nil {
			t.Fatalf("status %d: want an error", rp.status)
		}
		msg := err.Error()
		for _, canary := range []string{code, realm, cfg.clientSecret, "leak-", "invalid_grant", "upstream"} {
			if strings.Contains(msg, canary) {
				t.Errorf("status %d: error %q contains %q", rp.status, msg, canary)
			}
		}
	}
	q := quickBooksOver(t, cfg, &intuitStub{status: 200, body: "{}"}, time.Second)
	_, err := q.Exchange(context.Background(), ExchangeRequest{Code: code, RealmID: "x" + realm})
	if err == nil || strings.Contains(err.Error(), realm) || strings.Contains(err.Error(), code) {
		t.Errorf("bad realm error = %v", err)
	}
}

func TestQuickBooks_UnsupportedMethods(t *testing.T) {
	redirect, _ := url.Parse("https://gw.example/erp/callback")
	q := NewQuickBooks(redirect, intuitConfig{clientID: "v-" + uuid.NewString()})
	ctx, c := context.Background(), Credentials{}
	if q.System() != "quickbooks" || q.Capabilities() != (Capabilities{}) {
		t.Errorf("System %q, Capabilities %+v", q.System(), q.Capabilities())
	}
	if m := q.DefaultMapping(); m == nil || len(m) != 0 {
		t.Errorf("DefaultMapping = %#v, want empty non-nil", m)
	}
	errs := map[string]error{}
	_, errs["ListChanged"] = q.ListChanged(ctx, c, "", nil)
	_, errs["Fetch"] = q.Fetch(ctx, c, DocRef{})
	_, errs["FetchParty"] = q.FetchParty(ctx, c, "", "")
	_, errs["WriteBack"] = q.WriteBack(ctx, c, DocRef{}, WriteBack{})
	_, errs["VerifyWebhook"] = q.VerifyWebhook(ctx, nil, nil)
	errs["EnsureSubscriptions"] = q.EnsureSubscriptions(ctx, c, "")
	if len(errs) != 6 {
		t.Fatalf("%d methods checked", len(errs))
	}
	for name, err := range errs {
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestQuickBooks_RevokePostsTheRefreshToken(t *testing.T) {
	cfg := testIntuitConfig()
	s := &intuitStub{status: 200, body: "{}"}
	q := quickBooksOver(t, cfg, s, 5*time.Second)
	r1 := "r-" + uuid.NewString()
	creds := Credentials{Kind: GrantOAuthRefresh, Secret: qbSecret("a1", r1)}
	if err := q.Revoke(context.Background(), creds); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("%d requests, want 1", s.count())
	}
	r := s.reqs[0]
	id, secret, ok := r.BasicAuth()
	if r.Method != http.MethodPost || r.URL.Path != "/revoke" || !ok || id != cfg.clientID || secret != cfg.clientSecret {
		t.Errorf("request = %s %s basic %v", r.Method, r.URL.Path, ok)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if body := s.bodies[0]; string(body) != `{"token":"`+r1+`"}` {
		t.Errorf("body = %s", body)
	}

	canary := "canary-" + uuid.NewString()
	bad := quickBooksOver(t, cfg, &intuitStub{status: 400, body: canary}, 5*time.Second)
	err := bad.Revoke(context.Background(), creds)
	if err == nil || strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "400") {
		t.Errorf("400 error = %v, want the status and no body", err)
	}
	none := &intuitStub{status: 200, body: "{}"}
	if err := quickBooksOver(t, cfg, none, time.Second).Revoke(context.Background(), Credentials{Secret: []byte("junk")}); err == nil || none.count() != 0 {
		t.Errorf("unreadable secret: err %v, %d requests, want an error and none", err, none.count())
	}
}

func qbSecret(access, refresh string) []byte {
	b, _ := json.Marshal(qbToken{Access: access, Refresh: refresh})
	return b
}

func TestQuickBooks_RefreshPostsTheStoredRefreshToken(t *testing.T) {
	cfg := testIntuitConfig()
	s := &intuitStub{status: 200, body: stubTokens()}
	q := quickBooksOver(t, cfg, s, 5*time.Second)
	r1 := "r-" + uuid.NewString()
	if _, err := q.Refresh(context.Background(), Credentials{Kind: GrantOAuthRefresh, Secret: qbSecret("a1", r1)}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("%d requests, want 1", s.count())
	}
	r, form := s.reqs[0], s.forms[0]
	if r.Method != http.MethodPost || r.URL.Path != "/token" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != r1 {
		t.Errorf("form = %v", form)
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != cfg.clientID || pass != cfg.clientSecret {
		t.Errorf("Authorization header does not decode to the client id and secret (ok %v)", ok)
	}
}

func TestQuickBooks_RefreshKeepsTheTokenIntuitReturns(t *testing.T) {
	rows := []struct{ name, body, wantRefresh string }{
		{"rotated", `{"access_token":"a2","refresh_token":"r2","expires_in":3600}`, "r2"},
		{"omitted", `{"access_token":"a2","expires_in":3600}`, "r1"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: 200, body: r.body}, 5*time.Second)
			got, err := q.Refresh(context.Background(), Credentials{Secret: qbSecret("a1", "r1")})
			if err != nil {
				t.Fatal(err)
			}
			var tok qbToken
			if err := json.Unmarshal(got.Secret, &tok); err != nil || tok.Access != "a2" || tok.Refresh != r.wantRefresh {
				t.Errorf("secret = %+v (%v), want access a2 refresh %s", tok, err, r.wantRefresh)
			}
			if got.Kind != GrantOAuthRefresh || got.ExpiresAt == nil || !near(*got.ExpiresAt, time.Now().Add(time.Hour), 5*time.Second) {
				t.Errorf("kind %q ExpiresAt %v, want about now + 1h", got.Kind, got.ExpiresAt)
			}
		})
	}
}

func TestQuickBooks_RefreshInvalidGrantIsRevoked(t *testing.T) {
	rows := []struct {
		name    string
		status  int
		body    string
		delay   time.Duration
		revoked bool
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant"}`, 0, true},
		{"invalid_client", 400, `{"error":"invalid_client"}`, 0, false},
		{"500", 500, "upstream down", 0, false},
		{"timeout", 200, stubTokens(), 300 * time.Millisecond, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: r.status, body: r.body, delay: r.delay}, 50*time.Millisecond)
			_, err := q.Refresh(context.Background(), Credentials{Secret: qbSecret("a1", "r1")})
			if err == nil || errors.Is(err, ErrGrantRevoked) != r.revoked {
				t.Errorf("err = %v, want an error with revoked=%v", err, r.revoked)
			}
		})
	}
}

func TestQuickBooks_RefreshUnreadableSecret(t *testing.T) {
	for name, secret := range map[string][]byte{"empty": nil, "bad json": []byte("{"), "no refresh": []byte(`{"access":"a"}`)} {
		t.Run(name, func(t *testing.T) {
			s := &intuitStub{status: 200, body: stubTokens()}
			q := quickBooksOver(t, testIntuitConfig(), s, time.Second)
			_, err := q.Refresh(context.Background(), Credentials{Secret: secret})
			if err == nil || errors.Is(err, ErrGrantRevoked) || s.count() != 0 {
				t.Errorf("err %v, %d requests, want a non-revoked error and 0 requests", err, s.count())
			}
		})
	}
}

func TestQuickBooks_RefreshNoExpiryIsAnError(t *testing.T) {
	q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: 200, body: `{"access_token":"a2","refresh_token":"r2"}`}, time.Second)
	if _, err := q.Refresh(context.Background(), Credentials{Secret: qbSecret("a1", "r1")}); err == nil {
		t.Error("want an error for a reply with no expiry")
	}
}

func TestQuickBooks_RefreshErrorsQuoteNothing(t *testing.T) {
	refresh := "canary-r-" + uuid.NewString()
	leak := "leak-" + uuid.NewString()
	replies := []struct {
		status int
		body   string
	}{
		{400, `{"error":"invalid_grant","error_description":"` + leak + `"}`},
		{400, `{"error":"invalid_client","error_description":"` + leak + `"}`},
		{500, leak + " " + refresh},
		{200, `{"access_token":"` + leak + `"}`},
	}
	for _, rp := range replies {
		q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: rp.status, body: rp.body}, 5*time.Second)
		_, err := q.Refresh(context.Background(), Credentials{Secret: qbSecret("canary-a", refresh)})
		if err == nil {
			t.Fatalf("status %d: want an error", rp.status)
		}
		for _, canary := range []string{refresh, "canary-a", leak, "invalid_client", "upstream"} {
			if strings.Contains(err.Error(), canary) {
				t.Errorf("status %d: error %q contains %q", rp.status, err.Error(), canary)
			}
		}
	}
	_, err := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: 200, body: "{}"}, time.Second).
		Refresh(context.Background(), Credentials{Secret: []byte(`{"refresh":"` + refresh + `","x`)})
	if err == nil || strings.Contains(err.Error(), refresh) {
		t.Errorf("unreadable secret error = %v", err)
	}
}

func TestQuickBooks_ValidateReadsCompanyInfo(t *testing.T) {
	s := &intuitStub{status: 200, body: `{"CompanyInfo":{"Id":"1"}}`}
	q := quickBooksOver(t, testIntuitConfig(), s, 5*time.Second)
	if err := q.Validate(context.Background(), Credentials{Secret: qbSecret("a1", "r1")}, "4620816365"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("%d requests, want 1", s.count())
	}
	r := s.reqs[0]
	if r.Method != http.MethodGet || r.URL.Path != "/v3/company/4620816365/companyinfo/4620816365" || r.URL.Query().Get("minorversion") != "75" {
		t.Errorf("request = %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer a1" || r.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", r.Header)
	}
}

func TestQuickBooks_ValidateUnknownRealm(t *testing.T) {
	for _, status := range []int{403, 404} {
		s := &intuitStub{status: status, body: "{}"}
		q := quickBooksOver(t, testIntuitConfig(), s, 5*time.Second)
		if err := q.Validate(context.Background(), Credentials{Secret: qbSecret("a1", "r1")}, "123"); !errors.Is(err, ErrUnknownScope) {
			t.Errorf("status %d: %v", status, err)
		}
	}
	for _, scope := range []string{"", "abc"} {
		s := &intuitStub{status: 200, body: "{}"}
		q := quickBooksOver(t, testIntuitConfig(), s, 5*time.Second)
		if err := q.Validate(context.Background(), Credentials{Secret: qbSecret("a1", "r1")}, scope); !errors.Is(err, ErrUnknownScope) {
			t.Errorf("scope %q: %v", scope, err)
		}
		if s.count() != 0 {
			t.Errorf("scope %q: %d requests", scope, s.count())
		}
	}
}

func TestQuickBooks_ValidateOtherAnswers(t *testing.T) {
	cr := Credentials{Secret: qbSecret("a1", "r1")}
	run := func(status int, body string) error {
		q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: status, body: body}, 5*time.Second)
		return q.Validate(context.Background(), cr, "123")
	}
	if err := run(401, "{}"); err == nil || errors.Is(err, ErrUnknownScope) {
		t.Errorf("401: %v", err)
	}
	var rl *RateLimitError
	if err := run(429, "{}"); !errors.As(err, &rl) || rl.RetryAfter != 60*time.Second {
		t.Errorf("429: %v", err)
	}
	if err := run(200, "{}"); err == nil {
		t.Error("200 without CompanyInfo: nil error")
	}
}

func TestQuickBooks_ValidateErrorsQuoteNothing(t *testing.T) {
	access, canary, realm := "a-"+uuid.NewString(), "c-"+uuid.NewString(), "4620816365"
	for _, status := range []int{401, 500, 403, 429} {
		q := quickBooksOver(t, testIntuitConfig(), &intuitStub{status: status, body: canary}, 5*time.Second)
		err := q.Validate(context.Background(), Credentials{Secret: qbSecret(access, "r1")}, realm)
		if err == nil {
			t.Fatalf("status %d: nil error", status)
		}
		for _, bad := range []string{access, canary, realm} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("status %d: error quotes %q: %v", status, bad, err)
			}
		}
	}
}
