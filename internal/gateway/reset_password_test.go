package gateway

import (
	"bytes"
	"context"
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

	"golang.org/x/net/html"
)

const (
	rpPath     = "/auth/reset-password"
	rpToken    = "tok-Abc123_xyz"
	rpEmail    = "ada@corp.example"
	rpPass     = "n3w-Passw0rd"
	rpAccess   = "at1"
	rpFailed   = "https://site.example/?reset=failed"
	rpOK       = "https://site.example/?reset=1"
	rpAlert    = "Use a password of 6 to 72 characters."
	rpFormType = "application/x-www-form-urlencoded"

	rpMsgSignOutFailed = "reset-password: global sign-out failed"
	rpMsgIncomplete    = "reset-password: gotrue verify answer incomplete"
	rpMsgRefusedLink   = "reset-password: gotrue refused the link"
	rpMsgRefusedPass   = "reset-password: gotrue refused the password"
	rpMsgUnreachable   = "reset-password: gotrue unreachable"

	rpSamePassword = `{"code":422,"error_code":"same_password","msg":"New password should be different from the old password."}`
	rpWeakPassword = `{"code":422,"error_code":"weak_password","msg":"Password should be at least 6 characters."}`
	rpOTPExpired   = `{"code":403,"error_code":"otp_expired","msg":"Email link is invalid or has expired"}`
)

// rpCall is one request GoTrue saw, in arrival order across every route.
type rpCall struct {
	Method, Path, RawQuery, Auth, ContentType string
	Body                                      []byte
}

func (c rpCall) String() string { return c.Method + " " + c.Path }

// resetGoTrue is GoTrue's /verify, PUT /user and /logout, matched by path suffix. Each route
// defaults to success; a test swaps a field before the first request.
type resetGoTrue struct {
	URL                  *url.URL
	mu                   sync.Mutex
	calls                []rpCall
	verify, user, logout http.HandlerFunc
}

func rpVerifyAnswer(id, email string) http.HandlerFunc {
	b, _ := json.Marshal(map[string]any{"access_token": rpAccess, "user": map[string]string{"id": id, "email": email}})
	return answer(http.StatusOK, string(b))
}

func newResetGoTrue(t *testing.T) *resetGoTrue {
	t.Helper()
	f := &resetGoTrue{
		verify: rpVerifyAnswer(subjectS1, rpEmail),
		user:   answer(http.StatusOK, `{}`),
		logout: answer(http.StatusNoContent, ""),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, rpCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), b})
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/verify"):
			f.verify(w, r)
		case strings.HasSuffix(r.URL.Path, "/user"):
			f.user(w, r)
		case strings.HasSuffix(r.URL.Path, "/logout"):
			f.logout(w, r)
		default:
			http.NotFound(w, r)
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

func (f *resetGoTrue) Calls() []rpCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *resetGoTrue) count(method, path string) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func (f *resetGoTrue) names() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.String())
	}
	return out
}

func redirectTo(loc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loc, http.StatusFound) }
}

// newResetHandler builds the form handler against f with a live session cache and a fresh sign-in throttle.
func newResetHandler(t *testing.T, f *resetGoTrue, log *slog.Logger) http.Handler {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	return ResetPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, log)
}

// resetRig is the form handler sharing one session cache and one throttle with the checks that observe them:
// S1's verdict is cached live and rpEmail has used up its sign-in failures.
type resetRig struct {
	*sessionRig
	user     *userFake
	gotrue   *resetGoTrue
	throttle *SignInThrottle
	handler  http.Handler
	log      *bytes.Buffer
	tokS1    string
}

func newResetRig(t *testing.T) *resetRig {
	t.Helper()
	user := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, user.URL, nil, nil)
	f := newResetGoTrue(t)
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	for i := range SignInMaxFailures {
		if !th.Reserve(rpEmail) {
			t.Fatalf("priming failure %d was refused", i+1)
		}
	}
	log, buf := captureLog()
	r := &resetRig{
		sessionRig: rg, user: user, gotrue: f, throttle: th, log: buf,
		handler: ResetPasswordHandler(f.URL, siteURL(t), testClient(), rg.sessions, th, log),
		tokS1:   rg.signer.token(t, subjectS1, sid1),
	}
	if rec := rg.get(r.tokS1); rec.Code != http.StatusOK || user.Hits() != 1 {
		t.Fatalf("priming S1: status = %d, /user calls = %d, want 200 and 1", rec.Code, user.Hits())
	}
	if th.Reserve(rpEmail) {
		t.Fatal("priming: the address still has attempts left after 10 failures")
	}
	return r
}

// evicted reports whether S1's next request had to ask GoTrue again.
func (r *resetRig) evicted() bool {
	before := r.user.Hits()
	r.get(r.tokS1)
	return r.user.Hits() > before
}

// throttleReset reports whether the address may fail again; it spends one attempt when it may.
func (r *resetRig) throttleReset() bool { return r.throttle.Reserve(rpEmail) }

