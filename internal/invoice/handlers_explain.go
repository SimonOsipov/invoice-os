package invoice

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// ExplainAI is the slice of *ai.Client Explain uses; nil is off.
type ExplainAI interface {
	Enabled() bool
	Call(ctx context.Context, req ai.Request) (map[string]any, error)
}

// ViolationKey names one stored violation.
type ViolationKey struct {
	RuleKey, Path string
}

// Explainer answers Explain from the stored verdict. Compile-only stub (ENGI-17-03 Test-Spec).
type Explainer struct{}

func NewExplainer(get func(ctx context.Context, id string) (Invoice, error), a ExplainAI) *Explainer {
	return &Explainer{}
}

func (e *Explainer) Explain(ctx context.Context, id string, key ViolationKey) (ExplainResult, error) {
	return ExplainResult{}, nil
}

// ExplainHandler serves POST /v1/invoices/{id}/explain. Compile-only stub (ENGI-17-03 Test-Spec).
func ExplainHandler(explain func(ctx context.Context, id string, key ViolationKey) (ExplainResult, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}
}
