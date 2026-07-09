// Package tenancy is the 01 Tenancy context service. Its first real endpoint,
// GET /v1/me, resolves the caller injected by the gateway (X-Tenant-ID /
// X-User-ID / X-User-Role) to their tenant by reading the tenants table under
// Row-Level Security — the app-role query is scoped by the app.current_tenant GUC
// (SET LOCAL), so the policy, not a WHERE clause, is what limits it to the one
// tenant the caller acts within. It is the endpoint M2-13's mock-login round trip
// calls to prove the auth -> gateway -> SET LOCAL -> RLS path end to end.
package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Tenant is a caller's tenant as read from the tenants table under RLS.
type Tenant struct {
	ID   string
	Name string
}

// ErrTenantNotFound means the caller's tenant id resolved to no visible row — a
// well-formed identity whose tenant does not exist (or is invisible under RLS).
var ErrTenantNotFound = errors.New("tenancy: tenant not found")

// TenantLoader resolves the current caller's tenant. The handler depends on this
// narrow function type rather than a pool, so its HTTP contract is unit-testable
// without a database; the production loader (Store.CurrentTenant) runs the real
// tenant-scoped query.
type TenantLoader func(ctx context.Context) (Tenant, error)

// MeHandler returns GET /v1/me. It reads the verified identity the platform's
// identityMiddleware placed in the context (401 if absent — the endpoint is
// tenant-scoped and must never answer without a caller), resolves the tenant via
// load, and returns the caller's tenant and user. A missing/invalid tenant is 401
// (db.ErrNoTenant, fail-closed); an unknown tenant is 404; anything else is 500.
func MeHandler(load TenantLoader, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tenant, err := load(r.Context())
		switch {
		case errors.Is(err, db.ErrNoTenant):
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		case errors.Is(err, ErrTenantNotFound):
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		case err != nil:
			log.ErrorContext(r.Context(), "tenancy: load current tenant", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		var resp meResponse
		resp.Tenant.ID = tenant.ID
		resp.Tenant.Name = tenant.Name
		resp.User.ID = id.Subject
		resp.User.Role = id.Role
		writeJSON(w, http.StatusOK, resp)
	}
}

// meResponse is the GET /v1/me body: the caller's tenant (resolved through the
// RLS-scoped query) and the user identity carried by the token.
type meResponse struct {
	Tenant struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tenant"`
	User struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"user"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
