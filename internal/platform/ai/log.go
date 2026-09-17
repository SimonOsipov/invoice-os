// log.go: the one log line per call.
package ai

import (
	"context"
	"log/slog"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// logCall writes the one line per call (AIR-02-03). No prompt, hint, answer,
// schema name or error text: Q16 is logs only, and none of those belong in one.
func (c *Client) logCall(ctx context.Context, req Request, r result, latency time.Duration) {
	if c.logger == nil {
		return
	}
	attrs := make([]slog.Attr, 0, 9)
	if id, ok := auth.IdentityFromContext(ctx); ok && id.TenantID != "" {
		attrs = append(attrs, slog.String("tenant_id", id.TenantID))
	}
	attrs = append(attrs,
		slog.String("model", Model),
		slog.String("purpose", string(req.Purpose)),
		slog.Int("input_tokens", r.usage.PromptTokens),
		slog.Int("output_tokens", r.usage.CompletionTokens),
		slog.Float64("cost", r.usage.Cost),
		slog.Int64("latency_ms", latency.Milliseconds()),
		slog.Int("attempts", r.attempts),
		slog.String("outcome", r.outcome),
	)
	// Background, not ctx: the platform handler adds tenant_id from a context
	// value the HTTP middleware sets, which would write the key twice.
	c.logger.LogAttrs(context.Background(), slog.LevelInfo, "ai call", attrs...)
}
