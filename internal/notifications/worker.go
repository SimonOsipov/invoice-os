package notifications

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const QueueContacts = "contacts"

// DeliverArgs: Version is the row version the job was queued for ([version-guard]).
type DeliverArgs struct {
	Email       string `json:"email"`
	Destination string `json:"destination"`
	Version     int64  `json:"version"`
}

func (DeliverArgs) Kind() string { return "contact_deliver" }

// InsertOpts omits completed from ByState so a later delivery is never blocked; River requires the other four.
func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: QueueContacts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRetryable,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// DeliverWorker delivers one contact to one destination. Mode is what a delivery records.
type DeliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	Pool    *pgxpool.Pool
	HubSpot HubSpotClient
	Resend  ResendClient
	Mode    Mode
	Logger  *slog.Logger
}

// Work is a compile-only stub (AUTH-17-04 red phase).
func (w *DeliverWorker) Work(context.Context, *river.Job[DeliverArgs]) error { return nil }
