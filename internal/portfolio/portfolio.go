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
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		entity, err := create(r.Context(), CreateInput{
			Name:         req.Name,
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

// ListHandler returns GET /v1/entities (M3-03-03, task-36). NOT YET
// IMPLEMENTED: this is a compiling stub for the RED test-spec stage (Mode
// A) -- identity-first-401, query-param parsing into a ListFilter (status
// validation, limit default/clamp, offset), calling list, and building the
// {"entities","pagination"} response envelope are all the executor's job
// (Mode B). Always answers 501 for now so callers get a well-formed error
// body rather than a panic while this subtask is mid-flight. Route
// registration itself is M3-03-05.
func ListHandler(list func(ctx context.Context, f ListFilter) ([]Entity, int, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented: M3-03-03")
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
