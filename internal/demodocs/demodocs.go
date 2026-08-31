// Package demodocs gives the demo tenants' seeded invoices the source document
// they would have been imported from.
//
// db/seed.dev.sql INSERTs invoices directly and cannot reach object storage, so
// every seeded invoice has a NULL source_document_id and the source-document
// previewer has nothing to show. This writes the missing file: one CSV per
// supplier entity, one row per line item, and source_rows pointing at the rows
// that produced each invoice.
//
// The tenant allowlist is the safety boundary, not the environment. ENVIRONMENT
// reads "development" on production and forks verbatim (docs/deploy-model.md),
// so gating on it would be fail-open; gating on the four fixed uuids
// db/seed.dev.sql creates cannot reach a real tenant's data wherever it runs.
package demodocs

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DemoTenants are the only tenants this package will ever touch — the four
// db/seed.dev.sql creates. Not a parameter: a caller-supplied tenant is what
// would make "seed a synthetic document onto real invoices" representable.
var DemoTenants = []string{
	"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	"11111111-1111-1111-1111-111111111111",
	"22222222-2222-2222-2222-222222222222",
}

// csvHeader matches e2e/importFixtures.ts PERF_HEADER — the importer's
// auto-recognised column names, so the seeded file is one the real import path
// would accept.
const csvHeader = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price"

// firstDataSheetRow mirrors importer.sheetRow: row 1 is the header, so the
// first data row is sheet row 2. The invoices_source_rows_are_sheet_rows CHECK
// enforces the same floor.
const firstDataSheetRow = 2

// StoreFunc is document.Service.Store. Taking the method rather than the
// service keeps this package off internal/document's object-storage half.
type StoreFunc func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, bool, error)

type Result struct {
	DocumentsStored int
	InvoicesLinked  int
	// EligibleRows and Note exist for the boot log. "Did nothing" has several
	// distinct causes -- absent tenant, no admin to attribute the upload to,
	// nothing left unlinked -- and they are not interchangeable when this is
	// running unattended on a deploy.
	EligibleRows int
	Note         string
}

// lineRow is one row of the generated file: an invoice's field values repeated
// per line item, exactly as a real export would repeat them.
type lineRow struct {
	entityName    string
	invoiceID     string
	invoiceNumber string
	issueDate     string
	buyerTIN      string
	buyerName     string
	currency      string
	subtotal      string
	vat           string
	total         string
	itemDesc      string
	qty           string
	unitPrice     string
}

// Seed attaches a source document to every demo invoice that has line items and
// no document yet. Invoices with no line items are left alone on purpose: a row
// in an import file IS a line item, so an invoice without one was never in a
// file. That keeps the "no source document" state represented in demo data
// (DEMO-2026-5005 is the seed's own example).
//
// Idempotent twice over — the WHERE source_document_id IS NULL filter empties
// on a second run, and identical bytes resolve to the existing documents row
// through the (tenant_id, content_hash) unique index.
func Seed(ctx context.Context, pool *pgxpool.Pool, store StoreFunc, logger *slog.Logger) (Result, error) {
	var total Result
	var failed []string
	for _, tenantID := range DemoTenants {
		res, err := seedTenant(ctx, pool, store, tenantID)
		total.DocumentsStored += res.DocumentsStored
		total.InvoicesLinked += res.InvoicesLinked
		total.EligibleRows += res.EligibleRows

		// Every tenant reports, including the ones that did nothing. A boot step
		// that is silent on a no-op is indistinguishable from one that never ran,
		// which is exactly the ambiguity this cost an afternoon to resolve once.
		if logger != nil {
			logger.Info("demodocs: tenant scanned",
				"tenant", tenantID, "outcome", res.Note, "eligible_rows", res.EligibleRows,
				"documents", res.DocumentsStored, "invoices", res.InvoicesLinked, "error", err)
		}
		// One tenant's failure must not cost the others theirs -- object storage
		// or a single malformed row should degrade, not abort.
		if err != nil {
			failed = append(failed, tenantID)
		}
	}
	if len(failed) > 0 {
		return total, fmt.Errorf("demodocs: %d of %d tenants failed: %s",
			len(failed), len(DemoTenants), strings.Join(failed, ", "))
	}
	return total, nil
}

func seedTenant(ctx context.Context, pool *pgxpool.Pool, store StoreFunc, tenantID string) (Result, error) {
	// The actor is a real admin of THIS tenant, not a synthetic "seeder"
	// subject: document.Store.Upsert records it on the document.created audit
	// row, which is the only source the previewer's "Uploaded by" reads
	// (internal/invoice/source_document.go). A made-up uuid there would render
	// as a fabricated uploader.
	actor, err := tenantAdmin(ctx, pool, tenantID)
	if err != nil {
		return Result{Note: "admin lookup failed"}, err
	}
	if actor == "" {
		return Result{Note: "no admin membership; nothing to attribute an upload to"}, nil
	}
	ctx = auth.WithIdentity(ctx, auth.Identity{Subject: actor, Role: "authenticated", TenantID: tenantID})

	rows, err := pendingRows(ctx, pool)
	if err != nil {
		return Result{Note: "pending-row query failed"}, err
	}
	if len(rows) == 0 {
		return Result{Note: "no unlinked invoice with line items"}, nil
	}

	res := Result{EligibleRows: len(rows), Note: "seeded"}
	for _, group := range groupByEntity(rows) {
		body, sheetRows := buildCSV(group.rows)
		doc, _, err := store(ctx, filenameFor(group.entityName), "text/csv", int64(len(body)), bytes.NewReader(body))
		if err != nil {
			res.Note = "storing " + group.entityName + "'s file failed"
			return res, err
		}
		res.DocumentsStored++

		linked, err := linkInvoices(ctx, pool, doc.ID, sheetRows)
		if err != nil {
			res.Note = "linking " + group.entityName + "'s invoices failed"
			return res, err
		}
		res.InvoicesLinked += linked
	}
	return res, nil
}

