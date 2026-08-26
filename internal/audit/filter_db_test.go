// AUDIT-04-03: the five filters against a real Postgres — each alone, all composing, the
// three company modes, free-text's routes, and the absence-shaped claim that no event type
// is suppressed. AUDIT-11-09 added the fifth route (an invoice number, resolved through
// invoices) and turned the old "search cannot match a number" claim into its opposite.
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

// --- AC #5: free text's routes (four here; AUDIT-11-09 added the number as a fifth) ---------

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

// TestAuditSearch_DoesNotMatchAPayloadKeyName is R-A's reversal, pinned: search matches
// payload VALUES via jsonb_each_text, not payload::text, which was measured to also
// render key names (50,000/50,000 false positives on q="id"). Nothing else in this file
// distinguishes the two: every other search term here is also a value elsewhere in its
// row, so a revert to payload::text would leave the rest of the suite green.
func TestAuditSearch_DoesNotMatchAPayloadKeyName(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	const keyOnly = "zzzkeyonlyneedlezzz"
	payload := fmt.Sprintf(`{%q:"unrelated-value"}`, keyOnly)
	filtInsert(t, f, p, []filtRow{
		{event: "validation.rule.enabled", actor: uuid.NewString(), payload: payload, ageSeconds: 10},
	})

	got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: keyOnly})
	if len(got.Events) != 0 {
		t.Errorf("q=%q, present only as a payload KEY and never a value, matched %d rows, want 0 — "+
			"search must match payload contents, not key names (R-A)", keyOnly, len(got.Events))
	}
	if got.Total != 0 {
		t.Errorf("q=%q reported total %d, want 0", keyOnly, got.Total)
	}
}

// TestAuditSearch_PercentIsALiteralNotAWildcard pins escapeLike, copied from
// internal/invoice/store.go because that package imports this one. A literal "%" in q
// must not become an ILIKE wildcard: unescaped, q="%" would match every row in the
// tenant (internal/invoice's own TestStoreList_QueryMatchesNumberOrBuyer proves the same
// hazard there). The positive half proves escaping does not also break a genuine literal
// "%" in stored data.
func TestAuditSearch_PercentIsALiteralNotAWildcard(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	rows := filtInsert(t, f, p, []filtRow{
		{event: "validation.rule.enabled", actor: uuid.NewString(), payload: `{"note":"fifty percent"}`, ageSeconds: 20},
		{event: "membership.suspended", actor: uuid.NewString(), payload: `{"note":"50% off"}`, ageSeconds: 10},
	})

	// Unescaped, "%" is a wildcard matching everything; escaped, it must match only the
	// row whose payload holds a literal "%".
	got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: "%"})
	want := []int64{rows[1].id}
	if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
		t.Errorf("q=\"%%\" matched %v, want exactly %v (the row with a literal %% in its payload) — "+
			"an unescaped q would match every row in the tenant", ids, want)
	}

	// The fuller literal, to rule out an escape that neutralises "%" into matching
	// nothing at all rather than matching it literally.
	got = pageQuery(t, f, p, audit.Filter{Limit: 20, Q: "50%"})
	if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
		t.Errorf("q=\"50%%\" matched %v, want exactly %v", ids, want)
	}
}

