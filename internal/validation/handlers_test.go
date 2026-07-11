package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- handler tests (httptest + stubbed engine/store closures, no DB) -----------------
//
// GREEN (Mode A->B, task M3-04-07): this block (through
// TestToggle_MissingEnabled400) is the AC-derived suite authored RED before
// implementation and now passing against handlers.go's real ValidateHandler/
// ToggleHandler (decode/delegate/statusForErr, mirroring
// internal/portfolio/portfolio.go's CreateHandler/GetHandler shape).
// Identity is injected via auth.WithIdentity, exactly like
// portfolio_test.go's doCreate/doUpdate helpers; stub closures capture their
// call args so the happy-path tests aren't vacuous. QA-added adversarial
// coverage (collect-all order, 500 no-leak, toggle enable path, error
// envelope shape) follows below TestToggle_MissingEnabled400.
//
// Contract decisions this file's tests pin for the executor:
//   - ValidateHandler's request body is {"invoice": {...}} decoded into a
//     Payload; a malformed (not-valid-JSON) body -> 400.
//   - ToggleHandler's request body is {"enabled": bool} decoded via a *bool
//     field, so an ABSENT "enabled" key (body "{}") is distinguishable from
//     an explicit {"enabled":false} -- the former is 400
//     (TestToggle_MissingEnabled400), the latter is a valid false-toggle
//     request.
//   - ToggleHandler's 200 response includes the updated rule's "key" and
//     "enabled" fields (snake_case, per rule.go's documented
//     {key,type,target,params,severity,when,message,scope,enabled} shape).
//     Rule itself has no JSON tags today -- the executor either tags Rule
//     directly or wraps it in an equivalent DTO; ruleBody below only
//     decodes the two fields these tests need.

// validateResponseBody decodes ValidateHandler's success body
// ({"rule_set_version":...,"violations":[...]}) or its flat error envelope
// ({"error":...}) -- both are read through the same struct since Go's
// decoder leaves absent fields at their zero value.
type validateResponseBody struct {
	RuleSetVersion int         `json:"rule_set_version"`
	Violations     []Violation `json:"violations"`
	Error          string      `json:"error"`
}

// ruleBody decodes ToggleHandler's success body far enough to assert the
// key/enabled fields these tests need (see contract decision above), or its
// flat error envelope.
type ruleBody struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error"`
}

// doValidate issues a POST /v1/validate request with a raw JSON body (raw,
// not marshaled from a struct, so callers can pass literal malformed JSON
// for the bad-body test) through ValidateHandler.
func doValidate(t *testing.T, loadAndEval func(ctx context.Context, p Payload) (Result, error), id *auth.Identity, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/validate", strings.NewReader(rawBody))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ValidateHandler(loadAndEval, nil).ServeHTTP(rec, r)
	return rec
}

// doToggle issues a PATCH /v1/rules/{key} request through ToggleHandler,
// with r.PathValue("key") set directly (ServeHTTP is called without a mux).
func doToggle(t *testing.T, toggle func(ctx context.Context, key string, enabled bool) (Rule, error), id *auth.Identity, key, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/v1/rules/"+key, strings.NewReader(rawBody))
	r.SetPathValue("key", key)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ToggleHandler(toggle, nil).ServeHTTP(rec, r)
	return rec
}

