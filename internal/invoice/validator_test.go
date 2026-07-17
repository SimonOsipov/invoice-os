// task-110 / M4-04-04 ("03: the validation client") -- RED tests VC-01..14,
// authored BEFORE internal/invoice/validator.go exists (Test-first: yes).
// Every test below currently runs against validator_qa_scaffold.go, a
// DELIBERATELY WRONG placeholder (see that file's header for the full list
// of anti-patterns it embodies) -- its only job is to let this file compile
// so each spec fails on a REAL assertion instead of an undefined symbol.
// The executor deletes validator_qa_scaffold.go and writes the real
// validator.go; every test here should then go green unmodified.
//
// Spec-to-test map (task-110's Test Specs table + Stage-1 addendum VC-11..14):
//
//	VC-01 TestValidatorClient_BatchDecodesToByRefAndVersion
//	VC-02 TestValidatorClient_SendsS2STokenNoIdentityHeaders
//	VC-03 TestValidatorClient_500MapsToErrUpstream
//	VC-04 TestValidatorClient_503MapsToDistinguishableNoActiveRuleSet
//	VC-05 TestValidatorClient_401MapsToErrUpstream
//	VC-06 TestValidatorClient_ClosedConnectionMapsToErrUpstream
//	VC-07 TestValidatorClient_200NonJSONBodyMapsToErrUpstream
//	VC-08 TestValidatorClient_TimeoutReturnsErrorNotHang
//	VC-09 TestValidatorClient_NullViolationsMapsToEmptySlice
//	VC-10 TestValidatorClient_RequestBodyPreservesOrderAndShape
//	VC-11 TestValidatorClient_400MapsToErrUpstream
//	VC-12 TestValidatorClient_413MapsToErrUpstream
//	VC-13 TestValidatorClient_MissingRefInResponseMapsToErrUpstream
//	VC-14 TestValidatorClient_DoesNotImportValidationPackage
//
// F1's test trap (binding, Stage-1 addendum): every canned 04 response
// below is a VERBATIM JSON STRING LITERAL, copied field-for-field from
// internal/validation/handlers.go's shipped batchResponse/writeError wire
// shapes -- NEVER json.Marshal(ValidateResult{...}). Marshal-then-unmarshal
// would round-trip through the SAME (possibly wrong) field names as the
// client under test and pass green even against a broken client.
package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// cannedRuleSetVersion is a placeholder rule-set version number used only
// by the canned httptest.Server fixtures below. These are pure/in-process
// unit tests against a fake 04, never the real DB, so the actual integer is
// arbitrary -- F1 (Stage-1 addendum) requires the JSON KEYS/shape to be
// verbatim, not that any specific version number be baked into this file's
// source text. Injected via fmt.Sprintf's %d, never a literal digit
// adjacent to "rule_set_version" in the SOURCE TEXT, so these fixtures do
// not trip TestRuleSetV2_JSONQuotedVersionPinNotPresent
// (internal/validation/rule_set_v2_qa_test.go, the F7 safety net added in
// task-111 QA Mode B) -- that repo-wide guard bans exactly a hardcoded
// `"rule_set_version": <digit>` literal anywhere outside its own file,
// precisely because such a literal would silently go stale on the next
// rule-set publish. See this file's return-to-orchestrator notes: F1
// (verbatim JSON literals) and F7 (no hardcoded version-number literals)
// are in direct tension for any 03-side client fixture that needs to
// simulate 04's real wire response; parameterizing the value is how this
// file satisfies both.
const cannedRuleSetVersion = 2

