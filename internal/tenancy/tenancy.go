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

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Tenant is a caller's tenant as read from the tenants table under RLS.
type Tenant struct {
	ID   string
	Name string
	Kind string // "firm" | "in_house" (M3-01 tenants.kind discriminator)
}

// Membership is one row of the caller's tenant's memberships. JSON tags are
// snake_case (user_id, role, status, display_name, email) -- the GET
// /v1/memberships wire contract (A3). display_name/email are nullable and
// serialize as JSON null, never omitted.
type Membership struct {
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
}

// ErrTenantNotFound means the caller's tenant id resolved to no visible row — a
// well-formed identity whose tenant does not exist (or is invisible under RLS).
var ErrTenantNotFound = errors.New("tenancy: tenant not found")

// ErrNoMembership means the caller's identity and tenant both resolved, but the
// caller holds no memberships row in that tenant — an authenticated caller with
// no domain role. Fail-closed: a role must never be defaulted when this occurs.
var ErrNoMembership = errors.New("tenancy: no membership")

// Sentinels for PATCH /v1/memberships/{user_id} (SetMembershipStatus).
var (
	ErrNotPermitted             = errors.New("tenancy: not permitted")
	ErrMembershipNotFound       = errors.New("tenancy: membership not found")
	ErrInvalidStatus            = errors.New("tenancy: invalid status")
	ErrInvitedNotTransitionable = errors.New("tenancy: invited membership is not transitionable")
	ErrLastActiveAdmin          = errors.New("tenancy: last active admin")
)

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
// memberships, each as {user_id, role, status, display_name, email} (A3).
type membershipsResponse struct {
	Memberships []Membership `json:"memberships"`
}

// MembershipStatusSetter applies an admin-directed status change to one
// membership row, returning the updated row. Store.SetMembershipStatus is
// the production implementation.
type MembershipStatusSetter func(ctx context.Context, userID, status string) (Membership, error)

// maxSetStatusBodyBytes bounds the PATCH body BEFORE it is decoded — the
// platform server applies no request body limit of its own. A legitimate body
// is ~30 bytes, so 4 KiB is ~130x headroom without opening the door to
// unbounded allocation. Over-cap is a 400, not a 413
// (TestMembership_BodyOverCapRejected).
const maxSetStatusBodyBytes = 4 * 1024

// setMembershipStatusRequest is the PATCH /v1/memberships/{user_id} wire body.
type setMembershipStatusRequest struct {
	Status string `json:"status"`
}

// SetMembershipStatusHandler returns PATCH /v1/memberships/{user_id}: identity
// (401, before anything else) -> capped decode (400 on any decode error) ->
// status vocabulary (400) -> path user_id -> set -> 200 with the updated
// membership in the same five-key shape as a list element.
//
// The body is validated before the path id so a malformed request reads as 400
// rather than 404 (TestMembership_InvalidStatusRejected). Admin-only-ness and
// the last-active-admin rule are the store's, not this layer's — see
// Store.SetMembershipStatus for why the caller's role must be read before the
// target row.
func SetMembershipStatusHandler(set MembershipStatusSetter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxSetStatusBodyBytes)
		var req setMembershipStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Status != "active" && req.Status != "suspended" {
			writeError(w, http.StatusBadRequest, `status must be "active" or "suspended"`)
			return
		}

		// Parsed last: no route can deliver an empty segment. An unparseable id
		// is 404, not 400 — indistinguishable by design from one that is merely
		// invisible. The canonical form is what the store compares against the
		// uuid text Postgres returns.
		userID, err := uuid.Parse(r.PathValue("user_id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}

		membership, err := set(r.Context(), userID.String(), req.Status)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: set membership status", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, membership)
	}
}

// statusForErr maps a store error to the HTTP status + message, in the
// internal/portfolio shape. The two 409 messages are hand-written rather than
// err.Error() so the "tenancy: " sentinel prefix never reaches the SPA, which
// renders them as the reason.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrInvalidStatus):
		return http.StatusBadRequest, `status must be "active" or "suspended"`
	case errors.Is(err, ErrNotPermitted):
		return http.StatusForbidden, "only an admin can change a member's status"
	case errors.Is(err, ErrMembershipNotFound):
		return http.StatusNotFound, "membership not found"
	case errors.Is(err, ErrInvitedNotTransitionable):
		return http.StatusConflict, "an invited member has no sign-in to suspend or reactivate"
	case errors.Is(err, ErrLastActiveAdmin):
		return http.StatusConflict, "this is the tenant's last active admin — make another member an active admin first"
	default:
		return http.StatusInternalServerError, "internal server error"
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
