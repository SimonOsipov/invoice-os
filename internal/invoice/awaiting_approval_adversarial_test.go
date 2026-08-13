// task-498 (APPR-08-07, Mode B): adversarial coverage for ListFilter.AwaitingApproval,
// on top of the Mode A specs in awaiting_approval_test.go.
//
// Three of these close holes mutation testing proved open against the Mode A set:
//
//	AC #7  wrapping the fragment in `if !s.approvalsEnforced` left the whole suite green --
//	       every test store defaults to flag-off, so nothing read the flag.
//	AC #1  deleting the migration Down left the suite green AND `make migrate-down` green
//	       (goose reports EMPTY and exits 0). CI reset->up cannot see it either: the
//	       approval_runs Down drops the table, which takes the index with it.
//	leak   the policy EXISTS is uncorrelated and carries no tenant predicate, so RLS on
//	       approval_policy_versions is the only thing scoping it. Probed directly below.
package invoice

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- AC #7: the flag gates enforcement, not visibility ----------------------

// TestStoreList_AwaitingApprovalIsNotGatedByApprovalsEnforced: the same fixture read
// through a flag-off and a flag-on store must return the identical rows.
//
// Every other DB-backed case builds NewStore(app), which leaves approvalsEnforced false,
// so a `if !s.approvalsEnforced` wrapper around the fragment is invisible to all of them --
// and it would make the list surface vanish the moment APPR-14 turns the flag on. The
// ApprovalFacts leg below is the non-vacuity guard: it proves the option really is live on
// the flag-on store, so "identical" is a fact about the filter and not about a dead option.
func TestStoreList_AwaitingApprovalIsNotGatedByApprovalsEnforced(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-FLAG")
	c := tenantCtx(tenantID)

	gatedOpen := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-FLAG-open", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gatedOpen, versionID)
	gatedNoRun := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-FLAG-norun", StatusValidated)
	clearedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-FLAG-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, clearedID, versionID), "approved", "approver")

	off := NewStore(app)
	on := NewStore(app, WithApprovalsEnforced(true))

	// The option is live: ApprovalFacts folds the flag, so the two stores MUST disagree
	// here. Without this leg a no-op option would make the equality below vacuous.
	offFacts, err := off.ApprovalFacts(c, gatedOpen)
	if err != nil {
		t.Fatalf("flag-off ApprovalFacts: %v", err)
	}
	onFacts, err := on.ApprovalFacts(c, gatedOpen)
	if err != nil {
		t.Fatalf("flag-on ApprovalFacts: %v", err)
	}
	if !offFacts.TransmitClear || onFacts.TransmitClear {
		t.Fatalf("ApprovalFacts.TransmitClear = %v (flag off) / %v (flag on), want true/false -- "+
			"WithApprovalsEnforced is not reaching the store, so the List comparison below proves nothing",
			offFacts.TransmitClear, onFacts.TransmitClear)
	}

	want := map[string]bool{gatedOpen: true, gatedNoRun: true}

	offItems, offTotal := listAwaiting(t, off, c, ListFilter{})
	onItems, onTotal := listAwaiting(t, on, c, ListFilter{})
	offSet, onSet := idSet(offItems), idSet(onItems)

	if !reflect.DeepEqual(offSet, want) {
		t.Errorf("flag-off ids = %v, want exactly %v", sortedIDs(offSet), sortedIDs(want))
	}
	if !reflect.DeepEqual(onSet, want) {
		t.Errorf("flag-on ids = %v, want exactly %v -- APPROVALS_ENFORCED must not gate the "+
			"awaiting_approval filter (AC #7, docs/approvals.md §11 \"Not gated\")", sortedIDs(onSet), sortedIDs(want))
	}
	if !reflect.DeepEqual(offSet, onSet) || offTotal != onTotal {
		t.Errorf("flag-off returned %v/total %d and flag-on returned %v/total %d, want identical",
			sortedIDs(offSet), offTotal, sortedIDs(onSet), onTotal)
	}
	if offSet[clearedID] {
		t.Errorf("the approved-run invoice %s is in the result -- the filter is not discriminating, "+
			"so the flag comparison above would hold for a broken predicate too", clearedID)
	}
}

// --- the uncorrelated policy EXISTS carries no tenant predicate -------------

