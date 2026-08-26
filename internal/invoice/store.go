package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/actor"
	"github.com/SimonOsipov/invoice-os/internal/approval"
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

	// APPROVALS_ENFORCED. The two write doors into queued and the wire flag must
	// all read THIS field, never re-derive it
	// (TestApprovalsEnforced_DeclaredOnceWrittenOnce).
	approvalsEnforced bool
}

// StoreOption configures a Store at construction. Variadic so the existing
// NewStore(pool) call sites compile unchanged (TestNewStore_BothAritiesCompile).
type StoreOption func(*Store)

// WithApprovalsEnforced turns the transmit gate on. Default false: an unset flag
// leaves both doors into queued as they were (TestNewStore_DefaultsToNotEnforced).
func WithApprovalsEnforced(v bool) StoreOption {
	return func(s *Store) { s.approvalsEnforced = v }
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle. Options apply in order, last wins
// (TestStoreOptions_ApplyInOrderLastWins).
func NewStore(pool *pgxpool.Pool, opts ...StoreOption) *Store {
	s := &Store{pool: pool}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
	`violations, rule_set_version_id, created_at, ` +
	`irn, csid, qr_payload, rejection_reasons, ` +
	`kept_as_is_at, kept_as_is_by, kept_as_is_reason, failure_kind`

func scanInvoice(row scanner, inv *Invoice) error {
	return row.Scan(
		&inv.ID, &inv.EntityID, &inv.ImportBatchID, &inv.InvoiceNumber, &inv.Status,
		&inv.IssueDate, &inv.SupplierTIN, &inv.SupplierName, &inv.BuyerTIN, &inv.BuyerName,
		&inv.Currency, &inv.Subtotal, &inv.VAT, &inv.Total,
		&inv.Violations, &inv.RuleSetVersionID, &inv.CreatedAt,
		&inv.IRN, &inv.CSID, &inv.QRPayload, &inv.RejectionReasons,
		&inv.KeptAsIsAt, &inv.KeptAsIsBy, &inv.KeptAsIsReason, &inv.FailureKind,
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

// historyColumns deliberately excludes id/tenant_id/invoice_id (AC #7). It is the
// SELECT list, not the wire shape: StatusChange also carries ActorName/ActorKind,
// which History populates after the scan and no column supplies.
const historyColumns = `from_status, to_status, actor, changed_at`

func scanStatusChange(row scanner, sc *StatusChange) error {
	return row.Scan(&sc.FromStatus, &sc.ToStatus, &sc.Actor, &sc.ChangedAt)
}

// Create inserts one invoice and, in the SAME db.WithinRequestTenantTx closure
// and in this order: (0) a tenant-scoped ownership pre-check on entity_id
// (M4-06-03 -- mirrors the importer's EntitySupplier idiom,
// internal/importer/store.go, and closes the direct-path gap the "22P02 does
// not disambiguate" note below used to accept: a cross-tenant OR nonexistent
// entity_id now returns ErrValidation HERE, before any row is written), WIDENED
// by INVCR-01-17 (C7 fix) to also resolve the entity's name/tin and OVERWRITE
// in.SupplierTIN/in.SupplierName with them (MBSSupplierTIN-restored for a
// 12-bare-digit FIRS tin, unchanged for a 10-digit JTB tin or no tin at all) --
// whatever the caller sent in those two fields is discarded, never trusted
// ([supplier-from-entity]; CreateHandler's own doc comment records the
// override-not-400 ruling); (1) the invoices row (tenant_id from the caller's
// identity, status left to the column DEFAULT 'draft', MBS-content passed
// through un-rejected incl. NULL/negative — store-invalid-faithfully, AC-6 --
// EXCEPT supplier_tin/supplier_name, which step 0 already overwrote); (2) one
// line_items row per CreateInput.LineItems entry with a system-assigned
// line_no = 1..N by array position ([D10]); (3) the genesis
// invoice_status_history row (from_status NULL -> to_status 'draft', actor =
// the caller's Subject, [D5]); (4) an "invoice.created" audit.Record. Because
// all these writes share one transaction, a later failure rolls the earlier
// ones back too (INV-STORE-07).
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
// earlier via its own zero-rows (pgx.ErrNoRows) branch, so this INSERT-time 23503
// in practice now only fires for import_batch_id) or an invalid_text_representation
// (22P02, a malformed entity_id/import_batch_id uuid, OR a malformed numeric
// MBS-content value; the pre-check maps its own 22P02 the same way for entity_id) ->
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

		// Tenant-scoped ownership pre-check, WIDENED by INVCR-01-17 (C7 fix)
		// to also resolve the entity's name/tin: RLS scopes this SELECT to
		// the caller's tenant (same mechanism EntitySupplier relies on,
		// internal/importer/store.go), so a foreign OR nonexistent entity_id
		// both come back zero rows -- pgx.ErrNoRows, mapped to ErrValidation
		// exactly as the old bare EXISTS check's !exists branch was. This
		// rejects the cross-tenant case EARLY, as a friendly ErrValidation
		// with NO row written and NO audit row -- the composite (tenant_id,
		// entity_id) FK below is the DB-authoritative backstop (see this
		// func's doc comment; M4-06-03 closes the direct-path gap noted
		// there). Widening this query's SELECT list (rather than adding a
		// SECOND lookup) means AC #9's cross-tenant refusal is still the
		// pre-existing coverage (TestStoreCreate_CrossTenantEntityIDRejected
		// and its no-partial-write sibling) -- no new lookup was introduced.
		var entityName string
		var entityTIN *string
		if err := tx.QueryRow(ctx,
			`SELECT name, tin FROM business_entities WHERE id = $1`, in.EntityID,
		).Scan(&entityName, &entityTIN); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrValidation
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		// [supplier-from-entity], INVCR-01-17 (C7 fix): supplier_tin/
		// supplier_name are ALWAYS derived from the entity this pre-check
		// just resolved, OVERRIDING whatever the caller sent in
		// in.SupplierTIN/in.SupplierName -- the architect ruling CreateHandler's
		// own doc comment records (AC #8: override, not a 400, so the
		// existing e2e harness that already sends these fields keeps
		// working). Supplier identity is the FIRM's own data, never the
		// caller's -- true for every Store.Create caller, not just the HTTP
		// handler, which is why this lives here rather than in CreateHandler
		// alone: internal/importer's buildCreateInput already computes the
		// identical value via EntitySupplier + MBSSupplierTIN before calling
		// Create (needed for its own dry-run preview, which never reaches
		// this method), so this re-derivation is a no-op recompute for that
		// caller, not a behavior change. MBSSupplierTIN restores the MBS
		// wire spelling (NNNNNNNN-NNNN) of a 12-bare-digit canonical FIRS
		// TIN; a 10-digit JTB TIN or a nil TIN passes through unchanged --
		// nil overrides a caller-supplied value too (there is no fallback to
		// the caller when the entity has none of its own).
		//
		// buyer_tin/buyer_name are NOT touched here (scope fence, AC #4/#7):
		// they stay exactly what the caller sent, so a malformed buyer TIN
		// still violates buyer-tin-format faithfully.
		in.SupplierTIN = MBSSupplierTIN(entityTIN)
		in.SupplierName = &entityName

		if err := scanInvoice(tx.QueryRow(ctx,
			`INSERT INTO invoices
			   (tenant_id, entity_id, invoice_number,
			    issue_date, supplier_tin, supplier_name, buyer_tin, buyer_name,
			    currency, subtotal, vat, total, import_batch_id, source_document_id, source_rows)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
			         $10::text::numeric, $11::text::numeric, $12::text::numeric, $13, $14, $15)
			 RETURNING `+invoiceColumns,
			id.TenantID, in.EntityID, in.InvoiceNumber,
			in.IssueDate, in.SupplierTIN, in.SupplierName, in.BuyerTIN, in.BuyerName,
			in.Currency, in.Subtotal, in.VAT, in.Total, in.ImportBatchID, in.SourceDocumentID, in.SourceRows,
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

		return audit.Record(ctx, tx, id.Subject, "invoice.created", map[string]any{
			"id":             inv.ID,
			"invoice_number": inv.InvoiceNumber,
		})
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
		var err error
		inv, err = getTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// getTx is Get's tx-scoped body, extracted verbatim (M5-04-03,
// [invoice-port-in-05]) so submission's InvoicePort.Canonical can hydrate an
// invoice inside a caller-supplied tx (e.g. the worker's own
// db.WithinTenantTx, with no request identity) without opening a second,
// nested transaction. Get itself is now a thin wrapper around this plus its
// own db.WithinRequestTenantTx. Byte-identical observable behaviour to the
// pre-extraction Get, incl. line_no ordering and the rule_set_version
// subselect (T03-2).
func getTx(ctx context.Context, tx pgx.Tx, id string) (Invoice, error) {
	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id,
	), &inv); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return Invoice{}, ErrValidation
		}
		return Invoice{}, err
	}

	lines, err := hydrateLinesTx(ctx, tx, inv.ID)
	if err != nil {
		return Invoice{}, err
	}
	inv.LineItems = lines

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
				return Invoice{}, err
			}
		} else {
			inv.RuleSetVersion = &v
		}
	}

	return inv, nil
}

