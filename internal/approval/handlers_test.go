package approval

// The five workflow-role HTTP handlers, driven through a real http.ServeMux with
// injected function values. No DSN, no pool, no skip: this file must run in every
// CI job and on a bare `go test ./...` (Core AC-8).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ---------------------------------------------------------------

// roleSeam bundles the five injected store functions. Every field of a
// failClosedSeam fails the test, so a handler reaching a seam the case did not
// deliberately wire is a failure rather than a silent zero-value call.
type roleSeam struct {
	list   RolesLister
	create RoleCreator
	update RoleUpdater
	del    RoleDeleter
	staff  RoleStaffer
}

func failClosedSeam(t *testing.T) *roleSeam {
	t.Helper()
	return &roleSeam{
		list: func(context.Context) ([]Role, error) {
			t.Fatal("lister must not run on a request the handler has to reject")
			return nil, nil
		},
		create: func(context.Context, string, string) (Role, error) {
			t.Fatal("creator must not run on a request the handler has to reject")
			return Role{}, nil
		},
		update: func(context.Context, string, *string, *string) (Role, error) {
			t.Fatal("updater must not run on a request the handler has to reject")
			return Role{}, nil
		},
		del: func(context.Context, string) (Role, error) {
			t.Fatal("deleter must not run on a request the handler has to reject")
			return Role{}, nil
		},
		staff: func(context.Context, string, []string) (Role, error) {
			t.Fatal("staffer must not run on a request the handler has to reject")
			return Role{}, nil
		},
	}
}

// errSeam wires all five funcs to the same error, so one table drives every route.
func errSeam(err error) *roleSeam {
	return &roleSeam{
		list:   func(context.Context) ([]Role, error) { return nil, err },
		create: func(context.Context, string, string) (Role, error) { return Role{}, err },
		update: func(context.Context, string, *string, *string) (Role, error) { return Role{}, err },
		del:    func(context.Context, string) (Role, error) { return Role{}, err },
		staff:  func(context.Context, string, []string) (Role, error) { return Role{}, err },
	}
}

// okSeam wires all five funcs to the same successful Role.
func okSeam(role Role) *roleSeam {
	return &roleSeam{
		list:   func(context.Context) ([]Role, error) { return []Role{role}, nil },
		create: func(context.Context, string, string) (Role, error) { return role, nil },
		update: func(context.Context, string, *string, *string) (Role, error) { return role, nil },
		del:    func(context.Context, string) (Role, error) { return role, nil },
		staff:  func(context.Context, string, []string) (Role, error) { return role, nil },
	}
}

// rolesMux registers the five patterns cmd/invoice/main.go serves, so {key} is
// populated the way production populates it — a direct ServeHTTP leaves PathValue
// empty. A deliberate copy of the patterns; the copy is pinned against the real
// file by TestWorkflowRoleHandlers_RoutesRegisteredInCmdInvoiceMain.
func rolesMux(s *roleSeam, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workflow-roles", ListRolesHandler(s.list, log))
	mux.HandleFunc("POST /v1/workflow-roles", CreateRoleHandler(s.create, log))
	mux.HandleFunc("PATCH /v1/workflow-roles/{key}", UpdateRoleHandler(s.update, log))
	mux.HandleFunc("DELETE /v1/workflow-roles/{key}", DeleteRoleHandler(s.del, log))
	mux.HandleFunc("PUT /v1/workflow-roles/{key}/members", SetRoleMembersHandler(s.staff, log))
	return mux
}

func caller() auth.Identity {
	return auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
}

// serveRole drives one request through the mux. A nil id means no identity in
// context; body "" means no body at all.
func serveRole(t *testing.T, s *roleSeam, log *slog.Logger, method, path, body string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	rolesMux(s, log).ServeHTTP(rec, r)
	return rec
}

// roleRoutes is the five routes with a body that WOULD reach the store, so a
// missing guard shows up as a success rather than as a differently-shaped 400.
var roleRoutes = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"list", "GET", "/v1/workflow-roles", ""},
	{"create", "POST", "/v1/workflow-roles", `{"title":"Tax Reviewer"}`},
	{"update", "PATCH", "/v1/workflow-roles/tax-reviewer", `{"title":"Renamed"}`},
	{"delete", "DELETE", "/v1/workflow-roles/tax-reviewer", ""},
	{"staff", "PUT", "/v1/workflow-roles/tax-reviewer/members", `{"members":[]}`},
}

