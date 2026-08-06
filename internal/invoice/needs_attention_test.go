// M4-09-02 (task-183): RED acceptance tests for the needs_attention list
// filter, written BEFORE Store.List/ListHandler apply the predicate (RED
// against ListFilter.NeedsAttention -- a bare bool field added for compile
// scaffolding only, [Stage 2.5 compile-scaffolding allowance]; Store.List
// does not yet inject the WHERE, so every assertion below fails on its
// VALUE -- the filtered total/membership -- not on a compile error).
//
// Spec-to-test map (Test Specs table, M4-09-02 story / task-183):
//
//	Core AC #2/#3 TestStoreList_NeedsAttentionMatchesDashboardRollup (drift guard)
//	AC #4         TestRLS_ListNeedsAttention_TenantIsolated
//
// The verbatim predicate under test (copied from internal/dashboard/store.go
// Rollup's own count(*) FILTER clause, alias dropped):
//
//	status IN ('rejected', 'failed')
//	  OR (status = 'draft' AND violations @> '[{"severity": "error"}]'::jsonb)
//
// Run: `make test-rls`, or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/... -run 'NeedsAttention|ListNeedsAttention' -v
package invoice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// matchesNeedsAttentionPredicate reports whether inv satisfies the verbatim
// dashboard predicate, evaluated in Go against the ALREADY-SCANNED row (not a
// second SQL query) -- rejected always matches, failed matches unless
// resolved (kept_as_is_at set); a draft matches iff its
// violations contain a severity:"error" entry (hasBlockingViolation,
// store.go -- the SAME predicate ApplyValidation's promotion gate uses,
// [error semantics]); every other status (validated/queued/submitted/
// accepted) never matches.
func matchesNeedsAttentionPredicate(t *testing.T, inv Invoice) bool {
	t.Helper()
	switch inv.Status {
	case StatusRejected:
		return true
	case StatusFailed:
		return inv.KeptAsIsAt == nil
	case StatusDraft:
		var vs []Violation
		if err := json.Unmarshal(inv.Violations, &vs); err != nil {
			t.Fatalf("unmarshal violations for invoice %s: %v", inv.ID, err)
		}
		return hasBlockingViolation(vs)
	default:
		return false
	}
}

