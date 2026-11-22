package db_test

// Static, not runtime: routes are registered inline in each main(), and ServeMux lists no patterns.
// Blind spots: ParseForm/FormValue/MultipartReader body reads in the shape scan (multipart routes are
// Out of Scope), the size of a cap, and a 413 reference anywhere in a handler satisfies the walk, so the
// runtime *_bodycap_test.go tests carry the per-branch proof.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"testing"
)

const (
	bcModule       = "github.com/SimonOsipov/invoice-os/"
	bcJSONBodyPath = bcModule + "internal/platform/jsonbody"
	bcCapped413    = "capped-413"
	bcNoBody       = "no-body"
)

// Capped reads measured 63; floors are the measured count less 5.
const (
	bcRouteFloor      = 70
	bcCapped413Floor  = 54
	bcDecodeFloor     = 24
	bcCappedReadFloor = 58
)

// bcNoBodyRoutes must compute to no-body; a handler that starts reading the body fails as stale.
var bcNoBodyRoutes = map[string]string{
	"PATCH /v1/rules/{key}":                                          "reads only the path key",
	"POST /v1/approval-policies/{id}/publish":                        "reads only the path id",
	"POST /v1/customers/{id}/archive":                                "status change from the path",
	"POST /v1/customers/{id}/restore":                                "status change from the path",
	"POST /v1/entities/{id}/erp-connections/{connection}/disconnect": "status change from the path",
	"POST /v1/entities/{id}/offboard":                                "status change from the path",
	"POST /v1/entities/{id}/onboard":                                 "status change from the path",
	"POST /v1/imports/{id}/undo":                                     "reads only the path id",
	"POST /v1/invitations/{id}/resend":                               "reads only the path id",
	"POST /v1/invitations/{id}/revoke":                               "reads only the path id",
	"POST /v1/invoices/{id}/send-again":                              "reads only the path id",
	"POST /v1/invoices/{id}/validate":                                "reads only the path id",
	"POST /v1/memberships/{user_id}/remove":                          "reads only the path user id",
	"POST /v1/staff/rule-versions/draft":                             "takes no request body",
	"POST /v1/workspace/deletion/cancel":                             "takes no request body",
}

// bcExemptRoutes may not compute to capped-413; one that does is stale.
var bcExemptRoutes = map[string]string{
	"POST /v1/custom-rules":     "Out of Scope: caps with MaxBytesReader and answers 400, left as is by decision",
	"PUT /v1/custom-rules/{id}": "Out of Scope: caps with MaxBytesReader and answers 400, left as is by decision",
}

// bcShapeExemptions are the only handlers allowed to read a body uncapped.
var bcShapeExemptions = []struct{ file, fn, why string }{
	{"internal/gateway/gateway.go", "MockLoginHandler", "mock-issuer build only, public /auth route, never in production"},
	{"internal/platform/resetretry.go", "retryable", "an outgoing request; it only checks that a body is absent and never reads it"},
}

// ---------------------------------------------------------------------------
// Route walk
// ---------------------------------------------------------------------------

// bcPkgLoader returns the package-level funcs of one repo directory.
type bcPkgLoader func(dir string) map[string]*ast.FuncDecl

func bcHandlerSelector(call *ast.CallExpr, imports map[string]string) (alias, name string, n int) {
	ast.Inspect(call.Args[1], func(x ast.Node) bool {
		if s, ok := x.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && imports[id.Name] != "" && strings.HasSuffix(s.Sel.Name, "Handler") {
				alias, name = id.Name, s.Sel.Name
				n++
			}
		}
		return true
	})
	return alias, name, n
}