// existsActivePolicyVersion runs the fragment subquery verbatim through the APP pool under
// the caller tenant GUC. Verbatim on purpose: restating it with a tenant_id predicate would
// probe a query the store does not run.
func existsActivePolicyVersion(t *testing.T, app *pgxpool.Pool, c context.Context) bool {
	t.Helper()
	var out bool
	if err := db.WithinRequestTenantTx(c, app, func(tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)`).Scan(&out)
	}); err != nil {
		t.Fatalf("probe active-policy EXISTS: %v", err)
	}
	return out
}

// TestRLS_AwaitingApprovalActivePolicyDoesNotLeakAcrossTenants: tenant A publishes a
// policy; tenant B has none at all. B must still read the EXISTS as false.
//
// Stronger than TestStoreList_AwaitingApprovalIsTenantScoped, which gives B its OWN active
// version -- there, a leaking EXISTS would read true for the right reason and the test
// would pass. Here B has nothing to read but A rows, so a true is a leak. If this ever
// fails the filter reports every tenant gated the moment ANY tenant publishes a policy.
func TestRLS_AwaitingApprovalActivePolicyDoesNotLeakAcrossTenants(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantA, entityA, versionA := seedOneStepActivePolicyTenant(t, super, "AWAIT-LEAK-A")
	gatedA := seedInvoiceAtStatus(t, super, tenantA, entityA, "AWAIT-LEAK-A-1", StatusValidated)
	seedApprovalRunFor(t, super, tenantA, gatedA, versionA)

	// Tenant B holds NO approval policy row of any kind -- not an inactive one.
	tenantB := seedTenant(t, super, "AWAIT-LEAK-B tenant")
	entityB := seedEntity(t, super, tenantB, "AWAIT-LEAK-B entity")
	validatedB := seedInvoiceAtStatus(t, super, tenantB, entityB, "AWAIT-LEAK-B-1", StatusValidated)

	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1`, tenantB); n != 0 {
		t.Fatalf("tenant B holds %d policy versions, want 0 -- the fixture does not test a leak", n)
	}

	if !existsActivePolicyVersion(t, app, tenantCtx(tenantA)) {
		t.Error("tenant A reads the active-policy EXISTS as false, want true -- its own version is invisible")
	}
	if existsActivePolicyVersion(t, app, tenantCtx(tenantB)) {
		t.Error("tenant B reads the active-policy EXISTS as TRUE with no policy of its own -- the " +
			"uncorrelated subquery is leaking across tenants and every tenant now reports gated")
	}

	items, total := listAwaiting(t, store, tenantCtx(tenantB), ListFilter{})
	if total != 0 || len(items) != 0 {
		t.Errorf("tenant B ids = %v, total = %d, want none -- %s is validated with no run, so it "+
			"appears only if another tenant active policy reached B", sortedIDs(idSet(items)), total, validatedB)
	}

	if _, totalA := listAwaiting(t, store, tenantCtx(tenantA), ListFilter{}); totalA != 1 {
		t.Errorf("tenant A total = %d, want 1 -- the fixture did not land", totalA)
	}
}

// --- policy-version shapes the fragment must read the same way as the gate --

