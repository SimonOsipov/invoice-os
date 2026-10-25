// main_test.go: cmd/submission's wiring specs. main() is not unit-testable -- it calls
// platform.Fatal and connects a real pool -- so the claims are read off main.go's source, the
// cmd/gateway/main_test.go idiom: locate an anchor by name, t.Fatal if it is missing so a
// rename cannot make the scan vacuous, then assert inside a fixed window after it.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os/exec"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// wtRepoRoot locates the worktree root; every module-wide path below is relative to it.
func wtRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; every scan below would read nothing")
	}
	return root
}

// wtSourceWithoutComments re-prints path's Go source without comments, so a byte-window scan
// reads code only.
func wtSourceWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var b strings.Builder
	if err := printer.Fprint(&b, fset, f); err != nil {
		t.Fatalf("print %s: %v", path, err)
	}
	if !strings.Contains(b.String(), "func main()") {
		t.Fatalf("%s stripped to no func main(); a scan of it would read nothing", path)
	}
	return b.String()
}

// wtCallName renders a call target: "queueConfigs" for a package function, "pkg.Name" for a
// qualified one. Anything else yields "".
func wtCallName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return "." + f.Sel.Name
	}
	return ""
}

// wtMainBody returns main()'s top-level statements. Fatal on no main(), so a rename fails
// loudly rather than emptying every scan below.
func wtMainBody(t *testing.T, f *ast.File) []ast.Stmt {
	t.Helper()
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			return fd.Body.List
		}
	}
	t.Fatal("cmd/submission/main.go declares no main(); every assertion below would be vacuous")
	return nil
}

// wtOneCall requires exactly one top-level `x := want(...)` in main(), returning x and the
// call's arguments. Two call sites make "the value main built" ambiguous.
func wtOneCall(t *testing.T, f *ast.File, want string) (string, []ast.Expr) {
	t.Helper()
	var lhs string
	var args []ast.Expr
	calls := 0
	for _, stmt := range wtMainBody(t, f) {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != want {
			continue
		}
		calls++
		args = call.Args
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			lhs = id.Name
		}
	}
	if calls != 1 {
		t.Fatalf("cmd/submission/main.go assigns from %s at the top level of main() %d time(s), want exactly 1", want, calls)
	}
	if lhs == "" {
		t.Fatalf("the %s call assigns to no identifier, so nothing below can follow the value it built", want)
	}
	return lhs, args
}

// wtRender names an expression for a failure message.
func wtRender(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.CallExpr:
		return wtCallName(x.Fun) + "(...)"
	case *ast.SelectorExpr:
		return wtCallName(x)
	}
	return fmt.Sprintf("%T", e)
}

// wtFatalAfter reports whether main() assigns from a call named want as a TOP-LEVEL statement
// (so it is unconditional) and whether the statement right after it is an `if err != nil` that
// terminates the process. calls counts the call sites found, so a zero can be told from a miss.
func wtFatalAfter(t *testing.T, f *ast.File, want string) (calls int, fatal bool) {
	t.Helper()
	body := wtMainBody(t, f)
	for i, stmt := range body {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != want {
			continue
		}
		calls++
		if i+1 >= len(body) {
			continue
		}
		// The call's own err, tested `err != nil`, fatal in the body: `x, _ :=`, `err == nil` or
		// a fatal only in an else checks nothing.
		if last, ok := as.Lhs[len(as.Lhs)-1].(*ast.Ident); !ok || last.Name != "err" {
			continue
		}
		ifs, ok := body[i+1].(*ast.IfStmt)
		if !ok || ifs.Init != nil || !wtIsErrNotNil(ifs.Cond) {
			continue
		}
		ast.Inspect(ifs.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if wtCallName(c.Fun) == "platform.Fatal" {
					fatal = true
				}
			}
			return true
		})
	}
	return calls, fatal
}

func wtIsErrNotNil(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.NEQ {
		return false
	}
	x, xok := b.X.(*ast.Ident)
	y, yok := b.Y.(*ast.Ident)
	return xok && yok && x.Name == "err" && y.Name == "nil"
}

