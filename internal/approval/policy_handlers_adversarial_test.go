package approval

// Adversarial coverage for the six approval-policy handlers: the decoder's real
// behaviour on hostile bodies, what a client can and cannot smuggle past it, and
// what the mux does with the path id and the verbs no route declares.
// httptest only, no DB — same gate as policy_handlers_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- what a body may carry -------------------------------------------------

// There is no DisallowUnknownFields, so an unknown field is ignored rather than a
// 400. The server-owned fields are the ones that matter: they are absent from the
// request structs, so a client cannot reach them by naming them.
func TestPolicyHandlers_UnknownFieldsAreIgnoredNotRejected(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		id := caller()
		var gotName, gotScope string
		ran := false
		s := failClosedPolicySeam(t)
		s.create = func(_ context.Context, name, scope string) (Policy, error) {
			ran, gotName, gotScope = true, name, scope
			return newPolicy(), nil
		}
		body := `{"name":"Standard","scope":"All invoices","id":"11111111-1111-1111-1111-111111111111",` +
			`"tenant_id":"22222222-2222-2222-2222-222222222222","status":"published","version":99,` +
			`"sealed":true,"versions":[{"version":7}],"steps":[{"kind":"approval"}],"nonsense":null}`
		rec := servePolicy(t, s, nil, "POST", "/v1/approval-policies", body, &id)
		if !ran {
			t.Fatalf("unknown fields turned into a rejection: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
		}
		if gotName != "Standard" || gotScope != "All invoices" {
			t.Errorf("store received name %q scope %q, want the two declared fields only", gotName, gotScope)
		}
	})

	t.Run("draft", func(t *testing.T) {
		id := caller()
		ran := false
		s := failClosedPolicySeam(t)
		s.put = func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
			ran = true
			return newPolicy(), nil
		}
		body := `{"steps":[],"id":"33333333-3333-3333-3333-333333333333","version":9,"sealed":true,` +
			`"status":"published","published_by":"someone-else","nonsense":{"a":[1,2]}}`
		rec := servePolicy(t, s, nil, "PUT", "/v1/approval-policies/"+policyHandlerTestID+"/draft", body, &id)
		if !ran {
			t.Fatalf("unknown fields turned into a rejection: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// A step id is server-minted. stepInput declares no id field, so a client-supplied
// one is dropped at decode — the reflect leg is what keeps that true if the struct
// grows a field later.
func TestPutDraftHandler_ClientSuppliedStepIDIsDropped(t *testing.T) {
	t.Run("stepInput declares no id", func(t *testing.T) {
		rt := reflect.TypeOf(stepInput{})
		for i := 0; i < rt.NumField(); i++ {
			tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if strings.EqualFold(tag, "id") || strings.EqualFold(rt.Field(i).Name, "ID") {
				t.Errorf("stepInput.%s carries json tag %q — a client can now name its own step id", rt.Field(i).Name, tag)
			}
		}
		if rt.NumField() == 0 {
			t.Fatal("stepInput has no fields — the scan above is vacuous")
		}
	})

	t.Run("a supplied id never reaches the store", func(t *testing.T) {
		id := caller()
		var got []stepInput
		ran := false
		s := failClosedPolicySeam(t)
		s.put = func(_ context.Context, _ string, _, _ *string, steps []stepInput) (Policy, error) {
			ran, got = true, steps
			return newPolicy(), nil
		}
		body := `{"steps":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","kind":"condition",` +
			`"cond_op":">","cond_amount":"100.00","then":[{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",` +
			`"kind":"approval","workflow_role_key":"cfo"}]}]}`
		rec := servePolicy(t, s, nil, "PUT", "/v1/approval-policies/"+policyHandlerTestID+"/draft", body, &id)
		if !ran {
			t.Fatalf("the store never ran: %d %s", rec.Code, rec.Body.String())
		}
		want := []stepInput{{
			Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("100.00"),
			Then: []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("cfo")}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("store received %+v, want %+v — the supplied ids must vanish and nothing else may", got, want)
		}
	})
}

// encoding/json takes the LAST occurrence of a repeated key, so a duplicate cannot
// be used to show the guard one value and the store another.
func TestPolicyHandlers_DuplicateJSONKeysTakeTheLast(t *testing.T) {
	draft := "/v1/approval-policies/" + policyHandlerTestID + "/draft"

	t.Run("create name", func(t *testing.T) {
		id := caller()
		got := ""
		s := failClosedPolicySeam(t)
		s.create = func(_ context.Context, name, _ string) (Policy, error) {
			got = name
			return newPolicy(), nil
		}
		servePolicy(t, s, nil, "POST", "/v1/approval-policies", `{"name":"first","name":"second"}`, &id)
		if got != "second" {
			t.Errorf("store received name %q, want %q (the last occurrence wins)", got, "second")
		}
	})

	// The dangerous direction: a trailing null must not slip past the presence check.
	t.Run("steps array then null is still a 400", func(t *testing.T) {
		id := caller()
		rec := servePolicy(t, failClosedPolicySeam(t), nil, "PUT", draft, `{"steps":[],"steps":null}`, &id)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 — the trailing null is the value the guard must see: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("steps null then array reaches the store empty", func(t *testing.T) {
		id := caller()
		var got []stepInput
		ran := false
		s := failClosedPolicySeam(t)
		s.put = func(_ context.Context, _ string, _, _ *string, steps []stepInput) (Policy, error) {
			ran, got = true, steps
			return newPolicy(), nil
		}
		rec := servePolicy(t, s, nil, "PUT", draft, `{"steps":null,"steps":[]}`, &id)
		if !ran {
			t.Fatalf("status = %d, want the store to run: %s", rec.Code, rec.Body.String())
		}
		if got == nil || len(got) != 0 {
			t.Errorf("store received %v, want an empty non-nil slice", got)
		}
	})
}

// TestPolicyHandlers_JSONEscapedNULReachesTheValidators: a NUL is legal in a JSON string
// as an escape, so the one byte text will not take arrives through the decoder intact.
// The handler forwards it untouched — the store's validators are the only thing between
// it and a 22021. Pins the wire half of TestPolicy_NameWithANULIsRefusedNotA500 and
// TestPutDraft_NULInAnyTextFieldIsRefusedNotA500, both of which start below the decoder.
//
// The body is marshalled rather than hand-written, so the escape under test is the one a
// client's own encoder emits.
func TestPolicyHandlers_JSONEscapedNULReachesTheValidators(t *testing.T) {
	body := func(t *testing.T, v any) string {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal the request body: %v", err)
		}
		// Escaped, not raw: a raw NUL would be invalid JSON and the decoder, not the
		// validator, would be what refused it.
		if strings.ContainsRune(string(b), 0) {
			t.Fatalf("body %q carries a raw NUL — the case would prove the decoder, not the guard", b)
		}
		return string(b)
	}

	t.Run("create/name", func(t *testing.T) {
		id := caller()
		got := "unset"
		s := failClosedPolicySeam(t)
		s.create = func(_ context.Context, name, _ string) (Policy, error) {
			got = name
			return Policy{}, ErrValidation
		}
		rec := servePolicy(t, s, nil, "POST", "/v1/approval-policies",
			body(t, map[string]string{"name": "Sign\x00off"}), &id)
		if got != "Sign\x00off" {
			t.Fatalf("store received name %q, want the NUL undisturbed", got)
		}
		if _, err := normalizeName(got); !errors.Is(err, ErrValidation) {
			t.Errorf("normalizeName(%q) = %v, want ErrValidation", got, err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want the store's 400: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("draft/step fields", func(t *testing.T) {
		id := caller()
		var got []stepInput
		s := failClosedPolicySeam(t)
		s.put = func(_ context.Context, _ string, _, _ *string, steps []stepInput) (Policy, error) {
			got = steps
			return Policy{}, ErrValidation
		}
		rec := servePolicy(t, s, nil, "PUT", "/v1/approval-policies/"+policyHandlerTestID+"/draft",
			body(t, map[string]any{"steps": []map[string]string{
				{"kind": "notify", "notify_target": "prep\x00arer", "notify_channel": "email"},
			}}), &id)
		if len(got) != 1 || got[0].NotifyTarget == nil || *got[0].NotifyTarget != "prep\x00arer" {
			t.Fatalf("store received %+v, want the NUL undisturbed", got)
		}
		if err := validateTree(got); !errors.Is(err, ErrValidation) {
			t.Errorf("validateTree = %v, want ErrValidation", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want the store's 400: %s", rec.Code, rec.Body.String())
		}
	})
}

// --- bodies that are not objects -------------------------------------------

func TestPolicyHandlers_EmptyAndWrongShapeBodiesAre400(t *testing.T) {
	draft := "/v1/approval-policies/" + policyHandlerTestID + "/draft"
	cases := []struct {
		name, method, path, body string
	}{
		{"create/no body", "POST", "/v1/approval-policies", ""},
		{"create/array", "POST", "/v1/approval-policies", `[]`},
		{"create/string", "POST", "/v1/approval-policies", `"Standard"`},
		{"create/number", "POST", "/v1/approval-policies", `7`},
		{"draft/no body", "PUT", draft, ""},
		{"draft/array", "PUT", draft, `[]`},
		{"draft/steps is an object", "PUT", draft, `{"steps":{}}`},
		{"draft/steps is a string", "PUT", draft, `{"steps":"[]"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			rec := servePolicy(t, failClosedPolicySeam(t), nil, c.method, c.path, c.body, &id)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != "error" {
				t.Errorf("body keys = %v, want exactly [error]", got)
			}
		})
	}

	// json.Decoder reads ONE value and leaves the rest of the stream alone, so bytes
	// after a complete object are ignored rather than rejected — the same idiom, and
	// the same behaviour, as the workflow-role handlers. Only the first value binds.
	t.Run("bytes after the first value are ignored", func(t *testing.T) {
		for _, c := range []struct{ name, body, want string }{
			{"trailing brace", `{"name":"first"}}`, "first"},
			{"second object", `{"name":"first"}{"name":"second"}`, "first"},
			{"trailing junk", `{"name":"first"} not json at all`, "first"},
		} {
			t.Run(c.name, func(t *testing.T) {
				id := caller()
				got := ""
				ran := false
				s := failClosedPolicySeam(t)
				s.create = func(_ context.Context, name, _ string) (Policy, error) {
					ran, got = true, name
					return newPolicy(), nil
				}
				rec := servePolicy(t, s, nil, "POST", "/v1/approval-policies", c.body, &id)
				if !ran {
					t.Fatalf("status = %d, want the first value to bind: %s", rec.Code, rec.Body.String())
				}
				if got != c.want {
					t.Errorf("store received name %q, want %q", got, c.want)
				}
			})
		}
	})

	// A bare null decodes cleanly into the zero struct — it is NOT a decode error.
	// Create therefore forwards an empty name for the store to refuse, while the
	// draft's presence check catches it here.
	t.Run("create/null forwards an empty name", func(t *testing.T) {
		id := caller()
		ran := false
		got := "unset"
		s := failClosedPolicySeam(t)
		s.create = func(_ context.Context, name, _ string) (Policy, error) {
			ran, got = true, name
			return Policy{}, ErrValidation
		}
		rec := servePolicy(t, s, nil, "POST", "/v1/approval-policies", `null`, &id)
		if !ran {
			t.Fatalf("null was treated as a decode error: %d %s", rec.Code, rec.Body.String())
		}
		if got != "" {
			t.Errorf("store received name %q, want the zero value", got)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want the store's 400: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("draft/null is caught by the presence check", func(t *testing.T) {
		id := caller()
		rec := servePolicy(t, failClosedPolicySeam(t), nil, "PUT", draft, `null`, &id)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if msg := errorMessage(t, rec.Body.Bytes()); !strings.Contains(strings.ToLower(msg), "steps") {
			t.Errorf("error = %q, want the steps-presence message, not a decode failure", msg)
		}
	})
}

// --- content type is not a guard, and the response type is fixed ------------

// Nothing reads Content-Type: the decoder is the only gate, so a JSON body under a
// wrong label is accepted and a form body under any label is a 400.
func TestPolicyHandlers_ContentTypeIsNotAGate(t *testing.T) {
	send := func(t *testing.T, s *policySeam, contentType, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/approval-policies", strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r = r.WithContext(auth.WithIdentity(r.Context(), caller()))
		rec := httptest.NewRecorder()
		policiesMux(s, nil).ServeHTTP(rec, r)
		return rec
	}

	t.Run("json body under a foreign content type is accepted", func(t *testing.T) {
		for _, ct := range []string{"", "text/plain", "application/xml", "multipart/form-data; boundary=x"} {
			ran := false
			s := failClosedPolicySeam(t)
			s.create = func(context.Context, string, string) (Policy, error) { ran = true; return newPolicy(), nil }
			rec := send(t, s, ct, `{"name":"Standard"}`)
			if !ran || rec.Code != http.StatusCreated {
				t.Errorf("Content-Type %q: ran=%v status=%d, want a 201 — content type is not a gate", ct, ran, rec.Code)
			}
		}
	})

	t.Run("a form body is a 400 even when labelled correctly", func(t *testing.T) {
		rec := send(t, failClosedPolicySeam(t), "application/x-www-form-urlencoded", "name=Standard")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})

	// Every answer this layer writes is JSON, including the ones written before any
	// store call.
	t.Run("every response is application/json", func(t *testing.T) {
		cases := []struct {
			name string
			run  func() *httptest.ResponseRecorder
		}{
			{"401", func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/v1/approval-policies", nil)
				rec := httptest.NewRecorder()
				policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
				return rec
			}},
			{"400 decode", func() *httptest.ResponseRecorder {
				id := caller()
				return servePolicy(t, failClosedPolicySeam(t), nil, "POST", "/v1/approval-policies", `{`, &id)
			}},
			{"400 steps presence", func() *httptest.ResponseRecorder {
				id := caller()
				return servePolicy(t, failClosedPolicySeam(t), nil, "PUT",
					"/v1/approval-policies/"+policyHandlerTestID+"/draft", `{}`, &id)
			}},
			{"200 list", func() *httptest.ResponseRecorder {
				id := caller()
				return servePolicy(t, policyOkSeam(newPolicy()), nil, "GET", "/v1/approval-policies", "", &id)
			}},
		}
		for _, c := range cases {
			if got := c.run().Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("%s response Content-Type = %q, want application/json", c.name, got)
			}
		}
	})
}

// --- no null lane anywhere in the envelope ---------------------------------

// The outer slice is the list handler's to rebuild; every lane BELOW it is the two
// MarshalJSONs' job, and they must still fire through the named envelope field and
// through a nested Step. One nil anywhere and the SPA maps over null.
func TestPolicyHandlers_NoNullLaneInsideTheListEnvelope(t *testing.T) {
	nested := Policy{
		ID: policyHandlerTestID, Name: "Standard", Scope: policyScopeAll, Status: "draft", Version: 2,
		Steps: []Step{{
			ID: "s1", Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("100.00"),
			Then: []Step{{ID: "s2", Kind: "approval", WorkflowRoleKey: ptr("cfo")}}, // Then/Else nil below
		}},
		Versions: nil, // the policy's own nil lane
	}

	assertNoNull := func(t *testing.T, raw []byte) {
		t.Helper()
		for _, bad := range []string{
			`"steps":null`, `"versions":null`, `"then":null`, `"else":null`, `"approval_policies":null`,
		} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("response carries %s: %s", bad, raw)
			}
		}
		// Non-vacuity: the lanes the fixture left nil must be present as [].
		for _, want := range []string{`"versions":[]`, `"then":[]`, `"else":[]`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("response is missing %s, so the scan above proves nothing: %s", want, raw)
			}
		}
	}

	t.Run("list", func(t *testing.T) {
		id := caller()
		s := failClosedPolicySeam(t)
		s.list = func(context.Context) ([]Policy, error) { return []Policy{nested}, nil }
		rec := servePolicy(t, s, nil, "GET", "/v1/approval-policies", "", &id)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		assertNoNull(t, rec.Body.Bytes())
	})

	// The single-policy routes return a bare Policy, so they exercise the same rule
	// without the envelope.
	t.Run("get", func(t *testing.T) {
		id := caller()
		s := failClosedPolicySeam(t)
		s.get = func(context.Context, string) (Policy, error) { return nested, nil }
		rec := servePolicy(t, s, nil, "GET", "/v1/approval-policies/"+policyHandlerTestID, "", &id)
		assertNoNull(t, rec.Body.Bytes())
	})
}

// --- the path id -----------------------------------------------------------

// {id} is delivered UNESCAPED, so %2F and %2E%2E arrive as "/" and ".." inside the
// value. That is safe only because the id is passed straight to the store seam and
// is never joined into a path or a URL — these cases pin the delivered value so a
// future handler that builds something out of it fails here first.
func TestPolicyRoutes_PathIdEdgeCases(t *testing.T) {
	deliveredID := func(t *testing.T, method, path string) (string, int) {
		t.Helper()
		id := caller()
		got := "unset"
		s := failClosedPolicySeam(t)
		record := func(pid string) (Policy, error) { got = pid; return newPolicy(), nil }
		s.get = func(_ context.Context, pid string) (Policy, error) { return record(pid) }
		s.del = func(_ context.Context, pid string) (Policy, error) { return record(pid) }
		rec := servePolicy(t, s, nil, method, path, "", &id)
		return got, rec.Code
	}

	t.Run("percent escapes are decoded into the id", func(t *testing.T) {
		for _, c := range []struct{ path, want string }{
			{"/v1/approval-policies/a%2Fb", "a/b"},
			{"/v1/approval-policies/a%20b", "a b"},
			{"/v1/approval-policies/%2E%2E", ".."},
		} {
			got, code := deliveredID(t, "GET", c.path)
			if code != http.StatusOK {
				t.Errorf("GET %s = %d, want the handler to run", c.path, code)
			}
			if got != c.want {
				t.Errorf("GET %s delivered id %q, want %q", c.path, got, c.want)
			}
		}
	})

	t.Run("a second segment matches no id route", func(t *testing.T) {
		for _, p := range []string{
			"/v1/approval-policies/a/b",
			"/v1/approval-policies/" + policyHandlerTestID + "/draft/extra",
			"/v1/approval-policies/" + policyHandlerTestID + "/publish/extra",
		} {
			r := httptest.NewRequest("GET", p, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404 — {id} spans exactly one segment", p, rec.Code)
			}
		}
	})

	// A LITERAL dot segment is resolved by the mux into a redirect, so no handler is
	// handed one. Only the percent-encoded form above reaches a handler.
	t.Run("literal dot segments redirect instead of matching", func(t *testing.T) {
		for _, c := range []struct{ path, loc string }{
			{"/v1/approval-policies/..", "/v1"},
			{"/v1/approval-policies/x/../y", "/v1/approval-policies/y"},
		} {
			r := httptest.NewRequest("GET", c.path, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("GET %s = %d, want 307", c.path, rec.Code)
			}
			if got := rec.Header().Get("Location"); got != c.loc {
				t.Errorf("GET %s redirected to %q, want %q", c.path, got, c.loc)
			}
		}
	})

	// The query string belongs to neither the id nor the store.
	t.Run("a query string is not part of the id", func(t *testing.T) {
		id := caller()
		got := ""
		s := failClosedPolicySeam(t)
		s.del = func(_ context.Context, pid string) (Policy, error) {
			got = pid
			return newPolicy(), nil
		}
		servePolicy(t, s, nil, "DELETE", "/v1/approval-policies/"+policyHandlerTestID+"?force=true&x=1", "", &id)
		if got != policyHandlerTestID {
			t.Errorf("id = %q, want %q", got, policyHandlerTestID)
		}
	})
}

// --- the verbs no route declares -------------------------------------------

// HEAD rides the GET patterns (net/http registers the pair), so it must still clear
// identity and the store. OPTIONS matches nothing here: preflight is the gateway's,
// and this service must never answer it itself.
func TestPolicyRoutes_HeadAndOptions(t *testing.T) {
	getPaths := []string{"/v1/approval-policies", "/v1/approval-policies/" + policyHandlerTestID}
	writePaths := []string{
		"/v1/approval-policies/" + policyHandlerTestID + "/draft",
		"/v1/approval-policies/" + policyHandlerTestID + "/publish",
	}

	t.Run("HEAD on a GET route runs the handler with no body", func(t *testing.T) {
		for _, p := range getPaths {
			id := caller()
			rec := servePolicy(t, policyOkSeam(newPolicy()), nil, "HEAD", p, "", &id)
			if rec.Code != http.StatusOK {
				t.Errorf("HEAD %s = %d, want 200", p, rec.Code)
			}
		}
	})

	t.Run("HEAD is still 401 without identity", func(t *testing.T) {
		for _, p := range getPaths {
			r := httptest.NewRequest("HEAD", p, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("HEAD %s = %d, want 401", p, rec.Code)
			}
		}
	})

	t.Run("HEAD on a write route is 405", func(t *testing.T) {
		for _, p := range writePaths {
			r := httptest.NewRequest("HEAD", p, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("HEAD %s = %d, want 405", p, rec.Code)
			}
		}
	})

	t.Run("OPTIONS reaches no handler on any of the six", func(t *testing.T) {
		for _, p := range append(append([]string{}, getPaths...), writePaths...) {
			r := httptest.NewRequest("OPTIONS", p, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("OPTIONS %s = %d, want 405 — preflight is answered upstream", p, rec.Code)
			}
		}
	})
}