// TestValidatorClient_BatchDecodesToByRefAndVersion (VC-01): a 200 with a
// canned 2-item batch response maps to ByRef keyed by the sent refs, with
// violations decoded correctly (incl. the multi-word rule_key field) and
// the batch's rule_set_version / rule_set_version_id populated.
func TestValidatorClient_BatchDecodesToByRefAndVersion(t *testing.T) {
	// Verbatim JSON literal matching internal/validation/handlers.go's
	// batchResponse shape -- see file header, this must NOT be built via
	// json.Marshal(ValidateResult{...}).
	body := fmt.Sprintf(`{
		"rule_set_version": %d,
		"rule_set_version_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"results": [
			{"ref": "inv-1", "violations": []},
			{"ref": "inv-2", "violations": [
				{"rule_key": "line-items-required", "severity": "error", "message": "boom", "path": "line_items"}
			]}
		]
	}`, cannedRuleSetVersion)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "test-token", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{
		{Ref: "inv-1", Invoice: map[string]any{"a": 1}},
		{Ref: "inv-2", Invoice: map[string]any{"a": 2}},
	})
	if err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
	if result.RuleSetVersion != 2 {
		t.Errorf("RuleSetVersion = %d, want 2 -- multi-word snake_case top-level field [Stage-1 F1]", result.RuleSetVersion)
	}
	if result.RuleSetVersionID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("RuleSetVersionID = %q, want the batch uuid -- multi-word snake_case top-level field [Stage-1 F1]", result.RuleSetVersionID)
	}
	wantRefs := []string{"inv-1", "inv-2"}
	for _, ref := range wantRefs {
		if _, ok := result.ByRef[ref]; !ok {
			t.Errorf("ByRef missing key %q -- ByRef keys must equal the refs sent [AC#1]", ref)
		}
	}
	if len(result.ByRef) != len(wantRefs) {
		t.Errorf("ByRef has %d keys, want exactly %d", len(result.ByRef), len(wantRefs))
	}
	if len(result.ByRef["inv-1"]) != 0 {
		t.Errorf("ByRef[inv-1] = %v, want zero violations (clean invoice)", result.ByRef["inv-1"])
	}
	got := result.ByRef["inv-2"]
	if len(got) != 1 {
		t.Fatalf("ByRef[inv-2] has %d violations, want 1 (body=%s)", len(got), body)
	}
	if got[0].RuleKey != "line-items-required" {
		t.Errorf("ByRef[inv-2][0].RuleKey = %q, want %q -- rule_key is a multi-word snake_case field; "+
			"missing json tags silently drop it on decode [Stage-1 F1]", got[0].RuleKey, "line-items-required")
	}
	if got[0].Severity != "error" {
		t.Errorf("ByRef[inv-2][0].Severity = %q, want %q", got[0].Severity, "error")
	}
}

// TestValidatorClient_SendsS2STokenNoIdentityHeaders (VC-02): the request
// carries X-S2S-Token and carries NO X-Tenant-ID / X-User-ID / X-User-Role
// ([s2s-identity], AC#2).
func TestValidatorClient_SendsS2STokenNoIdentityHeaders(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"x","results":[{"ref":"inv-1","violations":[]}]}`, cannedRuleSetVersion)))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "secret-peer-token", nil)
	if _, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}}); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}

	if got := captured.Get("X-S2S-Token"); got != "secret-peer-token" {
		t.Errorf("X-S2S-Token = %q, want %q", got, "secret-peer-token")
	}
	for _, h := range []string{"X-Tenant-ID", "X-User-ID", "X-User-Role"} {
		if got := captured.Get(h); got != "" {
			t.Errorf("%s = %q, want absent -- 04's batch surface has no tenant, so 03 must assert none [s2s-identity]", h, got)
		}
	}
}

// TestValidatorClient_500MapsToErrUpstream (VC-03): a 500 must surface as
// ErrUpstream, never as an empty-violations success [AC#3].
func TestValidatorClient_500MapsToErrUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a 500 must surface as an error, never as a clean result [AC#3, Stage-1 F2]", err)
	}
	if err == nil && len(result.ByRef) == 0 {
		t.Errorf("a 500 produced ByRef = %v (nil error) -- a failure must NEVER be laundered into an empty-violations "+
			"success [AC#3, Stage-1 F2]", result.ByRef)
	}
}

// TestValidatorClient_503MapsToDistinguishableNoActiveRuleSet (VC-04): a
// 503 must map to a DISTINGUISHABLE no-active-rule-set error, not a
// generic ErrUpstream, so the caller can answer 503 rather than 502 [AC#4].
func TestValidatorClient_503MapsToDistinguishableNoActiveRuleSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no active rule-set"}`))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("err = %v, want a DISTINGUISHABLE ErrNoActiveRuleSet [AC#4, Stage-1 F3]", err)
	}
	if errors.Is(err, ErrUpstream) {
		t.Errorf("a 503 (no active rule-set) must be distinguishable from a generic upstream failure -- "+
			"got ErrUpstream instead of ErrNoActiveRuleSet")
	}
}

// TestValidatorClient_401MapsToErrUpstream (VC-05): a 401 is a
// misconfigured token -- an outage, not a verdict on the invoice [AC#3].
func TestValidatorClient_401MapsToErrUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "wrong-token", nil)
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a 401 is a misconfigured token, an outage, not a verdict [AC#3]", err)
	}
}

