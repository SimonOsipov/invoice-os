// reachability_test.go: the two guards that fence the mock out of any live path. The compiler
// already refuses to let another package name the unexported args type; these close the routes
// it cannot -- a live file naming the type after a rename, and this package growing a SECOND
// enqueue surface beside the one EXTR-09 sanctioned.
//
// Both are green once the seam is implemented. Their value is the control needle and the
// floor: a scan that reached nothing reports all-clear, which reads exactly like a clean repo.
//
// Mode 0 parsing: comments are never attached to the AST, so a banned name inside a comment
// cannot fail either scan. This file carries no skip call and no database-DSN variable name:
// internal/tools/rlsgate/ci_registration_test.go classifies a package DB-gated when both appear
// in one file raw bytes.
package extraction_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// rxRepoRoot locates the worktree root. go test runs with cwd set to this package directory.
func rxRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; the walk below would read nothing")
	}
	return root
}

// TestNoNonTestCodeNamesTheExtractionArgsType: AC #5, source half. Ident-only, because
// ast.Walk visits SelectorExpr.Sel as an *ast.Ident -- so this one match catches a struct
// field type, a var, a slice, a map value, a pointer, a composite literal, a type assertion, a
// func parameter and a type alias alike. ast.File.Unresolved is the wrong instrument here: it
// holds only bare identifiers unresolved in file scope, so every qualified reference
// contributes the package identifier and never the selector.
func TestNoNonTestCodeNamesTheExtractionArgsType(t *testing.T) {
	root := rxRepoRoot(t)
	banned := map[string]bool{"extractArgs": true, "ExtractArgs": true}
	needles := map[string]bool{"SubmitArgs": true, "EnqueueTx": true}

	const controlFile = "internal/invoice/batch_submit.go"

	offenders := map[string][]string{}
	needleHits := map[string]map[string]bool{}
	var parsed []string

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/extraction/") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		parsed = append(parsed, rel)
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if banned[id.Name] {
				offenders[rel] = append(offenders[rel], id.Name)
			}
			if needles[id.Name] {
				if needleHits[rel] == nil {
					needleHits[rel] = map[string]bool{}
				}
				needleHits[rel][id.Name] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	cmdFiles := 0
	inPopulation := false
	for _, rel := range parsed {
		if strings.HasPrefix(rel, "cmd/") {
			cmdFiles++
		}
		if rel == controlFile {
			inPopulation = true
		}
	}
	if len(parsed) < 130 || cmdFiles < 1 {
		t.Fatalf("the walk parsed %d non-test .go file(s), %d of them under cmd/, want at least 130 and at least 1 -- a clean report over a broken walk means nothing", len(parsed), cmdFiles)
	}
	if !inPopulation {
		t.Fatalf("%s is not in the walked population; the control needle below cannot prove anything", controlFile)
	}
	// The control shares this walk and this ast.Inspect closure. Parsing it separately would
	// prove the matcher compiles, not that the walk reached anything.
	for name := range needles {
		if !needleHits[controlFile][name] {
			t.Fatalf("the walk did not find the identifier %s in %s; it can no longer find a planted hit, so the absence reported below means nothing", name, controlFile)
		}
	}

	if len(offenders) > 0 {
		files := make([]string, 0, len(offenders))
		for rel := range offenders {
			files = append(files, rel)
		}
		sort.Strings(files)
		for _, rel := range files {
			t.Errorf("%s names %v: the extraction args type is unexported so no live path can construct one, and a package that names it has either re-exported it or grown a second definition", rel, offenders[rel])
		}
	}
}

// rxExtractionFiles parses this package own non-test files. Returns them keyed by file name so
// a failure can say where.
func rxExtractionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, _ := mxParse(t, name)
		out[name] = f
	}
	if len(out) < 4 {
		t.Fatalf("scanned %d non-test file(s) in internal/extraction, want at least 4 (extractor.go, mock.go, store.go, worker.go) -- every absence below would be vacuous", len(out))
	}
	return out
}