// bcVerdictOf closes over the same-package funcs fd references and classifies how the closure treats the body.
func bcVerdictOf(fns map[string]*ast.FuncDecl, fd *ast.FuncDecl) string {
	bodies := []*ast.FuncDecl{fd}
	seen := map[*ast.FuncDecl]bool{fd: true}
	for i := 0; i < len(bodies); i++ {
		ast.Inspect(bodies[i], func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok && fns[id.Name] != nil && !seen[fns[id.Name]] {
				seen[fns[id.Name]] = true
				bodies = append(bodies, fns[id.Name])
			}
			return true
		})
	}
	capped, s413, reads := false, false, false
	for _, b := range bodies {
		ast.Inspect(b, func(x ast.Node) bool {
			switch e := x.(type) {
			case *ast.CallExpr:
				if s, ok := e.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "LimitReader" && len(e.Args) > 0 {
					if a, ok := e.Args[0].(*ast.SelectorExpr); ok && a.Sel.Name == "Body" {
						capped = true
					}
				}
			case *ast.SelectorExpr:
				switch e.Sel.Name {
				case "MaxBytesReader":
					capped = true
				case "StatusRequestEntityTooLarge", "TooLarge":
					s413 = true
				case "Body", "ParseForm", "FormValue", "PostFormValue", "ParseMultipartForm", "MultipartReader", "FormFile":
					reads = true
				case "Decode":
					if id, ok := e.X.(*ast.Ident); ok && id.Name == "jsonbody" {
						capped, reads = true, true
					}
				}
			}
			return true
		})
	}
	switch {
	case capped && s413:
		return bcCapped413
	case capped:
		return "capped-not-413"
	case !reads:
		return bcNoBody
	}
	return "uncapped-read"
}

func bcRouteWalk(fset *token.FileSet, mains map[string]*ast.File, load bcPkgLoader, noBody, exempt map[string]string) (problems []string, routes, capped int) {
	registered := map[string]bool{}
	for _, rel := range sortedKeys(mains) {
		f := mains[rel]
		consts := map[string]string{}
		scStringConstsIn(f, consts)
		found, unresolved := scRoutesIn(fset, rel, f, consts)
		for _, u := range unresolved {
			problems = append(problems, u+": route argument cannot be resolved")
		}
		imports := map[string]string{}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			name := path.Base(p)
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = p
		}
		calls := map[int]*ast.CallExpr{}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && len(call.Args) >= 2 {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && scMuxMethods[sel.Sel.Name] && scBaseName(sel.X) == "Mux" {
					calls[fset.Position(call.Pos()).Line] = call
				}
			}
			return true
		})
		for _, r := range found {
			method, pattern, _ := strings.Cut(r.route, " ")
			if (method != "POST" && method != "PUT" && method != "PATCH") || !strings.HasPrefix(pattern, "/v1/") {
				continue
			}
			registered[r.route] = true
			routes++
			call := calls[r.line]
			if call == nil {
				problems = append(problems, r.String()+": handler argument not found")
				continue
			}
			alias, name, n := bcHandlerSelector(call, imports)
			if n != 1 {
				problems = append(problems, r.String()+": handler argument unresolved ("+strconv.Itoa(n)+" imported *Handler selectors, want 1)")
				continue
			}
			dir, ok := strings.CutPrefix(imports[alias], bcModule)
			if !ok {
				problems = append(problems, r.String()+": handler "+alias+"."+name+" is outside the module")
				continue
			}
			fns := load(dir)
			fd := fns[name]
			if fd == nil {
				problems = append(problems, r.String()+": handler "+alias+"."+name+" not found in "+dir)
				continue
			}
			v := bcVerdictOf(fns, fd)
			if v == bcCapped413 {
				capped++
			}
			_, isNoBody := noBody[r.route]
			_, isExempt := exempt[r.route]
			switch {
			case isNoBody:
				if v != bcNoBody {
					problems = append(problems, r.String()+": in bcNoBodyRoutes but "+name+" is "+v)
				}
			case isExempt:
				if v == bcCapped413 {
					problems = append(problems, r.String()+": in bcExemptRoutes but "+name+" is now capped-413")
				}
			case v != bcCapped413:
				problems = append(problems, r.String()+": "+name+" is "+v+" and has no verdict entry; cap it with 413 or classify it")
			}
		}
	}
	for _, m := range []map[string]string{noBody, exempt} {
		for _, k := range sortedKeys(m) {
			if !registered[k] {
				problems = append(problems, k+": has an entry but is not a registered /v1/ body route")
			}
		}
	}
	return problems, routes, capped
}