// TestAuditSearch_NumberReachesRowsOnlyThroughTheResolvedArm is AUDIT-11-09 AC #4's
// inertness spec, and the rewrite of the case that asserted AUDIT-04 D-17's premise — no
// writer records an invoice number — which this story deletes. Its own failure message
// said that premise must be rewritten rather than re-asserted.
//
// The fixture is unchanged and is exactly what the new claim needs: 17 invoice-scoped
// events whose payloads carry no invoice_number key, so the number can reach them only
// through the fold-in. A whole-table count can no longer prove inertness — AUDIT-11-01..04
// now write the key on most rows — so the assertion is fixture-local, with the
// Q=<invoice id> positive control keeping it honest.
func TestAuditSearch_NumberReachesRowsOnlyThroughTheResolvedArm(t *testing.T) {
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
	// The inertness premise, asserted inside this tenant rather than over the table: no
	// payload here holds the key, so the generic value arm can never reach these rows by
	// number and the resolved arm is the only path left.
	if withNumberKey != 0 {
		t.Fatalf("%d payloads carry an invoice_number key; this fixture must hold none, or the "+
			"claim below cannot tell the two arms apart", withNumberKey)
	}

	// Positive control FIRST: without it, the assertion below is indistinguishable from
	// search being unwired, and would pass green on a stub.
	present := pageQuery(t, f, p, audit.Filter{Limit: 50, Q: invoice})
	if len(present.Events) != seeded {
		t.Fatalf("searching the invoice id %s returned %d rows, want all %d; search is not working, "+
			"so the assertion below would prove nothing", invoice, len(present.Events), seeded)
	}

	// The full number including its INV- prefix: the bare 8 hex characters are also a
	// prefix of the uuid in every payload, so searching them would match the payload
	// route and produce a false red.
	number := "INV-" + invoice[:8]
	if strings.Contains(invoice, number) {
		t.Fatalf("the invoice number %q is a substring of the invoice id; pick a needle that is not", number)
	}
	got := pageQuery(t, f, p, audit.Filter{Limit: 50, Q: number})
	if len(got.Events) != seeded {
		t.Errorf("searching the invoice number %q matched %d rows, want all %d — the number now "+
			"resolves to this invoice, and no payload here carries the key, so the resolved arm "+
			"is the only way it can have reached them", number, len(got.Events), seeded)
	}
	if got.Total != seeded {
		t.Errorf("searching the invoice number %q reported total %d, want %d", number, got.Total, seeded)
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

// --- AUDIT-11-09: the number match is scoped by entity ------------------------------------

// filtSeedNumberedInvoice commits one invoice with a chosen number. pageSeedInvoice derives
// its number from the id, which cannot express two entities sharing one number.
func filtSeedNumberedInvoice(t *testing.T, f *fixture, p pageFixture, entityID, number string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			id, p.tenant, entityID, number)
		return e
	}); err != nil {
		t.Fatalf("seed invoice %q under %s: %v", number, entityID, err)
	}
	return id
}

// filtPayloadShape counts p's audit rows three ways: how many landed, how many the
// generated column attributed to an invoice, and how many carry the invoice_number key.
// Read in SQL rather than counted off the Go fixture: a row that failed to insert would
// not show up in a slice length.
func filtPayloadShape(t *testing.T, f *fixture, p pageFixture) (landed, withInvoice, withNumberKey int) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE invoice_id IS NOT NULL),
			       count(*) FILTER (WHERE jsonb_exists(payload, 'invoice_number'))
			  FROM audit_log`).Scan(&landed, &withInvoice, &withNumberKey)
	}); err != nil {
		t.Fatalf("payload shape: %v", err)
	}
	return landed, withInvoice, withNumberKey
}

// filtCountInvoices counts p's invoices whose number contains needle, under RLS.
func filtCountInvoices(t *testing.T, f *fixture, p pageFixture, needle string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM invoices WHERE invoice_number ILIKE '%' || $1 || '%'`, needle).Scan(&n)
	}); err != nil {
		t.Fatalf("count invoices matching %q: %v", needle, err)
	}
	return n
}

