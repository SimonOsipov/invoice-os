package accountevents

// AUDIT-13-03 red-test stubs: exported names only, zero-value bodies. The executor replaces
// this file with events.go, device.go, mover.go and audit_job.go and deletes it.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// Events lists the six account event names.
var Events []string

// Device is the browser and operating system parsed from a user agent.
type Device struct{ Browser, OS string }

func Parse(ua string) Device { return Device{} }

func (d Device) Label() string { return "" }

func MoveHandoffs(pool *pgxpool.Pool, rc *river.Client[pgx.Tx], log *slog.Logger) func(context.Context) error {
	return func(context.Context) error { return nil }
}

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

type AuditWorker struct {
	river.WorkerDefaults[AuditArgs]
	Pool *pgxpool.Pool
}

func (w *AuditWorker) Work(ctx context.Context, job *river.Job[AuditArgs]) error { return nil }
