// extraction_audit_test.go: the terminal-outcome audit adapter, read from both ends. What it
// WRITES comes from a recording pgx.Tx -- cmd/submission opens no database, and audit.Record's
// whole observable effect is one INSERT. Where it is SPELLED comes from an AST scan: a const
// event name or a helper-built payload drops the site out of the repo-wide audit.Record
// partition in internal/platform/db/audit_number_scan_test.go, which no behavioural test sees.
package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	eaSucceededEvent = "extraction.succeeded"
	eaFailedEvent    = "extraction.failed"

	// The literal by value, matching internal/submission/verdict_audit.go:25 -- never
	// internal/extraction's unexported workerActor ("extraction-worker"), which lands on the
	// document.read rows the worker causes, not on the outcome of the job itself.
	eaActor      = "system"
	eaWorkerName = "extraction-worker"

	eaAuditPath = "github.com/SimonOsipov/invoice-os/internal/audit"
	eaAuditDef  = "audit"
	eaAdapterFn = "newExtractionAuditor"

	// audit.Record(ctx, tx, actor, event, payload).
	eaRecordArgs = 5
	eaActorArg   = 2
	eaEventArg   = 3
	eaPayloadArg = 4

	// Floors. The file walk mirrors internal/platform/db/seam_coverage_test.go:148 (151 files,
	// 9 under cmd/, measured here). main.go holds 4 function literals today: the two closures
	// newDocumentOpener and newPageSink return, the deferred Close inside the first, and the
	// /v1/ping handler.
	eaMinProdFiles = 130
	eaMinCmdFiles  = 8
	eaMinFuncLits  = 4

	// Spelled once as a production string literal, measured. The classifier must still find it,
	// or every "spelled exactly once" clearance below is a broken walk reporting an empty set.
	eaNeedleEvent = "reconciliation.drift_detected"
)

