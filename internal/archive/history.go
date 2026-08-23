package archive

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/actor"
)

// historyCSVHeader: pinned by AUDIT-05-04's Stage 1 architecture (D-9). No column
// carries the raw subject separately from actor_name.
var historyCSVHeader = []string{
	"invoice_id", "invoice_number", "seq", "from_status", "to_status",
	"actor_name", "actor_kind", "changed_at",
}

// historyScope: the FROM/WHERE selectHistorySQL and countHistorySQL share (D-47).
const historyScope = `
  FROM invoice_status_history
 WHERE invoice_id = ANY($1::uuid[])`

// selectHistorySQL: invoice_number comes from invoiceNumbers, never a JOIN against
// invoices (see TestHistorySQL_ContainsNoJoinAgainstInvoices). id breaks a
// changed_at tie -- two rows in one transaction can share the same now().
const selectHistorySQL = `
SELECT invoice_id, from_status, to_status, actor, changed_at` +
	historyScope + `
 ORDER BY invoice_id, changed_at, id`

// countHistorySQL backs the preview's status_transitions count (subtask-09).
const countHistorySQL = `SELECT count(*)` + historyScope

// historyRow is one scanned invoice_status_history row, held in memory only long
// enough to resolve its actor before being written out.
type historyRow struct {
	invoiceID    string
	fromStatus   *string
	toStatus     string
	actorSubject string
	changedAt    time.Time
}

// selectHistory streams invoice_status_history for ids as status_history.csv, with
// seq restarting at 1 per invoice.
func selectHistory(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter) error {
	if err := w.Write(historyCSVHeader); err != nil {
		return fmt.Errorf("archive: write status_history.csv header: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	numbers, err := invoiceNumbers(ctx, tx, ids)
	if err != nil {
		return err
	}

	var lastInvoiceID string
	var seq int
	for _, batch := range chunk(ids, 500) {
		rows, err := tx.Query(ctx, selectHistorySQL, batch)
		if err != nil {
			return fmt.Errorf("archive: select invoice_status_history: %w", err)
		}
		var batchRows []historyRow
		var subjects []string
		for rows.Next() {
			var hr historyRow
			if err := rows.Scan(&hr.invoiceID, &hr.fromStatus, &hr.toStatus, &hr.actorSubject, &hr.changedAt); err != nil {
				rows.Close()
				return fmt.Errorf("archive: scan invoice_status_history row: %w", err)
			}
			batchRows = append(batchRows, hr)
			subjects = append(subjects, hr.actorSubject)
		}
		iterErr := rows.Err()
		rows.Close()
		if iterErr != nil {
			return fmt.Errorf("archive: iterate invoice_status_history: %w", iterErr)
		}

		// One Resolve call for the whole chunk (AC-6) -- it dedupes subjects
		// internally, so callers never pre-dedupe.
		labels, err := actor.Resolve(ctx, tx, subjects)
		if err != nil {
			return fmt.Errorf("archive: resolve actors: %w", err)
		}

		for _, hr := range batchRows {
			if hr.invoiceID != lastInvoiceID {
				seq = 0
				lastInvoiceID = hr.invoiceID
			}
			seq++
			label := labels[hr.actorSubject]
			record := []string{
				hr.invoiceID,
				numbers[hr.invoiceID],
				strconv.Itoa(seq),
				emptyIfNil(hr.fromStatus),
				hr.toStatus,
				label.Text,
				string(label.Kind),
				hr.changedAt.UTC().Format(time.RFC3339Nano),
			}
			if err := w.Write(record); err != nil {
				return fmt.Errorf("archive: write status_history.csv row: %w", err)
			}
		}
	}
	return nil
}

// invoiceNumbers looks up id -> invoice_number by primary key, one query, not
// chunked (invoices is the table maxBundleInvoices already bounds).
func invoiceNumbers(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, invoice_number FROM invoices WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("archive: select invoice numbers: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, number string
		if err := rows.Scan(&id, &number); err != nil {
			return nil, fmt.Errorf("archive: scan invoice number: %w", err)
		}
		out[id] = number
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: iterate invoice numbers: %w", err)
	}
	return out, nil
}
