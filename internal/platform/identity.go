package platform

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Trusted identity headers the gateway injects on every proxied request after it
// verifies the caller's JWT. They mirror the gateway's outbound contract
// (internal/gateway). headerTenantID is
// already declared in middleware.go (same package) and reused here.
const (
	headerUserID    = "X-User-ID"
	headerUserRole  = "X-User-Role"
	headerUserEmail = "X-User-Email"

	headerUserStaff     = "X-User-Staff"
	headerUserRulesRole = "X-User-Rules-Role"
)

// identityMiddleware reconstructs the caller Identity from the trusted headers the
// gateway sets after verifying the JWT, and places it in the context so tenant-scoped
// data access (db.WithinRequestTenantTx) and handlers can read the caller without
// re-verifying a token. A context service TRUSTS these headers
// rather than validating a bearer token itself; the gateway overwrites any
// client-supplied copies from the verified token before forwarding. Each context main
// refuses a request without the gateway token (RequireGateway;
// TestRLS_EveryContextServiceRefusesAForgedRequest).
//
// A user header that is empty or not a uuid builds no identity and no tenant-less caller;
// uuid.Parse is the verifier's own check.
//
// With no tenant header but a user header it stores a tenant-less caller on its own key,
// so IdentityFromContext still reports none. With neither it is a no-op, so a service
// still boots and serves unscoped routes (/healthz) with no gateway in front.
// On the gateway's own binary this runs too, but the gateway's Verifier overwrites the
// identity from the verified token before any handler reads it, so a spoofed header can
// never take effect there.
func identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(headerUserID)
		if _, err := uuid.Parse(user); err != nil {
			next.ServeHTTP(w, r)
			return
		}
		if tenant := r.Header.Get(headerTenantID); tenant != "" {
			r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{
				Subject:  user,
				Role:     r.Header.Get(headerUserRole),
				TenantID: tenant,
				Email:    r.Header.Get(headerUserEmail),
			}))
		} else {
			r = r.WithContext(auth.WithTenantlessCaller(r.Context(), auth.Identity{
				Subject: user,
				Role:    r.Header.Get(headerUserRole),
				Email:   r.Header.Get(headerUserEmail),
			}))
		}
		next.ServeHTTP(w, r)
	})
}

// staffMiddleware reads the staff headers into the caller identityMiddleware stored, then runs
// auth.RequireRulesRole on every /v1/staff path. It reads the headers only on an App that called
// RequireGateway: the guard admits a request only with the gateway token or strips both headers.
// The path is checked decoded and cleaned, before the mux redirects an unclean one.
// ceiling: every /v1/staff/ route needs the rules role; split by sub-path when a second staff role exists.
func (a *App) staffMiddleware(next http.Handler) http.Handler {
	checked := auth.RequireRulesRole(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.gatewayToken != "" {
			r = r.WithContext(withStaffHeaders(r.Context(), r.Header))
		}
		if inStaffClass(r.URL.Path) || inStaffClass(path.Clean(r.URL.Path)) {
			checked.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func inStaffClass(p string) bool {
	return p == "/v1/staff" || strings.HasPrefix(p, "/v1/staff/")
}

func withStaffHeaders(ctx context.Context, h http.Header) context.Context {
	staff := h.Get(headerUserStaff) == "true"
	rules := staff && h.Get(headerUserRulesRole) == "true"
	if id, ok := auth.IdentityFromContext(ctx); ok {
		id.Staff, id.RulesRole = staff, rules
		return auth.WithIdentity(ctx, id)
	}
	if id, ok := auth.TenantlessCallerFromContext(ctx); ok {
		id.Staff, id.RulesRole = staff, rules
		return auth.WithTenantlessCaller(ctx, id)
	}
	return ctx
}