// Sorted: the key-set assertions compare against sort.Strings output.
var (
	eaSucceededKeys = []string{
		"document_id", "extraction_job_id", "extractor", "extractor_version",
		"field_count", "flagged_count",
	}
	eaFailedKeys = []string{
		"document_id", "extraction_job_id", "extractor", "extractor_version",
		"failure_kind", "state",
	}

	// The spellings an adapter reaching for extraction_jobs.last_error would use. That column
	// is free text the audit-payload rule forbids and the jobs table already holds.
	eaForbiddenKeys = []string{"body", "detail", "error", "last_error", "message", "raw", "wire"}

	// What an unassigned or drifted kind looks like. "" is the zero value and the only one
	// Work() could ever hand over; the rest are the spellings a rename or a hand-typed
	// constant lands on, none of which internal/extraction would accept back.
	eaInvalidKinds = []extraction.FailureKind{
		"", "document_unavailble", "DOCUMENT_UNAVAILABLE", "extract_failed ", "unknown",
	}
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

// eaFullEvent populates EVERY field, both branches' worth. The key-set assertions then prove
// each branch DROPS the other's fields rather than merely never having been given them.
func eaFullEvent(succeeded bool) extraction.ExtractionAudit {
	return extraction.ExtractionAudit{
		Succeeded:        succeeded,
		DocumentID:       "11111111-1111-1111-1111-111111111111",
		ExtractionJobID:  "22222222-2222-2222-2222-222222222222",
		Extractor:        "docling",
		ExtractorVersion: "1.4.0",
		FieldCount:       7,
		FlaggedCount:     3,
		State:            "dead_lettered",
		FailureKind:      extraction.FailurePagesNotRendered,
	}
}

// eaRun drives the adapter once and floors the observation at exactly one row: zero rows
// satisfies every assertion below vacuously.
func eaRun(t *testing.T, ev extraction.ExtractionAudit) eaRow {
	t.Helper()

	tx := &eaTx{}
	if err := newExtractionAuditor()(context.Background(), tx, ev); err != nil {
		t.Fatalf("the auditor returned %v, want nil", err)
	}
	if len(tx.execs) != 1 {
		t.Fatalf("the auditor issued %d statement(s), want exactly 1 -- one terminal outcome is one audit row", len(tx.execs))
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

func TestNewExtractionAuditor_SucceededWritesTheSucceededEvent(t *testing.T) {
	row := eaRun(t, eaFullEvent(true))

	if row.event != eaSucceededEvent {
		t.Fatalf("event = %q, want %q -- the branch is chosen by ev.Succeeded, and a swapped pair is invisible to every source scan in this file", row.event, eaSucceededEvent)
	}
	if got := row.keys(); !reflect.DeepEqual(got, eaSucceededKeys) {
		t.Errorf("payload keys = %v, want exactly %v -- set equality, so a seventh key of any name fails here", got, eaSucceededKeys)
	}
	want := map[string]any{
		"document_id":       "11111111-1111-1111-1111-111111111111",
		"extraction_job_id": "22222222-2222-2222-2222-222222222222",
		"extractor":         "docling",
		"extractor_version": "1.4.0",
		"field_count":       float64(7),
		"flagged_count":     float64(3),
	}
	if !reflect.DeepEqual(row.payload, want) {
		t.Errorf("payload = %v, want %v -- six right-named keys carrying the wrong values reads as clean on a key-set check alone", row.payload, want)
	}
}

func TestNewExtractionAuditor_FailedWritesTheFailedEvent(t *testing.T) {
	row := eaRun(t, eaFullEvent(false))

	if row.event != eaFailedEvent {
		t.Fatalf("event = %q, want %q", row.event, eaFailedEvent)
	}
	if got := row.keys(); !reflect.DeepEqual(got, eaFailedKeys) {
		t.Errorf("payload keys = %v, want exactly %v -- field_count and flagged_count were both set on the event and must not travel on a failure", got, eaFailedKeys)
	}
	want := map[string]any{
		"document_id":       "11111111-1111-1111-1111-111111111111",
		"extraction_job_id": "22222222-2222-2222-2222-222222222222",
		"extractor":         "docling",
		"extractor_version": "1.4.0",
		"state":             "dead_lettered",
		"failure_kind":      string(extraction.FailurePagesNotRendered),
	}
	if !reflect.DeepEqual(row.payload, want) {
		t.Errorf("payload = %v, want %v", row.payload, want)
	}
}

// TestNewExtractionAuditor_FailedRefusesAnInvalidFailureKind: the adapter is the one chokepoint
// between the struct and the row, and audit_log never rewrites a row. Work() assigns a kind on
// every error path, so "" is unreachable today -- this gate is what keeps it unreachable, and
// without it extraction.FailureKind.Valid() has no production caller at all.
func TestNewExtractionAuditor_FailedRefusesAnInvalidFailureKind(t *testing.T) {
	// Control needle: a filled failure passes the gate and still writes its row. Without it a
	// gate that refused every failure would satisfy each refusal below.
	if row := eaRun(t, eaFullEvent(false)); row.event != eaFailedEvent {
		t.Fatalf("control needle: a filled failure wrote %q, want %q -- the gate refuses everything, so the refusals below prove nothing", row.event, eaFailedEvent)
	}

	checked := 0
	for _, kind := range eaInvalidKinds {
		if kind.Valid() {
			t.Errorf("%q is an accepted kind, so the case below asserts the opposite of the gate", kind)
			continue
		}
		ev := eaFullEvent(false)
		ev.FailureKind = kind
		checked++

		tx := &eaTx{}
		if err := newExtractionAuditor()(context.Background(), tx, ev); err == nil {
			t.Errorf("a failure carrying kind %q returned nil, want an error -- the row it writes names a kind nothing in Work() produces, and audit_log is append-only", kind)
		}
		if len(tx.execs) != 0 {
			t.Errorf("a failure carrying kind %q issued %d statement(s), want 0 -- refusing after the INSERT leaves the wrong row behind on any caller that swallows the error", kind, len(tx.execs))
		}
	}
	if checked != len(eaInvalidKinds) {
		t.Fatalf("checked %d kind(s), want %d -- a shrunken set reports the gate closed over spellings nobody tried", checked, len(eaInvalidKinds))
	}

	// The gate is on the failure branch alone: a success carries no kind by construction and
	// must not be refused for it.
	success := eaFullEvent(true)
	success.FailureKind = ""
	if row := eaRun(t, success); row.event != eaSucceededEvent {
		t.Errorf("a success carrying no failure kind wrote %q, want %q -- the gate is guarding the wrong branch", row.event, eaSucceededEvent)
	}
}

// The gate admits four kinds, so the row must carry the one the event carried. A constant
// substituted after the gate reads as clean against a single-kind fixture.
func TestNewExtractionAuditor_FailedEchoesTheEventsFailureKind(t *testing.T) {
	kinds := []extraction.FailureKind{
		extraction.FailureDocumentUnavailable, extraction.FailurePagesNotRendered,
		extraction.FailurePageRowsNotWritten, extraction.FailureExtractFailed,
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("%q is not an accepted kind, so this case asserts the opposite of the gate", kind)
		}
		ev := eaFullEvent(false)
		ev.FailureKind = kind
		row := eaRun(t, ev)
		if row.event != eaFailedEvent {
			t.Fatalf("kind %q wrote %q, want %q", kind, row.event, eaFailedEvent)
		}
		if got := row.payload["failure_kind"]; got != string(kind) {
			t.Errorf("kind %q wrote failure_kind %v, want %q", kind, got, kind)
		}
		seen[string(kind)] = true
	}
	if len(seen) != len(kinds) {
		t.Fatalf("exercised %d distinct kind(s), want %d", len(seen), len(kinds))
	}
}

func TestNewExtractionAuditor_ActorIsTheSystemLiteral(t *testing.T) {
	seen := 0
	for _, succeeded := range []bool{true, false} {
		row := eaRun(t, eaFullEvent(succeeded))
		seen++
		if row.actor != eaActor {
			t.Errorf("%s carries actor %q, want %q -- %q is internal/extraction's workerActor, which belongs on the document.read rows the worker causes, not on the outcome of the job",
				row.event, row.actor, eaActor, eaWorkerName)
		}
	}
	if seen != 2 {
		t.Fatalf("observed %d row(s), want 2 -- a loop that ran zero times reports a correct actor too", seen)
	}
}

// eaForbidden returns, sorted, every forbidden key present in payload.
func eaForbidden(payload map[string]any) []string {
	var hits []string
	for _, k := range eaForbiddenKeys {
		if _, ok := payload[k]; ok {
			hits = append(hits, k)
		}
	}
	sort.Strings(hits)
	return hits
}

func TestNewExtractionAuditor_CarriesNoErrorText(t *testing.T) {
	// Control needle: the same matcher over a planted payload must name every forbidden key.
	// A matcher that finds nothing reports every real payload clean.
	planted := map[string]any{"document_id": "doc-1"}
	for _, k := range eaForbiddenKeys {
		planted[k] = `pq: SSL connection has been closed unexpectedly`
	}
	if got := eaForbidden(planted); !reflect.DeepEqual(got, eaForbiddenKeys) {
		t.Fatalf("the forbidden-key matcher found %v in a payload carrying all of %v -- it cannot report a hit, so the clean sweeps below mean nothing", got, eaForbiddenKeys)
	}

	rows := 0
	for _, succeeded := range []bool{true, false} {
		row := eaRun(t, eaFullEvent(succeeded))
		rows++
		if got := eaForbidden(row.payload); len(got) != 0 {
			t.Errorf("%s carries %v -- extraction_jobs.last_error is adapter-shaped free text that can hold wire detail, and the jobs table already holds it", row.event, got)
		}
		want := eaSucceededKeys
		if !succeeded {
			want = eaFailedKeys
		}
		if got := row.keys(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s payload keys = %v, want exactly %v -- absence of seven named keys is not absence of wire detail; only set equality is", row.event, got, want)
		}
	}
	if rows != 2 {
		t.Fatalf("observed %d payload(s), want 2 -- a loop that ran zero times finds no forbidden key either", rows)
	}
}

// ---------------------------------------------------------------------------
// Where the adapter is spelled
// ---------------------------------------------------------------------------

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

// eaMainSites reads cmd/submission/main.go's two audit.Record calls, flooring the walk on both
// the function literals it descended into and the exact number of sites it must find.
func eaMainSites(t *testing.T) []eaSite {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v -- a file the scan cannot read is a file it reports clean on", err)
	}
	sites, lits := eaSitesIn(fset, "cmd/submission/main.go", f)
	if lits < eaMinFuncLits {
		t.Fatalf("walked %d function literal(s) in cmd/submission/main.go, want at least %d (newDocumentOpener's closure, its deferred Close, newPageSink's closure, the /v1/ping handler) -- a walk that descends into nothing finds no call site either", lits, eaMinFuncLits)
	}
	if len(sites) != 2 {
		t.Fatalf("cmd/submission/main.go holds %d audit.Record call site(s) %v, want exactly 2 -- one per terminal outcome, each spelled at its own call", len(sites), sites)
	}
	return sites
}

// eaControlNeedles proves the classifier reports both outcomes. Three synthetic files, each the
// shape a later refactor would produce, run through the SAME matcher.
func eaControlNeedles(t *testing.T) {
	fixture := func(t *testing.T, name, src string) eaSite {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse fixture %s: %v", name, err)
		}
		sites, _ := eaSitesIn(fset, name, f)
		if len(sites) != 1 {
			t.Fatalf("fixture %s yielded %d site(s), want exactly 1", name, len(sites))
		}
		return sites[0]
	}

	const head = `package x

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

`

	t.Run("N1 a literal event with an inline payload reads as literal-and-inline", func(t *testing.T) {
		s := fixture(t, "n1.go", head+`func f(ctx context.Context, tx any, id string) error {
	return audit.Record(ctx, tx, "system", "extraction.succeeded", map[string]any{"document_id": id})
}
`)
		if !s.eventLit || s.event != eaSucceededEvent || !s.inline || !s.actorLit || s.actor != eaActor {
			t.Fatalf("site = %+v -- the classifier cannot report a compliant site, so its clearances mean nothing", s)
		}
		if !reflect.DeepEqual(s.keys, []string{"document_id"}) {
			t.Fatalf("site keys = %v, want [document_id]", s.keys)
		}
	})

	t.Run("N2 a const event name reads as NOT literal", func(t *testing.T) {
		s := fixture(t, "n2.go", head+`const evt = "extraction.succeeded"

func f(ctx context.Context, tx any, id string) error {
	return audit.Record(ctx, tx, "system", evt, map[string]any{"document_id": id})
}
`)
		if s.eventLit {
			t.Fatalf("site = %+v read its event as a literal -- a const identifier is not an *ast.BasicLit, and a classifier that cannot tell the difference would wave through the refactor that files this site under audit_number_scan_test.go's \"no bucket\" case", s)
		}
	})

	t.Run("N3 a helper-built payload reads as NOT inline", func(t *testing.T) {
		s := fixture(t, "n3.go", head+`func f(ctx context.Context, tx any, id string) error {
	return audit.Record(ctx, tx, "system", "extraction.succeeded", build(id))
}
`)
		if s.inline {
			t.Fatalf("site = %+v read its payload as inline -- a payload built one hop away is not something a forward-only walk can read, and pretending otherwise excuses it silently", s)
		}
		if !s.eventLit {
			t.Fatalf("site = %+v lost its literal event too -- the two properties must be reported independently", s)
		}
	})

	t.Run("N4 an actor identifier reads as NOT a literal", func(t *testing.T) {
		s := fixture(t, "n4.go", head+`func f(ctx context.Context, tx any, id string) error {
	return audit.Record(ctx, tx, workerActor, "extraction.succeeded", map[string]any{"document_id": id})
}
`)
		if s.actorLit || s.actor != "" {
			t.Fatalf("site = %+v read workerActor as the literal %q -- the actor assertion below could then pass on a const whose value nobody in this package can see", s, s.actor)
		}
	})

	t.Run("N5 an aliased audit import stays in the population", func(t *testing.T) {
		s := fixture(t, "n5.go", `package x

import (
	"context"

	a "github.com/SimonOsipov/invoice-os/internal/audit"
)

func f(ctx context.Context, tx any, id string) error {
	return a.Record(ctx, tx, "system", "extraction.failed", map[string]any{"document_id": id})
}
`)
		if s.event != eaFailedEvent {
			t.Fatalf("site = %+v -- an aliased import would drop the adapter out of the population entirely", s)
		}
	})
}