func rpValues(token, typ, password string) url.Values {
	return url.Values{"token": {token}, "type": {typ}, "password": {password}}
}

func rpPostRaw(ctx context.Context, h http.Handler, target, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func rpPost(t *testing.T, h http.Handler, v url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return rpPostRaw(t.Context(), h, rpPath, rpFormType, v.Encode())
}

func rpRequireRedirect(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("answer = %d Location %q, want 303 %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

// rpRequireWarn requires exactly one WARN in buf, with msg and the given attributes.
func rpRequireWarn(t *testing.T, buf *bytes.Buffer, msg string, attrs map[string]any) {
	t.Helper()
	recs := recordsNamed(t, buf, msg)
	if len(recs) != 1 || warnCount(t, buf) != 1 {
		t.Fatalf("want exactly one WARN %q and no other, got %d of it among %d WARNs: %s", msg, len(recs), warnCount(t, buf), buf.String())
	}
	if recs[0]["level"] != "WARN" {
		t.Errorf("%q level = %v, want WARN", msg, recs[0]["level"])
	}
	for k, want := range attrs {
		if recs[0][k] != want {
			t.Errorf("%q attribute %s = %v, want %v", msg, k, recs[0][k], want)
		}
	}
}

func rpJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", b, err)
	}
	return m
}

func rpPageHandler(t *testing.T) http.Handler {
	t.Helper()
	h := ResetPasswordPageHandler(siteURL(t))
	if h == nil {
		t.Fatal("ResetPasswordPageHandler returned a nil handler")
	}
	return h
}

func rpGet(h http.Handler, method, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, rpPath+"?"+query, nil))
	return rec
}

func rpQuery(token string) string { return "token=" + url.QueryEscape(token) + "&type=recovery" }

// rpPage serves the page for token and fails unless it is a 200 with a parsed document.
func rpPage(t *testing.T, token string) (*httptest.ResponseRecorder, *html.Node) {
	t.Helper()
	rec := rpGet(rpPageHandler(t), http.MethodGet, rpQuery(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	return rec, vpParse(t, rec.Body.String())
}

func rpInputs(doc *html.Node) map[string]*html.Node {
	out := map[string]*html.Node{}
	for _, in := range vpFind(doc, vpTag("input")) {
		name, _ := vpAttr(in, "name")
		out[name] = in
	}
	return out
}

func rpTokenInput(t *testing.T, body string) string {
	t.Helper()
	in, ok := rpInputs(vpParse(t, body))["token"]
	if !ok {
		t.Fatalf("page holds no token input: %q", body)
	}
	v, _ := vpAttr(in, "value")
	return v
}

func TestResetPasswordPage_RendersOneFormWithTheToken(t *testing.T) {
	rec, doc := rpPage(t, "abc123")
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	forms := vpFind(doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want exactly 1", len(forms))
	}
	if got, _ := vpAttr(forms[0], "method"); got != "post" {
		t.Errorf("form method = %q, want post", got)
	}
	if got, _ := vpAttr(forms[0], "action"); got != rpPath {
		t.Errorf("form action = %q, want %s", got, rpPath)
	}
	inputs := vpFind(forms[0], vpTag("input"))
	if len(inputs) != 3 {
		t.Fatalf("inputs in the form = %d, want exactly 3 (token, type, password)", len(inputs))
	}
	byName := rpInputs(doc)
	for name, want := range map[string]string{"token": "abc123", "type": "recovery"} {
		in := byName[name]
		if in == nil {
			t.Errorf("no %q input", name)
			continue
		}
		if typ, _ := vpAttr(in, "type"); typ != "hidden" {
			t.Errorf("input %q type = %q, want hidden", name, typ)
		}
		if v, _ := vpAttr(in, "value"); v != want {
			t.Errorf("input %q value = %q, want %q", name, v, want)
		}
	}
	pw := byName["password"]
	if pw == nil {
		t.Fatal("no password input")
	}
	for k, want := range map[string]string{"type": "password", "autocomplete": "new-password", "minlength": "6", "maxlength": "72"} {
		if got, ok := vpAttr(pw, k); !ok || got != want {
			t.Errorf("password input %s = %q (present %v), want %q", k, got, ok, want)
		}
	}
	if _, ok := vpAttr(pw, "required"); !ok {
		t.Error("password input is not required")
	}
	id, _ := vpAttr(pw, "id")
	labels := vpFind(doc, func(n *html.Node) bool {
		f, _ := vpAttr(n, "for")
		return n.Data == "label" && f == "password"
	})
	if id != "password" || len(labels) != 1 || strings.TrimSpace(vpText(labels[0])) == "" {
		t.Errorf("password input id = %q with %d <label for=password>; want id password and one label with text", id, len(labels))
	}
	submits := vpFind(doc, func(n *html.Node) bool {
		typ, has := vpAttr(n, "type")
		return (n.Data == "button" && (!has || typ == "submit")) || (n.Data == "input" && (typ == "submit" || typ == "image"))
	})
	if len(submits) != 1 || submits[0].Data != "button" || strings.TrimSpace(vpText(submits[0])) != "Set new password" {
		t.Fatalf("submit controls = %d, want one <button> reading %q", len(submits), "Set new password")
	}
	if strings.Contains(rec.Body.String(), rpAlert) {
		t.Error("a first view shows the password alert")
	}
}

func TestResetPasswordPage_HeadersAndScriptHash(t *testing.T) {
	h := rpPageHandler(t)
	get := rpGet(h, http.MethodGet, rpQuery(rpToken))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	doc := vpParse(t, get.Body.String())
	script := vpScript(t, doc)
	if strings.TrimSpace(script) == "" {
		t.Fatal("the inline script is empty")
	}
	wantCSP := strings.Replace(vpWantCSP, "%s", vpHash(script), 1)
	head := rpGet(h, http.MethodHead, rpQuery(rpToken))
	for name, rec := range map[string]*httptest.ResponseRecorder{"GET": get, "HEAD": head} {
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", name, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", name, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s Referrer-Policy = %q, want no-referrer", name, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s Content-Security-Policy = %q, want %q", name, got, wantCSP)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s Content-Type = %q, want text/html; charset=utf-8", name, got)
		}
	}
	if head.Body.Len() != 0 {
		t.Errorf("HEAD body = %q, want empty", head.Body.String())
	}
}

