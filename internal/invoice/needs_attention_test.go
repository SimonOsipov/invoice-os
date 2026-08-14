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
// The predicate under test, a hand-maintained twin of internal/dashboard/store.go
// Rollup's own count(*) FILTER clause (store.go's f.NeedsAttention doc comment
// names the two licensed differences):
//
//	status = 'rejected'
//	  OR (status = 'failed' AND kept_as_is_at IS NULL)
//	  OR (status = 'draft' AND violations @> '[{"severity": "error"}]'::jsonb)
//	  OR (status = 'draft' AND the newest approval_runs row closed 'rejected')
//
// Run: `make test-rls`, or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/... -run 'NeedsAttention|ListNeedsAttention' -v
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// setRunOpenedAt force-writes a run's opened_at. It defaults to now(), so insert
// order and recency coincide and a fixture meant to separate them silently cannot.
func setRunOpenedAt(t *testing.T, super *pgxpool.Pool, runID string, openedAt time.Time) {
	t.Helper()
	mustExec(t, super, `UPDATE approval_runs SET opened_at = $1 WHERE id = $2`, openedAt, runID)
}

// matchesNeedsAttentionPredicate reports whether inv satisfies the dashboard
// predicate, evaluated in Go against the ALREADY-SCANNED row (not a
// second SQL query) -- rejected always matches, failed matches unless
// resolved (kept_as_is_at set); a draft matches iff its
// violations contain a severity:"error" entry (hasBlockingViolation,
// store.go -- the SAME predicate ApplyValidation's promotion gate uses,
// [error semantics]) OR its most recent approval run closed 'rejected'; every
// other status (validated/queued/submitted/accepted) never matches.
//
// Invoice carries no run state, so latestRunState supplies each id's newest
// approval_runs.state; an id absent from it has no run.
func matchesNeedsAttentionPredicate(t *testing.T, inv Invoice, latestRunState map[string]string) bool {
	t.Helper()
	switch inv.Status {
	case StatusRejected:
		return true
	case StatusFailed:
		return inv.KeptAsIsAt == nil
	case StatusDraft:
		if latestRunState[inv.ID] == "rejected" {
			return true
		}
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
// exercising every branch of the predicate --
//
//	TRUE : rejected, failed, draft-with-severity:"error", draft whose newest
//	       approval run closed 'rejected'
//	FALSE: clean draft (violations '[]'), validated, accepted, a resolved
//	       failed invoice (kept_as_is_at set -- T3-5), a draft whose rejection
//	       was superseded by a newer run, and the
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
// The fixture seeds approval_runs precisely so the guard can see an approval-arm
// divergence: with none, a dashboard-only widening leaves both sides equal and
// this test stays green while the two predicates have drifted.
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

	// The approval arm. Without approval_runs in this fixture the guard cannot see a
	// dashboard-only widening at all -- both rows below carry violations '[]', so the
	// latest run state is the only thing that can flag them. The version stays
	// inactive: needs_attention reads runs, never policy activity.
	policyID := seedApprovalPolicyFor(t, super, tenantID, "M4-09-02 drift-guard policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	t0 := time.Now().Add(-2 * time.Hour)
	t1 := t0.Add(time.Hour)

	approvalRejectedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-approval-rejected", string(StatusDraft), `[]`)
	sentBackRun := seedApprovalRunFor(t, super, tenantID, approvalRejectedID, versionID)
	closeApprovalRunFor(t, super, sentBackRun, "rejected", "approver")
	setRunOpenedAt(t, super, sentBackRun, t1)

	// Superseded: the rejection is no longer the newest run, so it must NOT count.
	supersededID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-superseded", string(StatusDraft), `[]`)
	oldRun := seedApprovalRunFor(t, super, tenantID, supersededID, versionID)
	closeApprovalRunFor(t, super, oldRun, "rejected", "approver")
	setRunOpenedAt(t, super, oldRun, t0)
	reopenedRun := seedApprovalRunFor(t, super, tenantID, supersededID, versionID)
	setRunOpenedAt(t, super, reopenedRun, t1)

	latestRunState := map[string]string{
		approvalRejectedID: "rejected",
		supersededID:       "open",
	}

	excludedIDs := map[string]string{
		cleanDraftID:     "clean draft",
		validatedID:      "validated",
		acceptedID:       "accepted",
		warningDraftID:   "warning-only draft",
		resolvedFailedID: "resolved failed",
		supersededID:     "superseded rejection",
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
	if total != 4 {
		t.Errorf("List(NeedsAttention: true).total = %d, want 4 (rejected + failed + error-draft + approval-rejected draft; the 6 excluded rows must not count)", total)
	}

	// (b) no false positive -- every returned row satisfies the predicate.
	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
		if !matchesNeedsAttentionPredicate(t, inv, latestRunState) {
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
	includedIDs := map[string]string{
		rejectedID:         "rejected",
		failedID:           "failed",
		errorDraftID:       "error-draft",
		approvalRejectedID: "approval-rejected draft",
	}
	for id, label := range includedIDs {
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

// T3-8: source-text guard for the naive edit the story specifically warns
// about -- "AND kept_as_is_at IS NULL" suffixed onto a blanket
// status IN ('rejected', 'failed') instead of the tuple split. A rejected row
// can never carry the mark (invoices_kept_as_is_status), so that naive form
// is BEHAVIOURALLY INDISTINGUISHABLE from the tuple split today -- no
// DB-backed test can catch a regression to it. Mirrors
// internal/dashboard's TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare
// for this package's own (unaliased) copy of the fragment.
func TestStoreList_NeedsAttentionSQLRejectedArmIsBare(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	marker := "if f.NeedsAttention {"
	idx := bytes.Index(src, []byte(marker))
	if idx < 0 {
		t.Fatal(`store.go: no "if f.NeedsAttention {" marker -- has List been restructured?`)
	}
	rest := src[idx+len(marker):]
	open := bytes.IndexByte(rest, '`')
	if open < 0 {
		t.Fatal("store.go: NeedsAttention condition is not a raw string literal")
	}
	rest = rest[open+1:]
	closeIdx := bytes.IndexByte(rest, '`')
	if closeIdx < 0 {
		t.Fatal("store.go: unterminated NeedsAttention condition literal")
	}
	block := string(rest[:closeIdx])

	if !strings.Contains(block, "status = 'rejected'") {
		t.Errorf("NeedsAttention condition lost its bare `status = 'rejected'` disjunct:\n%s", block)
	}
	if !strings.Contains(block, "status = 'failed' AND kept_as_is_at IS NULL") {
		t.Errorf("NeedsAttention condition lost its `status = 'failed' AND kept_as_is_at IS NULL` disjunct:\n%s", block)
	}
	if strings.Contains(block, "IN (") {
		t.Errorf("NeedsAttention condition contains an IN(...) -- the naive "+
			"`status IN ('rejected', 'failed') AND kept_as_is_at IS NULL` form is a silent regression "+
			"risk if invoices_kept_as_is_status is ever relaxed to allow rejected+mark; keep the tuple split:\n%s", block)
	}

	// The approval-rejected arm (AC-1): a correlated EXISTS over the MOST RECENT run,
	// with `=`, never `IN (`, and correlated on invoices.id -- approval_runs has its
	// own id, and a bare id binds there and silently never matches. Whitespace is
	// normalized so the anchors survive re-indentation, not so they survive a rewrite.
	norm := strings.Join(strings.Fields(block), " ")
	for _, want := range []string{
		"status = 'draft' AND EXISTS (",
		"SELECT r.state FROM approval_runs r",
		"r.invoice_id = invoices.id",
		"ORDER BY r.opened_at DESC LIMIT 1",
		"state = 'rejected'",
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("NeedsAttention condition is missing %q -- the approval-rejected arm reads the "+
				"latest run through a derived table, not `EXISTS (any rejected run)`:\n%s", want, norm)
		}
	}
}

// The list side of the fourth arm, asserted on membership rather than on a count:
// the sent-back draft is on the page and the superseded one is not.
func TestStoreList_NeedsAttentionApprovalRejectedRowIsReturned(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID := seedTenant(t, super, "APPR-11-02 list-arm tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-11-02 list-arm entity")
	policyID := seedApprovalPolicyFor(t, super, tenantID, "APPR-11-02 list-arm policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	c := tenantCtx(tenantID)

	t0 := time.Now().Add(-2 * time.Hour)
	t1 := t0.Add(time.Hour)

	sentBackID := seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-list-sentback", string(StatusDraft), `[]`)
	sentBackRun := seedApprovalRunFor(t, super, tenantID, sentBackID, versionID)
	closeApprovalRunFor(t, super, sentBackRun, "rejected", "approver")
	setRunOpenedAt(t, super, sentBackRun, t1)

	supersededID := seedInvoiceWithViolations(t, super, tenantID, entityID, "APPR-11-02-list-superseded", string(StatusDraft), `[]`)
	oldRun := seedApprovalRunFor(t, super, tenantID, supersededID, versionID)
	closeApprovalRunFor(t, super, oldRun, "rejected", "approver")
	setRunOpenedAt(t, super, oldRun, t0)
	reopenedRun := seedApprovalRunFor(t, super, tenantID, supersededID, versionID)
	setRunOpenedAt(t, super, reopenedRun, t1)

	items, total, err := store.List(c, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsAttention: true): %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (the sent-back draft only)", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{sentBackID: true}) {
		t.Errorf("ids = %v, want exactly [%s] -- the superseded draft %s must not appear",
			sortedIDs(got), sentBackID, supersededID)
	}
}

// The behavioural lockstep guard for the SECOND predicate pair, mirroring
// TestStoreList_NeedsAttentionMatchesDashboardRollup: the awaiting_approval list
// filter and the rollup arm are two hand-maintained copies of one predicate, and
// nothing else compares them by behaviour.
func TestStoreList_AwaitingApprovalMatchesDashboardRollup(t *testing.T) {
	super, app := dbTestPools(t)
	invStore := NewStore(app)
	dashStore := dashboard.NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "APPR-11-02 awaiting-lockstep")
	c := tenantCtx(tenantID)

	noRunID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-norun", StatusValidated)

	openID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-open", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, openID, versionID)

	approvedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-approved", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, approvedID, versionID), "approved", "approver")

	rejectedRunID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-rejected-run", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, rejectedRunID, versionID), "rejected", "approver")

	cancelledRunID := seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-cancelled-run", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, cancelledRunID, versionID), "cancelled", "system")

	// Non-validated controls: neither is awaiting approval whatever its runs.
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-draft", StatusDraft)
	seedInvoiceAtStatus(t, super, tenantID, entityID, "APPR-11-02-await-failed", StatusFailed)

	items, total := listAwaiting(t, invStore, c, ListFilter{})

	roll, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("dashboard Rollup: %v", err)
	}
	if total != roll.Totals.AwaitingApproval {
		t.Errorf("List(AwaitingApproval: true).total = %d, dashboard Rollup().Totals.AwaitingApproval = %d, want equal",
			total, roll.Totals.AwaitingApproval)
	}
	if total != 4 {
		t.Errorf("List(AwaitingApproval: true).total = %d, want 4 (no-run, open, rejected-run, cancelled-run; the approved one and the two non-validated rows must not count)", total)
	}
	want := map[string]bool{noRunID: true, openID: true, rejectedRunID: true, cancelledRunID: true}
	if got := idSet(items); !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want exactly %v -- the approved-run invoice %s is transmit-clear", sortedIDs(got), sortedIDs(want), approvedID)
	}
}

// T3-9: resolving removes a failed invoice from the needs_attention NUMBER,
// never from the VIEW (story Core AC #3) -- an unfiltered List must still
// return it, status unchanged, mark visible.
func TestStoreList_ResolvedFailedStillInUnfilteredList(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T3-9 tenant")
	entityID := seedEntity(t, super, tenantID, "T3-9 entity")
	resolvedID := seedResolvedFailed(t, super, tenantID, entityID, "T3-9-resolved", uuid.NewString(), "resolved outside")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 100})
	if err != nil {
		t.Fatalf("List (unfiltered): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (unfiltered).total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != resolvedID {
		t.Fatalf("List (unfiltered) items = %+v, want exactly [%s]", items, resolvedID)
	}
	if items[0].Status != StatusFailed {
		t.Errorf("resolved invoice Status = %q, want %q -- resolving must not change status", items[0].Status, StatusFailed)
	}
	if items[0].KeptAsIsAt == nil {
		t.Errorf("resolved invoice KeptAsIsAt = nil, want set -- the mark must still be visible in the unfiltered view")
	}
}

// T3-10: cross-tenant isolation specifically for the new kept_as_is_at
// clause -- tenant A's own resolved failed row must not count for A, and
// tenant B's unresolved failed row must not leak into A's page.
func TestRLS_ListNeedsAttention_ResolvedFailedIsolatedPerTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T3-10 tenant A")
	tenantB := seedTenant(t, super, "T3-10 tenant B")
	entityA := seedEntity(t, super, tenantA, "T3-10 entity A")
	entityB := seedEntity(t, super, tenantB, "T3-10 entity B")

	resolvedA := seedResolvedFailed(t, super, tenantA, entityA, "T3-10-A-resolved", uuid.NewString(), "resolved outside")
	unresolvedA := seedInvoiceWithViolations(t, super, tenantA, entityA, "T3-10-A-unresolved", string(StatusFailed), `[]`)
	unresolvedB := seedInvoiceWithViolations(t, super, tenantB, entityB, "T3-10-B-unresolved", string(StatusFailed), `[]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, total, err := store.List(cA, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List (as A, NeedsAttention: true): %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (only A's unresolved failed row; A's own resolved row and B's row must not count)", total)
	}
	for _, inv := range items {
		if inv.ID == resolvedA {
			t.Errorf("List (as A) returned A's own RESOLVED failed invoice %s, want excluded", resolvedA)
		}
		if inv.ID == unresolvedB {
			t.Errorf("List (as A) leaked tenant B's invoice %s", unresolvedB)
		}
		if inv.ID != unresolvedA {
			t.Errorf("List (as A) returned unexpected invoice %s, want only %s", inv.ID, unresolvedA)
		}
	}
}
