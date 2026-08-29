// Two source scans over the db.ErrNotActiveMember -> 403 mapping (AUDIT-10-03).
// Both assert an ABSENCE — that no refusal-mapping site is left without the arm,
// and that the wire message is written down exactly once — which is the
// instrument class that reports all-clear while examining nothing. So both carry
// planted control needles and population floors naming their measured baseline.
//
// Scan 1 is AST, never text: a site is the INNERMOST enclosing func, so an inline
// switch inside a returned handler literal is attributed to the literal and not
// to the FuncDecl around it. A name-shaped predicate alone cannot see tenancy's
// two inline switches, which is what the P2-only floor makes loud. The site
// counts live in those floors, not here: they move with every new handler.
//
// The P2 predicate is a CALL to errors.Is(_, db.ErrNoTenant), never a bare
// reference to the sentinel. Three store methods return db.ErrNoTenant; a
// reference-shaped predicate would demand a 403 arm inside a store method, and
// the allowlist that "fixes" that is the rot these scans exist to prevent.
//
// Names carry the TestHandlerMapping prefix deliberately — ci.yml's -run
// alternation is what makes them run in CI at all
// (TestCIRunFiltersReachEveryTestInThePackage). Neither test touches a database.
//
// frontend/** and e2e/** are out of both walks, matching
// retention_claim_gate_test.go: no ci.yml path filter routes a frontend-only
// commit to the Go job, so a Go assertion over them would be unreachable on the
// very commit it guards. AUDIT-10-07 owns the TypeScript mirror.
//
// Known limits. Scan 1 sees only errors.Is; a mapper switching on a type
// assertion escapes it. Scan 2 counts string literals, so a message split across
// a concatenation, or mentioned in a comment, is invisible to it.
package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const hmDBImportPath = "github.com/SimonOsipov/invoice-os/internal/platform/db"

// hmLiteralName is what a site with no declared name is reported as. A site's
// name is diagnostic only; attribution is by position.
const hmLiteralName = "func literal"

// hmGoFiles returns every non-test .go file under roots, repo-relative.
func hmGoFiles(t *testing.T, root string, roots []string) []string {
	t.Helper()
	var out []string
	for _, r := range roots {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(r)), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Scan 1 — every refusal-mapping site names ErrNotActiveMember
// ---------------------------------------------------------------------------

// hmSite is one refusal-mapping site: the innermost func enclosing the mapping.
// p1 is the name-shaped population (statusForErr / *StatusForErr); p2 is the
// call-shaped one (a body calling errors.Is(_, db.ErrNoTenant)). armed says the
// same body also names db.ErrNotActiveMember.
type hmSite struct {
	file  string
	line  int
	name  string
	p1    bool
	p2    bool
	armed bool
}

func (s hmSite) String() string {
	return s.file + ":" + strconv.Itoa(s.line) + " (" + s.name + ")"
}

// hmIsMapperName is the P1 predicate.
func hmIsMapperName(n string) bool {
	return n == "statusForErr" || strings.HasSuffix(n, "StatusForErr")
}

// hmDBAlias returns the name internal/platform/db is bound to in f, or "" when f
// does not import it. Resolving the alias rather than assuming "db" keeps an
// aliased import from silently leaving a site out of the population.
func hmDBAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != hmDBImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "db"
	}
	return ""
}

// hmIsSelector reports whether e is exactly x.sel.
func hmIsSelector(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x
}

// hmOwnBodyCallsErrorsIs reports whether body calls errors.Is(_, alias.sentinel),
// EXCLUDING nested func literals: a literal is its own site, so a mapping inside
// one belongs to the literal, not to the func around it.
func hmOwnBodyCallsErrorsIs(body *ast.BlockStmt, alias, sentinel string) bool {
	if alias == "" {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !hmIsSelector(call.Fun, "errors", "Is") || len(call.Args) != 2 {
			return true
		}
		if hmIsSelector(call.Args[1], alias, sentinel) {
			found = true
			return false
		}
		return true
	})
	return found
}

// hmSitesIn collects every refusal-mapping site in f.
func hmSitesIn(fset *token.FileSet, rel string, f *ast.File) []hmSite {
	alias := hmDBAlias(f)
	var sites []hmSite

	consider := func(pos token.Pos, name string, body *ast.BlockStmt, named bool) {
		if body == nil {
			return
		}
		p2 := hmOwnBodyCallsErrorsIs(body, alias, "ErrNoTenant")
		if !named && !p2 {
			return
		}
		sites = append(sites, hmSite{
			file:  rel,
			line:  fset.Position(pos).Line,
			name:  name,
			p1:    named,
			p2:    p2,
			armed: hmOwnBodyCallsErrorsIs(body, alias, "ErrNotActiveMember"),
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			consider(fn.Pos(), fn.Name.Name, fn.Body, fn.Recv == nil && hmIsMapperName(fn.Name.Name))
		case *ast.FuncLit:
			consider(fn.Pos(), hmLiteralName, fn.Body, false)
		}
		return true
	})
	return sites
}

