package validation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- handler tests (httptest + stubbed store closures, no DB) ----------------
//
// Identity is injected via auth.WithIdentity, exactly like portfolio_test.go's
// doCreate/doUpdate helpers; stub closures capture their call args so the
// happy-path tests aren't vacuous.
//
// Contract decisions this file's tests pin for the executor:
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

// ruleBody decodes ToggleHandler's success body far enough to assert the
// key/enabled fields these tests need (see contract decision above), or its
// flat error envelope.
type ruleBody struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error"`
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

// TestToggle_NoIdentity401: no identity in the request context must 401
// before toggle ever runs -- asserted by failing the test if toggle is
// called.
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
// "invalid request body" message, NOT the "enabled is required" message
// reserved for the missing-key case. Toggle must never run.
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
// tests below are QA-added: the toggle enable direction (existing coverage
// only exercised disable), toggle's own 503, and the flat {"error":...}
// envelope shape asserted structurally rather than just non-empty.

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
// LoadActiveRuleSet) -- must map to 503.
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

// TestHandlers_ErrorEnvelopeShape: every error response must be the FLAT
// {"error": "<msg>"} envelope -- exactly one key, a string value -- with
// Content-Type: application/json, not e.g. a nested object or an array.
// Checked structurally (not just "non-empty") against a ToggleHandler response.
func TestHandlers_ErrorEnvelopeShape(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

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
