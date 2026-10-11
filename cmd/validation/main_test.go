// main_test.go: how cmd/validation mounts PATCH /v1/rules/{key}, and what that route answers
// for each identity shape the gateway forwards. No database, no listener.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const (
	rulesRoute  = "PATCH /v1/rules/{key}"
	refusal403  = `{"error":"rules are managed by ASComply"}` + "\n"
	unauthed401 = `{"error":"unauthorized"}` + "\n"
)

// rulesRouteProblems reads src's registrations of rulesRoute. Nothing in Go reads a mux
// pattern, so a PUT spelling or a different handler compiles and vets clean.
func rulesRouteProblems(t *testing.T, src string) (registrations int, problems []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || strings.Trim(lit.Value, `"`) != rulesRoute {
			return true
		}
		registrations++
		if mux, ok := sel.X.(*ast.SelectorExpr); !ok || mux.Sel.Name != "Mux" {
			problems = append(problems, "not registered on app.Mux")
		}
		h, ok := call.Args[1].(*ast.CallExpr)
		if !ok || len(h.Args) != 0 {
			problems = append(problems, "handler is not a zero-argument call")
			return true
		}
		hs, ok := h.Fun.(*ast.SelectorExpr)
		if !ok || hs.Sel.Name != "ToggleHandler" || exprName(hs.X) != "validation" {
			problems = append(problems, "handler is not validation.ToggleHandler()")
		}
		return true
	})
	return registrations, problems
}

func exprName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func TestValidationMain_MountsToggleHandlerOnPatchRulesRoute(t *testing.T) {
	t.Run("the scan tells the shipped wiring from a wrong one", func(t *testing.T) {
		const wrap = "package main\nfunc main() {\n%s\n}\n"
		for _, tc := range []struct {
			name, call  string
			wantRegs    int
			wantProblem bool
		}{
			{"shipped", `app.Mux.HandleFunc("PATCH /v1/rules/{key}", validation.ToggleHandler())`, 1, false},
			{"other method", `app.Mux.HandleFunc("PUT /v1/rules/{key}", validation.ToggleHandler())`, 0, false},
			{"other handler", `app.Mux.HandleFunc("PATCH /v1/rules/{key}", validation.BatchValidateHandler())`, 1, true},
			{"handler with args", `app.Mux.HandleFunc("PATCH /v1/rules/{key}", validation.ToggleHandler(store))`, 1, true},
			{"local mux", `mux.HandleFunc("PATCH /v1/rules/{key}", validation.ToggleHandler())`, 1, true},
		} {
			regs, problems := rulesRouteProblems(t, strings.Replace(wrap, "%s", tc.call, 1))
			if regs != tc.wantRegs {
				t.Fatalf("%s: found %d registration(s), want %d — the scan cannot judge what it never read", tc.name, regs, tc.wantRegs)
			}
			if (len(problems) > 0) != tc.wantProblem {
				t.Errorf("%s: problems = %v, wantProblem = %v", tc.name, problems, tc.wantProblem)
			}
		}
	})

	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	regs, problems := rulesRouteProblems(t, string(raw))
	if regs != 1 {
		t.Fatalf("main.go registers %q %d time(s), want exactly 1", rulesRoute, regs)
	}
	for _, p := range problems {
		t.Errorf("main.go: %s", p)
	}
}

// The gateway forwards X-Tenant-ID, X-User-ID, X-User-Role and X-User-Email after it verifies
// the token; platform.Handler rebuilds the Identity from them. The role is a free string and the
// tenant is any tenant, so no value of either may reach anything but the refusal.
func TestValidationMain_PatchRulesAnswersEveryForwardedIdentity(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	app, err := platform.New("validation")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc(rulesRoute, validation.ToggleHandler())
	h := app.Handler()

	const tenantA, tenantB = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	user := uuid.NewString()
	cases := []struct {
		name    string
		headers map[string]string
		status  int
		body    string
	}{
		{"member of tenant A", map[string]string{"X-Tenant-ID": tenantA, "X-User-ID": user, "X-User-Role": "authenticated"}, 403, refusal403},
		{"member of another tenant", map[string]string{"X-Tenant-ID": tenantB, "X-User-ID": user, "X-User-Role": "authenticated"}, 403, refusal403},
		{"with email", map[string]string{"X-Tenant-ID": tenantA, "X-User-ID": user, "X-User-Role": "authenticated", "X-User-Email": "a@example.test"}, 403, refusal403},
		{"service_role", map[string]string{"X-Tenant-ID": tenantA, "X-User-ID": user, "X-User-Role": "service_role"}, 403, refusal403},
		{"anon role", map[string]string{"X-Tenant-ID": tenantA, "X-User-ID": user, "X-User-Role": "anon"}, 403, refusal403},
		{"owner role name", map[string]string{"X-Tenant-ID": tenantA, "X-User-ID": user, "X-User-Role": "invoice_migrator"}, 403, refusal403},
		{"tenant header alone", map[string]string{"X-Tenant-ID": tenantA}, 401, unauthed401},
		{"user without a tenant", map[string]string{"X-User-ID": user, "X-User-Role": "authenticated"}, 401, unauthed401},
		{"no identity headers", map[string]string{}, 401, unauthed401},
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/v1/rules/vat-standard-rate", strings.NewReader(`{"enabled":false}`))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, tc.status, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
		})
	}
}

// Each staff route is registered once (a second owner of a pattern panics ServeMux at boot);
// other /v1/staff patterns are allowed.
func TestValidationMain_StaffRoutesAreRegisteredAndToggleStays(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", string(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern := strings.Trim(lit.Value, `"`)
		name := "?"
		if h, ok := call.Args[1].(*ast.CallExpr); ok {
			if hs, ok := h.Fun.(*ast.SelectorExpr); ok {
				name = exprName(hs.X) + "." + hs.Sel.Name
			}
		}
		handlers[pattern] = append(handlers[pattern], name)
		return true
	})
	for pattern, want := range map[string]string{
		"PATCH /v1/rules/{key}":       "validation.ToggleHandler",
		"GET /v1/staff/rules":         "validation.StaffListRulesHandler",
		"PATCH /v1/staff/rules/{key}": "validation.StaffSwitchRuleHandler",
		"GET /v1/staff/rule-versions": "validation.StaffVersionsHandler",
	} {
		if got := handlers[pattern]; len(got) != 1 || got[0] != want {
			t.Errorf("%q registered with %v, want exactly [%s]", pattern, got, want)
		}
	}
}