// hmFixtureSites parses an in-test source string. Control needles are parsed as
// strings, never read from the repo, so they cannot drift with it.
func hmFixtureSites(t *testing.T, name, src string) []hmSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return hmSitesIn(fset, name, f)
}

// hmFixtureHead is the preamble every control needle shares.
const hmFixtureHead = `package x

import (
	"errors"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

`

// hmNamedMapperNoArm is C1: a named mapper that never names the sentinel.
const hmNamedMapperNoArm = `func statusForErr(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
`

// hmNamedMapperArmed is hmNamedMapperNoArm, corrected.
const hmNamedMapperArmed = `func statusForErr(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
`

// hmInlineLitNoArm is C2: the tenancy shape — an inline switch inside a returned
// handler literal. The site is the literal; the FuncDecl around it maps nothing.
const hmInlineLitNoArm = `func MeHandler(load func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch err := load(); {
		case errors.Is(err, db.ErrNoTenant):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
}
`

// hmInlineLitArmed is hmInlineLitNoArm, corrected.
const hmInlineLitArmed = `func MeHandler(load func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch err := load(); {
		case errors.Is(err, db.ErrNoTenant):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case errors.Is(err, db.ErrNotActiveMember):
			http.Error(w, db.NotActiveMemberMessage, http.StatusForbidden)
			return
		}
	}
}
`

// hmProducer is C4: a store method RETURNING the sentinel. Demanding a 403 arm
// here is nonsense, and the three-entry allowlist that would follow is what the
// call-shaped predicate exists to avoid.
const hmProducer = `func p() error { return db.ErrNoTenant }
`

// hmCommentOnlyArm is C5: the arm named only in a comment.
const hmCommentOnlyArm = `func statusForErr(err error) (int, string) {
	// db.ErrNotActiveMember is mapped by the caller.
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	}
	return http.StatusInternalServerError, "internal server error"
}
`

func hmControlNeedles(t *testing.T) {
	t.Run("C1 named mapper missing the arm", func(t *testing.T) {
		sites := hmFixtureSites(t, "c1.go", hmFixtureHead+hmNamedMapperNoArm)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v in a fixture carrying exactly one named mapper — the scan cannot see what it looks for", len(sites), sites)
		}
		if !sites[0].p1 || !sites[0].p2 || sites[0].name != "statusForErr" {
			t.Fatalf("site = %+v, want the named mapper in both populations", sites[0])
		}
		if sites[0].armed {
			t.Fatal("the scan reports an unarmed named mapper as armed — a clean report against the repo would prove nothing")
		}
	})

	t.Run("C2 inline literal attributed to the literal", func(t *testing.T) {
		sites := hmFixtureSites(t, "c2.go", hmFixtureHead+hmInlineLitNoArm)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1 — an inline switch must be attributed to the literal alone, not also to the FuncDecl around it", len(sites), sites)
		}
		if sites[0].name != hmLiteralName {
			t.Fatalf("site name = %q, want %q — attributing the mapping to MeHandler would make the enclosing func answer for a literal it merely returns", sites[0].name, hmLiteralName)
		}
		if sites[0].p1 || !sites[0].p2 {
			t.Fatalf("site = %+v, want P2-only — without this the two tenancy sites are invisible", sites[0])
		}
		if sites[0].armed {
			t.Fatal("the scan reports an unarmed inline switch as armed")
		}
	})

	t.Run("C3 corrected fixture still counts 2 sites", func(t *testing.T) {
		sites := hmFixtureSites(t, "c3.go", hmFixtureHead+hmNamedMapperArmed+"\n"+hmInlineLitArmed)
		if len(sites) != 2 {
			t.Fatalf("found %d site(s) %v in the CORRECTED fixture, want 2 — the floors below cannot tell an added arm from a deleted mapper", len(sites), sites)
		}
		for _, s := range sites {
			if !s.armed {
				t.Errorf("%s reads as unarmed in the CORRECTED fixture — the scan cannot recognise a fix", s)
			}
		}
	})

	t.Run("C4 a producer is not a site", func(t *testing.T) {
		sites := hmFixtureSites(t, "c4.go", hmFixtureHead+hmProducer)
		if len(sites) != 0 {
			t.Fatalf("found %d site(s) %v for a func that RETURNS db.ErrNoTenant — the predicate has slipped from a call to a reference, which yields 17 sites and invites a three-entry allowlist", len(sites), sites)
		}
	})

	t.Run("C5 a comment is not an arm", func(t *testing.T) {
		sites := hmFixtureSites(t, "c5.go", hmFixtureHead+hmCommentOnlyArm)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want 1", len(sites), sites)
		}
		if sites[0].armed {
			t.Fatal("a mapper whose only mention of the sentinel is in a comment reads as armed — the scan is matching text, not AST")
		}
	})
}

