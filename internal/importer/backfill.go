package importer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// backfillActor is the audit_log.actor for every document.read the backfill
// causes. audit_log.actor is text (1..255), so a non-uuid is legal.
const backfillActor = "backfill-source-rows"

// ErrBackfillPrivilegedRole refuses a SUPERUSER/BYPASSRLS connection: the
// tenant_isolation policy would be inert and the run would write across
// tenants with no error to show for it.
var ErrBackfillPrivilegedRole = errors.New("importer: refusing to backfill over a SUPERUSER/BYPASSRLS role; run as invoice_app")

// BackfillResult is one tenant's run summary. InvoicesRecoverable counts what
// a real run would write and is populated identically on both paths, so a
// dry run still reports the yield.
type BackfillResult struct {
	DocumentsScanned    int
	DocumentsSkipped    int
	DocumentsAmbiguous  int
	InvoicesRecoverable int
	InvoicesWritten     int
	InvoicesAmbiguous   int
	Notes               []string
}

// backfillInvoice is one invoices row under the document being repaired.
type backfillInvoice struct {
	id        string
	number    string
	lineItems int
	needsRows bool // source_rows IS NULL
}

// BackfillSourceRows recovers invoices.source_rows for tenantID by reparsing
// each stored source document through the same Decode the import path uses,
// then matching each invoice's raw invoice_number back to the file's rows.
// Anything ambiguous writes nothing ([ambiguous-backfill-is-null]) -- a
// best-guess range would be a wrong number that looks authoritative on an
// evidence surface. Idempotent: the UPDATE is guarded by source_rows IS NULL.
//
// open is the same seam SheetHandler takes; production passes docSvc.Open.
func BackfillSourceRows(
	ctx context.Context,
	pool *pgxpool.Pool,
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	tenantID string,
	dryRun bool,
) (BackfillResult, error) {
	if err := refusePrivilegedRole(ctx, pool); err != nil {
		return BackfillResult{}, err
	}

	// document.Store.Get resolves its tenant from the identity in ctx and a CLI
	// has none. Building it from our OWN tenantID argument -- rather than
	// accepting a caller's context -- is what makes "run as A, open B's
	// document" unrepresentable. First production caller of auth.WithIdentity;
	// internal/document has no SystemActor equivalent.
	ctx = auth.WithIdentity(ctx, auth.Identity{Subject: backfillActor, Role: "authenticated", TenantID: tenantID})

	docIDs, err := backfillDocumentIDs(ctx, pool, tenantID)
	if err != nil {
		return BackfillResult{}, err
	}

	var res BackfillResult
	for _, docID := range docIDs {
		res.DocumentsScanned++

		rows, err := decodeStoredDocument(ctx, open, docID)
		if err != nil {
			res.DocumentsSkipped++
			res.Notes = append(res.Notes, fmt.Sprintf("document %s: skipped (%v)", docID, err))
			continue
		}

		if err := backfillDocument(ctx, pool, tenantID, docID, rows, dryRun, &res); err != nil {
			return BackfillResult{}, err
		}
	}
	return res, nil
}

// refusePrivilegedRole fails closed before the first document is opened.
// pg_roles is world-readable, so this costs one query.
func refusePrivilegedRole(ctx context.Context, pool *pgxpool.Pool) error {
	var privileged bool
	if err := pool.QueryRow(ctx,
		`SELECT rolbypassrls OR rolsuper FROM pg_roles WHERE rolname = current_user`,
	).Scan(&privileged); err != nil {
		return fmt.Errorf("importer: check current_user privileges: %w", err)
	}
	if privileged {
		return ErrBackfillPrivilegedRole
	}
	return nil
}