// TestStoreList_AwaitingApprovalWithASupersededPolicyVersion: is_active alone is the whole
// resolve, so a tenant carrying a sealed non-active version alongside an active one is
// still gated. approval_policy_versions_one_active makes "exactly one active" a schema
// fact; this pins that the fragment reads that one and ignores the other.
func TestStoreList_AwaitingApprovalWithASupersededPolicyVersion(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, activeVersion := seedOneStepActivePolicyTenant(t, super, "AWAIT-SUPERSEDED")
	c := tenantCtx(tenantID)

	// A second policy whose version is sealed but never activated.
	oldPolicy := seedApprovalPolicyFor(t, super, tenantID, "AWAIT-SUPERSEDED old policy")
	oldVersion := seedApprovalPolicyVersionFor(t, super, tenantID, oldPolicy)
	seedApprovalStepFor(t, super, tenantID, oldVersion, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: strPtr("finance-lead"),
	})
	sealApprovalPolicyVersionFor(t, super, oldVersion)

	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1`, tenantID); n != 2 {
		t.Fatalf("tenant holds %d policy versions, want 2 -- the second version did not land", n)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, tenantID); n != 1 {
		t.Fatalf("tenant holds %d ACTIVE policy versions, want exactly 1", n)
	}

	gated := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-SUPERSEDED-1", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gated, activeVersion)

	items, total := listAwaiting(t, store, c, ListFilter{})

	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{gated: true}) {
		t.Errorf("ids = %v, want exactly [%s] -- a superseded sealed version must neither add nor "+
			"remove rows", sortedIDs(got), gated)
	}
}

// TestStoreList_AwaitingApprovalFollowsTheGateOnASoftDeletedPolicy: approval_policies
// carries deleted_at, and NEITHER the fragment nor approval.TransmitClearTx joins to it --
// both resolve on approval_policy_versions.is_active alone. So soft-deleting the policy
// under an active version leaves its invoices gated in both.
//
// Asserted as agreement, not as a product opinion: whichever way that behaviour should go,
// the list surface and the transmit gate must move together (AC #4).
func TestStoreList_AwaitingApprovalFollowsTheGateOnASoftDeletedPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-SOFTDEL")
	c := tenantCtx(tenantID)

	gated := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-SOFTDEL-1", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gated, versionID)

	mustExec(t, super, `UPDATE approval_policies SET deleted_at = now() WHERE tenant_id = $1`, tenantID)
	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, tenantID); n != 1 {
		t.Fatalf("the soft delete deactivated the version (%d active, want 1) -- this test now proves "+
			"nothing about a deleted policy holding an active version", n)
	}

	items, _ := listAwaiting(t, store, c, ListFilter{})
	got := idSet(items)

	clear := transmitClearFor(t, app, c, []string{gated})
	want := map[string]bool{}
	if !clear[gated] {
		want[gated] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v but approval.TransmitClearTx reports clear=%v for %s -- the list filter and "+
			"the transmit gate disagree about a soft-deleted policy", sortedIDs(got), clear[gated], gated)
	}
}

// --- run history: EXISTS over every run, not the latest ---------------------

// TestStoreList_AwaitingApprovalApprovedRunSurvivesALaterRun: an approved run clears the
// invoice permanently. A later cancelled or rejected run does not put it back on the list,
// in either arrival order.
//
// GateFactsTx deliberately answers approvedRun as EXISTS rather than latest-run
// (TestGateFactsTx_ApprovedRunIsExistsNotLatestRun), so a latest-run rewrite of either copy
// is a drift. Only the approved-then-closed orderings below can see it.
func TestStoreList_AwaitingApprovalApprovedRunSurvivesALaterRun(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-HISTORY")
	c := tenantCtx(tenantID)

	// runs are closed in order; approval_runs_one_open forbids two open at once.
	seq := func(number string, states ...string) string {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, number, StatusValidated)
		for _, state := range states {
			closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, id, versionID), state, "closer")
		}
		return id
	}

	approvedThenCancelled := seq("AWAIT-HISTORY-appr-canc", "approved", "cancelled")
	cancelledThenApproved := seq("AWAIT-HISTORY-canc-appr", "cancelled", "approved")
	approvedThenRejected := seq("AWAIT-HISTORY-appr-rej", "approved", "rejected")
	neverApproved := seq("AWAIT-HISTORY-rej-canc", "rejected", "cancelled")

	items, total := listAwaiting(t, store, c, ListFilter{})
	got := idSet(items)

	want := map[string]bool{neverApproved: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want exactly [%s]. Every other invoice holds an approved run somewhere in "+
			"its history (%s, %s, %s) and must stay off the list whatever closed last",
			sortedIDs(got), neverApproved, approvedThenCancelled, cancelledThenApproved, approvedThenRejected)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}

	// Agreement with the gate, on the same fixture.
	ids := []string{approvedThenCancelled, cancelledThenApproved, approvedThenRejected, neverApproved}
	clear := transmitClearFor(t, app, c, ids)
	for _, id := range ids {
		if want[id] == clear[id] {
			t.Errorf("invoice %s: filter membership %v and approval.TransmitClearTx clear %v agree in "+
				"sign, want them opposite on validated rows", id, want[id], clear[id])
		}
	}
}

// --- composition ------------------------------------------------------------

// TestStoreList_AwaitingApprovalPaginatesWithoutOverlapOrGap (AC #8): the filter pages
// under the shipped ORDER BY created_at DESC, id DESC -- a total order, so three Limit-2
// pages must partition the five matches exactly, with total steady at 5 on every page.
func TestStoreList_AwaitingApprovalPaginatesWithoutOverlapOrGap(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-PAGE")
	c := tenantCtx(tenantID)

	want := map[string]bool{}
	for _, suffix := range []string{"1", "2", "3", "4", "5"} {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-PAGE-"+suffix, StatusValidated)
		seedApprovalRunFor(t, super, tenantID, id, versionID)
		want[id] = true
	}
	// Two non-matching rows, so a filter dropped on the page query alone would show up.
	cleared := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-PAGE-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, cleared, versionID), "approved", "approver")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-PAGE-draft", StatusDraft)

	seen := map[string]bool{}
	for _, offset := range []int{0, 2, 4} {
		items, total := listAwaiting(t, store, c, ListFilter{Limit: 2, Offset: offset})
		if total != 5 {
			t.Errorf("offset %d: total = %d, want 5 on every page", offset, total)
		}
		wantLen := 2
		if offset == 4 {
			wantLen = 1
		}
		if len(items) != wantLen {
			t.Errorf("offset %d: len(items) = %d, want %d", offset, len(items), wantLen)
		}
		for _, inv := range items {
			if seen[inv.ID] {
				t.Errorf("offset %d: invoice %s appeared on an earlier page too", offset, inv.ID)
			}
			seen[inv.ID] = true
		}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("paging the filter yielded %v, want exactly the 5 matches %v", sortedIDs(seen), sortedIDs(want))
	}
}

// TestStoreList_AwaitingApprovalAndsWithStatus: the fragment opens with its own
// status = "validated", so combining it with the Status filter puts two status predicates
// in one WHERE. A compatible pair must narrow to the same rows; a contradictory pair must
// return nothing, and must not error -- Status binds a parameter and the fragment does not,
// which is exactly where a placeholder mistake would surface.
func TestStoreList_AwaitingApprovalAndsWithStatus(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-STATUS")
	c := tenantCtx(tenantID)

	gated := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-STATUS-1", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gated, versionID)
	blockedDraft := seedInvoiceWithViolations(t, super, tenantID, entityID, "AWAIT-STATUS-draft",
		string(StatusDraft), `[{"rule_key":"buyer_tin_present","severity":"error","message":"x"}]`)

	if _, draftTotal, err := store.List(c, ListFilter{Status: StatusDraft, Limit: 100}); err != nil {
		t.Fatalf("List(Status draft): %v", err)
	} else if draftTotal != 1 {
		t.Fatalf("draft-only total = %d, want 1 (%s) -- the contradiction below would prove nothing",
			draftTotal, blockedDraft)
	}

	items, total := listAwaiting(t, store, c, ListFilter{Status: StatusValidated})
	if total != 1 {
		t.Errorf("awaiting + status=validated: total = %d, want 1", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{gated: true}) {
		t.Errorf("awaiting + status=validated: ids = %v, want exactly [%s]", sortedIDs(got), gated)
	}

	items, total = listAwaiting(t, store, c, ListFilter{Status: StatusDraft})
	if total != 0 || len(items) != 0 {
		t.Errorf("awaiting + status=draft: ids = %v, total = %d, want none -- the two status predicates "+
			"must AND to the empty set, not fall back to either one", sortedIDs(idSet(items)), total)
	}
}

// --- AC #1: the Down nothing else can see -----------------------------------

// TestMigration_AwaitingApprovalIndexCarriesAWorkingDown: a source scan, because every
// other gate on this migration is blind to a missing Down.
//
// goose reports EMPTY and exits 0 when a migration has no Down section, so `make
// migrate-down` stays green. CI reversibility job runs reset -> up, and the approval_runs
// migration Down drops the table, which drops this index regardless -- so the round trip
// stays green too. Only reading the file catches it.
func TestMigration_AwaitingApprovalIndexCarriesAWorkingDown(t *testing.T) {
	const idx = "approval_runs_invoice_lookup_idx"

	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*_"+idx+".sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d migrations named *_%s.sql, want exactly 1: %v", len(matches), idx, matches)
	}

	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	src := string(body)

	up := strings.Index(src, "-- +goose Up")
	down := strings.Index(src, "-- +goose Down")
	if up < 0 {
		t.Fatalf("%s has no -- +goose Up annotation", matches[0])
	}
	if down < 0 {
		t.Fatalf("%s has no -- +goose Down annotation. goose reports EMPTY and exits 0 for a "+
			"missing Down, so make migrate-down would still look green", matches[0])
	}
	if down < up {
		t.Errorf("%s declares Down before Up", matches[0])
	}
	if !strings.Contains(src[down:], "DROP INDEX "+idx) {
		t.Errorf("the Down section of %s does not DROP INDEX %s:\n%s", matches[0], idx, src[down:])
	}
	if strings.Contains(src[up:], "CONCURRENTLY") {
		t.Errorf("%s uses CREATE INDEX CONCURRENTLY, which cannot run inside the transaction goose "+
			"wraps a migration in unless the file declares -- +goose NO TRANSACTION", matches[0])
	}
}
