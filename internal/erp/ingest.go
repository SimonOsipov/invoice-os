package erp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	ingestActor = "system"
	// A claim older than this is stale: its job was discarded without a terminal row.
	claimLife   = "15 minutes"
	claimSnooze = 30 * time.Second
)

var errUnvalidated = errors.New("erp: the gate left the draft unvalidated")

var errClaimLost = errors.New("erp: ingest claim is no longer this job's")

// IngestArgs is one document revision to take. The client comes from the connection row, never the args.
type IngestArgs struct {
	TenantID       string `json:"tenant_id"`
	ConnectionID   string `json:"connection_id"`
	SourceDocID    string `json:"source_doc_id"`
	SourceRevision string `json:"source_revision"`
	Deleted        bool   `json:"deleted"`
}

func (IngestArgs) Kind() string { return "erp.ingest" }

func (a IngestArgs) Tenant() string { return a.TenantID }

// InsertOpts omits completed: once per revision is the idempotency key's job, and a completed job must not block a relist after an epoch bump.
func (IngestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: inFlightStates}}
}

// IngestKey is the EnqueueTx dedupe key of one ingest. Each field is length-prefixed before
// hashing so no pair of ids can run together.
func IngestKey(connectionID string, epoch int64, sourceDocID, sourceRevision string, deleted bool) string {
	flag := "0"
	if deleted {
		flag = "1"
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s%d:%s%s", len(sourceDocID), sourceDocID, len(sourceRevision), sourceRevision, flag)
	return fmt.Sprintf("erp.ingest:%s:%d:%s", connectionID, epoch, hex.EncodeToString(h.Sum(nil)))
}

// credentialsFor loads what a connector needs for the grant. The fake needs no secret.
func credentialsFor(ctx context.Context, tx pgx.Tx, grantID *string) (Credentials, error) {
	if grantID == nil {
		return Credentials{}, nil
	}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM erp_grants WHERE id = $1`, *grantID).Scan(&kind); err != nil {
		return Credentials{}, err
	}
	// ceiling: no secret loaded; a real connector's epic reads secret_ref here
	return Credentials{Kind: GrantKind(kind)}, nil
}

type IngestWorker struct {
	river.WorkerDefaults[IngestArgs]
	Pool     *pgxpool.Pool
	Registry Registry
	Invoices importer.DraftCreator
	Gate     importer.DraftValidator
	Logger   *slog.Logger
}

type connInfo struct {
	entity, system, externalKey, status string
	grantID                             *string
}

type docState struct {
	id, state, rev           string
	failure, invoiceID, hash *string
	claimed                  *string
	hashVersion              *int16
	lastJob                  *int64
	deleted, changed         *time.Time
	live                     bool
}

type invRef struct {
	id, status, number string
	conn               *string
}

// plan is what the claim transaction decided for the work outside any transaction.
type plan struct {
	kind      int
	invoiceID string
}

const (
	planDone = iota
	planCreate
	planAdopt
)

// ingestJob is the args plus the River job id, which orders the jobs of one document.
type ingestJob struct {
	IngestArgs
	id int64
}

// stale: a job of another revision that was enqueued before the job that last wrote the row.
// Revisions are opaque, so the job id is the only order.
func (a ingestJob) stale(d docState, found bool) bool {
	return found && d.lastJob != nil && d.rev != a.SourceRevision && a.id < *d.lastJob
}

func (w *IngestWorker) Work(ctx context.Context, job *river.Job[IngestArgs]) error {
	j := ingestJob{IngestArgs: job.Args, id: job.ID}
	err := w.ingest(ctx, j)
	if err == nil {
		return nil
	}
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) && job.Attempt >= job.MaxAttempts {
		if terr := w.terminal(context.WithoutCancel(ctx), j); terr != nil && w.Logger != nil {
			w.Logger.Error("erp ingest: terminal row not written", "connection_id", job.Args.ConnectionID, "err", terr)
		}
	}
	return err
}

func (w *IngestWorker) ingest(ctx context.Context, a ingestJob) error {
	var (
		c       connInfo
		d       docState
		found   bool
		skipped bool
		creds   Credentials
	)
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		var ok bool
		var err error
		c, ok, err = readConn(ctx, tx, a.ConnectionID, false)
		if err != nil {
			return err
		}
		if skipped, err = w.stopped(ctx, tx, c, ok, a.ConnectionID); skipped || err != nil {
			return err
		}
		if creds, err = credentialsFor(ctx, tx, c.grantID); err != nil {
			return err
		}
		d, found, err = readDoc(ctx, tx, a, false)
		return err
	})
	if err != nil || skipped {
		return err
	}
	if a.stale(d, found) || found && shortCircuit(d, a) {
		return nil
	}
	ictx := auth.WithIdentity(ctx, auth.Identity{Subject: ingestActor, Role: "authenticated", TenantID: a.TenantID})

	// A new revision of a draft whose validation never ran gets validated first (a retry after a gate outage).
	if found && d.state == "drafted" && d.invoiceID != nil && !a.Deleted {
		if err := w.validateIfDraft(ictx, a.TenantID, *d.invoiceID); err != nil {
			return err
		}
	}
	if a.Deleted {
		_, err := w.claim(ctx, a, nil, "")
		return err
	}

	m := w.Registry[c.system].DefaultMapping()
	if ValidateMapping(m) != nil {
		return w.fail(ctx, a, "no_mapping")
	}
	doc, err := w.Registry[c.system].Fetch(ctx, creds, DocRef{SourceDocID: a.SourceDocID, SourceRevision: a.SourceRevision, Scope: c.externalKey})
	if errors.Is(err, ErrNotFound) {
		return w.fail(ctx, a, "not_found")
	}
	if err != nil {
		return throttled(err)
	}
	customerID, err := CustomerIDOf(m, doc)
	if errors.Is(err, ErrUnmappable) {
		return w.fail(ctx, a, "unmappable")
	}
	if err != nil {
		return err
	}
	var party *Party
	if customerID != "" {
		p, err := w.Registry[c.system].FetchParty(ctx, creds, c.externalKey, customerID)
		switch {
		case err == nil:
			party = &p
		case !errors.Is(err, ErrNotFound):
			return throttled(err)
		}
	}
	mapped, err := Apply(m, doc, party)
	if errors.Is(err, ErrUnmappable) {
		return w.fail(ctx, a, "unmappable")
	}
	if err != nil {
		return err
	}
	in := mapped.In
	in.EntityID = c.entity
	in.ERPConnectionID = &a.ConnectionID
	hash := ContentHash(in)

	p, err := w.claim(ctx, a, &in, hash)
	if err != nil {
		return err
	}
	switch p.kind {
	case planAdopt:
		if err := w.validateIfDraft(ictx, a.TenantID, p.invoiceID); err != nil {
			return err
		}
		return w.settle(ctx, a, in, hash, p.invoiceID, nil)
	case planCreate:
		_, err := importer.CreateDrafts(ictx, w.Invoices, w.Gate, []invoice.CreateInput{in},
			func(ctx context.Context, out importer.DraftOutcome, status string) error {
				if status != "completed" {
					return nil // the claim stays; the retry adopts what was created
				}
				r := out.Results[0]
				if r.Invoice != nil && r.Unvalidated {
					return errUnvalidated // the claim stays; the retry adopts the draft and validates it
				}
				if r.Invoice != nil {
					return w.settle(ctx, a, in, hash, r.Invoice.ID, nil)
				}
				return w.settle(ctx, a, in, hash, "", r.Err)
			})
		return err
	}
	return nil
}

// stopped reports whether sync is off for the connection; a stopped ingest reopens the connection's
// listing so a later reconnect takes the document under a new key.
func (w *IngestWorker) stopped(ctx context.Context, tx pgx.Tx, c connInfo, ok bool, connID string) (bool, error) {
	stop := !ok || c.status != "active" || w.Registry[c.system] == nil
	if !stop {
		locked, err := db.WorkspaceLocked(ctx, tx)
		if err != nil {
			return false, err
		}
		stop = locked
	}
	if stop && ok {
		return true, reopen(ctx, tx, connID)
	}
	return stop, nil
}

// reopen clears the cursor and bumps the epoch so the next sweep lists every document under new keys.
func reopen(ctx context.Context, tx pgx.Tx, connID string) error {
	_, err := tx.Exec(ctx, `UPDATE erp_connections SET cursor = NULL, ingest_epoch = ingest_epoch + 1, updated_at = now() WHERE id = $1`, connID)
	return err
}

const (
	yieldNone = iota
	yieldSkip
	yieldSnooze
)

// yieldsTo says whether a job at a.SourceRevision must leave the row alone: another revision's live
// claim is in flight (snooze), or the row is a deleted one that this job's revision does not own (skip).
func yieldsTo(d docState, found bool, a ingestJob) int {
	switch {
	case !found || d.rev == a.SourceRevision:
		return yieldNone
	case a.stale(d, found):
		return yieldSkip
	case d.state == "pending" && d.claimed != nil && d.live:
		return yieldSnooze
	case d.state == "deleted" || d.deleted != nil:
		return yieldSkip
	}
	return yieldNone
}

func readConn(ctx context.Context, tx pgx.Tx, id string, lock bool) (connInfo, bool, error) {
	q := `SELECT entity_id::text, system, coalesce(external_key, ''), status, grant_id::text FROM erp_connections WHERE id = $1`
	if lock {
		q += " FOR NO KEY UPDATE"
	}
	var c connInfo
	err := tx.QueryRow(ctx, q, id).Scan(&c.entity, &c.system, &c.externalKey, &c.status, &c.grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return connInfo{}, false, nil
	}
	return c, err == nil, err
}

func readDoc(ctx context.Context, tx pgx.Tx, a ingestJob, lock bool) (docState, bool, error) {
	q := `SELECT id::text, state, source_revision, failure, invoice_id::text, content_hash, claimed_number, hash_version,
	             changed_after_clearance_at, deleted_in_erp_at, last_job_id, updated_at > now() - interval '` + claimLife + `'
	      FROM erp_source_documents WHERE connection_id = $1 AND source_doc_id = $2`
	if lock {
		q += " FOR UPDATE"
	}
	var d docState
	err := tx.QueryRow(ctx, q, a.ConnectionID, a.SourceDocID).Scan(&d.id, &d.state, &d.rev, &d.failure, &d.invoiceID,
		&d.hash, &d.claimed, &d.hashVersion, &d.changed, &d.deleted, &d.lastJob, &d.live)
	if errors.Is(err, pgx.ErrNoRows) {
		return docState{}, false, nil
	}
	return d, err == nil, err
}

// shortCircuit is the settled-row check: this revision was already taken, in the same live or deleted shape.
func shortCircuit(d docState, a ingestJob) bool {
	switch d.state {
	case "drafted", "linked", "held", "deleted":
	case "failed":
		if d.failure != nil && *d.failure == "exhausted" {
			return false
		}
	default:
		return false
	}
	return d.rev == a.SourceRevision && (d.deleted != nil || d.state == "deleted") == a.Deleted
}

func lockNumber(ctx context.Context, tx pgx.Tx, entity, number string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_number:' || $1 || ':' || $2, 0))`, entity, number)
	return err
}

