package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// listedDemotion is one list row's demotion as it reaches the wire.
type listedDemotion struct {
	status  Status
	present bool            // the "demotion" key is on the row
	raw     json.RawMessage // "null" or the object
	d       *Demotion
}

func (f crFixture) listDemotionsAs(t *testing.T, caller context.Context) map[string]listedDemotion {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil).WithContext(caller)
	rec := httptest.NewRecorder()
	ListHandler(f.store.List, f.store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	out := map[string]listedDemotion{}
	for _, row := range listRowsRaw(t, rec) {
		var id string
		var l listedDemotion
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("decode row id: %v", err)
		}
		if err := json.Unmarshal(row["status"], &l.status); err != nil {
			t.Fatalf("decode row status: %v", err)
		}
		l.raw, l.present = row["demotion"]
		if err := json.Unmarshal(l.raw, &l.d); l.present && err != nil {
			t.Fatalf("decode demotion %q of %s: %v", l.raw, id, err)
		}
		out[id] = l
	}
	return out
}

func (f crFixture) listDemotions(t *testing.T) map[string]listedDemotion {
	return f.listDemotionsAs(t, f.admin)
}

// historyLen is the invoice's history row count.
func (f crFixture) historyLen(t *testing.T, id string) int {
	t.Helper()
	rows, err := f.store.History(f.member, id)
	if err != nil {
		t.Fatalf("History(%s): %v", id, err)
	}
	return len(rows)
}

func (f crFixture) lastHistory(t *testing.T, id string) StatusChange {
	t.Helper()
	rows, err := f.store.History(f.member, id)
	if err != nil || len(rows) == 0 {
		t.Fatalf("History(%s) = %d rows, %v", id, len(rows), err)
	}
	return rows[len(rows)-1]
}

// demotedByVersion creates a validated invoice dated inside version b and lets RecheckCovered demote it.
func (f crFixture) demotedByVersion(t *testing.T, bID string, bVer int, number string) string {
	t.Helper()
	requireVersionFor(t, f.super, "3001-07-01", bID)
	date := day(3001, 7, 1)
	inv, err := f.store.Create(f.member, CreateInput{EntityID: f.entity, InvoiceNumber: number, IssueDate: &date})
	if err != nil {
		t.Fatalf("Create %s: %v", number, err)
	}
	if _, err := f.store.Transition(f.member, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition %s to validated (setup): %v", number, err)
	}
	if _, err := RecheckCovered(context.Background(), f.app, f.store, realRecheckGate(t, f.app, f.store), f.tenant,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1)); err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if got := f.status(t, inv.ID); got != StatusDraft {
		t.Fatalf("%s status = %q after RecheckCovered, want draft", number, got)
	}
	return inv.ID
}

func wantVersionCause(bID string, bVer int) HistoryCause {
	return HistoryCause{RuleSetVersion: bVer, RuleSetVersionID: bID, EffectiveFrom: strPtr("3001-06-01")}
}

func wantRuleCause(r customrule.Rule, msg string) CustomRuleCause {
	return CustomRuleCause{CustomRuleID: r.ID, Revision: 1, RevisionID: r.RevisionID, Key: customrule.Key(r.ID), Message: msg}
}

func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode object %q: %v", raw, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestListDemotion_DB_VersionCause(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01") // before the tenant: LIFO cleanup
	f := newCRFixture(t, "ENGI-15-04 version")
	id := f.demotedByVersion(t, bID, bVer, "E15-04-V1")

	l, ok := f.listDemotions(t)[id]
	if !ok {
		t.Fatalf("demoted invoice %s is not on the list page", id)
	}
	if !l.present || l.d == nil {
		t.Fatalf("row demotion = %s (key present %v), want an object", l.raw, l.present)
	}
	if want := []string{"cause", "changed_at", "custom_rule_cause"}; !reflect.DeepEqual(jsonKeys(t, l.raw), want) {
		t.Errorf("demotion keys = %v, want %v", jsonKeys(t, l.raw), want)
	}
	want := wantVersionCause(bID, bVer)
	if l.d.Cause == nil || l.d.Cause.RuleSetVersion != want.RuleSetVersion || l.d.Cause.RuleSetVersionID != want.RuleSetVersionID ||
		l.d.Cause.EffectiveFrom == nil || *l.d.Cause.EffectiveFrom != *want.EffectiveFrom {
		t.Errorf("demotion.cause = %+v, want %+v", l.d.Cause, want)
	}
	if l.d.CustomRuleCause != nil {
		t.Errorf("demotion.custom_rule_cause = %+v, want null", l.d.CustomRuleCause)
	}
	if last := f.lastHistory(t, id); !l.d.ChangedAt.Equal(last.ChangedAt) {
		t.Errorf("demotion.changed_at = %v, want the demotion row's %v", l.d.ChangedAt, last.ChangedAt)
	}
}

