// log.go writes the one line per Ask.
package jev

import (
	"context"
	"log/slog"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// logCall writes no state, question, option, answer, key or body.
func (c *Client) logCall(ctx context.Context, req Request, r result, latency time.Duration) {
	if c.logger == nil {
		return
	}
	attrs := make([]slog.Attr, 0, 7)
	if id, ok := auth.IdentityFromContext(ctx); ok && id.TenantID != "" {
		attrs = append(attrs, slog.String("tenant_id", id.TenantID))
	}
	attrs = append(attrs,
		slog.String("purpose", string(req.Purpose)),
		slog.Int("question_count", len(req.Questions)),
		slog.Int("input_tokens", r.usage.InputTokens),
		slog.Int64("latency_ms", latency.Milliseconds()),
		slog.Int("attempts", r.attempts),
		slog.String("outcome", r.outcome),
	)
	// Background, not ctx: the platform handler adds tenant_id from ctx, which
	// would write the key twice.
	c.logger.LogAttrs(context.Background(), slog.LevelInfo, "jev call", attrs...)
}