func TestResetPasswordPage_BadLinksRedirectToTheFailedNotice(t *testing.T) {
	h := rpPageHandler(t)
	for name, query := range map[string]string{
		"no token":       "type=recovery",
		"empty token":    "token=&type=recovery",
		"257-byte token": rpQuery(strings.Repeat("a", 257)),
		"type signup":    "token=abc&type=signup",
		"no type":        "token=abc",
		"type magiclink": "token=abc&type=magiclink",
	} {
		t.Run(name, func(t *testing.T) {
			rec := rpGet(h, http.MethodGet, query)
			rpRequireRedirect(t, rec, rpFailed)
			if strings.Contains(rec.Body.String(), "<form") {
				t.Errorf("a bad link rendered the form: %q", rec.Body.String())
			}
		})
	}
	rpRequireRedirect(t, rpGet(h, http.MethodHead, "token=&type=recovery"), rpFailed)
	tok := strings.Repeat("a", 256)
	rec := rpGet(h, http.MethodGet, rpQuery(tok))
	if rec.Code != http.StatusOK || rpTokenInput(t, rec.Body.String()) != tok {
		t.Errorf("256-byte token: status %d; want 200 rendering the whole token", rec.Code)
	}
}

func TestResetPasswordPage_EscapesTheTokenAndIgnoresRedirectTo(t *testing.T) {
	raw := `"><script>alert(1)</script>`
	rec := rpGet(rpPageHandler(t), http.MethodGet, rpQuery(raw)+"&redirect_to="+url.QueryEscape("https://evil.example/x"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "&#34;&gt;&lt;script&gt;") {
		t.Errorf("the token is not HTML-escaped in the page: %q", body)
	}
	if strings.Contains(body, "evil.example") {
		t.Error("the page renders redirect_to")
	}
	if got := rpTokenInput(t, body); got != raw {
		t.Errorf("token input value = %q, want %q", got, raw)
	}
	if n := len(vpFind(vpParse(t, body), vpTag("script"))); n != 1 {
		t.Errorf("script elements = %d, want exactly 1 (the token added one)", n)
	}
}

func TestResetPasswordPage_OtherMethodsAre405(t *testing.T) {
	h := rpPageHandler(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, rpPath+"?"+rpQuery(rpToken), nil))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s: status %d Allow %q, want 405 GET, HEAD", m, rec.Code, rec.Header().Get("Allow"))
			}
		})
	}
	if rec := rpGet(h, http.MethodGet, rpQuery(rpToken)); rec.Code != http.StatusOK {
		t.Errorf("GET control: status = %d, want 200", rec.Code)
	}
}

func TestResetPassword_NotConfigured503(t *testing.T) {
	f := newResetGoTrue(t)
	handlers := map[string]*httptest.ResponseRecorder{}
	page := httptest.NewRecorder()
	ResetPasswordPageHandler(nil).ServeHTTP(page, httptest.NewRequest(http.MethodGet, rpPath+"?"+rpQuery(rpToken), nil))
	handlers["page"] = page
	form := ResetPasswordHandler(f.URL, nil, testClient(), liveSessions(t), NewSignInThrottle("sign-in", 10, 10, SignInWindow, newTestClock().Now), slog.New(slog.DiscardHandler))
	handlers["form"] = rpPostRaw(t.Context(), form, rpPath, rpFormType, rpValues(rpToken, "recovery", rpPass).Encode())
	for name, rec := range handlers {
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "registration is not configured") {
			t.Errorf("%s: answer = %d %q, want 503 registration is not configured", name, rec.Code, rec.Body.String())
		}
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("an unconfigured form handler called GoTrue: %v", got)
	}
}

