// BUG-12-01 (task-868): the list wire must carry the SAME submitGate answer the
// detail wire already publishes, folded against APPROVALS_ENFORCED in ONE place.
//
// Every assertion here reads RAW wire bytes rather than a decoded struct: the keys
// under test do not exist yet, and a decode would turn an absent key into a silent
// zero -- the fail-open shape this story exists to remove.
//
// Run: DEV_DB_PORT=5437 make -s test-invoice
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- helpers ----------------------------------------------------------------

// listSubmitPair drives the REAL ListHandler (the cmd/invoice/main.go wiring) and
// returns invID's submit pair UNDECODED. An invoice missing from the page is a
// Fatal: a silent "not found" would make every comparison below vacuous.
func listSubmitPair(t *testing.T, store *Store, ctx context.Context, invID, label string) (canSubmit, reason string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: LIST status = %d, want 200 (body=%s)", label, rec.Code, rec.Body.String())
	}
	for _, row := range listRowsRaw(t, rec) {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("%s: decode row id: %v", label, err)
		}
		if id == invID {
			return submitFlagsOf(t, row, label+" (the LIST row)")
		}
	}
	t.Fatalf("%s: invoice %s is not on the page: %s", label, invID, rec.Body.String())
	return "", ""
}

// detailSubmitPair is listSubmitPair's twin on GET /v1/invoices/{id}.
func detailSubmitPair(t *testing.T, store *Store, ctx context.Context, invID, label string) (canSubmit, reason string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invID, nil)
	r.SetPathValue("id", invID)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: GET status = %d, want 200 (body=%s)", label, rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode detail body %q: %v", label, rec.Body.String(), err)
	}
	return submitFlagsOf(t, body, label+" (the DETAIL response)")
}

// --- AC-2: one gate, two wires ----------------------------------------------

