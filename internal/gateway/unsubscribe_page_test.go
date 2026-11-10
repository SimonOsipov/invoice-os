package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"
)

const (
	upToken     = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	upGWToken   = "gw-secret"
	upHeading   = "Unsubscribe from ASComply marketing email"
	upNote      = "Unsubscribing stops marketing email only. You will still get email about your account and your own requests, such as password resets, sign-in alerts and readiness check results."
	upDone      = "You are unsubscribed. We will send you no more marketing email. You will still get email about your account and your own requests, such as password resets, sign-in alerts and readiness check results. The blog newsletter has its own unsubscribe link."
	upTemporary = "We could not unsubscribe you just now. Try again in a few minutes."
	// unsubscribeContact in unsubscribe_page.go, which equals PRIVACY_CONTACT.
	upBad = "This unsubscribe link does not work. Email sam@ascomply.com and we will unsubscribe you."
)

type upCall struct{ path, token, gateway string }

// upFake is a notifications stand-in: lookup and press answer as set, every call is recorded.
type upFake struct {
	mu                    sync.Mutex
	calls                 []upCall
	lookupCode, pressCode int
	lookupBody, pressBody string
	pressDrops            bool
}

func (f *upFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.calls = append(f.calls, upCall{r.URL.Path, in.Token, r.Header.Get("X-Gateway-Token")})
	f.mu.Unlock()
	code, body := f.lookupCode, f.lookupBody
	if r.URL.Path == "/internal/contacts/unsubscribe" {
		if f.pressDrops {
			panic(http.ErrAbortHandler)
		}
		code, body = f.pressCode, f.pressBody
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func (f *upFake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.path)
	}
	return out
}

