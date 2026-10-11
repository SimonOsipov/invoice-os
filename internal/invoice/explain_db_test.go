package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	explainNoMembershipSubject = "d4a10005-0000-4000-8000-0000000000a1"
	explainSuspendedSubject    = "d4a10005-0000-4000-8000-0000000000a2"
	explainBody                = `{"rule_key":"line-cost-non-negative","path":"line_items[2]"}`
)

// explainDBFixture is a tenant with one invoice: three lines (unit_price 105, -5, 0)
// and a stored verdict naming line 2.
type explainDBFixture struct {
	nrsFixture
	invoiceID string
	fake      *explainRecorder
}

func explainThreeLines() []LineItemInput {
	return []LineItemInput{nrsLine("a", "1", "105.00"), nrsLine("b", "1", "-5.00"), nrsLine("c", "1", "0.00")}
}

func (f nrsFixture) seedVerdict(t *testing.T, invoiceID, violationsJSON string) {
	t.Helper()
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET violations = $1::jsonb, rule_set_version_id = $2 WHERE id = $3`,
		violationsJSON, seedRuleSetVersionID(t, f.super), invoiceID,
	); err != nil {
		t.Fatalf("seed stored verdict: %v", err)
	}
}

func newExplainDB(t *testing.T, label string) explainDBFixture {
	t.Helper()
	f := newNRSFixture(t, label)
	inv := f.create(t, label+"-1", CreateInput{LineItems: explainThreeLines()})
	rec := newExplainRecorder()
	rec.answer = map[string]any{"explanation": "x", "fix_field": "unit_price", "fix_value": "5.00"}
	x := explainDBFixture{nrsFixture: f, invoiceID: inv.ID, fake: rec}
	f.seedVerdict(t, inv.ID, `[{"rule_key":"line-cost-non-negative","severity":"error","message":"`+explainTestMessage+`","path":"line_items[2]"}]`)
	return x
}

func explainIdentity(subject, tenantID string) auth.Identity {
	return auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}
}

// post drives the real handler over the real store.
func (x explainDBFixture) post(t *testing.T, ident auth.Identity, id, body string, client ExplainAI) *httptest.ResponseRecorder {
	t.Helper()
	return doExplain(t, NewExplainer(x.store.Get, client).Explain, nil, &ident, id, body)
}

func (x explainDBFixture) member() auth.Identity { return explainIdentity(memberSubject, x.tenantID) }

func decodeExplain(t *testing.T, rec *httptest.ResponseRecorder) ExplainResult {
	t.Helper()
	var res ExplainResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return res
}

func TestRLS_ExplainReadsTheStoredViolation(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-OK")
	rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	res := decodeExplain(t, rec)
	if res.Status != "ok" || res.Explanation == nil || *res.Explanation != "x" {
		t.Fatalf("result = %+v, want status ok with explanation x", res)
	}
	f := res.Fix
	if f == nil || f.Field != "unit_price" || f.Line == nil || *f.Line != 2 || f.Current == nil || *f.Current != "-5.00" || f.Value != "5.00" {
		t.Errorf("fix = %+v, want unit_price on line 2, -5.00 -> 5.00", f)
	}
	if n := x.fake.callCount(); n != 1 {
		t.Fatalf("AI calls = %d, want 1", n)
	}
	req := x.fake.calls[0]
	if req.Purpose != "explain" || req.FakeScope != "EXPLAIN" || req.SchemaName != explainSchemaName {
		t.Errorf("request = (%q, %q, %q), want (explain, EXPLAIN, %s)", req.Purpose, req.FakeScope, req.SchemaName, explainSchemaName)
	}
	if req.System != explainSystem || !bytes.Equal(req.Schema, explainSchema) {
		t.Error("request carries a system prompt or schema other than the pinned ones")
	}
	for _, want := range []string{`"rule_key":"line-cost-non-negative"`, `"unit_price":-5`} {
		if !strings.Contains(req.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, req.Text)
		}
	}
}

func TestRLS_ExplainCrossTenantIs404AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-XT")
	other := seedTenant(t, x.super, "EXPLAIN-XT other tenant")

	rec := x.post(t, explainIdentity(memberSubject, other), x.invoiceID, explainBody, x.fake)
	if rec.Code != http.StatusNotFound || explainErrorOf(t, rec) != "not found" {
		t.Errorf("cross-tenant: got %d %s, want 404 not found", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("cross-tenant AI calls = %d, want 0", n)
	}
	if rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake); rec.Code != http.StatusOK || x.fake.callCount() != 1 {
		t.Errorf("owner control: status %d, calls %d, want 200 and 1", rec.Code, x.fake.callCount())
	}
}

func TestRLS_ExplainNonMemberIs403AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-NM")
	seedMembershipWithStatus(t, x.super, x.tenantID, explainSuspendedSubject, "preparer", "suspended")

	for name, subject := range map[string]string{"no membership row": explainNoMembershipSubject, "suspended member": explainSuspendedSubject} {
		t.Run(name, func(t *testing.T) {
			assertNotActiveMember403(t, x.post(t, explainIdentity(subject, x.tenantID), x.invoiceID, explainBody, x.fake))
		})
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("non-member AI calls = %d, want 0", n)
	}
	if rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake); rec.Code != http.StatusOK {
		t.Errorf("active member control: status %d, want 200", rec.Code)
	}
}

func TestRLS_ExplainNoTenantIdentityIs401(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-NT")
	if rec := x.post(t, explainIdentity(memberSubject, ""), x.invoiceID, explainBody, x.fake); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

func TestRLS_ExplainViolationNotOnVerdictIs409(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-409")
	rec := x.post(t, x.member(), x.invoiceID, `{"rule_key":"currency-allowed","path":"currency"}`, x.fake)
	if rec.Code != http.StatusConflict || explainErrorOf(t, rec) != "the violation is not on this invoice's last validation" {
		t.Errorf("got %d %s, want 409 with the D5 message", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
	if rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake); rec.Code != http.StatusOK {
		t.Errorf("stored violation control: status %d, want 200", rec.Code)
	}
}

func TestRLS_ExplainNeverValidatedIs409AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-NV")
	fresh := x.create(t, "EXPLAIN-NV-2", CreateInput{LineItems: explainThreeLines()})
	var violations string
	var version *string
	if err := x.super.QueryRow(context.Background(), `SELECT violations::text, rule_set_version_id::text FROM invoices WHERE id = $1`, fresh.ID).Scan(&violations, &version); err != nil {
		t.Fatal(err)
	}
	if violations != "[]" || version != nil {
		t.Fatalf("precondition: violations %s, rule_set_version_id %v, want [] and null", violations, version)
	}
	rec := x.post(t, x.member(), fresh.ID, `{"rule_key":"line-cost-non-negative","path":"line_items[1]"}`, x.fake)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

func TestRLS_ExplainCorruptStoredViolationsIs500AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-500")
	if _, err := x.super.Exec(context.Background(), `UPDATE invoices SET violations = '{}'::jsonb WHERE id = $1`, x.invoiceID); err != nil {
		t.Fatal(err)
	}
	rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake)
	if rec.Code != http.StatusInternalServerError || explainErrorOf(t, rec) != "internal server error" {
		t.Errorf("got %d %s, want 500 internal server error", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

func TestRLS_ExplainMalformedIDIs400AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-MID")
	rec := x.post(t, x.member(), "not-a-uuid", explainBody, x.fake)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

func TestRLS_ExplainLogsTenantPurposeAndCost(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-LOG")
	answer := `{"explanation":"Line 2 has a negative unit price.","fix_field":null,"fix_value":null}`
	steered := x.create(t, "EXPLAIN-LOG-2", CreateInput{LineItems: []LineItemInput{nrsLine("a", "1", "1.00"), nrsLine(explainMarker("EXPLAIN-", answer), "1", "-5.00")}})
	x.seedVerdict(t, steered.ID, `[{"rule_key":"line-cost-non-negative","severity":"error","message":"`+explainTestMessage+`","path":"line_items[2]"}]`)

	var buf bytes.Buffer
	client := explainFakeClient(t, slog.New(slog.NewJSONHandler(&buf, nil)))
	rec := x.post(t, x.member(), steered.ID, explainBody, client)
	if rec.Code != http.StatusOK || decodeExplain(t, rec).Status != "ok" {
		t.Fatalf("got %d %s, want 200 ok", rec.Code, rec.Body.String())
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		if m["msg"] == "ai call" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("ai call lines = %d, want 1:\n%s", len(lines), buf.String())
	}
	got := lines[0]
	if _, hasCost := got["cost"]; got["tenant_id"] != x.tenantID || got["purpose"] != "explain" || !hasCost || got["outcome"] != "fake" {
		t.Errorf("ai call line = %v, want tenant_id %s, purpose explain, a cost key, outcome fake", got, x.tenantID)
	}
}

func TestRLS_ExplainAfterAnEditSinceValidationIs409AndCallsNoAI(t *testing.T) {
	x := newExplainDB(t, "EXPLAIN-STALE")
	vs := []Violation{{RuleKey: "line-cost-non-negative", Severity: "error", Message: explainTestMessage, Path: "line_items[2]"}}
	cur := x.get(t, x.invoiceID)
	if _, err := x.store.ApplyValidation(x.c, x.invoiceID, vs, seedRuleSetVersionID(t, x.super), contentFingerprint(cur, cur.LineItems)); err != nil {
		t.Fatalf("ApplyValidation: %v", err)
	}
	if rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake); rec.Code != http.StatusOK || x.fake.callCount() != 1 {
		t.Fatalf("control before the edit: status %d, calls %d, want 200 and 1 (body=%s)", rec.Code, x.fake.callCount(), rec.Body.String())
	}

	lines := []LineItemInput{nrsLine("a", "1", "105.00"), nrsLine("c", "1", "0.00")}
	if _, err := x.store.Edit(x.c, x.invoiceID, EditInput{LineItems: &lines}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	rec := x.post(t, x.member(), x.invoiceID, explainBody, x.fake)
	if rec.Code != http.StatusConflict || explainErrorOf(t, rec) != "the invoice changed since its last validation; re-validate it first" {
		t.Errorf("got %d %s, want 409 with the re-validate message", rec.Code, rec.Body.String())
	}
	if n := x.fake.callCount(); n != 1 {
		t.Errorf("AI calls = %d, want still 1", n)
	}
	if !x.get(t, x.invoiceID).VerdictStale {
		t.Error("Get.VerdictStale = false after an edit, want true")
	}
}

func TestRLS_VerdictStaleTracksContentNotTheWriter(t *testing.T) {
	f := newNRSFixture(t, "STALE-WRITERS")
	in := CreateInput{LineItems: []LineItemInput{fullNRSLine()}}
	fullNRSHeader(&in)
	inv := f.create(t, "STALE-W-1", in)
	rsv := seedRuleSetVersionID(t, f.super)
	apply := func(vs []Violation) {
		t.Helper()
		cur := f.get(t, inv.ID)
		if _, err := f.store.ApplyValidation(f.c, inv.ID, vs, rsv, contentFingerprint(cur, cur.LineItems)); err != nil {
			t.Fatalf("ApplyValidation: %v", err)
		}
	}
	blocking := []Violation{{RuleKey: "line-cost-non-negative", Severity: "error", Message: "m", Path: "line_items[1]"}}

	if f.get(t, inv.ID).VerdictStale {
		t.Fatal("a never-validated invoice is stale, want false (NULL fingerprint is trusted)")
	}
	apply([]Violation{})
	got := f.get(t, inv.ID)
	if got.Status != StatusValidated || got.VerdictStale {
		t.Fatalf("after a clean validation: status %s stale %v, want validated and fresh (NRS fields must hash the same on both paths)", got.Status, got.VerdictStale)
	}

	demoted, err := demoteRevalidated(f.c, f.app, f.store, f.tenantID, inv.ID, blocking, rsv, "")
	if err != nil || !demoted {
		t.Fatalf("demoteRevalidated: demoted %v err %v, want true and nil", demoted, err)
	}
	if got := f.get(t, inv.ID); got.Status != StatusDraft || got.VerdictStale {
		t.Errorf("after a revalidate demotion: status %s stale %v, want draft and fresh (content did not change)", got.Status, got.VerdictStale)
	}

	renamed := "STALE-W-2"
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{InvoiceNumber: &renamed}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !f.get(t, inv.ID).VerdictStale {
		t.Error("after an invoice-number correction: stale = false, want true")
	}
	orig := inv.InvoiceNumber
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{InvoiceNumber: &orig}); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	if f.get(t, inv.ID).VerdictStale {
		t.Error("after the number is restored: stale = true, want false (same content as the verdict)")
	}

	changed := []LineItemInput{fullNRSLine()}
	changed[0].TaxPercent = strPtr("5")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &changed}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if !f.get(t, inv.ID).VerdictStale {
		t.Fatal("after an NRS line field edit: stale = false, want true")
	}

	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET verdict_fingerprint = NULL WHERE id = $1`, inv.ID); err != nil {
		t.Fatalf("null the fingerprint: %v", err)
	}
	if f.get(t, inv.ID).VerdictStale {
		t.Error("a NULL fingerprint on an edited invoice is stale, want trusted (false)")
	}
}