// bcRepoLoader reads one package directory per call, cached; it reuses hmGoFiles as the file lister.
func bcRepoLoader(t *testing.T, fset *token.FileSet, root string) bcPkgLoader {
	cache := map[string]map[string]*ast.FuncDecl{}
	return func(dir string) map[string]*ast.FuncDecl {
		if m, ok := cache[dir]; ok {
			return m
		}
		m := map[string]*ast.FuncDecl{}
		for _, rel := range hmGoFiles(t, root, []string{dir}) {
			if path.Dir(rel) != dir {
				continue
			}
			for _, d := range scParse(t, fset, root, rel).Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
					m[fd.Name.Name] = fd
				}
			}
		}
		cache[dir] = m
		return m
	}
}

const bcFixtureMain = `package main

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/fx"
)

func main() {
	app.Mux.Handle("POST /v1/raw", fx.RawHandler(1))
	app.Mux.Handle("POST /v1/cap-only", fx.CapOnlyHandler(1))
	app.Mux.Handle("POST /v1/lim-only", fx.LimOnlyHandler(1))
	app.Mux.Handle("POST /v1/dyn", app.Wrap(http.NotFound))
	app.Mux.Handle("POST /v1/good", fx.GoodHandler(1))
	app.Mux.Handle("POST /v1/lim-good", fx.LimGoodHandler(1))
	app.Mux.Handle("GET /v1/peek", fx.RawHandler(1))
	app.Mux.Handle("POST /internal/raw", fx.RawHandler(1))
	app.Mux.Handle("POST /v1/claimed", fx.RawHandler(1))
	app.Mux.Handle("POST /v1/silent", fx.QuietHandler(1))
}
`

const bcFixtureHandlers = `package fx

func RawHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { _ = json.NewDecoder(r.Body).Decode(&n) }
}

func CapOnlyHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { r.Body = http.MaxBytesReader(w, r.Body, 1) }
}

func LimOnlyHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { _, _ = io.ReadAll(io.LimitReader(r.Body, 1)) }
}

func GoodHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := jsonbody.Decode(w, r, 1, &n); jsonbody.TooLarge(err) {
		}
	}
}

func LimGoodHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(io.LimitReader(r.Body, 1))
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}
}

func QuietHandler(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
`

func bcRouteControlNeedles(t *testing.T) {
	fset := token.NewFileSet()
	mainF, err := parser.ParseFile(fset, "cmd/fx/main.go", bcFixtureMain, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgF, err := parser.ParseFile(fset, "internal/fx/fx.go", bcFixtureHandlers, 0)
	if err != nil {
		t.Fatal(err)
	}
	fns := map[string]*ast.FuncDecl{}
	for _, d := range pkgF.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			fns[fd.Name.Name] = fd
		}
	}
	load := func(dir string) map[string]*ast.FuncDecl {
		if dir == "internal/fx" {
			return fns
		}
		return nil
	}
	problems, routes, capped := bcRouteWalk(fset, map[string]*ast.File{"cmd/fx/main.go": mainF}, load,
		map[string]string{"POST /v1/claimed": "x", "POST /v1/ghost": "x"}, nil)
	want := []string{"POST /v1/raw", "POST /v1/cap-only", "POST /v1/lim-only", "POST /v1/dyn", "POST /v1/claimed", "POST /v1/ghost", "POST /v1/silent"}
	for _, w := range want {
		n := 0
		for _, p := range problems {
			if strings.Contains(p, w) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("route %q named by %d problem(s), want exactly 1; problems: %v", w, n, problems)
		}
	}
	if len(problems) != len(want) {
		t.Errorf("got %d problems, want %d: %v", len(problems), len(want), problems)
	}
	if routes != 8 || capped != 2 {
		t.Errorf("counted %d routes and %d capped-413, want 8 and 2 (GET and /internal/ rows are not body routes; LimitReader+413 is capped-413)", routes, capped)
	}
}

