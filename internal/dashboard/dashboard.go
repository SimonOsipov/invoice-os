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

// Metric is one readiness/health indicator, numerator over denominator
// (e.g. readiness: invoices ready / total invoices).
type Metric struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// Metric keys emitted per client and in Totals.Metrics.
const (
	MetricReadiness            = "readiness"
	MetricBarFieldCompleteness = "bar_field_completeness"
	MetricBarTaxAccuracy       = "bar_tax_accuracy"
	MetricBarIdentifiersFormat = "bar_identifiers_format"
	MetricBlockedByRules       = "blocked_by_rules"
	MetricFailedInTransmission = "failed_in_transmission"
	MetricNeverValidated       = "never_validated"
	MetricVATTracked           = "vat_tracked"
)

// Bucket is one rollup scope: the state counts plus two overlays. Neither
// overlay is an eighth state. NeedsAttention cuts across draft/rejected/failed
// (rejected ∪ failed ∪ (draft AND an error-severity violation), AC-3);
// AwaitingApproval is a subset of Counts.Validated (validated invoices an
// active approval policy blocks), so the two never overlap and folding either
// into Counts would double-count the same invoice in the donut.
// TopViolations has no `omitempty`: an empty scope still marshals
// "top_violations":[], never null.
type Bucket struct {
	Counts           Counts            `json:"counts"`
	NeedsAttention   int               `json:"needs_attention"`
	AwaitingApproval int               `json:"awaiting_approval"`
	Metrics          map[string]Metric `json:"metrics"`
	TopViolations    []RuleCount       `json:"top_violations"`
}

// Client is one per-entity row. Bucket is embedded ANONYMOUSLY so encoding/json
// promotes EVERY Bucket key — counts, both overlays, metrics, top_violations —
// to the row's top level alongside entity_id/entity_name, never nested under a
// "bucket" key (TestRollupJSON_AwaitingApprovalPromotesOntoEveryClientRow).
type Client struct {
	EntityID   string `json:"entity_id"`
	EntityName string `json:"entity_name"`
	Bucket
}

// RuleCount is one violation rule's frequency -- tenant-wide on
// Rollup/Totals.TopViolations, or scoped to one entity on Client.TopViolations.
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
