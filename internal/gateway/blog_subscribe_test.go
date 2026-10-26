package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const subTestToken = "gw-token-for-subscribe-tests"

var subGoodToken = strings.Repeat("aB3_-", 8) + "xyz" // 43 characters

type subUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies [][]byte
	header http.Header
	status int
	reply  string
}

func newSubUpstream(t *testing.T, status int, reply string) *subUpstream {
	t.Helper()
	u := &subUpstream{status: status, reply: reply}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.paths = append(u.paths, r.Method+" "+r.URL.Path)
		u.bodies = append(u.bodies, b)
		u.header = r.Header.Clone()
		status, reply := u.status, u.reply
		u.mu.Unlock()
		if status == http.StatusFound {
			w.Header().Set("Location", u.srv.URL+"/elsewhere")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *subUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

func (u *subUpstream) set(status int, reply string) {
	u.mu.Lock()
	u.status, u.reply = status, reply
	u.mu.Unlock()
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func subRoutes(t *testing.T, base string, minResponse time.Duration, enforce bool, log *slog.Logger) BlogSubscribeRoutes {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	perEmail := NewSignInThrottle("email", BlogSubscribePerEmail, BlogSubscribeMaxKeys, BlogSubscribeWindow, time.Now)
	return BlogSubscribeHandlers(u, http.DefaultClient, subTestToken, minResponse, perEmail, enforce, log)
}

func subBody(email string) string {
	b, _ := json.Marshal(map[string]any{"email": email, "category_ids": []string{"tax-policy", "e-invoicing"}, "frequency": "weekly"})
	return string(b)
}

func subPost(h http.Handler, remote, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions", strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func confirmBody(token string) string {
	b, _ := json.Marshal(map[string]string{"token": token})
	return string(b)
}

func TestBlogSubscribe_SameAnswerForEveryValidRequest(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{"status":"accepted"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	fwd := subPost(rt.Subscribe, "203.0.113.2:1", subBody("e@x.co"))
	for range BlogSubscribePerEmail - 1 {
		subPost(rt.Subscribe, "203.0.113.3:1", subBody("e@x.co"))
	}
	emailCapped := subPost(rt.Subscribe, "203.0.113.4:1", subBody("e@x.co"))

	if got := up.calls(); got != BlogSubscribePerEmail {
		t.Fatalf("upstream calls = %d; a capped request reached it", got)
	}
	if emailCapped.Code != http.StatusAccepted || emailCapped.Body.String() != fwd.Body.String() {
		t.Errorf("email-capped = %d %q, want %d %q", emailCapped.Code, emailCapped.Body.String(), fwd.Code, fwd.Body.String())
	}
	if fwd.Code != http.StatusAccepted {
		t.Errorf("forwarded = %d, want 202", fwd.Code)
	}
}

func TestBlogSubscribe_HoldsTheFloor(t *testing.T) {
	const floor = 80 * time.Millisecond
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, floor, true, nil)
	// Fill the cap first (no timing asserted), then time each answer.
	for range BlogSubscribePerEmail {
		subPost(rt.Subscribe, "198.51.100.2:1", subBody("cap@x.co"))
	}
	for name, c := range map[string]struct {
		remote, email string
		status        int
		want          int
	}{
		"202":          {"198.51.100.3:1", "ok@x.co", http.StatusAccepted, http.StatusAccepted},
		"email-capped": {"198.51.100.4:1", "cap@x.co", http.StatusAccepted, http.StatusAccepted},
		"502":          {"198.51.100.5:1", "e502@x.co", http.StatusInternalServerError, http.StatusBadGateway},
		"503":          {"198.51.100.6:1", "e503@x.co", http.StatusServiceUnavailable, http.StatusServiceUnavailable},
	} {
		up.set(c.status, `{}`)
		start := time.Now()
		rec := subPost(rt.Subscribe, c.remote, subBody(c.email))
		if el := time.Since(start); el < floor {
			t.Errorf("%s answered in %v, before the %v floor", name, el, floor)
		}
		if rec.Code != c.want {
			t.Errorf("%s = %d, want %d", name, rec.Code, c.want)
		}
	}
}

func TestBlogSubscribe_BadFieldIs400AtOnce(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	slow := subRoutes(t, up.srv.URL, 2*time.Second, true, nil)
	start := time.Now()
	rec := subPost(slow.Subscribe, "203.0.113.9:1", subBody("nope"))
	if el := time.Since(start); rec.Code != http.StatusBadRequest || el > 200*time.Millisecond {
		t.Fatalf("bad email = %d in %v, want 400 in under 200 ms", rec.Code, el)
	}
	if up.calls() != 0 {
		t.Fatalf("upstream calls = %d, want 0", up.calls())
	}
	// Same throttle, no floor: the 400s spent no cap, so every valid request for the address forwards.
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	subPost(rt.Subscribe, "203.0.113.9:1", `{"email":"a@x.co","category_ids":[],"frequency":"weekly"}`)
	subPost(rt.Subscribe, "203.0.113.9:1", `{"email":"a@x.co","category_ids":["a"],"frequency":"daily"}`)
	subPost(rt.Subscribe, "203.0.113.9:1", `not json`)
	for range BlogSubscribePerEmail {
		subPost(rt.Subscribe, "203.0.113.9:1", subBody("a@x.co"))
	}
	if up.calls() != BlogSubscribePerEmail {
		t.Fatalf("upstream calls = %d, want %d: a 400 spent a cap", up.calls(), BlogSubscribePerEmail)
	}
}

func TestBlogSubscribe_EmailCapAdmitsThreeThenForwardsNothing(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for i, e := range []string{"A@x.co", " a@x.co", "a@X.CO", "a@x.co"} {
		if rec := subPost(rt.Subscribe, fmt.Sprintf("203.0.113.%d:1", 10+i), subBody(e)); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d = %d, want 202", i+1, rec.Code)
		}
	}
	if up.calls() != BlogSubscribePerEmail {
		t.Errorf("upstream calls = %d, want %d", up.calls(), BlogSubscribePerEmail)
	}
}

func TestBlogSubscribe_Upstream400RefundsTheEmailCount(t *testing.T) {
	up := newSubUpstream(t, http.StatusBadRequest, `{"error":"unknown category"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for range 3 {
		if rec := subPost(rt.Subscribe, "203.0.113.40:1", subBody("e@x.co")); rec.Code != http.StatusBadRequest {
			t.Fatalf("upstream 400 = %d, want 400", rec.Code)
		}
	}
	up.set(http.StatusAccepted, `{}`)
	subPost(rt.Subscribe, "203.0.113.40:1", subBody("e@x.co"))
	if up.calls() != 4 {
		t.Errorf("upstream calls = %d, want 4: a 400 kept its reservation", up.calls())
	}
}

func TestBlogSubscribe_PreviewLogsAndAdmits(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	buf := &safeBuf{}
	rt := subRoutes(t, up.srv.URL, 0, false, slog.New(slog.NewTextHandler(buf, nil)))
	for range BlogSubscribePerEmail + 2 {
		subPost(rt.Subscribe, "203.0.113.50:1", subBody("p@x.co"))
	}
	if up.calls() != BlogSubscribePerEmail+2 {
		t.Errorf("upstream calls = %d, want %d", up.calls(), BlogSubscribePerEmail+2)
	}
	if out := buf.String(); !strings.Contains(out, "limit reached") || !strings.Contains(out, "enforced=false") {
		t.Errorf("no `limit reached` WARN with enforced=false in %q", out)
	}
}

func TestBlogSubscribe_ForwardsBodyWithGatewayToken(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	body := `{"email":" Ada@Corp.CO ","category_ids":["tax-policy","e-invoicing","tax-policy"],"frequency":"monthly"}`
	req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.60:1"
	req.Header.Set("X-User-ID", "u-1")
	rt.Subscribe.ServeHTTP(httptest.NewRecorder(), req)

	if up.calls() != 1 || up.paths[0] != "POST /internal/blog/subscriptions" {
		t.Fatalf("upstream calls = %v, want POST /internal/blog/subscriptions", up.paths)
	}
	if got := up.header.Get("X-Gateway-Token"); got != subTestToken {
		t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
	}
	if up.header.Get("X-User-ID") != "" {
		t.Error("X-User-ID reached notifications")
	}
	var got map[string]any
	if err := json.Unmarshal(up.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"email": "ada@corp.co", "category_ids": []any{"e-invoicing", "tax-policy"}, "frequency": "monthly"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded body = %v, want %v", got, want)
	}
}

func TestBlogSubscribe_StatusMapping(t *testing.T) {
	for _, c := range []struct{ upstream, want int }{
		{http.StatusServiceUnavailable, http.StatusServiceUnavailable},
		{http.StatusInternalServerError, http.StatusBadGateway},
		{http.StatusFound, http.StatusBadGateway},
	} {
		up := newSubUpstream(t, c.upstream, `{}`)
		rt := subRoutes(t, up.srv.URL, 0, true, nil)
		if rec := subPost(rt.Subscribe, "203.0.113.70:1", subBody("e@x.co")); rec.Code != c.want {
			t.Errorf("upstream %d -> %d, want %d", c.upstream, rec.Code, c.want)
		}
		if up.calls() != 1 {
			t.Errorf("upstream %d: %d calls, want 1 (no redirect followed)", c.upstream, up.calls())
		}
	}
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	up.srv.Close()
	if rec := subPost(rt.Subscribe, "203.0.113.71:1", subBody("e@x.co")); rec.Code != http.StatusBadGateway {
		t.Errorf("transport error -> %d, want 502", rec.Code)
	}
}

func TestBlogSubscribe_SlowAcceptedUpstreamIsStill202(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	rt := subRoutes(t, srv.URL, 0, true, nil)
	if rec := subPost(rt.Subscribe, "203.0.113.72:1", subBody("slow@x.co")); rec.Code != http.StatusAccepted {
		t.Errorf("upstream 202 after 6 s -> %d, want 202", rec.Code)
	}
}

func TestBlogConfirm_MalformedTokenIs410WithoutCall(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for _, tok := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("a", 42) + "+"} {
		req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions/confirm", strings.NewReader(confirmBody(tok)))
		rec := httptest.NewRecorder()
		rt.Confirm.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Errorf("token %q = %d, want 410", tok, rec.Code)
		}
	}
	if up.calls() != 0 {
		t.Errorf("upstream calls = %d, want 0", up.calls())
	}
}

func confirmPost(h http.Handler, remote, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions/confirm", strings.NewReader(confirmBody(token)))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBlogConfirm_StatusMapping(t *testing.T) {
	for _, c := range []struct{ upstream, want int }{
		{http.StatusOK, http.StatusOK}, {http.StatusGone, http.StatusGone},
		{http.StatusServiceUnavailable, http.StatusServiceUnavailable}, {http.StatusInternalServerError, http.StatusBadGateway},
	} {
		up := newSubUpstream(t, c.upstream, `{"status":"confirmed"}`)
		rt := subRoutes(t, up.srv.URL, 0, true, nil)
		if rec := confirmPost(rt.Confirm, "203.0.113.80:1", subGoodToken); rec.Code != c.want {
			t.Errorf("upstream %d -> %d, want %d", c.upstream, rec.Code, c.want)
		}
		if up.calls() != 1 || up.paths[0] != "POST /internal/blog/subscriptions/confirm" || up.header.Get("X-Gateway-Token") != subTestToken {
			t.Errorf("upstream %d: calls %v, token %q", c.upstream, up.paths, up.header.Get("X-Gateway-Token"))
		}
	}
}

func TestBlogConfirm_RelaysUnsubscribedEverywhere(t *testing.T) {
	for _, c := range []struct{ upstream, want string }{
		{`{"status":"confirmed","unsubscribed_everywhere":true}`, `{"status":"confirmed","unsubscribed_everywhere":true}`},
		{`{"status":"confirmed","unsubscribed_everywhere":false}`, `{"status":"confirmed","unsubscribed_everywhere":false}`},
		{`{"status":"confirmed"}`, `{"status":"confirmed","unsubscribed_everywhere":false}`},
		{`{"status":"confirmed","unsubscribed_everywhere":"yes"}`, `{"status":"confirmed","unsubscribed_everywhere":false}`},
		{`not json`, `{"status":"confirmed","unsubscribed_everywhere":false}`},
		{`{"status":"confirmed","unsubscribed_everywhere":true,"email":"a@x.co","extra":1}`, `{"status":"confirmed","unsubscribed_everywhere":true}`},
	} {
		up := newSubUpstream(t, http.StatusOK, c.upstream)
		rt := subRoutes(t, up.srv.URL, 0, true, nil)
		rec := confirmPost(rt.Confirm, "203.0.113.81:1", subGoodToken)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("upstream %q -> %d %q, want 200 %q", c.upstream, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func TestBlogSubscribeRoutes_MethodNotAllowed(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for name, h := range map[string]http.Handler{"subscribe": rt.Subscribe, "confirm": rt.Confirm} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blog/subscriptions", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("GET %s = %d Allow %q, want 405 Allow POST", name, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if up.calls() != 0 {
		t.Errorf("a GET reached upstream %d times", up.calls())
	}
}

func TestBlogSubscribe_LogsCarryNoEmailOrToken(t *testing.T) {
	const email = "Secret.Person@Example.COM"
	token := strings.Repeat("SeCrEt", 7) + "_"
	buf := &safeBuf{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, time.Millisecond, true, log)

	subPost(rt.Subscribe, "203.0.113.90:1", subBody("nope"))
	for range BlogSubscribePerEmail + 1 { // the 4th is email-capped
		subPost(rt.Subscribe, "203.0.113.91:1", subBody(email))
	}
	up.set(http.StatusInternalServerError, `{"echo":"`+email+`"}`)
	subPost(rt.Subscribe, "203.0.113.93:1", subBody("x"+email))
	confirmPost(rt.Confirm, "203.0.113.94:1", token)
	up.srv.Close()
	subPost(rt.Subscribe, "203.0.113.95:1", subBody("y"+email))
	confirmPost(rt.Confirm, "203.0.113.96:1", token)

	prev := subRoutes(t, "http://127.0.0.1:1", 0, false, log)
	for range BlogSubscribePerEmail + 1 {
		subPost(prev.Subscribe, "203.0.113.97:1", subBody(email))
	}

	out := strings.ToLower(buf.String())
	if !strings.Contains(out, "limit reached") || !strings.Contains(out, "upstream failed") {
		t.Fatalf("control: the paths logged nothing to scan: %q", out)
	}
	for _, needle := range []string{"secret.person", "example.co", strings.ToLower(token), "secret"} {
		if strings.Contains(out, needle) {
			t.Errorf("log carries %q", needle)
		}
	}
}

func TestBlogSubscribe_UniformAnswerCarriesSameHeaders(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{"status":"accepted"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	fwd := subPost(rt.Subscribe, "203.0.113.100:1", subBody("e@x.co"))
	for range BlogSubscribePerEmail - 1 {
		subPost(rt.Subscribe, "203.0.113.101:1", subBody("e@x.co"))
	}
	emailCapped := subPost(rt.Subscribe, "203.0.113.102:1", subBody("e@x.co"))
	if len(fwd.Header()) == 0 {
		t.Fatal("control: the forwarded answer carries no headers")
	}
	if !reflect.DeepEqual(emailCapped.Header(), fwd.Header()) {
		t.Errorf("email-capped headers = %v, want %v", emailCapped.Header(), fwd.Header())
	}
	if fwd.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", fwd.Header().Get("Cache-Control"))
	}
}

func TestBlogSubscribe_RejectsOversizedAndMalformedBodiesWithoutCall(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	huge := `{"email":"a@x.co","category_ids":["a"],"frequency":"weekly","pad":"` + strings.Repeat("x", 5000) + `"}`
	for name, body := range map[string]string{
		"oversized":      huge,
		"not json":       "email=a@x.co",
		"empty":          "",
		"email not str":  `{"email":42,"category_ids":["a"],"frequency":"weekly"}`,
		"two at signs":   subBody("a@b@x.co"),
		"inner space":    subBody("a b@x.co"),
		"long email":     subBody(strings.Repeat("a", 400) + "@x.co"),
		"bad category":   `{"email":"a@x.co","category_ids":["Bad Id"],"frequency":"weekly"}`,
		"51 categories":  `{"email":"a@x.co","category_ids":["` + strings.Join(manyIDs(51), `","`) + `"],"frequency":"weekly"}`,
		"empty category": `{"email":"a@x.co","category_ids":[""],"frequency":"weekly"}`,
	} {
		rec := subPost(rt.Subscribe, "203.0.113.110:1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if up.calls() != 0 {
		t.Errorf("upstream calls = %d, want 0", up.calls())
	}
	// Positive pair: exactly 50 categories is valid.
	ok := `{"email":"a@x.co","category_ids":["` + strings.Join(manyIDs(50), `","`) + `"],"frequency":"weekly"}`
	if rec := subPost(rt.Subscribe, "203.0.113.110:1", ok); rec.Code != http.StatusAccepted || up.calls() != 1 {
		t.Errorf("50 categories = %d with %d calls, want 202 with 1", rec.Code, up.calls())
	}
}

func manyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i)
	}
	return ids
}

func TestBlogConfirm_BodyShapes(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/blog/subscriptions/confirm", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.120:1"
		rec := httptest.NewRecorder()
		rt.Confirm.ServeHTTP(rec, req)
		return rec.Code
	}
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"missing token": {`{}`, http.StatusGone},
		"null token":    {`{"token":null}`, http.StatusGone},
		"numeric token": {`{"token":42}`, http.StatusBadRequest},
		"not json":      {`token=abc`, http.StatusBadRequest},
		"empty":         {``, http.StatusBadRequest},
		"oversized":     {`{"token":"` + subGoodToken + `","pad":"` + strings.Repeat("x", 1500) + `"}`, http.StatusBadRequest},
	} {
		if got := post(c.body); got != c.want {
			t.Errorf("%s = %d, want %d", name, got, c.want)
		}
	}
	if up.calls() != 0 {
		t.Fatalf("upstream calls = %d, want 0", up.calls())
	}
	if got := post(confirmBody(subGoodToken)); got != http.StatusOK || up.calls() != 1 {
		t.Errorf("good token = %d with %d calls, want 200 with 1", got, up.calls())
	}
}

func TestBlogSubscribeRoutes_OnlyPostIsServed(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{"status":"confirmed"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	n := 0
	for name, h := range map[string]http.Handler{"subscribe": rt.Subscribe, "confirm": rt.Confirm} {
		for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			n++
			req := httptest.NewRequest(m, "/blog/subscriptions/confirm?token="+subGoodToken, strings.NewReader(confirmBody(subGoodToken)))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s = %d Allow %q Cache-Control %q", m, name, rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Cache-Control"))
			}
		}
	}
	if n == 0 || up.calls() != 0 {
		t.Errorf("%d non-POST requests made %d upstream calls, want 0", n, up.calls())
	}
}

func TestBlogSubscribe_UpstreamErrorBodyIsNotRelayed(t *testing.T) {
	up := newSubUpstream(t, http.StatusServiceUnavailable, `{"error":"already subscribed a@x.co"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	rec := subPost(rt.Subscribe, "203.0.113.150:1", subBody("a@x.co"))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "already") || strings.Contains(rec.Body.String(), "a@x.co") {
		t.Errorf("503 answer = %d %q, want a fixed body", rec.Code, rec.Body.String())
	}
	up.set(http.StatusBadRequest, `{"error":"unknown category"}`)
	rec = subPost(rt.Subscribe, "203.0.113.151:1", subBody("a@x.co"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request is invalid") || strings.Contains(rec.Body.String(), "unknown category") {
		t.Errorf("upstream 400 answer = %d %q, want 400 `request is invalid` without the upstream text", rec.Code, rec.Body.String())
	}
}

// The other cap tests loop on the constant; this one pins the value the AC states.
func TestBlogSubscribe_EmailCapValueIsThree(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for i := 0; i < 4; i++ {
		subPost(rt.Subscribe, fmt.Sprintf("203.0.113.%d:1", 161+i), subBody("same@x.co"))
	}
	if got := up.calls(); got != 3 {
		t.Errorf("per-email: %d subscribes forwarded, want 3", got)
	}
}

// D6: with the shared count unreadable, an enforced subscribe answers the same 202 and forwards nothing.
func TestBlogSubscribe_AnUnreadableThrottleIsTheSame202AndForwardsNothing(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	u, err := url.Parse(up.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	perEmail := NewSignInThrottle("blog-subscribe-email", BlogSubscribePerEmail, BlogSubscribeMaxKeys, BlogSubscribeWindow, time.Now)
	perEmail.Shared(deadPool(t), slog.New(slog.DiscardHandler))
	rt := BlogSubscribeHandlers(u, http.DefaultClient, subTestToken, 0, perEmail, true, slog.New(slog.DiscardHandler))

	rec := subPost(rt.Subscribe, "203.0.113.60:1", subBody("p@x.co"))

	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"status":"accepted"}` {
		t.Errorf("enforced subscribe, count unreadable: %d %q, want 202 accepted", rec.Code, rec.Body.String())
	}
	if up.calls() != 0 {
		t.Errorf("upstream calls = %d, want 0", up.calls())
	}
}
