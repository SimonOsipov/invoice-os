package gateway

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	resetFormMaxBytes = 2 << 10
	resetPasswordMin  = 6
	resetPasswordMax  = 72
	resetPasswordHint = "Use a password of 6 to 72 characters."
)

// resetScript blocks a second submit; its hash goes into the CSP.
const resetScript = `var f = document.querySelector('form')
f.addEventListener('submit', function (e) { if (this.dataset.sent) e.preventDefault(); else this.dataset.sent = '1' })
addEventListener('pageshow', function () { delete f.dataset.sent })`

// Filled by strings.NewReplacer, not html/template: see verifyPageHTML.
//
//go:embed reset_password.html
var resetPageHTML string

var resetCSP = func() string {
	sum := sha256.Sum256([]byte(resetScript))
	return "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'"
}()

func renderResetPage(token, alert string) string {
	return renderPasswordPage(resetPageHTML, token, alert)
}

func renderPasswordPage(page, token, alert string) string {
	if alert != "" {
		alert = `<p class="alert" role="alert">` + html.EscapeString(alert) + `</p>`
	}
	return strings.NewReplacer("{{.Token}}", html.EscapeString(token), "{{.Alert}}", alert, "{{.Script}}", resetScript).Replace(page)
}

func setResetPageHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", resetCSP)
}

func resetFailedURL(siteURL *url.URL) string {
	return strings.TrimSuffix(siteURL.String(), "/") + "/?reset=failed"
}

// ResetPasswordPageHandler serves the set-new-password page; it holds no GoTrue client, so opening the link spends nothing.
func ResetPasswordPageHandler(siteURL *url.URL) http.Handler {
	if siteURL == nil {
		return RegistrationNotConfigured()
	}
	failed := resetFailedURL(siteURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setResetPageHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// redirect_to and every other query value are ignored, never rendered.
		q := r.URL.Query()
		token := q.Get("token")
		if token == "" || len(token) > maxVerifyTokenBytes || q.Get("type") != "recovery" {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		writeResetPage(w, r.Method, http.StatusOK, renderResetPage(token, ""))
	})
}

func writeResetPage(w http.ResponseWriter, method string, status int, page string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(status)
	if method != http.MethodHead {
		_, _ = io.WriteString(w, page)
	}
}

// ResetPasswordHandler answers the page's form POST: verify, set the password, end every session.
func ResetPasswordHandler(authURL, siteURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, log *slog.Logger) http.Handler {
	if authURL == nil || siteURL == nil {
		return RegistrationNotConfigured()
	}
	site := strings.TrimSuffix(siteURL.String(), "/")
	return passwordLinkHandler(authURL, client, sessions, signIn, log, passwordLinkFlow{
		verifyType: "recovery", label: "reset-password", page: resetPageHTML,
		done: site + "/?reset=1", failed: site + "/?reset=failed",
	})
}

// passwordLinkFlow is what differs between the reset and the invitee set-password POST.
type passwordLinkFlow struct {
	verifyType, label, page, done, failed string
	onConfirmed                           func(ctx context.Context, user gotrueUser)
}

// passwordLinkHandler verifies the link, sets the password with that session, ends every session and clears the sign-in failures.
func passwordLinkHandler(authURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, log *slog.Logger, flow passwordLinkFlow) http.Handler {
	verify := authURL.JoinPath("verify").String()
	user := authURL.JoinPath("user").String()
	logoutURL := authURL.JoinPath("logout")
	logoutURL.RawQuery = "scope=global"
	logout := logoutURL.String()
	failed, done, label := flow.failed, flow.done, flow.label

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, resetFormMaxBytes)
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		token, password := r.PostForm.Get("token"), r.PostForm.Get("password")
		if token == "" || len(token) > maxVerifyTokenBytes || r.PostForm.Get("type") != flow.verifyType {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		if len(password) < resetPasswordMin || len(password) > resetPasswordMax {
			setResetPageHeaders(w.Header())
			writeResetPage(w, r.Method, http.StatusBadRequest, renderPasswordPage(flow.page, token, resetPasswordHint))
			return
		}

		// A client that leaves must not strand a spent token half-way.
		ctx := context.WithoutCancel(r.Context())
		rr := r.WithContext(ctx)
		var confirmed struct {
			AccessToken string     `json:"access_token"`
			User        gotrueUser `json:"user"`
		}
		status, _, err := postGoTrue(rr, client, verify, map[string]string{"type": flow.verifyType, "token_hash": token}, &confirmed)
		switch {
		case err != nil:
			log.WarnContext(ctx, label+": gotrue unreachable", slog.String("error", err.Error()))
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		case status != http.StatusOK:
			log.WarnContext(ctx, label+": gotrue refused the link", slog.Int("upstream_status", status))
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		case confirmed.AccessToken == "" || confirmed.User.ID == "" || confirmed.User.Email == "":
			log.WarnContext(ctx, label+": gotrue verify answer incomplete")
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}

		status, gt, err := putGoTrueBearerJSON(ctx, client, user, confirmed.AccessToken, map[string]string{"password": password})
		samePassword := status == http.StatusUnprocessableEntity && gt.ErrorCode == "same_password"
		if err != nil || (status != http.StatusOK && !samePassword) {
			attrs := []any{slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode)}
			if err != nil {
				attrs = []any{slog.String("error", err.Error())}
			}
			log.WarnContext(ctx, label+": gotrue refused the password", attrs...)
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}

		status, gone, err := postGoTrueBearer(ctx, client, logout, confirmed.AccessToken)
		signedOut := err == nil && ((status >= 200 && status < 300) || gone)
		if !signedOut {
			attrs := []any{slog.Int("upstream_status", status)}
			if err != nil {
				attrs = []any{slog.String("error", err.Error())}
			}
			log.WarnContext(ctx, label+": global sign-out failed", attrs...)
		}
		sessions.EvictSubject(confirmed.User.ID)
		// After same_password GoTrue ended no session, so only a completed sign-out may claim success.
		if samePassword && !signedOut {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		signIn.Reset(confirmed.User.Email)
		if flow.onConfirmed != nil {
			flow.onConfirmed(ctx, confirmed.User)
		}
		http.Redirect(w, r, done, http.StatusSeeOther)
	})
}
