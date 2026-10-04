package notifications

import (
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