func TestListDemotion_DB_CustomRuleCause(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-04 custom")
	id, rule := f.demotedByRule(t, "E15-04-C1")

	l, ok := f.listDemotions(t)[id]
	if !ok {
		t.Fatalf("demoted invoice %s is not on the list page", id)
	}
	if !l.present || l.d == nil {
		t.Fatalf("row demotion = %s (key present %v), want an object", l.raw, l.present)
	}
	want := wantRuleCause(rule, "email needed")
	if l.d.CustomRuleCause == nil || *l.d.CustomRuleCause != want {
		t.Errorf("demotion.custom_rule_cause = %+v, want %+v", l.d.CustomRuleCause, want)
	}
	if l.d.Cause != nil {
		t.Errorf("demotion.cause = %+v, want null", l.d.Cause)
	}
}

// demotionCase is one invoice of the shared table that T1 pins on the SPA side too (latestDemotion_matchesTheServerRule).
type demotionCase struct {
	name       string
	id         string
	wantStatus Status
	wantSet    bool
}

type demotionCases struct {
	f       crFixture
	cases   []demotionCase
	version HistoryCause
	rule    CustomRuleCause
	// versionID and ruleID are the invoices whose demotion carries each cause.
	versionID, ruleID string
}

// buildDemotionCases seeds one tenant holding every case of the D11/D21 table.
func buildDemotionCases(t *testing.T) demotionCases {
	t.Helper()
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01") // before the tenant: LIFO cleanup
	f := newCRFixture(t, "ENGI-15-04 cases")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	out := demotionCases{f: f, version: wantVersionCause(bID, bVer)}

	// A version demotion first: no custom rule exists yet.
	out.versionID = f.demotedByVersion(t, bID, bVer, "E15-04-T-VER")

	revalidated := f.validatedOn(t, gate, f.entity, "E15-04-T-REVAL", false)
	editedAfter := f.validatedOn(t, gate, f.entity, "E15-04-T-EDITED", false)
	stillFailing := f.validatedOn(t, gate, f.entity, "E15-04-T-STILL", false)
	reEdited := f.validatedOn(t, gate, f.entity, "E15-04-T-REEDIT", false)
	byEdit := f.validatedOn(t, gate, f.entity, "E15-04-T-BYEDIT", true) // passes the rule, so only the edit demotes it
	neverValidated := f.invoice(t, "E15-04-T-NEVER").ID

	rule := f.save(t, &f.entity, "error", true, "email needed")
	f.must(t, gate)
	out.rule = wantRuleCause(rule, "email needed")
	out.ruleID = editedAfter
	for _, id := range []string{revalidated, editedAfter, stillFailing, reEdited} {
		if f.status(t, id) != StatusDraft {
			t.Fatalf("invoice %s not demoted by the rule", id)
		}
	}
	if f.status(t, byEdit) != StatusValidated {
		t.Fatalf("invoice %s was demoted by the rule, want it to pass", byEdit)
	}

	// demoted, then re-validated
	if _, err := f.store.Update(f.member, revalidated, UpdateInput{BuyerEmail: strPtr("buyer@example.com")}); err != nil {
		t.Fatalf("Update %s: %v", revalidated, err)
	}
	if got, _, err := gate.Validate(f.member, revalidated); err != nil || got.Status != StatusValidated {
		t.Fatalf("re-validate %s = %q, %v, want validated", revalidated, got.Status, err)
	}

	// demoted, re-validated, then edited back to draft: an older caused row, a newer row without a cause
	if _, err := f.store.Update(f.member, reEdited, UpdateInput{BuyerEmail: strPtr("buyer@example.com")}); err != nil {
		t.Fatalf("Update %s: %v", reEdited, err)
	}
	if got, _, err := gate.Validate(f.member, reEdited); err != nil || got.Status != StatusValidated {
		t.Fatalf("re-validate %s = %q, %v, want validated", reEdited, got.Status, err)
	}
	if _, err := f.store.Edit(f.member, reEdited, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Edited Ltd")}}); err != nil {
		t.Fatalf("Edit %s: %v", reEdited, err)
	}
	if last := f.lastHistory(t, reEdited); last.ToStatus != StatusDraft || last.Cause != nil || last.CustomRuleCause != nil {
		t.Fatalf("edit row of %s = %+v, want validated->draft with no cause", reEdited, last)
	}

	// validated -> draft by an edit: a history row without a cause
	if _, err := f.store.Edit(f.member, byEdit, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Edited Ltd")}}); err != nil {
		t.Fatalf("Edit %s: %v", byEdit, err)
	}
	if last := f.lastHistory(t, byEdit); last.ToStatus != StatusDraft || last.Cause != nil || last.CustomRuleCause != nil {
		t.Fatalf("edit demotion row = %+v, want validated->draft with no cause", last)
	}

	// demoted, then edited: still draft, no new history row
	n := f.historyLen(t, editedAfter)
	if _, err := f.store.Edit(f.member, editedAfter, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Edited Ltd")}}); err != nil {
		t.Fatalf("Edit %s: %v", editedAfter, err)
	}
	if f.status(t, editedAfter) != StatusDraft || f.historyLen(t, editedAfter) != n {
		t.Fatalf("edit of draft %s changed its status or history", editedAfter)
	}

	// demoted, edited, re-validated and still failing: draft -> draft writes no history row
	n = f.historyLen(t, stillFailing)
	if _, err := f.store.Edit(f.member, stillFailing, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Edited Ltd")}}); err != nil {
		t.Fatalf("Edit %s: %v", stillFailing, err)
	}
	if got, _, err := gate.Validate(f.member, stillFailing); err != nil || got.Status != StatusDraft {
		t.Fatalf("re-validate %s = %q, %v, want it to stay draft", stillFailing, got.Status, err)
	}
	if f.historyLen(t, stillFailing) != n {
		t.Fatalf("re-validate of draft %s wrote a history row", stillFailing)
	}

	out.cases = []demotionCase{
		{"version re-check demotion", out.versionID, StatusDraft, true},
		{"demoted then re-validated", revalidated, StatusValidated, false},
		{"draft never validated", neverValidated, StatusDraft, false},
		{"validated to draft by an edit", byEdit, StatusDraft, false},
		{"demoted, re-validated, then edited back to draft", reEdited, StatusDraft, false},
		{"demoted then edited, still draft", editedAfter, StatusDraft, true},
		{"demoted, edited, re-validated, still failing (D21)", stillFailing, StatusDraft, true},
	}
	return out
}

