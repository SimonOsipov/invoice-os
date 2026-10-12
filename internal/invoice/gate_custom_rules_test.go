package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// crFixture is one tenant with an admin who saves rules and a member who validates.
type crFixture struct {
	super, app    *pgxpool.Pool
	tenant        string
	entity        string
	admin, member context.Context
	rules         *customrule.Store
	store         *Store
}

func newCRFixture(t *testing.T, label string) crFixture {
	t.Helper()
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, label)
	adminID := uuid.NewString()
	seedMembership(t, super, tenant, adminID, "admin")
	return crFixture{
		super: super, app: app, tenant: tenant, entity: seedEntityWithTIN(t, super, tenant, label+" entity", "12345678-0001"),
		admin:  auth.WithIdentity(context.Background(), auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenant}),
		member: auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant}),
		rules:  customrule.NewStore(app, func(context.Context, customrule.CheckInput) error { return nil }),
		store:  NewStore(app),
	}
}

// save stores a rule that fails an invoice with no buyer email.
func (f crFixture) save(t *testing.T, entity *string, severity string, enabled bool, msg string) customrule.Rule {
	t.Helper()
	r, err := f.rules.Create(f.admin, customrule.RuleInput{EntityID: entity, Type: "required", Target: "buyer.email",
		Params: json.RawMessage(`{}`), Severity: severity, Message: msg, Enabled: enabled})
	if err != nil {
		t.Fatalf("save custom rule: %v", err)
	}
	return r
}

func (f crFixture) invoice(t *testing.T, number string) Invoice {
	t.Helper()
	inv, err := f.store.Create(f.member, gapiValidInvoiceInput(f.entity, number))
	if err != nil {
		t.Fatalf("Create invoice: %v", err)
	}
	return inv
}

// start04Wrapped is the real batch handler with a hook before each call and an optional
// edit of each decoded response; bodies records the raw requests.
type wrapped04 struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func (w *wrapped04) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.bodies)
}

func start04Wrapped(t *testing.T, app *pgxpool.Pool, before func(call int), edit func(resp map[string]any)) *wrapped04 {
	t.Helper()
	inner := validation.S2SMiddleware(gapiS2SToken)(validation.BatchValidateHandler(validation.NewStore(app).LoadForBatch, validation.NewDefaultEngine(), nil, nil))
	w := &wrapped04{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		w.mu.Lock()
		w.bodies = append(w.bodies, string(body))
		n := len(w.bodies)
		w.mu.Unlock()
		if before != nil {
			before(n)
		}
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r)
		out := rec.Body.Bytes()
		if edit != nil && rec.Code == http.StatusOK {
			var resp map[string]any
			if err := json.Unmarshal(out, &resp); err == nil {
				edit(resp)
				out, _ = json.Marshal(resp)
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(rec.Code)
		_, _ = rw.Write(out)
	}))
	t.Cleanup(w.Close)
	return w
}

func (f crFixture) gate(t *testing.T, srv *wrapped04) *Gate {
	t.Helper()
	return NewGate(f.store, NewValidator(srv.URL, gapiS2SToken, nil))
}

func violationsOf(t *testing.T, inv Invoice) []Violation {
	t.Helper()
	var vs []Violation
	if err := json.Unmarshal(inv.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", inv.Violations, err)
	}
	return vs
}

func violationOf(vs []Violation, key string) (Violation, bool) {
	for _, v := range vs {
		if v.RuleKey == key {
			return v, true
		}
	}
	return Violation{}, false
}

// validatedRows returns the custom_rule_revision_ids of each invoice.validated audit row of an invoice, oldest first.
func validatedRows(t *testing.T, f crFixture, invoiceID string) [][]string {
	t.Helper()
	rows, err := f.super.Query(context.Background(),
		`SELECT payload->'custom_rule_revision_ids' FROM audit_log
		  WHERE tenant_id = $1 AND event = 'invoice.validated' AND payload->>'id' = $2 ORDER BY id`, f.tenant, invoiceID)
	if err != nil {
		t.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var ids []string
		if raw == nil || json.Unmarshal(raw, &ids) != nil || ids == nil {
			t.Fatalf("custom_rule_revision_ids = %s, want a JSON array", raw)
		}
		sort.Strings(ids)
		out = append(out, ids)
	}
	return out
}