type docWrite struct {
	state, rev string
	failure    *string
	claimed    *string
	invoiceID  *string
	differs    bool
	hash       string
}

// upsertDoc writes one row and resets every mark: the ref is live. A nil hash keeps the stored one.
func upsertDoc(ctx context.Context, tx pgx.Tx, a ingestJob, entity string, v docWrite) error {
	var hash *string
	var version *int
	if v.hash != "" {
		hash, version = &v.hash, new(int)
		*version = HashVersion
	}
	_, err := tx.Exec(ctx, `
INSERT INTO erp_source_documents (tenant_id, entity_id, connection_id, source_doc_id, source_revision, state, failure,
                                  claimed_number, invoice_id, differs_from_erp, content_hash, hash_version, last_job_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (connection_id, source_doc_id) DO UPDATE SET
    source_revision = EXCLUDED.source_revision, state = EXCLUDED.state, failure = EXCLUDED.failure,
    claimed_number = EXCLUDED.claimed_number, invoice_id = EXCLUDED.invoice_id,
    differs_from_erp = EXCLUDED.differs_from_erp,
    content_hash = coalesce(EXCLUDED.content_hash, erp_source_documents.content_hash),
    hash_version = coalesce(EXCLUDED.hash_version, erp_source_documents.hash_version),
    last_job_id = greatest(erp_source_documents.last_job_id, EXCLUDED.last_job_id),
    deleted_in_erp_at = NULL, changed_after_clearance_at = NULL, updated_at = now()`,
		a.TenantID, entity, a.ConnectionID, a.SourceDocID, v.rev, v.state, v.failure, v.claimed, v.invoiceID, v.differs, hash, version, a.id)
	return err
}

