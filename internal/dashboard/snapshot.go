package dashboard

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Stubs: the executor replaces every body.
const SnapshotInterval time.Duration = 0

// WATDay is the West Africa Time date of t as YYYY-MM-DD.
func WATDay(t time.Time) string { return "" }

// Snapshotter writes each company's rollup for the current WAT day. Pool must be the invoice_app pool.
type Snapshotter struct {
	Pool    *pgxpool.Pool
	Tenants func(context.Context) ([]string, error)
	Now     func() time.Time // nil = time.Now
	Logger  *slog.Logger     // nil = slog.Default()

	// afterTenant is test-only (nil in production): runs inside the tenant tx after the upserts.
	afterTenant func(tenantID string) error
}

// RunOnce snapshots every tenant. One tenant's failure does not stop the others.
func (s *Snapshotter) RunOnce(ctx context.Context) error { return nil }