// rxArgsTypeName reads the args type off ExtractWorker embedded river.WorkerDefaults[...], so
// a rename cannot leave this guard scanning for a name nothing declares any more.
func rxArgsTypeName(t *testing.T, files map[string]*ast.File) string {
	t.Helper()
	var found string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ExtractWorker" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return false
			}
			for _, fld := range st.Fields.List {
				if len(fld.Names) != 0 {
					continue
				}
				ix, ok := fld.Type.(*ast.IndexExpr)
				if !ok {
					continue
				}
				sel, ok := ix.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "WorkerDefaults" {
					continue
				}
				if id, ok := ix.Index.(*ast.Ident); ok {
					found = id.Name
				}
			}
			return false
		})
	}
	if found == "" {
		t.Fatal("no river.WorkerDefaults[...] embedded in ExtractWorker; the args type name is unknown and the result-list scan below would be vacuous")
	}
	return found
}

// rxTypeNames renders every type name a field list mentions, unwrapping pointer, slice, array,
// map-value, variadic, channel and NESTED FUNC SIGNATURE so a helper cannot hide a type one
// level down. A bare type yields "Name"; a qualified one yields "pkg.Name".
func rxTypeNames(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var out []string
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.Ident:
			out = append(out, x.Name)
		case *ast.SelectorExpr:
			if pkg, ok := x.X.(*ast.Ident); ok {
				out = append(out, pkg.Name+"."+x.Sel.Name)
			}
		case *ast.StarExpr:
			walk(x.X)
		case *ast.ArrayType:
			walk(x.Elt)
		case *ast.MapType:
			walk(x.Value)
		case *ast.IndexExpr:
			walk(x.X)
		case *ast.Ellipsis:
			walk(x.Elt)
		case *ast.ChanType:
			walk(x.Value)
		case *ast.FuncType:
			// An injected inserter hides river.JobArgs inside a parameter's own signature.
			for _, sub := range []*ast.FieldList{x.Params, x.Results} {
				if sub == nil {
					continue
				}
				for _, fld := range sub.List {
					walk(fld.Type)
				}
			}
		}
	}
	for _, fld := range fields.List {
		walk(fld.Type)
	}
	return out
}

