// task-498 (APPR-08-07, Mode A): RED specs for ListFilter.AwaitingApproval -- the list
// surface of the transmit gate -- and for the index that serves its anti-join.
//
// Nothing under test exists yet: Store.List carries no f.AwaitingApproval arm and
// approval_runs_invoice_lookup_idx has not been created, so every case below fails on
// its own assertion, never on a compile or connection error. ListFilter gains the field
// as a declaration-only stub so these specs compile.
//
// Spec-to-test map (task-498 Test Specs table):
//
//	AC-1 TestRLS_AwaitingApprovalIndexExists
//	AC-2/#3 TestStoreList_AwaitingApprovalNarrowsToGatedValidated
//	AC-2/#3 TestStoreList_AwaitingApprovalIncludesValidatedWithNoRun
//	AC-3 TestStoreList_AwaitingApprovalEmptyWithoutAnActivePolicy
//	AC-3 TestStoreList_AwaitingApprovalFragmentCarriesNoBindParams
//	AC-4 TestStoreList_AwaitingApprovalIsTheExactNegationOfTransmitClear
//	AC-4 TestStoreList_AwaitingApprovalIsNotNeedsAttention
//	AC-4 TestStoreList_AwaitingApprovalIsNotNeedsFix
//	AC-6 TestListHandler_AwaitingApproval{ParamIsABoolean,EmptyIsAbsent,TrueReachesTheFilter,FalseExplicitMatchesAbsent}
//	AC-8 TestStoreList_AwaitingApprovalTotalIsTheFilteredTotal
//	RLS  TestStoreList_AwaitingApprovalIsTenantScoped
//	misc TestStoreList_AwaitingApprovalAndsWithTheOtherFilters
//
// AC-1's Up/Down/Up round trip is a PROCEDURE, not a Go test: driving goose Down needs
// the migrator DSN, goose.NewProvider and migrations.FS, which only
// internal/platform/db's harness carries. The standing gate is CI's migrations job.
//
// Fixtures are reused wholesale: seedOneStepActivePolicyTenant / seedApprovalRunFor /
// closeApprovalRunFor (apply_validation_arming_test.go), seedInactivePolicyTenant
// (batch_submit_gate_test.go), seedInvoiceAtStatus (transition_adversarial_test.go),
// seedInvoiceWithViolations / mustCount (store_test.go), mustExec
// (approve_reject_gate_adversarial_test.go).
//
// Run: DATABASE_URL=... DATABASE_SUPERUSER_URL=... go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- local helpers (no new SQL: every seeder called here already ships) -----

// tenantCtx is the one-liner every case below opens with.
func tenantCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})
}

// transmitClearFor answers approval.TransmitClearTx for ids inside c's tenant tx. The
// negation oracle reads the gate's OWN code through this, so a drift in either SQL copy
// breaks the agreement rather than being restated identically on both sides.
func transmitClearFor(t *testing.T, app *pgxpool.Pool, c context.Context, ids []string) map[string]bool {
	t.Helper()
	var out map[string]bool
	if err := db.WithinRequestTenantTx(c, app, func(tx pgx.Tx) error {
		var err error
		out, err = approval.TransmitClearTx(c, tx, ids)
		return err
	}); err != nil {
		t.Fatalf("approval.TransmitClearTx: %v", err)
	}
	return out
}

// idSet collapses a List page to a set, for set-equality assertions.
func idSet(items []Invoice) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, inv := range items {
		out[inv.ID] = true
	}
	return out
}

// sortedIDs renders a set for failure messages.
func sortedIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// listAwaiting runs the filter under test and fails on error.
func listAwaiting(t *testing.T, store *Store, c context.Context, f ListFilter) ([]Invoice, int) {
	t.Helper()
	f.AwaitingApproval = true
	if f.Limit == 0 {
		f.Limit = 100
	}
	items, total, err := store.List(c, f)
	if err != nil {
		t.Fatalf("List(AwaitingApproval): %v", err)
	}
	return items, total
}

// --- AC #1: the index ------------------------------------------------------