// TestAuditSearch_NumberScopedByCompanyMode is AUDIT-11-09 AC #3 and AC #8, and the reason
// the subtask exists: the user rejected accepting the cross-entity conflation and chose to
// fence it (D-24), then ruled on the residue the fence cannot reach (D-25). Both modes are
// pinned rather than left emergent, because emergent is how a decision silently reverses.
//
// Each entity carries TWO row shapes: a payload without the key, reachable only through the
// resolved arm, and one with it, the shape AUDIT-11-01..04 writes. Asserting only the
// second shape would pass on today's generic arm and prove nothing.
//
// This is also the behavioural guard on the arm's parenthesisation. An arm appended outside
// the OR-group makes every other predicate evaporate, so the two named-company cases would
// come back with the other company's rows.
func TestAuditSearch_NumberScopedByCompanyMode(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	const number = "INV-DUP-1"
	const nameA, nameB = "Alpha Freight", "Beta Logistics"
	entityA := pageSeedEntity(t, f, p, nameA)
	entityB := pageSeedEntity(t, f, p, nameB)
	// Legal: invoices_tenant_entity_number_uq is per-entity, which is exactly why the
	// number is not a unique handle and the fence is needed.
	invoiceA := filtSeedNumberedInvoice(t, f, p, entityA, number)
	invoiceB := filtSeedNumberedInvoice(t, f, p, entityB, number)

	rows := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, invoiceA), ageSeconds: 40},
		{event: "invoice.updated", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, invoiceA, number), ageSeconds: 30},
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, invoiceB), ageSeconds: 20},
		{event: "invoice.updated", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, invoiceB, number), ageSeconds: 10},
	})

	wantEntity, wantName := map[string]string{}, map[string]string{}
	for i, r := range rows {
		entity, name := entityA, nameA
		if i >= 2 {
			entity, name = entityB, nameB
		}
		// Read back what the BEFORE INSERT resolver stored rather than predicting it: an
		// unattributed row could never reach the resolved arm.
		if r.entity == nil || *r.entity != entity {
			t.Fatalf("row %d resolved to entity %v, want %s — the fence would have nothing to scope by",
				i, r.entity, entity)
		}
		key := fmt.Sprintf("%d", r.id)
		wantEntity[key], wantName[key] = entity, name
	}
	if entityA == entityB || nameA == nameB {
		t.Fatalf("the two companies are not distinct; D-25's label claim would be vacuous")
	}
	landed, withInvoice, withNumberKey := filtPayloadShape(t, f, p)
	if landed != 4 || withInvoice != 4 {
		t.Fatalf("fixture: %d rows landed and %d were attributed to an invoice, want 4 and 4",
			landed, withInvoice)
	}
	if withNumberKey != 2 {
		t.Fatalf("fixture: %d payloads carry invoice_number, want 2 — the fixture must hold both "+
			"row shapes or half the claim is untested", withNumberKey)
	}

	cases := []struct {
		label      string
		company    audit.CompanyFilter
		want       []int64
		wantLabels int
	}{
		// The fence: filterPredicates ANDs a.entity_id = $1, so the named company's rows
		// come back and the other company's do not — in both directions, so a predicate
		// that always picks the first entity fails.
		{"named company A", audit.NamedCompany(entityA), []int64{rows[1].id, rows[0].id}, 1},
		{"named company B", audit.NamedCompany(entityB), []int64{rows[3].id, rows[2].id}, 1},
		// D-25: the residue survives BY DECISION here. An explicit cross-company view is
		// an explicit request to see across companies, and company_name is what tells the
		// two same-numbered invoices apart. Do not "fix" this without reopening D-25.
		{"all companies", audit.AllCompanies(), []int64{rows[3].id, rows[2].id, rows[1].id, rows[0].id}, 2},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: number, Company: c.company})
			if ids := pageIDs(t, got); !pageEqualIDs(ids, c.want) {
				t.Errorf("%s: q=%q matched %v, want exactly %v", c.label, number, ids, c.want)
			}
			if got.Total != len(c.want) {
				t.Errorf("%s: total = %d, want %d — the fence reached the page but not the count",
					c.label, got.Total, len(c.want))
			}

			labels := map[string]bool{}
			for _, e := range got.Events {
				if e.EntityID == nil {
					t.Errorf("%s: row %s came back with a null entity_id", c.label, e.ID)
					continue
				}
				if *e.EntityID != wantEntity[e.ID] {
					t.Errorf("%s: row %s carries entity_id %s, want its own invoice's entity %s",
						c.label, e.ID, *e.EntityID, wantEntity[e.ID])
				}
				if e.CompanyName == nil || *e.CompanyName == "" {
					t.Errorf("%s: row %s came back with no company_name; D-25 rests on that label",
						c.label, e.ID)
					continue
				}
				if *e.CompanyName != wantName[e.ID] {
					t.Errorf("%s: row %s is labelled %q, want %q", c.label, e.ID, *e.CompanyName, wantName[e.ID])
				}
				labels[*e.CompanyName] = true
			}
			if len(labels) != c.wantLabels {
				t.Errorf("%s: the returned rows carry %d distinct company labels (%v), want %d — "+
					"\"both rows came back\" is not enough if the user cannot tell them apart",
					c.label, len(labels), labels, c.wantLabels)
			}
		})
	}

	// D-25 bounds the residue to ModeAllCompanies alone: a workspace-only view cannot reach
	// an invoice-scoped row at all.
	ws := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: number, Company: audit.WorkspaceOnly()})
	if len(ws.Events) != 0 || ws.Total != 0 {
		t.Errorf("workspace-only: q=%q matched %d rows (total %d), want 0 — every row bearing the "+
			"number is company-scoped", number, len(ws.Events), ws.Total)
	}
}