func TestResetPassword_VerifiesSetsThePasswordThenSignsOutEverywhere(t *testing.T) {
	f := newResetGoTrue(t)
	f.verify = answer(http.StatusOK, `{"access_token":"at1","user":{"id":"u1","email":"ada@corp.example"}}`)

	rec := rpPost(t, newResetHandler(t, f, nil), rpValues(rpToken, "recovery", rpPass))

	rpRequireRedirect(t, rec, rpOK)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	want := []string{"POST /verify", "PUT /user", "POST /logout"}
	if got := f.names(); !slices.Equal(got, want) {
		t.Fatalf("GoTrue calls = %v, want %v in that order", got, want)
	}
	calls := f.Calls()
	if got, want := rpJSON(t, calls[0].Body), map[string]any{"type": "recovery", "token_hash": rpToken}; !maps.Equal(got, want) {
		t.Errorf("verify body = %v, want exactly %v", got, want)
	}
	if got, want := rpJSON(t, calls[1].Body), map[string]any{"password": rpPass}; !maps.Equal(got, want) {
		t.Errorf("PUT /user body = %v, want exactly %v", got, want)
	}
	for _, i := range []int{0, 1} {
		if !strings.HasPrefix(calls[i].ContentType, "application/json") {
			t.Errorf("%s Content-Type = %q, want application/json", calls[i], calls[i].ContentType)
		}
	}
	if calls[1].Auth != "Bearer at1" || calls[2].Auth != "Bearer at1" {
		t.Errorf("bearers: PUT %q, logout %q; want Bearer at1 for both", calls[1].Auth, calls[2].Auth)
	}
	if calls[2].RawQuery != "scope=global" {
		t.Errorf("logout query = %q, want scope=global", calls[2].RawQuery)
	}
}

func TestResetPassword_EvictsTheSubjectAndClearsSignInFailures(t *testing.T) {
	rg := newResetRig(t)

	rec := rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass))

	rpRequireRedirect(t, rec, rpOK)
	if !rg.evicted() {
		t.Error("the subject's cached verdicts survive a reset: the next /api/ check did not ask GoTrue")
	}
	if !rg.throttleReset() {
		t.Error("the address is still refused after a reset")
	}

	ctl := newResetRig(t)
	ctl.gotrue.verify = answer(http.StatusForbidden, rpOTPExpired)
	rpRequireRedirect(t, rpPost(t, ctl.handler, rpValues(rpToken, "recovery", rpPass)), rpFailed)
	if n := ctl.gotrue.count(http.MethodPost, "/verify"); n != 1 {
		t.Fatalf("control: verify calls = %d, want 1", n)
	}
	if ctl.evicted() {
		t.Error("control: a refused link evicted the subject")
	}
	if ctl.throttleReset() {
		t.Error("control: a refused link cleared the address's failures")
	}
}

func TestResetPassword_SamePasswordAndSignOutFailureStillSucceed(t *testing.T) {
	rows := []struct {
		name         string
		user, logout http.HandlerFunc
		warn         bool
	}{
		{"same_password, sign-out 204", answer(http.StatusUnprocessableEntity, rpSamePassword), answer(http.StatusNoContent, ""), false},
		{"PUT 200, sign-out 500", nil, answer(http.StatusInternalServerError, gtInternal), true},
		{"PUT 200, sign-out unreachable", nil, dropped, true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			rg := newResetRig(t)
			if c.user != nil {
				rg.gotrue.user = c.user
			}
			rg.gotrue.logout = c.logout

			rec := rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass))

			rpRequireRedirect(t, rec, rpOK)
			if n := rg.gotrue.count(http.MethodPost, "/logout"); n < 1 {
				t.Fatalf("sign-out calls = %d, want at least 1", n)
			}
			if !rg.evicted() {
				t.Error("the subject was not evicted")
			}
			if !rg.throttleReset() {
				t.Error("the address's failures were not cleared")
			}
			if c.warn {
				rpRequireWarn(t, rg.log, rpMsgSignOutFailed, nil)
			} else if n := warnCount(t, rg.log); n != 0 {
				t.Errorf("a clean reset logged %d WARNs: %s", n, rg.log.String())
			}
		})
	}
}

func TestResetPassword_SamePasswordWithAFailedSignOutIsTheFailedNotice(t *testing.T) {
	rg := newResetRig(t)
	rg.gotrue.user = answer(http.StatusUnprocessableEntity, rpSamePassword)
	rg.gotrue.logout = answer(http.StatusInternalServerError, gtInternal)

	rec := rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass))

	rpRequireRedirect(t, rec, rpFailed)
	if n := rg.gotrue.count(http.MethodPost, "/logout"); n != 1 {
		t.Fatalf("sign-out calls = %d, want 1", n)
	}
	rpRequireWarn(t, rg.log, rpMsgSignOutFailed, nil)
	if !rg.evicted() {
		t.Error("the subject was not evicted")
	}
	if rg.throttleReset() {
		t.Error("the address's failures were cleared although no session ended")
	}
}

