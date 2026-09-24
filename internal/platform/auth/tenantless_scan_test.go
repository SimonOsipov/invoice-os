package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tenantlessReaders are the only non-test files outside this package allowed to
// read the tenant-less caller (D5: every other consumer must keep seeing no identity).
var tenantlessReaders = []string{
	"internal/platform/identity.go",
	"internal/tenancy/store.go",
}

func TestTenantlessCallerKeyReadOnlyByProvisioning(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var parsed int
	sites := map[string]int{} // repo-relative file -> references
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			parsed++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			// Any selector reference counts, not only a call: a function value escapes too.
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "TenantlessCallerFromContext" {
					sites[rel]++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}

	if parsed < 150 {
		t.Fatalf("parsed %d non-test .go files under internal/ and cmd/, want >= 150", parsed)
	}
	if sites["internal/tenancy/store.go"] == 0 {
		t.Errorf("control needle: no TenantlessCallerFromContext reference in internal/tenancy/store.go")
	}
	for file, n := range sites {
		if strings.HasPrefix(file, "internal/platform/auth/") || slices.Contains(tenantlessReaders, file) {
			continue
		}
		t.Errorf("%s references TenantlessCallerFromContext %d time(s); only %v may", file, n, tenantlessReaders)
	}
}
