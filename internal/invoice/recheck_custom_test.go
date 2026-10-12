package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
)

// validatedOn creates an invoice on entity and validates it (no pending rule exists yet).
// withEmail gives it a buyer email, so a required buyer.email rule passes it.
func (f crFixture) validatedOn(t *testing.T, gate *Gate, entity, number string, withEmail bool) string {
	t.Helper()
	in := gapiValidInvoiceInput(entity, number)
	if withEmail {
		in.BuyerEmail = strPtr("buyer@example.com")
	}
	inv, err := f.store.Create(f.member, in)
	if err != nil {
		t.Fatalf("Create %s: %v", number, err)
	}
	got, _, err := gate.Validate(f.member, inv.ID)
	if err != nil || got.Status != StatusValidated {
		t.Fatalf("validate %s = %q, %v, want validated", number, got.Status, err)
	}
	return inv.ID
}

func (f crFixture) recheck(gate *Gate) (RevalidateResult, error) {
	return RecheckCustomRules(context.Background(), f.app, f.store, gate, f.tenant)
}

func (f crFixture) must(t *testing.T, gate *Gate) RevalidateResult {
	t.Helper()
	res, err := f.recheck(gate)
	if err != nil {
		t.Fatalf("RecheckCustomRules: %v", err)
	}
	return res
}

func (f crFixture) otherEntity(t *testing.T, name string) string {
	t.Helper()
	return seedEntityWithTIN(t, f.super, f.tenant, name, "12345679-0002")
}

// edit appends a revision of rule r (same target) with the given scope, severity and enabled flag.
func (f crFixture) edit(t *testing.T, r customrule.Rule, entity *string, severity string, enabled bool) customrule.Rule {
	t.Helper()
	out, err := f.rules.Update(f.admin, r.ID, customrule.RuleInput{EntityID: entity, Type: r.Type, Target: r.Target,
		Params: r.Params, Severity: severity, Message: r.Message, Enabled: enabled})
	if err != nil {
		t.Fatalf("edit rule: %v", err)
	}
	return out
}

func (f crFixture) status(t *testing.T, id string) Status { return statusOf(t, f.super, id) }

// causes returns the two cause columns of the invoice's validated->draft history row.
func (f crFixture) causes(t *testing.T, id string) (version, revision *string) {
	t.Helper()
	if err := f.super.QueryRow(context.Background(),
		`SELECT cause_rule_set_version_id::text, cause_custom_rule_revision_id::text FROM invoice_status_history
		  WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, id).Scan(&version, &revision); err != nil {
		t.Fatalf("read demotion history of %s: %v", id, err)
	}
	return version, revision
}

func (f crFixture) pending(t *testing.T, revisionID string) bool {
	t.Helper()
	var p bool
	if err := f.super.QueryRow(context.Background(),
		`SELECT rechecked_at IS NULL FROM custom_rule_revisions WHERE id = $1`, revisionID).Scan(&p); err != nil {
		t.Fatalf("read rechecked_at: %v", err)
	}
	return p
}

func (f crFixture) rsv(t *testing.T, id string) *string {
	t.Helper()
	var v *string
	if err := f.super.QueryRow(context.Background(), `SELECT rule_set_version_id::text FROM invoices WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read rule_set_version_id: %v", err)
	}
	return v
}

func TestRecheckCustomRules_DemotesCoveredInvoicesThatNowFail(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 demote")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	failing := f.validatedOn(t, gate, f.entity, "E14-06-FAIL", false)
	passing := f.validatedOn(t, gate, f.entity, "E14-06-PASS", true)
	stamp := f.rsv(t, passing)
	rule := f.save(t, &f.entity, "error", true, "email needed")

	res := f.must(t, gate)
	if f.status(t, failing) != StatusDraft || f.status(t, passing) != StatusValidated {
		t.Errorf("statuses = %s / %s, want draft / validated", f.status(t, failing), f.status(t, passing))
	}
	if res.Pending != 1 || res.Examined != 2 || res.Demoted != 1 || res.Clean != 1 {
		t.Errorf("result = %+v, want pending 1, examined 2, demoted 1, clean 1", res)
	}
	var vs []Violation
	var raw []byte
	_ = f.super.QueryRow(context.Background(), `SELECT violations FROM invoices WHERE id = $1`, failing).Scan(&raw)
	_ = json.Unmarshal(raw, &vs)
	if _, ok := violationOf(vs, customrule.Key(rule.ID)); !ok {
		t.Errorf("demoted violations = %s, want the custom rule's", raw)
	}
	if got := f.rsv(t, passing); got == nil || stamp == nil || *got != *stamp {
		t.Errorf("clean invoice rule_set_version_id = %v, want its old stamp %v", got, stamp)
	}
}

