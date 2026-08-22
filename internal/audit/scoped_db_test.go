// AUDIT-04-06: the scoped per-invoice read against a real Postgres.
//
// AUDIT-04-11 moved the two payload spellings into audit_log.invoice_id, a STORED
// generated column that dispatches on the EVENT NAME. So these cases assert the reader
// composes with a column the database already resolved — not that the reader parses a
// payload. scoped_test.go pins the two dispatch lists against the live resolver by text;
// this file is the behavioural half, which that comparison cannot make.
//
// Helpers use an inv* prefix; plan/trigger/scoped/reader/page/filt/fsql/act/fct/fctsql
// are taken.
package audit_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// --- fixture ----------------------------------------------------------------------------

// invFixture is a tenant holding one real company and one real invoice, so the scoped read
// runs against an id an invoice actually has and the entity resolver has something to join.
type invFixture struct {
	p       pageFixture
	entity  string
	invoice string
}

func invSeed(t *testing.T, f *fixture) invFixture {
	t.Helper()
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Scoped Ltd")
	return invFixture{p: p, entity: entity, invoice: pageSeedInvoice(t, f, p, entity)}
}

// invIDPayload and invInvoiceIDPayload are the two spellings the generated column
// dispatches between: ten invoice events carry the id under `id`, seven under `invoice_id`.
func invIDPayload(id string) string { return fmt.Sprintf(`{"id":%q}`, id) }

func invInvoiceIDPayload(id string) string { return fmt.Sprintf(`{"invoice_id":%q}`, id) }

// invEvents is the returned page's event names, sorted, for set comparison.
func invEvents(r audit.Response) []string {
	out := make([]string, 0, len(r.Events))
	for _, e := range r.Events {
		out = append(out, e.Event)
	}
	sort.Strings(out)
	return out
}

// --- AC #1: both spellings, one read -----------------------------------------------------

// TestAuditScoped_ReturnsBothPayloadSpellingsForOneInvoice is AC #1. The third row belongs
// to a different invoice and must not come back: without it the case would pass on a filter
// that was ignored entirely.
func TestAuditScoped_ReturnsBothPayloadSpellingsForOneInvoice(t *testing.T) {
	f := requireFixture(t)
	fx := invSeed(t, f)

	other := uuid.NewString()
	filtInsert(t, f, fx.p, []filtRow{
		{event: "invoice.created", actor: "system", payload: invIDPayload(fx.invoice), ageSeconds: 30},
		{event: "submission.accepted", actor: "system", payload: invInvoiceIDPayload(fx.invoice), ageSeconds: 20},
		{event: "invoice.created", actor: "system", payload: invIDPayload(other), ageSeconds: 10},
	})

	got := pageQuery(t, f, fx.p, audit.Filter{Limit: 10, InvoiceID: fx.invoice})

	want := []string{"invoice.created", "submission.accepted"}
	if have := invEvents(got); !invEqual(have, want) {
		t.Errorf("scoped read returned %v, want %v — the two spellings must land in one read",
			have, want)
	}
	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
}

// invEqual compares two sorted string slices.
func invEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- AC #3: the event gate ---------------------------------------------------------------

// TestAuditScoped_EventGateExcludesACollidingID is AC #3. document.* and portfolio.entity.*
// rows carry a real id under the bare `id` key, spelled exactly like invoice.created's, so
// a resolver keyed on which payload key is present would misattribute them. Dispatch is on
// the event name, and neither family is in either invoice_id list.
//
// The genuine row is the control needle: without it every case here would pass by returning
// nothing at all.
func TestAuditScoped_EventGateExcludesACollidingID(t *testing.T) {
	f := requireFixture(t)

	for _, colliding := range []string{"document.created", "portfolio.entity.updated"} {
		t.Run(colliding, func(t *testing.T) {
			fx := invSeed(t, f)
			filtInsert(t, f, fx.p, []filtRow{
				{event: colliding, actor: "system", payload: invIDPayload(fx.invoice), ageSeconds: 30},
				{event: "invoice.created", actor: "system", payload: invIDPayload(fx.invoice), ageSeconds: 20},
			})

			got := pageQuery(t, f, fx.p, audit.Filter{Limit: 10, InvoiceID: fx.invoice})

			if have, want := invEvents(got), []string{"invoice.created"}; !invEqual(have, want) {
				t.Errorf("scoped read returned %v, want %v — %q carries the same id under the same "+
					"key and must still be gated out by its event name", have, want, colliding)
			}
			if got.Total != 1 {
				t.Errorf("total = %d, want 1", got.Total)
			}
		})
	}
}

