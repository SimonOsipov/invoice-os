// Three source scans that make AUDIT-10's read-path coverage STRUCTURAL rather
// than asserted. Core AC 6 wants every read endpoint covered or individually
// exempted with a stated reason, and wants that kept true mechanically.
//
// The naive oracle — "prove each route reaches the gated seam" — is a call-graph
// question this repo cannot answer cheaply: routes are registered as higher-order
// constructions (invoice.ListHandler(store.List, store.RowFacts, app.Logger)), so
// the func named at the route is not the func that touches the database. Answering
// it needs real type information (golang.org/x/tools, absent from go.mod) and a
// large instrument whose false-negative mode is silent.
//
// So these scans ask the complementary question, which IS locally decidable: can
// HTTP-serving code reach the database WITHOUT the gate? Scan 1's invariant is
// "no database HANDLE is acquired outside the allowlist" — a pool method, a bare
// connection, a constructed pool and a database/sql handle all count, because a
// monopoly with one named DSN hole left open is not a monopoly. Scan 2 says the
// only HTTP-path caller of the identity-free core is the one deliberate
// exemption. Together they make the gated seam a monopoly, so every route that
// touches the database is gated BY CONSTRUCTION. Scan 3 keeps the written
// enumeration complete. Core AC 6 = 1 + 2 + 3.
//
// All three assert an ABSENCE, which is the instrument class that reports
// all-clear while examining nothing. So each carries planted control needles and
// a population floor naming its measured baseline, and each allowlist is matched
// EXACTLY: an entry that matches nothing fails, so a stale exemption cannot sit
// there forever.
//
// AST, never text. Scan 1 resolves pool-typed identifiers by TYPE SPELLING, not by
// name: a name-shaped predicate misses r.ReaderPool.Query (case) and over-matches
// r.URL.Query() (7 non-test occurrences, all net/url). Scan 2 counts CALLS: a text
// scan for WithinTenantTx returns ~34 hits of which only 14 are calls, and would
// demand an exemption for every doc comment naming the seam.
//
// Names carry the TestRLS_ prefix deliberately — ci.yml runs this package under
// -run TestRLS, and that filter is what makes them run in CI at all
// (TestCIRunFiltersReachEveryTestInThePackage). No scan touches a database.
//
// Known limits. Scan 1 cannot follow a pool passed as any or through an
// interface, and knows only the four handle packages named below — a fifth driver
// would need adding. Scan 2 sees a direct db.WithinTenantTx selector call, not one
// reached through a func value. Scan 3 sees only routes registered on app.Mux in
// the walked roots, and resolves a non-literal route argument only when it is a
// string const declared in the same directory.
//
// Scans 1 and 2 walk internal/ and cmd/; scan 3 walks cmd/ plus the one
// internal/ file that registers a route for every service. Nothing outside those
// two trees is walked. Both scans attribute a site to its innermost enclosing
// func, so an exemption can name a func and not a whole file, and both cross-check
// the attributed count against the file's raw count so a site outside every func
// fails rather than vanishing.
//
// All three walk non-test .go files only: a fixture reaching a pool directly is
// not a production hole, and admitting them would accrete exemptions that blur
// the signal.
package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	scPgxpoolPath = "github.com/jackc/pgx/v5/pgxpool"
	scPgxPath     = "github.com/jackc/pgx/v5"
	// scDocPath is the contract doc scan 3 reads.
	scDocPath = "docs/read-path-suspension.md"
	// scSelfPath is this file, which the reason check reads back.
	scSelfPath = "internal/platform/db/seam_coverage_test.go"
	// scMinReasonRunes is the shortest text that can carry a reason.
	scMinReasonRunes = 20
)

// scSeamRoots are the roots scans 1 and 2 walk. cmd/ holds no direct pool use
// today; walking it is what keeps that a guarded property rather than a claim.
var scSeamRoots = []string{"cmd", "internal"}

// scPoolMethods are the pgxpool.Pool methods that reach the database.
var scPoolMethods = map[string]bool{
	"Begin": true, "BeginTx": true, "Query": true, "QueryRow": true,
	"Exec": true, "Acquire": true, "SendBatch": true, "CopyFrom": true,
}

// scHandleFuncs are the package-level funcs that hand back a database handle off
// a DSN, keyed by import path so an aliased import cannot hide one. Without them
// the monopoly has two holes: pgx.Connect bypasses every pool, and a pool built
// by pgxpool.New into a short-var local carries no declared type, so the pool
// method called on it is invisible to the type-spelling pass. Construction IS the
// acquisition, and that is where it must be caught.
var scHandleFuncs = []struct {
	path, def string
	funcs     map[string]bool
}{
	{scPgxPath, "pgx", map[string]bool{"Connect": true, "ConnectConfig": true}},
	{scPgxpoolPath, "pgxpool", map[string]bool{"New": true, "NewWithConfig": true}},
	{"github.com/jackc/pgx/v5/pgconn", "pgconn", map[string]bool{"Connect": true, "ConnectConfig": true}},
	{"database/sql", "sql", map[string]bool{"Open": true, "OpenDB": true}},
}

// scImportAlias returns the name path is bound to in f, or "" when f does not
// import it. Resolving the alias keeps an aliased import from silently leaving a
// site out of the population.
func scImportAlias(f *ast.File, path, def string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return def
	}
	return ""
}

// scBaseName is the identifier a receiver resolves to: the ident itself, or a
// selector's final segment. Anything else answers "".
func scBaseName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

// scSeamFiles is the corpus scans 1 and 2 share. It floors the whole walk AND
// the cmd/ half separately: internal/ alone measured 130 files, so a total-only
// floor would still pass if cmd/ were dropped from the roots again.
func scSeamFiles(t *testing.T, root string) []string {
	t.Helper()
	files := hmGoFiles(t, root, scSeamRoots)
	cmdFiles := 0
	for _, rel := range files {
		if strings.HasPrefix(rel, "cmd/") {
			cmdFiles++
		}
	}
	if len(files) < 130 || cmdFiles < 8 {
		t.Fatalf("the walk found %d non-test .go file(s) under %v, %d of them under cmd/, want at least 130 and at least 8 (139 and 9 measured at AUDIT-10-04) — a clean report over a broken walk means nothing", len(files), scSeamRoots, cmdFiles)
	}
	return files
}

// scParse parses one repo file, failing loudly: a file the scan cannot read is a
// file it silently reports clean on.
func scParse(t *testing.T, fset *token.FileSet, root, rel string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v — the scan cannot report on a file it cannot parse", rel, err)
	}
	return f
}

// ---------------------------------------------------------------------------
// Scan 1 — no HTTP-serving code reaches a pool directly
// ---------------------------------------------------------------------------

// scPoolExemption is one exemption from the pool monopoly. fn narrows it to a
// single func; a bare file exempts the whole file. There is no package scope:
// this scan's whole claim is that no HTTP-serving file reaches a handle, and a
// package-wide hole is not something it should be able to express.
type scPoolExemption struct {
	file string
	fn   string
}

func (e scPoolExemption) String() string {
	if e.fn != "" {
		return e.file + " func " + e.fn
	}
	return e.file
}

func (e scPoolExemption) covers(s scPoolSite) bool {
	if e.fn != "" {
		return s.file == e.file && s.fn == e.fn
	}
	return s.file == e.file
}

// scPoolAllowlist is every caller allowed to reach the database without the seam.
var scPoolAllowlist = []scPoolExemption{
	{file: "internal/platform/db/db.go"},                                  // declares the identity-free core; its pool.BeginTx IS what every other caller wraps
	{file: "internal/platform/db/tenant.go"},                              // declares the gated seam; its pool.BeginTx IS the gate this story shipped
	{file: "internal/platform/db/migrate.go"},                             // goose needs a database/sql handle, which no pgx pool can supply; it runs at boot on the migrator role
	{file: "internal/platform/db/bootstrap.go"},                           // boot-time role and password provisioning on a superuser connection, before any request or tenant exists
	{file: "internal/platform/db/provision.go"},                           // boot-time readiness probe on the same pre-request phase; it waits for Postgres to speak the wire
	{file: "internal/validation/store.go", fn: "LoadActiveRuleSetGlobal"}, // the S2S peer path, which has no caller identity at all to gate on; func-scoped because internal/validation serves HTTP and its file-mates are gated
	{file: "internal/importer/backfill.go"},                               // operator CLI tools/backfill-source-rows; it carries a job tenant and never a request identity
	{file: "internal/invoice/revalidate.go"},                              // operator CLI tools/revalidate-invoices; same shape, same absence of a caller
	{file: "internal/reconciliation/sweep.go"},                            // enumerateTenants reads tenants as invoice_tenant_reader with no GUC set, which a tenant-scoped tx cannot express
}

// scPoolSite is one direct pool call: recv is the pool-typed name it was made on,
// fn the INNERMOST enclosing func so a func-scoped exemption cannot cover a
// literal the exempted func merely returns.
type scPoolSite struct {
	file   string
	line   int
	recv   string
	method string
	fn     string
}

func (s scPoolSite) String() string {
	return s.file + ":" + strconv.Itoa(s.line) + " (" + s.recv + "." + s.method + " in " + s.fn + ")"
}

// scIsPoolType reports whether e is spelled *alias.Pool.
func scIsPoolType(e ast.Expr, alias string) bool {
	if alias == "" {
		return false
	}
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Pool" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == alias
}

// scPoolNamesIn adds every identifier f declares as *pgxpool.Pool to names, and
// records each short-var alias as a (lhs, rhs) pair for the fixpoint below.
func scPoolNamesIn(f *ast.File, names map[string]bool, aliases *[][2]string) {
	alias := scImportAlias(f, scPgxpoolPath, "pgxpool")
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field: // struct fields, params, results
			if scIsPoolType(x.Type, alias) {
				for _, id := range x.Names {
					names[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if scIsPoolType(x.Type, alias) {
				for _, id := range x.Names {
					names[id.Name] = true
				}
			}
		case *ast.AssignStmt:
			if x.Tok != token.DEFINE || len(x.Lhs) != len(x.Rhs) {
				return true
			}
			for i, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if rhs := scBaseName(x.Rhs[i]); rhs != "" {
					*aliases = append(*aliases, [2]string{id.Name, rhs})
				}
			}
		}
		return true
	})
}

// scPoolNameFixpoint follows p := r.AppPool until nothing new is added.
func scPoolNameFixpoint(names map[string]bool, aliases [][2]string) {
	for changed := true; changed; {
		changed = false
		for _, pair := range aliases {
			if names[pair[1]] && !names[pair[0]] {
				names[pair[0]] = true
				changed = true
			}
		}
	}
}

// scPoolHandles resolves each handle package's alias once, so an aliased import
// is still seen.
func scPoolHandles(f *ast.File) map[string]map[string]bool {
	handles := map[string]map[string]bool{}
	for _, h := range scHandleFuncs {
		if alias := scImportAlias(f, h.path, h.def); alias != "" {
			handles[alias] = h.funcs
		}
	}
	return handles
}

// scPoolCallParts reports whether call reaches the database without the seam: a
// pool method called on a pool-typed name, or a connection opened straight off a
// DSN.
func scPoolCallParts(call *ast.CallExpr, names map[string]bool, handles map[string]map[string]bool) (recv, method string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	recv = scBaseName(sel.X)
	switch {
	case scPoolMethods[sel.Sel.Name] && recv != "" && names[recv]:
	case handles[recv][sel.Sel.Name]:
	default:
		return "", "", false
	}
	return recv, sel.Sel.Name, true
}

// scOwnBodyPoolCalls returns the acquisitions in body, EXCLUDING nested func
// literals: a literal is its own site.
func scOwnBodyPoolCalls(body *ast.BlockStmt, names map[string]bool, handles map[string]map[string]bool) []*ast.CallExpr {
	if body == nil {
		return nil
	}
	var out []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if _, _, isSite := scPoolCallParts(call, names, handles); isSite {
				out = append(out, call)
			}
		}
		return true
	})
	return out
}

