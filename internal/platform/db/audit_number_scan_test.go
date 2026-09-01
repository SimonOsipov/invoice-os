// AUDIT-11's mechanical half: every invoice-scoped audit.Record writer carries the
// "invoice_number" payload key, proved by walking the source rather than asserted
// once per writer. A writer added later without the key is what this catches.
//
// The 17 invoice-scoped event names are parsed at test time out of the generated
// column's own CASE expression, so no Go copy of the list exists to drift.
// internal/audit/scoped_test.go already does this parse, but it is unexported in
// package audit_test and a _test.go helper cannot be imported across packages, so
// the parse is re-implemented here over the SAME migration file. Two parses of one
// file cannot disagree about what is true.
//
// Known limit: no type information (golang.org/x/tools is absent from go.mod, and
// seam_coverage_test.go records that decision), so the scan cannot follow a payload
// built elsewhere. Three sites are indirect. driftPayload is one hop away, so its
// own composite literal IS checked; recordSubmissionAudit takes the payload as a
// parameter and is NOT structurally guarded -- submission.accepted, .rejected and
// .failed rest on the behavioural tests in internal/submission.
//
// A fourth class needs naming or a forward-only walk skips it in silence: four
// sites build the event name in an if/else, so there is no literal to match on.
// None is invoice-scoped today. The partition assertion is what forces the next
// one to be classified rather than skipped.
//
// Sites are attributed to their innermost enclosing NAMED func, descending into
// func literals -- deliberately the opposite of seam_coverage_test.go, which stops
// at a literal. Most writes here sit inside a WithinTenantTx closure, and a literal
// has no name for an exemption table to key on.
//
// This scan reports an ABSENCE, which is the instrument class that reports
// all-clear while examining nothing. So it carries a control needle it must still
// find and floors on the population walked. cmd/'s only audit.Record calls are
// cmd/submission's two audit adapters, a population
// TestSubmissionMain_AuditWritersAreEachAccountedFor pins; this file's cmd/ file floor
// is what proves the cmd/ half of the walk ran at all.
//
// The TestRLS_ prefix is load-bearing: ci.yml runs this package under -run TestRLS,
// and nothing else in that filter set would reach these names
// (TestCIRunFiltersReachEveryTestInThePackage). No test here touches a database.
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
	anAuditPath = "github.com/SimonOsipov/invoice-os/internal/audit"
	anAuditDef  = "audit"
	anRecord    = "Record"
	anNumberKey = "invoice_number"

	// audit.Record(ctx, tx, actor, event, payload).
	anArgs       = 5
	anEventArg   = 3
	anPayloadArg = 4

	// Floors measured at AUDIT-11-06. scSeamFiles already floors the file walk.
	anMinCalls = 35
	anMinFiles = 13

	// anControlEvent is a non-scoped writer with an inline payload the scan must
	// still find. Located by event name, never by line: line numbers drift.
	anControlEvent = "portfolio.entity.created"

	anDocPath          = "docs/audit-log-read-contract.md"
	anMinSectionRunes  = 200
	anMinDocSubheads   = 12
	anDocSubheadPrefix = "### 10."
)

// ---------------------------------------------------------------------------
// The 17 invoice-scoped events, derived from the migration
// ---------------------------------------------------------------------------

// Copied verbatim from internal/audit/scoped_test.go. The event-name shape is
// dotted and lowercase, so the bare payload-key literals in the same CASE never
// match it.
var (
	anEventInRE   = regexp.MustCompile(`(?is)event\s+IN\s*\(([^)]*)\)`)
	anEventNameRE = regexp.MustCompile(`'([a-z_]+(?:\.[a-z_]+)+)'`)
)

func anEventNames(t *testing.T, listText string) []string {
	t.Helper()
	matches := anEventNameRE.FindAllStringSubmatch(listText, -1)
	if len(matches) == 0 {
		t.Fatalf("event list %q holds no dotted event-name literal", listText)
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m[1]
	}
	return names
}