// TestExtractionAuditor_CallSitesAreLiteralAndInline: D-7. Both properties are required, not
// stylistic: anIsDirect in internal/platform/db/audit_number_scan_test.go is
// `s.event != "" && s.inline`, so a const event name or a helper-built payload puts the site in
// that scan's "no bucket" case, where nothing structural guards its payload again.
func TestExtractionAuditor_CallSitesAreLiteralAndInline(t *testing.T) {
	t.Run("control needles", eaControlNeedles)

	sites := eaMainSites(t)
	var events []string
	for _, s := range sites {
		if s.args != eaRecordArgs {
			t.Errorf("%s calls audit.Record with %d argument(s), want %d -- every argument position below reads the wrong thing", s, s.args, eaRecordArgs)
			continue
		}
		if s.fn != eaAdapterFn {
			t.Errorf("%s sits in %s, want %s -- the adapter is the one place this binary names an 08 event", s, s.fn, eaAdapterFn)
		}
		if !s.eventLit {
			t.Errorf("%s names its event with something other than a string literal", s)
		}
		if !s.inline {
			t.Errorf("%s builds its payload somewhere other than an inline composite literal", s)
		}
		events = append(events, s.event)
	}
	sort.Strings(events)
	want := []string{eaFailedEvent, eaSucceededEvent}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("the two sites spell %v, want %v -- one name per terminal outcome, and two sites spelling one name emit half the vocabulary", events, want)
	}
}