// rxExportedFuncs returns the exported funcs and methods whose result list -- or, when params
// is true, whose parameter list -- mentions want. Used for both the bans and their controls.
func rxExportedFuncs(files map[string]*ast.File, want string, params bool) []string {
	var out []string
	for name, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !fd.Name.IsExported() {
				continue
			}
			list := fd.Type.Results
			if params {
				list = fd.Type.Params
			}
			for _, got := range rxTypeNames(list) {
				if got == want {
					out = append(out, name+":"+fd.Name.Name)
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func rxExportedFuncsReturning(files map[string]*ast.File, want string) []string {
	return rxExportedFuncs(files, want, false)
}

func rxExportedFuncsAccepting(files map[string]*ast.File, want string) []string {
	return rxExportedFuncs(files, want, true)
}

// The one sanctioned enqueue surface, keyed as the attribution map below keys it.
const (
	rxSeamFile = "enqueue.go"
	rxSeamFunc = "EnqueueExtraction"
	rxSeamKey  = rxSeamFile + ":" + rxSeamFunc
)

// TestExtractionExposesExactlyOneEnqueueSeam: the retired absence ban, now a count. EXTR-01's
// AC #6 and AC #7 forbade this package any enqueue surface at all; EXTR-09 opened exactly one
// at the user's critical-fork gate, so the fence counts to one rather than to zero.
// EnqueueExtraction in enqueue.go is what replaced the absence.
//
// Five bans, four of them unchanged: no func DECLARED with a banned enqueue name, no exported
// func returning the args type, and no exported func returning or ACCEPTING river.JobArgs --
// the interface river.Client.Insert and queue.EnqueueTx both take, which the name bans miss.
// The fifth, the enqueue SELECTOR, is now attributed to its enclosing func: exactly one func
// may reach one, and it must be the seam.
//
// Exact-match on the name ban, and the Enqueue prefix only on the count: river.JobArgs requires
// InsertOpts, so an Insert prefix would red-fail the shipped worker.
//
// rxTypeNames' nested-func-signature descent is mutation-proven, not needle-proven. JobsHandler
// and UploadHandler are the package's exported funcs taking funcs, and neither signature names a
// banned type.
func TestExtractionExposesExactlyOneEnqueueSeam(t *testing.T) {
	files := rxExtractionFiles(t)

	banned := map[string]bool{
		"Enqueue": true, "EnqueueTx": true,
		"Insert": true, "InsertTx": true, "InsertMany": true, "InsertManyTx": true,
	}
	// In-population control needles. internal/invoice/batch_submit.go, which the story names,
	// sits OUTSIDE this walk, so it can only prove the matcher compiles.
	control := map[string]bool{"OncePerJob": false, "WithinTenantTx": false}

	// seamHits attributes every tracked selector -- banned AND control -- to the func whose
	// body holds it, keyed file:func. The control selectors ride this same map so the needle
	// below proves the attribution reached a real body; a hit outside every body keys "file:"
	// and is an offender.
	seamHits := map[string][]string{}
	declared := map[string][]string{}
	nameOffenders := map[string][]string{}
	var exportedEnqueue []string
	seamDecls := 0
	seamExported := false

	scan := func(key string, root ast.Node) {
		ast.Inspect(root, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if banned[sel.Sel.Name] {
				seamHits[key] = append(seamHits[key], sel.Sel.Name)
			}
			if _, tracked := control[sel.Sel.Name]; tracked {
				control[sel.Sel.Name] = true
				seamHits[key] = append(seamHits[key], sel.Sel.Name)
			}
			return true
		})
	}

	for name, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				scan(name+":", decl)
				continue
			}
			declared[name] = append(declared[name], fd.Name.Name)
			if banned[fd.Name.Name] {
				nameOffenders[name] = append(nameOffenders[name], "func "+fd.Name.Name)
			}
			if fd.Name.IsExported() && strings.HasPrefix(fd.Name.Name, "Enqueue") {
				exportedEnqueue = append(exportedEnqueue, name+":"+fd.Name.Name)
			}
			if name == rxSeamFile && fd.Name.Name == rxSeamFunc {
				seamDecls++
				seamExported = fd.Name.IsExported()
			}
			// A closure nested in this body still attributes to the func that holds it.
			if fd.Body != nil {
				scan(name+":"+fd.Name.Name, fd.Body)
			}
		}
	}
	for name, seen := range control {
		if !seen {
			t.Fatalf("the selector matcher did not find %s anywhere in internal/extraction non-test files; it can no longer find a planted hit, so the absences reported below mean nothing", name)
		}
	}
	// Control for the per-func attribution: without a planted hit under a known key, an
	// attribution map that reached no body at all would report exactly one clean seam.
	if !slices.Contains(seamHits["worker.go:Work"], "OncePerJob") {
		t.Fatalf("the attribution collected %v for worker.go:Work, want it to include OncePerJob; it no longer reaches a func body, so the one-seam count below means nothing", seamHits["worker.go:Work"])
	}
	// Control for the declaration-name matcher: a selector scan cannot see a func DECLARED with
	// a banned name, which is the route an enqueue helper walked through.
	if !slices.Contains(declared["worker.go"], "AddTo") {
		t.Fatalf("the declaration-name matcher collected %v from worker.go, want it to include AddTo; it can no longer find a planted hit, so the name bans mean nothing", declared["worker.go"])
	}

	// The seam must exist before the count below can mean one rather than zero.
	if seamDecls != 1 {
		t.Fatalf("internal/extraction/%s declares %s %d time(s), want exactly 1; the seam is gone, so this guard is now vacuous", rxSeamFile, rxSeamFunc, seamDecls)
	}
	if !seamExported {
		t.Errorf("internal/extraction/%s declares %s unexported; no handler outside this package could reach it", rxSeamFile, rxSeamFunc)
	}

	// The selector ban, as a count. Every func that reaches an enqueue selector must be the seam.
	seamKeys := make([]string, 0, len(seamHits))
	for key, sels := range seamHits {
		for _, s := range sels {
			if banned[s] {
				seamKeys = append(seamKeys, key)
				break
			}
		}
	}
	sort.Strings(seamKeys)
	if !slices.Equal(seamKeys, []string{rxSeamKey}) {
		for _, key := range seamKeys {
			if key == rxSeamKey {
				continue
			}
			t.Errorf("internal/extraction/%s names %v: this package must enqueue only through %s, or the unexported args type fences nothing", key, seamHits[key], rxSeamFunc)
		}
		if !slices.Contains(seamKeys, rxSeamKey) {
			t.Errorf("no enqueue selector is attributed to %s; the seam enqueues nothing, so this guard counts to zero rather than to one", rxSeamKey)
		}
	}

	// AC-4 directly: a second exported Enqueue-prefixed decl is a second seam whatever it calls.
	sort.Strings(exportedEnqueue)
	if !slices.Equal(exportedEnqueue, []string{rxSeamKey}) {
		t.Errorf("the package exports %v as Enqueue-prefixed funcs, want exactly [%s]", exportedEnqueue, rxSeamKey)
	}

	names := make([]string, 0, len(nameOffenders))
	for name := range nameOffenders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("internal/extraction/%s declares %v: a func named for the client's own method is an enqueue surface whatever it does", name, nameOffenders[name])
	}

	// Control for the result-list matcher, same files, same matcher: a bare local type, a
	// slice of one, and a qualified one.
	for want, mustFind := range map[string]string{
		"MockExtractor": "mock.go:NewMockExtractor",
		"MockFixture":   "mock.go:MockFixtures",
		// The reason InsertOpts is not on the ban list, stated as a needle.
		"river.InsertOpts": "worker.go:InsertOpts",
	} {
		got := rxExportedFuncsReturning(files, want)
		found := false
		for _, g := range got {
			if g == mustFind {
				found = true
			}
		}
		if !found {
			t.Fatalf("the result-list matcher looking for %s found %v, want it to include %s; it can no longer find a planted hit, so the bans below mean nothing", want, got, mustFind)
		}
	}

	// Control for the parameter matcher, same files: it must find a planted hit through a
	// pointer and through a generic index, or the accepting ban below is vacuous.
	for want, mustFind := range map[string]string{
		"river.Workers": "worker.go:AddTo",
		"river.Job":     "worker.go:Work",
	} {
		got := rxExportedFuncsAccepting(files, want)
		if !slices.Contains(got, mustFind) {
			t.Fatalf("the parameter matcher looking for %s found %v, want it to include %s; it can no longer find a planted hit, so the accepting ban below means nothing", want, got, mustFind)
		}
	}

	argsType := rxArgsTypeName(t, files)
	if got := rxExportedFuncsReturning(files, argsType); len(got) > 0 {
		t.Errorf("%v hand a %s out through an exported result: an exported constructor is the one route the unexported type does not close", got, argsType)
	}
	if got := rxExportedFuncsReturning(files, "river.JobArgs"); len(got) > 0 {
		t.Errorf("%v return river.JobArgs: an args value handed out through the interface queue.EnqueueTx accepts is an enqueue surface, whatever the concrete type is called", got)
	}
	if got := rxExportedFuncsAccepting(files, "river.JobArgs"); len(got) > 0 {
		t.Errorf("%v accept river.JobArgs: a helper that builds the args and hands them to an injected inserter enqueues just as surely as one that calls the client itself", got)
	}
}
