package erp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// txEnqueuer is (*queue.Client).EnqueueTx, so a test can make the enqueue fail.
type txEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, tenantID, key string, args river.JobArgs, opts *river.InsertOpts) (skipped bool, err error)
}

// WriteBackEnqueuer is the submission result sink: it enqueues erp.writeback on the caller's
// transaction for an invoice linked to an ERP document. Queue is backfilled once queue.New returns.
type WriteBackEnqueuer struct{ Queue txEnqueuer }

const reasonRunes = 500

var failureReasons = map[string]string{
	"payload_not_built":       "the invoice could not be built for NRS",
	"never_acknowledged":      "NRS did not acknowledge the submission",
	"acknowledged_no_verdict": "NRS acknowledged the submission but gave no verdict",
}

// ResultReached runs on tx and opens none.
func (e *WriteBackEnqueuer) ResultReached(ctx context.Context, tx pgx.Tx, tenantID, invoiceID string) error {
	var linked bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM erp_source_documents WHERE invoice_id = $1 AND state IN ('drafted', 'linked'))`,
		invoiceID).Scan(&linked); err != nil {
		return fmt.Errorf("erp: write-back linked documents: %w", err)
	}
	if !linked {
		return nil
	}
	a := WriteBackArgs{TenantID: tenantID, InvoiceID: invoiceID}
	var (
		reasons []byte
		kind    string
	)
	if err := tx.QueryRow(ctx,
		`SELECT invoice_number, status, coalesce(irn, ''), rejection_reasons, coalesce(failure_kind, ''),
		        (SELECT count(*) FROM invoice_status_history
		          WHERE invoice_id = invoices.id AND to_status IN ('accepted', 'rejected', 'failed'))
		   FROM invoices WHERE id = $1`, invoiceID).
		Scan(&a.InvoiceNumber, &a.Outcome, &a.IRN, &reasons, &kind, &a.ResultVersion); err != nil {
		return fmt.Errorf("erp: write-back invoice result: %w", err)
	}
	switch a.Outcome {
	case "accepted":
	case "rejected":
		a.IRN = ""
		var rs []struct{ Code, Message string }
		if err := json.Unmarshal(reasons, &rs); err != nil {
			return fmt.Errorf("erp: write-back rejection reasons: %w", err)
		}
		parts := make([]string, len(rs))
		for i, r := range rs {
			parts[i] = r.Code + ": " + r.Message
		}
		a.Reason = strings.Join(parts, "; ")
		if a.Reason == "" {
			a.Reason = "no reason given"
		}
	case "failed":
		a.IRN = ""
		a.Reason = failureReasons[kind]
	default:
		return nil
	}
	if r := []rune(a.Reason); len(r) > reasonRunes {
		a.Reason = string(r[:reasonRunes])
	}
	if _, err := e.Queue.EnqueueTx(ctx, tx, tenantID, WriteBackKey(invoiceID, a.ResultVersion), a, nil); err != nil {
		return fmt.Errorf("erp: enqueue write-back: %w", err)
	}
	return nil
}