// TestListAndDetail_SubmitGateCannotDisagree is the spec that proves ONE predicate
// feeds TWO wires. A second copy of submitGate's ladder written for the list -- in
// Go, in SQL or in TypeScript -- passes every other spec in this file and fails here
// on the first rung whose wording, order or flag fold drifted.
//
// TWO tenants, and they cannot be merged. TransmitClearTx's gate is
// `EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)`, which is
// TENANT-wide under RLS, so one active policy makes every unarmed row in that tenant
// read blocked under flag-ON and the seven-status walk stops testing the status rungs.
//
// The flag-polarity claim itself lives in
// TestListHandler_SubmitFlagsFlipWithTheEnforcementFlag; this spec only requires the
// two wires to agree, at every status and at both settings.
func TestListAndDetail_SubmitGateCannotDisagree(t *testing.T) {
	super, app := dbTestPools(t)

	// Leg 1: a tenant with NO active policy version. TransmitClearTx short-circuits
	// every id to clear at both flag settings, isolating the role and status rungs.
	walkTenant := seedTenant(t, super, "BUG-12-01-WALK")
	walkEntity := seedEntity(t, super, walkTenant, "BUG-12-01 Walk Corp")
	walkSubject := uuid.NewString()
	seedMembership(t, super, walkTenant, walkSubject, "admin")
	walkCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: walkSubject, Role: "authenticated", TenantID: walkTenant,
	})

	// wantCan/wantReason are hand-written, not derived from submitGate: an oracle that
	// called the predicate under test would agree with any ladder, including a broken one.
	walk := []struct {
		status     Status
		wantCan    string
		wantReason string
	}{
		{StatusDraft, "false", jsonOf(t, "Only validated invoices can be submitted — re-validate this invoice first.")},
		{StatusValidated, "true", "null"},
		{StatusQueued, "false", "null"},
		{StatusSubmitted, "false", "null"},
		{StatusAccepted, "false", "null"},
		{StatusRejected, "false", jsonOf(t, "Only validated invoices can be submitted — edit this invoice and re-validate it first.")},
		{StatusFailed, "false", "null"},
	}
	walkIDs := make([]string, len(walk))
	for i, c := range walk {
		walkIDs[i] = seedInvoiceAtStatus(t, super, walkTenant, walkEntity, "bug-12-01-walk-"+string(c.status), c.status)
	}

	// The vacuity guard the sibling approve spec uses: an agreement of two falses,
	// or of two nulls, proves nothing.
	sawTrue := false
	sawSentence := false

	for _, enforced := range []bool{false, true} {
		store := NewStore(app, WithApprovalsEnforced(enforced))
		for i, c := range walk {
			t.Run(string(c.status)+"/enforced="+boolLabel(enforced), func(t *testing.T) {
				label := string(c.status)
				listCan, listReason := listSubmitPair(t, store, walkCtx, walkIDs[i], label)
				detailCan, detailReason := detailSubmitPair(t, store, walkCtx, walkIDs[i], label)

				if listCan != detailCan {
					t.Errorf("can_submit: list = %s, detail = %s -- ONE submitGate call feeds both wires, so they cannot differ", listCan, detailCan)
				}
				if listReason != detailReason {
					t.Errorf("submit_blocked_reason:\n  list   = %s\n  detail = %s\n-- the same submitGate call must produce both", listReason, detailReason)
				}
				if listCan != c.wantCan {
					t.Errorf("can_submit = %s, want %s for a %s invoice read by an admin in a policy-free tenant", listCan, c.wantCan, c.status)
				}
				if listReason != c.wantReason {
					t.Errorf("submit_blocked_reason = %s, want %s", listReason, c.wantReason)
				}
				if listCan == "true" {
					sawTrue = true
				}
				if listCan == "false" && listReason != "null" {
					sawSentence = true
				}
			})
		}
	}

	// Leg 2: MF-2 row 5, the production defect state -- an active policy with an open
	// run, in its OWN tenant. seedApprovalFactsFixture publishes the policy and
	// armInvoice opens the run through Store.ApplyValidation, the real arming path.
	fx := seedApprovalFactsFixture(t, super, "BUG-12-01-ARMED", true)
	fx.armInvoice(t, super, app, "bug-12-01-armed")

	reallyArmed := false
	t.Run("armed_validated_really_armed", func(t *testing.T) {
		on := NewStore(app, WithApprovalsEnforced(true))
		af, err := on.ApprovalFacts(fx.ctx, fx.invID)
		if err != nil {
			t.Fatalf("ApprovalFacts (flag on): %v", err)
		}
		if af.TransmitClear {
			t.Fatal("ApprovalFacts(flag on).TransmitClear = true for an OPEN run under an active policy -- the fixture did not arm, so the armed legs below prove nothing")
		}
		reallyArmed = true
	})

	for _, c := range []struct {
		enforced   bool
		wantCan    string
		wantReason string
	}{
		{false, "true", "null"},
		{true, "false", jsonOf(t, awaitingApprovalReason)},
	} {
		t.Run("armed_validated/enforced="+boolLabel(c.enforced), func(t *testing.T) {
			if !reallyArmed {
				t.Fatal("the armed fixture never reached the defect state -- see armed_validated_really_armed")
			}
			store := NewStore(app, WithApprovalsEnforced(c.enforced))
			listCan, listReason := listSubmitPair(t, store, fx.ctx, fx.invID, "armed")
			detailCan, detailReason := detailSubmitPair(t, store, fx.ctx, fx.invID, "armed")

			if listCan != detailCan {
				t.Errorf("can_submit: list = %s, detail = %s -- the armed row is the state the two surfaces disagree on in production", listCan, detailCan)
			}
			if listReason != detailReason {
				t.Errorf("submit_blocked_reason:\n  list   = %s\n  detail = %s", listReason, detailReason)
			}
			if listCan != c.wantCan {
				t.Errorf("can_submit = %s, want %s (APPROVALS_ENFORCED=%v, open run under an active policy)", listCan, c.wantCan, c.enforced)
			}
			if listReason != c.wantReason {
				t.Errorf("submit_blocked_reason = %s, want %s", listReason, c.wantReason)
			}
		})
	}

	if !sawTrue {
		t.Error("no walk leg read can_submit true -- an agreement of two falses proves nothing")
	}
	if !sawSentence {
		t.Error("no walk leg read a refusal SENTENCE -- an agreement of two nulls proves nothing")
	}
}