// TestSubmissionMain_FatalOnDocumentConfigError: AC #8. Both object-store calls sit at the top
// level of main(), so the refusal is unconditional the way PORT already is -- gating it on
// whether a route ever reads the store defers the failure to the worst moment.
//
// NOTE for the operator: the submission service carries no DOCUMENT_* variables today, so this
// wiring crash-loops it until they are set. tools/prenv/dsn.go now states that requirement.
func TestSubmissionMain_FatalOnDocumentConfigError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	// Control: the shipped pool site, same matcher, same file. Its fatal is known
	// good, so a false here is the matcher being broken rather than a real finding.
	if calls, fatal := wtFatalAfter(t, f, "db.NewPool"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional db.NewPool call(s), fatal=%v, want 1 and true -- it can no longer recognise a known-good boot fatal, so the findings below mean nothing", calls, fatal)
	}

	for _, want := range []string{"document.ConfigFromEnv", "document.NewS3Store"} {
		calls, fatal := wtFatalAfter(t, f, want)
		if calls == 0 {
			t.Errorf("main() has no unconditional %s call: the object store must be built at boot, not lazily on the first extraction job", want)
			continue
		}
		if calls != 1 {
			t.Errorf("main() calls %s %d times, want 1", want, calls)
		}
		if !fatal {
			t.Errorf("the statement after %s is not an error check that calls platform.Fatal: a malformed object-store configuration must refuse to boot, exactly as PORT does", want)
		}
	}
}

func TestPageKey_MatchesTheDocumentStorageKeyPrefix(t *testing.T) {
	const (
		tenant = "3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11"
		hash   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	prefix := "tenants/" + tenant + "/"

	docKey := document.StorageKey(tenant, hash)
	pageKey := extraction.PageKey(tenant, hash, 1)

	if !strings.HasPrefix(docKey, prefix) {
		t.Fatalf("document.StorageKey = %q, which does not start with %q; the comparison below has no baseline", docKey, prefix)
	}
	if !strings.HasPrefix(pageKey, prefix) {
		t.Errorf("extraction.PageKey = %q, which does not start with %q -- the storage_key CHECK refuses it", pageKey, prefix)
	}
	if pageKey == docKey || strings.HasPrefix(pageKey, docKey) {
		t.Errorf("extraction.PageKey = %q sits at or under document.StorageKey's %q; a rendered page would overwrite the source document it came from", pageKey, docKey)
	}
}

// TestSubmissionMain_RegistersTheExtractionsRoute: GET /v1/extractions must be mounted on
// app.Mux dispatching to extraction.JobsHandler(...), GET /v1/extractions/{id} to
// extraction.DetailHandler(...), GET /v1/extractions/{id}/pages/{n} to
// extraction.PageImageHandler(...), and POST /v1/documents on the same mux
// dispatching to extraction.UploadHandler(...). AST, not a byte scan, so
// gofmt cannot break the anchor. The GET /v1/ping needle is a control: it proves the argument
// matcher still finds a real, already-shipped registration before a negative result is trusted.
// The receiver is asserted too -- a route on a locally built mux is registered and unreachable.
func TestSubmissionMain_RegistersTheExtractionsRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var foundPing, pingOnAppMux, foundExtractions, foundDetail, foundPageImage, foundUpload bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
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
		switch strings.Trim(lit.Value, `"`) {
		case "GET /v1/ping":
			foundPing = true
			pingOnAppMux = wtRender(sel.X) == "app.Mux"
		case "GET /v1/extractions":
			foundExtractions = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "JobsHandler" {
				t.Errorf(`GET /v1/extractions' handler call is not ....JobsHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions' handler is not extraction.JobsHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
		case "GET /v1/extractions/{id}":
			foundDetail = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions/{id} is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions/{id}`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions/{id}' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "DetailHandler" {
				t.Errorf(`GET /v1/extractions/{id}' handler call is not ....DetailHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions/{id}' handler is not extraction.DetailHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
		case "GET /v1/extractions/{id}/pages/{n}":
			foundPageImage = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n} is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions/{id}/pages/{n}`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "PageImageHandler" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' handler call is not ....PageImageHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' handler is not extraction.PageImageHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
			// The object seam is unit-tested over a fake store (page_image_route_test.go), so
			// only this scan can say the handler is built over the REAL bucket. An adapter over
			// anything else streams from nowhere the renderer wrote.
			if len(handlerCall.Args) != 3 {
				t.Errorf("extraction.PageImageHandler is called with %d argument(s), want 3 (key, object, logger)", len(handlerCall.Args))
				return true
			}
			if call, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "docadapter.PageObjectReader" {
				t.Errorf("PageImageHandler's object argument is %s, want a docadapter.PageObjectReader(...) call", wtRender(handlerCall.Args[1]))
			}
		case "POST /v1/documents":
			foundUpload = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`POST /v1/documents is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/documents`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`POST /v1/documents' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "UploadHandler" {
				t.Errorf(`POST /v1/documents' handler call is not ....UploadHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`POST /v1/documents' handler is not extraction.UploadHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
			// The two adapters are unit-tested (upload_storer_test.go) over injected seams, so
			// only this scan can say they are built over the REAL service and pool. A storer
			// over anything but the document service stores nowhere the reader can find.
			if len(handlerCall.Args) != 3 {
				t.Errorf("extraction.UploadHandler is called with %d argument(s), want 3 (store, enqueue, logger)", len(handlerCall.Args))
				return true
			}
			if call, ok := handlerCall.Args[0].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newDocumentStorer" {
				t.Errorf("UploadHandler's store argument is %s, want a newDocumentStorer(...) call", wtRender(handlerCall.Args[0]))
			} else if len(call.Args) != 1 {
				t.Errorf("newDocumentStorer is called with %d argument(s), want 1", len(call.Args))
			} else if sel, ok := call.Args[0].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Store" {
				t.Errorf("newDocumentStorer is built over %s, want the document service's .Store: the reuse flag and the sanitized filename both come off that method", wtRender(call.Args[0]))
			}
			if call, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newExtractionEnqueuer" {
				t.Errorf("UploadHandler's enqueue argument is %s, want a newExtractionEnqueuer(...) call: only that closure puts the business key and the job insert in one transaction", wtRender(handlerCall.Args[1]))
			}
		}
		return true
	})

	if !foundPing {
		t.Fatal("control needle: no GET /v1/ping registration found -- the AST walk itself is broken, so the assertion below is vacuous")
	}
	if !pingOnAppMux {
		t.Fatal("control needle: the GET /v1/ping receiver does not render as app.Mux -- the receiver check above cannot fail, so it proves nothing")
	}
	if !foundExtractions {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions", extraction.JobsHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundDetail {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions/{id}", extraction.DetailHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundPageImage {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions/{id}/pages/{n}", extraction.PageImageHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundUpload {
		t.Error(`no app.Mux.HandleFunc("POST /v1/documents", extraction.UploadHandler(...)) registration found in cmd/submission/main.go`)
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, recv, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == recv
}

func isSelectorCall(e ast.Expr, pkg, name string) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && isSelector(call.Fun, pkg, name)
}

