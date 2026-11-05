package approval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Clause names in failure messages: check 1 and 2 = requireCatchAllLastTx (top-version and in-force
// views); check 3 = the last live policy leaves the last place while a scoped policy exists.

const reorderEvent = "approval_policy.reordered"

var updateOfPolicies = regexp.MustCompile(`(?i)UPDATE\s+approval_policies\b`)

// livePositions reads id -> position of the tenant's live policies as the superuser.
func livePositions(t *testing.T, super *pgxpool.Pool, tenantID string) map[string]int {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, position FROM approval_policies WHERE tenant_id = $1 AND deleted_at IS NULL`, tenantID)
	if err != nil {
		t.Fatalf("read positions: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var pos int
		if err := rows.Scan(&id, &pos); err != nil {
			t.Fatalf("scan position: %v", err)
		}
		out[id] = pos
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read positions: %v", err)
	}
	return out
}

func listedIDs(t *testing.T, store *Store, c context.Context) []string {
	t.Helper()
	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

func idsOf(list []Policy) []string {
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

// assertPositionsUnchanged is the "a refused or no-op reorder wrote nothing" read-back; a non-empty
// before-map keeps it from passing over zero rows.
func assertPositionsUnchanged(t *testing.T, super *pgxpool.Pool, tenantID string, before map[string]int) {
	t.Helper()
	if len(before) == 0 {
		t.Fatal("the before-map is empty, so the unchanged check proves nothing")
	}
	if after := livePositions(t, super, tenantID); !reflect.DeepEqual(after, before) {
		t.Errorf("positions after = %v, want unchanged %v", after, before)
	}
}

// seedUnscoped seeds one live, unscoped, draft-only policy per name; ids come back in seed order.
func seedUnscoped(t *testing.T, super *pgxpool.Pool, tenantID string, names ...string) []string {
	t.Helper()
	ids := make([]string, 0, len(names))
	for _, n := range names {
		id, _ := seedPolicyShape(t, super, tenantID, n, false, false)
		ids = append(ids, id)
	}
	return ids
}

func reorderTenant(t *testing.T, name string) (super, app *pgxpool.Pool, tenantID string, c context.Context) {
	t.Helper()
	super, app = dbTestPools(t)
	tenantID = policyTenant(t, super, name)
	c, _ = activeAdmin(t, super, tenantID)
	return super, app, tenantID, c
}

// --- AC1 ------------------------------------------------------------------------

func TestReorderPolicies_SetsTheOrderAndAnswersTheList(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 sets-order")
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B", "Gone", "C")
	a, b, gone, cc := ids[0], ids[1], ids[2], ids[3]
	softDeleteApprovalPolicy(t, super, gone)
	if got := livePositions(t, super, tenantID); got[a] != 1 || got[b] != 2 || got[cc] != 4 || len(got) != 3 {
		t.Fatalf("fixture positions = %v, want A 1, B 2, C 4 (a soft-deleted row holds 3)", got)
	}

	list, err := store.ReorderPolicies(c, []string{b, a, cc})
	if err != nil {
		t.Fatalf("ReorderPolicies: %v", err)
	}
	if got, want := idsOf(list), []string{b, a, cc}; len(list) == 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("answer order = %v, want [B A C] = %v", got, want)
	}
	if got, want := listedIDs(t, store, c), []string{b, a, cc}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListPolicies order = %v, want %v", got, want)
	}
	got := livePositions(t, super, tenantID)
	if got[b] != 1 || got[a] != 2 || got[cc] != 4 {
		t.Errorf("positions = %v, want B 1, A 2, C 4: the old live positions, permuted, the gap kept", got)
	}
	for _, p := range list {
		if p.Position != got[p.ID] {
			t.Errorf("answer position of %s = %d, stored %d", p.ID, p.Position, got[p.ID])
		}
	}
}

func TestReorderPolicies_UppercaseIdsAreCanonicalised(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 canonical")
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B")

	list, err := store.ReorderPolicies(c, []string{strings.ToUpper(ids[1]), strings.ToUpper(ids[0])})
	if err != nil {
		t.Fatalf("ReorderPolicies with upper-case ids: %v, want success (ids are canonicalised)", err)
	}
	if got, want := idsOf(list), []string{ids[1], ids[0]}; !reflect.DeepEqual(got, want) {
		t.Errorf("answer order = %v, want %v", got, want)
	}
}

func TestReorderPolicies_ThreeCycleInOneStatement(t *testing.T) {
	super, _, tenantID, c := reorderTenant(t, "APSC-02-04 three-cycle")
	traced, rec := tracedAppPool(t)
	store := NewStore(traced, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B", "C")
	a, b, cc := ids[0], ids[1], ids[2]

	rec.reset()
	list, err := store.ReorderPolicies(c, []string{cc, a, b})
	if err != nil {
		t.Fatalf("ReorderPolicies: %v", err)
	}
	if got, want := idsOf(list), []string{cc, a, b}; !reflect.DeepEqual(got, want) {
		t.Errorf("answer order = %v, want [C A B] = %v", got, want)
	}
	updates := 0
	for _, s := range rec.mentioning("approval_policies") {
		if updateOfPolicies.MatchString(s) {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("UPDATE approval_policies statements = %d, want 1: a cycle needs one statement (the position constraint is checked at its end)", updates)
	}
}

// --- AC2 ------------------------------------------------------------------------

func TestReorderPolicies_PreparerIs403AndNothingChanges(t *testing.T) {
	super, _, tenantID, _ := reorderTenant(t, "APSC-02-04 preparer")
	traced, rec := tracedAppPool(t)
	store := NewStore(traced, stubFingerprinter, nil)
	pc, _ := callerCtx(t, super, tenantID, "preparer", "active")
	ids := seedUnscoped(t, super, tenantID, "A", "B")
	before := livePositions(t, super, tenantID)

	rec.reset()
	_, err := store.ReorderPolicies(pc, []string{ids[1], ids[0]})
	if !errors.Is(err, ErrNotPermitted) {
		t.Errorf("err = %v, want ErrNotPermitted for a preparer (admin check)", err)
	}
	assertPositionsUnchanged(t, super, tenantID, before)
	if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", reorderEvent, n)
	}
	stmts := rec.mentioning("")
	adminAt := -1
	for i, s := range stmts {
		if strings.Contains(s, "SELECT role FROM memberships") {
			adminAt = i
			break
		}
	}
	if adminAt < 0 {
		t.Fatalf("no admin check among the statements:\n%v", stmts)
	}
	for _, s := range stmts[adminAt+1:] {
		if strings.Contains(s, "approval_policies") {
			t.Errorf("a statement after the admin check names approval_policies:\n%s", s)
		}
	}
}

func TestReorderPolicies_PreparerNoOpIs403(t *testing.T) {
	super, app, tenantID, _ := reorderTenant(t, "APSC-02-04 preparer-noop")
	store := NewStore(app, stubFingerprinter, nil)
	pc, _ := callerCtx(t, super, tenantID, "preparer", "active")
	ids := seedUnscoped(t, super, tenantID, "A", "B")

	if _, err := store.ReorderPolicies(pc, ids); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("same-order body as a preparer: err = %v, want ErrNotPermitted (the no-op answer is still admin-only)", err)
	}
}

// --- AC3 ------------------------------------------------------------------------

// Rows written in one transaction share an xmin; the audit row must share it with the moved rows.
func TestReorderPolicies_AuditsFromAndToOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-04 audit")
	c, adminID := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B")
	a, b := ids[0], ids[1]

	if _, err := store.ReorderPolicies(c, []string{b, a}); err != nil {
		t.Fatalf("ReorderPolicies: %v", err)
	}
	if n := auditCount(t, super, tenantID, reorderEvent); n != 1 {
		t.Fatalf("%s rows = %d, want 1", reorderEvent, n)
	}
	var policyXmin, auditXmin, actor, payload string
	if err := super.QueryRow(context.Background(),
		`SELECT p.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM approval_policies p, audit_log a
		  WHERE p.id = $1::uuid AND a.tenant_id = $2 AND a.event = $3`,
		a, tenantID, reorderEvent).Scan(&policyXmin, &auditXmin, &actor, &payload); err != nil {
		t.Fatalf("xmin join: %v", err)
	}
	if policyXmin == "0" || policyXmin == "2" || policyXmin != auditXmin {
		t.Errorf("xmin: moved policy = %s, audit row = %s, want one shared transaction", policyXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}
	var body struct {
		From []string `json:"from_order"`
		To   []string `json:"to_order"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("payload %q: %v", payload, err)
	}
	if !reflect.DeepEqual(body.From, []string{a, b}) || !reflect.DeepEqual(body.To, []string{b, a}) {
		t.Errorf("payload from_order = %v, to_order = %v, want [A B] and [B A] (A=%s B=%s)", body.From, body.To, a, b)
	}
}

