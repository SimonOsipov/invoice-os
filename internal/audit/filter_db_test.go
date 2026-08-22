// AUDIT-04-03: the five filters against a real Postgres — each alone, all composing, the
// three company modes, free-text's four routes, and the two absence-shaped claims (no
// event type is suppressed; search cannot match an invoice number).
//
// Helpers use a filt* prefix. Rows seed by raw INSERT with an explicit created_at grid:
// audit_log.created_at defaults to now(), which Postgres freezes for a whole transaction,
// so rows written in one tx would tie and every date-filter assertion would be vacuous.
package audit_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- seeding --------------------------------------------------------------------------

// filtRow is one audit row to insert. Unlike pageInsert's rows it carries its own actor,
// which is what makes a multi-actor fixture possible.
type filtRow struct {
	event      string
	actor      string
	payload    string
	ageSeconds int
}

// filtSeeded is a written row plus the attributes the Go-side oracle filters on. entity
// is what the BEFORE INSERT resolver actually stored, read back rather than predicted.
type filtSeeded struct {
	filtRow
	id      int64
	created time.Time
	entity  *string
	note    string
}

// filtInsert writes rows with explicit created_at and reads back the id, the timestamp
// and the resolver's entity_id. Reading entity_id back (rather than asserting what it
// ought to be) keeps the oracle honest about what the trigger really did.
func filtInsert(t *testing.T, f *fixture, p pageFixture, rows []filtRow) []filtSeeded {
	t.Helper()
	ctx := context.Background()
	out := make([]filtSeeded, 0, len(rows))
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		for _, r := range rows {
			var s filtSeeded
			s.filtRow = r
			err := tx.QueryRow(ctx, `
				INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
				VALUES ($1, $2, $3, $4::jsonb, now() - ($5 * interval '1 second'))
				RETURNING id, created_at, entity_id`,
				p.tenant, r.actor, r.event, r.payload, r.ageSeconds).
				Scan(&s.id, &s.created, &s.entity)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert %d audit rows: %v", len(rows), err)
	}
	return out
}

