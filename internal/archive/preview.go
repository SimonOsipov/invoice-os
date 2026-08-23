package archive

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Preview is what the download would produce, without producing it. Reuses
// manifest.go's own types (D-49) so the two descriptions of one bundle cannot drift
// apart -- manifestCounts already carries the five field names, and manifestEntity.TIN
// is already *string with no omitempty (AC-6 inherited).
type Preview struct {
	Entity    manifestEntity `json:"entity"`
	Period    manifestPeriod `json:"period"`
	Filename  string         `json:"filename"`
	Counts    manifestCounts `json:"counts"`
	OverLimit bool           `json:"over_limit"`
}

// previewOpts carries what Store.Preview knows about the cap into the tx-scoped body,
// mirroring assembleOpts (D-51).
type previewOpts struct {
	maxInvoices int
}

// preview drives selectEntity -> selectInvoiceIDs -> countChildren(ids) over one
// already-open tx (D-37). over_limit is a 200 carrying the real counts, never a
// short-circuit (D-51): the child counts are always computed, cap or no cap.
func preview(ctx context.Context, tx pgx.Tx, r Request, o previewOpts) (Preview, error) {
	if o.maxInvoices <= 0 {
		return Preview{}, fmt.Errorf("archive: maxInvoices must be positive, got %d", o.maxInvoices)
	}

	entity, err := selectEntity(ctx, tx, r.EntityID)
	if err != nil {
		return Preview{}, err
	}

	ids, err := selectInvoiceIDs(ctx, tx, r)
	if err != nil {
		return Preview{}, err
	}

	counts, err := countChildren(ctx, tx, ids)
	if err != nil {
		return Preview{}, err
	}
	counts.Invoices = len(ids)

	return Preview{
		Entity:    bundleEntity(entity),
		Period:    bundlePeriod(r),
		Filename:  bundleFilename(entity.Name, r),
		Counts:    counts,
		OverLimit: len(ids) > o.maxInvoices,
	}, nil
}

// countChildren sums status_transitions/submissions/exchange_attempts/body_files over
// ids, chunked through chunk(ids, 500) so preview only ever runs the parameter regime
// the download already exercises. chunk(nil, 500) is nil, so zero ids runs zero
// statements and every count stays 0 (AC-3 structural, D-47).
func countChildren(ctx context.Context, tx pgx.Tx, ids []string) (manifestCounts, error) {
	var c manifestCounts
	for _, batch := range chunk(ids, 500) {
		var n int
		if err := tx.QueryRow(ctx, countHistorySQL, batch).Scan(&n); err != nil {
			return manifestCounts{}, fmt.Errorf("archive: count invoice_status_history: %w", err)
		}
		c.StatusTransitions += n

		if err := tx.QueryRow(ctx, countSubmissionsSQL, batch).Scan(&n); err != nil {
			return manifestCounts{}, fmt.Errorf("archive: count submission_jobs: %w", err)
		}
		c.Submissions += n

		var attempts, bodyFiles int
		if err := tx.QueryRow(ctx, countExchangeSQL, batch).Scan(&attempts, &bodyFiles); err != nil {
			return manifestCounts{}, fmt.Errorf("archive: count app_exchange: %w", err)
		}
		c.ExchangeAttempts += attempts
		c.BodyFiles += bodyFiles
	}
	return c, nil
}
