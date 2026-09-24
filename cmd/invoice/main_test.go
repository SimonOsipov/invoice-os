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
	"net/http"
	"slices"
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

// TestInvoiceMain_AIFakeFailureUsesFatalNotLogFatalf: an unparseable AI_FAKE stops the
// boot through fatal(app.Logger, ...), never log.Fatalf (see fatal's doc comment). The
// window ends at jev.FromEnv( so the Jev block's fatal( cannot satisfy it.
func TestInvoiceMain_AIFakeFailureUsesFatalNotLogFatalf(t *testing.T) {
	src := sourceWithoutComments(t, "main.go")

	for _, msg := range aiGuard(src) {
		t.Error(msg)
	}
}

// TestInvoiceMain_EachFromEnvGuardRedsOnlyOnItsOwnBranch: each guard reds on its own
// branch's deletion and only on that. A separate test, so a Jev regression does not red
// the AI guard's test.
func TestInvoiceMain_EachFromEnvGuardRedsOnlyOnItsOwnBranch(t *testing.T) {
	src := sourceWithoutComments(t, "main.go")
	t.Run("AIBranchDeleted", func(t *testing.T) {
		mut := dropErrBranchAfter(t, src, "ai.FromEnv")
		if len(aiGuard(mut)) == 0 {
			t.Error("the AI guard stays green with the ai.FromEnv error branch deleted")
		}
		if msgs := jevGuard(mut); len(msgs) != 0 {
			t.Errorf("the Jev guard reds on the AI branch's deletion: %v", msgs)
		}
	})
	t.Run("JevBranchDeleted", func(t *testing.T) {
		mut := dropErrBranchAfter(t, src, "jev.FromEnv")
		if msgs := aiGuard(mut); len(msgs) != 0 {
			t.Errorf("the AI guard reds on the Jev branch's deletion: %v", msgs)
		}
		if len(jevGuard(mut)) == 0 {
			t.Error("the Jev guard stays green with the jev.FromEnv error branch deleted")
		}
	})
}

// TestInvoiceMain_JevFromEnvFailureUsesFatalNotLogFatalf: both jev.FromEnv errors stop
// the boot through fatal(app.Logger, ...), never log.Fatal.
func TestInvoiceMain_JevFromEnvFailureUsesFatalNotLogFatalf(t *testing.T) {
	for _, msg := range jevGuard(sourceWithoutComments(t, "main.go")) {
		t.Error(msg)
	}
}

func aiGuard(src string) []string {
	return fatalGuard(src, "ai.FromEnv", "jev.FromEnv")
}

func jevGuard(src string) []string {
	return fatalGuard(src, "jev.FromEnv", "app.Mux.HandleFunc")
}

// fatalGuard reports why the code from from( to the next end( lacks fatal( or holds log.Fatal.
func fatalGuard(src, from, end string) []string {
	i := callSiteIndex(src, from)
	if i == -1 {
		return []string{"cmd/invoice/main.go has no " + from + "( call site"}
	}
	j := callSiteIndex(src[i:], end)
	if j == -1 {
		return []string{"cmd/invoice/main.go has no " + end + "( after " + from + "( to end the window"}
	}
	window := src[i : i+j]
	var msgs []string
	if !strings.Contains(window, "fatal(") {
		msgs = append(msgs, "no fatal( between "+from+"( and "+end+"(:\n"+window)
	}
	if strings.Contains(window, "log.Fatal") {
		msgs = append(msgs, "log.Fatal between "+from+"( and "+end+"( -- use fatal(app.Logger, ...):\n"+window)
	}
	return msgs
}

// dropErrBranchAfter deletes the first `if err != nil {...}` after anchor( from src.
func dropErrBranchAfter(t *testing.T, src, anchor string) string {
	t.Helper()
	i := callSiteIndex(src, anchor)
	if i == -1 {
		t.Fatalf("no %s( call site to mutate", anchor)
	}
	const head = "if err != nil {"
	k := strings.Index(src[i:], head)
	if k == -1 {
		t.Fatalf("no %q after %s(", head, anchor)
	}
	open := i + k + len(head) - 1
	if next := callSiteIndex(src[i:], "app.Mux.HandleFunc"); next != -1 && i+next < open {
		t.Fatalf("the first %q after %s( lies past the next app.Mux.HandleFunc( -- not its error branch", head, anchor)
	}
	depth := 0
	for p := open; p < len(src); p++ {
		switch src[p] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[:i+k] + src[p+1:]
			}
		}
	}
	t.Fatalf("unbalanced braces after %s(", anchor)
	return ""
}