// --- AC #4: composition ------------------------------------------------------------------

// TestAuditScoped_ComposesWithTheDateAndEventFilters is AC #4. The rows for the second
// invoice are recent and carry the selected event, so only the scoped predicate can exclude
// them — that is what makes this a composition test rather than a date test.
func TestAuditScoped_ComposesWithTheDateAndEventFilters(t *testing.T) {
	f := requireFixture(t)
	fx := invSeed(t, f)
	other := pageSeedInvoice(t, f, fx.p, fx.entity)

	events := []string{"invoice.created", "invoice.updated", "invoice.validated", "invoice.transitioned"}
	ages := []int{1, 3, 5, 20, 40, 60} // days
	rows := make([]filtRow, 0, len(ages)+2)
	for i, days := range ages {
		rows = append(rows, filtRow{
			event:      events[i%len(events)],
			actor:      "system",
			payload:    invIDPayload(fx.invoice),
			ageSeconds: days * 86400,
		})
	}
	// Same event, same week, different invoice.
	rows = append(rows,
		filtRow{event: "invoice.created", actor: "system", payload: invIDPayload(other), ageSeconds: 2 * 86400},
		filtRow{event: "invoice.created", actor: "system", payload: invIDPayload(other), ageSeconds: 4 * 86400})
	filtInsert(t, f, fx.p, rows)

	// Unscoped, the date+event filters alone match three rows: this invoice's day-1
	// invoice.created plus the other invoice's two. Asserting that first is what proves
	// the scoped predicate is the thing doing the work below.
	loose := pageQuery(t, f, fx.p, audit.Filter{
		Limit:  10,
		From:   time.Now().Add(-7 * 24 * time.Hour),
		Events: []string{"invoice.created"},
	})
	if loose.Total != 3 {
		t.Fatalf("without the scoped filter the date+event pair matched %d rows, want 3", loose.Total)
	}

	got := pageQuery(t, f, fx.p, audit.Filter{
		Limit:     10,
		InvoiceID: fx.invoice,
		From:      time.Now().Add(-7 * 24 * time.Hour),
		Events:    []string{"invoice.created"},
	})
	if got.Total != 1 {
		t.Errorf("total = %d, want 1 — the scoped read must compose with date and event, not "+
			"replace them", got.Total)
	}
	if len(got.Events) != 1 || got.Events[0].Event != "invoice.created" {
		t.Errorf("scoped read returned %v, want one invoice.created", invEvents(got))
	}
}

// --- the grammar, behaviourally ----------------------------------------------------------

// TestAuditScoped_MatchesAnUppercaseOrBraceWrappedPayloadID is the half scoped_test.go
// cannot assert. Its two cases compare the migration's grammar TEXT to the resolver's and
// prove they AGREE; neither proves either one WORKS. The generated expression lowercases,
// strips {braces} and strips hyphens before casting, so a caller echoing a raw URL segment
// in any admitted spelling must still be reachable by a plain lowercase filter value.
func TestAuditScoped_MatchesAnUppercaseOrBraceWrappedPayloadID(t *testing.T) {
	f := requireFixture(t)
	fx := invSeed(t, f)

	filtInsert(t, f, fx.p, []filtRow{
		{event: "invoice.created", actor: "system",
			payload: invIDPayload(strings.ToUpper(fx.invoice)), ageSeconds: 30},
		{event: "invoice.updated", actor: "system",
			payload: invIDPayload("{" + fx.invoice + "}"), ageSeconds: 20},
		{event: "invoice.validated", actor: "system",
			payload: invIDPayload(strings.ReplaceAll(fx.invoice, "-", "")), ageSeconds: 10},
		// Another invoice, so "all three spellings matched" cannot be satisfied by a
		// filter that matched everything.
		{event: "invoice.transitioned", actor: "system",
			payload: invIDPayload(uuid.NewString()), ageSeconds: 5},
	})

	got := pageQuery(t, f, fx.p, audit.Filter{Limit: 10, InvoiceID: fx.invoice})

	want := []string{"invoice.created", "invoice.updated", "invoice.validated"}
	if have := invEvents(got); !invEqual(have, want) {
		t.Errorf("scoped read returned %v, want %v — uppercase, brace-wrapped and unhyphenated "+
			"spellings all normalise to the same stored uuid", have, want)
	}
	if got.Total != 3 {
		t.Errorf("total = %d, want 3", got.Total)
	}
}
