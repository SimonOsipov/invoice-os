// QA adversarial cover for the list wire's submit gate: the nil TransmitClear
// map, the role rung no DB-backed leg in submit_gate_list_test.go reaches, and
// the two cross-tenant claims the fold makes.
//
// Run: DEV_DB_PORT=5437 make -s test-invoice
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- helpers ----------------------------------------------------------------

// transmitGateStub is gateStub's twin for specs that must SET TransmitClear.
// gateStub leaves it nil on purpose, which is the fail-closed path pinned below.
func transmitGateStub(callerRole string, transmit map[string]bool) func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
	return func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return map[string]approval.RowFacts{}, ListGateFacts{CallerRole: callerRole, TransmitClear: transmit}, nil
	}
}

// listPageRaw drives the REAL ListHandler and returns the whole page undecoded.
func listPageRaw(t *testing.T, store *Store, ctx context.Context, label string) []map[string]json.RawMessage {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: LIST status = %d, want 200 (body=%s)", label, rec.Code, rec.Body.String())
	}
	return listRowsRaw(t, rec)
}

// rowIDs pulls the decoded ids off a page, sorted, so a set comparison is stable.
func rowIDs(t *testing.T, rows []map[string]json.RawMessage, label string) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for i, row := range rows {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("%s: decode row %d id: %v", label, i, err)
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// --- the nil TransmitClear map ----------------------------------------------

// TestListHandler_NilTransmitClearFailsClosedForAnApprover pins the posture of a
// ListGateFacts whose map was never populated. gateStub (list_approve_gate_test.go)
// leaves it nil, and every ListGateFacts{} literal in this package does too, so this
// is the value most of the suite actually runs on.
//
// The caller is a reviewer, which CLEARS the role rung: the nil map, not the role,
// decides the validated rows. TestListHandler_NoActionFlagKeys cannot make this
// claim -- its zero ListGateFacts stops at rung 1 and never consults the map.
func TestListHandler_NilTransmitClearFailsClosedForAnApprover(t *testing.T) {
	id := listIdentity()
	mapped, absent, posted := uuid.NewString(), uuid.NewString(), uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: mapped, Status: StatusValidated},
			{ID: absent, Status: StatusValidated},
			{ID: posted, Status: StatusAccepted},
		}, 3, nil
	}

	nilRows := listRowsRaw(t, doInvoiceListWithFacts(t, list, gateStub(listGateInputs{
		facts:      map[string]approval.RowFacts{},
		callerRole: "reviewer",
	}), &id, ""))
	if len(nilRows) != 3 {
		t.Fatalf("len(invoices) = %d, want 3 -- the row-by-row claims below are vacuous on any other page", len(nilRows))
	}
	wantAwaiting := jsonOf(t, awaitingApprovalReason)
	for i := 0; i < 2; i++ {
		gotCan, gotReason := submitFlagsOf(t, nilRows[i], "a validated row under a nil TransmitClear")
		if gotCan != "false" {
			t.Errorf("row %d can_submit = %s, want false -- a nil map reads false for every id, which is the fail-closed posture ListGateFacts documents", i, gotCan)
		}
		if gotReason != wantAwaiting {
			t.Errorf("row %d submit_blocked_reason = %s, want %s", i, gotReason, wantAwaiting)
		}
	}
	// The ladder still ran: a post-submission row is refused SILENTLY, so the
	// sentence above is rung 3's answer rather than a constant.
	postCan, postReason := submitFlagsOf(t, nilRows[2], "an accepted row under a nil TransmitClear")
	if postCan != "false" || postReason != "null" {
		t.Errorf("the accepted row reads %s/%s, want false/null", postCan, postReason)
	}

	// DISCRIMINATOR: the same caller and the same three statuses, with ONE id
	// mapped true. Only the map differs, so a hardcoded false reds here and a
	// hardcoded true reds above.
	setRows := listRowsRaw(t, doInvoiceListWithFacts(t, list, transmitGateStub("reviewer", map[string]bool{mapped: true}), &id, ""))
	if len(setRows) != 3 {
		t.Fatalf("len(invoices) = %d, want 3", len(setRows))
	}
	openCan, openReason := submitFlagsOf(t, setRows[0], "the mapped validated row")
	if openCan != "true" || openReason != "null" {
		t.Errorf("the mapped row reads %s/%s, want true/null -- a populated map must be able to open the gate, or the assertions above pass on a wire that ignores it", openCan, openReason)
	}
	absentCan, absentReason := submitFlagsOf(t, setRows[1], "the validated row absent from a populated map")
	if absentCan != "false" || absentReason != wantAwaiting {
		t.Errorf("the row absent from a populated map reads %s/%s, want false/%s -- an absent key is not permissive", absentCan, absentReason, wantAwaiting)
	}
}

// --- the role rung, DB-backed -----------------------------------------------