// TestInvoiceMain_RegistersTheCheckMappingRoute: the check route is mounted with
// importer.CheckMappingHandler(docSvc.Open, <client>, app.Logger). POST /v1/imports is
// the control that proves the walk sees a shipped registration.
func TestInvoiceMain_RegistersTheCheckMappingRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	var foundCreate, foundCheck bool
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
			if !isSelectorCall(call.Args[1], "importer", "CreateHandler") {
				t.Error(`the control needle "POST /v1/imports" no longer names importer.CreateHandler`)
			}
		case "POST /v1/imports/check-mapping":
			foundCheck = true
			hc, ok := call.Args[1].(*ast.CallExpr)
			if !ok || !isSelector(hc.Fun, "importer", "CheckMappingHandler") {
				t.Fatalf("POST /v1/imports/check-mapping's handler is not importer.CheckMappingHandler(...), got %#v", call.Args[1])
			}
			if len(hc.Args) != 3 {
				t.Fatalf("importer.CheckMappingHandler has %d argument(s), want 3 (open, jev client, logger)", len(hc.Args))
			}
			if !isSelector(hc.Args[0], "docSvc", "Open") {
				t.Errorf("CheckMappingHandler's first argument is not docSvc.Open, got %#v", hc.Args[0])
			}
			if _, ok := hc.Args[1].(*ast.Ident); !ok {
				t.Errorf("CheckMappingHandler's second argument is not a plain identifier (the jev client), got %#v", hc.Args[1])
			}
			if !isSelector(hc.Args[2], "app", "Logger") {
				t.Errorf("CheckMappingHandler's third argument is not app.Logger, got %#v", hc.Args[2])
			}
		}
		return true
	})

	if !foundCreate {
		t.Fatal("control needle: no POST /v1/imports registration found -- the AST walk is broken")
	}
	if !foundCheck {
		t.Error(`no app.Mux.HandleFunc("POST /v1/imports/check-mapping", importer.CheckMappingHandler(...)) registration found in cmd/invoice/main.go`)
	}
}

// TestInvoiceMain_BuildsTheJevClientOnceAndHandsItToTheCheck: main() assigns from
// jev.FromEnv once, as a statement of its own body, and that variable is
// CheckMappingHandler's second argument.
func TestInvoiceMain_BuildsTheJevClientOnceAndHandsItToTheCheck(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}
	fromEnvAssign := func(n ast.Node, pkg string) (*ast.AssignStmt, bool) {
		a, ok := n.(*ast.AssignStmt)
		if !ok || len(a.Rhs) != 1 || len(a.Lhs) == 0 {
			return nil, false
		}
		return a, isSelectorCall(a.Rhs[0], pkg, "FromEnv")
	}

	var total, aiTotal int
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := fromEnvAssign(n, "jev"); ok {
			total++
		}
		if _, ok := fromEnvAssign(n, "ai"); ok {
			aiTotal++
		}
		return true
	})
	if aiTotal != 1 {
		t.Fatalf("control: found %d ai.FromEnv assignment(s), want 1 -- the walk is broken", aiTotal)
	}

	var mainFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			mainFn = fd
		}
	}
	if mainFn == nil || mainFn.Body == nil {
		t.Fatal("no func main() in cmd/invoice/main.go")
	}
	var client string
	var topLevel int
	for _, st := range mainFn.Body.List {
		a, ok := fromEnvAssign(st, "jev")
		if !ok {
			continue
		}
		topLevel++
		if id, ok := a.Lhs[0].(*ast.Ident); ok {
			client = id.Name
		}
	}
	if total != 1 || topLevel != 1 {
		t.Fatalf("found %d jev.FromEnv assignment(s), %d of them statements of main()'s body; want exactly 1 of each", total, topLevel)
	}
	if client == "" || client == "_" {
		t.Fatalf("jev.FromEnv's result is assigned to %q, want a named client variable", client)
	}

	var reaches bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSelector(call.Fun, "importer", "CheckMappingHandler") || len(call.Args) < 2 {
			return true
		}
		if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == client {
			reaches = true
		}
		return true
	})
	if !reaches {
		t.Errorf("jev.FromEnv's result (%s) is not importer.CheckMappingHandler's second argument", client)
	}
}