// scPoolSitesIn returns the attributed sites and the file's unattributed total.
// The two must agree; an acquisition outside every func would otherwise vanish
// the moment attribution replaced the flat walk.
func scPoolSitesIn(fset *token.FileSet, rel string, f *ast.File, names map[string]bool) (sites []scPoolSite, total int) {
	handles := scPoolHandles(f)
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		var fn string
		switch x := n.(type) {
		case *ast.FuncDecl:
			body, fn = x.Body, x.Name.Name
		case *ast.FuncLit:
			body, fn = x.Body, hmLiteralName
		default:
			return true
		}
		for _, call := range scOwnBodyPoolCalls(body, names, handles) {
			recv, method, _ := scPoolCallParts(call, names, handles)
			sites = append(sites, scPoolSite{
				file:   rel,
				line:   fset.Position(call.Pos()).Line,
				recv:   recv,
				method: method,
				fn:     fn,
			})
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if _, _, isSite := scPoolCallParts(call, names, handles); isSite {
				total++
			}
		}
		return true
	})
	return sites, total
}

// scFixturePoolSites runs the whole scan over one in-test source string. Control
// needles are parsed as strings, never read from the repo, so they cannot drift
// with it.
func scFixturePoolSites(t *testing.T, name, src string) []scPoolSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	names := map[string]bool{}
	var aliases [][2]string
	scPoolNamesIn(f, names, &aliases)
	scPoolNameFixpoint(names, aliases)
	sites, total := scPoolSitesIn(fset, name, f, names)
	if len(sites) != total {
		t.Fatalf("fixture %s: attributed %d of %d acquisition(s) — a site outside every func escaped attribution", name, len(sites), total)
	}
	return sites
}

// scNeedleReaderPoolAndURL pins both real bugs at once: a case-sensitive `pool.`
// text match misses r.ReaderPool.Query, and a name-shaped match takes
// r.URL.Query() for a database call.
const scNeedleReaderPoolAndURL = `package x

import (
	"context"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

type R struct {
	ReaderPool *pgxpool.Pool
	URL        *url.URL
}

func (r *R) f(ctx context.Context) {
	_, _ = r.ReaderPool.Query(ctx, "SELECT id FROM tenants")
	_ = r.URL.Query()
}
`

// scNeedleBareParam is the shape backfill.go and revalidate.go use.
const scNeedleBareParam = `package x

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func f(ctx context.Context, pool *pgxpool.Pool) {
	_ = pool.QueryRow(ctx, "SELECT 1")
}
`

// scNeedleNonDBMethod is a pool-typed field whose method is not in the set.
const scNeedleNonDBMethod = `package x

import "github.com/jackc/pgx/v5/pgxpool"

type S struct{ pool *pgxpool.Pool }

func (s *S) f() { s.pool.Close() }
`

// scNeedleAliasedLocal is the fixpoint: a local carrying no type of its own.
const scNeedleAliasedLocal = `package x

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type S struct{ AppPool *pgxpool.Pool }

func (s *S) f(ctx context.Context) {
	p := s.AppPool
	_ = p.Exec(ctx, "SELECT 1")
}
`

// scNeedleBareConn is every way to acquire a handle off a DSN with no pool in
// sight. pgxpool.New is a site because the local it lands in carries no declared
// type, so the pool method called on it would otherwise be invisible.
const scNeedleBareConn = `package x

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func f(ctx context.Context, dsn string) {
	conn, _ := pgx.Connect(ctx, dsn)
	_ = conn
	p, _ := pgxpool.New(ctx, dsn)
	_, _ = p.Query(ctx, "SELECT 1")
	d, _ := sql.Open("pgx", dsn)
	_ = d
}
`

// scNeedleAliasedImport is the same acquisition behind a renamed import.
const scNeedleAliasedImport = `package x

import (
	"context"

	pp "github.com/jackc/pgx/v5/pgxpool"
)

func f(ctx context.Context, dsn string) {
	p, _ := pp.New(ctx, dsn)
	_ = p
}
`

// scNeedlePoolLiteral is the attribution rule a func-scoped exemption rests on:
// an acquisition inside an inline literal belongs to the literal.
const scNeedlePoolLiteral = `package x

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type S struct{ AppPool *pgxpool.Pool }

func (s *S) Outer(ctx context.Context) func() error {
	return func() error {
		_, err := s.AppPool.Exec(ctx, "SELECT 1")
		return err
	}
}
`

func scPoolControlNeedles(t *testing.T) {
	t.Run("N1 ReaderPool is a site and URL is not", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n1.go", scNeedleReaderPoolAndURL)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1 — a case-sensitive text match misses r.ReaderPool.Query, and a name-shaped one counts r.URL.Query() as a database call", len(sites), sites)
		}
		if sites[0].recv != "ReaderPool" || sites[0].method != "Query" {
			t.Fatalf("site = %+v, want ReaderPool.Query", sites[0])
		}
	})

	t.Run("N2 a bare pool parameter is a site", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n2.go", scNeedleBareParam)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1 — the two operator CLIs take the pool as a plain parameter", len(sites), sites)
		}
	})

	t.Run("N3 a non-database method is not a site", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n3.go", scNeedleNonDBMethod)
		if len(sites) != 0 {
			t.Fatalf("found %d site(s) %v for pool.Close() — the method set has slipped, and every allowlist below is answering a different question", len(sites), sites)
		}
	})

	t.Run("N4 an aliased local is a site", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n4.go", scNeedleAliasedLocal)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1 — p := s.AppPool must carry the pool's type forward or the scan is one assignment from blind", len(sites), sites)
		}
		if sites[0].recv != "p" {
			t.Fatalf("site = %+v, want the aliased local p", sites[0])
		}
	})

	t.Run("N5 every DSN entry point is a site", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n5.go", scNeedleBareConn)
		got := map[string]bool{}
		for _, s := range sites {
			got[s.recv+"."+s.method] = true
		}
		for _, want := range []string{"pgx.Connect", "pgxpool.New", "sql.Open"} {
			if !got[want] {
				t.Errorf("%s is not a site %v — a monopoly with one named DSN hole left open is not a monopoly", want, sites)
			}
		}
		// The reason construction must be a site: p carries no declared type, so
		// the type-spelling pass never learns it is a pool and the Query on it is
		// invisible. Catching pgxpool.New is what closes that.
		if got["p.Query"] {
			t.Errorf("p.Query reads as a site %v — if the type-spelling pass has started resolving short-var locals, say so here; until then pgxpool.New is the only thing standing between this shape and an unseen read", sites)
		}
	})

	t.Run("N6 an aliased import cannot hide an acquisition", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n6.go", scNeedleAliasedImport)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v for pp.New behind a renamed import, want 1 — resolving by import path is what keeps a rename from silently emptying the population", len(sites), sites)
		}
	})

	t.Run("N7 an acquisition inside a literal is the literal's", func(t *testing.T) {
		sites := scFixturePoolSites(t, "n7.go", scNeedlePoolLiteral)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1", len(sites), sites)
		}
		if sites[0].fn != hmLiteralName {
			t.Fatalf("site = %+v, want %q — attributing it to Outer would let a func-scoped exemption cover a literal it merely returns", sites[0], hmLiteralName)
		}
	})
}

func TestRLS_NoDirectPoolUseOutsideTheSeam(t *testing.T) {
	// First, so the scan is proved able to see both outcomes even on the runs
	// where the repo is clean.
	t.Run("control needles", scPoolControlNeedles)

	root := repoRootDir(t)
	files := scSeamFiles(t, root)

	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	names := map[string]bool{}
	var aliases [][2]string
	for _, rel := range files {
		f := scParse(t, fset, root, rel)
		parsed = append(parsed, f)
		scPoolNamesIn(f, names, &aliases)
	}
	scPoolNameFixpoint(names, aliases)
	if len(names) < 4 {
		t.Fatalf("the scan resolved %d pool-typed name(s) %v, want at least 4 (pool, Pool, AppPool, ReaderPool measured at AUDIT-10-04) — resolution by type spelling is what makes this scan self-widening", len(names), sortedKeys(names))
	}

	var sites []scPoolSite
	total := 0
	for i, rel := range files {
		s, n := scPoolSitesIn(fset, rel, parsed[i], names)
		sites = append(sites, s...)
		total += n
	}
	if len(sites) != total {
		t.Fatalf("attributed %d acquisition(s) of %d found — one sits outside every func, and an exemption cannot be scoped to a func that does not exist", len(sites), total)
	}

	byFile := map[string][]scPoolSite{}
	for _, s := range sites {
		byFile[s.file] = append(byFile[s.file], s)
	}
	if len(sites) < 9 || len(byFile) < 8 {
		t.Fatalf("found %d ungated database handle(s) across %d file(s) %v, want at least 9 across at least 8 (10 across 9 measured at AUDIT-10-04) — zero is what a broken scan and a clean repo both look like", len(sites), len(byFile), sortedKeys(byFile))
	}

	for _, s := range sites {
		covered := false
		for _, e := range scPoolAllowlist {
			if e.covers(s) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s reaches the database without the seam — if this func can serve HTTP, the read-path gate does not apply to it; exempt it with its reason only if it cannot serve a request", s)
		}
	}
	// Anti-rot: every exemption must still be earning its place.
	for _, e := range scPoolAllowlist {
		used := false
		for _, s := range sites {
			if e.covers(s) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("allowlist entry %q matched nothing — delete it; a stale exemption is an open door nobody is looking at", e)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan 2 — the identity-free core is worker, CLI and one exemption only
// ---------------------------------------------------------------------------

// scCoreExemption is one exemption from the ungated core. Exactly one of pkg,
// file or (file, fn) is set: package scope is only honest where the WHOLE package
// is off the HTTP path.
type scCoreExemption struct {
	pkg  string
	file string
	fn   string
}

func (e scCoreExemption) String() string {
	switch {
	case e.pkg != "":
		return "package " + e.pkg
	case e.fn != "":
		return e.file + " func " + e.fn
	default:
		return e.file
	}
}

func (e scCoreExemption) covers(s scCoreSite) bool {
	switch {
	case e.pkg != "":
		return filepath.Dir(s.file) == e.pkg
	case e.fn != "":
		return s.file == e.file && s.fn == e.fn
	default:
		return s.file == e.file
	}
}

// scCoreAllowlist is every caller allowed to reach the identity-free core.
var scCoreAllowlist = []scCoreExemption{
	{pkg: "internal/submission"},                  // River job workers; the job row carries its tenant and there is no request identity to gate
	{pkg: "internal/reconciliation"},              // the sweep worker, same shape: a schedule opened it, not a caller
	{pkg: "internal/demodocs"},                    // boot-time document seeder; it runs to completion before the first request is served
	{pkg: "internal/demopolicy"},                  // boot-time approval-policy seeder, on the same pre-request boot phase
	{file: "internal/importer/backfill.go"},       // operator CLI only, and internal/importer DOES serve HTTP, so a package exemption would un-gate the import handlers
	{file: "internal/invoice/revalidate.go"},      // operator CLI only, and internal/invoice is the largest HTTP-serving package in the tree
	{file: "internal/tenancy/store.go", fn: "Me"}, // the one deliberate HTTP-path exemption; func-scoped because ListMemberships and SetMembershipStatus share this file and ARE gated
}

// scCoreSite is one call of the ungated core, attributed to the INNERMOST
// enclosing func so a call inside a returned handler literal belongs to the
// literal, not to the FuncDecl around it.
type scCoreSite struct {
	file string
	line int
	fn   string
}

func (s scCoreSite) String() string {
	return s.file + ":" + strconv.Itoa(s.line) + " (" + s.fn + ")"
}

// scIsUngatedCore reports whether call is alias.WithinTenantTx / ...Opts.
func scIsUngatedCore(call *ast.CallExpr, alias string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "WithinTenantTx" && sel.Sel.Name != "WithinTenantTxOpts" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == alias
}

// scOwnBodyCoreCalls returns the ungated-core calls in body, EXCLUDING nested
// func literals: a literal is its own site.
func scOwnBodyCoreCalls(body *ast.BlockStmt, alias string) []token.Pos {
	if body == nil {
		return nil
	}
	var out []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && scIsUngatedCore(call, alias) {
			out = append(out, call.Pos())
		}
		return true
	})
	return out
}

// scCoreSitesIn returns the attributed sites and the file's unattributed total.
// The two must agree; a call outside every func would otherwise vanish.
func scCoreSitesIn(fset *token.FileSet, rel string, f *ast.File) (sites []scCoreSite, total int) {
	alias := scImportAlias(f, hmDBImportPath, "db")
	if alias == "" {
		return nil, 0
	}
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		var name string
		switch x := n.(type) {
		case *ast.FuncDecl:
			body, name = x.Body, x.Name.Name
		case *ast.FuncLit:
			body, name = x.Body, hmLiteralName
		default:
			return true
		}
		for _, pos := range scOwnBodyCoreCalls(body, alias) {
			sites = append(sites, scCoreSite{file: rel, line: fset.Position(pos).Line, fn: name})
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && scIsUngatedCore(call, alias) {
			total++
		}
		return true
	})
	return sites, total
}

