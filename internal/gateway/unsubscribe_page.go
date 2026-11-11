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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// unsubscribeContact must equal PRIVACY_CONTACT in the landing page; the privacy page's code-fact reader pins it.
const unsubscribeContact = "sam@ascomply.com"

const (
	unsubscribeLookupTimeout = 5 * time.Second
	// Above notifications' 40 s press budget: the lock wait, a Resend read of up to 21 calls, and the PATCH.
	unsubscribePressTimeout = 45 * time.Second
	maxUnsubscribeAnswer    = 4096
)

const (
	unsubscribeDoneCopy = "You are unsubscribed. We will send you no more marketing email. You will still get email about your account and your own requests, such as password resets, sign-in alerts and readiness check results. The blog newsletter has its own unsubscribe link."
	unsubscribeNoteCopy = "Unsubscribing stops marketing email only. You will still get email about your account and your own requests, such as password resets, sign-in alerts and readiness check results."
	unsubscribeFailCopy = "We could not unsubscribe you just now. Try again in a few minutes."
	unsubscribeBadCopy  = "This unsubscribe link does not work. Email " + unsubscribeContact + " and we will unsubscribe you."
)

var unsubscribeTokenRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// unsubscribeScript blocks a second submit and shows the press in progress; its hash goes into the CSP.
const unsubscribeScript = `var f = document.querySelector('form')
if (f) {
  var b = f.querySelector('button')
  f.addEventListener('submit', function (e) {
    if (this.dataset.sent) { e.preventDefault(); return }
    this.dataset.sent = '1'
    b.textContent = 'Unsubscribing…'
    b.disabled = true
  })
  // A back/forward-cache restore must not leave the button dead.
  addEventListener('pageshow', function () { delete f.dataset.sent; b.textContent = 'Unsubscribe'; b.disabled = false })
}`

// Filled by strings.NewReplacer, not html/template (see verifyPageHTML).
//
//go:embed unsubscribe_page.html
var unsubscribePageHTML string

// UnsubscribePageHandler serves the unsubscribe page: GET and HEAD look the token up and write nothing,
// POST (the button, or an RFC 8058 one-click) presses. Neither the token nor the address reaches a log line.
//
// ceiling: no throttle; a 122-bit random token cannot be enumerated.
// ceiling: the token in the query reaches load-balancer and Caddy logs; revisit if those logs leave our control.
func UnsubscribePageHandler(notifications *url.URL, client *http.Client, gatewayToken string, log *slog.Logger) http.Handler {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sum := sha256.Sum256([]byte(unsubscribeScript))
	csp := "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'"
	u := &unsubscribePage{base: notifications, client: &c, gatewayToken: gatewayToken, log: log, csp: csp}
	return http.HandlerFunc(u.serve)
}

type unsubscribePage struct {
	base         *url.URL
	client       *http.Client
	gatewayToken string
	log          *slog.Logger
	csp          string
}

func (u *unsubscribePage) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", u.csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		h.Set("Allow", "GET, HEAD, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token := r.URL.Query().Get("t")
	if !unsubscribeTokenRE.MatchString(token) {
		u.write(w, r, http.StatusNotFound, unsubscribeBad())
		return
	}
	if r.Method == http.MethodPost {
		u.press(w, r, token)
		return
	}
	u.open(w, r, token)
}

func (u *unsubscribePage) open(w http.ResponseWriter, r *http.Request, token string) {
	code, answer := u.post(r, "lookup", unsubscribeLookupTimeout, token)
	var in struct {
		Masked       string `json:"masked"`
		Unsubscribed bool   `json:"unsubscribed"`
	}
	switch {
	case code == http.StatusOK && json.Unmarshal(answer, &in) == nil && in.Masked != "":
		if in.Unsubscribed {
			u.write(w, r, http.StatusOK, unsubscribeDone())
			return
		}
		body := "<p>This stops marketing email to " + html.EscapeString(in.Masked) + ".</p>\n" + unsubscribeNote() + unsubscribeForm(token)
		u.write(w, r, http.StatusOK, body)
	case code == http.StatusNotFound || code == http.StatusBadRequest:
		u.write(w, r, http.StatusNotFound, unsubscribeBad())
	default:
		u.fail(w, r, "lookup", code, token)
	}
}

func (u *unsubscribePage) press(w http.ResponseWriter, r *http.Request, token string) {
	code, _ := u.post(r, "", unsubscribePressTimeout, token)
	switch code {
	case http.StatusOK:
		u.write(w, r, http.StatusOK, unsubscribeDone())
	case http.StatusNotFound, http.StatusBadRequest:
		u.write(w, r, http.StatusNotFound, unsubscribeBad())
	default:
		u.fail(w, r, "press", code, token)
	}
}

func (u *unsubscribePage) fail(w http.ResponseWriter, r *http.Request, step string, code int, token string) {
	u.log.WarnContext(r.Context(), "unsubscribe-page: upstream failed", slog.String("step", step), slog.Int("upstream_status", code))
	u.write(w, r, http.StatusServiceUnavailable, "<p>"+unsubscribeFailCopy+"</p>\n"+unsubscribeNote()+unsubscribeForm(token))
}

func (u *unsubscribePage) write(w http.ResponseWriter, r *http.Request, status int, content string) {
	page := strings.NewReplacer("{{.Content}}", content, "{{.Script}}", unsubscribeScript).Replace(unsubscribePageHTML)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, page)
	}
}

// post calls the notifications unsubscribe route (path "" is the press, "lookup" the read); status 0 is a transport error.
func (u *unsubscribePage) post(r *http.Request, path string, timeout time.Duration, token string) (int, []byte) {
	raw, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	segments := []string{"internal", "contacts", "unsubscribe"}
	if path != "" {
		segments = append(segments, path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.base.JoinPath(segments...).String(), bytes.NewReader(raw))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", u.gatewayToken)
	resp, err := u.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxUnsubscribeAnswer))
	return resp.StatusCode, answer
}

func unsubscribeDone() string { return "<p>" + unsubscribeDoneCopy + "</p>" }
func unsubscribeBad() string  { return "<p>" + unsubscribeBadCopy + "</p>\n" + unsubscribeNote() }
func unsubscribeNote() string { return `<p class="note">` + unsubscribeNoteCopy + "</p>\n" }

func unsubscribeForm(token string) string {
	return `<form method="post" action="/unsubscribe?t=` + html.EscapeString(url.QueryEscape(token)) + `">
    <button type="submit">Unsubscribe</button>
  </form>`
}