func TestBodyCap_EverySignedInBodyRouteIsCappedWith413(t *testing.T) {
	root := repoRootDir(t)
	fset := token.NewFileSet()
	mains := map[string]*ast.File{}
	for _, rel := range hmGoFiles(t, root, []string{"cmd"}) {
		if !strings.HasPrefix(rel, "cmd/gateway/") {
			mains[rel] = scParse(t, fset, root, rel)
		}
	}
	problems, routes, capped := bcRouteWalk(fset, mains, bcRepoLoader(t, fset, root), bcNoBodyRoutes, bcExemptRoutes)
	for _, p := range problems {
		t.Error(p)
	}
	t.Logf("%d /v1/ body routes, %d capped-413", routes, capped)
	if routes < bcRouteFloor || capped < bcCapped413Floor {
		t.Errorf("walk found %d routes and %d capped-413, want at least %d and %d (75 and 59 measured) — a clean report over a broken walk means nothing", routes, capped, bcRouteFloor, bcCapped413Floor)
	}
	if len(bcNoBodyRoutes) == 0 || len(bcExemptRoutes) == 0 {
		t.Error("a hand map is empty; both must be live")
	}
	t.Run("control needles", func(t *testing.T) { bcRouteControlNeedles(t) })
}

// ---------------------------------------------------------------------------
// Shape scan
// ---------------------------------------------------------------------------

type bcFinding struct {
	file, fn string
	line     int
}

func (f bcFinding) String() string {
	return f.file + ":" + strconv.Itoa(f.line) + " " + f.fn + " reads the request body with no cap before it"
}

type bcScope struct{ caps []token.Pos }

type bcRead struct {
	sc  *bcScope
	pos token.Pos
}

type bcWalker struct {
	httpA, ioA  string
	reads       []bcRead
	cappedReads int
}

func bcIsCall(e ast.Node, pkg, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || pkg == "" {
		return false
	}
	s, ok := call.Fun.(*ast.SelectorExpr)
	return ok && s.Sel.Name == name && scBaseName(s.X) == pkg
}

// fn opens a scope for each *http.Request parameter; a literal without one shares the enclosing scopes.
func (w *bcWalker) fn(ft *ast.FuncType, body *ast.BlockStmt, env map[string]*bcScope) {
	inner := map[string]*bcScope{}
	for k, v := range env {
		inner[k] = v
	}
	for _, fld := range ft.Params.List {
		star, ok := fld.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if s, ok := star.X.(*ast.SelectorExpr); ok && s.Sel.Name == "Request" && w.httpA != "" && scBaseName(s.X) == w.httpA {
			for _, n := range fld.Names {
				inner[n.Name] = &bcScope{}
			}
		}
	}
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if lit, ok := n.(*ast.FuncLit); ok {
			w.fn(lit.Type, lit.Body, inner)
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Body" {
			if id, ok := sel.X.(*ast.Ident); ok && inner[id.Name] != nil {
				w.classify(sel, inner[id.Name], stack)
			}
		}
		stack = append(stack, n)
		return true
	})
}

