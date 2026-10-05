package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const (
	vpToken   = "tok-Abc123_xyz"
	vpPath    = "/auth/verify"
	vpIcon    = "/emails/mark.png"
	vpMaxLen  = 256 // maxVerifyTokenBytes
	vpWantCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src %s; base-uri 'none'; frame-ancestors 'none'"
)

func vpHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := VerifyPageHandler(siteURL(t))
	if err != nil {
		t.Fatalf("VerifyPageHandler: %v", err)
	}
	if h == nil {
		t.Fatal("VerifyPageHandler returned a nil handler")
	}
	return h
}

func vpDo(t *testing.T, h http.Handler, method, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, vpPath+"?"+query, nil))
	return rec
}

func vpQuery(token string) string { return "token=" + url.QueryEscape(token) + "&type=signup" }

func vpParse(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	return doc
}

func vpFind(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && match(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func vpTag(tag string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Data == tag }
}

func vpAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func vpText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// vpPage serves the page for token and fails unless it is a 200 with a parsed document.
func vpPage(t *testing.T, token string) (*httptest.ResponseRecorder, *html.Node) {
	t.Helper()
	rec := vpDo(t, vpHandler(t), http.MethodGet, vpQuery(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	return rec, vpParse(t, rec.Body.String())
}

func vpScript(t *testing.T, doc *html.Node) string {
	t.Helper()
	scripts := vpFind(doc, vpTag("script"))
	if len(scripts) != 1 {
		t.Fatalf("script elements = %d, want exactly 1", len(scripts))
	}
	return vpText(scripts[0])
}

func vpHash(script string) string {
	sum := sha256.Sum256([]byte(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestVerifyPage_GetRendersThePage(t *testing.T) {
	rec, doc := vpPage(t, vpToken)
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	h1 := vpFind(doc, vpTag("h1"))
	if len(h1) == 0 {
		t.Fatal("page has no <h1>")
	}
	if got := strings.TrimSpace(vpText(h1[0])); got != "Confirm your email address" {
		t.Errorf("h1 = %q, want %q", got, "Confirm your email address")
	}
}

func TestVerifyPage_HeadAnswersLikeGet(t *testing.T) {
	srv := httptest.NewServer(vpHandler(t))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method string) (*http.Response, string) {
		req, err := http.NewRequest(method, srv.URL+vpPath+"?"+vpQuery(vpToken), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	get, getBody := do(http.MethodGet)
	head, headBody := do(http.MethodHead)
	if get.StatusCode != http.StatusOK || head.StatusCode != http.StatusOK {
		t.Fatalf("status GET=%d HEAD=%d, want 200 for both", get.StatusCode, head.StatusCode)
	}
	if !strings.Contains(getBody, "<form") {
		t.Fatalf("GET body holds no form: %q", getBody)
	}
	if headBody != "" {
		t.Errorf("HEAD body = %q, want empty", headBody)
	}
	gh, hh := get.Header.Clone(), head.Header.Clone()
	gh.Del("Date")
	hh.Del("Date")
	if len(gh) == 0 {
		t.Fatal("GET sent no headers")
	}
	if !headersEqual(gh, hh) {
		t.Errorf("HEAD headers differ from GET:\nGET  %v\nHEAD %v", gh, hh)
	}
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if !slices.Equal(av, b[k]) {
			return false
		}
	}
	return true
}

func TestVerifyPage_OneFormOneConfirmButton(t *testing.T) {
	_, doc := vpPage(t, vpToken)
	forms := vpFind(doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want exactly 1", len(forms))
	}
	form := forms[0]
	if got, _ := vpAttr(form, "method"); got != "post" {
		t.Errorf("form method = %q, want post", got)
	}
	if got, _ := vpAttr(form, "action"); got != vpPath {
		t.Errorf("form action = %q, want %s", got, vpPath)
	}
	hidden := map[string]string{}
	for _, in := range vpFind(form, vpTag("input")) {
		name, _ := vpAttr(in, "name")
		typ, _ := vpAttr(in, "type")
		val, _ := vpAttr(in, "value")
		if typ != "hidden" {
			t.Errorf("input %q has type %q, want hidden", name, typ)
		}
		hidden[name] = val
	}
	if len(hidden) != 2 || hidden["token"] != vpToken || hidden["type"] != "signup" {
		t.Errorf("hidden inputs = %v, want token=%q and type=signup only", hidden, vpToken)
	}
	submits := vpFind(doc, func(n *html.Node) bool {
		typ, has := vpAttr(n, "type")
		switch n.Data {
		case "button":
			return !has || typ == "submit"
		case "input":
			return typ == "submit" || typ == "image"
		}
		return false
	})
	if len(submits) != 1 {
		t.Fatalf("submit controls in the document = %d, want exactly 1", len(submits))
	}
	btn := submits[0]
	if btn.Data != "button" {
		t.Errorf("submit control is <%s>, want <button>", btn.Data)
	}
	if typ, _ := vpAttr(btn, "type"); typ != "submit" {
		t.Errorf(`button type = %q, want "submit"`, typ)
	}
	if got := strings.TrimSpace(vpText(btn)); got != "Confirm my email" {
		t.Errorf("button text = %q, want %q", got, "Confirm my email")
	}
}

func TestVerifyPage_RevealsNothingButTheToken(t *testing.T) {
	h := vpHandler(t)
	tokA, tokB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	recA := vpDo(t, h, http.MethodGet, vpQuery(tokA)+"&redirect_to="+url.QueryEscape("https://evil.example/x")+"&extra=leak")
	recB := vpDo(t, h, http.MethodGet, vpQuery(tokB))
	if recA.Code != http.StatusOK || recB.Code != http.StatusOK {
		t.Fatalf("status A=%d B=%d, want 200 for both", recA.Code, recB.Code)
	}
	bodyA := strings.ReplaceAll(recA.Body.String(), tokA, "TOKEN")
	bodyB := strings.ReplaceAll(recB.Body.String(), tokB, "TOKEN")
	if !strings.Contains(bodyA, "TOKEN") {
		t.Fatal("the page never echoes the token, so the comparison proves nothing")
	}
	if bodyA != bodyB {
		t.Errorf("bodies differ beyond the token:\nA %q\nB %q", bodyA, bodyB)
	}
	if !headersEqual(recA.Header(), recB.Header()) {
		t.Errorf("headers differ:\nA %v\nB %v", recA.Header(), recB.Header())
	}
	for _, leak := range []string{"evil.example", "leak"} {
		if strings.Contains(recA.Body.String(), leak) {
			t.Errorf("body contains %q from the query", leak)
		}
	}
}

func TestVerifyPage_TokenIsEscaped(t *testing.T) {
	const raw = `"><script>alert(1)</script>`
	_, doc := vpPage(t, raw)
	if script := vpScript(t, doc); strings.Contains(script, "alert") {
		t.Errorf("the one script holds the token: %q", script)
	}
	var got []string
	for _, in := range vpFind(doc, vpTag("input")) {
		if name, _ := vpAttr(in, "name"); name == "token" {
			v, _ := vpAttr(in, "value")
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != raw {
		t.Errorf("token input values = %q, want [%q]", got, raw)
	}
}

func TestVerifyPage_SecurityHeaders(t *testing.T) {
	h := vpHandler(t)
	_, doc := vpPage(t, vpToken)
	wantCSP := strings.Replace(vpWantCSP, "%s", vpHash(vpScript(t, doc)), 1)
	cases := []struct {
		name, method, query string
		status              int
	}{
		{"get page", http.MethodGet, vpQuery(vpToken), http.StatusOK},
		{"head page", http.MethodHead, vpQuery(vpToken), http.StatusOK},
		{"get empty token", http.MethodGet, "token=&type=signup", http.StatusSeeOther},
		{"head empty token", http.MethodHead, "token=&type=signup", http.StatusSeeOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := vpDo(t, h, c.method, c.query)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d", rec.Code, c.status)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q, want no-referrer", got)
			}
			csp := rec.Header().Get("Content-Security-Policy")
			if csp != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", csp, wantCSP)
			}
			if strings.Contains(csp, "form-action") {
				t.Errorf("CSP names form-action: %q", csp)
			}
		})
	}
}

func TestVerifyPage_ScriptHashMatchesCSP(t *testing.T) {
	rec, doc := vpPage(t, vpToken)
	script := vpScript(t, doc)
	if strings.TrimSpace(script) == "" {
		t.Fatal("the inline script is empty")
	}
	var srcs []string
	for _, d := range strings.Split(rec.Header().Get("Content-Security-Policy"), ";") {
		if f := strings.Fields(d); len(f) > 0 && f[0] == "script-src" {
			srcs = append(srcs, f[1:]...)
		}
	}
	if want := []string{vpHash(script)}; !slices.Equal(srcs, want) {
		t.Errorf("script-src sources = %v, want %v", srcs, want)
	}
}

func TestVerifyPage_LoadsOnlySameOriginResources(t *testing.T) {
	_, doc := vpPage(t, vpToken)
	vpScript(t, doc)
	if src, has := vpAttr(vpFind(doc, vpTag("script"))[0], "src"); has {
		t.Errorf("the script has src=%q, want inline", src)
	}
	var refs []string
	for _, n := range vpFind(doc, func(*html.Node) bool { return true }) {
		for _, k := range []string{"src", "href", "action"} {
			if v, has := vpAttr(n, k); has {
				refs = append(refs, v)
				if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
					t.Errorf("<%s %s=%q> is not a same-origin path", n.Data, k, v)
				}
			}
		}
	}
	if len(refs) == 0 {
		t.Fatal("the page references no resource, so the sweep proves nothing")
	}
	icon := vpFind(doc, func(n *html.Node) bool {
		rel, _ := vpAttr(n, "rel")
		href, _ := vpAttr(n, "href")
		return n.Data == "link" && rel == "icon" && href == vpIcon
	})
	if len(icon) != 1 {
		t.Errorf("link rel=icon href=%s count = %d, want 1", vpIcon, len(icon))
	}
}

func TestVerifyPage_MalformedLinkRedirectsToFailed(t *testing.T) {
	h := vpHandler(t)
	if rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("control: a valid link answered %d without a form", rec.Code)
	}
	for name, query := range map[string]string{
		"no token":             "type=signup",
		"empty token":          "token=&type=signup",
		"type absent":          "token=" + vpToken,
		"type empty":           "token=" + vpToken + "&type=",
		"type recovery":        "token=" + vpToken + "&type=recovery",
		"type Signup":          "token=" + vpToken + "&type=Signup",
		"recovery then signup": "token=" + vpToken + "&type=recovery&type=signup",
	} {
		t.Run(name, func(t *testing.T) {
			rec := vpDo(t, h, http.MethodGet, query)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != failedLocation {
				t.Errorf("Location = %q, want %q", got, failedLocation)
			}
			if strings.Contains(rec.Body.String(), "<form") {
				t.Errorf("body holds a form: %q", rec.Body.String())
			}
		})
	}
}

func TestVerifyPage_HeadWithEmptyTokenRedirects(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodHead, "token=&type=signup")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != failedLocation {
		t.Errorf("Location = %q, want %q", got, failedLocation)
	}
}

func TestVerifyPage_TokenLengthCap(t *testing.T) {
	h := vpHandler(t)
	atCap := strings.Repeat("a", vpMaxLen)
	if rec := vpDo(t, h, http.MethodGet, vpQuery(atCap)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), atCap) {
		t.Errorf("%d-byte token: status %d, token echoed = %v; want 200 and echoed", vpMaxLen, rec.Code, strings.Contains(rec.Body.String(), atCap))
	}
	over := strings.Repeat("a", vpMaxLen+1)
	rec := vpDo(t, h, http.MethodGet, vpQuery(over))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%d-byte token: status = %d, want 303", vpMaxLen+1, rec.Code)
	}
	if got := rec.Header().Get("Location"); got != failedLocation {
		t.Errorf("Location = %q, want %q", got, failedLocation)
	}
	if strings.Contains(rec.Body.String(), over) {
		t.Error("the refusal body echoes the over-long token")
	}
}

func TestVerifyPage_RepeatedTypeFirstValueWins(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodGet, "token="+vpToken+"&type=signup&type=recovery")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<form") {
		t.Error("no page was rendered")
	}
}

func TestVerifyPage_NotConfigured503(t *testing.T) {
	h, err := VerifyPageHandler(nil)
	if err != nil || h == nil {
		t.Fatalf("VerifyPageHandler(nil) = %v, %v; want a handler and nil", h, err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := vpDo(t, h, method, vpQuery(vpToken))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", method, rec.Code)
		}
		if method == http.MethodGet {
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"registration is not configured"}` {
				t.Errorf("GET body = %q", got)
			}
		}
	}
}

func TestVerifyPage_OtherMethods405(t *testing.T) {
	h := vpHandler(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			rec := vpDo(t, h, method, vpQuery(vpToken))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
			if strings.Contains(rec.Body.String(), "<form") {
				t.Errorf("body holds a form: %q", rec.Body.String())
			}
		})
	}
}
