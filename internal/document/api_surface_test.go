// api_surface_test.go: the SDK must not surface above s3.go.
package document_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func isSDKImport(path string) bool {
	return strings.HasPrefix(path, "github.com/aws/")
}

// TestDocument_ExportedAPINamesNoSDKType keeps *s3.Client (and every other SDK
// type) out of this package's exported surface, so consumers depend on
// ObjectStore rather than on aws-sdk-go-v2.
func TestDocument_ExportedAPINamesNoSDKType(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/document: %v", err)
	}

	fset := token.NewFileSet()
	var filesWithSDKImport, exportedSeen []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Local package names for the SDK imports in THIS file — an alias
		// (awshttp) must be tracked under the alias, not the base name.
		sdkNames := map[string]bool{}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !isSDKImport(path) {
				continue
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			sdkNames[local] = true
		}
		if len(sdkNames) > 0 {
			filesWithSDKImport = append(filesWithSDKImport, name)
		}

		report := func(what string, node ast.Node) {
			ast.Inspect(node, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && sdkNames[id.Name] {
					t.Errorf("%s: exported %s names the SDK type %s.%s — the SDK must not surface above s3.go; "+
						"consumers depend on ObjectStore", name, what, id.Name, sel.Sel.Name)
				}
				return true
			})
		}

		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() || decl.Recv != nil {
					continue
				}
				exportedSeen = append(exportedSeen, decl.Name.Name)
				report("func "+decl.Name.Name+"'s signature", decl.Type)
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						exportedSeen = append(exportedSeen, s.Name.Name)
						report("type "+s.Name.Name, s.Type)
					case *ast.ValueSpec:
						for _, id := range s.Names {
							if !id.IsExported() {
								continue
							}
							exportedSeen = append(exportedSeen, id.Name)
							if s.Type != nil {
								report("var/const "+id.Name, s.Type)
							}
							for _, v := range s.Values {
								report("var/const "+id.Name+"'s initializer", v)
							}
						}
					}
				}
			}
		}
	}

	if len(filesWithSDKImport) == 0 {
		t.Fatalf("no file in internal/document imports the AWS SDK — the scan tracks no package names, so every " +
			"assertion above passed vacuously")
	}
	if len(exportedSeen) == 0 {
		t.Fatalf("the scan inspected no exported declaration in internal/document — the walk is broken")
	}
}

// TestDocument_ImportsNoRepoPackage keeps the internal/importer ->
// internal/document edge one-directional (precedent:
// internal/submission/deps_test.go): internal/platform/* and internal/audit are
// allowed, every other repo package — internal/importer above all — is not.
func TestDocument_ImportsNoRepoPackage(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}

	const module = "github.com/SimonOsipov/invoice-os"
	cmd := exec.Command("go", "list", "-deps", "./internal/document")
	cmd.Dir = strings.TrimSpace(string(root))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/document: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("go list -deps returned %d lines — the command did not resolve the package, so the check below "+
			"is vacuous:\n%s", len(lines), out)
	}
	// The store methods need db.WithinRequestTenantTx, auth.IdentityFromContext and
	// audit.Record; none of those three reaches internal/importer or back here.
	allowed := func(dep string) bool {
		return dep == module+"/internal/document" ||
			dep == module+"/internal/audit" ||
			strings.HasPrefix(dep, module+"/internal/platform/")
	}
	for _, line := range lines {
		dep := strings.TrimSpace(line)
		if !strings.HasPrefix(dep, module) || allowed(dep) {
			continue
		}
		t.Errorf("internal/document imports %s — it may depend only on internal/platform/* and internal/audit "+
			"so internal/importer can depend on it without a cycle", dep)
	}
}
