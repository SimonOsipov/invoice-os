// main_adversarial_test.go: APPR-08-02 Mode B. Coverage the RED spec did not
// reach — the closed accepted set, and the two wiring facts main_test.go's
// anchors cannot see (which VALUE reaches the option, and which binaries arm it).
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// parseBoolTrue/parseBoolFalse are strconv.ParseBool's entire accepted set.
var (
	parseBoolTrue  = map[string]bool{"1": true, "t": true, "T": true, "TRUE": true, "true": true, "True": true}
	parseBoolFalse = map[string]bool{"0": true, "f": true, "F": true, "FALSE": true, "false": true, "False": true}
)

// caseVariants returns every upper/lower permutation of s.
func caseVariants(s string) []string {
	out := []string{""}
	for _, r := range s {
		lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r))
		next := make([]string, 0, len(out)*2)
		for _, p := range out {
			next = append(next, p+lo, p+up)
		}
		out = next
	}
	return out
}

// TestParseEnvBool_AcceptedSetIsClosed walks all 48 case permutations of true and
// false and asserts only ParseBool's six-plus-six survive. A case-insensitive
// rewrite (strings.EqualFold) passes TestParseEnvBool_Table but dies here.
func TestParseEnvBool_AcceptedSetIsClosed(t *testing.T) {
	var accepted int
	for _, word := range []string{"true", "false"} {
		for _, v := range caseVariants(word) {
			got, err := parseEnvBool(v)
			switch {
			case parseBoolTrue[v]:
				accepted++
				if err != nil || !got {
					t.Errorf("parseEnvBool(%q) = (%v, %v), want (true, nil)", v, got, err)
				}
			case parseBoolFalse[v]:
				accepted++
				if err != nil || got {
					t.Errorf("parseEnvBool(%q) = (%v, %v), want (false, nil)", v, got, err)
				}
			default:
				if err == nil {
					t.Errorf("parseEnvBool(%q) = (%v, nil), want an error — only ParseBool's set is accepted", v, got)
				}
			}
		}
	}
	// 3 accepted spellings of true plus 3 of false among the permutations.
	if accepted != 6 {
		t.Fatalf("the case sweep exercised %d accepted spellings, want 6 — the permutation generator is broken and every reject above passed vacuously", accepted)
	}
}

// TestParseEnvBool_SingleCharAndRejectSpread covers the rest of ParseBool's
// accepted set and the operator typos that must stop the boot: shell quoting,
// a stray newline from a copied secret, and Cyrillic/fullwidth lookalikes.
func TestParseEnvBool_SingleCharAndRejectSpread(t *testing.T) {
	for raw := range parseBoolTrue {
		if got, err := parseEnvBool(raw); err != nil || !got {
			t.Errorf("parseEnvBool(%q) = (%v, %v), want (true, nil)", raw, got, err)
		}
	}
	for raw := range parseBoolFalse {
		if got, err := parseEnvBool(raw); err != nil || got {
			t.Errorf("parseEnvBool(%q) = (%v, %v), want (false, nil)", raw, got, err)
		}
	}

	rejects := []string{
		"yes", "no", "on", "off", "y", "n", "Y", "N", "enabled", "disabled",
		"2", "-1", "01", "1.0", "+1", "1 ", "0x1", "00",
		" ", "\t", "\n", "true\n", "\ntrue", "true\r", "true\r\n",
		`"true"`, "'true'", "`true`", "true;", "true,",
		"tru\u0435",  // Cyrillic small ie
		"\uff11",     // fullwidth digit one
		"\u0442rue",  // Cyrillic te
		"\u00a0true", // non-breaking space
		"true\u200b", // zero-width space
		"TRUE FALSE",
	}
	for _, raw := range rejects {
		got, err := parseEnvBool(raw)
		if err == nil {
			t.Errorf("parseEnvBool(%q) = (%v, nil), want an error — a set-but-unparseable APPROVALS_ENFORCED must stop the boot", raw, got)
		}
		if got {
			t.Errorf("parseEnvBool(%q) returned true alongside its error; the caller must never see a permissive value", raw)
		}
	}
}

// mainFuncBody returns cmd/invoice/main.go's parsed AST and main()'s statement list.
func mainFuncBody(t *testing.T) (*ast.File, []ast.Stmt) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			return f, fn.Body.List
		}
	}
	t.Fatal("no func main in cmd/invoice/main.go — this test's anchor moved")
	return nil, nil
}