func TestResetPassword_IncompleteVerifyAnswerIsTheFailedNotice(t *testing.T) {
	rows := map[string]string{
		"empty object":       `{}`,
		"empty access_token": `{"access_token":"","user":{"id":"` + subjectS1 + `","email":"` + rpEmail + `"}}`,
		"no user.id":         `{"access_token":"at1","user":{"email":"` + rpEmail + `"}}`,
		"no user.email":      `{"access_token":"at1","user":{"id":"` + subjectS1 + `"}}`,
		"null user":          `{"access_token":"at1","user":null}`,
		"not JSON":           `<html>bad gateway</html>`,
	}
	for name, body := range rows {
		t.Run(name, func(t *testing.T) {
			rg := newResetRig(t)
			rg.gotrue.verify = answer(http.StatusOK, body)

			rec := rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass))

			rpRequireRedirect(t, rec, rpFailed)
			if got := rg.gotrue.names(); !slices.Equal(got, []string{"POST /verify"}) {
				t.Errorf("GoTrue calls = %v, want only POST /verify", got)
			}
			rpRequireWarn(t, rg.log, rpMsgIncomplete, nil)
			if rg.evicted() {
				t.Error("an incomplete answer evicted a subject")
			}
			if rg.throttleReset() {
				t.Error("an incomplete answer cleared the address's failures")
			}
		})
	}
}

func TestResetPassword_GoTrueRedirectIsNotFollowed(t *testing.T) {
	t.Run("verify 302", func(t *testing.T) {
		f := newResetGoTrue(t)
		f.verify = func(w http.ResponseWriter, r *http.Request) { redirectTo(f.URL.String()+"/user")(w, r) }

		rec := rpPost(t, newResetHandler(t, f, nil), rpValues(rpToken, "recovery", rpPass))

		rpRequireRedirect(t, rec, rpFailed)
		if got := f.names(); !slices.Equal(got, []string{"POST /verify"}) {
			t.Errorf("GoTrue calls = %v, want only POST /verify", got)
		}
	})
	t.Run("PUT /user 302", func(t *testing.T) {
		f := newResetGoTrue(t)
		f.user = func(w http.ResponseWriter, r *http.Request) { redirectTo(f.URL.String()+"/logout")(w, r) }

		rec := rpPost(t, newResetHandler(t, f, nil), rpValues(rpToken, "recovery", rpPass))

		rpRequireRedirect(t, rec, rpFailed)
		if got := f.names(); !slices.Equal(got, []string{"POST /verify", "PUT /user"}) {
			t.Errorf("GoTrue calls = %v, want [POST /verify PUT /user]", got)
		}
	})
}

func TestResetPassword_RefusedLinkOrPasswordIsTheFailedNotice(t *testing.T) {
	rows := []struct {
		name         string
		verify, user http.HandlerFunc
		warn         string
		attrs        map[string]any
		noPut        bool
	}{
		{"verify 403 otp_expired", answer(http.StatusForbidden, rpOTPExpired), nil, rpMsgRefusedLink, map[string]any{"upstream_status": float64(403)}, true},
		{"verify 500", answer(http.StatusInternalServerError, gtInternal), nil, rpMsgRefusedLink, map[string]any{"upstream_status": float64(500)}, true},
		{"verify unreachable", dropped, nil, rpMsgUnreachable, nil, true},
		{"PUT 422 weak_password", nil, answer(http.StatusUnprocessableEntity, rpWeakPassword), rpMsgRefusedPass, map[string]any{"upstream_status": float64(422), "error_code": "weak_password"}, false},
		{"PUT 500", nil, answer(http.StatusInternalServerError, gtInternal), rpMsgRefusedPass, map[string]any{"upstream_status": float64(500)}, false},
		{"PUT unreachable", nil, dropped, rpMsgRefusedPass, nil, false},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)
			if c.verify != nil {
				f.verify = c.verify
			}
			if c.user != nil {
				f.user = c.user
			}
			log, buf := captureLog()

			rec := rpPost(t, newResetHandler(t, f, log), rpValues(rpToken, "recovery", rpPass))

			rpRequireRedirect(t, rec, rpFailed)
			if f.count(http.MethodPost, "/verify") != 1 {
				t.Errorf("verify calls = %d, want 1", f.count(http.MethodPost, "/verify"))
			}
			if c.noPut && f.count(http.MethodPut, "/user") != 0 {
				t.Errorf("PUT /user was sent after a refused link: %v", f.names())
			}
			if !c.noPut && f.count(http.MethodPut, "/user") < 1 {
				t.Errorf("PUT /user was never sent: %v", f.names())
			}
			if n := f.count(http.MethodPost, "/logout"); n != 0 {
				t.Errorf("sign-out calls = %d, want 0", n)
			}
			rpRequireWarn(t, buf, c.warn, c.attrs)
		})
	}
}