// throttled turns a vendor rate limit into a snooze, so it does not spend an attempt.
func throttled(err error) error {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return river.JobSnooze(rl.RetryAfter)
	}
	return err
}

func (w *IngestWorker) snoozeErr() error { return river.JobSnooze(claimSnooze) }

// claim is TxA: it decides the document under the connection, row and number locks.
func (w *IngestWorker) claim(ctx context.Context, a ingestJob, in *invoice.CreateInput, hash string) (plan, error) {
	p := plan{kind: planDone}
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		c, ok, err := readConn(ctx, tx, a.ConnectionID, true)
		if err != nil {
			return err
		}
		if stop, err := w.stopped(ctx, tx, c, ok, a.ConnectionID); stop || err != nil {
			return err
		}
		d, found, err := readDoc(ctx, tx, a, true)
		if err != nil {
			return err
		}
		if a.stale(d, found) || found && shortCircuit(d, a) {
			return nil
		}
		held := found && d.state == "pending" && d.claimed != nil && d.live
		if a.Deleted {
			if held {
				return w.snoozeErr()
			}
			return w.markDeleted(ctx, tx, a, c, d, found)
		}

		number := in.InvoiceNumber
		if err := lockNumber(ctx, tx, c.entity, number); err != nil {
			return err
		}
		// A claim of this revision and number is this job's own, however old: River's backoff outlasts claimLife.
		ownRetry := found && d.state == "pending" && d.claimed != nil && d.rev == a.SourceRevision && *d.claimed == number
		if held && !ownRetry {
			return w.snoozeErr()
		}
		rowID := ""
		if found {
			rowID = d.id
		}
		var rival bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM erp_source_documents
			WHERE entity_id = $1 AND claimed_number = $2 AND updated_at > now() - interval '`+claimLife+`' AND id::text <> $3)`,
			c.entity, number, rowID).Scan(&rival); err != nil {
			return err
		}
		if rival {
			return w.snoozeErr()
		}
		if found && (d.state == "drafted" || d.state == "linked") && d.invoiceID != nil {
			return w.newRevision(ctx, tx, a, c, d, *in, hash)
		}

		inv, exists, err := lookupInvoice(ctx, tx, c.entity, number)
		if err != nil {
			return err
		}
		if !exists {
			p = plan{kind: planCreate}
			return upsertDoc(ctx, tx, a, c.entity, docWrite{state: "pending", rev: a.SourceRevision, claimed: &number, hash: hash})
		}
		linked, err := linkedElsewhere(ctx, tx, a.ConnectionID, inv.id, rowID)
		if err != nil {
			return err
		}
		if ownRetry && !linked && inv.conn != nil && *inv.conn == a.ConnectionID {
			p = plan{kind: planAdopt, invoiceID: inv.id}
			return nil
		}
		return w.linkOrHold(ctx, tx, a, c, *in, hash, inv, linked)
	})
	return p, err
}

func lookupInvoice(ctx context.Context, tx pgx.Tx, entity, number string) (invRef, bool, error) {
	var r invRef
	err := tx.QueryRow(ctx, `SELECT id::text, status, invoice_number, erp_connection_id::text FROM invoices
		WHERE entity_id = $1 AND invoice_number = $2`, entity, number).Scan(&r.id, &r.status, &r.number, &r.conn)
	if errors.Is(err, pgx.ErrNoRows) {
		return invRef{}, false, nil
	}
	return r, err == nil, err
}

func linkedElsewhere(ctx context.Context, tx pgx.Tx, connID, invoiceID, ownRow string) (bool, error) {
	var linked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM erp_source_documents
		WHERE connection_id = $1 AND invoice_id = $2 AND id::text <> $3)`, connID, invoiceID, ownRow).Scan(&linked)
	return linked, err
}

