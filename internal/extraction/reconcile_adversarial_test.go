// reconcile_adversarial_test.go: EXTR-05-03's edge and negative cases -- the withholding-gap
// non-failure, and the AST scan proving the money path never touches float64.
package extraction_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func TestReconcile_WithholdingGapIsNotAFailure(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandidate("subtotal", "1000.00"),
			rcCandidate("total", "950.00"),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
		},
	}
	results := extraction.Reconcile(in)
	for _, name := range []string{"subtotal", "total"} {
		got, ok := rcFind(results, name)
		if !ok {
			t.Fatalf("%q result not found in %+v", name, results)
		}
		if got.Reason != extraction.ReasonNone {
			t.Errorf("%s reason = %q, want ReasonNone -- total below subtotal is a withholding gap, never a failure", name, got.Reason)
		}
	}
}

// --- purity scan: no float on the money path -----------------------------------

// rcParse parses one source. src nil reads the named file; a string is a needle/control.
func rcParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// rcFloatHits reports every float64 conversion and strconv.ParseFloat call in f.
func rcFloatHits(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "float64" {
				hits = append(hits, "float64(...) conversion")
			}
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "strconv" && fn.Sel.Name == "ParseFloat" {
				hits = append(hits, "strconv.ParseFloat")
			}
		}
		return true
	})
	return hits
}

func TestReconcile_NamesNoFloatOnTheMoneyPath(t *testing.T) {
	f := rcParse(t, "reconcile.go", nil)
	if len(f.Decls) == 0 {
		t.Fatal("reconcile.go declares nothing; the scan below reported all-clear over an empty AST")
	}
	if hits := rcFloatHits(f); len(hits) != 0 {
		t.Errorf("reconcile.go carries %v; the money path must stay on shopspring/decimal", hits)
	}

	// Needle/control: a scan matching nothing on a clean file reads exactly like a scan that
	// never fires at all.
	const floatNeedle = `package p

import "strconv"

func f(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return float64(v)
}
`
	const floatControl = `package p

import "github.com/shopspring/decimal"

func f(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}
`
	needleHits := rcFloatHits(rcParse(t, "needle.go", floatNeedle))
	if len(needleHits) == 0 {
		t.Fatal("the needle source calls strconv.ParseFloat and converts to float64, and the scan reported nothing; the all-clear above proves nothing")
	}
	if controlHits := rcFloatHits(rcParse(t, "control.go", floatControl)); len(controlHits) != 0 {
		t.Errorf("the control source only calls decimal.NewFromString, an allowed idiom, and the scan flagged %v; the scan is not specific", controlHits)
	}
}
