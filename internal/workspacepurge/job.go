package workspacepurge

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/workspacepurge/purgeargs"
)

// PurgeWorker purges the tenant a job names.
type PurgeWorker struct {
	river.WorkerDefaults[purgeargs.Args]
	Purger *Purger
}

// Stub for the red tests: DEL-09-03 implements Work and AddPurgeTo.
func (w *PurgeWorker) Work(context.Context, *river.Job[purgeargs.Args]) error { return nil }

func AddPurgeTo(*river.Workers, *PurgeWorker) {}
