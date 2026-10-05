package gateway

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxVerifyTokenBytes caps the token on the page and on the verifying POST.
const maxVerifyTokenBytes = 256

// verifyScript blocks a second submit; its hash goes into the CSP.
const verifyScript = `// A second submit would show the spent token's failure to a verified registrant.
document.querySelector('form').addEventListener('submit', function (e) { if (this.dataset.sent) e.preventDefault(); else this.dataset.sent = '1' })`

//go:embed verify_page.html
var verifyPageHTML string

// VerifyPageHandler serves the confirm page; it holds no GoTrue client, so opening the link spends nothing.
func VerifyPageHandler(siteURL *url.URL) (http.Handler, error) {
	if siteURL == nil {
		return RegistrationNotConfigured(), nil
	}
	tmpl, err := template.New("verify_page").Parse(verifyPageHTML)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifyScript))
	csp := "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'"
	failed := strings.TrimSuffix(siteURL.String(), "/") + "/?verify=failed"

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
		var page bytes.Buffer
		if err := tmpl.Execute(&page, struct {
			Token  string
			Script template.JS
		}{token, template.JS(verifyScript)}); err != nil {
			writeError(w, http.StatusInternalServerError, "page unavailable")
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(page.Len()))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(page.Bytes())
		}
	}), nil
}
