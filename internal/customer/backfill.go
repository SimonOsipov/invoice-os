package customer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrBackfillPrivilegedRole refuses a SUPERUSER/BYPASSRLS connection: RLS would be inert.
var ErrBackfillPrivilegedRole = errors.New("customer: refusing to backfill over a SUPERUSER/BYPASSRLS role; run as invoice_app")

type SkippedInvoice struct{ InvoiceNumber, TIN string }

type Collision struct {
	TIN      string
	Names    []string
	Chosen   string
	Existing bool
}

type BackfillResult struct {
	InvoicesRead int
	TINs         int
	Created      int
	Existing     int
	NoTIN        int
	Skipped      []SkippedInvoice
	Collisions   []Collision
}

// Backfill is a compile stub until CUST-04-01 is implemented.
func Backfill(ctx context.Context, pool *pgxpool.Pool, tenantID string, dryRun bool) (BackfillResult, error) {
	return BackfillResult{}, errors.New("not implemented")
}
