// Package portfolio is the 03 Portfolio Entity context: the CRUD and
// lifecycle surface for a tenant's business_entities (the client businesses a
// firm manages, or the single entity an in-house tenant tracks — story
// M3-03). Every store method wraps db.WithinRequestTenantTx, so writes and
// reads are scoped to the caller's tenant under RLS; every mutation writes an
// audit.Record row in the SAME transaction as the domain change (story
// Decision [A5]), so a failed audit write rolls back the domain write too.
//
// This subtask (M3-03-02) establishes the domain types, the error model, and
// the Create + Read handlers/store methods; M3-03-03/04 add List/Update/
// lifecycle-transition methods on the same scaffold. Handlers here are
// constructed but not yet wired to a mux — cmd/portfolio/main.go route
// registration is M3-03-05.
package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Entity is a business_entities row: one of a tenant's portfolio businesses.
type Entity struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	TIN          *string   `json:"tin"`
	Registration *string   `json:"registration"`
	Sector       *string   `json:"sector"`
	Address      *string   `json:"address"`
	Status       string    `json:"status"` // "active" | "archived"
	CreatedAt    time.Time `json:"created_at"`
}

// CreateInput is the Store.Create argument. TIN is required (string, not
// *string) per story Decision [A3]; Registration/Sector/Address are optional.
type CreateInput struct {
	Name         string
	TIN          string
	Registration *string
	Sector       *string
	Address      *string
}

// UpdateInput is the Store.Update argument (M3-03-04, task-37): a partial
// update over business_entities' mutable fields. Only non-nil fields are
// applied; nil means "leave unchanged". Deliberately has NO Status field --
// lifecycle transitions are Store.SetStatus's job (via OffboardHandler/
// OnboardHandler), not Update's, so PATCH structurally cannot touch status
// (story Decision [A6]).
type UpdateInput struct {
	Name         *string
	TIN          *string
	Registration *string
	Sector       *string
	Address      *string
}

// ListFilter is the Store.List query (M3-03-03, task-36). Status nil means
// both active and archived; Q is an optional case-insensitive substring
// match over name OR tin (empty = no filter); Limit/Offset paginate.
// ListHandler is responsible for producing a validated ListFilter from query
// params (status must be exactly "active"/"archived" or omitted; limit
// defaults to 50 and clamps to [1,200]; offset defaults to 0) -- that
// parsing/validation is the executor's job (Mode B), not part of this
// RED-stage scaffold.
type ListFilter struct {
	Status *string
	Q      string
	Limit  int
	Offset int
}

// Sentinels for the portfolio error model (story Decision [A5]). ErrInvalidTIN
// is declared in tin.go (M3-03-01, ValidateTIN's error) and reused here, not
// redeclared.
var (
	ErrValidation          = errors.New("portfolio: validation")
	ErrNotFound            = errors.New("portfolio: not found")
	ErrDuplicateTIN        = errors.New("portfolio: duplicate tin")
	ErrRedundantTransition = errors.New("portfolio: redundant transition")
)

// createEntityRequest is the POST /v1/entities wire body (snake_case JSON
// tags). Named distinctly from the test file's own createRequest (same
// package, used there only to marshal test request bodies).
type createEntityRequest struct {
	Name         string  `json:"name"`
	TIN          string  `json:"tin"`
	Registration *string `json:"registration,omitempty"`
	Sector       *string `json:"sector,omitempty"`
	Address      *string `json:"address,omitempty"`
}

// CreateHandler returns POST /v1/entities. It checks the verified identity is
// present (401 before decode/create, exactly like tenancy.MeHandler's order),
// decodes the request body (400 on decode error or empty name), calls create,
// maps errors via statusForErr, and answers 201 + Entity on success.
func CreateHandler(create func(ctx context.Context, in CreateInput) (Entity, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req createEntityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		trimmedName := strings.TrimSpace(req.Name)
		if trimmedName == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		entity, err := create(r.Context(), CreateInput{
			Name:         trimmedName,
			TIN:          req.TIN,
			Registration: req.Registration,
			Sector:       req.Sector,
			Address:      req.Address,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: create entity", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusCreated, entity)
	}
}

// GetHandler returns GET /v1/entities/{id}. Same identity-first-401 order as
// CreateHandler, reading r.PathValue("id"); 404 via ErrNotFound, 200 + Entity
// on success.
func GetHandler(get func(ctx context.Context, id string) (Entity, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		entity, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: get entity", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, entity)
	}
}

// listPagination is the "pagination" object in ListHandler's response
// envelope (story Decision [A4]): the effective limit/offset applied (after
// defaulting/clamping) plus the total filtered count across all pages.
type listPagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

// listResponse is the GET /v1/entities response body: {"entities":
// [...],"pagination": {...}}. Entities is []Entity (never a nil slice from
// Store.List), so an empty result serializes "entities":[] rather than
// "entities":null.
type listResponse struct {
	Entities   []Entity       `json:"entities"`
	Pagination listPagination `json:"pagination"`
}

// ListHandler returns GET /v1/entities (M3-03-03, task-36). Same
// identity-first-401 order as Create/GetHandler. Query params: status
// (omitted -> both; must be exactly "active"/"archived" else 400), q (raw
// substring, empty = no filter), limit (default 50, non-integer or <1 -> 400,
// clamped down to 200 when over), offset (default 0, non-integer or negative
// -> 400). limit<1 and offset<0 both 400 rather than clamp -- a caller asking
// for zero/negative rows almost certainly has a bug, so silently returning
// data would hide it; only the over-200 case is a generous cap, not a
// nonsensical request. Route registration itself is M3-03-05.
func ListHandler(list func(ctx context.Context, f ListFilter) ([]Entity, int, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		query := r.URL.Query()

		var statusFilter *string
		if raw := query.Get("status"); raw != "" {
			if raw != "active" && raw != "archived" {
				writeError(w, http.StatusBadRequest, `status must be "active" or "archived"`)
				return
			}
			statusFilter = &raw
		}

		limit := 50
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit > 200 {
			limit = 200
		} else if limit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be >= 1")
			return
		}

		offset := 0
		if raw := query.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "offset must be an integer")
				return
			}
			offset = n
		}
		if offset < 0 {
			writeError(w, http.StatusBadRequest, "offset must be >= 0")
			return
		}

		filter := ListFilter{Status: statusFilter, Q: query.Get("q"), Limit: limit, Offset: offset}

		items, total, err := list(r.Context(), filter)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: list entities", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, listResponse{
			Entities:   items,
			Pagination: listPagination{Limit: filter.Limit, Offset: filter.Offset, Total: total},
		})
	}
}

