// correction_store.go: the append-only record of human field corrections. A field's current
// value is the LATEST row here, never an UPDATE — append-only is enforced by GRANT, not by
// this file.
package extraction

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	// Superseded is the value the correction before this one carried on the same field, nil when
	// this is the field's first. Read-only: the INSERT never names it.
	Superseded *string
}

// Both readers and writers here take a transaction: the review handler writes a correction and
// the invoice field it settles inside ONE transaction, and the detail read composes the
// latest-per-field read into its own request transaction.
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

// latestCorrectionsPerFieldTx takes the highest seq per field, with the value that row
// superseded beside it. One window spec serves both lead() and row_number(), which is what lets
// _tenant_job_field_seq_idx answer the order with no Sort
// (TestExtractionCorrectionStore_LatestPerFieldOrdersFromTheIndexWithoutASort). It names no
// tenant_id: the tenant_isolation policy is the only predicate
// (TestRLS_ExtractionDetailDocumentJoinNamesNoTenantId). Never returns nil.
func latestCorrectionsPerFieldTx(ctx context.Context, tx pgx.Tx, jobID string) ([]Correction, error) {
	out := []Correction{}

	rows, err := tx.Query(ctx,
		`SELECT s.id, s.field_name, s.value, s.superseded, s.method, s.page,
		        s.bbox_x0, s.bbox_y0, s.bbox_x1, s.bbox_y1, s.anchor_label, s.actor,
		        s.seq, s.created_at
		   FROM (SELECT id, field_name, value, method, page, bbox_x0, bbox_y0, bbox_x1,
		                bbox_y1, anchor_label, actor, seq, created_at,
		                lead(value) OVER w  AS superseded,
		                row_number() OVER w AS rn
		           FROM extraction_field_corrections
		          WHERE extraction_job_id = $1
		         WINDOW w AS (PARTITION BY field_name ORDER BY seq DESC)) s
		  WHERE s.rn = 1
		  ORDER BY s.field_name`,
		jobID)
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
		if err := rows.Scan(&c.ID, &c.FieldName, &c.Value, &c.Superseded, &method, &page,
			&x0, &y0, &x1, &y1, &anchor, &c.Actor, &c.Seq, &c.CreatedAt); err != nil {
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