// filtSeedMembership commits one membership whose user_id is the actor string a test
// stores on its audit rows, so the display-name fold-in has something to resolve.
// tenant_id, user_id and role are the three NOT NULL columns without a default; role is
// an FK to roles(name), whose legal values are admin, preparer and reviewer.
func filtSeedMembership(t *testing.T, f *fixture, p pageFixture, userID, displayName string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, display_name) VALUES ($1, $2, 'admin', $3)`,
			p.tenant, userID, displayName)
		return e
	}); err != nil {
		t.Fatalf("seed membership %s: %v", displayName, err)
	}
}

// --- the AC-1/AC-2 corpus ---------------------------------------------------------------

// filtCorpus is the shared 40-row fixture: four events, three actors (one of them the
// literal "system", so the actor-class filter has both sides), three company slots
// (two real companies plus an unattributable one) and three payload notes, spread over
// 60 days. Index 0 is the newest row, so a page ordered newest-first is index order.
type filtCorpus struct {
	p       pageFixture
	rows    []filtSeeded
	entityA string
	entityB string
	actors  []string
	events  []string
	notes   []string
}

var (
	filtEvents = []string{"invoice.created", "invoice.updated", "invoice.validated", "invoice.transitioned"}
	filtNotes  = []string{"alpha", "bravo", "charlie"}
)

// filtBuildCorpus seeds the shared fixture. Company attribution rides on the payload's
// invoice id, which the BEFORE INSERT resolver joins to invoices — that is what lets the
// event and the company vary independently. The third slot carries an invoice id that
// matches no row, so the resolver stores NULL.
func filtBuildCorpus(t *testing.T, f *fixture) filtCorpus {
	t.Helper()
	c := filtCorpus{
		p:      pageSeedTenant(t, f),
		events: filtEvents,
		notes:  filtNotes,
		// "system" is the class the ActorKind filter splits on; the other two are
		// ordinary subjects.
		actors: []string{uuid.NewString(), uuid.NewString(), "system"},
	}
	c.entityA = pageSeedEntity(t, f, c.p, "Acme Holdings")
	c.entityB = pageSeedEntity(t, f, c.p, "Borealis Ltd")
	invoiceA := pageSeedInvoice(t, f, c.p, c.entityA)
	invoiceB := pageSeedInvoice(t, f, c.p, c.entityB)
	orphan := uuid.NewString() // resolves to NULL: no invoices row has this id

	const rows = 40
	invoices := []string{invoiceA, invoiceB, orphan}
	in := make([]filtRow, 0, rows)
	notes := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		note := filtNotes[(i/5)%len(filtNotes)]
		notes = append(notes, note)
		in = append(in, filtRow{
			event:   filtEvents[i%len(filtEvents)],
			actor:   c.actors[i%len(c.actors)],
			payload: fmt.Sprintf(`{"id":%q,"note":%q}`, invoices[(i/4)%len(invoices)], note),
			// 1.5 days apart: 40 rows spanning 58.5 days.
			ageSeconds: i * 129600,
		})
	}
	c.rows = filtInsert(t, f, c.p, in)
	for i := range c.rows {
		c.rows[i].note = notes[i]
	}

	// The corpus is the oracle for every AC-1 case, so a degenerate assignment would
	// make those cases claim less than they read as claiming.
	filtAssertCorpusShape(t, c)
	return c
}

// filtAssertCorpusShape fails before any Query when the fixture does not actually vary
// along all four dimensions, which is the only thing that makes "excludes only its own
// rows" a real assertion.
func filtAssertCorpusShape(t *testing.T, c filtCorpus) {
	t.Helper()
	if len(c.rows) != 40 {
		t.Fatalf("corpus has %d rows, want 40", len(c.rows))
	}
	events, actors, entities, notes := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range c.rows {
		events[r.event]++
		actors[r.actor]++
		notes[r.note]++
		if r.entity == nil {
			entities["<null>"]++
		} else {
			entities[*r.entity]++
		}
	}
	if len(events) != 4 || len(actors) != 3 || len(entities) != 3 || len(notes) != 3 {
		t.Fatalf("corpus spread = %d events, %d actors, %d company slots, %d notes; want 4/3/3/3\n"+
			"events=%v actors=%v entities=%v notes=%v",
			len(events), len(actors), len(entities), len(notes), events, actors, entities, notes)
	}
	if entities[c.entityA] == 0 || entities[c.entityB] == 0 || entities["<null>"] == 0 {
		t.Fatalf("corpus company slots = %v, want all three populated", entities)
	}
	if actors["system"] == 0 {
		t.Fatalf("corpus has no system actor; the actor-class filter would have only one side")
	}
	// Strictly descending created_at, so index order IS newest-first order.
	for i := 1; i < len(c.rows); i++ {
		if !c.rows[i].created.Before(c.rows[i-1].created) {
			t.Fatalf("row %d created_at %v is not older than row %d's %v — the grid collapsed",
				i, c.rows[i].created, i-1, c.rows[i-1].created)
		}
	}
}

// filtExpect is the Go-side oracle: the corpus rows matching pred, newest first.
func filtExpect(c filtCorpus, pred func(filtSeeded) bool) []int64 {
	out := []int64{}
	for _, r := range c.rows {
		if pred(r) {
			out = append(out, r.id)
		}
	}
	return out
}

// filtAssertPage compares a page against the oracle and refuses a vacuous comparison:
// an empty expectation, or one that happens to be the whole corpus, proves nothing about
// a filter.
func filtAssertPage(t *testing.T, label string, got audit.Response, c filtCorpus, want []int64) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: the oracle expects 0 rows, so this case cannot distinguish a working "+
			"filter from a broken one", label)
	}
	if len(want) == len(c.rows) {
		t.Fatalf("%s: the oracle expects all %d rows, so this case cannot detect a filter that "+
			"was never applied", label, len(c.rows))
	}
	if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
		t.Errorf("%s: page ids = %v, want %v", label, ids, want)
	}
	if got.Total != len(want) {
		t.Errorf("%s: total = %d, want %d — the filter reached the page but not the count",
			label, got.Total, len(want))
	}
}

// --- AC #7: no event type is suppressed by default ---------------------------------------

// filtAllPrefixEvents is one event per dotted prefix plus three extras, so the fixture
// clears the twelve-row floor while covering all nine prefixes.
var filtAllPrefixEvents = []string{
	"invoice.created", "invoice.updated",
	"submission.accepted",
	"reconciliation.drift_detected",
	"portfolio.entity.created",
	"approval_policy.published",
	"workflow_role.created", "workflow_role.staffed",
	"membership.suspended",
	"validation.rule.enabled",
	"document.created", "document.read",
}

func TestAuditRead_DefaultResponseSuppressesNoEventType(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	rows := make([]filtRow, 0, len(filtAllPrefixEvents))
	for i, e := range filtAllPrefixEvents {
		rows = append(rows, filtRow{event: e, actor: "seed-actor", payload: "{}", ageSeconds: i * 3600})
	}
	filtInsert(t, f, p, rows)

	// Population floor, read back in SQL rather than counted off the Go fixture: a row
	// that failed to insert would not show up in a slice length.
	var seededRows, seededPrefixes int
	if err := db.WithinTenantTx(context.Background(), f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*), count(DISTINCT split_part(event, '.', 1)) FROM audit_log`).
			Scan(&seededRows, &seededPrefixes)
	}); err != nil {
		t.Fatalf("population floor: %v", err)
	}
	if seededRows < 12 {
		t.Fatalf("population floor: %d rows landed, want at least 12", seededRows)
	}
	if seededPrefixes != 9 {
		t.Fatalf("population floor: %d distinct event prefixes, want 9 (the whole vocabulary)", seededPrefixes)
	}

	// Limit above the seeded count, or "the returned set equals the seeded set" would be
	// a claim about one page and would pass vacuously once the fixture outgrew the limit.
	got := pageQuery(t, f, p, audit.Filter{Limit: len(filtAllPrefixEvents) + 10})
	if got.Page.HasMore {
		t.Fatalf("has_more = true at limit %d over %d rows; the set comparison below would only "+
			"cover one page", len(filtAllPrefixEvents)+10, seededRows)
	}

	seen := map[string]bool{}
	for _, e := range got.Events {
		seen[e.Event] = true
	}
	for _, want := range filtAllPrefixEvents {
		if !seen[want] {
			t.Errorf("event %q is absent from an unfiltered response — no event type may be suppressed", want)
		}
		delete(seen, want)
	}
	for extra := range seen {
		t.Errorf("event %q appeared but was never seeded", extra)
	}
	// The control needle, named by the story: the first event any "hide the noisy reads"
	// default would drop.
	needle := false
	for _, e := range got.Events {
		if e.Event == "document.read" {
			needle = true
		}
	}
	if !needle {
		t.Errorf("document.read is missing from an unfiltered response (control needle)")
	}
}