func TestListDemotion_DB_NullUnlessLatestRowIsACausedDemotion(t *testing.T) {
	c := buildDemotionCases(t)
	list := c.f.listDemotions(t)

	set := 0
	for _, k := range c.cases {
		l, ok := list[k.id]
		if !ok {
			t.Fatalf("%s: invoice %s is not on the list page", k.name, k.id)
		}
		if !l.present {
			t.Errorf("%s: row has no demotion key, want null or an object", k.name)
		}
		if l.status != k.wantStatus {
			t.Errorf("%s: status = %q, want %q (setup)", k.name, l.status, k.wantStatus)
		}
		if got := l.d != nil; got != k.wantSet {
			t.Errorf("%s: demotion = %s, want set=%v", k.name, l.raw, k.wantSet)
		}
		if k.wantSet {
			set++
		}
	}
	if set == 0 {
		t.Fatal("no case expects a demotion -- the null legs prove nothing")
	}

	// A re-check demotion that is still the latest row keeps its own cause.
	if l := list[c.ruleID]; l.d == nil || l.d.CustomRuleCause == nil || *l.d.CustomRuleCause != c.rule || l.d.Cause != nil {
		t.Errorf("custom demotion of %s = %s, want custom_rule_cause %+v and cause null", c.ruleID, l.raw, c.rule)
	}
	if l := list[c.versionID]; l.d == nil || l.d.Cause == nil || l.d.Cause.RuleSetVersionID != c.version.RuleSetVersionID || l.d.CustomRuleCause != nil {
		t.Errorf("version demotion of %s = %s, want cause %+v and custom_rule_cause null", c.versionID, l.raw, c.version)
	}
}