// --- AC4 ------------------------------------------------------------------------

func TestReorderPolicies_StaleListIs409(t *testing.T) {
	cases := []struct {
		name string
		body func(t *testing.T, super *pgxpool.Pool, live []string, tenantID string) []string
	}{
		{"a live id is missing", func(_ *testing.T, _ *pgxpool.Pool, live []string, _ string) []string {
			return []string{live[2], live[0]}
		}},
		{"an extra random uuid", func(_ *testing.T, _ *pgxpool.Pool, live []string, _ string) []string {
			return []string{live[2], live[1], live[0], uuid.NewString()}
		}},
		{"another tenant's policy id", func(t *testing.T, super *pgxpool.Pool, live []string, _ string) []string {
			other := policyTenant(t, super, "APSC-02-04 stale other tenant")
			foreign, _ := seedPolicyShape(t, super, other, "Foreign", false, false)
			return []string{live[2], live[1], foreign}
		}},
		{"a soft-deleted id of this tenant", func(t *testing.T, super *pgxpool.Pool, live []string, tenantID string) []string {
			gone, _ := seedPolicyShape(t, super, tenantID, "Gone", false, false)
			softDeleteApprovalPolicy(t, super, gone)
			return []string{live[2], live[1], live[0], gone}
		}},
		{"an empty list with live policies", func(*testing.T, *pgxpool.Pool, []string, string) []string {
			return []string{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, app, tenantID, c := reorderTenant(t, "APSC-02-04 stale")
			store := NewStore(app, stubFingerprinter, nil)
			live := seedUnscoped(t, super, tenantID, "A", "B", "C")
			body := tc.body(t, super, live, tenantID)
			before := livePositions(t, super, tenantID)

			_, err := store.ReorderPolicies(c, body)
			if !errors.Is(err, ErrPolicyOrderStale) {
				t.Errorf("err = %v, want ErrPolicyOrderStale (the body is not exactly the live set)", err)
			}
			assertPositionsUnchanged(t, super, tenantID, before)
			if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
				t.Errorf("%s rows = %d, want 0 for a refused reorder", reorderEvent, n)
			}
		})
	}
}