// TestInvoiceMain_ReadsTheJevKeyOnlyThroughFromEnv: the Jev variables are read only
// inside jev.FromEnv, never named in this file.
func TestInvoiceMain_ReadsTheJevKeyOnlyThroughFromEnv(t *testing.T) {
	src := sourceWithoutComments(t, "main.go")
	if !strings.Contains(src, `"invoice"`) {
		t.Fatal(`control: the literal "invoice" is missing from the stripped source -- string literals did not survive`)
	}
	if callSiteIndex(src, "jev.FromEnv") == -1 {
		t.Error("cmd/invoice/main.go has no jev.FromEnv( call site -- the Jev key is read nowhere")
	}
	for _, lit := range []string{`"TYPESAFE_API_KEY"`, `"JEV_FAKE"`} {
		if n := strings.Count(src, lit); n != 0 {
			t.Errorf("cmd/invoice/main.go contains the literal %s %d time(s), want 0 -- read it only inside jev.FromEnv", lit, n)
		}
	}
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

func parseMain(t *testing.T) (*ast.File, *ast.FuncDecl) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" && fd.Body != nil {
			return f, fd
		}
	}
	t.Fatal("no func main() in cmd/invoice/main.go")
	return nil, nil
}

// TestInvoiceMain_MountsTheCheckPathOnceForPostOnly: no second registration of the check
// path under any method, and CheckMappingHandler is built once.
func TestInvoiceMain_MountsTheCheckPathOnceForPostOnly(t *testing.T) {
	f, _ := parseMain(t)
	byPath := map[string][]string{}
	var handlers, total int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isSelector(call.Fun, "importer", "CheckMappingHandler") {
			handlers++
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		total++
		pattern := strings.Trim(lit.Value, `"`)
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			method, path = "", pattern
		}
		byPath[path] = append(byPath[path], method)
		return true
	})
	if total < 20 || len(byPath["/v1/imports/suggest-mapping"]) != 1 {
		t.Fatalf("control: walked %d registration(s), suggest-mapping %q -- the walk is broken", total, byPath["/v1/imports/suggest-mapping"])
	}
	if got := byPath["/v1/imports/check-mapping"]; len(got) != 1 || got[0] != "POST" {
		t.Errorf("/v1/imports/check-mapping is registered with methods %q, want exactly [POST]", got)
	}
	if handlers != 1 {
		t.Errorf("importer.CheckMappingHandler is called %d time(s), want 1", handlers)
	}
}

// TestInvoiceMain_PatternsRegisterWithoutConflict: every literal pattern main.go mounts
// registers on one ServeMux; net/http panics on a conflicting pair.
func TestInvoiceMain_PatternsRegisterWithoutConflict(t *testing.T) {
	f, _ := parseMain(t)
	var patterns []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			patterns = append(patterns, strings.Trim(lit.Value, `"`))
		}
		return true
	})
	if len(patterns) < 20 || !slices.Contains(patterns, "POST /v1/imports/check-mapping") {
		t.Fatalf("control: %d pattern(s) walked, check-mapping among them=%v", len(patterns), slices.Contains(patterns, "POST /v1/imports/check-mapping"))
	}
	mux := http.NewServeMux()
	for _, p := range patterns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("registering %q panics: %v", p, r)
				}
			}()
			mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
		}()
	}
}

