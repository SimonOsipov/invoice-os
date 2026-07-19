// M4-07-02 (task-156): QA adversarial coverage ON TOP OF DASH-20..27
// (top_violations_test.go / cross_tenant_integration_test.go), written
// during the Mode B (post-implementation) verify pass. The 8 shipped specs
// prove the happy paths, severity/array-shape filtering, and cross-tenant
// isolation; this file closes gaps they don't touch: JSON-null vs
// empty-string rule_key, scalar/nested-array junk elements mixed into an
// otherwise-valid array, odd-but-valid element shapes (empty object,
// one-field-only), consistency between the per-entity and per-rule queries
// over the SAME invoice, and ordering correctness at a fanout large enough
// to actually exercise the rule_key ASC tie-break (see
// TestStoreRollup_TopViolationsLargeFanoutOrderingStaysExactAndStable's own
// doc comment for why DASH-24's 2-row case does not). Reuses the
// dbTestPools/seedTenant/seedEntity/seedInvoiceWithViolations/
// assertTopViolations harness from store_test.go/top_violations_test.go
// (same package).
package dashboard

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStoreRollup_TopViolationsNullRuleKeyNeverPhantom: a violation element
// whose rule_key is JSON `null` (distinct from a MISSING rule_key key,
// DASH-26's case) must not produce a phantom TopViolations entry. In
// Postgres, `v->>'rule_key'` on `{"rule_key": null, ...}` evaluates to SQL
// NULL (confirmed empirically against the dev DB), so it fails the query's
// `IS NOT NULL` filter the same way a missing key does.
func TestStoreRollup_TopViolationsNullRuleKeyNeverPhantom(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial null rule_key tenant")
	entityID := seedEntity(t, super, tenantID, "null rule_key entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "NULLRULE-1", "draft",
		`[{"rule_key":null,"severity":"error","message":"x"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{})
}

// TestStoreRollup_TopViolationsEmptyStringRuleKeyNeverPhantom: a violation
// element whose rule_key is the EMPTY STRING "" (distinct from JSON null
// above) must not produce a phantom TopViolations entry either -- an empty
// string is not a real rule identifier.
//
// Regression guard: store.go's query nullifies an empty-string rule_key
// before the IS NOT NULL filter, so it is excluded the same as a missing
// key. This test pins that behavior.
func TestStoreRollup_TopViolationsEmptyStringRuleKeyNeverPhantom(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial empty rule_key tenant")
	entityID := seedEntity(t, super, tenantID, "empty rule_key entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "EMPTYRULE-1", "draft",
		`[{"rule_key":"","severity":"error","message":"x"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{})
}

// TestStoreRollup_TopViolationsScalarAndNestedArrayElementsAreSkipped: a
// violations array that mixes non-object junk elements (a bare scalar, a
// nested array) alongside a well-formed severity:"error" element must not
// error and must not let the junk elements contribute anything -- only the
// valid element counts. Distinct from DASH-26 (which mixes a malformed
// OBJECT, missing rule_key, into the array): here the junk elements are not
// even objects, so `v->>'rule_key'` is applied to a jsonb scalar/array,
// which Postgres evaluates to SQL NULL rather than erroring (confirmed
// empirically: `1->>'rule_key'` and `[1,2]->>'rule_key'` both yield NULL,
// no SQLSTATE raised).
func TestStoreRollup_TopViolationsScalarAndNestedArrayElementsAreSkipped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial scalar/array junk tenant")
	entityID := seedEntity(t, super, tenantID, "scalar/array junk entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "JUNK-scalar", "draft",
		`[1, {"rule_key":"x","severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "JUNK-nested-array", "draft",
		`[[1,2], {"rule_key":"y","severity":"error","message":"m"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup with scalar/nested-array junk elements present: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "x", Invoices: 1},
		{RuleKey: "y", Invoices: 1},
	})
}

// TestStoreRollup_TopViolationsOddButValidElementShapesProduceNoEntries:
// three separately-seeded invoices exercise three odd-but-VALID (not
// malformed-JSON) element shapes the 8 shipped specs don't isolate on their
// own: an empty object element `[{}]` (has neither key), an element with
// severity but no rule_key standalone (DASH-26 only tests this MIXED with a
// valid element, never alone), and the mirror case -- an element with
// rule_key but no severity. None of the three may error or produce a
// TopViolations entry.
func TestStoreRollup_TopViolationsOddButValidElementShapesProduceNoEntries(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial odd valid shapes tenant")
	entityID := seedEntity(t, super, tenantID, "odd valid shapes entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "ODD-empty-object", "draft", `[{}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "ODD-severity-no-rulekey", "draft",
		`[{"severity":"error"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "ODD-rulekey-no-severity", "draft",
		`[{"rule_key":"orphan-rule"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup with odd-but-valid element shapes present: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{})
}

// TestStoreRollup_TopViolationsConsistentWithNeedsAttentionForSameInvoice:
// the SAME broken draft that flips an entity's needs_attention to 1 (via
// the per-entity query) must be exactly the invoice contributing count 1 to
// its rule in TopViolations (via the per-rule query) -- proving the two
// queries agree on the same underlying data, as expected of two queries
// against the same transaction. A SEPARATE warning-only draft on the same
// entity must appear in NEITHER query's output (not needs_attention, per
// DASH-06/AC-3; not TopViolations, per DASH-21/AC-2). This deliberately does
// NOT claim rejected/failed invoices (which count toward needs_attention
// regardless of violations content, DASH-08) show up in TopViolations --
// that would be a false general invariant; this test targets only the
// draft+error overlap the two queries share.
func TestStoreRollup_TopViolationsConsistentWithNeedsAttentionForSameInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial cross-query consistency tenant")
	entityID := seedEntity(t, super, tenantID, "cross-query consistency entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "CONSIST-broken", "draft",
		`[{"rule_key":"r1","severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "CONSIST-warning-only", "draft",
		`[{"rule_key":"r2","severity":"warning","message":"m"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.Counts.Draft != 2 {
		t.Errorf("Counts.Draft = %d, want 2 (both invoices are drafts)", row.Counts.Draft)
	}
	if row.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1 (only the broken draft, not the warning-only one)", row.NeedsAttention)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "r1", Invoices: 1},
	})
}

// TestStoreRollup_TopViolationsLargeFanoutOrderingStaysExactAndStable: 20
// distinct rule keys across 4 tied-count buckets (5 keys each, at counts
// 4/3/2/1), seeded across 50 invoices.
//
// Why this test exists (not just DASH-24): DASH-24 pins the rule_key ASC
// tie-break with only TWO tied rule keys ("a-rule"/"b-rule"). Empirically
// probing Postgres directly (dev DB, m4-07-postgres-1) shows that a
// GROUP BY with no explicit tie-break, aggregated via a Hash Aggregate,
// happens to return a 2-row tied group in an order that COINCIDENTALLY
// matches rule_key ASC for that specific pair -- verified by mutation
// testing store.go directly (dropping `, 1 ASC` from the query's
// `ORDER BY 2 DESC, 1 ASC` while keeping `2 DESC`; also independently
// flipping to `ORDER BY 2 ASC` alone): DASH-24 stayed GREEN in both cases.
// The same probe against 10 tied string keys (unordered INSERT) came back
// in genuinely hash-shuffled, non-alphabetical order -- so a large enough
// tied group makes coincidental alphabetical ordering astronomically
// unlikely, and this test's assertion is fully deterministic (not flaky)
// whenever the implementation's `ORDER BY 2 DESC, 1 ASC` is intact, because
// rule_key is unique per GROUP BY group -- a complete, unambiguous sort key,
// not merely a probabilistic tie-break.
func TestStoreRollup_TopViolationsLargeFanoutOrderingStaysExactAndStable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial large rule fanout tenant")
	entityID := seedEntity(t, super, tenantID, "large rule fanout entity")

	const buckets = 4   // distinct invoice counts per rule: 4,3,2,1
	const perBucket = 5 // rule keys tied within each count level
	broken := func(rule string) string {
		return fmt.Sprintf(`[{"rule_key":%q,"severity":"error","message":"m"}]`, rule)
	}

	var wantOrder []RuleCount // filled in count-DESC, rule_key-ASC (seed) order
	invoiceSeq := 0
	for b := buckets; b >= 1; b-- { // seed highest-count bucket first
		for j := 0; j < perBucket; j++ {
			rule := fmt.Sprintf("fanout-rule-%d-%02d", b, j) // zero-padded j sorts lexicographically as seeded
			for k := 0; k < b; k++ {
				invoiceSeq++
				seedInvoiceWithViolations(t, super, tenantID, entityID,
					fmt.Sprintf("FANOUT-RULE-%d", invoiceSeq), "draft", broken(rule))
			}
			wantOrder = append(wantOrder, RuleCount{RuleKey: rule, Invoices: b})
		}
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, wantOrder)
}

// TestStoreRollup_TopViolationsSameRuleTwiceOnOneInvoiceCountsOnce: ONE
// invoice whose violations array carries the SAME rule_key TWICE (both
// severity:"error") must still count as 1 for that rule, not 2 -- this is
// what `count(DISTINCT i.id)` in store.go actually protects against.
// DASH-23 (multiple errors on one invoice) does NOT cover this: it uses two
// DIFFERENT rules on one invoice, each getting its own GROUP BY group, so a
// plain `count(*)` would pass DASH-23 identically to `count(DISTINCT i.id)`
// -- confirmed by mutation testing (dropping DISTINCT left every shipped
// spec green). Here, jsonb_array_elements emits TWO rows for the SAME
// (invoice, rule_key) pair, so `count(*)` would wrongly return 2 while
// `count(DISTINCT i.id)` correctly returns 1.
func TestStoreRollup_TopViolationsSameRuleTwiceOnOneInvoiceCountsOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial same-rule-twice tenant")
	entityID := seedEntity(t, super, tenantID, "same-rule-twice entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "SAMERULE-1", "draft",
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"a"},`+
			`{"rule_key":"supplier-tin-required","severity":"error","message":"b"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 1},
	})
}

// TestStoreRollup_TopViolationsSameRuleTwiceOnEachOfTwoInvoicesCountsTwo:
// the same same-rule-twice-per-invoice shape as above, but on TWO separate
// invoices, pins that DISTINCT is keyed on invoice id, not on occurrence
// count: each invoice contributes exactly 1 (not 2) to the rule's total, so
// two such invoices must total 2 (not 4). This rules out an implementation
// that merely deduplicates per-invoice element pairs without actually
// counting distinct invoices (e.g. a bug that divided the raw element count
// by a fixed factor would coincidentally pass the single-invoice test above
// but fail here).
func TestStoreRollup_TopViolationsSameRuleTwiceOnEachOfTwoInvoicesCountsTwo(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial same-rule-twice x2 tenant")
	entityID := seedEntity(t, super, tenantID, "same-rule-twice x2 entity")
	twice := `[{"rule_key":"supplier-tin-required","severity":"error","message":"a"},` +
		`{"rule_key":"supplier-tin-required","severity":"error","message":"b"}]`
	seedInvoiceWithViolations(t, super, tenantID, entityID, "SAMERULE-X2-1", "draft", twice)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "SAMERULE-X2-2", "draft", twice)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 2},
	})
}
