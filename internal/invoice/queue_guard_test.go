package invoice

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func (f crFixture) historyRows(t *testing.T, id string) int {
	t.Helper()
	return mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'queued'`, id)
}

func TestQueueGuard_PendingClientRuleRefusesTransition(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard client")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, gate, f.entity, "E14-06-QG1", true)
	f.save(t, &f.entity, "error", true, "email needed")

	if _, err := f.store.Transition(f.member, id, StatusQueued); !errors.Is(err, ErrRuleChangePending) {
		t.Fatalf("Transition(queued) = %v, want ErrRuleChangePending", err)
	}
	if f.status(t, id) != StatusValidated || f.historyRows(t, id) != 0 {
		t.Errorf("status %s with %d queued history rows, want validated and none", f.status(t, id), f.historyRows(t, id))
	}
	if code, msg := statusForErr(ErrRuleChangePending); code != http.StatusConflict || msg != ruleChangePendingReason {
		t.Errorf("statusForErr = %d %q, want 409 %q", code, msg, ruleChangePendingReason)
	}
}

func TestQueueGuard_PendingFirmRuleCoversEveryClient(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard firm")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E14-06-QG2A", true)
	onB := f.validatedOn(t, gate, b, "E14-06-QG2B", true)
	f.save(t, nil, "error", true, "email needed")

	for _, id := range []string{onA, onB} {
		if _, err := f.store.Transition(f.member, id, StatusQueued); !errors.Is(err, ErrRuleChangePending) {
			t.Errorf("Transition(queued) of %s = %v, want ErrRuleChangePending", id, err)
		}
	}
}

func TestQueueGuard_OtherClientAndMarkedRevisionsQueue(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard other")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E14-06-QG3A", true)
	onA2 := f.validatedOn(t, gate, f.entity, "E14-06-QG3A2", true)
	onA3 := f.validatedOn(t, gate, f.entity, "E14-06-QG3A3", true)

	f.save(t, &b, "error", true, "pending on B")
	if _, err := f.store.Transition(f.member, onA, StatusQueued); err != nil {
		t.Errorf("A's invoice with a pending revision on B: %v, want queued", err)
	}
	f.save(t, &f.entity, "warning", true, "born marked")
	if _, err := f.store.Transition(f.member, onA2, StatusQueued); err != nil {
		t.Errorf("A's invoice with a warning revision: %v, want queued", err)
	}

	f.save(t, &f.entity, "error", true, "pending on A")
	if _, err := f.store.Transition(f.member, onA3, StatusQueued); !errors.Is(err, ErrRuleChangePending) {
		t.Fatalf("with a pending revision on A: %v, want ErrRuleChangePending", err)
	}
	f.must(t, gate) // the invoice passes the rule: it stays validated and the revision is marked
	if _, err := f.store.Transition(f.member, onA3, StatusQueued); err != nil {
		t.Errorf("after the pass marked the revision: %v, want queued", err)
	}
}

func TestQueueGuard_BatchSubmitSkipsWithRuleChangePending(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard batch")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E14-06-QG4A", true)
	onB := f.validatedOn(t, gate, b, "E14-06-QG4B", true)
	f.save(t, &f.entity, "error", true, "email needed")

	res, err := gateSubmitter(t, f.app).BatchSubmit(f.member, BatchSubmitInput{InvoiceIDs: []string{onA, onB}, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatalf("BatchSubmit: %v", err)
	}
	got := map[string]BatchSubmitResultItem{}
	for _, it := range res.Results {
		got[it.InvoiceID] = it
	}
	if a := got[onA]; a.Enqueued || a.Reason != "rule_change_pending" {
		t.Errorf("covered invoice = %+v, want skipped with rule_change_pending", a)
	}
	if b := got[onB]; !b.Enqueued {
		t.Errorf("uncovered invoice = %+v, want enqueued", b)
	}
	if f.status(t, onA) != StatusValidated || f.status(t, onB) != StatusQueued {
		t.Errorf("statuses = %s / %s, want validated / queued", f.status(t, onA), f.status(t, onB))
	}
}

func TestQueueGuard_NeverSeesAnotherTenantsPendingRevision(t *testing.T) {
	f1 := newCRFixture(t, "ENGI-14-06 guard t1")
	f2 := newCRFixture(t, "ENGI-14-06 guard t2")
	id := f1.validatedOn(t, f1.gate(t, start04Wrapped(t, f1.app, nil, nil)), f1.entity, "E14-06-QG5", true)
	f2.save(t, nil, "error", true, "their firm rule")

	if _, err := f1.store.Transition(f1.member, id, StatusQueued); err != nil {
		t.Errorf("Transition(queued) = %v, want it unaffected by tenant 2", err)
	}
}

func TestQueueGuard_BatchPendingReadIsConstantInBatchSize(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard const")
	tracedApp, rec := tracedAppPool(t)
	entities := []string{f.entity, f.otherEntity(t, "B"), seedEntityWithTIN(t, f.super, f.tenant, "C", "12345680-0003")}
	f.save(t, &entities[1], "error", true, "one pending revision")
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = seedInvoiceAtStatus(t, f.super, f.tenant, entities[i%3], "E14-06-QG6-"+uuid.NewString(), StatusValidated)
	}

	for _, batch := range [][]string{ids, ids[:1]} {
		rec.reset()
		if _, err := gateSubmitter(t, tracedApp).BatchSubmit(f.member, BatchSubmitInput{InvoiceIDs: batch, IdempotencyKey: uuid.NewString()}); err != nil {
			t.Fatalf("BatchSubmit of %d: %v", len(batch), err)
		}
		if n := len(rec.mentioning("custom_rule_revisions")); n != 1 {
			t.Errorf("batch of %d issued %d statements against custom_rule_revisions, want 1", len(batch), n)
		}
	}
}

func TestQueueGuard_DisabledAndDeletedRevisionsBlockNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 guard born marked")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = f.validatedOn(t, gate, f.entity, "E14-06-QG7-"+uuid.NewString(), true)
	}
	disabled := f.save(t, &f.entity, "error", false, "disabled from birth")
	if _, err := f.store.Transition(f.member, ids[0], StatusQueued); err != nil {
		t.Errorf("with a disabled revision: %v, want queued", err)
	}
	deleted := f.save(t, &f.entity, "error", false, "deleted soon")
	if err := f.rules.Delete(f.admin, deleted.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if f.pending(t, disabled.RevisionID) || f.pending(t, deleted.RevisionID) {
		t.Fatalf("a disabled or deleted revision is pending")
	}
	if _, err := f.store.Transition(f.member, ids[1], StatusQueued); err != nil {
		t.Errorf("with a deleted rule's revisions: %v, want queued", err)
	}
	f.save(t, &f.entity, "error", true, "enabled error")
	if _, err := f.store.Transition(f.member, ids[2], StatusQueued); !errors.Is(err, ErrRuleChangePending) {
		t.Errorf("with an enabled error revision: %v, want ErrRuleChangePending", err)
	}
}

func TestQueueGuard_BatchFirmRevisionSkipsEveryClientOfItsTenantOnly(t *testing.T) {
	f1 := newCRFixture(t, "ENGI-14-06 guard batch firm t1")
	f2 := newCRFixture(t, "ENGI-14-06 guard batch firm t2")
	b := f1.otherEntity(t, "client B")
	gate := f1.gate(t, start04Wrapped(t, f1.app, nil, nil))
	onA := f1.validatedOn(t, gate, f1.entity, "E14-06-QG8A", true)
	onB := f1.validatedOn(t, gate, b, "E14-06-QG8B", true)
	other := f2.validatedOn(t, f2.gate(t, start04Wrapped(t, f2.app, nil, nil)), f2.entity, "E14-06-QG8T2", true)
	f1.save(t, nil, "error", true, "firm rule")

	res, err := gateSubmitter(t, f1.app).BatchSubmit(f1.member, BatchSubmitInput{InvoiceIDs: []string{onA, onB}, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatalf("BatchSubmit: %v", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("%d results, want 2", len(res.Results))
	}
	for _, it := range res.Results {
		if it.Enqueued || it.Reason != "rule_change_pending" {
			t.Errorf("%+v, want skipped with rule_change_pending", it)
		}
	}
	res, err = gateSubmitter(t, f2.app).BatchSubmit(f2.member, BatchSubmitInput{InvoiceIDs: []string{other}, IdempotencyKey: uuid.NewString()})
	if err != nil || len(res.Results) != 1 || !res.Results[0].Enqueued {
		t.Errorf("tenant 2 batch = %+v, %v, want enqueued despite tenant 1's firm revision", res, err)
	}

	f1.must(t, gate)
	res, err = gateSubmitter(t, f1.app).BatchSubmit(f1.member, BatchSubmitInput{InvoiceIDs: []string{onA, onB}, IdempotencyKey: uuid.NewString()})
	if err != nil || len(res.Results) != 2 || !res.Results[0].Enqueued || !res.Results[1].Enqueued {
		t.Errorf("after the pass marked the revision: %+v, %v, want both enqueued", res, err)
	}
}
