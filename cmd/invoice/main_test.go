// main_test.go: APPR-08-02, the route and seam wiring of cmd/invoice/main.go.
// The wiring scan mirrors cmd/submission/main_test.go's idiom (read the sibling source,
// anchor, assert in a window) and ci_gate_test.go's AST walk for the argument shape,
// which reformatting cannot break.
package main

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// TestInvoiceMain_RegistersTheEvidenceBundleRoutes (AUDIT-05-08 AC-3, AUDIT-05-09): GET
// /v1/evidence-bundle and GET /v1/evidence-bundle/preview must both be mounted beside
// GET /v1/audit-log, dispatching to archive.DownloadHandler(...) and
// archive.PreviewHandler(...) respectively. AST, not a byte scan, so gofmt cannot break
// the anchor (TestInvoiceMain_WiresApprovalFactsIntoGetHandler's idiom). The
// GET /v1/audit-log needle is a control: it proves the walk finds a real, already-
// shipped registration before trusting a negative result for the new ones.
func TestInvoiceMain_RegistersTheEvidenceBundleRoutes(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	checkHandler := func(pattern string, call *ast.CallExpr, wantHandler string) {
		handlerCall, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("%s's second argument is %T, want a call expression", pattern, call.Args[1])
			return
		}
		hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
		if !ok || hsel.Sel.Name != wantHandler {
			t.Errorf("%s's handler call is not ....%s(...)", pattern, wantHandler)
			return
		}
		if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "archive" {
			t.Errorf("%s's handler is not archive.%s(...)", pattern, wantHandler)
		}
	}

	var foundAuditLog, foundBundle, foundPreview bool
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
		case "GET /v1/audit-log":
			foundAuditLog = true
		case "GET /v1/evidence-bundle":
			foundBundle = true
			checkHandler("GET /v1/evidence-bundle", call, "DownloadHandler")
		case "GET /v1/evidence-bundle/preview":
			foundPreview = true
			checkHandler("GET /v1/evidence-bundle/preview", call, "PreviewHandler")
		}
		return true
	})

	if !foundAuditLog {
		t.Fatal("control needle: no GET /v1/audit-log registration found -- the AST walk itself is broken, so the assertions below are vacuous")
	}
	if !foundBundle {
		t.Error(`no app.Mux.HandleFunc("GET /v1/evidence-bundle", archive.DownloadHandler(...)) registration found in cmd/invoice/main.go`)
	}
	if !foundPreview {
		t.Error(`no app.Mux.HandleFunc("GET /v1/evidence-bundle/preview", archive.PreviewHandler(...)) registration found in cmd/invoice/main.go`)
	}
}

// AST, so gofmt cannot break the anchor. The GET /v1/imports/{id} needle proves the walk
// finds a shipped registration before a negative result is trusted.
func TestInvoiceMain_RegistersTheSavedMappingRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	var foundBatchGet, foundSavedMapping bool
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
		case "GET /v1/imports/{id}":
			foundBatchGet = true
		case "GET /v1/imports/saved-mapping":
			foundSavedMapping = true

			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf("GET /v1/imports/saved-mapping's second argument is %T, want a call expression", call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "SavedMappingHandler" {
				t.Error("GET /v1/imports/saved-mapping's handler call is not ....SavedMappingHandler(...)")
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "importer" {
				t.Error("GET /v1/imports/saved-mapping's handler is not importer.SavedMappingHandler(...)")
			}
			if len(handlerCall.Args) != 3 {
				t.Fatalf("importer.SavedMappingHandler has %d argument(s), want 3 (open, lookup, logger)", len(handlerCall.Args))
			}
			openArg, ok := handlerCall.Args[0].(*ast.SelectorExpr)
			if !ok || openArg.Sel.Name != "Open" {
				t.Errorf("SavedMappingHandler's first argument is not ....Open, got %#v", handlerCall.Args[0])
			} else if recv, ok := openArg.X.(*ast.Ident); !ok || recv.Name != "docSvc" {
				t.Errorf("SavedMappingHandler's first argument is not docSvc.Open")
			}
			lookupArg, ok := handlerCall.Args[1].(*ast.SelectorExpr)
			if !ok || lookupArg.Sel.Name != "SavedMapping" {
				t.Errorf("SavedMappingHandler's second argument is not ....SavedMapping, got %#v", handlerCall.Args[1])
			} else if recv, ok := lookupArg.X.(*ast.Ident); !ok || recv.Name != "impStore" {
				t.Errorf("SavedMappingHandler's second argument is not impStore.SavedMapping")
			}
		}
		return true
	})

	if !foundBatchGet {
		t.Fatal("control needle: no GET /v1/imports/{id} registration found -- the AST walk itself is broken, so the assertions below are vacuous")
	}
	if !foundSavedMapping {
		t.Error(`no app.Mux.HandleFunc("GET /v1/imports/saved-mapping", importer.SavedMappingHandler(...)) registration found in cmd/invoice/main.go`)
	}
}