// scFixtureCoreSites runs scan 2 over one in-test source string.
func scFixtureCoreSites(t *testing.T, name, src string) ([]scCoreSite, int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return scCoreSitesIn(fset, name, f)
}

const scCoreFixtureHead = `package x

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

`

// scNeedleCoreCall is the ordinary shape: the call sits in a named func and the
// closure it takes is an argument, not the site.
const scNeedleCoreCall = `func Me(ctx context.Context, pool interface{}) error {
	return db.WithinTenantTx(ctx, nil, "t", func(tx pgx.Tx) error { return nil })
}
`

// scNeedleCoreComment is the 34-vs-14 gap made loud: a doc comment naming the
// seam is not a call.
const scNeedleCoreComment = `// f runs inside db.WithinTenantTx, which the caller opened.
// See db.WithinTenantTxOpts for the options form.
func f(tx pgx.Tx) error { return nil }
`

// scNeedleCoreLiteral is the attribution rule: a call inside an inline literal
// belongs to the literal.
const scNeedleCoreLiteral = `func Outer(ctx context.Context) func() error {
	return func() error {
		return db.WithinTenantTx(ctx, nil, "t", func(tx pgx.Tx) error { return nil })
	}
}
`

func scCoreControlNeedles(t *testing.T) {
	t.Run("N1 a call in a named func", func(t *testing.T) {
		sites, total := scFixtureCoreSites(t, "n1.go", scCoreFixtureHead+scNeedleCoreCall)
		if len(sites) != 1 || total != 1 {
			t.Fatalf("found %d attributed site(s) %v and %d total, want 1 and 1 — the scan cannot see what it looks for", len(sites), sites, total)
		}
		if sites[0].fn != "Me" {
			t.Fatalf("site = %+v, want the enclosing func Me — the closure passed as an argument is not the site", sites[0])
		}
	})

	t.Run("N2 a comment is not a call", func(t *testing.T) {
		sites, total := scFixtureCoreSites(t, "n2.go", scCoreFixtureHead+scNeedleCoreComment)
		if len(sites) != 0 || total != 0 {
			t.Fatalf("found %d site(s) %v and %d total for a file naming the seam only in comments — a text scan returns 34 hits where 14 are calls, and every doc comment would need an exemption", len(sites), sites, total)
		}
	})

	t.Run("N3 a call inside a literal is the literal's", func(t *testing.T) {
		sites, total := scFixtureCoreSites(t, "n3.go", scCoreFixtureHead+scNeedleCoreLiteral)
		if len(sites) != 1 || total != 1 {
			t.Fatalf("found %d site(s) %v and %d total, want 1 and 1", len(sites), sites, total)
		}
		if sites[0].fn != hmLiteralName {
			t.Fatalf("site = %+v, want %q — attributing it to Outer would let a func-scoped exemption cover a literal it merely returns", sites[0], hmLiteralName)
		}
	})
}

func TestRLS_UngatedCoreIsWorkerAndExemptionOnly(t *testing.T) {
	t.Run("control needles", scCoreControlNeedles)

	root := repoRootDir(t)
	files := scSeamFiles(t, root)

	fset := token.NewFileSet()
	var sites []scCoreSite
	attributed, total := 0, 0
	for _, rel := range files {
		s, n := scCoreSitesIn(fset, rel, scParse(t, fset, root, rel))
		sites = append(sites, s...)
		attributed += len(s)
		total += n
	}
	if attributed != total {
		t.Fatalf("attributed %d call(s) of %d found — a call outside every func escaped attribution, and an exemption cannot be scoped to a func that does not exist", attributed, total)
	}

	pkgs := map[string]bool{}
	for _, s := range sites {
		pkgs[filepath.Dir(s.file)] = true
	}
	if len(sites) < 12 || len(pkgs) < 6 {
		t.Fatalf("found %d ungated-core call(s) across %d package(s) %v, want at least 12 across at least 6 (14 across 7 measured at AUDIT-10-04) — zero is what a broken scan and a clean repo both look like", len(sites), len(pkgs), sortedKeys(pkgs))
	}

	for _, s := range sites {
		covered := false
		for _, e := range scCoreAllowlist {
			if e.covers(s) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s calls the identity-free db.WithinTenantTx — a request reaching it skips the membership gate entirely; use db.WithinRequestTenantTx, or exempt this func with its reason", s)
		}
	}
	// Anti-rot: every exemption must still be earning its place.
	for _, e := range scCoreAllowlist {
		used := false
		for _, s := range sites {
			if e.covers(s) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("allowlist entry %q matched nothing — delete it; a stale exemption is an open door nobody is looking at", e)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan 3 — the route table and the contract doc agree
// ---------------------------------------------------------------------------

// scRoute is one Mux registration.
type scRoute struct {
	file  string
	line  int
	route string
}

func (r scRoute) String() string {
	return r.route + " (" + r.file + ":" + strconv.Itoa(r.line) + ")"
}

var scMuxMethods = map[string]bool{"Handle": true, "HandleFunc": true}

var scBacktickRE = regexp.MustCompile("`([^`]+)`")

// scEndpointTableHeading bounds the parse. The doc carries other tables — the
// cost figures, the plan crossover — and an unbounded walk would read a
// measurement row as an unclassified endpoint.
const scEndpointTableHeading = "the endpoint table"

// scStringConstsIn adds f's file-level string consts to out.
func scStringConstsIn(f *ast.File, out map[string]string) {
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
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil {
					out[name.Name] = v
				}
			}
		}
	}
}

// scRoutesIn returns f's routes and the positions of any route argument it could
// not resolve. An unresolvable argument is reported, never skipped.
func scRoutesIn(fset *token.FileSet, rel string, f *ast.File, consts map[string]string) (routes []scRoute, unresolved []string) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !scMuxMethods[sel.Sel.Name] || scBaseName(sel.X) != "Mux" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		line := fset.Position(call.Pos()).Line
		switch a := call.Args[0].(type) {
		case *ast.BasicLit:
			if a.Kind == token.STRING {
				if v, err := strconv.Unquote(a.Value); err == nil {
					routes = append(routes, scRoute{file: rel, line: line, route: v})
					return true
				}
			}
		case *ast.Ident:
			if v, ok := consts[a.Name]; ok {
				routes = append(routes, scRoute{file: rel, line: line, route: v})
				return true
			}
		}
		unresolved = append(unresolved, rel+":"+strconv.Itoa(line))
		return true
	})
	return routes, unresolved
}

// scDocRow is one endpoint-table row: the routes its FIRST cell backticks, and
// the verdict some later cell states.
type scDocRow struct {
	line    int
	routes  []string
	verdict string
}

// scVerdict reads a cell as a verdict. Exact match on the trimmed cell, never a
// substring of the row: "not covered" and "recovered" both contain the word and
// neither classifies anything.
func scVerdict(cell string) string {
	c := strings.ToLower(strings.Trim(strings.TrimSpace(cell), "`* "))
	if c == "covered" || c == "exempt" {
		return c
	}
	return ""
}

// scDocRows parses the doc's endpoint table. A row declares the routes backticked
// in its first cell, so a longer path in a neighbouring row cannot answer for a
// shorter one. found is false when the table's heading is absent.
func scDocRows(doc string) (rows []scDocRow, found bool) {
	lines := strings.Split(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") && strings.Contains(strings.ToLower(l), scEndpointTableHeading) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "#") { // any heading, including a sub-section's own table
			break
		}
		if !strings.HasPrefix(t, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		var routes []string
		for _, m := range scBacktickRE.FindAllStringSubmatch(cells[0], -1) {
			routes = append(routes, strings.TrimSpace(m[1]))
		}
		if len(routes) == 0 { // header row, separator row, or prose
			continue
		}
		verdict := ""
		for _, c := range cells[1:] {
			if v := scVerdict(c); v != "" {
				verdict = v
				break
			}
		}
		rows = append(rows, scDocRow{line: i + 1, routes: routes, verdict: verdict})
	}
	return rows, true
}

// scFixtureRoutes runs scan 3's route walk over one in-test source string.
func scFixtureRoutes(t *testing.T, name, src string) ([]scRoute, []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	consts := map[string]string{}
	scStringConstsIn(f, consts)
	return scRoutesIn(fset, name, f, consts)
}

// scNeedleRoutes carries a literal route, a const-indirected one (the gateway's
// shape, which a Mux.Handle(" grep cannot see) and one that resolves to neither.
const scNeedleRoutes = `package main

func main() {
	app.Mux.HandleFunc("GET /v1/ping", nil)
	app.Mux.Handle(routePrefix, nil)
	app.Mux.Handle(routePrefix+"x", nil)
}

const routePrefix = "/api/"
`

// scNeedleDocHead is the fixture table every doc needle is written under.
const scNeedleDocHead = "## 8. The endpoint table\n\n| route | service | verdict | reason |\n|---|---|---|---|\n"

func scRouteControlNeedles(t *testing.T) {
	t.Run("N1 a literal and a const both resolve", func(t *testing.T) {
		routes, unresolved := scFixtureRoutes(t, "n1.go", scNeedleRoutes)
		got := map[string]bool{}
		for _, r := range routes {
			got[r.route] = true
		}
		if !got["GET /v1/ping"] || !got["/api/"] {
			t.Fatalf("resolved %v, want both the literal and the const — the gateway mounts its proxy on a const, and a text grep never saw that line", routes)
		}
		if len(unresolved) != 1 {
			t.Fatalf("reported %d unresolvable argument(s) %v, want exactly 1 — a route the scan cannot read must be loud, never skipped", len(unresolved), unresolved)
		}
	})

	t.Run("N2 the table's heading bounds the parse", func(t *testing.T) {
		if _, found := scDocRows("## 6. The measured cost\n\n| shape | µs/op |\n|---|---|\n| `A` | 689 |\n"); found {
			t.Fatal("a doc with no endpoint table reads as having one — every route would then be checked against a measurement table")
		}
		rows, found := scDocRows(scNeedleDocHead + "| `GET /v1/ping` | tenancy | exempt | no DB, a liveness echo |\n")
		if !found || len(rows) != 1 {
			t.Fatalf("found=%v rows=%v, want one row under the heading", found, rows)
		}
		if rows[0].verdict != "exempt" || len(rows[0].routes) != 1 || rows[0].routes[0] != "GET /v1/ping" {
			t.Fatalf("row = %+v, want the ping route marked exempt", rows[0])
		}

		// A sub-section's own table is past the boundary: the endpoint table ends
		// at the next heading of ANY level, or §8.1's non-HTTP callers read as
		// routes nobody registers.
		rows, _ = scDocRows(scNeedleDocHead +
			"| `GET /v1/ping` | tenancy | exempt | no DB |\n\n" +
			"### 8.1 The non-HTTP callers\n\n| `internal/submission` | River jobs |\n")
		if len(rows) != 1 {
			t.Fatalf("parsed %d row(s) %v, want 1 — the parse ran past the sub-heading", len(rows), rows)
		}
	})

	t.Run("N3 a verdict is an exact cell, never a substring", func(t *testing.T) {
		for _, tc := range []struct{ name, cell string }{
			{"prose", "a liveness echo"},
			{"negated", "not covered"},
			{"not yet", "not yet covered"},
			{"embedded", "recovered"},
			{"exempted-elsewhere", "the gateway exempts it"},
		} {
			rows, _ := scDocRows(scNeedleDocHead + "| `GET /v1/ping` | tenancy | " + tc.cell + " | why |\n")
			if len(rows) != 1 {
				t.Fatalf("%s: parsed %d row(s), want 1", tc.name, len(rows))
			}
			if rows[0].verdict != "" {
				t.Errorf("%s: cell %q read as the verdict %q — a row that classifies nothing would satisfy the guard", tc.name, tc.cell, rows[0].verdict)
			}
		}
		for _, cell := range []string{"covered", "**covered**", " Exempt ", "`exempt`"} {
			rows, _ := scDocRows(scNeedleDocHead + "| `GET /v1/ping` | tenancy | " + cell + " | why |\n")
			if len(rows) != 1 || rows[0].verdict == "" {
				t.Errorf("cell %q states a verdict the scan cannot read", cell)
			}
		}
	})

	t.Run("N4 a longer path does not answer for a shorter one", func(t *testing.T) {
		rows, _ := scDocRows(scNeedleDocHead + "| `GET /v1/invoices/{id}/history` | invoice | covered | the seam |\n")
		if len(rows) != 1 {
			t.Fatalf("parsed %d row(s), want 1", len(rows))
		}
		if rows[0].routes[0] != "GET /v1/invoices/{id}/history" {
			t.Fatalf("row routes = %v — a substring match would let this row classify GET /v1/invoices/{id}, which has no row of its own", rows[0].routes)
		}
	})
}

func TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute(t *testing.T) {
	t.Run("control needles", scRouteControlNeedles)

	root := repoRootDir(t)
	// internal/platform/server.go registers GET /healthz and GET /readyz for
	// EVERY service, so a cmd/-only walk cannot see them.
	roots := append(hmGoFiles(t, root, []string{"cmd"}), "internal/platform/server.go")
	sort.Strings(roots)

	fset := token.NewFileSet()
	consts := map[string]map[string]string{}
	parsed := map[string]*ast.File{}
	for _, rel := range roots {
		f := scParse(t, fset, root, rel)
		parsed[rel] = f
		dir := filepath.Dir(rel)
		if consts[dir] == nil {
			consts[dir] = map[string]string{}
		}
		scStringConstsIn(f, consts[dir])
	}

	var routes []scRoute
	var unresolved []string
	rootsWithRoutes := 0
	for _, rel := range roots {
		r, u := scRoutesIn(fset, rel, parsed[rel], consts[filepath.Dir(rel)])
		routes = append(routes, r...)
		unresolved = append(unresolved, u...)
		if len(r) > 0 {
			rootsWithRoutes++
		}
	}
	if len(unresolved) > 0 {
		t.Fatalf("%d route argument(s) could not be resolved to a string: %v — a silently skipped route is exactly the rot this scan exists to prevent; resolve it or teach the scan its shape", len(unresolved), unresolved)
	}
	if rootsWithRoutes < 8 || len(routes) < 55 {
		t.Fatalf("found %d registration(s) across %d file(s), want at least 55 across at least 8 (63 across 9 measured at AUDIT-10-04) — a walk that reads nothing classifies nothing", len(routes), rootsWithRoutes)
	}

	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(scDocPath)))
	if err != nil {
		t.Fatalf("read %s: %v — Core AC 6 wants the enumeration written down, and this scan is what keeps it complete", scDocPath, err)
	}
	rows, found := scDocRows(string(raw))
	if !found {
		t.Fatalf("%s carries no heading naming %q — the parse is bounded by that heading, and without it this scan reads nothing", scDocPath, scEndpointTableHeading)
	}
	if len(rows) == 0 {
		t.Fatalf("%s's endpoint table declares no route — the doc parser found nothing to check against", scDocPath)
	}

	declared := map[string]scDocRow{}
	for _, row := range rows {
		for _, route := range row.routes {
			if prev, dup := declared[route]; dup {
				t.Errorf("%s:%d declares %q, already declared at line %d — two rows for one route can disagree", scDocPath, row.line, route, prev.line)
			}
			declared[route] = row
		}
	}

	registered := map[string]bool{}
	for _, r := range routes {
		if registered[r.route] {
			continue
		}
		registered[r.route] = true
		row, ok := declared[r.route]
		switch {
		case !ok:
			t.Errorf("%s is registered but has no row in %s — add `%s` to the endpoint table; Core AC 6 wants every endpoint enumerated, and a partial rollout that leaves one unclassified is the failure it names", r, scDocPath, r.route)
		case row.verdict == "":
			t.Errorf("%s:%d lists `%s` with no verdict cell reading exactly covered or exempt — an endpoint listed without a verdict is not an enumeration", scDocPath, row.line, r.route)
		}
	}
	// Anti-rot: a row for a route nobody registers any more is a stale claim.
	for route, row := range declared {
		if !registered[route] {
			t.Errorf("%s:%d classifies `%s`, which no walked root registers — delete the row or fix the path", scDocPath, row.line, route)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan 4 — every swept package builds its DB-test identities off the seeded
// member (AUDIT-12-02, D-7)
// ---------------------------------------------------------------------------
//
// The sweep (AUDIT-12 System Design §5.2) is behaviour-neutral against today's
// predicate: an active membership row is admitted exactly like no row at all,
// so ordinary `go test` cannot tell a swept package from a reverted one.
// scSweepPopulationPackages scopes both scans to the 9 packages AUDIT-12's
// blast radius actually names; scSweepUnsweptAllowlist then narrows that to
// the ones not yet swept. Each AUDIT-12 sweep subtask deletes its own
// unswept-allowlist entries, so a revert between subtasks fails on the commit
// that causes it.
//
// The match is textual for two shapes and AST-based for a third:
// `Subject: uuid.NewString()` directly, `x := uuid.NewString()` followed
// anywhere in the same func by `Subject: x` (internal/portfolio spells ~9 of
// its 41 sites the second way, AUDIT-12-02 Implementation Plan Correction 2),
// and — AUDIT-12-03's finding — a fresh uuid passed POSITIONALLY into a local
// test helper that itself builds the identity, e.g.
// `identity(ctx, tenantID, uuid.NewString())` where `identity(ctx, tenantID,
// subject string)` does `auth.Identity{Subject: subject, ...}` internally.
// Neither text pattern ever appears at that call site — "Subject:" is inside
// the helper, not the caller — so this shape needs a real value-flow answer:
// scSweepFindIdentitySinks walks every local func for one that routes a
// parameter into `Subject:` (directly, or by relaying a parameter into
// another already-found sink — a fixed-point closure, so a chain of
// wrappers is caught at any depth, not just one hop), and
// scSweepHelperCallSitesIn then flags any call into a sink fed a fresh uuid
// — inline or through a `:=`-assigned variable — at that parameter position.
// This is deliberately structural (which parameter position feeds Subject),
// not name-based (no hardcoded "identity" or "testIdentity"), so a
// differently-named helper in the next swept package is still caught.
//
// A site that legitimately needs a fresh, unmembered subject inside an
// already-swept package (the claim-changing set, AUDIT-12 System Design §5.4)
// goes on scSweepSubjectAllowlist instead, always func-scoped — never a whole
// file, matching scCoreAllowlist's `{file, fn: "Me"}` precedent — so a revert
// of any OTHER site in that file still fails (Correction 1).
//
// Known limits. Attribution is to the innermost FuncDecl only: a site inside a
// nested t.Run closure is attributed to the enclosing test func, not the
// subtest — none of today's sites need finer scoping. A variable also fed to
// its own explicit membership insert is still flagged; a later sweep subtask
// that needs that shape extends the predicate or the allowlist, not this
// comment. The helper-sink walk resolves calls by plain identifier only (a
// local func called directly, not through a struct method or an interface
// value) — every known helper in this package's population is a bare func.

// scSweepFuncLineRanges maps every FuncDecl in f to its body's 1-based
// [start,end] line range. AST supplies the boundary; the match itself is a
// plain regex test over the raw lines inside it, never a type or call check.
func scSweepFuncLineRanges(fset *token.FileSet, f *ast.File) map[string][2]int {
	out := map[string][2]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		out[fn.Name.Name] = [2]int{fset.Position(fn.Body.Pos()).Line, fset.Position(fn.Body.End()).Line}
		return true
	})
	return out
}

// scSweepSubjectSite is one place a _test.go file builds a caller identity
// from a fresh, unmembered uuid rather than the package's seeded
// memberSubject.
type scSweepSubjectSite struct {
	file string
	line int
	fn   string
	kind string // "literal" (Subject: uuid.NewString()), "var" (x := uuid.NewString() ... Subject: x), or "helper" (a fresh uuid passed positionally into a local identity-building func)
	name string // for "var" kind, the identifier assigned from uuid.NewString() -- the recognise-the-seeding rule below correlates it against a membership seed
}

func (s scSweepSubjectSite) String() string {
	return s.file + ":" + strconv.Itoa(s.line) + " (" + s.fn + ", " + s.kind + ")"
}

var scSweepLiteralRE = regexp.MustCompile(`Subject:\s*uuid\.NewString\(\)`)
var scSweepVarAssignRE = regexp.MustCompile(`(\w+)\s*:=\s*uuid\.NewString\(\)`)

func scSweepVarUseRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`Subject:\s*` + regexp.QuoteMeta(name) + `\b`)
}

// scSweepTextualSubjectSitesIn finds the two textual shapes (literal,
// var-mediated) in one parsed _test.go file. Both are self-contained inside
// one func body, so per-file matching is correct for them.
func scSweepTextualSubjectSitesIn(fset *token.FileSet, rel string, f *ast.File, lines []string) []scSweepSubjectSite {
	var out []scSweepSubjectSite
	for fn, rng := range scSweepFuncLineRanges(fset, f) {
		start, end := rng[0], rng[1]
		if start < 1 || end > len(lines) {
			continue
		}
		body := strings.Join(lines[start-1:end], "\n")
		for i := start - 1; i < end; i++ {
			line := lines[i]
			switch {
			case scSweepLiteralRE.MatchString(line):
				out = append(out, scSweepSubjectSite{file: rel, line: i + 1, fn: fn, kind: "literal"})
			case scSweepVarAssignRE.MatchString(line):
				name := scSweepVarAssignRE.FindStringSubmatch(line)[1]
				if scSweepVarUseRE(name).MatchString(body) {
					out = append(out, scSweepSubjectSite{file: rel, line: i + 1, fn: fn, kind: "var", name: name})
				}
			}
		}
	}
	return out
}

// scSweepSubjectSitesIn is the single-file convenience the needle fixtures
// below use, where the sink and its call always live in the same synthetic
// file: textual shapes plus helper-shape sinks resolved from this file
// alone. The real population scan does NOT call this — it resolves sinks
// per PACKAGE (see TestRLS_SweptPackagesBuildIdentitiesFromASeededMember),
// because a package's identity()-style helper is routinely declared in one
// _test.go file and called from its siblings.
func scSweepSubjectSitesIn(fset *token.FileSet, rel string, f *ast.File, lines []string) []scSweepSubjectSite {
	out := scSweepTextualSubjectSitesIn(fset, rel, f, lines)
	sinks := scSweepFindIdentitySinks(scSweepFuncDecls(f))
	out = append(out, scSweepHelperCallSitesInFile(fset, rel, f, lines, sinks)...)
	return out
}

// scSweepFuncDecls returns every top-level func with a body, in source order.
func scSweepFuncDecls(f *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			out = append(out, fd)
		}
	}
	return out
}

// scSweepFlattenParams returns a func's parameter names in call-site
// positional order (`func f(ctx context.Context, tenantID, subject string)`
// flattens to ["ctx", "tenantID", "subject"]) — an unnamed field contributes
// a blank slot so later indices still line up with real call args.
func scSweepFlattenParams(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, field := range fl.List {
		if len(field.Names) == 0 {
			out = append(out, "")
			continue
		}
		for _, n := range field.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// scSweepIdentitySink is one local func that routes one of its own
// parameters (at paramIdx, positional) into auth.Identity{Subject: ...} —
// directly, or by relaying that parameter into another sink.
type scSweepIdentitySink struct {
	paramIdx int
}

// scSweepFindIdentitySinks finds every local sink in one file. Base case: a
// CompositeLit key `Subject` whose value is an Ident matching one of the
// enclosing func's own parameters. Then a fixed-point pass: a func that
// calls an already-found sink, passing one of ITS OWN parameters at the
// sink's paramIdx position, becomes a sink itself — repeated until nothing
// new is found, so a chain of wrapper helpers is resolved at any depth, not
// just one hop. Resolution is by plain identifier call (`f(...)`), the only
// shape any known local test helper uses.
func scSweepFindIdentitySinks(decls []*ast.FuncDecl) map[string]scSweepIdentitySink {
	sinks := map[string]scSweepIdentitySink{}

	for _, fd := range decls {
		if _, ok := sinks[fd.Name.Name]; ok {
			continue
		}
		params := scSweepFlattenParams(fd.Type.Params)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Subject" {
					continue
				}
				id, ok := kv.Value.(*ast.Ident)
				if !ok {
					continue
				}
				for i, p := range params {
					if p != "" && p == id.Name {
						if _, exists := sinks[fd.Name.Name]; !exists {
							sinks[fd.Name.Name] = scSweepIdentitySink{paramIdx: i}
						}
					}
				}
			}
			return true
		})
	}

	for changed := true; changed; {
		changed = false
		for _, fd := range decls {
			if _, ok := sinks[fd.Name.Name]; ok {
				continue
			}
			params := scSweepFlattenParams(fd.Type.Params)
			found := -1
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if found != -1 {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				sink, ok := sinks[callee.Name]
				if !ok || sink.paramIdx >= len(call.Args) {
					return true
				}
				argID, ok := call.Args[sink.paramIdx].(*ast.Ident)
				if !ok {
					return true
				}
				for i, p := range params {
					if p != "" && p == argID.Name {
						found = i
						return false
					}
				}
				return true
			})
			if found != -1 {
				sinks[fd.Name.Name] = scSweepIdentitySink{paramIdx: found}
				changed = true
			}
		}
	}
	return sinks
}

