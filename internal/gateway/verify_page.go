package gateway

import (
	"net/http"
	"net/url"
	"strings"
)

// maxVerifyTokenBytes caps the token on the page and on the verifying POST.
const maxVerifyTokenBytes = 256

// VerifyPageHandler serves the set-password page for every signup link; it holds no GoTrue client, so opening the link spends nothing.
func VerifyPageHandler(siteURL *url.URL) (http.Handler, error) {
	if siteURL == nil {
		return RegistrationNotConfigured(), nil
	}
	site := strings.TrimSuffix(siteURL.String(), "/")
	failed := verifyFailedURL(siteURL)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setResetPageHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// The confirm=invite literal is parsed by frontend/landing/src/verifyLink.test.ts.
		confirm := "1"
		if inv := r.URL.Query()["invite"]; len(inv) == 1 && inv[0] == "1" {
			confirm = "invite"
		}
		serveSetPasswordPage(w, r, site, failed, confirm)
	}), nil
}