// anScopedEvents splits the generated column's CASE into its two dispatch lists,
// keyed by which payload spelling the THEN clause reads -- not by branch order,
// since the migration is free to write either branch first.
func anScopedEvents(t *testing.T, body string) (idEvents, invoiceIDEvents []string) {
	t.Helper()
	locs := anEventInRE.FindAllStringSubmatchIndex(body, -1)
	if len(locs) != 2 {
		t.Fatalf("the generated expression holds %d event IN (...) branch(es), want exactly 2 (one per payload spelling) -- the derivation is what makes this scan self-widening, so a parse that reads one branch guards half the writers", len(locs))
	}
	for i, loc := range locs {
		thenEnd := len(body)
		if i+1 < len(locs) {
			thenEnd = locs[i+1][0]
		}
		names := anEventNames(t, body[loc[2]:loc[3]])
		thenText := body[loc[1]:thenEnd]
		switch {
		case strings.Contains(thenText, "->>'invoice_id'"):
			invoiceIDEvents = append(invoiceIDEvents, names...)
		case strings.Contains(thenText, "->>'id'"):
			idEvents = append(idEvents, names...)
		default:
			t.Fatalf("branch %d names neither payload key spelling in its THEN clause: %q", i, thenText)
		}
	}
	return idEvents, invoiceIDEvents
}

// anScopedSet is the derived 17, as a set.
func anScopedSet(t *testing.T) map[string]bool {
	t.Helper()
	idEvents, invoiceIDEvents := anScopedEvents(t, auditInvoiceIDSection(t, "Up"))
	set := map[string]bool{}
	for _, e := range append(append([]string{}, idEvents...), invoiceIDEvents...) {
		set[e] = true
	}
	if len(idEvents) != 10 || len(invoiceIDEvents) != 7 || len(set) != 17 {
		t.Fatalf("derived %d id-keyed and %d invoice_id-keyed event(s), %d distinct, want 10 and 7 and 17 -- the split IS the guard on the regex", len(idEvents), len(invoiceIDEvents), len(set))
	}
	return set
}

// ---------------------------------------------------------------------------
// The call sites
// ---------------------------------------------------------------------------

// anSite is one audit.Record call. event is "" unless the event argument is a
// string literal; keys holds the payload composite literal's string keys, and
// inline says whether there was a composite literal to read at all.
type anSite struct {
	file   string
	line   int
	fn     string
	event  string
	keys   []string
	inline bool
	args   int
}

func (s anSite) String() string {
	name := s.event
	if name == "" {
		name = "<non-literal event>"
	}
	return s.file + ":" + strconv.Itoa(s.line) + " (" + name + " in " + s.fn + ")"
}

func (s anSite) hasKey(key string) bool {
	for _, k := range s.keys {
		if k == key {
			return true
		}
	}
	return false
}

func anStringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// anCompositeKeys reads the string keys off an inline composite literal.
func anCompositeKeys(e ast.Expr) (keys []string, inline bool) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := anStringLit(kv.Key); ok {
			keys = append(keys, k)
		}
	}
	return keys, true
}

// anIsRecord reports whether n is an audit.Record call, alias resolved so a
// renamed import cannot drop a site out of the population.
func anIsRecord(n ast.Node, alias string) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != anRecord {
		return nil, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != alias {
		return nil, false
	}
	return call, true
}

// anSitesIn returns the attributed sites and the file's unattributed total. The
// two must agree: a call outside every FuncDecl would otherwise vanish the moment
// attribution replaced the flat walk.
func anSitesIn(fset *token.FileSet, rel string, f *ast.File) (sites []anSite, total int) {
	alias := scImportAlias(f, anAuditPath, anAuditDef)
	if alias == "" {
		return nil, 0
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := anIsRecord(n, alias)
			if !ok {
				return true
			}
			s := anSite{file: rel, line: fset.Position(call.Pos()).Line, fn: fd.Name.Name, args: len(call.Args)}
			if s.args == anArgs {
				s.event, _ = anStringLit(call.Args[anEventArg])
				s.keys, s.inline = anCompositeKeys(call.Args[anPayloadArg])
			}
			sites = append(sites, s)
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := anIsRecord(n, alias); ok {
			total++
		}
		return true
	})
	return sites, total
}