// hydrateLinesTx reads one invoice's line_items, ordered line_no ASC -- the
// SINGLE line-read in this package (INVED-01-02), extracted verbatim from
// getTx's own loop so getTx, Store.Edit and Store.ApplyValidation cannot drift
// apart on projection or ordering. Returns nil (never []LineItem{}) for a
// line-less invoice: `out` is only ever appended to, so getTx's observable
// behaviour is byte-identical to the pre-extraction version.
//
// TX-SCOPED, NEVER POOL-SCOPED, and this is correctness rather than style.
// db.WithinTenantTx/WithinRequestTenantTx set the tenant with
// set_config('app.current_tenant', $1, true) -- is_local = TRUE, so the GUC
// lives and dies with the transaction (internal/platform/db/db.go:62), and
// line_items' RLS policy filters on exactly that GUC. Handed s.pool instead of
// tx, this would run on a connection where the GUC is unset, the policy's
// predicate would be false for every row, and it would return ZERO ROWS
// SILENTLY WITH NO ERROR -- every lined invoice's locked-row fingerprint would
// hash no lines while the evaluated one hashed the real ones, so every validate
// would return ErrStaleValidation. A pool read is also a different MVCC
// snapshot (defeating [toctou-staleness]) and takes a second connection while
// holding a row lock. There is no precedent for a pool read inside a tx in this
// package; do not create one.
//
// No separate lock on line_items is needed: the invoice row's FOR UPDATE is the
// serialization point for its lines too.
func hydrateLinesTx(ctx context.Context, tx pgx.Tx, invoiceID string) ([]LineItem, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+lineItemColumns+` FROM line_items WHERE invoice_id = $1 ORDER BY line_no ASC`, invoiceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LineItem
	for rows.Next() {
		var item LineItem
		if err := scanLineItem(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FingerprintTx hashes one invoice's stored content inside the caller's transaction —
// the same three steps ApplyValidation's staleness re-check takes (scanInvoice over
// invoiceColumns, hydrateLinesTx, contentFingerprint), read through one MVCC snapshot.
//
// Exported for exactly one consumer: approval.Fingerprinter, which internal/approval's
// publish sweep is built with at cmd/invoice/main.go. contentFingerprint stays unexported
// so that edge cannot reverse (TestApproval_DoesNotImportInvoicePackage).
//
// Takes NO row lock: the sweep already holds it, matching ApplyValidation's shape where
// the FOR UPDATE is step 1 of the caller's closure.
func FingerprintTx(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id,
	), &inv); err != nil {
		return "", err
	}
	lines, err := hydrateLinesTx(ctx, tx, id)
	if err != nil {
		return "", err
	}
	return contentFingerprint(inv, lines), nil
}

// DemoteApprovalRejectedTx walks a validated invoice back to draft after an approver
// rejects it, via transitionTx on the caller's transaction — exported for exactly one
// consumer: approval.Demoter, bound at cmd/invoice/main.go, mirroring FingerprintTx's
// shape and the same reason (the internal/approval -> internal/invoice edge must not
// open).
//
// Takes NO row lock, like FingerprintTx: the caller (decideTx) already holds it. The
// status is read fresh rather than assumed validated, so a source that is not
// legally validated->draft (e.g. already draft) surfaces transitionTx's own
// ErrIllegalTransition instead of silently rewriting the row.
func DemoteApprovalRejectedTx(ctx context.Context, tx pgx.Tx, id, tenantID, subject string) error {
	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id,
	), &inv); err != nil {
		return err
	}
	_, err := transitionTx(ctx, tx, id, inv.Status, StatusDraft, Actor{TenantID: tenantID, Subject: subject})
	return err
}

