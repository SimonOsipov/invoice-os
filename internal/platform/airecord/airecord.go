// Package airecord carries one AI call's usage record from a client to its sink.
package airecord

import (
	"context"
	"log/slog"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Call is one AI call's usage. It holds no prompt, answer or document text.
type Call struct {
	TenantID     string
	Client       string // "ai" | "jev"
	Purpose      string
	Model        string
	InputTokens  int
	OutputTokens int
	Cost         float64
	LatencyMS    int64
	Attempts     int
	Outcome      string
}

// Sink receives one Call. It returns at once and never affects the AI call.
type Sink func(Call)

// Record hands c to sink under the caller's tenant. A call that sent nothing is not recorded.
// Negative vendor usage becomes 0, so a CHECK never drops the row.
func Record(ctx context.Context, sink Sink, logger *slog.Logger, c Call) {
	if sink == nil || c.Attempts == 0 {
		return
	}
	id, ok := auth.IdentityFromContext(ctx)
	if !ok || id.TenantID == "" {
		if logger != nil {
			// WARN, not ERROR: ERROR reaches Sentry, one event per call.
			logger.LogAttrs(context.Background(), slog.LevelWarn, "ai call not recorded: no tenant",
				slog.String("client", c.Client), slog.String("purpose", c.Purpose))
		}
		return
	}
	c.TenantID = id.TenantID
	c.InputTokens = max(c.InputTokens, 0)
	c.OutputTokens = max(c.OutputTokens, 0)
	c.Cost = max(c.Cost, 0)
	sink(c)
}