// scSweepFreshVarNames returns every name assigned via `x := uuid.NewString()`
// anywhere in body — the same shape scSweepVarAssignRE matches, reused here to
// decide whether a variable fed into a helper call is a fresh uuid.
func scSweepFreshVarNames(body string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if m := scSweepVarAssignRE.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// scSweepIsFreshValue reports whether e is a fresh uuid: an inline
// `uuid.NewString()` call, or an Ident previously `:=`-assigned from one
// (per fresh, scoped to the enclosing func).
func scSweepIsFreshValue(e ast.Expr, fresh map[string]bool) bool {
	switch v := e.(type) {
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == "uuid" && sel.Sel.Name == "NewString"
	case *ast.Ident:
		return fresh[v.Name]
	default:
		return false
	}
}

// scSweepHelperCallSitesInFile finds every call into a local identity sink
// (scSweepFindIdentitySinks, precomputed by the caller — package-wide for
// the real scan, single-file for the needle fixtures) fed a fresh uuid at
// the parameter position that reaches Subject — the positional-helper shape
// (`identity(ctx, tenantID, uuid.NewString())`), regardless of how the fresh
// value is spelled at the call site. A sink's own body is walked like any
// other func, so a wrapper that merely relays a parameter is not flagged for
// relaying it — only the func that actually introduces the freshness is.
func scSweepHelperCallSitesInFile(fset *token.FileSet, rel string, f *ast.File, lines []string, sinks map[string]scSweepIdentitySink) []scSweepSubjectSite {
	if len(sinks) == 0 {
		return nil
	}
	var out []scSweepSubjectSite
	for _, fd := range scSweepFuncDecls(f) {
		start := fset.Position(fd.Body.Pos()).Line
		end := fset.Position(fd.Body.End()).Line
		if start < 1 || end > len(lines) {
			continue
		}
		fresh := scSweepFreshVarNames(strings.Join(lines[start-1:end], "\n"))
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			sink, ok := sinks[callee.Name]
			if !ok || sink.paramIdx >= len(call.Args) {
				return true
			}
			if scSweepIsFreshValue(call.Args[sink.paramIdx], fresh) {
				out = append(out, scSweepSubjectSite{
					file: rel,
					line: fset.Position(call.Pos()).Line,
					fn:   fd.Name.Name,
					kind: "helper",
				})
			}
			return true
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Recognise-the-seeding: co-occurrence (Tier 1) + guard-sensitivity (Tier 2)
// ---------------------------------------------------------------------------
//
// A blanket func-scoped exemption for every var-kind site that already seeds
// its own caller a membership row (internal/invoice: 47 such functions)
// would blind this scan across the package's own RBAC suites. Tier 1
// answers "does this identifier also feed a membership INSERT's user_id
// column" using the SAME sink-resolution + fixed-point closure as the
// identity-sink walk above, retargeted at a second target. Tier 2 answers
// the sharper question a co-occurrence check alone cannot: does the seed
// run on EVERY path that reaches the identity use, or only some of them
// (seedApprovalFactsFixture: seedMembership sits inside `if staffed {}`,
// `Subject: subject` is unconditional). Only "var" kind sites are eligible:
// a fresh `uuid.NewString()` literal is a different value on every
// evaluation and can never be data-flow-matched to a separately generated
// seed value.
//
// Known limits (beyond scSweepFindIdentitySinks' own, above): the guard-stack
// walk does not merge "seeded in every branch of an if/else" into
// "effectively unconditional" -- a seed split across a symmetric if/else is
// flagged as still-unsound even though every path seeds. It also matches by
// identifier NAME, not go/types object identity, so two different variables
// sharing a name in sibling closures within one function are not
// distinguished. Neither manifests in internal/invoice today.

// scSweepMembershipSink is one local func that routes one of its own
// parameters (at paramIdx, positional) into an `INSERT INTO memberships`
// statement's user_id column -- directly, or by relaying that parameter
// into another already-found membership sink.
type scSweepMembershipSink struct {
	paramIdx int
}

var scSweepMembershipInsertRE = regexp.MustCompile(`(?is)INSERT\s+INTO\s+memberships\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)`)
var scSweepPlaceholderRE = regexp.MustCompile(`^\$(\d+)$`)

// scSweepMembershipColumnBindArg parses one `INSERT INTO memberships (...)
// VALUES (...)` string and returns the 1-based bind position ($N) that
// binds the user_id column -- or false if the column has no placeholder
// there (a hardcoded literal like 'admin' for role, never observed for
// user_id in this population, but the parse must not assume it).
func scSweepMembershipColumnBindArg(sql string) (int, bool) {
	m := scSweepMembershipInsertRE.FindStringSubmatch(sql)
	if m == nil {
		return 0, false
	}
	cols := strings.Split(m[1], ",")
	vals := strings.Split(m[2], ",")
	if len(cols) != len(vals) {
		return 0, false
	}
	for i, c := range cols {
		if strings.TrimSpace(c) != "user_id" {
			continue
		}
		pm := scSweepPlaceholderRE.FindStringSubmatch(strings.TrimSpace(vals[i]))
		if pm == nil {
			return 0, false
		}
		n, err := strconv.Atoi(pm[1])
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// scSweepMembershipInsertCallBindArg finds an INSERT-into-memberships string
// literal among call's own args and returns the arg expression bound to the
// user_id column, or nil if call carries no such literal or the column has
// no placeholder.
func scSweepMembershipInsertCallBindArg(call *ast.CallExpr) ast.Expr {
	for k, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.Contains(val, "INSERT INTO memberships") {
			continue
		}
		bindN, ok := scSweepMembershipColumnBindArg(val)
		if !ok {
			continue
		}
		pos := k + bindN
		if pos >= len(call.Args) {
			continue
		}
		return call.Args[pos]
	}
	return nil
}

// scSweepFindMembershipSinks finds every local func that seeds a
// memberships row for one of its own parameters, package-wide (mirrors
// scSweepFindIdentitySinks' base-case-then-fixed-point structure exactly).
func scSweepFindMembershipSinks(decls []*ast.FuncDecl) map[string]scSweepMembershipSink {
	sinks := map[string]scSweepMembershipSink{}

	for _, fd := range decls {
		params := scSweepFlattenParams(fd.Type.Params)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if _, ok := sinks[fd.Name.Name]; ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			target := scSweepMembershipInsertCallBindArg(call)
			if target == nil {
				return true
			}
			argID, ok := target.(*ast.Ident)
			if !ok {
				return true
			}
			for i, p := range params {
				if p != "" && p == argID.Name {
					sinks[fd.Name.Name] = scSweepMembershipSink{paramIdx: i}
				}
			}
			return true
		})
	}

	for changed := true; changed; {
		changed = false
		for _, fd := range decls {
			if _, ok := sinks[fd.Name.Name]; ok {
				continue
			}
			params := scSweepFlattenParams(fd.Type.Params)
			found := -1
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if found != -1 {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				sink, ok := sinks[callee.Name]
				if !ok || sink.paramIdx >= len(call.Args) {
					return true
				}
				argID, ok := call.Args[sink.paramIdx].(*ast.Ident)
				if !ok {
					return true
				}
				for i, p := range params {
					if p != "" && p == argID.Name {
						found = i
						return false
					}
				}
				return true
			})
			if found != -1 {
				sinks[fd.Name.Name] = scSweepMembershipSink{paramIdx: found}
				changed = true
			}
		}
	}
	return sinks
}

// scSweepFindSeedCall finds the call inside body that seeds identifier name
// a membership row -- an inline INSERT-into-memberships literal fed name at
// the user_id position, or a call into a resolved membership sink fed name
// at its paramIdx. Returns nil if body seeds no such row for name (Tier 1:
// no co-occurrence at all).
func scSweepFindSeedCall(body *ast.BlockStmt, name string, sinks map[string]scSweepMembershipSink) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if target := scSweepMembershipInsertCallBindArg(call); target != nil {
			if id, ok := target.(*ast.Ident); ok && id.Name == name {
				found = call
				return false
			}
		}
		if callee, ok := call.Fun.(*ast.Ident); ok {
			if sink, ok := sinks[callee.Name]; ok && sink.paramIdx < len(call.Args) {
				if id, ok := call.Args[sink.paramIdx].(*ast.Ident); ok && id.Name == name {
					found = call
					return false
				}
			}
		}
		return true
	})
	return found
}

// scSweepFindSubjectUse finds the `Subject: name` composite-literal value
// inside body and returns that Ident node.
func scSweepFindSubjectUse(body *ast.BlockStmt, name string) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Subject" {
			return true
		}
		id, ok := kv.Value.(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		found = id
		return false
	})
	return found
}

// scSweepFuncByName returns the named top-level FuncDecl in f, or nil.
func scSweepFuncByName(f *ast.File, name string) *ast.FuncDecl {
	for _, fd := range scSweepFuncDecls(f) {
		if fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

// scSweepGuardEntry is one branch/loop/closure a position is nested inside,
// identified by its own source position so sibling branches of the same
// kind (two case clauses, an if's then vs else) are told apart.
type scSweepGuardEntry struct {
	kind string
	pos  token.Pos
}

func scSweepContainsPos(n ast.Node, pos token.Pos) bool {
	if n == nil {
		return false
	}
	return n.Pos() <= pos && pos < n.End()
}

// scSweepGuardStackTo returns the guard stack at targetPos within n --
// the ordered list of if/else/switch-case/for/range/closure entries a
// position at targetPos is nested inside. Descends only into the child
// subtree containing targetPos; an IfStmt's Init and Cond run unconditionally
// (no push), its Body pushes "then" and its Else pushes "else". Handles the
// statement/expression shapes this population's fixtures actually use;
// falls through to "at this level" for anything else, which is the correct
// terminal case once no more branch structure remains to descend into.
func scSweepGuardStackTo(n ast.Node, targetPos token.Pos, stack []scSweepGuardEntry) ([]scSweepGuardEntry, bool) {
	if !scSweepContainsPos(n, targetPos) {
		return nil, false
	}
	switch v := n.(type) {
	case *ast.IfStmt:
		if v.Init != nil && scSweepContainsPos(v.Init, targetPos) {
			return scSweepGuardStackTo(v.Init, targetPos, stack)
		}
		if v.Cond != nil && scSweepContainsPos(v.Cond, targetPos) {
			return scSweepGuardStackTo(v.Cond, targetPos, stack)
		}
		if scSweepContainsPos(v.Body, targetPos) {
			return scSweepGuardStackTo(v.Body, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"then", v.Pos()}))
		}
		if v.Else != nil && scSweepContainsPos(v.Else, targetPos) {
			return scSweepGuardStackTo(v.Else, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"else", v.Pos()}))
		}
		return stack, true
	case *ast.ForStmt:
		if scSweepContainsPos(v.Body, targetPos) {
			return scSweepGuardStackTo(v.Body, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"for", v.Pos()}))
		}
		return stack, true
	case *ast.RangeStmt:
		if scSweepContainsPos(v.Body, targetPos) {
			return scSweepGuardStackTo(v.Body, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"range", v.Pos()}))
		}
		return stack, true
	case *ast.SwitchStmt:
		for _, stmt := range v.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok || !scSweepContainsPos(cc, targetPos) {
				continue
			}
			for _, s := range cc.Body {
				if scSweepContainsPos(s, targetPos) {
					return scSweepGuardStackTo(s, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"case", cc.Pos()}))
				}
			}
		}
		return stack, true
	case *ast.BlockStmt:
		for _, stmt := range v.List {
			if scSweepContainsPos(stmt, targetPos) {
				return scSweepGuardStackTo(stmt, targetPos, stack)
			}
		}
		return stack, true
	case *ast.ExprStmt:
		return scSweepGuardStackTo(v.X, targetPos, stack)
	case *ast.AssignStmt:
		for _, e := range v.Rhs {
			if scSweepContainsPos(e, targetPos) {
				return scSweepGuardStackTo(e, targetPos, stack)
			}
		}
		return stack, true
	case *ast.ReturnStmt:
		for _, e := range v.Results {
			if scSweepContainsPos(e, targetPos) {
				return scSweepGuardStackTo(e, targetPos, stack)
			}
		}
		return stack, true
	case *ast.CallExpr:
		for _, a := range v.Args {
			if scSweepContainsPos(a, targetPos) {
				return scSweepGuardStackTo(a, targetPos, stack)
			}
		}
		return stack, true
	case *ast.FuncLit:
		if scSweepContainsPos(v.Body, targetPos) {
			return scSweepGuardStackTo(v.Body, targetPos, append(append([]scSweepGuardEntry{}, stack...), scSweepGuardEntry{"closure", v.Pos()}))
		}
		return stack, true
	case *ast.CompositeLit:
		for _, e := range v.Elts {
			if scSweepContainsPos(e, targetPos) {
				return scSweepGuardStackTo(e, targetPos, stack)
			}
		}
		return stack, true
	case *ast.KeyValueExpr:
		if scSweepContainsPos(v.Value, targetPos) {
			return scSweepGuardStackTo(v.Value, targetPos, stack)
		}
		return stack, true
	default:
		return stack, true
	}
}

