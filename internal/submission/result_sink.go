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

// reachResult runs the sink in a savepoint, so its error cannot undo the result or re-submit it.
// A nil sink is a no-op.
// ceiling: a sink error loses that one write-back; one Sentry issue names the job.
func reachResult(ctx context.Context, tx pgx.Tx, sink ResultSink, logger *slog.Logger, job *rivertype.JobRow, tenantID, invoiceID string) error {
	if sink == nil {
		return nil
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
		logger.Error("submission: result sink failed; result kept",
			"job_id", job.ID, "invoice_id", invoiceID, "error", sinkErr)
	}
	queue.ReportJobError(job, sinkErr)
	return nil
}