// TestRLS_AwaitingApprovalIndexExists: pg_indexes carries no RLS, so the superuser pool
// answers this directly. Column ORDER is load-bearing -- tenant_id must lead so the RLS
// qual becomes an Index Cond, and opened_at DESC is what RowFactsTx's DISTINCT ON and
// GateFactsTx's ORDER BY LIMIT 1 consume without a sort.
func TestRLS_AwaitingApprovalIndexExists(t *testing.T) {
	super, _ := dbTestPools(t)

	const idxQuery = `SELECT count(*) FROM pg_indexes
	                   WHERE tablename = 'approval_runs'
	                     AND indexname = 'approval_runs_invoice_lookup_idx'`
	if n := mustCount(t, super, idxQuery); n != 1 {
		t.Fatalf("pg_indexes rows for approval_runs_invoice_lookup_idx = %d, want exactly 1 "+
			"-- the APPR-08-07 migration has not been applied", n)
	}

	var indexdef string
	if err := super.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes
		   WHERE tablename = 'approval_runs' AND indexname = 'approval_runs_invoice_lookup_idx'`,
	).Scan(&indexdef); err != nil {
		t.Fatalf("read indexdef: %v", err)
	}
	const wantCols = "(tenant_id, invoice_id, opened_at DESC)"
	if !strings.Contains(indexdef, wantCols) {
		t.Errorf("indexdef = %q, want it to contain %q", indexdef, wantCols)
	}
	if strings.Contains(indexdef, "WHERE") {
		t.Errorf("indexdef = %q, want NO partial predicate -- the lookup filters state = "+
			"approved, and a partial index on that would stop serving the latest-run ordering", indexdef)
	}
}

// --- AC #2/#3: what the filter narrows to ----------------------------------

// TestStoreList_AwaitingApprovalNarrowsToGatedValidated: only the validated invoice an
// active policy is still holding. The approved-run validated row is the control that
// makes this more than "status = validated".
func TestStoreList_AwaitingApprovalNarrowsToGatedValidated(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-NARROW")
	c := tenantCtx(tenantID)

	openID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NARROW-open", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, openID, versionID)

	approvedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NARROW-approved", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, approvedID, versionID), "approved", "approver")

	seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NARROW-draft", StatusDraft)
	seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NARROW-rejected", StatusRejected)

	items, total := listAwaiting(t, store, c, ListFilter{})

	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{openID: true}) {
		t.Errorf("ids = %v, want exactly [%s] (the open-run validated invoice)", sortedIDs(got), openID)
	}
}

// TestStoreList_AwaitingApprovalIncludesValidatedWithNoRun: a validated invoice with no
// run at all is BLOCKED by the gate (TransmitClear reads clear only via an approved run),
// so an operator must see it. A "has an open run" reading of the filter would drop it.
func TestStoreList_AwaitingApprovalIncludesValidatedWithNoRun(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-NORUN")
	c := tenantCtx(tenantID)

	noRunID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NORUN-1", StatusValidated)
	// A cleared sibling, so "the run-less one is included" is not just "everything is".
	clearedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NORUN-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, clearedID, versionID), "approved", "approver")

	items, total := listAwaiting(t, store, c, ListFilter{})

	if total != 1 {
		t.Errorf("total = %d, want 1 (the run-less invoice only)", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{noRunID: true}) {
		t.Errorf("ids = %v, want exactly [%s] -- the run-less validated invoice is blocked and must "+
			"appear; the approved-run sibling %s must not", sortedIDs(got), noRunID, clearedID)
	}
}

// TestStoreList_AwaitingApprovalEmptyWithoutAnActivePolicy: a sealed-but-inactive version
// gates nothing, so nothing is awaiting -- including invoices carrying stale closed runs
// from a version that is no longer active.
func TestStoreList_AwaitingApprovalEmptyWithoutAnActivePolicy(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedInactivePolicyTenant(t, super, "AWAIT-NOPOLICY")
	c := tenantCtx(tenantID)

	for i, suffix := range []string{"a", "b", "c"} {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-NOPOLICY-"+suffix, StatusValidated)
		if i > 0 {
			closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, id, versionID), "cancelled", "system")
		}
	}

	// Control: the tenant really does hold rows, so zero below is the predicate talking.
	if _, unfilteredTotal, err := store.List(c, ListFilter{Limit: 100}); err != nil {
		t.Fatalf("List (unfiltered): %v", err)
	} else if unfilteredTotal != 3 {
		t.Fatalf("unfiltered total = %d, want 3 -- the fixture did not land", unfilteredTotal)
	}

	items, total := listAwaiting(t, store, c, ListFilter{})

	if total != 0 {
		t.Errorf("total = %d, want 0 -- no active version means no invoice is awaiting approval", total)
	}
	if len(items) != 0 {
		t.Errorf("ids = %v, want none", sortedIDs(idSet(items)))
	}
}

// --- AC #4: the exact-negation oracle --------------------------------------

// awaitFixtureSpec is one seeded invoice: a status plus the approval_runs state to leave
// behind ("" = no run, "open" = leave the run open).
type awaitFixtureSpec struct {
	suffix string
	status Status
	run    string
}

// awaitNegationFixture spans {no run, open, approved, rejected, cancelled} on validated
// invoices, plus two non-validated controls that the gate ALSO reports blocked -- they are
// what makes the "restricted to validated" half of AC #4 assertable.
var awaitNegationFixture = []awaitFixtureSpec{
	{"validated-norun", StatusValidated, ""},
	{"validated-open", StatusValidated, "open"},
	{"validated-approved", StatusValidated, "approved"},
	{"validated-rejected-run", StatusValidated, "rejected"},
	{"validated-cancelled-run", StatusValidated, "cancelled"},
	{"draft-norun", StatusDraft, ""},
	{"rejected-approved-run", StatusRejected, "approved"},
}

// TestStoreList_AwaitingApprovalIsTheExactNegationOfTransmitClear (AC #4's oracle): the
// filter's id set must equal {id : status == validated AND NOT TransmitClearTx[id]}.
//
// The expectation is DERIVED by calling approval.TransmitClearTx, never by restating the
// gate's SQL here -- the two copies live in different packages with no shared constant, so
// only an agreement test can catch one drifting (narrowing the policy resolve, switching
// the approved-run EXISTS to the latest run, widening the approval_runs state CHECK).
func TestStoreList_AwaitingApprovalIsTheExactNegationOfTransmitClear(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	seed := func(tenantID, entityID, versionID, label string) map[string]Status {
		statuses := make(map[string]Status, len(awaitNegationFixture))
		for _, spec := range awaitNegationFixture {
			id := seedInvoiceAtStatus(t, super, tenantID, entityID, label+"-"+spec.suffix, spec.status)
			if spec.run != "" {
				runID := seedApprovalRunFor(t, super, tenantID, id, versionID)
				if spec.run != "open" {
					closeApprovalRunFor(t, super, runID, spec.run, "closer")
				}
			}
			statuses[id] = spec.status
		}
		return statuses
	}

	activeTenant, activeEntity, activeVersion := seedOneStepActivePolicyTenant(t, super, "AWAIT-NEG-ACTIVE")
	activeStatuses := seed(activeTenant, activeEntity, activeVersion, "AWAIT-NEG-ACTIVE")

	inactiveTenant, inactiveEntity, inactiveVersion := seedInactivePolicyTenant(t, super, "AWAIT-NEG-INACTIVE")
	inactiveStatuses := seed(inactiveTenant, inactiveEntity, inactiveVersion, "AWAIT-NEG-INACTIVE")

	for _, tc := range []struct {
		name         string
		tenantID     string
		statuses     map[string]Status
		policyActive bool
	}{
		{"active policy", activeTenant, activeStatuses, true},
		{"no active policy", inactiveTenant, inactiveStatuses, false},
	} {
		c := tenantCtx(tc.tenantID)

		ids := make([]string, 0, len(tc.statuses))
		validated := 0
		for id, status := range tc.statuses {
			ids = append(ids, id)
			if status == StatusValidated {
				validated++
			}
		}

		clear := transmitClearFor(t, app, c, ids)
		want := map[string]bool{}
		for id, status := range tc.statuses {
			if status == StatusValidated && !clear[id] {
				want[id] = true
			}
		}

		// Guard the oracle before trusting it: under an active policy it must split the
		// validated rows (some blocked, some clear), and under none it must clear them all.
		if tc.policyActive {
			if len(want) == 0 || len(want) == validated {
				t.Fatalf("%s: TransmitClearTx blocked %d of %d validated invoices -- the fixture "+
					"no longer discriminates, so the comparison below would be vacuous", tc.name, len(want), validated)
			}
		} else if len(want) != 0 {
			t.Fatalf("%s: TransmitClearTx blocked %d invoices with no active version, want 0", tc.name, len(want))
		}

		items, total := listAwaiting(t, store, c, ListFilter{})
		got := idSet(items)

		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: AwaitingApproval ids = %v, want exactly %v -- the filter is not the "+
				"exact negation of approval.TransmitClear restricted to validated",
				tc.name, sortedIDs(got), sortedIDs(want))
		}
		if total != len(want) {
			t.Errorf("%s: total = %d, want %d", tc.name, total, len(want))
		}
	}
}

// --- AC #4: disjoint from BOTH siblings ------------------------------------

// TestStoreList_AwaitingApprovalIsNotNeedsAttention: a third predicate, not a slice of
// needs_attention. Disjoint by status -- needs_attention matches only rejected/failed/
// draft, awaiting_approval only validated -- and both sets are non-empty here, so neither
// can be a subset of the other.
func TestStoreList_AwaitingApprovalIsNotNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-VS-ATT")
	c := tenantCtx(tenantID)

	gatedOpen := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-ATT-open", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gatedOpen, versionID)
	gatedNoRun := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-ATT-norun", StatusValidated)

	rejectedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-ATT-rejected", StatusRejected)
	failedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-ATT-failed", StatusFailed)
	blockedDraft := seedInvoiceWithViolations(t, super, tenantID, entityID, "AWAIT-VS-ATT-blocked",
		string(StatusDraft), `[{"rule_key":"buyer_tin_present","severity":"error","message":"x"}]`)

	// Statuses awaiting_approval must never reach, whatever their run state.
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, rejectedID, versionID), "rejected", "approver")

	awaiting, _ := listAwaiting(t, store, c, ListFilter{})
	attentionItems, _, err := store.List(c, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsAttention): %v", err)
	}

	awaitingSet, attentionSet := idSet(awaiting), idSet(attentionItems)

	wantAwaiting := map[string]bool{gatedOpen: true, gatedNoRun: true}
	if !reflect.DeepEqual(awaitingSet, wantAwaiting) {
		t.Errorf("AwaitingApproval ids = %v, want exactly %v", sortedIDs(awaitingSet), sortedIDs(wantAwaiting))
	}
	wantAttention := map[string]bool{rejectedID: true, failedID: true, blockedDraft: true}
	if !reflect.DeepEqual(attentionSet, wantAttention) {
		t.Fatalf("NeedsAttention ids = %v, want exactly %v -- the control arm of this test did not land",
			sortedIDs(attentionSet), sortedIDs(wantAttention))
	}
	assertDisjointAndNeitherSubset(t, "AwaitingApproval", awaitingSet, "NeedsAttention", attentionSet)
}

// TestStoreList_AwaitingApprovalIsNotNeedsFix: the other sibling AC #4 names. needs_fix is
// draft-only and drops kept rows; awaiting_approval is validated-only and knows nothing of
// the kept mark.
func TestStoreList_AwaitingApprovalIsNotNeedsFix(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-VS-FIX")
	c := tenantCtx(tenantID)

	gatedOpen := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-FIX-open", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gatedOpen, versionID)
	gatedNoRun := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-VS-FIX-norun", StatusValidated)

	const blocking = `[{"rule_key":"buyer_tin_present","severity":"error","message":"x"}]`
	blockedDraft := seedInvoiceWithViolations(t, super, tenantID, entityID, "AWAIT-VS-FIX-blocked", string(StatusDraft), blocking)
	keptDraft := seedInvoiceWithViolations(t, super, tenantID, entityID, "AWAIT-VS-FIX-kept", string(StatusDraft), blocking)
	mustExec(t, super,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'triaged' WHERE id = $1`,
		keptDraft)

	awaiting, _ := listAwaiting(t, store, c, ListFilter{})
	fixItems, _, err := store.List(c, ListFilter{NeedsFix: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsFix): %v", err)
	}

	awaitingSet, fixSet := idSet(awaiting), idSet(fixItems)

	wantAwaiting := map[string]bool{gatedOpen: true, gatedNoRun: true}
	if !reflect.DeepEqual(awaitingSet, wantAwaiting) {
		t.Errorf("AwaitingApproval ids = %v, want exactly %v", sortedIDs(awaitingSet), sortedIDs(wantAwaiting))
	}
	wantFix := map[string]bool{blockedDraft: true}
	if !reflect.DeepEqual(fixSet, wantFix) {
		t.Fatalf("NeedsFix ids = %v, want exactly %v (the kept draft %s is excluded) -- the control "+
			"arm of this test did not land", sortedIDs(fixSet), sortedIDs(wantFix), keptDraft)
	}
	assertDisjointAndNeitherSubset(t, "AwaitingApproval", awaitingSet, "NeedsFix", fixSet)
}

