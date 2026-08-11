package approval

// The six approval-policy HTTP handlers, driven through a real http.ServeMux with
// injected function values. No DSN, no pool, no skip: this file must run in every
// CI job and on a bare `go test ./...`.

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ---------------------------------------------------------------

const policyHandlerTestID = "8f1c2d3e-4a5b-4c6d-8e9f-0a1b2c3d4e5f"

// policySeam bundles the six injected store functions. Every field of a
// failClosedPolicySeam fails the test, so a handler reaching a seam the case did
// not deliberately wire is a failure rather than a silent zero-value call.
type policySeam struct {
	list    PolicyLister
	get     PolicyGetter
	create  PolicyCreator
	put     PolicyDrafter
	publish PolicyPublisher
	del     PolicyDeleter
}

func failClosedPolicySeam(t *testing.T) *policySeam {
	t.Helper()
	return &policySeam{
		list: func(context.Context) ([]Policy, error) {
			t.Fatal("policy lister must not run on a request the handler has to reject")
			return nil, nil
		},
		get: func(context.Context, string) (Policy, error) {
			t.Fatal("policy getter must not run on a request the handler has to reject")
			return Policy{}, nil
		},
		create: func(context.Context, string, string) (Policy, error) {
			t.Fatal("policy creator must not run on a request the handler has to reject")
			return Policy{}, nil
		},
		put: func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
			t.Fatal("policy drafter must not run on a request the handler has to reject")
			return Policy{}, nil
		},
		publish: func(context.Context, string) (Policy, error) {
			t.Fatal("policy publisher must not run on a request the handler has to reject")
			return Policy{}, nil
		},
		del: func(context.Context, string) (Policy, error) {
			t.Fatal("policy deleter must not run on a request the handler has to reject")
			return Policy{}, nil
		},
	}
}

// policyErrSeam wires all six funcs to the same error, so one table drives every route.
func policyErrSeam(err error) *policySeam {
	return &policySeam{
		list:   func(context.Context) ([]Policy, error) { return nil, err },
		get:    func(context.Context, string) (Policy, error) { return Policy{}, err },
		create: func(context.Context, string, string) (Policy, error) { return Policy{}, err },
		put: func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
			return Policy{}, err
		},
		publish: func(context.Context, string) (Policy, error) { return Policy{}, err },
		del:     func(context.Context, string) (Policy, error) { return Policy{}, err },
	}
}

// policyOkSeam wires all six funcs to the same successful Policy.
func policyOkSeam(p Policy) *policySeam {
	return &policySeam{
		list:   func(context.Context) ([]Policy, error) { return []Policy{p}, nil },
		get:    func(context.Context, string) (Policy, error) { return p, nil },
		create: func(context.Context, string, string) (Policy, error) { return p, nil },
		put: func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
			return p, nil
		},
		publish: func(context.Context, string) (Policy, error) { return p, nil },
		del:     func(context.Context, string) (Policy, error) { return p, nil },
	}
}

// policiesMux registers the six patterns cmd/invoice/main.go serves, so {id} is
// populated the way production populates it — a direct ServeHTTP leaves PathValue
// empty. A deliberate copy of the patterns; the copy is pinned against the real
// file by TestPolicyRoutes_RegisteredInCmdInvoiceMain.
func policiesMux(s *policySeam, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/approval-policies", ListPoliciesHandler(s.list, log))
	mux.HandleFunc("POST /v1/approval-policies", CreatePolicyHandler(s.create, log))
	mux.HandleFunc("GET /v1/approval-policies/{id}", GetPolicyHandler(s.get, log))
	mux.HandleFunc("PUT /v1/approval-policies/{id}/draft", PutDraftHandler(s.put, log))
	mux.HandleFunc("POST /v1/approval-policies/{id}/publish", PublishPolicyHandler(s.publish, log))
	mux.HandleFunc("DELETE /v1/approval-policies/{id}", DeletePolicyHandler(s.del, log))
	return mux
}

// servePolicy drives one request through the mux. A nil id means no identity in
// context; body "" means no body at all.
func servePolicy(t *testing.T, s *policySeam, log *slog.Logger, method, path, body string, id *auth.Identity) *httptest.ResponseRecorder {
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
	policiesMux(s, log).ServeHTTP(rec, r)
	return rec
}

