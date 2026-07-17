// This file (s2s.go) is the M4-04-03 service-to-service peer auth for 04's
// batch validate surface: S2SMiddleware proves the caller is a fleet peer
// (03 submission) and nothing more ([s2s-peer-auth]).
//
// It is defense-in-depth ON TOP OF private networking (docs/add-a-service.md
// §4), which stays the primary control -- this makes the endpoint refuse a
// non-peer even if that control is ever misconfigured (one Railway "generate
// domain" misclick). A shared secret is proportionate precisely BECAUSE the
// endpoint behind it guards no tenant data: rule evaluation is a pure
// function of (payload, active global rule-set), identical for every tenant.
//
// It deliberately establishes NO identity. The middleware neither reads
// X-Tenant-ID nor calls auth.IdentityFromContext, and passes nothing
// downstream but the request itself -- so a peer's tenant assertion cannot be
// trusted here, because there is no mechanism by which it could be heard
// ([s2s-identity]: answered by construction, not by policy). The precedent
// this sets for 05/06 ([s2s-precedent]): a peer endpoint is either (a)
// tenant-free and peer-authenticated, like this one, or (b) independently
// verifies the caller's real token. A peer's tenant assertion is never
// trusted.
package validation

import (
	"crypto/subtle"
	"net/http"
)

// headerS2SToken carries the shared peer secret. The gateway STRIPS this
// header from every proxied request (internal/gateway/gateway.go's
// injectIdentity), so it can only ever arrive from inside the private
// network -- a client cannot smuggle a leaked token in through the one public
// backend surface ([s2s-gateway-strip]).
const headerS2SToken = "X-S2S-Token"

// S2SMiddleware returns middleware that admits only callers presenting the
// configured peer token in X-S2S-Token, comparing with
// crypto/subtle.ConstantTimeCompare so a wrong token leaks no timing signal.
//
// The token check runs BEFORE the request body is touched: an unauthenticated
// caller gets a 401 without 04 ever reading, buffering, or size-checking their
// payload ([s2s-peer-auth]). This ordering is load-bearing and mirrors the
// importer's identity-first-401-then-size-cap order -- oversized-AND-
// unauthenticated is a 401, never a 413. It is also why the batch handler's
// http.MaxBytesReader is armed downstream of this middleware, not upstream.
//
// token MUST be non-empty: ConstantTimeCompare("", "") returns 1, so an empty
// configured token would admit every caller presenting no token at all. The
// caller guarantees this -- cmd/validation/main.go sources it via
// mustEnv("S2S_TOKEN"), which log.Fatalf's at boot on an unset var rather than
// starting an open endpoint.
func S2SMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			supplied := r.Header.Get(headerS2SToken)
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