// assertDisjointAndNeitherSubset pins the whole of AC #4's disjointness claim: no shared
// member, and both sets non-empty so "neither is a subset of the other" is a real property
// rather than a fact about the empty set.
func assertDisjointAndNeitherSubset(t *testing.T, nameA string, a map[string]bool, nameB string, b map[string]bool) {
	t.Helper()
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("%s has %d ids and %s has %d -- an empty set is a subset of everything, so this "+
			"comparison would prove nothing", nameA, len(a), nameB, len(b))
	}
	for id := range a {
		if b[id] {
			t.Errorf("invoice %s is in BOTH %s and %s, want the two predicates disjoint", id, nameA, nameB)
		}
	}
}

// --- composition, paging, tenancy ------------------------------------------

// TestStoreList_AwaitingApprovalAndsWithTheOtherFilters: the fragment joins the WHERE with
// AND like every other condition, so entity_id still narrows inside it.
func TestStoreList_AwaitingApprovalAndsWithTheOtherFilters(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityA, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-AND")
	entityB := seedEntity(t, super, tenantID, "AWAIT-AND entity B")
	c := tenantCtx(tenantID)

	gatedA := seedInvoiceAtStatus(t, super, tenantID, entityA, "AWAIT-AND-a", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gatedA, versionID)
	gatedB := seedInvoiceAtStatus(t, super, tenantID, entityB, "AWAIT-AND-b", StatusValidated)
	seedApprovalRunFor(t, super, tenantID, gatedB, versionID)
	// Entity A also holds a CLEARED invoice, so the awaiting conjunct has to do work --
	// otherwise entity_id alone would produce this same answer.
	clearedA := seedInvoiceAtStatus(t, super, tenantID, entityA, "AWAIT-AND-a-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, clearedA, versionID), "approved", "approver")

	if _, entityOnlyTotal, err := store.List(c, ListFilter{EntityID: entityA, Limit: 100}); err != nil {
		t.Fatalf("List(EntityID only): %v", err)
	} else if entityOnlyTotal != 2 {
		t.Fatalf("entity_id-only total = %d, want 2 -- the fixture did not land, so the AND below "+
			"would prove nothing", entityOnlyTotal)
	}

	items, total := listAwaiting(t, store, c, ListFilter{EntityID: entityA})

	if total != 1 {
		t.Errorf("total = %d, want 1 -- entity_id and awaiting_approval must both narrow", total)
	}
	if got := idSet(items); !reflect.DeepEqual(got, map[string]bool{gatedA: true}) {
		t.Errorf("ids = %v, want exactly [%s]", sortedIDs(got), gatedA)
	}
}

// TestStoreList_AwaitingApprovalTotalIsTheFilteredTotal (AC #8): total counts every
// matching row across all pages, not the page -- and it is the FILTERED count, not the
// tenant's invoice count, which the two non-matching rows below pin.
func TestStoreList_AwaitingApprovalTotalIsTheFilteredTotal(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "AWAIT-TOTAL")
	c := tenantCtx(tenantID)

	for _, suffix := range []string{"1", "2", "3", "4", "5"} {
		id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-TOTAL-"+suffix, StatusValidated)
		seedApprovalRunFor(t, super, tenantID, id, versionID)
	}
	clearedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-TOTAL-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantID, clearedID, versionID), "approved", "approver")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "AWAIT-TOTAL-draft", StatusDraft)

	items, total := listAwaiting(t, store, c, ListFilter{Limit: 2})

	if total != 5 {
		t.Errorf("total = %d, want 5 -- the filtered count across all pages (7 invoices exist, 2 do not match)", total)
	}
	if len(items) != 2 {
		t.Errorf("len(items) = %d, want 2 (Limit)", len(items))
	}
}