// TestAuditSearch_NumberKeyIsNotReachableThroughTheGenericArm is AC #1's behavioural half,
// and the only case in this file that fails when the resolved arm is added WITHOUT the
// exclusion. The row's number resolves to no invoice, so after the fence nothing can reach
// it by number while its other keys stay reachable. That unreachability is the design's
// recorded consequence of resolving through the live table, not a defect.
func TestAuditSearch_NumberKeyIsNotReachableThroughTheGenericArm(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	const ghost, note = "ZZ-GHOST-9", "quokka"
	rows := filtInsert(t, f, p, []filtRow{
		{event: "validation.rule.enabled", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"note":%q,"invoice_number":%q}`, note, ghost), ageSeconds: 10},
	})
	landed, _, withNumberKey := filtPayloadShape(t, f, p)
	if landed != 1 || withNumberKey != 1 {
		t.Fatalf("fixture: %d rows landed and %d carry invoice_number, want 1 and 1", landed, withNumberKey)
	}
	if n := filtCountInvoices(t, f, p, ghost); n != 0 {
		t.Fatalf("%d invoices bear %q; the resolved arm would reach this row legitimately", n, ghost)
	}

	// Positive control FIRST: the same row's other key must stay reachable, or the zero
	// below is indistinguishable from search being unwired — and it is what an over-broad,
	// row-scoped exclusion would break.
	want := []int64{rows[0].id}
	present := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: note})
	if ids := pageIDs(t, present); !pageEqualIDs(ids, want) {
		t.Fatalf("q=%q matched %v, want %v — the exclusion must be keyed on kv.key, not applied to "+
			"the whole row", note, ids, want)
	}

	got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: ghost})
	if len(got.Events) != 0 {
		t.Errorf("q=%q matched %d rows, want 0 — the number key must leave the generic value arm, "+
			"or the resolved arm only widens what is already unscoped", ghost, len(got.Events))
	}
	if got.Total != 0 {
		t.Errorf("q=%q reported total %d, want 0", ghost, got.Total)
	}
}

// TestAuditSearch_OtherSixTargetsUnchanged is AC #4. The exclusion is keyed on kv.key, so
// every other pair on the same row still yields its value. An over-broad form — skipping
// the row, or filtering on kv.value instead of kv.key — silently breaks most of the search.
// Case "an invoice row's other keys" is the sharpest: that row DOES carry the number.
func TestAuditSearch_OtherSixTargetsUnchanged(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	zanzibar := pageSeedEntity(t, f, p, "Zanzibar Trading")
	keeper := pageSeedEntity(t, f, p, "Keeper Ltd")
	invoice := filtSeedNumberedInvoice(t, f, p, keeper, "INV-KEEP-1")
	named := uuid.NewString()
	filtSeedMembership(t, f, p, named, "Wilhelmina Thorne")

	// Each row is reachable by exactly one route: actors are pairwise distinct because the
	// actor route folds to actor = ANY(...), and the same reasoning applies to entity_id.
	rows := filtInsert(t, f, p, []filtRow{
		{event: "validation.rule.enabled", actor: uuid.NewString(), payload: `{}`, ageSeconds: 80},
		{event: "membership.suspended", actor: uuid.NewString(), payload: `{}`, ageSeconds: 70},
		{event: "workflow_role.created", actor: uuid.NewString(), payload: `{"note":"quokka"}`, ageSeconds: 60},
		{event: "approval_policy.published", actor: uuid.NewString(), payload: `{"key":"buyer_tin_present"}`, ageSeconds: 50},
		{event: "document.created", actor: uuid.NewString(), payload: `{"reference":"IRN-77ZK"}`, ageSeconds: 40},
		{event: "portfolio.entity.updated", actor: named, payload: `{}`, ageSeconds: 30},
		{event: "portfolio.entity.created", actor: uuid.NewString(), payload: fmt.Sprintf(`{"id":%q}`, zanzibar), ageSeconds: 20},
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":"INV-KEEP-1","note":"wombat"}`, invoice), ageSeconds: 10},
	})

	cases := []struct {
		route string
		term  string
		row   int
	}{
		{"event fragment", "validation.rule", 0},
		{"raw event type", "membership.suspended", 1},
		{"reason text", "quokka", 2},
		{"rule key", "buyer_tin_present", 3},
		{"IRN reference", "IRN-77ZK", 4},
		{"actor display name", "Wilhelmina", 5},
		{"company name", "Zanzibar", 6},
		{"an invoice row's other keys", "wombat", 7},
	}
	if len(cases) != len(rows) {
		t.Fatalf("%d cases over %d seeded rows; every row must be claimed by exactly one route",
			len(cases), len(rows))
	}
	landed, _, withNumberKey := filtPayloadShape(t, f, p)
	if landed != len(rows) {
		t.Fatalf("fixture: %d rows landed, want %d", landed, len(rows))
	}
	if withNumberKey != 1 {
		t.Fatalf("fixture: %d payloads carry invoice_number, want exactly 1 — without it the "+
			"over-broad-exclusion case cannot fail", withNumberKey)
	}
	if rows[6].entity == nil || *rows[6].entity != zanzibar {
		t.Fatalf("the company row resolved to entity %v, want %s — its fold-in would have nothing "+
			"to match", rows[6].entity, zanzibar)
	}

	for _, c := range cases {
		t.Run(c.route, func(t *testing.T) {
			got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: c.term})
			want := []int64{rows[c.row].id}
			if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
				t.Errorf("q=%q matched %v, want exactly %v (the %s route's row and only it)",
					c.term, ids, want, c.route)
			}
			if got.Total != 1 {
				t.Errorf("q=%q reported total %d, want 1", c.term, got.Total)
			}
		})
	}
}