// policyRoutes is the six routes with a body that WOULD reach the store, so a
// missing guard shows up as a success rather than as a differently-shaped 400.
var policyRoutes = []struct {
	name       string
	method     string
	path       string
	body       string
	wantStatus int
}{
	{"list", "GET", "/v1/approval-policies", "", http.StatusOK},
	{"create", "POST", "/v1/approval-policies", `{"name":"Standard"}`, http.StatusCreated},
	{"get", "GET", "/v1/approval-policies/" + policyHandlerTestID, "", http.StatusOK},
	{"draft", "PUT", "/v1/approval-policies/" + policyHandlerTestID + "/draft", `{"steps":[]}`, http.StatusOK},
	{"publish", "POST", "/v1/approval-policies/" + policyHandlerTestID + "/publish", "", http.StatusOK},
	{"delete", "DELETE", "/v1/approval-policies/" + policyHandlerTestID, "", http.StatusOK},
}

// --- identity, before anything else ----------------------------------------

func TestPolicyHandlers_IdentityFirst401(t *testing.T) {
	for _, rt := range policyRoutes {
		t.Run(rt.name, func(t *testing.T) {
			s := failClosedPolicySeam(t)
			// Undecodable: a handler that decodes first would answer 400, not 401.
			body := &countingReader{r: strings.NewReader(`{`)}
			r := httptest.NewRequest(rt.method, rt.path, body)
			rec := httptest.NewRecorder()
			policiesMux(s, nil).ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 with no identity in context: %s", rec.Code, rec.Body.String())
			}
			if got := errorMessage(t, rec.Body.Bytes()); got != "unauthorized" {
				t.Errorf("error = %q, want %q", got, "unauthorized")
			}
			if body.read {
				t.Error("the body was read before the identity check")
			}
		})
	}
}

// --- the one semantic 400 this layer owns ----------------------------------

