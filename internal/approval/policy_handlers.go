package approval

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The six approval-policy handlers. Same division as the workflow-role layer: this
// file owns WIRE shape only — identity, the body cap, whether the body decodes, and
// PUT's steps presence. Every semantic 400 (name, scope, tree) is the store's, and no
// handler reads auth.Identity.Role: permission is requireActiveAdmin's.

// policiesResponse is the GET /v1/approval-policies body. A named field, never an
// embedded Policy — the promoted MarshalJSON would hijack this struct.
type policiesResponse struct {
	ApprovalPolicies []Policy `json:"approval_policies"`
}

// ListPoliciesHandler returns GET /v1/approval-policies. No access-role gate — any
// caller the request seam admits may list.
func ListPoliciesHandler(list PolicyLister, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		policies, err := list(r.Context())
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: list approval policies", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		// Rebuilt, so a nil result also lands as []. Policy.MarshalJSON only fixes a
		// policy's OWN nil lanes; the outer slice is plain.
		out := make([]Policy, 0, len(policies))
		out = append(out, policies...)
		writeJSON(w, http.StatusOK, policiesResponse{ApprovalPolicies: out})
	}
}

// GetPolicyHandler returns GET /v1/approval-policies/{id}: identity (401) -> path id
// -> read -> 200. No body is read at all.
func GetPolicyHandler(get PolicyGetter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		policy, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: get approval policy", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, policy)
	}
}

// CreatePolicyHandler returns POST /v1/approval-policies: identity (401) -> capped
// decode (400) -> create -> 201. Name and scope reach the store verbatim; the scope
// vocabulary is normalizeScope's, so a foreign one is the store's 400, not this
// layer's.
func CreatePolicyHandler(create PolicyCreator, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxPolicyBodyBytes)
		var req createPolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		policy, err := create(r.Context(), req.Name, req.Scope)
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: create approval policy", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusCreated, policy)
	}
}

// PutDraftHandler returns PUT /v1/approval-policies/{id}/draft: identity (401) ->
// capped decode (400) -> steps presence (400) -> path id -> whole-tree replace.
//
// The presence check is this layer's ONE semantic 400 and it must exist: a nil slice
// at the store means clear-the-tree, so without it {} would silently wipe a policy's
// steps. It runs before the path id, so a malformed body at an unknown id reads 400.
func PutDraftHandler(put PolicyDrafter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxPolicyBodyBytes)
		var req putDraftRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Steps == nil {
			writeError(w, http.StatusBadRequest, "steps must be an array of approval steps")
			return
		}

		policy, err := put(r.Context(), r.PathValue("id"), req.Name, req.Scope, *req.Steps)
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: put approval policy draft", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, policy)
	}
}

// PublishPolicyHandler returns POST /v1/approval-policies/{id}/publish: identity
// (401) -> path id -> seal and activate -> 200. No body is read at all — published_by
// is the caller's subject, taken inside the store.
func PublishPolicyHandler(publish PolicyPublisher, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		policy, err := publish(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: publish approval policy", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, policy)
	}
}

// DeletePolicyHandler returns DELETE /v1/approval-policies/{id}: identity (401) ->
// path id -> soft delete -> 200. No body is read at all.
func DeletePolicyHandler(del PolicyDeleter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		policy, err := del(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := policyStatusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "approval: delete approval policy", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, policy)
	}
}
