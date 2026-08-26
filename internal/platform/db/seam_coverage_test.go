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

	want := len(scPoolAllowlist) + len(scCoreAllowlist)
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
			if vs.Names[0].Name != "scPoolAllowlist" && vs.Names[0].Name != "scCoreAllowlist" {
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
