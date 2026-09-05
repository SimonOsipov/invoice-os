// store.go: the worker has no request identity, so the tenant is passed, never read from ctx
// (TestExtractionStore_UsesTenantTxNotRequestTx). The tx-taking helpers exist so EXTR-01-09 can
// compose them inside one queue.OncePerJob, whose marker and effect must share a transaction.
package extraction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store holds the invoice_app pool. Every method opens its own tenant-scoped transaction.
type Store struct{ Pool *pgxpool.Pool }

// Job is the part of an extraction_jobs row the worker decides on.
type Job struct {
	ID       string
	State    string
	Attempts int
}

// EnsureJob returns the job for riverJobID, creating it on the first attempt.
func (s *Store) EnsureJob(ctx context.Context, tenantID, documentID, extractor, version string, riverJobID int64) (Job, error) {
	var job Job
	err := db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var err error
		job, err = ensureJobTx(ctx, tx, tenantID, documentID, extractor, version, riverJobID)
		return err
	})
	return job, err
}

// Advance writes a caller-computed state, attempts count and error onto one job row. It takes
// no failure kind: the worker calls advanceJobTx directly, and this wrapper is test-only. The
// "" it passes clears a prior kind (TestExtractionStore_AdvanceClearsFailureKindToNull); no
// test pins the zero-production-callers claim, so re-measure it before relying on it.
func (s *Store) Advance(ctx context.Context, tenantID, jobID, state, lastErr string, attempts int) error {
	return db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		return advanceJobTx(ctx, tx, tenantID, jobID, state, lastErr, attempts, "")
	})
}

// WriteFieldResults appends one row per field at rank 0 (the decided reading) plus one row
// per alternative at ranks 1..N, in slice order.
func (s *Store) WriteFieldResults(ctx context.Context, tenantID, jobID string, fields []FieldResult) error {
	return db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		return writeFieldResultsTx(ctx, tx, tenantID, jobID, fields)
	})
}

// PageImage is one written page: the row extraction_page_images stores.
type PageImage struct {
	Page       int
	WidthPx    int
	HeightPx   int
	StorageKey string
}

// WritePageImages replaces the document's whole page-image inventory.
func (s *Store) WritePageImages(ctx context.Context, tenantID, documentID string, pages []PageImage) error {
	return db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		return writePageImagesTx(ctx, tx, tenantID, documentID, pages)
	})
}

// FieldResults returns the job's stored fields, never a nil slice.
func (s *Store) FieldResults(ctx context.Context, tenantID, jobID string) ([]FieldResult, error) {
	out := []FieldResult{}
	err := db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = fieldResultsTx(ctx, tx, tenantID, jobID)
		return err
	})
	return out, err
}