// TestValidate_NoIdentity401: no identity in the request context must 401
// before loadAndEval ever runs -- asserted by failing the test if
// loadAndEval is called.
func TestValidate_NoIdentity401(t *testing.T) {
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		t.Fatal("loadAndEval must not run without an identity")
		return Result{}, nil
	}
	rec := doValidate(t, loadAndEval, nil, `{"invoice":{"total":100}}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "unauthorized" {
		t.Errorf(`error = %q, want "unauthorized"`, body["error"])
	}
}

// TestValidate_Happy200: identity present, a valid {"invoice":{...}} body,
// and a stubbed loadAndEval returning a Result with 2 violations must
// produce 200 with rule_set_version and both violations round-tripped in
// the response -- and loadAndEval must have been called with the decoded
// invoice payload (not vacuously skipped).
func TestValidate_Happy200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Result{
		RuleSetVersion: 3,
		Violations: []Violation{
			{RuleKey: "supplier.tin.required", Severity: "error", Message: "supplier TIN is required", Path: "supplier.tin"},
			{RuleKey: "totals.tax_math", Severity: "warning", Message: "tax total does not match computed VAT", Path: "totals.tax"},
		},
	}
	var gotPayload Payload
	called := false
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		called = true
		gotPayload = p
		return want, nil
	}
	rec := doValidate(t, loadAndEval, &id, `{"invoice":{"supplier":{"tin":""},"totals":{"tax":10}}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("loadAndEval was not called")
	}
	inv, ok := gotPayload["invoice"].(map[string]any)
	if !ok {
		t.Fatalf("loadAndEval called with unexpected payload: %+v (want a Payload with the decoded \"invoice\" object, unwrapped)", gotPayload)
	}
	supplier, ok := inv["supplier"].(map[string]any)
	if !ok || supplier["tin"] != "" {
		t.Errorf("loadAndEval's payload[\"invoice\"] = %+v, want supplier.tin present and empty", inv)
	}

	var body validateResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.RuleSetVersion != 3 {
		t.Errorf("rule_set_version = %d, want 3", body.RuleSetVersion)
	}
	if len(body.Violations) != 2 {
		t.Fatalf("violations count = %d, want 2 (body=%+v)", len(body.Violations), body)
	}
	if body.Violations[0].RuleKey != "supplier.tin.required" || body.Violations[0].Severity != "error" || body.Violations[0].Message == "" {
		t.Errorf("violations[0] = %+v, want rule_key=supplier.tin.required severity=error with a message", body.Violations[0])
	}
	if body.Violations[1].RuleKey != "totals.tax_math" || body.Violations[1].Severity != "warning" || body.Violations[1].Message == "" {
		t.Errorf("violations[1] = %+v, want rule_key=totals.tax_math severity=warning with a message", body.Violations[1])
	}
}

// TestValidate_BadBody400: a body that is not valid JSON must 400 before
// loadAndEval ever runs -- asserted by failing the test if loadAndEval is
// called.
func TestValidate_BadBody400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		t.Fatal("loadAndEval must not run when the request body is not valid JSON")
		return Result{}, nil
	}
	rec := doValidate(t, loadAndEval, &id, `{"invoice":`) // truncated -- not valid JSON

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidate_NoRuleSet503: the stubbed loadAndEval returning
// ErrNoActiveRuleSet must map to 503 with a non-empty error message in the
// flat {"error":...} envelope -- the exact message string is the executor's
// to choose, this only pins the status + envelope shape.
func TestValidate_NoRuleSet503(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		return Result{}, ErrNoActiveRuleSet
	}
	rec := doValidate(t, loadAndEval, &id, `{"invoice":{"total":100}}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestToggle_NoIdentity401: no identity in the request context must 401
// before toggle ever runs -- asserted by failing the test if toggle is
// called. (Symmetry with TestValidate_NoIdentity401.)
func TestToggle_NoIdentity401(t *testing.T) {
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		t.Fatal("toggle must not run without an identity")
		return Rule{}, nil
	}
	rec := doToggle(t, toggle, nil, "R", `{"enabled":false}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestToggle_Happy200: identity present, a stubbed toggle returning an
// updated Rule, must produce 200 with the response reflecting the updated
// rule -- and toggle must have been called with the path's key and the
// decoded "enabled" value (not vacuously skipped).
func TestToggle_Happy200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Rule{Key: "R", Type: TypeRequired, Target: "supplier.tin", Severity: "error", Message: "supplier TIN is required", Scope: "document", Enabled: false}
	var gotKey string
	var gotEnabled bool
	called := false
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		called = true
		gotKey = key
		gotEnabled = enabled
		return want, nil
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("toggle was not called")
	}
	if gotKey != "R" {
		t.Errorf("toggle called with key = %q, want %q", gotKey, "R")
	}
	if gotEnabled {
		t.Error("toggle called with enabled = true, want false")
	}

	var body ruleBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.Key != "R" {
		t.Errorf("key = %q, want %q", body.Key, "R")
	}
	if body.Enabled {
		t.Error("enabled = true, want false")
	}
}

