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
	mk := func(name string, max int) *SignInThrottle {
		return NewSignInThrottle(name, max, BlogSubscribeMaxKeys, BlogSubscribeWindow, time.Now)
	}
	return BlogSubscribeHandlers(u, http.DefaultClient, subTestToken, minResponse,
		mk("ip", BlogSubscribePerIP), mk("email", BlogSubscribePerEmail), mk("confirm", BlogConfirmPerIP), enforce, log)
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
	for i := range BlogSubscribePerIP - 1 { // leave one IP slot so the email cap is what refuses
		subPost(rt.Subscribe, "203.0.113.1:1", subBody(fmt.Sprintf("fill%d@x.co", i)))
	}
	base := up.calls()

	fwd := subPost(rt.Subscribe, "203.0.113.2:1", subBody("e@x.co"))
	for range BlogSubscribePerEmail - 1 {
		subPost(rt.Subscribe, "203.0.113.3:1", subBody("e@x.co"))
	}
	emailCapped := subPost(rt.Subscribe, "203.0.113.4:1", subBody("e@x.co"))
	subPost(rt.Subscribe, "203.0.113.1:1", subBody("last@x.co"))
	ipCapped := subPost(rt.Subscribe, "203.0.113.1:1", subBody("other@x.co"))

	if got := up.calls() - base; got != 1+(BlogSubscribePerEmail-1)+1 {
		t.Fatalf("upstream calls = %d; a capped request reached it", got)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"email-capped": emailCapped, "ip-capped": ipCapped} {
		if rec.Code != http.StatusAccepted || rec.Body.String() != fwd.Body.String() {
			t.Errorf("%s = %d %q, want %d %q", name, rec.Code, rec.Body.String(), fwd.Code, fwd.Body.String())
		}
	}
	if fwd.Code != http.StatusAccepted {
		t.Errorf("forwarded = %d, want 202", fwd.Code)
	}
}

func TestBlogSubscribe_HoldsTheFloor(t *testing.T) {
	const floor = 80 * time.Millisecond
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, floor, true, nil)
	// Fill the caps first (no timing asserted), then time each answer.
	for i := range BlogSubscribePerIP {
		subPost(rt.Subscribe, "198.51.100.1:1", subBody(fmt.Sprintf("f%d@x.co", i)))
	}
	for range BlogSubscribePerEmail {
		subPost(rt.Subscribe, "198.51.100.2:1", subBody("cap@x.co"))
	}
	for name, c := range map[string]struct {
		remote, email string
		status        int
		want          int
	}{
		"202":          {"198.51.100.3:1", "ok@x.co", http.StatusAccepted, http.StatusAccepted},
		"ip-capped":    {"198.51.100.1:1", "n1@x.co", http.StatusAccepted, http.StatusAccepted},
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
	// Same throttles, no floor: the 400 spent no cap, so all 10 valid requests forward.
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	subPost(rt.Subscribe, "203.0.113.9:1", subBody("nope"))
	subPost(rt.Subscribe, "203.0.113.9:1", `{"email":"a@x.co","category_ids":[],"frequency":"weekly"}`)
	subPost(rt.Subscribe, "203.0.113.9:1", `{"email":"a@x.co","category_ids":["a"],"frequency":"daily"}`)
	subPost(rt.Subscribe, "203.0.113.9:1", `not json`)
	for i := range BlogSubscribePerIP {
		subPost(rt.Subscribe, "203.0.113.9:1", subBody(fmt.Sprintf("v%d@x.co", i)))
	}
	if up.calls() != BlogSubscribePerIP {
		t.Fatalf("upstream calls = %d, want %d: a 400 spent a cap", up.calls(), BlogSubscribePerIP)
	}
}