// The list rule is the History rule: the last history row's causes while the invoice is a draft, else null.
func TestListDemotion_EqualsTheLatestHistoryRowCause(t *testing.T) {
	c := buildDemotionCases(t)
	list := c.f.listDemotions(t)

	causedLast := 0
	for _, k := range c.cases {
		l, ok := list[k.id]
		if !ok {
			t.Fatalf("%s: invoice %s is not on the list page", k.name, k.id)
		}
		last := c.f.lastHistory(t, k.id)
		wantSet := l.status == StatusDraft && (last.Cause != nil || last.CustomRuleCause != nil)
		if wantSet {
			causedLast++
		}
		if got := l.d != nil; got != wantSet {
			t.Errorf("%s: demotion = %s, want set=%v from the last history row %+v", k.name, l.raw, wantSet, last)
			continue
		}
		if l.d == nil {
			continue
		}
		if !reflect.DeepEqual(l.d.Cause, last.Cause) || !reflect.DeepEqual(l.d.CustomRuleCause, last.CustomRuleCause) {
			t.Errorf("%s: demotion causes = %+v / %+v, want History's %+v / %+v", k.name, l.d.Cause, l.d.CustomRuleCause, last.Cause, last.CustomRuleCause)
		}
		if !l.d.ChangedAt.Equal(last.ChangedAt) {
			t.Errorf("%s: demotion.changed_at = %v, want %v", k.name, l.d.ChangedAt, last.ChangedAt)
		}
	}
	if causedLast == 0 {
		t.Fatal("no case ends on a caused demotion -- the equality proves nothing")
	}
}

