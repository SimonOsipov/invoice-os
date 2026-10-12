package invoice

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// submitFlags is one surface's can_submit verdict for one invoice.
type submitFlags struct {
	can    bool
	reason string // "" when submit_blocked_reason is null
}

func flagsOfRow(t *testing.T, raw map[string]json.RawMessage, where string) submitFlags {
	t.Helper()
	var can bool
	if err := json.Unmarshal(raw["can_submit"], &can); err != nil {
		t.Fatalf("%s: decode can_submit %q: %v", where, raw["can_submit"], err)
	}
	var reason *string
	if err := json.Unmarshal(raw["submit_blocked_reason"], &reason); err != nil {
		t.Fatalf("%s: decode submit_blocked_reason %q: %v", where, raw["submit_blocked_reason"], err)
	}
	out := submitFlags{can: can}
	if reason != nil {
		out.reason = *reason
	}
	return out
}

// listFlags reads every row of the admin's list page, keyed by invoice id.
func (f crFixture) listFlags(t *testing.T) map[string]submitFlags {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil).WithContext(f.admin)
	rec := httptest.NewRecorder()
	ListHandler(f.store.List, f.store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	out := map[string]submitFlags{}
	for _, row := range listRowsRaw(t, rec) {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("decode row id: %v", err)
		}
		out[id] = flagsOfRow(t, row, "list row "+id)
	}
	return out
}

func (f crFixture) detailFlags(t *testing.T, id string) submitFlags {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+id, nil).WithContext(f.admin)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	GetHandler(f.store.Get, f.store.CallerRole, f.store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode detail %q: %v", rec.Body.String(), err)
	}
	return flagsOfRow(t, raw, "detail "+id)
}

// wantSubmit asserts the list row and the detail of id both read want; blocked
// carries ruleChangePendingReason.
func (f crFixture) wantSubmit(t *testing.T, list map[string]submitFlags, id string, want bool, label string) {
	t.Helper()
	row, ok := list[id]
	if !ok {
		t.Fatalf("%s: invoice %s is not on the list page -- the list leg cannot run", label, id)
	}
	for surface, got := range map[string]submitFlags{"list": row, "detail": f.detailFlags(t, id)} {
		if got.can != want {
			t.Errorf("%s (%s): can_submit = %v, want %v", label, surface, got.can, want)
		}
		if !want && got.reason != ruleChangePendingReason {
			t.Errorf("%s (%s): submit_blocked_reason = %q, want ruleChangePendingReason %q", label, surface, got.reason, ruleChangePendingReason)
		}
		if want && got.reason != "" {
			t.Errorf("%s (%s): submit_blocked_reason = %q, want null", label, surface, got.reason)
		}
	}
}

func TestSubmitGate_DB_PendingClientRuleTurnsSubmitOff(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-03 client")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, gate, f.entity, "E15-03-C1", true)

	f.wantSubmit(t, f.listFlags(t), id, true, "before any revision") // control: the invoice is submittable
	f.save(t, &f.entity, "error", true, "email needed")
	f.wantSubmit(t, f.listFlags(t), id, false, "pending error revision on the invoice's client")

	f.must(t, gate) // the invoice passes the rule: the pass marks the revision
	f.wantSubmit(t, f.listFlags(t), id, true, "after the re-check marked the revision")
}

func TestSubmitGate_DB_PendingFirmRuleCoversEveryClient(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-03 firm")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E15-03-F1A", true)
	onB := f.validatedOn(t, gate, b, "E15-03-F1B", true)

	f.wantSubmit(t, f.listFlags(t), onB, true, "control before the firm revision")
	f.save(t, nil, "error", true, "email needed")
	list := f.listFlags(t)
	f.wantSubmit(t, list, onA, false, "firm revision, client A")
	f.wantSubmit(t, list, onB, false, "firm revision, client B")
}

func TestSubmitGate_DB_UncoveredAndMarkedRevisionsLeaveSubmitOn(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-03 uncovered")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E15-03-U1A", true)
	onB := f.validatedOn(t, gate, b, "E15-03-U1B", true)

	f.save(t, &b, "error", true, "pending on B")
	list := f.listFlags(t)
	f.wantSubmit(t, list, onB, false, "control: B is covered by its own pending revision")
	f.wantSubmit(t, list, onA, true, "A, revision pending on B only")

	f.must(t, gate)
	list = f.listFlags(t)
	f.wantSubmit(t, list, onB, true, "B after the re-check marked the revision")
	f.wantSubmit(t, list, onA, true, "A after the re-check")

	f.save(t, &f.entity, "warning", true, "born marked") // a warning never blocks
	f.wantSubmit(t, f.listFlags(t), onA, true, "A, warning revision")
}

