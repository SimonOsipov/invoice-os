package invoice

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTransitionTx_DocCommentNamesEveryCaller (APPR-07-08): transitionTx's
// doc comment (store.go) lists its callers by file -- the exact kind of
// citation this story is fixing everywhere else. This scans every .go file
// in the package for transitionTx( call sites -- excluding the definition
// and comment-only mentions, both handled for free by parsing real Go
// syntax rather than grepping text, since comments never parse into
// *ast.CallExpr -- and asserts each calling file's basename appears
// somewhere in the doc comment.
//
// LIMITATION: the assertion is per-FILE, not per-call-site. Four of the
// seven real callers live in store.go, so a future edit dropping three of
// them while leaving any one store.go mention in the doc comment would
// still pass. Deliberate: asserting the exact cited line numbers would go
// red on every unrelated edit that shifts a line in internal/invoice --
// precisely the churn that produced the dead citations this story fixes.
func TestTransitionTx_DocCommentNamesEveryCaller(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	fset := token.NewFileSet()
	var doc string
	callerFiles := map[string]bool{}
	callSites := 0

	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		base := filepath.Base(path)

		ast.Inspect(f, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "transitionTx" {
				if fn.Doc != nil {
					doc = fn.Doc.Text()
				}
				return false // the definition, not a call site
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "transitionTx" {
				return true
			}
			callSites++
			callerFiles[base] = true
			return true
		})
	}

	// A broken scan (wrong dir, no files matched) must not pass silently.
	if callSites < 2 {
		t.Fatalf("scan found only %d transitionTx( call site(s) across %d file(s) -- the scan is broken, "+
			"so every assertion below would pass vacuously", callSites, len(files))
	}
	if doc == "" {
		t.Fatal("scan found no doc comment on func transitionTx -- the definition scan is broken")
	}

	for file := range callerFiles {
		if !strings.Contains(doc, file) {
			t.Errorf("transitionTx's doc comment does not name %s, but %s calls transitionTx(...)", file, file)
		}
	}
}