// linkOrHold links or holds a document whose number is taken: the number belongs to an existing invoice, which is never touched.
func (w *IngestWorker) linkOrHold(ctx context.Context, tx pgx.Tx, a ingestJob, c connInfo, in invoice.CreateInput, hash string, inv invRef, linked bool) error {
	v := docWrite{state: "held", rev: a.SourceRevision, hash: hash}
	var differs bool
	if !linked {
		var err error
		if differs, err = differsFrom(ctx, tx, inv.id, in); err != nil {
			return err
		}
		v = docWrite{state: "linked", rev: a.SourceRevision, invoiceID: &inv.id, differs: differs, hash: hash}
	}
	if err := upsertDoc(ctx, tx, a, c.entity, v); err != nil {
		return err
	}
	return recordSkipped(ctx, tx, c, a, v.invoiceID, inv.number, !linked, differs)
}

// differsFrom compares the mapped content with the stored invoice and its lines.
func differsFrom(ctx context.Context, tx pgx.Tx, invoiceID string, in invoice.CreateInput) (bool, error) {
	var amounts invoiceAmounts
	if err := tx.QueryRow(ctx, `SELECT subtotal::text, vat::text, total::text FROM invoices WHERE id = $1`, invoiceID).
		Scan(&amounts.Subtotal, &amounts.VAT, &amounts.Total); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT description, quantity::text, unit_price::text, line_total::text, line_tax::text
		FROM line_items WHERE invoice_id = $1 ORDER BY line_no`, invoiceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var lines []lineAmounts
	for rows.Next() {
		var l lineAmounts
		if err := rows.Scan(&l.Description, &l.Quantity, &l.UnitPrice, &l.LineTotal, &l.LineTax); err != nil {
			return false, err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return Differs(in, amounts, lines), nil
}

// newRevision handles a taken document's new revision never writes the invoice.
func (w *IngestWorker) newRevision(ctx context.Context, tx pgx.Tx, a ingestJob, c connInfo, d docState, in invoice.CreateInput, hash string) error {
	var status, number string
	if err := tx.QueryRow(ctx, `SELECT status, invoice_number FROM invoices WHERE id = $1`, *d.invoiceID).Scan(&status, &number); err != nil {
		return err
	}
	const set = `UPDATE erp_source_documents SET source_revision = $2, last_job_id = greatest(last_job_id, $3), deleted_in_erp_at = NULL, updated_at = now()`
	switch {
	case d.hashVersion == nil || int(*d.hashVersion) != HashVersion:
		differs, err := differsFrom(ctx, tx, *d.invoiceID, in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, set+`, content_hash = $4, hash_version = $5, differs_from_erp = $6 WHERE id = $1`,
			d.id, a.SourceRevision, a.id, hash, HashVersion, differs)
		return err
	case d.hash != nil && *d.hash == hash:
		_, err := tx.Exec(ctx, set+` WHERE id = $1`, d.id, a.SourceRevision, a.id)
		return err
	case status == string(invoice.StatusAccepted):
		if _, err := tx.Exec(ctx, set+`, content_hash = $4, changed_after_clearance_at = coalesce(changed_after_clearance_at, now()) WHERE id = $1`,
			d.id, a.SourceRevision, a.id, hash); err != nil {
			return err
		}
		return recordChanged(ctx, tx, c, a, *d.invoiceID, number, false)
	default:
		differs, err := differsFrom(ctx, tx, *d.invoiceID, in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, set+`, content_hash = $4, differs_from_erp = $5 WHERE id = $1`, d.id, a.SourceRevision, a.id, hash, differs)
		return err
	}
}

// markDeleted: a deleted ref marks the document and never touches an invoice.
func (w *IngestWorker) markDeleted(ctx context.Context, tx pgx.Tx, a ingestJob, c connInfo, d docState, found bool) error {
	if !found || d.invoiceID == nil {
		_, err := tx.Exec(ctx, `
INSERT INTO erp_source_documents (tenant_id, entity_id, connection_id, source_doc_id, source_revision, state, deleted_in_erp_at, last_job_id)
VALUES ($1, $2, $3, $4, $5, 'deleted', now(), $6)
ON CONFLICT (connection_id, source_doc_id) DO UPDATE SET source_revision = EXCLUDED.source_revision, state = 'deleted',
    last_job_id = greatest(erp_source_documents.last_job_id, EXCLUDED.last_job_id),
    failure = NULL, claimed_number = NULL, invoice_id = NULL, deleted_in_erp_at = now(), updated_at = now()`,
			a.TenantID, c.entity, a.ConnectionID, a.SourceDocID, a.SourceRevision, a.id)
		return err
	}
	var status, number string
	if err := tx.QueryRow(ctx, `SELECT status, invoice_number FROM invoices WHERE id = $1`, *d.invoiceID).Scan(&status, &number); err != nil {
		return err
	}
	if status != string(invoice.StatusAccepted) {
		_, err := tx.Exec(ctx, `UPDATE erp_source_documents SET source_revision = $2, last_job_id = greatest(last_job_id, $3), deleted_in_erp_at = now(), updated_at = now() WHERE id = $1`,
			d.id, a.SourceRevision, a.id)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp_source_documents SET source_revision = $2, last_job_id = greatest(last_job_id, $3), deleted_in_erp_at = now(),
		changed_after_clearance_at = coalesce(changed_after_clearance_at, now()), updated_at = now() WHERE id = $1`,
		d.id, a.SourceRevision, a.id); err != nil {
		return err
	}
	return recordChanged(ctx, tx, c, a, *d.invoiceID, number, true)
}

