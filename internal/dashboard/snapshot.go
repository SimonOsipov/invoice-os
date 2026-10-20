package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// SnapshotInterval keeps the last run of a WAT day within an hour of midnight.
const SnapshotInterval = time.Hour

var wat = time.FixedZone("WAT", 3600) // no DST in West Africa Time

// WATDay is the West Africa Time date of t as YYYY-MM-DD.
func WATDay(t time.Time) string { return t.In(wat).Format(time.DateOnly) }

// Snapshotter writes each company's rollup for the current WAT day. Pool must be the invoice_app pool.
type Snapshotter struct {
	Pool    *pgxpool.Pool
	Tenants func(context.Context) ([]string, error)
	Now     func() time.Time // nil = time.Now
	Logger  *slog.Logger     // nil = slog.Default()

	// afterTenant is test-only (nil in production): runs inside the tenant tx after the upserts.
	afterTenant func(tenantID string) error
}

// RunOnce snapshots every tenant. One tenant's failure does not stop the others; a done ctx does.
func (s *Snapshotter) RunOnce(ctx context.Context) error {
	tenants, err := s.Tenants(ctx)
	if err != nil {
		return fmt.Errorf("dashboard: enumerate tenants: %w", err)
	}
	now, log := s.Now, s.Logger
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	day := WATDay(now())

	var failures []error
	for _, id := range tenants {
		if ctx.Err() != nil {
			break
		}
		if err := db.WithinTenantTx(ctx, s.Pool, id, func(tx pgx.Tx) error {
			return s.snapshotTenant(ctx, tx, id, day)
		}); err != nil {
			if ctx.Err() != nil { // shutdown, not a tenant fault
				break
			}
			log.ErrorContext(ctx, "dashboard: tenant snapshot failed", "tenant_id", id, "error", err)
			failures = append(failures, fmt.Errorf("dashboard: snapshot tenant %s: %w", id, err))
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, fmt.Errorf("dashboard: snapshot interrupted: %w", err))
	}
	return errors.Join(failures...)
}

// ceiling: one statement per company, batch above about 1k companies per tenant.
func (s *Snapshotter) snapshotTenant(ctx context.Context, tx pgx.Tx, tenantID, day string) error {
	clients, err := queryClients(ctx, tx)
	if err != nil {
		return err
	}
	for _, c := range clients {
		r := c.Metrics[MetricReadiness]
		if _, err := tx.Exec(ctx,
			`INSERT INTO rollup_snapshots (tenant_id, entity_id, day, total, readiness_num, needs_attention, not_submitted, vat_kobo)
			 VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8)
			 ON CONFLICT (tenant_id, entity_id, day) DO UPDATE SET
			     total = EXCLUDED.total, readiness_num = EXCLUDED.readiness_num, needs_attention = EXCLUDED.needs_attention,
			     not_submitted = EXCLUDED.not_submitted, vat_kobo = EXCLUDED.vat_kobo`,
			tenantID, c.EntityID, day, r.Den, r.Num, c.NeedsAttention,
			c.Counts.Draft+c.Counts.Validated, c.Metrics[MetricVATTracked].Num,
		); err != nil {
			return err
		}
	}
	if s.afterTenant != nil {
		return s.afterTenant(tenantID)
	}
	return nil
}