// RLS: another tenant's ids neither move its rows nor read differently from an unknown id.
func TestReorderPolicies_AnotherTenantsIdsNeverMoveItsRows(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 cross-tenant")
	store := NewStore(app, stubFingerprinter, nil)
	mine := seedUnscoped(t, super, tenantID, "A", "B")
	otherID := policyTenant(t, super, "APSC-02-04 cross-tenant victim")
	theirs := seedUnscoped(t, super, otherID, "X", "Y")
	beforeMine := livePositions(t, super, tenantID)
	beforeTheirs := livePositions(t, super, otherID)

	for name, body := range map[string][]string{
		"their ids only, reversed":  {theirs[1], theirs[0]},
		"mine plus one of theirs":   {mine[1], mine[0], theirs[0]},
		"theirs swapped into mine":  {theirs[0], mine[1]},
		"all four, theirs reversed": {mine[0], mine[1], theirs[1], theirs[0]},
	} {
		if _, err := store.ReorderPolicies(c, body); !errors.Is(err, ErrPolicyOrderStale) {
			t.Errorf("%s: err = %v, want ErrPolicyOrderStale", name, err)
		}
	}
	assertPositionsUnchanged(t, super, otherID, beforeTheirs)
	assertPositionsUnchanged(t, super, tenantID, beforeMine)
	if n := auditCount(t, super, otherID, reorderEvent) + auditCount(t, super, tenantID, reorderEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", reorderEvent, n)
	}
}