// TestAuditSearch_NumberWildcardIsALiteral is AC #4's escaping half. Writers store the
// number byte-for-byte, so the search side owns all escaping and the fold-in must reuse the
// `like` filter.go already computed, under ESCAPE '\'. Measured three ways: unescaped,
// q="%" resolves EVERY invoice in the tenant; with an empty ESCAPE clause, or a second
// escapeLike call, it resolves none.
//
// TestAuditSearch_PercentIsALiteralNotAWildcard cannot make this claim — its tenant holds no
// invoice, so its fold-in resolves nothing whatever the escaping.
func TestAuditSearch_NumberWildcardIsALiteral(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	// The company name carries neither metacharacter, so the company fold-in stays out of
	// every case below.
	entity := pageSeedEntity(t, f, p, "Escape Holdings")

	const hostile = `50%_OFF`
	hot := filtSeedNumberedInvoice(t, f, p, entity, hostile)
	cold1 := filtSeedNumberedInvoice(t, f, p, entity, "INV-PLAIN-1")
	cold2 := filtSeedNumberedInvoice(t, f, p, entity, "INV-PLAIN-2")

	rows := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(), payload: fmt.Sprintf(`{"id":%q}`, hot), ageSeconds: 30},
		{event: "invoice.created", actor: uuid.NewString(), payload: fmt.Sprintf(`{"id":%q}`, cold1), ageSeconds: 20},
		{event: "invoice.created", actor: uuid.NewString(), payload: fmt.Sprintf(`{"id":%q}`, cold2), ageSeconds: 10},
	})
	landed, withInvoice, withNumberKey := filtPayloadShape(t, f, p)
	if landed != 3 || withInvoice != 3 {
		t.Fatalf("fixture: %d rows landed and %d were attributed to an invoice, want 3 and 3",
			landed, withInvoice)
	}
	if withNumberKey != 0 {
		t.Fatalf("fixture: %d payloads carry invoice_number, want 0 — these rows must be reachable "+
			"only through the fold-in", withNumberKey)
	}
	if n := filtCountInvoices(t, f, p, "INV-PLAIN"); n != 2 {
		t.Fatalf("%d ordinary invoices in the tenant, want 2 — without them an unescaped fold-in "+
			"would have nothing extra to sweep in", n)
	}

	want := []int64{rows[0].id}
	// Positive control: the uuid reaches the same row through the generic arm, so a zero
	// below cannot be search being unwired.
	if ids := pageIDs(t, pageQuery(t, f, p, audit.Filter{Limit: 20, Q: hot})); !pageEqualIDs(ids, want) {
		t.Fatalf("q=<invoice id> matched %v, want %v; search is not working here", ids, want)
	}

	for _, tc := range []struct{ label, q string }{
		{"a bare percent", "%"},
		{"a bare underscore", "_"},
		// The whole hostile number: an escape that neutralises the metacharacters into
		// matching nothing, or a second escapeLike call, returns 0 rows here.
		{"the whole number", hostile},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: tc.q})
			if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
				t.Errorf("q=%q matched %v, want exactly %v (the %s invoice's row) — unescaped, a "+
					"metacharacter resolves every invoice in the tenant; double-escaped it resolves none",
					tc.q, ids, want, hostile)
			}
			if got.Total != len(want) {
				t.Errorf("q=%q reported total %d, want %d", tc.q, got.Total, len(want))
			}
		})
	}
}

