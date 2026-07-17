// Package invoice is the 03 Invoice context: the CRUD and lifecycle surface
// for a tenant's invoices — the canonical record the import -> validate ->
// fix -> re-validate loop (M4-03/M4-04/M4-05) reads and writes. Every store
// method wraps db.WithinRequestTenantTx, so writes and reads are scoped to
// the caller's tenant under RLS; every mutation writes an audit.Record row
// (plus, for Create, a genesis invoice_status_history row) in the SAME
// transaction as the domain change, so a failed audit/history write rolls
// back the domain write too (mirrors internal/portfolio's [A5] convention).
//
// This file establishes the domain types and error model shared by the
// Create/Get/List/Update/Transition store methods (store.go) and the HTTP
// handlers (handlers.go).
package invoice

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Status is one of the seven canonical invoice lifecycle states, matching the
// invoices.status / invoice_status_history.to_status CHECK constraint
// (migrations/20260714103137_invoices.sql) [D11].
type Status string

const (
	StatusDraft     Status = "draft"
	StatusValidated Status = "validated"
	StatusQueued    Status = "queued"
	StatusSubmitted Status = "submitted"
	StatusAccepted  Status = "accepted"
	StatusRejected  Status = "rejected"
	StatusFailed    Status = "failed"
)

// valid reports whether s is one of the seven canonical invoice lifecycle
// states. TransitionHandler ([D12], M4-02-03) uses this to reject an unknown
// target string as 400 "unknown status" BEFORE ever calling Store.Transition.
func (s Status) valid() bool {
	switch s {
	case StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// LineItem is a line_items row: one line of an invoice, always read ordered
// by its system-assigned LineNo (1..N, [D10]). MBS-content fields are
// nullable (store-invalid-faithfully, no CHECK, per the migration's own
// header); Quantity/UnitPrice/LineTotal/LineTax are numeric columns read via
// ::text ([D13]) — never float64 or pgtype.Numeric, to avoid a
// floating-point misrepresentation of money.
type LineItem struct {
	ID          string  `json:"id"`
	LineNo      int     `json:"line_no"`
	Description *string `json:"description"`
	Quantity    *string `json:"quantity"`
	UnitPrice   *string `json:"unit_price"`
	LineTotal   *string `json:"line_total"`
	LineTax     *string `json:"line_tax"`
}

// Invoice is an invoices row plus its hydrated LineItems (Store.Get only;
// Store.List returns headers with LineItems left nil, [D7]/[D8]). Money
// fields (Subtotal/VAT/Total) are *string, read via ::text ([D13]) — see
// LineItem. Violations/RuleSetVersionID are read-only here: M4-02 never
// writes them (M4-04's validate gate owns both). RuleSetVersionID is
// intentionally NOT surfaced on the wire (json:"-") — it is always null in
// M4-02 and M4-04 will define its eventual wire shape; Violations IS
// surfaced (always the literal "[]" here) since it is a real, if currently
// always-empty, part of the row.
type Invoice struct {
	ID               string          `json:"id"`
	EntityID         string          `json:"entity_id"`
	ImportBatchID    *string         `json:"import_batch_id"`
	InvoiceNumber    string          `json:"invoice_number"`
	Status           Status          `json:"status"`
	IssueDate        *time.Time      `json:"issue_date"`
	SupplierTIN      *string         `json:"supplier_tin"`
	SupplierName     *string         `json:"supplier_name"`
	BuyerTIN         *string         `json:"buyer_tin"`
	BuyerName        *string         `json:"buyer_name"`
	Currency         *string         `json:"currency"`
	Subtotal         *string         `json:"subtotal"`
	VAT              *string         `json:"vat"`
	Total            *string         `json:"total"`
	Violations       json.RawMessage `json:"violations"`
	RuleSetVersionID *string         `json:"-"`
	CreatedAt        time.Time       `json:"created_at"`
	LineItems        []LineItem      `json:"line_items,omitempty"`
}

// LineItemInput is one line of Store.Create's CreateInput.LineItems. LineNo
// is deliberately NOT part of this input — it is system-assigned 1..N by the
// slice's array position ([D10]), never caller-supplied.
type LineItemInput struct {
	Description *string
	Quantity    *string
	UnitPrice   *string
	LineTotal   *string
	LineTax     *string
}

// CreateInput is the Store.Create argument. EntityID and InvoiceNumber are
// required (non-empty, [D10]); every other field is optional MBS content
// that Store.Create persists un-rejected even when negative/NULL/blank
// (store-invalid-faithfully, AC-6).
type CreateInput struct {
	EntityID      string
	InvoiceNumber string
	IssueDate     *time.Time
	SupplierTIN   *string
	SupplierName  *string
	BuyerTIN      *string
	BuyerName     *string
	Currency      *string
	Subtotal      *string
	VAT           *string
	Total         *string
	LineItems     []LineItemInput
	ImportBatchID *string
}

// UpdateInput is the Store.Update argument: a partial update over invoices'
// mutable MBS-content columns only. Only non-nil fields are applied; nil
// means "leave unchanged". Deliberately has NO EntityID/InvoiceNumber/Status
// field — identity and lifecycle are not Update's job ([D9]); an all-nil
// UpdateInput is rejected as ErrValidation before any tx opens (a no-op
// UPDATE is forbidden).
type UpdateInput struct {
	IssueDate    *time.Time
	SupplierTIN  *string
	SupplierName *string
	BuyerTIN     *string
	BuyerName    *string
	Currency     *string
	Subtotal     *string
	VAT          *string
	Total        *string
}

// ListFilter is the Store.List query ([D8]): no filters, just pagination —
// Limit/Offset. (Unlike internal/portfolio's ListFilter, there is no
// Status/Q — List's only job here is a paginated, tenant-scoped header
// feed, ordered created_at DESC, id DESC.)
type ListFilter struct {
	Limit  int
	Offset int
}

// Sentinels for the invoice error model. ErrIllegalTransition/
// ErrRedundantTransition are declared here (types live in this file) even
// though Store.Transition itself is a later subtask (M4-02-02) — no method
// returns them yet.
var (
	ErrValidation          = errors.New("invoice: validation")
	ErrNotFound            = errors.New("invoice: not found")
	ErrDuplicateNumber     = errors.New("invoice: duplicate number")
	ErrRedundantTransition = errors.New("invoice: redundant transition")
	ErrIllegalTransition   = errors.New("invoice: illegal transition")

	// ErrUpstream / ErrNoActiveRuleSet are the validation client's error model
	// (validator.go, M4-04-04), declared here to keep one sentinel home per
	// package. Declared by THIS subtask because it is their first consumer:
	// the story originally assigned all three new sentinels to M4-04-05, but
	// the client (order 4) precedes it, so M4-04-05 is narrowed to the two it
	// still owns (ErrNotDraft, ErrStaleValidation) -- otherwise a redeclaration
	// there is a duplicate-declaration CI red. [Stage-1 F3]
	//
	// They are DISTINGUISHABLE on purpose, and the distinction is what
	// M4-04-06's statusForErr maps: ErrUpstream -> 502 (04 is broken or
	// unreachable -- 03 cannot get a verdict), ErrNoActiveRuleSet -> 503 (04 is
	// healthy but has no published rule-set to evaluate against). Both are
	// outages, never verdicts: neither ever means "the invoice is clean".
	ErrUpstream        = errors.New("invoice: upstream validation error")
	ErrNoActiveRuleSet = errors.New("invoice: no active rule-set")
)

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a
// *pgconn.PgError. Copied verbatim from internal/portfolio/portfolio.go's own
// copy (itself copied from internal/platform/db/tenants_kind_test.go), needed
// here to map Store.Create's 23505/23503 to ErrDuplicateNumber/ErrValidation.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
