// QA Mode-B adversarial coverage for task-110 / M4-04-04 ("03: the
// validation client"), added AFTER implementation (validator.go exists and
// VC-01..14 are green) per the QA agent's own charter: extend coverage the
// Test-first red specs did not include. Gaps identified against the
// Stage-1 addendum's "Audit HARD" list and independent mutation testing of
// this file's central safety property (validator.go's own header: "a
// failure is NEVER laundered into a verdict"):
//
//   - AC#5's untested half (flagged, not covered, by Stage-1 Mode A): no VC
//     exercises NewValidator's nil-hc -> default-client path. Every VC-*
//     test calls NewValidator(url, token, nil) and happens to work because
//     httptest answers fast, but nothing asserts the resulting client
//     carries defaultValidateTimeout rather than some other value (e.g. a
//     silent regression to http.DefaultClient's zero Timeout).
//   - A genuine coverage gap found by mutation-testing Validate's totality
//     check: it is TWO checks (count equality, then per-ref presence), and
//     every existing VC scenario that trips totality does so via the COUNT
//     check alone (VC-13 sends 2 items, gets 1 result back -- count
//     mismatch, caught before the per-ref loop ever runs). No VC exercises
//     a response where the COUNT matches but the ref SET does not (a
//     dropped ref masked by an unexpected one) -- the only scenario that
//     actually requires the second (per-ref) loop. Verified by mutation:
//     deleting the per-ref loop alone (keeping the count check) leaves all
//     14 VC-* tests green; TestValidatorClient_RefSwapSameCountMapsToErrUpstream
//     below is the one test that goes red.
//   - Duplicate ref in the response: reasoned about in validator.go's
//     comment ("a duplicate ... collapses ... and so trips the count
//     check") but never exercised by a VC-*.
//   - F1's tag contract, tested directly rather than through the full
//     Validate() HTTP round trip: a canary that unmarshals a verbatim
//     04-shaped JSON literal straight onto the wire structs, so it fails
//     the moment anyone strips a json tag, independent of any transport
//     concern.
//   - The "structural, not enumerated" claim about the closed status
//     switch (Stage-1 F2): a status 04 does not ship today (429) must still
//     default to ErrUpstream.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidatorClient_NilClientDefaultsToTimeoutNotDefaultClient (AC#5,
// Stage-1 F4, untested half flagged in Mode A): NewValidator(url, token,
// nil) must produce a client carrying defaultValidateTimeout, and it must
// not be http.DefaultClient (which has Timeout == 0, i.e. no bound at all).
func TestValidatorClient_NilClientDefaultsToTimeoutNotDefaultClient(t *testing.T) {
	v := NewValidator("http://example.invalid", "tok", nil)

	if v.hc == nil {
		t.Fatal("NewValidator with nil hc left v.hc nil -- every request would panic on v.hc.Do")
	}
	if v.hc == http.DefaultClient {
		t.Error("NewValidator with nil hc defaulted to http.DefaultClient, which has NO timeout -- " +
			"an unbounded hang on the import path is a 500-invoice outage [AC#5]")
	}
	if v.hc.Timeout != defaultValidateTimeout {
		t.Errorf("v.hc.Timeout = %v, want defaultValidateTimeout (%v) -- the nil->default path must carry "+
			"the explicit, sized timeout, not some other value [AC#5, Stage-1 F4]", v.hc.Timeout, defaultValidateTimeout)
	}
}

// TestValidatorClient_InjectedClientIsNotOverridden: a caller-supplied hc
// must be used as-is -- NewValidator must not silently replace a non-nil
// client (which would defeat VC-08's whole construction of injecting a
// millisecond-timeout client to keep CI fast).
func TestValidatorClient_InjectedClientIsNotOverridden(t *testing.T) {
	custom := &http.Client{Timeout: 7}
	v := NewValidator("http://example.invalid", "tok", custom)
	if v.hc != custom {
		t.Error("NewValidator replaced a non-nil injected client -- only a nil hc should default")
	}
}

