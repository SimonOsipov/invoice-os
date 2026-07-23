package invoice

import (
	"context"
	"encoding/json"
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

// historyColumns deliberately excludes id/tenant_id/invoice_id (AC #7) --
// StatusChange surfaces only from_status/to_status/actor/changed_at.
const historyColumns = `from_status, to_status, actor, changed_at`

func scanStatusChange(row scanner, sc *StatusChange) error {
	return row.Scan(&sc.FromStatus, &sc.ToStatus, &sc.Actor, &sc.ChangedAt)
}

// Create inserts one invoice and, in the SAME db.WithinRequestTenantTx closure
// and in this order: (0) a tenant-scoped ownership pre-check on entity_id
// (M4-06-03 -- mirrors the importer's EntitySupplier idiom,
// internal/importer/store.go, and closes the direct-path gap the "22P02 does
// not disambiguate" note below used to accept: a cross-tenant OR nonexistent
// entity_id now returns ErrValidation HERE, before any row is written); (1)
// the invoices row (tenant_id from the caller's identity, status left to the
// column DEFAULT 'draft', MBS-content passed through un-rejected incl.
// NULL/negative — store-invalid-faithfully, AC-6); (2) one line_items row per
// CreateInput.LineItems entry with a system-assigned line_no = 1..N by array
// position ([D10]); (3) the genesis invoice_status_history row (from_status
// NULL -> to_status 'draft', actor = the caller's Subject, [D5]); (4) an
// "invoice.created" audit.Record. Because all these writes share one
// transaction, a later failure rolls the earlier ones back too (INV-STORE-07).
//
// The pre-check is a friendly early exit, not the enforcement mechanism: the
// composite (tenant_id, entity_id) FK (invoices_tenant_entity_fk, added
// alongside this pre-check by M4-06-03) is the DB-authoritative backstop, so
// a cross-tenant entity_id is rejected even for a caller that bypassed the
// pre-check (e.g. a race against a concurrent entity delete).
//
// The pre-check query and the invoices INSERT are the only pg errors mapped: a
// unique_violation (23505) on invoices_tenant_entity_number_uq -> ErrDuplicateNumber
// (INSERT only), a foreign_key_violation (23503, a non-existent entity_id or
// import_batch_id -- the pre-check turns the entity_id case into ErrValidation
// earlier via the exists=false branch above, so this INSERT-time 23503 in practice
// now only fires for import_batch_id) or an invalid_text_representation (22P02, a
// malformed entity_id/import_batch_id uuid, OR a malformed numeric MBS-content
// value; the pre-check maps its own 22P02 the same way for entity_id) ->
// ErrValidation. 22P02 at the INSERT does not disambiguate which input was bad; the
// importer avoids this ambiguity by pre-validating entity_id itself and
// quarantining the row on ANY Create error. The line_items/history/audit errors
// propagate raw so their SQLSTATE (e.g. the actor CHECK's 23514) is not masked --
// the atomicity specs assert on it.
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

		// Tenant-scoped ownership pre-check: RLS scopes this SELECT to the
		// caller's tenant (same mechanism EntitySupplier relies on,
		// internal/importer/store.go), so a foreign OR nonexistent entity_id
		// both come back exists=false. This rejects the cross-tenant case
		// EARLY, as a friendly ErrValidation with NO row written and NO audit
		// row -- the composite (tenant_id, entity_id) FK below is the
		// DB-authoritative backstop (see this func's doc comment; M4-06-03
		// closes the direct-path gap noted there).
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM business_entities WHERE id = $1)`, in.EntityID,
		).Scan(&exists); err != nil {
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}
		if !exists {
			return ErrValidation
		}

		if err := scanInvoice(tx.QueryRow(ctx,
			`INSERT INTO invoices
			   (tenant_id, entity_id, invoice_number,
			    issue_date, supplier_tin, supplier_name, buyer_tin, buyer_name,
			    currency, subtotal, vat, total, import_batch_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
			         $10::text::numeric, $11::text::numeric, $12::text::numeric, $13)
			 RETURNING `+invoiceColumns,
			id.TenantID, in.EntityID, in.InvoiceNumber,
			in.IssueDate, in.SupplierTIN, in.SupplierName, in.BuyerTIN, in.BuyerName,
			in.Currency, in.Subtotal, in.VAT, in.Total, in.ImportBatchID,
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
// caller's tenant, so a cross-tenant (or genuinely absent) VALID uuid 0-rows;
// pgx.ErrNoRows maps to ErrNotFound. A malformed (non-uuid) id raises 22P02
// (invalid_text_representation), mapped to ErrValidation -- mirrors Create's
// entity_id mapping (CodeRabbit finding, M4-02 PR review).
func (s *Store) Get(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id,
		), &inv); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
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
		if err := rows.Err(); err != nil {
			return err
		}

		// Resolve the human-facing rule_set_versions.version int onto the
		// transient inv.RuleSetVersion (M4-09-01, [read-shape-via-subselect]):
		// a correlated scalar SELECT, not a join (a join would make the bare
		// `id` column ambiguous against invoiceColumns/scanInvoice, shared by
		// six other writers). Nil when rule_set_version_id IS NULL (never
		// validated); rule_set_versions is a global table with GRANT SELECT
		// TO invoice_app, so this is RLS-safe inside the app-pool tx.
		if inv.RuleSetVersionID != nil {
			var v int
			if err := tx.QueryRow(ctx,
				`SELECT version FROM rule_set_versions WHERE id = $1`, *inv.RuleSetVersionID,
			).Scan(&v); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			} else {
				inv.RuleSetVersion = &v
			}
		}

		return nil
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// History returns the caller's tenant's invoice_status_history rows for id,
// ordered changed_at ASC, id ASC ([D1]/AC #1), inside one
// db.WithinRequestTenantTx -- the invoice_app pool, never superuser.
//
// Unlike Get's single-row tx.QueryRow (where pgx.ErrNoRows maps directly to
// ErrNotFound), this is a multi-row tx.Query: Query()/Next() never yields
// pgx.ErrNoRows for a zero-row result, so a cross-tenant or unknown id needs
// an explicit post-query check instead. That check is sound only because
// Store.Create always writes the genesis row in the same transaction as the
// invoice insert -- "zero history rows" therefore always means "not visible
// to this caller," never "a real invoice with no history yet."
//
// A malformed (non-uuid) id raises 22P02 at Postgres, surfaced via
// rows.Err() after the Next() loop (not tx.Query()'s own error, which only
// covers client-side encoding) -- mapped to ErrValidation like
// Get/Update/Transition, not ErrNotFound.
func (s *Store) History(ctx context.Context, id string) ([]StatusChange, error) {
	var result []StatusChange
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+historyColumns+`
			 FROM invoice_status_history
			 WHERE invoice_id = $1
			 ORDER BY changed_at ASC, id ASC`, id,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sc StatusChange
			if err := scanStatusChange(rows, &sc); err != nil {
				return err
			}
			result = append(result, sc)
		}
		if err := rows.Err(); err != nil {
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		if len(result) == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// List returns the caller's tenant's invoice HEADERS (LineItems left nil, [D7]),
// ordered created_at DESC, id DESC (deterministic), paginated by f.Limit/f.Offset,
// plus the total tenant-scoped count for the pagination envelope. RLS (not a
// manual WHERE tenant_id) scopes both the COUNT and the page. An empty result
// is []Invoice{}, never a nil slice.
//
// f.NeedsAttention (M4-09-02) is the one predicate filter ([D8]): when true, a
// WHERE clause is applied to BOTH the COUNT and the page query, copied
// VERBATIM from the dashboard rollup's own count(*) FILTER predicate
// (internal/dashboard/store.go Rollup, alias dropped -- List has no join) so
// the two surfaces can never drift apart ([needs-attention-drift-guard],
// TestStoreList_NeedsAttentionMatchesDashboardRollup). The predicate carries
// NO bind params of its own, so it does not disturb LIMIT/OFFSET's $1/$2.
// When false (the zero value / omitted), `where` is empty and both queries
// are byte-identical to before this filter existed.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
	items := []Invoice{}
	var total int
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		where := ""
		if f.NeedsAttention {
			where = ` WHERE (status IN ('rejected', 'failed') OR (status = 'draft' AND violations @> '[{"severity": "error"}]'::jsonb))`
		}

		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices`+where).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx,
			`SELECT `+invoiceColumns+`
			 FROM invoices`+where+`
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
// (cross-tenant VALID uuid, RLS-invisible) maps to ErrNotFound; a malformed
// (non-uuid) id raises 22P02, mapped to ErrValidation (CodeRabbit finding,
// mirrors Get/Create). Numeric inputs are bound as $N::text::numeric, same
// rationale as Create.
//
// A thin wrapper over updateContentTx (M4-05-02 extraction, [content-write-
// extraction]): the guard/tx/audit shell stays HERE, byte-identical to before
// the extraction; the SET-clause build/query/scan/error-map moved verbatim
// into updateContentTx so Store.Edit's fix-loop can reuse it without an
// audit write of its own (Edit's audit is conditional on a real content
// change, which Update's is not, [D9]).
func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Invoice, error) {
	if in.IssueDate == nil && in.SupplierTIN == nil && in.SupplierName == nil &&
		in.BuyerTIN == nil && in.BuyerName == nil && in.Currency == nil &&
		in.Subtotal == nil && in.VAT == nil && in.Total == nil {
		return Invoice{}, fmt.Errorf("%w: no fields to update", ErrValidation)
	}

	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		var changedFields []string
		var err error
		inv, changedFields, err = updateContentTx(ctx, tx, id, in)
		if err != nil {
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

// updateContentTx is the tx-scoped CONTENT write shared by Store.Update and
// Store.Edit (M4-05-02 extraction from Store.Update): it builds the dynamic
// SET clause over in's non-nil fields, runs the UPDATE ... RETURNING, and
// maps the same errors Update always has (pgx.ErrNoRows -> ErrNotFound,
// 22P02 -> ErrValidation). It does NO audit write and NO all-nil guard --
// both callers enforce the guard themselves before opening a tx, and each
// writes its own audit row under its own conditions (Update always; Edit
// only when the DB-authoritative fingerprint says something really changed).
// Assumes at least one field in in is non-nil.
func updateContentTx(ctx context.Context, tx pgx.Tx, id string, in UpdateInput) (Invoice, []string, error) {
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

	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx, query, args...), &inv); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, nil, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return Invoice{}, nil, ErrValidation
		}
		return Invoice{}, nil, err
	}

	return inv, changedFields, nil
}

// Edit is M4-05-02's fix-loop orchestrator (System Design §4): the edit +
// validated->draft demotion sequence over the fixable states (draft,
// validated), composed with Store.ApplyValidation's template ([A2]: one
// WithinRequestTenantTx, lock-then-recheck, propagate raw errors so their
// SQLSTATE survives). Inside ONE db.WithinRequestTenantTx:
//
//  1. all-nil guard (checked BEFORE any tx opens, mirroring Store.Update's
//     own guard, [A7]) -- ErrValidation.
//  2. lock+read `before`: SELECT <invoiceColumns> ... FOR UPDATE, same lock
//     and error mapping as ApplyValidation/Transition (pgx.ErrNoRows ->
//     ErrNotFound; 22P02 -> ErrValidation).
//  3. fixable-state guard -- before.Status is neither draft nor validated ->
//     ErrNotFixable, NOTHING written. This runs BEFORE the content write, so
//     a not-fixable status wins over a malformed numeric in the same call
//     ([A8], GuardBeforeContentValidation).
//  4. preFP := contentFingerprint(before) -- taken on the LOCKED row, so it
//     is authoritative under concurrency the same way ApplyValidation's
//     re-check is.
//  5. updateContentTx writes the content (shared with Store.Update, no audit
//     of its own).
//  6. DB-authoritative no-op check: contentFingerprint(after) == preFP means
//     nothing really changed (either every field was resent unchanged, or
//     only its NUMERIC SCALE changed and Postgres normalized it away, e.g.
//     "100.00"->"100.0") -- return `after` with no audit, no demotion, no
//     history row ([A6]: idempotence applies to draft AND validated).
//  7. audit.Record("invoice.updated") -- a real content change, always.
//  8. demote iff `before` was validated: transitionTx(validated->draft) on
//     THIS same tx, so the content write and the demotion are one atomic
//     unit -- a failure at either step (including this audit's own actor
//     CHECK) rolls back the whole edit, never a partial one
//     (ContentAuditFailureRollsBackWholeEdit). A draft `before` has nothing
//     to demote from and stays draft.
//  9. return `after` -- draft (demoted) after a validated content change,
//     the demoted row's OWN state after a draft content change, or the
//     no-op return from step 6 (a validated no-op stays validated).
//
// Edit never touches violations/rule_set_version_id -- a demotion leaves the
// prior verdict's stamp deliberately STALE until Store.ApplyValidation
// re-runs and re-stamps it (DemoteThenRevalidateSucceeds closes that loop
// end to end through the gate, completely unmodified by M4-05, [A12]).
func (s *Store) Edit(ctx context.Context, id string, in UpdateInput) (Invoice, error) {
	if in.IssueDate == nil && in.SupplierTIN == nil && in.SupplierName == nil &&
		in.BuyerTIN == nil && in.BuyerName == nil && in.Currency == nil &&
		in.Subtotal == nil && in.VAT == nil && in.Total == nil {
		return Invoice{}, fmt.Errorf("%w: no fields to update", ErrValidation)
	}

	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		// 2. lock+read the full row -- the fingerprint and the fixable-state
		// guard both need it.
		var before Invoice
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, id,
		), &before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		// 3. fixable-state guard -- BEFORE the content write, so it wins over
		// a malformed numeric ([A8]).
		if before.Status != StatusDraft && before.Status != StatusValidated {
			return ErrNotFixable
		}

		// 4. the locked row's fingerprint, taken before the write.
		preFP := contentFingerprint(before)

		// 5. the content write, shared with Store.Update.
		after, changed, err := updateContentTx(ctx, tx, id, in)
		if err != nil {
			return err
		}

		// 6. DB-authoritative no-op check -- nothing to audit, demote, or
		// record history for.
		if contentFingerprint(after) == preFP {
			inv = after
			return nil
		}

		// 7. the content change is real -- audit it.
		if err := audit.Record(ctx, tx, callerID.Subject, "invoice.updated", map[string]any{
			"id":     id,
			"fields": changed,
		}); err != nil {
			return err
		}

		// 8. demote iff `before` was validated -- a draft has nothing to
		// demote from.
		if before.Status == StatusValidated {
			if after, err = transitionTx(ctx, tx, id, StatusValidated, StatusDraft, actorFromContext(ctx)); err != nil {
				return err
			}
		}

		inv = after
		return nil
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// legalTransitions is the SINGLE source of truth for the invoice lifecycle
// state machine ([D1], [D11] -- no generic FSM framework, Simplicity First):
// forward-only in M4-02 -- 7 edges, 3 terminals (accepted/rejected/failed have
// no outgoing edge, so they are simply absent as map keys). M4-05 adds the
// first recovery edge, validated->draft (the fix-loop demotion: editing a
// validated invoice sends it back to draft for re-validation). M5-04-02 adds
// queued->failed -- 8 edges now -- the dead-letter path for a background
// worker that gives up on an invoice before it ever reaches submitted; unlike
// validated->draft this is a forward FAILURE edge, not a recovery edge (it
// has no reverse). Remaining recovery/retry edges (e.g. rejected->draft,
// failed->queued) are added by the consumer stories that DRIVE them (M5-05),
// never speculatively here.
var legalTransitions = map[Status][]Status{
	StatusDraft:     {StatusValidated},
	StatusValidated: {StatusQueued, StatusDraft},
	StatusQueued:    {StatusSubmitted, StatusFailed},
	StatusSubmitted: {StatusAccepted, StatusRejected, StatusFailed},
}

// canTransition reports whether target is a legal next state from from, per
// legalTransitions.
func canTransition(from, target Status) bool {
	for _, s := range legalTransitions[from] {
		if s == target {
			return true
		}
	}
	return false
}

// Transition is the PUBLIC, request-scoped status change (M4-02-02, System
// Design [D1]/[D2]/[D4]/[D11]) and one of transitionTx's exactly two callers
// (M4-04-05's extraction moved the SOLE-writer-of-invoices.status role down
// to transitionTx, which both callers must go through; Transition's own
// observable behaviour is unchanged). Inside ONE db.WithinRequestTenantTx
// closure:
// SELECT status FROM invoices WHERE id=$1 FOR UPDATE locks and reads the
// current status (RLS-scoped, so a cross-tenant VALID uuid 0-rows same as a
// genuinely nonexistent one; pgx.ErrNoRows -> ErrNotFound; a malformed
// non-uuid id raises 22P02, mapped to ErrValidation, mirroring Get/Update/
// Create -- CodeRabbit finding) -> a no-op (current==target)
// -> ErrRedundantTransition (checked FIRST, [D4], before legality, and so
// retained HERE rather than in transitionTx) -> then transitionTx on this
// same tx: an edge outside legalTransitions -> ErrIllegalTransition -> else
// UPDATE status, INSERT invoice_status_history (from_status=current,
// to_status=target, actor=Subject), and audit.Record("invoice.transitioned",
// {id,from,to}, [D6]) -- all in one transaction, so a later failure rolls the
// earlier writes back too (INV-SM-05). The FOR UPDATE row lock serializes concurrent
// transitions on the same invoice (INV-SM-06): a losing goroutine blocks on
// the lock, then observes the winner's already-applied status and resolves
// to ErrRedundantTransition.
//
// The history/audit INSERTs are NOT actor-length pre-validated -- the
// atomicity specs rely on the real CHECK constraints firing (an empty Subject
// fails invoice_status_history's char_length(actor)>0; a 256-char Subject
// passes that but fails audit_log's char_length(actor)<=255) and propagate
// raw so their SQLSTATE (23514) is not masked, mirroring Create's write-order
// handling.
func (s *Store) Transition(ctx context.Context, id string, target Status) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var current Status
		if err := tx.QueryRow(ctx,
			`SELECT status FROM invoices WHERE id = $1 FOR UPDATE`, id,
		).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		// Redundancy is checked BEFORE legality ([D4]) and therefore stays
		// HERE, above transitionTx — which owns the legality guard.
		if current == target {
			return ErrRedundantTransition
		}

		var err error
		inv, err = transitionTx(ctx, tx, id, current, target, actorFromContext(ctx))
		return err
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// transitionTx is the tx-scoped TAIL of every status change: the legality
// guard, the invoices.status UPDATE, the invoice_status_history INSERT and
// the "invoice.transitioned" audit row ([D6]) — all on the CALLER'S
// transaction, never one of its own. Extracted from Store.Transition by
// M4-04-05 ([transition-tx-extraction]) so Store.ApplyValidation can promote
// draft->validated inside the SAME tx that stamps violations/
// rule_set_version_id. (Rejected: having ApplyValidation call the public
// Transition — that opens a SECOND transaction, so the version stamp and the
// status change could diverge on a crash, breaking M4's "every transition
// writes audit 08 in the same transaction".)
//
// It has exactly TWO callers — Store.Transition and Store.ApplyValidation —
// and remains the SINGLE writer of invoices.status, with legalTransitions/
// canTransition still the single source of truth for what is legal. That is
// what PRESERVES the M4 gate's "illegal state transitions are rejected by the
// single transition function" across the extraction: no edge is added, and
// neither caller can reach the UPDATE without passing canTransition.
//
// The CALLER owns the FOR UPDATE lock and the redundancy check
// (current == target -> ErrRedundantTransition, [D4] — checked before
// legality, hence above the call, not in here). The caller also owns the
// actor: an `actor Actor` parameter (M5-04-02) rather than transitionTx
// re-deriving it from ctx itself, because the history INSERT binds BOTH
// TenantID and Subject, so a Subject-only `actor` param (the originally
// specified signature) would have to re-derive the identity for TenantID
// anyway, and could then only ever disagree with the tenant_id beside it
// [Stage-1 F3] — the {TenantID, Subject} pair sidesteps that by construction.
// The three pre-M5-04 HTTP-path callers pass actorFromContext(ctx), which
// reproduces the old inline `callerID, _ := auth.IdentityFromContext(ctx)`
// byte-for-byte; MarkSubmittedTx/MarkFailedTx (actor.go) pass
// SystemActor(tenantID) instead, so a background worker with no JWT identity
// in ctx no longer trips the actor CHECK constraints.
//
// Errors propagate RAW — never wrapped, and the actor is never
// pre-validated — so their SQLSTATE survives for the atomicity specs: an
// empty Subject fails invoice_status_history's char_length(actor)>0 and a
// 256-char one passes that but fails audit_log's char_length(actor)<=255,
// both 23514, which TestTransition_AtomicityRollsBackOnActorCheckFailure and
// GATE-13 assert via pgCode.
func transitionTx(ctx context.Context, tx pgx.Tx, id string, current, target Status, actor Actor) (Invoice, error) {
	if !canTransition(current, target) {
		return Invoice{}, ErrIllegalTransition
	}

	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`UPDATE invoices SET status = $1 WHERE id = $2 RETURNING `+invoiceColumns,
		string(target), id,
	), &inv); err != nil {
		return Invoice{}, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1, $2, $3, $4, $5)`,
		actor.TenantID, id, string(current), string(target), actor.Subject,
	); err != nil {
		return Invoice{}, err
	}

	if err := audit.Record(ctx, tx, actor.Subject, "invoice.transitioned", map[string]any{
		"id":   id,
		"from": current,
		"to":   target,
	}); err != nil {
		return Invoice{}, err
	}

	return inv, nil
}

