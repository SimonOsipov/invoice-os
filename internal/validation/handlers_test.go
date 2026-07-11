package validation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- handler tests (httptest + stubbed engine/store closures, no DB) -----------------
//
// RED-stage (Mode A, task M3-04-07): handlers.go's ValidateHandler/
// ToggleHandler both unconditionally answer 501 and never call their
// closure, so every assertion below fails on the STATUS check first (got
// 501, want 200/401/400/...) -- the executor's real decode/delegate/
// statusForErr logic (mirroring internal/portfolio/portfolio.go's
// CreateHandler/GetHandler shape) turns these green without changing this
// file. Identity is injected via auth.WithIdentity, exactly like
// portfolio_test.go's doCreate/doUpdate helpers; stub closures capture their
// call args so the happy-path tests aren't vacuous.
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
	supplier, ok := gotPayload["supplier"].(map[string]any)
	if !ok || supplier["tin"] != "" {
		t.Errorf("loadAndEval called with unexpected payload: %+v (want the decoded \"invoice\" object)", gotPayload)
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
// "absent" and "false" are distinguishable.
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
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}
