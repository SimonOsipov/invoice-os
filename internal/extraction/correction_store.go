// correction_store.go: the append-only record of human field corrections. A field's current
// value is the LATEST row here, never an UPDATE — append-only is enforced by GRANT, not by
// this file.
package extraction

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// CorrectionMethod is how a field was settled, persisted as
// extraction_field_corrections.method.
type CorrectionMethod string

// The four values the method CHECK admits. MethodUndone re-asserts the extractor's own
// reading, so it carries a value like every other method.
const (
	MethodTyped   CorrectionMethod = "typed"
	MethodChosen  CorrectionMethod = "chosen"
	MethodPointed CorrectionMethod = "pointed"
	MethodUndone  CorrectionMethod = "undone"
)

// Correction is one row of extraction_field_corrections. Seq, not CreatedAt, is the order:
// created_at is transaction-constant, so two corrections written together tie on it.
type Correction struct {
	ID          string
	FieldName   string
	Value       string
	Method      CorrectionMethod
	Region      *Region // non-nil exactly when Method is MethodPointed
	AnchorLabel string
	Actor       string
	Seq         int64
	CreatedAt   time.Time
}

// CorrectionStore holds the invoice_app pool. Every method opens its own tenant-scoped
// transaction, the same posture as Store.
type CorrectionStore struct{ Pool *pgxpool.Pool }

// Append appends one correction and returns it with its database-assigned id, seq and
// created_at.
func (s *CorrectionStore) Append(ctx context.Context, tenantID, jobID string, c Correction) (Correction, error) {
	var out Correction
	err := db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = appendCorrectionTx(ctx, tx, tenantID, jobID, c)
		return err
	})
	return out, err
}

// LatestPerField returns the newest correction per field_name for jobID, ordered by
// field_name. Never nil.
func (s *CorrectionStore) LatestPerField(ctx context.Context, tenantID, jobID string) ([]Correction, error) {
	out := []Correction{}
	err := db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = latestCorrectionsPerFieldTx(ctx, tx, tenantID, jobID)
		return err
	})
	return out, err
}

// The tx-taking halves exist so the review handler can write a correction and the invoice
// field it settles inside ONE transaction.
//
// appendCorrectionTx binds an empty AnchorLabel and a nil Region as SQL NULL, the same
// no-sentinels rule advanceJobTx follows. The CHECK set is the only validation: duplicating
// the method/region pairing in Go would give two places to keep in step.
func appendCorrectionTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string, c Correction) (Correction, error) {
	var page, x0, y0, x1, y1 any
	if c.Region != nil {
		page, x0, y0, x1, y1 = c.Region.Page, c.Region.X0, c.Region.Y0, c.Region.X1, c.Region.Y1
	}
	var anchor any
	if c.AnchorLabel != "" {
		anchor = c.AnchorLabel
	}

	out := c
	if err := tx.QueryRow(ctx,
		`INSERT INTO extraction_field_corrections
		     (tenant_id, extraction_job_id, field_name, value, method, page,
		      bbox_x0, bbox_y0, bbox_x1, bbox_y1, anchor_label, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, seq, created_at`,
		tenantID, jobID, c.FieldName, c.Value, string(c.Method), page,
		x0, y0, x1, y1, anchor, c.Actor).
		Scan(&out.ID, &out.Seq, &out.CreatedAt); err != nil {
		return Correction{}, fmt.Errorf("extraction: append correction to %s for job %s: %w", c.FieldName, jobID, err)
	}
	return out, nil
}

// latestCorrectionsPerFieldTx takes the highest seq per field. DISTINCT ON's ORDER BY
// matches _tenant_job_field_seq_idx column for column, so it skip-scans without a sort.
// Never returns nil: nil marshals to a JSON null where a caller expects an array.
func latestCorrectionsPerFieldTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]Correction, error) {
	out := []Correction{}

	rows, err := tx.Query(ctx,
		`SELECT DISTINCT ON (field_name)
		        id, field_name, value, method, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1,
		        anchor_label, actor, seq, created_at
		   FROM extraction_field_corrections
		  WHERE tenant_id = $1 AND extraction_job_id = $2
		  ORDER BY field_name, seq DESC`,
		tenantID, jobID)
	if err != nil {
		return out, fmt.Errorf("extraction: read corrections for job %s: %w", jobID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			c              Correction
			method         string
			page           *int
			x0, y0, x1, y1 *float64
			anchor         *string
		)
		if err := rows.Scan(&c.ID, &c.FieldName, &c.Value, &method, &page, &x0, &y0, &x1, &y1,
			&anchor, &c.Actor, &c.Seq, &c.CreatedAt); err != nil {
			return []Correction{}, fmt.Errorf("extraction: scan correction for job %s: %w", jobID, err)
		}
		c.Method = CorrectionMethod(method)
		// _region_complete makes the five columns all-or-none, so page decides the box.
		if page != nil {
			c.Region = &Region{Page: *page, X0: *x0, Y0: *y0, X1: *x1, Y1: *y1}
		}
		if anchor != nil {
			c.AnchorLabel = *anchor
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return []Correction{}, fmt.Errorf("extraction: read corrections for job %s: %w", jobID, err)
	}
	return out, nil
}