// --- AC #1: each filter alone ------------------------------------------------------------

func TestAuditFilter_EachFilterAloneExcludesOnlyItsOwnRows(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)
	limit := len(c.rows) + 5

	from, to := c.rows[20].created, c.rows[10].created

	cases := []struct {
		label  string
		filter audit.Filter
		pred   func(filtSeeded) bool
	}{
		{
			label:  "date range",
			filter: audit.Filter{Limit: limit, From: from, To: to},
			pred:   func(r filtSeeded) bool { return !r.created.Before(from) && !r.created.After(to) },
		},
		{
			label:  "event multi-select",
			filter: audit.Filter{Limit: limit, Events: []string{filtEvents[0], filtEvents[2]}},
			pred:   func(r filtSeeded) bool { return r.event == filtEvents[0] || r.event == filtEvents[2] },
		},
		{
			label:  "named actors",
			filter: audit.Filter{Limit: limit, Actors: []string{c.actors[0]}},
			pred:   func(r filtSeeded) bool { return r.actor == c.actors[0] },
		},
		{
			label:  "actor class system",
			filter: audit.Filter{Limit: limit, ActorKind: "system"},
			pred:   func(r filtSeeded) bool { return r.actor == "system" },
		},
		{
			label:  "actor class people",
			filter: audit.Filter{Limit: limit, ActorKind: "people"},
			pred:   func(r filtSeeded) bool { return r.actor != "system" },
		},
		{
			label:  "named company",
			filter: audit.Filter{Limit: limit, Company: audit.NamedCompany(c.entityA)},
			pred:   func(r filtSeeded) bool { return r.entity != nil && *r.entity == c.entityA },
		},
		{
			label:  "workspace only",
			filter: audit.Filter{Limit: limit, Company: audit.WorkspaceOnly()},
			pred:   func(r filtSeeded) bool { return r.entity == nil },
		},
		{
			label:  "free text over the payload",
			filter: audit.Filter{Limit: limit, Q: filtNotes[1]},
			pred:   func(r filtSeeded) bool { return r.note == filtNotes[1] },
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := pageQuery(t, f, c.p, tc.filter)
			filtAssertPage(t, tc.label, got, c, filtExpect(c, tc.pred))
		})
	}
}

