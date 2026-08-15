// Wire-shape tests for the domain types in dashboard.go. No database: these
// assert what encoding/json emits, which is the contract the two hand-maintained
// TypeScript mirrors (frontend/app/src/lib/dashboard.ts, e2e/api/client.ts) are
// written against.
package dashboard

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// awaiting_approval is a Bucket sibling of needs_attention, never an eighth
// Counts key: it is an overlay on counts.validated, so folding it into Counts
// would double-count the same invoice in the donut.
func TestRollupJSON_AwaitingApprovalIsABucketSiblingNotACountsKey(t *testing.T) {
	b := Bucket{
		Counts:           Counts{Draft: 1, Validated: 2, Queued: 3, Submitted: 4, Accepted: 5, Rejected: 6, Failed: 7},
		NeedsAttention:   8,
		AwaitingApproval: 9,
		Metrics:          map[string]Metric{MetricReadiness: {Num: 1, Den: 2}},
		TopViolations:    []RuleCount{{RuleKey: "supplier-tin-required", Invoices: 1}},
	}

	body, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("unmarshal bucket: %v", err)
	}
	raw, ok := top["awaiting_approval"]
	if !ok {
		t.Fatalf("marshalled bucket = %s, want a top-level \"awaiting_approval\" key", body)
	}
	var awaiting int
	if err := json.Unmarshal(raw, &awaiting); err != nil {
		t.Fatalf("unmarshal awaiting_approval: %v", err)
	}
	if awaiting != 9 {
		t.Errorf("awaiting_approval = %d, want 9", awaiting)
	}

	countsRaw, ok := top["counts"]
	if !ok {
		t.Fatalf("marshalled bucket = %s, want a top-level \"counts\" key", body)
	}
	var counts map[string]json.RawMessage
	if err := json.Unmarshal(countsRaw, &counts); err != nil {
		t.Fatalf("unmarshal counts: %v", err)
	}
	gotKeys := make([]string, 0, len(counts))
	for k := range counts {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{"accepted", "draft", "failed", "queued", "rejected", "submitted", "validated"}
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("counts keys = %v, want exactly the 7 states %v", gotKeys, wantKeys)
	}
	for i, k := range wantKeys {
		if gotKeys[i] != k {
			t.Fatalf("counts keys = %v, want exactly the 7 states %v", gotKeys, wantKeys)
		}
	}

	// Field order: awaiting_approval sits between needs_attention and metrics.
	needsAt := bytes.Index(body, []byte(`"needs_attention"`))
	awaitingAt := bytes.Index(body, []byte(`"awaiting_approval"`))
	metricsAt := bytes.Index(body, []byte(`"metrics"`))
	if needsAt < 0 || awaitingAt < 0 || metricsAt < 0 {
		t.Fatalf("marshalled bucket = %s, want needs_attention, awaiting_approval and metrics all present", body)
	}
	if !(needsAt < awaitingAt && awaitingAt < metricsAt) {
		t.Errorf("key order in %s is needs_attention@%d, awaiting_approval@%d, metrics@%d -- want awaiting_approval immediately after needs_attention",
			body, needsAt, awaitingAt, metricsAt)
	}
}

// Bucket is embedded anonymously in Client, so every Bucket key promotes onto a
// client row's top level rather than nesting under a "bucket" key.
func TestRollupJSON_AwaitingApprovalPromotesOntoEveryClientRow(t *testing.T) {
	c := Client{
		EntityID:   "e1",
		EntityName: "Okafor & Partners",
		Bucket: Bucket{
			Counts:           Counts{Validated: 4},
			NeedsAttention:   0,
			AwaitingApproval: 3,
			Metrics:          map[string]Metric{},
			TopViolations:    []RuleCount{},
		},
	}

	body, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("unmarshal client: %v", err)
	}
	if _, nested := row["bucket"]; nested {
		t.Errorf("marshalled client = %s, want the Bucket keys promoted, not nested under \"bucket\"", body)
	}
	raw, ok := row["awaiting_approval"]
	if !ok {
		t.Fatalf("marshalled client = %s, want a promoted \"awaiting_approval\" key", body)
	}
	var awaiting int
	if err := json.Unmarshal(raw, &awaiting); err != nil {
		t.Fatalf("unmarshal awaiting_approval: %v", err)
	}
	if awaiting != 3 {
		t.Errorf("client awaiting_approval = %d, want 3", awaiting)
	}
}

// The all-zero wire shape, which is what an empty tenant actually serves. No
// field carries omitempty, so both overlays and all seven counts must still be
// present as explicit zeros -- an absent key reads as undefined in the two
// TypeScript mirrors, not as 0.
func TestRollupJSON_ZeroValueBucketStillEmitsBothOverlaysAndSevenCounts(t *testing.T) {
	if n := reflect.TypeOf(Counts{}).NumField(); n != 7 {
		t.Fatalf("Counts has %d fields, want exactly 7 -- awaiting_approval is a Bucket sibling, never an eighth state", n)
	}

	body, err := json.Marshal(Bucket{Metrics: map[string]Metric{}, TopViolations: []RuleCount{}})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("unmarshal bucket: %v", err)
	}
	for _, key := range []string{"counts", "needs_attention", "awaiting_approval", "metrics", "top_violations"} {
		if _, ok := top[key]; !ok {
			t.Errorf("all-zero bucket = %s, want an explicit %q key", body, key)
		}
	}
	if got := string(top["awaiting_approval"]); got != "0" {
		t.Errorf("all-zero bucket's awaiting_approval = %s, want 0", got)
	}

	var counts map[string]json.RawMessage
	if err := json.Unmarshal(top["counts"], &counts); err != nil {
		t.Fatalf("unmarshal counts: %v", err)
	}
	if len(counts) != 7 {
		t.Errorf("all-zero counts = %s, want exactly 7 explicit-zero keys", top["counts"])
	}
}
