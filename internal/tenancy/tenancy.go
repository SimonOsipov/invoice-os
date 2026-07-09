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
	Kind string // "firm" | "in_house" (M3-01 tenants.kind discriminator)
}

// Membership is one row of the caller's tenant's memberships: a user and their
// domain role. Added in M3-02-01 for the Me/loader shape; M3-02-02's member-list
// endpoint is the first consumer of the slice form. JSON tags are snake_case
// (user_id, role) -- the GET /v1/memberships wire contract (A3).
type Membership struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// ErrTenantNotFound means the caller's tenant id resolved to no visible row — a
// well-formed identity whose tenant does not exist (or is invisible under RLS).
var ErrTenantNotFound = errors.New("tenancy: tenant not found")

// ErrNoMembership means the caller's identity and tenant both resolved, but the
// caller holds no memberships row in that tenant — an authenticated caller with
// no domain role. Fail-closed: a role must never be defaulted when this occurs.
var ErrNoMembership = errors.New("tenancy: no membership")

// MeLoader resolves the current caller's tenant and their domain role (from
// memberships). The handler depends on this narrow function type rather than a
// pool, so its HTTP contract is unit-testable without a database; the production
// loader (Store.Me) runs the real tenant + membership queries.
type MeLoader func(ctx context.Context) (Tenant, string, error)

// MeHandler returns GET /v1/me. It reads the verified identity the platform's
// identityMiddleware placed in the context (401 if absent — the endpoint is
// tenant-scoped and must never answer without a caller), resolves the tenant and
// domain role via load, and returns them. A missing/invalid tenant is 401
// (db.ErrNoTenant, fail-closed); an unknown tenant is 404; a resolved tenant with
// no membership row is 403 (ErrNoMembership, fail-closed — a role is never
// defaulted); anything else is 500.
func MeHandler(load MeLoader, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tenant, role, err := load(r.Context())
		switch {
		case errors.Is(err, db.ErrNoTenant):
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		case errors.Is(err, ErrTenantNotFound):
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		case errors.Is(err, ErrNoMembership):
			writeError(w, http.StatusForbidden, "no membership")
			return
		case err != nil:
			log.ErrorContext(r.Context(), "tenancy: load current tenant", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		var resp meResponse
		resp.Tenant.ID = tenant.ID
		resp.Tenant.Name = tenant.Name
		resp.Tenant.Kind = tenant.Kind
		resp.User.ID = id.Subject
		resp.User.Role = role
		writeJSON(w, http.StatusOK, resp)
	}
}

// meResponse is the GET /v1/me body: the caller's tenant (resolved through the
// RLS-scoped query, including its kind discriminator) and the user identity,
// with the domain role resolved from memberships (not the JWT role claim).
type meResponse struct {
	Tenant struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"tenant"`
	User struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"user"`
}

// MembershipsLoader lists the caller's tenant's memberships (user + domain
// role), RLS-scoped to the current tenant. As with MeLoader, the handler
// depends on this narrow function type rather than a pool, so its HTTP
// contract is unit-testable without a database; the production loader
// (Store.ListMemberships) runs the real, RLS-scoped query.
type MembershipsLoader func(ctx context.Context) ([]Membership, error)

// MembershipsHandler returns GET /v1/memberships. It reads the verified
// identity from context (401 if absent, before the loader ever runs -- same
// fail-closed shape as MeHandler), then lists the caller's tenant's
// memberships via load. Unlike MeHandler, this endpoint does NOT gate on the
// caller holding a membership row: db.ErrNoTenant is 401 (fail-closed, no
// tenant context at all); any other loader error is 500. Per A4 there is
// deliberately no 403/404 mapping here. A nil/empty result is normalized to
// an empty slice so the memberships field always serializes as `[]`, never
// `null`.
func MembershipsHandler(load MembershipsLoader, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		memberships, err := load(r.Context())
		switch {
		case errors.Is(err, db.ErrNoTenant):
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		case err != nil:
			log.ErrorContext(r.Context(), "tenancy: list memberships", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		if memberships == nil {
			memberships = []Membership{}
		}
		writeJSON(w, http.StatusOK, membershipsResponse{Memberships: memberships})
	}
}

// membershipsResponse is the GET /v1/memberships body: the caller's tenant's
// memberships, each as {user_id, role} (A3).
type membershipsResponse struct {
	Memberships []Membership `json:"memberships"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
