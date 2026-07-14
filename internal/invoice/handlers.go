// M4-02-03 (task-98) HTTP handler scaffold: the four routes the invoice
// service exposes over internal/invoice's Store -- POST /v1/invoices, GET
// /v1/invoices/{id}, GET /v1/invoices, POST /v1/invoices/{id}/transitions.
//
// RED-stage (Stage 2.5, Mode A) stub: every handler below always answers 501
// "not implemented" -- it never checks the caller's identity, never decodes
// the request body, and never calls the injected store closure. Real handler
// logic (identity-first-401, decode/validate, the [D4] statusForErr error
// map, the transition target's 7-state validity check, JSON response shape)
// is the executor's Stage 3 job, following internal/portfolio/portfolio.go's
// CreateHandler/GetHandler/ListHandler idiom plus a new TransitionHandler
// ([D12]: single POST .../transitions with a {"target":...} body).
//
// This file exists only so handlers_test.go (the INV-HTTP-* acceptance tests
// transcribed from task-98's Test Specs table) compiles against the real
// function signatures and fails on its status/body assertions -- not on a
// compile error.
package invoice

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// CreateHandler will become POST /v1/invoices (Stage 3): decode the
// snake_case wire body, 400 on a blank entity_id/invoice_number, call create,
// map errors via statusForErr, 201 + Invoice on success. Stub: always 501,
// never touches r or create.
func CreateHandler(create func(ctx context.Context, in CreateInput) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	return notImplementedHandler()
}

// GetHandler will become GET /v1/invoices/{id} (Stage 3): r.PathValue("id"),
// 200 + Invoice (with line_items) on success, 404 via ErrNotFound. Stub:
// always 501, never touches r or get.
func GetHandler(get func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	return notImplementedHandler()
}

// ListHandler will become GET /v1/invoices (Stage 3): parse/clamp
// limit/offset (portfolio's exact defaulting rules -- default 50, >200
// clamps to 200, <1 or non-integer 400; default 0, <0 or non-integer 400),
// call list, 200 + {"invoices":[...],"pagination":{...}}. Stub: always 501,
// never touches r or list.
func ListHandler(list func(ctx context.Context, f ListFilter) ([]Invoice, int, error), log *slog.Logger) http.HandlerFunc {
	return notImplementedHandler()
}

// TransitionHandler will become POST /v1/invoices/{id}/transitions (Stage 3,
// [D12]): decode {"target":...}, 400 "unknown status" if target is not one
// of the 7 canonical Status values (WITHOUT calling transition), else call
// transition and map ErrRedundantTransition/ErrIllegalTransition/ErrNotFound
// via statusForErr, 200 + updated Invoice on success. Stub: always 501,
// never touches r or transition.
func TransitionHandler(transition func(ctx context.Context, id string, target Status) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	return notImplementedHandler()
}

// notImplementedHandler is the shared RED-stage stub body: writes 501 with a
// non-empty {"error":...} envelope (so every handler_test.go response decode
// succeeds against valid JSON) and reads neither the request nor any
// injected closure -- the "store not called" assertions on the
// identity-missing/unknown-status specs hold trivially here, but the
// deliberately-wrong 501 status is what actually reddens each test.
func notImplementedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}

// writeJSON and writeError mirror internal/portfolio/portfolio.go's helpers
// of the same name verbatim (the shared {"error":"..."} envelope convention).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