// settle is TxB: it writes what the create (or the adopted invoice) came to, under this job's claim.
func (w *IngestWorker) settle(ctx context.Context, a ingestJob, in invoice.CreateInput, hash, invoiceID string, refusal error) error {
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		c, ok, err := readConn(ctx, tx, a.ConnectionID, true)
		if err != nil {
			return err
		}
		if !ok {
			return errClaimLost
		}
		d, found, err := readDoc(ctx, tx, a, true)
		if err != nil {
			return err
		}
		if !found || d.state != "pending" || d.rev != a.SourceRevision || d.claimed == nil || *d.claimed != in.InvoiceNumber {
			return errClaimLost
		}
		if err := lockNumber(ctx, tx, c.entity, in.InvoiceNumber); err != nil {
			return err
		}
		switch {
		case invoiceID != "":
			if err := upsertDoc(ctx, tx, a, c.entity, docWrite{state: "drafted", rev: a.SourceRevision, invoiceID: &invoiceID, hash: hash}); err != nil {
				return err
			}
			return recordPulled(ctx, tx, c, a, invoiceID, in.InvoiceNumber)
		case errors.Is(refusal, invoice.ErrDuplicateNumber):
			inv, exists, err := lookupInvoice(ctx, tx, c.entity, in.InvoiceNumber)
			if err != nil {
				return err
			}
			if !exists {
				return refusal
			}
			linked, err := linkedElsewhere(ctx, tx, a.ConnectionID, inv.id, d.id)
			if err != nil {
				return err
			}
			return w.linkOrHold(ctx, tx, a, c, in, hash, inv, linked)
		default:
			failure := "refused"
			return upsertDoc(ctx, tx, a, c.entity, docWrite{state: "failed", rev: a.SourceRevision, failure: &failure, hash: hash})
		}
	})
}

