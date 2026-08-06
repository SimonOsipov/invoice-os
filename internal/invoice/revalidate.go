package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// errRevalidateNotImplemented is Stage 2.5 scaffolding (task-411/BUG-05-02):
// DemoteRevalidatedTx below always returns it so revalidate_test.go's specs
// fail on assertion, never a compile error. Stage 3 replaces this body.
var errRevalidateNotImplemented = errors.New("invoice: DemoteRevalidatedTx not implemented")

// DemoteRevalidatedTx is a Stage 2.5 stub -- see errRevalidateNotImplemented.
func (s *Store) DemoteRevalidatedTx(ctx context.Context, tx pgx.Tx, id, tenantID string, vs []Violation, ruleSetVersionID string) (Invoice, error) {
	return Invoice{}, errRevalidateNotImplemented
}
