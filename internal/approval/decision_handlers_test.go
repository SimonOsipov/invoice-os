package approval

// RunHandler's tests, driven through a real http.ServeMux with an injected RunReader.
// No DSN, no pool, no skip — httptest only, matching policiesMux/servePolicy
// (policy_handlers_test.go:103-129). exprName/keysOfMap are reused from
// handlers_test.go, not redefined.

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ---------------------------------------------------------------

const runHandlerTestID = "9f1c2d3e-4a5b-4c6d-8e9f-0a1b2c3d4e60"

// failClosedRun fails the test if the seam runs at all, so a request the handler
// has to reject before calling it shows up as a failure rather than a silent call.
func failClosedRun(t *testing.T) RunReader {
	t.Helper()
	return func(context.Context, string) (Run, error) {
		t.Fatal("run reader must not run on a request the handler has to reject")
		return Run{}, nil
	}
}

// runMux registers the one pattern cmd/invoice/main.go serves, so {id} is populated
// the way production populates it. Pinned against the real file by
// TestApprovalRoutesRegisteredInCmdInvoiceMain.
func runMux(read RunReader, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/invoices/{id}/approval", RunHandler(read, log))
	return mux
}

// serveRun drives one request through the mux. A nil id means no identity in context.
func serveRun(t *testing.T, read RunReader, log *slog.Logger, path string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	runMux(read, log).ServeHTTP(rec, r)
	return rec
}

// --- identity, before anything else ----------------------------------------

func TestRunHandler_NoIdentityIs401(t *testing.T) {
	rec := serveRun(t, failClosedRun(t), nil, "/v1/invoices/"+runHandlerTestID+"/approval", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no identity in context: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "unauthorized" {
		t.Errorf("error = %q, want %q", got, "unauthorized")
	}
}

// --- decisionStatusForErr, through the handler ------------------------------

func TestRunHandler_NotFoundEnvelope(t *testing.T) {
	id := caller()
	read := func(context.Context, string) (Run, error) { return Run{}, ErrRunNotFound }
	rec := serveRun(t, read, nil, "/v1/invoices/"+runHandlerTestID+"/approval", &id)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != "error" {
		t.Errorf("error body keys = %v, want exactly [error]", got)
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "no approval run for this invoice" {
		t.Errorf("error = %q, want %q", got, "no approval run for this invoice")
	}
}

// AC-3: decisionStatusForErr's db.ErrNoTenant case must match statusForErr/
// policyStatusForErr's, same as every other approval route.
func TestRunHandler_NoTenantIs401(t *testing.T) {
	id := caller()
	read := func(context.Context, string) (Run, error) { return Run{}, db.ErrNoTenant }
	rec := serveRun(t, read, nil, "/v1/invoices/"+runHandlerTestID+"/approval", &id)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "unauthorized" {
		t.Errorf("error = %q, want %q", got, "unauthorized")
	}
}

// AC-2: unknown id, cross-tenant id, malformed non-uuid id, and invoice-with-no-run
// all collapse into ErrRunNotFound before this handler ever sees which one it was
// (read_model.go:96-116), so the wire body must be byte-identical across all four —
// the no-existence-oracle guarantee.
func TestRunHandler_FourNotFoundCausesAreByteIdentical(t *testing.T) {
	causes := []struct{ name, id string }{
		{"unknown id", "11111111-1111-1111-1111-111111111111"},
		{"cross-tenant id", "22222222-2222-2222-2222-222222222222"},
		{"malformed non-uuid id", "not-a-uuid"},
		{"invoice with no run", "33333333-3333-3333-3333-333333333333"},
	}
	read := func(context.Context, string) (Run, error) { return Run{}, ErrRunNotFound }

	var want []byte
	for i, c := range causes {
		id := caller()
		rec := serveRun(t, read, nil, "/v1/invoices/"+c.id+"/approval", &id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404: %s", c.name, rec.Code, rec.Body.String())
			continue
		}
		if i == 0 {
			want = rec.Body.Bytes()
			continue
		}
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: body = %s, want byte-identical to %q's %s", c.name, rec.Body.String(), causes[0].name, want)
		}
	}
}

// --- success -----------------------------------------------------------------

// AC-4: the top-level body carries the Run's own keys, not an {"...": {...}} wrapper.
func TestRunHandler_SuccessIsABareObject(t *testing.T) {
	id := caller()
	want := Run{
		RunID:     runHandlerTestID,
		State:     "pending",
		OpenedAt:  time.Now(),
		Steps:     []RunStep{{Ord: 1, Kind: "approval", State: "pending"}},
		Decisions: []RunDecision{},
	}
	read := func(context.Context, string) (Run, error) { return want, nil }
	rec := serveRun(t, read, nil, "/v1/invoices/"+runHandlerTestID+"/approval", &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	wantKeys := []string{"closed_at", "closed_by", "decisions", "opened_at", "run_id", "state", "steps"}
	if got := keySet(t, rec.Body.Bytes()); strings.Join(got, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("response keys = %v, want exactly %v (a bare Run, no envelope)", got, wantKeys)
	}
	var got Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.RunID != want.RunID || got.State != want.State {
		t.Errorf("body = %+v, want the seam's run %+v", got, want)
	}
}

// AC-4's other half: Run.MarshalJSON's []-never-null rule must survive the handler.
func TestRunHandler_NilCollectionsSerialiseAsArrays(t *testing.T) {
	id := caller()
	read := func(context.Context, string) (Run, error) {
		return Run{RunID: runHandlerTestID, State: "pending", OpenedAt: time.Now(), Steps: nil, Decisions: nil}, nil
	}
	rec := serveRun(t, read, nil, "/v1/invoices/"+runHandlerTestID+"/approval", &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rawField(t, rec.Body.Bytes(), "steps"); got != "[]" {
		t.Errorf("steps raw JSON = %s, want [] (never null)", got)
	}
	if got := rawField(t, rec.Body.Bytes(), "decisions"); got != "[]" {
		t.Errorf("decisions raw JSON = %s, want [] (never null)", got)
	}
}

// AC-5: "authenticated" carries no membership/access-role — any caller holding a
// tenant claim may read this route, no approver check.
func TestRunHandler_NoRoleGate(t *testing.T) {
	id := caller()
	read := func(context.Context, string) (Run, error) {
		return Run{RunID: runHandlerTestID, State: "pending", OpenedAt: time.Now()}, nil
	}
	rec := serveRun(t, read, nil, "/v1/invoices/"+runHandlerTestID+"/approval", &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no role gate on this route): %s", rec.Code, rec.Body.String())
	}
}

// --- the route is mounted in production -------------------------------------

// Filtered on this story's prefix so it never contends with
// TestWorkflowRoleHandlers_RoutesRegisteredInCmdInvoiceMain's /v1/workflow-roles
// filter (D33) — that scan cannot see this route and stays untouched.
//
// One row only: subtask 06 adds POST /v1/invoices/{id}/approvals under the same
// prefix and owns the len(found) == len(want) exhaustiveness check once both
// routes exist (D33 5b) — copying it here now would go red the moment that POST
// lands.
func TestApprovalRoutesRegisteredInCmdInvoiceMain(t *testing.T) {
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
		if !strings.Contains(pattern, "/v1/invoices/{id}/approval") {
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
		{"GET /v1/invoices/{id}/approval", "approval.RunHandler", "ApprovalRun"},
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
		if !strings.HasPrefix(pattern, "GET ") && !strings.HasPrefix(pattern, "POST ") {
			t.Errorf("approval-run pattern %q carries no method, so it answers every verb", pattern)
		}
	}
}