// TestListAndDetail_SubmitGateAgreeOnTheRoleRung closes the gap
// TestListAndDetail_SubmitGateCannotDisagree names: both of its tenants seed an
// admin caller, so no DB-backed leg reaches submitGate's FIRST rung. A sentence the
// wire can emit that no DB-backed spec reaches is how an unmapped reason ships blank.
//
// The rung ORDER is the claim: a preparer reads the same sentence on a validated
// invoice (where an approver reads true) and on an accepted one (where an approver
// reads a silent false).
func TestListAndDetail_SubmitGateAgreeOnTheRoleRung(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "BUG-12-01-QA-ROLE")
	entityID := seedEntity(t, super, tenantID, "BUG-12-01 QA Role Corp")
	validatedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "bug-12-01-qa-role-validated", StatusValidated)
	acceptedID := seedInvoiceAtStatus(t, super, tenantID, entityID, "bug-12-01-qa-role-accepted", StatusAccepted)

	ctxFor := func(role string) context.Context {
		subject := uuid.NewString()
		seedMembership(t, super, tenantID, subject, role)
		return auth.WithIdentity(context.Background(), auth.Identity{
			Subject: subject, Role: "authenticated", TenantID: tenantID,
		})
	}
	preparerCtx, adminCtx := ctxFor("preparer"), ctxFor("admin")

	store := NewStore(app, WithApprovalsEnforced(true))

	// CONTROL: the SAME two invoices read by an admin answer two DIFFERENT things.
	// Without it a page that refused everything would satisfy the role claim below.
	for _, c := range []struct{ id, wantCan, wantReason string }{
		{validatedID, "true", "null"},
		{acceptedID, "false", "null"},
	} {
		gotCan, gotReason := listSubmitPair(t, store, adminCtx, c.id, "admin")
		if gotCan != c.wantCan || gotReason != c.wantReason {
			t.Fatalf("an admin reads %s/%s for %s, want %s/%s -- the control leg is broken, so the preparer assertions below prove nothing", gotCan, gotReason, c.id, c.wantCan, c.wantReason)
		}
	}

	want := jsonOf(t, notApproverTransmitReason)
	for _, id := range []string{validatedID, acceptedID} {
		listCan, listReason := listSubmitPair(t, store, preparerCtx, id, "preparer")
		detailCan, detailReason := detailSubmitPair(t, store, preparerCtx, id, "preparer")
		if listCan != detailCan || listReason != detailReason {
			t.Errorf("%s: list = %s/%s, detail = %s/%s -- one submitGate call feeds both wires, including on its first rung", id, listCan, listReason, detailCan, detailReason)
		}
		if listCan != "false" {
			t.Errorf("%s: can_submit = %s, want false for a preparer", id, listCan)
		}
		if listReason != want {
			t.Errorf("%s: submit_blocked_reason = %s, want %s -- the role rung outranks status", id, listReason, want)
		}
	}
}

// --- the cross-tenant fold --------------------------------------------------

// TestStoreRowFacts_TransmitClearFailsClosedForAnIdRLSCannotSee is the oracle
// .ralph/bug-12-01-plan.md's T3 records as owed to BUG-12-04. It is reachable at the
// STORE seam, which is where C6's contract lives: RowFacts folds over every REQUESTED
// id, and TransmitClearTx's set read carries no tenant predicate, so a foreign id
// returns no row and is ABSENT from its map.
//
// Both tenants publish an active policy, which is what keeps TransmitClearTx off its
// short-circuit -- the short-circuit maps every requested id and would hide the claim.
func TestStoreRowFacts_TransmitClearFailsClosedForAnIdRLSCannotSee(t *testing.T) {
	super, app := dbTestPools(t)

	a := seedApprovalFactsFixture(t, super, "BUG-12-01-QA-RLS-A", true)
	b := seedApprovalFactsFixture(t, super, "BUG-12-01-QA-RLS-B", true)
	ids := []string{a.invID, b.invID}

	on := NewStore(app, WithApprovalsEnforced(true))
	facts, gate, err := on.RowFacts(a.ctx, ids)
	if err != nil {
		t.Fatalf("RowFacts (flag on): %v", err)
	}

	// CONTROL 1: RLS really hides B from A, so the fold really is being asked
	// about an id its set read could not see.
	if _, ok := facts[b.invID]; ok {
		t.Fatalf("RowFacts returned visibility facts for tenant B's invoice %s under tenant A's identity -- RLS is not scoping this read", b.invID)
	}
	// CONTROL 2: A really has an active policy, so TransmitClearTx took its
	// two-statement path. On the short-circuit every requested id maps true and an
	// absent id would read clear for free.
	if gate.TransmitClear[a.invID] {
		t.Fatalf("tenant A's own unarmed validated invoice reads clear under APPROVALS_ENFORCED -- the no-policy short-circuit fired")
	}

	if len(gate.TransmitClear) != len(ids) {
		t.Fatalf("TransmitClear has %d entries for %d requested ids (%v) -- the fold must cover every REQUESTED id", len(gate.TransmitClear), len(ids), gate.TransmitClear)
	}
	got, ok := gate.TransmitClear[b.invID]
	if !ok {
		t.Fatalf("TransmitClear has no entry for the invisible id %s -- a Go map read of an absent key is false, but only a PRESENT false survives a caller that ranges the map", b.invID)
	}
	if got {
		t.Errorf("TransmitClear[%s] = true for an id RLS cannot see -- an unreadable invoice must fail CLOSED", b.invID)
	}

	// The flag-off half of C6: the detail wire folds to true for ANY id when the
	// flag is off, so the list cannot answer false at the same setting.
	off := NewStore(app)
	_, offGate, err := off.RowFacts(a.ctx, ids)
	if err != nil {
		t.Fatalf("RowFacts (flag off): %v", err)
	}
	offGot, ok := offGate.TransmitClear[b.invID]
	if !ok {
		t.Fatalf("APPROVALS_ENFORCED off: TransmitClear has no entry for %s", b.invID)
	}
	if !offGot {
		t.Errorf("APPROVALS_ENFORCED off: TransmitClear[%s] = false, want true -- the detail wire answers true for any id here, and the two surfaces cannot differ at one setting", b.invID)
	}
	if offGot == got {
		t.Errorf("TransmitClear[%s] is %v at BOTH flag settings -- the fold is not reading the flag for an absent id", b.invID, got)
	}
}