// fail ends the document as failed; a document that already has an invoice only records the revision.
func (w *IngestWorker) fail(ctx context.Context, a ingestJob, failure string) error {
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		c, ok, err := readConn(ctx, tx, a.ConnectionID, true)
		if err != nil || !ok {
			return err
		}
		d, found, err := readDoc(ctx, tx, a, true)
		if err != nil {
			return err
		}
		switch yield := yieldsTo(d, found, a); {
		case yield == yieldSnooze:
			return w.snoozeErr()
		case yield == yieldSkip, found && d.rev != a.SourceRevision && failure == "not_found":
			return nil // a superseded revision's job leaves the newer revision's row alone
		}
		if found && d.invoiceID != nil {
			_, err := tx.Exec(ctx, `UPDATE erp_source_documents SET source_revision = $2, last_job_id = greatest(last_job_id, $3), updated_at = now() WHERE id = $1`, d.id, a.SourceRevision, a.id)
			return err
		}
		return upsertDoc(ctx, tx, a, c.entity, docWrite{state: "failed", rev: a.SourceRevision, failure: &failure})
	})
}

// terminal leaves the row an exhausted ingest ends on: drafted when its own create landed, else failed.
func (w *IngestWorker) terminal(ctx context.Context, a ingestJob) error {
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		c, ok, err := readConn(ctx, tx, a.ConnectionID, true)
		if err != nil || !ok {
			return err
		}
		d, found, err := readDoc(ctx, tx, a, true)
		if err != nil {
			return err
		}
		if yieldsTo(d, found, a) != yieldNone {
			return nil
		}
		if found && d.invoiceID != nil {
			if d.rev != a.SourceRevision {
				return reopen(ctx, tx, a.ConnectionID) // the revision's key is spent; the next sweep lists it again
			}
			return nil
		}
		if found && d.state == "pending" && d.claimed != nil && d.rev == a.SourceRevision {
			inv, exists, err := lookupInvoice(ctx, tx, c.entity, *d.claimed)
			if err != nil {
				return err
			}
			linked := false
			if exists {
				if linked, err = linkedElsewhere(ctx, tx, a.ConnectionID, inv.id, d.id); err != nil {
					return err
				}
			}
			if exists && !linked && inv.conn != nil && *inv.conn == a.ConnectionID {
				if err := upsertDoc(ctx, tx, a, c.entity, docWrite{state: "drafted", rev: a.SourceRevision, invoiceID: &inv.id}); err != nil {
					return err
				}
				return recordPulled(ctx, tx, c, a, inv.id, inv.number)
			}
		}
		failure := "exhausted"
		return upsertDoc(ctx, tx, a, c.entity, docWrite{state: "failed", rev: a.SourceRevision, failure: &failure})
	})
}

