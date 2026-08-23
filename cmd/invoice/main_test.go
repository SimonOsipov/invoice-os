// main_test.go: APPR-08-02, parseEnvBool and the APPROVALS_ENFORCED wire-up.
// cmd/invoice/ had no test file before this one; the wiring scan mirrors
// cmd/submission/main_test.go's idiom (read the sibling source, anchor, assert in a
// window) and ci_gate_test.go's AST walk for the argument shape, which reformatting
// cannot break.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestParseEnvBool_Table: strconv.ParseBool's accepted set and nothing else,
// plus the two divergences that matter — "" boots as false, and whitespace is
// NOT trimmed, so a padded value stops the boot loudly instead of leaving the
// gate silently off. Pure and value-based, so no t.Setenv (gateway's
// TestMockIssuerEnabled shape).
func TestParseEnvBool_Table(t *testing.T) {
	cases := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{"1", true, false},
		{"t", true, false},
		{"T", true, false},
		{"TRUE", true, false},
		{"true", true, false},
		{"True", true, false},
		{"0", false, false},
		{"f", false, false},
		{"F", false, false},
		{"FALSE", false, false},
		{"false", false, false},
		{"False", false, false},

		{"", false, false}, // unset must boot — see TestParseEnvBool_UnsetIsNeverAnError

		{" true", false, true}, // whitespace is not trimmed
		{"true ", false, true},
		{"\ttrue", false, true},
		{"TrUe", false, true}, // only True/TRUE/true
		{"yes", false, true},
		{"on", false, true},
		{"y", false, true},
		{"2", false, true},
		{"-1", false, true},
		{"ture", false, true},
	}

	for _, c := range cases {
		got, err := parseEnvBool(c.raw)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("parseEnvBool(%q) = (%v, nil), want an error — a set-but-unparseable APPROVALS_ENFORCED must stop the boot, never fall back to off", c.raw, got)
		case c.wantErr && got:
			t.Errorf("parseEnvBool(%q) = (true, %v), want false alongside its error", c.raw, err)
		case !c.wantErr && err != nil:
			t.Errorf("parseEnvBool(%q) = (%v, %v), want (%v, nil)", c.raw, got, err, c.want)
		case !c.wantErr && got != c.want:
			t.Errorf("parseEnvBool(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// TestParseEnvBool_UnsetIsNeverAnError: the empty string is handled BEFORE
// strconv.ParseBool, which rejects it. An absent flag boots, unlike mustEnv.
func TestParseEnvBool_UnsetIsNeverAnError(t *testing.T) {
	got, err := parseEnvBool("")
	if err != nil {
		t.Fatalf(`parseEnvBool("") returned error %v — an unset APPROVALS_ENFORCED must boot as off`, err)
	}
	if got {
		t.Errorf(`parseEnvBool("") = true, want false`)
	}
}

// TestParseEnvBool_ReturnsTheErrorRatherThanExiting: the helper hands the error
// back to main, which owns the fatal. A helper that exited would take this test
// binary down with it, so reaching the assertion at all is half the proof.
func TestParseEnvBool_ReturnsTheErrorRatherThanExiting(t *testing.T) {
	if _, err := parseEnvBool("yes"); err == nil {
		t.Error(`parseEnvBool("yes") returned a nil error — the caller cannot fatal on a failure it is never told about`)
	}
}

// TestInvoiceMain_WiresTheApprovalsEnforcedFlag: AC #4 plus BLOCKER-4's
// which-fatal ruling, as one guard rather than three tests that must agree
// forever. Missing anchors are reported by name, never skipped, so a rename
// cannot make this vacuous.
func TestInvoiceMain_WiresTheApprovalsEnforcedFlag(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/invoice/main.go: %v", err)
	}
	src := string(b)

	// (a) Read exactly once. The QUOTED literal only: the surrounding comment and
	// the fatal's message name the variable too, and neither is a read.
	if n := strings.Count(src, `"APPROVALS_ENFORCED"`); n != 1 {
		t.Errorf(`cmd/invoice/main.go contains the literal "APPROVALS_ENFORCED" %d time(s), want exactly 1 (AC #4) — two readers are two places an operator must keep consistent`, n)
	}

	// (b) The error path fatals through fatal(app.Logger, …). log.Fatalf is
	// emitted as {"level":"INFO"} after platform.New calls slog.SetDefault, and
	// vanishes entirely under LOG_LEVEL=warn — see fatal's own doc comment.
	if idx := callSiteIndex(src, "parseEnvBool"); idx == -1 {
		t.Error(`cmd/invoice/main.go has no parseEnvBool( CALL site (only, at most, its declaration) — APPROVALS_ENFORCED is read nowhere, or this test's anchor moved`)
	} else {
		end := idx + 400
		if end > len(src) {
			end = len(src)
		}
		window := src[idx:end]
		if !strings.Contains(window, "fatal(") {
			t.Errorf("no fatal( within 400 bytes after the parseEnvBool( call site — an unparseable APPROVALS_ENFORCED must stop the boot:\n%s", window)
		}
		if strings.Contains(window, "log.Fatal") {
			t.Errorf("found log.Fatal within 400 bytes after the parseEnvBool( call site — use fatal(app.Logger, …), which logs at ERROR; log.Fatalf here would be silent under LOG_LEVEL=warn:\n%s", window)
		}
	}

	// (c) The parsed value reaches the Store, as the option and not by some other
	// route. AST rather than a byte scan, so a renamed pool variable or a gofmt
	// pass cannot break it (ci_gate_test.go's TestMain_WiresTheStoreCollaborators).
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
		if !ok || sel.Sel.Name != "NewStore" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "invoice" {
			return true
		}
		found = true

		if len(call.Args) != 2 {
			t.Errorf("invoice.NewStore in cmd/invoice/main.go has %d argument(s), want 2 (pool, invoice.WithApprovalsEnforced(…))", len(call.Args))
			return false
		}
		opt, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("invoice.NewStore's second argument is %T, want an invoice.WithApprovalsEnforced(…) call", call.Args[1])
			return false
		}
		optSel, ok := opt.Fun.(*ast.SelectorExpr)
		if !ok || optSel.Sel.Name != "WithApprovalsEnforced" {
			t.Errorf("invoice.NewStore's second argument is not invoice.WithApprovalsEnforced(…)")
			return false
		}
		if pkg, ok := optSel.X.(*ast.Ident); !ok || pkg.Name != "invoice" {
			t.Errorf("invoice.NewStore's second argument is not qualified invoice.WithApprovalsEnforced(…)")
		}
		return false
	})
	if !found {
		t.Error("no invoice.NewStore( call found in cmd/invoice/main.go — this test's anchor moved")
	}
}