// TestListHandler_SubmitGateDoesNotCrossTenants reads two tenants whose pages must
// answer OPPOSITE verdicts, through one store, and pins that neither page carries the
// other's rows or the other's answer.
//
// The two pages carry DIFFERENT row counts on purpose: with equal counts a map keyed
// by position rather than by id, or one shared between the two reads, could still
// line up ([a-tie-cannot-discriminate-two-orderings]).
func TestListHandler_SubmitGateDoesNotCrossTenants(t *testing.T) {
	super, app := dbTestPools(t)

	// Tenant A: no policy, THREE validated rows, so every row reads clear.
	aTenant := seedTenant(t, super, "BUG-12-01-QA-XT-A")
	aEntity := seedEntity(t, super, aTenant, "BUG-12-01 QA XT A Corp")
	aSubject := uuid.NewString()
	seedMembership(t, super, aTenant, aSubject, "admin")
	aCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: aSubject, Role: "authenticated", TenantID: aTenant,
	})
	aIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		aIDs = append(aIDs, seedInvoiceAtStatus(t, super, aTenant, aEntity,
			"bug-12-01-qa-xt-a-"+string(rune('1'+i)), StatusValidated))
	}
	sort.Strings(aIDs)

	// Tenant B: an ACTIVE policy and no approved run, TWO validated rows, so every
	// row reads blocked under the same store.
	b := seedApprovalFactsFixture(t, super, "BUG-12-01-QA-XT-B", true)
	bIDs := []string{b.invID, seedInvoiceAtStatus(t, super, b.tenantID, b.entityID, "bug-12-01-qa-xt-b-2", StatusValidated)}
	sort.Strings(bIDs)

	store := NewStore(app, WithApprovalsEnforced(true))

	aRows := listPageRaw(t, store, aCtx, "tenant A")
	bRows := listPageRaw(t, store, b.ctx, "tenant B")
	if len(aRows) != 3 || len(bRows) != 2 {
		t.Fatalf("tenant A's page has %d rows and tenant B's has %d, want 3 and 2 -- unequal, non-empty pages are what make the id and verdict claims below discriminating", len(aRows), len(bRows))
	}
	if got := rowIDs(t, aRows, "tenant A"); !equalStrings(got, aIDs) {
		t.Fatalf("tenant A's page carries %v, want %v -- a foreign or missing row makes every verdict below unattributable", got, aIDs)
	}
	if got := rowIDs(t, bRows, "tenant B"); !equalStrings(got, bIDs) {
		t.Fatalf("tenant B's page carries %v, want %v", got, bIDs)
	}

	for i, row := range aRows {
		gotCan, gotReason := submitFlagsOf(t, row, "tenant A")
		if gotCan != "true" || gotReason != "null" {
			t.Errorf("tenant A row %d reads %s/%s, want true/null -- A publishes no policy, so its gate cannot be answered from B's map", i, gotCan, gotReason)
		}
	}
	want := jsonOf(t, awaitingApprovalReason)
	for i, row := range bRows {
		gotCan, gotReason := submitFlagsOf(t, row, "tenant B")
		if gotCan != "false" || gotReason != want {
			t.Errorf("tenant B row %d reads %s/%s, want false/%s -- B's active policy with no approved run blocks every row, and A's clear verdict must not reach it", i, gotCan, gotReason, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