// Version cause, re-validate, then custom cause: the later row names the cause.
func TestListDemotion_DB_LatestCausedRowWins(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	f := newCRFixture(t, "ENGI-15-04 latest")
	gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
	id := f.demotedByVersion(t, bID, bVer, "E15-04-L1")
	if _, err := f.store.Update(f.member, id, UpdateInput{BuyerName: strPtr("Buyer Ltd")}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _, err := gate.Validate(f.member, id); err != nil || got.Status != StatusValidated {
		t.Fatalf("re-validate = %q, %v, want validated", got.Status, err)
	}
	rule := f.save(t, &f.entity, "error", true, "email needed")
	f.must(t, gate)
	if f.status(t, id) != StatusDraft {
		t.Fatal("the custom re-check did not demote the invoice")
	}

	l, ok := f.listDemotions(t)[id]
	if !ok {
		t.Fatalf("invoice %s is not on the list page", id)
	}
	want := wantRuleCause(rule, "email needed")
	if l.d == nil || l.d.CustomRuleCause == nil || *l.d.CustomRuleCause != want || l.d.Cause != nil {
		t.Errorf("demotion = %s, want the later custom cause %+v and cause null", l.raw, want)
	}
}

// One statement against invoice_status_history per RowFacts call, whatever the page size.
func TestListDemotion_OneStatementPerPage(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01") // before the tenant: LIFO cleanup
	f := newCRFixture(t, "ENGI-15-04 const")
	tracedApp, rec := tracedAppPool(t)

	ids := make([]string, 50)
	demoted := map[string]bool{}
	for i := range ids {
		ids[i] = seedInvoiceAtStatus(t, f.super, f.tenant, f.entity, "E15-04-CONST-"+uuid.NewString(), StatusDraft)
		if i%3 == 1 {
			demoted[ids[i]] = true
			if _, err := f.super.Exec(context.Background(),
				`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, cause_rule_set_version_id)
				 VALUES ($1, $2, 'validated', 'draft', 'system', $3)`, f.tenant, ids[i], bID); err != nil {
				t.Fatalf("seed demotion row: %v", err)
			}
		}
	}

	store := NewStore(tracedApp)
	for _, page := range [][]string{ids, ids[1:2], ids[:3]} {
		rec.reset()
		_, gate, err := store.RowFacts(f.admin, page)
		if err != nil {
			t.Fatalf("RowFacts of %d: %v", len(page), err)
		}
		if n := len(rec.mentioning("invoice_status_history")); n != 1 {
			t.Errorf("page of %d issued %d statements against invoice_status_history, want 1", len(page), n)
		}
		want, wrong := 0, 0
		for _, id := range page {
			if demoted[id] {
				want++
				if d := gate.Demotion[id]; d.Cause == nil || d.Cause.RuleSetVersionID != bID || d.Cause.RuleSetVersion != bVer {
					wrong++
				}
			}
		}
		if wrong > 0 {
			t.Errorf("page of %d: %d of %d demoted invoices lack the cause of version %d", len(page), wrong, want, bVer)
		}
		if want == 0 {
			t.Fatalf("page of %d holds no demoted invoice -- the demotion leg cannot run", len(page))
		}
		if len(gate.Demotion) != want {
			t.Errorf("page of %d: %d Demotion entries, want %d (only demoted invoices)", len(page), len(gate.Demotion), want)
		}
	}
}

// Tenant 2's history rows never read as tenant 1's demotion, at the helper and on the list.
// Tenant 2's two demotions carry a firm-wide custom rule and a rule-set version.
func TestListDemotion_DB_NeverReadsAnotherTenant(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01") // before the tenants: LIFO cleanup
	f1 := newCRFixture(t, "ENGI-15-04 xt t1")
	f2 := newCRFixture(t, "ENGI-15-04 xt t2")
	demote := func(f crFixture, entity *string, number, msg string) (string, CustomRuleCause) {
		gate := f.gate(t, start04Wrapped(t, f.app, nil, nil))
		id := f.validatedOn(t, gate, f.entity, number, false)
		rule := f.save(t, entity, "error", true, msg)
		f.must(t, gate)
		if f.status(t, id) != StatusDraft {
			t.Fatalf("invoice %s not demoted", number)
		}
		return id, wantRuleCause(rule, msg)
	}
	id1, cause1 := demote(f1, &f1.entity, "E15-04-XT1", "tenant 1 rule")
	id2, _ := demote(f2, nil, "E15-04-XT2", "tenant 2 firm rule")
	idV := f2.demotedByVersion(t, bID, bVer, "E15-04-XT2V")

	read := func(ids ...string) map[string]Demotion {
		t.Helper()
		var got map[string]Demotion
		err := db.WithinRequestTenantTx(f1.admin, f1.app, func(tx pgx.Tx) error {
			var err error
			got, err = latestDemotionsTx(f1.admin, tx, ids)
			return err
		})
		if err != nil {
			t.Fatalf("latestDemotionsTx under tenant 1: %v", err)
		}
		return got
	}

	mixed := read(id1, id2, idV)
	if d, ok := mixed[id1]; !ok || d.CustomRuleCause == nil || *d.CustomRuleCause != cause1 {
		t.Fatalf("control: tenant 1's own demotion = %+v (present %v), want %+v", d, ok, cause1)
	}
	if len(mixed) != 1 {
		t.Errorf("tenant 1 read %d entries for [own, tenant 2's firm rule, tenant 2's version], want only its own: %+v", len(mixed), mixed)
	}
	if only := read(id2, idV); len(only) != 0 {
		t.Errorf("tenant 1 read tenant 2's ids: %+v, want an empty map", only)
	}
	list2 := f2.listDemotions(t)
	if list2[id2].d == nil || list2[idV].d == nil {
		t.Fatalf("control: tenant 2's own list lacks its demotions (firm rule %s, version %s)", list2[id2].raw, list2[idV].raw)
	}

	_, gate, err := f1.store.RowFacts(f1.admin, []string{id1, id2, idV})
	if err != nil {
		t.Fatalf("RowFacts: %v", err)
	}
	if _, ok := gate.Demotion[id1]; !ok {
		t.Errorf("control: RowFacts has no Demotion for tenant 1's own invoice")
	}
	for _, other := range []string{id2, idV} {
		if _, ok := gate.Demotion[other]; ok {
			t.Errorf("RowFacts under tenant 1 carries tenant 2's demotion for %s", other)
		}
	}

	list := f1.listDemotions(t)
	if l, ok := list[id1]; !ok || l.d == nil || l.d.CustomRuleCause == nil || l.d.CustomRuleCause.Message != "tenant 1 rule" {
		t.Errorf("tenant 1 list row = %+v (present %v), want its own demotion naming %q", l, ok, "tenant 1 rule")
	}
	for _, other := range []string{id2, idV} {
		if _, ok := list[other]; ok {
			t.Errorf("tenant 1's list carries tenant 2's invoice %s", other)
		}
	}
}

// demotion is a list-only key: the detail wire stays without it (D11).
func TestGetHandler_HasNoDemotionKey(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-04 detail")
	id, _ := f.demotedByRule(t, "E15-04-D1")

	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+id, nil).WithContext(f.admin)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	GetHandler(f.store.Get, f.store.CallerRole, f.store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if _, ok := raw["can_submit"]; !ok {
		t.Fatal("detail wire has no can_submit -- this is not the detail response")
	}
	if _, ok := raw["demotion"]; ok {
		t.Errorf("detail wire carries a demotion key: %s", raw["demotion"])
	}
}

// The handler sets demotion only on a draft row that has a map entry.
func TestListHandler_DemotionOnlyOnDraftRowsWithAnEntry(t *testing.T) {
	draft := Invoice{ID: uuid.NewString(), Status: StatusDraft}
	validated := Invoice{ID: uuid.NewString(), Status: StatusValidated}
	noEntry := Invoice{ID: uuid.NewString(), Status: StatusDraft}
	d := Demotion{ChangedAt: time.Now().UTC(), Cause: &HistoryCause{RuleSetVersion: 5, RuleSetVersionID: uuid.NewString(), EffectiveFrom: strPtr("2027-01-01")}}
	list := func(context.Context, ListFilter) ([]Invoice, int, error) {
		return []Invoice{draft, validated, noEntry}, 3, nil
	}
	rowFacts := func(context.Context, []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return map[string]approval.RowFacts{}, ListGateFacts{Demotion: map[string]Demotion{draft.ID: d, validated.ID: d}}, nil
	}
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
	rec := doInvoiceListWithFacts(t, list, rowFacts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, row := range listRowsRaw(t, rec) {
		var rid string
		if err := json.Unmarshal(row["id"], &rid); err != nil {
			t.Fatalf("decode id: %v", err)
		}
		raw, ok := row["demotion"]
		if !ok {
			t.Fatalf("row %s has no demotion key", rid)
		}
		got[rid] = string(raw)
	}
	if got[draft.ID] == "null" || got[draft.ID] == "" {
		t.Errorf("draft row with an entry: demotion = %q, want an object", got[draft.ID])
	}
	if got[validated.ID] != "null" {
		t.Errorf("validated row with an entry: demotion = %q, want null", got[validated.ID])
	}
	if got[noEntry.ID] != "null" {
		t.Errorf("draft row with no entry: demotion = %q, want null", got[noEntry.ID])
	}
}

// A deleted rule appends a revision; the history row still cites the revision that demoted the draft.
func TestListDemotion_DB_DeletedRuleKeepsItsDemotion(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-04 deleted")
	id, rule := f.demotedByRule(t, "E15-04-DEL")
	if err := f.rules.Delete(f.admin, rule.ID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}

	l, ok := f.listDemotions(t)[id]
	if !ok {
		t.Fatalf("invoice %s is not on the list page", id)
	}
	want := wantRuleCause(rule, "email needed")
	if l.d == nil || l.d.CustomRuleCause == nil || *l.d.CustomRuleCause != want || l.d.Cause != nil {
		t.Fatalf("demotion = %s, want the demoting revision %+v and cause null", l.raw, want)
	}
	if last := f.lastHistory(t, id); !reflect.DeepEqual(last.CustomRuleCause, l.d.CustomRuleCause) {
		t.Errorf("list cause %+v differs from History's %+v", l.d.CustomRuleCause, last.CustomRuleCause)
	}
}

// Rows tied on changed_at resolve by id, as History does: the list never contradicts History's last row.
func TestListDemotion_DB_TiedTimestampsFollowHistoryOrder(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	f := newCRFixture(t, "ENGI-15-04 tie")
	// seed ties a caused and an uncaused row; the caused row's id sorts last when causedLast.
	seed := func(causedLast bool) string {
		low, high := "00000000-"+uuid.NewString()[9:], "ffffffff-"+uuid.NewString()[9:]
		causedID, otherID := low, high
		if causedLast {
			causedID, otherID = high, low
		}
		inv := seedInvoiceAtStatus(t, super, f.tenant, f.entity, "E15-04-TIE-"+uuid.NewString(), StatusDraft)
		for _, row := range []struct {
			id    string
			cause any
		}{{causedID, bID}, {otherID, nil}} {
			if _, err := super.Exec(context.Background(),
				`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor, changed_at, cause_rule_set_version_id)
				 VALUES ($1, $2, $3, 'validated', 'draft', 'system', '2026-01-01T00:00:00Z', $4)`, row.id, f.tenant, inv, row.cause); err != nil {
				t.Fatalf("seed tied history row: %v", err)
			}
		}
		return inv
	}
	causedLast, causedFirst := seed(true), seed(false)

	list := f.listDemotions(t)
	for _, c := range []struct {
		name    string
		id      string
		wantSet bool
	}{{"caused row sorts last", causedLast, true}, {"caused row sorts first", causedFirst, false}} {
		l, ok := list[c.id]
		if !ok {
			t.Fatalf("%s: invoice %s is not on the list page", c.name, c.id)
		}
		last := f.lastHistory(t, c.id)
		if got := last.Cause != nil; got != c.wantSet {
			t.Fatalf("%s: History's last row cause set = %v, want %v (setup)", c.name, got, c.wantSet)
		}
		if got := l.d != nil; got != c.wantSet {
			t.Errorf("%s: demotion = %s, want set=%v like History", c.name, l.raw, c.wantSet)
		}
		if c.wantSet && (l.d == nil || l.d.Cause == nil || l.d.Cause.RuleSetVersion != bVer) {
			t.Errorf("%s: demotion = %s, want version %d", c.name, l.raw, bVer)
		}
	}
}

// One history row may cite both a version and a custom revision; the list carries both, as History does.
func TestListDemotion_DB_RowCitingBothCausesCarriesBoth(t *testing.T) {
	super, _ := dbTestPools(t)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	f := newCRFixture(t, "ENGI-15-04 both")
	rule := f.save(t, &f.entity, "error", true, "email needed")
	inv := seedInvoiceAtStatus(t, super, f.tenant, f.entity, "E15-04-BOTH", StatusDraft)
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, cause_rule_set_version_id, cause_custom_rule_revision_id)
		 VALUES ($1, $2, 'validated', 'draft', 'system', $3, $4)`, f.tenant, inv, bID, rule.RevisionID); err != nil {
		t.Fatalf("seed history row: %v", err)
	}

	l, ok := f.listDemotions(t)[inv]
	if !ok || l.d == nil || l.d.Cause == nil || l.d.CustomRuleCause == nil {
		t.Fatalf("demotion = %s (on page %v), want both causes", l.raw, ok)
	}
	if l.d.Cause.RuleSetVersion != bVer || *l.d.CustomRuleCause != wantRuleCause(rule, "email needed") {
		t.Errorf("demotion = %+v / %+v, want version %d and the rule's revision", l.d.Cause, l.d.CustomRuleCause, bVer)
	}
	last := f.lastHistory(t, inv)
	if !reflect.DeepEqual(last.Cause, l.d.Cause) || !reflect.DeepEqual(last.CustomRuleCause, l.d.CustomRuleCause) {
		t.Errorf("list causes differ from History's %+v / %+v", last.Cause, last.CustomRuleCause)
	}
}

// An empty ask reads nothing, though the tenant holds a demoted invoice; the control proves the fixture is live.
func TestListDemotion_DB_EmptyPageReadsNothing(t *testing.T) {
	f := newCRFixture(t, "ENGI-15-04 empty")
	id, _ := f.demotedByRule(t, "E15-04-EMPTY")

	if _, gate, err := f.store.RowFacts(f.admin, []string{id}); err != nil || len(gate.Demotion) != 1 {
		t.Fatalf("control: RowFacts([demoted]) = %d entries, %v, want 1", len(gate.Demotion), err)
	}
	for name, ids := range map[string][]string{"empty slice": {}, "nil slice": nil} {
		_, gate, err := f.store.RowFacts(f.admin, ids)
		if err != nil {
			t.Fatalf("RowFacts(%s): %v", name, err)
		}
		if len(gate.Demotion) != 0 {
			t.Errorf("RowFacts(%s) carries %d demotions, want none", name, len(gate.Demotion))
		}
	}
}