// hasBlockingViolation reports whether vs carries a severity:"error" entry —
// the ONLY thing that blocks promotion. warning/info are advisory and never
// block ([error semantics]); one error is enough, and every other violation
// in the set is still STORED regardless (collect-all is preserved end to end,
// not just at the evaluator).
func hasBlockingViolation(vs []Violation) bool {
	for _, v := range vs {
		if v.Severity == "error" {
			return true
		}
	}
	return false
}

// HasBlockingViolation is hasBlockingViolation's exported face, for the
// importer's DRY-RUN clean count (M4-04-07, [dry-run-evaluates]).
//
// A dry-run never writes, so it has no BatchOutcome to read Clean from —
// ValidateBatch, which computes it, is the WRITING path. Without this the
// importer would have to re-derive the severity test in another package: a
// SECOND predicate that must agree with promotion forever. It would not.
// The obvious guess — len(violations) == 0 — is already wrong today: a
// warning-only invoice carries violations and still promotes ([error
// semantics]), so it would under-report clean invoices on every dry-run
// while the real run promoted them. Exporting the one predicate makes the
// dry-run's count and ApplyValidation's promotion decision identical BY
// CONSTRUCTION rather than by agreement.
func HasBlockingViolation(vs []Violation) bool { return hasBlockingViolation(vs) }