// The matcher is the one TestSubmissionMain_FatalOnDocumentConfigError uses.
func TestSubmissionMain_FatalOnGatewayURLError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}
	if calls, fatal := wtFatalAfter(t, f, "db.NewPool"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional db.NewPool call(s), fatal=%v, want 1 and true -- it can no longer recognise a known-good boot fatal, so the findings below mean nothing", calls, fatal)
	}
	calls, fatal := wtFatalAfter(t, f, "systemhealth.GatewayURL")
	if calls != 1 {
		t.Fatalf("main() assigns from systemhealth.GatewayURL %d time(s) at the top level, want 1: GATEWAY_URL is required at every boot", calls)
	}
	if !fatal {
		t.Error("the statement after systemhealth.GatewayURL is not an error check that calls platform.Fatal: a missing or malformed GATEWAY_URL must refuse to boot")
	}
	_, args := wtOneCall(t, f, "systemhealth.GatewayURL")
	var getenv *ast.CallExpr
	if len(args) == 1 {
		getenv, _ = args[0].(*ast.CallExpr)
	}
	var lit *ast.BasicLit
	if getenv != nil && wtCallName(getenv.Fun) == "os.Getenv" && len(getenv.Args) == 1 {
		lit, _ = getenv.Args[0].(*ast.BasicLit)
	}
	if lit == nil || lit.Value != `"GATEWAY_URL"` {
		t.Error(`systemhealth.GatewayURL is not called with os.Getenv("GATEWAY_URL")`)
	}
}