// TestInvoiceMain_RegistersTheEvidenceBundleRoute (AUDIT-05-08, AC-3): GET
// /v1/evidence-bundle must be mounted beside GET /v1/audit-log and dispatch to
// archive.DownloadHandler(...). AST, not a byte scan, so gofmt cannot break the anchor
// (part (c) of TestInvoiceMain_WiresTheApprovalsEnforcedFlag's idiom). The
// GET /v1/audit-log needle is a control: it proves the walk finds a real, already-
// shipped registration before trusting a negative result for the new one.
func TestInvoiceMain_RegistersTheEvidenceBundleRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/invoice/main.go: %v", err)
	}

	var foundAuditLog, foundBundle bool
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
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf("GET /v1/evidence-bundle's second argument is %T, want a call expression", call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "DownloadHandler" {
				t.Error("GET /v1/evidence-bundle's handler call is not ....DownloadHandler(...)")
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "archive" {
				t.Error("GET /v1/evidence-bundle's handler is not archive.DownloadHandler(...)")
			}
		}
		return true
	})

	if !foundAuditLog {
		t.Fatal("control needle: no GET /v1/audit-log registration found -- the AST walk itself is broken, so the assertion below is vacuous")
	}
	if !foundBundle {
		t.Error(`no app.Mux.HandleFunc("GET /v1/evidence-bundle", archive.DownloadHandler(...)) registration found in cmd/invoice/main.go`)
	}
}

// callSiteIndex returns the first index of name+"(" that is not its own
// declaration, so an anchor cannot silently resolve to `func name(`.
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
// closure -- the flag folds inside that method, so bypassing it hands every caller
// an unconditional can_submit. AST, so gofmt or a renamed store variable cannot
// break the anchor (part (c) above's idiom).
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
