// This file (handlers.go) is the M3-04-07 HTTP surface over the engine:
// ValidateHandler (POST /v1/validate, story API spec) runs a submitted
// invoice payload through loadAndEval and answers the engine's Result;
// ToggleHandler (PATCH /v1/rules/{key}, the M3-06 admin kill-switch) flips a
// rule's enabled bit via toggle and answers the updated Rule. Both take the
// store/engine call as an injected closure (no DB import here) so
// handlers_test.go can stub them -- mirrors internal/portfolio/portfolio.go's
// CreateHandler/GetHandler shape: identity-first-401, decode, delegate,
// statusForErr, flat {"error":...} envelope on failure.
//
// This is the RED skeleton (Mode A, task M3-04-07): both handlers
// unconditionally answer 501 "not implemented" and never call their closure.
// handlers_test.go is the AC-derived httptest suite written against this
// contract; the executor fills in the real decode/delegate/statusForErr
// logic (plus writeJSON/writeError helpers, following portfolio.go's
// pattern) next. See rule.go for the Payload/Result/Violation/Rule wire
// shapes and store.go for the loadAndEval/toggle signatures this file's
// closures are bound to at cmd/validation wiring time (M3-04-08).
package validation

import (
	"context"
	"log/slog"
	"net/http"
)

// ValidateHandler returns POST /v1/validate: decodes a {"invoice": {...}}
// body, calls loadAndEval, and answers 200 + Result (rule_set_version +
// violations) on success.
func ValidateHandler(loadAndEval func(ctx context.Context, p Payload) (Result, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}

// ToggleHandler returns PATCH /v1/rules/{key}: decodes a {"enabled": bool}
// body, reads the rule key from r.PathValue("key"), calls toggle, and
// answers 200 + the updated Rule on success.
func ToggleHandler(toggle func(ctx context.Context, key string, enabled bool) (Rule, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}
