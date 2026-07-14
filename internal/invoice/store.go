package invoice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store persists invoices/line_items/invoice_status_history rows as the
// invoice_app role. It holds the app-role pool (DATABASE_URL); every method
// wraps db.WithinRequestTenantTx, so the app.current_tenant GUC is set for the
// transaction and RLS enforces isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// scanner is the common Scan(...) surface of both pgx.Row (QueryRow) and
// pgx.Rows (Query iteration), so scanInvoice/scanLineItem serve single-row and
// multi-row callers without duplication.
type scanner interface {
	Scan(dest ...any) error
}

// invoiceColumns is the invoices projection shared by every SELECT/RETURNING,
// scanned in order by scanInvoice. The numeric(14,2) money columns are read via
// a ::text cast ([D13]) so an exact decimal (incl. store-invalid negatives)
// round-trips into a *string and a NULL scans into a nil *string — never a
// float64 or pgtype.Numeric. status/violations scan straight into the named
// Status type / json.RawMessage (pgx v5 resolves the underlying kind; the
// validation store relies on the same).
const invoiceColumns = `id, entity_id, import_batch_id, invoice_number, status, ` +
	`issue_date, supplier_tin, supplier_name, buyer_tin, buyer_name, ` +
	`currency, subtotal::text, vat::text, total::text, ` +
	`violations, rule_set_version_id, created_at`

func scanInvoice(row scanner, inv *Invoice) error {
	return row.Scan(
		&inv.ID, &inv.EntityID, &inv.ImportBatchID, &inv.InvoiceNumber, &inv.Status,
		&inv.IssueDate, &inv.SupplierTIN, &inv.SupplierName, &inv.BuyerTIN, &inv.BuyerName,
		&inv.Currency, &inv.Subtotal, &inv.VAT, &inv.Total,
		&inv.Violations, &inv.RuleSetVersionID, &inv.CreatedAt,
	)
}

// lineItemColumns is the line_items projection scanned by scanLineItem; the
// numeric columns are read via ::text ([D13]), same rationale as invoiceColumns.
const lineItemColumns = `id, line_no, description, ` +
	`quantity::text, unit_price::text, line_total::text, line_tax::text`

func scanLineItem(row scanner, li *LineItem) error {
	return row.Scan(
		&li.ID, &li.LineNo, &li.Description,
		&li.Quantity, &li.UnitPrice, &li.LineTotal, &li.LineTax,
	)
}

