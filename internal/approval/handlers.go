package approval

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// This layer owns WIRE shape only — identity, the body cap, whether the body decodes,
// PUT's members presence. Every semantic 400 is the store's, and no handler reads
// memberships or auth.Identity.Role: permission is requireActiveAdmin's, read before
// any target row, which keeps the 403-vs-404 existence oracle closed.

// rolesResponse is the GET /v1/workflow-roles body.
type rolesResponse struct {
	WorkflowRoles []Role `json:"workflow_roles"`
}

// normalise fills a nil Members: a nil []T with no omitempty serialises as null, and
// the [] contract is this boundary's rather than any one producer's.
func normalise(r Role) Role {
	if r.Members == nil {
		r.Members = []string{}
	}
	return r
}

// ListRolesHandler returns GET /v1/workflow-roles. No access-role gate — any caller
// holding a tenant claim may list, the same as GET /v1/memberships.
func ListRolesHandler(list RolesLister, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		roles, err := list(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: list workflow roles", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		// Rebuilt, so a nil result also lands as [].
		out := make([]Role, 0, len(roles))
		for _, role := range roles {
			out = append(out, normalise(role))
		}
		writeJSON(w, http.StatusOK, rolesResponse{WorkflowRoles: out})
	}
}

// createRoleRequest is the POST /v1/workflow-roles wire body. No `key`: it is minted
// server-side, so a client that sends one is ignored.
type createRoleRequest struct {
	Title string `json:"title"`
	Desc  string `json:"desc"`
}

// CreateRoleHandler returns POST /v1/workflow-roles: identity (401) -> capped decode
// (400) -> create -> 201. No empty-title check here; that is the store's, on the
// trimmed value it actually stores.
func CreateRoleHandler(create RoleCreator, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxCreateBodyBytes)
		var req createRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		role, err := create(r.Context(), req.Title, req.Desc)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: create workflow role", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusCreated, normalise(role))
	}
}

// updateRoleRequest is the PATCH /v1/workflow-roles/{key} wire body. Pointers, so an
// ABSENT field is distinguishable from an empty one: {"desc":""} clears the blurb,
// {} changes nothing and is the store's ErrValidation.
type updateRoleRequest struct {
	Title *string `json:"title"`
	Desc  *string `json:"desc"`
}

// UpdateRoleHandler returns PATCH /v1/workflow-roles/{key}: identity (401) -> capped
// decode (400) -> path key -> update -> 200. Body before path, so a malformed request
// reads as 400 rather than 404. The key reaches the store verbatim — normalising it
// would make Tax-Reviewer address tax-reviewer, an alias newRoleKey never mints.
func UpdateRoleHandler(update RoleUpdater, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Same cap as create: the same two fields.
		r.Body = http.MaxBytesReader(w, r.Body, maxCreateBodyBytes)
		var req updateRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		role, err := update(r.Context(), r.PathValue("key"), req.Title, req.Desc)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: update workflow role", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, normalise(role))
	}
}

// DeleteRoleHandler returns DELETE /v1/workflow-roles/{key}: identity (401) -> path key
// -> soft delete -> 200. No body is read at all. Its `members` is [] because a deleted
// role has no addressable holders — that reads as "unstaffed", not "not reported", which
// is safe only while the SPA's removeRole drops the card without reading the body.
func DeleteRoleHandler(del RoleDeleter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		role, err := del(r.Context(), r.PathValue("key"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: delete workflow role", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, normalise(role))
	}
}

// setRoleMembersRequest is the PUT /v1/workflow-roles/{key}/members wire body — an
// object, not a bare array, so the SPA can PUT a whole Role and only members is read.
//
// *[]string: {} and {"members":null} must be a 400, while {"members":[]} is a legal
// unstaff. A plain []string collapses the first two into the third.
type setRoleMembersRequest struct {
	Members *[]string `json:"members"`
}

// SetRoleMembersHandler returns PUT /v1/workflow-roles/{key}/members: identity (401)
// -> capped decode (400) -> members presence (400) -> path key -> whole-set replace.
//
// The presence check is this layer's ONE semantic 400 and it must exist: a nil slice
// at the store means unstaff, so without it {} would silently wipe a role's staffing.
func SetRoleMembersHandler(staff RoleStaffer, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxStaffBodyBytes)
		var req setRoleMembersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Members == nil {
			writeError(w, http.StatusBadRequest, "members must be an array of member ids")
			return
		}

		role, err := staff(r.Context(), r.PathValue("key"), *req.Members)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: staff workflow role", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, normalise(role))
	}
}

// RunHandler returns GET /v1/invoices/{id}/approval. STUB — answers 501 so
// decision_handlers_test.go fails on its assertions rather than on a missing
// symbol; Stage 3 replaces the body with identity -> path id -> read ->
// decisionStatusForErr -> bare 200.
func RunHandler(read RunReader, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
