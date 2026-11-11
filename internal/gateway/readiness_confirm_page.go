package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Above the worker's 25 s confirm budget.
const (
	readinessConfirmTimeout = 30 * time.Second
	maxConfirmAnswer        = 4096
)

const (
	confirmAskCopy     = "Press the button and we will email the results of your readiness check to this address."
	confirmDoneCopy    = "Done. Your email from no-reply@ascomply.com is on its way. Check your spam folder if you cannot find it."
	confirmExpiredCopy = `This link has expired or was already used. Run the check again at <a href="https://www.ascomply.com/check">www.ascomply.com/check</a>.`
	confirmRetryCopy   = "We could not send your results just now. Try again in a few minutes."
)

// confirmScript blocks a second submit and shows the press in progress; its hash goes into the CSP.
const confirmScript = `var f = document.querySelector('form')
if (f) {
  var b = f.querySelector('button')
  f.addEventListener('submit', function (e) {
    if (this.dataset.sent) { e.preventDefault(); return }
    this.dataset.sent = '1'
    b.textContent = 'Sending…'
    b.disabled = true
  })
  // A back/forward-cache restore must not leave the button dead.
  addEventListener('pageshow', function () { delete f.dataset.sent; b.textContent = 'Get my results'; b.disabled = false })
}`

// Filled by strings.NewReplacer, not html/template (see verifyPageHTML).
//
//go:embed readiness_confirm_page.html
var readinessConfirmPageHTML string

// ReadinessConfirmPageHandler serves the page behind the mailed link: GET and HEAD show the ask and call nothing
// upstream, POST (the button) presses. The POST body is never read; the worker gets the server's consent sentence.
// Neither the token nor the address reaches a log line.
//
// ceiling: no throttle; a 256-bit random token that lives 24 h cannot be enumerated.
// ceiling: the token in the query reaches load-balancer and Caddy logs; revisit if those logs leave our control or the token outlives 24 h.
func ReadinessConfirmPageHandler(worker *url.URL, client *http.Client, gatewayToken string, log *slog.Logger) http.Handler {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sum := sha256.Sum256([]byte(confirmScript))
	csp := "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'"
	p := &readinessConfirmPage{worker: worker, client: &c, gatewayToken: gatewayToken, log: log, csp: csp}
	return http.HandlerFunc(p.serve)
}

type readinessConfirmPage struct {
	worker       *url.URL
	client       *http.Client
	gatewayToken string
	log          *slog.Logger
	csp          string
}

func (p *readinessConfirmPage) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", p.csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		h.Set("Allow", "GET, HEAD, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token := r.URL.Query().Get("t")
	if !blogConfirmTokenRE.MatchString(token) {
		p.write(w, r, http.StatusNotFound, confirmExpired())
		return
	}
	if r.Method == http.MethodPost {
		p.press(w, r, token)
		return
	}
	body := "<p>" + confirmAskCopy + "</p>\n<p class=\"note\">Pressing it also confirms the box on the check page: “" +
		html.EscapeString(checkConsentText) + "”</p>\n" + confirmForm(token)
	p.write(w, r, http.StatusOK, body)
}

func (p *readinessConfirmPage) press(w http.ResponseWriter, r *http.Request, token string) {
	code, answer := p.confirm(r.Context(), token)
	var in struct {
		Outcome string `json:"outcome"`
	}
	switch {
	case code == http.StatusOK && json.Unmarshal(answer, &in) == nil && (in.Outcome == "sent" || in.Outcome == "used"):
		p.write(w, r, http.StatusOK, "<p>"+confirmDoneCopy+"</p>")
	case code == http.StatusGone:
		p.write(w, r, http.StatusGone, confirmExpired())
	default:
		p.log.WarnContext(r.Context(), "readiness-confirm: upstream failed", slog.Int("upstream_status", code))
		p.write(w, r, http.StatusServiceUnavailable, "<p>"+confirmRetryCopy+"</p>\n"+confirmForm(token))
	}
}

func (p *readinessConfirmPage) write(w http.ResponseWriter, r *http.Request, status int, content string) {
	page := strings.NewReplacer("{{.Content}}", content, "{{.Script}}", confirmScript).Replace(readinessConfirmPageHTML)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, page)
	}
}

// confirm calls the worker; the press outlives the visitor's connection. Status 0 is a transport error or timeout.
func (p *readinessConfirmPage) confirm(ctx context.Context, token string) (int, []byte) {
	raw, err := json.Marshal(map[string]string{"token": token, "consent_text": checkConsentText})
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readinessConfirmTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.worker.JoinPath("internal", "public-checks", "confirm").String(), bytes.NewReader(raw))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", p.gatewayToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxConfirmAnswer))
	return resp.StatusCode, answer
}

func confirmExpired() string { return "<p>" + confirmExpiredCopy + "</p>" }

func confirmForm(token string) string {
	return `<form method="post" action="/readiness-check/confirm?t=` + html.EscapeString(url.QueryEscape(token)) + `">
    <button type="submit">Get my results</button>
  </form>`
}