// ApplyValidation is M4-04's validate GATE: it stamps an evaluation's verdict
// onto a draft invoice and, when nothing blocks, promotes it draft->validated
// — all inside ONE db.WithinRequestTenantTx, so a failure anywhere rolls back
// ALL of it (the M4 same-transaction atomicity gate, Core AC #2).
//
// The tx deliberately does NOT span the HTTP call to 04 ([toctou-staleness]):
// holding a Postgres transaction and a FOR UPDATE row lock open across a
// network call to another service would pin a pool connection under unbounded
// remote latency — 500x over on an import. So the shape is
// EVALUATE (no tx open, the caller's job) -> ONE tx that RE-CHECKS and writes:
//
//  1. SELECT <invoiceColumns> ... FOR UPDATE — the full row, same lock and
//     round trip as Transition's status-only read. RLS-scoped, so another
//     tenant's VALID uuid 0-rows exactly like a genuinely nonexistent one
//     (pgx.ErrNoRows -> ErrNotFound); a malformed non-uuid id raises 22P02 ->
//     ErrValidation, mirroring Get/Update/Create/Transition.
//  2. status re-check — must still be draft, else ErrNotDraft
//     ([gate-scope-draft-only]).
//  3. content re-check — contentFingerprint(locked) != evaluatedFingerprint
//     -> ErrStaleValidation. FOR UPDATE makes this EXACT: Store.Update's
//     UPDATE serializes against the lock, so the locked row is authoritative.
//  4. stamp violations + rule_set_version_id (always — the version is stamped
//     even on a blocking verdict; "these violations came from THAT rule set"
//     is exactly what makes the verdict auditable).
//  5. promote via transitionTx iff nothing blocks — the same tx, hence the
//     extraction.
//  6. audit.Record("invoice.validated") — every gate outcome writes it; a
//     promotion additionally wrote "invoice.transitioned" in step 5.
//
// Step 2 MUST precede step 3 and the order is load-bearing under concurrency:
// the winner of a race mutates only status/violations/rule_set_version_id,
// NONE of which are in the content fingerprint, so a loser's fingerprint still
// MATCHES — only the status re-check catches it, yielding ErrNotDraft rather
// than a misleading ErrStaleValidation (GATE-17).
//
// A blocking verdict is a normal, nil-error return: "this invoice has errors"
// is a legitimate OUTCOME of the gate, never a store failure. Errors from the
// writes propagate RAW so their SQLSTATE survives (23503 when 04 hands over a
// phantom rule_set_version_id the FK refuses; 23514 on the actor CHECKs).
func (s *Store) ApplyValidation(ctx context.Context, id string, vs []Violation, ruleSetVersionID, evaluatedFingerprint string) (Invoice, error) {
	// Normalize the SLICE, then marshal ([violations-write]). Both guards, in
	// THIS order — normalizing the bytes afterwards would not do: pgx encodes a
	// nil Go slice as SQL NULL (23502 against `violations jsonb NOT NULL`), but
	// json.Marshal of a nil []Violation returns []byte("null") — a NON-nil
	// slice holding the JSON scalar null, which binds to jsonb SUCCESSFULLY and
	// silently lands violations='null'::jsonb. Only normalizing the slice first
	// yields []. Same discipline as internal/validation/engine.go:53-58, which
	// likewise normalizes the slice.
	if vs == nil {
		vs = []Violation{}
	}
	violationsJSON, err := json.Marshal(vs)
	if err != nil {
		return Invoice{}, fmt.Errorf("marshal violations: %w", err)
	}

	// The verdict is a pure function of the evaluated set, decided once and
	// used for both the promotion and the audit row's outcome.
	blocked := hasBlockingViolation(vs)

	var inv Invoice
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		// 1. lock and read the FULL row — the fingerprint needs its content.
		var locked Invoice
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, id,
		), &locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		// 2. status re-check — BEFORE the fingerprint check (see the doc).
		if locked.Status != StatusDraft {
			return ErrNotDraft
		}

		// 3. content re-check — the invoice must not have been edited under
		// the run; the status check above cannot see an edit.
		if contentFingerprint(locked) != evaluatedFingerprint {
			return ErrStaleValidation
		}

		// 4. stamp the verdict, blocking or not.
		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET violations = $1, rule_set_version_id = $2 WHERE id = $3 RETURNING `+invoiceColumns,
			violationsJSON, ruleSetVersionID, id,
		), &inv); err != nil {
			return err
		}

		// 5. promote iff earned ([validated-is-earned]). transitionTx's
		// RETURNING re-reads the row step 4 just stamped (same tx), so inv
		// carries the violations/version AND the new status.
		if !blocked {
			var err error
			if inv, err = transitionTx(ctx, tx, id, StatusDraft, StatusValidated, actorFromContext(ctx)); err != nil {
				return err
			}
		}

		// 6. one audit row per gate outcome, promoted or not. outcome names
		// the gate's VERDICT and is deliberately drawn from a vocabulary
		// disjoint from Status: "validated"/"failed" would collide with real
		// statuses (draft->validated; M5's submitted->failed) and make an
		// M4-07 rollup ambiguous. It is NOT the same axis as violation_count
		// either -- a warning-only invoice is "promoted" WITH violations.
		outcome := "promoted"
		if blocked {
			outcome = "blocked"
		}
		return audit.Record(ctx, tx, callerID.Subject, "invoice.validated", map[string]any{
			"id":                  id,
			"rule_set_version_id": ruleSetVersionID,
			"outcome":             outcome,
			"violation_count":     len(vs),
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}
