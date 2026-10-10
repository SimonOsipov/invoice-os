package gateway

import (
	"context"
	_ "embed"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Filled by strings.NewReplacer, not html/template: see verifyPageHTML.
//
//go:embed invitation_password.html
var invitationPageHTML string

// serveInvitationPasswordPage answers GET /auth/verify?invite=1 with the set-password page, or bounces a stateless open to landing; it makes no GoTrue call.
func serveInvitationPasswordPage(w http.ResponseWriter, r *http.Request, site, failed string) {
	q := r.URL.Query()
	token := q.Get("token")
	if token == "" || len(token) > maxVerifyTokenBytes || q.Get("type") != "signup" {
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	state := q.Get("state")
	if len(q["state"]) != 1 || !stateShape.MatchString(state) {
		http.Redirect(w, r, site+"/?confirm=invite#token="+url.QueryEscape(token), http.StatusSeeOther)
		return
	}
	setResetPageHeaders(w.Header())
	writeResetPage(w, r.Method, http.StatusOK, renderPasswordPage(invitationPageHTML, token, state, ""))
}

// InvitationPasswordHandler answers the invitee page's form POST: confirm the address, set the password, end every session.
func InvitationPasswordHandler(authURL, siteURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, sink ContactSink, log *slog.Logger) http.Handler {
	if authURL == nil || siteURL == nil {
		return RegistrationNotConfigured()
	}
	site := strings.TrimSuffix(siteURL.String(), "/")
	return passwordLinkHandler(authURL, client, sessions, signIn, log, passwordLinkFlow{
		verifyType: "signup", label: "invitation-password", page: invitationPageHTML,
		done: site + "/?verified=1", failed: verifyFailedURL(siteURL),
		onConfirmed: func(ctx context.Context, user gotrueUser) {
			handOffRegistrant(ctx, log, "invitation-password", sink, user.contact())
		},
	})
}
