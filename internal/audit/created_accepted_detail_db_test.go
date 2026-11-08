// AUDIT-14-08: the IP and device on "Workspace created" and "Invite accepted" reach only an admin and the row's actor.
// Reads go through Store.List as identities built from seeded memberships. Helpers use a cd* prefix.
package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	cdProvisioned = "workspace.provisioned" // internal/tenancy/store.go:154
	cdAccepted    = "invitation.accepted"   // internal/tenancy/store.go:603
	cdIP          = "203.0.113.7"
	cdDevice      = "Chrome on macOS"
)

// cdKeys are the payload keys of accountevents' auditPayload (internal/accountevents/audit_job.go:40-41).
var cdKeys = []string{"ip_address", "device"}

type cdWorld struct {
	*aeFirm
	m                   string // the actor of the three rows, an all-clients preparer
	mCtx                context.Context
	prov, accepted, old int64
	stored              map[int64]map[string]any
	both                []int64
}

func cdPayload(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(b)
}

func cdSeed(t *testing.T, f *fixture, w *aeWorld, stored map[int64]map[string]any, actor, event string, payload map[string]any, age int) int64 {
	t.Helper()
	id := w.add(t, f, filtRow{event: event, actor: actor, payload: cdPayload(t, payload), ageSeconds: age})
	if !w.meta[id].workspace {
		t.Fatalf("row %d (%s) was attributed to a company: the oracle is void", id, event)
	}
	if stored != nil {
		stored[id] = payload
	}
	return id
}

func cdProvisionedPayload(tenant, user, name string, withDetail bool) map[string]any {
	p := map[string]any{"tenant_id": tenant, "user_id": user, "name": name, "kind": "firm"}
	if withDetail {
		p["ip_address"], p["device"] = cdIP, cdDevice
	}
	return p
}

func cdAcceptedPayload(withDetail bool) map[string]any {
	p := map[string]any{"invitation_id": uuid.NewString(), "role": "preparer"}
	if withDetail {
		p["ip_address"], p["device"] = cdIP, cdDevice
	}
	return p
}

func cdNew(t *testing.T, f *fixture) *cdWorld {
	t.Helper()
	w := &cdWorld{aeFirm: aeNewFirm(t, f), m: uuid.NewString(), stored: map[int64]map[string]any{}}
	w.mCtx = csMemberUser(t, csSuper(t), w.m, w.p.tenant, "preparer", true)
	w.prov = cdSeed(t, f, &w.aeWorld, w.stored, w.m, cdProvisioned, cdProvisionedPayload(w.p.tenant, w.m, "Kestrel Works", true), 170)
	w.accepted = cdSeed(t, f, &w.aeWorld, w.stored, w.m, cdAccepted, cdAcceptedPayload(true), 160)
	w.old = cdSeed(t, f, &w.aeWorld, w.stored, w.m, cdProvisioned, cdProvisionedPayload(w.p.tenant, w.m, "Heritage Works", false), 150)
	w.both = []int64{w.prov, w.accepted}
	return w
}

// cdWant is the stored payload, decoded fresh; redacted drops the two keys.
func cdWant(t *testing.T, w *cdWorld, id int64, redacted bool) map[string]any {
	t.Helper()
	b, err := json.Marshal(w.stored[id])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if redacted {
		for _, k := range cdKeys {
			delete(m, k)
		}
	}
	return m
}

func cdRead(t *testing.T, label string, got audit.Response, id int64) map[string]any {
	t.Helper()
	if len(got.Events) == 0 {
		t.Fatalf("%s: the page is empty", label)
	}
	for _, e := range got.Events {
		if e.ID != strconv.FormatInt(id, 10) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatalf("%s: row %d payload %s: %v", label, id, e.Payload, err)
		}
		return m
	}
	t.Fatalf("%s: row %d was not read", label, id)
	return nil
}

// cdAssertPayloads compares each row's payload decoded, not as bytes (jsonb reorders keys).
func cdAssertPayloads(t *testing.T, w *cdWorld, label string, got audit.Response, ids []int64, redacted bool) {
	t.Helper()
	for _, id := range ids {
		if have, want := cdRead(t, label, got, id), cdWant(t, w, id, redacted); !reflect.DeepEqual(have, want) {
			t.Errorf("%s: row %d payload %v, want %v", label, id, have, want)
		}
	}
}

func cdIDs(got audit.Response) []int64 {
	var out []int64
	for _, e := range got.Events {
		var id int64
		fmt.Sscan(e.ID, &id)
		out = append(out, id)
	}
	return out
}

func cdSearch(t *testing.T, f *fixture, ctx context.Context, q string, events ...string) audit.Response {
	t.Helper()
	return csList(t, f, ctx, audit.Filter{Limit: 50, Q: q, Events: events})
}

