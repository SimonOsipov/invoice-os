package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const want415 = `{"error":"Content-Type must be application/json"}`

func jsonOnlyAnswer(contentType string, set bool) (bool, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	if set {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	return jsonOnly(rec, req), rec
}

func TestJSONOnly_AdmitsApplicationJSON(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		ok, rec := jsonOnlyAnswer(ct, true)
		if !ok || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Errorf("%q: ok=%v body=%q, want true and nothing written", ct, ok, rec.Body.String())
		}
	}
}

func TestJSONOnly_RefusesEveryOtherContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x", "application/json-patch+json", "application/jsonx", "application/json; charset"} {
		ok, rec := jsonOnlyAnswer(ct, ct != "")
		if ok || rec.Code != http.StatusUnsupportedMediaType || strings.TrimSpace(rec.Body.String()) != want415 {
			t.Errorf("%q: ok=%v %d %q, want false and 415 %s", ct, ok, rec.Code, rec.Body.String(), want415)
		}
	}
}

type jsonRoute struct {
	name, path, body string
	h                http.Handler
}

// jsonRig builds the eleven public JSON handlers on recording fakes.
type jsonRig struct {
	routes   []jsonRoute
	gotrue   atomic.Int32
	throttle map[string]*SignInThrottle
	previews *recordingPreviewer
	claims   atomic.Int32
	sink     *demoSink
	upstream *subUpstream
}

const takenEmail = "taken@corp.example"

func newJSONRig(t *testing.T, minResponse time.Duration) *jsonRig {
	t.Helper()
	r := &jsonRig{throttle: map[string]*SignInThrottle{}, sink: &demoSink{}}
	gt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.gotrue.Add(1)
		b, _ := io.ReadAll(req.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), takenEmail) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error_code":"user_already_exists"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(gt.Close)
	authURL, _ := url.Parse(gt.URL)
	r.upstream = newSubUpstream(t, http.StatusAccepted, `{"status":"accepted"}`)
	subURL, _ := url.Parse(r.upstream.srv.URL)
	log := nopLog()

	th := func(name string) *SignInThrottle {
		r.throttle[name] = NewSignInThrottle(name, 1, ResendMaxKeys, ResendWindow, time.Now)
		return r.throttle[name]
	}
	r.previews = previewing(liveInvite, nil)
	regs := InvitationRegistrations{
		Claim: func(context.Context, string) (string, bool, error) {
			r.claims.Add(1)
			return inviteAddress, false, nil // used token
		},
		Release: func(context.Context, string) error { r.claims.Add(1); return nil },
	}
	blog := BlogSubscribeHandlers(subURL, http.DefaultClient, subTestToken, minResponse, th("blog"), true, log)
	add := func(name, path, body string, h http.Handler) {
		r.routes = append(r.routes, jsonRoute{name, path, body, h})
	}
	add("register", "/auth/register", registerBody(regEmail, regPassword),
		RegisterHandler(authURL, testClient(), minResponse, log, noPendingInvite, nil, false, nil))
	add("resend", "/auth/resend-verification", resendBody(regEmail),
		ResendVerificationHandler(authURL, testClient(), minResponse, th("resend"), true, log, nil))
	add("reset", "/auth/request-password-reset", resendBody(regEmail),
		RequestPasswordResetHandler(authURL, testClient(), minResponse, th("reset"), true, log, nil))
	add("invitation register", "/auth/invitation/register", tokenBody(inviteToken),
		InvitationRegisterHandler(authURL, testClient(), minResponse, log, r.previews.preview, regs, nil))
	add("invitation resend", "/auth/invitation/resend", tokenBody(inviteToken),
		InvitationResendHandler(authURL, testClient(), th("invitation-resend"), true, log, r.previews.preview))
	add("demo request", "/contacts/demo-request", demoJSON(nil), DemoRequestHandler(r.sink, log))
	add("blog subscribe", "/blog/subscriptions", subBody("e@x.co"), blog.Subscribe)
	add("blog confirm", "/blog/subscriptions/confirm", confirmBody(subGoodToken), blog.Confirm)
	add("sign-in", "/auth/sign-in", signInBody(regEmail, regPassword, randomState(t)),
		SignInHandler(authURL, testClient(), testHandoffStore(), th("sign-in"), log, nil, nil))
	add("exchange", "/auth/exchange", `{"code":"c","state":"s"}`, ExchangeHandler(testHandoffStore(), nil))
	add("invitation preview", "/auth/invitation", tokenBody(inviteToken), InvitationHandler(r.previews.preview, log))
	return r
}

