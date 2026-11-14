package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
	h, err := VerifyPageHandler(siteURL(t), nil, lrThrottle(time.Now), true, nopLog())
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
			if n := strings.Count(body, resetScript); n != 1 {
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
			if csp != resetCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", csp, resetCSP)
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
	h, err := VerifyPageHandler(site, nil, lrThrottle(time.Now), true, nopLog())
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
	h, err := VerifyPageHandler(nil, nil, nil, true, nil)
	if err != nil || h == nil {
		t.Fatalf("VerifyPageHandler(nil, nil, nil, true, nil) = %v, %v; want a handler and nil", h, err)
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
	t.Run("state twice", func(t *testing.T) {
		vpBounced(t, vpDo(t, h, http.MethodGet, vpQueryBare(vpToken)+"&state="+vpState+"&state="+vpState), bounceLocation)
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

func vpInviteQuery(token string) string { return vpQuery(token) + "&invite=1" }

// vpFormAction is the action of the page's one form.
func vpFormAction(t *testing.T, body string) string {
	t.Helper()
	forms := vpFind(vpParse(t, body), vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want exactly 1 in %q", len(forms), body)
	}
	action, _ := vpAttr(forms[0], "action")
	return action
}

const inviteBounceLocation = siteURLValue + "/?confirm=invite#token=" + vpToken

func TestVerifyPage_StatelessInviteLinkBouncesToLanding(t *testing.T) {
	h := vpHandler(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := vpDo(t, h, method, vpQueryBare(vpToken)+"&invite=1")
			vpBounced(t, rec, inviteBounceLocation)
			if method == http.MethodHead && rec.Body.Len() != 0 {
				t.Errorf("HEAD body = %q, want empty", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
	t.Run("an empty token fails before the bounce", func(t *testing.T) {
		vpBounced(t, vpDo(t, h, http.MethodGet, "token=&type=signup&invite=1"), failedLocation)
	})
}

func TestVerifyPage_MalformedStateOnInviteLinkBounces(t *testing.T) {
	h := vpHandler(t)
	base := vpQueryBare(vpToken) + "&invite=1&state="
	for name, query := range map[string]string{
		"42 chars":    base + vpState[:42],
		"44 chars":    base + vpState + "a",
		"plus":        base + url.QueryEscape(vpState[:42]+"+"),
		"state twice": base + vpState + "&state=" + vpState,
	} {
		t.Run(name, func(t *testing.T) {
			vpBounced(t, vpDo(t, h, http.MethodGet, query), inviteBounceLocation)
		})
	}
}

func TestVerifyPage_InviteBounceEscapesTheToken(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodGet, vpQueryBare("a&b=c")+"&invite=1")
	vpBounced(t, rec, siteURLValue+"/?confirm=invite#token=a%26b%3Dc")
}

func vpInputValue(doc *html.Node, name string) string {
	for _, in := range vpFind(doc, vpTag("input")) {
		if n, _ := vpAttr(in, "name"); n == name {
			v, _ := vpAttr(in, "value")
			return v
		}
	}
	return ""
}

func TestVerifyPage_InvitePageHasOneBoundedPasswordForm(t *testing.T) {
	get := vpDo(t, vpHandler(t), http.MethodGet, vpInviteQuery(vpToken))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	doc := vpParse(t, get.Body.String())
	forms := vpFind(doc, vpTag("form"))
	if len(forms) != 1 {
		t.Fatalf("forms = %d, want exactly 1", len(forms))
	}
	if got, _ := vpAttr(forms[0], "method"); got != "post" {
		t.Errorf("form method = %q, want post", got)
	}
	if got, _ := vpAttr(forms[0], "action"); got != "/auth/invitation/password" {
		t.Errorf("form action = %q, want /auth/invitation/password", got)
	}
	inputs := map[string]*html.Node{}
	for _, in := range vpFind(forms[0], vpTag("input")) {
		name, _ := vpAttr(in, "name")
		inputs[name] = in
	}
	if len(inputs) != 4 {
		t.Fatalf("named inputs = %d, want exactly 4 (token, type, state, password)", len(inputs))
	}
	for name, want := range map[string]string{"token": vpToken, "type": "signup", "state": vpState} {
		in := inputs[name]
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
	if pw := inputs["password"]; pw == nil {
		t.Error("no password input")
	} else {
		for k, want := range map[string]string{"type": "password", "autocomplete": "new-password", "minlength": "6", "maxlength": "72"} {
			if got, ok := vpAttr(pw, k); !ok || got != want {
				t.Errorf("password input %s = %q (present %v), want %q", k, got, ok, want)
			}
		}
		if _, ok := vpAttr(pw, "required"); !ok {
			t.Error("password input is not required")
		}
	}
	h1 := vpFind(doc, vpTag("h1"))
	if len(h1) != 1 || strings.TrimSpace(vpText(h1[0])) != "Choose your password" {
		t.Errorf("h1 elements = %d, want one reading %q", len(h1), "Choose your password")
	}
	buttons := vpFind(doc, vpTag("button"))
	if len(buttons) != 1 || strings.TrimSpace(vpText(buttons[0])) != "Confirm and set password" {
		t.Errorf("buttons = %d, want one reading %q", len(buttons), "Confirm and set password")
	}

	wantCSP := strings.Replace(vpWantCSP, "%s", vpHash(vpScript(t, doc)), 1)
	for header, want := range map[string]string{
		"Content-Security-Policy": wantCSP, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer",
	} {
		if got := get.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := get.Header().Get("Content-Security-Policy"); got != resetCSP {
		t.Errorf("Content-Security-Policy = %q, want the reset page's %q", got, resetCSP)
	}
}

func TestVerifyPage_InviteLinkBadTokenOrTypeIsTheFailedNotice(t *testing.T) {
	h := vpHandler(t)
	for name, query := range map[string]string{
		"empty token":              "token=&type=signup&invite=1&state=" + vpState,
		"257-byte token":           vpInviteQuery(strings.Repeat("a", vpMaxLen+1)),
		"type recovery":            "token=" + vpToken + "&type=recovery&invite=1&state=" + vpState,
		"empty token, no state":    "token=&type=signup&invite=1",
		"257-byte token, no state": vpQueryBare(strings.Repeat("a", vpMaxLen+1)) + "&invite=1",
		"type recovery, no state":  "token=" + vpToken + "&type=recovery&invite=1",
	} {
		t.Run(name, func(t *testing.T) {
			rec := vpDo(t, h, http.MethodGet, query)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?verify=failed" {
				t.Errorf("answer = %d Location %q, want 303 https://site.example/?verify=failed", rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "<form") {
				t.Errorf("a bad link rendered a form: %q", rec.Body.String())
			}
		})
	}
	rec := vpDo(t, h, http.MethodGet, vpInviteQuery(strings.Repeat("a", vpMaxLen)))
	if rec.Code != http.StatusOK || vpFormAction(t, rec.Body.String()) != "/auth/invitation/password" {
		t.Errorf("control, 256-byte token: status %d; want 200 serving the set-password page", rec.Code)
	}
}

func TestVerifyPage_WithoutExactlyOneInviteFlagStillServesTheSetPasswordPage(t *testing.T) {
	h := vpHandler(t)
	for name, query := range map[string]string{
		"no invite":        vpQuery(vpToken),
		"invite=0":         vpQuery(vpToken) + "&invite=0",
		"invite=1 twice":   vpQuery(vpToken) + "&invite=1&invite=1",
		"invite=1, then 0": vpQuery(vpToken) + "&invite=1&invite=0",
	} {
		t.Run(name, func(t *testing.T) {
			rec := vpDo(t, h, http.MethodGet, query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := vpFormAction(t, rec.Body.String()); got != "/auth/invitation/password" {
				t.Errorf("form action = %q, want /auth/invitation/password", got)
			}
		})
	}
	for name, extra := range map[string]string{"no invite": "", "invite=0": "&invite=0", "invite=1 twice": "&invite=1&invite=1", "invite=1, then 0": "&invite=1&invite=0", "invite empty": "&invite="} {
		t.Run(name+", stateless", func(t *testing.T) {
			vpBounced(t, vpDo(t, h, http.MethodGet, vpQueryBare(vpToken)+extra), bounceLocation)
		})
	}
	rec := vpDo(t, h, http.MethodGet, vpInviteQuery(vpToken))
	if rec.Code != http.StatusOK || vpFormAction(t, rec.Body.String()) != "/auth/invitation/password" {
		t.Errorf("control, one invite=1: status %d; want 200 serving the set-password page", rec.Code)
	}
}

func TestVerifyPage_InviteTokenIsEscaped(t *testing.T) {
	for _, raw := range []string{`<script>"{{.Alert}}`, "{{.Script}}", "{{.Token}}"} {
		t.Run(raw, func(t *testing.T) {
			rec := vpDo(t, vpHandler(t), http.MethodGet, vpInviteQuery(raw))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			if got := vpFormAction(t, body); got != "/auth/invitation/password" {
				t.Fatalf("form action = %q, want /auth/invitation/password", got)
			}
			doc := vpParse(t, body)
			var token string
			for _, in := range vpFind(doc, vpTag("input")) {
				if name, _ := vpAttr(in, "name"); name == "token" {
					token, _ = vpAttr(in, "value")
				}
			}
			if token != raw {
				t.Errorf("token input = %q, want the literal %q", token, raw)
			}
			if state := vpInputValue(doc, "state"); state != vpState {
				t.Errorf("state input = %q, want %q", state, vpState)
			}
			if strings.Contains(body, "<script>\"") {
				t.Errorf("the token is not HTML-escaped in the page: %q", body)
			}
			if n := len(vpFind(doc, vpTag("script"))); n != 1 {
				t.Errorf("script elements = %d, want 1", n)
			}
			if n := strings.Count(body, "addEventListener('submit'"); n != 1 {
				t.Errorf("the submit-once script appears %d times, want 1", n)
			}
		})
	}
}

// vpSetPasswordPage is the page the stated link must render; serveInvitationPasswordPage renders it today.
func vpSetPasswordPage() string { return renderPasswordPage(invitationPageHTML, vpToken, vpState, "") }

func TestVerifyPage_EveryStatedSignupLinkServesTheSetPasswordPage(t *testing.T) {
	h := vpHandler(t)
	for name, query := range map[string]string{"plain": vpQuery(vpToken), "invite=1": vpInviteQuery(vpToken)} {
		t.Run(name, func(t *testing.T) {
			rec := vpDo(t, h, http.MethodGet, query)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("status %d Content-Type %q, want 200 text/html; charset=utf-8", rec.Code, rec.Header().Get("Content-Type"))
			}
			body := rec.Body.String()
			if strings.Contains(body, "Confirm my email") {
				t.Error("the page still offers the confirm-only button")
			}
			if body != vpSetPasswordPage() {
				t.Errorf("body is not the set-password page the invitation link renders:\n%s", body)
			}
			doc := vpParse(t, body)
			forms := vpFind(doc, vpTag("form"))
			if len(forms) != 1 {
				t.Fatalf("forms = %d, want exactly 1", len(forms))
			}
			if got, _ := vpAttr(forms[0], "action"); got != "/auth/invitation/password" {
				t.Errorf("form action = %q, want /auth/invitation/password", got)
			}
			for field, want := range map[string]string{"token": vpToken, "type": "signup", "state": vpState} {
				if got := vpInputValue(doc, field); got != want {
					t.Errorf("hidden %s = %q, want %q", field, got, want)
				}
			}
			var pw *html.Node
			for _, in := range vpFind(forms[0], vpTag("input")) {
				if n, _ := vpAttr(in, "name"); n == "password" {
					pw = in
				}
			}
			if pw == nil {
				t.Fatal("no password input")
			}
			for k, want := range map[string]string{"type": "password", "minlength": "6", "maxlength": "72"} {
				if got, _ := vpAttr(pw, k); got != want {
					t.Errorf("password input %s = %q, want %q", k, got, want)
				}
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != resetCSP {
				t.Errorf("Content-Security-Policy = %q, want the set-password page's %q", got, resetCSP)
			}
			// [page-note]: one note for a registrant and an invitee.
			if !strings.Contains(body, "If you did not create an ASComply account or expect an invite, close this page.") {
				t.Error("the page note does not cover a registrant")
			}
		})
	}
}

func TestVerifyPage_HeadServesNoBody(t *testing.T) {
	rec := vpDo(t, vpHandler(t), http.MethodHead, vpQuery(vpToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %q, want empty", rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(vpSetPasswordPage())); got != want {
		t.Errorf("Content-Length = %q, want %q (the set-password page's length)", got, want)
	}
}

// vpLookup is a LinkAddress fake that records the tokens it is asked about.
type vpLookup struct {
	mu     sync.Mutex
	tokens []string
	answer func(ctx context.Context, token string) (string, bool, error)
}

func vpFound(masked string) *vpLookup {
	return &vpLookup{answer: func(context.Context, string) (string, bool, error) { return masked, true, nil }}
}

func vpMissing() *vpLookup {
	return &vpLookup{answer: func(context.Context, string) (string, bool, error) { return "", false, nil }}
}

func vpFails(err error) *vpLookup {
	return &vpLookup{answer: func(context.Context, string) (string, bool, error) { return "", false, err }}
}

func (l *vpLookup) fn() LinkAddress {
	return func(ctx context.Context, token string) (string, bool, error) {
		l.mu.Lock()
		l.tokens = append(l.tokens, token)
		l.mu.Unlock()
		return l.answer(ctx, token)
	}
}

func (l *vpLookup) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.tokens)
}

func vpHandlerWith(t *testing.T, address LinkAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	t.Helper()
	h, err := VerifyPageHandler(siteURL(t), address, perIP, enforce, log)
	if err != nil {
		t.Fatalf("VerifyPageHandler: %v", err)
	}
	return h
}

// vpLimit is the link-redeem throttle with a small limit, in-process.
func vpLimit(max int) *SignInThrottle {
	return NewSignInThrottle("link-redeem-ip", max, LinkRedeemMaxKeys, LinkRedeemIPWindow, time.Now)
}

// vpSpent reports whether the test client's count is used up (a throttle of limit 1 refuses the next Admit).
func vpSpent(t *testing.T, th *SignInThrottle) bool {
	t.Helper()
	held, err := th.Admit(gotrueAddress(httptest.NewRequest(http.MethodGet, vpPath, nil)))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return !held
}

const (
	vpMasked     = "o***@acme.co.za"
	vpTodayIntro = "Choose a password for your ASComply account. This also confirms your email address."
	vpFailedLoc  = siteURLValue + "/?verify=failed"
)

// vpAddressSpans returns the page's <span class="address"> elements.
func vpAddressSpans(t *testing.T, body string) []*html.Node {
	t.Helper()
	return vpFind(vpParse(t, body), func(n *html.Node) bool {
		c, _ := vpAttr(n, "class")
		return n.Data == "span" && c == "address"
	})
}

func vpRequireNamed(t *testing.T, rec *httptest.ResponseRecorder, masked string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d Location %q, want 200", rec.Code, rec.Header().Get("Location"))
	}
	spans := vpAddressSpans(t, rec.Body.String())
	if len(spans) != 1 || vpText(spans[0]) != masked {
		t.Fatalf("address spans = %d, want one holding %q", len(spans), masked)
	}
	intro := vpText(spans[0].Parent)
	if want := "Choose a password for the ASComply account of " + masked + ". This also confirms the address. Continue only if this is your address."; intro != want {
		t.Errorf("intro = %q, want %q", intro, want)
	}
	body := rec.Body.String()
	if i, j := strings.Index(body, `<span class="address">`), strings.Index(body, "<form"); i < 0 || j < 0 || i > j {
		t.Errorf("address span at %d, form at %d; want the span before the form", i, j)
	}
}

func TestVerifyPage_AFoundLinkNamesTheMaskedAddressBeforeTheForm(t *testing.T) {
	l := vpFound(vpMasked)

	rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpQuery(vpToken))

	vpRequireNamed(t, rec, vpMasked)
	if got := l.calls(); !slices.Equal(got, []string{vpToken}) {
		t.Errorf("lookup calls = %v, want [%s]", got, vpToken)
	}
}

func TestVerifyPage_AFoundInviteLinkNamesTheMaskedAddress(t *testing.T) {
	l := vpFound(vpMasked)

	rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpInviteQuery(vpToken))

	vpRequireNamed(t, rec, vpMasked)
}

func TestVerifyPage_HeadWithAFoundLinkAnswersLikeGet(t *testing.T) {
	h := vpHandlerWith(t, vpFound(vpMasked).fn(), vpLimit(5), true, nopLog())

	get := vpDo(t, h, http.MethodGet, vpQuery(vpToken))
	head := vpDo(t, h, http.MethodHead, vpQuery(vpToken))

	vpRequireNamed(t, get, vpMasked)
	if head.Code != get.Code || !headersEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD = %d %v, GET = %d %v; want the same status and headers", head.Code, head.Header(), get.Code, get.Header())
	}
	if head.Body.Len() != 0 {
		t.Errorf("HEAD body = %q, want empty", head.Body.String())
	}
}

func TestVerifyPage_TheLookupGetsTheFirstTokenOnly(t *testing.T) {
	l := vpFound(vpMasked)

	vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, "token=first&token=second&type=signup&state="+vpState)

	if got := l.calls(); !slices.Equal(got, []string{"first"}) {
		t.Errorf("lookup calls = %v, want [first]", got)
	}
}

func TestVerifyPage_TheAddressIsEscaped(t *testing.T) {
	const masked = `<b id="x">o</b>***@a.co`
	l := vpFound(masked)

	rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpQuery(vpToken))

	spans := vpAddressSpans(t, rec.Body.String())
	if len(spans) != 1 || vpText(spans[0]) != masked {
		t.Fatalf("address spans = %d, want one whose text is the literal %q", len(spans), masked)
	}
	if b := vpFind(vpParse(t, rec.Body.String()), func(n *html.Node) bool { id, _ := vpAttr(n, "id"); return n.Data == "b" || id == "x" }); len(b) != 0 {
		t.Errorf("the address injected %d elements", len(b))
	}
	if !strings.Contains(rec.Body.String(), html.EscapeString(masked)) {
		t.Errorf("the escaped address is not in the body")
	}
}

func TestVerifyPage_AnUnknownLinkRendersWithoutAnAddress(t *testing.T) {
	l := vpMissing()

	rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpQuery(vpToken))

	if got := l.calls(); !slices.Equal(got, []string{vpToken}) {
		t.Fatalf("lookup calls = %v, want [%s]: an unknown link is a lookup that found nothing", got, vpToken)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, vpTodayIntro) || strings.Contains(body, "***@") || strings.Contains(body, `class="address"`) {
		t.Errorf("answer = %d; want 200 with today's intro and no address", rec.Code)
	}
}

func TestVerifyPage_NoLookupRendersWithoutAnAddress(t *testing.T) {
	th := vpLimit(1)
	h := vpHandlerWith(t, nil, th, true, nopLog())

	rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken))

	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, vpTodayIntro) || strings.Contains(body, `class="address"`) {
		t.Errorf("answer = %d; want 200 with today's intro and no address", rec.Code)
	}
	if vpSpent(t, th) {
		t.Error("a nil lookup spent a link-redeem count")
	}
}

