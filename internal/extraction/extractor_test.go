// extractor_test.go: the declared surface must match the columns EXTR-05 writes.
package extraction_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// reason_code appears three times on one line of the migration; only the IN form
// is the CHECK, so anchoring on the whole group skips the column declaration.
var (
	reasonCheckRE = regexp.MustCompile(`reason_code\s+IN\s*\(([^)]*)\)`)
	sqlLiteralRE  = regexp.MustCompile(`'([^']*)'`)
)

// reasonConstants reads the declared Reason constants out of the package source.
// Go cannot enumerate constants at runtime, so a hardcoded list of four could not
// notice a fifth being added.
func reasonConstants(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}

	fset := token.NewFileSet()
	out := map[string]string{}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				// An explicit type only. A spec with values but no type declares an
				// untyped constant, which is not a Reason.
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Reason" {
					continue
				}
				for i, n := range vs.Names {
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: const %s is typed Reason but is not a string literal", name, n.Name)
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Errorf("%s: const %s: unquote %s: %v", name, n.Name, lit.Value, err)
						continue
					}
					out[n.Name] = v
				}
			}
		}
	}
	return out
}

func TestReasonConstantsMatchMigrationCheck(t *testing.T) {
	const glob = "*_extraction_field_results.sql"

	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", glob))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d migrations named %s, want exactly 1: %v", len(matches), glob, matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}

	groups := reasonCheckRE.FindAllStringSubmatch(string(body), -1)
	if len(groups) != 1 {
		t.Fatalf("found %d reason_code IN (...) groups in %s, want exactly 1", len(groups), matches[0])
	}
	var want []string
	for _, lit := range sqlLiteralRE.FindAllStringSubmatch(groups[0][1], -1) {
		want = append(want, lit[1])
	}
	if len(want) == 0 {
		t.Fatalf("parsed 0 reason literals out of %q; the comparison below would pass vacuously", groups[0][1])
	}

	consts := reasonConstants(t)
	if len(consts) == 0 {
		t.Fatalf("found no constants declared with type Reason in internal/extraction; the comparison below would pass vacuously")
	}

	var got, empties []string
	for name, value := range consts {
		if value == "" {
			empties = append(empties, name)
			continue
		}
		got = append(got, value)
	}
	sort.Strings(got)
	sort.Strings(want)
	sort.Strings(empties)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Reason constants carry %v, the migration CHECK allows %v", got, want)
	}
	if len(empties) != 1 || empties[0] != "ReasonNone" {
		t.Errorf("empty-valued Reason constants are %v, want exactly [ReasonNone]", empties)
	}
}

func TestReasonNoneIsTheEmptyString(t *testing.T) {
	if extraction.ReasonNone != "" {
		t.Errorf("ReasonNone = %q, want the empty string: no reason means a NULL reason_code", extraction.ReasonNone)
	}
}

// The columns are page int and bbox_x0..bbox_y1 double precision, so a float32
// field would silently lose precision at the EXTR-05 write.
func TestRegionFieldsAreFloat64AndPageIsInt(t *testing.T) {
	rt := reflect.TypeOf(extraction.Region{})

	// First, so a renamed field fails here rather than being skipped below.
	if got := rt.NumField(); got != 5 {
		t.Fatalf("Region has %d fields, want 5: Page, X0, Y0, X1, Y1", got)
	}

	want := map[string]reflect.Kind{
		"Page": reflect.Int,
		"X0":   reflect.Float64,
		"Y0":   reflect.Float64,
		"X1":   reflect.Float64,
		"Y1":   reflect.Float64,
	}
	for _, name := range []string{"Page", "X0", "Y0", "X1", "Y1"} {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("Region has no field %s", name)
			continue
		}
		if f.Type.Kind() != want[name] {
			t.Errorf("Region.%s is %s, want %s", name, f.Type.Kind(), want[name])
		}
	}
}