// callSiteIndex returns the first index of name+"(" that is not its own
// declaration, so an anchor cannot silently resolve to `func name(`.
// sourceWithoutComments re-prints path's Go source with every comment dropped, so a
// window or count scan reads code and nothing else. Without it a comment naming the
// symbol satisfies the scan while the code it guards is gone.
func sourceWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0) // mode 0 attaches no comments
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var b strings.Builder
	if err := printer.Fprint(&b, fset, f); err != nil {
		t.Fatalf("print %s: %v", path, err)
	}
	src := b.String()
	if len(src) < 4000 || !strings.Contains(src, "func main()") {
		t.Fatalf("%s stripped to %d byte(s) with no func main() -- a truncated read scans nothing and reports clean", path, len(src))
	}
	if strings.Contains(src, "// ") {
		t.Fatalf("%s still carries a line comment after stripping -- the scans below would read prose as code", path)
	}
	return src
}

func callSiteIndex(src, name string) int {
	needle := name + "("
	const decl = "func "
	for i := 0; i < len(src); {
		j := strings.Index(src[i:], needle)
		if j == -1 {
			return -1
		}
		abs := i + j
		if abs < len(decl) || src[abs-len(decl):abs] != decl {
			return abs
		}
		i = abs + len(needle)
	}
	return -1
}

// TestInvoiceMain_WiresApprovalFactsIntoGetHandler (APPR-08-05 AC #8): the third
// argument to invoice.GetHandler is store.ApprovalFacts, not a clear literal or a
// closure -- the submit gate's TransmitClear comes from that method, so bypassing it hands
// every caller an unconditional can_submit. AST, so gofmt or a renamed store variable cannot
// break the anchor.
func TestInvoiceMain_WiresApprovalFactsIntoGetHandler(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "GetHandler" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "invoice" {
			return true
		}
		found = true

		if len(call.Args) != 4 {
			t.Errorf("invoice.GetHandler in cmd/invoice/main.go has %d argument(s), want 4 (get, callerRole, approvalFacts, logger)", len(call.Args))
			return false
		}
		arg, ok := call.Args[2].(*ast.SelectorExpr)
		if !ok {
			t.Errorf("invoice.GetHandler's third argument is %T, want the method value store.ApprovalFacts", call.Args[2])
			return false
		}
		if arg.Sel.Name != "ApprovalFacts" {
			t.Errorf("invoice.GetHandler's third argument is .%s, want .ApprovalFacts", arg.Sel.Name)
		}
		if recv, ok := arg.X.(*ast.Ident); !ok || recv.Name != "store" {
			t.Errorf("invoice.GetHandler's third argument is not store.ApprovalFacts")
		}
		return false
	})
	if !found {
		t.Error("no invoice.GetHandler( call found in cmd/invoice/main.go — this test's anchor moved")
	}
}

// TestInvoiceMain_RegistersTheSuggestMappingRoute (AIR-07-02): the AST walk
// TestInvoiceMain_RegistersTheSavedMappingRoute uses, for the new POST sibling. The
// control needle POST /v1/imports proves the walk finds a real, already-shipped
// registration (and still names CreateHandler) before a negative result is trusted.
func TestInvoiceMain_RegistersTheSuggestMappingRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	var foundCreate, foundSuggest bool
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
		case "POST /v1/imports":
			foundCreate = true
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "CreateHandler" {
				t.Error(`the control needle "POST /v1/imports" no longer names CreateHandler -- this test's anchor moved`)
			}
		case "POST /v1/imports/suggest-mapping":
			foundSuggest = true

			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Fatalf("POST /v1/imports/suggest-mapping's second argument is %T, want a call expression", call.Args[1])
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "SuggestMappingHandler" {
				t.Fatal("POST /v1/imports/suggest-mapping's handler call is not ....SuggestMappingHandler(...)")
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "importer" {
				t.Error("POST /v1/imports/suggest-mapping's handler is not importer.SuggestMappingHandler(...)")
			}
			if len(handlerCall.Args) != 4 {
				t.Fatalf("importer.SuggestMappingHandler has %d argument(s), want 4 (open, lookup, ai client, logger)", len(handlerCall.Args))
			}
			openArg, ok := handlerCall.Args[0].(*ast.SelectorExpr)
			if !ok || openArg.Sel.Name != "Open" {
				t.Errorf("SuggestMappingHandler's first argument is not ....Open, got %#v", handlerCall.Args[0])
			} else if recv, ok := openArg.X.(*ast.Ident); !ok || recv.Name != "docSvc" {
				t.Errorf("SuggestMappingHandler's first argument is not docSvc.Open")
			}
			lookupArg, ok := handlerCall.Args[1].(*ast.SelectorExpr)
			if !ok || lookupArg.Sel.Name != "SavedMapping" {
				t.Errorf("SuggestMappingHandler's second argument is not ....SavedMapping, got %#v", handlerCall.Args[1])
			} else if recv, ok := lookupArg.X.(*ast.Ident); !ok || recv.Name != "impStore" {
				t.Errorf("SuggestMappingHandler's second argument is not impStore.SavedMapping")
			}
			if _, ok := handlerCall.Args[2].(*ast.Ident); !ok {
				t.Errorf("SuggestMappingHandler's third argument is not a plain identifier (the ai client variable), got %#v", handlerCall.Args[2])
			}
			loggerArg, ok := handlerCall.Args[3].(*ast.SelectorExpr)
			if !ok || loggerArg.Sel.Name != "Logger" {
				t.Errorf("SuggestMappingHandler's fourth argument is not ....Logger, got %#v", handlerCall.Args[3])
			} else if recv, ok := loggerArg.X.(*ast.Ident); !ok || recv.Name != "app" {
				t.Errorf("SuggestMappingHandler's fourth argument is not app.Logger")
			}
		}
		return true
	})

	if !foundCreate {
		t.Fatal("control needle: no POST /v1/imports registration found -- the AST walk itself is broken, so the assertions below are vacuous")
	}
	if !foundSuggest {
		t.Error(`no app.Mux.HandleFunc("POST /v1/imports/suggest-mapping", importer.SuggestMappingHandler(...)) registration found in cmd/invoice/main.go`)
	}
}