// scSweepGuardStackIsPrefix reports whether seed's guard stack is a prefix of
// use's -- the seed must run on every path that reaches the use, so every
// branch guarding the seed must also guard the use.
func scSweepGuardStackIsPrefix(seed, use []scSweepGuardEntry) bool {
	if len(seed) > len(use) {
		return false
	}
	for i := range seed {
		if seed[i] != use[i] {
			return false
		}
	}
	return true
}

// scSweepRecogniseSeeding is the two-tier rule: Tier 1 requires fd to seed a
// membership row for name somewhere in its body (else there is no
// co-occurrence at all); Tier 2 requires that seed's guard stack to be a
// prefix of the `Subject: name` use's -- unconditional relative to the use,
// or in the exact same branch. seedApprovalFactsFixture fails Tier 2: its
// seed is inside `if staffed {}`, its use is unconditional.
func scSweepRecogniseSeeding(fd *ast.FuncDecl, name string, sinks map[string]scSweepMembershipSink) bool {
	seedCall := scSweepFindSeedCall(fd.Body, name, sinks)
	if seedCall == nil {
		return false
	}
	useIdent := scSweepFindSubjectUse(fd.Body, name)
	if useIdent == nil {
		return false
	}
	seedStack, ok := scSweepGuardStackTo(fd.Body, seedCall.Pos(), nil)
	if !ok {
		return false
	}
	useStack, ok := scSweepGuardStackTo(fd.Body, useIdent.Pos(), nil)
	if !ok {
		return false
	}
	return scSweepGuardStackIsPrefix(seedStack, useStack)
}

func scSweepFixtureSubjectSites(t *testing.T, name, src string) []scSweepSubjectSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return scSweepSubjectSitesIn(fset, name, f, strings.Split(src, "\n"))
}

const scSweepNeedleLiteral = `package x

func TestN1(t *testing.T) {
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated"})
	_ = c
}
`

const scSweepNeedleVar = `package x

func TestN2(t *testing.T) {
	userID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleClean is the seeded shape the sweep produces: no site.
const scSweepNeedleClean = `package x

func TestN3(t *testing.T) {
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleTenantOnly proves the second hop is required: a fresh uuid
// never read back as Subject is a tenant id, not a caller identity, and
// flagging it would demand an exemption for every seedTenant call.
const scSweepNeedleTenantOnly = `package x

func TestN4(t *testing.T) {
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, TenantID: tenantID})
	_ = c
}
`

// scSweepNeedleHelper is the positional-helper shape (AUDIT-12-03
// Implementation Plan §2, internal/document's identity()): the fresh uuid is
// an inline call, never text-adjacent to "Subject:".
const scSweepNeedleHelper = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}

func TestN5(t *testing.T) {
	c := identity(ctx, tenantID, uuid.NewString())
	_ = c
}
`

// scSweepNeedleHelperVar is the positional-helper shape spelled through a
// variable (internal/importer's handlers_adversarial_test.go: `subject :=
// uuid.NewString()` then passed as identity()'s 3rd arg, never `Subject:
// subject`).
const scSweepNeedleHelperVar = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}

func TestN6(t *testing.T) {
	subject := uuid.NewString()
	c := identity(ctx, tenantID, subject)
	_ = c
}
`

// scSweepNeedleHelperClean proves a swept call into the same helper is not a
// site: memberSubject is not a fresh uuid, however it is spelled.
const scSweepNeedleHelperClean = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}

func TestN7(t *testing.T) {
	c := identity(ctx, tenantID, memberSubject)
	_ = c
}
`

// scSweepNeedleHelperTenantParam proves the sink is positional, not "any
// fresh uuid anywhere in the call": a fresh uuid at the TENANT parameter,
// with a swept subject, is not a site.
const scSweepNeedleHelperTenantParam = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}

func TestN8(t *testing.T) {
	c := identity(ctx, uuid.NewString(), memberSubject)
	_ = c
}
`

// scSweepNeedleHelperChain proves the fixed-point resolves a two-hop wrapper
// (identityFor relays its own "actor" param into identity's sink slot, so
// identityFor becomes a sink too) — generality beyond the single hop the
// shipped corpus needs today.
const scSweepNeedleHelperChain = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}

func identityFor(ctx context.Context, tenantID, actor string) context.Context {
	return identity(ctx, tenantID, actor)
}

func TestN9(t *testing.T) {
	c := identityFor(ctx, tenantID, uuid.NewString())
	_ = c
}
`

// scSweepNeedleHelperSinkOnly and scSweepNeedleHelperCallerOnly are N10's
// pair: the sink (identity()) and its call site live in TWO SEPARATE files,
// as internal/document actually spells it (identity() in document_test.go,
// every call in its four siblings, AUDIT-12-03 Implementation Plan §2). A
// per-FILE-only sink resolution -- the first draft of this scan -- finds
// nothing scanning the caller file alone, because the sink is declared
// elsewhere; only per-PACKAGE resolution (pooling every sibling's FuncDecls
// before resolving sinks) catches it. N1-N9 are all single-file, so none of
// them would have caught a regression back to per-file resolution.
const scSweepNeedleHelperSinkOnly = `package x

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, TenantID: tenantID})
}
`

const scSweepNeedleHelperCallerOnly = `package x

func TestN10(t *testing.T) {
	c := identity(ctx, tenantID, uuid.NewString())
	_ = c
}
`

func scSweepSubjectControlNeedles(t *testing.T) {
	t.Run("N1 direct literal", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n1.go", scSweepNeedleLiteral)
		if len(sites) != 1 || sites[0].kind != "literal" || sites[0].fn != "TestN1" {
			t.Fatalf("sites = %v, want exactly one literal site in TestN1 — the scan cannot see the shape it exists to catch", sites)
		}
	})
	t.Run("N2 variable-mediated", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n2.go", scSweepNeedleVar)
		if len(sites) != 1 || sites[0].kind != "var" || sites[0].fn != "TestN2" {
			t.Fatalf("sites = %v, want exactly one var-mediated site in TestN2 — a literal-only match misses ~9 of internal/portfolio's own sites", sites)
		}
	})
	t.Run("N3 a seeded subject is not a site", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n3.go", scSweepNeedleClean)
		if len(sites) != 0 {
			t.Fatalf("sites = %v, want none — memberSubject is the seeded caller, not a fresh identity", sites)
		}
	})
	t.Run("N4 a tenant id is not a subject", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n4.go", scSweepNeedleTenantOnly)
		if len(sites) != 0 {
			t.Fatalf("sites = %v, want none — a fresh uuid never read back as Subject is a tenant id", sites)
		}
	})
	t.Run("N5 positional helper, inline literal", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n5.go", scSweepNeedleHelper)
		if len(sites) != 1 || sites[0].kind != "helper" || sites[0].fn != "TestN5" {
			t.Fatalf("sites = %v, want exactly one helper site in TestN5 — this is the shape a text-only scan cannot see (internal/document's identity())", sites)
		}
	})
	t.Run("N6 positional helper, variable-mediated", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n6.go", scSweepNeedleHelperVar)
		if len(sites) != 1 || sites[0].kind != "helper" || sites[0].fn != "TestN6" {
			t.Fatalf("sites = %v, want exactly one helper site in TestN6 — a fresh var passed positionally is still a fresh subject", sites)
		}
	})
	t.Run("N7 a swept helper call is not a site", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n7.go", scSweepNeedleHelperClean)
		if len(sites) != 0 {
			t.Fatalf("sites = %v, want none — memberSubject at the subject position is the seeded caller, not a fresh identity", sites)
		}
	})
	t.Run("N8 a fresh tenant id at the helper's tenant slot is not a subject", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n8.go", scSweepNeedleHelperTenantParam)
		if len(sites) != 0 {
			t.Fatalf("sites = %v, want none — the sink is positional; a fresh value at the TENANT parameter is not the Subject parameter", sites)
		}
	})
	t.Run("N9 a two-hop wrapper chain resolves to the origin call", func(t *testing.T) {
		sites := scSweepFixtureSubjectSites(t, "n9.go", scSweepNeedleHelperChain)
		if len(sites) != 1 || sites[0].kind != "helper" || sites[0].fn != "TestN9" {
			t.Fatalf("sites = %v, want exactly one helper site in TestN9, attributed to the caller that introduces the freshness — not to the relaying wrapper identityFor", sites)
		}
	})
	t.Run("N10 a sink declared in one file is resolved when called from a sibling", func(t *testing.T) {
		sinkFset := token.NewFileSet()
		sinkFile, err := parser.ParseFile(sinkFset, "n10_sink.go", scSweepNeedleHelperSinkOnly, 0)
		if err != nil {
			t.Fatalf("parse n10_sink.go: %v", err)
		}
		callerFset := token.NewFileSet()
		callerFile, err := parser.ParseFile(callerFset, "n10_caller.go", scSweepNeedleHelperCallerOnly, 0)
		if err != nil {
			t.Fatalf("parse n10_caller.go: %v", err)
		}
		callerLines := strings.Split(scSweepNeedleHelperCallerOnly, "\n")

		// Per-file-only resolution (the first draft): resolving sinks from the
		// caller file alone must find NOTHING, or this needle proves nothing.
		fileOnlySinks := scSweepFindIdentitySinks(scSweepFuncDecls(callerFile))
		fileOnlySites := scSweepHelperCallSitesInFile(callerFset, "n10_caller.go", callerFile, callerLines, fileOnlySinks)
		if len(fileOnlySites) != 0 {
			t.Fatalf("per-file-only sites = %v, want none — this needle is meaningless unless scanning the caller file alone (without its sibling) misses the call", fileOnlySites)
		}

		// Per-package resolution (the real population scan's approach, see
		// TestRLS_SweptPackagesBuildIdentitiesFromASeededMember): pool both
		// files' FuncDecls before resolving sinks, exactly as document's real
		// identity()/siblings split requires.
		pkgDecls := append(scSweepFuncDecls(sinkFile), scSweepFuncDecls(callerFile)...)
		pkgSinks := scSweepFindIdentitySinks(pkgDecls)
		pkgSites := scSweepHelperCallSitesInFile(callerFset, "n10_caller.go", callerFile, callerLines, pkgSinks)
		if len(pkgSites) != 1 || pkgSites[0].kind != "helper" || pkgSites[0].fn != "TestN10" {
			t.Fatalf("per-package sites = %v, want exactly one helper site in TestN10 — a sink declared in a SIBLING file must still be resolved", pkgSites)
		}
	})
}