// --- AC #2: all five compose to the intersection ------------------------------------------

func TestAuditFilter_AllFiveComposeToTheIntersection(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	// Derive the filter set from a row that really exists, so the intersection cannot be
	// empty for an uninteresting reason.
	var target filtSeeded
	for _, r := range c.rows {
		if r.entity != nil && *r.entity == c.entityA && r.actor != "system" {
			target = r
			break
		}
	}
	if target.id == 0 {
		t.Fatalf("no corpus row is both attributed to %s and written by a person", c.entityA)
	}
	from, to := c.rows[len(c.rows)-1].created, c.rows[0].created

	filter := audit.Filter{
		Limit:     len(c.rows) + 5,
		From:      from,
		To:        to,
		Events:    []string{target.event},
		ActorKind: "people",
		Company:   audit.NamedCompany(c.entityA),
		Q:         target.note,
	}
	pred := func(r filtSeeded) bool {
		return !r.created.Before(from) && !r.created.After(to) &&
			r.event == target.event &&
			r.actor != "system" &&
			r.entity != nil && *r.entity == c.entityA &&
			r.note == target.note
	}
	want := filtExpect(c, pred)

	got := pageQuery(t, f, c.p, filter)
	filtAssertPage(t, "all five composed", got, c, want)

	// The control: a row satisfying four of the five must be absent. Relaxing only the
	// event predicate is what catches a builder that drops one fragment.
	fourOfFive := filtExpect(c, func(r filtSeeded) bool {
		return !r.created.Before(from) && !r.created.After(to) &&
			r.event != target.event &&
			r.actor != "system" &&
			r.entity != nil && *r.entity == c.entityA &&
			r.note == target.note
	})
	if len(fourOfFive) == 0 {
		t.Fatalf("no control row satisfies four of the five filters, so the composition claim is untested")
	}
	inPage := map[int64]bool{}
	for _, id := range pageIDs(t, got) {
		inPage[id] = true
	}
	for _, id := range fourOfFive {
		if inPage[id] {
			t.Errorf("row %d satisfies only four of the five filters but is in the page — "+
				"the filters are composing as OR, not AND", id)
		}
	}
}

// --- AC #4: the three company modes partition the log --------------------------------------

func TestAuditFilter_CompanyPartitionsAreDisjointAndExhaustive(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)
	limit := len(c.rows) + 5

	all := pageQuery(t, f, c.p, audit.Filter{Limit: limit, Company: audit.AllCompanies()})
	a := pageQuery(t, f, c.p, audit.Filter{Limit: limit, Company: audit.NamedCompany(c.entityA)})
	b := pageQuery(t, f, c.p, audit.Filter{Limit: limit, Company: audit.NamedCompany(c.entityB)})
	ws := pageQuery(t, f, c.p, audit.Filter{Limit: limit, Company: audit.WorkspaceOnly()})

	for label, r := range map[string]audit.Response{"all": all, "company A": a, "company B": b, "workspace": ws} {
		if r.Total == 0 {
			t.Fatalf("%s returned 0 rows; a partition claim over an empty part proves nothing", label)
		}
	}
	if sum := a.Total + b.Total + ws.Total; sum != all.Total {
		t.Errorf("company A (%d) + company B (%d) + workspace (%d) = %d, want the all-companies "+
			"total %d — the three modes must partition the log", a.Total, b.Total, ws.Total, sum, all.Total)
	}

	// Disjoint, not merely equinumerous: a named company must return no workspace-level
	// row and vice versa.
	for _, e := range a.Events {
		if e.EntityID == nil {
			t.Errorf("company A's page holds a workspace-level row (%s)", e.ID)
		}
	}
	for _, e := range ws.Events {
		if e.EntityID != nil {
			t.Errorf("the workspace page holds a company row (%s, entity %s)", e.ID, *e.EntityID)
		}
	}
}