// can_submit must equal "the queue door would not refuse with ErrRuleChangePending".
func TestSubmitGate_DB_GateAgreesWithTheQueueDoor(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-03 agree")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	type pair struct{ a, b string }
	mk := func(tag string) pair {
		return pair{
			a: f.validatedOn(t, gate, f.entity, "E15-03-G-"+tag+"A", true),
			b: f.validatedOn(t, gate, b, "E15-03-G-"+tag+"B", true),
		}
	}
	none, bOnly, firm := mk("0"), mk("1"), mk("2")

	var blocked, allowed int
	agree := func(label string, ids ...string) {
		t.Helper()
		list := f.listFlags(t)
		for _, id := range ids {
			row, ok := list[id]
			if !ok {
				t.Fatalf("%s: invoice %s is not on the list page", label, id)
			}
			detail := f.detailFlags(t, id)
			_, err := f.store.Transition(f.member, id, StatusQueued)
			doorRefuses := errors.Is(err, ErrRuleChangePending)
			if err != nil && !doorRefuses {
				t.Fatalf("%s: Transition(queued) of %s = %v, want nil or ErrRuleChangePending", label, id, err)
			}
			if doorRefuses {
				blocked++
			} else {
				allowed++
			}
			if row.can == doorRefuses {
				t.Errorf("%s: list can_submit = %v but the door refusing = %v for %s", label, row.can, doorRefuses, id)
			}
			if detail.can == doorRefuses {
				t.Errorf("%s: detail can_submit = %v but the door refusing = %v for %s", label, detail.can, doorRefuses, id)
			}
		}
	}

	agree("no revision", none.a, none.b)
	f.save(t, &b, "error", true, "pending on B")
	agree("revision pending on B", bOnly.a, bOnly.b)
	f.save(t, nil, "error", true, "firm rule")
	agree("firm revision pending", firm.a, firm.b)

	if blocked == 0 || allowed == 0 {
		t.Fatalf("door refused %d and allowed %d, want both non-zero -- otherwise the agreement proves nothing", blocked, allowed)
	}
}

// Columns: caller role, status, approval clear, rule pending.
func TestSubmitGate_RungOrder(t *testing.T) {
	cases := []struct {
		name          string
		role          string
		status        Status
		approvalClear bool
		rulePending   bool
		wantCan       bool
		wantReason    *string
	}{
		{"non-approver beats pending", "preparer", StatusValidated, true, true, false, strPtr(notApproverTransmitReason)},
		{"draft status beats pending", "admin", StatusDraft, true, true, false, submitBlockedReason(StatusDraft)},
		{"pending beats awaiting approval", "admin", StatusValidated, false, true, false, strPtr(ruleChangePendingReason)},
		{"pending with approval clear", "reviewer", StatusValidated, true, true, false, strPtr(ruleChangePendingReason)},
		{"awaiting approval when not pending", "admin", StatusValidated, false, false, false, strPtr(awaitingApprovalReason)},
		{"nothing in the way", "admin", StatusValidated, true, false, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			can, reason := submitGate(c.status, c.role, c.approvalClear, c.rulePending)
			if can != c.wantCan {
				t.Errorf("can = %v, want %v", can, c.wantCan)
			}
			switch {
			case c.wantReason == nil && reason != nil:
				t.Errorf("reason = %q, want nil", *reason)
			case c.wantReason != nil && reason == nil:
				t.Errorf("reason = nil, want %q", *c.wantReason)
			case c.wantReason != nil && *reason != *c.wantReason:
				t.Errorf("reason = %q, want %q", *reason, *c.wantReason)
			}
		})
	}
}

// One custom_rule_revisions read per RowFacts call, whatever the page size.
func TestStoreRowFacts_RulePendingIsConstantInPageSize(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-03 const")
	tracedApp, rec := tracedAppPool(t)
	entities := []string{f.entity, f.otherEntity(t, "B"), seedEntityWithTIN(t, f.super, f.tenant, "C", "12345680-0003")}
	f.save(t, &entities[1], "error", true, "one pending revision")
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = seedInvoiceAtStatus(t, f.super, f.tenant, entities[i%3], "E15-03-CONST-"+uuid.NewString(), StatusValidated)
	}

	store := NewStore(tracedApp)
	for _, page := range [][]string{ids, ids[:1]} {
		rec.reset()
		_, gate, err := store.RowFacts(f.admin, page)
		if err != nil {
			t.Fatalf("RowFacts of %d: %v", len(page), err)
		}
		if n := len(rec.mentioning("custom_rule_revisions")); n != 1 {
			t.Errorf("page of %d issued %d statements against custom_rule_revisions, want 1", len(page), n)
		}
		if got := gate.RulePending[ids[1]]; !got {
			t.Errorf("page of %d: RulePending[%s] = false, want true -- the invoice on the covered entity", len(page), ids[1])
		}
		if gate.RulePending[ids[0]] || gate.RulePending[ids[2]] {
			t.Errorf("page of %d: RulePending marks an uncovered entity's invoice", len(page))
		}
	}
}

func TestSubmitGate_DB_NeverSeesAnotherTenantsPendingRevision(t *testing.T) {
	f1 := newCRFixture(t, "ENGI-15-03 xt t1")
	f2 := newCRFixture(t, "ENGI-15-03 xt t2")
	b1 := f1.otherEntity(t, "client B")
	gate1 := f1.gate(t, start04Wrapped(t, f1.app, nil, nil))
	gate2 := f2.gate(t, start04Wrapped(t, f2.app, nil, nil))
	t1A := f1.validatedOn(t, gate1, f1.entity, "E15-03-XT1A", true)
	t1B := f1.validatedOn(t, gate1, b1, "E15-03-XT1B", true)
	t2A := f2.validatedOn(t, gate2, f2.entity, "E15-03-XT2A", true)

	f2.save(t, nil, "error", true, "tenant 2 firm rule")
	f1.save(t, &b1, "error", true, "tenant 1 rule on B")

	list1 := f1.listFlags(t)
	f1.wantSubmit(t, list1, t1B, false, "control: tenant 1's own pending revision on B")
	f1.wantSubmit(t, list1, t1A, true, "tenant 1 client A, tenant 2 firm revision pending")
	f2.wantSubmit(t, f2.listFlags(t), t2A, false, "control: tenant 2's own firm revision")
}
