// worker.go: the extraction River worker. tx1 marks the job extracting, the extract runs with
// no transaction held, tx2 writes the results under queue.OncePerJob.
package extraction

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// QueueName is persisted as river_job.queue, so a rename orphans every queued row
// (TestExtractArgs_QueueIsNotRiverDefault). Extraction gets its own pool so a slow read cannot
// starve submission.
const QueueName = "extraction"

// workerActor lands in audit_log.actor for every document.read the worker causes. Non-uuid on
// purpose: memberships.user_id is uuid, so this subject can own no membership row and the
// request seam's suspension gate has nothing to refuse. Precedent: backfillActor.
const workerActor = "extraction-worker"

// extractArgs stays unexported so no package outside this one can enqueue extraction work
// (TestExtractArgsTypeIsUnexported).
type extractArgs struct {
	TenantID       string `json:"tenant_id"`
	DocumentID     string `json:"document_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (extractArgs) Kind() string     { return "extraction_extract" }
func (a extractArgs) Tenant() string { return a.TenantID }

func (extractArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueName, MaxAttempts: 3}
}

// ExtractWorker runs one extraction per River job. Every dependency is an exported field, the
// shape SubmitWorker and PollWorker already use.
type ExtractWorker struct {
	river.WorkerDefaults[extractArgs]
	Pool      *pgxpool.Pool
	Extractor Extractor
	Open      OpenDocument
	Logger    *slog.Logger
}

// Timeout raises River's 60s client default for this kind alone: the executor resolves
// cmp.Or(workUnit.Timeout(), clientJobTimeout), so SubmitWorker and PollWorker keep theirs
// (TestExtractWorker_TimeoutExceedsRiverDefault, TestQueueConfig_HasNoJobTimeoutField).
func (w *ExtractWorker) Timeout(*river.Job[extractArgs]) time.Duration { return 10 * time.Minute }

// AddTo registers ew on the caller's bundle. A River client takes exactly one bundle, so a
// second one would leave its workers unfetched (TestRLS_ExtractAddToRegistersOnTheCallersBundle).
func AddTo(bundle *river.Workers, ew *ExtractWorker) { river.AddWorker(bundle, ew) }

func (w *ExtractWorker) Work(ctx context.Context, job *river.Job[extractArgs]) error {
	args := job.Args

	var row Job
	var terminal bool
	if err := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		j, err := ensureJobTx(ctx, tx, args.TenantID, args.DocumentID,
			w.Extractor.Name(), w.Extractor.Version(), job.ID)
		if err != nil {
			return err
		}
		row = j
		// A job River replays after it already finished must not extract again
		// (TestRLS_ExtractWorkerWritesOneResultSetPerJobOnReplay).
		if j.State == "succeeded" || j.State == "dead_lettered" {
			terminal = true
			return nil
		}
		return advanceJobTx(ctx, tx, args.TenantID, j.ID, "extracting", "", job.Attempt)
	}); err != nil {
		return err
	}
	if terminal {
		return nil
	}

	// The tenant comes from the job's own args: a worker's context belongs to the River client,
	// never to whoever enqueued the work (TestRLS_ExtractWorkerBuildsIdentityFromArgsNotContext).
	octx := auth.WithIdentity(ctx, auth.Identity{
		Subject: workerActor, Role: "authenticated", TenantID: args.TenantID,
	})

	var fields []Field
	doc, err := w.Open(octx, args.DocumentID)
	if err == nil {
		// The extractor is fenced from the database and has no use for a tenant identity.
		fields, err = w.Extractor.Extract(ctx, doc)
	}
	if err != nil {
		state := "failed"
		if job.Attempt >= job.MaxAttempts {
			state = "dead_lettered"
		}
		if txErr := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
			return advanceJobTx(ctx, tx, args.TenantID, row.ID, state, err.Error(), job.Attempt)
		}); txErr != nil {
			return txErr
		}
		return err
	}

	return db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		// The marker, the result rows and the advance to succeeded share one transaction and
		// one fate (TestRLS_ExtractWorkerResultWriteIsGuardedByTheJobMarker).
		_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
			if err := writeFieldResultsTx(ctx, tx, args.TenantID, row.ID, fields); err != nil {
				return err
			}
			return advanceJobTx(ctx, tx, args.TenantID, row.ID, "succeeded", "", job.Attempt)
		})
		return err
	})
}
