// task-283 (INVCR-01-07 subtask 7): RED, DB-backed tests for
// internal/invoice's Store.ViolationSummary -- the violation-summary rail's
// aggregate query, written BEFORE the real implementation exists (RED
// against store.go's not-implemented stub: ViolationSummary always returns
// nil, nil -- see that file's doc comment). Reuses
// dbTestPools/seedTenant/seedEntity/seedInvoiceWithViolations
// (store_test.go), seedImportBatch (import_batch_test.go),
// seedInvoiceWithBatchAndStatus (batch_filter_test.go) -- all same-package,
// none redefined here.
//
// Spec-to-test map (Stage 1 Test Specs table, task-283):
//
//	spec 7  TestViolationSummary_CountsDistinctInvoicesPerRule
//	spec 8  TestViolationSummary_OneInvoiceTwiceOnOneRuleCountsOnce
//	spec 9  TestViolationSummary_MatchesRuleKeyFilterTotalIncludingWarnings
//	spec 10 TestViolationSummary_NonArrayViolationsDoNotError
//	spec 11 TestRLS_ViolationSummaryTenantScopedAndNonVacuous
//
// Specs 12-14 (handler-level + routing) live in handlers_test.go.
//
// Run (`make test-rls` does NOT cover this package -- it targets
// ./internal/platform/db/... at port 5432):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 -p 1 ./internal/invoice/...
package invoice

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestViolationSummary_CountsDistinctInvoicesPerRule (spec 7): invoice A
// violates r1+r2, invoice B violates r1, invoice C is clean -- the summary
// must report exactly [{r1,2},{r2,1}], ordered count DESC then key ASC. RED
// against the stub: ViolationSummary returns nil, so len(got) != 2 already
// fails.
func TestViolationSummary_CountsDistinctInvoicesPerRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VIOSUM-COUNT tenant")
	entityID := seedEntity(t, super, tenantID, "VIOSUM-COUNT entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)

	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-COUNT-A", batchID, string(StatusDraft),
		`[{"rule_key":"r1","severity":"error","message":"a"},{"rule_key":"r2","severity":"error","message":"b"}]`)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-COUNT-B", batchID, string(StatusDraft),
		`[{"rule_key":"r1","severity":"error","message":"c"}]`)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-COUNT-C", batchID, string(StatusValidated), `[]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.ViolationSummary(c, batchID)
	if err != nil {
		t.Fatalf("ViolationSummary: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(ViolationSummary) = %d, want 2: %+v", len(got), got)
	}
	if got[0] != (RuleCount{RuleKey: "r1", Invoices: 2}) {
		t.Errorf("got[0] = %+v, want {r1 2} (ordered count DESC)", got[0])
	}
	if got[1] != (RuleCount{RuleKey: "r2", Invoices: 1}) {
		t.Errorf("got[1] = %+v, want {r2 1}", got[1])
	}
}

