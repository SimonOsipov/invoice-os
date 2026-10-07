// Package gateway is the ASComply API edge: a thin reverse proxy that is the
// single authenticated ingress to the backend context services. For each request
// it verifies the caller's JWT (via the platform auth Verifier), authorizes the
// route, injects the verified tenant/user/role and request id as headers the
// services trust, and forwards the request to the owning service. It is the
// D1/D3 chokepoint — the only public backend surface. Deploying it over Railway
// private networking is M2-12; this package is the behavior, tested in-process.
package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
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
	headerUserEmail = "X-User-Email"
	headerRequestID = "X-Request-ID"
	// headerS2SToken is 04's service-to-service peer credential
	// (internal/validation/s2s.go). The gateway never mints it and never
	// forwards a client-supplied one -- see injectIdentity.
	headerS2SToken = "X-S2S-Token"
)

// routePrefix is the public path space the gateway proxies. Everything under it
// is authenticated; /healthz, the mock issuer routes, etc. live outside it.
const routePrefix = "/api/"

// Options configures the gateway handler.
type Options struct {
	Verifier  *auth.Verifier      // verifies bearer tokens; required
	Sessions  *SessionChecker     // refuses revoked sessions; required
	Upstreams map[string]*url.URL // service name -> base URL; required
	Logger    *slog.Logger        // defaults to slog.Default()

	GatewayToken string // sent to every upstream as X-Gateway-Token; required
}

// Handler returns the handler to mount at "/api/". Request flow: verify (401) ->
// session check (401 revoked, 503 unavailable) -> route by path prefix (404 on
// unknown) -> authorize (403) -> inject identity -> reverse-proxy to the owning
// service (502 if unreachable). Auth runs before routing, so an unauthenticated
// caller gets a uniform 401 and never learns which service prefixes exist.
func Handler(opts Options) http.Handler {
	if opts.Sessions == nil {
		panic("gateway: Options.Sessions is required")
	}
	if opts.GatewayToken == "" {
		panic("gateway: Options.GatewayToken is required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	proxies := make(map[string]http.Handler, len(opts.Upstreams))
	for svc, target := range opts.Upstreams {
		proxies[svc] = http.StripPrefix(routePrefix+svc, newReverseProxy(svc, target, opts.GatewayToken, log))
	}
	return opts.Verifier.Middleware(opts.Sessions.Middleware(&router{proxies: proxies, log: log}))
}

// router resolves the owning service from the first path segment under /api/,
// authorizes, and delegates to that service's proxy. It runs only for requests
// the Verifier has already authenticated.
type router struct {
	proxies map[string]http.Handler
	log     *slog.Logger
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, routePrefix), "/")
	proxy, ok := rt.proxies[service]
	// Refuse "internal" as the raw or the cleaned first segment: CONNECT is not cleaned by the mux, and the proxy forwards the raw path.
	raw, _, _ := strings.Cut(rest, "/")
	if cleaned, _, _ := strings.Cut(strings.TrimPrefix(path.Clean("/"+rest), "/"), "/"); !ok || raw == "internal" || cleaned == "internal" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, _ := auth.IdentityFromContext(r.Context())
	if status := authorize(r, service, id); status != 0 {
		rt.log.WarnContext(r.Context(), "gateway authz denied",
			slog.String("upstream", service), slog.Int("status", status))
		writeError(w, status, strings.ToLower(http.StatusText(status)))
		return
	}
	proxy.ServeHTTP(w, r)
}

// A token with no tenant may reach these two POST routes only: provisioning creates a tenant, accept joins one.
const (
	tenantlessPath       = "/api/tenancy/v1/workspaces"
	tenantlessAcceptPath = "/api/tenancy/v1/invitations/accept"
)