// --- AUDIT-11-09 QA: the fence's residues and its hostile inputs ---------------------------

// filtRenameInvoice changes an invoice's number in place. No production path does this
// (D-15), which is why the behaviour it pins below is latent rather than observed.
func filtRenameInvoice(t *testing.T, f *fixture, p pageFixture, invoiceID, number string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE invoices SET invoice_number = $2 WHERE id = $1`, invoiceID, number)
		if e == nil && tag.RowsAffected() != 1 {
			return fmt.Errorf("renamed %d rows, want 1", tag.RowsAffected())
		}
		return e
	}); err != nil {
		t.Fatalf("rename invoice %s to %q: %v", invoiceID, number, err)
	}
}

// filtDeleteInvoice removes an invoice as the migrator: invoice_app holds no DELETE grant
// on invoices. Its audit rows survive — audit_log carries no FK to invoices, and
// audit_log_no_update_delete forbids deleting them anyway.
func filtDeleteInvoice(t *testing.T, f *fixture, p pageFixture, invoiceID string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.mig, p.tenant, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceID)
		if e == nil && tag.RowsAffected() != 1 {
			return fmt.Errorf("deleted %d rows, want 1", tag.RowsAffected())
		}
		return e
	}); err != nil {
		t.Fatalf("delete invoice %s: %v", invoiceID, err)
	}
}

// TestAuditSearch_NumberResolvesThroughTheLiveTableNotTheFrozenPayload pins CF-26 — the
// residue the entity fence buys with reachability, recorded rather than fixed.
//
// The payload freezes the number at write time; the fence resolves the typed number through
// the LIVE invoices table. So the search renders "now", not "then": after a rename the
// frozen value is unreachable and the new number reaches the old event, and a deleted
// invoice strands its rows. That inverts D-15's "frozen, so it renders then" for search
// alone. It is the direct consequence of the user's Fork 2 choice (D-24), so it is pinned
// here to make a future change to it deliberate — do NOT "fix" this without reopening D-24.
//
// The invoice-id control runs at every step: it separates "unreachable by number" from
// "the row is gone", which is the only reading that would be a defect.
func TestAuditSearch_NumberResolvesThroughTheLiveTableNotTheFrozenPayload(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Frozen Ledger Co")

	const before, after = "INV-FROZEN-1", "INV-RENAMED-1"
	invoice := filtSeedNumberedInvoice(t, f, p, entity, before)
	rows := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, invoice, before), ageSeconds: 10},
	})
	want := []int64{rows[0].id}

	landed, withInvoice, withNumberKey := filtPayloadShape(t, f, p)
	if landed != 1 || withInvoice != 1 || withNumberKey != 1 {
		t.Fatalf("fixture: %d rows landed, %d attributed, %d carry invoice_number; want 1, 1, 1 — "+
			"the payload must freeze the number or the rename claim is vacuous", landed, withInvoice, withNumberKey)
	}

	ids := func(q string) []int64 { return pageIDs(t, pageQuery(t, f, p, audit.Filter{Limit: 20, Q: q})) }

	// Before the rename both routes reach the row: the number resolves, and the id sits in
	// the payload. Without this the zeroes below cannot be told from an unwired search.
	if got := ids(before); !pageEqualIDs(got, want) {
		t.Fatalf("q=%q matched %v before the rename, want %v; search is not working here", before, got, want)
	}
	if got := ids(invoice); !pageEqualIDs(got, want) {
		t.Fatalf("q=<invoice id> matched %v, want %v", got, want)
	}

	filtRenameInvoice(t, f, p, invoice, after)

	if got := ids(before); len(got) != 0 {
		t.Errorf("after the rename q=%q still matched %v, want none — the payload's frozen value "+
			"left the generic arm with the key, so it is unreachable by design (CF-26)", before, got)
	}
	if got := ids(after); !pageEqualIDs(got, want) {
		t.Errorf("after the rename q=%q matched %v, want %v — the new number reaches the OLD event, "+
			"because the arm resolves through the live table (CF-26)", after, got, want)
	}
	if got := ids(invoice); !pageEqualIDs(got, want) {
		t.Fatalf("q=<invoice id> matched %v after the rename, want %v — the row itself must still "+
			"be reachable, or this is a lost row rather than a reachability residue", got, want)
	}

	filtDeleteInvoice(t, f, p, invoice)

	for _, q := range []string{before, after} {
		if got := ids(q); len(got) != 0 {
			t.Errorf("after the invoice was deleted q=%q matched %v, want none — a deleted invoice "+
				"strands its audit rows from every number search (CF-26)", q, got)
		}
	}
	if got := ids(invoice); !pageEqualIDs(got, want) {
		t.Errorf("q=<invoice id> matched %v after the delete, want %v — the audit row outlives its "+
			"invoice and stays reachable by id", got, want)
	}
}

// TestAuditSearch_NumberArmUnionsWithTheGenericArm is the adversarial case AUDIT-11-09's own
// specs skip: one term that is BOTH an invoice number and an unrelated payload value. The
// arms are OR-ed, so all three shapes must come back. A fence written as an AND, or an
// exclusion that dropped the whole EXISTS when the term resolves an invoice, would return a
// strict subset while every single-shape case stayed green.
func TestAuditSearch_NumberArmUnionsWithTheGenericArm(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Union Freight")

	const term = "OCTOPUS-7"
	invoice := filtSeedNumberedInvoice(t, f, p, entity, term)
	if n := filtCountInvoices(t, f, p, term); n != 1 {
		t.Fatalf("%d invoices bear %q, want exactly 1", n, term)
	}

	rows := filtInsert(t, f, p, []filtRow{
		// Reachable only through the fold-in: no payload key holds the term.
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, invoice), ageSeconds: 30},
		// Reachable only through the generic arm: an unrelated key, not invoice-scoped.
		{event: "validation.rule.enabled", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"note":%q}`, term), ageSeconds: 20},
		// Both routes at once, and the one shape AUDIT-11-01..04 actually writes.
		{event: "invoice.updated", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, invoice, term), ageSeconds: 10},
	})

	want := []int64{rows[2].id, rows[1].id, rows[0].id}
	got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: term})
	if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
		t.Errorf("q=%q matched %v, want all three shapes %v — the arms OR, so a term that is both a "+
			"number and a payload value reaches everything either arm reaches", term, ids, want)
	}
	if got.Total != len(want) {
		t.Errorf("q=%q reported total %d, want %d", term, got.Total, len(want))
	}
}

