// extractor_edge_test.go: the surface the spec tests leave unguarded -- Region was the only
// type with a reflection oracle.
package extraction_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// outOfTreeExtractor stands where EXTR-02 and EXTR-03 will: a type in another package.
// An unexported marker method on Extractor would stop this line compiling, which is the
// point -- the port is deliberately not sealed.
type outOfTreeExtractor struct{}

func (outOfTreeExtractor) Name() string    { return "out-of-tree" }
func (outOfTreeExtractor) Version() string { return "v0" }

func (outOfTreeExtractor) Extract(context.Context, extraction.Document) ([]extraction.Field, error) {
	return []extraction.Field{}, nil
}

var _ extraction.Extractor = outOfTreeExtractor{}

func TestExtractorPortHasExactlyNameVersionExtract(t *testing.T) {
	var (
		ctxType    = reflect.TypeOf((*context.Context)(nil)).Elem()
		errType    = reflect.TypeOf((*error)(nil)).Elem()
		strType    = reflect.TypeOf("")
		docType    = reflect.TypeOf(extraction.Document{})
		fieldsType = reflect.TypeOf([]extraction.Field(nil))
	)

	// FuncOf builds the exact signature; reflect drops the receiver from an interface method.
	want := map[string]reflect.Type{
		"Extract": reflect.FuncOf([]reflect.Type{ctxType, docType}, []reflect.Type{fieldsType, errType}, false),
		"Name":    reflect.FuncOf(nil, []reflect.Type{strType}, false),
		"Version": reflect.FuncOf(nil, []reflect.Type{strType}, false),
	}

	it := reflect.TypeOf((*extraction.Extractor)(nil)).Elem()
	if it.Kind() != reflect.Interface {
		t.Fatalf("Extractor is %s, want an interface", it.Kind())
	}

	got := map[string]reflect.Type{}
	for i := range it.NumMethod() {
		m := it.Method(i)
		got[m.Name] = m.Type
	}
	if len(got) != len(want) {
		t.Fatalf("Extractor declares %d methods %v, want exactly 3: Extract, Name, Version",
			len(got), sortedKeys(got))
	}

	for name, wantSig := range want {
		gotSig, ok := got[name]
		if !ok {
			t.Errorf("Extractor has no method %s; it declares %v", name, sortedKeys(got))
			continue
		}
		if gotSig != wantSig {
			t.Errorf("Extractor.%s is %s, want %s", name, gotSig, wantSig)
		}
	}
}

// Value and Region are pointers because the columns they feed are nullable: value is NULL
// when nothing was found, and the whole region is NULL when nothing can be pointed at.
func TestFieldMatchesTheColumnsItIsWrittenTo(t *testing.T) {
	rt := reflect.TypeOf(extraction.Field{})

	// First, so a renamed field fails here rather than being skipped below.
	if got := rt.NumField(); got != 4 {
		t.Fatalf("Field has %d fields, want 4: Name, Value, Region, Reason", got)
	}

	want := map[string]reflect.Type{
		"Name":   reflect.TypeOf(""),
		"Value":  reflect.TypeOf((*string)(nil)),
		"Region": reflect.TypeOf((*extraction.Region)(nil)),
		"Reason": reflect.TypeOf(extraction.ReasonNone),
	}
	assertStructFields(t, rt, want, []string{"Name", "Value", "Region", "Reason"})
}

// Bytes, not an io.Reader: the port takes bytes the caller already opened and owns, which
// is what lets a contract law say an extractor must not retain them past the call.
func TestDocumentCarriesOwnedBytesAndADeclaredContentType(t *testing.T) {
	rt := reflect.TypeOf(extraction.Document{})

	if got := rt.NumField(); got != 2 {
		t.Fatalf("Document has %d fields, want 2: Bytes, ContentType", got)
	}

	want := map[string]reflect.Type{
		"Bytes":       reflect.TypeOf([]byte(nil)),
		"ContentType": reflect.TypeOf(""),
	}
	assertStructFields(t, rt, want, []string{"Bytes", "ContentType"})
}

func TestOpenDocumentSeamTakesADocumentIDAndReturnsADocument(t *testing.T) {
	want := reflect.FuncOf(
		[]reflect.Type{reflect.TypeOf((*context.Context)(nil)).Elem(), reflect.TypeOf("")},
		[]reflect.Type{reflect.TypeOf(extraction.Document{}), reflect.TypeOf((*error)(nil)).Elem()},
		false,
	)

	// A named func type is never == its unnamed underlying, so compare the parts.
	got := reflect.TypeOf((*extraction.OpenDocument)(nil)).Elem()
	if got.Kind() != reflect.Func {
		t.Fatalf("OpenDocument is %s, want a func type", got.Kind())
	}
	if !sameSignature(got, want) {
		t.Errorf("OpenDocument is %s, want %s", signatureString(got), signatureString(want))
	}
}