func boolLabel(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// --- AC-3: the flag reaches the list wire -----------------------------------

// TestListHandler_SubmitFlagsFlipWithTheEnforcementFlag carries the flag-POLARITY
// claim for the list wire; TestListAndDetail_SubmitGateCannotDisagree carries the
// agreement claim. One armed fixture read at both settings: the pair must flip, and
// must equal the detail body's pair at each setting.
//
// The flip assertion runs FIRST. Two identical readings mean the fixture never armed,
// and every assertion below it would pass on a wire that ignored the flag entirely.
func TestListHandler_SubmitFlagsFlipWithTheEnforcementFlag(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "BUG-12-01-FLIP", true)
	fx.armInvoice(t, super, app, "bug-12-01-flip")

	pairsFor := func(t *testing.T, enforced bool) (listCan, listReason, detailCan, detailReason string) {
		t.Helper()
		store := NewStore(app, WithApprovalsEnforced(enforced))
		label := "enforced=" + boolLabel(enforced)
		listCan, listReason = listSubmitPair(t, store, fx.ctx, fx.invID, label)
		detailCan, detailReason = detailSubmitPair(t, store, fx.ctx, fx.invID, label)
		return
	}

	offList, offReason, offDetail, offDetailReason := pairsFor(t, false)
	onList, onReason, onDetail, onDetailReason := pairsFor(t, true)

	if offList == onList && offReason == onReason {
		t.Fatalf("the list submit pair is %s/%s at BOTH flag settings -- the fixture never armed, so this spec is vacuous", offList, offReason)
	}
	if offList != "true" || offReason != "null" {
		t.Errorf("APPROVALS_ENFORCED off: list can_submit/submit_blocked_reason = %s/%s, want true/null -- the fold makes the approval rung inert", offList, offReason)
	}
	if onList != "false" {
		t.Errorf("APPROVALS_ENFORCED on: list can_submit = %s, want false on an open run under an active policy", onList)
	}
	if want := jsonOf(t, awaitingApprovalReason); onReason != want {
		t.Errorf("APPROVALS_ENFORCED on: list submit_blocked_reason = %s, want %s", onReason, want)
	}
	if offList != offDetail || offReason != offDetailReason {
		t.Errorf("APPROVALS_ENFORCED off: list = %s/%s, detail = %s/%s -- one gate, two wires", offList, offReason, offDetail, offDetailReason)
	}
	if onList != onDetail || onReason != onDetailReason {
		t.Errorf("APPROVALS_ENFORCED on: list = %s/%s, detail = %s/%s -- one gate, two wires", onList, onReason, onDetail, onDetailReason)
	}
}

// --- AC-3: the fold lives in exactly one expression -------------------------

// TestStore_TransmitClearFoldIsTheOnlyReadPathFlagReader pins WHERE the flag is read
// in store.go, not merely that it is read once. Every non-comment s.approvalsEnforced
// line is mapped to its nearest preceding `^func `; the enclosing set must be exactly
// {WithApprovalsEnforced, transmitClear, Transition} -- the option that writes it, the
// ONE fold both read paths share, and the write door.
//
// Three controls run first, because a scan that found nothing satisfies an exact-set
// assertion as easily as a correct one:
//   - at least 3 non-comment hits;
//   - the total hit count exceeds the non-comment count, so the comment filter is
//     exercised by real data (store.go carries one // line naming the field);
//   - the set contains transmitClear, which a scan that matched only comments cannot.
func TestStore_TransmitClearFoldIsTheOnlyReadPathFlagReader(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	funcRe := regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z0-9_]+)`)

	total, nonComment := 0, 0
	enclosing := map[string]bool{}
	for i, line := range lines {
		if !strings.Contains(line, "s.approvalsEnforced") {
			continue
		}
		total++
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		nonComment++
		name := ""
		for j := i; j >= 0; j-- {
			if m := funcRe.FindStringSubmatch(lines[j]); m != nil {
				name = m[1]
				break
			}
		}
		if name == "" {
			t.Fatalf("store.go:%d reads s.approvalsEnforced outside any function -- the scan cannot attribute it", i+1)
		}
		enclosing[name] = true
	}

	if nonComment < 3 {
		t.Fatalf("the scan found %d non-comment s.approvalsEnforced lines in store.go, want >= 3 -- a scan that matches nothing passes the exact-set check below for free", nonComment)
	}
	if total <= nonComment {
		t.Fatalf("the scan found %d s.approvalsEnforced lines and filtered %d as comments, want at least one comment line -- the comment filter is not exercised by real data, so a regression in it would be invisible", total, total-nonComment)
	}
	if !enclosing["transmitClear"] {
		t.Fatalf("the enclosing functions are %v -- none is transmitClear, so the fold is not extracted into the ONE expression both read paths share", sortedSet(enclosing))
	}

	want := []string{"Transition", "WithApprovalsEnforced", "transmitClear"}
	if got := sortedSet(enclosing); !reflect.DeepEqual(got, want) {
		t.Errorf("s.approvalsEnforced is read inside %v, want exactly %v -- ApprovalFacts and RowFacts must both reach the flag through transmitClear, never directly", got, want)
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
