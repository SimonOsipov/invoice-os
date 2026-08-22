// reader.go: the pure-Go type layer for the audit-log reader (AUDIT-04). Every function
// body here is a stubbed zero-value return — Query/Facets/the store/the handler are later
// subtasks. No DB, no HTTP; audit.go's "free of internal/platform/db" claim stays true.
package audit

import (
	"encoding/json"
	"time"
)

// Cursor is the decoded keyset position (System Design §3): the (created_at, id) tuple
// used in WHERE (created_at, id) < ($1, $2). Opaque on the wire.
type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

// EncodeCursor uses base64.RawURLEncoding, the repo's convention for identifiers,
// tokens and opaque handles (auth/jwks.go:104; mock_script.go's CSID/QR-payload).
// The one exception is qrcode.RenderBase64 (StdEncoding, because a
// data:image/png;base64, URI needs padded standard base64) — a data-URI payload,
// not an opaque identifier, so it does not apply here.
func EncodeCursor(createdAt time.Time, id int64) string {
	return ""
}

// DecodeCursor is EncodeCursor's inverse. It returns a non-nil error, never a zero
// Cursor, for: empty input, invalid base64, a decoded body with a separator count other
// than one, an unparseable RFC3339Nano timestamp, or a non-numeric / out-of-int64-range
// id. A cursor minted in another tenant decodes fine — RLS in Query (subtask 02) is what
// bounds it, not this codec (D-24).
func DecodeCursor(s string) (Cursor, error) {
	return Cursor{}, nil
}

// CompanyMode is CompanyFilter's three states — see CompanyFilter.
type CompanyMode int

const (
	ModeAllCompanies  CompanyMode = iota // no entity_id predicate: every company plus workspace-level rows
	ModeNamedCompany                     // entity_id = ID()
	ModeWorkspaceOnly                    // entity_id IS NULL
)

// CompanyFilter uses unexported fields and constructor-only construction, unlike
// every other internal/ value type (internal/invoice/actor.go:22's Actor has
// plain exported fields) — deliberately, not by convention. The invariant this
// exists to hold (no way to express "named AND workspace") only holds if
// construction is closed; an exported-field struct literal would reopen exactly
// the swallowing state contract §4 forbids. No precedent is claimed for this
// shape, only for the value-type-in-internal/ pattern generally.
type CompanyFilter struct {
	mode CompanyMode
	id   string
}

// AllCompanies is the no-predicate state: every company plus workspace-level rows.
func AllCompanies() CompanyFilter { return CompanyFilter{} }

// NamedCompany scopes to entity_id = id.
func NamedCompany(id string) CompanyFilter { return CompanyFilter{} }

// WorkspaceOnly scopes to entity_id IS NULL — never entity_id = $1 OR entity_id IS
// NULL (contract §4's swallowing predicate, made unrepresentable by this type).
func WorkspaceOnly() CompanyFilter { return CompanyFilter{} }

// Mode reports which of the three states f is in.
func (f CompanyFilter) Mode() CompanyMode { return f.mode }

// ID is only meaningful when Mode() == ModeNamedCompany.
func (f CompanyFilter) ID() string { return f.id }

// CompanyScope is the closed three-value classification System Design §2's three rules
// derive from an event's entity_id and name (D-28). It is a Go-level split of the SQL
// resolver's NULL bucket, not a database column — see ScopeOf.
type CompanyScope string

const (
	ScopeCompany      CompanyScope = "company"
	ScopeWorkspace    CompanyScope = "workspace"
	ScopeUnattributed CompanyScope = "unattributed"
)

// firmWideEvents are the twelve genuinely firm-wide event names — the whole of the
// Policies (4), Roles (4), Memberships (2) and Validation-rule (2) domains (§2 rule 2).
// Hand-maintained: nothing derives this set from the SQL resolver.
var firmWideEvents = map[string]struct{}{
	"approval_policy.created":   {},
	"approval_policy.updated":   {},
	"approval_policy.published": {},
	"approval_policy.deleted":   {},
	"workflow_role.created":     {},
	"workflow_role.updated":     {},
	"workflow_role.deleted":     {},
	"workflow_role.staffed":     {},
	"membership.suspended":      {},
	"membership.reactivated":    {},
	"validation.rule.enabled":   {},
	"validation.rule.disabled":  {},
}

