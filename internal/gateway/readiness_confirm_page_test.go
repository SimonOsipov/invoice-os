package gateway

import (
	"context"
	"encoding/json"
	stdhtml "html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	cpGWToken = "gw-secret"
	cpPath    = "/internal/public-checks/confirm"
	cpTitle   = "Readiness check results · ASComply"
	cpHeading = "Get your readiness check results"
	cpAsk     = "Press the button and we will email the results of your readiness check to this address."
	cpButton  = "Get my results"
	cpDone    = "Done. Your email from no-reply@ascomply.com is on its way. Check your spam folder if you cannot find it."
	cpExpired = "This link has expired or was already used. Run the check again at www.ascomply.com/check."
	cpRetry   = "We could not send your results just now. Try again in a few minutes."
	cpCheck   = "https://www.ascomply.com/check"
	cpAddr    = "pii@x.io"
	cpAction  = "/readiness-check/confirm?t="
	cpWarnMsg = "readiness-confirm: upstream failed"
)

// 43 base64url characters, with both '-' and '_', as a minted token looks.
const cpToken = "Ab3-_Cd4Ef5Gh6Ij7Kl8Mn9Op0Qr1St2Uv3Wx4Yz5Ab"

type cpCall struct {
	method, path, contentType, gateway string
	body                               []byte
}

// cpFake stands in for the worker's confirm route; reply decides the answer of each call.
type cpFake struct {
	mu    sync.Mutex
	calls []cpCall
	reply func(w http.ResponseWriter, r *http.Request)
}

func cpAnswer(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *cpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, cpCall{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("X-Gateway-Token"), raw})
	f.mu.Unlock()
	f.reply(w, r)
}

func (f *cpFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type cpEnv struct {
	h    http.Handler
	f    *cpFake
	logs *safeBuf
}

// cpStart builds the confirm handler through ReadinessCheckHandlers, as main does. A nil client gets a plain one.
func cpStart(t *testing.T, reply func(http.ResponseWriter, *http.Request), client *http.Client) *cpEnv {
	t.Helper()
	f := &cpFake{reply: reply}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		client = srv.Client()
	}
	e := &cpEnv{f: f, logs: &safeBuf{}}
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	th := NewSignInThrottle("readiness-check-ip", ReadinessCheckPerIP, ReadinessCheckMaxKeys, ReadinessCheckIPWindow, time.Now)
	routes := ReadinessCheckHandlers(base, client, cpGWToken, th, true, platform.PostureLocal, log)
	if routes.Confirm == nil {
		t.Fatal("ReadinessCheckHandlers returned no Confirm handler")
	}
	e.h = routes.Confirm
	return e
}

func (e *cpEnv) serve(method, target, body string) *httptest.ResponseRecorder {
	return cpServe(e.h, method, target, body)
}

func cpServe(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// cpView is a parsed page: its paragraphs joined by newlines, its buttons, and the links by href.
type cpView struct {
	doc     *html.Node
	paras   string
	buttons int
	links   map[string]string
}

func cpParse(t *testing.T, rec *httptest.ResponseRecorder) cpView {
	t.Helper()
	doc := vpParse(t, rec.Body.String())
	v := cpView{doc: doc, buttons: len(vpFind(doc, vpTag("button"))), links: map[string]string{}}
	var paras []string
	for _, p := range vpFind(doc, vpTag("p")) {
		paras = append(paras, strings.TrimSpace(vpText(p)))
	}
	v.paras = strings.Join(paras, "\n")
	for _, a := range vpFind(doc, vpTag("a")) {
		href, _ := vpAttr(a, "href")
		v.links[href] = strings.TrimSpace(vpText(a))
	}
	if h1 := vpFind(doc, vpTag("h1")); len(h1) != 1 || strings.TrimSpace(vpText(h1[0])) != cpHeading {
		t.Errorf("h1 elements = %d, want one reading %q", len(h1), cpHeading)
	}
	return v
}

func (v cpView) wantExpired(t *testing.T) {
	t.Helper()
	if v.paras != cpExpired {
		t.Errorf("paragraphs = %q, want the expired copy", v.paras)
	}
	if text, ok := v.links[cpCheck]; !ok || text != "www.ascomply.com/check" {
		t.Errorf("links = %v, want %s reading www.ascomply.com/check", v.links, cpCheck)
	}
	if v.buttons != 0 {
		t.Errorf("buttons = %d, want 0", v.buttons)
	}
}

func (v cpView) wantRetry(t *testing.T) {
	t.Helper()
	if !strings.Contains(v.paras, cpRetry) {
		t.Errorf("paragraphs = %q, want the retry copy", v.paras)
	}
	if v.buttons != 1 {
		t.Errorf("buttons = %d, want 1", v.buttons)
	}
}

func (v cpView) wantDone(t *testing.T) {
	t.Helper()
	if v.paras != cpDone {
		t.Errorf("paragraphs = %q, want the done copy", v.paras)
	}
	if v.buttons != 0 {
		t.Errorf("buttons = %d, want 0", v.buttons)
	}
}

func cpBody(t *testing.T, c cpCall) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.body, &m); err != nil {
		t.Fatalf("worker body %q is not a JSON object: %v", c.body, err)
	}
	return m
}

