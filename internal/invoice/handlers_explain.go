package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const maxExplainBodyBytes = 4 << 10

// ExplainAI is the slice of *ai.Client Explain uses; nil is off.
type ExplainAI interface {
	Enabled() bool
	Call(ctx context.Context, req ai.Request) (map[string]any, error)
}

// ViolationKey names one stored violation.
type ViolationKey struct {
	RuleKey, Path string
}

// Explainer answers Explain from the stored verdict.
type Explainer struct {
	get func(ctx context.Context, id string) (Invoice, error)
	ai  ExplainAI
}

func NewExplainer(get func(ctx context.Context, id string) (Invoice, error), a ExplainAI) *Explainer {
	return &Explainer{get: get, ai: a}
}

// Explain reads the invoice in the tenant transaction, which has returned before the AI call starts.
// Any AI failure answers unavailable, never an error.
func (e *Explainer) Explain(ctx context.Context, id string, key ViolationKey) (ExplainResult, error) {
	inv, err := e.get(ctx, id)
	if err != nil {
		return ExplainResult{}, err
	}
	var stored []Violation
	if err := json.Unmarshal(inv.Violations, &stored); err != nil {
		return ExplainResult{}, fmt.Errorf("invoice: explain: stored violations: %w", err)
	}
	var v *Violation
	for i := range stored {
		if stored[i].RuleKey == key.RuleKey && stored[i].Path == key.Path {
			v = &stored[i]
			break
		}
	}
	if v == nil {
		return ExplainResult{}, ErrViolationGone
	}
	if e.ai == nil || !e.ai.Enabled() {
		return explainUnavailable(), nil
	}
	payload := MBSPayload(inv)
	ans, err := e.ai.Call(ctx, ai.Request{
		Purpose:    ai.PurposeExplain,
		System:     explainSystem,
		Text:       explainPromptText(*v, payload),
		FakeScope:  "EXPLAIN",
		SchemaName: explainSchemaName,
		Schema:     explainSchema,
	})
	if err != nil {
		return explainUnavailable(), nil
	}
	return guardExplanation(*v, payload, ans), nil
}

type explainRequest struct {
	RuleKey string `json:"rule_key"`
	Path    string `json:"path"`
}

// ExplainHandler serves POST /v1/invoices/{id}/explain.
// ceiling: one paid call per request, no cache or rate limit; add both when spend shows in the ai call cost log
func ExplainHandler(explain func(ctx context.Context, id string, key ViolationKey) (ExplainResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxExplainBodyBytes)
		var req explainRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.RuleKey == "" {
			writeError(w, http.StatusBadRequest, "rule_key is required")
			return
		}
		if len(req.RuleKey) > maxFilterTextLen || len(req.Path) > maxFilterTextLen {
			writeError(w, http.StatusBadRequest, "rule_key and path must be at most 200 bytes")
			return
		}
		res, err := explain(r.Context(), r.PathValue("id"), ViolationKey{RuleKey: req.RuleKey, Path: req.Path})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: explain", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