// TestValidatorClient_ClosedConnectionMapsToErrUpstream (VC-06): a closed
// connection is a transport failure, never a clean result [AC#3].
func TestValidatorClient_ClosedConnectionMapsToErrUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		conn.Close() // close without writing any response
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a closed connection is a transport failure, never a clean result [AC#3]", err)
	}
}

// TestValidatorClient_200NonJSONBodyMapsToErrUpstream (VC-07): a 200 with
// an unparseable body must never decode into a silent zero-value success
// [AC#3].
func TestValidatorClient_200NonJSONBodyMapsToErrUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a 200 with an unparseable body must never decode into a silent "+
			"zero-value success [AC#3]", err)
	}
}

// TestValidatorClient_TimeoutReturnsErrorNotHang (VC-08): a server that
// sleeps past the client's timeout returns an error rather than hanging
// [AC#5]. Injects a MILLISECOND-timeout *http.Client -- left on the (60s)
// production default this test would sleep out CI's wall-clock every run
// [Stage-1 F4].
func TestValidatorClient_TimeoutReturnsErrorNotHang(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang well past the client's timeout
	}))
	// Registration order matters: t.Cleanup runs LIFO, so registering
	// srv.Close FIRST means it runs LAST -- release is closed (unblocking
	// the handler goroutine) before Close waits on it.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	hc := &http.Client{Timeout: 50 * time.Millisecond}
	v := NewValidator(srv.URL, "tok", hc)

	start := time.Now()
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("Validate took %s -- the injected ms-timeout client did not bound the call (hung instead of erroring)", elapsed)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a timed-out request is a transport failure like any other [AC#3, AC#5]", err)
	}
}

// TestValidatorClient_NullViolationsMapsToEmptySlice (VC-09): a ref whose
// violations decode as null maps to []Violation{}, never nil
// ([violations-write]).
func TestValidatorClient_NullViolationsMapsToEmptySlice(t *testing.T) {
	body := fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","results":[{"ref":"inv-1","violations":null}]}`, cannedRuleSetVersion)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
	got, ok := result.ByRef["inv-1"]
	if !ok {
		t.Fatalf("ByRef missing key %q", "inv-1")
	}
	if got == nil {
		t.Errorf("ByRef[inv-1] is nil, want []Violation{} -- a nil Go slice encodes as SQL NULL downstream into "+
			"invoices.violations jsonb NOT NULL [violations-write]")
	}
	if len(got) != 0 {
		t.Errorf("ByRef[inv-1] = %v, want empty", got)
	}
}

// TestValidatorClient_RequestBodyPreservesOrderAndShape (VC-10): a 3-item
// request produces body {"invoices":[{ref,invoice}x3]} in request order
// [AC#7].
func TestValidatorClient_RequestBodyPreservesOrderAndShape(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = b
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"x","results":[`+
			`{"ref":"inv-1","violations":[]},{"ref":"inv-2","violations":[]},{"ref":"inv-3","violations":[]}]}`, cannedRuleSetVersion)))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	items := []ValidateItem{
		{Ref: "inv-1", Invoice: map[string]any{"n": float64(1)}},
		{Ref: "inv-2", Invoice: map[string]any{"n": float64(2)}},
		{Ref: "inv-3", Invoice: map[string]any{"n": float64(3)}},
	}
	if _, err := v.Validate(context.Background(), items); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}

	var decoded struct {
		Invoices []map[string]any `json:"invoices"`
	}
	if err := json.Unmarshal(capturedBody, &decoded); err != nil {
		t.Fatalf("captured request body is not valid JSON: %v (body=%s)", err, capturedBody)
	}
	if len(decoded.Invoices) != 3 {
		t.Fatalf("request body has %d items, want 3 (body=%s)", len(decoded.Invoices), capturedBody)
	}
	wantRefs := []string{"inv-1", "inv-2", "inv-3"}
	for i, want := range wantRefs {
		gotRef, _ := decoded.Invoices[i]["ref"].(string)
		if gotRef != want {
			t.Errorf("item[%d].ref = %q, want %q (in request order) -- ValidateItem must marshal as lowercase "+
				"\"ref\"/\"invoice\" [Stage-1 F1] (body=%s)", i, gotRef, want, capturedBody)
		}
		if _, ok := decoded.Invoices[i]["invoice"]; !ok {
			t.Errorf("item[%d] missing an \"invoice\" key -- (body=%s)", i, capturedBody)
		}
	}
}

// TestValidatorClient_400MapsToErrUpstream (VC-11, Stage-1 addendum F2): 04
// returns 400 (empty batch / over the 5,000-item cap) -- must map to
// ErrUpstream, never an empty-violations success. Reachable in production:
// the importer enforces no row ceiling (task-109 Stage-1 G2), so a real CSV
// import CAN exceed 5,000 items.
func TestValidatorClient_400MapsToErrUpstream(t *testing.T) {
	// Verbatim JSON literal matching internal/validation/handlers.go's
	// writeError envelope ({"error": msg}).
	const body = `{"error":"invoices must not be empty"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	result, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a 400 (empty batch / over the 5,000-item cap) must never decode "+
			"into a clean result [Stage-1 F2]", err)
	}
	if err == nil {
		for ref, v := range result.ByRef {
			t.Errorf("ref %q read as CLEAN (%d violations) after a 400 -- every invoice in the batch would "+
				"silently transition draft->validated [Stage-1 F2]", ref, len(v))
		}
	}
}

