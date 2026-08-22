// AUDIT-04-04: actor_name / actor_kind on the page, resolved in one batch.
//
// Four of the five ACs are properties internal/actor.Resolve already has (its own suite
// owns those). What these cases prove is that Query WIRED it: one statement per request
// whatever the page size, the stored actor value untouched, and every stored shape
// surviving the round trip.
//
// Helpers use an act* prefix; plan*, trigger*, scoped*, reader*, page*, filt* and fsql*
// are taken.
package audit_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// actCountingTx wraps a real transaction and counts the statements naming a table.
// pgx.Tx is an interface, so this is enough to see exactly what Query issued — no
// pg_stat_statements, no pool surgery.
type actCountingTx struct {
	pgx.Tx
	table string
	n     atomic.Int64
}

func (c *actCountingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, c.table) {
		c.n.Add(1)
	}
	return c.Tx.Query(ctx, sql, args...)
}

func (c *actCountingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, c.table) {
		c.n.Add(1)
	}
	return c.Tx.QueryRow(ctx, sql, args...)
}

// actQueryCounting runs Query through a counting wrapper and reports how many statements
// named table.
func actQueryCounting(t *testing.T, f *fixture, p pageFixture, filter audit.Filter, table string) (audit.Response, int) {
	t.Helper()
	ctx := context.Background()
	var out audit.Response
	var n int64
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		c := &actCountingTx{Tx: tx, table: table}
		var err error
		out, err = audit.Query(ctx, c, filter)
		n = c.n.Load()
		return err
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	return out, int(n)
}

// actByActor indexes a page by its stored actor value.
func actByActor(t *testing.T, r audit.Response) map[string]audit.Event {
	t.Helper()
	out := make(map[string]audit.Event, len(r.Events))
	for _, e := range r.Events {
		out[e.Actor] = e
	}
	return out
}

// --- AC #1: one memberships statement per request ------------------------------------

func TestAuditRead_IssuesOneResolveQueryForManyRows(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	const (
		rows   = 50
		actors = 12
	)
	subjects := make([]string, actors)
	for i := range subjects {
		subjects[i] = uuid.NewString()
		filtSeedMembership(t, f, p, subjects[i], "Member "+subjects[i][:8])
	}
	in := make([]filtRow, 0, rows)
	for i := 0; i < rows; i++ {
		in = append(in, filtRow{
			event: "invoice.created", actor: subjects[i%actors], payload: "{}", ageSeconds: i * 60,
		})
	}
	filtInsert(t, f, p, in)

	big, bigN := actQueryCounting(t, f, p, audit.Filter{Limit: rows}, "memberships")
	if len(big.Events) != rows {
		t.Fatalf("page has %d rows, want %d — the count below would be about the wrong page",
			len(big.Events), rows)
	}
	if bigN != 1 {
		t.Errorf("a %d-row page issued %d memberships statements, want exactly 1", rows, bigN)
	}

	// "Regardless of page size" is the actual claim, and only a second size tests it:
	// a per-row Resolve would give 50 and 5 rather than 1 and 1.
	small, smallN := actQueryCounting(t, f, p, audit.Filter{Limit: 5}, "memberships")
	if len(small.Events) != 5 {
		t.Fatalf("small page has %d rows, want 5", len(small.Events))
	}
	if smallN != bigN {
		t.Errorf("a 5-row page issued %d memberships statements and a %d-row page issued %d; "+
			"the count must not depend on page size", smallN, rows, bigN)
	}

	// Free-text search resolves display names against memberships too, so a searching
	// request legitimately issues two. Pinned so the budget is visible rather than
	// discovered, and so nobody "fixes" it by dropping one.
	_, searchN := actQueryCounting(t, f, p, audit.Filter{Limit: rows, Q: "Member"}, "memberships")
	if searchN != 2 {
		t.Errorf("a searching request issued %d memberships statements, want exactly 2 "+
			"(the search fold-in plus the actor resolve)", searchN)
	}
}

// --- AC #2: the stored actor value is untouched ----------------------------------------

func TestAuditRead_ActorColumnIsUnchanged(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	// Spellings actor.Resolve normalises internally. The wire must still carry what the
	// column holds, byte for byte — normalisation is a lookup detail, not a rendering.
	canonical := uuid.NewString()
	stored := []string{
		canonical,
		strings.ToUpper(canonical),
		"{" + canonical + "}",
		strings.ReplaceAll(canonical, "-", ""),
		"system",
		"backfill-source-rows",
	}
	filtSeedMembership(t, f, p, canonical, "Canonical Member")

	in := make([]filtRow, 0, len(stored))
	for i, s := range stored {
		in = append(in, filtRow{event: "invoice.created", actor: s, payload: "{}", ageSeconds: i * 60})
	}
	filtInsert(t, f, p, in)

	got := pageQuery(t, f, p, audit.Filter{Limit: 20})
	if len(got.Events) != len(stored) {
		t.Fatalf("page has %d rows, want %d", len(got.Events), len(stored))
	}
	seen := map[string]bool{}
	for _, e := range got.Events {
		seen[e.Actor] = true
	}
	for _, want := range stored {
		if !seen[want] {
			t.Errorf("no row carries actor %q byte-for-byte; the stored value was rewritten", want)
		}
	}
}