// tenantAdmin returns an active admin member's subject, or "" when the tenant
// has none — an absent demo tenant is skipped, not an error, because
// db/seed.dev.sql creates all four but only populates some.
//
// status = 'active' is load-bearing, not tidiness: seedTenant runs as this
// subject through the gated WithinRequestTenantTx, which refuses a suspended or
// invited caller (TestRLS_DemoDocsPrefersAnActiveAdminOverASuspendedOne).
func tenantAdmin(ctx context.Context, pool *pgxpool.Pool, tenantID string) (string, error) {
	var subject string
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT user_id::text FROM memberships
			  WHERE tenant_id = $1 AND role = 'admin' AND status = 'active'
			  ORDER BY user_id LIMIT 1`, tenantID,
		).Scan(&subject)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return subject, nil
}

// pendingRows reads every document-less invoice's line items, ordered so the
// generated file is byte-identical across runs — the content-hash dedup is only
// idempotent if the bytes are stable.
func pendingRows(ctx context.Context, pool *pgxpool.Pool) ([]lineRow, error) {
	var out []lineRow
	err := db.WithinRequestTenantTx(ctx, pool, func(tx pgx.Tx) error {
		// EVERY content column is coalesced, not just the two that looked
		// obviously optional. invoices and line_items store invalid data
		// faithfully -- "MBS-content: NULLABLE, no CHECK (store-invalid)" in
		// both migrations -- so issue_date, currency, the three money columns and
		// all three line-item columns are nullable in practice, and production
		// residue carries NULLs the seeded fixtures never do. An uncoalesced
		// column aborts the whole tenant on the row scan
		// (TestRLS_DemoDocsHandlesInvoicesWithNullContentColumns).
		//
		// '' is also the honest CSV rendering of an absent value: the file an
		// import would have produced has an empty cell there, not the string
		// "NULL" and not a dropped column.
		rows, err := tx.Query(ctx,
			`SELECT coalesce(e.name, ''), i.id::text, i.invoice_number,
			        coalesce(to_char(i.issue_date, 'YYYY-MM-DD'), ''),
			        coalesce(i.buyer_tin, ''), coalesce(i.buyer_name, ''), coalesce(i.currency, ''),
			        coalesce(i.subtotal::text, ''), coalesce(i.vat::text, ''), coalesce(i.total::text, ''),
			        coalesce(li.description, ''), coalesce(li.quantity::text, ''), coalesce(li.unit_price::text, '')
			   FROM invoices i
			   JOIN business_entities e ON e.id = i.entity_id
			   JOIN line_items li ON li.invoice_id = i.id
			  WHERE i.source_document_id IS NULL
			  ORDER BY e.name, i.invoice_number, li.line_no`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r lineRow
			if err := rows.Scan(&r.entityName, &r.invoiceID, &r.invoiceNumber, &r.issueDate,
				&r.buyerTIN, &r.buyerName, &r.currency, &r.subtotal, &r.vat, &r.total,
				&r.itemDesc, &r.qty, &r.unitPrice); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

type entityGroup struct {
	entityName string
	rows       []lineRow
}

// groupByEntity splits the rows into one file per supplier, preserving the
// query's order. One CSV per entity rather than per tenant because a real
// import file comes from one supplier's export.
func groupByEntity(rows []lineRow) []entityGroup {
	var out []entityGroup
	for _, r := range rows {
		if n := len(out); n > 0 && out[n-1].entityName == r.entityName {
			out[n-1].rows = append(out[n-1].rows, r)
			continue
		}
		out = append(out, entityGroup{entityName: r.entityName, rows: []lineRow{r}})
	}
	return out
}

// buildCSV renders the file and reports, per invoice id, the sheet rows it
// occupies. The two are produced together so the numbers can never drift from
// the bytes they describe.
func buildCSV(rows []lineRow) ([]byte, map[string][]int) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(strings.Split(csvHeader, ","))

	sheetRows := make(map[string][]int, len(rows))
	for i, r := range rows {
		_ = w.Write([]string{
			r.invoiceNumber, r.issueDate, r.buyerTIN, r.buyerName, r.currency,
			r.subtotal, r.vat, r.total, r.itemDesc, r.qty, r.unitPrice,
		})
		sheetRows[r.invoiceID] = append(sheetRows[r.invoiceID], i+firstDataSheetRow)
	}
	w.Flush()
	return buf.Bytes(), sheetRows
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func filenameFor(entityName string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(entityName), "-"), "-")
	if slug == "" {
		slug = "supplier"
	}
	return slug + "-invoices.csv"
}

// linkInvoices claims each invoice for the document. The IS NULL predicate is
// repeated here, not just in pendingRows: it makes a concurrent second seeder a
// no-op rather than a last-writer-wins overwrite.
func linkInvoices(ctx context.Context, pool *pgxpool.Pool, documentID string, sheetRows map[string][]int) (int, error) {
	var linked int
	err := db.WithinRequestTenantTx(ctx, pool, func(tx pgx.Tx) error {
		for invoiceID, srcRows := range sheetRows {
			tag, err := tx.Exec(ctx,
				`UPDATE invoices SET source_document_id = $1, source_rows = $2
				  WHERE id = $3 AND source_document_id IS NULL`,
				documentID, srcRows, invoiceID)
			if err != nil {
				return err
			}
			linked += int(tag.RowsAffected())
		}
		return nil
	})
	return linked, err
}