// TestValidatorClient_DuplicateRefInResponseMapsToErrUpstream (Stage-1 F5,
// validator.go's own comment on the totality check): 04 answers 200 but
// reports the SAME ref twice and never reports the other sent ref. The
// duplicate collapses in the map, so the count check (len(byRef) !=
// len(items)) must catch it -- this is the scenario validator.go's comment
// claims needs "no separate duplicate scan"; this test proves that claim
// rather than trusting the comment.
func TestValidatorClient_DuplicateRefInResponseMapsToErrUpstream(t *testing.T) {
	// cannedRuleSetVersion (declared in validator_test.go, same package) is
	// injected via fmt.Sprintf's %d, never a literal digit adjacent to
	// "rule_set_version" in the SOURCE TEXT -- see validator_test.go's own
	// comment on why: it would trip TestRuleSetV2_JSONQuotedVersionPinNotPresent
	// (the F7 safety net, task-111 QA Mode B).
	body := fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"x","results":[`+
		`{"ref":"inv-1","violations":[]},{"ref":"inv-1","violations":[{"rule_key":"r","severity":"error","message":"m"}]}]}`, cannedRuleSetVersion)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{
		{Ref: "inv-1", Invoice: map[string]any{}},
		{Ref: "inv-2", Invoice: map[string]any{}},
	})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- the response reported \"inv-1\" TWICE and never reported "+
			"\"inv-2\"; a duplicate must not silently pass totality [Stage-1 F5]", err)
	}
	if err == nil {
		if _, ok := result.ByRef["inv-2"]; !ok {
			t.Errorf("ByRef has no entry for inv-2 (never reported by 04, masked by the inv-1 duplicate) -- " +
				"reads as a clean verdict on an invoice 04 never judged [Stage-1 F5]")
		}
	}
}

// TestValidatorClient_RefSwapSameCountMapsToErrUpstream: 04 answers 200
// with the RIGHT COUNT of results but the WRONG SET of refs -- one sent ref
// ("inv-2") is silently dropped and replaced by a ref nobody sent
// ("inv-99"). The count check alone (len(byRef) == len(items), 2 == 2)
// CANNOT catch this; only the per-ref presence loop can. No VC-* spec
// exercises this shape (VC-13's "missing ref" case is a count mismatch, 1
// result for 2 sent items, caught before the per-ref loop ever runs) --
// verified by mutation: deleting the per-ref loop (keeping only the count
// check) leaves all 14 VC-* tests green and turns only this test red.
func TestValidatorClient_RefSwapSameCountMapsToErrUpstream(t *testing.T) {
	// See TestValidatorClient_DuplicateRefInResponseMapsToErrUpstream above for
	// why this is fmt.Sprintf'd rather than a literal digit.
	body := fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"x","results":[`+
		`{"ref":"inv-1","violations":[]},{"ref":"inv-99","violations":[]}]}`, cannedRuleSetVersion)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{
		{Ref: "inv-1", Invoice: map[string]any{}},
		{Ref: "inv-2", Invoice: map[string]any{}},
	})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- the response has the RIGHT count (2) but the WRONG ref set "+
			"(\"inv-2\" dropped, \"inv-99\" unexpected); only the per-ref presence loop catches this, and "+
			"nothing else does [Stage-1 F5, coverage gap found by mutation testing]", err)
	}
	if err == nil {
		if _, ok := result.ByRef["inv-2"]; !ok {
			t.Errorf("ByRef has no entry for inv-2 -- silently dropped by 04 and masked by the count matching " +
				"via the unexpected inv-99, yet Validate returned nil error")
		}
	}
}