// TestStoreList_AwaitingApprovalIsTenantScoped: the fragment carries no tenant_id
// predicate by design -- RLS is the whole scope. Tenant B runs the same filter over its
// own active policy and sees none of tenant A's gated invoices.
func TestStoreList_AwaitingApprovalIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantA, entityA, versionA := seedOneStepActivePolicyTenant(t, super, "AWAIT-TENANT-A")
	for _, suffix := range []string{"1", "2"} {
		id := seedInvoiceAtStatus(t, super, tenantA, entityA, "AWAIT-TENANT-A-"+suffix, StatusValidated)
		seedApprovalRunFor(t, super, tenantA, id, versionA)
	}

	tenantB, entityB, versionB := seedOneStepActivePolicyTenant(t, super, "AWAIT-TENANT-B")
	clearedB := seedInvoiceAtStatus(t, super, tenantB, entityB, "AWAIT-TENANT-B-cleared", StatusValidated)
	closeApprovalRunFor(t, super, seedApprovalRunFor(t, super, tenantB, clearedB, versionB), "approved", "approver")

	// Sanity: A really does have gated rows, so B's zero is isolation and not an empty DB.
	if _, totalA := listAwaiting(t, store, tenantCtx(tenantA), ListFilter{}); totalA != 2 {
		t.Fatalf("tenant A total = %d, want 2 -- the fixture did not land", totalA)
	}

	items, total := listAwaiting(t, store, tenantCtx(tenantB), ListFilter{})

	if total != 0 {
		t.Errorf("tenant B total = %d, want 0", total)
	}
	if len(items) != 0 {
		t.Errorf("tenant B ids = %v, want none -- RLS is the only tenant scope this fragment has",
			sortedIDs(idSet(items)))
	}
}