func upStart(t *testing.T, f *upFake) http.Handler {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return UnsubscribePageHandler(base, srv.Client(), upGWToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func upServe(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func upDoc(t *testing.T, rec *httptest.ResponseRecorder) (*html.Node, string) {
	t.Helper()
	doc := vpParse(t, rec.Body.String())
	var paras []string
	for _, p := range vpFind(doc, vpTag("p")) {
		paras = append(paras, strings.TrimSpace(vpText(p)))
	}
	return doc, strings.Join(paras, "\n")
}

func upH1(t *testing.T, doc *html.Node) string {
	t.Helper()
	h1 := vpFind(doc, vpTag("h1"))
	if len(h1) != 1 {
		t.Fatalf("h1 elements = %d, want 1", len(h1))
	}
	return strings.TrimSpace(vpText(h1[0]))
}

func TestUnsubscribePage_OpenShowsTheMaskedAddressAndOneButton(t *testing.T) {
	f := &upFake{lookupCode: 200, lookupBody: `{"masked":"s***@ascomply.com","unsubscribed":false}`}
	rec := upServe(upStart(t, f), http.MethodGet, "/unsubscribe?t="+upToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	doc, paras := upDoc(t, rec)
	if got := upH1(t, doc); got != upHeading {
		t.Errorf("h1 = %q, want %q", got, upHeading)
	}
	for _, want := range []string{"This stops marketing email to s***@ascomply.com.", upNote} {
		if !strings.Contains(paras, want) {
			t.Errorf("paragraphs %q lack %q", paras, want)
		}
	}
	buttons := vpFind(doc, vpTag("button"))
	if len(buttons) != 1 || strings.TrimSpace(vpText(buttons[0])) != "Unsubscribe" {
		t.Fatalf("buttons = %d, want one reading Unsubscribe", len(buttons))
	}
	forms := vpFind(doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	if action, _ := vpAttr(forms[0], "action"); action != "/unsubscribe?t="+upToken {
		t.Errorf("form action = %q", action)
	}
	if method, _ := vpAttr(forms[0], "method"); !strings.EqualFold(method, "post") {
		t.Errorf("form method = %q, want post", method)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "/internal/contacts/unsubscribe/lookup" {
		t.Errorf("upstream calls = %v, want the lookup only", got)
	}
	if c := f.calls[0]; c.token != upToken || c.gateway != upGWToken {
		t.Errorf("lookup carried token %q and gateway token %q", c.token, c.gateway)
	}
}

func TestUnsubscribePage_OpenOfAnUnsubscribedLinkShowsDone(t *testing.T) {
	f := &upFake{lookupCode: 200, lookupBody: `{"masked":"s***@ascomply.com","unsubscribed":true}`}
	rec := upServe(upStart(t, f), http.MethodGet, "/unsubscribe?t="+upToken, "")
	doc, paras := upDoc(t, rec)
	if rec.Code != http.StatusOK || paras != upDone {
		t.Errorf("status %d, paragraphs %q; want 200 and the done copy", rec.Code, paras)
	}
	if got := upH1(t, doc); got != upHeading {
		t.Errorf("h1 = %q", got)
	}
	if n := len(vpFind(doc, vpTag("button"))); n != 0 {
		t.Errorf("buttons = %d, want 0", n)
	}
}

func TestUnsubscribePage_ABadLinkNamesThePrivacyAddress(t *testing.T) {
	cases := []struct {
		name, target string
		lookups      int
	}{
		{"missing", "/unsubscribe", 0},
		{"malformed", "/unsubscribe?t=abc", 0},
		{"unknown", "/unsubscribe?t=" + upToken, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &upFake{lookupCode: 404, lookupBody: `{"error":"unknown link"}`}
			rec := upServe(upStart(t, f), http.MethodGet, c.target, "")
			doc, paras := upDoc(t, rec)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if paras != upBad+"\n"+upNote {
				t.Errorf("paragraphs = %q, want the bad-link copy and the note", paras)
			}
			if n := len(vpFind(doc, vpTag("button"))); n != 0 {
				t.Errorf("buttons = %d, want 0", n)
			}
			if got := len(f.paths()); got != c.lookups {
				t.Errorf("upstream calls = %d, want %d", got, c.lookups)
			}
		})
	}
	t.Run("press of a malformed token", func(t *testing.T) {
		f := &upFake{pressCode: 200, pressBody: `{"status":"unsubscribed"}`}
		rec := upServe(upStart(t, f), http.MethodPost, "/unsubscribe?t=abc", "")
		if rec.Code != http.StatusNotFound || len(f.paths()) != 0 {
			t.Errorf("status %d, upstream calls %v; want 404 and none", rec.Code, f.paths())
		}
	})
	t.Run("press of an unknown token", func(t *testing.T) {
		f := &upFake{pressCode: 404, pressBody: `{"error":"unknown link"}`}
		rec := upServe(upStart(t, f), http.MethodPost, "/unsubscribe?t="+upToken, "")
		if _, paras := upDoc(t, rec); rec.Code != http.StatusNotFound || !strings.HasPrefix(paras, upBad) {
			t.Errorf("status %d, paragraphs %q; want 404 and the bad-link copy", rec.Code, paras)
		}
	})
}

func TestUnsubscribePage_AFailureOffersTheButtonAgain(t *testing.T) {
	cases := []struct {
		name, method string
		fake         *upFake
	}{
		{"lookup 500", http.MethodGet, &upFake{lookupCode: 500, lookupBody: `{"error":"lookup failed"}`}},
		{"lookup unreadable", http.MethodGet, &upFake{lookupCode: 200, lookupBody: `not json`}},
		{"press 503", http.MethodPost, &upFake{pressCode: 503, pressBody: `{"error":"unsubscribe is unavailable"}`}},
		{"press transport error", http.MethodPost, &upFake{pressDrops: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := upServe(upStart(t, c.fake), c.method, "/unsubscribe?t="+upToken, "")
			doc, paras := upDoc(t, rec)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if paras != upTemporary+"\n"+upNote {
				t.Errorf("paragraphs = %q, want the temporary copy and the note", paras)
			}
			if n := len(vpFind(doc, vpTag("button"))); n != 1 {
				t.Errorf("buttons = %d, want 1", n)
			}
		})
	}
}

func TestUnsubscribePage_PressShowsTheDoneCopy(t *testing.T) {
	f := &upFake{pressCode: 200, pressBody: `{"status":"unsubscribed"}`}
	rec := upServe(upStart(t, f), http.MethodPost, "/unsubscribe?t="+upToken, "")
	doc, paras := upDoc(t, rec)
	if rec.Code != http.StatusOK || paras != upDone {
		t.Errorf("status %d, paragraphs %q; want 200 and the done copy", rec.Code, paras)
	}
	if n := len(vpFind(doc, vpTag("button"))); n != 0 {
		t.Errorf("buttons = %d, want 0", n)
	}
	if len(f.calls) != 1 || f.calls[0].path != "/internal/contacts/unsubscribe" || f.calls[0].token != upToken || f.calls[0].gateway != upGWToken {
		t.Errorf("upstream calls = %+v, want one press with the token and the gateway token", f.calls)
	}
}

func TestUnsubscribePage_OneClickPostUnsubscribes(t *testing.T) {
	f := &upFake{pressCode: 200, pressBody: `{"status":"unsubscribed"}`}
	rec := upServe(upStart(t, f), http.MethodPost, "/unsubscribe?t="+upToken, "List-Unsubscribe=One-Click")
	if _, paras := upDoc(t, rec); rec.Code != http.StatusOK || paras != upDone {
		t.Errorf("status %d, paragraphs %q; want 200 and the done copy", rec.Code, paras)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "/internal/contacts/unsubscribe" {
		t.Errorf("upstream calls = %v, want one press", got)
	}
}

func TestUnsubscribePage_AlreadyShowsTheSameDoneCopy(t *testing.T) {
	body := func(status string) string {
		f := &upFake{pressCode: 200, pressBody: `{"status":"` + status + `"}`}
		return upServe(upStart(t, f), http.MethodPost, "/unsubscribe?t="+upToken, "").Body.String()
	}
	if a, b := body("unsubscribed"), body("already"); a != b || !strings.Contains(a, upDone) {
		t.Errorf("the already page differs from the unsubscribed page")
	}
}

func TestUnsubscribePage_SetsTheSiblingHeadersAndEscapes(t *testing.T) {
	f := &upFake{lookupCode: 200, lookupBody: `{"masked":"<b>@x","unsubscribed":false}`}
	h := upStart(t, f)
	rec := upServe(h, http.MethodGet, "/unsubscribe?t="+upToken, "")
	if strings.Contains(rec.Body.String(), "<b>@x") || !strings.Contains(rec.Body.String(), "&lt;b&gt;@x") {
		t.Errorf("the masked address is not escaped: %q", rec.Body.String())
	}
	doc, _ := upDoc(t, rec)
	want := map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer"}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	scripts := vpFind(doc, vpTag("script"))
	if len(scripts) != 1 {
		t.Fatalf("script elements = %d, want 1", len(scripts))
	}
	wantCSP := strings.Replace(vpWantCSP, "%s", vpHash(vpText(scripts[0])), 1)
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("CSP = %q, want %q", got, wantCSP)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodOptions} {
		r := upServe(h, method, "/unsubscribe?t="+upToken, "")
		if r.Code != http.StatusMethodNotAllowed || r.Header().Get("Allow") != "GET, HEAD, POST" {
			t.Errorf("%s: status %d, Allow %q; want 405 and GET, HEAD, POST", method, r.Code, r.Header().Get("Allow"))
		}
		if r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("Content-Security-Policy") != wantCSP {
			t.Errorf("%s: the 405 lacks the sibling headers", method)
		}
	}
	if head := upServe(h, http.MethodHead, "/unsubscribe?t="+upToken, ""); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, body %q; want 200 and no body", head.Code, head.Body.String())
	}
}

// A token with markup in it never reaches the form: the malformed-token path renders no form at all.
func TestUnsubscribePage_ATokenIsNeverRendered(t *testing.T) {
	f := &upFake{lookupCode: 200, lookupBody: `{"masked":"s***@x","unsubscribed":false}`}
	rec := upServe(upStart(t, f), http.MethodGet, "/unsubscribe?t="+url.QueryEscape(`"><script>x</script>`), "")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<script>x") || len(f.paths()) != 0 {
		t.Errorf("status %d, upstream %v, body holds the token: %v", rec.Code, f.paths(), strings.Contains(rec.Body.String(), "<script>x"))
	}
}
