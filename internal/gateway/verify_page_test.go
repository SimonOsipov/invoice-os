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
	vpState   = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde" // 43 base64url, stateShape
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

func vpQueryBare(token string) string { return "token=" + url.QueryEscape(token) + "&type=signup" }

// vpQuery is a link carrying a valid state, the only kind that renders the page.
func vpQuery(token string) string { return vpQueryBare(token) + "&state=" + vpState }

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
	titles := vpFind(doc, vpTag("title"))
	if len(titles) != 1 || vpText(titles[0]) != "Confirm your email \u00b7 ASComply" {
		t.Errorf("title elements = %d, want exactly 1 reading %q", len(titles), "Confirm your email \u00b7 ASComply")
	}
	var paras []string
	for _, p := range vpFind(doc, vpTag("p")) {
		paras = append(paras, strings.TrimSpace(vpText(p)))
	}
	want := []string{
		"Click the button to finish creating your ASComply account.",
		"If you did not create an ASComply account, close this page.",
	}
	if !slices.Equal(paras, want) {
		t.Errorf("paragraphs = %q, want %q", paras, want)
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
	if rec := vpDo(t, vpHandler(t), http.MethodHead, vpQuery(vpToken)); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD at the handler: status %d, body %q; want 200 and no body", rec.Code, rec.Body.String())
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
	if len(hidden) != 3 || hidden["token"] != vpToken || hidden["type"] != "signup" || hidden["state"] != vpState {
		t.Errorf("hidden inputs = %v, want token=%q, type=signup and state=%q only", hidden, vpToken, vpState)
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

func TestVerifyPage_RevealsNothingButTheTokenAndState(t *testing.T) {
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
	rep := vpDo(t, h, http.MethodGet, "token=first-one&token=second-one&type=signup&state="+vpState)
	if rep.Code != http.StatusOK || !strings.Contains(rep.Body.String(), "first-one") || strings.Contains(rep.Body.String(), "second-one") {
		t.Errorf("repeated token: status %d; want 200 rendering only the first value", rep.Code)
	}
	for _, leak := range []string{"evil.example", "leak"} {
		if strings.Contains(recA.Body.String(), leak) {
			t.Errorf("body contains %q from the query", leak)
		}
	}
}

func TestVerifyPage_TokenIsEscaped(t *testing.T) {
	_, base := vpPage(t, vpToken)
	baseScript := vpScript(t, base)
	baseElems := len(vpFind(base, func(*html.Node) bool { return true }))
	for name, raw := range map[string]string{
		"quote and script":     `"><script>alert(1)</script>`,
		"closing script":       `</script><script>alert(1)</script>`,
		"quotes and ampersand": `'"&amp;<>`,
		"comment opener":       `<!--`,
		"javascript scheme":    `javascript:alert(1)`,
		"template syntax":      `{{.Script}}{{.Token}}`,
		"space plus percent":   `a b+c%20d`,
		"unicode":              "ключ-令牌-\U0001F511",
		"attribute breakout":   `x" onfocus="alert(1)" autofocus="`,
	} {
		t.Run(name, func(t *testing.T) {
			_, doc := vpPage(t, raw)
			if script := vpScript(t, doc); script != baseScript {
				t.Errorf("the one script = %q, want the submit-once script %q", script, baseScript)
			}
			if n := len(vpFind(doc, func(*html.Node) bool { return true })); n != baseElems {
				t.Errorf("elements = %d, want %d: the token added markup", n, baseElems)
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
		})
	}
}

// The token lands in the one hidden input and nowhere else; the script is rendered once, even from a token that names it.
func TestVerifyPage_TokenAndScriptFillOnlyTheirPlaceholders(t *testing.T) {
	for name, token := range map[string]string{
		"plain":                 "MARKERtoken123",
		"names the script slot": "MARKER{{.Script}}",
		"names the token slot":  "MARKER{{.Token}}",
		"escapable":             `MARKER"'<>&`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, doc := vpPage(t, token)
			body := rec.Body.String()
			if strings.Contains(body, "{{.Script}}") != strings.Contains(token, "{{.Script}}") ||
				strings.Contains(body, "{{.Token}}") != strings.Contains(token, "{{.Token}}") {
				t.Errorf("a placeholder is left unfilled in the page or leaked from the token")
			}
			if n := strings.Count(body, verifyScript); n != 1 {
				t.Errorf("the submit-once script occurs %d times in the page, want 1", n)
			}
			var hits []string
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if strings.Contains(n.Data, "MARKER") && n.Type != html.ElementNode {
					hits = append(hits, "node:"+n.Data)
				}
				for _, a := range n.Attr {
					if strings.Contains(a.Val, "MARKER") {
						hits = append(hits, n.Data+"["+a.Key+"]")
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			if want := []string{"input[value]"}; !slices.Equal(hits, want) {
				t.Errorf("the token appears at %v, want only %v", hits, want)
			}
		})
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

// The flag set by the first submit is cleared on pageshow, so a bfcache restore or an aborted
// submit leaves a live button. No JS runtime here; the script text is pinned.
func TestVerifyPage_ScriptRearmsOnPageshow(t *testing.T) {
	_, doc := vpPage(t, vpToken)
	script := vpScript(t, doc)
	for _, want := range []string{"addEventListener('pageshow'", "delete f.dataset.sent"} {
		if !strings.Contains(script, want) {
			t.Errorf("the inline script lacks %q", want)
		}
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
		for _, k := range []string{"src", "href", "action", "formaction"} {
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
	styles := vpFind(doc, vpTag("style"))
	if len(styles) == 0 {
		t.Fatal("the page has no <style>, so the style sweep proves nothing")
	}
	for _, st := range styles {
		css := vpText(st)
		if strings.TrimSpace(css) == "" {
			t.Error("the <style> is empty")
		}
		for _, bad := range []string{"@import", "url(", "://"} {
			if strings.Contains(css, bad) {
				t.Errorf("the <style> holds %q: an external load", bad)
			}
		}
	}
	for _, tag := range []string{"iframe", "frame", "object", "embed", "base", "meta http-equiv"} {
		if tag == "meta http-equiv" {
			if m := vpFind(doc, func(n *html.Node) bool { _, has := vpAttr(n, "http-equiv"); return n.Data == "meta" && has }); len(m) != 0 {
				t.Errorf("page holds %d <meta http-equiv>", len(m))
			}
		} else if e := vpFind(doc, vpTag(tag)); len(e) != 0 {
			t.Errorf("page holds %d <%s>", len(e), tag)
		}
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
		"type SIGNUP":          "token=" + vpToken + "&type=SIGNUP",
		"type padded":          "token=" + vpToken + "&type=%20signup",
		"type suffixed":        "token=" + vpToken + "&type=signupx",
		"type prefix only":     "token=" + vpToken + "&type=sign",
		"token key upper":      "TOKEN=" + vpToken + "&type=signup",
		"type key upper":       "token=" + vpToken + "&TYPE=signup",
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

func TestVerifyPage_TokenLengthCapCountsBytes(t *testing.T) {
	h := vpHandler(t)
	atCap := strings.Repeat("\u00e9", vpMaxLen/2)
	if rec := vpDo(t, h, http.MethodGet, vpQuery(atCap)); rec.Code != http.StatusOK {
		t.Errorf("%d-byte token of 2-byte runes: status %d, want 200", len(atCap), rec.Code)
	}
	over := atCap + "\u00e9"
	if rec := vpDo(t, h, http.MethodGet, vpQuery(over)); rec.Code != http.StatusSeeOther {
		t.Errorf("%d-byte token of %d runes: status %d, want 303", len(over), len([]rune(over)), rec.Code)
	}
}

func TestVerifyPage_RedirectUsesTheSiteWithoutDoubleSlash(t *testing.T) {
	site, err := url.Parse(siteURLValue + "/")
	if err != nil {
		t.Fatal(err)
	}
	h, err := VerifyPageHandler(site)
	if err != nil {
		t.Fatal(err)
	}
	rec := vpDo(t, h, http.MethodGet, "token=&type=signup")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != failedLocation {
		t.Errorf("answer = %d Location %q, want 303 %q", rec.Code, rec.Header().Get("Location"), failedLocation)
	}
}

func TestVerifyPage_RepeatedTypeFirstValueWins(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodGet, "token="+vpToken+"&type=signup&type=recovery&state="+vpState)
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
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodOptions, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			rec := vpDo(t, h, method, "token=&type=signup")
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

const bounceLocation = siteURLValue + "/?confirm=1#token=" + vpToken

func vpBounced(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("the bounce holds a form: %q", rec.Body.String())
	}
}

func TestVerifyPage_StatelessOpenBouncesToLanding(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodGet, vpQueryBare(vpToken))
	vpBounced(t, rec, bounceLocation)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestVerifyPage_MalformedStateBounces(t *testing.T) {
	h := vpHandler(t)
	for name, state := range map[string]string{
		"42 chars": vpState[:42],
		"44 chars": vpState + "a",
		"plus":     vpState[:42] + "+",
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			vpBounced(t, vpDo(t, h, http.MethodGet, vpQueryBare(vpToken)+"&state="+url.QueryEscape(state)), bounceLocation)
		})
	}
	t.Run("repeated state, bad first", func(t *testing.T) {
		vpBounced(t, vpDo(t, h, http.MethodGet, vpQueryBare(vpToken)+"&state=short&state="+vpState), bounceLocation)
	})
}

func TestVerifyPage_HeadWithoutStateBounces(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodHead, vpQueryBare(vpToken))
	vpBounced(t, rec, bounceLocation)
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD bounce body = %q, want empty", rec.Body.String())
	}
}

func TestVerifyPage_BounceEscapesTheToken(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodGet, vpQueryBare("a&b=c"))
	vpBounced(t, rec, siteURLValue+"/?confirm=1#token=a%26b%3Dc")
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.RawQuery != "confirm=1" {
		t.Errorf("site query = %q, want only confirm=1", loc.RawQuery)
	}
}

func TestVerifyPage_StateRendersAsHiddenField(t *testing.T) {
	_, doc := vpPage(t, vpToken)
	forms := vpFind(doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want 1", len(forms))
	}
	got := map[string]string{}
	for _, in := range vpFind(forms[0], vpTag("input")) {
		name, _ := vpAttr(in, "name")
		val, _ := vpAttr(in, "value")
		got[name] = val
	}
	want := map[string]string{"token": vpToken, "type": "signup", "state": vpState}
	if len(got) != len(want) || got["token"] != want["token"] || got["type"] != want["type"] || got["state"] != want["state"] {
		t.Errorf("inputs = %v, want %v", got, want)
	}
}

func TestVerifyPage_BadTokenFailsBeforeTheStateCheck(t *testing.T) {
	h := vpHandler(t)
	for name, query := range map[string]string{
		"empty token, valid state": "token=&type=signup&state=" + vpState,
		"recovery, no state":       "token=" + vpToken + "&type=recovery",
		"no token, no state":       "type=signup",
	} {
		t.Run(name, func(t *testing.T) {
			vpBounced(t, vpDo(t, h, http.MethodGet, query), failedLocation)
		})
	}
}