func TestConfirmPage_GetShowsTheConsentAndOneButtonWithoutAnUpstreamCall(t *testing.T) {
	e := cpStart(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the worker was called on a GET")
	}, nil)
	rec := e.serve(http.MethodGet, cpAction+cpToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	v := cpParse(t, rec)
	if title := vpFind(v.doc, vpTag("title")); len(title) != 1 || strings.TrimSpace(vpText(title[0])) != cpTitle {
		t.Errorf("title elements = %d, want one reading %q", len(title), cpTitle)
	}
	for _, want := range []string{cpAsk, "Pressing it also confirms the box on the check page: \u201c" + checkConsentText + "\u201d"} {
		if !strings.Contains(v.paras, want) {
			t.Errorf("paragraphs %q lack %q", v.paras, want)
		}
	}
	if !strings.Contains(rec.Body.String(), stdhtml.EscapeString(checkConsentText)) {
		t.Error("the page does not carry the escaped consent sentence")
	}
	buttons := vpFind(v.doc, vpTag("button"))
	if len(buttons) != 1 || strings.TrimSpace(vpText(buttons[0])) != cpButton {
		t.Fatalf("buttons = %d, want one reading %q", len(buttons), cpButton)
	}
	forms := vpFind(v.doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if action, _ := vpAttr(forms[0], "action"); action != cpAction+cpToken {
		t.Errorf("form action = %q, want %q", action, cpAction+cpToken)
	}
	if method, _ := vpAttr(forms[0], "method"); !strings.EqualFold(method, "post") {
		t.Errorf("form method = %q, want post", method)
	}
	head := e.serve(http.MethodHead, cpAction+cpToken, "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, body %q; want 200 and no body", head.Code, head.Body.String())
	}
	if n := e.f.count(); n != 0 {
		t.Errorf("worker calls = %d, want 0", n)
	}
}

func TestConfirmPage_AMalformedTokenIs404WithTheExpiredCopy(t *testing.T) {
	cases := map[string]string{
		"empty":     "",
		"42 chars":  cpToken[:42],
		"44 chars":  cpToken + "A",
		"plus sign": cpToken[:42] + "+",
		"padding":   cpToken[:42] + "=",
		"markup":    url.QueryEscape(`"><script>x</script>`),
	}
	for name, tok := range cases {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(name+" "+method, func(t *testing.T) {
				e := cpStart(t, func(http.ResponseWriter, *http.Request) {
					t.Error("the worker was called for a malformed token")
				}, nil)
				rec := e.serve(method, cpAction+tok, "")
				if rec.Code != http.StatusNotFound {
					t.Errorf("status = %d, want 404", rec.Code)
				}
				cpParse(t, rec).wantExpired(t)
				if strings.Contains(rec.Body.String(), "<script>x") {
					t.Error("the token reached the page")
				}
				if n := e.f.count(); n != 0 {
					t.Errorf("worker calls = %d, want 0", n)
				}
			})
		}
	}
	t.Run("no query at all", func(t *testing.T) {
		e := cpStart(t, cpAnswer(200, `{"outcome":"sent"}`), nil)
		if rec := e.serve(http.MethodGet, "/readiness-check/confirm", ""); rec.Code != http.StatusNotFound || e.f.count() != 0 {
			t.Errorf("status %d, worker calls %d; want 404 and none", rec.Code, e.f.count())
		}
	})
}