// TestAuditSearch_NumberThatIsASubstringOfAnInvoiceID is the adversarial collision: an
// invoice numbered with eight hex characters lifted out of ANOTHER invoice's uuid. The
// fold-in reaches the numbered invoice's rows and the generic arm reaches the uuid-bearing
// row, and neither shadows the other.
func TestAuditSearch_NumberThatIsASubstringOfAnInvoiceID(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Collision Ltd")

	host := filtSeedNumberedInvoice(t, f, p, entity, "INV-HOST-1")
	term := host[:8]
	if strings.Contains("INV-HOST-1", term) {
		t.Fatalf("the needle %q is part of the host invoice's own number; the two routes would not "+
			"be separable", term)
	}
	numbered := filtSeedNumberedInvoice(t, f, p, entity, term)
	if n := filtCountInvoices(t, f, p, term); n != 1 {
		t.Fatalf("%d invoices bear %q as a number, want exactly 1 (the collision must be with an "+
			"id, not with another number)", n, term)
	}

	rows := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, host), ageSeconds: 20},
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, numbered), ageSeconds: 10},
	})
	if _, _, withNumberKey := filtPayloadShape(t, f, p); withNumberKey != 0 {
		t.Fatalf("fixture: %d payloads carry invoice_number, want 0 — each row must be reachable by "+
			"exactly one route", withNumberKey)
	}

	want := []int64{rows[1].id, rows[0].id}
	got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: term})
	if ids := pageIDs(t, got); !pageEqualIDs(ids, want) {
		t.Errorf("q=%q matched %v, want %v — the host row through the generic arm (the needle is a "+
			"substring of its uuid) and the numbered row through the fold-in", term, ids, want)
	}
	if got.Total != len(want) {
		t.Errorf("q=%q reported total %d, want %d", term, got.Total, len(want))
	}
}