// --- AC #3: the half no behavioural test can see ---------------------------

// TestStoreList_AwaitingApprovalFragmentCarriesNoBindParams: a source scan, because both
// defects it catches are silent. A bind param inside the fragment shifts the placeholder
// numbers the LIMIT/OFFSET clause computes from len(args). A bare `id` in the NOT EXISTS
// binds to approval_runs.id -- approval_runs has its own id column -- comparing a run id
// to a run id, always false, with no error from Postgres.
//
// Same scan idiom as TestStoreList_NeedsAttentionSQLRejectedArmIsBare, which reads the
// FIRST occurrence of its own marker in this file and must stay green: keep this
// condition BELOW the NeedsAttention block and introduce no backtick literal above it.
func TestStoreList_AwaitingApprovalFragmentCarriesNoBindParams(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	marker := "if f.AwaitingApproval {"
	idx := bytes.Index(src, []byte(marker))
	if idx < 0 {
		t.Fatalf("store.go: no %q marker -- List has no AwaitingApproval condition yet", marker)
	}
	rest := src[idx+len(marker):]
	open := bytes.IndexByte(rest, '`')
	if open < 0 {
		t.Fatal("store.go: the AwaitingApproval condition is not a raw string literal")
	}
	rest = rest[open+1:]
	closeIdx := bytes.IndexByte(rest, '`')
	if closeIdx < 0 {
		t.Fatal("store.go: unterminated AwaitingApproval condition literal")
	}
	block := string(rest[:closeIdx])

	if strings.Contains(block, "$") {
		t.Errorf("AwaitingApproval condition carries a bind param, which shifts the LIMIT/OFFSET "+
			"placeholders computed from len(args):\n%s", block)
	}
	if !strings.Contains(block, "invoices.id") {
		t.Errorf("AwaitingApproval condition does not qualify the outer id as invoices.id -- a bare "+
			"id binds to approval_runs.id and silently never matches:\n%s", block)
	}
	if !strings.Contains(block, "status = 'validated'") {
		t.Errorf("AwaitingApproval condition lost its status = 'validated' restriction:\n%s", block)
	}
}

