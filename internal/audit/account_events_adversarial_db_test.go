// AUDIT-13-05 QA: the account rule follows the caller's role at read time and counts nothing it hides.
package audit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func aeRole(t *testing.T, tenant, user, role string) {
	t.Helper()
	if _, err := csSuper(t).Exec(context.Background(),
		`UPDATE memberships SET role = $3 WHERE tenant_id = $1 AND user_id = $2`, tenant, user, role); err != nil {
		t.Fatalf("set role %s: %v", role, err)
	}
}

// The same identity, before and after its role changes: the next request reads the new role.
func TestAccountEvents_RoleChangeTakesEffectOnTheNextRequest(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := &aeWorld{p: pageSeedTenant(t, f)}
	aeKind(t, super, w.p.tenant, "in_house")
	a, p := uuid.NewString(), uuid.NewString()
	adminCtx := csMemberUser(t, super, a, w.p.tenant, "admin", false)
	prepCtx := csMemberUser(t, super, p, w.p.tenant, "preparer", false)
	aRow := w.account(t, f, a, aeSignedIn, "Chrome on macOS", 300)
	pRow := w.account(t, f, p, aeSignedIn, "Safari on iOS", 290)

	csAssertRows(t, "admin before demotion", csList(t, f, adminCtx, audit.Filter{Limit: 50}), []int64{aRow, pRow})
	csAssertRows(t, "preparer before promotion", csList(t, f, prepCtx, audit.Filter{Limit: 50}), []int64{pRow})

	aeRole(t, w.p.tenant, a, "preparer")
	aeRole(t, w.p.tenant, p, "admin")

	csAssertRows(t, "demoted admin", csList(t, f, adminCtx, audit.Filter{Limit: 50}), []int64{aRow})
	csAssertRows(t, "promoted preparer", csList(t, f, prepCtx, audit.Filter{Limit: 50}), []int64{aRow, pRow})
}

// In a firm the demotion also narrows the client scope: an ex-admin with no client reads only its own account rows.
func TestAccountEvents_DemotedFirmAdminReadsOnlyItsOwn(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	ctx := w.ctx["A"]
	csAssertRows(t, "firm admin before", csList(t, f, ctx, audit.Filter{Limit: 50}), w.want()["A"])

	aeRole(t, w.p.tenant, w.ids["A"], "reviewer")

	csAssertRows(t, "firm ex-admin", csList(t, f, ctx, audit.Filter{Limit: 50}), w.acc["A"])
}

// A suspended or removed admin gets the refusal and no row, not a reduced page.
func TestAccountEvents_InactiveAdminIsRefused(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	for _, status := range []string{"suspended", "removed"} {
		t.Run(status, func(t *testing.T) {
			w := &aeWorld{p: pageSeedTenant(t, f)}
			a := uuid.NewString()
			ctx := csMemberUser(t, super, a, w.p.tenant, "admin", false)
			row := w.account(t, f, a, aeSignedIn, "Chrome on macOS", 300)
			csAssertRows(t, "active control", csList(t, f, ctx, audit.Filter{Limit: 50}), []int64{row})

			if _, err := super.Exec(context.Background(),
				`UPDATE memberships SET status = $3 WHERE tenant_id = $1 AND user_id = $2`, w.p.tenant, a, status); err != nil {
				t.Fatalf("set status %s: %v", status, err)
			}

			got, err := audit.NewStore(f.app).List(ctx, audit.Filter{Limit: 50})
			if !errors.Is(err, db.ErrNotActiveMember) {
				t.Errorf("%s admin: err = %v, want ErrNotActiveMember", status, err)
			}
			if len(got.Events) != 0 || got.Total != 0 {
				t.Errorf("%s admin read %d rows, total %d, want none", status, len(got.Events), got.Total)
			}
		})
	}
}

// The three facets and the actor-kind filter count and show only what the member may read.
func TestAccountEvents_FacetsAndActorKindStaySelfOnly(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)

	for _, member := range []string{"L", "N", "U"} {
		exp := w.want()[member]
		if len(exp) == 0 {
			t.Fatalf("%s: oracle expects no rows", member)
		}
		got := csList(t, f, w.ctx[member], audit.Filter{Limit: 50})
		if n := aeSum(got.Facets.Company); n != len(exp) {
			t.Errorf("%s: company facet sums to %d, want %d", member, n, len(exp))
		}
		for _, other := range []string{"A", "L", "N", "U"} {
			if other == member {
				continue
			}
			if slices.Contains(aeFacetValues(got.Facets.Actor), w.ids[other]) && !aeAuthored(w, member, other) {
				t.Errorf("%s: actor facet holds %s, whose only rows are account rows", member, other)
			}
		}
		people := csList(t, f, w.ctx[member], audit.Filter{Limit: 50, ActorKind: "people"})
		// "ae-system" is not the literal system actor, so people keeps every visible row and system keeps none.
		csAssertRows(t, member+", actor_kind=people", people, exp)
		csAssertRows(t, member+", actor_kind=system", csList(t, f, w.ctx[member], audit.Filter{Limit: 50, ActorKind: "system"}), nil)
	}
	// U holds only its own two account rows, so its company facet is the one workspace bucket.
	u := csList(t, f, w.ctx["U"], audit.Filter{Limit: 50})
	if len(u.Facets.Company) != 1 || u.Facets.Company[0].Value != nil || u.Facets.Company[0].Count != 2 {
		t.Errorf("U company facet = %+v, want one workspace bucket of 2", u.Facets.Company)
	}
}

// aeAuthored reports whether other authored a non-account row member may read: A wrote the workspace row N reads.
func aeAuthored(w *aeFirm, member, other string) bool { return member == "N" && other == "A" }

// Each of the six names is bound: a non-admin reads its own row of every name and none of the admin's.
func TestAccountEvents_EverySixNamesAreSelfOnly(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	for _, kind := range []string{"in_house", "firm"} {
		t.Run(kind, func(t *testing.T) {
			w := &aeWorld{p: pageSeedTenant(t, f)}
			aeKind(t, super, w.p.tenant, kind)
			a, p := uuid.NewString(), uuid.NewString()
			admin := csMemberUser(t, super, a, w.p.tenant, "admin", false)
			prep := csMemberUser(t, super, p, w.p.tenant, "preparer", false)
			var own, all []int64
			for i, name := range aeSix {
				all = append(all, w.account(t, f, a, name, "Chrome on macOS", 600-i))
				id := w.account(t, f, p, name, "Safari on iOS", 500-i)
				own, all = append(own, id), append(all, id)
			}
			if len(own) != 6 {
				t.Fatalf("seeded %d own rows, want 6", len(own))
			}
			csAssertRows(t, kind+" non-admin", csList(t, f, prep, audit.Filter{Limit: 50}), own)
			csAssertRows(t, kind+" admin", csList(t, f, admin, audit.Filter{Limit: 50}), all)
		})
	}
}