// replaceLinesTx replaces an invoice's WHOLE line set inside the caller's tx:
// DELETE every existing line, then re-INSERT in from array order with line_no
// system-assigned 1..N ([line-update-shape], [line-no-by-position]). The INSERT
// is Store.Create's line loop verbatim, including the $N::text::numeric binding.
// Returns the INSERTed rows as RETURNING projected them.
//
// The RETURNING rows are the ONLY legitimate source of Store.Edit's post-write
// fingerprint and of its response's LineItems. A slice synthesized from `in`
// would carry the caller's raw decimal scale, so re-sending "100.0" against a
// stored numeric(14,2) "100.00" would hash differently and fire a FALSE
// demotion; RETURNING re-reads the column through the same ::text projection
// hydrateLinesTx uses, so Postgres has already normalized it. There are no
// triggers on line_items, so RETURNING is the stored row.
//
// DELETE and the INSERTs must stay SEPARATE statements. A single CTE doing both
// genuinely collides on line_items_invoice_line_no_uq, which is NOT deferrable
// (pg_constraint.condeferrable = f). As separate statements there is no
// conflict and no renumbering dance is needed: tuples deleted by the current
// transaction are already dead to that same transaction's uniqueness check.
// Verified live on this worktree's DB as invoice_app under a real
// app.current_tenant GUC -- DELETE 2, then re-INSERT of line_no 1,2,3, no
// 23505. Do NOT add ON CONFLICT, a two-phase renumber, or a negative-offset
// shuffle; none of them are needed and each hides this property.
//
// Empty in returns nil, never []LineItem{} -- matching hydrateLinesTx, though
// contentFingerprint's count marker is len()-based so the two hash identically
// either way.
//
// TX-SCOPED, NEVER POOL-SCOPED, for exactly the reason spelled out at length on
// hydrateLinesTx: app.current_tenant is set with is_local = true, so on the
// pool the RLS predicate is false, the DELETE removes ZERO ROWS SILENTLY and
// the INSERT then collides. The DELETE carries no tenant predicate of its own
// because line_items' tenant_isolation policy is FOR ALL with no TO clause and
// so filters the DELETE's USING; the INSERT binds tenant_id from the caller's
// own identity, exactly as Store.Create does.
//
// 22P02 (invalid_text_representation) maps to ErrValidation so a malformed line
// numeric behaves exactly like a malformed HEADER numeric (updateContentTx) --
// 400, not a raw 500. Every other SQLSTATE propagates raw so Store.Edit's
// atomicity specs can still assert on it. Store.Create's raw propagation is
// Create's own contract and is deliberately left alone.
//
// Known residual, documented rather than defended (D8 / LI-RLS-12): a
// line_items row whose tenant_id disagrees with its invoice's tenant is
// invisible to the owning tenant, so the DELETE skips it and the re-INSERT at
// that line_no raises 23505 (the unique index is not RLS-filtered), surfacing
// as a raw 500. Unreachable through the app -- both line writers bind the
// caller's own tenant_id, so only a direct DB write can plant one.
func replaceLinesTx(ctx context.Context, tx pgx.Tx, tenantID, invoiceID string, in []LineItemInput) ([]LineItem, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM line_items WHERE invoice_id = $1`, invoiceID); err != nil {
		return nil, err
	}

	var out []LineItem
	for i, li := range in {
		var item LineItem
		if err := scanLineItem(tx.QueryRow(ctx,
			`INSERT INTO line_items
			   (tenant_id, invoice_id, line_no, description,
			    quantity, unit_price, line_total, line_tax)
			 VALUES ($1, $2, $3, $4,
			         $5::text::numeric, $6::text::numeric, $7::text::numeric, $8::text::numeric)
			 RETURNING `+lineItemColumns,
			tenantID, invoiceID, i+1, li.Description,
			li.Quantity, li.UnitPrice, li.LineTotal, li.LineTax,
		), &item); err != nil {
			if pgCode(err) == "22P02" {
				return nil, ErrValidation
			}
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
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

		// Resolved once here, never inside the loop: whatever the row count this
		// costs ONE extra statement, on History's own tx
		// (TestHistory_IssuesOneResolveQueryForManyRows). Resolve de-duplicates the
		// subjects itself, on the normalised uuid -- a stronger key than this
		// caller could apply to the raw strings.
		subjects := make([]string, 0, len(result))
		for _, sc := range result {
			subjects = append(subjects, sc.Actor)
		}
		labels, err := actor.Resolve(ctx, tx, subjects)
		if err != nil {
			return err
		}
		for i := range result {
			label := labels[result[i].Actor]
			result[i].ActorName = label.Text
			result[i].ActorKind = string(label.Kind)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// escapeLike neutralises the LIKE/ILIKE metacharacters in a user-supplied
// search string so it matches LITERALLY, for use with an explicit
// ESCAPE '\' clause. Order matters: backslash FIRST, or the backslashes this
// function itself introduces get escaped a second time.
//
// This deliberately diverges from internal/portfolio's List, whose q is bound
// but NOT escaped -- a ruling pinned there by
// TestStoreList_SearchQWildcardIsNotEscaped, whose own comment anticipated a
// future story wanting literal-search semantics. This is that story
// (INVCR-01-06): across a 500-row import review, a stray "%" silently
// matching all 500 is a worse lie than 0 results. portfolio/* is NOT changed,
// so the same typed "%" matches everything on Entities and nothing here --
// an accepted, flagged divergence.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// List returns the caller's tenant's invoice HEADERS (LineItems left nil, [D7]),
// ordered created_at DESC, id DESC (deterministic), paginated by f.Limit/f.Offset,
// plus the total FILTERED count (matching every predicate filter set on f,
// ignoring limit/offset) for the pagination envelope. Every condition is
// interpolated into BOTH the COUNT and the page query before LIMIT/OFFSET are
// appended last, so total is the filtered total across all pages, never the
// count of one page. RLS (not a manual WHERE tenant_id)
// additionally scopes both the COUNT and the page to the caller's tenant. An
// empty result is []Invoice{}, never a nil slice.
//
// f.EntityID ([entity-id-restored], regression fix) and f.NeedsAttention
// (M4-09-02) were Store.List's first two predicate filters ([D8]); INVCR-01-06
// added five more (below). ALL of them AND together when more than one is set.
// EntityID follows portfolio/store.go List's own
// conditions/args idiom (fmt.Sprintf("entity_id = $%d", len(args)), never
// string-interpolated) so it narrows the row set BEFORE LIMIT/OFFSET are ever
// applied -- the fix for the CI-caught regression where the SPA instead
// fetched a tenant-wide page and filtered it in the browser, dropping an
// entity's own invoices whenever they weren't inside the newest 50
// tenant-wide. Because EntityID may consume a bind param, LIMIT/OFFSET's
// placeholder numbers float (len(args)+1/+2), not the fixed $1/$2 this query
// used before EntityID existed. A malformed (non-uuid) f.EntityID raises
// Postgres 22P02 (invalid_text_representation), mapped to ErrValidation --
// mirrors Get/Update/Transition's own 22P02 handling -- even though
// ListHandler already rejects a malformed entity_id query param before
// Store.List is ever called.
//
// f.NeedsAttention's WHERE fragment, when true, is a hand-maintained twin of
// the dashboard rollup's own count(*) FILTER predicate (internal/dashboard/
// store.go Rollup) so the two surfaces can never drift apart
// ([needs-attention-drift-guard]). Exactly two things differ, because List has
// no join: the `i.` alias is dropped, and the approval arm correlates on
// invoices.id where the rollup uses i.id. Nothing else may diverge.
// TestStoreList_NeedsAttentionMatchesDashboardRollup compares the two by
// behaviour on a fixture that seeds approval_runs; the arm shapes are pinned
// per package by TestStoreList_NeedsAttentionSQLRejectedArmIsBare and the
// dashboard's TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare. It carries no
// bind params of its own. When every filter is false/absent (the zero ListFilter),
// `where` is empty and both queries are byte-identical to before any filter
// existed.
//
// f.ImportBatchIDs/Status/NeedsFix/RuleKey/Query (INVCR-01-06, [D4]) are the
// review screen's five filters. Four notes on them:
//
//   - NeedsFix is a NEW predicate, not a slice of NeedsAttention
//     ([needs-fix-is-a-new-predicate]): draft AND a blocking violation, so a
//     rejected/failed invoice is EXCLUDED where NeedsAttention includes it.
//     It is written out separately rather than sharing a Go constant with the
//     NeedsAttention fragment, precisely so a later change to one cannot
//     silently move the dashboard's meaning too. ([D6]'s kept_as_is_at IS NULL
//     clause lands in INVCR-01-15, not here.)
//   - RuleKey is bound as a jsonb ARGUMENT (violations @> $n::jsonb), never
//     interpolated. This is the one place NeedsAttention's idiom does NOT
//     generalise: that fragment is a hardcoded literal with no bind param, and
//     concatenating a caller-supplied key into its shape would be a direct
//     injection path.
//   - Query's fragment is wrapped in OUTER PARENTHESES. conditions are joined
//     with " AND ", so a bare `a ILIKE $n OR b ILIKE $n` would bind as
//     `(batch AND ...) OR (b ILIKE ...)` -- the other filters silently
//     evaporate and the query goes tenant-wide with a plausible-looking total.
//   - Query's wildcards are escaped (escapeLike + ESCAPE '\'), so a typed "%"
//     finds a literal percent sign, not every row. See escapeLike for why this
//     reverses portfolio's recorded ruling.
//
// A malformed (non-uuid) member of f.ImportBatchIDs raises 22P02 on the COUNT
// query and maps to ErrValidation, exactly as f.EntityID does -- verified
// live (BULK-01-02) against pgx v5.10.0/PG18: `= ANY($n)` binds a Go
// []string in TEXT format with each member written verbatim, so a malformed
// member reaches Postgres's own uuid parser (string_to_uuid) rather than
// failing client-side in pgx, and pgCode(err) == "22P02" below already
// catches it -- no ::uuid[] cast needed. A cross-tenant (or nonexistent)
// batch id is NOT an error and NOT a 404: RLS has already scoped the row
// set, so it narrows to an empty page with total 0 -- a 404 would be an
// existence oracle for another tenant's data.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
	items := []Invoice{}
	var total int
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var conditions []string
		var args []any

		if f.EntityID != "" {
			args = append(args, f.EntityID)
			conditions = append(conditions, fmt.Sprintf("entity_id = $%d", len(args)))
		}
		if f.NeedsAttention {
			// invoices.id is qualified: approval_runs has its own id, so a bare id
			// binds there and silently never matches.
			conditions = append(conditions, `(status = 'rejected'
			  OR (status = 'failed' AND kept_as_is_at IS NULL)
			  OR (status = 'draft' AND violations @> '[{"severity": "error"}]'::jsonb)
			  OR (status = 'draft' AND EXISTS (
			          SELECT 1 FROM (SELECT r.state FROM approval_runs r
			                          WHERE r.invoice_id = invoices.id
			                          ORDER BY r.opened_at DESC LIMIT 1) lr
			           WHERE lr.state = 'rejected')))`)
		}
		if f.AwaitingApproval {
			// A THIRD predicate: only validated rows match, a status neither the
			// needs_attention fragment above nor needs_fix below can reach
			// (TestStoreList_AwaitingApprovalIsNotNeedsAttention, ...IsNotNeedsFix).
			// Exact negation of approval.TransmitClear -- the UNFLAGGED predicate, so
			// APPROVALS_ENFORCED never gates it (...IsTheExactNegationOfTransmitClear).
			// invoices.id is qualified: approval_runs has its own id, so a bare id
			// binds there and silently never matches.
			conditions = append(conditions, `(status = 'validated'
			  AND EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)
			  AND NOT EXISTS (SELECT 1 FROM approval_runs r
			                   WHERE r.invoice_id = invoices.id AND r.state = 'approved'))`)
		}
		if len(f.ImportBatchIDs) > 0 {
			args = append(args, f.ImportBatchIDs)
			conditions = append(conditions, fmt.Sprintf("import_batch_id = ANY($%d)", len(args)))
		}
		if f.Status != "" {
			args = append(args, string(f.Status))
			conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
		}
		if f.NeedsFix {
			// AND kept_as_is_at IS NULL (INVCR-01-15, D6, [needs-fix-is-a-new-predicate]):
			// a kept row still matches the base needs_fix shape (draft + a blocking
			// violation) but has left the "needs a fix" working set by operator decision --
			// this clause lands ONLY here, never on needs_attention's fragment above
			// (which stays behaviourally in lockstep with the dashboard rollup, pinned by
			// TestStoreList_NeedsAttentionMatchesDashboardRollup -- not byte-identical:
			// that query aliases the table `i.` and correlates its approval arm on
			// i.id, where this one uses invoices.id).
			conditions = append(conditions, `(status = 'draft' AND violations @> '[{"severity": "error"}]'::jsonb AND kept_as_is_at IS NULL)`)
		}
		if f.KeptAsIs {
			// The review shell's footer counter query ("N kept as-is") -- a real server
			// total, never derived by arithmetic over the other totals
			// ([filters-are-server-side]).
			// status = 'draft': on a failed row the mark means resolved outside, not kept as-is.
			conditions = append(conditions, `(status = 'draft' AND kept_as_is_at IS NOT NULL)`)
		}
		if f.RuleKey != "" {
			// json.Marshal, never fmt.Sprintf: a quote-bearing rule_key built by
			// string formatting emits malformed JSON, which Postgres rejects as
			// 22P02 (a 500) instead of returning the honest zero rows.
			b, err := json.Marshal([]map[string]string{{"rule_key": f.RuleKey}})
			if err != nil {
				return err
			}
			args = append(args, string(b))
			conditions = append(conditions, fmt.Sprintf("violations @> $%d::jsonb", len(args)))
		}
		if f.Query != "" {
			args = append(args, escapeLike(f.Query))
			// All four arms bind the same escaped $n -- one argument, one wildcard.
			conditions = append(conditions, fmt.Sprintf(
				`(invoice_number ILIKE '%%'||$%d||'%%' ESCAPE '\' OR buyer_name ILIKE '%%'||$%d||'%%' ESCAPE '\' OR buyer_tin ILIKE '%%'||$%d||'%%' ESCAPE '\' OR supplier_tin ILIKE '%%'||$%d||'%%' ESCAPE '\')`,
				len(args), len(args), len(args), len(args),
			))
		}

		where := ""
		if len(conditions) > 0 {
			where = " WHERE " + strings.Join(conditions, " AND ")
		}

		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices`+where, args...).Scan(&total); err != nil {
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		// No 22P02 check needed on this second query: it shares `args` (and therefore
		// f.EntityID) byte-for-byte with the COUNT query above, which always runs
		// FIRST -- a malformed EntityID already raised ErrValidation there and
		// returned before this ever executes; LIMIT/OFFSET are plain Go ints, which
		// cannot themselves produce an invalid_text_representation.
		selectArgs := append(append([]any{}, args...), f.Limit, f.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			`SELECT `+invoiceColumns+`
			 FROM invoices%s
			 ORDER BY created_at DESC, id DESC
			 LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2,
		), selectArgs...)
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

// RuleCount is one row of the violation-summary aggregate: a distinct
// rule_key plus the count of distinct invoices carrying at least one
// violation entry for that key. Copied from internal/dashboard's own
// RuleCount (dashboard.go) rather than imported cross-package -- the
// established per-package-copy convention (pgCode, writeJSON/writeError,
// the two seedImportBatch test shapes) -- because the two aggregates
// deliberately diverge: this one is SEVERITY-AGNOSTIC (see
// Store.ViolationSummary's own doc), the dashboard's TopViolations counts
// severity:"error" only (internal/dashboard/store.go:95) -- a different
// question with its own predicate, task-283 R3.
type RuleCount struct {
	RuleKey  string `json:"rule_key"`
	Invoices int    `json:"invoices"`
}