// authorize returns 0 when the identity may use the service, otherwise the HTTP
// status to answer. P1 rule: every context service is tenant-scoped, so a valid
// token carrying no tenant is forbidden, except POST /api/tenancy/v1/workspaces and
// POST /api/tenancy/v1/invitations/accept.
// The M7 ops console adds its operator-role rule here, keyed on service.
func authorize(r *http.Request, service string, id auth.Identity) int {
	if id.TenantID == "" && !isTenantlessRoute(r) {
		return http.StatusForbidden
	}
	return 0
}

// isTenantlessRoute matches the escaped path, so an encoded variant (v1%2Fworkspaces) is not exempt.
func isTenantlessRoute(r *http.Request) bool {
	p := r.URL.EscapedPath()
	return r.Method == http.MethodPost && (p == tenantlessPath || p == tenantlessAcceptPath)
}

var errGuardRefused = errors.New("upstream refused the gateway token")

// newReverseProxy builds the per-service reverse proxy. The path prefix is
// stripped by the caller (http.StripPrefix); here we point the request at the
// upstream and overwrite the identity headers from the verified token.
func newReverseProxy(service string, target *url.URL, gatewayToken string, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: platform.TraceTransport(nil),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			injectIdentity(pr, gatewayToken)
			// The outbound round tripper sets its own sentry-trace; inbound ones are not trusted.
			pr.Out.Header.Del("sentry-trace")
			pr.Out.Header.Del("baggage")
		},
		ModifyResponse: func(resp *http.Response) error {
			// A guard refusal is a gateway/service token mismatch, not the user's: answer 502, never 401.
			if resp.Header.Get(platform.HeaderGatewayGuard) == platform.GatewayGuardRefused {
				return errGuardRefused
			}
			// Every upstream is a platform service that reports its own 5xx.
			platform.ReportedElsewhere(resp.Request.Context())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, errGuardRefused) {
				log.ErrorContext(r.Context(), "gateway token refused by upstream",
					slog.String("upstream", service))
				writeError(w, http.StatusBadGateway, "bad gateway")
				return
			}
			log.ErrorContext(r.Context(), "gateway upstream unreachable",
				slog.String("upstream", service), slog.Any("err", err))
			writeError(w, http.StatusBadGateway, "bad gateway")
		},
	}
}

// injectIdentity takes the identity headers from the verified token, never from
// the client. X-S2S-Token is deleted and X-Gateway-Token is set to the gateway's
// own credential.
func injectIdentity(pr *httputil.ProxyRequest, gatewayToken string) {
	id, _ := auth.IdentityFromContext(pr.In.Context())
	pr.Out.Header.Set(headerTenantID, id.TenantID)
	pr.Out.Header.Set(headerUserID, id.Subject)
	pr.Out.Header.Set(headerUserRole, id.Role)
	pr.Out.Header.Set(headerUserEmail, id.Email)
	pr.Out.Header.Del(headerS2SToken)
	pr.Out.Header.Set(platform.HeaderGatewayToken, gatewayToken)
	if rid := platform.RequestIDFromContext(pr.In.Context()); rid != "" {
		pr.Out.Header.Set(headerRequestID, rid)
	} else {
		pr.Out.Header.Del(headerRequestID)
	}
}

// MockIssuerEnabled reports whether the mock issuer should be wired in: every
// non-production environment. It is refused in production regardless of the flag,
// and environment is trimmed and lowercased first (the same normalization
// submission.IsProduction applies), so "Production" or " production" cannot defeat
// it. The production gateway is built without -tags mockissuer (AUTH-01), so this
// gate only filters builds that opted in.
func MockIssuerEnabled(environment, flag string) bool {
	return flag == "true" && strings.ToLower(strings.TrimSpace(environment)) != "production"
}

// MockLoginHandler mints a GoTrue-shaped token for the requested identity. It is
// the mock stand-in for GoTrue's login; main wires it only when the mock issuer
// is enabled (see MockIssuerEnabled). It mints for any identity, an empty body
// included, wherever it is wired.
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