// updateEntityRequest is the PATCH /v1/entities/{id} wire body (snake_case
// JSON tags, pointer fields so "field absent" is distinguishable from "field
// present"). Deliberately has NO status field -- lifecycle transitions are
// Offboard/OnboardHandler's job, not Update's (story Decision [A6]).
type updateEntityRequest struct {
	Name         *string `json:"name"`
	TIN          *string `json:"tin"`
	Registration *string `json:"registration"`
	Sector       *string `json:"sector"`
	Address      *string `json:"address"`
}

// UpdateHandler returns PATCH /v1/entities/{id} (M3-03-04, task-37). Same
// identity-first-401 order as Create/GetHandler, then decodes the PATCH body
// into updateEntityRequest (400 on decode error), rejects an all-nil body
// as 400 ("no fields to update") before ever calling update, maps store
// errors via statusForErr, and answers 200 + updated Entity on success.
func UpdateHandler(update func(ctx context.Context, id string, in UpdateInput) (Entity, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req updateEntityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == nil && req.TIN == nil && req.Registration == nil && req.Sector == nil && req.Address == nil {
			writeError(w, http.StatusBadRequest, "no fields to update")
			return
		}

		if req.Name != nil {
			trimmedName := strings.TrimSpace(*req.Name)
			if trimmedName == "" {
				writeError(w, http.StatusBadRequest, "name cannot be blank")
				return
			}
			req.Name = &trimmedName
		}

		entity, err := update(r.Context(), r.PathValue("id"), UpdateInput{
			Name:         req.Name,
			TIN:          req.TIN,
			Registration: req.Registration,
			Sector:       req.Sector,
			Address:      req.Address,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: update entity", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, entity)
	}
}

// OffboardHandler returns POST /v1/entities/{id}/offboard (M3-03-04,
// task-37). Same identity-first-401 order, reading r.PathValue("id") and
// calling setStatus(ctx, id) -- the caller binds "archived" as the target at
// wiring time (M3-03-05); this handler is target-agnostic. Maps ErrNotFound
// to 404, ErrRedundantTransition to 409, else statusForErr; 200 + Entity on
// success.
func OffboardHandler(setStatus func(ctx context.Context, id string) (Entity, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		entity, err := setStatus(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: offboard entity", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, entity)
	}
}

// OnboardHandler returns POST /v1/entities/{id}/onboard (M3-03-04, task-37):
// mirrors OffboardHandler; the caller binds "active" as the target at wiring
// time (M3-03-05).
func OnboardHandler(setStatus func(ctx context.Context, id string) (Entity, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		entity, err := setStatus(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "portfolio: onboard entity", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, entity)
	}
}

// statusForErr maps a store/domain error to the HTTP status + message the
// real handler bodies (added by the executor) write to the response.
// db.ErrNoTenant is 401 (fail-closed); ErrInvalidTIN/ErrValidation are 400
// with the wrapped message; ErrNotFound is 404; ErrDuplicateTIN/
// ErrRedundantTransition are 409; anything else is 500 with a generic body —
// this helper never leaks internals into the response. Logging the
// unrecognized (500) case via slog is the caller's responsibility, since only
// the caller knows the operation name to log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrInvalidTIN), errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrDuplicateTIN):
		return http.StatusConflict, "duplicate tin"
	case errors.Is(err, ErrRedundantTransition):
		return http.StatusConflict, "redundant transition"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a
// *pgconn.PgError. Copied verbatim from
// internal/platform/db/tenants_kind_test.go:33-40 — that copy lives in a
// _test.go file in a different package (db_test) so it is not importable;
// this is portfolio's own production copy, needed by Store.Create to map a
// unique_violation (23505) on business_entities_tenant_tin_uq to
// ErrDuplicateTIN.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