func TestRecheckCustomRules_ClientRuleTouchesOnlyItsClient(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 client")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E14-06-A", false)
	onB := f.validatedOn(t, gate, b, "E14-06-B", false)
	f.save(t, &f.entity, "error", true, "email needed")

	f.must(t, gate)
	if f.status(t, onA) != StatusDraft || f.status(t, onB) != StatusValidated {
		t.Errorf("statuses A/B = %s / %s, want draft / validated", f.status(t, onA), f.status(t, onB))
	}
}

func TestRecheckCustomRules_FirmRuleTouchesEveryClient(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 firm")
	b := f.otherEntity(t, "client B")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	onA := f.validatedOn(t, gate, f.entity, "E14-06-FA", false)
	onB := f.validatedOn(t, gate, b, "E14-06-FB", false)
	f.save(t, nil, "error", true, "email needed")

	f.must(t, gate)
	if f.status(t, onA) != StatusDraft || f.status(t, onB) != StatusDraft {
		t.Errorf("statuses A/B = %s / %s, want both draft", f.status(t, onA), f.status(t, onB))
	}
}

func TestRecheckCustomRules_IgnoresNonValidatedStatuses(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 statuses")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	ids := map[Status]string{}
	for _, s := range []Status{StatusDraft, StatusQueued, StatusSubmitted} {
		ids[s] = seedInvoiceAtStatus(t, f.super, f.tenant, f.entity, "E14-06-"+string(s), s)
	}
	f.save(t, nil, "error", true, "email needed")

	res := f.must(t, gate)
	for s, id := range ids {
		if got := f.status(t, id); got != s {
			t.Errorf("%s invoice is now %s, want unchanged", s, got)
		}
	}
	if res.Examined != 0 {
		t.Errorf("examined = %d, want 0", res.Examined)
	}
}

func TestRecheckCustomRules_HistoryNamesTheBlockingRevision(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 cause")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, gate, f.entity, "E14-06-CAUSE", true)
	f.save(t, &f.entity, "error", true, "email needed") // R1: passes (the invoice has an email)
	r2, err := f.rules.Create(f.admin, customrule.RuleInput{EntityID: &f.entity, Type: "required", Target: "buyer.telephone",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "phone needed", Enabled: true})
	if err != nil {
		t.Fatalf("save R2: %v", err)
	}

	f.must(t, gate)
	version, revision := f.causes(t, id)
	if version != nil || revision == nil || *revision != r2.RevisionID {
		t.Errorf("causes = version %v revision %v, want NULL and %s", version, revision, r2.RevisionID)
	}
}