// TestStoreList_NeedsAttentionMatchesDashboardRollup (Core AC #2/#3, the
// drift-guard teeth): seeds ONE tenant + entity with a deliberate mix
// exercising every branch of the verbatim predicate --
//
//	TRUE : rejected, failed, draft-with-severity:"error"
//	FALSE: clean draft (violations '[]'), validated, accepted, a resolved
//	       failed invoice (kept_as_is_at set -- T3-5), and the
//	       DRIFT-CRITICAL case -- a draft whose ONLY violation is
//	       severity:"warning" (must NOT count, exactly as the dashboard
//	       excludes it, DASH-06's own invariant).
//
// Then asserts, tenant-wide:
//
//	(a) List(NeedsAttention:true).total == dashboard.Rollup().Totals.NeedsAttention
//	(b) every item List returns satisfies the predicate (no false positive)
//	(c) every excluded row's id is ABSENT from the returned page (no false
//	    negative -- (a) alone could pass on a compensating-errors coincidence
//	    if the counts happened to match without the membership actually
//	    agreeing, so this is checked independently)
//
// RED today: Store.List does not apply the predicate to either the COUNT or
// the page query, so List(NeedsAttention:true) returns the FULL unfiltered
// tenant list -- total is 7 (all seeded rows), not 3, and every excluded id
// (clean draft/validated/accepted/warning-only draft) is present on the page,
// failing (a) and (c). The dashboard side (Rollup) is unaffected -- it is the
// oracle this test compares List against, not the thing under test.
func TestStoreList_NeedsAttentionMatchesDashboardRollup(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-02 drift-guard tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-02 drift-guard entity")

	// needs-attention TRUE.
	rejectedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-rejected", string(StatusRejected), `[]`)
	failedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-failed", string(StatusFailed), `[]`)
	errorDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-error-draft", string(StatusDraft),
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"missing supplier TIN"}]`)

	// needs-attention FALSE -- must be EXCLUDED.
	cleanDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-clean-draft", string(StatusDraft), `[]`)
	validatedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-validated", string(StatusValidated), `[]`)
	acceptedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-accepted", string(StatusAccepted), `[]`)
	// DRIFT-CRITICAL: a warning-only draft carries violations but no
	// severity:"error" entry -- it must NOT count, the same way DASH-06
	// (internal/dashboard/store_test.go) pins the dashboard side of this.
	warningDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-warning-draft", string(StatusDraft),
		`[{"rule_key":"some-rule","severity":"warning","message":"advisory only"}]`)
	// T3-5: a resolved failed invoice must also be excluded now that the
	// predicate stops counting a failed row once it carries the mark.
	resolvedFailedID := seedResolvedFailed(t, super, tenantID, entityID, "M4-09-02-resolved-failed", uuid.NewString(), "resolved outside")

	excludedIDs := map[string]string{
		cleanDraftID:     "clean draft",
		validatedID:      "validated",
		acceptedID:       "accepted",
		warningDraftID:   "warning-only draft",
		resolvedFailedID: "resolved failed",
	}

	invStore := NewStore(app)
	dashStore := dashboard.NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := invStore.List(c, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsAttention: true): %v", err)
	}

	roll, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("dashboard Rollup: %v", err)
	}

	// (a) the drift-guard invariant itself.
	if total != roll.Totals.NeedsAttention {
		t.Errorf("List(NeedsAttention: true).total = %d, dashboard Rollup().Totals.NeedsAttention = %d, want equal (drift guard, Core AC #2)",
			total, roll.Totals.NeedsAttention)
	}
	if total != 3 {
		t.Errorf("List(NeedsAttention: true).total = %d, want 3 (rejected + failed + error-draft; the 5 excluded rows must not count)", total)
	}

	// (b) no false positive -- every returned row satisfies the predicate.
	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
		if !matchesNeedsAttentionPredicate(t, inv) {
			t.Errorf("List(NeedsAttention: true) returned invoice %s (status=%s, violations=%s), which does NOT satisfy the predicate",
				inv.ID, inv.Status, inv.Violations)
		}
	}

	// (c) no false negative -- every excluded row's id is genuinely absent
	// from the page, checked independently of the total matching (a).
	for id, label := range excludedIDs {
		if seen[id] {
			t.Errorf("List(NeedsAttention: true) incorrectly returned the %s invoice %s", label, id)
		}
	}
	for id, label := range map[string]string{rejectedID: "rejected", failedID: "failed", errorDraftID: "error-draft"} {
		if !seen[id] {
			t.Errorf("List(NeedsAttention: true) is missing the %s invoice %s", label, id)
		}
	}
}

// TestRLS_ListNeedsAttention_TenantIsolated (AC #4): the needs_attention
// filter composes with RLS, not a manual `WHERE tenant_id` -- tenant A's
// filtered List never returns tenant B's needs-attention rows, and A's
// filtered total counts only A's rows. Modeled on
// TestStoreCrossTenant_UpdateGetListRefused (store_test.go).
//
// RED today: Store.List ignores NeedsAttention entirely, so List (as A) with
// NeedsAttention:true returns A's FULL unfiltered list -- which in this
// fixture already excludes B's row by RLS alone, so the no-B-id assertion
// would pass vacuously. The total assertion is the one that actually pins
// the RED: A's unfiltered total is 2 (both of A's seeded rows), not 1 (only
// A's rejected row), so this fails on VALUE once NeedsAttention starts being
// honored.
func TestRLS_ListNeedsAttention_TenantIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "M4-09-02 RLS tenant A")
	tenantB := seedTenant(t, super, "M4-09-02 RLS tenant B")
	entityA := seedEntity(t, super, tenantA, "M4-09-02 RLS A entity")
	entityB := seedEntity(t, super, tenantB, "M4-09-02 RLS B entity")

	// Tenant A: one needs-attention row (rejected), one clean draft (not
	// needs-attention, but still A's -- proves the filter isn't merely "A has
	// exactly one row").
	rejectedA := seedInvoiceWithViolations(t, super, tenantA, entityA, "M4-09-02-RLS-A-rejected", string(StatusRejected), `[]`)
	seedInvoiceWithViolations(t, super, tenantA, entityA, "M4-09-02-RLS-A-clean", string(StatusDraft), `[]`)

	// Tenant B: two needs-attention rows -- if the filter were a global
	// predicate with no tenant scoping, these would leak into A's page.
	failedB := seedInvoiceWithViolations(t, super, tenantB, entityB, "M4-09-02-RLS-B-failed", string(StatusFailed), `[]`)
	errorDraftB := seedInvoiceWithViolations(t, super, tenantB, entityB, "M4-09-02-RLS-B-error-draft", string(StatusDraft),
		`[{"rule_key":"x","severity":"error","message":"y"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, total, err := store.List(cA, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List (as tenant A, NeedsAttention: true): %v", err)
	}

	if total != 1 {
		t.Errorf("List (as A, NeedsAttention: true).total = %d, want 1 (only A's rejected row -- B's 2 needs-attention rows must not be counted)", total)
	}
	for _, inv := range items {
		if inv.ID == failedB || inv.ID == errorDraftB {
			t.Errorf("List (as tenant A, NeedsAttention: true) leaked tenant B's invoice %s", inv.ID)
		}
		if inv.ID != rejectedA {
			t.Errorf("List (as tenant A, NeedsAttention: true) returned unexpected invoice %s, want only %s", inv.ID, rejectedA)
		}
	}
}

// T3-4: a resolved failed invoice is excluded from List(NeedsAttention:true)
// while an unresolved failed, a rejected, and a blocked draft all still
// count exactly as they do today.
func TestStoreList_NeedsAttentionExcludesResolvedFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-4 entity")

	resolvedFailedID := seedResolvedFailed(t, super, tenantID, entityID, "T3-4-resolved", uuid.NewString(), "resolved outside")
	unresolvedFailedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "T3-4-unresolved", string(StatusFailed), `[]`)
	rejectedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "T3-4-rejected", string(StatusRejected), `[]`)
	blockedDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "T3-4-blocked-draft", string(StatusDraft),
		`[{"rule_key":"x","severity":"error","message":"y"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsAttention: true): %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (unresolved failed + rejected + blocked draft; the resolved failed row must not count)", total)
	}
	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
	}
	if seen[resolvedFailedID] {
		t.Errorf("List(NeedsAttention: true) returned the resolved failed invoice %s, want it excluded", resolvedFailedID)
	}
	for id, label := range map[string]string{unresolvedFailedID: "unresolved failed", rejectedID: "rejected", blockedDraftID: "blocked draft"} {
		if !seen[id] {
			t.Errorf("List(NeedsAttention: true) is missing the %s invoice %s", label, id)
		}
	}
}

// T3-6: needs_fix is unaffected by the resolved-failed predicate change -- a
// resolved failed row (wrong status) and a kept blocked draft (its own
// pre-existing kept_as_is_at IS NULL clause) are both already excluded by
// needs_fix's own rules, untouched by this story.
func TestStoreList_NeedsFixUnaffectedByResolvedFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-6 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-6 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)

	seedResolvedFailed(t, super, tenantID, entityID, "T3-6-resolved-failed", uuid.NewString(), "resolved outside")

	keptDraftID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "T3-6-kept-draft")
	if _, err := store.KeepAsIs(c, keptDraftID, "kept for T3-6"); err != nil {
		t.Fatalf("setup KeepAsIs: %v", err)
	}

	unkeptDraftID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "T3-6-unkept-draft")

	items, total, err := store.List(c, ListFilter{NeedsFix: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsFix: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1 (only the un-kept blocked draft)", total)
	}
	if len(items) != 1 || items[0].ID != unkeptDraftID {
		t.Fatalf("items = %+v, want exactly [%s]", items, unkeptDraftID)
	}
}

// T3-7: ResolveOutside then UnresolveOutside flips the dashboard's
// needs_attention count 1 -> 0 -> 1 for the same failed invoice.
func TestNeedsAttention_ResolvedThenUnresolvedFlipsTheCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-7 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-7 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T3-7", StatusFailed)

	approver := uuid.NewString()
	seedMembership(t, super, tenantID, approver, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: approver, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	dashStore := dashboard.NewStore(app)

	before, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup (before): %v", err)
	}
	if before.Totals.NeedsAttention != 1 {
		t.Fatalf("Rollup (before).Totals.NeedsAttention = %d, want 1", before.Totals.NeedsAttention)
	}

	if _, err := store.ResolveOutside(c, invID, "filed manually"); err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}
	resolved, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup (after ResolveOutside): %v", err)
	}
	if resolved.Totals.NeedsAttention != 0 {
		t.Fatalf("Rollup (after ResolveOutside).Totals.NeedsAttention = %d, want 0", resolved.Totals.NeedsAttention)
	}

	if _, err := store.UnresolveOutside(c, invID); err != nil {
		t.Fatalf("UnresolveOutside: %v", err)
	}
	unresolved, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("Rollup (after UnresolveOutside): %v", err)
	}
	if unresolved.Totals.NeedsAttention != 1 {
		t.Fatalf("Rollup (after UnresolveOutside).Totals.NeedsAttention = %d, want 1", unresolved.Totals.NeedsAttention)
	}
}
