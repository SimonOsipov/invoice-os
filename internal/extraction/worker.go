// worker.go: the extraction River worker. tx1 marks the job extracting, the extract runs with
// no transaction held, tx2 writes the results under queue.OncePerJob.
package extraction

import (
	"context"
	"errors"
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

// extractArgs stays unexported so extraction work can only be enqueued through
// EnqueueExtraction (TestExtractArgsTypeIsUnexported, TestExtractionExposesExactlyOneEnqueueSeam).
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
	Pages     *PageStore
	Audit     RecordExtractionAudit
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

	// Before ensureJobTx: a worker wired without the port must change no state at all, or the
	// job finishes and nothing records that its outcome went unwritten
	// (TestExtractWorker_NilAuditFailsBeforeAnyStateChange).
	if w.Audit == nil {
		return errors.New("extraction: ExtractWorker.Audit is nil")
	}

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
		// (TestRLS_ExtractWorkerWritesOneResultSetPerJobOnReplay), and its terminal event was
		// emitted by the attempt that ended it (TestRLS_ExtractWorkerReplayEmitsNoSecondRow).
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

	var results []FieldResult
	var images []PageImage
	// Set by whichever stage below fails. Control flow, never a parse of last_error
	// (TestExtractWorker_FailureKindPerStage).
	var kind FailureKind
	doc, err := w.Open(octx, args.DocumentID)
	if err != nil {
		kind = FailureDocumentUnavailable
	}
	if err == nil {
		// ctx, not octx: the sink's credentials come from config, so it is fenced from a tenant
		// identity the way the extractor is (TestRLS_ExtractWorkerWritesPageImagesThroughTheSink).
		if images, _, err = w.Pages.Ingest(ctx, args.TenantID, doc); err != nil {
			kind = FailurePagesNotRendered
		}
	}
	if err == nil {
		// Objects first, rows last, so a committed row always names an object that was PUT
		// (TestRLS_ExtractWorkerFailsTheJobWhenThePageSinkFails). Outside queue.OncePerJob on
		// purpose: these rows commit before Extract, so a document whose field extraction fails
		// still has a page inventory.
		if err = db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
			return writePageImagesTx(ctx, tx, args.TenantID, args.DocumentID, images)
		}); err != nil {
			kind = FailurePageRowsNotWritten
		}
	}
	if err == nil {
		// ctx, not octx: the extractor is fenced from the database and must not be handed a
		// tenant identity.
		if results, err = w.Extractor.Extract(ctx, doc); err != nil {
			kind = FailureExtractFailed
		}
	}
	if err != nil {
		state := "failed"
		if job.Attempt >= job.MaxAttempts {
			state = "dead_lettered"
		}
		if txErr := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
			if advErr := advanceJobTx(ctx, tx, args.TenantID, row.ID, state, err.Error(), job.Attempt); advErr != nil {
				return advErr
			}
			// An attempt with retries left is not a terminal outcome, so it is not an event
			// (TestRLS_ExtractWorkerEmitsFailedOnlyWhenDeadLettered).
			if state != "dead_lettered" {
				return nil
			}
			// Shares this transaction's fate on purpose, like internal/submission's
			// terminal-failure branch: an audit-write failure rolls the advance back with it.
			return w.Audit(ctx, tx, ExtractionAudit{
				DocumentID:       args.DocumentID,
				ExtractionJobID:  row.ID,
				Extractor:        w.Extractor.Name(),
				ExtractorVersion: w.Extractor.Version(),
				State:            state,
				FailureKind:      kind,
			})
		}); txErr != nil {
			return txErr
		}
		return err
	}

	flagged := flaggedCount(results)

	return db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		// The marker, the result rows, the advance to succeeded and the audit row share one
		// transaction and one fate (TestRLS_ExtractWorkerResultWriteIsGuardedByTheJobMarker,
		// TestExtractWorker_AuditWriteIsLastInItsClosure).
		_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
			if err := writeFieldResultsTx(ctx, tx, args.TenantID, row.ID, results); err != nil {
				return err
			}
			if err := advanceJobTx(ctx, tx, args.TenantID, row.ID, "succeeded", "", job.Attempt); err != nil {
				return err
			}
			return w.Audit(ctx, tx, ExtractionAudit{
				Succeeded:        true,
				DocumentID:       args.DocumentID,
				ExtractionJobID:  row.ID,
				Extractor:        w.Extractor.Name(),
				ExtractorVersion: w.Extractor.Version(),
				FieldCount:       len(results),
				FlaggedCount:     flagged,
			})
		})
		return err
	})
}

// flaggedCount counts decided fields only: it stops at the top level and never descends into
// Alternatives, which by FieldResult's own contract carry no reason of their own.
func flaggedCount(results []FieldResult) int {
	n := 0
	for _, r := range results {
		if r.Reason != ReasonNone {
			n++
		}
	}
	return n
}
