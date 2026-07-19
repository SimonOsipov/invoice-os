// Package dashboard is M4-07's per-tenant rollup: one query across a
// tenant's invoices, grouped by business entity, that answers "which clients
// need attention right now" (System Design, M4-07 story). This file holds
// the domain types only — Store.Rollup (store.go) is what populates them.
package dashboard

// Counts is the per-state invoice count block — always all seven states,
// zeros included (AC-2). No `omitempty` on any field: a client with zero
// rejected invoices must still see "rejected":0, not a missing key.
type Counts struct {
	Draft     int `json:"draft"`
	Validated int `json:"validated"`
	Queued    int `json:"queued"`
	Submitted int `json:"submitted"`
	Accepted  int `json:"accepted"`
	Rejected  int `json:"rejected"`
	Failed    int `json:"failed"`
}

// Bucket is one rollup scope: the state counts plus the needs-attention
// overlay. NeedsAttention is NOT an eighth state — it cuts across
// draft/rejected/failed (rejected ∪ failed ∪ (draft AND an error-severity
// violation), AC-3).
type Bucket struct {
	Counts         Counts `json:"counts"`
	NeedsAttention int    `json:"needs_attention"`
}

// Client is one per-entity row. Bucket is embedded ANONYMOUSLY so
// encoding/json promotes counts + needs_attention to the row's top level
// (AC-5: entity_id/entity_name alongside the promoted counts/needs_attention
// keys, not nested under a "bucket" key).
type Client struct {
	EntityID   string `json:"entity_id"`
	EntityName string `json:"entity_name"`
	Bucket
}

// RuleCount is one violation rule's tenant-wide frequency (M4-07-02 fills
// this in; left empty by Store.Rollup here).
type RuleCount struct {
	RuleKey  string `json:"rule_key"`
	Invoices int    `json:"invoices"`
}

// Rollup is the full per-tenant dashboard payload: tenant-wide Totals, the
// per-entity breakdown (AC-1: Clients is never nil, and Totals is the
// element-wise sum of Clients), and the top violation rules (populated by
// M4-07-02).
type Rollup struct {
	Totals        Bucket      `json:"totals"`
	Clients       []Client    `json:"clients"`
	TopViolations []RuleCount `json:"top_violations"`
}