func TestResetPassword_PasswordOutsideTheBoundsRerendersThePage(t *testing.T) {
	rows := map[string]string{
		"5 bytes": "abcde", "73 bytes": strings.Repeat("a", 73), "empty": "",
		"5 bytes in 3 runes":   "éé" + "a",
		"75 bytes in 25 runes": strings.Repeat("€", 25),
	}
	pageCSP := rpGet(rpPageHandler(t), http.MethodGet, rpQuery(rpToken)).Header().Get("Content-Security-Policy")
	for name, pw := range rows {
		t.Run(name, func(t *testing.T) {
			f := newResetGoTrue(t)

			rec := rpPost(t, newResetHandler(t, f, nil), rpValues(rpToken, "recovery", pw))

			if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("answer = %d %q, want 400 text/html", rec.Code, rec.Header().Get("Content-Type"))
			}
			if !strings.Contains(rec.Body.String(), rpAlert) {
				t.Errorf("the page lacks the alert %q: %q", rpAlert, rec.Body.String())
			}
			if got := rpTokenInput(t, rec.Body.String()); got != rpToken {
				t.Errorf("hidden token = %q, want the posted %q", got, rpToken)
			}
			if got := f.names(); len(got) != 0 {
				t.Errorf("GoTrue calls = %v, want none", got)
			}
			doc := vpParse(t, rec.Body.String())
			if n := len(vpFind(doc, vpTag("form"))); n != 1 {
				t.Errorf("re-rendered forms = %d, want 1", n)
			}
			if _, ok := rpInputs(doc)["password"]; !ok {
				t.Error("the re-rendered page holds no password input")
			}
			if n := len(vpFind(doc, func(n *html.Node) bool { r, _ := vpAttr(n, "role"); return r == "alert" })); n != 1 {
				t.Errorf("alert elements = %d, want 1", n)
			}
			for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "Content-Security-Policy": pageCSP} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if want := strings.Replace(vpWantCSP, "%s", vpHash(vpScript(t, doc)), 1); rec.Header().Get("Content-Security-Policy") != want {
				t.Errorf("CSP does not hash the re-rendered page's script: %q", rec.Header().Get("Content-Security-Policy"))
			}
		})
	}
	t.Run("hostile token is escaped in the re-render", func(t *testing.T) {
		raw := `"><script>alert(1)</script>`
		rec := rpPost(t, newResetHandler(t, newResetGoTrue(t), nil), rpValues(raw, "recovery", "abcde"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if got := rpTokenInput(t, rec.Body.String()); got != raw {
			t.Errorf("hidden token = %q, want %q", got, raw)
		}
		if n := len(vpFind(vpParse(t, rec.Body.String()), vpTag("script"))); n != 1 {
			t.Errorf("script elements = %d, want 1", n)
		}
	})
}

func TestResetPassword_PasswordAtTheBoundsIsSent(t *testing.T) {
	for name, pw := range map[string]string{
		"6 bytes": "abcdef", "72 bytes": strings.Repeat("a", 72),
		"6 bytes in 3 runes":       "ééé",
		"72 bytes in 24 runes":     strings.Repeat("€", 24),
		"quotes, spaces and signs": ` p"a\ss&w+o%rd=` + "\t ",
	} {
		t.Run(name, func(t *testing.T) {
			f := newResetGoTrue(t)

			rec := rpPost(t, newResetHandler(t, f, nil), rpValues(rpToken, "recovery", pw))

			rpRequireRedirect(t, rec, rpOK)
			var put *rpCall
			for _, c := range f.Calls() {
				if c.Method == http.MethodPut {
					put = &c
				}
			}
			if put == nil {
				t.Fatalf("PUT /user was never sent: %v", f.names())
			}
			if got := rpJSON(t, put.Body)["password"]; got != pw {
				t.Errorf("PUT /user password = %v, want the posted one", got)
			}
		})
	}
}

func TestResetPassword_BadFormsAreTheFailedNoticeWithoutACall(t *testing.T) {
	good := rpValues(rpToken, "recovery", "newpass1").Encode()
	const bound = "BOUND"
	multipart := "--" + bound + "\r\nContent-Disposition: form-data; name=\"token\"\r\n\r\n" + rpToken +
		"\r\n--" + bound + "\r\nContent-Disposition: form-data; name=\"type\"\r\n\r\nrecovery" +
		"\r\n--" + bound + "\r\nContent-Disposition: form-data; name=\"password\"\r\n\r\nnewpass1\r\n--" + bound + "--\r\n"
	big := good + "&pad=" + strings.Repeat("a", 2049-len(good)-len("&pad="))
	rows := []struct{ name, target, contentType, body string }{
		{"parse error", rpPath, rpFormType, "%zz"},
		{"multipart", rpPath, "multipart/form-data; boundary=" + bound, multipart},
		{"JSON", rpPath, "application/json", `{"token":"` + rpToken + `","type":"recovery","password":"newpass1"}`},
		{"2049-byte body", rpPath, rpFormType, big},
		{"empty token", rpPath, rpFormType, rpValues("", "recovery", "newpass1").Encode()},
		{"257-byte token", rpPath, rpFormType, rpValues(strings.Repeat("a", 257), "recovery", "newpass1").Encode()},
		{"type signup", rpPath, rpFormType, rpValues(rpToken, "signup", "newpass1").Encode()},
		{"token only in the query", rpPath + "?token=" + rpToken, rpFormType, "type=recovery&password=newpass1"},
		{"type only in the query", rpPath + "?type=recovery", rpFormType, "token=" + rpToken + "&password=newpass1"},
		{"no body", rpPath + "?token=" + rpToken + "&type=recovery", rpFormType, ""},
	}
	if len(big) != 2049 {
		t.Fatalf("test body is %d bytes, want 2049", len(big))
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)

			rec := rpPostRaw(t.Context(), newResetHandler(t, f, nil), c.target, c.contentType, c.body)

			rpRequireRedirect(t, rec, rpFailed)
			if got := f.names(); len(got) != 0 {
				t.Errorf("GoTrue calls = %v, want none", got)
			}
		})
	}
	t.Run("control: the same form without the defect", func(t *testing.T) {
		f := newResetGoTrue(t)
		rpRequireRedirect(t, rpPostRaw(t.Context(), newResetHandler(t, f, nil), rpPath, rpFormType, good), rpOK)
		if f.count(http.MethodPost, "/verify") != 1 {
			t.Errorf("GoTrue calls = %v, want one POST /verify", f.names())
		}
	})
}