// TestExtractionAuditor_ActorIsSystemByValue: D-9. The literal, read at the call.
func TestExtractionAuditor_ActorIsSystemByValue(t *testing.T) {
	sites := eaMainSites(t)
	for _, s := range sites {
		if !s.actorLit {
			t.Errorf("%s passes a non-literal actor -- internal/extraction's workerActor is unexported and would arrive here only through a copy nobody can diff against %q", s, eaActor)
			continue
		}
		if s.actor != eaActor {
			t.Errorf("%s passes actor %q, want the literal %q", s, s.actor, eaActor)
		}
	}
}

// TestExtractionAuditor_PayloadKeysAreExact: §5. Set equality per branch, not containment: a
// seventh key of any name fails, and so does a missing sixth.
func TestExtractionAuditor_PayloadKeysAreExact(t *testing.T) {
	want := map[string][]string{eaSucceededEvent: eaSucceededKeys, eaFailedEvent: eaFailedKeys}
	seen := map[string]bool{}
	for _, s := range eaMainSites(t) {
		w, ok := want[s.event]
		if !ok {
			t.Errorf("%s writes %q, which is neither terminal outcome", s, s.event)
			continue
		}
		if seen[s.event] {
			t.Errorf("%s is a second writer of %q -- one terminal outcome is one site", s, s.event)
			continue
		}
		seen[s.event] = true
		if !reflect.DeepEqual(s.keys, w) {
			t.Errorf("%s builds payload keys %v, want exactly %v", s, s.keys, w)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("read %d of the 2 terminal outcomes %v -- a partial read clears the branch it never looked at", len(seen), sortedEventNames(seen))
	}
}

func sortedEventNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// eaProdFiles lists every non-test .go file under cmd/ and internal/, floored on both roots the
// way internal/platform/db/seam_coverage_test.go:148 floors the same walk.
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

// TestExtractionAuditor_EventNameHasOneConstructionSiteEach: each name is spelled once in the
// tree's production code, so the vocabulary cannot grow a third value by accident and a rename
// cannot land in one branch only.
func TestExtractionAuditor_EventNameHasOneConstructionSiteEach(t *testing.T) {
	root, files := eaProdFiles(t)

	// Control needle: a name that IS spelled exactly once today. Without it a broken parse
	// reports every name below as spelled zero times, which reads like a missing adapter.
	if got := eaLiteralSites(t, root, files, eaNeedleEvent); len(got) != 1 {
		t.Fatalf("the literal matcher found %q at %v, want exactly 1 site -- the matcher is broken, so the counts below are answering a different question", eaNeedleEvent, got)
	}

	for _, event := range []string{eaSucceededEvent, eaFailedEvent} {
		got := eaLiteralSites(t, root, files, event)
		if len(got) != 1 {
			t.Errorf("%q is spelled as a production string literal at %v, want exactly 1 site -- zero means nothing emits it and the frontend label guard has nothing to find; two means a rename can land in one branch only", event, got)
			continue
		}
		if !strings.HasPrefix(got[0], "cmd/submission/main.go:") {
			t.Errorf("%q is spelled at %s, want cmd/submission/main.go -- internal/extraction is fenced off internal/audit, so the adapter is the composition root's job", event, got[0])
		}
	}
}