// backfillDocumentIDs lists the documents backing at least one repairable
// invoice. RLS scopes the SELECT; no tenant_id predicate is written by hand.
func backfillDocumentIDs(ctx context.Context, pool *pgxpool.Pool, tenantID string) ([]string, error) {
	var ids []string
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT DISTINCT source_document_id FROM invoices
			 WHERE source_document_id IS NOT NULL AND source_rows IS NULL
			 ORDER BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// decodeStoredDocument reproduces the import-time decode exactly: format from
// the stored (immutable) row, then Decode. Every failure is the caller's cue
// to report and skip, never fatal.
func decodeStoredDocument(
	ctx context.Context,
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	docID string,
) ([][]string, error) {
	doc, obj, err := open(ctx, docID, "")
	if err != nil {
		return nil, err
	}
	if obj.Body == nil {
		return nil, errors.New("opened with no body")
	}
	defer func() { _ = obj.Body.Close() }()

	format := detectFormat(derefOr(doc.Filename, ""), derefOr(doc.DeclaredContentType, ""))
	if format == "" {
		return nil, errors.New("unrecognized file format")
	}
	_, rows, _, err := Decode(obj.Body, format)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// backfillDocument loads one document's invoices, infers its invoice-number
// column and writes the rows it can prove, all in one transaction. A refusal
// commits nothing because it queues no UPDATE, not because it rolls back.
func backfillDocument(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, docID string,
	rows [][]string,
	dryRun bool,
	res *BackfillResult,
) error {
	var out BackfillResult
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		all, err := documentInvoices(ctx, tx, docID)
		if err != nil {
			return err
		}

		var targets []backfillInvoice
		for _, inv := range all {
			if inv.needsRows {
				targets = append(targets, inv)
			}
		}
		if len(targets) == 0 {
			return nil
		}

		// Duplicate numbers are counted over EVERY invoice on the document --
		// a populated twin makes a target's row->invoice mapping just as
		// ambiguous. The column inference below uses only the targets:
		// refusal is scoped to the decision being made, so one populated
		// invoice whose number was edited after import cannot block its
		// recoverable siblings.
		count := map[string]int{}
		for _, inv := range all {
			count[inv.number]++
		}
		for _, inv := range targets {
			if count[inv.number] > 1 {
				out.DocumentsAmbiguous++
				out.Notes = append(out.Notes, fmt.Sprintf(
					"document %s: ambiguous (invoice number %q is shared by %d invoices)", docID, inv.number, count[inv.number]))
				return nil
			}
		}

		col, note := inferInvoiceNumberColumn(rows, targets)
		if note != "" {
			out.DocumentsAmbiguous++
			out.Notes = append(out.Notes, "document "+docID+": ambiguous ("+note+")")
			return nil
		}

		for _, inv := range targets {
			var matched []int
			for i, row := range rows {
				// The bound check also absorbs excelize's nil gap rows.
				if col < len(row) && row[col] == inv.number {
					matched = append(matched, sheetRow(i))
				}
			}
			// Zero is rejected BEFORE the equality test: a 0 == 0 agreement
			// must never authorize a write, and cardinality >= 1 would then
			// abort the tenant on a 23514.
			if len(matched) == 0 {
				out.InvoicesAmbiguous++
				out.Notes = append(out.Notes, fmt.Sprintf(
					"invoice %s (%q): ambiguous (no row carries its number)", inv.id, inv.number))
				continue
			}
			// Exact oracle, not a heuristic: buildCreateInput appends one
			// LineItemInput per grouped row and Store.Create inserts one
			// line_items row per entry. A PATCH carrying line_items replaces
			// the whole set (replaceLinesTx), which is what moves the count.
			if len(matched) != inv.lineItems {
				out.InvoicesAmbiguous++
				out.Notes = append(out.Notes, fmt.Sprintf(
					"invoice %s (%q): ambiguous (%d matched rows vs %d line items)", inv.id, inv.number, len(matched), inv.lineItems))
				continue
			}

			out.InvoicesRecoverable++
			if dryRun {
				continue
			}
			tag, err := tx.Exec(ctx,
				`UPDATE invoices SET source_rows = $1 WHERE id = $2 AND source_rows IS NULL`,
				matched, inv.id)
			if err != nil {
				return err
			}
			out.InvoicesWritten += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return err
	}

	res.DocumentsAmbiguous += out.DocumentsAmbiguous
	res.InvoicesRecoverable += out.InvoicesRecoverable
	res.InvoicesWritten += out.InvoicesWritten
	res.InvoicesAmbiguous += out.InvoicesAmbiguous
	res.Notes = append(res.Notes, out.Notes...)
	return nil
}

// documentInvoices loads every RLS-visible invoice backed by docID.
func documentInvoices(ctx context.Context, tx pgx.Tx, docID string) ([]backfillInvoice, error) {
	rows, err := tx.Query(ctx,
		`SELECT i.id, i.invoice_number, i.source_rows IS NULL,
		        (SELECT count(*) FROM line_items li WHERE li.invoice_id = i.id)
		 FROM invoices i
		 WHERE i.source_document_id = $1
		 ORDER BY i.id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []backfillInvoice
	for rows.Next() {
		var inv backfillInvoice
		if err := rows.Scan(&inv.id, &inv.number, &inv.needsRows, &inv.lineItems); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// inferInvoiceNumberColumn returns the one column carrying every target's
// invoice_number verbatim, or a refusal note. The comparison is exact-string:
// Service.Import groups on the RAW untrimmed cell and stores it verbatim, so
// a trimmed comparison would mis-key a whitespace-bearing number.
func inferInvoiceNumberColumn(rows [][]string, targets []backfillInvoice) (int, string) {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}

	best, bestCol, ties := 0, -1, 0
	for c := 0; c < width; c++ {
		covered := 0
		for _, inv := range targets {
			for _, row := range rows {
				if c < len(row) && row[c] == inv.number {
					covered++
					break
				}
			}
		}
		switch {
		case covered > best:
			best, bestCol, ties = covered, c, 1
		case covered == best && covered > 0:
			ties++
		}
	}

	if best < len(targets) {
		return -1, fmt.Sprintf("no column carries all %d invoice numbers (best covers %d)", len(targets), best)
	}
	if ties > 1 {
		return -1, fmt.Sprintf("%d columns each carry all %d invoice numbers", ties, len(targets))
	}
	return bestCol, ""
}
