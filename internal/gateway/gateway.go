// Package gateway is the FiscalBridge API edge: a thin reverse proxy that is the
// single authenticated ingress to the backend context services. For each request
// it verifies the caller's JWT (via the platform auth Verifier), authorizes the
// route, injects the verified tenant/user/role and request id as headers the
// services trust, and forwards the request to the owning service. It is the
// D1/D3 chokepoint — the only public backend surface. Deploying it over Railway
// private networking is M2-12; this package is the behavior, tested in-process.
package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Downstream identity headers. The gateway sets these from the verified token and
// overwrites any client-supplied copies, so a service can trust them without
// re-verifying the JWT. They match the platform kit's inbound contract
// (X-Tenant-ID / X-Request-ID) that services already read.
const (
	headerTenantID  = "X-Tenant-ID"
	headerUserID    = "X-User-ID"
	headerUserRole  = "X-User-Role"
	headerRequestID = "X-Request-ID"
)

// routePrefix is the public path space the gateway proxies. Everything under it
// is authenticated; /healthz, the mock issuer routes, etc. live outside it.
const routePrefix = "/api/"

// Options configures the gateway handler.
type Options struct {
	Verifier  *auth.Verifier      // verifies bearer tokens; required
	Upstreams map[string]*url.URL // service name -> base URL; required
	Logger    *slog.Logger        // defaults to slog.Default()
}

// Handler returns the handler to mount at "/api/". Request flow: verify (401) ->
// authorize (403) -> route by path prefix (404 on unknown) -> inject identity ->
// reverse-proxy to the owning service (502 if unreachable). Auth runs before
// routing, so an unauthenticated caller gets a uniform 401 and never learns which
// service prefixes exist.
func Handler(opts Options) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	proxies := make(map[string]http.Handler, len(opts.Upstreams))
	for svc, target := range opts.Upstreams {
		proxies[svc] = http.StripPrefix(routePrefix+svc, newReverseProxy(svc, target, log))
	}
	return opts.Verifier.Middleware(&router{proxies: proxies, log: log})
}

// router resolves the owning service from the first path segment under /api/,
// authorizes, and delegates to that service's proxy. It runs only for requests
// the Verifier has already authenticated.
type router struct {
	proxies map[string]http.Handler
	log     *slog.Logger
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, routePrefix), "/")
	proxy, ok := rt.proxies[service]
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, _ := auth.IdentityFromContext(r.Context())
	if status := authorize(service, id); status != 0 {
		rt.log.WarnContext(r.Context(), "gateway authz denied",
			slog.String("service", service), slog.Int("status", status))
		writeError(w, status, strings.ToLower(http.StatusText(status)))
		return
	}
	proxy.ServeHTTP(w, r)
}

// authorize returns 0 when the identity may use the service, otherwise the HTTP
// status to answer. P1 rule: every context service is tenant-scoped, so a valid
// token carrying no tenant is forbidden. The M7 ops console adds its operator-role
// rule here, keyed on service.
func authorize(service string, id auth.Identity) int {
	if id.TenantID == "" {
		return http.StatusForbidden
	}
	return 0
}

// newReverseProxy builds the per-service reverse proxy. The path prefix is
// stripped by the caller (http.StripPrefix); here we point the request at the
// upstream and overwrite the identity headers from the verified token.
func newReverseProxy(service string, target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			injectIdentity(pr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.ErrorContext(r.Context(), "gateway upstream unreachable",
				slog.String("service", service), slog.Any("err", err))
			writeError(w, http.StatusBadGateway, "bad gateway")
		},
	}
}

// injectIdentity overwrites the trusted identity headers on the outbound request
// from the verified token, discarding any client-supplied X-Tenant-ID / X-User-*
// (Header.Set replaces every prior value). The request id comes from the platform
// kit's requestIDMiddleware, which always sets one upstream of this handler.
func injectIdentity(pr *httputil.ProxyRequest) {
	id, _ := auth.IdentityFromContext(pr.In.Context())
	pr.Out.Header.Set(headerTenantID, id.TenantID)
	pr.Out.Header.Set(headerUserID, id.Subject)
	pr.Out.Header.Set(headerUserRole, id.Role)
	if rid := platform.RequestIDFromContext(pr.In.Context()); rid != "" {
		pr.Out.Header.Set(headerRequestID, rid)
	} else {
		pr.Out.Header.Del(headerRequestID)
	}
}

// MockIssuerEnabled reports whether the dev/CI mock issuer should be wired in. It
// is refused in production regardless of the flag, so a misconfigured prod env
// can never expose the token-mint / JWKS endpoints — defense in depth ahead of
// the M8-07 production refusal.
func MockIssuerEnabled(environment, flag string) bool {
	return flag == "true" && environment != "production"
}

// MockLoginHandler mints a GoTrue-shaped token for the requested identity. It is
// the dev/CI stand-in for GoTrue's login; main wires it only when the mock issuer
// is enabled (see MockIssuerEnabled).
func MockLoginHandler(issuer *auth.MockIssuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Subject  string `json:"subject"`
			Role     string `json:"role"`
			TenantID string `json:"tenant_id"`
		}
		// The body is optional; on any decode error the zero value yields
		// GoTrue-shaped defaults (random subject, "authenticated" role).
		_ = json.NewDecoder(r.Body).Decode(&req)
		token, err := issuer.Mint(auth.MintOptions{
			Subject:  req.Subject,
			Role:     req.Role,
			TenantID: req.TenantID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not mint token")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"access_token": token,
			"token_type":   "bearer",
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