// anAllSites walks cmd/ and internal/ once. scSeamFiles floors the walk over both
// roots; cmd/'s only audit.Record calls are cmd/submission's two audit adapters, and
// its file floor is what proves the cmd/ half ran.
func anAllSites(t *testing.T) []anSite {
	t.Helper()
	root := repoRootDir(t)
	files := scSeamFiles(t, root)
	fset := token.NewFileSet()
	var sites []anSite
	total := 0
	for _, rel := range files {
		s, n := anSitesIn(fset, rel, scParse(t, fset, root, rel))
		sites = append(sites, s...)
		total += n
	}
	if len(sites) != total {
		t.Fatalf("attributed %d audit.Record call(s) of %d found -- one sits outside every named func, and an exemption cannot be scoped to a func that does not exist", len(sites), total)
	}
	for _, s := range sites {
		if s.args != anArgs {
			t.Fatalf("%s takes %d argument(s), want %d -- audit.Record's shape changed and every argument position below reads the wrong thing", s, s.args, anArgs)
		}
	}
	return sites
}

// ---------------------------------------------------------------------------
// The exemption tables -- the three classes the scan cannot read forward
// ---------------------------------------------------------------------------

// anIndirect is one call site whose event or payload the scan cannot read at the
// call. Every entry must match EXACTLY ONE site: an entry matching nothing is
// stale, and a second call in the same func would otherwise hide behind it.
type anIndirect struct {
	file   string
	fn     string
	helper string   // the func whose composite literal is checked instead, when there is one
	events []string // the invoice-scoped events this site writes
	reason string
}

func (e anIndirect) String() string { return e.file + " func " + e.fn }

func (e anIndirect) covers(s anSite) bool { return s.file == e.file && s.fn == e.fn }

// anPayloadHelpers name their event literally but build the payload one hop away,
// in a helper whose own composite literal the scan reads instead. The two sites
// pass DIFFERENT argument forms, so keying on the argument would excuse one only.
var anPayloadHelpers = []anIndirect{
	{
		file: "internal/reconciliation/reconciliation.go", fn: "recordDriftAudit", helper: "driftPayload",
		events: []string{"reconciliation.drift_detected"},
		reason: "the payload argument is a call to driftPayload, whose composite literal is checked instead",
	},
	{
		file: "internal/reconciliation/reconciliation.go", fn: "recordAutoFixAudit", helper: "driftPayload",
		events: []string{"reconciliation.auto_fixed"},
		reason: "the payload argument is a local assigned from driftPayload, then extended with one key",
	},
}

// anConstructedEvents build the event name from a variable, so it never appears as
// a literal, AND take the payload as a parameter. Nothing structural reaches them.
var anConstructedEvents = []anIndirect{
	{
		file: "internal/submission/verdict_audit.go", fn: "recordSubmissionAudit",
		events: []string{"submission.accepted", "submission.rejected", "submission.failed"},
		reason: "the event is a concatenation and the payload arrives as a parameter; the key rests on internal/submission's behavioural tests, not on this scan",
	},
}

// anVariableEvents pick their event name in an if/else, so a forward-only walk
// keyed on a literal event skips them in silence. Their payload IS inline. None is
// invoice-scoped today; the coverage assertion below is what forces the next one
// to be reclassified rather than skipped.
var anVariableEvents = []anIndirect{
	{
		file: "internal/document/document.go", fn: "Upsert",
		reason: "document.created or document.reused, chosen by whether the row already existed",
	},
	{
		file: "internal/portfolio/store.go", fn: "SetStatus",
		reason: "portfolio.entity.onboarded or offboarded, chosen by the target status",
	},
	{
		file: "internal/tenancy/store.go", fn: "SetMembershipStatus",
		reason: "membership.suspended or reactivated, chosen by the target status",
	},
	{
		file: "internal/validation/store.go", fn: "ToggleRule",
		reason: "validation.rule.enabled or disabled, chosen by the target state",
	},
}

func anTables() map[string][]anIndirect {
	return map[string][]anIndirect{
		"anPayloadHelpers":    anPayloadHelpers,
		"anConstructedEvents": anConstructedEvents,
		"anVariableEvents":    anVariableEvents,
	}
}

