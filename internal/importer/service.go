// M4-03-04 (task-105): the importer's orchestration surface — map -> normalize
// -> group -> classify -> (dry-run classify-only | real CreateBatch/Create/
// Finalize). THIS FILE IS A STUB, authored RED-first per M4-03-04's
// test-first convention: Service.Import always returns the zero BatchResult
// and a nil error, so every behavioral assertion in service_test.go
// (IMP-SVC-01..16) fails on ASSERTION (wrong count / missing persisted row /
// unexpected nil error), never on a compile or panic. The real algorithm is
// Stage 3 (executor), following the Implementation Plan carried on
// backlog task-105.
package importer

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// BatchResult is Import's return shape, whether dry-run or real. For a
// dry-run, ID/Status stay "" (no import_batches row is ever written).
type BatchResult struct {
	ID                  string
	Status              string
	RowsTotal           int
	RowsValid           int
	RowsInvalid         int
	ReadyInvoices       int
	QuarantinedInvoices int
	Errors              []RowError
}

// Service orchestrates decode-output (a header + data rows, already produced
// by Decode) into invoice drafts, holding both the importer Store
// (import_batches) and the invoice Store (invoices/line_items) it writes
// through.
type Service struct {
	batch *Store
	inv   *invoice.Store
}

// NewService wraps the two stores the orchestration needs. The caller owns
// both stores' pool lifecycles.
func NewService(batch *Store, inv *invoice.Store) *Service {
	return &Service{batch: batch, inv: inv}
}

// Import is implemented in Stage 3 (executor). Stub returns the zero
// BatchResult and a nil error so every IMP-SVC-01..16 behavioral assertion
// fails red-for-the-right-reason against this stub.
func (s *Service) Import(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
	return BatchResult{}, nil
}