// An admin reads the stored payload.
func TestCreatedAcceptedDetail_AdminReadsTheKeys(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)

	got := csList(t, f, w.ctx["A"], audit.Filter{Limit: 50})

	cdAssertPayloads(t, w, "admin", got, []int64{w.prov, w.accepted, w.old}, false)
}

// The row's actor reads their own keys.
func TestCreatedAcceptedDetail_ActorReadsOwnKeys(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)

	got := csList(t, f, w.mCtx, audit.Filter{Limit: 50})

	for _, id := range w.both {
		p := cdRead(t, "actor", got, id)
		if p["ip_address"] != cdIP || p["device"] != cdDevice {
			t.Errorf("actor: row %d payload %v, want ip_address %q and device %q", id, p, cdIP, cdDevice)
		}
	}
}

// A full-scope member who is not the actor reads both rows without the keys.
func TestCreatedAcceptedDetail_OtherFullScopeMemberReadsTheRowWithoutTheKeys(t *testing.T) {
	f := requireFixture(t)
	t.Run("firm all-clients member", func(t *testing.T) {
		w := cdNew(t, f)

		got := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50})

		cdAssertPayloads(t, w, "N", got, w.both, true)
	})
	t.Run("in-house", func(t *testing.T) {
		super := csSuper(t)
		w := &cdWorld{aeFirm: &aeFirm{aeWorld: aeWorld{p: pageSeedTenant(t, f)}}, m: uuid.NewString(), stored: map[int64]map[string]any{}}
		aeKind(t, super, w.p.tenant, "in_house")
		csMemberUser(t, super, w.m, w.p.tenant, "preparer", false)
		preparer := csMember(t, super, w.p.tenant, "preparer", false)
		w.prov = cdSeed(t, f, &w.aeWorld, w.stored, w.m, cdProvisioned, cdProvisionedPayload(w.p.tenant, w.m, "Kestrel Works", true), 170)
		w.accepted = cdSeed(t, f, &w.aeWorld, w.stored, w.m, cdAccepted, cdAcceptedPayload(true), 160)

		got := csList(t, f, preparer, audit.Filter{Limit: 50})

		cdAssertPayloads(t, w, "in-house preparer", got, []int64{w.prov, w.accepted}, true)
	})
}

// Control: FIRMS-04 hides workspace-level rows from a limited member and an unassigned one.
func TestCreatedAcceptedDetail_LimitedMemberReadsNeitherRow(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	for _, c := range []struct {
		name    string
		control int64 // a row the member does read
	}{{"L", w.c1}, {"U", w.aeFirm.acc["U"][0]}} {
		got := csList(t, f, w.ctx[c.name], audit.Filter{Limit: 50})
		ids := cdIDs(got)
		if !slices.Contains(ids, c.control) {
			t.Fatalf("%s: read %v, want the control row %d", c.name, ids, c.control)
		}
		for _, id := range []int64{w.prov, w.accepted, w.old} {
			if slices.Contains(ids, id) {
				t.Errorf("%s read workspace-level row %d", c.name, id)
			}
		}
	}
}

// Search by the IP or device label finds the rows for an admin and the actor, not for N.
func TestCreatedAcceptedDetail_SearchCannotReachTheKeys(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	events := []string{cdProvisioned, cdAccepted}

	for _, q := range []string{cdIP, cdDevice} {
		t.Run("non-actor member, "+q, func(t *testing.T) {
			for _, got := range []audit.Response{cdSearch(t, f, w.ctx["N"], q, events...), cdSearch(t, f, w.ctx["N"], q)} {
				if len(got.Events) != 0 || got.Total != 0 {
					t.Errorf("page %v, total %d, want none: the search matched a key N may not read", cdIDs(got), got.Total)
				}
				if v := aeFacetValues(got.Facets.Event); slices.Contains(v, cdProvisioned) || slices.Contains(v, cdAccepted) {
					t.Errorf("event facet %v counts a row only the keys matched", v)
				}
				if n := aeSum(got.Facets.Actor); n != 0 {
					t.Errorf("actor facet sums to %d, want 0", n)
				}
			}
		})
		t.Run("admin, "+q, func(t *testing.T) {
			got := cdSearch(t, f, w.ctx["A"], q, events...)
			csAssertRows(t, "admin", got, w.both)
		})
		t.Run("actor, "+q, func(t *testing.T) {
			got := cdSearch(t, f, w.mCtx, q, events...)
			csAssertRows(t, "actor", got, w.both)
		})
	}
	t.Run("control: a stored key that stays readable", func(t *testing.T) {
		got := cdSearch(t, f, w.ctx["N"], "Kestrel Works")
		csAssertRows(t, "N, name", got, []int64{w.prov})
	})
}

// Control: a row stored without the keys reads as stored for every caller.
func TestCreatedAcceptedDetail_OldRowReadsUnchanged(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	for name, ctx := range map[string]context.Context{"A": w.ctx["A"], "M": w.mCtx, "N": w.ctx["N"]} {
		got := csList(t, f, ctx, audit.Filter{Limit: 50})
		cdAssertPayloads(t, w, name, got, []int64{w.old}, false)
	}
}