// anIsDirect is the fourth class: the event is a string literal AND the payload is
// an inline composite literal, so the scan reads both at the call.
func anIsDirect(s anSite) bool { return s.event != "" && s.inline }

// ---------------------------------------------------------------------------
// Control needles -- the classifier proved able to report both outcomes
// ---------------------------------------------------------------------------

const anNeedleDirect = `package x

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

func f(ctx context.Context, tx any, id string) error {
	return audit.Record(ctx, tx, "system", "invoice.created", map[string]any{
		"id":             id,
		"invoice_number": "INV-1",
	})
}
`

const anNeedleMissingKey = `package x

import (
	"context"

	a "github.com/SimonOsipov/invoice-os/internal/audit"
)

func f(ctx context.Context, tx any, id string) error {
	return a.Record(ctx, tx, "system", "invoice.created", map[string]any{"id": id})
}
`

const anNeedleIndirect = `package x

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

func outer(ctx context.Context, tx any) error {
	return withTx(func() error {
		return audit.Record(ctx, tx, "system", "invoice.created", build())
	})
}
`

func anFixtureSites(t *testing.T, name, src string) []anSite {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	sites, total := anSitesIn(fset, name, f)
	if len(sites) != total {
		t.Fatalf("fixture %s: attributed %d of %d call(s)", name, len(sites), total)
	}
	return sites
}

func anControlNeedles(t *testing.T) {
	t.Run("N1 an inline payload carrying the key reads as direct and covered", func(t *testing.T) {
		sites := anFixtureSites(t, "n1.go", anNeedleDirect)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1", len(sites), sites)
		}
		if !anIsDirect(sites[0]) || !sites[0].hasKey(anNumberKey) {
			t.Fatalf("site = %+v -- the scan cannot report a covered writer, so its clearances mean nothing", sites[0])
		}
	})

	t.Run("N2 an inline payload missing the key is direct and NOT covered", func(t *testing.T) {
		sites := anFixtureSites(t, "n2.go", anNeedleMissingKey)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v behind an aliased import, want exactly 1 -- an aliased audit import would drop writers out of the population", len(sites), sites)
		}
		if !anIsDirect(sites[0]) || sites[0].hasKey(anNumberKey) {
			t.Fatalf("site = %+v -- a scan that cannot report a MISSING key reports all-clear forever", sites[0])
		}
	})

	t.Run("N3 a call inside a closure is attributed to the named func", func(t *testing.T) {
		sites := anFixtureSites(t, "n3.go", anNeedleIndirect)
		if len(sites) != 1 {
			t.Fatalf("found %d site(s) %v, want exactly 1", len(sites), sites)
		}
		if sites[0].fn != "outer" {
			t.Fatalf("site = %+v, want fn outer -- most writes sit inside a WithinTenantTx closure, and a literal has no name an exemption table can key on", sites[0])
		}
		if anIsDirect(sites[0]) {
			t.Fatalf("site = %+v read as direct -- a payload built elsewhere is not something this scan can read, and pretending otherwise excuses it silently", sites[0])
		}
	})

	t.Run("N4 the derivation reads branches by payload key, not by order", func(t *testing.T) {
		body := `CASE WHEN event IN ('a.one','a.two') THEN (payload->>'invoice_id')::uuid
		         WHEN event IN ('b.one') THEN (payload->>'id')::uuid END`
		idEvents, invoiceIDEvents := anScopedEvents(t, body)
		if len(idEvents) != 1 || len(invoiceIDEvents) != 2 {
			t.Fatalf("derived id=%v invoice_id=%v -- keying on branch order would swap the two lists the day the migration is rewritten", idEvents, invoiceIDEvents)
		}
	})
}

// ---------------------------------------------------------------------------
// AC-1 -- the 17 come from the migration, never from a Go list
// ---------------------------------------------------------------------------