func TestBlogSubscribe_IPCapAdmitsTenThenForwardsNothing(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{"status":"accepted"}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	var last *httptest.ResponseRecorder
	for i := range BlogSubscribePerIP + 1 {
		last = subPost(rt.Subscribe, "203.0.113.5:1", subBody(fmt.Sprintf("u%d@x.co", i)))
	}
	if up.calls() != BlogSubscribePerIP {
		t.Errorf("upstream calls = %d, want %d", up.calls(), BlogSubscribePerIP)
	}
	if last.Code != http.StatusAccepted {
		t.Errorf("11th = %d, want 202", last.Code)
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

func TestBlogSubscribe_IPRefusalSpendsNoEmailCount(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for i := range BlogSubscribePerIP {
		subPost(rt.Subscribe, "203.0.113.20:1", subBody(fmt.Sprintf("f%d@x.co", i)))
	}
	subPost(rt.Subscribe, "203.0.113.20:1", subBody("e@x.co"))
	if up.calls() != BlogSubscribePerIP {
		t.Fatalf("refused request reached upstream: %d calls", up.calls())
	}
	for i := range BlogSubscribePerEmail {
		subPost(rt.Subscribe, fmt.Sprintf("203.0.113.%d:1", 30+i), subBody("e@x.co"))
	}
	if got := up.calls() - BlogSubscribePerIP; got != BlogSubscribePerEmail {
		t.Errorf("calls for E from fresh IPs = %d, want %d: the IP refusal spent an email count", got, BlogSubscribePerEmail)
	}
}

func TestBlogSubscribe_Upstream400RefundsBoth(t *testing.T) {
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
	// The IP count came back too: nine more distinct emails still forward.
	for i := range BlogSubscribePerIP - 1 {
		subPost(rt.Subscribe, "203.0.113.40:1", subBody(fmt.Sprintf("z%d@x.co", i)))
	}
	if up.calls() != 4+BlogSubscribePerIP-1 {
		t.Errorf("upstream calls = %d, want %d", up.calls(), 4+BlogSubscribePerIP-1)
	}
}

func TestBlogSubscribe_PreviewLogsAndAdmits(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	buf := &safeBuf{}
	rt := subRoutes(t, up.srv.URL, 0, false, slog.New(slog.NewTextHandler(buf, nil)))
	for i := range 12 {
		subPost(rt.Subscribe, "203.0.113.50:1", subBody(fmt.Sprintf("p%d@x.co", i)))
	}
	if up.calls() != 12 {
		t.Errorf("upstream calls = %d, want 12", up.calls())
	}
	if out := buf.String(); !strings.Contains(out, "limit reached") || !strings.Contains(out, "enforced=false") {
		t.Errorf("no `limit reached` WARN with enforced=false in %q", out)
	}
}

func TestBlogSubscribe_IPv6KeyedBySlash64(t *testing.T) {
	up := newSubUpstream(t, http.StatusAccepted, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	for i := range BlogSubscribePerIP + 2 {
		remote := "[2001:db8:1:2::1]:1"
		if i%2 == 1 {
			remote = "[2001:db8:1:2:ffff::7]:1"
		}
		subPost(rt.Subscribe, remote, subBody(fmt.Sprintf("v6%d@x.co", i)))
	}
	if up.calls() != BlogSubscribePerIP {
		t.Errorf("upstream calls = %d, want %d across one /64", up.calls(), BlogSubscribePerIP)
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

func TestBlogConfirm_IPCapIs429(t *testing.T) {
	up := newSubUpstream(t, http.StatusOK, `{}`)
	rt := subRoutes(t, up.srv.URL, 0, true, nil)
	var last *httptest.ResponseRecorder
	for range BlogConfirmPerIP + 1 {
		last = confirmPost(rt.Confirm, "203.0.113.82:1", subGoodToken)
	}
	if last.Code != http.StatusTooManyRequests || up.calls() != BlogConfirmPerIP {
		t.Errorf("31st = %d with %d upstream calls, want 429 with %d", last.Code, up.calls(), BlogConfirmPerIP)
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
	for i := range BlogSubscribePerIP { // fills the IP, then the last is IP-capped
		subPost(rt.Subscribe, "203.0.113.92:1", subBody(fmt.Sprintf("%d%s", i, email)))
	}
	up.set(http.StatusInternalServerError, `{"echo":"`+email+`"}`)
	subPost(rt.Subscribe, "203.0.113.93:1", subBody("x"+email))
	for range BlogConfirmPerIP + 1 {
		confirmPost(rt.Confirm, "203.0.113.94:1", token)
	}
	up.srv.Close()
	subPost(rt.Subscribe, "203.0.113.95:1", subBody("y"+email))
	confirmPost(rt.Confirm, "203.0.113.96:1", token)

	prev := subRoutes(t, "http://127.0.0.1:1", 0, false, log)
	for range BlogSubscribePerIP + 1 {
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
