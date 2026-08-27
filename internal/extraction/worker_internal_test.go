// worker_internal_test.go: the worker specs that need no database. Package extraction so they
// can name the unexported args type; everything needing a pool is in worker_db_test.go.
package extraction

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// The kind is persisted as river_job.kind, so a rename orphans every already-queued row.
func TestExtractArgs_KindIsStable(t *testing.T) {
	if got, want := (extractArgs{}).Kind(), "extraction_extract"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
}

func TestExtractArgs_InsertOptsPinQueueAndAttempts(t *testing.T) {
	opts := (extractArgs{}).InsertOpts()
	if opts.Queue != QueueName {
		t.Errorf("InsertOpts().Queue = %q, want QueueName (%q)", opts.Queue, QueueName)
	}
	if opts.MaxAttempts != 3 {
		t.Errorf("InsertOpts().MaxAttempts = %d, want 3", opts.MaxAttempts)
	}
}

// QueueName is persisted as river_job.queue, so it is pinned to its literal exactly as Kind is:
// TestExtractArgs_InsertOptsPinQueueAndAttempts compares the opts against QueueName and is
// therefore blind to QueueName itself being wrong.
func TestExtractArgs_QueueIsNotRiverDefault(t *testing.T) {
	if got, want := QueueName, "extraction"; got != want {
		t.Errorf("QueueName = %q, want %q", got, want)
	}
	// Not implied by the literal above: a River release that renamed its default queue onto
	// this name would silently put extraction work back on the submission pool.
	if river.QueueDefault == QueueName {
		t.Errorf("river.QueueDefault is now %q, the same queue extraction claims; the extraction pool is no longer isolated", river.QueueDefault)
	}
}

// River resolves cmp.Or(workUnit.Timeout(), clientJobTimeout), so a per-worker Timeout wins
// over the client default without raising it for SubmitWorker and PollWorker too.
func TestExtractWorker_TimeoutExceedsRiverDefault(t *testing.T) {
	got := (&ExtractWorker{}).Timeout(nil)
	if want := 10 * time.Minute; got != want {
		t.Errorf("Timeout() = %v, want %v", got, want)
	}
	if got <= river.JobTimeoutDefault {
		t.Errorf("Timeout() = %v, want > river.JobTimeoutDefault (%v)", got, river.JobTimeoutDefault)
	}
}