// keySet returns the response body's top-level JSON keys, sorted.
func keySet(t *testing.T, raw []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// rawField returns one top-level field's raw JSON, so `[]` stays distinguishable
// from `null` (decoding into a Go slice collapses both).
func rawField(t *testing.T, raw []byte, field string) string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	v, ok := obj[field]
	if !ok {
		t.Fatalf("response %q has no %q field", raw, field)
	}
	return string(v)
}

func errorMessage(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return body.Error
}

// --- identity, before anything else ----------------------------------------

// countingReader records whether the body was read at all, so "401 before the
// body" is asserted directly and not inferred from the status code alone.
type countingReader struct {
	r    io.Reader
	read bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.read = true
	return c.r.Read(p)
}

func TestWorkflowRoleHandlers_IdentityCheckedBeforeBody(t *testing.T) {
	for _, rt := range roleRoutes {
		t.Run(rt.name, func(t *testing.T) {
			s := failClosedSeam(t)
			// Undecodable: a handler that decodes first would answer 400, not 401.
			body := &countingReader{r: strings.NewReader(`{`)}
			r := httptest.NewRequest(rt.method, rt.path, body)
			rec := httptest.NewRecorder()
			rolesMux(s, nil).ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 with no identity in context: %s", rec.Code, rec.Body.String())
			}
			if got := errorMessage(t, rec.Body.Bytes()); got != "unauthorized" {
				t.Errorf("error = %q, want %q", got, "unauthorized")
			}
			if body.read {
				t.Error("the request body was read before the identity check")
			}
		})
	}
}

// --- body caps -------------------------------------------------------------

// The over-cap bodies are VALID JSON, so a handler with no MaxBytesReader answers
// 2xx rather than tripping the malformed-JSON branch by accident.
func TestWorkflowRoleHandlers_BodyOverCapRejected(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create 5 KiB", "POST", "/v1/workflow-roles",
			`{"title":"T","pad":"` + strings.Repeat("x", 5*1024) + `"}`},
		{"update 5 KiB", "PATCH", "/v1/workflow-roles/tax-reviewer",
			`{"title":"T","pad":"` + strings.Repeat("x", 5*1024) + `"}`},
		{"staff 65 KiB", "PUT", "/v1/workflow-roles/tax-reviewer/members",
			`{"members":[],"pad":"` + strings.Repeat("x", 65*1024) + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			rec := serveRole(t, failClosedSeam(t), nil, c.method, c.path, c.body, &id)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (not 413, not 500): %s", rec.Code, rec.Body.String())
			}
			if got := errorMessage(t, rec.Body.Bytes()); got != "invalid request body" {
				t.Errorf("error = %q, want %q", got, "invalid request body")
			}
		})
	}
}