func TestRecheckCustomRules_GoldenOnlyBlockHasNoCustomCause(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 golden")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, gate, f.entity, "E14-06-GOLD", true)
	rule := f.save(t, &f.entity, "warning", true, "email wanted")
	f.edit(t, rule, &f.entity, "error", true) // pending; the invoice passes it
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET vat = '1.00' WHERE id = $1`, id); err != nil {
		t.Fatalf("break a golden rule: %v", err)
	}

	f.must(t, gate)
	if f.status(t, id) != StatusDraft {
		t.Fatalf("status = %s, want draft", f.status(t, id))
	}
	if version, revision := f.causes(t, id); version != nil || revision != nil {
		t.Errorf("causes = %v / %v, want both NULL", version, revision)
	}
}

func TestRecheckCustomRules_MarksExactlyWhatItRead(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 mark")
	plain := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	f.validatedOn(t, plain, f.entity, "E14-06-MARK", true)
	r1 := f.save(t, &f.entity, "error", true, "first")
	var r2 customrule.Rule
	gate := f.gate(t, start04Wrapped(t, f.app, func(call int) {
		if call == 1 {
			r2 = f.save(t, &f.entity, "error", true, "saved during the pass")
		}
	}, nil))

	f.must(t, gate)
	if f.pending(t, r1.RevisionID) {
		t.Errorf("R1 still pending after the pass")
	}
	if !f.pending(t, r2.RevisionID) {
		t.Errorf("R2, saved during the pass, was marked")
	}
}

func TestRecheckCustomRules_NothingPendingCallsNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 idle")
	srv := start04Wrapped(t, f.app, nil, nil)
	gate := f.gate(t, srv)
	f.save(t, nil, "warning", true, "born marked")

	res := f.must(t, gate)
	if res.Pending != 0 || res.Examined != 0 || srv.calls() != 0 {
		t.Errorf("result = %+v with %d validation calls, want zero", res, srv.calls())
	}
}

func TestRecheckCustomRules_OutageMarksNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 outage")
	plain := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, plain, f.entity, "E14-06-OUT", false)
	rule := f.save(t, &f.entity, "error", true, "email needed")

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	if _, err := f.recheck(NewGate(f.store, NewValidator(down.URL, gapiS2SToken, nil))); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if !f.pending(t, rule.RevisionID) || f.status(t, id) != StatusValidated {
		t.Fatalf("after the outage: pending %v status %s, want pending and validated", f.pending(t, rule.RevisionID), f.status(t, id))
	}

	f.must(t, plain)
	if f.pending(t, rule.RevisionID) || f.status(t, id) != StatusDraft {
		t.Errorf("after the repeat: pending %v status %s, want marked and draft", f.pending(t, rule.RevisionID), f.status(t, id))
	}
}

func TestRechecker_CustomOneTenantFailingDoesNotStopOthers(t *testing.T) {
	var seen []string
	r := &Rechecker{
		Tenants: func(context.Context) ([]string, error) { return []string{"t1", "t2", "t3"}, nil },
		recheckCustom: func(_ context.Context, tenantID string) (RevalidateResult, error) {
			seen = append(seen, tenantID)
			if tenantID == "t1" {
				return RevalidateResult{}, errors.New("boom")
			}
			return RevalidateResult{Pending: 1}, nil
		},
	}
	err := r.RunCustomOnce(context.Background())
	if len(seen) != 3 {
		t.Errorf("tenants run = %v, want all three", seen)
	}
	if err == nil || !strings.Contains(err.Error(), "t1") || strings.Contains(err.Error(), "t2") {
		t.Errorf("err = %v, want it to name t1 only", err)
	}
}

func TestRechecker_DB_CustomSaveDemotesOnTheNextTick(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 tick")
	srv := start04Wrapped(t, f.app, nil, nil)
	gate := f.gate(t, srv)
	id := f.validatedOn(t, gate, f.entity, "E14-06-TICK", false)
	f.save(t, &f.entity, "error", true, "email needed")
	r := &Rechecker{Pool: f.app, Store: f.store, Gate: gate,
		Tenants: func(context.Context) ([]string, error) { return []string{f.tenant}, nil }}

	if err := r.RunCustomOnce(context.Background()); err != nil {
		t.Fatalf("RunCustomOnce: %v", err)
	}
	if f.status(t, id) != StatusDraft {
		t.Fatalf("status = %s, want draft", f.status(t, id))
	}
	before := srv.calls()
	if err := r.RunCustomOnce(context.Background()); err != nil {
		t.Fatalf("second RunCustomOnce: %v", err)
	}
	if srv.calls() != before {
		t.Errorf("second tick called validation %d more time(s), want none", srv.calls()-before)
	}
}

func TestRecheckCustomRules_NeverTouchesAnotherTenant(t *testing.T) {
	f1 := newCRFixture(t, "ENGI-14-06 tenant 1")
	f2 := newCRFixture(t, "ENGI-14-06 tenant 2")
	gate2 := f2.gate(t, start04Wrapped(t, f2.app, nil, nil))
	other := f2.validatedOn(t, gate2, f2.entity, "E14-06-T2", false)
	rule := f2.save(t, nil, "error", true, "email needed")

	f1.must(t, f1.gate(t, start04Wrapped(t, f1.app, nil, nil)))
	if f2.status(t, other) != StatusValidated || !f2.pending(t, rule.RevisionID) {
		t.Errorf("tenant 2 changed by tenant 1's pass: status %s pending %v", f2.status(t, other), f2.pending(t, rule.RevisionID))
	}
}

func TestRecheckCustomRules_RefusesAPrivilegedRole(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 privileged")
	srv := start04Wrapped(t, f.app, nil, nil)
	superStore := NewStore(f.super)
	_, err := RecheckCustomRules(context.Background(), f.super, superStore, NewGate(superStore, NewValidator(srv.URL, gapiS2SToken, nil)), f.tenant)
	if !errors.Is(err, ErrRevalidatePrivilegedRole) || srv.calls() != 0 {
		t.Errorf("err = %v with %d calls, want ErrRevalidatePrivilegedRole and none", err, srv.calls())
	}
}

func TestRecheckCustomRules_DemotionAuditCarriesTheRevisions(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 audit")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.validatedOn(t, gate, f.entity, "E14-06-AUD", false)
	rule := f.save(t, &f.entity, "error", true, "email needed")
	f.must(t, gate)

	var raw []byte
	if err := f.super.QueryRow(context.Background(),
		`SELECT payload->'custom_rule_revision_ids' FROM audit_log
		  WHERE tenant_id = $1 AND event = 'invoice.validated' AND payload->>'id' = $2 AND payload->>'outcome' = 'demoted'`,
		f.tenant, id).Scan(&raw); err != nil {
		t.Fatalf("read demotion audit row: %v", err)
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || len(ids) != 1 || ids[0] != rule.RevisionID {
		t.Errorf("custom_rule_revision_ids = %s, want [%s]", raw, rule.RevisionID)
	}
}

func TestRecheckCustomRules_DeletedOrDisabledBeforeTheTickDemotesNothing(t *testing.T) {
	for _, mode := range []string{"deleted", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRFixture(t, "ENGI-14-06 "+mode)
			plain := f.gate(t, start04Wrapped(t, f.app, nil, nil))
			id := f.validatedOn(t, plain, f.entity, "E14-06-"+mode, false)
			rule := f.save(t, &f.entity, "error", true, "email needed")
			if mode == "deleted" {
				if err := f.rules.Delete(f.admin, rule.ID); err != nil {
					t.Fatalf("delete: %v", err)
				}
			} else {
				f.edit(t, rule, &f.entity, "error", false)
			}

			f.must(t, plain)
			if f.status(t, id) != StatusValidated || f.pending(t, rule.RevisionID) {
				t.Errorf("status %s pending %v, want validated and marked", f.status(t, id), f.pending(t, rule.RevisionID))
			}
		})
	}
}

func TestRecheckCustomRules_WarningToErrorDemotesAnInvoiceHoldingTheWarning(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-06 warn to error")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	warn := f.save(t, &f.entity, "warning", true, "email wanted")
	id := f.validatedOn(t, gate, f.entity, "E14-06-W2E", false)
	var raw []byte
	_ = f.super.QueryRow(context.Background(), `SELECT violations FROM invoices WHERE id = $1`, id).Scan(&raw)
	if !strings.Contains(string(raw), customrule.Key(warn.ID)) {
		t.Fatalf("stored violations = %s, want the warning held", raw)
	}
	errRev := f.edit(t, warn, &f.entity, "error", true)

	f.must(t, gate)
	_, revision := f.causes(t, id)
	if f.status(t, id) != StatusDraft || revision == nil || *revision != errRev.RevisionID {
		t.Errorf("status %s cause %v, want draft caused by %s", f.status(t, id), revision, errRev.RevisionID)
	}
}