// scSweepNeedleSeedThroughHelperClean mirrors internal/invoice's
// seedGatedTenantAsAdmin: a fresh var passed to a local seedMembership
// helper, unconditionally, then read back as Subject -- Tier 1+2 must clear
// it.
const scSweepNeedleSeedThroughHelperClean = `package x

func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	var id string
	super.QueryRow(ctx, "INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id", tenantID, userID, role).Scan(&id)
	return id
}

func TestN11(t *testing.T) {
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleSeedGuardedUnsound mirrors internal/invoice's
// seedApprovalFactsFixture exactly: the seedMembership call sits inside
// `if staffed {}`, the Subject use is unconditional. Tier 1 alone would
// clear this (the identifier co-occurs); Tier 2 must reject it.
const scSweepNeedleSeedGuardedUnsound = `package x

func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	var id string
	super.QueryRow(ctx, "INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id", tenantID, userID, role).Scan(&id)
	return id
}

func seedFixtureLike(t *testing.T, super *pgxpool.Pool, staffed bool) fixture {
	subject := uuid.NewString()
	if staffed {
		seedMembership(t, super, tenantID, subject, "admin")
	}
	return fixture{
		ctx: auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"}),
	}
}
`

// scSweepNeedleSeedDirectInsertClean mirrors internal/invoice's
// seedActiveAdminFor: the INSERT is inline (no helper), and sits in an
// `if _, err := ...; err != nil {}` idiom -- the seed call is in the
// IfStmt's Init, which runs unconditionally, not its Body. Tier 1+2 must
// clear it.
const scSweepNeedleSeedDirectInsertClean = `package x

