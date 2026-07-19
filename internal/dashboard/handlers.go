// M4-07-03 (task-157), Stage 2.5: handler scaffold ONLY -- writeJSON/
// writeError mirror internal/invoice/handlers.go:480-488's helpers of the
// same name verbatim (the shared {"error":"..."} envelope convention,
// per-package duplicate, not a shared library). RollupHandler is a STUB for
// this stage: it always answers 501 "not implemented" without checking
// identity, calling rollup, or mapping errors, so every DASH-30..35 test in
// handlers_test.go fails on its status/body assertion, not on a compile
// error. Stage 3 (the executor) replaces the stub body with the real
// identity-first-401 -> call rollup -> statusForErr -> JSON logic per
// task-157's Implementation Plan.
package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// RollupHandler returns GET /v1/rollup. STUB (Stage 2.5 only): always 501,
// never touches rollup or log beyond the nil-logger default.
func RollupHandler(rollup func(ctx context.Context) (Rollup, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
