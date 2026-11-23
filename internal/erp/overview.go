package erp

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// SourceDoc is one pulled document as the connector detail shows it.
type SourceDoc struct {
	ID                      string     `json:"id"`
	SourceDocID             string     `json:"source_doc_id"`
	SourceRevision          string     `json:"source_revision"`
	State                   string     `json:"state"`
	Failure                 *string    `json:"failure"`
	InvoiceID               *string    `json:"invoice_id"`
	InvoiceNumber           *string    `json:"invoice_number"`
	DiffersFromERP          bool       `json:"differs_from_erp"`
	ChangedAfterClearanceAt *time.Time `json:"changed_after_clearance_at"`
	DeletedInERPAt          *time.Time `json:"deleted_in_erp_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// HeldDoc is a document that needs a person, with why.
type HeldDoc struct {
	SourceDoc
	Reasons []string `json:"reasons"`
}

// Writeback is one attempt to write a cleared invoice's result back to the ERP.
type Writeback struct {
	ID            string    `json:"id"`
	InvoiceID     string    `json:"invoice_id"`
	InvoiceNumber *string   `json:"invoice_number"`
	Outcome       string    `json:"outcome"`
	State         string    `json:"state"`
	Reason        *string   `json:"reason"`
	Attempt       int       `json:"attempt"`
	CreatedAt     time.Time `json:"created_at"`
}

// Activity is what the connection pulled over the last day.
type Activity struct {
	Since     time.Time      `json:"since"`
	Counts    map[string]int `json:"counts"`
	Documents []SourceDoc    `json:"documents"`
}

// Overview is everything the connector detail reads about one connection.
type Overview struct {
	Connection Connection  `json:"connection"`
	Activity   Activity    `json:"activity"`
	Writebacks []Writeback `json:"writebacks"`
	Held       []HeldDoc   `json:"held"`
}

const (
	overviewDocs       = 50
	overviewWritebacks = 50
	overviewHeld       = 100
)

const ovDocCols = `d.id::text, d.source_doc_id, d.source_revision, d.state, d.failure, d.invoice_id::text, i.invoice_number,
       d.differs_from_erp, d.changed_after_clearance_at, d.deleted_in_erp_at, d.updated_at`

func scanDoc(row pgx.Rows, d *SourceDoc) error {
	return row.Scan(&d.ID, &d.SourceDocID, &d.SourceRevision, &d.State, &d.Failure, &d.InvoiceID, &d.InvoiceNumber,
		&d.DiffersFromERP, &d.ChangedAfterClearanceAt, &d.DeletedInERPAt, &d.UpdatedAt)
}

// heldReasons lists why a document is held, in the fixed order the screen reads.
func heldReasons(d SourceDoc) []string {
	r := []string{}
	if d.DiffersFromERP {
		r = append(r, "differs_from_erp")
	}
	if d.ChangedAfterClearanceAt != nil {
		r = append(r, "changed_after_clearance")
	}
	if d.DeletedInERPAt != nil {
		r = append(r, "deleted_in_erp")
	}
	if d.State == "held" {
		r = append(r, "number_held")
	}
	if d.State == "failed" {
		r = append(r, "failed")
	}
	return r
}

// Overview reads a connection's activity, write-backs and held documents; read-only, no role check, 404 outside the client.
func (s *Store) Overview(ctx context.Context, entityID, connectionID string) (Overview, error) {
	ov := Overview{
		Activity:   Activity{Counts: map[string]int{}, Documents: []SourceDoc{}},
		Writebacks: []Writeback{},
		Held:       []HeldDoc{},
	}
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		if _, err := uuid.Parse(connectionID); err != nil {
			return ErrNoSuchRecord
		}
		if err := scanConnection(tx.QueryRow(ctx,
			`SELECT `+connectionCols+` FROM erp_connections WHERE id = $2 AND entity_id = $1`, entityID, connectionID), &ov.Connection); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoSuchRecord
			}
			return err
		}

		if err := tx.QueryRow(ctx, `SELECT now() - interval '24 hours'`).Scan(&ov.Activity.Since); err != nil {
			return err
		}
		for _, st := range []string{"pulled", "pending", "drafted", "linked", "held", "failed", "deleted"} {
			ov.Activity.Counts[st] = 0
		}
		rows, err := tx.Query(ctx, `SELECT state, count(*) FROM erp_source_documents WHERE connection_id = $1 AND updated_at >= $2 GROUP BY state`,
			connectionID, ov.Activity.Since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st string
			var n int
			if err := rows.Scan(&st, &n); err != nil {
				rows.Close()
				return err
			}
			ov.Activity.Counts[st] = n
			ov.Activity.Counts["pulled"] += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// ceiling: no index orders a connection's documents by updated_at; add (connection_id, updated_at) above ~10k documents per connection.
		rows, err = tx.Query(ctx, `SELECT `+ovDocCols+` FROM erp_source_documents d LEFT JOIN invoices i ON i.id = d.invoice_id
WHERE d.connection_id = $1 ORDER BY d.updated_at DESC, d.id LIMIT $2`, connectionID, overviewDocs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d SourceDoc
			if err := scanDoc(rows, &d); err != nil {
				rows.Close()
				return err
			}
			ov.Activity.Documents = append(ov.Activity.Documents, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT w.id::text, w.invoice_id::text, i.invoice_number, w.outcome, w.state, w.reason, w.attempt, w.created_at
FROM erp_writebacks w LEFT JOIN invoices i ON i.id = w.invoice_id
WHERE w.connection_id = $1 ORDER BY w.created_at DESC, w.id DESC LIMIT $2`, connectionID, overviewWritebacks)
		if err != nil {
			return err
		}
		for rows.Next() {
			var w Writeback
			if err := rows.Scan(&w.ID, &w.InvoiceID, &w.InvoiceNumber, &w.Outcome, &w.State, &w.Reason, &w.Attempt, &w.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			ov.Writebacks = append(ov.Writebacks, w)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT `+ovDocCols+` FROM erp_source_documents d LEFT JOIN invoices i ON i.id = d.invoice_id
WHERE d.connection_id = $1 AND (d.differs_from_erp OR d.changed_after_clearance_at IS NOT NULL OR d.deleted_in_erp_at IS NOT NULL
      OR d.state IN ('held', 'failed'))
ORDER BY d.updated_at DESC, d.id LIMIT $2`, connectionID, overviewHeld)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HeldDoc
			if err := scanDoc(rows, &h.SourceDoc); err != nil {
				return err
			}
			h.Reasons = heldReasons(h.SourceDoc)
			ov.Held = append(ov.Held, h)
		}
		return rows.Err()
	})
	if err != nil {
		return Overview{}, err
	}
	return ov, nil
}

// Marks lists the marks of the invoice's linked documents; read-only, no role check, 404 outside the client.
func (s *Store) Marks(ctx context.Context, entityID, invoiceID string) ([]DocumentMark, error) {
	var marks []DocumentMark
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		if _, err := uuid.Parse(invoiceID); err != nil {
			return ErrNoSuchRecord
		}
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM invoices WHERE id = $2 AND entity_id = $1`, entityID, invoiceID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoSuchRecord
			}
			return err
		}
		var err error
		marks, err = InvoiceMarks(ctx, tx, invoiceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return marks, nil
}