func TestReorderPolicies_MalformedOrDuplicateIdIs400(t *testing.T) {
	cases := []struct {
		name string
		body func(live []string) []string
	}{
		{"a malformed id", func(live []string) []string { return []string{"not-a-uuid", live[0]} }},
		{"a live id twice", func(live []string) []string { return []string{live[0], live[1], live[0]} }},
		{"one id in two cases", func(live []string) []string {
			return []string{live[0], live[1], strings.ToUpper(live[0])}
		}},
		{"an empty-string id", func(live []string) []string { return []string{"", live[0]} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, _, tenantID, c := reorderTenant(t, "APSC-02-04 malformed")
			traced, rec := tracedAppPool(t)
			store := NewStore(traced, stubFingerprinter, nil)
			live := seedUnscoped(t, super, tenantID, "A", "B")
			before := livePositions(t, super, tenantID)

			rec.reset()
			_, err := store.ReorderPolicies(c, tc.body(live))
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation (id parse / duplicate clause, above the tx)", err)
			}
			if stmts := rec.mentioning(""); len(stmts) != 0 {
				t.Errorf("%d statement(s) ran for an invalid body, want none: %v", len(stmts), stmts)
			}
			assertPositionsUnchanged(t, super, tenantID, before)
		})
	}
}

// --- AC5 / AC6: the catch-all ----------------------------------------------------

func TestReorderPolicies_ScopedPolicyMovedLastIs409(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 scoped-last")
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, false)
	cc, _ := seedPolicyShape(t, super, tenantID, "C", false, false)
	before := livePositions(t, super, tenantID)

	_, err := store.ReorderPolicies(c, []string{cc, s})
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Errorf("err = %v, want ErrPolicyCatchAll (check 1 and 3: scoped S would be last)", err)
	}
	assertPositionsUnchanged(t, super, tenantID, before)
	if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", reorderEvent, n)
	}
}

// Last place is unchanged, so check 3 passes; check 2 alone must refuse.
func TestReorderPolicies_InForceViewRefuses(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 in-force")
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, true)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, true)
	cc, _ := seedPolicyShape(t, super, tenantID, "C", false, false)
	before := livePositions(t, super, tenantID)

	_, err := store.ReorderPolicies(c, []string{u, s, cc})
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Errorf("err = %v, want ErrPolicyCatchAll (check 2: S would be the last policy in force)", err)
	}
	assertPositionsUnchanged(t, super, tenantID, before)
}

func TestReorderPolicies_UnscopedPolicyMovedBelowTheCatchAllIs409(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 below-catchall")
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, false)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, false)
	cc, _ := seedPolicyShape(t, super, tenantID, "C", false, false)
	before := livePositions(t, super, tenantID)

	_, err := store.ReorderPolicies(c, []string{s, cc, u})
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Errorf("err = %v, want ErrPolicyCatchAll (check 3: the last policy C leaves last place while S is scoped)", err)
	}
	if got, want := listedIDs(t, store, c), []string{s, u, cc}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want unchanged %v", got, want)
	}
	assertPositionsUnchanged(t, super, tenantID, before)
	if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0", reorderEvent, n)
	}
}

// Over-refusal guard: the catch-all keeps its place, everything above it may move.
func TestReorderPolicies_KeepingTheLastPolicyInPlaceSucceeds(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 keep-last")
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, true)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, false)
	cc, _ := seedPolicyShape(t, super, tenantID, "C", false, true)

	list, err := store.ReorderPolicies(c, []string{u, s, cc})
	if err != nil {
		t.Fatalf("ReorderPolicies: %v, want success (the last policy stays last)", err)
	}
	if got, want := idsOf(list), []string{u, s, cc}; !reflect.DeepEqual(got, want) {
		t.Errorf("answer order = %v, want %v", got, want)
	}
	if n := auditCount(t, super, tenantID, reorderEvent); n != 1 {
		t.Errorf("%s rows = %d, want 1", reorderEvent, n)
	}
}