// TestViolationSummary_OneInvoiceTwiceOnOneRuleCountsOnce (spec 8): one
// invoice's violations array names rule r1 TWICE -- the summary must still
// count that invoice once for r1 (count(DISTINCT invoice.id), not
// count(*)).
func TestViolationSummary_OneInvoiceTwiceOnOneRuleCountsOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VIOSUM-DEDUP tenant")
	entityID := seedEntity(t, super, tenantID, "VIOSUM-DEDUP entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)

	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-DEDUP-A", batchID, string(StatusDraft),
		`[{"rule_key":"r1","severity":"error","message":"a"},{"rule_key":"r1","severity":"error","message":"b"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.ViolationSummary(c, batchID)
	if err != nil {
		t.Fatalf("ViolationSummary: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ViolationSummary) = %d, want 1 (count(DISTINCT invoice.id), not count(*)): %+v", len(got), got)
	}
	if got[0] != (RuleCount{RuleKey: "r1", Invoices: 1}) {
		t.Errorf("got[0] = %+v, want {r1 1} -- a count(*) implementation would report 2", got[0])
	}
}

// TestViolationSummary_MatchesRuleKeyFilterTotalIncludingWarnings (spec 9,
// task-283 R3 -- the highest-value spec in this set): two WARNING-only
// invoices on rule "W" must count in ViolationSummary, and that count must
// equal Store.List{ImportBatchID,RuleKey:"W"}.total -- the exact query the
// rail's click-through issues (subtask 06's shipped RuleKey filter,
// store.go). internal/dashboard/store.go's Rollup.TopViolations restricts
// to v->>'severity'='error' and would return 0 rows here (all 19 shipped
// rules are today "error", so this divergence is invisible until the first
// warning-only rule ships, [D23]) -- this spec is what catches that clause
// if it is ever copied into this aggregate. Verified live (2026-07-30): with
// the dashboard's severity='error' clause temporarily reintroduced, this
// spec fails (w == nil, 0 rows); with it removed, it passes.
func TestViolationSummary_MatchesRuleKeyFilterTotalIncludingWarnings(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VIOSUM-WARN tenant")
	entityID := seedEntity(t, super, tenantID, "VIOSUM-WARN entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)

	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-WARN-A", batchID, string(StatusDraft),
		`[{"rule_key":"W","severity":"warning","message":"advisory only"}]`)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-WARN-B", batchID, string(StatusDraft),
		`[{"rule_key":"W","severity":"warning","message":"advisory only"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, listTotal, err := store.List(c, ListFilter{ImportBatchID: batchID, RuleKey: "W", Limit: 50})
	if err != nil {
		t.Fatalf("List (RuleKey: W): %v", err)
	}
	if listTotal != 2 {
		t.Fatalf("List (RuleKey: W).total = %d, want 2 -- fixture assumption broken", listTotal)
	}

	got, err := store.ViolationSummary(c, batchID)
	if err != nil {
		t.Fatalf("ViolationSummary: %v", err)
	}
	var w *RuleCount
	for i := range got {
		if got[i].RuleKey == "W" {
			w = &got[i]
		}
	}
	if w == nil {
		t.Fatalf("ViolationSummary has no entry for rule_key \"W\": %+v (a severity='error'-filtered aggregate would drop it -- both seeded invoices are warning-only)", got)
	}
	if w.Invoices != listTotal {
		t.Errorf("ViolationSummary[\"W\"].Invoices = %d, want %d (Store.List{RuleKey:\"W\"}.total -- the rail must match the filter it triggers)", w.Invoices, listTotal)
	}
}

// TestViolationSummary_NonArrayViolationsDoNotError (spec 10): one invoice's
// violations column is force-seeded to a jsonb OBJECT (not an array) --
// without a jsonb_typeof(violations)='array' guard, jsonb_array_elements
// RAISES 22023 on that row and would 500 the whole aggregate
// (internal/dashboard/store.go:84-89's own reasoning, task-283 R3).
func TestViolationSummary_NonArrayViolationsDoNotError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "VIOSUM-NONARRAY tenant")
	entityID := seedEntity(t, super, tenantID, "VIOSUM-NONARRAY entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)

	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-NONARRAY-ok", batchID, string(StatusDraft),
		`[{"rule_key":"ok-rule","severity":"error","message":"x"}]`)
	// Force-seeded: a jsonb OBJECT, not an array.
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "VIOSUM-NONARRAY-malformed", batchID, string(StatusDraft),
		`{"not":"array"}`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.ViolationSummary(c, batchID)
	if err != nil {
		t.Fatalf("ViolationSummary: %v, want nil -- a non-array violations row must not 500 the whole aggregate (missing jsonb_typeof guard)", err)
	}
	if len(got) != 1 || got[0] != (RuleCount{RuleKey: "ok-rule", Invoices: 1}) {
		t.Errorf("ViolationSummary = %+v, want exactly [{ok-rule 1}] (the malformed row skipped, the well-formed row still counted)", got)
	}
}

// TestRLS_ViolationSummaryTenantScopedAndNonVacuous (spec 11): two legs,
// ONE test. Tenant 1 summarising tenant 2's batch must come back EMPTY
// (never an error, never tenant 2's rows) -- and, in the SAME test, tenant
// 1 summarising ITS OWN batch must return real rows. Neither store method
// here carries a manual tenant predicate (RLS alone scopes it), so without
// the same-tenant leg the cross-tenant assertion would pass vacuously even
// if the tenant scoping were deleted entirely
// (needs_attention_test.go:166-174 names this exact trap).
func TestRLS_ViolationSummaryTenantScopedAndNonVacuous(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "VIOSUM-RLS tenant 1")
	tenant2 := seedTenant(t, super, "VIOSUM-RLS tenant 2")
	entity1 := seedEntity(t, super, tenant1, "VIOSUM-RLS entity 1")
	entity2 := seedEntity(t, super, tenant2, "VIOSUM-RLS entity 2")

	batchA := seedImportBatch(t, super, tenant1, entity1)
	batchB := seedImportBatch(t, super, tenant2, entity2)

	seedInvoiceWithBatchAndStatus(t, super, tenant1, entity1, "VIOSUM-RLS-A", batchA, string(StatusDraft),
		`[{"rule_key":"r-own","severity":"error","message":"a"}]`)
	seedInvoiceWithBatchAndStatus(t, super, tenant2, entity2, "VIOSUM-RLS-B", batchB, string(StatusDraft),
		`[{"rule_key":"r-other","severity":"error","message":"b"}]`)

	store := NewStore(app)
	c1 := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant1})

	// Cross-tenant: tenant 1 summarising tenant 2's batch must come back
	// EMPTY -- never an error, never tenant 2's rows.
	crossGot, err := store.ViolationSummary(c1, batchB)
	if err != nil {
		t.Fatalf("ViolationSummary (tenant 1, tenant 2's batch): %v, want nil", err)
	}
	if len(crossGot) != 0 {
		t.Errorf("ViolationSummary (tenant 1, tenant 2's batch) = %+v, want empty", crossGot)
	}

	// Same-tenant leg, in the SAME test -- the discriminating half.
	sameGot, err := store.ViolationSummary(c1, batchA)
	if err != nil {
		t.Fatalf("ViolationSummary (tenant 1, own batch): %v", err)
	}
	if len(sameGot) != 1 || sameGot[0] != (RuleCount{RuleKey: "r-own", Invoices: 1}) {
		t.Errorf("ViolationSummary (tenant 1, own batch) = %+v, want exactly [{r-own 1}]", sameGot)
	}
}
