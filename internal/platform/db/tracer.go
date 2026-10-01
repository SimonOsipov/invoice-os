package db

import (
	"context"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"
)

type spanKey struct{}

// queryTracer records a db.sql.query child span per statement, only under an
// existing parent span, so River's polls (no span in context) record nothing.
// It never records arguments or error text: both can carry customer data.
//
// ceiling: Query/QueryRow/Exec only; batches (the membership gate), CopyFrom and Prepare are untraced — add BatchTracer if a slow request hides in one
// ceiling: one span per statement; sample or drop db spans above ~2.5M spans/month on Sentry Stats
type queryTracer struct{}

var _ pgx.QueryTracer = queryTracer{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	parent := sentry.SpanFromContext(ctx)
	if parent == nil {
		return ctx
	}
	span := parent.StartChild("db.sql.query")
	span.Description = data.SQL
	span.SetData("db.system", "postgresql")
	return context.WithValue(ctx, spanKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(spanKey{}).(*sentry.Span)
	if !ok {
		return
	}
	if data.Err != nil {
		span.Status = sentry.SpanStatusInternalError
	}
	span.Finish()
}