func TestPoolLinkAddress_NilPoolIsNoLookup(t *testing.T) {
	if PoolLinkAddress(nil) != nil {
		t.Error("PoolLinkAddress(nil) is not nil; the gateway without a pool would call through it")
	}
}

func TestVerifyPage_ALookupFailureIsTheFailedNotice(t *testing.T) {
	l := vpFails(errors.New("boom"))

	rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpQuery(vpToken))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != vpFailedLoc {
		t.Errorf("answer = %d Location %q, want 303 %s", rec.Code, rec.Header().Get("Location"), vpFailedLoc)
	}
	if strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("the failed notice still renders the form: %q", rec.Body.String())
	}
}

func TestVerifyPage_ALookupTimeoutIsTheFailedNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bounded bool
		l := &vpLookup{answer: func(ctx context.Context, _ string) (string, bool, error) {
			d, ok := ctx.Deadline()
			bounded = ok && time.Until(d) <= 5*time.Second
			<-ctx.Done()
			return "", false, ctx.Err()
		}}

		rec := vpDo(t, vpHandlerWith(t, l.fn(), vpLimit(5), true, nopLog()), http.MethodGet, vpQuery(vpToken))

		if len(l.calls()) != 1 || !bounded {
			t.Fatalf("lookup calls = %d, deadline of 5 s or less = %v; want one call under a deadline", len(l.calls()), bounded)
		}
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != vpFailedLoc {
			t.Errorf("answer = %d Location %q, want 303 %s", rec.Code, rec.Header().Get("Location"), vpFailedLoc)
		}
	})
}