func TestConfirmPage_PressSendsTheServerSentenceToTheWorker(t *testing.T) {
	bodies := map[string][2]string{
		"another consent_text":  {"consent_text=other&token=" + strings.Repeat("Z", 43), "application/x-www-form-urlencoded"},
		"a JSON body":           {`{"consent_text":"other","token":"` + strings.Repeat("Z", 43) + `"}`, "application/json"},
		"an empty body":         {"", ""},
		"a consent_text of 600": {"consent_text=" + strings.Repeat("x", 600), "application/x-www-form-urlencoded"},
	}
	for name, b := range bodies {
		t.Run(name, func(t *testing.T) {
			e := cpStart(t, cpAnswer(200, `{"outcome":"sent"}`), nil)
			req := httptest.NewRequest(http.MethodPost, cpAction+cpToken, strings.NewReader(b[0]))
			if b[1] != "" {
				req.Header.Set("Content-Type", b[1])
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			cpParse(t, rec).wantDone(t)
			if len(e.f.calls) != 1 {
				t.Fatalf("worker calls = %d, want 1", len(e.f.calls))
			}
			c := e.f.calls[0]
			if c.method != http.MethodPost || c.path != cpPath || c.gateway != cpGWToken || !strings.HasPrefix(c.contentType, "application/json") {
				t.Errorf("worker call = %s %s, gateway token %q, content type %q", c.method, c.path, c.gateway, c.contentType)
			}
			got := cpBody(t, c)
			if len(got) != 2 || got["token"] != cpToken || got["consent_text"] != checkConsentText {
				t.Errorf("worker body = %s, want exactly the gateway token and checkConsentText", c.body)
			}
		})
	}
}

// The press survives the visitor leaving: the worker call runs on a context that the request's cancel does not end.
func TestConfirmPage_AClientCancelDuringTheWorkerCallStillCompletesIt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	ctxErr := make(chan error, 1)
	var answered sync.WaitGroup
	answered.Add(1)
	e := cpStart(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		ctxErr <- r.Context().Err()
		cpAnswer(200, `{"outcome":"sent"}`)(w, r)
		answered.Done()
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest(http.MethodPost, cpAction+cpToken, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { e.h.ServeHTTP(rec, req); close(done) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker was never called")
	}
	cancel()
	time.Sleep(200 * time.Millisecond) // a call tied to the request would be abandoned here
	close(release)
	select {
	case err := <-ctxErr:
		if err != nil {
			t.Errorf("the worker's request context ended: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never finished")
	}
	answered.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never returned")
	}
	if n := e.f.count(); n != 1 {
		t.Errorf("worker calls = %d, want 1", n)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: the worker answered sent", rec.Code)
	}
}

func TestConfirmPage_AWorker401Or403IsTheRetryPage(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			e := cpStart(t, cpAnswer(status, `{"error":"`+cpAddr+`"}`), nil)
			rec := e.serve(http.MethodPost, cpAction+cpToken, "")
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			cpParse(t, rec).wantRetry(t)
			logs := e.logs.String()
			want := "upstream_status=" + strconv.Itoa(status)
			if strings.Count(logs, "level=WARN") != 1 || !strings.Contains(logs, cpWarnMsg) || !strings.Contains(logs, want) {
				t.Errorf("want one WARN %q with %s:\n%s", cpWarnMsg, want, logs)
			}
		})
	}
}

func TestConfirmPage_UsedShowsTheSameDoneCopy(t *testing.T) {
	press := func(outcome string) string {
		e := cpStart(t, cpAnswer(200, `{"outcome":"`+outcome+`"}`), nil)
		rec := e.serve(http.MethodPost, cpAction+cpToken, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", outcome, rec.Code)
		}
		cpParse(t, rec).wantDone(t)
		return rec.Body.String()
	}
	sent, used := press("sent"), press("used")
	if sent == "" || sent != used {
		t.Errorf("the used page differs from the sent page:\n%s\n---\n%s", used, sent)
	}
}

func TestConfirmPage_GoneShowsTheExpiredCopy(t *testing.T) {
	e := cpStart(t, cpAnswer(http.StatusGone, `{"error":"gone"}`), nil)
	rec := e.serve(http.MethodPost, cpAction+cpToken, "")
	if rec.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", rec.Code)
	}
	cpParse(t, rec).wantExpired(t)
	if n := e.f.count(); n != 1 {
		t.Errorf("worker calls = %d, want 1", n)
	}
}