func TestRLS_AuditNumberScanDerivesEventsFromTheMigration(t *testing.T) {
	t.Run("control needles", anControlNeedles)

	idEvents, invoiceIDEvents := anScopedEvents(t, auditInvoiceIDSection(t, "Up"))
	if len(idEvents) != 10 || len(invoiceIDEvents) != 7 {
		t.Fatalf("the generated column dispatches %d event(s) on the bare id key and %d on the invoice_id key, want 10 and 7 -- AUDIT-04-11 froze that split, so a change here changes which rows an invoice page can ever show", len(idEvents), len(invoiceIDEvents))
	}
	seen := map[string]bool{}
	for _, e := range append(append([]string{}, idEvents...), invoiceIDEvents...) {
		if seen[e] {
			t.Errorf("%q is dispatched by both branches -- one event cannot read two payload keys", e)
		}
		seen[e] = true
	}
	if len(seen) != 17 {
		t.Fatalf("derived %d distinct event(s) %v, want 17", len(seen), sortedKeys(seen))
	}
}

// ---------------------------------------------------------------------------
// AC-2 -- every literal-event writer in the 17-set carries the key
// ---------------------------------------------------------------------------

func TestRLS_EveryInvoiceScopedWriterCarriesTheNumber(t *testing.T) {
	scoped := anScopedSet(t)
	sites := anAllSites(t)

	covered := map[string]bool{}
	guarded := 0
	for _, s := range sites {
		if !anIsDirect(s) || !scoped[s.event] {
			continue
		}
		guarded++
		covered[s.event] = true
		if !s.hasKey(anNumberKey) {
			t.Errorf("%s writes an invoice-scoped event with payload keys %v and no %q -- the row it writes cannot say which invoice it is about, and AUDIT-11's whole claim is that every one of them can", s, s.keys, anNumberKey)
		}
	}
	// Never dedupe by event name: invoice.updated and invoice.validated each have
	// two writers, and clearing one would clear its twin unread.
	if guarded < 14 || len(covered) < 12 {
		t.Fatalf("checked %d literal-event site(s) covering %d event(s), want at least 14 and 12 (14 and 12 measured at AUDIT-11-06) -- a walk that reads nothing clears every writer at once", guarded, len(covered))
	}
}

// ---------------------------------------------------------------------------
// AC-3 -- the partition holds in both directions
// ---------------------------------------------------------------------------

func TestRLS_AuditNumberExemptionTablesMatchExactly(t *testing.T) {
	sites := anAllSites(t)

	// (a) every entry resolves to exactly one live site.
	for _, name := range sortedKeys(anTables()) {
		for _, e := range anTables()[name] {
			n := 0
			for _, s := range sites {
				if e.covers(s) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s entry %q matched %d site(s), want exactly 1 -- an entry matching nothing is a stale exemption nobody is looking at, and one matching two hides the second call behind the first", name, e, n)
			}
		}
	}

	// (b) every site lands in exactly one bucket, and the buckets sum to the whole.
	buckets := map[string]int{"direct": 0}
	for name := range anTables() {
		buckets[name] = 0
	}
	for _, s := range sites {
		var in []string
		if anIsDirect(s) {
			in = append(in, "direct")
		}
		for name, table := range anTables() {
			for _, e := range table {
				if e.covers(s) {
					in = append(in, name)
					break
				}
			}
		}
		switch len(in) {
		case 1:
			buckets[in[0]]++
		case 0:
			t.Errorf("%s is in no bucket -- its event or its payload is built somewhere this scan cannot read, so it would ship without the key in silence; classify it", s)
		default:
			sort.Strings(in)
			t.Errorf("%s is in %v -- two answers is no answer, and the count below would double-count it", s, in)
		}
	}
	sum := 0
	for _, n := range buckets {
		sum += n
	}
	if sum != len(sites) {
		t.Fatalf("the buckets hold %d of %d site(s) %v -- the partition has a hole", sum, len(sites), buckets)
	}

	// (c) the 17 are covered by direct sites plus the events the tables declare.
	scoped := anScopedSet(t)
	accounted := map[string]bool{}
	for _, s := range sites {
		if anIsDirect(s) && scoped[s.event] {
			accounted[s.event] = true
		}
	}
	for _, table := range [][]anIndirect{anPayloadHelpers, anConstructedEvents, anVariableEvents} {
		for _, e := range table {
			for _, ev := range e.events {
				if !scoped[ev] {
					t.Errorf("%s declares %q, which the migration does not dispatch to an invoice -- the exemption is answering a question nobody asked", e, ev)
					continue
				}
				accounted[ev] = true
			}
		}
	}
	for _, ev := range sortedKeys(scoped) {
		if !accounted[ev] {
			t.Errorf("%q is invoice-scoped and no writer this scan can read accounts for it -- either its writer builds the event or the payload indirectly and needs a table entry naming it, or nothing writes it at all", ev)
		}
	}

	// driftPayload is the one hop the scan does follow.
	anAssertHelperCarriesTheKey(t)
}

