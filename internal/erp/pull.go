package erp

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// PullInterval is how often the sweep enqueues a pull per active connection.
const PullInterval = time.Minute

// PullArgs is one connection's pull. Stub: the executor replaces this file whole.
type PullArgs struct {
	TenantID     string `json:"tenant_id"`
	ConnectionID string `json:"connection_id"`
}

func (PullArgs) Kind() string { return "erp.pull" }

func (a PullArgs) Tenant() string { return a.TenantID }

func (PullArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

type PullWorker struct {
	river.WorkerDefaults[PullArgs]
	Pool     *pgxpool.Pool
	Queue    *queue.Client
	Registry Registry
	Logger   *slog.Logger
}

func (w *PullWorker) Work(context.Context, *river.Job[PullArgs]) error { return nil }

// Sweeper enqueues one pull per active connection of a registered system.
type Sweeper struct {
	Reader, Pool *pgxpool.Pool
	River        *river.Client[pgx.Tx]
	Registry     Registry
	Logger       *slog.Logger
}

func (s *Sweeper) SweepOnce(context.Context) error { return nil }