func sameSignature(got, want reflect.Type) bool {
	if got.IsVariadic() != want.IsVariadic() || got.NumIn() != want.NumIn() || got.NumOut() != want.NumOut() {
		return false
	}
	for i := range got.NumIn() {
		if got.In(i) != want.In(i) {
			return false
		}
	}
	for i := range got.NumOut() {
		if got.Out(i) != want.Out(i) {
			return false
		}
	}
	return true
}

func signatureString(ft reflect.Type) string {
	in := make([]string, ft.NumIn())
	for i := range in {
		in[i] = ft.In(i).String()
	}
	out := make([]string, ft.NumOut())
	for i := range out {
		out[i] = ft.Out(i).String()
	}
	return fmt.Sprintf("func(%s) (%s)", strings.Join(in, ", "), strings.Join(out, ", "))
}

// EXTR-07 owns the wire and builds its own DTO, so a tag here would be a speculative
// contract nothing reads.
func TestValueTypesCarryNoStructTags(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(extraction.Document{}),
		reflect.TypeOf(extraction.Field{}),
		reflect.TypeOf(extraction.Region{}),
	} {
		if rt.Kind() != reflect.Struct {
			t.Errorf("%s is %s, want a struct", rt.Name(), rt.Kind())
			continue
		}
		n := rt.NumField()
		if n == 0 {
			t.Errorf("%s has no fields; the tag scan below would pass vacuously", rt.Name())
			continue
		}
		for i := range n {
			f := rt.Field(i)
			if tag := string(f.Tag); tag != "" {
				t.Errorf("%s.%s carries the struct tag %q; EXTR-07 owns the wire", rt.Name(), f.Name, tag)
			}
		}
	}
}

// A named type, not an alias: an alias would let any string be a reason_code with no
// conversion, which is the whole thing the enum exists to stop.
func TestReasonIsANamedStringTypeInThisPackage(t *testing.T) {
	rt := reflect.TypeOf(extraction.ReasonNone)

	if rt.Kind() != reflect.String {
		t.Errorf("Reason has kind %s, want string: reason_code is a text column", rt.Kind())
	}
	if rt.Name() != "Reason" || rt.PkgPath() != extractionPkg {
		t.Errorf("ReasonNone has type %q from %q, want Reason from %q -- an alias is not a type",
			rt.Name(), rt.PkgPath(), extractionPkg)
	}
}

// reasonConstants only records a spec that states BOTH an explicit Reason type and its own
// value, so three shapes carry a reason past it: an implicitly repeated const, a const typed
// through an alias of Reason, and a package-level var. Scan for those directly.
func TestNoReasonValueEscapesTheConstantScan(t *testing.T) {
	known := reasonConstants(t)
	if len(known) != 5 {
		t.Fatalf("reasonConstants found %d constants, want 5: the four CHECK values plus ReasonNone", len(known))
	}
	allowed := map[string]bool{}
	for _, v := range known {
		allowed[v] = true
	}

	files, fset := parsePackageFiles(t)
	names := reasonTypeNames(files)
	if !names["Reason"] {
		t.Fatalf("Reason is not declared in this package; the scan below would pass vacuously")
	}

	var typedSpecs int
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}

			// Does this block declare a reason at all? Only then does the repetition rule bite,
			// so an unrelated iota block elsewhere in the package stays legal.
			var isReasonBlock bool
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && reasonTyped(vs, names) {
					isReasonBlock = true
				}
			}

			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				pos := fset.Position(vs.Pos())

				if isReasonBlock && gd.Tok == token.CONST && len(vs.Values) != len(vs.Names) {
					t.Errorf("%s: %v repeats the previous spec instead of stating its own value; "+
						"reasonConstants cannot see it, so it is a reason nothing checks",
						pos, specNames(vs))
					continue
				}
				if !reasonTyped(vs, names) {
					continue
				}
				if gd.Tok == token.VAR {
					t.Errorf("%s: %v is a package-level var typed as a reason; a reason is a constant",
						pos, specNames(vs))
					continue
				}
				typedSpecs++
				for i, n := range vs.Names {
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: const %s is typed as a reason but is not a string literal", pos, n.Name)
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Errorf("%s: const %s: unquote %s: %v", pos, n.Name, lit.Value, err)
						continue
					}
					if !allowed[v] {
						t.Errorf("%s: const %s carries the reason %q, which is not one the migration CHECK allows: %v",
							pos, n.Name, v, sortedValues(known))
					}
				}
			}
		}
	}
	if typedSpecs < 5 {
		t.Errorf("the scan visited %d reason-typed specs, want at least 5; it is missing declarations", typedSpecs)
	}
}