// S's active v1 is scoped, its newer draft v2 is not: the active version alone pins the last place.
func TestReorderPolicies_ActiveScopeAlonePinsTheLast(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 active-scope")
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, true)
	seedApprovalPolicyVersionN(t, super, tenantID, s, 2)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, false)
	cc, _ := seedPolicyShape(t, super, tenantID, "C", false, true)
	before := livePositions(t, super, tenantID)

	_, err := store.ReorderPolicies(c, []string{s, cc, u})
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Errorf("err = %v, want ErrPolicyCatchAll (check 3, active leg: S's active v1 is scoped)", err)
	}
	assertPositionsUnchanged(t, super, tenantID, before)
}

func TestReorderPolicies_WithNoScopedPolicyAnyPolicyMovesLast(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 no-scope")
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B", "C")

	list, err := store.ReorderPolicies(c, []string{ids[2], ids[1], ids[0]})
	if err != nil {
		t.Fatalf("ReorderPolicies: %v, want success (no scoped policy, no pin)", err)
	}
	if got, want := idsOf(list), []string{ids[2], ids[1], ids[0]}; !reflect.DeepEqual(got, want) {
		t.Errorf("answer order = %v, want %v", got, want)
	}
}

// --- AC7: the no-op --------------------------------------------------------------

func TestReorderPolicies_EmptyWorkspaceIsANoOp(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 empty")
	store := NewStore(app, stubFingerprinter, nil)

	list, err := store.ReorderPolicies(c, []string{})
	if err != nil {
		t.Fatalf("ReorderPolicies([]) in an empty workspace: %v, want 200", err)
	}
	if list == nil || len(list) != 0 {
		t.Errorf("answer = %#v, want a non-nil empty list", list)
	}
	if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
		t.Errorf("%s rows = %d, want 0 for a no-op", reorderEvent, n)
	}
}

func TestReorderPolicies_SameOrderWritesNothing(t *testing.T) {
	for _, names := range [][]string{{"A", "B"}, {"A"}} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			super, _, tenantID, c := reorderTenant(t, "APSC-02-04 same-order")
			traced, rec := tracedAppPool(t)
			store := NewStore(traced, stubFingerprinter, nil)
			ids := seedUnscoped(t, super, tenantID, names...)
			before := livePositions(t, super, tenantID)

			rec.reset()
			list, err := store.ReorderPolicies(c, ids)
			if err != nil {
				t.Fatalf("ReorderPolicies(same order): %v, want 200", err)
			}
			if got := idsOf(list); len(list) == 0 || !reflect.DeepEqual(got, ids) {
				t.Errorf("answer order = %v, want %v", got, ids)
			}
			assertPositionsUnchanged(t, super, tenantID, before)
			if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
				t.Errorf("%s rows = %d, want 0 for a no-op", reorderEvent, n)
			}
			for _, s := range rec.mentioning("approval_policies") {
				if updateOfPolicies.MatchString(s) {
					t.Errorf("a no-op reorder issued an UPDATE of approval_policies:\n%s", s)
				}
			}
			if len(rec.mentioning("SELECT role FROM memberships")) == 0 {
				t.Error("no admin check ran: the no-op answer must still be admin-only")
			}
		})
	}
}

// --- AC8: races ------------------------------------------------------------------

// raceBoth releases two functions at once and returns their errors.
func raceBoth(f, g func() error) (ferr, gerr error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; ferr = f() }()
	go func() { defer wg.Done(); <-start; gerr = g() }()
	close(start)
	wg.Wait()
	return ferr, gerr
}

