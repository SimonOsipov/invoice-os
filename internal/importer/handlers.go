// M4-03-05 (task-106): POST /v1/imports -- the HTTP handler over Service.Import.
// This file currently holds ONLY a not-implemented stub (Stage 3 of this
// subtask writes the real handler; see handlers_test.go's doc comment for the
// full Test Specs map). The stub always answers 501 without checking
// identity, parsing the multipart body, enforcing the 10 MiB upload cap, or
// calling the injected imp closure -- every IMP-API-01..07 assertion in
// handlers_test.go fails on ITS status/body expectation (got 501), not on a
// compile error, which is what makes this subtask's RED commit valid.
package importer

import (
	"context"
	"log/slog"
	"net/http"
)

// CreateHandler will return POST /v1/imports (mirrors internal/invoice's
// CreateHandler factory: a closure over the injected Service.Import method ->
// http.HandlerFunc, identity-first-401, local statusForErr, shared
// {"error":"..."} envelope -- Stage 3 of this subtask). imp is
// (*Service).Import itself, so the executor wires CreateHandler(svc.Import,
// log) exactly like invoice.CreateHandler(store.Create, log).
func CreateHandler(imp func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}
}
