// This file (handlers.go) is the M3-04-07 HTTP surface over the engine:
// ValidateHandler (POST /v1/validate, story API spec) runs a submitted
// invoice payload through loadAndEval and answers the engine's Result;
// ToggleHandler (PATCH /v1/rules/{key}, the M3-06 admin kill-switch) flips a
// rule's enabled bit via toggle and answers the updated Rule. Both take the
// store/engine call as an injected closure (no DB import for the engine path)
// so handlers_test.go can stub them -- mirrors internal/portfolio/
// portfolio.go's CreateHandler/GetHandler shape: identity-first-401, decode,
// delegate, statusForErr, flat {"error":...} envelope on failure.
//
// PAYLOAD CONTRACT (see ValidateHandler): the handler UNWRAPS the request
// body's "invoice" object and hands THAT object to loadAndEval as the Payload
// -- loadAndEval receives the invoice payload itself, NOT the {"invoice":{...}}
// envelope. The engine's resolvePath roots at p["invoice"] (Decision N19), so
// the cmd/validation wiring (M3-04-08) that binds loadAndEval MUST re-wrap the
// invoice before calling the engine, i.e.
// engine.Evaluate(map[string]any{"invoice": inv}, rs). The unwrap lives here
// (pinned by handlers_test.go's TestValidate_Happy200, which asserts
// loadAndEval is called with the decoded invoice object); the re-wrap is the
// wiring seam's job. See rule.go for the Payload/Result/Violation/Rule wire
// shapes and store.go for the loadAndEval/toggle signatures.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ValidateHandler returns POST /v1/validate: checks the verified identity is
// present (401 before decode/eval, same order as portfolio.CreateHandler),
// decodes a {"invoice": {...}} body (400 on malformed JSON), unwraps the
// "invoice" object and calls loadAndEval with it, then answers 200 + Result
// (rule_set_version + violations) on success. A body whose "invoice" key is
// absent or not an object passes a nil payload straight through -- the engine's
// resolvePath tolerates a missing invoice (reports every path as absent, so the
// "required" rules simply fire), matching the {"invoice":{}} empty-invoice case
// rather than special-casing it into a 400.
func ValidateHandler(loadAndEval func(ctx context.Context, p Payload) (Result, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var body Payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Unwrap: hand loadAndEval the invoice object itself (the wiring
		// re-wraps {"invoice": inv} for the engine -- see file header).
		inv, _ := body["invoice"].(map[string]any)

		result, err := loadAndEval(r.Context(), inv)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: validate", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// toggleRequest is the PATCH /v1/rules/{key} wire body. Enabled is a *bool so
// an ABSENT "enabled" key (body "{}") is distinguishable from an explicit
// {"enabled":false}: the former is a 400 ("enabled is required"), the latter a
// valid false-toggle request.
type toggleRequest struct {
	Enabled *bool `json:"enabled"`
}

// ToggleHandler returns PATCH /v1/rules/{key}: same identity-first-401 order as
// ValidateHandler, decodes a {"enabled": bool} body (400 on malformed JSON or
// an absent "enabled" key), reads the rule key from r.PathValue("key"), calls
// toggle, maps store errors via statusForErr (404 unknown key, 409 redundant,
// 503 no active rule-set), and answers 200 + the updated Rule on success.
func ToggleHandler(toggle func(ctx context.Context, key string, enabled bool) (Rule, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req toggleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required")
			return
		}

		rule, err := toggle(r.Context(), r.PathValue("key"), *req.Enabled)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: toggle rule", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, rule)
	}
}

// statusForErr maps a store/engine error to the HTTP status + message the
// handlers write to the response. db.ErrNoTenant is 401 (fail-closed, mirroring
// portfolio.statusForErr); ErrValidation is 400 with the wrapped message;
// ErrNotFound is 404; ErrRedundantTransition is 409; ErrNoActiveRuleSet is 503
// (the engine has no published version to evaluate against); anything else is
// 500 with a generic body -- this helper never leaks internals into the
// response. Logging the unrecognized (500) case via slog is the caller's
// responsibility, since only the caller knows the operation name to log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrRedundantTransition):
		return http.StatusConflict, "redundant transition"
	case errors.Is(err, ErrNoActiveRuleSet):
		return http.StatusServiceUnavailable, "no active rule-set"
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