func TestVerifyPage_ALookupFailureLogsNoTokenOrAddress(t *testing.T) {
	// The error text carries the token and an address: neither may reach the log.
	err := &pgconn.PgError{Code: "42883", Message: "function auth.confirm_link_address(text) " + vpToken + " o***@acme.co.za"}
	log, buf := captureLog()

	vpDo(t, vpHandlerWith(t, vpFails(err).fn(), vpLimit(5), true, log), http.MethodGet, vpQuery(vpToken))

	lines := recordsNamed(t, buf, "verify-page: link address lookup failed")
	if len(lines) != 1 || warnCount(t, buf) != 1 {
		t.Fatalf("%d lookup-failed lines and %d WARNs, want 1 and 1: %s", len(lines), warnCount(t, buf), buf.String())
	}
	if lines[0]["class"] != "42883" || lines[0]["timeout"] != false {
		t.Errorf("record = %v, want class 42883 and timeout false", lines[0])
	}
	if out := buf.String(); strings.Contains(out, vpToken) || strings.Contains(out, "@") {
		t.Errorf("the log holds the token or an address: %s", out)
	}
}

func TestVerifyPage_StatelessAndMalformedOpensMakeNoLookup(t *testing.T) {
	l := vpFound(vpMasked)
	th := vpLimit(1)
	h := vpHandlerWith(t, l.fn(), th, true, nopLog())
	bounce := siteURLValue + "/?confirm=1#token="
	rows := []struct{ name, query, want string }{
		{"stateless", vpQueryBare(vpToken), bounce + vpToken},
		{"malformed state", vpQueryBare(vpToken) + "&state=short", bounce + vpToken},
		{"bad type", "token=" + vpToken + "&type=recovery&state=" + vpState, vpFailedLoc},
		{"empty token", "token=&type=signup&state=" + vpState, vpFailedLoc},
		{"over-long token", vpQuery(strings.Repeat("a", vpMaxLen+1)), vpFailedLoc},
	}
	for _, c := range rows {
		rec := vpDo(t, h, http.MethodGet, c.query)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != c.want {
			t.Errorf("%s: answer = %d Location %q, want 303 %q", c.name, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
	if n := len(l.calls()); n != 0 {
		t.Errorf("lookups = %d, want 0", n)
	}

	// Control: a stated open of a good token looks up. The limit is 1, so a 200 shows the opens above spent nothing.
	if rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken)); rec.Code != http.StatusOK || len(l.calls()) != 1 {
		t.Errorf("control: answer = %d, lookups = %d; want 200 and 1", rec.Code, len(l.calls()))
	}
}

func TestVerifyPage_AMissedLookupSpendsTheLinkRedeemCount(t *testing.T) {
	l := vpMissing()
	h := vpHandlerWith(t, l.fn(), vpLimit(2), true, nopLog())

	var got []string
	for range 3 {
		rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken))
		got = append(got, strconv.Itoa(rec.Code)+" "+rec.Header().Get("Location"))
	}

	if want := []string{"200 ", "200 ", "303 " + vpFailedLoc}; !slices.Equal(got, want) {
		t.Errorf("answers = %q, want %q", got, want)
	}
	if n := len(l.calls()); n != 2 {
		t.Errorf("lookups = %d, want 2", n)
	}
}

