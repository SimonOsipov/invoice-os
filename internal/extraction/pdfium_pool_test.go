// pdfium_pool_test.go: AC-1's backend fence and AC-7's MaxTotal pin, as source scans. The two
// runtime pins live in pdfium_pool_internal_test.go, which can name unexported identifiers.
//
// Each scan carries a floor and a control needle, per reachability_test.go:1-11. The backend
// fence moved here from EXTR-02-01: there nothing imported go-pdfium, so its positive half had
// nothing to find and its absence report covered an empty set.
//
// Stdlib only. deps_test.go scan B walks test imports too, and any in-module import outside
// internal/platform/* fails it.
package extraction_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	pxWebAssembly    = pdfiumModule + "/webassembly"
	pxRequests       = pdfiumModule + "/requests"
	pxResponses      = pdfiumModule + "/responses"
	pxSingleThreaded = pdfiumModule + "/single_threaded"
	pxMultiThreaded  = pdfiumModule + "/multi_threaded"

	// Floor, measured on this tree: the walk parses 154 non-test .go files.
	pxMinFiles = 130

	pxPoolFile    = "pdfium_pool.go"
	pxPoolFileRel = "internal/extraction/" + pxPoolFile

	pxSubmissionMain = "cmd/submission/main.go"

	// The extraction queue key as cmd/submission/main.go writes it.
	pxExtractionQueueKey = "extraction.QueueName"
)

// pxSourceFiles are the files the ambient scan reads. pdfium.go and pagestore.go arrive in
// later subtasks; the scan takes those that exist and fatals at zero.
var pxSourceFiles = []string{"pdfium.go", "pdfium_extractor.go", pxPoolFile, "pagestore.go"}

// pxAllowedImports is what the pdfium source may import. An allowlist is stronger than the
// denylist AC-7 names: time, os, math/rand and net/* all need an import, and so do crypto/rand,
// runtime and unsafe, which it never mentions. crypto/sha256 and encoding/hex are admitted for
// PageKey's content hash: both are pure functions of their input, which is the property the
// list exists to keep.
var pxAllowedImports = map[string]bool{
	"bytes":         true,
	"context":       true,
	"crypto/sha256": true,
	"encoding/hex":  true,
	"fmt":           true,
	"image/png":     true,
	"sync":          true,
	"sync/atomic":   true,
	"unicode":       true,
	pdfiumModule:    true,
	pxRequests:      true,
	pxResponses:     true,
	pxWebAssembly:   true,
}