// TestValidatorClient_413MapsToErrUpstream (VC-12, Stage-1 addendum F2): 04
// returns 413 (body over 16 MiB) -- must map to ErrUpstream, never an
// empty-violations success.
func TestValidatorClient_413MapsToErrUpstream(t *testing.T) {
	const body = `{"error":"request body exceeds the batch size limit"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	v := NewValidator(srv.URL, "tok", nil)
	_, err := v.Validate(context.Background(), []ValidateItem{{Ref: "inv-1", Invoice: map[string]any{}}})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream -- a 413 (body over 16 MiB) must never decode into a clean result "+
			"[Stage-1 F2]", err)
	}
}

// TestValidatorClient_MissingRefInResponseMapsToErrUpstream (VC-13,
// Stage-1 addendum F5): a 200 whose results omit one sent ref must map to
// ErrUpstream, not a clean verdict for the omitted ref -- the response
// must be TOTAL over the sent refs before ByRef is built.
func TestValidatorClient_MissingRefInResponseMapsToErrUpstream(t *testing.T) {
	// 04 answers 200 but omits "inv-2" from results -- e.g. truncation or a
	// future partial-response bug.
	body := fmt.Sprintf(`{"rule_set_version":%d,"rule_set_version_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","results":[{"ref":"inv-1","violations":[]}]}`, cannedRuleSetVersion)
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
		t.Errorf("err = %v, want ErrUpstream -- the response omitted \"inv-2\", one of the 2 sent refs; the "+
			"response must be TOTAL over the sent refs before ByRef is built [Stage-1 F5]", err)
	}
	if err == nil {
		if _, ok := result.ByRef["inv-2"]; !ok {
			t.Errorf("ByRef has no entry for inv-2 (omitted by 04) -- an absent key is indistinguishable from a "+
				"CLEAN verdict to any caller ranging over the refs it sent [Stage-1 F5]")
		}
	}
}

// TestValidatorClient_DoesNotImportValidationPackage (VC-14, Stage-1
// addendum F6, AC#8): internal/invoice must NOT import internal/validation
// -- 03 declares its own wire types instead ([wire-types-redeclared]).
// Precedent: RS-V2-14's repo-wide grep detector
// (internal/validation/rule_set_v2_test.go:641). Like RS-V2-14, this is a
// baseline/regression guard, not a strict red-to-green spec: the property
// it checks already holds for any implementation that never imports
// internal/validation. It fails RIGHT NOW only because
// validator_qa_scaffold.go deliberately imports internal/validation (see
// that file's header) so this guard has a genuine violation to catch
// before the real, import-free validator.go replaces it.
func TestValidatorClient_DoesNotImportValidationPackage(t *testing.T) {
	root := repoRootForValidatorTest(t)
	cmd := exec.Command("go", "list", "-deps", "./internal/invoice")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/invoice: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "github.com/SimonOsipov/invoice-os/internal/validation" {
			t.Errorf("internal/invoice imports internal/validation -- forbidden by [wire-types-redeclared] / AC#8: " +
				"03 must declare its OWN wire types (Violation/ValidateItem/ValidateResult), never import 04's package")
			return
		}
	}
}

// repoRootForValidatorTest resolves the git worktree root so
// TestValidatorClient_DoesNotImportValidationPackage can run `go list`
// from the right place regardless of `go test`'s working directory (the
// package dir). Mirrors internal/validation/rule_set_v2_test.go's
// repoRoot(t) helper (RS-V2-14) -- duplicated rather than imported, since
// internal/invoice must not import internal/validation even in tests
// [wire-types-redeclared].
func repoRootForValidatorTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}