// --- AC #6: the handler param ----------------------------------------------

// captureListFilter drives GET /v1/invoices with query and returns the ListFilter the
// handler built, failing unless the request reached the store with a 200.
func captureListFilter(t *testing.T, query string) ListFilter {
	t.Helper()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	var captured ListFilter
	called := false
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		called = true
		captured = f
		return []Invoice{}, 0, nil
	}
	rec, _ := doInvoiceList(t, list, &id, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("query=%q: status = %d, want 200 (body=%s)", query, rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatalf("query=%q: store.List was not called", query)
	}
	return captured
}

// TestListHandler_AwaitingApprovalParamIsABoolean: a non-boolean 400s with needs_fix's
// message shape, BEFORE the store is reached.
func TestListHandler_AwaitingApprovalParamIsABoolean(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		t.Fatalf("store.List was called with %+v -- a non-boolean awaiting_approval must 400 first", f)
		return nil, 0, nil
	}
	rec, resp := doInvoiceList(t, list, &id, "?awaiting_approval=maybe")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	const want = "awaiting_approval must be a boolean"
	if resp.Error != want {
		t.Errorf("error = %q, want %q", resp.Error, want)
	}
}

// TestListHandler_AwaitingApprovalEmptyIsAbsent: ?awaiting_approval= applies no predicate.
// The true leg is what stops this passing against a handler that ignores the param.
func TestListHandler_AwaitingApprovalEmptyIsAbsent(t *testing.T) {
	if got := captureListFilter(t, "?awaiting_approval="); got.AwaitingApproval {
		t.Errorf("?awaiting_approval= : captured.AwaitingApproval = true, want false (empty is absent)")
	}
	if got := captureListFilter(t, "?awaiting_approval=true"); !got.AwaitingApproval {
		t.Errorf("?awaiting_approval=true : captured.AwaitingApproval = false, want true -- without this " +
			"leg the empty-is-absent assertion above passes against a handler that never reads the param")
	}
}