// --- AC #3: all three stored shapes ------------------------------------------------------

func TestAuditRead_ResolvesAllThreeActorShapes(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	member := uuid.NewString()
	filtSeedMembership(t, f, p, member, "Ada Okonkwo")

	// backfill-source-rows is free text, not a uuid. Without the resolver's shape gate it
	// would reach uuid_in and abort the whole transaction with 22P02 — the failure this
	// case exists to catch.
	filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: member, payload: "{}", ageSeconds: 30},
		{event: "invoice.updated", actor: "system", payload: "{}", ageSeconds: 20},
		{event: "invoice.validated", actor: "backfill-source-rows", payload: "{}", ageSeconds: 10},
	})

	got := pageQuery(t, f, p, audit.Filter{Limit: 20})
	if len(got.Events) != 3 {
		t.Fatalf("page has %d rows, want 3", len(got.Events))
	}
	by := actByActor(t, got)

	for _, tc := range []struct{ actor, name, kind string }{
		{member, "Ada Okonkwo", "person"},
		{"system", "System", "system"},
		{"backfill-source-rows", "backfill-source-rows", "raw"},
	} {
		e, ok := by[tc.actor]
		if !ok {
			t.Errorf("no row for actor %q", tc.actor)
			continue
		}
		if e.ActorName != tc.name {
			t.Errorf("actor %q: actor_name = %q, want %q", tc.actor, e.ActorName, tc.name)
		}
		if e.ActorKind != tc.kind {
			t.Errorf("actor %q: actor_kind = %q, want %q", tc.actor, e.ActorKind, tc.kind)
		}
	}
}

// --- AC #4: a departed member falls back to the raw subject -------------------------------

func TestAuditRead_DepartedMemberFallsBackToRawSubject(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	departed := uuid.NewString() // a well-formed subject with no memberships row
	present := uuid.NewString()
	filtSeedMembership(t, f, p, present, "Still Employed")

	filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: departed, payload: "{}", ageSeconds: 20},
		{event: "invoice.updated", actor: present, payload: "{}", ageSeconds: 10},
	})

	got := pageQuery(t, f, p, audit.Filter{Limit: 20})
	by := actByActor(t, got)

	e, ok := by[departed]
	if !ok {
		t.Fatalf("no row for the departed subject %s", departed)
	}
	if e.ActorName != departed {
		t.Errorf("departed subject: actor_name = %q, want the raw subject %q", e.ActorName, departed)
	}
	if e.ActorKind != "raw" {
		t.Errorf("departed subject: actor_kind = %q, want %q", e.ActorKind, "raw")
	}
	// The control: a resolvable member on the same page must still resolve, or "falls
	// back to raw" would also pass with resolution switched off entirely.
	if p := by[present]; p.ActorName != "Still Employed" || p.ActorKind != "person" {
		t.Errorf("the present member resolved to (%q, %q), want (\"Still Employed\", \"person\") — "+
			"without this, a wholly unwired resolver would pass this test", p.ActorName, p.ActorKind)
	}
}

// --- AC #5: neither field is ever JSON null ------------------------------------------------

func TestAuditRead_ActorNameAndKindAreNeverNull(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	member := uuid.NewString()
	filtSeedMembership(t, f, p, member, "Named Person")
	filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: member, payload: "{}", ageSeconds: 30},
		{event: "invoice.updated", actor: "system", payload: "{}", ageSeconds: 20},
		{event: "invoice.validated", actor: "backfill-source-rows", payload: "{}", ageSeconds: 10},
	})

	got := pageQuery(t, f, p, audit.Filter{Limit: 20})
	if len(got.Events) == 0 {
		t.Fatalf("page is empty; a null-check over no rows proves nothing")
	}

	body, err := json.Marshal(got.Events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	var probe []map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	for i, row := range probe {
		for _, field := range []string{"actor_name", "actor_kind"} {
			raw, present := row[field]
			if !present {
				t.Errorf("row %d has no %q field", i, field)
				continue
			}
			if string(raw) == "null" {
				t.Errorf("row %d: %q is null; both are plain strings and must never marshal as null", i, field)
			}
			if string(raw) == `""` {
				t.Errorf("row %d: %q is empty; every stored shape resolves to something renderable", i, field)
			}
		}
	}
}
