package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	writeBackActor = "system"
	// A snooze spends no attempt, so past this many rate limits one spends an attempt instead.
	rateLimitSnoozes = 8
	lockedSnooze     = 15 * time.Minute
)

// WriteBackArgs is the NRS result of one invoice, snapshotted when it was reached.
type WriteBackArgs struct {
	TenantID      string `json:"tenant_id"`
	InvoiceID     string `json:"invoice_id"`
	InvoiceNumber string `json:"invoice_number"`
	ResultVersion int    `json:"result_version"`
	Outcome       string `json:"outcome"`
	IRN           string `json:"irn"`
	Reason        string `json:"reason"`
}

func (WriteBackArgs) Kind() string { return "erp.writeback" }

func (a WriteBackArgs) Tenant() string { return a.TenantID }

func (WriteBackArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// WriteBackKey is the EnqueueTx dedupe key of one invoice result.
func WriteBackKey(invoiceID string, version int) string {
	return fmt.Sprintf("erp.writeback:%s:%d", invoiceID, version)
}

type WriteBackWorker struct {
	river.WorkerDefaults[WriteBackArgs]
	Pool     *pgxpool.Pool
	Registry Registry
	Logger   *slog.Logger
}

// wbDoc is one document to write, as the first transaction decided it.
type wbDoc struct {
	id, sourceDocID, rev, entity, connID string
	conn                                 connInfo
	creds                                Credentials
	caps                                 Capabilities
	wb                                   WriteBack
	rateLimited                          int
}

// wbPlan writes only what the connector's capabilities allow. ok is false for an empty plan.
func wbPlan(caps Capabilities, a WriteBackArgs) (wb WriteBack, ok bool) {
	if a.Outcome == "accepted" && caps.CustomFieldSlots >= 1 && a.IRN != "" {
		wb.IRN = a.IRN // the IRN takes the first slot
	}
	if caps.CustomFieldSlots >= 2 {
		wb.Fields = []string{a.Outcome}
	}
	if caps.Note {
		switch a.Outcome {
		case "accepted":
			wb.Note = "NRS accepted. IRN: " + a.IRN
		case "rejected":
			wb.Note = "NRS rejected: " + a.Reason
		default:
			wb.Note = "NRS submission failed: " + a.Reason
		}
	}
	return wb, wb.IRN != "" || len(wb.Fields) > 0 || wb.Note != ""
}

func (w *WriteBackWorker) Work(ctx context.Context, job *river.Job[WriteBackArgs]) error {
	docs, err := w.load(ctx, job)
	if err != nil {
		return err
	}
	final := job.Attempt >= job.MaxAttempts
	var failure error
	var snooze time.Duration
	var limited []wbDoc
	for _, d := range docs {
		err, rerr := w.write(ctx, job, d, final)
		var rl *RateLimitError
		switch {
		case rerr != nil:
			failure = errors.Join(failure, rerr)
		case err == nil:
		case errors.As(err, &rl) && d.rateLimited < rateLimitSnoozes:
			limited = append(limited, d)
			snooze = max(snooze, rl.RetryAfter)
		case failure == nil:
			failure = err
		}
	}
	if failure != nil {
		if final {
			// The job ends here, so a document still rate-limited ends failed too.
			for _, d := range limited {
				aerr := w.recordTx(ctx, job.Args, func(tx pgx.Tx) error { return auditFailed(ctx, tx, job.Args, d, "rate_limited") })
				if aerr != nil {
					failure = errors.Join(failure, aerr)
				}
			}
		}
		return failure
	}
	if len(limited) > 0 {
		return river.JobSnooze(snooze)
	}
	return nil
}

// load is the first transaction: the skips, and the documents still to write.
func (w *WriteBackWorker) load(ctx context.Context, job *river.Job[WriteBackArgs]) ([]wbDoc, error) {
	a := job.Args
	var todo []wbDoc
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		todo = nil
		locked, err := db.WorkspaceLocked(ctx, tx)
		if err != nil {
			return err
		}
		if locked {
			return river.JobSnooze(lockedSnooze)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE id = $1`, a.InvoiceID).Scan(&n); err != nil || n == 0 {
			return err
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_status_history
			WHERE invoice_id = $1 AND to_status IN ('accepted', 'rejected', 'failed')`, a.InvoiceID).Scan(&current); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT d.id::text, d.source_doc_id, d.source_revision, d.entity_id::text, d.connection_id::text, d.deleted_in_erp_at IS NOT NULL,
			EXISTS (SELECT 1 FROM erp_writebacks b WHERE b.source_document_id = d.id AND b.result_version = $2 AND b.state IN ('succeeded', 'skipped')),
			(SELECT count(*) FROM erp_writebacks b WHERE b.source_document_id = d.id AND b.result_version = $2 AND b.reason = 'rate_limited')
			FROM erp_source_documents d WHERE d.invoice_id = $1 AND d.state IN ('drafted', 'linked') ORDER BY d.created_at, d.id`,
			a.InvoiceID, a.ResultVersion)
		if err != nil {
			return err
		}
		type cand struct {
			wbDoc
			deleted, done bool
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.id, &c.sourceDocID, &c.rev, &c.entity, &c.connID, &c.deleted, &c.done, &c.rateLimited); err != nil {
				rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range cands {
			if c.done {
				continue
			}
			skip := func(reason string) error {
				return insertAttempt(ctx, tx, job, c.wbDoc, "skipped", &reason, nil, nil)
			}
			if current > a.ResultVersion {
				if err := skip("superseded"); err != nil {
					return err
				}
				continue
			}
			conn, ok, err := readConn(ctx, tx, c.connID, false)
			if err != nil {
				return err
			}
			if !ok || conn.status != "active" || w.Registry[conn.system] == nil {
				if err := skip("disconnected"); err != nil {
					return err
				}
				continue
			}
			if c.deleted {
				if err := skip("deleted_in_erp"); err != nil {
					return err
				}
				continue
			}
			c.conn = conn
			c.caps = w.Registry[conn.system].Capabilities()
			var writable bool
			if c.wb, writable = wbPlan(c.caps, a); !writable {
				if err := skip("nothing_writable"); err != nil {
					return err
				}
				continue
			}
			if c.creds, err = credentialsFor(ctx, tx, conn.grantID); err != nil {
				return err
			}
			todo = append(todo, c.wbDoc)
		}
		return nil
	})
	return todo, err
}

// write sends one document's write with no transaction held, then records the attempt.
// vendorErr is the connector's error; recordErr is the failure to record the attempt.
func (w *WriteBackWorker) write(ctx context.Context, job *river.Job[WriteBackArgs], d wbDoc, final bool) (vendorErr, recordErr error) {
	written, newRef, err := w.send(ctx, w.Registry[d.conn.system], d)
	a := job.Args
	return err, w.recordTx(ctx, a, func(tx pgx.Tx) error {
		if err == nil {
			rev := newRef.SourceRevision
			if e := insertAttempt(ctx, tx, job, d, "succeeded", nil, &written, &rev); e != nil {
				return e
			}
			if a.Outcome == "accepted" {
				return audit.Record(ctx, tx, writeBackActor, "erp.writeback.succeeded", map[string]any{
					"system": d.conn.system, "connection_id": d.connID, "source_doc_id": d.sourceDocID,
					"invoice_id": a.InvoiceID, "invoice_number": a.InvoiceNumber, "outcome": a.Outcome,
					"result_version": a.ResultVersion, "irn": a.IRN,
				})
			}
			return audit.Record(ctx, tx, writeBackActor, "erp.writeback.succeeded", map[string]any{
				"system": d.conn.system, "connection_id": d.connID, "source_doc_id": d.sourceDocID,
				"invoice_id": a.InvoiceID, "invoice_number": a.InvoiceNumber, "outcome": a.Outcome,
				"result_version": a.ResultVersion,
			})
		}
		reason := failureReason(err)
		if e := insertAttempt(ctx, tx, job, d, "failed", &reason, strPtr(written), nil); e != nil {
			return e
		}
		var rl *RateLimitError
		if !final || (errors.As(err, &rl) && d.rateLimited < rateLimitSnoozes) {
			return nil
		}
		return auditFailed(ctx, tx, a, d, reason)
	})
}

func auditFailed(ctx context.Context, tx pgx.Tx, a WriteBackArgs, d wbDoc, reason string) error {
	return audit.Record(ctx, tx, writeBackActor, "erp.writeback.failed", map[string]any{
		"system": d.conn.system, "connection_id": d.connID, "source_doc_id": d.sourceDocID,
		"invoice_id": a.InvoiceID, "invoice_number": a.InvoiceNumber, "outcome": a.Outcome,
		"result_version": a.ResultVersion, "failure": reason,
	})
}

func (w *WriteBackWorker) recordTx(ctx context.Context, a WriteBackArgs, fn func(pgx.Tx) error) error {
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, fn)
}

// send fetches the current revision first when the vendor needs it, and retries one conflict.
func (w *WriteBackWorker) send(ctx context.Context, c Connector, d wbDoc) (written string, out DocRef, err error) {
	ref := DocRef{SourceDocID: d.sourceDocID, SourceRevision: d.rev, Scope: d.conn.externalKey}
	needRev := d.caps.RequiresRevisionOnWrite
	for retried := false; ; retried = true {
		if needRev {
			doc, ferr := c.Fetch(ctx, d.creds, ref)
			if ferr != nil {
				return written, DocRef{}, ferr
			}
			ref.SourceRevision = doc.Ref.SourceRevision
		}
		written = ref.SourceRevision
		out, err = c.WriteBack(ctx, d.creds, ref, d.wb)
		if needRev && !retried && errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return written, out, err
	}
}

func failureReason(err error) string {
	var rl *RateLimitError
	switch {
	case errors.As(err, &rl):
		return "rate_limited"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrRevisionConflict):
		return "revision_conflict"
	}
	return "vendor"
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func insertAttempt(ctx context.Context, tx pgx.Tx, job *river.Job[WriteBackArgs], d wbDoc, state string, reason, written, newRev *string) error {
	a := job.Args
	_, err := tx.Exec(ctx, `INSERT INTO erp_writebacks
		(tenant_id, entity_id, connection_id, source_document_id, invoice_id, result_version, outcome, attempt, state, reason, written_revision, new_revision, river_job_id)
		VALUES ($13, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		d.entity, d.connID, d.id, a.InvoiceID, a.ResultVersion, a.Outcome, job.Attempt, state, reason, written, newRev, job.ID, a.TenantID)
	return err
}
