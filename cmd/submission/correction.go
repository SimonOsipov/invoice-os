// correction.go: the two adapters the correction route is built over -- the audit writer that
// spells the event here rather than in internal/extraction, and the applier that projects
// internal/invoice's domain errors onto the extraction sentinels. Declarations only until the
// route lands.
package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// invoiceFieldEdit is (*invoice.Store).EditBySourceDocumentTx's shape, so a test can substitute
// one -- the documentOpen idiom.
type invoiceFieldEdit func(ctx context.Context, tx pgx.Tx, documentID string, in invoice.EditInput) (invoice.Invoice, error)

// newFieldCorrectedAuditor adapts the audit module to the correction seam.
func newFieldCorrectedAuditor() extraction.RecordFieldCorrected {
	return func(context.Context, pgx.Tx, string, extraction.FieldCorrection) error {
		return errors.New("submission: field-corrected auditor not implemented")
	}
}

// newInvoiceFieldApplier adapts the invoice store to the extraction seam.
func newInvoiceFieldApplier(edit invoiceFieldEdit) extraction.ApplyFieldToInvoice {
	return func(context.Context, pgx.Tx, string, string, string) (string, error) {
		return "", errors.New("submission: invoice field applier not implemented")
	}
}
