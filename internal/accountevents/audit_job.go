package accountevents

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// AuditArgs is one account_audit job; tenant_id is top-level for the job-tenant guard.
type AuditArgs struct {
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	Event      string `json:"event"`
	IP         string `json:"ip"`
	Device     string `json:"device"`
	OccurredAt string `json:"occurred_at"`
}

func (AuditArgs) Kind() string { return "account_audit" }

// AuditWorker writes one audit row in the job's tenant. It suppresses no event.
type AuditWorker struct {
	river.WorkerDefaults[AuditArgs]
	Pool *pgxpool.Pool
}

// AddAuditTo registers w on the worker's one bundle.
func AddAuditTo(bundle *river.Workers, w *AuditWorker) { river.AddWorker(bundle, w) }

type auditPayload struct {
	IP         string `json:"ip_address"`
	Device     string `json:"device"`
	OccurredAt string `json:"occurred_at"`
}

func (w *AuditWorker) Work(ctx context.Context, job *river.Job[AuditArgs]) error {
	a := job.Args
	p := auditPayload{IP: a.IP, Device: a.Device, OccurredAt: a.OccurredAt}
	if p.IP == "" {
		p.IP = "Unknown IP"
	}
	if p.Device == "" {
		p.Device = unknownDevice
	}
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		// audit_log has no FK to tenants; RLS shows this tenant's own row only while it exists.
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1`, a.TenantID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return river.JobCancel(fmt.Errorf("accountevents: tenant %s does not exist", a.TenantID))
			}
			return fmt.Errorf("accountevents: read tenant: %w", err)
		}
		return audit.Record(ctx, tx, a.UserID, a.Event, p)
	})
}
