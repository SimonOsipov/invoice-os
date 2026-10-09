package platform

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// HeaderGatewayToken carries the gateway's credential to a context service.
const HeaderGatewayToken = "X-Gateway-Token"

// HeaderGatewayGuard marks the gateway guard's own 401, so the gateway can tell a token mismatch from a service's auth refusal.
const HeaderGatewayGuard = "X-Gateway-Guard"

// GatewayGuardRefused is the value of HeaderGatewayGuard on a guard refusal.
const GatewayGuardRefused = "refused"

// RequireToken admits a request only when header equals token; others get 401 before the body is read.
// An empty token admits a request that sends no header; callers must pass a non-empty one.
func RequireToken(header, token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !tokenMatches(r.Header.Get(header), token) {
				writeUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireGateway refuses every request without token in X-Gateway-Token, except
// GET /healthz, GET /readyz and the open patterns. Panics on an empty token.
func (a *App) RequireGateway(token string, open ...string) {
	if token == "" {
		panic("platform: RequireGateway needs a non-empty token")
	}
	a.gatewayToken = token
	a.openPatterns = open
}

func tokenMatches(supplied, token string) bool {
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

// gatewayGuard is a no-op until RequireGateway sets a token.
func (a *App) gatewayGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.gatewayToken == "" || tokenMatches(r.Header.Get(HeaderGatewayToken), a.gatewayToken) {
			next.ServeHTTP(w, r)
			return
		}
		if a.isOpen(r) {
			for _, h := range []string{headerTenantID, headerUserID, headerUserRole, headerUserEmail, headerUserStaff, headerUserRulesRole} {
				r.Header.Del(h)
			}
			next.ServeHTTP(w, r)
			return
		}
		a.Logger.WarnContext(r.Context(), "request refused: no gateway token",
			slog.String("method", r.Method), slog.String("path", r.URL.Path))
		w.Header().Set(HeaderGatewayGuard, GatewayGuardRefused)
		writeUnauthorized(w)
	})
}

func (a *App) isOpen(r *http.Request) bool {
	_, pattern := a.Mux.Handler(r)
	switch pattern {
	case "GET /healthz", "GET /readyz":
		return true
	}
	for _, p := range a.openPatterns {
		if pattern == p {
			return true
		}
	}
	return false
}
