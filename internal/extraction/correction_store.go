// correction_store.go: the append-only record of human field corrections. A field's current
// value is the LATEST row here, never an UPDATE — append-only is enforced by GRANT, not by
// this file. Declarations only until the migration lands.
package extraction

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

var errCorrectionStoreNotImplemented = errors.New("extraction: correction store not implemented")

// Append appends one correction and returns it with its database-assigned id, seq and
// created_at.
func (s *CorrectionStore) Append(ctx context.Context, tenantID, jobID string, c Correction) (Correction, error) {
	return Correction{}, errCorrectionStoreNotImplemented
}

// LatestPerField returns the newest correction per field_name for jobID, ordered by
// field_name. Never nil.
func (s *CorrectionStore) LatestPerField(ctx context.Context, tenantID, jobID string) ([]Correction, error) {
	return []Correction{}, errCorrectionStoreNotImplemented
}

// The tx-taking halves exist so the review handler can write a correction and the invoice
// field it settles inside ONE transaction.
func appendCorrectionTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string, c Correction) (Correction, error) {
	return Correction{}, errCorrectionStoreNotImplemented
}

func latestCorrectionsPerFieldTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]Correction, error) {
	return []Correction{}, errCorrectionStoreNotImplemented
}