func TestConfirmPage_AnUpstreamFailureOffersTheButtonAgain(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	drop := func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }
	cases := []struct {
		name   string
		reply  func(http.ResponseWriter, *http.Request)
		client *http.Client
		status string
	}{
		{"400", cpAnswer(400, `{"error":"bad request"}`), nil, "400"},
		{"404", cpAnswer(404, ``), nil, "404"},
		{"409", cpAnswer(409, `{"error":"sign_up"}`), nil, "409"},
		{"500", cpAnswer(500, `{"error":"internal error"}`), nil, "500"},
		{"503", cpAnswer(503, `{"error":"failed"}`), nil, "503"},
		{"200 with an unknown outcome", cpAnswer(200, `{"outcome":"maybe"}`), nil, "200"},
		{"200 with no outcome", cpAnswer(200, `{}`), nil, "200"},
		{"200 not json", cpAnswer(200, `not json`), nil, "200"},
		{"transport error", drop, nil, "0"},
		{"timeout", slow, &http.Client{Timeout: 200 * time.Millisecond}, "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := cpStart(t, c.reply, c.client)
			rec := e.serve(http.MethodPost, cpAction+cpToken, "")
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			cpParse(t, rec).wantRetry(t)
			logs := e.logs.String()
			if strings.Count(logs, "level=WARN") != 1 || !strings.Contains(logs, cpWarnMsg) || !strings.Contains(logs, "upstream_status="+c.status) {
				t.Errorf("want one WARN %q with upstream_status=%s:\n%s", cpWarnMsg, c.status, logs)
			}
		})
	}
}

func TestConfirmPage_SetsTheSiblingHeaders(t *testing.T) {
	pages := []struct {
		name, method, token string
		reply               func(http.ResponseWriter, *http.Request)
	}{
		{"GET ask", http.MethodGet, cpToken, cpAnswer(200, `{"outcome":"sent"}`)},
		{"POST done", http.MethodPost, cpToken, cpAnswer(200, `{"outcome":"sent"}`)},
		{"POST expired", http.MethodPost, cpToken, cpAnswer(410, `{"error":"gone"}`)},
		{"POST retry", http.MethodPost, cpToken, cpAnswer(503, `{"error":"failed"}`)},
		{"GET malformed", http.MethodGet, "abc", cpAnswer(200, `{"outcome":"sent"}`)},
	}
	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			rec := cpServe(cpStart(t, p.reply, nil).h, p.method, cpAction+p.token, "")
			doc := vpParse(t, rec.Body.String())
			for k, v := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "Content-Type": "text/html; charset=utf-8"} {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			wantCSP := strings.Replace(vpWantCSP, "%s", vpHash(vpScript(t, doc)), 1)
			if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("CSP = %q, want %q", got, wantCSP)
			}
			var noindex bool
			for _, m := range vpFind(doc, vpTag("meta")) {
				name, _ := vpAttr(m, "name")
				content, _ := vpAttr(m, "content")
				noindex = noindex || (name == "robots" && content == "noindex")
			}
			if !noindex {
				t.Error(`the page lacks <meta name="robots" content="noindex">`)
			}
		})
	}
}

// The token and the address must reach no log line, on any branch.
func TestConfirmPage_LogsNoTokenOrAddress(t *testing.T) {
	leak := `{"error":"` + cpAddr + `"}`
	steps := []struct {
		method, token, body string
		reply               func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodGet, cpToken, "", cpAnswer(200, leak)},
		{http.MethodGet, "abc", "", cpAnswer(200, leak)},
		{http.MethodPost, "abc", "", cpAnswer(200, leak)},
		{http.MethodPost, cpToken, "email=" + cpAddr, cpAnswer(200, `{"outcome":"sent"}`)},
		{http.MethodPost, cpToken, "email=" + cpAddr, cpAnswer(200, `{"outcome":"used"}`)},
		{http.MethodPost, cpToken, "", cpAnswer(410, leak)},
		{http.MethodPost, cpToken, "", cpAnswer(401, leak)},
		{http.MethodPost, cpToken, "", cpAnswer(403, leak)},
		{http.MethodPost, cpToken, "", cpAnswer(400, leak)},
		{http.MethodPost, cpToken, "", cpAnswer(500, leak)},
		{http.MethodPost, cpToken, "", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }},
	}
	var all strings.Builder
	for _, s := range steps {
		e := cpStart(t, s.reply, nil)
		e.serve(s.method, cpAction+s.token, s.body)
		all.WriteString(e.logs.String())
	}
	logs := all.String()
	if !strings.Contains(logs, cpWarnMsg) {
		t.Fatalf("no failure was logged: the scan went blind:\n%s", logs)
	}
	for _, s := range []string{cpToken, cpAddr, "pii", "Ab3-_"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log carries %q:\n%s", s, logs)
		}
	}
}
