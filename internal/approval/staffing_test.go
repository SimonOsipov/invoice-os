package approval

// SetRoleMembers under a real Postgres: the whole-set replace, its 0-based dense `ord`,
// the FK disposition on both legs, and both directions of remove-prunes/suspend-keeps.
//
// Same gate as workflow_roles_test.go — every test here self-skips without DATABASE_URL
// + DATABASE_SUPERUSER_URL. Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI
// the rls job's gate step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- staffing helpers ------------------------------------------------------

// staffingRow is one workflow_role_members row's full image, so "the surviving rows are
// byte-identical" is an assertion about the rows and not about their user_ids.
type staffingRow struct {
	ID, UserID string
	Ord        int
	CreatedAt  time.Time
}

func (r staffingRow) equal(o staffingRow) bool {
	return r.ID == o.ID && r.UserID == o.UserID && r.Ord == o.Ord && r.CreatedAt.Equal(o.CreatedAt)
}

func (r staffingRow) String() string {
	return fmt.Sprintf("{user:%s ord:%d created_at:%s}",
		r.UserID, r.Ord, r.CreatedAt.UTC().Format(time.RFC3339Nano))
}

// staffingRows reads a role's staffing as the superuser — a read-back of what the store
// committed, never a way around RLS for a domain call. user_id breaks the tie so a
// collapsed `ord` still reads deterministically.
func staffingRows(t *testing.T, super *pgxpool.Pool, roleID string) []staffingRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, user_id, ord, created_at FROM workflow_role_members
		  WHERE workflow_role_id = $1 ORDER BY ord, user_id`, roleID)
	if err != nil {
		t.Fatalf("read back staffing for %s: %v", roleID, err)
	}
	defer rows.Close()
	out := []staffingRow{}
	for rows.Next() {
		var r staffingRow
		if err := rows.Scan(&r.ID, &r.UserID, &r.Ord, &r.CreatedAt); err != nil {
			t.Fatalf("scan staffing row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read back staffing for %s: %v", roleID, err)
	}
	return out
}

// staffedUserIDs and staffedOrds split a read-back in two: the served order cannot show
// that the column itself is dense from 0, which is what the DDL promises.
func staffedUserIDs(rows []staffingRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UserID)
	}
	return out
}

func staffedOrds(rows []staffingRow) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Ord)
	}
	return out
}

func sameStaffing(a, b []staffingRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].equal(b[i]) {
			return false
		}
	}
	return true
}

// sent copies the ids on the way in. A store that sorts its argument IN PLACE would
// otherwise rewrite the very expectation it is compared against, and every order
// assertion below would pass vacuously.
func sent(members ...string) []string {
	return append([]string{}, members...)
}

// membersOf returns one role's members as ListRoles served them.
func membersOf(t *testing.T, roles []Role, key string) []string {
	t.Helper()
	for _, r := range roles {
		if r.Key == key {
			return r.Members
		}
	}
	t.Fatalf("ListRoles returned no role %q (got %v)", key, keysOf(roles))
	return nil
}

// seedActiveMembers seeds n active preparer memberships and returns their ids SORTED, so
// insertion order == user_id order and neither can pass for a submitted `ord`.
func seedActiveMembers(t *testing.T, super *pgxpool.Pool, tenantID string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for range n {
		ids = append(ids, uuid.NewString())
	}
	sort.Strings(ids)
	for _, id := range ids {
		seedMembership(t, super, tenantID, id, "preparer", "active")
	}
	return ids
}

// setMembershipStatus and deleteMembership are superuser fixture ops on the roster —
// the two levers whose staffing consequences must differ. Both Fatal unless exactly one
// row moved: a mis-seed would otherwise turn either test into a no-op that passes.
func setMembershipStatus(t *testing.T, super *pgxpool.Pool, tenantID, userID, status string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE memberships SET status = $3 WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID, status)
	if err != nil {
		t.Fatalf("set membership %s status=%s: %v", userID, status, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set membership %s status=%s affected %d rows, want 1", userID, status, tag.RowsAffected())
	}
}

func deleteMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID)
	if err != nil {
		t.Fatalf("delete membership %s: %v", userID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("delete membership %s affected %d rows, want 1", userID, tag.RowsAffected())
	}
}

// staffedAudit reads the tenant's single workflow_role.staffed row. Callers assert the
// count first, so "the single row" is a fact and not this helper's assumption.
func staffedAudit(t *testing.T, super *pgxpool.Pool, tenantID string) (actor string, payload map[string]any) {
	t.Helper()
	var payloadJSON string
	if err := super.QueryRow(context.Background(),
		`SELECT actor, payload::text FROM audit_log
		  WHERE tenant_id = $1 AND event = 'workflow_role.staffed'`, tenantID).Scan(&actor, &payloadJSON); err != nil {
		t.Fatalf("read the workflow_role.staffed audit row: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload %s: %v", payloadJSON, err)
	}
	return actor, payload
}

// pgConstraint extracts the CONSTRAINT NAME from err. The FK cases must prove WHICH
// foreign key refused, not merely that one did (copied from documents_rls_test.go).
func pgConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// --- the subject axis (Decision Q2) ----------------------------------------

// TestStaffing_PreparerMayBeStaffed pins Q2: staffing applies NO access-role check to
// the staffed subject, so a preparer may hold any workflow role. The approver filter
// lives in the satisfaction predicate (APPR-06/07), not here. A reviewer will read this
// as missing validation; it is the user's decision, and this test is what a "hardening"
// into the rejected refuse-to-staff alternative has to argue with.
//
// The caller is an active admin, so this cannot pass by the caller gate being absent.
func TestStaffing_PreparerMayBeStaffed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-preparer")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	preparer := uuid.NewString()
	seedMembership(t, super, tenantID, preparer, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	got, err := store.SetRoleMembers(c, "tax-reviewer", []string{preparer})
	if err != nil {
		t.Fatalf("SetRoleMembers with a preparer subject: %v, want success — Q2 leaves the staffed subject unrestricted", err)
	}
	want := Role{Key: "tax-reviewer", Title: "Tax Reviewer", Desc: "", Members: []string{preparer}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SetRoleMembers = %+v, want %+v", got, want)
	}
	rows := staffingRows(t, super, roleID)
	if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, []string{preparer}) {
		t.Errorf("stored members = %v, want [%s]", ids, preparer)
	}
	if ords := staffedOrds(rows); !reflect.DeepEqual(ords, []int{0}) {
		t.Errorf("stored ord = %v, want [0]", ords)
	}

	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if ms := membersOf(t, roles, "tax-reviewer"); !reflect.DeepEqual(ms, []string{preparer}) {
		t.Errorf("ListRoles members = %v, want [%s]", ms, preparer)
	}
}

// TestStaffing_SuspendedAndInvitedMembersAreStaffable: the picker filters `invited` only
// (roles.ts:341) and deliberately renders "· suspended" (RoleModal.tsx:224), so a
// suspended id does arrive in a real PUT; the composite FK is on (tenant_id, user_id)
// alone, so an invited row satisfies it too. Neither status is a staffing gate.
func TestStaffing_SuspendedAndInvitedMembersAreStaffable(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-suspended-invited")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	suspended, invited := uuid.NewString(), uuid.NewString()
	seedMembership(t, super, tenantID, suspended, "reviewer", "suspended")
	seedMembership(t, super, tenantID, invited, "reviewer", "invited")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	want := []string{suspended, invited}
	got, err := store.SetRoleMembers(c, "tax-reviewer", sent(want...))
	if err != nil {
		t.Fatalf("SetRoleMembers with a suspended and an invited member: %v, want success", err)
	}
	if !reflect.DeepEqual(got.Members, want) {
		t.Errorf("returned members = %v, want %v", got.Members, want)
	}
	rows := staffingRows(t, super, roleID)
	if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, want) {
		t.Errorf("stored members = %v, want %v", ids, want)
	}
	if ords := staffedOrds(rows); !reflect.DeepEqual(ords, []int{0, 1}) {
		t.Errorf("stored ord = %v, want [0 1]", ords)
	}

	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if ms := membersOf(t, roles, "tax-reviewer"); !reflect.DeepEqual(ms, want) {
		t.Errorf("ListRoles members = %v, want %v", ms, want)
	}
}

// --- order and whole-set replace -------------------------------------------

// TestStaffing_OrderRoundTrips: the submitted array order is what is persisted and what
// comes back. A store that ignores order on write passes the first round and fails the
// second, and the `ord` values are asserted ABSOLUTELY — the DDL says 0-based, while the
// one repo precedent for an ordered child rewrite (invoice.replaceLinesTx) writes i+1,
// which the served order alone cannot tell apart.
func TestStaffing_OrderRoundTrips(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-order")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 3)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	rounds := [][]string{
		{users[0], users[1], users[2]},
		{users[2], users[0], users[1]}, // the same set, reordered
	}
	if sort.StringsAreSorted(rounds[1]) {
		t.Fatal("the second submission is in user_id order; a store that sorts would pass")
	}
	for _, want := range rounds {
		got, err := store.SetRoleMembers(c, "tax-reviewer", sent(want...))
		if err != nil {
			t.Fatalf("SetRoleMembers(%v): %v", want, err)
		}
		if !reflect.DeepEqual(got.Members, want) {
			t.Errorf("returned members = %v, want %v", got.Members, want)
		}
		rows := staffingRows(t, super, roleID)
		if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, want) {
			t.Errorf("stored members in ord order = %v, want %v", ids, want)
		}
		if ords := staffedOrds(rows); !reflect.DeepEqual(ords, []int{0, 1, 2}) {
			t.Errorf("stored ord = %v, want [0 1 2] — 0-based and dense", ords)
		}
		roles, err := store.ListRoles(c)
		if err != nil {
			t.Fatalf("ListRoles: %v", err)
		}
		if ms := membersOf(t, roles, "tax-reviewer"); !reflect.DeepEqual(ms, want) {
			t.Errorf("ListRoles members = %v, want %v", ms, want)
		}
	}
}

// TestStaffing_ReplaceIsWholeSet: one PUT adds, drops and reorders at once. users[1] is
// in both submissions, so a store that INSERTs before it DELETEs trips
// workflow_role_members_tenant_role_user_uq and 500s — restaffing someone already
// staffed is not a duplicate.
func TestStaffing_ReplaceIsWholeSet(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-whole-set")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 3)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	if _, err := store.SetRoleMembers(c, "tax-reviewer", sent(users[0], users[1])); err != nil {
		t.Fatalf("first SetRoleMembers: %v", err)
	}
	second := []string{users[2], users[1]} // users[0] dropped, users[2] added, users[1] moved
	got, err := store.SetRoleMembers(c, "tax-reviewer", sent(second...))
	if err != nil {
		t.Fatalf("second SetRoleMembers: %v (SQLSTATE %q)", err, pgCode(err))
	}
	if !reflect.DeepEqual(got.Members, second) {
		t.Errorf("returned members = %v, want %v", got.Members, second)
	}
	rows := staffingRows(t, super, roleID)
	if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, second) {
		t.Errorf("stored members = %v, want exactly %v — the PUT is a whole-set replace", ids, second)
	}
	if ords := staffedOrds(rows); !reflect.DeepEqual(ords, []int{0, 1}) {
		t.Errorf("stored ord = %v, want [0 1]", ords)
	}
	if n := rowCount(t, super, "workflow_role_members", tenantID); n != 2 {
		t.Errorf("workflow_role_members rows = %d, want 2 — the dropped member must leave no row", n)
	}

	// The returned Role must not alias the caller's slice (roles.ts:107 copies for the
	// same reason: one memberIds argument must not end up owned by the role it staffed).
	arg := sent(users[2], users[1])
	again, err := store.SetRoleMembers(c, "tax-reviewer", arg)
	if err != nil {
		t.Fatalf("third SetRoleMembers: %v", err)
	}
	arg[0] = users[0]
	if again.Members[0] != users[2] {
		t.Errorf("returned members[0] = %s after the caller mutated its own argument, want %s", again.Members[0], users[2])
	}
}

// TestStaffing_EmptySetUnstaffs: an empty list is legal and unstaffs the role — the
// seeded quality_reviewer state. `[]` on the wire and in the audit payload, never null.
// The pre-count is what keeps the zero-row assertion from passing vacuously.
func TestStaffing_EmptySetUnstaffs(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-empty")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	roleID := seedWorkflowRole(t, super, tenantID, "quality-reviewer", "Quality Reviewer")
	seedRoleDesc(t, super, roleID, "second sign-off")
	for i, u := range users {
		staffWorkflowRole(t, super, tenantID, roleID, u, i)
	}
	if n := rowCount(t, super, "workflow_role_members", tenantID); n != 2 {
		t.Fatalf("seeded staffing rows = %d, want 2 — the zero-row assertion below is vacuous without them", n)
	}

	got, err := store.SetRoleMembers(c, "quality-reviewer", []string{})
	if err != nil {
		t.Fatalf("SetRoleMembers with an empty list: %v, want success", err)
	}
	// All four wire fields: the locking read scans title and description because the PUT
	// answers 200 Role.
	want := Role{Key: "quality-reviewer", Title: "Quality Reviewer", Desc: "second sign-off", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SetRoleMembers = %+v, want %+v", got, want)
	}
	if raw, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if !strings.Contains(string(raw), `"members":[]`) {
		t.Errorf("wire = %s, want it to carry \"members\":[] — null would break the SPA", raw)
	}
	if rows := staffingRows(t, super, roleID); len(rows) != 0 {
		t.Errorf("staffing = %v, want none", rows)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 1 {
		t.Fatalf("workflow_role.staffed audit rows = %d, want 1 — an unstaff is a staffing event", n)
	}
	_, payload := staffedAudit(t, super, tenantID)
	wantPayload := map[string]any{"key": "quality-reviewer", "members": []any{}}
	if !reflect.DeepEqual(payload, wantPayload) {
		t.Errorf("audit payload = %v, want %v — a nil Go slice would log members: null", payload, wantPayload)
	}
}

// --- argument validation, above the transaction ----------------------------

// TestStaffing_DuplicateUserRejected: a repeated id is ErrValidation, rejected and not
// silently deduped — deduping would store something other than what was asked, on the
// one field this method exists to persist. The check is a property of the argument
// alone, so NO transaction is opened; the traced pool is the only way to see that, and
// the trailing control keeps the zero-statement assertions honest.
func TestStaffing_DuplicateUserRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-duplicate")
	c, _ := activeAdmin(t, super, tenantID)
	app, rec := tracedAppPool(t)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	for i, u := range users {
		staffWorkflowRole(t, super, tenantID, roleID, u, i)
	}
	before := staffingRows(t, super, roleID)

	rec.reset()
	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{users[0], users[0]}); !errors.Is(err, ErrValidation) {
		t.Errorf("SetRoleMembers with a repeated id: err = %v (SQLSTATE %q), want ErrValidation", err, pgCode(err))
	}
	// The request seam batches its set_config, which a QueryTracer alone cannot see —
	// tenantTxCount() counts both routes (db.WithinRequestTenantTxOpts).
	if got := rec.tenantTxCount(); got != 0 {
		t.Errorf("a rejected duplicate opened %d transactions, want 0 — the check reads no row", got)
	}
	if got := rec.mentioning("workflow_role"); len(got) != 0 {
		t.Errorf("a rejected duplicate issued %d workflow_role statements, want 0: %v", len(got), got)
	}
	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing = %v, want it byte-identical to %v", after, before)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	rec.reset()
	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{users[1]}); err != nil {
		t.Fatalf("control: SetRoleMembers with one id: %v — the assertions above are vacuous unless this succeeds", err)
	}
	if got := rec.tenantTxCount(); got == 0 {
		t.Error("control: the successful call opened no transaction, so the statement counts above prove nothing")
	}
}

// TestStaffing_MalformedMembersRejectedBeforeTheTx pins ORDER, not a store that refuses
// everything: a malformed id answers ErrValidation to the very caller the permission
// gate would refuse. A malformed id must never reach the ::uuid[] cast and leak 22P02 as
// a 500 (06's body vocabulary covers JSON type errors, not uuid syntax).
func TestStaffing_MalformedMembersRejectedBeforeTheTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-malformed")
	store := NewStore(app, stubFingerprinter, nil)
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	for _, id := range []string{"abc", "", "not-a-uuid", "0d2ba9a1-9b31-4a1a-8b5e-2f6a3c7d8e9"} {
		t.Run(fmt.Sprintf("id=%q", id), func(t *testing.T) {
			if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{id}); !errors.Is(err, ErrValidation) {
				t.Errorf("SetRoleMembers(%q) err = %v (SQLSTATE %q), want ErrValidation", id, err, pgCode(err))
			}
		})
	}
	// The same caller with a well-formed id reaches the permission gate.
	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{uuid.NewString()}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("preparer with a well-formed id: err = %v, want ErrNotPermitted", err)
	}
	if rows := staffingRows(t, super, roleID); len(rows) != 0 {
		t.Errorf("staffing = %v, want none — neither refusal may write", rows)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	// uuid.Validate accepts the urn:uuid: form; Postgres `::uuid` rejects it. Either
	// answer here is honest — a raw 22P02 surfacing as a 500 is not.
	admin, _ := activeAdmin(t, super, tenantID)
	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	got, err := store.SetRoleMembers(admin, "tax-reviewer", []string{"urn:uuid:" + member})
	switch {
	case err == nil:
		if !reflect.DeepEqual(got.Members, []string{member}) {
			t.Errorf("urn-form id accepted, members = %v, want the canonical [%s]", got.Members, member)
		}
	case errors.Is(err, ErrValidation):
	default:
		t.Errorf("urn-form id: err = %v (SQLSTATE %q), want ErrValidation or a canonicalising success", err, pgCode(err))
	}
}

// --- the FK disposition ----------------------------------------------------

// TestStaffing_UnknownUserRejected: a user_id holding no membership in the caller's
// tenant is request data, not a path resource, so the composite FK's 23503 becomes
// ErrValidation (400) and never surfaces raw. Tenant B's real user answers identically
// to a random uuid — referential checks run with RLS bypassed, so there is no
// cross-tenant existence oracle. The mixed row proves the DELETE rolls back with the
// failed INSERT.
func TestStaffing_UnknownUserRejected(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "APPR-02 staff-unknown-user A")
	tenantB := seedTenant(t, super, "APPR-02 staff-unknown-user B")
	c, _ := activeAdmin(t, super, tenantA)
	store := NewStore(app, stubFingerprinter, nil)

	known := uuid.NewString()
	seedMembership(t, super, tenantA, known, "preparer", "active")
	bOnly := uuid.NewString()
	seedMembership(t, super, tenantB, bOnly, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantA, "tax-reviewer", "Tax Reviewer")
	staffWorkflowRole(t, super, tenantA, roleID, known, 0)
	before := staffingRows(t, super, roleID)

	type refusal struct {
		status int
		msg    string
	}
	var seen []refusal
	for _, tc := range []struct {
		name    string
		members []string
	}{
		{"no membership anywhere", []string{uuid.NewString()}},
		{"a membership in tenant B only", []string{bOnly}},
		{"a known id beside a stranger", []string{known, uuid.NewString()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.SetRoleMembers(c, "tax-reviewer", tc.members)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v (SQLSTATE %q, constraint %q), want ErrValidation",
					err, pgCode(err), pgConstraint(err))
			}
			status, msg := statusForErr(err)
			seen = append(seen, refusal{status, msg})
		})
	}
	if len(seen) != 3 {
		t.Fatalf("probed %d cases, want 3 — a short table would pass vacuously", len(seen))
	}
	for i, r := range seen[1:] {
		if r != seen[0] {
			t.Errorf("case %d answered %d %q but case 0 answered %d %q — the refusals must be indistinguishable",
				i+1, r.status, r.msg, seen[0].status, seen[0].msg)
		}
	}
	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing = %v, want it byte-identical to %v — a refused set must roll its DELETE back too", after, before)
	}
	if n := auditCount(t, super, tenantA, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{known}); err != nil {
		t.Fatalf("control: SetRoleMembers with a known member: %v — the refusals above are vacuous unless this succeeds", err)
	}
}

// TestStaffing_FKViolationOnlyMapsTheUserConstraint: the 400 is discriminated by
// constraint NAME. A SQLSTATE-only check passes both provocations below, which is what
// the second assertion catches: the role leg is unreachable (the id comes from the
// FOR UPDATE one statement earlier, invoice_app holds no DELETE grant on workflow_roles,
// and a soft delete is an UPDATE), so if it ever fires an invariant broke — a
// 400-about-users would be a lie and 500 is honest.
func TestStaffing_FKViolationOnlyMapsTheUserConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-02 staff-fk-name")
	_, adminID := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	userErr := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord)
			 VALUES ($1, $2, gen_random_uuid(), 0)`, tenantID, roleID)
		return err
	})
	roleErr := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord)
			 VALUES ($1, gen_random_uuid(), $2, 0)`, tenantID, adminID)
		return err
	})

	// Both provocations must be 23503 on the constraint they were aimed at, or the
	// discrimination below proves nothing.
	for label, err := range map[string]error{"user leg": userErr, "role leg": roleErr} {
		if code := pgCode(err); code != "23503" {
			t.Fatalf("%s provocation: err = %v (SQLSTATE %q), want 23503", label, err, code)
		}
	}
	if got := pgConstraint(userErr); got != "workflow_role_members_tenant_user_fk" {
		t.Fatalf("user-leg provocation hit constraint %q, want workflow_role_members_tenant_user_fk", got)
	}
	if got := pgConstraint(roleErr); got != "workflow_role_members_tenant_role_fk" {
		t.Fatalf("role-leg provocation hit constraint %q, want workflow_role_members_tenant_role_fk", got)
	}

	if !fkViolationOn(userErr, "workflow_role_members_tenant_user_fk") {
		t.Errorf("fkViolationOn(userErr, workflow_role_members_tenant_user_fk) = false, want true")
	}
	if fkViolationOn(roleErr, "workflow_role_members_tenant_user_fk") {
		t.Errorf("fkViolationOn(roleErr, workflow_role_members_tenant_user_fk) = true — a 23503 on the role leg must stay a 500")
	}
}

// --- remove prunes, suspend keeps ------------------------------------------

// TestStaffing_MemberRemovalPrunesEveryRole: user_id has no FK to any users table
// (GoTrue is external), so what makes the prune free is the composite FK to
// memberships (tenant_id, user_id) ON DELETE CASCADE — a membership DELETE silently
// unstaffs across EVERY role. Asserted, not trusted: the RLS suite only proves the
// cascade transitively, through a tenant delete.
//
// The other holders keep their own `ord`: nothing renumbers on a cascade.
func TestStaffing_MemberRemovalPrunesEveryRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-prune")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 3)
	x, other1, other2 := users[0], users[1], users[2]
	reviewerID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	partnerID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")

	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{x, other1}); err != nil {
		t.Fatalf("staff tax-reviewer: %v", err)
	}
	if _, err := store.SetRoleMembers(c, "engagement-partner", []string{other2, x}); err != nil {
		t.Fatalf("staff engagement-partner: %v", err)
	}
	beforeReviewer := staffingRows(t, super, reviewerID)
	beforePartner := staffingRows(t, super, partnerID)
	// X is in both roles before the removal, or the zero-row assertions below hold
	// without any cascade at all.
	if ids := staffedUserIDs(beforeReviewer); !reflect.DeepEqual(ids, []string{x, other1}) {
		t.Fatalf("tax-reviewer staffing = %v, want [%s %s] before the removal", ids, x, other1)
	}
	if ids := staffedUserIDs(beforePartner); !reflect.DeepEqual(ids, []string{other2, x}) {
		t.Fatalf("engagement-partner staffing = %v, want [%s %s] before the removal", ids, other2, x)
	}

	deleteMembership(t, super, tenantID, x)

	for _, tc := range []struct {
		label, roleID string
		want          []staffingRow
	}{
		{"tax-reviewer", reviewerID, []staffingRow{beforeReviewer[1]}},     // other1, still ord 1
		{"engagement-partner", partnerID, []staffingRow{beforePartner[0]}}, // other2, still ord 0
	} {
		after := staffingRows(t, super, tc.roleID)
		if !sameStaffing(after, tc.want) {
			t.Errorf("%s staffing = %v, want %v — the cascade prunes X and renumbers nothing", tc.label, after, tc.want)
		}
	}
	// Nothing cascaded upward: both roles are still live, and neither still serves X.
	if keys := liveRoleKeys(t, super, tenantID); !reflect.DeepEqual(keys, []string{"engagement-partner", "tax-reviewer"}) {
		t.Errorf("live keys = %v, want both roles still live", keys)
	}
	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Fatalf("ListRoles = %d roles, want 2 -- the loop below would assert nothing", len(roles))
	}
	for _, r := range roles {
		for _, m := range r.Members {
			if m == x {
				t.Errorf("%s members = %v, still lists the removed member %s", r.Key, r.Members, x)
			}
		}
	}
}

// TestStaffing_SuspendKeepsStaffing: suspending is not removing. ListRoles' members
// query reads workflow_role_members alone — no join to memberships, no status predicate
// — so a suspended holder stays staffed and stays listed, which is the
// suspended-but-staffed state the SPA renders as a warning. Add that join and this is
// the ONLY test that goes red: the other order tests staff active members.
func TestStaffing_SuspendKeepsStaffing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-suspend")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	x, y := users[0], users[1]
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{x, y}); err != nil {
		t.Fatalf("SetRoleMembers: %v", err)
	}
	before := staffingRows(t, super, roleID)
	if ids := staffedUserIDs(before); !reflect.DeepEqual(ids, []string{x, y}) {
		t.Fatalf("staffing = %v, want [%s %s] before the suspension", ids, x, y)
	}

	setMembershipStatus(t, super, tenantID, x, "suspended")

	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing = %v, want it byte-identical to %v — a status UPDATE cascades nowhere", after, before)
	}
	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if ms := membersOf(t, roles, "tax-reviewer"); !reflect.DeepEqual(ms, []string{x, y}) {
		t.Errorf("ListRoles members = %v, want [%s %s] — a suspended holder is still a holder", ms, x, y)
	}
}

// --- the caller axis -------------------------------------------------------

// TestStaffing_RequiresActiveAdminCaller: the CALLER axis, distinct from the unrestricted
// SUBJECT axis above. The caller-role read carries AND status = 'active', so a suspended
// or invited admin is refused as firmly as a preparer, and a caller with no membership
// row at all is refused too. Every refusal submits a member the tenant really has, so it
// is the gate answering and not validation.
func TestStaffing_RequiresActiveAdminCaller(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-caller-axis")
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	staffWorkflowRole(t, super, tenantID, roleID, users[0], 0)
	before := staffingRows(t, super, roleID)

	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts); an ACTIVE caller, and one with no row at all,
	// are still role refusals.
	type refusal struct {
		ctx  context.Context
		want error
	}
	refused := map[string]refusal{}
	for _, caller := range []struct {
		name, role, status string
		want               error
	}{
		{"active preparer", "preparer", "active", ErrNotPermitted},
		{"active reviewer", "reviewer", "active", ErrNotPermitted},
		{"suspended admin", "admin", "suspended", db.ErrNotActiveMember},
		{"invited admin", "admin", "invited", db.ErrNotActiveMember},
	} {
		c, _ := callerCtx(t, super, tenantID, caller.role, caller.status)
		refused[caller.name] = refusal{c, caller.want}
	}
	refused["no membership row"] = refusal{auth.WithIdentity(context.Background(),
		auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}), ErrNotPermitted}

	if len(refused) != 5 {
		t.Fatalf("built %d callers, want 5 — a short table would pass vacuously", len(refused))
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			if _, err := store.SetRoleMembers(tc.ctx, "tax-reviewer", []string{users[1]}); !errors.Is(err, tc.want) {
				t.Errorf("SetRoleMembers as %s: err = %v, want %v", name, err, tc.want)
			}
		})
	}
	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing = %v, want it byte-identical to %v", after, before)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	admin, _ := activeAdmin(t, super, tenantID)
	got, err := store.SetRoleMembers(admin, "tax-reviewer", []string{users[1]})
	if err != nil {
		t.Fatalf("control: SetRoleMembers as an active admin: %v — the refusals above are vacuous unless this succeeds", err)
	}
	if !reflect.DeepEqual(got.Members, []string{users[1]}) {
		t.Errorf("control: members = %v, want [%s]", got.Members, users[1])
	}
}

// TestStaffing_PermissionCheckedBeforeRowRead: the caller-role read is the first
// statement in the closure and takes no target argument, so a non-admin is refused
// identically whether the key exists, is soft-deleted, belongs to another tenant, or is
// garbage — no 403-vs-404 existence oracle. Compared as the status+message the SPA
// actually sees. The admin controls prove 403 is not simply the only error this method
// can reach.
func TestStaffing_PermissionCheckedBeforeRowRead(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-no-oracle")
	otherTenant := seedTenant(t, super, "APPR-02 staff-no-oracle other")
	store := NewStore(app, stubFingerprinter, nil)
	preparer, _ := callerCtx(t, super, tenantID, "preparer", "active")

	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	staffWorkflowRole(t, super, tenantID, roleID, member, 0)
	before := staffingRows(t, super, roleID)
	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	softDeleteWorkflowRole(t, super, deadID)
	seedWorkflowRole(t, super, otherTenant, "other-tenants-role", "Other Tenant's Role")

	type refusal struct {
		status int
		msg    string
	}
	var seen []refusal
	for _, tc := range []struct{ class, key string }{
		{"live", "tax-reviewer"},
		{"soft-deleted", "engagement-partner"},
		{"another tenant's", "other-tenants-role"},
		{"garbage", "no-such-role"},
	} {
		t.Run(tc.class, func(t *testing.T) {
			_, err := store.SetRoleMembers(preparer, tc.key, []string{member})
			if !errors.Is(err, ErrNotPermitted) {
				t.Errorf("SetRoleMembers(%s) as a preparer: err = %v, want ErrNotPermitted", tc.class, err)
			}
			status, msg := statusForErr(err)
			seen = append(seen, refusal{status, msg})
		})
	}
	if len(seen) != 4 {
		t.Fatalf("probed %d target classes, want 4 — a short table would pass vacuously", len(seen))
	}
	for i, r := range seen[1:] {
		if r != seen[0] {
			t.Errorf("class %d answered %d %q but class 0 answered %d %q — the refusals must be indistinguishable",
				i+1, r.status, r.msg, seen[0].status, seen[0].msg)
		}
	}
	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing = %v, want it byte-identical to %v", after, before)
	}

	admin, _ := activeAdmin(t, super, tenantID)
	for _, key := range []string{"no-such-role", "engagement-partner", "other-tenants-role"} {
		if _, err := store.SetRoleMembers(admin, key, []string{member}); !errors.Is(err, ErrNotFound) {
			t.Errorf("control: SetRoleMembers(%q) as an admin: err = %v, want ErrNotFound", key, err)
		}
	}
	if _, err := store.SetRoleMembers(admin, "tax-reviewer", []string{member}); err != nil {
		t.Fatalf("control: SetRoleMembers on the live role: %v", err)
	}
}

// TestStaffing_DeletedRoleCannotBeStaffed: deleted_at IS NULL is this resource's
// existence predicate, so a soft-deleted role is ErrNotFound here too. Its surviving
// staffing rows — inert, unreachable — must be left exactly as they are, and nothing may
// be audited. The live-role control keeps this from passing against a store that refuses
// everything.
func TestStaffing_DeletedRoleCannotBeStaffed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-deleted-role")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, deadID, users[0], 0)
	softDeleteWorkflowRole(t, super, deadID)
	before := staffingRows(t, super, deadID)
	if len(before) != 1 {
		t.Fatalf("the deleted role has %d staffing rows, want 1 — the survival assertion is vacuous without one", len(before))
	}
	liveID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	if _, err := store.SetRoleMembers(c, "engagement-partner", []string{users[1]}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRoleMembers on a soft-deleted role: err = %v, want ErrNotFound", err)
	}
	if after := staffingRows(t, super, deadID); !sameStaffing(after, before) {
		t.Errorf("the deleted role's staffing = %v, want it byte-identical to %v", after, before)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{users[1]}); err != nil {
		t.Fatalf("control: SetRoleMembers on the live role: %v — the refusal above is vacuous unless this succeeds", err)
	}
	if ids := staffedUserIDs(staffingRows(t, super, liveID)); !reflect.DeepEqual(ids, []string{users[1]}) {
		t.Errorf("control: the live role's staffing = %v, want [%s]", ids, users[1])
	}
}

// DeleteRole reports members: [] while every staffing row survives the soft delete. The
// [] is a wire convention (four keys always), not a fact, and it is unobservable today:
// ListRoles filters deleted_at IS NULL, and the SPA discards the DELETE body
// (App.tsx removeRole takes only a list and a key). Anything that makes a deleted role
// readable again — a restore path — has to report the real holders instead.
func TestStaffing_DeleteReportsNoMembersButKeepsTheRows(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 delete-keeps-staffing")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	users := seedActiveMembers(t, super, tenantID, 2)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	for i, u := range users {
		staffWorkflowRole(t, super, tenantID, roleID, u, i)
	}
	before := staffingRows(t, super, roleID)
	if len(before) != 2 {
		t.Fatalf("seeded staffing rows = %d, want 2 — the survival assertion is vacuous without them", len(before))
	}

	got, err := store.DeleteRole(c, "tax-reviewer")
	if err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if !reflect.DeepEqual(got.Members, []string{}) {
		t.Errorf("DeleteRole reported members = %v, want [] (the wire convention)", got.Members)
	}
	if raw, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if !strings.Contains(string(raw), `"members":[]`) {
		t.Errorf("wire = %s, want \"members\":[]", raw)
	}
	if after := staffingRows(t, super, roleID); !sameStaffing(after, before) {
		t.Errorf("staffing after the delete = %v, want it byte-identical to %v — the [] above is a convention, not a purge",
			after, before)
	}

	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if keys := keysOf(roles); len(keys) != 0 {
		t.Errorf("ListRoles = %v, want none — a deleted role must stay unreadable, which is what makes the [] harmless", keys)
	}
}

// --- tenancy, concurrency, audit -------------------------------------------

// TestStaffing_IsTenantScoped: no statement carries a tenant_id predicate, so RLS is the
// only thing keeping a by-key staffing write inside the caller's tenant. Both tenants
// hold the same key, which is what makes a mis-scoped write observable.
func TestStaffing_IsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "APPR-02 staff-scope A")
	tenantB := seedTenant(t, super, "APPR-02 staff-scope B")
	store := NewStore(app, stubFingerprinter, nil)

	aRole := seedWorkflowRole(t, super, tenantA, "shared", "A Shared")
	bRole := seedWorkflowRole(t, super, tenantB, "shared", "B Shared")
	aUser, bUser := uuid.NewString(), uuid.NewString()
	seedMembership(t, super, tenantA, aUser, "preparer", "active")
	seedMembership(t, super, tenantB, bUser, "preparer", "active")
	staffWorkflowRole(t, super, tenantB, bRole, bUser, 0)
	beforeB := staffingRows(t, super, bRole)
	if len(beforeB) != 1 {
		t.Fatalf("tenant B has %d staffing rows, want 1", len(beforeB))
	}

	c, _ := activeAdmin(t, super, tenantA)
	got, err := store.SetRoleMembers(c, "shared", []string{aUser})
	if err != nil {
		t.Fatalf("SetRoleMembers on the shared key: %v", err)
	}
	if got.Title != "A Shared" {
		t.Errorf("returned title = %q, want A Shared — the shared key must resolve to the caller's row", got.Title)
	}
	if ids := staffedUserIDs(staffingRows(t, super, aRole)); !reflect.DeepEqual(ids, []string{aUser}) {
		t.Errorf("tenant A staffing = %v, want [%s]", ids, aUser)
	}
	if after := staffingRows(t, super, bRole); !sameStaffing(after, beforeB) {
		t.Errorf("tenant B staffing = %v, want %v — no write may cross tenants", after, beforeB)
	}
	if n := auditCount(t, super, tenantB, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows in tenant B = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantA, "workflow_role.staffed"); n != 1 {
		t.Errorf("workflow_role.staffed audit rows in tenant A = %d, want 1", n)
	}
}

// TestStaffing_ConcurrentPutsDoNotMerge is the only pin for the FOR UPDATE on the role
// row. Unlocked, T2's DELETE never sees T1's uncommitted INSERTs, so both submissions
// survive at ord 0,1,0,1 — two disjoint sets trip NO constraint, which is why the
// assertion has to be the set read back from the database. Tolerant of either winner;
// several rounds because a lockless store also passes whenever the two happen not to
// overlap.
func TestStaffing_ConcurrentPutsDoNotMerge(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 staff-concurrent")
		c, _ := activeAdmin(t, super, tenantID)
		users := seedActiveMembers(t, super, tenantID, 4)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
		submissions := [][]string{{users[0], users[1]}, {users[2], users[3]}}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		for i, members := range submissions {
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = store.SetRoleMembers(c, "tax-reviewer", sent(members...))
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: errs[%d] = %v (SQLSTATE %q) — two whole-set replaces must serialise, not fail",
					round, i, err, pgCode(err))
			}
		}

		rows := staffingRows(t, super, roleID)
		if len(rows) != 2 {
			t.Fatalf("round %d: staffing = %v (%d rows), want 2 — the two sets merged, which trips no constraint",
				round, rows, len(rows))
		}
		ids, ords := staffedUserIDs(rows), staffedOrds(rows)
		if !reflect.DeepEqual(ids, submissions[0]) && !reflect.DeepEqual(ids, submissions[1]) {
			t.Errorf("round %d: staffing = %v, want exactly one of %v", round, ids, submissions)
		}
		if !reflect.DeepEqual(ords, []int{0, 1}) {
			t.Errorf("round %d: stored ord = %v, want [0 1]", round, ords)
		}
		if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 2 {
			t.Errorf("round %d: workflow_role.staffed audit rows = %d, want 2 — both calls committed", round, n)
		}
	}
}

// --- uuid canonicalisation -------------------------------------------------

// nonCanonicalForms are the spellings uuid.Parse accepts for one id. Postgres itself
// normalises braced/unhyphenated/upper-case, but rejects the urn: prefix and the
// space-padded form outright (uuid.Parse's 38-char branch strips the first and last
// byte without checking they are braces), so both would leak 22P02 as a 500 unless the
// store canonicalises before the ::uuid[] cast.
func nonCanonicalForms(id string) map[string]string {
	return map[string]string{
		"urn":            "urn:uuid:" + id,
		"urn upper":      "URN:UUID:" + id,
		"braced":         "{" + id + "}",
		"space padded":   " " + id + " ",
		"unhyphenated":   strings.ReplaceAll(id, "-", ""),
		"upper-case hex": strings.ToUpper(id),
	}
}

// TestStaffing_NonCanonicalUuidFormsAreCanonicalised: whatever spelling arrives, the
// canonical id is what is stored, returned and logged. The response and the audit
// payload are read back too — Postgres canonicalises the column on its own, so echoing
// the raw argument would silently disagree with the row it just wrote.
func TestStaffing_NonCanonicalUuidFormsAreCanonicalised(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	member := uuid.NewString()
	for name, form := range nonCanonicalForms(member) {
		t.Run(name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "APPR-02 staff-uuid-form "+name)
			c, _ := activeAdmin(t, super, tenantID)
			seedMembership(t, super, tenantID, member, "preparer", "active")
			roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

			got, err := store.SetRoleMembers(c, "tax-reviewer", []string{form})
			if err != nil {
				t.Fatalf("SetRoleMembers(%q): err = %v (SQLSTATE %q), want the canonical id stored", form, err, pgCode(err))
			}
			want := []string{member}
			if !reflect.DeepEqual(got.Members, want) {
				t.Errorf("returned members = %v, want the canonical %v", got.Members, want)
			}
			if ids := staffedUserIDs(staffingRows(t, super, roleID)); !reflect.DeepEqual(ids, want) {
				t.Errorf("stored members = %v, want %v", ids, want)
			}
			if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 1 {
				t.Fatalf("workflow_role.staffed audit rows = %d, want 1", n)
			}
			_, payload := staffedAudit(t, super, tenantID)
			wantPayload := map[string]any{"key": "tax-reviewer", "members": []any{member}}
			if !reflect.DeepEqual(payload, wantPayload) {
				t.Errorf("audit payload = %v, want %v — the log must carry what was stored", payload, wantPayload)
			}
		})
	}
}

// TestStaffing_DuplicateIsDetectedAcrossSpellings: the dedupe reads canonical ids, so two
// spellings of one uuid are the ErrValidation a repeated id is — not a 23505 on
// workflow_role_members_tenant_role_user_uq, which is unmapped and would 500.
func TestStaffing_DuplicateIsDetectedAcrossSpellings(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-duplicate-spellings")
	c, _ := activeAdmin(t, super, tenantID)
	app, rec := tracedAppPool(t)
	store := NewStore(app, stubFingerprinter, nil)

	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	for name, form := range nonCanonicalForms(member) {
		t.Run(name, func(t *testing.T) {
			rec.reset()
			if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{member, form}); !errors.Is(err, ErrValidation) {
				t.Errorf("SetRoleMembers with %q beside its canonical form: err = %v (SQLSTATE %q), want ErrValidation",
					form, err, pgCode(err))
			}
			// The request seam batches its set_config, which a QueryTracer alone cannot
			// see — tenantTxCount() counts both routes (db.WithinRequestTenantTxOpts).
			if got := rec.tenantTxCount(); got != 0 {
				t.Errorf("a rejected duplicate opened %d transactions, want 0 — the check reads no row", got)
			}
			if rows := staffingRows(t, super, roleID); len(rows) != 0 {
				t.Errorf("staffing = %v, want none", rows)
			}
		})
	}
	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 0 {
		t.Errorf("workflow_role.staffed audit rows = %d, want 0", n)
	}

	if _, err := store.SetRoleMembers(c, "tax-reviewer", []string{strings.ToUpper(member)}); err != nil {
		t.Fatalf("control: one spelling alone: %v — the refusals above are vacuous unless this succeeds", err)
	}
}

// --- scale and cross-method interleaving -----------------------------------

// TestStaffing_LargeMemberListIsOneInsert: the statement count against
// workflow_role_members is 2 (clear, re-staff) whatever the member count, so nothing here
// is per-member — the body cap 06 applies leaves room for ~1,700 ids. The submission is in
// reverse id order, so `ord` cannot pass by echoing either insertion or user_id order.
func TestStaffing_LargeMemberListIsOneInsert(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-large-list")
	c, _ := activeAdmin(t, super, tenantID)
	app, rec := tracedAppPool(t)
	store := NewStore(app, stubFingerprinter, nil)

	const n = 400
	users := seedActiveMembers(t, super, tenantID, n)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	for _, size := range []int{3, n} {
		submitted := make([]string, 0, size)
		for i := size - 1; i >= 0; i-- {
			submitted = append(submitted, users[i])
		}
		rec.reset()
		got, err := store.SetRoleMembers(c, "tax-reviewer", sent(submitted...))
		if err != nil {
			t.Fatalf("SetRoleMembers with %d members: %v (SQLSTATE %q)", size, err, pgCode(err))
		}
		if stmts := rec.mentioning("workflow_role_members"); len(stmts) != 2 {
			t.Errorf("%d members took %d workflow_role_members statements, want 2 (clear + re-staff): %v",
				size, len(stmts), stmts)
		}
		if !reflect.DeepEqual(got.Members, submitted) {
			t.Errorf("%d members: returned order differs from the submitted order", size)
		}
		rows := staffingRows(t, super, roleID)
		if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, submitted) {
			t.Errorf("%d members: stored order differs from the submitted order", size)
		}
		wantOrds := make([]int, size)
		for i := range wantOrds {
			wantOrds[i] = i
		}
		if ords := staffedOrds(rows); !reflect.DeepEqual(ords, wantOrds) {
			t.Errorf("%d members: stored ord = %v, want 0..%d dense", size, ords, size-1)
		}
	}
}

// TestStaffing_ConcurrentStaffAndDeleteResolveCoherently: a staffing write and a soft
// delete contend for the SAME workflow_roles row lock, so one of two coherent outcomes
// holds — never a half-replaced set, and never staffing a role no door can reach. When the
// delete commits first, the FOR UPDATE's re-check drops the row and the answer is
// ErrNotFound; when it commits second, the replaced set survives inert.
func TestStaffing_ConcurrentStaffAndDeleteResolveCoherently(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 staff-vs-delete")
		c, _ := activeAdmin(t, super, tenantID)
		users := seedActiveMembers(t, super, tenantID, 3)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
		staffWorkflowRole(t, super, tenantID, roleID, users[0], 0)
		before := staffingRows(t, super, roleID)
		submitted := []string{users[2], users[1]}

		var wg sync.WaitGroup
		var staffErr, delErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, staffErr = store.SetRoleMembers(c, "tax-reviewer", sent(submitted...))
		}()
		go func() {
			defer wg.Done()
			<-start
			_, delErr = store.DeleteRole(c, "tax-reviewer")
		}()
		close(start)
		wg.Wait()

		if delErr != nil {
			t.Fatalf("round %d: DeleteRole = %v (SQLSTATE %q) — the sole deleter of a live role must win", round, delErr, pgCode(delErr))
		}
		if r := roleRow(t, super, roleID); r.DeletedAt == nil {
			t.Fatalf("round %d: the role is still live after DeleteRole committed", round)
		}
		rows := staffingRows(t, super, roleID)
		switch {
		case staffErr == nil:
			if ids := staffedUserIDs(rows); !reflect.DeepEqual(ids, submitted) {
				t.Errorf("round %d: staffing = %v, want %v — a committed replace is whole", round, ids, submitted)
			}
			if ords := staffedOrds(rows); !reflect.DeepEqual(ords, []int{0, 1}) {
				t.Errorf("round %d: stored ord = %v, want [0 1]", round, ords)
			}
		case errors.Is(staffErr, ErrNotFound):
			if !sameStaffing(rows, before) {
				t.Errorf("round %d: staffing = %v, want it byte-identical to %v — a refused replace writes nothing",
					round, rows, before)
			}
		default:
			t.Fatalf("round %d: SetRoleMembers = %v (SQLSTATE %q), want success or ErrNotFound", round, staffErr, pgCode(staffErr))
		}
		if keys := liveRoleKeys(t, super, tenantID); len(keys) != 0 {
			t.Errorf("round %d: live keys = %v, want none — the delete committed", round, keys)
		}
	}
}

// TestStaffing_AuditsInSameTx: one transaction wrote both rows, proven by a shared
// non-frozen xmin. The join is against workflow_role_members on purpose — staffing never
// writes the workflow_roles row, so its xmin is still the CREATE's xid and a copy of the
// rename proof would compare the wrong row.
//
// The submitted order is against user_id order, so the payload cannot pass by echoing a
// sorted or re-read list, and it is compared as a whole map: a widened payload is a wire
// change the log's only reader would have to be taught.
func TestStaffing_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 staff-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)
	users := seedActiveMembers(t, super, tenantID, 2)
	submitted := []string{users[1], users[0]}
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	if _, err := NewStore(app, stubFingerprinter, nil).SetRoleMembers(c, "tax-reviewer", sent(submitted...)); err != nil {
		t.Fatalf("SetRoleMembers: %v", err)
	}

	var memberXmin, auditXmin string
	if err := super.QueryRow(context.Background(),
		`SELECT m.xmin::text, a.xmin::text
		   FROM workflow_role_members m, audit_log a
		  WHERE m.workflow_role_id = $1 AND m.ord = 0
		    AND a.tenant_id = $2 AND a.event = 'workflow_role.staffed'`,
		roleID, tenantID).Scan(&memberXmin, &auditXmin); err != nil {
		t.Fatalf("xmin join (no row means the staffing and its audit event do not both exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{"workflow_role_members": memberXmin, "audit_log": auditXmin} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if memberXmin != auditXmin {
		t.Errorf("xmin: workflow_role_members = %s, audit_log = %s — the audit must be written on the same tx as the rewrite",
			memberXmin, auditXmin)
	}

	if n := auditCount(t, super, tenantID, "workflow_role.staffed"); n != 1 {
		t.Fatalf("workflow_role.staffed audit rows = %d, want 1", n)
	}
	actor, payload := staffedAudit(t, super, tenantID)
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}
	want := map[string]any{
		"key":     "tax-reviewer",
		"members": []any{submitted[0], submitted[1]},
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("audit payload = %v, want %v", payload, want)
	}
}