// ensureJobTx is SELECT-then-INSERT because river_job_id carries no unique index: an
// ON CONFLICT naming it is 42P10, and concurrent callers would each insert. River leases a
// job to one worker at a time, so replay is sequential. The ORDER BY keeps the lookup
// deterministic if a race ever did leave two rows.
func ensureJobTx(ctx context.Context, tx pgx.Tx, tenantID, documentID, extractor, version string, riverJobID int64) (Job, error) {
	var job Job
	err := tx.QueryRow(ctx,
		`SELECT id, state, attempts FROM extraction_jobs
		  WHERE tenant_id = $1 AND river_job_id = $2
		  ORDER BY created_at LIMIT 1 FOR UPDATE`,
		tenantID, riverJobID).Scan(&job.ID, &job.State, &job.Attempts)
	switch {
	case err == nil:
		return job, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Job{}, fmt.Errorf("extraction: look up job for river job %d: %w", riverJobID, err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version, river_job_id)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, state, attempts`,
		tenantID, documentID, extractor, version, riverJobID).
		Scan(&job.ID, &job.State, &job.Attempts); err != nil {
		return Job{}, fmt.Errorf("extraction: insert job for river job %d: %w", riverJobID, err)
	}
	return job, nil
}

// advanceJobTx turns zero rows affected into an error: the row is the caller's own, so a
// no-op means the tenant did not match. An empty lastErr or kind binds SQL NULL, the same
// no-sentinels rule ReasonNone follows, which also means every advance clears a prior error
// and a prior kind (TestExtractionStore_AdvanceClearsFailureKindToNull).
func advanceJobTx(ctx context.Context, tx pgx.Tx, tenantID, jobID, state, lastErr string, attempts int, kind FailureKind) error {
	var lastError any
	if lastErr != "" {
		lastError = lastErr
	}
	var failureKind any
	if kind != "" {
		failureKind = string(kind)
	}

	ct, err := tx.Exec(ctx,
		`UPDATE extraction_jobs SET state = $3, attempts = $4, last_error = $5, failure_kind = $6
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, jobID, state, attempts, lastError, failureKind)
	if err != nil {
		return fmt.Errorf("extraction: advance job %s: %w", jobID, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("extraction: advance job %s: no row affected", jobID)
	}
	return nil
}

// writeLayoutTx records the layout the job's own read observed. The caller computes both
// values, the division of labour advanceJobTx already follows. Zero rows affected is an error:
// the row is the caller's own, so a no-op means the tenant did not match. anchors is bound as a
// string so pgx sends it to the jsonb column as raw JSON, and updated_at is the trigger's
// (stAssertStoreNeverNamesUpdatedAt).
func writeLayoutTx(ctx context.Context, tx pgx.Tx, tenantID, jobID, fingerprint string, anchors []byte) error {
	ct, err := tx.Exec(ctx,
		`UPDATE extraction_jobs SET layout_fingerprint = $3, layout_anchors = $4
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, jobID, fingerprint, string(anchors))
	if err != nil {
		return fmt.Errorf("extraction: write layout for job %s: %w", jobID, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("extraction: write layout for job %s: no row affected", jobID)
	}
	return nil
}

// writeLayoutTokensTx records page-1's token text for a boxless job, in writeLayoutTx's own
// transaction so the identity and the input a rule is derived from share one fate. A nil tokens
// binds SQL NULL through an untyped nil: the empty string cast to jsonb is a syntax error, not
// NULL. Zero rows affected is an error for writeLayoutTx's reason: the row is the caller's own.
func writeLayoutTokensTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string, tokens []byte) error {
	var v any
	if tokens != nil {
		v = string(tokens)
	}
	ct, err := tx.Exec(ctx,
		`UPDATE extraction_jobs SET layout_tokens = $3
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, jobID, v)
	if err != nil {
		return fmt.Errorf("extraction: write layout tokens for job %s: %w", jobID, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("extraction: write layout tokens for job %s: no row affected", jobID)
	}
	return nil
}

// writeFieldResultsTx writes one row per decided field at candidate_rank 0, then one row per
// alternative at ranks 1..N in slice order. It binds ReasonNone and a nil Region as SQL NULL:
// the reason_code CHECK admits four words or NULL, and the all-NULL arm is what
// _region_complete accepts for a field with no box. The CHECK set is the only validation; the
// extractor contract is the first line and duplicating either in Go buys nothing.
func writeFieldResultsTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string, fields []FieldResult) error {
	for _, fr := range fields {
		if err := writeFieldResultRowTx(ctx, tx, tenantID, jobID, fr.Field, 0); err != nil {
			return err
		}
		for i, alt := range fr.Alternatives {
			if err := writeFieldResultRowTx(ctx, tx, tenantID, jobID, alt, i+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFieldResultRowTx writes one candidate_rank row for one field.
func writeFieldResultRowTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string, f Field, rank int) error {
	var reason any
	if f.Reason != ReasonNone {
		reason = string(f.Reason)
	}
	var page, x0, y0, x1, y1 any
	if f.Region != nil {
		page, x0, y0, x1, y1 = f.Region.Page, f.Region.X0, f.Region.Y0, f.Region.X1, f.Region.Y1
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO extraction_field_results
		     (tenant_id, extraction_job_id, field_name, value, page,
		      bbox_x0, bbox_y0, bbox_x1, bbox_y1, reason_code, candidate_rank)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		tenantID, jobID, f.Name, f.Value, page, x0, y0, x1, y1, reason, rank); err != nil {
		return fmt.Errorf("extraction: write field result %s (rank %d) for job %s: %w", f.Name, rank, jobID, err)
	}
	return nil
}

// writePageImagesTx replaces the row set rather than upserting it: the DELETE is a separate
// statement from the INSERT because tuples deleted by this transaction are already dead to
// its own uniqueness check, so a re-render needs no ON CONFLICT and the unique index stays a
// guard that can fire. The policy's USING scopes the DELETE and doubles as the INSERT WITH
// CHECK, so both statements are tenant-bound on top of the explicit predicate.
func writePageImagesTx(ctx context.Context, tx pgx.Tx, tenantID, documentID string, pages []PageImage) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM extraction_page_images WHERE tenant_id = $1 AND document_id = $2`,
		tenantID, documentID); err != nil {
		return fmt.Errorf("extraction: clear page images for document %s: %w", documentID, err)
	}

	for _, p := range pages {
		if _, err := tx.Exec(ctx,
			`INSERT INTO extraction_page_images
			     (tenant_id, document_id, page_number, width_px, height_px, storage_key)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			tenantID, documentID, p.Page, p.WidthPx, p.HeightPx, p.StorageKey); err != nil {
			return fmt.Errorf("extraction: write page image %d for document %s: %w", p.Page, documentID, err)
		}
	}
	return nil
}

// fieldResultsTx groups rows by field_name: rank 0 is the decided reading, ranks 1..N become
// its Alternatives in rank order. Never returns nil: nil marshals to a JSON null where a
// caller expects an array. created_at leads the ORDER BY -- rows from one WriteFieldResults
// call share a transaction timestamp, so it separates writes; (field_name, candidate_rank)
// then orders within one write.
func fieldResultsTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]FieldResult, error) {
	out := []FieldResult{}

	rows, err := tx.Query(ctx,
		`SELECT field_name, value, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1, reason_code, candidate_rank
		   FROM extraction_field_results
		  WHERE tenant_id = $1 AND extraction_job_id = $2
		  ORDER BY created_at, field_name, candidate_rank`,
		tenantID, jobID)
	if err != nil {
		return out, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}
	defer rows.Close()

	indexOf := map[string]int{} // field_name -> position in out, for attaching alternatives
	for rows.Next() {
		var (
			f              Field
			page           *int
			x0, y0, x1, y1 *float64
			reason         *string
			rank           int
		)
		if err := rows.Scan(&f.Name, &f.Value, &page, &x0, &y0, &x1, &y1, &reason, &rank); err != nil {
			return []FieldResult{}, fmt.Errorf("extraction: scan field result for job %s: %w", jobID, err)
		}
		// _region_complete makes the five columns all-or-none, so page decides the box.
		if page != nil {
			f.Region = &Region{Page: *page, X0: *x0, Y0: *y0, X1: *x1, Y1: *y1}
		}
		if reason != nil {
			f.Reason = Reason(*reason)
		}

		if rank == 0 {
			indexOf[f.Name] = len(out)
			out = append(out, FieldResult{Field: f, Alternatives: []Field{}})
			continue
		}
		i, ok := indexOf[f.Name]
		if !ok {
			// A rank>0 row with no rank-0 sibling is a shape Reconcile never produces; refuse it
			// rather than dropping it (loses audit data) or promoting it (fabricates a reading).
			return []FieldResult{}, fmt.Errorf("extraction: field result for job %s: alternative at rank %d for %q has no decided reading", jobID, rank, f.Name)
		}
		out[i].Alternatives = append(out[i].Alternatives, f)
	}
	if err := rows.Err(); err != nil {
		return []FieldResult{}, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}
	return out, nil
}