func TestReorderPolicies_ConcurrentDeleteEndsStaleOrApplied(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 race-delete")
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B", "C")
	a, b, cc := ids[0], ids[1], ids[2]

	reorderErr, deleteErr := raceBoth(
		func() error { _, err := store.ReorderPolicies(c, []string{cc, b, a}); return err },
		func() error { _, err := store.DeletePolicy(c, b); return err },
	)
	if deleteErr != nil {
		t.Errorf("DeletePolicy(B): %v, want success", deleteErr)
	}
	got := listedIDs(t, store, c)
	switch {
	case reorderErr == nil:
		if want := []string{cc, a}; !reflect.DeepEqual(got, want) {
			t.Errorf("reorder applied: final order = %v, want [C A] = %v", got, want)
		}
	case errors.Is(reorderErr, ErrPolicyOrderStale):
		if want := []string{a, cc}; !reflect.DeepEqual(got, want) {
			t.Errorf("reorder stale: final order = %v, want [A C] = %v", got, want)
		}
		if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
			t.Errorf("a stale reorder wrote %d audit row(s)", n)
		}
	default:
		t.Errorf("reorder err = %v, want nil or ErrPolicyOrderStale", reorderErr)
	}
}

func TestReorderPolicies_ConcurrentCreateEndsStaleOrApplied(t *testing.T) {
	super, app, tenantID, c := reorderTenant(t, "APSC-02-04 race-create")
	store := NewStore(app, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B", "C")
	a, b, cc := ids[0], ids[1], ids[2]

	var created Policy
	reorderErr, createErr := raceBoth(
		func() error { _, err := store.ReorderPolicies(c, []string{b, a, cc}); return err },
		func() (err error) { created, err = store.CreatePolicy(c, "N", nil); return err },
	)
	if createErr != nil {
		t.Fatalf("CreatePolicy(N): %v, want success", createErr)
	}
	got := listedIDs(t, store, c)
	switch {
	case reorderErr == nil:
		if want := []string{b, a, created.ID, cc}; !reflect.DeepEqual(got, want) {
			t.Errorf("reorder applied: final order = %v, want [B A N C] = %v", got, want)
		}
	case errors.Is(reorderErr, ErrPolicyOrderStale):
		if want := []string{a, b, created.ID, cc}; !reflect.DeepEqual(got, want) {
			t.Errorf("reorder stale: final order = %v, want [A B N C] = %v", got, want)
		}
		if n := auditCount(t, super, tenantID, reorderEvent); n != 0 {
			t.Errorf("a stale reorder wrote %d audit row(s)", n)
		}
	default:
		t.Errorf("reorder err = %v, want nil or ErrPolicyOrderStale", reorderErr)
	}
}

// --- AC9: lock order -------------------------------------------------------------

func TestReorderPolicies_TakesTheTenantLockAfterTheAdminCheck(t *testing.T) {
	const lock = "SELECT pg_advisory_xact_lock(hashtext($1))"
	super, _, tenantID, c := reorderTenant(t, "APSC-02-04 lock-order")
	traced, rec := tracedAppPool(t)
	store := NewStore(traced, stubFingerprinter, nil)
	ids := seedUnscoped(t, super, tenantID, "A", "B")

	rec.reset()
	if _, err := store.ReorderPolicies(c, []string{ids[1], ids[0]}); err != nil {
		t.Fatalf("ReorderPolicies: %v", err)
	}
	stmts := rec.mentioning("")
	adminAt := -1
	for i, s := range stmts {
		if strings.Contains(s, "SELECT role FROM memberships") {
			adminAt = i
			break
		}
	}
	if adminAt < 0 || adminAt+2 >= len(stmts) {
		t.Fatalf("admin check not followed by two statements:\n%v", stmts)
	}
	if got := strings.TrimSpace(stmts[adminAt+1]); got != lock {
		t.Errorf("the statement after the admin check is:\n%s\nwant %s", got, lock)
	}
	rows := stmts[adminAt+2]
	if !strings.Contains(rows, "approval_policies") || !strings.Contains(rows, "deleted_at IS NULL") ||
		!strings.Contains(rows, "FOR UPDATE") {
		t.Errorf("the statement after the lock is:\n%s\nwant the live-row SELECT ... FOR UPDATE on approval_policies", rows)
	}
}