// A body just UNDER the cap still reaches the store — otherwise the test above
// would pass on a handler that rejects every body.
func TestWorkflowRoleHandlers_BodyUnderCapReaches(t *testing.T) {
	id := caller()
	body := `{"title":"T","pad":"` + strings.Repeat("x", 3*1024) + `"}`
	s := failClosedSeam(t)
	ran := false
	s.create = func(_ context.Context, title, desc string) (Role, error) {
		ran = true
		return Role{Key: "t", Title: title, Desc: desc, Members: []string{}}, nil
	}
	rec := serveRole(t, s, nil, "POST", "/v1/workflow-roles", body, &id)
	if !ran {
		t.Error("a 3 KiB body did not reach the store; the cap is below the plan's 4 KiB")
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// --- body before path ------------------------------------------------------

func TestWorkflowRoleHandlers_MalformedBodyBeforeUnknownKey(t *testing.T) {
	id := caller()
	rec := serveRole(t, failClosedSeam(t), nil, "PATCH", "/v1/workflow-roles/does-not-exist", `{`, &id)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (a malformed body outranks an unknown key): %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "invalid request body" {
		t.Errorf("error = %q, want %q", got, "invalid request body")
	}
}

// --- PATCH partial semantics ----------------------------------------------

// `{}` is the STORE's ErrValidation, not a handler pre-check: the handler must
// forward the absence (nil, nil) rather than judge it.
func TestWorkflowRoleHandlers_UpdateBothFieldsAbsentIsStoreValidation(t *testing.T) {
	id := caller()
	var gotTitle, gotDesc *string
	ran := false
	s := failClosedSeam(t)
	s.update = func(_ context.Context, _ string, title, desc *string) (Role, error) {
		ran, gotTitle, gotDesc = true, title, desc
		return Role{}, ErrValidation
	}
	rec := serveRole(t, s, nil, "PATCH", "/v1/workflow-roles/tax-reviewer", `{}`, &id)

	if !ran {
		t.Fatal("the updater never ran: the handler pre-judged an empty object instead of forwarding it")
	}
	if gotTitle != nil || gotDesc != nil {
		t.Errorf("updater saw (%v, %v), want (nil, nil)", gotTitle, gotDesc)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "invalid request" {
		t.Errorf("error = %q, want %q", got, "invalid request")
	}
}

// Clearing the blurb is a real edit: `{"desc":""}` must arrive as a non-nil
// pointer to "", or a `string` field silently turns it into "changed nothing".
func TestWorkflowRoleHandlers_UpdateEmptyDescIsForwardedNotDropped(t *testing.T) {
	id := caller()
	var gotTitle, gotDesc *string
	ran := false
	s := failClosedSeam(t)
	s.update = func(_ context.Context, _ string, title, desc *string) (Role, error) {
		ran, gotTitle, gotDesc = true, title, desc
		return Role{Key: "tax-reviewer", Title: "T", Members: []string{}}, nil
	}
	rec := serveRole(t, s, nil, "PATCH", "/v1/workflow-roles/tax-reviewer", `{"desc":""}`, &id)

	if !ran {
		t.Fatal("the updater never ran")
	}
	if gotTitle != nil {
		t.Errorf("title = %v, want nil (it was absent from the body)", *gotTitle)
	}
	if gotDesc == nil {
		t.Fatal(`desc = nil, want a non-nil pointer to "" — an explicit clear was dropped`)
	}
	if *gotDesc != "" {
		t.Errorf("desc = %q, want %q", *gotDesc, "")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// --- request vocabulary ----------------------------------------------------

// DisallowUnknownFields has zero production uses repo-wide; the SPA PUTs whole
// Role objects and only the fields the handler names are read.
func TestWorkflowRoleHandlers_UnknownFieldsIgnored(t *testing.T) {
	id := caller()
	var gotTitle, gotDesc string
	ran := false
	s := failClosedSeam(t)
	s.create = func(_ context.Context, title, desc string) (Role, error) {
		ran, gotTitle, gotDesc = true, title, desc
		return Role{Key: "t", Title: title, Members: []string{}}, nil
	}
	rec := serveRole(t, s, nil, "POST", "/v1/workflow-roles",
		`{"title":"T","members":["x"],"key":"forged","nope":1}`, &id)

	if !ran {
		t.Fatal("the creator never ran: an unknown field was rejected")
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if gotTitle != "T" || gotDesc != "" {
		t.Errorf("creator saw (%q, %q), want (%q, %q) — only title/desc are read", gotTitle, gotDesc, "T", "")
	}
}

func TestWorkflowRoleHandlers_StaffBodyVocabulary(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"absent members", `{}`, "members must be an array of member ids"},
		{"null members", `{"members":null}`, "members must be an array of member ids"},
		{"string members", `{"members":"x"}`, "invalid request body"},
		{"numeric ids", `{"members":[1]}`, "invalid request body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			rec := serveRole(t, failClosedSeam(t), nil, "PUT", "/v1/workflow-roles/tax-reviewer/members", c.body, &id)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if got := errorMessage(t, rec.Body.Bytes()); got != c.want {
				t.Errorf("error = %q, want %q", got, c.want)
			}
		})
	}
}

// `[]` is a legal unstaff, so it must arrive as a non-nil empty slice: a nil
// slice at the store means the same thing but travels the absent-field path.
func TestWorkflowRoleHandlers_StaffEmptyArrayIsAnUnstaffNotAnError(t *testing.T) {
	id := caller()
	var got []string
	ran := false
	s := failClosedSeam(t)
	s.staff = func(_ context.Context, _ string, members []string) (Role, error) {
		ran, got = true, members
		return Role{Key: "tax-reviewer", Title: "T", Members: []string{}}, nil
	}
	rec := serveRole(t, s, nil, "PUT", "/v1/workflow-roles/tax-reviewer/members", `{"members":[]}`, &id)

	if !ran {
		t.Fatal("the staffer never ran: an empty array was rejected instead of unstaffing")
	}
	if got == nil {
		t.Error("staffer received a nil slice, want a non-nil empty one")
	}
	if len(got) != 0 {
		t.Errorf("staffer received %v, want an empty slice", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkflowRoleHandlers_StaffMembersArriveInSubmittedOrder(t *testing.T) {
	id := caller()
	want := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	var got []string
	s := failClosedSeam(t)
	s.staff = func(_ context.Context, _ string, members []string) (Role, error) {
		got = members
		return Role{Key: "tax-reviewer", Title: "T", Members: members}, nil
	}
	body, err := json.Marshal(map[string]any{"members": want})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := serveRole(t, s, nil, "PUT", "/v1/workflow-roles/tax-reviewer/members", string(body), &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("staffer received %v, want %v in submitted order", got, want)
	}
}

// --- status codes and the four wire keys -----------------------------------

func TestWorkflowRoleHandlers_CreateReturns201AndFourKeys(t *testing.T) {
	id := caller()
	role := Role{Key: "tax-reviewer", Title: "Tax Reviewer", Desc: "", Members: []string{}}
	rec := serveRole(t, okSeam(role), nil, "POST", "/v1/workflow-roles", `{"title":"Tax Reviewer"}`, &id)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (POST /v1/entities and POST /v1/invoices both answer 201): %s",
			rec.Code, rec.Body.String())
	}
	want := []string{"desc", "key", "members", "title"}
	if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("response keys = %v, want exactly %v", got, want)
	}
}

func TestWorkflowRoleHandlers_WriteRoutesReturn200(t *testing.T) {
	role := Role{Key: "tax-reviewer", Title: "Tax Reviewer", Desc: "blurb", Members: []string{"u1"}}
	cases := []struct{ name, method, path, body string }{
		{"update", "PATCH", "/v1/workflow-roles/tax-reviewer", `{"title":"Tax Reviewer"}`},
		{"delete", "DELETE", "/v1/workflow-roles/tax-reviewer", ""},
		{"staff", "PUT", "/v1/workflow-roles/tax-reviewer/members", `{"members":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			rec := serveRole(t, okSeam(role), nil, c.method, c.path, c.body, &id)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			want := []string{"desc", "key", "members", "title"}
			if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("response keys = %v, want exactly %v", got, want)
			}
			var got Role
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if got.Key != role.Key || got.Title != role.Title || got.Desc != role.Desc {
				t.Errorf("body = %+v, want the affected role %+v", got, role)
			}
		})
	}
}

// --- `[]` never `null`, on raw bytes --------------------------------------

func TestWorkflowRoleHandlers_ListSerializesEmptyMembersAsArray(t *testing.T) {
	t.Run("nil members on a role", func(t *testing.T) {
		id := caller()
		s := failClosedSeam(t)
		s.list = func(context.Context) ([]Role, error) {
			return []Role{{Key: "tax-reviewer", Title: "T", Members: nil}}, nil
		}
		rec := serveRole(t, s, nil, "GET", "/v1/workflow-roles", "", &id)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != "workflow_roles" {
			t.Errorf("envelope keys = %v, want exactly [workflow_roles]", got)
		}
		var env struct {
			Roles []json.RawMessage `json:"workflow_roles"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if len(env.Roles) != 1 {
			t.Fatalf("workflow_roles has %d items, want 1", len(env.Roles))
		}
		if got := rawField(t, env.Roles[0], "members"); got != "[]" {
			t.Errorf("members raw JSON = %s, want [] (never null)", got)
		}
	})

	t.Run("nil role slice", func(t *testing.T) {
		id := caller()
		s := failClosedSeam(t)
		s.list = func(context.Context) ([]Role, error) { return nil, nil }
		rec := serveRole(t, s, nil, "GET", "/v1/workflow-roles", "", &id)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := rawField(t, rec.Body.Bytes(), "workflow_roles"); got != "[]" {
			t.Errorf("workflow_roles raw JSON = %s, want [] (never null)", got)
		}
	})
}

func TestWorkflowRoleHandlers_WriteResponsesNeverSerialiseNullMembers(t *testing.T) {
	role := Role{Key: "tax-reviewer", Title: "T", Members: nil}
	cases := []struct{ name, method, path, body string }{
		{"create", "POST", "/v1/workflow-roles", `{"title":"T"}`},
		{"update", "PATCH", "/v1/workflow-roles/tax-reviewer", `{"title":"T"}`},
		{"delete", "DELETE", "/v1/workflow-roles/tax-reviewer", ""},
		{"staff", "PUT", "/v1/workflow-roles/tax-reviewer/members", `{"members":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			rec := serveRole(t, okSeam(role), nil, c.method, c.path, c.body, &id)
			if rec.Code < 200 || rec.Code > 299 {
				t.Fatalf("status = %d, want 2xx: %s", rec.Code, rec.Body.String())
			}
			if got := rawField(t, rec.Body.Bytes(), "members"); got != "[]" {
				t.Errorf("members raw JSON = %s, want [] (never null)", got)
			}
		})
	}
}

// --- statusForErr, on every route -----------------------------------------

func TestWorkflowRoleHandlers_StatusMatrix(t *testing.T) {
	sentinels := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"validation", ErrValidation, http.StatusBadRequest, "invalid request"},
		{"not found", ErrNotFound, http.StatusNotFound, "workflow role not found"},
		{"not permitted", ErrNotPermitted, http.StatusForbidden, "only an admin can change workflow roles"},
		{"conflict", ErrConflict, http.StatusConflict, "that role was just created — try again"},
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"bare error", errors.New("boom"), http.StatusInternalServerError, "internal server error"},
	}
	for _, rt := range roleRoutes {
		for _, s := range sentinels {
			t.Run(rt.name+"/"+s.name, func(t *testing.T) {
				id := caller()
				rec := serveRole(t, errSeam(s.err), slog.New(slog.NewJSONHandler(io.Discard, nil)),
					rt.method, rt.path, rt.body, &id)
				if rec.Code != s.status {
					t.Errorf("status = %d, want %d: %s", rec.Code, s.status, rec.Body.String())
				}
				if got := errorMessage(t, rec.Body.Bytes()); got != s.msg {
					t.Errorf("error = %q, want %q", got, s.msg)
				}
				if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != "error" {
					t.Errorf("error body keys = %v, want exactly [error]", got)
				}
			})
		}
	}
}

// --- 500-only logging ------------------------------------------------------

func TestWorkflowRoleHandlers_ErrorsBelow500AreNotLoggedAtError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		records int
	}{
		{"validation", ErrValidation, http.StatusBadRequest, 0},
		{"not found", ErrNotFound, http.StatusNotFound, 0},
		{"not permitted", ErrNotPermitted, http.StatusForbidden, 0},
		{"conflict", ErrConflict, http.StatusConflict, 0},
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, 0},
		{"bare error", errors.New("boom"), http.StatusInternalServerError, 1},
	}
	for _, rt := range roleRoutes {
		for _, c := range cases {
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				id := caller()
				var buf bytes.Buffer
				rec := serveRole(t, errSeam(c.err), slog.New(slog.NewJSONHandler(&buf, nil)),
					rt.method, rt.path, rt.body, &id)
				if rec.Code != c.status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body.String())
				}
				if got := strings.Count(buf.String(), `"level":"ERROR"`); got != c.records {
					t.Errorf("ERROR log records = %d, want %d: %s", got, c.records, buf.String())
				}
				// The 500 record must still say which surface failed.
				if c.records > 0 && !strings.Contains(buf.String(), "approval") {
					t.Errorf("the 500 log record does not name the approval surface: %s", buf.String())
				}
			})
		}
	}
}

// --- the path token, verbatim ---------------------------------------------

// Normalising would make Tax-Reviewer address tax-reviewer, inventing an alias
// newRoleKey never mints; trimming would do the same for a padded token.
func TestWorkflowRoleHandlers_PathTokenPassedThrough(t *testing.T) {
	tokens := []struct{ name, urlSegment, want string }{
		{"suffixed slug", "tax-reviewer-2", "tax-reviewer-2"},
		{"mixed case and underscore", "Tax_Reviewer", "Tax_Reviewer"},
		{"padded", "%20tax-reviewer%20", " tax-reviewer "},
	}
	for _, tok := range tokens {
		for _, c := range []struct{ name, method, suffix, body string }{
			{"update", "PATCH", "", `{"title":"T"}`},
			{"delete", "DELETE", "", ""},
			{"staff", "PUT", "/members", `{"members":[]}`},
		} {
			t.Run(tok.name+"/"+c.name, func(t *testing.T) {
				id := caller()
				var got string
				ran := false
				role := Role{Key: tok.want, Title: "T", Members: []string{}}
				s := failClosedSeam(t)
				s.update = func(_ context.Context, key string, _, _ *string) (Role, error) {
					ran, got = true, key
					return role, nil
				}
				s.del = func(_ context.Context, key string) (Role, error) {
					ran, got = true, key
					return role, nil
				}
				s.staff = func(_ context.Context, key string, _ []string) (Role, error) {
					ran, got = true, key
					return role, nil
				}
				path := "/v1/workflow-roles/" + tok.urlSegment + c.suffix
				rec := serveRole(t, s, nil, c.method, path, c.body, &id)
				if !ran {
					t.Fatalf("the store never ran for %s %s: %d %s", c.method, path, rec.Code, rec.Body.String())
				}
				if got != tok.want {
					t.Errorf("store received key %q, want %q verbatim (no trim, no lowercase, no re-slugify)", got, tok.want)
				}
			})
		}
	}
}

// --- route resolution on a mux --------------------------------------------

// The five patterns resolve, unlisted verbs on the same paths are 405, and no
// route can deliver an empty {key}.
func TestWorkflowRoleHandlers_RoutesResolveOnAMuxAndUnlistedVerbsAre405(t *testing.T) {
	patterns := []string{
		"GET /v1/workflow-roles",
		"POST /v1/workflow-roles",
		"PATCH /v1/workflow-roles/{key}",
		"DELETE /v1/workflow-roles/{key}",
		"PUT /v1/workflow-roles/{key}/members",
	}
	mux := http.NewServeMux()
	for _, p := range patterns {
		pattern := p
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Matched-Pattern", pattern)
			w.Header().Set("X-Key", r.PathValue("key"))
			w.WriteHeader(http.StatusOK)
		})
	}

	resolves := []struct{ method, path, wantPattern, wantKey string }{
		{"GET", "/v1/workflow-roles", "GET /v1/workflow-roles", ""},
		{"POST", "/v1/workflow-roles", "POST /v1/workflow-roles", ""},
		{"PATCH", "/v1/workflow-roles/tax-reviewer", "PATCH /v1/workflow-roles/{key}", "tax-reviewer"},
		{"DELETE", "/v1/workflow-roles/tax-reviewer", "DELETE /v1/workflow-roles/{key}", "tax-reviewer"},
		{"PUT", "/v1/workflow-roles/tax-reviewer/members", "PUT /v1/workflow-roles/{key}/members", "tax-reviewer"},
	}
	for _, c := range resolves {
		r := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want a handler hit (200)", c.method, c.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("X-Matched-Pattern"); got != c.wantPattern {
			t.Errorf("%s %s matched %q, want %q", c.method, c.path, got, c.wantPattern)
		}
		if got := rec.Header().Get("X-Key"); got != c.wantKey {
			t.Errorf("%s %s key = %q, want %q", c.method, c.path, got, c.wantKey)
		}
	}

	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/workflow-roles/tax-reviewer"},
		{"PUT", "/v1/workflow-roles/tax-reviewer"},
		{"POST", "/v1/workflow-roles/tax-reviewer/members"},
		{"DELETE", "/v1/workflow-roles/tax-reviewer/members"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, rec.Code)
		}
	}

	// A bare trailing slash reaches no handler, so {key} is never "" in a handler.
	for _, path := range []string{"/v1/workflow-roles/", "/v1/workflow-roles//members"} {
		r := httptest.NewRequest("PATCH", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Header().Get("X-Matched-Pattern") != "" {
			t.Errorf("PATCH %s reached %q with key %q, want no handler at all",
				path, rec.Header().Get("X-Matched-Pattern"), rec.Header().Get("X-Key"))
		}
	}
}

// --- the routes are mounted in production ---------------------------------

// The mux above is a copy of the production patterns; this is what pins the copy
// to the real file. An AST walk, not a byte scan, so reformatting main.go cannot
// break it — and it requires app.Mux.HandleFunc, which the document/importer
// scans of the same file also depend on.
func TestWorkflowRoleHandlers_RoutesRegisteredInCmdInvoiceMain(t *testing.T) {
	const path = "../../cmd/invoice/main.go"
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	type reg struct {
		receiver string
		handler  string
		arg0     string
	}
	found := map[string]reg{}
	seen := 0
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		seen++
		pattern := strings.Trim(lit.Value, `"`)
		if !strings.Contains(pattern, "/v1/workflow-roles") {
			return true
		}
		if _, dup := found[pattern]; dup {
			t.Errorf("cmd/invoice/main.go registers %q more than once", pattern)
		}
		r := reg{receiver: exprName(sel.X)}
		if h, ok := call.Args[1].(*ast.CallExpr); ok {
			r.handler = exprName(h.Fun)
			if len(h.Args) > 0 {
				r.arg0 = exprName(h.Args[0])
			}
		}
		found[pattern] = r
		return true
	})

	if seen == 0 {
		t.Fatal("no HandleFunc call found in cmd/invoice/main.go — the scan matched nothing, so every assertion is vacuous")
	}

	want := []struct{ pattern, handler, storeMethod string }{
		{"GET /v1/workflow-roles", "approval.ListRolesHandler", "ListRoles"},
		{"POST /v1/workflow-roles", "approval.CreateRoleHandler", "CreateRole"},
		{"PATCH /v1/workflow-roles/{key}", "approval.UpdateRoleHandler", "UpdateRole"},
		{"DELETE /v1/workflow-roles/{key}", "approval.DeleteRoleHandler", "DeleteRole"},
		{"PUT /v1/workflow-roles/{key}/members", "approval.SetRoleMembersHandler", "SetRoleMembers"},
	}
	for _, w := range want {
		got, ok := found[w.pattern]
		if !ok {
			t.Errorf("cmd/invoice/main.go registers no %q — the route is unreachable in production", w.pattern)
			continue
		}
		if got.receiver != "app.Mux" {
			t.Errorf("%q is registered on %q, want app.Mux", w.pattern, got.receiver)
		}
		if got.handler != w.handler {
			t.Errorf("%q is served by %q, want %q", w.pattern, got.handler, w.handler)
		}
		if !strings.HasSuffix(got.arg0, "."+w.storeMethod) {
			t.Errorf("%q is wired to %q, want a .%s seam", w.pattern, got.arg0, w.storeMethod)
		}
	}
	for pattern := range found {
		if !strings.HasPrefix(pattern, "GET ") && !strings.HasPrefix(pattern, "POST ") &&
			!strings.HasPrefix(pattern, "PATCH ") && !strings.HasPrefix(pattern, "DELETE ") &&
			!strings.HasPrefix(pattern, "PUT ") {
			t.Errorf("workflow-role pattern %q carries no method, so it answers every verb", pattern)
		}
	}
	if len(found) != len(want) {
		t.Errorf("main.go registers %d /v1/workflow-roles routes, want %d: %v", len(found), len(want), keysOfMap(found))
	}
}

// exprName renders `pkg.Name` / `recv.Name` / `Name`, and "" for anything else.
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if x := exprName(v.X); x != "" {
			return x + "." + v.Sel.Name
		}
		return v.Sel.Name
	default:
		return ""
	}
}

func keysOfMap[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