func TestResetPassword_ClientGoneAfterVerifyStillFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newResetGoTrue(t)
	ok := rpVerifyAnswer(subjectS1, rpEmail)
	f.verify = func(w http.ResponseWriter, r *http.Request) {
		cancel()
		ok(w, r)
	}

	rec := rpPostRaw(ctx, newResetHandler(t, f, nil), rpPath, rpFormType, rpValues(rpToken, "recovery", rpPass).Encode())

	rpRequireRedirect(t, rec, rpOK)
	if f.count(http.MethodPut, "/user") != 1 || f.count(http.MethodPost, "/logout") != 1 {
		t.Errorf("GoTrue calls = %v, want one PUT /user and one POST /logout after the client left", f.names())
	}
}

func TestResetPassword_NonPostIs405(t *testing.T) {
	f := newResetGoTrue(t)
	h := newResetHandler(t, f, nil)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, rpPath+"?"+rpQuery(rpToken), nil))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: status %d Allow %q Cache-Control %q, want 405 POST no-store", m, rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Cache-Control"))
			}
		})
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("GoTrue calls = %v, want none", got)
	}
}

func TestResetPassword_LogsCarryNoTokenPasswordOrAddress(t *testing.T) {
	const (
		token = "leak-token-9f3a7c"
		pass  = "leak-pass-7c1d4e"
		mail  = "leak-probe@corp.example"
	)
	// GoTrue bodies echo the secrets, so a handler that logs a body or a message leaks them.
	echo := func(status int, code string) http.HandlerFunc {
		return answer(status, `{"code":`+strconv.Itoa(status)+`,"error_code":"`+code+`","msg":"`+pass+` `+mail+` `+token+`"}`)
	}
	rows := []struct {
		name                 string
		verify, user, logout http.HandlerFunc
		want                 string
		warn                 bool
	}{
		{name: "success", want: rpOK},
		{name: "same_password, sign-out ok", user: echo(422, "same_password"), want: rpOK},
		{name: "PUT 200, sign-out 500", logout: echo(500, "unexpected_failure"), want: rpOK, warn: true},
		{name: "PUT 200, sign-out unreachable", logout: dropped, want: rpOK, warn: true},
		{name: "same_password, sign-out 500", user: echo(422, "same_password"), logout: echo(500, "unexpected_failure"), want: rpFailed, warn: true},
		{name: "verify 403", verify: echo(403, "otp_expired"), want: rpFailed, warn: true},
		{name: "verify 500", verify: echo(500, "unexpected_failure"), want: rpFailed, warn: true},
		{name: "verify unreachable", verify: dropped, want: rpFailed, warn: true},
		{name: "verify incomplete", verify: answer(200, `{"access_token":"","user":{"id":"u1","email":"`+mail+`"}}`), want: rpFailed, warn: true},
		{name: "verify 302", verify: redirectTo("/user"), want: rpFailed, warn: true},
		{name: "PUT 422 weak_password", user: echo(422, "weak_password"), want: rpFailed, warn: true},
		{name: "PUT 500", user: echo(500, "unexpected_failure"), want: rpFailed, warn: true},
		{name: "PUT unreachable", user: dropped, want: rpFailed, warn: true},
		{name: "PUT 302", user: redirectTo("/logout"), want: rpFailed, warn: true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)
			f.verify = rpVerifyAnswer("u1", mail)
			for _, set := range []struct {
				dst *http.HandlerFunc
				src http.HandlerFunc
			}{{&f.verify, c.verify}, {&f.user, c.user}, {&f.logout, c.logout}} {
				if set.src != nil {
					*set.dst = set.src
				}
			}
			log, buf := captureLog()

			rec := rpPost(t, newResetHandler(t, f, log), rpValues(token, "recovery", pass))

			rpRequireRedirect(t, rec, c.want)
			if c.warn && warnCount(t, buf) == 0 {
				t.Fatalf("the outcome logged no WARN, so the log check proves nothing")
			}
			for _, secret := range []string{token, pass, mail} {
				if strings.Contains(buf.String(), secret) {
					t.Errorf("the log holds %q: %s", secret, buf.String())
				}
			}
		})
	}
}

