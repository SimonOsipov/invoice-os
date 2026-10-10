// This file (handlers.go) is the HTTP surface over the engine: ToggleHandler
// (PATCH /v1/rules/{key}, 401 without an identity, otherwise 403) and BatchValidateHandler
// (POST /v1/validate/batch, behind S2SMiddleware). Failures use the flat
// {"error":...} envelope. See rule.go for the wire shapes.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// rulesManagedMessage is the 403 body for every authenticated PATCH /v1/rules/{key}.
const rulesManagedMessage = "rules are managed by ASComply"

// ToggleHandler answers 401 without an identity, otherwise 403: rules change only through the operator
// kill switch (vault runbook "Rule kill switch"). It never reads the body and reaches
// no database, so no key or body shape is an oracle.
func ToggleHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusForbidden, rulesManagedMessage)
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
// violation (collect-ALL, same as the single-invoice Result), stamped with the
// rule-set version it was judged by.
type batchItemResult struct {
	Ref              string      `json:"ref"`
	Violations       []Violation `json:"violations"`
	RuleSetVersion   int         `json:"rule_set_version"`
	RuleSetVersionID string      `json:"rule_set_version_id"`
}

// batchResponse is the POST /v1/validate/batch success body. The top-level
// rule-set version + uuid are the LOWEST version used in the batch; each
// item carries its own stamp.
type batchResponse struct {
	RuleSetVersion   int               `json:"rule_set_version"`
	RuleSetVersionID string            `json:"rule_set_version_id"`
	Results          []batchItemResult `json:"results"`
}

// BatchValidateHandler returns POST /v1/validate/batch: 03 submits many
// invoices in one request and gets each one's violations back, stamped with
// the rule-set version in force on that invoice's issue date.
//
// It reads NO tenant. It never touches X-Tenant-ID and never calls
// auth.IdentityFromContext ([s2s-identity]) -- contrast ToggleHandler's
// identity-first-401 above. Peer authentication is S2SMiddleware's job,
// upstream of here; all tenant-scoped work stays in 03.
//
// load is injected so the one-call-per-batch property is provable with a
// counting fake; main.go binds it to Store.LoadForDates. An item's date is its
// invoice.issue_date when that parses as time.DateOnly, else today in UTC, so
// an undated item never meets a scheduled version. now nil means time.Now.
// eng is the shipped, stateless *Engine, reused across every item.
//
// Order of operations is load-bearing:
//  1. cap the body (MaxBytesReader) -- but only after S2SMiddleware's 401.
//  2. decode; an oversized body is a 413, checked BEFORE the generic 400.
//  3. bound the item count (400 on empty or over-cap) BEFORE loading, so a
//     junk request never costs a query.
//  4. load once with the distinct dates; a requested date missing from the
//     result is a 500 with no partial results.
//  5. evaluate every item against its date's rule-set, results in REQUEST
//     order.
func BatchValidateHandler(load func(ctx context.Context, dates []string) (map[string]RuleSet, error), eng *Engine, now func() time.Time, log *slog.Logger) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
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

		today := now().UTC().Format(time.DateOnly)
		dates := make([]string, len(req.Invoices))
		var distinct []string
		seen := map[string]bool{}
		for i, it := range req.Invoices {
			d := today
			if v, ok := it.Invoice["issue_date"].(string); ok {
				// Year 0 parses in Go but Postgres rejects it at d::date, which would 500 the batch.
				if t, err := time.Parse(time.DateOnly, v); err == nil && t.Year() >= 1 {
					d = v
				}
			}
			dates[i] = d
			if !seen[d] {
				seen[d] = true
				distinct = append(distinct, d)
			}
		}

		// ErrNoActiveRuleSet (and ErrEmptyRuleSet, which wraps it) -> 503 via
		// statusForErr: the gate cannot evaluate, so it refuses -- it never
		// answers a clean 200 it cannot stand behind.
		sets, err := load(r.Context(), distinct)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError || errors.Is(err, ErrCodeListMissing) {
				log.ErrorContext(r.Context(), "validation: batch validate: load rule-set", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		for _, d := range distinct {
			if _, ok := sets[d]; !ok {
				log.ErrorContext(r.Context(), "validation: batch validate: loader omitted a requested date", slog.String("date", d))
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
		}

		results := make([]batchItemResult, 0, len(req.Invoices))
		for i, it := range req.Invoices {
			rs := sets[dates[i]]
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
			results = append(results, batchItemResult{
				Ref: it.Ref, Violations: result.Violations,
				RuleSetVersion: rs.Version, RuleSetVersionID: rs.ID,
			})
		}

		lowest := sets[distinct[0]]
		for _, d := range distinct[1:] {
			if sets[d].Version < lowest.Version {
				lowest = sets[d]
			}
		}
		writeJSON(w, http.StatusOK, batchResponse{
			RuleSetVersion:   lowest.Version,
			RuleSetVersionID: lowest.ID,
			Results:          results,
		})
	}
}

// statusForErr maps a store/engine error to the HTTP status + message the
// handlers write to the response. db.ErrNoTenant is 401 (fail-closed, mirroring
// portfolio.statusForErr); db.ErrNotActiveMember is 403; ErrValidation is 400
// with the wrapped message; ErrNoActiveRuleSet is 503 (the engine has no
// published version to evaluate against); anything else is
// 500 with a generic body -- this helper never leaks internals into the
// response. Logging the unrecognized (500) case via slog is the caller's
// responsibility, since only the caller knows the operation name to log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
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