// TestPDFium_UsesTheWebAssemblyBackendOnly: AC-1. The wazero backend is what keeps the module
// building with cgo off; single_threaded needs pkg-config under cgo and does not compile at all
// without it, so one such import strands the whole fleet's image build.
func TestPDFium_UsesTheWebAssemblyBackendOnly(t *testing.T) {
	root := rxRepoRoot(t)

	var parsed int
	var wasm []string
	cgoBackends := map[string][]string{}

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
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		parsed++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, spec := range f.Imports {
			p, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Errorf("%s: unparseable import %s", rel, spec.Path.Value)
				continue
			}
			switch p {
			case pxWebAssembly:
				wasm = append(wasm, rel)
			case pxSingleThreaded, pxMultiThreaded:
				cgoBackends[rel] = append(cgoBackends[rel], p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if parsed < pxMinFiles {
		t.Fatalf("the walk parsed %d non-test .go file(s), want at least %d -- a clean report over a broken walk means nothing", parsed, pxMinFiles)
	}

	// Positive half first. Without a real importer every verdict below is a report about a
	// module that uses no backend at all.
	if len(wasm) == 0 {
		t.Fatalf("no non-test file in %s imports %s; the absence reported below would cover nothing", root, pxWebAssembly)
	}
	sort.Strings(wasm)
	t.Logf("%s is imported by %d file(s): %s", pxWebAssembly, len(wasm), strings.Join(wasm, ", "))
	if len(wasm) != 1 || wasm[0] != pxPoolFileRel {
		t.Errorf("%s is imported by %s, want %s alone: the pool is process-wide and one file owns its construction", pxWebAssembly, strings.Join(wasm, ", "), pxPoolFileRel)
	}

	offenders := make([]string, 0, len(cgoBackends))
	for rel := range cgoBackends {
		offenders = append(offenders, rel)
	}
	sort.Strings(offenders)
	for _, rel := range offenders {
		t.Errorf("%s imports %v: the cgo backends need pkg-config and do not build with CGO_ENABLED=0, which is how the shared Dockerfile builds every service", rel, cgoBackends[rel])
	}
}

// TestPDFiumSourceHasNoAmbientDependency: AC-7. Mirrors TestMockExtractor_HasNoAmbientDependency,
// which reads mock.go alone and so cannot see these files.
func TestPDFiumSourceHasNoAmbientDependency(t *testing.T) {
	var scanned, seen int

	fset := token.NewFileSet()
	for _, name := range pxSourceFiles {
		if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, spec := range f.Imports {
			p, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Errorf("%s: unparseable import %s", fset.Position(spec.Pos()), spec.Path.Value)
				continue
			}
			seen++
			if !pxAllowedImports[p] {
				t.Errorf("%s: %s imports %q; the allowlist is %v -- time, os, math/rand and net/* are all outside it. For a deadline take pdfium.Pool.GetInstanceWithContext, not GetInstance(timeout): it carries the job context and needs no clock",
					fset.Position(spec.Pos()), name, p, mxSortedStrings(pxAllowedImports))
			}
		}
	}

	if scanned == 0 {
		t.Fatalf("none of %v exists in this package; the allowlist above examined nothing", pxSourceFiles)
	}
	if seen == 0 {
		t.Fatalf("the %d scanned file(s) declare no imports at all; the allowlist above examined nothing", scanned)
	}
}

// TestPDFiumMaxTotalMatchesTheQueueWorkerCount: AC-7. Both numbers are read from source, so a
// change to either side alone fails here rather than shipping a pool that blocks a worker or
// holds an instance no worker can ever ask for.
func TestPDFiumMaxTotalMatchesTheQueueWorkerCount(t *testing.T) {
	root := rxRepoRoot(t)
	main := filepath.Join(root, filepath.FromSlash(pxSubmissionMain))

	workers := pxQueueMaxWorkers(t, main)
	if len(workers) == 0 {
		t.Fatalf("the parse found no MaxWorkers in %s; the comparison below would be vacuous", pxSubmissionMain)
	}
	want, ok := workers[pxExtractionQueueKey]
	if !ok {
		t.Fatalf("%s sets MaxWorkers for %s but not for %s; the extraction queue has been renamed or moved", pxSubmissionMain, strings.Join(pxSortedKeys(workers), ", "), pxExtractionQueueKey)
	}

	got := pxConfigMaxTotal(t, pxPoolFile)
	if got != want {
		t.Errorf("%s builds the pool with MaxTotal %d, but %s runs the %s queue with MaxWorkers %d: a smaller pool blocks a worker, and a larger one holds an instance no worker can ask for", pxPoolFile, got, pxSubmissionMain, pxExtractionQueueKey, want)
	}
}

// pxConfigMaxTotal reads MaxTotal off the webassembly.Config literal, resolving a const
// identifier to its number. MinIdle is read first as the control needle: it differs from
// MaxTotal, so a field reader answering every key with one value shows up here.
func pxConfigMaxTotal(t *testing.T, name string) int {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	consts := pxIntConsts(f)

	var lit *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || pxExprName(cl.Type) != "webassembly.Config" {
			return true
		}
		lit = cl
		return false
	})
	if lit == nil {
		t.Fatalf("%s declares no webassembly.Config literal; the comparison above would be vacuous", name)
	}

	idle, ok := pxIntField(lit, "MinIdle", consts)
	if !ok {
		t.Fatalf("the webassembly.Config literal in %s carries no MinIdle; field reading is broken, so the MaxTotal below means nothing", name)
	}
	total, ok := pxIntField(lit, "MaxTotal", consts)
	if !ok {
		t.Fatalf("the webassembly.Config literal in %s carries no MaxTotal", name)
	}
	if idle == total {
		t.Fatalf("MinIdle and MaxTotal both read %d in %s; the field reader is answering every key with one value", total, name)
	}
	return total
}

// pxQueueMaxWorkers reads every MaxWorkers in one file, keyed by the queue-name expression as
// written.
func pxQueueMaxWorkers(t *testing.T, path string) map[string]int {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	consts := pxIntConsts(f)

	out := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		cl, isLit := kv.Value.(*ast.CompositeLit)
		key := pxExprName(kv.Key)
		if !isLit || key == "" {
			return true
		}
		if v, ok := pxIntField(cl, "MaxWorkers", consts); ok {
			out[key] = v
		}
		return true
	})
	return out
}

// pxIntConsts collects the file's package-level int consts, so a field written as an identifier
// still reads as a number.
func pxIntConsts(f *ast.File) map[string]int {
	out := map[string]int{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if v, ok := pxIntLit(vs.Values[i]); ok {
					out[n.Name] = v
				}
			}
		}
	}
	return out
}

func pxIntField(cl *ast.CompositeLit, field string, consts map[string]int) (int, bool) {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != field {
			continue
		}
		if v, ok := pxIntLit(kv.Value); ok {
			return v, true
		}
		if id, ok := kv.Value.(*ast.Ident); ok {
			v, known := consts[id.Name]
			return v, known
		}
		return 0, false
	}
	return 0, false
}

func pxIntLit(e ast.Expr) (int, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	v, err := strconv.Atoi(lit.Value)
	if err != nil {
		return 0, false
	}
	return v, true
}

func pxExprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if x, ok := v.X.(*ast.Ident); ok {
			return x.Name + "." + v.Sel.Name
		}
	}
	return ""
}

func pxSortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
