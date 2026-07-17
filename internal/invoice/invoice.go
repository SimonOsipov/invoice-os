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
// LineItem. Violations/RuleSetVersionID have exactly ONE writer:
// Store.ApplyValidation, M4-04's validate gate (M4-02 wrote neither).
//
// RuleSetVersionID IS surfaced on the wire as rule_set_version_id. M4-02
// deliberately hid it (json:"-") because it was always null then, and
// deferred its eventual shape to M4-04 — this is M4-04 defining it
// (M4-04-05 §c): the stamp is the auditable answer to "which rule-set
// version produced these violations?", so it belongs beside them rather
// than hidden. It stays null until the gate stamps it, and carries no
// omitempty on purpose — an un-validated invoice renders an explicit null
// rather than dropping the key, so a consumer can tell "not yet validated"
// from "field absent". Violations is likewise surfaced, and is the literal
// "[]" until the gate writes — never null (Store.ApplyValidation's
// [violations-write]).
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
	RuleSetVersionID *string         `json:"rule_set_version_id"`
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

	// ErrNotDraft / ErrStaleValidation are Store.ApplyValidation's own error
	// model (M4-04-05), declared here because the gate's two in-tx re-checks
	// are their first consumer. Both are 409s in M4-04-06's statusForErr: the
	// caller asked for something that is no longer true, not something
	// malformed (400) or missing (404).
	//
	// ErrNotDraft — the gate is draft-only ([gate-scope-draft-only]); any
	// other status is refused with NOTHING written. From draft the gate needs
	// only edges that already exist (clean -> draft->validated; blocked ->
	// stay draft). A validated-but-now-dirty invoice would need a
	// validated->draft demotion edge, which does not exist, is asserted
	// ILLEGAL today (transition_test.go:163), and which legalTransitions
	// reserves for the M4-05 fix loop — M4-05 widens the accepted states here
	// and adds that edge.
	//
	// ErrStaleValidation — the invoice's content changed under the validate
	// run ([toctou-staleness]). The write tx cannot span the HTTP call to 04
	// (it would pin a pool connection and hold a row lock under unbounded
	// remote latency), so the gate evaluates with no tx open, then re-checks
	// the LOCKED row's contentFingerprint against the one taken when the
	// payload was built. A mismatch means the violations describe content that
	// no longer exists, and stamping those as validated is the same class of
	// lie [validated-is-earned] forbids. The status re-check alone does NOT
	// catch this — status stays draft across a Store.Update.
	ErrNotDraft        = errors.New("invoice: not draft")
	ErrStaleValidation = errors.New("invoice: stale validation")
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