// --- AC #5: free text's four routes ---------------------------------------------------------

func TestAuditSearch_MatchesEventPayloadActorNameAndCompanyName(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	// Each row is reachable by exactly one route. Actors are pairwise distinct because
	// the actor route folds to actor = ANY(...), which would otherwise sweep in a
	// row it was not meant to reach; the same reasoning applies to entity_id.
	zanzibar := pageSeedEntity(t, f, p, "Zanzibar Trading")
	named := uuid.NewString()
	filtSeedMembership(t, f, p, named, "Wilhelmina Thorne")

	rows := filtInsert(t, f, p, []filtRow{
		{event: "validation.rule.enabled", actor: uuid.NewString(), payload: `{}`, ageSeconds: 40},
		{event: "membership.suspended", actor: uuid.NewString(), payload: `{"note":"quokka"}`, ageSeconds: 30},
		{event: "workflow_role.created", actor: named, payload: `{}`, ageSeconds: 20},
		{event: "portfolio.entity.created", actor: uuid.NewString(), payload: fmt.Sprintf(`{"id":%q}`, zanzibar), ageSeconds: 10},
	})
	byRoute := map[string]filtSeeded{
		"event":   rows[0],
		"payload": rows[1],
		"actor":   rows[2],
		"company": rows[3],
	}
	if rows[3].entity == nil || *rows[3].entity != zanzibar {
		t.Fatalf("the company row resolved to entity %v, want %s — the fold-in would have nothing to match",
			rows[3].entity, zanzibar)
	}

	for route, term := range map[string]string{
		"event":   "validation.rule",
		"payload": "quokka",
		"actor":   "Wilhelmina",
		"company": "Zanzibar",
	} {
		t.Run(route, func(t *testing.T) {
			got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: term})
			want := []int64{byRoute[route].id}
			if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
				t.Errorf("q=%q matched %v, want exactly %v (the %s route's row and only it)",
					term, ids, want, route)
			}
		})
	}
}