func (r *jsonRig) post(rt jsonRoute, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	rt.h.ServeHTTP(rec, req)
	return rec
}

func TestPublicJSONRoutes_RefuseANonJSONBodyBeforeAnyDownstreamCall(t *testing.T) {
	r := newJSONRig(t, 0)
	if len(r.routes) != 11 {
		t.Fatalf("%d routes, want 11", len(r.routes))
	}
	for _, rt := range r.routes {
		rec := r.post(rt, "text/plain", rt.body)
		if rec.Code != http.StatusUnsupportedMediaType || strings.TrimSpace(rec.Body.String()) != want415 {
			t.Errorf("%s: %d %q, want 415 %s", rt.name, rec.Code, rec.Body.String(), want415)
		}
	}
	if n := r.gotrue.Load(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
	if n := len(r.previews.calls()); n != 0 {
		t.Errorf("previewer saw %d tokens, want 0", n)
	}
	if n := r.claims.Load(); n != 0 {
		t.Errorf("registrations store saw %d calls, want 0", n)
	}
	if n := len(r.sink.reqs); n != 0 {
		t.Errorf("contact sink saw %d requests, want 0", n)
	}
	if n := r.upstream.calls(); n != 0 {
		t.Errorf("notifications saw %d calls, want 0", n)
	}
	for name, th := range r.throttle {
		for _, addr := range []string{regEmail, "e@x.co", inviteAddress} {
			if ok, err := th.Admit(addr); !ok || err != nil {
				t.Errorf("%s throttle: address %s already spent its budget", name, addr)
			}
			th.Reset(addr)
		}
	}
}

func TestPublicJSONRoutes_The415DoesNotWaitOrDependOnTheAddress(t *testing.T) {
	r := newJSONRig(t, 2*time.Second)
	bodies := []string{registerBody(regEmail, regPassword), registerBody(takenEmail, regPassword),
		registerBody("someone@gmail.com", regPassword), `{"email":""}`, `{`}
	for _, rt := range r.routes[:3] {
		var first string
		for i, b := range bodies {
			start := time.Now()
			rec := r.post(rt, "text/plain", b)
			if d := time.Since(start); d > 500*time.Millisecond {
				t.Errorf("%s body %d: took %s, want under 500ms", rt.name, i, d)
			}
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s body %d: %d, want 415", rt.name, i, rec.Code)
			}
			if i == 0 {
				first = rec.Body.String()
			} else if rec.Body.String() != first {
				t.Errorf("%s body %d: %q differs from %q", rt.name, i, rec.Body.String(), first)
			}
		}
	}
}

func TestPublicJSONRoutes_AJSONBodyWithACharsetPassesTheCheck(t *testing.T) {
	r := newJSONRig(t, 0)
	for _, rt := range r.routes {
		before := r.gotrue.Load()
		rec := r.post(rt, "application/json; charset=utf-8", rt.body)
		if rec.Code == http.StatusUnsupportedMediaType {
			t.Errorf("%s: 415 for application/json; charset=utf-8", rt.name)
		}
		if rt.name == "register" && r.gotrue.Load() == before {
			t.Errorf("register: GoTrue saw no call")
		}
	}
}

func TestPublicJSONRoutes_AWrongMethodIs405NotTheJSON415(t *testing.T) {
	r := newJSONRig(t, 0)
	if len(r.routes) != 11 {
		t.Fatalf("%d routes, want 11", len(r.routes))
	}
	for _, rt := range r.routes {
		for _, m := range []string{http.MethodGet, http.MethodOptions, http.MethodPut} {
			rec := httptest.NewRecorder()
			rt.h.ServeHTTP(rec, httptest.NewRequest(m, rt.path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d, want 405", m, rt.name, rec.Code)
			}
		}
	}
}