// The Go constants are pinned to one migration file. A later ALTER that widened the CHECK
// somewhere else would leave that pin passing against a set the database no longer enforces.
func TestNoOtherMigrationRedefinesTheReasonCheck(t *testing.T) {
	all, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("globbed %d migrations; the scan below would pass vacuously", len(all))
	}

	var carriers []string
	for _, p := range all {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if reasonCheckRE.Match(body) {
			carriers = append(carriers, filepath.Base(p))
		}
	}
	if len(carriers) != 1 || !strings.HasSuffix(carriers[0], "_extraction_field_results.sql") {
		t.Errorf("the reason_code CHECK set is written in %v, want only the extraction_field_results "+
			"migration -- TestReasonConstantsMatchMigrationCheck reads that file alone", carriers)
	}
}

// fenceRecorder collects what assertFenced reports, so the fence can be shown to fire.
type fenceRecorder struct{ msgs []string }

func (*fenceRecorder) Helper() {}

func (r *fenceRecorder) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

// Both live scans classify zero repo packages -- that is what a passing absence scan means --
// so nothing else proves assertFenced would report anything at all.
func TestImportFenceReportsARepoDependency(t *testing.T) {
	var rec fenceRecorder
	assertFenced(&rec, "probe", []string{
		"",
		"context",
		extractionPkg,
		extractionPkg + "_test [" + extractionPkg + ".test]",
		extractionPkg + ".test",
		platformPfx + "db",
		documentPkg,
		documentPkg + " [" + extractionPkg + ".test]",
		modulePath + "/internal/importer",
		modulePath,
	})

	if len(rec.msgs) != 4 {
		t.Fatalf("the fence reported %d of 4 offending lines: %v", len(rec.msgs), rec.msgs)
	}
	if n := countContaining(rec.msgs, "OpenDocument"); n != 2 {
		t.Errorf("%d reports named the OpenDocument seam, want 2 (bare and test-annotated %s)", n, documentPkg)
	}
	if n := countContaining(rec.msgs, "depends on "+modulePath+"/internal/importer --"); n != 1 {
		t.Errorf("%d reports named internal/importer, want 1", n)
	}
	if n := countContaining(rec.msgs, "depends on "+modulePath+" --"); n != 1 {
		t.Errorf("%d reports named the module root package, want 1", n)
	}
}

// go list -deps omits test imports, so scan B is the only reason a fixture test cannot drag
// internal/document back in. If -test ever stopped augmenting, scan B would silently collapse
// onto scan A and clear its own line floor.
func TestImportFenceScanBSeesTheTestBinary(t *testing.T) {
	scanA := goListDeps(t)
	scanB := goListDeps(t, "-test")

	if len(scanB) <= len(scanA) {
		t.Fatalf("scan B lists %d deps and scan A lists %d; scan B must add the test-only imports",
			len(scanB), len(scanA))
	}

	want := extractionPkg + ".test"
	for _, raw := range scanB {
		if dep, _, _ := strings.Cut(strings.TrimSpace(raw), " "); dep == want {
			return
		}
	}
	t.Errorf("scan B does not list %s; the test binary was never built, so the scan sees no test imports", want)
}

func assertStructFields(t *testing.T, rt reflect.Type, want map[string]reflect.Type, order []string) {
	t.Helper()

	for _, name := range order {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("%s has no field %s", rt.Name(), name)
			continue
		}
		if f.Type != want[name] {
			t.Errorf("%s.%s is %s, want %s", rt.Name(), name, f.Type, want[name])
		}
	}
}

// parsePackageFiles parses the package source, skipping tests -- the same set reasonConstants
// walks.
func parsePackageFiles(t *testing.T) ([]*ast.File, *token.FileSet) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}

	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("parsed 0 non-test files in internal/extraction; the scans below would pass vacuously")
	}
	return files, fset
}

// reasonTypeNames returns every type name that IS Reason: Reason itself plus any alias or
// defined type over it, followed to a fixpoint so an alias of an alias still counts.
func reasonTypeNames(files []*ast.File) map[string]bool {
	names := map[string]bool{"Reason": true}
	for grew := true; grew; {
		grew = false
		for _, f := range files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					id, ok := ts.Type.(*ast.Ident)
					if !ok || !names[id.Name] || names[ts.Name.Name] {
						continue
					}
					names[ts.Name.Name] = true
					grew = true
				}
			}
		}
	}
	return names
}

func reasonTyped(vs *ast.ValueSpec, names map[string]bool) bool {
	id, ok := vs.Type.(*ast.Ident)
	return ok && names[id.Name]
}

func specNames(vs *ast.ValueSpec) []string {
	var out []string
	for _, n := range vs.Names {
		out = append(out, n.Name)
	}
	return out
}

func countContaining(msgs []string, sub string) int {
	var n int
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func sortedKeys(m map[string]reflect.Type) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