// ViolationSummary returns one row per distinct rule_key among the
// violations of the invoices linked to importBatchIDs, counted as
// count(DISTINCT invoice.id) -- an invoice naming the same rule twice counts
// ONCE -- ordered invoices DESC then rule_key ASC. importBatchIDs is
// REQUIRED to carry at least one usable id (ViolationSummaryHandler rejects
// zero usable ids): an unbounded tenant-wide aggregation is not a supported
// query. Widened from a single importBatchID string to []string (BULK-01-02,
// [one-review-screen]) so the rail can span every batch a multi-file run
// produced, via `= ANY($1)` -- the same bare-array idiom List uses above, no
// cast (verified live, see List's own doc comment).
//
// Reuses internal/dashboard/store.go's Rollup aggregate shape, including
// BOTH of its guards:
//   - jsonb_typeof(violations) = 'array' is REQUIRED, not decorative:
//     jsonb_array_elements RAISES 22023 on non-array input (unlike the `@>`
//     predicate elsewhere in this file, which just returns false), and
//     invoices carries no array CHECK on violations -- so one malformed row
//     would 500 the whole rail without it.
//   - the nullif guard on rule_key stops an empty key becoming a group.
//
// DIVERGENCE, deliberate: the dashboard's v->>'severity' = 'error' clause is
// OMITTED. This aggregate is a PREVIEW OF A FILTER -- clicking a rail pill
// issues ?import_batch_id=X&rule_key=K, and List's RuleKey filter above is
// severity-agnostic. The rail must therefore use the same predicate as the
// filter it triggers, or a warning-only rule shows 0 in the rail while the
// table below shows its rows. All shipped rules are today severity "error",
// so the two clauses coincide and a severity-filtered implementation would
// pass every test written against today's data -- which is exactly why
// TestViolationSummary_MatchesRuleKeyFilterTotalIncludingWarnings seeds a
// warning-only rule. Do NOT "fix" this divergence by copying the
// dashboard's clause back in; the dashboard asks a different question
// (task-283 R3).
//
// RLS-scoped like every other read here -- no manual tenant predicate. A
// cross-tenant batch id is therefore an empty result, not an error.
func (s *Store) ViolationSummary(ctx context.Context, importBatchIDs []string) ([]RuleCount, error) {
	// Never nil: the handler renders "rules":[] and a nil slice would
	// marshal to null.
	rules := []RuleCount{}

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT v->>'rule_key' AS rule_key, count(DISTINCT i.id) AS invoices
			 FROM invoices i
			 CROSS JOIN LATERAL jsonb_array_elements(i.violations) AS v
			 WHERE i.import_batch_id = ANY($1)
			   AND jsonb_typeof(i.violations) = 'array'
			   AND nullif(v->>'rule_key', '') IS NOT NULL
			 GROUP BY 1
			 ORDER BY 2 DESC, 1 ASC`,
			importBatchIDs,
		)
		if err != nil {
			// Defence in depth behind the handler's own uuid.Parse guard,
			// mirroring List above: a malformed batch id must be a 400, not
			// a 500.
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rc RuleCount
			if err := rows.Scan(&rc.RuleKey, &rc.Invoices); err != nil {
				return err
			}
			rules = append(rules, rc)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
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
// supplier_tin/supplier_name are ALWAYS overwritten with the invoice's
// entity-derived values, discarding whatever the caller sent in
// in.SupplierTIN/in.SupplierName (INVCR-01-18, C7 fix, mirroring Store.
// Create's own [supplier-from-entity] override) -- see updateContentTx's
// doc comment for the full mechanism. This guard's own all-nil check above
// is UNCHANGED and still runs against the caller's raw in (a caller who
// sends nothing at all is still rejected; one who sends ONLY a
// since-discarded supplier_tin still legitimately passes it and triggers a
// real write).
//
// A thin wrapper over updateContentTx (M4-05-02 extraction, [content-write-
// extraction]): the guard/tx/audit shell stays HERE, byte-identical to before
// the extraction; the SET-clause build/query/scan/error-map moved verbatim
// into updateContentTx so Store.Edit's fix-loop can reuse it without an
// audit write of its own (Edit's audit is conditional on a real content
// change, which Update's is not, [D9]).
func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Invoice, error) {
	// Deliberately still INLINE rather than headerFieldsPresent(in): Store.Update
	// is kept byte-identical by [store-update-untouched]. The duplication with
	// that helper (INVED-01-04) is recorded, not accidental -- keep the two in
	// sync if UpdateInput ever gains a field.
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
			"id":             inv.ID,
			"fields":         changedFields,
			"invoice_number": inv.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// headerFieldsPresent reports whether in carries at least one header field --
// the exact negation of Store.Update's inline all-nil guard, over the same 9
// UpdateInput fields. Extracted by INVED-01-04 because Store.Edit now needs the
// answer TWICE: once in its widened pre-tx guard (a lines-only edit is legal, so
// "no header fields" alone is no longer a rejection) and once to decide whether
// to call updateContentTx at all -- that function assumes >= 1 non-nil field and
// would otherwise build `UPDATE invoices SET  WHERE ...`, a SQL syntax error.
func headerFieldsPresent(in UpdateInput) bool {
	return in.IssueDate != nil || in.SupplierTIN != nil || in.SupplierName != nil ||
		in.BuyerTIN != nil || in.BuyerName != nil || in.Currency != nil ||
		in.Subtotal != nil || in.VAT != nil || in.Total != nil
}

// strPtrEqual reports whether two possibly-nil *string values represent the
// same content: both nil, or both non-nil with an identical dereferenced
// value. Used by updateContentTx (INVCR-01-18) to decide whether the
// entity-derived supplier_tin genuinely differs from what is already
// stored, so the audit trail names a real correction without falsely
// claiming one on every ordinary edit.
func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
//
// [supplier-from-entity-on-edit], INVCR-01-18 (C7 fix, edit path): BEFORE
// building the SET clause, this function ALWAYS re-resolves the invoice's
// entity -- a single JOIN read, RLS-scoped on BOTH sides exactly like every
// other read in this package, no manual tenant_id predicate -- and
// OVERWRITES in.SupplierTIN/in.SupplierName with
// MBSSupplierTIN(entity.tin)/entity.name, mirroring Store.Create's own
// [supplier-from-entity] override (INVCR-01-17). Because Store.Update and
// Store.Edit BOTH funnel their content write through this one function, this
// single change closes C7 on both -- "one owner", AC #1/#5 of task-303's
// story, not a second copy of the derivation. It runs UNCONDITIONALLY
// whenever updateContentTx runs at all (i.e. the caller sent >= 1 header
// field), NOT gated on whether the caller's UpdateInput happened to include
// SupplierTIN/SupplierName -- so a PATCH that never mentions supplier
// identity still re-derives it from the entity's CURRENT name/tin. This is
// exactly why AC #8's no-op trap is real and has two legitimate outcomes:
// an invoice whose STORED supplier_tin already agrees with its entity
// re-derives to the SAME value and Edit's existing DB-authoritative
// fingerprint check (step 6, below in Store.Edit) correctly treats an
// otherwise-identical resend as a true no-op
// (TestStoreEdit_ValidatedNoOpStaysValidated, EDIT-04, left byte-unchanged
// by this subtask); an invoice whose stored value DISAGREES -- e.g. one
// created before this fix shipped -- genuinely changes fingerprint on its
// first PATCH afterwards, and the SAME no-op check correctly demotes it
// (TestStoreEdit_PreExistingWrongSupplierTINGenuinelyChangesAndDemotesOnFirstPatch,
// supplier_tin_update_test.go). Both are correct: idempotence protects a
// truly-unchanged edit, not a stale stored value the entity has since
// disagreed with.
//
// buyer_tin/buyer_name are NOT touched by this override (scope fence, AC
// #3/#7 of task-303, mirroring AC #4/#7 of task-293): the `if in.BuyerTIN
// != nil` / `if in.BuyerName != nil` branches below are unchanged, so a
// malformed buyer TIN still writes -- and still violates buyer-tin-format
// -- exactly as before.
//
// The resolved supplier_tin/supplier_name are appended to setClauses/args
// DIRECTLY, deliberately bypassing the set() helper below -- so a PATCH that
// merely re-submits its own already-correct supplier fields is not, by
// itself, enough to earn a "supplier_tin"/"supplier_name" audit-fields
// entry. But this is NOT "never audited" (product-advisor review, 2026-07-31):
// audit.Record's "invoice.updated" payload is fields-ONLY, no from/to
// snapshot (map[string]any{"id":..., "fields": changedFields, ...}, below in
// Store.Update/Store.Edit) -- if the override were unconditionally excluded
// from changedFields, a REAL silent correction of a compliance-relevant
// field (a fiscal invoice's own supplier identity) would leave literally NO
// trace in the audit trail naming what changed, which is a materially worse
// gap than the alternative. So the widened query below ALSO reads the
// invoice's CURRENTLY STORED supplier_tin/supplier_name (i.strPtrEqual
// comparison against the freshly-derived value, immediately below) and adds
// "supplier_tin"/"supplier_name" to changedFields precisely when the
// derived value actually DIFFERS from what is already stored -- i.e.
// exactly the cases that matter: a stale/wrong stored value (AC #8's
// "genuine content change" half) is now named in the audit trail, not just
// silently corrected, while the overwhelmingly common case (an
// already-correct stored value, re-derived to the SAME value on every
// ordinary PATCH) still resolves to "no entry", preserving the pre-existing
// "fields lists what genuinely changed" contract several edit_test.go specs
// pin byte-for-byte (e.g.
// TestStoreEdit_LinesRemovedOutOfBandThenHeaderOnlyEditSucceeds's fields ==
// ["vat"], TestStoreEdit_EmptyLineItemsRemovesAllLinesGuardWidened's fields
// == ["line_items"]) -- those fixtures' entities never drift from what was
// already stored, so the comparison is a no-op for them.
// TestStoreUpdate_AuditFieldsOmitSupplierWhenUnchangedButNameItWhenCorrected
// (supplier_tin_update_test.go) pins BOTH halves of this directly.
//
// A malformed (non-uuid) id or a since-deleted/cross-tenant-invisible
// invoice both surface HERE first, before the UPDATE statement itself ever
// runs: pgx.ErrNoRows (the JOIN matches no row) maps to ErrNotFound, 22P02
// to ErrValidation -- the identical two outcomes Update/Edit already
// produced via the UPDATE's own RETURNING clause (below), just detected one
// query earlier. entity_id is NOT NULL and FK-enforced on invoices
// (migrations/20260714103137_invoices.sql), and immutable after Create
// (UpdateInput carries no EntityID field, [store-update-untouched]), so this
// read cannot itself introduce a NEW cross-tenant vector (AC #7): a
// legitimately-owned invoice's entity was already proven same-tenant at
// Create time by the composite (tenant_id, entity_id) FK
// (M4-06-03/INVCR-01-17's own pre-check), and a cross-tenant OR nonexistent
// invoice id simply 0-rows here exactly as it always 0-rowed at the UPDATE
// -- TestStoreCrossTenant_UpdateGetListRefused (store_test.go, INV-STORE-12,
// left unmodified by this subtask) already covers that outcome. This same
// JOIN also runs on Store's ONE pool (s.pool, the invoice_app role) every
// other method here shares -- there is no second, more-restricted role that
// can reach Store.Update/Store.Edit, so unlike a multi-role deployment,
// there is no RLS-grant-parity gap where invoices are writable but
// business_entities is not: Store.Create's own pre-existing, structurally
// identical business_entities SELECT (INVCR-01-17) already proves this role
// can read it, and this subtask's own specs (which observe a real non-nil
// derived value, not a 0-row fallback) additionally prove it empirically.
//
// Store.Update itself carries NO status/editability guard (pre-existing,
// [D9] -- "never touches status" -- unchanged by this subtask) and has NO
// production caller today (grep-confirmed: only Store.Edit's own step 5 and
// this package's tests call updateContentTx at all; cmd/invoice/main.go
// wires PATCH /v1/invoices/{id} to Store.Edit, never Store.Update
// directly). Store.Edit DOES guard -- canEdit(before.Status), step 3, run
// BEFORE this function is ever reached -- so the wired PATCH path can never
// hit this derivation on a queued/submitted/accepted/failed invoice, and
// this override can never silently diverge from what an APP has already
// received. A hypothetical FUTURE direct Store.Update caller with no such
// guard would not inherit that protection; documented here as a residual,
// not fixed, since there is no live code path to fix (product-advisor
// review, 2026-07-31).
func updateContentTx(ctx context.Context, tx pgx.Tx, id string, in UpdateInput) (Invoice, []string, error) {
	var entityName string
	var entityTIN *string
	var storedSupplierTIN, storedSupplierName *string
	if err := tx.QueryRow(ctx,
		`SELECT be.name, be.tin, i.supplier_tin, i.supplier_name
		 FROM invoices i JOIN business_entities be ON be.id = i.entity_id
		 WHERE i.id = $1`, id,
	).Scan(&entityName, &entityTIN, &storedSupplierTIN, &storedSupplierName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, nil, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return Invoice{}, nil, ErrValidation
		}
		return Invoice{}, nil, err
	}
	derivedSupplierTIN := MBSSupplierTIN(entityTIN)

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

	// supplier_tin/supplier_name: ALWAYS written, derived from the entity
	// resolved above -- see this function's own doc comment for the full
	// rationale, incl. why they bypass set() yet are conditionally still
	// named in changedFields when they actually changed.
	args = append(args, derivedSupplierTIN)
	setClauses = append(setClauses, fmt.Sprintf(text, "supplier_tin", len(args)))
	args = append(args, entityName)
	setClauses = append(setClauses, fmt.Sprintf(text, "supplier_name", len(args)))
	if !strPtrEqual(storedSupplierTIN, derivedSupplierTIN) {
		changedFields = append(changedFields, "supplier_tin")
	}
	if storedSupplierName == nil || *storedSupplierName != entityName {
		changedFields = append(changedFields, "supplier_name")
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

// Edit is M4-05-02's fix-loop orchestrator (System Design §4; widened by
// task-237 to a third fixable status): the edit + demote-to-draft sequence
// over draft, validated, and rejected, composed with Store.ApplyValidation's
// template ([A2]: one
// WithinRequestTenantTx, lock-then-recheck, propagate raw errors so their
// SQLSTATE survives). Inside ONE db.WithinRequestTenantTx:
//
//  1. nothing-to-do guard (checked BEFORE any tx opens, mirroring Store.Update's
//     own guard, [A7]) -- ErrValidation. WIDENED by INVED-01-04: refused only
//     when there are no header fields AND no line array was sent, because a
//     lines-only edit is legitimate.
//  2. lock+read `before`: SELECT <invoiceColumns> ... FOR UPDATE, same lock
//     and error mapping as ApplyValidation/Transition (pgx.ErrNoRows ->
//     ErrNotFound; 22P02 -> ErrValidation).
//  3. fixable-state guard -- before.Status must be draft, validated, or
//     rejected, else ErrNotFixable, NOTHING written. This runs BEFORE the
//     content write, so a not-fixable status wins over a malformed numeric
//     in the same call ([A8], GuardBeforeContentValidation).
//  4. preFP := contentFingerprint(before, beforeLines) -- taken on the LOCKED
//     row, so it is authoritative under concurrency the same way
//     ApplyValidation's re-check is. beforeLines comes from hydrateLinesTx on
//     THIS tx: scanInvoice leaves LineItems nil and the fingerprint takes its
//     lines explicitly ([fingerprint-explicit-lines-param]).
//  5. updateContentTx writes the header content (shared with Store.Update, no
//     audit of its own) -- but ONLY when header fields were actually sent. It
//     assumes >= 1 non-nil field, so calling it with an all-nil UpdateInput
//     would emit `UPDATE invoices SET  WHERE ...` and fail with a syntax error;
//     hence the hasHeader gate. A lines-only edit skips it entirely. Then
//     replaceLinesTx replaces the WHOLE line set -- but ONLY when in.LineItems
//     is non-nil ([line-items-optional]): nil leaves the stored lines exactly
//     as they are, a present-but-empty slice legitimately removes all of them.
//     Its RETURNING rows are afterLines; when no array was sent, afterLines is
//     beforeLines, because nothing was written.
//  6. DB-authoritative no-op check: contentFingerprint(after, afterLines) ==
//     preFP means nothing really changed (either every field was resent
//     unchanged, or only its NUMERIC SCALE changed and Postgres normalized it
//     away, e.g. "100.00"->"100.0") -- return `after` with no audit, no
//     demotion, no history row ([A6]: idempotence applies to draft AND
//     validated). The post-write lines are still attached to the return: a
//     content-identical resend churns the line ids (replace-all always
//     deletes and re-inserts, [fingerprint-excludes-line-ids]) and the caller
//     must see the ids that are actually stored, never the pre-edit ones.
//  7. audit.Record("invoice.updated") -- a real content change, always. Its
//     fields array carries what was SUBMITTED (updateContentTx's list), plus
//     the literal "line_items" whenever an array was sent
//     ([audit-fields-includes-line-items]), so a lines-only edit audits
//     fields: ["line_items"].
//  8. demotes to draft whenever the state machine allows before.Status -> draft,
//     via transitionTx on THIS same tx -- the real
//     source status, never a hardcoded literal, so the history row's
//     from_status stays truthful (task-237). A rejected `before`'s
//     rejection_reasons are RETAINED across the demotion, not cleared
//     ([reason-lifecycle], reversed by M5-09-02/task-255) -- the only
//     remaining clear lives in transitionTx itself, gated on
//     target == accepted, because POST /transitions {"target":"accepted"}
//     bypasses MarkAcceptedTx entirely and must clear too. Either way the
//     content write and the demotion are one atomic unit -- a failure at
//     either step (including this audit's own actor CHECK) rolls back the
//     whole edit, never a partial one (ContentAuditFailureRollsBackWholeEdit),
//     the line replace included. A draft `before` has nothing to demote from
//     and stays draft.
//  9. re-attach afterLines and return `after` -- draft (demoted) after a
//     validated content change, the demoted row's OWN state after a draft
//     content change, or the no-op return from step 6 (a validated no-op stays
//     validated). The lines are re-attached LAST because both updateContentTx
//     and transitionTx return a freshly scanned Invoice, and scanInvoice
//     leaves LineItems nil -- an earlier assignment would be overwritten.
//     Every success path carries them ([edit-response-carries-lines]).
//
// Edit never touches violations/rule_set_version_id -- a demotion leaves the
// prior verdict's stamp deliberately STALE until Store.ApplyValidation
// re-runs and re-stamps it (DemoteThenRevalidateSucceeds closes that loop
// end to end through the gate, completely unmodified by M4-05, [A12]).
func (s *Store) Edit(ctx context.Context, id string, in EditInput) (Invoice, error) {
	hasHeader := headerFieldsPresent(in.UpdateInput)
	if !hasHeader && in.LineItems == nil {
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
		if !canEdit(before.Status) {
			return ErrNotFixable
		}

		// 4. the locked row's fingerprint, taken before the write.
		// scanInvoice leaves LineItems nil, so the lines come from an explicit
		// tx-scoped read ([fingerprint-explicit-lines-param]) -- the invoice
		// row's FOR UPDATE above already serializes them.
		beforeLines, err := hydrateLinesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		preFP := contentFingerprint(before, beforeLines)

		// 5. the header write, shared with Store.Update -- gated on hasHeader
		// because updateContentTx assumes at least one non-nil field and would
		// otherwise emit an empty SET clause. A lines-only edit skips it and
		// carries `before` forward untouched.
		var after Invoice
		var changed []string
		if hasHeader {
			if after, changed, err = updateContentTx(ctx, tx, id, in.UpdateInput); err != nil {
				return err
			}
		} else {
			after = before
		}

		// 5b. the line write -- replace-all, only when an array was actually
		// sent. afterLines are replaceLinesTx's RETURNING rows, i.e. the lines
		// as they now stand in the DB; when nothing was written they are
		// beforeLines, which is then post-write by definition.
		afterLines := beforeLines
		if in.LineItems != nil {
			if afterLines, err = replaceLinesTx(ctx, tx, callerID.TenantID, id, *in.LineItems); err != nil {
				return err
			}
		}

		// 6. DB-authoritative no-op check -- nothing to audit, demote, or
		// record history for.
		//
		// Both arguments must be POST-write. Feeding beforeLines here once Edit
		// can write lines would be a SILENT bug, not a compile error: a
		// lines-only edit would hash both sides over the pre-edit lines, come
		// out equal, take this no-op path, and return with no audit row, no
		// demotion and no history -- an edit that visibly changed the invoice
		// reported as a no-op, which is precisely the Core AC 2 compliance hole.
		// beforeLines is legitimate on this side ONLY through the afterLines
		// assignment above, in the branch where no line write happened at all.
		if contentFingerprint(after, afterLines) == preFP {
			after.LineItems = afterLines
			inv = after
			return nil
		}

		// 6b. [kept-marks-clear-on-edit] (INVCR-01-15, D6, AC #8): a genuine content
		// change invalidates any recorded keep-as-is reason. Gated on `before` --
		// the pre-edit, locked row -- actually carrying a mark: the marks can only
		// ever be set on a draft (invoices_kept_as_is_draft_only), so this never
		// fires for a validated/rejected `before`, and skipping it for the
		// overwhelmingly common un-kept case avoids a pointless UPDATE on every
		// ordinary edit. A standalone statement (not folded into updateContentTx,
		// which Store.Update also shares and which this story does not touch) so
		// it applies uniformly whether this edit changed header fields, lines
		// only, or both -- a lines-only edit never runs updateContentTx at all.
		// `after` is safe to overwrite here: step 9 re-attaches afterLines LAST
		// regardless of what happened in between.
		if before.KeptAsIsAt != nil {
			if err := scanInvoice(tx.QueryRow(ctx,
				`UPDATE invoices SET kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL
				 WHERE id = $1 RETURNING `+invoiceColumns, id,
			), &after); err != nil {
				return err
			}
		}

		// 7. the content change is real -- audit it. `fields` lists what was
		// SUBMITTED (updateContentTx's own list), plus "line_items" whenever an
		// array was sent ([audit-fields-includes-line-items]); a lines-only
		// edit therefore audits fields: ["line_items"]. Built as a fresh slice
		// rather than appended in place, so updateContentTx's returned slice is
		// never aliased -- and so a lines-only edit's nil `changed` marshals as
		// ["line_items"] rather than the JSON null a nil []string would give.
		fields := changed
		if in.LineItems != nil {
			fields = append(append([]string{}, changed...), "line_items")
		}
		// invoice_number is immutable, so before and after agree.
		if err := audit.Record(ctx, tx, callerID.Subject, "invoice.updated", map[string]any{
			"id":             id,
			"fields":         fields,
			"invoice_number": before.InvoiceNumber,
		}); err != nil {
			return err
		}

		// 8. demote whenever the state machine allows before.Status -> draft --
		// DERIVED from legalTransitions, never a hand-maintained status literal
		// (AC #11): a future edge that widened canEdit without symmetrically
		// widening a literal here would let Edit commit on a non-draft invoice
		// WITHOUT demoting it, a silent Core AC 2 violation. Deliberately not
		// canEdit(before.Status), which also admits draft -- draft has nothing
		// to demote from and transitionTx(draft->draft) is ErrIllegalTransition.
		// A rejected invoice's rejection_reasons are RETAINED across the
		// demotion (reversed by M5-09-02/task-255, [reason-lifecycle]) -- the
		// only remaining clear lives in transitionTx, gated on
		// target == accepted.
		if canTransition(before.Status, StatusDraft) {
			if after, err = transitionTx(ctx, tx, id, before.Status, StatusDraft, actorFromContext(ctx)); err != nil {
				return err
			}
			// 8b. no run outlives the promotion it belonged to (APPR-06-07, D37).
			// Hooked BELOW step 6's no-op return, so an unchanged edit cancels
			// nothing (TestEdit_NoOpEditCancelsNothing).
			if _, err := approval.CancelLiveRunTx(ctx, tx, id, before.InvoiceNumber, callerID.Subject); err != nil {
				return err
			}
		}

		// 9. re-attach the post-write lines LAST: both updateContentTx and
		// transitionTx return a freshly scanned Invoice and scanInvoice leaves
		// LineItems nil, so an earlier assignment would be silently dropped.
		after.LineItems = afterLines
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
// has no reverse). task-237 adds the final three: queued->accepted and
// queued->rejected (the synchronous-verdict shortcuts a worker takes when
// the APP answers immediately, bypassing submitted entirely) and
// rejected->draft (the second recovery edge, mirroring validated->draft) --
// rejected is therefore no longer a terminal, gaining exactly ONE outgoing
// edge. 11 edges total; accepted and failed remain the only true terminals.
// [failed-invoices] deliberately excludes failed->queued: a dead-lettered
// invoice is never auto-retried back into the queue.
var legalTransitions = map[Status][]Status{
	StatusDraft:     {StatusValidated},
	StatusValidated: {StatusQueued, StatusDraft},
	StatusQueued:    {StatusSubmitted, StatusFailed, StatusAccepted, StatusRejected},
	StatusSubmitted: {StatusAccepted, StatusRejected, StatusFailed},
	StatusRejected:  {StatusDraft},
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

// canEdit reports whether an invoice in status s may be edited -- the SINGLE
// statement of Store.Edit's fixable-state rule (INVED-01-03, Core AC 1/2/4),
// DERIVED from legalTransitions rather than restated as a status literal. An
// invoice is editable iff it is already a draft, or the machine lets it
// become one: draft is editable by definition (there is no draft->draft
// self-edge, and Transition rejects self-edges as ErrRedundantTransition
// anyway), every other editable status is editable precisely because Edit's
// step 8 can demote it back to draft in the same tx.
//
// Exactly ONE hop, never reachability. A transitive/BFS reading would admit
// queued and submitted -- both have real 2-hop paths to draft via rejected
// (queued->rejected->draft, submitted->rejected->draft) -- which would let a
// caller edit an invoice already handed to the APP. Editability is "can this
// invoice be demoted to draft by the edit itself", and Edit demotes with a
// single transitionTx call, so one hop is the whole rule.
//
// Yields {draft, validated, rejected} on today's table -- byte-identical to
// the three-status guard it replaces -- and tracks legalTransitions
// automatically if the table ever changes (TestCanEdit_TracksLegalTransitions
// perturbs the table at runtime to prove this is a derivation and not a
// hand-copied list).
func canEdit(s Status) bool {
	return s == StatusDraft || canTransition(s, StatusDraft)
}

// canRevalidate reports whether the validation gate may run on status s --
// the SINGLE statement of the gate's draft-only rule, shared by Gate.Validate's
// advisory pre-check and Store.ApplyValidation's authoritative in-tx re-check
// (INVED-01-03, Core AC 3).
//
// Deliberately a hand-written literal, NOT derived from
// canTransition(s, StatusValidated), even though the two agree on today's
// table. Deriving it would be a FALSE coupling: ApplyValidation promotes via
// transitionTx(..., StatusDraft, StatusValidated, ...) with the FROM state
// hardwired to draft, so an inbound edge to validated from some other status
// would make a derived gate advertise re-validation on a status whose write
// still 409s. Draft-only-ness is the gate's OWN contract, not an edge-table
// property. TestCanRevalidate_AgreesWithThePromotionEdge is the tripwire: it
// goes red the day such an edge is added, forcing a human decision on whether
// the gate widens -- so it must be satisfied by this literal being right, never
// by weakening the test. Draft-only is also what keeps ApplyValidation's step 5b
// from double-arming: widening this now requires cancelling the live run first,
// or the widened path 23505s on approval_runs_one_open.
func canRevalidate(s Status) bool { return s == StatusDraft }

// canSubmit is a deliberate literal, not canTransition(s, StatusQueued):
// BatchSubmit hardwires its FROM state to validated, so submittability is
// the endpoint's own contract, not an edge-table property.
//
// It is only HALF the positive predicate. The other half is
// approval.TransmitClear (internal/approval/gate.go), which a validated invoice
// must also satisfy before it may pass into queued. The two are composed in
// submitGate (handlers.go), never here: approval is not a status property, so
// this stays a pure status literal.
func canSubmit(s Status) bool { return s == StatusValidated }

// isApprover takes the memberships.role, not auth.Identity.Role -- the latter
// is the GoTrue role, always "authenticated" (gateway.go:160-170).
func isApprover(role string) bool { return role == "admin" || role == "reviewer" }

// callerRoleTx is CallerRole's query run on an already-open tx -- the shape
// ResolveOutside/UnresolveOutside inline as their own first step, so the
// permission check shares one transaction with the row lock instead of
// opening a second (nesting db.WithinRequestTenantTx's own pool.Begin from
// inside a closure has no precedent in this codebase and risks pool
// exhaustion). Fails closed like CallerRole: no row is ("", nil), never an
// error — and a suspended or invited member IS no row, so suspension refuses
// the isApprover-gated writes rather than only changing a pill
// (TestStore_ResolveOutside_SuspendedApproverRefused).
func callerRoleTx(ctx context.Context, tx pgx.Tx, subject string) (string, error) {
	var role string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships WHERE user_id = $1 AND status = 'active'`, subject,
	).Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return role, nil
}

// CallerRole reads the caller's memberships.role, RLS-scoped: a thin
// WithinRequestTenantTx wrapper around callerRoleTx. Exported for the handler
// layer, which needs the role without a write.
func (s *Store) CallerRole(ctx context.Context) (string, error) {
	var role string
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, _ := auth.IdentityFromContext(ctx)
		r, err := callerRoleTx(ctx, tx, id.Subject)
		if err != nil {
			return err
		}
		role = r
		return nil
	})
	if err != nil {
		return "", err
	}
	return role, nil
}

// ApprovalFacts is one invoice's approval standing as internal/invoice reads it:
// approval.GateFacts with the transmit verdict already resolved against
// APPROVALS_ENFORCED. TransmitClear is the ONLY field the flag touches -- the
// other three feed can_approve/can_reject, which ship unflagged
// (docs/approvals.md section 11).
type ApprovalFacts struct {
	TransmitClear   bool
	RunState        string
	PendingStepOrd  *int
	CallerHoldsRole bool
}

// ApprovalFacts reads id's approval standing for the caller inside ONE
// db.WithinRequestTenantTx -- CallerRole's wrapper above, for the same reason: a
// read with no write needs no second transaction. The approval read runs
// whatever the flag says; only TransmitClear folds it
// (TestStoreApprovalFacts_ReadsRunFactsEvenWithTheFlagOff), deliberately unlike
// the two write doors, which skip the read entirely when the flag is off. An
// error returns the ZERO value, whose TransmitClear is false, so a caller that
// ignores the error still fails closed.
func (s *Store) ApprovalFacts(ctx context.Context, id string) (ApprovalFacts, error) {
	var out ApprovalFacts
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := approval.GateFactsTx(ctx, tx, id, actorFromContext(ctx).Subject)
		if err != nil {
			return err
		}
		out = ApprovalFacts{
			TransmitClear:   !s.approvalsEnforced || approval.TransmitClear(f.PolicyActive, f.ApprovedRun),
			RunState:        f.RunState,
			PendingStepOrd:  f.PendingStepOrd,
			CallerHoldsRole: f.CallerHoldsRole,
		}
		return nil
	})
	if err != nil {
		return ApprovalFacts{}, err
	}
	return out, nil
}

// ListGateFacts is the page's approve-gate input, resolved once per request: the
// caller's membership role, plus which invoices' pending workflow role the caller
// actually holds, keyed by invoice id. An absent id reads false -- fail closed.
type ListGateFacts struct {
	CallerRole       string
	HoldsPendingRole map[string]bool
}

// RowFacts reads the list-row approval standing of a page of invoice ids, plus the
// caller's gate inputs, in ONE transaction. Unlike ApprovalFacts above it must NOT
// consult s.approvalsEnforced: the flag gates enforcement, not visibility
// (docs/approvals.md section 11, TestStoreRowFacts_DoesNotConsultApprovalsEnforced).
// RLS is the only tenant scope (TestStoreRowFacts_IsTenantScopedByRLS).
//
// The two gate reads are here rather than inside approval.RowFactsTx so that helper's
// statement count stays five (TestRowFactsTx_FiveStatementsRegardlessOfRowAndRoleCount);
// both are set-shaped, so the whole request stays constant in page size.
func (s *Store) RowFacts(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
	var out map[string]approval.RowFacts
	var gate ListGateFacts
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		facts, err := approval.RowFactsTx(ctx, tx, ids)
		if err != nil {
			return err
		}

		subject := actorFromContext(ctx).Subject
		role, err := callerRoleTx(ctx, tx, subject)
		if err != nil {
			return err
		}

		keys := []string{}
		seen := map[string]bool{}
		for _, f := range facts {
			if f.PendingRoleKey != nil && !seen[*f.PendingRoleKey] {
				seen[*f.PendingRoleKey] = true
				keys = append(keys, *f.PendingRoleKey)
			}
		}
		held, err := approval.HeldRoleKeysTx(ctx, tx, keys, subject)
		if err != nil {
			return err
		}

		holds := make(map[string]bool, len(facts))
		for id, f := range facts {
			if f.PendingRoleKey != nil && held[*f.PendingRoleKey] {
				holds[id] = true
			}
		}
		out, gate = facts, ListGateFacts{CallerRole: role, HoldsPendingRole: holds}
		return nil
	})
	if err != nil {
		return nil, ListGateFacts{}, err
	}
	return out, gate, nil
}

// Transition is the PUBLIC, request-scoped status change (M4-02-02, System
// Design [D1]/[D2]/[D4]/[D11]) and one of transitionTx's exactly two callers
// (M4-04-05's extraction moved the SOLE-writer-of-invoices.status role down
// to transitionTx, which both callers must go through; Transition's own
// observable behaviour is unchanged). Inside ONE db.WithinRequestTenantTx
// closure:
// SELECT id, status FROM invoices WHERE id=$1 FOR UPDATE locks and reads the
// current status (RLS-scoped, so a cross-tenant VALID uuid 0-rows same as a
// genuinely nonexistent one; pgx.ErrNoRows -> ErrNotFound; a malformed
// non-uuid id raises 22P02, mapped to ErrValidation, mirroring Get/Update/
// Create -- CodeRabbit finding) -> a no-op (current==target)
// -> ErrRedundantTransition (checked FIRST, [D4], before legality, and so
// retained HERE rather than in transitionTx) -> then, on the ONE legal edge
// into queued and only when APPROVALS_ENFORCED is on, the transmit gate:
// approval.TransmitClearTx on this same tx, after the lock so the answer
// cannot be stale (TestTransition_GateRunsAfterTheRowLock) -> not clear ->
// ErrAwaitingApproval -> then transitionTx on this
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
		var lockedID string
		var current Status
		if err := tx.QueryRow(ctx,
			`SELECT id, status FROM invoices WHERE id = $1 FOR UPDATE`, id,
		).Scan(&lockedID, &current); err != nil {
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

		// Keyed on the LOCKED row's id — TransmitClearTx maps Postgres's canonical
		// uuid text, so a non-canonical id from the URL would read false
		// (TestTransition_UppercaseIdOnAnApprovedInvoiceReachesQueued). The
		// canTransition conjunct keeps an illegal edge reading ErrIllegalTransition
		// (TestTransition_IllegalEdgeIntoQueuedStillReadsIllegal).
		if s.approvalsEnforced && target == StatusQueued && canTransition(current, target) {
			clear, err := approval.TransmitClearTx(ctx, tx, []string{lockedID})
			if err != nil {
				return err
			}
			if !clear[lockedID] {
				return ErrAwaitingApproval
			}
		}

		var err error
		if inv, err = transitionTx(ctx, tx, id, current, target, actorFromContext(ctx)); err != nil {
			return err
		}

		// -> draft ONLY (APPR-06-07, D30). validated -> queued still leaves the run
		// alone: cancelling there would erase the drift APPR-06-09's
		// approval_run_orphaned detector reads. The gate above narrows that window
		// rather than closing it — the edge still passes when no policy is active.
		if target == StatusDraft {
			if _, err := approval.CancelLiveRunTx(ctx, tx, id, inv.InvoiceNumber, actorFromContext(ctx).Subject); err != nil {
				return err
			}
		}
		return nil
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
// It has exactly SEVEN callers today — Store.Transition (store.go),
// Store.ApplyValidation (store.go), Store.Edit's demotion branch
// (store.go), DemoteApprovalRejectedTx (store.go), Submitter.BatchSubmit
// (batch_submit.go), Store.DemoteRevalidatedTx (revalidate.go), and
// Store.markTerminalTx (actor.go, shared by MarkSubmittedTx/MarkFailedTx) —
// FOUR of the seven live in store.go — and remains the SINGLE writer of
// invoices.status, with legalTransitions/
// canTransition still the single source of truth for what is legal. That is
// what PRESERVES the M4 gate's "illegal state transitions are rejected by the
// single transition function" no matter how many callers accrue: none of
// them can reach the UPDATE without passing canTransition.
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

	// [reason-lifecycle] (M5-09-02/task-255, reversing M5-05's wipe-on-
	// demotion): accepted is the only status whose entry clears
	// rejection_reasons, and this is the ONLY place that still does so, now
	// that Store.Edit's rejected->draft demotion retains them. It has to
	// live HERE rather than in MarkAcceptedTx's outcome closure (actor.go)
	// because accepted has a second live writer that never calls
	// MarkAcceptedTx: POST /transitions {"target":"accepted"} on a queued
	// invoice (legalTransitions[StatusQueued], unguarded by
	// TransitionHandler, which refuses only validated). Riding the same
	// UPDATE ... RETURNING keeps the clear atomic with the status write for
	// BOTH callers -- no second statement, so a rolled-back transition never
	// leaves an observable partial clear. '[]' is a SQL literal, not a bind
	// parameter, so it does not renumber $2 (id).
	//
	// Deliberately NOT symmetric with rejected, even though the same
	// reasoning applies there too (a residual risk recorded, not coded
	// around, in M5-09-02's Stage-1 architecture validation, finding F3):
	// rejected's two writers are MarkRejectedTx's outcome closure and the
	// identical {"target":"rejected"} handler path, and markTerminalTx runs
	// the outcome callback BEFORE transitionTx (actor.go) -- so clearing on
	// rejected here would wipe the reasons MarkRejectedTx had just written
	// moments earlier in the SAME tx, destroying every APP rejection reason.
	// [kept-marks-clear-on-promote] (INVCR-01-15, D6, task-291): draft has exactly
	// ONE outgoing edge (draft->validated, legalTransitions), so target ==
	// StatusValidated unambiguously means "promoting a draft" -- no other status
	// transitions into validated. The invoices_kept_as_is_draft_only CHECK
	// FORCES this clear to happen: a kept invoice promoted without it would
	// 23514 on this very UPDATE (status becomes non-draft while the marks are
	// still set) -- the intended failure mode, a loud CI red rather than a
	// stale KEPT badge on a validated row. Nulling all three unconditionally
	// (never gated on "was it actually kept") is deliberate: it is a no-op
	// on the overwhelmingly common un-kept case and keeps this single UPDATE
	// the SAME UPDATE that promotes, per the story's own requirement, rather
	// than a second statement that could be forgotten or fail independently.
	setClause := "status = $1"
	switch target {
	case StatusAccepted:
		setClause = "status = $1, rejection_reasons = '[]'"
	case StatusValidated:
		setClause = "status = $1, kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL"
	}

	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`UPDATE invoices SET `+setClause+` WHERE id = $2 RETURNING `+invoiceColumns,
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
		"id":             id,
		"from":           current,
		"to":             target,
		"invoice_number": inv.InvoiceNumber,
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
//  3. content re-check — contentFingerprint(locked, lockedLines) !=
//     evaluatedFingerprint -> ErrStaleValidation. FOR UPDATE makes this EXACT:
//     Store.Update's UPDATE serializes against the lock, so the locked row is
//     authoritative. lockedLines comes from hydrateLinesTx on THIS tx —
//     scanInvoice leaves LineItems nil, and reading them off the pool instead
//     would silently see zero rows under RLS (see hydrateLinesTx).
//  4. stamp violations + rule_set_version_id (always — the version is stamped
//     even on a blocking verdict; "these violations came from THAT rule set"
//     is exactly what makes the verdict auditable).
//  5. promote via transitionTx iff nothing blocks — the same tx, hence the
//     extraction.
//     5b. arm the approval run on that promotion (approval.ArmTx), same tx.
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
		// This is the AUTHORITATIVE guard (Gate.Validate's is advisory), so
		// widening canRevalidate widens what actually gets written here — and
		// step 5's transitionTx still hardwires FROM=draft. See canRevalidate.
		if !canRevalidate(locked.Status) {
			return ErrNotDraft
		}

		// 3. content re-check — the invoice must not have been edited under
		// the run; the status check above cannot see an edit. scanInvoice
		// leaves LineItems nil, so the locked row's lines are read explicitly
		// INSIDE this tx ([fingerprint-explicit-lines-param]): the evaluated
		// fingerprint was taken over a Get-hydrated invoice, so hashing no
		// lines here would make every lined invoice fail the re-check.
		lockedLines, err := hydrateLinesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if contentFingerprint(locked, lockedLines) != evaluatedFingerprint {
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
			// 5b. Armed inside the one !blocked gate so a second gate cannot drift.
			// No active policy short-circuits in ArmTx; a 23505 here means a live run
			// survived a demotion (APPR-06-07 cancels), so it is not swallowed.
			if _, err := approval.ArmTx(ctx, tx, callerID.TenantID, id, evaluatedFingerprint, callerID.Subject); err != nil {
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
			"invoice_number":      locked.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// KeepAsIs is D6's auditable-triage write (INVCR-01-15, task-291): records who/when/why
// an operator chose to keep a failing draft rather than fix it, WITHOUT touching status
// or legalTransitions at all -- this method never calls transitionTx. Inside ONE
// db.WithinRequestTenantTx:
//
//  1. SELECT <invoiceColumns> ... FOR UPDATE -- RLS-scoped, so a cross-tenant VALID uuid
//     0-rows exactly like a genuinely nonexistent one (pgx.ErrNoRows -> ErrNotFound); a
//     malformed non-uuid id raises 22P02 -> ErrValidation, mirroring every other
//     lock-and-read method in this file.
//  2. the keepable guard -- draft AND at least one severity:"error" violation, else
//     ErrNotKeepable, NOTHING written. Keeping a clean invoice is meaningless (there is
//     nothing being suppressed); a non-draft invoice cannot reach here in practice
//     either (invoices_kept_as_is_draft_only would refuse the write anyway, but this
//     guard is what turns that DB-level refusal into an honest 409 instead of a raw
//     23514 surfacing as a 500).
//  3. UPDATE ... SET the triple, RETURNING <invoiceColumns> -- re-keeping an
//     already-kept invoice is legal (a changed mind about the reason), overwriting the
//     prior at/by/reason.
//  4. audit.Record(ctx, tx, subject, "invoice.kept_as_is", {id, reason, invoice_number}) in the SAME
//     transaction as the column write (this package's standing convention,
//     invoice.go:1-9 / the audit_log CHECKs' own actor-length failure mode) -- a failed
//     audit write rolls the column write back too.
//
// reason arrives already trimmed and non-empty/within-bound (KeepAsIsHandler's own
// pre-tx guard, mirroring BatchSubmitHandler's idempotency_key validation) -- this
// method does not re-validate it, matching Store.Edit/Store.Update's own division of
// labour between handler-level shape checks and store-level domain guards.
func (s *Store) KeepAsIs(ctx context.Context, id, reason string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

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

		var vs []Violation
		if err := json.Unmarshal(locked.Violations, &vs); err != nil {
			return err
		}
		if locked.Status != StatusDraft || !hasBlockingViolation(vs) {
			return ErrNotKeepable
		}

		now := time.Now().UTC()
		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = $1, kept_as_is_by = $2, kept_as_is_reason = $3
			 WHERE id = $4 RETURNING `+invoiceColumns,
			now, callerID.Subject, reason, id,
		), &inv); err != nil {
			return err
		}

		return audit.Record(ctx, tx, callerID.Subject, "invoice.kept_as_is", map[string]any{
			"id":             id,
			"reason":         reason,
			"invoice_number": inv.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// UnkeepAsIs is KeepAsIs's un-do (INVCR-01-15, task-291): clears the triple and audits
// "invoice.unkept_as_is". Inside ONE db.WithinRequestTenantTx: the same RLS-scoped
// lock+read as KeepAsIs (cross-tenant/unknown -> ErrNotFound, malformed id ->
// ErrValidation), then an IDEMPOTENT no-op when the invoice is not currently kept
// (locked.KeptAsIsAt == nil) -- nothing written, nothing audited, mirroring
// markTerminalTx's own already-at-target short-circuit (actor.go) -- else the clearing
// UPDATE plus the audit row, same transaction.
func (s *Store) UnkeepAsIs(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

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

		// A failed invoice's mark is ResolveOutside's, cleared only by
		// UnresolveOutside -- UnkeepAsIs stays draft-only.
		if locked.KeptAsIsAt == nil || locked.Status != StatusDraft {
			inv = locked
			return nil
		}

		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL
			 WHERE id = $1 RETURNING `+invoiceColumns, id,
		), &inv); err != nil {
			return err
		}

		return audit.Record(ctx, tx, callerID.Subject, "invoice.unkept_as_is", map[string]any{
			"id":             id,
			"invoice_number": inv.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// ResolveOutside marks a failed invoice resolved outside the system: an
// approver-only (isApprover) stamp of kept_as_is_at/by/reason, auditing
// "invoice.resolved_outside" in the same transaction as the write. The
// permission check (callerRoleTx) runs BEFORE the row lock, inside the SAME
// transaction, so a non-approver cannot probe id existence via a
// 403-vs-404/409 distinction. Never calls transitionTx -- status is untouched.
// Re-resolving an already-resolved invoice is legal and overwrites
// at/by/reason, same as KeepAsIs.
func (s *Store) ResolveOutside(ctx context.Context, id, reason string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		role, err := callerRoleTx(ctx, tx, callerID.Subject)
		if err != nil {
			return err
		}
		if !isApprover(role) {
			return ErrNotPermitted
		}

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

		if locked.Status != StatusFailed {
			return ErrNotResolvable
		}

		now := time.Now().UTC()
		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = $1, kept_as_is_by = $2, kept_as_is_reason = $3
			 WHERE id = $4 RETURNING `+invoiceColumns,
			now, callerID.Subject, reason, id,
		), &inv); err != nil {
			return err
		}

		return audit.Record(ctx, tx, callerID.Subject, "invoice.resolved_outside", map[string]any{
			"id":             id,
			"reason":         reason,
			"invoice_number": inv.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}

// UnresolveOutside is ResolveOutside's un-do: same in-tx permission-before-lock
// and not-failed guards, then an idempotent no-op (no write, no audit) if the
// invoice is not currently resolved, else clears the triple and audits
// "invoice.unresolved_outside" in the same transaction.
func (s *Store) UnresolveOutside(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		callerID, _ := auth.IdentityFromContext(ctx)

		role, err := callerRoleTx(ctx, tx, callerID.Subject)
		if err != nil {
			return err
		}
		if !isApprover(role) {
			return ErrNotPermitted
		}

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

		if locked.Status != StatusFailed {
			return ErrNotResolvable
		}

		if locked.KeptAsIsAt == nil {
			inv = locked
			return nil
		}

		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL
			 WHERE id = $1 RETURNING `+invoiceColumns, id,
		), &inv); err != nil {
			return err
		}

		return audit.Record(ctx, tx, callerID.Subject, "invoice.unresolved_outside", map[string]any{
			"id":             id,
			"invoice_number": inv.InvoiceNumber,
		})
	})
	if err != nil {
		return Invoice{}, err
	}
	return inv, nil
}
