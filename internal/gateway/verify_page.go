package gateway

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxVerifyTokenBytes caps the token on the page and on the verifying POST.
const maxVerifyTokenBytes = 256

// verifyScript blocks a second submit; its hash goes into the CSP.
const verifyScript = `// A second submit would show the spent token's failure to a verified registrant.
var f = document.querySelector('form')
f.addEventListener('submit', function (e) { if (this.dataset.sent) e.preventDefault(); else this.dataset.sent = '1' })
// A back/forward-cache restore or an aborted submit must not leave the button dead.
addEventListener('pageshow', function () { delete f.dataset.sent })`

// Filled by strings.NewReplacer, not html/template: its reflection keeps every exported method
// in the binary, which TestProductionGatewayBinaryCannotMint rejects.
//
//go:embed verify_page.html
var verifyPageHTML string

// VerifyPageHandler serves the confirm page; it holds no GoTrue client, so opening the link spends nothing.
func VerifyPageHandler(siteURL *url.URL) (http.Handler, error) {
	if siteURL == nil {
		return RegistrationNotConfigured(), nil
	}
	sum := sha256.Sum256([]byte(verifyScript))
	csp := "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'"
	site := strings.TrimSuffix(siteURL.String(), "/")
	failed := site + "/?verify=failed"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// redirect_to and every other query value are ignored, never rendered.
		q := r.URL.Query()
		token := q.Get("token")
		if token == "" || len(token) > maxVerifyTokenBytes || q.Get("type") != "signup" {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		// The app mints the state in the clicking tab; a stateless open goes there via landing.
		state := q.Get("state")
		if !stateShape.MatchString(state) {
			http.Redirect(w, r, site+"/?confirm=1#token="+url.QueryEscape(token), http.StatusSeeOther)
			return
		}
		page := strings.NewReplacer("{{.Token}}", html.EscapeString(token), "{{.State}}", html.EscapeString(state), "{{.Script}}", verifyScript).Replace(verifyPageHTML)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(len(page)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, page)
		}
	}), nil
}