func TestN13(t *testing.T) {
	userID := uuid.NewString()
	if _, err := super.Exec(ctx, "INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')", tenantID, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleSeedUnrelatedSubject proves Tier 1 does not over-clear: the
// function mentions INSERT INTO memberships, but seeds a DIFFERENT
// identifier than the one read back as Subject.
const scSweepNeedleSeedUnrelatedSubject = `package x

func TestN14(t *testing.T) {
	subject := uuid.NewString()
	otherUserID := uuid.NewString()
	super.Exec(ctx, "INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, otherUserID, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleCosmeticMention proves Tier 1 must not match on string
// content alone: the INSERT-into-memberships text sits inside a t.Logf
// call, never a real DB seed. KNOWN DEFECT (QA, AUDIT-12-04): the scan does
// not check the call's callee, so this clears today -- t.Fatalf below fires
// until scSweepMembershipInsertCallBindArg is restricted to an Exec/Query-
// shaped call.
const scSweepNeedleCosmeticMention = `package x

func TestN15(t *testing.T) {
	subject := uuid.NewString()
	t.Logf("would run: INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, subject, "preparer")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleHiddenConditionalWrapper proves Tier 2's guard check must
// reach into a called sink's own body: seedIfAdmin only seeds when
// role=="admin", called here with "guest" -- no row is ever inserted, yet
// the call site itself is unconditional. KNOWN DEFECT (QA, AUDIT-12-04):
// scSweepFindMembershipSinks marks a func a sink whenever it relays its
// param to another sink, regardless of what guards that relay -- t.Fatalf
// below fires until sink resolution also propagates the sink's own
// internal guard stack.
const scSweepNeedleHiddenConditionalWrapper = `package x

func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	var id string
	super.QueryRow(ctx, "INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id", tenantID, userID, role).Scan(&id)
	return id
}

func seedIfAdmin(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	if role == "admin" {
		return seedMembership(t, super, tenantID, userID, role)
	}
	return ""
}

func TestN16(t *testing.T) {
	subject := uuid.NewString()
	seedIfAdmin(t, super, tenantID, subject, "guest")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"})
	_ = c
}
`

// scSweepNeedleSeedTwoHopSinkOnly and scSweepNeedleSeedTwoHopCallerOnly are
// N17's pair: seedMembership and its two-hop relay seedViaWrapper live in one
// file, the call site in a sibling that declares neither -- proving the
// MEMBERSHIP sink walk resolves cross-file exactly like the identity sink
// walk N10 already proves for the other mechanism.
const scSweepNeedleSeedTwoHopSinkOnly = `package x

func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	var id string
	super.QueryRow(ctx, "INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id", tenantID, userID, role).Scan(&id)
	return id
}

func seedViaWrapper(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	return seedMembership(t, super, tenantID, userID, role)
}
`

const scSweepNeedleSeedTwoHopCallerOnly = `package x

func TestN17(t *testing.T) {
	subject := uuid.NewString()
	seedViaWrapper(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated"})
	_ = c
}
`

// scSweepFixtureVarSiteCleared parses src, locates its lone "var" kind
// subject site in fn, and reports whether the recognise-the-seeding rule
// clears it -- the same package-wide membership-sink resolution the real
// scan uses, scoped to this one fixture file.
func scSweepFixtureVarSiteCleared(t *testing.T, name, src, fn string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	lines := strings.Split(src, "\n")
	var site *scSweepSubjectSite
	for _, s := range scSweepTextualSubjectSitesIn(fset, name, f, lines) {
		if s.kind == "var" && s.fn == fn {
			s := s
			site = &s
		}
	}
	if site == nil {
		t.Fatalf("no var-kind site found in %s — fixture is malformed", fn)
	}
	fd := scSweepFuncByName(f, fn)
	if fd == nil {
		t.Fatalf("no FuncDecl named %s in fixture", fn)
	}
	sinks := scSweepFindMembershipSinks(scSweepFuncDecls(f))
	return scSweepRecogniseSeeding(fd, site.name, sinks)
}

func scSweepRecogniseSeedingControlNeedles(t *testing.T) {
	t.Run("N11 seed through a local helper, unconditional -- clears", func(t *testing.T) {
		if !scSweepFixtureVarSiteCleared(t, "n11.go", scSweepNeedleSeedThroughHelperClean, "TestN11") {
			t.Fatalf("TestN11 not cleared — Tier 1+2 must recognise an unconditional seedMembership call for the same identifier")
		}
	})
	t.Run("N12 seed inside if staffed{}, use unconditional -- stays flagged", func(t *testing.T) {
		if scSweepFixtureVarSiteCleared(t, "n12.go", scSweepNeedleSeedGuardedUnsound, "seedFixtureLike") {
			t.Fatalf("seedFixtureLike cleared — this is seedApprovalFactsFixture's exact shape: Tier 2 must reject a seed that does not run on every path reaching the use")
		}
	})
	t.Run("N13 direct inline INSERT in an if-err Init -- clears", func(t *testing.T) {
		if !scSweepFixtureVarSiteCleared(t, "n13.go", scSweepNeedleSeedDirectInsertClean, "TestN13") {
			t.Fatalf("TestN13 not cleared — an if-err Init runs unconditionally and must not be treated as a guard")
		}
	})
	t.Run("N14 INSERT seeds a different identifier -- stays flagged", func(t *testing.T) {
		if scSweepFixtureVarSiteCleared(t, "n14.go", scSweepNeedleSeedUnrelatedSubject, "TestN14") {
			t.Fatalf("TestN14 cleared — Tier 1 over-cleared: the seeded identifier (otherUserID) is not the one read back as Subject (subject)")
		}
	})
	t.Run("N15 cosmetic mention in a t.Logf, not a real seed -- KNOWN DEFECT, wrongly clears", func(t *testing.T) {
		if !scSweepFixtureVarSiteCleared(t, "n15.go", scSweepNeedleCosmeticMention, "TestN15") {
			t.Fatalf("TestN15 stayed flagged — the cosmetic-mention defect is fixed; drop this needle's inverted assertion and its KNOWN DEFECT comment")
		}
		t.Errorf("DEFECT: TestN15 cleared via a t.Logf call that merely CONTAINS INSERT-INTO-memberships text — " +
			"Tier 1 matches any CallExpr's string-literal args, not just an actual DB Exec/QueryRow; see scSweepMembershipInsertCallBindArg")
	})
	t.Run("N16 seed hidden inside a two-hop wrapper's own guard -- KNOWN DEFECT, wrongly clears", func(t *testing.T) {
		if !scSweepFixtureVarSiteCleared(t, "n16.go", scSweepNeedleHiddenConditionalWrapper, "TestN16") {
			t.Fatalf("TestN16 stayed flagged — the hidden-conditional-wrapper defect is fixed; drop this needle's inverted assertion and its KNOWN DEFECT comment")
		}
		t.Errorf("DEFECT: TestN16 cleared via seedIfAdmin(..., \"guest\") — the outer call site is unconditional but the " +
			"wrapper's OWN if role==\"admin\" guard means no row was ever inserted; scSweepFindMembershipSinks resolves a " +
			"relay as an unconditional sink without checking what guards the relay inside the sink's own body")
	})
	t.Run("N17 two-hop membership wrapper, sibling file, genuinely unconditional -- clears", func(t *testing.T) {
		fset := token.NewFileSet()
		sinkFile, err := parser.ParseFile(fset, "n17_sink.go", scSweepNeedleSeedTwoHopSinkOnly, 0)
		if err != nil {
			t.Fatalf("parse n17_sink.go: %v", err)
		}
		callerFset := token.NewFileSet()
		callerFile, err := parser.ParseFile(callerFset, "n17_caller.go", scSweepNeedleSeedTwoHopCallerOnly, 0)
		if err != nil {
			t.Fatalf("parse n17_caller.go: %v", err)
		}
		callerLines := strings.Split(scSweepNeedleSeedTwoHopCallerOnly, "\n")
		var site *scSweepSubjectSite
		for _, s := range scSweepTextualSubjectSitesIn(callerFset, "n17_caller.go", callerFile, callerLines) {
			if s.kind == "var" && s.fn == "TestN17" {
				s := s
				site = &s
			}
		}
		if site == nil {
			t.Fatalf("no var-kind site found in TestN17 — fixture is malformed")
		}
		fd := scSweepFuncByName(callerFile, "TestN17")
		if fd == nil {
			t.Fatalf("no FuncDecl named TestN17 in fixture")
		}

		// Per-file-only: the caller file alone declares neither seedMembership
		// nor seedViaWrapper, so this needle is meaningless unless resolving
		// sinks from the caller file alone finds nothing.
		fileOnlySinks := scSweepFindMembershipSinks(scSweepFuncDecls(callerFile))
		if scSweepRecogniseSeeding(fd, site.name, fileOnlySinks) {
			t.Fatalf("fixture meaningless: per-file-only resolution cleared the site with no sink visible in that file")
		}

		// Per-package: pool both files' decls first, as the real population
		// scan does -- N11-N14 never exercise this for the MEMBERSHIP sink
		// walk (all single-file), only N10 exercises it for the identity walk.
		pkgDecls := append(scSweepFuncDecls(sinkFile), scSweepFuncDecls(callerFile)...)
		pkgSinks := scSweepFindMembershipSinks(pkgDecls)
		if !scSweepRecogniseSeeding(fd, site.name, pkgSinks) {
			t.Fatalf("TestN17 not cleared — a genuine two-hop, always-unconditional seed via a sibling-file wrapper " +
				"must clear; package-wide membership-sink resolution is required for this shape")
		}
	})
}

// scSweepPopulationPackages is every internal/ package the AUDIT-12 blast
// radius names (System Design, package table): the 9 packages whose _test.go
// files build an identity that today's narrow predicate admits and the strict
// one refuses. A package outside this set — internal/actor, internal/audit,
// and the rest — has its own reasons to skip a DB test and is not this
// story's concern; walking it too would make both scans below noisy with
// findings that have nothing to do with the sweep.
var scSweepPopulationPackages = []string{
	"internal/invoice", "internal/importer", "internal/dashboard", "internal/portfolio",
	"internal/document", "internal/approval", "internal/platform/db", "internal/submission",
	"internal/tenancy",
}

func scSweepInPopulation(pkg string) bool {
	for _, p := range scSweepPopulationPackages {
		if p == pkg {
			return true
		}
	}
	return false
}

// scSweepUnsweptAllowlist is every population package this scan does not yet
// hold for. Each AUDIT-12 sweep subtask deletes its own entries.
var scSweepUnsweptAllowlist = []string{
	"internal/approval",    // one of the tail's three packages swept together in AUDIT-12-05
	"internal/platform/db", // one of the tail's three packages swept together in AUDIT-12-05
	"internal/submission",  // one of the tail's three packages swept together in AUDIT-12-05
	"internal/tenancy",     // never swept: its one failing test's claim is about a no-row caller, inverted rather than fixtured, in AUDIT-12-07
}

func scSweepPackageAllowed(pkg string) bool {
	for _, p := range scSweepUnsweptAllowlist {
		if p == pkg {
			return true
		}
	}
	return false
}

// scSweepSubjectExemption is one func allowed to keep building a fresh,
// unmembered identity inside an already-swept package. Always func-scoped.
type scSweepSubjectExemption struct {
	file, fn string
}

func (e scSweepSubjectExemption) covers(s scSweepSubjectSite) bool {
	return s.file == e.file && s.fn == e.fn
}

// scSweepSubjectAllowlist is every func exempted from an already-swept
// package's rule.
var scSweepSubjectAllowlist = []scSweepSubjectExemption{
	{file: "internal/dashboard/cross_tenant_integration_test.go", fn: "TestRLS_DashboardRollupUnknownTenantSeesNothing"}, // its claim is about a caller in a tenant nobody is a member of; AUDIT-12-07 inverts it, this subtask does not
	{file: "internal/invoice/resolved_outside_test.go", fn: "TestResolveOutside_NoMembershipIsNotPermitted"},             // AUDIT-12-07 inverts this claim: a no-row caller, not a fixture to sweep
	{file: "internal/invoice/resolved_outside_test.go", fn: "TestUnresolveOutside_NoMembershipIsNotPermitted"},           // same claim, the UnresolveOutside leg
	{file: "internal/invoice/transmission_rbac_test.go", fn: "TestGetHandler_RealStore_NoMembershipSeesRoleReason"},      // same claim, GetHandler's role-reason leg
}

// scSweepTestFiles returns every _test.go file under internal/ (repo-relative,
// forward-slashed), skipping the same directories scSeamFiles skips.
func scSweepTestFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
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
		t.Fatalf("walk internal/: %v", err)
	}
	sort.Strings(out)
	return out
}

// scSweepParsedTestFile is one already-read, already-parsed _test.go file,
// kept around so the population scan below can resolve identity sinks
// per-package (one AST walk over every sibling file) before scanning any
// single file's calls against them.
type scSweepParsedTestFile struct {
	rel   string
	fset  *token.FileSet
	file  *ast.File
	lines []string
}

func TestRLS_SweptPackagesBuildIdentitiesFromASeededMember(t *testing.T) {
	t.Run("control needles", scSweepSubjectControlNeedles)
	t.Run("recognise-the-seeding control needles", scSweepRecogniseSeedingControlNeedles)

	root := repoRootDir(t)
	files := scSweepTestFiles(t, root)
	if len(files) < 350 {
		t.Fatalf("walk found %d _test.go file(s) under internal/, want at least 350 (408 measured at AUDIT-12-02) — a clean report over a broken walk means nothing", len(files))
	}

	byPkg := map[string][]scSweepParsedTestFile{}
	swept := 0
	for _, rel := range files {
		pkg := filepath.Dir(rel)
		if !scSweepInPopulation(pkg) || scSweepPackageAllowed(pkg) {
			continue
		}
		swept++
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, raw, 0)
		if err != nil {
			t.Fatalf("parse %s: %v — the scan cannot report on a file it cannot parse", rel, err)
		}
		byPkg[pkg] = append(byPkg[pkg], scSweepParsedTestFile{rel: rel, fset: fset, file: f, lines: strings.Split(string(raw), "\n")})
	}
	if swept < 15 {
		t.Fatalf("walked %d _test.go file(s) in a swept population package, want at least 15 (19 measured at AUDIT-12-02: portfolio's 4 plus dashboard's 15) — the population floor is meant to catch exactly this", swept)
	}

	// Sinks (the positional-helper shape) are resolved per PACKAGE, not per
	// file — internal/document's identity() is declared in document_test.go
	// and called from service_test.go/store_adversarial_test.go/etc, none of
	// which declare it themselves (AUDIT-12-03 Implementation Plan §2).
	var sites []scSweepSubjectSite
	for _, pfs := range byPkg {
		var pkgDecls []*ast.FuncDecl
		for _, pf := range pfs {
			pkgDecls = append(pkgDecls, scSweepFuncDecls(pf.file)...)
		}
		sinks := scSweepFindIdentitySinks(pkgDecls)
		membershipSinks := scSweepFindMembershipSinks(pkgDecls)
		for _, pf := range pfs {
			for _, s := range scSweepTextualSubjectSitesIn(pf.fset, pf.rel, pf.file, pf.lines) {
				if s.kind == "var" {
					if fd := scSweepFuncByName(pf.file, s.fn); fd != nil && scSweepRecogniseSeeding(fd, s.name, membershipSinks) {
						continue // recognise-the-seeding rule: this identifier is seeded a membership row on every path that reaches its use
					}
				}
				sites = append(sites, s)
			}
			sites = append(sites, scSweepHelperCallSitesInFile(pf.fset, pf.rel, pf.file, pf.lines, sinks)...)
		}
	}

	for _, s := range sites {
		covered := false
		for _, e := range scSweepSubjectAllowlist {
			if e.covers(s) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s builds a caller identity from a fresh, unmembered uuid in a swept package — use memberSubject, or exempt this func with its reason", s)
		}
	}
	// Anti-rot: every exemption must still be earning its place.
	for _, e := range scSweepSubjectAllowlist {
		used := false
		for _, s := range sites {
			if e.covers(s) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("subject allowlist entry %s func %s matched nothing — delete it; a stale exemption is an open door nobody is looking at", e.file, e.fn)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan 5 — no swept package gains a t.Skip( the sweep did not put there
// (AUDIT-12-02, Core AC 4)
// ---------------------------------------------------------------------------
//
// The sweep must land green (D-7): a test that cannot be made to pass by
// seeding its caller a real membership row is a defect in the plan, not
// license to skip it. This scan pins the t.Skip( census of every swept
// package so a fixture subtask cannot quietly turn a hard fixture into a
// skipped one, and a later revert of a fix cannot reintroduce one either.
//
// scSweepSkipAllowlist starts holding exactly the pre-existing, env-gated
// dbTestPools skip in each of this subtask's two packages — present before
// AUDIT-12 and unrelated to the sweep. A later sweep subtask adds its own
// package's pre-existing skips the same way; it never grows because of a new
// one.

type scSweepSkipSite struct {
	file string
	line int
	fn   string
}

func (s scSweepSkipSite) String() string {
	return s.file + ":" + strconv.Itoa(s.line) + " (" + s.fn + ")"
}

var scSweepSkipRE = regexp.MustCompile(`\bt\.Skip\(`)

func scSweepSkipSitesIn(fset *token.FileSet, rel string, f *ast.File, lines []string) []scSweepSkipSite {
	var out []scSweepSkipSite
	for fn, rng := range scSweepFuncLineRanges(fset, f) {
		start, end := rng[0], rng[1]
		if start < 1 || end > len(lines) {
			continue
		}
		for i := start - 1; i < end; i++ {
			if scSweepSkipRE.MatchString(lines[i]) {
				out = append(out, scSweepSkipSite{file: rel, line: i + 1, fn: fn})
			}
		}
	}
	return out
}

func scSweepFixtureSkipSites(t *testing.T, name, src string) []scSweepSkipSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return scSweepSkipSitesIn(fset, name, f, strings.Split(src, "\n"))
}

const scSweepNeedleSkip = `package x

func TestSkipsWhenUnconfigured(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("db-integration test skipped")
	}
}
`

func scSweepSkipControlNeedle(t *testing.T) {
	sites := scSweepFixtureSkipSites(t, "skip.go", scSweepNeedleSkip)
	if len(sites) != 1 || sites[0].fn != "TestSkipsWhenUnconfigured" {
		t.Fatalf("sites = %v, want exactly one t.Skip( site in TestSkipsWhenUnconfigured — the scan cannot see the shape it exists to catch", sites)
	}
}

// scSweepSkipExemption is one known, pre-existing t.Skip( site inside a swept
// package. A new one is what this scan exists to catch.
type scSweepSkipExemption struct {
	file, fn string
}

func (e scSweepSkipExemption) covers(s scSweepSkipSite) bool {
	return s.file == e.file && s.fn == e.fn
}

var scSweepSkipAllowlist = []scSweepSkipExemption{
	{file: "internal/portfolio/portfolio_test.go", fn: "dbTestPools"},                                         // env-gated: skips when DATABASE_URL/DATABASE_SUPERUSER_URL are unset, the same guard every DB-backed package uses
	{file: "internal/dashboard/store_test.go", fn: "dbTestPools"},                                             // same guard, dashboard's own pool helper
	{file: "internal/document/document_test.go", fn: "dbTestPools"},                                           // same guard, document's own pool helper
	{file: "internal/importer/store_test.go", fn: "dbTestPools"},                                              // same guard, importer's own pool helper
	{file: "internal/invoice/store_test.go", fn: "dbTestPools"},                                               // same guard, invoice's own pool helper
	{file: "internal/invoice/payload_engine_test.go", fn: "rulesAppPool"},                                     // same guard, PAY-18's app-role-only pool helper
	{file: "internal/invoice/revalidate_test.go", fn: "TestRevalidateAllTenants_CoversEveryEnumeratedTenant"}, // pre-existing: skips when DATABASE_READER_URL is unset, unrelated to the sweep
}

func TestRLS_NoNewSkipsInASweptPackage(t *testing.T) {
	t.Run("control needle", scSweepSkipControlNeedle)

	root := repoRootDir(t)
	files := scSweepTestFiles(t, root)

	var sites []scSweepSkipSite
	swept := 0
	for _, rel := range files {
		pkg := filepath.Dir(rel)
		if !scSweepInPopulation(pkg) || scSweepPackageAllowed(pkg) {
			continue
		}
		swept++
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, raw, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		sites = append(sites, scSweepSkipSitesIn(fset, rel, f, strings.Split(string(raw), "\n"))...)
	}
	if swept < 15 {
		t.Fatalf("walked %d _test.go file(s) in a swept population package, want at least 15 (19 measured at AUDIT-12-02) — the population floor is meant to catch exactly this", swept)
	}
	if len(sites) < 2 {
		t.Fatalf("found %d t.Skip( site(s) outside the unswept-package allowlist, want at least 2 (portfolio's and dashboard's own dbTestPools, measured at AUDIT-12-02) — a clean report over a broken walk means nothing", len(sites))
	}

	for _, s := range sites {
		covered := false
		for _, e := range scSweepSkipAllowlist {
			if e.covers(s) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s is a new t.Skip( in a swept package — fix the fixture, never skip the test; a genuine new env-gated skip is added to scSweepSkipAllowlist with its reason, not left bare", s)
		}
	}
	// Anti-rot: every exemption must still be earning its place.
	for _, e := range scSweepSkipAllowlist {
		used := false
		for _, s := range sites {
			if e.covers(s) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("skip allowlist entry %s func %s matched nothing — delete it; a stale exemption is an open door nobody is looking at", e.file, e.fn)
		}
	}
}

// ---------------------------------------------------------------------------
// Every exemption carries its own reason
// ---------------------------------------------------------------------------

func TestRLS_EveryAllowlistEntryNamesItsReason(t *testing.T) {
	root := repoRootDir(t)
	path := filepath.Join(root, filepath.FromSlash(scSelfPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", scSelfPath, err)
	}
	lines := strings.Split(string(raw), "\n")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, raw, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", scSelfPath, err)
	}

	want := len(scPoolAllowlist) + len(scCoreAllowlist) + len(scSweepUnsweptAllowlist) + len(scSweepSubjectAllowlist) + len(scSweepSkipAllowlist)
	found := 0
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			switch vs.Names[0].Name {
			case "scPoolAllowlist", "scCoreAllowlist", "scSweepUnsweptAllowlist", "scSweepSubjectAllowlist", "scSweepSkipAllowlist":
			default:
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range cl.Elts {
				found++
				end := fset.Position(elt.End())
				src := lines[end.Line-1]
				reason := ""
				if i := strings.Index(src[min(end.Column-1, len(src)):], "//"); i >= 0 {
					reason = strings.TrimSpace(src[min(end.Column-1, len(src))+i+2:])
				}
				if len([]rune(reason)) < scMinReasonRunes {
					t.Errorf("%s:%d states no reason (%q) — Core AC 6 wants a reason per exemption, not a category", scSelfPath, end.Line, reason)
				}
			}
		}
	}
	if found != want {
		t.Fatalf("read %d allowlist entr(ies) from %s, want %d — the source read and the compiled lists disagree, so a missing reason could pass unread", found, scSelfPath, want)
	}
}