// Control: only the two events are redacted; account.* and membership.removed keep ip_address and device.
func TestCreatedAcceptedDetail_OtherEventsKeepTheirPayload(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	removed := cdSeed(t, f, &w.aeWorld, w.stored, w.m, "membership.removed",
		map[string]any{"user_id": uuid.NewString(), "ip_address": cdIP, "device": cdDevice}, 140)
	own := w.aeFirm.acc["N"][0]
	w.stored[own] = map[string]any{"ip_address": "198.51.100.7", "device": "Safari on iOS", "occurred_at": "2026-10-29T08:00:00Z"} // aePayload

	got := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50})

	cdAssertPayloads(t, w, "N", got, []int64{removed, own}, false)
}

// Control: an in-process (non-UUID) subject is admin and reads the keys.
func TestCreatedAcceptedDetail_NonUUIDSubjectReadsTheKeys(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	system := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", Role: "authenticated", TenantID: w.p.tenant})

	got := csList(t, f, system, audit.Filter{Limit: 50})

	cdAssertPayloads(t, w, "non-UUID subject", got, w.both, false)
}

// Control (RLS): M is admin and actor in workspace Y; X rows stay out of every answer.
func TestCreatedAcceptedDetail_CrossTenantReaderSeesNothing(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	y := &aeWorld{p: pageSeedTenant(t, f)}
	yCtx := csMemberUser(t, csSuper(t), w.m, y.p.tenant, "admin", false)
	yRow := cdSeed(t, f, y, nil, w.m, cdProvisioned, cdProvisionedPayload(y.p.tenant, w.m, "Other Works", true), 100)

	listed := csList(t, f, yCtx, audit.Filter{Limit: 50})
	searched := cdSearch(t, f, yCtx, cdIP)

	csAssertRows(t, "Y list", listed, []int64{yRow})
	csAssertRows(t, "Y search", searched, []int64{yRow})
	for _, id := range []int64{w.prov, w.accepted, w.old} {
		if slices.Contains(cdIDs(listed), id) || slices.Contains(cdIDs(searched), id) {
			t.Errorf("a reader in workspace Y read row %d of workspace X", id)
		}
	}
}

// An admin in Y who is an all-clients preparer in X, not the actor, reads X without the keys.
func TestCreatedAcceptedDetail_AdminElsewhereIsNotAdminHere(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := cdNew(t, f)
	y := &aeWorld{p: pageSeedTenant(t, f)}
	v := uuid.NewString()
	csMemberUser(t, super, v, y.p.tenant, "admin", false)
	vInX := csMemberUser(t, super, v, w.p.tenant, "preparer", true)

	got := csList(t, f, vInX, audit.Filter{Limit: 50})

	cdAssertPayloads(t, w, "V in X", got, w.both, true)
	if n := cdSearch(t, f, vInX, cdIP).Total; n != 0 {
		t.Errorf("V in X: search for the IP matched %d rows, want 0", n)
	}
}

// Pages of one row each: the bound redaction values sit after the cursor values and before the limit.
func TestCreatedAcceptedDetail_RedactionHoldsOnEveryCursorPage(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	var cur *audit.Cursor
	seen := map[int64]bool{}
	for page := 0; page < 20; page++ {
		got := csList(t, f, w.ctx["N"], audit.Filter{Limit: 1, Cursor: cur})
		if len(got.Events) != 1 {
			t.Fatalf("page %d holds %d rows, want 1", page, len(got.Events))
		}
		id := cdIDs(got)[0]
		seen[id] = true
		if slices.Contains(w.both, id) {
			cdAssertPayloads(t, w, fmt.Sprintf("N, page %d", page), got, []int64{id}, true)
		}
		if got.Page.NextCursor == nil {
			break
		}
		c, err := audit.DecodeCursor(*got.Page.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		cur = &c
	}
	for _, id := range w.both {
		if !seen[id] {
			t.Errorf("paging never reached row %d", id)
		}
	}
}

// ILIKE matches a fragment in any letter case, so the search arm must hide a fragment of a withheld value too.
func TestCreatedAcceptedDetail_PartialAndMixedCaseSearchCannotReachTheKeys(t *testing.T) {
	f := requireFixture(t)
	w := cdNew(t, f)
	for _, q := range []string{"203.0.113", "113.7", "chrome on", "MACOS", "Chrome"} {
		if got := cdSearch(t, f, w.ctx["N"], q); len(got.Events) != 0 || got.Total != 0 {
			t.Errorf("N, %q: page %v, total %d, want none", q, cdIDs(got), got.Total)
		}
		csAssertRows(t, "admin, "+q, cdSearch(t, f, w.ctx["A"], q, cdProvisioned, cdAccepted), w.both)
	}
}