// TestInvoiceMain_PassesTheParsedValueNotALiteral closes what
// TestInvoiceMain_WiresTheApprovalsEnforcedFlag's assertions (b) and (c) leave
// open. (c) checks only that the second argument IS a WithApprovalsEnforced call,
// so invoice.WithApprovalsEnforced(true) — the gate wedged permanently ON,
// ignoring the environment — passes it. (b) is a byte scan, so a comment holding
// the token fatal( satisfies it while the real error path silently continues.
// Both are asserted here on the AST, where comments do not exist.
func TestInvoiceMain_PassesTheParsedValueNotALiteral(t *testing.T) {
	_, stmts := mainFuncBody(t)

	// 1. The assignment: enforced, err := parseEnvBool(os.Getenv("APPROVALS_ENFORCED")).
	idx, flagVar, errVar := -1, "", ""
	for i, st := range stmts {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "parseEnvBool" {
			continue
		}
		idx = i
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			flagVar = id.Name
		}
		if id, ok := as.Lhs[1].(*ast.Ident); ok {
			errVar = id.Name
		}
		if len(call.Args) != 1 || !strings.Contains(exprString(call.Args[0]), "APPROVALS_ENFORCED") {
			t.Errorf("parseEnvBool is not called on os.Getenv(\"APPROVALS_ENFORCED\"); got %s", exprString(call.Args[0]))
		}
		break
	}
	if idx == -1 {
		t.Fatal("no `x, err := parseEnvBool(...)` statement at the top level of main() — this test's anchor moved")
	}
	if flagVar == "" || errVar == "" {
		t.Fatalf("parseEnvBool's results are not plain identifiers (%q, %q); the value cannot be traced to the option", flagVar, errVar)
	}

	// 2. The very next statement is the fatal guard, and it calls fatal, not log.Fatal*.
	if idx+1 >= len(stmts) {
		t.Fatal("nothing follows the parseEnvBool assignment; the error is never checked")
	}
	guard, ok := stmts[idx+1].(*ast.IfStmt)
	if !ok {
		t.Fatalf("the statement after parseEnvBool is %T, want `if %s != nil {`", stmts[idx+1], errVar)
	}
	if cond := exprString(guard.Cond); cond != errVar+" != nil" {
		t.Errorf("the guard condition is %q, want %q", cond, errVar+" != nil")
	}
	var fatalCalls, logFatalCalls int
	ast.Inspect(guard.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "fatal" {
				fatalCalls++
			}
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "log" && strings.HasPrefix(fn.Sel.Name, "Fatal") {
				logFatalCalls++
			}
		}
		return true
	})
	if fatalCalls != 1 {
		t.Errorf("the APPROVALS_ENFORCED error path makes %d call(s) to fatal(app.Logger, …), want exactly 1 — a guard that does not exit leaves the gate silently off", fatalCalls)
	}
	if logFatalCalls != 0 {
		t.Errorf("the APPROVALS_ENFORCED error path calls log.Fatal* %d time(s); platform.New's slog.SetDefault emits it at INFO, so it vanishes under LOG_LEVEL=warn", logFatalCalls)
	}

	// 3. That identifier, and no literal, is what WithApprovalsEnforced receives.
	var optArg string
	for _, st := range stmts {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || exprString(call.Fun) != "invoice.NewStore" || len(call.Args) < 2 {
			continue
		}
		opt, ok := call.Args[1].(*ast.CallExpr)
		if !ok || exprString(opt.Fun) != "invoice.WithApprovalsEnforced" || len(opt.Args) != 1 {
			continue
		}
		optArg = exprString(opt.Args[0])
		break
	}
	if optArg == "" {
		t.Fatal("no invoice.NewStore(pool, invoice.WithApprovalsEnforced(…)) assignment in main() — this test's anchor moved")
	}
	if optArg != flagVar {
		t.Errorf("invoice.WithApprovalsEnforced receives %q, want the parsed %q — a literal or a negation makes APPROVALS_ENFORCED decorative", optArg, flagVar)
	}
}

// TestOnlyTheInvoiceBinaryArmsTheFlag: cmd/submission and tools/revalidate-invoices
// own no route into queued, so the flag must stay inert in them by construction.
// A repo-wide scan rather than two hardcoded checks, so a fourth binary cannot arm
// it unnoticed either.
func TestOnlyTheInvoiceBinaryArmsTheFlag(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))

	args := map[string]int{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case ".git", ".claude", "node_modules", "frontend", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// Widest call per file, so a second armed call cannot hide behind a bare one.
			if n := len(call.Args); exprString(call.Fun) == "invoice.NewStore" {
				if cur, seen := args[rel]; !seen || n > cur {
					args[rel] = n
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// Named explicitly so a rename fails the test rather than emptying it.
	for _, unflagged := range []string{"cmd/submission/main.go", "tools/revalidate-invoices/main.go"} {
		n, ok := args[unflagged]
		if !ok {
			t.Errorf("no invoice.NewStore call found in %s — the file moved and this assertion went vacuous", unflagged)
			continue
		}
		if n != 1 {
			t.Errorf("%s calls invoice.NewStore with %d arguments, want 1: it owns no door into queued, so arming the flag there adds a value an operator must keep consistent for no behavioural gain", unflagged, n)
		}
	}
	if n, ok := args["cmd/invoice/main.go"]; !ok || n != 2 {
		t.Errorf("cmd/invoice/main.go calls invoice.NewStore with %d arguments (found=%v), want 2 — the one binary that must arm the flag", n, ok)
	}
	for path, n := range args {
		if n > 1 && path != "cmd/invoice/main.go" {
			t.Errorf("%s arms a StoreOption; only cmd/invoice/main.go owns a door into queued", path)
		}
	}
}

// exprString renders an expression for comparison and error messages.
func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.BasicLit:
		return x.Value
	case *ast.UnaryExpr:
		return x.Op.String() + exprString(x.X)
	case *ast.BinaryExpr:
		return exprString(x.X) + " " + x.Op.String() + " " + exprString(x.Y)
	case *ast.CallExpr:
		parts := make([]string, 0, len(x.Args))
		for _, a := range x.Args {
			parts = append(parts, exprString(a))
		}
		return exprString(x.Fun) + "(" + strings.Join(parts, ", ") + ")"
	default:
		return ""
	}
}