func TestAuditSearch_CannotMatchAnInvoiceNumberBecauseNoneIsRecorded(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Ledger Co")
	invoice := pageSeedInvoice(t, f, p, entity)

	// Derived from the package's own rule sets, so a new invoice-scoped event raises the
	// floor automatically instead of silently shrinking it.
	if len(triggerRuleAEvents)+len(triggerRuleBEvents) != 17 {
		t.Fatalf("the invoice-scoped vocabulary is %d events, want 17 — this test's floor moved",
			len(triggerRuleAEvents)+len(triggerRuleBEvents))
	}

	ctx := context.Background()
	for _, e := range triggerRuleAEvents {
		recordAudit(t, f, p.tenant, e, map[string]any{"id": invoice})
	}
	for _, e := range triggerRuleBEvents {
		recordAudit(t, f, p.tenant, e, map[string]any{"invoice_id": invoice})
	}

	var seeded, withNumberKey int
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE jsonb_exists(payload, 'invoice_number'))
			  FROM audit_log`).Scan(&seeded, &withNumberKey)
	}); err != nil {
		t.Fatalf("population floor: %v", err)
	}
	if seeded != 17 {
		t.Fatalf("population floor: %d rows landed, want 17", seeded)
	}
	if withNumberKey != 0 {
		t.Fatalf("%d payloads carry an invoice_number key; D-17's premise no longer holds and this "+
			"AC must be rewritten rather than re-asserted", withNumberKey)
	}

	// Positive control FIRST: without it, the zero-rows assertion below is
	// indistinguishable from search being unwired, and would pass green on a stub.
	present := pageQuery(t, f, p, audit.Filter{Limit: 50, Q: invoice})
	if len(present.Events) == 0 {
		t.Fatalf("searching the invoice id %s returned 0 rows; search is not working, so the "+
			"absence assertion below would prove nothing", invoice)
	}

	// The full number including its INV- prefix: the bare 8 hex characters are also a
	// prefix of the uuid in every payload, so searching them would match the payload
	// route and produce a false red.
	number := "INV-" + invoice[:8]
	if strings.Contains(invoice, number) {
		t.Fatalf("the invoice number %q is a substring of the invoice id; pick a needle that is not", number)
	}
	absent := pageQuery(t, f, p, audit.Filter{Limit: 50, Q: number})
	if len(absent.Events) != 0 {
		t.Errorf("searching the invoice number %q matched %d rows; no writer records an invoice "+
			"number, so nothing should match", number, len(absent.Events))
	}
	if absent.Total != 0 {
		t.Errorf("searching the invoice number %q reported total %d, want 0", number, absent.Total)
	}
}

// --- AC #6: an empty value applies no filter, and a missing term applies one ---------------

func TestAuditFilter_EmptyValueAppliesNoFilter(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)
	limit := len(c.rows) + 5

	unfiltered := pageQuery(t, f, c.p, audit.Filter{Limit: limit})
	if len(unfiltered.Events) != len(c.rows) {
		t.Fatalf("the unfiltered page has %d rows, want %d", len(unfiltered.Events), len(c.rows))
	}

	empty := audit.Filter{
		Limit:     limit,
		Events:    []string{},
		Actors:    []string{},
		ActorKind: "",
		Company:   audit.AllCompanies(),
		Q:         "",
	}
	got := pageQuery(t, f, c.p, empty)
	if ids, want := pageIDs(t, got), pageIDs(t, unfiltered); !pageEqualIDs(ids, want) {
		t.Errorf("empty filter values changed the page: got %v, want the unfiltered %v", ids, want)
	}
	if got.Total != unfiltered.Total {
		t.Errorf("empty filter values changed total: got %d, want %d", got.Total, unfiltered.Total)
	}
}

// TestAuditFilter_SearchThatMatchesNothingReturnsZeroRows is the mirror of the case above
// and the one that matters. Both fold-in lookups come back empty here, and a builder that
// drops the whole search fragment when that happens returns the UNFILTERED set — which
// TestAuditFilter_EmptyValueAppliesNoFilter would read as correct behaviour.
func TestAuditFilter_SearchThatMatchesNothingReturnsZeroRows(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	const needle = "zzzznomatchzzzz"
	for _, r := range c.rows {
		if strings.Contains(r.payload, needle) || strings.Contains(r.event, needle) {
			t.Fatalf("the needle %q occurs in the corpus; pick one that does not", needle)
		}
	}

	got := pageQuery(t, f, c.p, audit.Filter{Limit: len(c.rows) + 5, Q: needle})
	if len(got.Events) != 0 {
		t.Errorf("q=%q matched %d rows, want 0 — an unmatched search must narrow to nothing, "+
			"never fall back to the unfiltered set", needle, len(got.Events))
	}
	if got.Total != 0 {
		t.Errorf("q=%q reported total %d, want 0", needle, got.Total)
	}
}

// TestAuditFilter_UnknownActorKindFailsTheQuery carries the fail-closed rule through to
// Query itself, not only to the pure builder.
func TestAuditFilter_UnknownActorKindFailsTheQuery(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	filtInsert(t, f, p, []filtRow{{event: "invoice.created", actor: "system", payload: "{}", ageSeconds: 10}})

	ctx := context.Background()
	err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, qErr := audit.Query(ctx, tx, audit.Filter{Limit: 10, ActorKind: "person"})
		return qErr
	})
	if err == nil {
		t.Fatalf("Query accepted ActorKind=\"person\"; an unrecognised kind must fail rather than " +
			"silently return the unfiltered set")
	}
}
