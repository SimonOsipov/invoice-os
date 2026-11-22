package erpweb

import (
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

const fakeConsentPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Fake ERP consent</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem}button,select{font:inherit;margin:.25rem 0}</style>
</head><body><main>
<h1>Fake ERP consent</h1>
<p>Pick the company the connection should see. The suffixes make the access token expire at once, or the first refresh fail.</p>
<form method="get" action="/erp/callback">
<input type="hidden" name="state" value="{{.State}}">
<select name="code">{{.Options}}</select>
<div><button type="submit">Allow</button> <button type="submit" name="error" value="access_denied">Deny</button></div>
</form>
</main></body></html>
`

// The fake vendor's companies and code suffixes; erp's fake connector and its seed use the same names.
var FakeCompanies = []string{"fake-co-1", "fake-co-2", "fake-co-3"}

const (
	FakeExpired      = "expired"
	FakeRefreshFails = "refresh_fails"
)

// FakeCodes are the nine consent codes: each company, plain and with each suffix, sorted.
func FakeCodes() []string {
	var out []string
	for _, key := range FakeCompanies {
		out = append(out, key, key+"~"+FakeExpired, key+"~"+FakeRefreshFails)
	}
	slices.Sort(out)
	return out
}

const fakeConsentCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// FakeConsentPage serves the fake vendor's consent form, one option per code. Rendered with strings.NewReplacer and
// html.EscapeString, as the gateway's other public pages are, not html/template.
func FakeConsentPage(codes []string) http.Handler {
	var opts strings.Builder
	for _, c := range codes {
		e := html.EscapeString(c)
		opts.WriteString("<option value=\"" + e + "\">" + e + "</option>")
	}
	options := opts.String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", fakeConsentCSP)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state := r.URL.Query().Get("state")
		if _, _, ok := ParseState(state); !ok {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		page := strings.NewReplacer("{{.State}}", html.EscapeString(state), "{{.Options}}", options).Replace(fakeConsentPage)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(len(page)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(page))
	})
}