// A nil slice means "clear the tree" at the store, so without the presence check
// {} would silently wipe a policy's steps.
func TestPutDraftHandler_StepsPresenceIsA400(t *testing.T) {
	path := "/v1/approval-policies/" + policyHandlerTestID + "/draft"

	for _, body := range []string{`{}`, `{"steps":null}`} {
		t.Run("rejects "+body, func(t *testing.T) {
			id := caller()
			rec := servePolicy(t, failClosedPolicySeam(t), nil, "PUT", path, body, &id)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s: %s", rec.Code, body, rec.Body.String())
			}
			// Wording is free, but the reason must name the field the SPA has to fix.
			if msg := errorMessage(t, rec.Body.Bytes()); !strings.Contains(strings.ToLower(msg), "steps") {
				t.Errorf("error = %q, want a message naming steps", msg)
			}
		})
	}

	t.Run("accepts an empty array", func(t *testing.T) {
		id := caller()
		var got []stepInput
		ran := false
		s := failClosedPolicySeam(t)
		s.put = func(_ context.Context, _ string, _, _ *string, steps []stepInput) (Policy, error) {
			ran, got = true, steps
			return newPolicy(), nil
		}
		rec := servePolicy(t, s, nil, "PUT", path, `{"steps":[]}`, &id)
		if !ran {
			t.Fatalf(`{"steps":[]} never reached the store: %d %s`, rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got == nil {
			t.Error("the store received a nil slice, which means clear-the-tree; want an empty non-nil slice")
		}
		if len(got) != 0 {
			t.Errorf("the store received %d steps, want 0", len(got))
		}
	})
}

// The body outranks the path id, so a malformed request at an unknown policy reads
// as 400 rather than leaking whether that id exists.
func TestPutDraftHandler_BodyCheckedBeforePathId(t *testing.T) {
	const unknown = "00000000-0000-0000-0000-000000000000"
	cases := []struct {
		name string
		body string
		msg  string // "" means any message
	}{
		{"malformed", `{`, "invalid request body"},
		{"steps absent", `{}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			ran := false
			s := failClosedPolicySeam(t)
			s.put = func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
				ran = true
				return Policy{}, ErrPolicyNotFound
			}
			rec := servePolicy(t, s, nil, "PUT", "/v1/approval-policies/"+unknown+"/draft", c.body, &id)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (the body outranks an unknown id): %s", rec.Code, rec.Body.String())
			}
			if ran {
				t.Error("the store ran; the body checks must come before the path id")
			}
			if c.msg != "" {
				if got := errorMessage(t, rec.Body.Bytes()); got != c.msg {
					t.Errorf("error = %q, want %q", got, c.msg)
				}
			}
		})
	}
}

// --- publish and delete take no body ---------------------------------------

func TestPublishAndDeleteHandlers_ReadNoBody(t *testing.T) {
	junk := strings.Repeat("x", 1<<20) // 1 MiB, not JSON, far over maxPolicyBodyBytes
	want := Policy{ID: policyHandlerTestID, Name: "Standard", Scope: policyScopeAll, Status: "published", Version: 1, Sealed: true}
	cases := []struct{ name, method, path string }{
		{"publish", "POST", "/v1/approval-policies/" + policyHandlerTestID + "/publish"},
		{"delete", "DELETE", "/v1/approval-policies/" + policyHandlerTestID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			ran := false
			s := failClosedPolicySeam(t)
			s.publish = func(context.Context, string) (Policy, error) { ran = true; return want, nil }
			s.del = func(context.Context, string) (Policy, error) { ran = true; return want, nil }

			body := &countingReader{r: strings.NewReader(junk)}
			r := httptest.NewRequest(c.method, c.path, body)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			policiesMux(s, nil).ServeHTTP(rec, r)

			if !ran {
				t.Fatalf("the store never ran; a 1 MiB junk body must not be read at all: %d %s", rec.Code, rec.Body.String())
			}
			if body.read {
				t.Error("the body was read; publish and delete decode nothing")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got := rawField(t, rec.Body.Bytes(), "id"); got != `"`+policyHandlerTestID+`"` {
				t.Errorf("id = %s, want the store's %q", got, policyHandlerTestID)
			}
		})
	}
}

// --- the body cap ----------------------------------------------------------

func TestPolicyHandlers_OversizeBodyIs400(t *testing.T) {
	draftPath := "/v1/approval-policies/" + policyHandlerTestID + "/draft"
	over := strings.Repeat("x", maxPolicyBodyBytes+1)
	under := strings.Repeat("x", maxPolicyBodyBytes/2)

	t.Run("over the cap", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			path   string
			body   string
		}{
			{"create", "POST", "/v1/approval-policies", `{"name":"T","pad":"` + over + `"}`},
			{"draft", "PUT", draftPath, `{"steps":[],"pad":"` + over + `"}`},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				id := caller()
				rec := servePolicy(t, failClosedPolicySeam(t), nil, c.method, c.path, c.body, &id)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (not 413, not 500): %s", rec.Code, rec.Body.String())
				}
				if got := errorMessage(t, rec.Body.Bytes()); got != "invalid request body" {
					t.Errorf("error = %q, want %q", got, "invalid request body")
				}
			})
		}
	})

	// A body UNDER the cap still reaches the store — otherwise the leg above would
	// pass on a handler that rejects every body.
	t.Run("under the cap", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			path   string
			body   string
			want   int
		}{
			{"create", "POST", "/v1/approval-policies", `{"name":"T","pad":"` + under + `"}`, http.StatusCreated},
			{"draft", "PUT", draftPath, `{"steps":[],"pad":"` + under + `"}`, http.StatusOK},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				id := caller()
				ran := false
				s := failClosedPolicySeam(t)
				s.create = func(context.Context, string, string) (Policy, error) { ran = true; return newPolicy(), nil }
				s.put = func(context.Context, string, *string, *string, []stepInput) (Policy, error) {
					ran = true
					return newPolicy(), nil
				}
				rec := servePolicy(t, s, nil, c.method, c.path, c.body, &id)
				if !ran {
					t.Fatalf("a %d-byte body did not reach the store; the cap is below maxPolicyBodyBytes: %d %s",
						len(c.body), rec.Code, rec.Body.String())
				}
				if rec.Code != c.want {
					t.Errorf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
				}
			})
		}
	})
}

// --- statuses, on every route ----------------------------------------------

func TestPolicyHandlers_StatusCodes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		for _, rt := range policyRoutes {
			t.Run(rt.name, func(t *testing.T) {
				id := caller()
				rec := servePolicy(t, policyOkSeam(newPolicy()), nil, rt.method, rt.path, rt.body, &id)
				if rec.Code != rt.wantStatus {
					t.Fatalf("status = %d, want %d (POST creates, every other success is 200): %s",
						rec.Code, rt.wantStatus, rec.Body.String())
				}
			})
		}
	})

	// Every sentinel policyStatusForErr maps. ErrConflict is the concurrent-publish
	// loser and carries the POLICY wording, not statusForErr's role-domain string —
	// the two mappers share the sentinel and nothing else.
	sentinels := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"validation", ErrValidation, http.StatusBadRequest, "invalid request"},
		{"not permitted", ErrNotPermitted, http.StatusForbidden, "only an admin can change approval policies"},
		{"not found", ErrPolicyNotFound, http.StatusNotFound, "approval policy not found"},
		{"step role", ErrPolicyStepRole, http.StatusConflict, "an approval step names a workflow role that no longer exists"},
		{"empty branches", ErrPolicyEmptyBranches, http.StatusConflict, "a condition must have at least one step in one of its two lanes"},
		{"nothing to publish", ErrPolicyNothingToPublish, http.StatusConflict, "this policy has no unpublished changes"},
		{"conflict", ErrConflict, http.StatusConflict, "another version was published first — reload the policy and try again"},
		{"bare error", errors.New("boom"), http.StatusInternalServerError, "internal server error"},
	}
	for _, rt := range policyRoutes {
		for _, s := range sentinels {
			t.Run(rt.name+"/"+s.name, func(t *testing.T) {
				id := caller()
				rec := servePolicy(t, policyErrSeam(s.err), slog.New(slog.NewJSONHandler(io.Discard, nil)),
					rt.method, rt.path, rt.body, &id)
				if rec.Code != s.status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, s.status, rec.Body.String())
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

// The scope vocabulary is the STORE's (normalizeScope), so both writers must forward
// a foreign scope rather than judge it. One table over both doors, so a handler
// wired to only one of them fails.
func TestPolicyHandlers_ForeignScopeIs400OnBothWriters(t *testing.T) {
	const foreign = "Capex & fixed assets"
	cases := []struct{ name, method, path, body string }{
		{"create", "POST", "/v1/approval-policies", `{"name":"x","scope":"` + foreign + `"}`},
		{"draft", "PUT", "/v1/approval-policies/" + policyHandlerTestID + "/draft",
			`{"steps":[],"scope":"` + foreign + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			ran := false
			got := ""
			s := failClosedPolicySeam(t)
			s.create = func(_ context.Context, _, scope string) (Policy, error) {
				ran, got = true, scope
				return Policy{}, ErrValidation
			}
			s.put = func(_ context.Context, _ string, _, scope *string, _ []stepInput) (Policy, error) {
				ran = true
				if scope != nil {
					got = *scope
				}
				return Policy{}, ErrValidation
			}
			rec := servePolicy(t, s, nil, c.method, c.path, c.body, &id)
			if !ran {
				t.Fatalf("%s never reached the store; the scope vocabulary is the store's: %d %s",
					c.name, rec.Code, rec.Body.String())
			}
			if got != foreign {
				t.Errorf("the store received scope %q, want %q verbatim", got, foreign)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if msg := errorMessage(t, rec.Body.Bytes()); msg != "invalid request" {
				t.Errorf("error = %q, want %q", msg, "invalid request")
			}
		})
	}
}

// --- 500-only logging ------------------------------------------------------

func TestPolicyHandlers_LogsOnlyOn500(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		records int
	}{
		{"not found", ErrPolicyNotFound, http.StatusNotFound, 0},
		{"bare error", errors.New("boom"), http.StatusInternalServerError, 1},
	}
	for _, rt := range policyRoutes {
		for _, c := range cases {
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				id := caller()
				var buf bytes.Buffer
				rec := servePolicy(t, policyErrSeam(c.err), slog.New(slog.NewJSONHandler(&buf, nil)),
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

// --- the list envelope -----------------------------------------------------

// Policy.MarshalJSON only fixes a policy's OWN nil lanes; the outer []Policy is a
// plain slice with no omitempty, so a nil result still renders null unless the
// handler rebuilds it.
func TestPolicyHandlers_ListIsNeverNull(t *testing.T) {
	id := caller()
	s := failClosedPolicySeam(t)
	s.list = func(context.Context) ([]Policy, error) { return nil, nil }
	rec := servePolicy(t, s, nil, "GET", "/v1/approval-policies", "", &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != "approval_policies" {
		t.Errorf("envelope keys = %v, want exactly [approval_policies]", got)
	}
	if got := rawField(t, rec.Body.Bytes(), "approval_policies"); got != "[]" {
		t.Errorf("approval_policies raw JSON = %s, want [] (never null)", got)
	}
}

// --- route resolution on a mux ---------------------------------------------

func TestPolicyRoutes_RegisteredOnMux(t *testing.T) {
	t.Run("the six resolve and carry their path id", func(t *testing.T) {
		wantID := map[string]string{
			"list": "", "create": "",
			"get": policyHandlerTestID, "draft": policyHandlerTestID,
			"publish": policyHandlerTestID, "delete": policyHandlerTestID,
		}
		for _, rt := range policyRoutes {
			t.Run(rt.name, func(t *testing.T) {
				id := caller()
				gotID := ""
				ran := false
				record := func(pathID string) (Policy, error) {
					ran, gotID = true, pathID
					return newPolicy(), nil
				}
				s := &policySeam{
					list:    func(context.Context) ([]Policy, error) { ran = true; return nil, nil },
					create:  func(context.Context, string, string) (Policy, error) { ran = true; return newPolicy(), nil },
					get:     func(_ context.Context, pid string) (Policy, error) { return record(pid) },
					put:     func(_ context.Context, pid string, _, _ *string, _ []stepInput) (Policy, error) { return record(pid) },
					publish: func(_ context.Context, pid string) (Policy, error) { return record(pid) },
					del:     func(_ context.Context, pid string) (Policy, error) { return record(pid) },
				}
				rec := servePolicy(t, s, nil, rt.method, rt.path, rt.body, &id)
				if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
					t.Fatalf("%s %s = %d, want a registered handler", rt.method, rt.path, rec.Code)
				}
				if !ran {
					t.Fatalf("%s %s reached no store seam: %d %s", rt.method, rt.path, rec.Code, rec.Body.String())
				}
				if rec.Code != rt.wantStatus {
					t.Errorf("%s %s = %d, want %d: %s", rt.method, rt.path, rec.Code, rt.wantStatus, rec.Body.String())
				}
				if gotID != wantID[rt.name] {
					t.Errorf("%s %s delivered id %q, want %q", rt.method, rt.path, gotID, wantID[rt.name])
				}
			})
		}
	})

	t.Run("unlisted verbs are 405", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{"PUT", "/v1/approval-policies"},
			{"DELETE", "/v1/approval-policies"},
			{"PATCH", "/v1/approval-policies/" + policyHandlerTestID},
			{"POST", "/v1/approval-policies/" + policyHandlerTestID},
			{"GET", "/v1/approval-policies/" + policyHandlerTestID + "/draft"},
			{"GET", "/v1/approval-policies/" + policyHandlerTestID + "/publish"},
		} {
			r := httptest.NewRequest(c.method, c.path, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", c.method, c.path, rec.Code)
			}
		}
	})

	// A bare trailing slash reaches no handler, so {id} is never "" in a handler.
	t.Run("no route delivers an empty id", func(t *testing.T) {
		for _, path := range []string{"/v1/approval-policies/", "/v1/approval-policies//draft"} {
			r := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()
			policiesMux(failClosedPolicySeam(t), nil).ServeHTTP(rec, r)
			if rec.Code == http.StatusOK {
				t.Errorf("GET %s reached a handler, want no match at all", path)
			}
		}
	})
}

// --- the routes are mounted in production ----------------------------------

// The mux above is a copy of the production patterns; this is what pins the copy to
// the real file. An AST walk, not a byte scan, so reformatting main.go cannot break
// it. Mirrors TestWorkflowRoleHandlers_RoutesRegisteredInCmdInvoiceMain, filtered on
// this story's prefix so the two scans never contend.
func TestPolicyRoutes_RegisteredInCmdInvoiceMain(t *testing.T) {
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
		if !strings.Contains(pattern, "/v1/approval-policies") {
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
		{"GET /v1/approval-policies", "approval.ListPoliciesHandler", "ListPolicies"},
		{"POST /v1/approval-policies", "approval.CreatePolicyHandler", "CreatePolicy"},
		{"GET /v1/approval-policies/{id}", "approval.GetPolicyHandler", "GetPolicy"},
		{"PUT /v1/approval-policies/{id}/draft", "approval.PutDraftHandler", "PutDraft"},
		{"POST /v1/approval-policies/{id}/publish", "approval.PublishPolicyHandler", "PublishPolicy"},
		{"DELETE /v1/approval-policies/{id}", "approval.DeletePolicyHandler", "DeletePolicy"},
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
			!strings.HasPrefix(pattern, "PUT ") && !strings.HasPrefix(pattern, "DELETE ") {
			t.Errorf("approval-policy pattern %q carries no method, so it answers every verb", pattern)
		}
	}
	if len(found) != len(want) {
		t.Errorf("main.go registers %d /v1/approval-policies routes, want %d: %v", len(found), len(want), keysOfMap(found))
	}
}
