package customrule_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const ruleBody = `{"entity_id":null,"type":"required","target":"buyer.name","params":{},"severity":"error","when":null,"message":"m","enabled":true}`

// fakeStore records every call and answers with err, or with a canned rule when err is nil.
type fakeStore struct {
	err                       error
	creates, updates, deletes int
	lists                     int
	lastID                    string
	lastIn                    customrule.RuleInput
	rule                      customrule.Rule
}

func (f *fakeStore) list(context.Context) ([]customrule.Rule, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return []customrule.Rule{f.rule}, nil
}

func (f *fakeStore) create(_ context.Context, in customrule.RuleInput) (customrule.Rule, error) {
	f.creates++
	f.lastIn = in
	return f.rule, f.err
}

func (f *fakeStore) update(_ context.Context, id string, in customrule.RuleInput) (customrule.Rule, error) {
	f.updates++
	f.lastID, f.lastIn = id, in
	return f.rule, f.err
}

func (f *fakeStore) del(_ context.Context, id string) error {
	f.deletes++
	f.lastID = id
	return f.err
}

func (f *fakeStore) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/custom-rules", customrule.ListHandler(f.list, nil))
	mux.HandleFunc("POST /v1/custom-rules", customrule.CreateHandler(f.create, nil))
	mux.HandleFunc("PUT /v1/custom-rules/{id}", customrule.UpdateHandler(f.update, nil))
	mux.HandleFunc("DELETE /v1/custom-rules/{id}", customrule.DeleteHandler(f.del, nil))
	return mux
}

func (f *fakeStore) writes() int { return f.creates + f.updates + f.deletes }

func newFakeStore() *fakeStore {
	id := "11111111-1111-4111-8111-111111111111"
	return &fakeStore{rule: customrule.Rule{
		ID: id, Key: "custom:" + id, Revision: 1, RevisionID: "22222222-2222-4222-8222-222222222222",
		Type: "required", Target: "buyer.name", Params: json.RawMessage(`{}`), Severity: "error",
		Message: "m", Enabled: true, UpdatedAt: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
		UpdatedBy: "33333333-3333-4333-8333-333333333333",
	}}
}

func memberCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "33333333-3333-4333-8333-333333333333", Role: "authenticated", TenantID: "44444444-4444-4444-8444-444444444444"})
}

func call(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(memberCtx())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type route struct{ method, target, body string }

func writeRoutes() map[string]route {
	const id = "/v1/custom-rules/11111111-1111-4111-8111-111111111111"
	return map[string]route{
		"create": {http.MethodPost, "/v1/custom-rules", ruleBody},
		"update": {http.MethodPut, id, ruleBody},
		"delete": {http.MethodDelete, id, ""},
	}
}

func TestCustomRulesHandlers_NonAdminIs403WithTheMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"not permitted", customrule.ErrNotPermitted, http.StatusForbidden, "only a workspace admin can change custom rules"},
		{"wrapped not permitted", fmt.Errorf("save: %w", customrule.ErrNotPermitted), http.StatusForbidden, "only a workspace admin can change custom rules"},
		{"not an active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
		{"wrapped not an active member", fmt.Errorf("tx: %w", db.ErrNotActiveMember), http.StatusForbidden, db.NotActiveMemberMessage},
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, ""},
	}
	for name, rt := range writeRoutes() {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				fs := newFakeStore()
				fs.err = c.err
				rec := call(fs.mux(), rt.method, rt.target, rt.body)
				if rec.Code != c.code {
					t.Fatalf("status = %d (%q), want %d", rec.Code, rec.Body.String(), c.code)
				}
				if got := errorBody(t, rec); got == "" || (c.msg != "" && got != c.msg) {
					t.Errorf("error = %q, want %q", got, c.msg)
				}
				if fs.writes() != 1 {
					t.Errorf("store writes = %d, want the handler to reach the store once", fs.writes())
				}
			})
		}
	}
	for _, c := range cases[2:] {
		fs := newFakeStore()
		fs.err = c.err
		if rec := call(fs.mux(), http.MethodGet, "/v1/custom-rules", ""); rec.Code != c.code {
			t.Errorf("GET with %s = %d, want %d", c.name, rec.Code, c.code)
		}
	}
}