func TestResetPassword_SignOutAnswersThatMeanTheSessionIsGone(t *testing.T) {
	gone := answer(http.StatusUnauthorized, `{"code":401,"error_code":"session_not_found","msg":"gone"}`)
	goneForbidden := answer(http.StatusForbidden, `{"code":403,"error_code":"user_banned","msg":"gone"}`)
	notGone := answer(http.StatusUnauthorized, `{"code":401,"error_code":"bad_jwt","msg":"bad"}`)
	same := answer(http.StatusUnprocessableEntity, rpSamePassword)
	rows := []struct {
		name         string
		user, logout http.HandlerFunc
		want         string
		warn         bool
	}{
		{"PUT 200, logout 401 session_not_found", nil, gone, rpOK, false},
		{"PUT 200, logout 403 user_banned", nil, goneForbidden, rpOK, false},
		{"same_password, logout 401 session_not_found", same, gone, rpOK, false},
		{"PUT 200, logout 401 bad_jwt", nil, notGone, rpOK, true},
		{"same_password, logout 401 bad_jwt", same, notGone, rpFailed, true},
		{"same_password, logout 302", same, redirectTo("/user"), rpFailed, true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			rg := newResetRig(t)
			if c.user != nil {
				rg.gotrue.user = c.user
			}
			rg.gotrue.logout = c.logout

			rpRequireRedirect(t, rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass)), c.want)

			if !rg.evicted() {
				t.Error("the subject was not evicted")
			}
			if cleared := rg.throttleReset(); cleared != (c.want == rpOK) {
				t.Errorf("throttle cleared = %v, want %v", cleared, c.want == rpOK)
			}
			if c.warn {
				rpRequireWarn(t, rg.log, rpMsgSignOutFailed, nil)
			} else if n := warnCount(t, rg.log); n != 0 {
				t.Errorf("a gone session logged %d WARNs: %s", n, rg.log.String())
			}
		})
	}
}

func TestResetPassword_RefusedPasswordLeavesTheSignInFailuresAlone(t *testing.T) {
	rows := map[string]http.HandlerFunc{
		"PUT 422 weak_password": answer(http.StatusUnprocessableEntity, rpWeakPassword),
		"PUT 500":               answer(http.StatusInternalServerError, gtInternal),
		"PUT unreachable":       dropped,
		"PUT 302":               redirectTo("/logout"),
	}
	for name, user := range rows {
		t.Run(name, func(t *testing.T) {
			rg := newResetRig(t)
			rg.gotrue.user = user

			rpRequireRedirect(t, rpPost(t, rg.handler, rpValues(rpToken, "recovery", rpPass)), rpFailed)

			if n := rg.gotrue.count(http.MethodPut, "/user"); n != 1 {
				t.Fatalf("PUT /user calls = %d, want 1", n)
			}
			if rg.throttleReset() {
				t.Error("a refused password cleared the address's sign-in failures")
			}
		})
	}
}

func TestResetPasswordPage_TokenHoldingAPlaceholderIsNotExpanded(t *testing.T) {
	for _, tok := range []string{"{{.Script}}", "{{.Alert}}", "{{.Token}}"} {
		t.Run(tok, func(t *testing.T) {
			rec, doc := rpPage(t, tok)
			if got := rpTokenInput(t, rec.Body.String()); got != tok {
				t.Errorf("token input = %q, want the literal %q", got, tok)
			}
			if n := len(vpFind(doc, vpTag("script"))); n != 1 {
				t.Errorf("script elements = %d, want 1", n)
			}
			if n := strings.Count(rec.Body.String(), "addEventListener('submit'"); n != 1 {
				t.Errorf("the submit-once script appears %d times, want 1", n)
			}
		})
	}
}