// classify applies the read rules to one N.Body; stack ends at the node's parent.
func (w *bcWalker) classify(sel *ast.SelectorExpr, sc *bcScope, stack []ast.Node) {
	var parent ast.Node
	if len(stack) > 0 {
		parent = stack[len(stack)-1]
	}
	switch p := parent.(type) {
	case *ast.AssignStmt:
		for _, l := range p.Lhs {
			if l == sel {
				for _, r := range p.Rhs {
					if bcIsCall(r, w.httpA, "MaxBytesReader") {
						sc.caps = append(sc.caps, p.End())
					}
				}
				return
			}
		}
	case *ast.CallExpr:
		if (bcIsCall(p, w.httpA, "MaxBytesReader") && len(p.Args) > 1 && p.Args[1] == sel) ||
			(bcIsCall(p, w.ioA, "LimitReader") && len(p.Args) > 0 && p.Args[0] == sel) {
			w.cappedReads++
			return
		}
	case *ast.SelectorExpr:
		if p.X == sel && p.Sel.Name == "Close" {
			return
		}
	case *ast.BinaryExpr:
		other := p.Y
		if p.Y == sel {
			other = p.X
		}
		if id, ok := other.(*ast.Ident); ok && id.Name == "nil" && (p.Op == token.EQL || p.Op == token.NEQ) {
			return
		}
	}
	w.reads = append(w.reads, bcRead{sc, sel.Pos()})
}

// bcScanFile returns the uncapped reads in f, the capped-read count and the jsonbody.Decode call sites.
func bcScanFile(fset *token.FileSet, rel string, f *ast.File) (findings []bcFinding, cappedReads, decodes int) {
	jb := scImportAlias(f, bcJSONBodyPath, "jsonbody")
	ast.Inspect(f, func(n ast.Node) bool {
		if bcIsCall(n, jb, "Decode") {
			decodes++
		}
		return true
	})
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		w := &bcWalker{httpA: scImportAlias(f, "net/http", "http"), ioA: scImportAlias(f, "io", "io")}
		w.fn(fd.Type, fd.Body, nil)
		for _, r := range w.reads {
			capped := false
			for _, c := range r.sc.caps {
				if c < r.pos {
					capped = true
				}
			}
			if capped {
				w.cappedReads++
			} else {
				findings = append(findings, bcFinding{rel, fd.Name.Name, fset.Position(r.pos).Line})
			}
		}
		cappedReads += w.cappedReads
	}
	return findings, cappedReads, decodes
}

const bcShapeHead = `package x

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

`

// A flagged case carries exactly one line ending in "// flag".
var bcShapeCases = []struct {
	name, src string
	flagged   bool
}{
	{"decoder on r.Body", `func H(w http.ResponseWriter, r *http.Request) {
	var v any
	_ = json.NewDecoder(r.Body).Decode(&v) // flag
}`, true},
	{"io.ReadAll(r.Body)", `func H(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body) // flag
}`, true},
	{"alias then read", `func H(w http.ResponseWriter, r *http.Request) {
	b := r.Body // flag
	_, _ = io.ReadAll(b)
}`, true},
	{"cap after the read", `func H(w http.ResponseWriter, r *http.Request) {
	var v any
	_ = json.NewDecoder(r.Body).Decode(&v) // flag
	r.Body = http.MaxBytesReader(w, r.Body, 1)
}`, true},
	{"cap on another request variable", `func H(w http.ResponseWriter, r, other *http.Request) {
	other.Body = http.MaxBytesReader(w, other.Body, 1)
	_, _ = io.ReadAll(r.Body) // flag
}`, true},
	{"literal with its own request param", `func H(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	f := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // flag
	}
	f(w, r)
}`, true},
	{"cap before the read", `func H(w http.ResponseWriter, r *http.Request) {
	var v any
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	_ = json.NewDecoder(r.Body).Decode(&v)
}`, false},
	{"inline MaxBytesReader", `func H(w http.ResponseWriter, r *http.Request) {
	var v any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1)).Decode(&v)
}`, false},
	{"io.LimitReader", `func H(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(io.LimitReader(r.Body, 1))
}`, false},
	{"Close", `func H(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
}`, false},
	{"nil compare", `func H(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		w.WriteHeader(http.StatusOK)
	}
}`, false},
	{"jsonbody.Decode", `func H(w http.ResponseWriter, r *http.Request) {
	var v any
	_ = jsonbody.Decode(w, r, 1, &v)
}`, false},
	{"literal without a request param after the cap", `func H(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	func() { _, _ = io.ReadAll(r.Body) }()
}`, false},
}