// TestToggle_Redundant409: the stubbed toggle returning
// ErrRedundantTransition (already at the requested target) must map to 409.
func TestToggle_Redundant409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		return Rule{}, ErrRedundantTransition
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":false}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestToggle_Unknown404: the stubbed toggle returning ErrNotFound (no rule
// under the active version matches the path key) must map to 404.
func TestToggle_Unknown404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		return Rule{}, ErrNotFound
	}
	rec := doToggle(t, toggle, &id, "Z", `{"enabled":false}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestToggle_MissingEnabled400: a body of "{}" (the "enabled" key absent, as
// opposed to explicitly {"enabled":false}) must 400 before toggle ever runs
// -- asserted by failing the test if toggle is called. Pins the contract
// decision that the handler decodes "enabled" into a *bool field so
// "absent" and "false" are distinguishable. The message is "enabled is
// required" -- distinct from the malformed-JSON case's "invalid request
// body" (see TestToggle_MalformedJSON400).
func TestToggle_MissingEnabled400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		t.Fatal("toggle must not run when \"enabled\" is absent from the body")
		return Rule{}, nil
	}
	rec := doToggle(t, toggle, &id, "R", `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "enabled is required" {
		t.Errorf(`error = %q, want "enabled is required"`, body["error"])
	}
}

// TestToggle_MalformedJSON400 (regression, CodeRabbit): a malformed
// (truncated) JSON body -- as opposed to the well-formed-but-missing-
// "enabled" "{}" of TestToggle_MissingEnabled400 -- must 400 with the
// "invalid request body" message (the same message ValidateHandler uses for
// a bad body), NOT the "enabled is required" message reserved for the
// missing-key case. Toggle must never run.
func TestToggle_MalformedJSON400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		t.Fatal("toggle must not run when the request body is not valid JSON")
		return Rule{}, nil
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":`) // truncated -- not valid JSON

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "invalid request body" {
		t.Errorf(`error = %q, want "invalid request body"`, body["error"])
	}
}

// --- QA adversarial coverage (Mode B, M3-04-07) -----------------------------
//
// The tests above are the executor's committed (RED->GREEN) AC suite. The
// tests below are QA-added: collect-ALL pass-through order (story Core AC
// #2/#4), the 500 no-leak contract, the toggle enable direction (existing
// coverage only exercised disable), toggle's own 503, and the flat
// {"error":...} envelope shape asserted structurally rather than just
// non-empty.

// TestValidate_CollectAllOrderPreserved: the stub returns 3 violations in a
// deliberately NON-alphabetical rule-key order (z, a, m) with distinct
// severities/messages/paths. The response must reproduce that exact order
// and every field verbatim -- proving ValidateHandler is a pure pass-through
// of loadAndEval's Result and does NOT re-sort or otherwise mutate it. (The
// engine's own deterministic sort -- Decision N16 -- is Evaluate's job, one
// layer below this stub; the handler must not duplicate or second-guess it.)
func TestValidate_CollectAllOrderPreserved(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Result{
		RuleSetVersion: 7,
		Violations: []Violation{
			{RuleKey: "zzz.rule", Severity: "info", Message: "third msg", Path: "z.path"},
			{RuleKey: "aaa.rule", Severity: "error", Message: "first msg", Path: "a.path"},
			{RuleKey: "mmm.rule", Severity: "warning", Message: "second msg", Path: "m.path"},
		},
	}
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		return want, nil
	}
	rec := doValidate(t, loadAndEval, &id, `{"invoice":{"total":100}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body validateResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.RuleSetVersion != 7 {
		t.Errorf("rule_set_version = %d, want 7", body.RuleSetVersion)
	}
	if len(body.Violations) != 3 {
		t.Fatalf("violations count = %d, want 3 (body=%+v)", len(body.Violations), body)
	}
	if diff := cmpViolations(body.Violations, want.Violations); diff != "" {
		t.Errorf("violations not passed through verbatim (order+fields): %s", diff)
	}
}

// cmpViolations reports a human-readable diff if got != want, element by
// element and in order -- a plain reflect.DeepEqual failure message doesn't
// show WHICH element/order broke, which matters for an order-sensitive
// assertion.
func cmpViolations(got, want []Violation) string {
	if len(got) != len(want) {
		return fmt.Sprintf("len(got)=%d len(want)=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Sprintf("index %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	return ""
}

// TestValidate_GenericErrorIs500: loadAndEval returning an error that is
// none of the recognized sentinels must map to 500 with the generic body --
// and, critically, the raw error string must NOT leak into the response
// (statusForErr's default case never echoes err.Error()).
func TestValidate_GenericErrorIs500(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		return Result{}, errors.New("boom: rule-set jsonb decode exploded")
	}
	rec := doValidate(t, loadAndEval, &id, `{"invoice":{"total":100}}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "internal server error" {
		t.Errorf(`error = %q, want "internal server error"`, body["error"])
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "jsonb") {
		t.Errorf("response body leaked the raw error: %s", rec.Body.String())
	}
}