// validateIfDraft runs the gate on an invoice that is still an unvalidated draft.
func (w *IngestWorker) validateIfDraft(ctx context.Context, tenantID, invoiceID string) error {
	var pending bool
	if err := db.WithinTenantTx(ctx, w.Pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status = 'draft' AND rule_set_version_id IS NULL FROM invoices WHERE id = $1`, invoiceID).Scan(&pending)
	}); err != nil || !pending {
		return err
	}
	g, ok := w.Invoices.(interface {
		Get(ctx context.Context, id string) (invoice.Invoice, error)
	})
	if !ok {
		return errors.New("erp: the invoice store cannot read a draft back")
	}
	inv, err := g.Get(ctx, invoiceID)
	if err != nil {
		return err
	}
	_, err = w.Gate.ValidateBatch(ctx, []invoice.Invoice{inv})
	return err
}

func recordPulled(ctx context.Context, tx pgx.Tx, c connInfo, a ingestJob, invoiceID, number string) error {
	return audit.Record(ctx, tx, ingestActor, "erp.document.pulled", map[string]any{
		"system": c.system, "connection_id": a.ConnectionID, "source_doc_id": a.SourceDocID,
		"source_revision": a.SourceRevision, "invoice_id": invoiceID, "invoice_number": number,
	})
}

func recordSkipped(ctx context.Context, tx pgx.Tx, c connInfo, a ingestJob, invoiceID *string, number string, linked, differs bool) error {
	return audit.Record(ctx, tx, ingestActor, "erp.document.skipped_duplicate", map[string]any{
		"system": c.system, "connection_id": a.ConnectionID, "source_doc_id": a.SourceDocID,
		"source_revision": a.SourceRevision, "invoice_id": invoiceID, "invoice_number": number,
		"linked": linked, "differs": differs,
	})
}

func recordChanged(ctx context.Context, tx pgx.Tx, c connInfo, a ingestJob, invoiceID, number string, deleted bool) error {
	return audit.Record(ctx, tx, ingestActor, "erp.document.changed_after_clearance", map[string]any{
		"system": c.system, "connection_id": a.ConnectionID, "source_doc_id": a.SourceDocID,
		"source_revision": a.SourceRevision, "invoice_id": invoiceID, "invoice_number": number,
		"deleted": deleted,
	})
}
