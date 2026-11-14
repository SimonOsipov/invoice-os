package gateway

import (
	_ "embed"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Filled by strings.NewReplacer, not html/template: its reflection keeps every exported method
// in the binary, which TestProductionGatewayBinaryCannotMint rejects.
//
//go:embed invitation_password.html
var invitationPageHTML string

// serveSetPasswordPage answers GET /auth/verify with the set-password page naming the account's masked address, or bounces a stateless open to landing as ?confirm=<confirm>.
// It makes no GoTrue call; a stated open reads the database.
func serveSetPasswordPage(w http.ResponseWriter, r *http.Request, site, failed, confirm string, address LinkAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) {
	q := r.URL.Query()
	token := q.Get("token")
	if token == "" || len(token) > maxVerifyTokenBytes || q.Get("type") != "signup" {
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	state := q.Get("state")
	if len(q["state"]) != 1 || !stateShape.MatchString(state) {
		http.Redirect(w, r, site+"/?confirm="+confirm+"#token="+url.QueryEscape(token), http.StatusSeeOther)
		return
	}
	masked, ok := lookupLinkAddress(r, address, perIP, enforce, "verify-page", log, token)
	if !ok {
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	setResetPageHeaders(w.Header())
	writeResetPage(w, r.Method, http.StatusOK, renderNamedPasswordPage(invitationPageHTML, token, state, "", masked))
}

// InvitationPasswordHandler answers the set-password page's form POST: confirm the address, set the password, end every session; mints no session.
func InvitationPasswordHandler(authURL, siteURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, sink ContactSink, log *slog.Logger, perIP *SignInThrottle, enforce bool, address LinkAddress) http.Handler {
	if authURL == nil || siteURL == nil {
		return RegistrationNotConfigured()
	}
	site := strings.TrimSuffix(siteURL.String(), "/")
	return passwordLinkHandler(authURL, client, sessions, signIn, log, perIP, enforce, passwordLinkFlow{
		verifyType: "signup", label: "invitation-password", page: invitationPageHTML,
		done: site + "/?verified=1", failed: verifyFailedURL(siteURL), address: address,
		onConfirmed: func(r *http.Request, user gotrueUser) {
			handOffRegistrant(r.Context(), log, "invitation-password", sink, user.contact())
		},
	})
}