// TestValidate_EmptyBodyIs400: a completely empty request body (io.EOF from
// the decoder, distinct from TestValidate_BadBody400's truncated-but-nonempty
// malformed JSON) must also 400 before loadAndEval ever runs.
func TestValidate_EmptyBodyIs400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
		t.Fatal("loadAndEval must not run when the request body is empty")
		return Result{}, nil
	}
	rec := doValidate(t, loadAndEval, &id, ``)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestToggle_EnablePath: the existing committed TestToggle_Happy200 only
// exercises the disable direction ({"enabled":false}); this covers the
// enable direction ({"enabled":true}) -- toggle must be called with
// enabled==true and the 200 response's rule must have enabled==true.
func TestToggle_EnablePath(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Rule{Key: "R", Type: TypeRequired, Target: "supplier.tin", Severity: "error", Message: "supplier TIN is required", Scope: "document", Enabled: true}
	var gotKey string
	var gotEnabled bool
	called := false
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		called = true
		gotKey = key
		gotEnabled = enabled
		return want, nil
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("toggle was not called")
	}
	if gotKey != "R" {
		t.Errorf("toggle called with key = %q, want %q", gotKey, "R")
	}
	if !gotEnabled {
		t.Error("toggle called with enabled = false, want true")
	}

	var body ruleBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if !body.Enabled {
		t.Error("enabled = false, want true")
	}
}

// TestToggle_GenericErrorIs500: toggle returning an error that is none of
// the recognized sentinels must map to 500 with the generic body, and the
// raw error string must NOT leak into the response.
func TestToggle_GenericErrorIs500(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		return Rule{}, errors.New("boom: connection reset by peer")
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":true}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "internal server error" {
		t.Errorf(`error = %q, want "internal server error"`, body["error"])
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "peer") {
		t.Errorf("response body leaked the raw error: %s", rec.Body.String())
	}
}

// TestToggle_NoActiveRuleSet503: toggle can also hit "no active rule-set"
// (ToggleRule re-derives the active version internally, same as
// LoadActiveRuleSet) -- must map to 503, same as ValidateHandler's
// TestValidate_NoRuleSet503.
func TestToggle_NoActiveRuleSet503(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
		return Rule{}, ErrNoActiveRuleSet
	}
	rec := doToggle(t, toggle, &id, "R", `{"enabled":true}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestHandlers_ErrorEnvelopeShape: every error response across both handlers
// must be the FLAT {"error": "<msg>"} envelope -- exactly one key, a string
// value -- with Content-Type: application/json, not e.g. a nested object or
// an array. Checked structurally (not just "non-empty") against one
// ValidateHandler response and one ToggleHandler response.
func TestHandlers_ErrorEnvelopeShape(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	t.Run("validate 404", func(t *testing.T) {
		loadAndEval := func(ctx context.Context, p Payload) (Result, error) {
			return Result{}, ErrNotFound
		}
		rec := doValidate(t, loadAndEval, &id, `{"invoice":{"total":100}}`)
		assertFlatErrorEnvelope(t, rec)
	})

	t.Run("toggle 409", func(t *testing.T) {
		toggle := func(ctx context.Context, key string, enabled bool) (Rule, error) {
			return Rule{}, ErrRedundantTransition
		}
		rec := doToggle(t, toggle, &id, "R", `{"enabled":true}`)
		assertFlatErrorEnvelope(t, rec)
	})
}

// assertFlatErrorEnvelope asserts rec's body decodes to a JSON object with
// EXACTLY one key ("error") holding a string, and that Content-Type is
// application/json.
func assertFlatErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("body has %d keys, want exactly 1 (%q): %+v", len(body), "error", body)
	}
	msg, ok := body["error"].(string)
	if !ok || msg == "" {
		t.Errorf(`body["error"] = %#v, want a non-empty string`, body["error"])
	}
}