// ScopeOf classifies one event (§2): entity_id set → company; entity_id nil and event in
// firmWideEvents → workspace; otherwise → unattributed. Rule 3 is the fallback so an
// unclassified event fails safe as "we do not know" rather than falsely claiming
// "this was firm-wide" (D-28) — it also catches the three document.* events and an
// invoice-scoped event whose invoice is gone or invisible. Pure Go, no DB.
func ScopeOf(event string, entityID *string) CompanyScope {
	return ""
}

// Event is one row of the page (System Design §2). ActorName and ActorKind are plain
// strings, never pointers, so they cannot marshal as JSON null — the same rule as
// invoice.StatusChange (docs/audit-log-read-contract.md §9). Actor stays byte-identical
// to the stored column. EntityID/CompanyName/CompanyScope together encode the four
// states in §2's table; see ScopeOf for how CompanyScope is derived.
type Event struct {
	ID           string          `json:"id"`
	CreatedAt    time.Time       `json:"created_at"`
	Event        string          `json:"event"`
	Actor        string          `json:"actor"`
	ActorName    string          `json:"actor_name"`
	ActorKind    string          `json:"actor_kind"`
	EntityID     *string         `json:"entity_id"`
	CompanyName  *string         `json:"company_name"`
	CompanyScope CompanyScope    `json:"company_scope"`
	Payload      json.RawMessage `json:"payload"`
}

// PageInfo is a keyset envelope, not this repo's usual {Limit, Offset, Total}
// (portfolio.listResponse, invoice.listResponse) — audit_log is append-only and
// grows fast enough that an OFFSET page shifts under concurrent inserts (System
// Design §3). HasMore/NextCursor are net-new to this codebase; there is no
// existing pagination envelope this matches, and none is claimed. NextCursor is
// nil exactly when HasMore is false.
type PageInfo struct {
	Limit      int     `json:"limit"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// Facet is one bucket of a facet array (§7). Value and Name are nil for the company
// facet's workspace-level bucket (entity_id IS NULL, contract §4). Kind is set only on
// the actor facet (person/system/raw, via actor.Resolve) and omitted elsewhere — the
// one field here that does carry omitempty (a scalar, not a slice; AC #5 is about
// Response/Facets slice fields, not this).
type Facet struct {
	Value *string `json:"value"`
	Name  *string `json:"name"`
	Kind  string  `json:"kind,omitempty"`
	Count int     `json:"count"`
}

// Facets holds the three facet arrays. No slice field here carries omitempty (D-9):
// the store, not the handler, coerces a nil slice to make(…, 0, n) before this is
// marshaled — omitempty would hide a coercion bug instead of the field going visibly
// null.
type Facets struct {
	Event   []Facet `json:"event"`
	Actor   []Facet `json:"actor"`
	Company []Facet `json:"company"`
}

// Response is the endpoint's whole JSON body (System Design §2). Events and every
// Facets slice are coerced to make(…, 0, n) at the store, never left nil (D-9) — no
// field here carries omitempty either, for the same reason as Facets.
type Response struct {
	Events     []Event  `json:"events"`
	Page       PageInfo `json:"page"`
	Total      int      `json:"total"`
	LogIsEmpty bool     `json:"log_is_empty"`
	Facets     Facets   `json:"facets"`
}

// Filter is the parsed, validated form of the endpoint's query parameters (§2). The
// handler builds one from the raw querystring; Query (subtask 02) turns it into SQL.
type Filter struct {
	Limit  int
	Cursor *Cursor
	From   *time.Time
	To     *time.Time
	Events []string
	Actors []string
	// ActorKind is the §4.3/D-16 class filter: "" (unfiltered), "system"
	// (actor = 'system', an Index Cond) or "people" (actor <> 'system', a Filter —
	// D-16: not very selective, and a SQL Filter runs before LIMIT, so pagination is
	// unaffected). This is NOT actor.Kind's 3-way System/Person/Raw split on the
	// RESOLVED name (that is actor.Resolve's business, elsewhere) — it is a 2-way
	// predicate on the stored actor column alone. No UUID grammar involved.
	ActorKind string
	Company   CompanyFilter
	Q         string
	InvoiceID string
}
