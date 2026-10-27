package workspacepurge

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/workspacepurge/purgeargs"
)

const purgeTimeout = 10 * time.Minute

// PurgeWorker purges the tenant a job names. Purger is set by the wiring once it is built.
type PurgeWorker struct {
	river.WorkerDefaults[purgeargs.Args]
	Purger *Purger
}

func (w *PurgeWorker) Timeout(*river.Job[purgeargs.Args]) time.Duration { return purgeTimeout }

// Work purges the tenant; a tenant that is not due or started, or that another runner is purging, is a no-op.
func (w *PurgeWorker) Work(ctx context.Context, job *river.Job[purgeargs.Args]) error {
	if _, err := uuid.Parse(job.Args.TenantID); err != nil {
		return river.JobCancel(err)
	}
	log := w.Purger.Logger
	if log == nil {
		log = slog.Default()
	}
	return w.Purger.purgeLogged(ctx, log, job.Args.TenantID)
}

func AddPurgeTo(bundle *river.Workers, w *PurgeWorker) { river.AddWorker(bundle, w) }