func bcShapeControlNeedles(t *testing.T) {
	for _, c := range bcShapeCases {
		t.Run(c.name, func(t *testing.T) {
			src := bcShapeHead + c.src + "\n"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fx.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			got, _, _ := bcScanFile(fset, "fx.go", f)
			if !c.flagged {
				if len(got) != 0 {
					t.Fatalf("flagged %v, want none", got)
				}
				return
			}
			want := 0
			for i, l := range strings.Split(src, "\n") {
				if strings.HasSuffix(l, "// flag") {
					want = i + 1
				}
			}
			if len(got) != 1 || got[0].line != want || got[0].fn != "H" {
				t.Fatalf("got %v, want exactly one finding in H at line %d", got, want)
			}
		})
	}
}

func TestBodyCap_NoHandlerReadsTheBodyBeforeACap(t *testing.T) {
	root := repoRootDir(t)
	fset := token.NewFileSet()
	used := make([]bool, len(bcShapeExemptions))
	cappedReads, decodes := 0, 0
	for _, rel := range hmGoFiles(t, root, []string{"cmd", "internal"}) {
		got, c, d := bcScanFile(fset, rel, scParse(t, fset, root, rel))
		cappedReads += c
		decodes += d
	next:
		for _, f := range got {
			for i, e := range bcShapeExemptions {
				if e.file == f.file && e.fn == f.fn {
					used[i] = true
					continue next
				}
			}
			t.Error(f)
		}
	}
	for i, e := range bcShapeExemptions {
		if !used[i] {
			t.Errorf("exemption %s %s matches nothing; delete it", e.file, e.fn)
		}
	}
	t.Logf("%d jsonbody.Decode sites, %d capped reads", decodes, cappedReads)
	if decodes < bcDecodeFloor || cappedReads < bcCappedReadFloor {
		t.Errorf("scan found %d jsonbody.Decode sites and %d capped reads, want at least %d and %d — a clean report over a broken walk means nothing", decodes, cappedReads, bcDecodeFloor, bcCappedReadFloor)
	}
	t.Run("control needles", func(t *testing.T) { bcShapeControlNeedles(t) })
}

func TestBodyCap_FlagsTheRevertedCreateHandler(t *testing.T) {
	const rel = "internal/invoice/handlers.go"
	const needle = `jsonbody.Decode(w, r, jsonbody.LineItemsLimit, &req)`
	root := repoRootDir(t)
	src := vrRead(t, root, rel)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fd *ast.FuncDecl
	for _, d := range f.Decls {
		if x, ok := d.(*ast.FuncDecl); ok && x.Recv == nil && x.Name.Name == "CreateHandler" {
			fd = x
		}
	}
	if fd == nil {
		t.Fatal("CreateHandler not found")
	}
	start, end := fset.Position(fd.Pos()).Offset, fset.Position(fd.End()).Offset
	body := src[start:end]
	if n := strings.Count(body, needle); n != 1 {
		t.Fatalf("control needle found %d times in CreateHandler, want exactly 1", n)
	}
	mutated := src[:start] + strings.Replace(body, needle, `json.NewDecoder(r.Body).Decode(&req)`, 1) + src[end:]
	fset2 := token.NewFileSet()
	f2, err := parser.ParseFile(fset2, rel, mutated, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := bcScanFile(fset2, rel, f2)
	if len(got) != 1 || got[0].fn != "CreateHandler" {
		t.Fatalf("got %v, want exactly one finding naming CreateHandler", got)
	}
}