// Create inserts one invoice and, in the SAME db.WithinRequestTenantTx closure
// and in this order: (1) the invoices row (tenant_id from the caller's identity,
// status left to the column DEFAULT 'draft', MBS-content passed through
// un-rejected incl. NULL/negative — store-invalid-faithfully, AC-6); (2) one
// line_items row per CreateInput.LineItems entry with a system-assigned line_no
// = 1..N by array position ([D10]); (3) the genesis invoice_status_history row
// (from_status NULL -> to_status 'draft', actor = the caller's Subject, [D5]);
// (4) an "invoice.created" audit.Record. Because all four writes share one
// transaction, a later failure rolls the earlier ones back too (INV-STORE-07).
//
// Only the invoices INSERT's pg error is mapped: a unique_violation (23505) on
// invoices_tenant_entity_number_uq -> ErrDuplicateNumber, a foreign_key_violation
// (23503, non-existent entity_id) or an invalid_text_representation (22P02,
// entity_id is the only uuid-typed input here, so a malformed non-empty value
// unambiguously means a bad entity_id) -> ErrValidation. The line_items/history/
// audit errors propagate raw so their SQLSTATE (e.g. the actor CHECK's 23514) is
// not masked -- the atomicity specs assert on it.
//
// EntityID/InvoiceNumber are required non-empty ([D10]); an empty value is
// rejected as ErrValidation BEFORE any tx opens, mirroring Update's all-nil
// pre-tx guard -- this also completes the contract for the importer-reuse path
// ([D3]), since the HTTP layer is not the only caller.
//
// Numeric inputs are bound as $N::text::numeric: the innermost ::text pins the
// wire parameter type to text so pgx encodes the *string (pgx's NumericCodec has
// no string encode plan), then Postgres casts text->numeric.
func (s *Store) Create(ctx context.Context, in CreateInput) (Invoice, error) {
	if in.EntityID == "" || in.InvoiceNumber == "" {
		return Invoice{}, fmt.Errorf("%w: entity_id and invoice_number are required", ErrValidation)
	}

	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The identity is guaranteed present here: WithinRequestTenantTx already
		// resolved it (as the tenant id) before this closure ran, returning
		// db.ErrNoTenant otherwise.
		id, _ := auth.IdentityFromContext(ctx)

		if err := scanInvoice(tx.QueryRow(ctx,
			`INSERT INTO invoices
			   (tenant_id, entity_id, invoice_number,
			    issue_date, supplier_tin, supplier_name, buyer_tin, buyer_name,
			    currency, subtotal, vat, total)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
			         $10::text::numeric, $11::text::numeric, $12::text::numeric)
			 RETURNING `+invoiceColumns,
			id.TenantID, in.EntityID, in.InvoiceNumber,
			in.IssueDate, in.SupplierTIN, in.SupplierName, in.BuyerTIN, in.BuyerName,
			in.Currency, in.Subtotal, in.VAT, in.Total,
		), &inv); err != nil {
			switch pgCode(err) {
			case "23505":
				return ErrDuplicateNumber
			case "23503", "22P02":
				return ErrValidation
			}
			return err
		}

		for i, li := range in.LineItems {
			var item LineItem
			if err := scanLineItem(tx.QueryRow(ctx,
				`INSERT INTO line_items
				   (tenant_id, invoice_id, line_no, description,
				    quantity, unit_price, line_total, line_tax)
				 VALUES ($1, $2, $3, $4,
				         $5::text::numeric, $6::text::numeric, $7::text::numeric, $8::text::numeric)
				 RETURNING `+lineItemColumns,
				id.TenantID, inv.ID, i+1, li.Description,
				li.Quantity, li.UnitPrice, li.LineTotal, li.LineTax,
			), &item); err != nil {
				return err
			}
			inv.LineItems = append(inv.LineItems, item)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
			 VALUES ($1, $2, NULL, $3, $4)`,
			id.TenantID, inv.ID, string(inv.Status), id.Subject,
		); err != nil {
			return err
		}

		return audit.Record(ctx, tx, id.Subject, "invoice.created", map[string]any{"id": inv.ID})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// Get returns the invoice by id with its line_items hydrated (ordered by line_no,
// [D7]) inside one db.WithinRequestTenantTx. RLS scopes the row set to the
// caller's tenant, so a cross-tenant (or genuinely absent) id 0-rows;
// pgx.ErrNoRows maps to ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id,
		), &inv); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		rows, err := tx.Query(ctx,
			`SELECT `+lineItemColumns+` FROM line_items WHERE invoice_id = $1 ORDER BY line_no ASC`, inv.ID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item LineItem
			if err := scanLineItem(rows, &item); err != nil {
				return err
			}
			inv.LineItems = append(inv.LineItems, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// List returns the caller's tenant's invoice HEADERS (LineItems left nil, [D7]),
// ordered created_at DESC, id DESC (deterministic), paginated by f.Limit/f.Offset,
// plus the total tenant-scoped count for the pagination envelope. No filters
// ([D8]). RLS (not a manual WHERE tenant_id) scopes both the COUNT and the page.
// An empty result is []Invoice{}, never a nil slice.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
	items := []Invoice{}
	var total int
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx,
			`SELECT `+invoiceColumns+`
			 FROM invoices
			 ORDER BY created_at DESC, id DESC
			 LIMIT $1 OFFSET $2`, f.Limit, f.Offset,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var inv Invoice
			if err := scanInvoice(rows, &inv); err != nil {
				return err
			}
			items = append(items, inv)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Update applies only in's non-nil MBS-content fields to an invoices row and
// writes an "invoice.updated" audit row in the same transaction. An all-nil in
// is rejected as ErrValidation BEFORE any tx opens (a no-op UPDATE is forbidden,
// [D9]). It never touches status/violations/line_items -- status is Transition's
// sole province (M4-02-02), violations is M4-04's. Zero rows affected
// (cross-tenant id, RLS-invisible) maps to ErrNotFound. Numeric inputs are bound
// as $N::text::numeric, same rationale as Create.
func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Invoice, error) {
	if in.IssueDate == nil && in.SupplierTIN == nil && in.SupplierName == nil &&
		in.BuyerTIN == nil && in.BuyerName == nil && in.Currency == nil &&
		in.Subtotal == nil && in.VAT == nil && in.Total == nil {
		return Invoice{}, fmt.Errorf("%w: no fields to update", ErrValidation)
	}

	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		var setClauses []string
		var args []any
		var changedFields []string

		set := func(col, placeholder string, val any) {
			args = append(args, val)
			setClauses = append(setClauses, fmt.Sprintf(placeholder, col, len(args)))
			changedFields = append(changedFields, col)
		}
		const text = "%s = $%d"
		const numeric = "%s = $%d::text::numeric"

		if in.IssueDate != nil {
			set("issue_date", text, *in.IssueDate)
		}
		if in.SupplierTIN != nil {
			set("supplier_tin", text, *in.SupplierTIN)
		}
		if in.SupplierName != nil {
			set("supplier_name", text, *in.SupplierName)
		}
		if in.BuyerTIN != nil {
			set("buyer_tin", text, *in.BuyerTIN)
		}
		if in.BuyerName != nil {
			set("buyer_name", text, *in.BuyerName)
		}
		if in.Currency != nil {
			set("currency", text, *in.Currency)
		}
		if in.Subtotal != nil {
			set("subtotal", numeric, *in.Subtotal)
		}
		if in.VAT != nil {
			set("vat", numeric, *in.VAT)
		}
		if in.Total != nil {
			set("total", numeric, *in.Total)
		}

		args = append(args, id)
		query := fmt.Sprintf(
			`UPDATE invoices SET %s WHERE id = $%d RETURNING `+invoiceColumns,
			strings.Join(setClauses, ", "), len(args),
		)

		if err := scanInvoice(tx.QueryRow(ctx, query, args...), &inv); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		return audit.Record(ctx, tx, callerID.Subject, "invoice.updated", map[string]any{
			"id":     inv.ID,
			"fields": changedFields,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}
