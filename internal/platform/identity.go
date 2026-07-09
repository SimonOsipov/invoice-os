package platform

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Trusted identity headers the gateway injects on every proxied request after it
// verifies the caller's JWT. They mirror the gateway's outbound contract
// (internal/gateway: X-Tenant-ID / X-User-ID / X-User-Role). headerTenantID is
// already declared in middleware.go (same package) and reused here.
const (
	headerUserID   = "X-User-ID"
	headerUserRole = "X-User-Role"
)

// identityMiddleware reconstructs the caller Identity from the trusted headers the
// gateway sets after verifying the JWT, and places it in the context so tenant-scoped
// data access (db.WithinRequestTenantTx) and handlers can read the caller without
// re-verifying a token. A context service is reachable only through the gateway — the
// single authenticated ingress (the D1/D3 chokepoint) — so it TRUSTS these headers
// rather than validating a bearer token itself; the gateway overwrites any
// client-supplied copies from the verified token before forwarding.
//
// It is a no-op when the tenant header is absent (exactly like tenantIDMiddleware), so
// a service still boots and serves unscoped routes (/healthz) with no gateway in front.
// On the gateway's own binary this runs too, but the gateway's Verifier overwrites the
// identity from the verified token before any handler reads it, so a spoofed header can
// never take effect there.
func identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant := r.Header.Get(headerTenantID); tenant != "" {
			r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{
				Subject:  r.Header.Get(headerUserID),
				Role:     r.Header.Get(headerUserRole),
				TenantID: tenant,
			}))
		}
		next.ServeHTTP(w, r)
	})
}
