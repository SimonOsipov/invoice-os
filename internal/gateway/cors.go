package gateway

import (
	"net/http"
	"strings"
)

// The browser round trip crosses origins: the app SPA (app-development-*.up.railway.app)
// and the gateway are separate origins, so a fetch to POST /auth/login (JSON body) or
// GET /api/tenancy/v1/me (Authorization header) is a non-simple request the browser
// gates behind a CORS preflight. These are the grant this gateway makes — the exact
// methods and request headers that round trip needs, nothing wider.
const (
	corsAllowMethods = "GET, POST, OPTIONS"
	corsAllowHeaders = "Authorization, Content-Type"
	corsMaxAge       = "600" // seconds a browser may cache a preflight before re-asking
)

// CORS returns middleware that grants the listed origins cross-origin access to the
// gateway. It is composed OUTSIDE the JWT Verifier.Middleware: a preflight OPTIONS
// carries no Authorization header, so it must be answered here (204) and never reach
// the verifier, which would 401 it. Behavior:
//
//   - Allowed Origin → Access-Control-Allow-Origin echoes it (+ Vary: Origin), so both
//     the preflight and the actual response carry the grant.
//   - Preflight (OPTIONS with an Origin) → answered 204 here and never forwarded, so it
//     bypasses auth regardless of whether the origin is allowed (a disallowed origin
//     simply gets no grant headers, and the browser blocks the follow-up).
//   - Disallowed / absent Origin → no grant. A request with no Origin (same-origin or a
//     server-to-server caller such as the Verifier fetching JWKS) passes through
//     untouched.
//
// No Access-Control-Allow-Credentials: the round trip authenticates with a bearer token,
// not cookies, so credentialed CORS (and its ban on a wildcard origin) never applies.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, originAllowed := allowed[origin]
			originAllowed = originAllowed && origin != ""

			if originAllowed {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin") // the grant depends on Origin; caches must key on it
			}

			// Preflight: an OPTIONS carrying an Origin. Answer it here so it never falls
			// through to the verifier. Only an allowed origin gets the methods/headers grant.
			if r.Method == http.MethodOptions && origin != "" {
				if originAllowed {
					h := w.Header()
					h.Set("Access-Control-Allow-Methods", corsAllowMethods)
					h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
					h.Set("Access-Control-Max-Age", corsMaxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