func TestHandlerMappingEveryRefusalSiteNamesNotActiveMember(t *testing.T) {
	// First, so the scan is proved able to see both outcomes even on the runs
	// where every real site is still unarmed.
	t.Run("control needles", hmControlNeedles)

	root := repoRootDir(t)
	files := hmGoFiles(t, root, []string{"internal"})
	if len(files) < 120 {
		t.Fatalf("the walk found %d non-test .go file(s) under internal/, want at least 120 (130 measured at AUDIT-10-03) — a clean report over a broken walk means nothing", len(files))
	}

	fset := token.NewFileSet()
	var sites []hmSite
	for _, rel := range files {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v — the scan cannot report on a file it cannot parse", rel, err)
		}
		sites = append(sites, hmSitesIn(fset, rel, f)...)
	}

	var p1, p2, p2Only int
	pkgs := map[string]bool{}
	for _, s := range sites {
		if s.p1 {
			p1++
		}
		if s.p2 {
			p2++
		}
		if s.p2 && !s.p1 {
			p2Only++
		}
		pkgs[filepath.Dir(s.file)] = true
	}

	if p1 < 12 {
		t.Fatalf("P1 (statusForErr / *StatusForErr) = %d, want at least 12 measured", p1)
	}
	if p2 < 12 {
		t.Fatalf("P2 (calls errors.Is(_, db.ErrNoTenant)) = %d, want at least 12 measured", p2)
	}
	if p2Only < 2 {
		t.Fatalf("P2-only = %d, want at least 2 — tenancy's MeHandler and MembershipsHandler map inline, and a name-shaped population alone sees neither", p2Only)
	}
	if len(sites) < 14 {
		t.Fatalf("P1 union P2 = %d site(s), want at least the 14 measured", len(sites))
	}
	if len(pkgs) < 10 {
		t.Fatalf("sites span %d package(s) %v, want at least the 10 measured", len(pkgs), sortedKeys(pkgs))
	}

	for _, s := range sites {
		if !s.armed {
			t.Errorf("%s maps a refusal but never names db.ErrNotActiveMember — a suspended caller gets this handler's default, which is a 401 or a 500, not the 403 Core AC 2 requires", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan 2 — the wire message is written down exactly once
// ---------------------------------------------------------------------------

// hmMessageDeclSite is the one file allowed to carry the literal.
const hmMessageDeclSite = "internal/platform/db/tenant.go"

// hmCountMessageLiterals counts string literals in src whose value is msg.
func hmCountMessageLiterals(t *testing.T, name, src, msg string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v — the scan cannot report on a file it cannot parse", name, err)
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil && v == msg {
			n++
		}
		return true
	})
	return n
}

func TestHandlerMappingMessageIsNeverRetyped(t *testing.T) {
	// The value is pinned here and nowhere else in Go. Changing it fails in one
	// place and forces AUDIT-10-07's TypeScript mirror to move with it.
	const wantMessage = "your membership in this workspace is not active"
	if db.NotActiveMemberMessage != wantMessage {
		t.Fatalf("db.NotActiveMemberMessage = %q, want %q — update the TypeScript mirror in the same commit", db.NotActiveMemberMessage, wantMessage)
	}

	// N2: a fixture that re-types the literal. Without it, a detector that found
	// nothing anywhere would be indistinguishable from a clean repo.
	retyped := hmFixtureHead + "func f() string { return " + strconv.Quote(db.NotActiveMemberMessage) + " }\n"
	if n := hmCountMessageLiterals(t, "n2.go", retyped, db.NotActiveMemberMessage); n != 1 {
		t.Fatalf("the detector counted %d literal(s) in a fixture that re-types the message exactly once — it cannot see what it looks for", n)
	}

	root := repoRootDir(t)
	files := hmGoFiles(t, root, []string{"internal", "cmd", "tools"})
	if len(files) < 135 {
		t.Fatalf("the walk found %d non-test .go file(s) under internal/, cmd/ and tools/, want at least 135 (147 measured at AUDIT-10-03)", len(files))
	}

	hits := map[string]int{}
	total := 0
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if n := hmCountMessageLiterals(t, rel, string(raw), db.NotActiveMemberMessage); n > 0 {
			hits[rel] = n
			total += n
		}
	}

	// N1: the constant's own declaration is the planted needle. Zero is what a
	// broken walk and a clean repo both look like, so zero is fatal.
	if total == 0 {
		t.Fatalf("the message literal occurs nowhere under internal/, cmd/ or tools/ — %s no longer declares it, or the walk is broken; either way every check here would pass having read nothing", hmMessageDeclSite)
	}

	if total != 1 || hits[hmMessageDeclSite] != 1 {
		t.Errorf("the message literal occurs %d time(s) %v, want exactly once at %s — every site must source it from db.NotActiveMemberMessage so a reword cannot leave one handler behind", total, hits, hmMessageDeclSite)
	}
}
