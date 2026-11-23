package submission

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// ResultSink is told, inside the terminal transaction, that an invoice reached an NRS result.
type ResultSink interface {
	ResultReached(ctx context.Context, tx pgx.Tx, tenantID, invoiceID string) error
}

// reachResult calls the sink on the closure's tx; a nil sink is a no-op.
// Before the final attempt its error rolls the closure back and the job retries. On the final
// attempt the sink runs in a savepoint, so its error cannot strand the invoice: the result commits
// and one Sentry issue names the job.
// ceiling: that one result is never written back.
func reachResult(ctx context.Context, tx pgx.Tx, sink ResultSink, logger *slog.Logger, job *rivertype.JobRow, tenantID, invoiceID string) error {
	if sink == nil {
		return nil
	}
	if job.Attempt < job.MaxAttempts {
		return sink.ResultReached(ctx, tx, tenantID, invoiceID)
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	sinkErr := sink.ResultReached(ctx, sp, tenantID, invoiceID)
	if sinkErr == nil {
		return sp.Commit(ctx)
	}
	if err := sp.Rollback(ctx); err != nil {
		return err
	}
	if logger != nil {
		logger.Error("submission: result sink failed on the final attempt; result kept",
			"job_id", job.ID, "invoice_id", invoiceID, "error", sinkErr)
	}
	queue.ReportJobError(job, sinkErr)
	return nil
}