func TestCustomRulesHandlers_ErrorMapping(t *testing.T) {
	const reason = "params.expr must compile to a boolean expression"
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"invalid", customrule.InvalidError{Msg: reason}, http.StatusBadRequest, reason},
		{"wrapped invalid", fmt.Errorf("check: %w", customrule.InvalidError{Msg: reason}), http.StatusBadRequest, reason},
		{"not found", customrule.ErrNotFound, http.StatusNotFound, "custom rule not found"},
		{"check unavailable", customrule.ErrCheckUnavailable, http.StatusBadGateway, "custom rules cannot be checked right now"},
		{"anything else", errors.New("pq: connection refused at 10.0.0.7"), http.StatusInternalServerError, "internal server error"},
	}
	for name, rt := range writeRoutes() {
		for _, c := range cases {
			if name == "delete" && (c.name == "invalid" || c.name == "wrapped invalid" || c.name == "check unavailable") {
				continue
			}
			t.Run(name+"/"+c.name, func(t *testing.T) {
				fs := newFakeStore()
				fs.err = c.err
				rec := call(fs.mux(), rt.method, rt.target, rt.body)
				if rec.Code != c.code {
					t.Fatalf("status = %d (%q), want %d", rec.Code, rec.Body.String(), c.code)
				}
				if got := errorBody(t, rec); got != c.msg {
					t.Errorf("error = %q, want %q", got, c.msg)
				}
			})
		}
	}
}

func TestCustomRulesHandlers_UnknownFieldsAre400(t *testing.T) {
	withKey := strings.Replace(ruleBody, `"enabled":true`, `"enabled":true,"key":"supplier-tin-required"`, 1)
	withDisables := strings.Replace(ruleBody, `"enabled":true`, `"enabled":true,"disables":["x"]`, 1)
	for name, rt := range writeRoutes() {
		if name == "delete" {
			continue
		}
		for field, body := range map[string]string{"key": withKey, "disables": withDisables} {
			t.Run(name+"/"+field, func(t *testing.T) {
				fs := newFakeStore()
				rec := call(fs.mux(), rt.method, rt.target, body)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("status = %d (%q), want 400", rec.Code, rec.Body.String())
				}
				if fs.writes() != 0 {
					t.Errorf("store writes = %d, want 0: an unknown field must stop the request before the store", fs.writes())
				}
			})
		}
		t.Run(name+"/control", func(t *testing.T) {
			fs := newFakeStore()
			rec := call(fs.mux(), rt.method, rt.target, rt.body)
			if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
				t.Fatalf("a body of known fields = %d (%q), want 2xx", rec.Code, rec.Body.String())
			}
			want := customrule.RuleInput{Type: "required", Target: "buyer.name", Params: json.RawMessage(`{}`),
				Severity: "error", Message: "m", Enabled: true}
			if fs.writes() != 1 || fs.lastIn.EntityID != nil || fs.lastIn.When != nil ||
				fs.lastIn.Type != want.Type || fs.lastIn.Target != want.Target || fs.lastIn.Severity != want.Severity ||
				fs.lastIn.Message != want.Message || !fs.lastIn.Enabled || string(fs.lastIn.Params) != "{}" {
				t.Errorf("store received %+v after %d writes, want %+v once", fs.lastIn, fs.writes(), want)
			}
		})
	}
}

func TestCustomRulesHandlers_SuccessStatusesAndRuleShape(t *testing.T) {
	const id = "/v1/custom-rules/11111111-1111-4111-8111-111111111111"
	wantKeys := []string{"enabled", "entity_id", "id", "key", "message", "params", "revision", "revision_id",
		"severity", "target", "type", "updated_at", "updated_by", "when"}
	keysOf := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	fs := newFakeStore()
	h := fs.mux()
	for _, tc := range []struct {
		name string
		rt   route
		code int
	}{
		{"create", route{http.MethodPost, "/v1/custom-rules", ruleBody}, http.StatusCreated},
		{"update", route{http.MethodPut, id, ruleBody}, http.StatusOK},
	} {
		rec := call(h, tc.rt.method, tc.rt.target, tc.rt.body)
		if rec.Code != tc.code {
			t.Errorf("%s = %d (%q), want %d", tc.name, rec.Code, rec.Body.String(), tc.code)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("%s body %q: %v", tc.name, rec.Body.String(), err)
			continue
		}
		if !reflect.DeepEqual(keysOf(got), wantKeys) {
			t.Errorf("%s keys = %v, want %v", tc.name, keysOf(got), wantKeys)
		}
		if v, ok := got["entity_id"]; !ok || v != nil {
			t.Errorf("%s entity_id = %v (present %v), want null", tc.name, v, ok)
		}
		if v, ok := got["when"]; !ok || v != nil {
			t.Errorf("%s when = %v (present %v), want null", tc.name, v, ok)
		}
	}
	if fs.lastID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("update id = %q, want the path value", fs.lastID)
	}

	rec := call(h, http.MethodDelete, id, "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("delete = %d %q, want 204 with no body", rec.Code, rec.Body.String())
	}

	rec = call(h, http.MethodGet, "/v1/custom-rules", "")
	var list struct {
		CustomRules []map[string]any `json:"custom_rules"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.CustomRules) != 1 {
		t.Fatalf("list = %d %q, want 200 {\"custom_rules\":[1 rule]}", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(keysOf(list.CustomRules[0]), wantKeys) {
		t.Errorf("list rule keys = %v, want %v", keysOf(list.CustomRules[0]), wantKeys)
	}

	nilList := customrule.ListHandler(func(context.Context) ([]customrule.Rule, error) { return nil, nil }, nil)
	rec = httptest.NewRecorder()
	nilList.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/custom-rules", nil).WithContext(memberCtx()))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"custom_rules":[]}` {
		t.Errorf("list with no rules = %d %q, want 200 {\"custom_rules\":[]}", rec.Code, rec.Body.String())
	}
}