// The staff routes stay here; cmd/worker runs the sampler (TestWorkerMain_RunsTheHealthSampler).
func TestSubmissionMain_RegistersTheStaffHealthRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}
	type reg struct {
		onAppMux bool
		handler  string
		firstArg string
	}
	found := map[string][]reg{}
	var routes int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) != 2 {
			return true
		}
		routes++
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		r := reg{onAppMux: wtRender(sel.X) == "app.Mux"}
		if h, ok := call.Args[1].(*ast.CallExpr); ok {
			r.handler = wtCallName(h.Fun)
			if len(h.Args) > 0 {
				r.firstArg = wtRender(h.Args[0])
			}
		}
		pattern := strings.Trim(lit.Value, `"`)
		found[pattern] = append(found[pattern], r)
		return true
	})
	if routes < 5 {
		t.Fatalf("control needle: %d HandleFunc call(s) found, want at least 5 -- the AST walk is broken", routes)
	}
	for _, want := range []struct{ route, handler, firstArg string }{
		{"GET /v1/staff/health", "systemhealth.Handler", ""},
		{"GET /v1/staff/metrics", "systemhealth.MetricsHandler", "healthStore.StaffCheck"},
	} {
		t.Run(want.route, func(t *testing.T) {
			regs := found[want.route]
			if len(regs) != 1 {
				t.Fatalf("app.Mux.HandleFunc(%q, ...) registered %d time(s), want exactly 1", want.route, len(regs))
			}
			got := regs[0]
			if !got.onAppMux {
				t.Errorf("%s is not registered on app.Mux -- only app.Mux is served", want.route)
			}
			if got.handler != want.handler {
				t.Errorf("%s's handler is %q, want a %s(...) call", want.route, got.handler, want.handler)
			}
			if want.firstArg != "" && got.firstArg != want.firstArg {
				t.Errorf("%s's first argument is %q, want %s: the database staff check must be wired", want.route, got.firstArg, want.firstArg)
			}
		})
	}
}

func TestSubmissionMain_GatewayURLFatalSitsBeforeThePool(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}
	pos := map[string]token.Pos{}
	for _, stmt := range wtMainBody(t, f) {
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := wtCallName(call.Fun)
			switch {
			case name == "platform.New" || name == "systemhealth.GatewayURL":
				if _, seen := pos[name]; !seen {
					pos[name] = call.Pos()
				}
			case name == "os.Getenv" && len(call.Args) == 1:
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"DATABASE_URL"` {
					pos["DATABASE_URL"] = call.Pos()
				}
			}
			return true
		})
	}
	for _, name := range []string{"platform.New", "systemhealth.GatewayURL", "DATABASE_URL"} {
		if pos[name] == token.NoPos {
			t.Fatalf("main() has no %s call: the order below cannot be read", name)
		}
	}
	if !(pos["platform.New"] < pos["systemhealth.GatewayURL"] && pos["systemhealth.GatewayURL"] < pos["DATABASE_URL"]) {
		t.Errorf("order is platform.New@%d gatewayURL@%d DATABASE_URL@%d, want platform.New < gatewayURL < DATABASE_URL: the GATEWAY_TOKEN refusal stays first and no database is contacted before the GATEWAY_URL refusal", pos["platform.New"], pos["systemhealth.GatewayURL"], pos["DATABASE_URL"])
	}
}

func TestSubmissionMain_RegistersTheStaffJobsRoutes(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}
	want := map[string]string{
		"GET /v1/staff/jobs":               "staffjobs.ListHandler",
		"GET /v1/staff/jobs/{id}":          "staffjobs.GetHandler",
		"POST /v1/staff/jobs/{id}/redrive": "staffjobs.RedriveHandler",
		"POST /v1/staff/jobs/{id}/cancel":  "staffjobs.CancelHandler",
	}
	// A handler of the right name fed the wrong store method (cancel wired to Redrive) would compile.
	store := map[string]string{
		"GET /v1/staff/jobs":               "jobs.List",
		"GET /v1/staff/jobs/{id}":          "jobs.Get",
		"POST /v1/staff/jobs/{id}/redrive": "jobs.Redrive",
		"POST /v1/staff/jobs/{id}/cancel":  "jobs.Cancel",
	}
	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
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
		pattern := strings.Trim(lit.Value, `"`)
		handler, ok := want[pattern]
		if !ok {
			return true
		}
		found[pattern] = true
		if got := wtRender(sel.X); got != "app.Mux" {
			t.Errorf("%s is registered on %s, want app.Mux", pattern, got)
		}
		hc, ok := call.Args[1].(*ast.CallExpr)
		if !ok || wtRender(hc.Fun) != handler {
			t.Errorf("%s handler = %s, want %s(...)", pattern, wtRender(call.Args[1]), handler)
		} else if fed := "nothing"; len(hc.Args) == 0 || wtRender(hc.Args[0]) != store[pattern] {
			if len(hc.Args) > 0 {
				fed = wtRender(hc.Args[0])
			}
			t.Errorf("%s is fed %s, want %s", pattern, fed, store[pattern])
		}
		return true
	})
	for pattern := range want {
		if !found[pattern] {
			t.Errorf("main.go registers no %s", pattern)
		}
	}
}
