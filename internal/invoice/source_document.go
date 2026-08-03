// source_document.go: the read side of invoices.source_document_id/source_rows.
// A sibling of GET /v1/invoices/{id}/history rather than a widening of the
// invoice payload -- invoiceColumns feeds a positional scanInvoice and the MBS
// compliance fingerprint, so neither column may enter it. documents/audit_log
// are read with plain SQL: no import of internal/document, no coupling.
package invoice

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// SourceDocument is the GET /v1/invoices/{id}/source-document body. No
// omitempty anywhere: an explicit null is the contract. SourceRows nil means
// "never recorded", which the previewer must tell apart from an empty range.
type SourceDocument struct {
	InvoiceID  string                `json:"invoice_id"`
	SourceRows []int                 `json:"source_rows"`
	Document   *SourceDocumentRecord `json:"document"`
}

// SourceDocumentRecord is the documents row behind an imported invoice, plus
// the two derived fields the previewer's marker track needs.
type SourceDocumentRecord struct {
	ID                  string    `json:"id"`
	Filename            *string   `json:"filename"`
	DeclaredContentType *string   `json:"declared_content_type"`
	SizeBytes           int64     `json:"size_bytes"`
	ContentHash         string    `json:"content_hash"`
	UploadedAt          time.Time `json:"uploaded_at"`
	// UploadedBy is a bare GoTrue subject uuid: there is no users/profiles
	// table, and memberships carries no name or email.
	UploadedBy       *string `json:"uploaded_by"`
	InvoicesCreated  int     `json:"invoices_created"`
	OtherInvoiceRows []int   `json:"other_invoice_rows"`
}

// SourceDocument returns the document an invoice was imported from, or a
// populated InvoiceID with both other fields nil for a manually created one --
// never ErrNotFound for the absence of a document. RLS scopes all three
// statements, so a cross-tenant id is indistinguishable from an unknown one.
//
// This read writes no audit row: the invoice detail screen fetches it on mount,
// and the byte reads that follow are already audited by document.Store.Get.
func (s *Store) SourceDocument(ctx context.Context, id string) (SourceDocument, error) {
	var out SourceDocument
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// A dedicated narrow projection, never invoiceColumns.
		var documentID *string
		var sourceRows []int
		if err := tx.QueryRow(ctx,
			`SELECT source_document_id::text, source_rows FROM invoices WHERE id = $1`, id,
		).Scan(&documentID, &sourceRows); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		out = SourceDocument{InvoiceID: id, SourceRows: sourceRows}
		if documentID == nil {
			return nil
		}

		var rec SourceDocumentRecord
		if err := tx.QueryRow(ctx,
			`SELECT d.id::text, d.filename, d.declared_content_type, d.size_bytes,
			        d.content_hash, d.created_at,
			        (SELECT count(*) FROM invoices i WHERE i.source_document_id = d.id),
			        (SELECT a.actor FROM audit_log a
			           WHERE a.event = 'document.created' AND a.payload->>'id' = d.id::text
			           ORDER BY a.id ASC LIMIT 1)
			   FROM documents d WHERE d.id = $1`, *documentID,
		).Scan(&rec.ID, &rec.Filename, &rec.DeclaredContentType, &rec.SizeBytes,
			&rec.ContentHash, &rec.UploadedAt, &rec.InvoicesCreated, &rec.UploadedBy,
		); err != nil {
			return err
		}

		// Coerced here, not at the handler: a nil []T without omitempty
		// marshals to JSON null, which the marker track would misread.
		rec.OtherInvoiceRows = []int{}
		rows, err := tx.Query(ctx,
			`SELECT source_rows[1] FROM invoices
			  WHERE source_document_id = $1 AND id <> $2 AND source_rows IS NOT NULL
			  ORDER BY source_rows[1]`, *documentID, id,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			// invoices_source_rows_are_sheet_rows pins cardinality >= 1 and
			// 2 <= ALL, so element 1 exists and is never NULL.
			var first int
			if err := rows.Scan(&first); err != nil {
				return err
			}
			rec.OtherInvoiceRows = append(rec.OtherInvoiceRows, first)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		out.Document = &rec
		return nil
	})
	if err != nil {
		return SourceDocument{}, err
	}
	return out, nil
}
