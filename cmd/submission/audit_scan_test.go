// audit_scan_test.go: the recording pgx.Tx and the audit.Record AST scan the audit-adapter tests
// of this package share. cmd/worker/extraction_audit_test.go holds the same helpers.
package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// The literal by value, matching recordSubmissionAudit in internal/submission.
	eaActor = "system"

	eaAuditPath = "github.com/SimonOsipov/invoice-os/internal/audit"
	eaAuditDef  = "audit"

	// audit.Record(ctx, tx, actor, event, payload).
	eaRecordArgs = 5
	eaActorArg   = 2
	eaEventArg   = 3
	eaPayloadArg = 4

	// Floors on the walks: the repo's production files, those under cmd/, and main.go's
	// function literals (handlers and adapter closures).
	eaMinProdFiles = 130
	eaMinCmdFiles  = 8
	eaMinFuncLits  = 4
)

// ---------------------------------------------------------------------------
// What the adapter writes
// ---------------------------------------------------------------------------

// eaTx records the statement audit.Record issues. The embedded pgx.Tx is nil, so any method
// but Exec panics: a second write cannot pass unseen.
type eaTx struct {
	pgx.Tx
	execs []eaExec
}

type eaExec struct {
	sql  string
	args []any
}

func (tx *eaTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, eaExec{sql: sql, args: args})
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

// eaRow is one decoded audit_log INSERT.
type eaRow struct {
	actor   string
	event   string
	payload map[string]any
}

func (r eaRow) keys() []string {
	out := make([]string, 0, len(r.payload))
	for k := range r.payload {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// eaDecodeOne reads the single audit_log INSERT off a recording tx. Shared with
// document_read_audit_test.go: two copies of the decoder could disagree about what a row is.
func eaDecodeOne(t *testing.T, tx *eaTx) eaRow {
	t.Helper()

	if len(tx.execs) != 1 {
		t.Fatalf("the auditor issued %d statement(s), want exactly 1 -- one audited action is one audit row", len(tx.execs))
	}
	e := tx.execs[0]
	if !strings.Contains(e.sql, "audit_log") {
		t.Fatalf("the auditor executed %q, which writes no audit_log row -- this decoder is reading the wrong statement", e.sql)
	}
	if len(e.args) != 3 {
		t.Fatalf("the audit_log INSERT binds %d value(s), want 3 (actor, event, payload)", len(e.args))
	}
	actor, ok := e.args[0].(string)
	if !ok {
		t.Fatalf("actor bound as %T, want string", e.args[0])
	}
	event, ok := e.args[1].(string)
	if !ok {
		t.Fatalf("event bound as %T, want string", e.args[1])
	}
	body, ok := e.args[2].(string)
	if !ok {
		t.Fatalf("payload bound as %T, want string -- pgx sends raw JSON to the jsonb column as a string", e.args[2])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload %q is not a JSON object: %v", body, err)
	}
	return eaRow{actor: actor, event: event, payload: payload}
}

// eaSite is one audit.Record call read AT THE CALL, the way the repo-wide partition in
// internal/platform/db/audit_number_scan_test.go reads it.
type eaSite struct {
	file     string
	line     int
	fn       string
	args     int
	actor    string
	actorLit bool
	event    string
	eventLit bool
	keys     []string
	inline   bool
}

func (s eaSite) String() string { return s.file + ":" + strconv.Itoa(s.line) + " in " + s.fn }

func eaStringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// eaCompositeKeys reads the string keys off an inline composite literal. inline is false for a
// payload built anywhere else -- a helper call, a local, a parameter.
func eaCompositeKeys(e ast.Expr) (keys []string, inline bool) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := eaStringLit(kv.Key); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, true
}

// eaAuditAlias resolves the local name of the internal/audit import, so a renamed import cannot
// drop a site out of the population.
func eaAuditAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != eaAuditPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return eaAuditDef
	}
	return ""
}

func eaIsRecord(n ast.Node, alias string) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Record" {
		return nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != alias {
		return nil, false
	}
	return call, true
}

// eaSitesIn attributes every audit.Record call to its innermost enclosing NAMED func, and
// returns the function literals walked so a caller can floor the descent.
func eaSitesIn(fset *token.FileSet, rel string, f *ast.File) (sites []eaSite, funcLits int) {
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			funcLits++
		}
		return true
	})
	alias := eaAuditAlias(f)
	if alias == "" {
		return nil, funcLits
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := eaIsRecord(n, alias)
			if !ok {
				return true
			}
			s := eaSite{file: rel, line: fset.Position(call.Pos()).Line, fn: fd.Name.Name, args: len(call.Args)}
			if s.args == eaRecordArgs {
				s.actor, s.actorLit = eaStringLit(call.Args[eaActorArg])
				s.event, s.eventLit = eaStringLit(call.Args[eaEventArg])
				s.keys, s.inline = eaCompositeKeys(call.Args[eaPayloadArg])
			}
			sites = append(sites, s)
			return true
		})
	}
	return sites, funcLits
}

// eaProdFiles lists every non-test .go file under cmd/ and internal/, floored on both roots the
// way internal/platform/db/seam_coverage_test.go floors the same walk.
func eaProdFiles(t *testing.T) (string, []string) {
	t.Helper()

	root := wtRepoRoot(t)
	var files []string
	cmdFiles := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			files = append(files, filepath.ToSlash(rel))
			if dir == "cmd" {
				cmdFiles++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v -- a failed walk reads exactly like a clean one", dir, err)
		}
	}
	if len(files) < eaMinProdFiles || cmdFiles < eaMinCmdFiles {
		t.Fatalf("the walk found %d non-test .go file(s), %d of them under cmd/, want at least %d and %d (151 and 9 measured) -- a clean report over a broken walk means nothing", len(files), cmdFiles, eaMinProdFiles, eaMinCmdFiles)
	}
	sort.Strings(files)
	return root, files
}

// eaLiteralSites returns every production file:line whose source holds want as a string
// literal. Comments and identifiers are excluded: it walks the AST, not the bytes.
func eaLiteralSites(t *testing.T, root string, files []string, want string) []string {
	t.Helper()

	var hits []string
	fset := token.NewFileSet()
	for _, rel := range files {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v -- the scan cannot report on a file it cannot read", rel, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if v, ok := eaStringLit(e); ok && v == want {
				hits = append(hits, rel+":"+strconv.Itoa(fset.Position(n.Pos()).Line))
			}
			return true
		})
	}
	sort.Strings(hits)
	return hits
}