// anAssertHelperCarriesTheKey checks the composite literal inside each named
// payload helper, the only indirection this scan can follow.
func anAssertHelperCarriesTheKey(t *testing.T) {
	t.Helper()
	root := repoRootDir(t)
	fset := token.NewFileSet()
	// Two entries name one helper; check it once so the message is not doubled.
	seen := map[string]bool{}
	for _, e := range anPayloadHelpers {
		if e.helper == "" || seen[e.file+" "+e.helper] {
			continue
		}
		seen[e.file+" "+e.helper] = true
		f := scParse(t, fset, root, e.file)
		found, has := false, false
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != e.helper || fd.Body == nil {
				continue
			}
			found = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				expr, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				keys, inline := anCompositeKeys(expr)
				if !inline {
					return true
				}
				for _, k := range keys {
					if k == anNumberKey {
						has = true
					}
				}
				return true
			})
		}
		if !found {
			t.Errorf("%s declares no func %s -- the exemption names a helper that no longer exists", e.file, e.helper)
			continue
		}
		if !has {
			t.Errorf("%s func %s builds no composite literal holding %q -- the writers it feeds are exempt from the call-site check on the promise that the key lives here", e.file, e.helper, anNumberKey)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-4 -- the control needle and the floors
// ---------------------------------------------------------------------------

func TestRLS_AuditNumberScanFindsItsControlNeedle(t *testing.T) {
	sites := anAllSites(t)
	var found []anSite
	for _, s := range sites {
		if s.event == anControlEvent {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the walk found %d writer(s) of %q, want exactly 1 -- a walk that parsed nothing reports a clean sweep of an empty set, and this needle is what proves it read the repo", len(found), anControlEvent)
	}
	s := found[0]
	if !anIsDirect(s) {
		t.Fatalf("%s did not read as a literal event with an inline payload -- the classifier has drifted, and every clearance above is answering a different question", s)
	}
	if !s.hasKey("id") || !s.hasKey("tin") {
		t.Errorf("%s has payload keys %v, want id and tin -- the needle is meant to be a real writer read in full, not a name matched in passing", s, s.keys)
	}
	if s.hasKey(anNumberKey) {
		t.Errorf("%s carries %q -- the needle is a NON-scoped writer, and one that carries the key can no longer prove the scan reports both outcomes", s, anNumberKey)
	}
}

func TestRLS_AuditNumberScanPopulationFloor(t *testing.T) {
	sites := anAllSites(t)
	byFile := map[string][]anSite{}
	for _, s := range sites {
		byFile[s.file] = append(byFile[s.file], s)
	}
	if len(sites) < anMinCalls || len(byFile) < anMinFiles {
		t.Fatalf("found %d audit.Record call(s) across %d file(s) %v, want at least %d across at least %d (35 across 13 measured at AUDIT-11-06) -- zero is what a broken walk and a repo with no writers both look like; these are CALLS, not the 41 text occurrences, four of which are doc comments", len(sites), len(byFile), sortedKeys(byFile), anMinCalls, anMinFiles)
	}
}

// ---------------------------------------------------------------------------
// AC-5 -- the read contract no longer denies the number
// ---------------------------------------------------------------------------

// anDocRule is one phrase the corrected sections must, or must not, carry.
type anDocRule struct {
	section string
	phrase  string
	reason  string
}

// anForbidden is one entry per false sentence AUDIT-11 leaves behind. Matched
// case-insensitively on whitespace-normalised text.
var anForbidden = []anDocRule{
	{"10.12", "no writer records an invoice number", "17 event families now record the key"},
	{"10.12", "no writer puts the human-facing number", "every invoice-scoped writer puts it there"},
	{"10.8", "nothing to match", "a typed number now reaches rows through the invoices fold-in"},
	{"10.12", "should not promise otherwise", "the audit screen promises exactly that"},
	{"10.8", "exactly four OR-ed arms", "there are five"},
	{"10.8", "two fold-in", "there are three, and the third resolves a number"},
	{"10.8", "3 of 27 keys", "a corpus-dependent denominator this page's own 10.7 says not to pin"},
}

// anRequired is what stops a delete-only correction from passing.
var anRequired = []anDocRule{
	{"10.8", "five OR-ed arms", "the arm count must be restated, not deleted"},
	{"10.8", "three fold-in", "the fold-in count must be restated, not deleted"},
	{"10.8", "a.invoice_id = ANY", "the arm a typed number actually reaches"},
	{"10.8", "kv.key <> 'invoice_number'", "the generic arm skips the recorded key, the opposite of what the old prose implied"},
	{"10.12", "invoice_number", "the section must name the key writers now record"},
}

// anDocSection returns one numbered subsection, heading line included, with every
// whitespace run collapsed to one space. Re-wrapping a markdown paragraph would
// otherwise split a phrase across a newline and make its absence vacuous.
func anDocSection(t *testing.T, doc, number string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	var starts []int
	for i, line := range lines {
		if !strings.HasPrefix(line, "### ") {
			continue
		}
		if fields := strings.Fields(strings.TrimPrefix(line, "### ")); len(fields) > 0 && fields[0] == number {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("%s holds %d heading(s) numbered %s, want exactly 1 -- a renamed, deleted or duplicated heading leaves this oracle reading the wrong prose, or none", anDocPath, len(starts), number)
	}
	end := len(lines)
	for i := starts[0] + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") || strings.HasPrefix(lines[i], "### ") {
			end = i
			break
		}
	}
	body := strings.TrimSpace(whitespaceRun.ReplaceAllString(strings.Join(lines[starts[0]:end], " "), " "))
	if n := len([]rune(body)); n < anMinSectionRunes {
		t.Fatalf("%s section %s carries %d rune(s), want at least %d -- an absence check over an emptied section always passes", anDocPath, number, n, anMinSectionRunes)
	}
	return body
}

func TestRLS_AuditDocReadContractNoLongerDeniesTheNumber(t *testing.T) {
	root := repoRootDir(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(anDocPath)))
	if err != nil {
		t.Fatalf("read %s: %v -- three downstream stories cite this page as fact, and this oracle is what keeps it true", anDocPath, err)
	}
	doc := string(raw)

	if n := strings.Count(doc, "\n"+anDocSubheadPrefix); n < anMinDocSubheads {
		t.Fatalf("%s holds %d %q subsection(s), want at least %d (12 measured at AUDIT-11-06) -- a truncated file passes every absence check below", anDocPath, n, anDocSubheadPrefix, anMinDocSubheads)
	}

	bodies := map[string]string{
		"10.8":  anDocSection(t, doc, "10.8"),
		"10.12": anDocSection(t, doc, "10.12"),
	}

	// Present-needle: proves the parse landed on live prose, not on nothing.
	if !strings.Contains(bodies["10.8"], "jsonb_each_text") {
		t.Fatalf("%s section 10.8 no longer names jsonb_each_text -- the extraction is reading the wrong section, so every phrase check below is vacuous", anDocPath)
	}

	for _, r := range anForbidden {
		if strings.Contains(strings.ToLower(bodies[r.section]), strings.ToLower(r.phrase)) {
			t.Errorf("%s section %s still says %q -- %s", anDocPath, r.section, r.phrase, r.reason)
		}
	}
	for _, r := range anRequired {
		if !strings.Contains(strings.ToLower(bodies[r.section]), strings.ToLower(r.phrase)) {
			t.Errorf("%s section %s does not say %q -- %s; deleting the false sentence is not the same as correcting it", anDocPath, r.section, r.phrase, r.reason)
		}
	}
}
