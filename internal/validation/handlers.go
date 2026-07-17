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
// PAYLOAD CONTRACT (see ValidateHandler): the handler passes the FULL decoded
// request body -- a Payload map with the "invoice" key intact, i.e.
// {"invoice": {...}} -- to loadAndEval UNCHANGED. This matches the engine's
// resolvePath, which roots at p["invoice"] (Decision N19), so the
// cmd/validation wiring (task-47) that binds loadAndEval needs NO re-wrap
// seam: the exact contract task-47 must implement is
//
//	func(ctx context.Context, p Payload) (Result, error) {
//	    rs, err := store.LoadActiveRuleSet(ctx)
//	    if err != nil {
//	        return Result{}, err
//	    }
//	    return engine.Evaluate(p, rs)
//	}
//
// (QA/M3-04-07: an earlier revision unwrapped "invoice" here and expected
// task-47 to re-wrap it before calling engine.Evaluate -- that seam's
// failure mode is a SILENT "all fields missing" bug if the re-wrap is ever
// forgotten, since a wrongly-rooted payload just resolves every target as
// absent rather than erroring. Passing the body through unchanged removes
// the seam entirely.) See rule.go for the Payload/Result/Violation/Rule wire
// shapes and store.go for the toggle signature.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ValidateHandler returns POST /v1/validate: checks the verified identity is
// present (401 before decode/eval, same order as portfolio.CreateHandler),
// decodes a {"invoice": {...}} body (400 on malformed JSON) into a Payload,
// and calls loadAndEval with the decoded body UNCHANGED (see file header --
// no unwrap/re-wrap seam), then answers 200 + Result (rule_set_version +
// violations) on success. A body whose "invoice" key is absent or not an
// object is passed straight through -- the engine's resolvePath tolerates a
// missing invoice (reports every path as absent, so the "required" rules
// simply fire), matching the {"invoice":{}} empty-invoice case rather than
// special-casing it into a 400.
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

		result, err := loadAndEval(r.Context(), body)
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Enabled == nil {
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

// --------------------------------------------------------------------------
// M4-04-03 -- POST /v1/validate/batch, the tenant-free peer surface.
// --------------------------------------------------------------------------

const (
	// maxBatchBytes caps the request body. Armed with http.MaxBytesReader
	// DOWNSTREAM of S2SMiddleware, so an unauthenticated oversized body is a
	// 401, never a 413 (see s2s.go).
	maxBatchBytes = 16 << 20 // 16 MiB
	// maxBatchItems caps per-batch fan-out: a hard bound on the work one
	// request can ask for, sized to pair with the 16 MiB body cap
	// (5,000 x ~3KB ~= 15MB). This is an independent choice, NOT inherited
	// from the importer -- the importer enforces no row/item ceiling at all,
	// only a 10 MiB byte cap (M4-04-03 Stage-1 addendum G2). Do not "re-align"
	// the two against a ceiling that does not exist.
	maxBatchItems = 5000
)

// batchItem is one wire item: an opaque caller-owned ref plus the invoice
// object to evaluate. Ref is echoed back untouched -- 04 never interprets it
// (it is 03's correlation handle).
type batchItem struct {
	Ref     string         `json:"ref"`
	Invoice map[string]any `json:"invoice"`
}

// batchRequest is the POST /v1/validate/batch request body.
type batchRequest struct {
	Invoices []batchItem `json:"invoices"`
}

// batchItemResult is one item's outcome: its echoed ref + every collected
// violation (collect-ALL, same as the single-invoice Result).
type batchItemResult struct {
	Ref        string      `json:"ref"`
	Violations []Violation `json:"violations"`
}

// batchResponse is the POST /v1/validate/batch success body. The rule-set
// version + its uuid are stamped ONCE for the whole batch, not per item --
// one load means one version, so a per-item stamp could only ever repeat
// itself ([uuid-stamp]).
type batchResponse struct {
	RuleSetVersion   int               `json:"rule_set_version"`
	RuleSetVersionID string            `json:"rule_set_version_id"`
	Results          []batchItemResult `json:"results"`
}

// BatchValidateHandler returns POST /v1/validate/batch: 03 submits many
// invoices in one request and gets each one's violations back, stamped with
// the single rule-set version they were all evaluated against ([batch-wire]).
//
// It reads NO tenant. It never touches X-Tenant-ID and never calls
// auth.IdentityFromContext ([s2s-identity]) -- contrast ValidateHandler's
// identity-first-401 above. Evaluation is a pure function of (payload, active
// global rule-set): there is no tenant to assert and no tenant-scoped data
// behind this endpoint to leak, so the trust boundary does not widen. Peer
// authentication is S2SMiddleware's job, upstream of here; all tenant-scoped
// work stays in 03.
//
// loadRuleSet is injected (rather than reaching for a Store) so the single-
// load-per-batch property is provable with a counting fake, and so this file
// keeps its no-DB-import shape -- the same discipline as ValidateHandler's
// loadAndEval closure. main.go binds it to Store.LoadActiveRuleSetGlobal.
// eng is the shipped, stateless *Engine, reused across every item.
//
// Order of operations is load-bearing:
//  1. cap the body (MaxBytesReader) -- but only after S2SMiddleware's 401.
//  2. decode; an oversized body is a 413, checked BEFORE the generic 400
//     (Stage-1 addendum G1: statusForErr has no 413 branch and cannot grow
//     one usefully, since only the decode site knows the body was capped --
//     house pattern, internal/importer/handlers.go:112-120).
//  3. bound the item count (400 on empty or over-cap) BEFORE loading, so a
//     junk request never costs a query.
//  4. load the rule-set EXACTLY ONCE for the whole batch -- the entire point
//     of this endpoint, and what makes the <60s gate reachable.
//  5. evaluate every item against that one rule-set, results in REQUEST
//     order.
func BatchValidateHandler(loadRuleSet func(ctx context.Context) (RuleSet, error), eng *Engine, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)

		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// The 413 check MUST precede the generic 400: MaxBytesReader
			// surfaces the cap as a *http.MaxBytesError from Decode, which is
			// otherwise indistinguishable from malformed JSON.
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the batch size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if len(req.Invoices) == 0 {
			writeError(w, http.StatusBadRequest, "invoices must not be empty")
			return
		}
		if len(req.Invoices) > maxBatchItems {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invoices exceeds the %d-item batch limit", maxBatchItems))
			return
		}

		// ONE load for the whole batch, however many invoices it carries.
		// ErrNoActiveRuleSet (and ErrEmptyRuleSet, which wraps it) -> 503 via
		// statusForErr: the gate cannot evaluate, so it refuses -- it never
		// answers a clean 200 it cannot stand behind.
		rs, err := loadRuleSet(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: batch validate: load rule-set", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		results := make([]batchItemResult, 0, len(req.Invoices))
		for _, it := range req.Invoices {
			// RE-ROOT: the engine's resolvePath roots at p["invoice"]
			// (Decision N19), so each item's invoice object must be wrapped
			// back into a Payload before evaluation. Passing it.Invoice
			// unwrapped fails LOUDLY but misleadingly -- every target resolves
			// as absent, so every `required` rule fires on data that is in
			// fact valid ([batch-payload-rooting]). This wrap is one typed
			// line inside the one function that owns it; VB-12 (a fully valid
			// invoice -> zero violations) is what discriminates it.
			result, err := eng.Evaluate(Payload{"invoice": it.Invoice}, rs)
			if err != nil {
				// A config fault (unknown rule type, bad regex, broken CEL) is
				// a property of the RULE-SET, not of this item -- it would fail
				// identically for every item, so the WHOLE batch fails with a
				// 500 and no partial results (Decision N15 / [batch-fault-
				// semantics]: fail loud on a broken rule, never silently pass).
				// Bad DATA is always a violation, never an error, so this can
				// never be triggered by a caller's payload.
				log.ErrorContext(r.Context(), "validation: batch validate: evaluate",
					slog.Any("err", err), slog.String("ref", it.Ref))
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			results = append(results, batchItemResult{Ref: it.Ref, Violations: result.Violations})
		}

		writeJSON(w, http.StatusOK, batchResponse{
			RuleSetVersion:   rs.Version,
			RuleSetVersionID: rs.ID,
			Results:          results,
		})
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