// TestInvoiceMain_ReadsTheAIKeyOnlyThroughFromEnv (AIR-07-02 Constraint/AC-5) replaces
// the pre-Stage-1 TestInvoiceBoots_WithNoOpenRouterKey, which pinned
// internal/platform/ai's OWN shipped behaviour (already TestFromEnv_NoKeyIsOff in
// env_test.go) and could never turn red from a change in this file. What this file
// owns instead: the key literal appears nowhere here, and ai.FromEnv's result is what
// reaches SuggestMappingHandler.
func TestInvoiceMain_ReadsTheAIKeyOnlyThroughFromEnv(t *testing.T) {
	src := sourceWithoutComments(t, "main.go")

	if n := strings.Count(src, `"OPENROUTER_API_KEY"`); n != 0 {
		t.Errorf(`cmd/invoice/main.go contains the literal "OPENROUTER_API_KEY" %d time(s), want 0 -- the key must be read only inside ai.FromEnv`, n)
	}

	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	var fromEnvVar string
	ast.Inspect(f, func(n ast.Node) bool {
		if fromEnvVar != "" {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) == 0 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "FromEnv" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "ai" {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		fromEnvVar = ident.Name
		return false
	})
	if fromEnvVar == "" {
		t.Fatal("no ai.FromEnv( assignment found in cmd/invoice/main.go -- this test's anchor moved")
	}

	var reachesHandler bool
	ast.Inspect(f, func(n ast.Node) bool {
		if reachesHandler {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SuggestMappingHandler" {
			return true
		}
		for _, arg := range call.Args {
			if ident, ok := arg.(*ast.Ident); ok && ident.Name == fromEnvVar {
				reachesHandler = true
			}
		}
		return true
	})
	if !reachesHandler {
		t.Errorf("ai.FromEnv's result (%s) does not reach a SuggestMappingHandler( call as a plain argument", fromEnvVar)
	}
}

// TestInvoiceMain_AIFakeFailureUsesFatalNotLogFatalf (AIR-07-02 Constraint): an
// unparseable AI_FAKE must stop the boot through fatal(app.Logger, ...), never
// log.Fatalf -- fatal's own doc comment explains why log.Fatalf is silent under
// LOG_LEVEL=warn.
func TestInvoiceMain_AIFakeFailureUsesFatalNotLogFatalf(t *testing.T) {
	src := sourceWithoutComments(t, "main.go")

	idx := callSiteIndex(src, "ai.FromEnv")
	if idx == -1 {
		t.Fatal("cmd/invoice/main.go has no ai.FromEnv( call site -- AIR-07-02's ai client is not wired, or this test's anchor moved")
	}
	end := idx + 400
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]
	if !strings.Contains(window, "fatal(") {
		t.Errorf("no fatal( within 400 bytes after the ai.FromEnv( call site -- an unparseable AI_FAKE must stop the boot:\n%s", window)
	}
	if strings.Contains(window, "log.Fatal") {
		t.Errorf("found log.Fatal within 400 bytes after the ai.FromEnv( call site -- use fatal(app.Logger, ...), which logs at ERROR:\n%s", window)
	}
}
