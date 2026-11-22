package gateway

import (
	"bytes"
	"context"
	_ "embed"
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

const (
	erpCallbackTimeout   = 30 * time.Second // above the worker's 20 s callback budget
	erpMaxCode           = 2048
	maxERPCallbackAnswer = 4096
)

var erpStateRE = regexp.MustCompile(`^[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$`)

const erpCallbackCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type erpOutcomePage struct {
	status      int
	title, copy string
}

// Each page is fixed text: nothing the visitor or the vendor sent reaches the HTML.
var erpOutcomePages = map[string]erpOutcomePage{
	"connected": {http.StatusOK, "Connected", "The ERP connection is active. You can close this tab and return to ascomply."},
	"invalid":   {http.StatusBadRequest, "This link is not valid", "It has expired or was already used. Start the connection again in ascomply."},
	"denied":    {http.StatusBadRequest, "Access was not granted", "The ERP did not give ascomply access. Start again in ascomply if this was a mistake."},
	"conflict":  {http.StatusBadRequest, "Already connected", "This ERP company is already connected to this client, or the connection is for another company. Nothing was changed."},
	"ended":     {http.StatusBadRequest, "Connection cancelled", "The connection changed or you no longer have access, so nothing was connected. Start again in ascomply."},
	"failed":    {http.StatusBadGateway, "Connection failed", "We could not finish the connection. Start again in ascomply."},
	"unknown":   {http.StatusBadGateway, "Result not confirmed", "We could not confirm the result. Check the connection in ascomply."},
}

// Filled by strings.NewReplacer, not html/template (see invitationPageHTML).
//
//go:embed erp_callback_page.html
var erpCallbackPageHTML string

// ERPCallbackPageHandler serves the page the vendor redirects the browser to: it checks the shape of
// state and code, forwards state, code and error to the worker, and renders its outcome. Nothing the
// request carried reaches a log line or the page.
//
// ceiling: no throttle; a random 256-bit state cannot be enumerated (as /readiness-check/confirm).
func ERPCallbackPageHandler(worker *url.URL, client *http.Client, gatewayToken string, log *slog.Logger) http.Handler {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	p := &erpCallbackPage{worker: worker, client: &c, gatewayToken: gatewayToken, log: log}
	return http.HandlerFunc(p.serve)
}

type erpCallbackPage struct {
	worker       *url.URL
	client       *http.Client
	gatewayToken string
	log          *slog.Logger
}

func (p *erpCallbackPage) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", erpCallbackCSP)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	state, code, vendorErr := q.Get("state"), q.Get("code"), q.Get("error")
	if !erpStateRE.MatchString(state) || len(code) > erpMaxCode {
		p.write(w, r, "invalid")
		return
	}
	p.write(w, r, p.forward(r.Context(), state, code, vendorErr))
}

// forward returns the worker's outcome. A worker 500 means tx2 did not commit (failed); a timeout or any
// other answer may follow a commit, so the result is unknown.
func (p *erpCallbackPage) forward(ctx context.Context, state, code, vendorErr string) string {
	raw, err := json.Marshal(map[string]string{"state": state, "code": code, "error": vendorErr})
	if err != nil {
		return "unknown"
	}
	// The call outlives the visitor's connection: the state is spent on arrival.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), erpCallbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.worker.JoinPath("internal", "erp", "callback").String(), bytes.NewReader(raw))
	if err != nil {
		return "unknown"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", p.gatewayToken)
	resp, err := p.client.Do(req)
	if err != nil {
		p.log.WarnContext(ctx, "erp-callback: worker call failed")
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		p.log.WarnContext(ctx, "erp-callback: worker answered 500")
		return "failed"
	}
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxERPCallbackAnswer))
	var in struct {
		Outcome string `json:"outcome"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(answer, &in) != nil {
		p.log.WarnContext(ctx, "erp-callback: unusable worker answer", slog.Int("upstream_status", resp.StatusCode))
		return "unknown"
	}
	if _, ok := erpOutcomePages[in.Outcome]; !ok || in.Outcome == "unknown" {
		return "unknown"
	}
	return in.Outcome
}

func (p *erpCallbackPage) write(w http.ResponseWriter, r *http.Request, outcome string) {
	o := erpOutcomePages[outcome]
	page := strings.NewReplacer(
		"{{.Title}}", html.EscapeString(o.title),
		"{{.Outcome}}", outcome,
		"{{.Copy}}", html.EscapeString(o.copy),
	).Replace(erpCallbackPageHTML)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(o.status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, page)
	}
}