// TestListHandler_AwaitingApprovalTrueReachesTheFilter: the param arrives on ListFilter,
// and touches nothing else on it.
func TestListHandler_AwaitingApprovalTrueReachesTheFilter(t *testing.T) {
	got := captureListFilter(t, "?awaiting_approval=true")

	if !got.AwaitingApproval {
		t.Fatalf("captured.AwaitingApproval = false, want true (filter=%+v)", got)
	}
	absent := captureListFilter(t, "")
	absent.AwaitingApproval = true
	if !reflect.DeepEqual(absent, got) {
		t.Errorf("?awaiting_approval=true captured %+v, want it to differ from the absent filter in "+
			"AwaitingApproval ALONE (%+v)", got, absent)
	}
}

// TestListHandler_AwaitingApprovalFalseExplicitMatchesAbsent: on the WIRE those are two
// different query strings that must both parse to the same ListFilter -- neither 400ing
// nor diverging. The true leg keeps the comparison honest; see
// TestListFilterDeepEqualStillDiscriminates for the strength of DeepEqual itself.
func TestListHandler_AwaitingApprovalFalseExplicitMatchesAbsent(t *testing.T) {
	absent := captureListFilter(t, "")
	explicitFalse := captureListFilter(t, "?awaiting_approval=false")
	explicitTrue := captureListFilter(t, "?awaiting_approval=true")

	if absent.AwaitingApproval {
		t.Errorf("absent ?awaiting_approval: captured.AwaitingApproval = true, want false")
	}
	if explicitFalse.AwaitingApproval {
		t.Errorf("?awaiting_approval=false: captured.AwaitingApproval = true, want false")
	}
	if !reflect.DeepEqual(absent, explicitFalse) {
		t.Errorf("captured ListFilter differs between absent (%+v) and explicit false (%+v), want identical",
			absent, explicitFalse)
	}
	if reflect.DeepEqual(absent, explicitTrue) {
		t.Errorf("captured ListFilter is identical for absent and ?awaiting_approval=true (%+v) -- the "+
			"handler is not reading the param, so the equality above proves nothing", explicitTrue)
	}
}