// --- Mode B: adversarial ---------------------------------------------------------

func TestCustomRulesHandlers_BadBodiesAre400AndNeverReachTheStore(t *testing.T) {
	pad := func(n int) string {
		return strings.Replace(ruleBody, `"message":"m"`, `"message":"`+strings.Repeat("a", n)+`"`, 1)
	}
	base := len(pad(0))
	const limit = 32 << 10
	noEnabled := strings.Replace(ruleBody, `,"enabled":true`, "", 1)
	cases := []struct {
		name, body, msg string
	}{
		{"malformed json", `{"type":`, "invalid request body"},
		{"empty body", ``, "invalid request body"},
		{"not an object", `[]`, "invalid request body"},
		{"wrong type for enabled", strings.Replace(ruleBody, `"enabled":true`, `"enabled":"yes"`, 1), "invalid request body"},
		{"wrong type for entity_id", strings.Replace(ruleBody, `"entity_id":null`, `"entity_id":5`, 1), "invalid request body"},
		{"enabled absent", noEnabled, "enabled is required"},
		{"enabled null", strings.Replace(ruleBody, `"enabled":true`, `"enabled":null`, 1), "enabled is required"},
		{"one byte over 32 KiB", pad(limit + 1 - base), "invalid request body"},
		{"far over 32 KiB", pad(4 * limit), "invalid request body"},
	}
	for name, rt := range writeRoutes() {
		if name == "delete" {
			continue
		}
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				fs := newFakeStore()
				rec := call(fs.mux(), rt.method, rt.target, c.body)
				if rec.Code != http.StatusBadRequest || errorBody(t, rec) != c.msg {
					t.Errorf("status = %d %q, want 400 %q", rec.Code, rec.Body.String(), c.msg)
				}
				if fs.writes() != 0 {
					t.Errorf("store writes = %d, want 0", fs.writes())
				}
			})
		}
		t.Run(name+"/exactly 32 KiB is accepted", func(t *testing.T) {
			body := pad(limit - base)
			if len(body) != limit {
				t.Fatalf("test body is %d bytes, want %d", len(body), limit)
			}
			fs := newFakeStore()
			rec := call(fs.mux(), rt.method, rt.target, body)
			if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
				t.Fatalf("a body of exactly 32 KiB = %d %q, want 2xx", rec.Code, rec.Body.String())
			}
			if fs.writes() != 1 || len(fs.lastIn.Message) != limit-base {
				t.Errorf("store saw %d writes, message length %d", fs.writes(), len(fs.lastIn.Message))
			}
		})
		t.Run(name+"/enabled false is a value, not an absence", func(t *testing.T) {
			fs := newFakeStore()
			rec := call(fs.mux(), rt.method, rt.target, strings.Replace(ruleBody, `"enabled":true`, `"enabled":false`, 1))
			if (rec.Code != http.StatusCreated && rec.Code != http.StatusOK) || fs.writes() != 1 || fs.lastIn.Enabled {
				t.Errorf("enabled:false = %d %q, writes %d, Enabled %v, want 2xx and one write with Enabled false",
					rec.Code, rec.Body.String(), fs.writes(), fs.lastIn.Enabled)
			}
		})
	}
}

func TestCustomRulesHandlers_NoIdentityIs401BeforeTheStore(t *testing.T) {
	const id = "/v1/custom-rules/11111111-1111-4111-8111-111111111111"
	for name, rt := range map[string]route{
		"list":   {http.MethodGet, "/v1/custom-rules", ""},
		"create": {http.MethodPost, "/v1/custom-rules", ruleBody},
		"update": {http.MethodPut, id, ruleBody},
		"delete": {http.MethodDelete, id, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fs := newFakeStore()
			req := httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body))
			rec := httptest.NewRecorder()
			fs.mux().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d %q, want 401", rec.Code, rec.Body.String())
			}
			if fs.writes()+fs.lists != 0 {
				t.Errorf("store calls = %d, want 0", fs.writes()+fs.lists)
			}
		})
	}
}