// TestAuditSearch_HostileNeedlesReachTheFoldInAsData is the adversarial injection case. The
// needle is a bind parameter in all three fold-ins and in both text arms, so a quote, a
// comment marker or a statement separator is data. A needle that terminated the literal
// would either error or widen the result to the whole tenant; both are asserted against.
func TestAuditSearch_HostileNeedlesReachTheFoldInAsData(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Bobby Tables Ltd")
	invoice := filtSeedNumberedInvoice(t, f, p, entity, "INV-SAFE-1")

	rows := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"note":"pangolin"}`, invoice), ageSeconds: 10},
	})
	want := []int64{rows[0].id}

	for _, q := range []string{
		`' OR 1=1 --`,
		`'); DROP TABLE audit_log; --`,
		`%' OR '1'='1`,
		`' UNION SELECT id::text FROM invoices --`,
		`\`,
		`" OR ""="`,
		`INV-SAFE-1'`,
	} {
		t.Run(q, func(t *testing.T) {
			got := pageQuery(t, f, p, audit.Filter{Limit: 20, Q: q})
			if ids := pageIDs(t, got); len(ids) != 0 {
				t.Errorf("q=%q matched %v, want none — the needle is data, and no row here contains "+
					"it literally", q, ids)
			}
			if got.Total != 0 {
				t.Errorf("q=%q reported total %d, want 0", q, got.Total)
			}
		})
	}

	// The fixture survived every needle: nothing was dropped, and the ordinary routes still
	// work. Without this the zeroes above would also pass on a search that had stopped
	// working entirely.
	for _, q := range []string{"INV-SAFE-1", "pangolin", invoice} {
		if ids := pageIDs(t, pageQuery(t, f, p, audit.Filter{Limit: 20, Q: q})); !pageEqualIDs(ids, want) {
			t.Errorf("after the hostile needles q=%q matched %v, want %v", q, ids, want)
		}
	}
}

// TestAuditSearch_ResolvedInvoiceListIsNotCapped pins the "no cap" rule the third fold-in
// inherits from the two beside it. A cap would silently drop matches — the caller would see
// a plausible page and a plausible total, both short. Total is asserted rather than the page
// because it is exact at any Limit.
func TestAuditSearch_ResolvedInvoiceListIsNotCapped(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Bulk Holdings")

	const n = 600 // above every plausible LIMIT a future cap would reach for.
	const prefix = "BULK-"
	ctx := context.Background()
	invoices := make([]string, 0, n)
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			id := uuid.NewString()
			if _, e := tx.Exec(ctx,
				`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
				id, p.tenant, entity, fmt.Sprintf("%s%04d", prefix, i)); e != nil {
				return e
			}
			invoices = append(invoices, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d invoices: %v", n, err)
	}
	if got := filtCountInvoices(t, f, p, prefix); got != n {
		t.Fatalf("%d invoices bear %q, want %d", got, prefix, n)
	}

	seed := make([]filtRow, 0, n)
	for i, inv := range invoices {
		seed = append(seed, filtRow{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, inv), ageSeconds: i + 1})
	}
	filtInsert(t, f, p, seed)
	landed, withInvoice, withNumberKey := filtPayloadShape(t, f, p)
	if landed != n || withInvoice != n || withNumberKey != 0 {
		t.Fatalf("fixture: %d rows landed, %d attributed, %d carry invoice_number; want %d, %d, 0",
			landed, withInvoice, withNumberKey, n, n)
	}

	got := pageQuery(t, f, p, audit.Filter{Limit: 5, Q: prefix})
	if got.Total != n {
		t.Errorf("q=%q reported total %d, want %d — the resolved list is uncapped, so a cap at any "+
			"k < %d would show here as a short total with a plausible page", prefix, got.Total, n, n)
	}
	if len(got.Events) != 5 {
		t.Errorf("q=%q returned %d events at Limit 5, want 5", prefix, len(got.Events))
	}
}