func TestVerifyPage_AFoundLookupGivesTheCountBack(t *testing.T) {
	h := vpHandlerWith(t, vpFound(vpMasked).fn(), vpLimit(2), true, nopLog())

	for i := range 5 {
		vpRequireNamed(t, vpDo(t, h, http.MethodGet, vpQuery(vpToken)), vpMasked)
		if t.Failed() {
			t.Fatalf("GET #%d", i+1)
		}
	}
}

func TestVerifyPage_OverTheLimitMakesNoLookup(t *testing.T) {
	l := &vpLookup{answer: func(_ context.Context, token string) (string, bool, error) { return vpMasked, token == "good", nil }}
	log, buf := captureLog()
	h := vpHandlerWith(t, l.fn(), vpLimit(1), true, log)

	if rec := vpDo(t, h, http.MethodGet, vpQuery("miss")); rec.Code != http.StatusOK {
		t.Fatalf("first open = %d, want 200", rec.Code)
	}
	rec := vpDo(t, h, http.MethodGet, vpQuery("good"))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != vpFailedLoc {
		t.Errorf("over the limit: answer = %d Location %q, want 303 %s", rec.Code, rec.Header().Get("Location"), vpFailedLoc)
	}
	if got := l.calls(); !slices.Equal(got, []string{"miss"}) {
		t.Errorf("lookups = %v, want only [miss]", got)
	}
	requireLimitLine(t, buf, "verify-page: limit reached", "ip", true)
	if out := buf.String(); strings.Contains(out, "good") || strings.Contains(out, "miss") || strings.Contains(out, "@") {
		t.Errorf("the log holds a token or an address: %s", out)
	}
}