// TestValidatorClient_WireTagsSurviveDirectUnmarshal (Stage-1 F1 canary):
// unmarshals a verbatim, 04-shaped JSON literal (multi-word snake_case
// fields throughout) DIRECTLY onto the package's wire structs -- no HTTP
// round trip, no Validate() -- so this fails immediately, and specifically,
// if anyone ever strips a json tag from validateBatchResponse,
// validateBatchItemResult, or Violation. F1's danger is that the failure is
// PARTIAL: Severity/Message (single-word) decode fine even untagged, so
// blocking still appears to work while RuleKey/RuleSetVersion/
// RuleSetVersionID silently zero -- this test checks every multi-word field
// by name specifically because a partial failure hiding behind passing
// single-word fields is what makes F1 dangerous.
func TestValidatorClient_WireTagsSurviveDirectUnmarshal(t *testing.T) {
	// fmt.Sprintf'd, not a literal digit adjacent to "rule_set_version" in the
	// source text -- see TestValidatorClient_DuplicateRefInResponseMapsToErrUpstream's
	// comment above for why (F7 / task-111 QA Mode B).
	body := fmt.Sprintf(`{
		"rule_set_version": %d,
		"rule_set_version_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"results": [
			{"ref": "inv-1", "violations": [
				{"rule_key": "line-items-required", "severity": "error", "message": "boom", "path": "line_items"}
			]}
		]
	}`, cannedRuleSetVersion)
	var wire validateBatchResponse
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if wire.RuleSetVersion != 2 {
		t.Errorf("RuleSetVersion = %d, want 2 -- validateBatchResponse.RuleSetVersion must carry "+
			"`json:\"rule_set_version\"` [Stage-1 F1 canary]", wire.RuleSetVersion)
	}
	if wire.RuleSetVersionID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("RuleSetVersionID = %q, want the batch uuid -- validateBatchResponse.RuleSetVersionID must "+
			"carry `json:\"rule_set_version_id\"` [Stage-1 F1 canary]", wire.RuleSetVersionID)
	}
	if len(wire.Results) != 1 {
		t.Fatalf("Results has %d items, want 1 -- validateBatchResponse.Results must carry `json:\"results\"`", len(wire.Results))
	}
	got := wire.Results[0]
	if got.Ref != "inv-1" {
		t.Errorf("Results[0].Ref = %q, want %q", got.Ref, "inv-1")
	}
	if len(got.Violations) != 1 {
		t.Fatalf("Results[0].Violations has %d items, want 1", len(got.Violations))
	}
	v := got.Violations[0]
	if v.RuleKey != "line-items-required" {
		t.Errorf("Violations[0].RuleKey = %q, want %q -- Violation.RuleKey must carry `json:\"rule_key\"`; "+
			"this is the exact field F1 warns silently zeros without the tag while Severity/Message keep "+
			"decoding fine [Stage-1 F1 canary]", v.RuleKey, "line-items-required")
	}
	if v.Severity != "error" {
		t.Errorf("Violations[0].Severity = %q, want %q", v.Severity, "error")
	}
	if v.Message != "boom" {
		t.Errorf("Violations[0].Message = %q, want %q", v.Message, "boom")
	}
	if v.Path != "line_items" {
		t.Errorf("Violations[0].Path = %q, want %q -- Violation.Path must carry `json:\"path,omitempty\"`", v.Path, "line_items")
	}
}

// TestValidatorClient_RequestMarshalsWireTagsToo (F1 canary, request side):
// mirrors the response-side canary above but for validateBatchRequest /
// ValidateItem -- marshals the real struct and checks the wire keys
// directly, independent of VC-10's httptest round trip.
func TestValidatorClient_RequestMarshalsWireTagsToo(t *testing.T) {
	req := validateBatchRequest{Invoices: []ValidateItem{
		{Ref: "inv-1", Invoice: map[string]any{"a": float64(1)}},
	}}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	invoices, ok := decoded["invoices"].([]any)
	if !ok || len(invoices) != 1 {
		t.Fatalf("marshaled body missing \"invoices\" array (body=%s)", b)
	}
	item, ok := invoices[0].(map[string]any)
	if !ok {
		t.Fatalf("invoices[0] is not an object (body=%s)", b)
	}
	if item["ref"] != "inv-1" {
		t.Errorf("invoices[0].ref = %v, want %q -- ValidateItem.Ref must carry `json:\"ref\"` (body=%s)", item["ref"], "inv-1", b)
	}
	if _, ok := item["invoice"]; !ok {
		t.Errorf("invoices[0] missing \"invoice\" key -- ValidateItem.Invoice must carry `json:\"invoice\"` (body=%s)", b)
	}
}

// TestValidatorClient_UnexpectedResponseStatusDefaultsToErrUpstream
// (Stage-1 F2's "structural rather than enumerated" claim): a status 04
// does not ship TODAY (429) must still map to ErrUpstream via the switch's
// default case, never fall through to a silent decode. This is the
// "any status 04 grows tomorrow is safe by default" property from
// validator.go's own comment, exercised directly rather than trusted.
func TestValidatorClient_UnexpectedResponseStatusDefaultsToErrUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a status 04 does not ship today (429) must still default to "+
			"ErrUpstream via the closed switch, not decode into a clean result [Stage-1 F2]", err)
	}
	if err == nil {
		t.Errorf("a 429 produced a nil error with ByRef = %v -- the switch must be closed against ANY "+
			"unmapped status, not just the ones 04 ships today", result.ByRef)
	}
}