// TestInvoiceMain_JevFromEnvErrorIsTheOneThatStopsTheBoot: the statement after
// `c, err := jev.FromEnv(app.Logger)` is `if err != nil { fatal(app.Logger, ..., err) }`.
// The window guards cannot see a flipped condition or a dropped logger.
func TestInvoiceMain_JevFromEnvErrorIsTheOneThatStopsTheBoot(t *testing.T) {
	_, mainFn := parseMain(t)
	body := mainFn.Body.List
	for _, pkg := range []string{"ai", "jev"} { // ai is the control: the shipped shape matches
		var matched int
		for i, st := range body {
			a, ok := st.(*ast.AssignStmt)
			if !ok || len(a.Rhs) != 1 || !isSelectorCall(a.Rhs[0], pkg, "FromEnv") {
				continue
			}
			matched++
			call := a.Rhs[0].(*ast.CallExpr)
			if len(call.Args) != 1 || !isSelector(call.Args[0], "app", "Logger") {
				t.Errorf("%s.FromEnv is not called with exactly app.Logger", pkg)
			}
			if len(a.Lhs) != 2 || a.Tok != token.DEFINE {
				t.Errorf("%s.FromEnv's result is not `client, err :=`", pkg)
				continue
			}
			errID, ok := a.Lhs[1].(*ast.Ident)
			if !ok || i+1 >= len(body) {
				t.Errorf("%s.FromEnv's error is not a named identifier followed by a statement", pkg)
				continue
			}
			ifs, ok := body[i+1].(*ast.IfStmt)
			if !ok || ifs.Init != nil || ifs.Else != nil {
				t.Errorf("the statement after %s.FromEnv is not a plain if with no else", pkg)
				continue
			}
			cond, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok {
				t.Errorf("%s.FromEnv's branch condition is not a comparison", pkg)
				continue
			}
			x, xok := cond.X.(*ast.Ident)
			y, yok := cond.Y.(*ast.Ident)
			if cond.Op != token.NEQ || !xok || x.Name != errID.Name || !yok || y.Name != "nil" {
				t.Errorf("%s.FromEnv's branch condition is not `%s != nil`", pkg, errID.Name)
				continue
			}
			if len(ifs.Body.List) != 1 {
				t.Errorf("%s.FromEnv's error branch holds %d statement(s), want the fatal call alone", pkg, len(ifs.Body.List))
				continue
			}
			es, ok := ifs.Body.List[0].(*ast.ExprStmt)
			if !ok {
				t.Errorf("%s.FromEnv's error branch is not a call", pkg)
				continue
			}
			fc, ok := es.X.(*ast.CallExpr)
			if !ok {
				t.Errorf("%s.FromEnv's error branch is not a call", pkg)
				continue
			}
			fn, ok := fc.Fun.(*ast.Ident)
			if !ok || fn.Name != "fatal" || len(fc.Args) < 3 || !isSelector(fc.Args[0], "app", "Logger") {
				t.Errorf("%s.FromEnv's error branch is not fatal(app.Logger, format, ..., %s)", pkg, errID.Name)
				continue
			}
			if id, ok := fc.Args[len(fc.Args)-1].(*ast.Ident); !ok || id.Name != errID.Name {
				t.Errorf("%s.FromEnv's fatal does not report %s", pkg, errID.Name)
			}
		}
		if matched != 1 {
			t.Errorf("found %d top-level %s.FromEnv assignment(s), want 1", matched, pkg)
		}
	}
}

// TestInvoiceMain_TheJevClientIsNeverReassigned: the variable jev.FromEnv fills is not
// overwritten or taken by address before the handler reads it.
func TestInvoiceMain_TheJevClientIsNeverReassigned(t *testing.T) {
	f, mainFn := parseMain(t)
	var client string
	for _, st := range mainFn.Body.List {
		a, ok := st.(*ast.AssignStmt)
		if ok && len(a.Rhs) == 1 && len(a.Lhs) > 0 && isSelectorCall(a.Rhs[0], "jev", "FromEnv") {
			if id, ok := a.Lhs[0].(*ast.Ident); ok {
				client = id.Name
			}
		}
	}
	if client == "" || client == "_" {
		t.Fatalf("no named top-level jev.FromEnv assignment (got %q)", client)
	}
	var assigns, addrs, uses int
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == client {
					assigns++
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				if id.Name == client {
					assigns++
				}
			}
		case *ast.UnaryExpr:
			if id, ok := n.X.(*ast.Ident); ok && n.Op == token.AND && id.Name == client {
				addrs++
			}
		case *ast.Ident:
			if n.Name == client {
				uses++
			}
		}
		return true
	})
	if uses < 2 {
		t.Fatalf("control: %s appears %d time(s), want its assignment and the handler argument at least", client, uses)
	}
	if assigns != 1 || addrs != 0 {
		t.Errorf("%s is assigned %d time(s) and has its address taken %d time(s), want 1 and 0", client, assigns, addrs)
	}
}

// TestInvoiceMain_NamesNoJevVariableAnyOtherWay: neither jev.EnvKey nor jev.EnvFake, nor a
// string literal naming either variable in any case, appears in main.go.
func TestInvoiceMain_NamesNoJevVariableAnyOtherWay(t *testing.T) {
	f, _ := parseMain(t)
	var lits int
	var sawInvoice, sawFromEnv bool
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "jev" {
				switch n.Sel.Name {
				case "FromEnv":
					sawFromEnv = true
				case "EnvKey", "EnvFake":
					t.Errorf("main.go names jev.%s -- read it only inside jev.FromEnv", n.Sel.Name)
				}
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			lits++
			v := strings.ToLower(n.Value)
			if v == `"invoice"` {
				sawInvoice = true
			}
			for _, name := range []string{"typesafe_api_key", "jev_fake"} {
				if strings.Contains(v, name) {
					t.Errorf("main.go holds the string literal %s, which names %s", n.Value, strings.ToUpper(name))
				}
			}
		}
		return true
	})
	if lits < 50 || !sawInvoice || !sawFromEnv {
		t.Fatalf("control: %d string literal(s), \"invoice\" seen=%v, jev.FromEnv seen=%v -- the walk is broken", lits, sawInvoice, sawFromEnv)
	}
}
