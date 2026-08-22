// AUDIT-04-07 AC #7: log_is_empty against a real Postgres.
//
// Helpers use an emp* prefix. Both answers look identical on the wire otherwise — events:
// [] and total: 0 — so this flag is the only thing separating "your workspace has no
// history yet" from "your filters excluded everything", which are opposite instructions to
// the reader.
package audit_test

import (
	"context"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// empList runs the store as the given tenant.
func empList(t *testing.T, store *audit.Store, tenant string, f audit.Filter) audit.Response {
	t.Helper()
	got, err := store.List(auth.WithIdentity(context.Background(), auth.Identity{TenantID: tenant}), f)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return got
}

// TestAuditRead_LogIsEmptyDistinguishesNewWorkspaceFromFilteredOut is AC #7. The two
// responses agree on every other field, which is exactly why the flag has to exist.
func TestAuditRead_LogIsEmptyDistinguishesNewWorkspaceFromFilteredOut(t *testing.T) {
	f := requireFixture(t)
	fresh := pageSeedTenant(t, f)
	populated := pageSeedTenant(t, f)
	pageInsert(t, f, populated, pageSeries("invoice.created", 4, 40))

	store, _ := stTracedStore(t)

	// A workspace that has never been written to.
	empty := empList(t, store, fresh.tenant, audit.Filter{Limit: 25})
	if !empty.LogIsEmpty {
		t.Errorf("a tenant with no audit rows reported log_is_empty=false; the UI would tell a "+
			"new workspace to widen filters it never set (total=%d)", empty.Total)
	}

	// A workspace with history, emptied by a filter.
	filtered := empList(t, store, populated.tenant,
		audit.Filter{Limit: 25, Events: []string{"no.such.event"}})
	if filtered.LogIsEmpty {
		t.Errorf("a tenant with history reported log_is_empty=true under an excluding filter; the " +
			"UI would tell them their log is empty when it is not")
	}

	// Everything else about the two answers must match, or the flag is not carrying the
	// distinction on its own.
	for _, tc := range []struct {
		name string
		got  audit.Response
	}{{"fresh", empty}, {"filtered", filtered}} {
		if tc.got.Total != 0 {
			t.Errorf("%s: total = %d, want 0", tc.name, tc.got.Total)
		}
		if tc.got.Events == nil {
			t.Errorf("%s: events is nil, want an empty array", tc.name)
		}
		if len(tc.got.Events) != 0 {
			t.Errorf("%s: events has %d rows, want 0", tc.name, len(tc.got.Events))
		}
	}

	// The control: the same populated tenant unfiltered must read as non-empty, or
	// log_is_empty=false above could be a constant.
	full := empList(t, store, populated.tenant, audit.Filter{Limit: 25})
	if full.Total == 0 {
		t.Fatalf("the populated tenant read back 0 rows unfiltered; the fixture is wrong")
	}
	if full.LogIsEmpty {
		t.Errorf("a tenant with %d rows reported log_is_empty=true unfiltered", full.Total)
	}
}

// TestAuditRead_LogIsEmptyIgnoresEveryFilter keeps the probe unfiltered. A probe that
// inherited the request's filters would answer the same question as total and the flag
// would become a second, redundant way to say "nothing matched".
func TestAuditRead_LogIsEmptyIgnoresEveryFilter(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Probe Ltd")
	pageInsert(t, f, p, pageSeries("invoice.created", 3, 30))

	store, _ := stTracedStore(t)

	// Each of these excludes every row, one filter dimension at a time.
	for _, tc := range []struct {
		name   string
		filter audit.Filter
	}{
		{"event", audit.Filter{Limit: 25, Events: []string{"no.such.event"}}},
		{"actor", audit.Filter{Limit: 25, Actors: []string{"nobody"}}},
		{"company", audit.Filter{Limit: 25, Company: audit.NamedCompany(entity)}},
		{"search", audit.Filter{Limit: 25, Q: "no-such-needle-anywhere"}},
		{"invoice", audit.Filter{Limit: 25, InvoiceID: "00000000-0000-0000-0000-000000000001"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := empList(t, store, p.tenant, tc.filter)
			if got.Total != 0 {
				t.Fatalf("the %s filter matched %d rows, want 0 — this case cannot make its claim",
					tc.name, got.Total)
			}
			if got.LogIsEmpty {
				t.Errorf("the %s filter emptied the page and log_is_empty went true; the probe must "+
					"ignore every filter", tc.name)
			}
		})
	}
}