func TestVerifyPage_APreviewOverTheLimitStillLooksUp(t *testing.T) {
	l := vpMissing()
	log, buf := captureLog()
	h := vpHandlerWith(t, l.fn(), vpLimit(1), false, log)

	for i := range 2 {
		if rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken)); rec.Code != http.StatusOK {
			t.Errorf("open #%d = %d, want 200", i+1, rec.Code)
		}
	}

	if n := len(l.calls()); n != 2 {
		t.Errorf("lookups = %d, want 2", n)
	}
	requireLimitLine(t, buf, "verify-page: limit reached", "ip", false)
}

func TestVerifyPage_TheLookupAndThePostShareOneCount(t *testing.T) {
	th := vpLimit(2)
	l := vpMissing()
	page := vpHandlerWith(t, l.fn(), th, true, nopLog())
	signIn := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	post := InvitationPasswordHandler(newResetGoTrue(t).URL, siteURL(t), testClient(), liveSessions(t), signIn, nil, nopLog(), th, true, nil)

	if rec := vpDo(t, page, http.MethodGet, vpQuery(vpToken)); rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rec.Code)
	}
	v := rpValues(rpToken, "signup", rpPass)
	v.Set("state", vpState)
	if rec := ipPost(t, post, v); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), ipOK) {
		t.Fatalf("POST = %d Location %q, want a 303 to %s", rec.Code, rec.Header().Get("Location"), ipOK)
	}
	rec := vpDo(t, page, http.MethodGet, vpQuery(vpToken))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != vpFailedLoc {
		t.Errorf("3rd open = %d Location %q, want 303 %s: the GET and the POST count apart", rec.Code, rec.Header().Get("Location"), vpFailedLoc)
	}
}