func sorted(ss ...string) []string { sort.Strings(ss); return ss }

func TestGate_DB_NeverSendsAnotherTenantsRules(t *testing.T) {
	f1 := newCRFixture(t, "ENGI-14-05 tenant 1")
	f2 := newCRFixture(t, "ENGI-14-05 tenant 2")
	own := f1.save(t, nil, "error", true, "own firm rule")
	theirs := f2.save(t, nil, "error", true, "their firm rule")
	theirsClient := f2.save(t, &f2.entity, "error", true, "their client rule")

	srv := start04Wrapped(t, f1.app, nil, nil)
	inv := f1.invoice(t, "ENGI-14-05-ISO")
	res, err := f1.gate(t, srv).Evaluate(f1.member, []EvalItem{{Ref: inv.ID, Invoice: inv}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := res.CustomByRef[inv.ID]; !reflect.DeepEqual(got, []string{own.RevisionID}) {
		t.Errorf("custom_rule_ids = %v, want only tenant 1's %s", got, own.RevisionID)
	}
	for _, id := range []string{theirs.RevisionID, theirsClient.RevisionID} {
		if strings.Contains(srv.bodies[0], id) {
			t.Errorf("request carries tenant 2's revision %s", id)
		}
	}
}

func TestGate_OneCustomRuleReadPerEvaluate(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 one read")
	f.save(t, nil, "warning", true, "firm")
	other1 := seedEntity(t, f.super, f.tenant, "second")
	other2 := seedEntity(t, f.super, f.tenant, "third")
	f.save(t, &other1, "warning", true, "client")

	pool, rec := tracedAppPool(t)
	gate := NewGate(NewStore(pool), NewValidator(start04Wrapped(t, f.app, nil, nil).URL, gapiS2SToken, nil))
	var items []EvalItem
	for i, e := range []string{f.entity, other1, other2, f.entity, other1} {
		items = append(items, EvalItem{Ref: string(rune('a' + i)), Invoice: Invoice{EntityID: e, InvoiceNumber: "n"}})
	}
	rec.reset()
	if _, err := gate.Evaluate(f.member, items); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if n := len(rec.mentioning("custom_rule_revisions")); n != 1 {
		t.Errorf("statements against custom_rule_revisions = %d, want exactly 1 for 5 invoices on 3 entities", n)
	}
}

func TestGate_CustomRuleReadFailureIsAnError(t *testing.T) {
	_, app := dbTestPools(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	t.Cleanup(srv.Close)
	gate := NewGate(NewStore(app), NewValidator(srv.URL, gapiS2SToken, nil))

	_, err := gate.Evaluate(context.Background(), []EvalItem{{Ref: "a", Invoice: Invoice{EntityID: uuid.NewString()}}})
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("err = %v, want db.ErrNoTenant", err)
	}
	if calls != 0 {
		t.Errorf("validation received %d calls, want 0", calls)
	}
}

func TestGate_DB_BlockingCustomRuleKeepsDraft(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 blocking")
	rule := f.save(t, &f.entity, "error", true, "buyer email needed")
	inv := f.invoice(t, "ENGI-14-05-BLK")

	got, _, err := f.gate(t, start04Wrapped(t, f.app, nil, nil)).Validate(f.member, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want draft", got.Status)
	}
	v, ok := violationOf(violationsOf(t, got), customrule.Key(rule.ID))
	if !ok || v.Severity != "error" {
		t.Errorf("violations = %s, want %s at severity error", got.Violations, customrule.Key(rule.ID))
	}
}

func TestGate_DB_WarningCustomRuleStillValidates(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 warning")
	rule := f.save(t, &f.entity, "warning", true, "buyer email wanted")
	inv := f.invoice(t, "ENGI-14-05-WARN")

	got, _, err := f.gate(t, start04Wrapped(t, f.app, nil, nil)).Validate(f.member, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want validated", got.Status)
	}
	v, ok := violationOf(violationsOf(t, got), customrule.Key(rule.ID))
	if !ok || v.Severity != "warning" {
		t.Errorf("violations = %s, want %s at severity warning", got.Violations, customrule.Key(rule.ID))
	}
}

func TestGate_DB_SavedRuleAppliesToTheNextValidation(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 saved rule")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	inv := f.invoice(t, "ENGI-14-05-NEXT")

	if got, _, err := gate.Validate(f.member, inv.ID); err != nil || got.Status != StatusValidated {
		t.Fatalf("clean validate = %q, %v, want validated", got.Status, err)
	}
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET status = 'draft' WHERE id = $1`, inv.ID); err != nil {
		t.Fatalf("reset to draft: %v", err)
	}

	rule := f.save(t, &f.entity, "error", true, "email needed")
	got, _, err := gate.Validate(f.member, inv.ID)
	if err != nil || got.Status != StatusDraft {
		t.Fatalf("validate with a failing rule = %q, %v, want draft", got.Status, err)
	}
	if _, ok := violationOf(violationsOf(t, got), customrule.Key(rule.ID)); !ok {
		t.Errorf("violations = %s, want the new rule's", got.Violations)
	}

	off, err := f.rules.Update(f.admin, rule.ID, customrule.RuleInput{EntityID: &f.entity, Type: "required", Target: "buyer.email",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "email needed", Enabled: false})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _, err = gate.Validate(f.member, inv.ID)
	if err != nil || got.Status != StatusValidated {
		t.Fatalf("validate after disabling %s = %q, %v, want validated", off.RevisionID, got.Status, err)
	}
	if _, ok := violationOf(violationsOf(t, got), customrule.Key(rule.ID)); ok {
		t.Errorf("violations = %s, want the disabled rule gone", got.Violations)
	}
}

func TestGate_DB_VerdictAuditRecordsTheRevisionsThatRan(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 audit")
	firm := f.save(t, nil, "error", true, "firm rule")
	client := f.save(t, &f.entity, "error", true, "old message")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	inv := f.invoice(t, "ENGI-14-05-AUD")

	if _, _, err := gate.Validate(f.member, inv.ID); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	edited, err := f.rules.Update(f.admin, client.ID, customrule.RuleInput{EntityID: &f.entity, Type: "required", Target: "buyer.email",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "new message", Enabled: true})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, _, err := gate.Validate(f.member, inv.ID); err != nil {
		t.Fatalf("second Validate: %v", err)
	}

	want := [][]string{sorted(firm.RevisionID, client.RevisionID), sorted(firm.RevisionID, edited.RevisionID)}
	if got := validatedRows(t, f, inv.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("audit custom_rule_revision_ids = %v, want %v", got, want)
	}
	var msg string
	if err := f.super.QueryRow(context.Background(), `SELECT message FROM custom_rule_revisions WHERE id = $1`, client.RevisionID).Scan(&msg); err != nil || msg != "old message" {
		t.Errorf("revision named by the first row holds %q (%v), want the earlier content", msg, err)
	}
}

func TestGate_DB_NoCustomRulesRecordsAnEmptyList(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 empty list")
	inv := f.invoice(t, "ENGI-14-05-EMPTY")
	if _, _, err := f.gate(t, start04Wrapped(t, f.app, nil, nil)).Validate(f.member, inv.ID); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := validatedRows(t, f, inv.ID); !reflect.DeepEqual(got, [][]string{{}}) {
		t.Errorf("custom_rule_revision_ids = %v, want one row holding []", got)
	}
}

func TestGate_DB_EchoMismatchWritesNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 echo")
	f.save(t, &f.entity, "error", true, "email needed")
	inv := f.invoice(t, "ENGI-14-05-ECHO")
	srv := start04Wrapped(t, f.app, nil, func(resp map[string]any) {
		for _, r := range resp["results"].([]any) {
			delete(r.(map[string]any), "custom_rule_ids")
		}
	})

	if _, _, err := f.gate(t, srv).Validate(f.member, inv.ID); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	f.assertUntouched(t, inv.ID)
}

// assertUntouched: the invoice is still the unvalidated draft and no verdict row exists.
func (f crFixture) assertUntouched(t *testing.T, invoiceID string) {
	t.Helper()
	var status string
	var rsv *string
	var violations string
	if err := f.super.QueryRow(context.Background(),
		`SELECT status, rule_set_version_id::text, violations::text FROM invoices WHERE id = $1`, invoiceID).Scan(&status, &rsv, &violations); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if status != "draft" || rsv != nil || violations != "[]" {
		t.Errorf("invoice = %s / %v / %s, want the untouched draft", status, rsv, violations)
	}
	if rows := validatedRows(t, f, invoiceID); len(rows) != 0 {
		t.Errorf("invoice.validated rows = %d, want 0", len(rows))
	}
}

func TestGate_DB_RuleSavedDuringValidationIsApplied(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 save during")
	inv := f.invoice(t, "ENGI-14-05-RACE1")
	var saved customrule.Rule
	srv := start04Wrapped(t, f.app, func(call int) {
		if call == 1 {
			saved = f.save(t, &f.entity, "error", true, "saved mid-flight")
		}
	}, nil)

	got, _, err := f.gate(t, srv).Validate(f.member, inv.ID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if n := srv.calls(); n != 2 {
		t.Errorf("validation calls = %d, want 2 (one re-evaluation)", n)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want draft", got.Status)
	}
	if _, ok := violationOf(violationsOf(t, got), customrule.Key(saved.ID)); !ok {
		t.Errorf("violations = %s, want the rule saved mid-flight", got.Violations)
	}
	if rows := validatedRows(t, f, inv.ID); !reflect.DeepEqual(rows, [][]string{{saved.RevisionID}}) {
		t.Errorf("audit rows = %v, want one naming the new revision %s", rows, saved.RevisionID)
	}
}

func TestGate_DB_RulesChangingTwiceIs409AndWritesNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 save twice")
	inv := f.invoice(t, "ENGI-14-05-RACE2")
	srv := start04Wrapped(t, f.app, func(call int) {
		f.save(t, &f.entity, "error", true, "saved during call "+string(rune('0'+call)))
	}, nil)
	gate := f.gate(t, srv)

	_, _, err := gate.Validate(f.member, inv.ID)
	if !errors.Is(err, ErrRulesChanged) {
		t.Fatalf("err = %v, want ErrRulesChanged", err)
	}
	f.assertUntouched(t, inv.ID)

	req := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+inv.ID+"/validate", nil).WithContext(f.member)
	req.SetPathValue("id", inv.ID)
	rec := httptest.NewRecorder()
	ValidateHandler(gate.Validate, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "the rules changed during validation; validate again") {
		t.Errorf("handler = %d %s, want 409 with the rules-changed message", rec.Code, rec.Body.String())
	}
	f.assertUntouched(t, inv.ID)
}

func TestApplyValidation_DB_StaleCustomIDsRefused(t *testing.T) {
	f := newCRFixture(t, "ENGI-14-05 stale ids")
	rule := f.save(t, &f.entity, "error", true, "email needed")
	inv := f.invoice(t, "ENGI-14-05-STALE")
	got, err := f.store.Get(f.member, inv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	fp := contentFingerprint(got, got.LineItems)
	versionID := seedRuleSetVersionID(t, f.super)

	if _, err := f.store.applyValidation(f.member, inv.ID, []Violation{}, versionID, nil, fp); !errors.Is(err, ErrRulesChanged) {
		t.Fatalf("applyValidation with no ids = %v, want ErrRulesChanged", err)
	}
	if s := statusOf(t, f.super, inv.ID); s != StatusDraft {
		t.Errorf("status = %q after the refusal, want draft", s)
	}
	out, err := f.store.applyValidation(f.member, inv.ID, []Violation{}, versionID, []string{rule.RevisionID}, fp)
	if err != nil || out.Status != StatusValidated {
		t.Errorf("applyValidation with every current id = %q, %v, want validated", out.Status, err)
	}
}
