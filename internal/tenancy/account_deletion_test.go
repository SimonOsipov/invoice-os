package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	evAccountDeleted = "membership.account_deleted"
	evAdminHandedOn  = "membership.admin_handed_on"
)

var joinedBase = time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)

func joined(hours int) time.Time { return joinedBase.Add(time.Duration(hours) * time.Hour) }

// newAccWorld is a workspace with one active admin and a store whose erase queue is an insert-only River client.
func newAccWorld(t *testing.T, name string) invWorld {
	t.Helper()
	super, app := dbTestPools(t)
	return accWorldOn(t, super, app, name)
}

func accWorldOn(t *testing.T, super, app *pgxpool.Pool, name string) invWorld {
	t.Helper()
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("build the insert-only queue client: %v", err)
	}
	w := invWorld{super: super, app: app, store: NewStore(app)}
	w.store.SetEraseQueue(q.River())
	w.tenant = seedTenant(t, super, name)
	w.admin = w.addMember(t, "admin", "active", "Ada Obi")
	t.Cleanup(func() { // runs before the tenant delete; erasure rows and jobs are global
		const users = `(SELECT user_id::text FROM memberships WHERE tenant_id = $1)`
		_, _ = super.Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' IN `+users, w.tenant)
		_, _ = super.Exec(context.Background(), `DELETE FROM person_erasures WHERE user_id::text IN `+users, w.tenant)
	})
	return w
}

// addJoined seeds a member under a chosen id and join time.
func (w invWorld) addJoined(t *testing.T, id, role, status, name string, at time.Time) string {
	t.Helper()
	seedIdentityMembership(t, w.super, w.tenant, id, role, status, strp(name), strp(id+"@members.test"))
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET created_at = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, id, at); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
	return id
}

func (w invWorld) member(t *testing.T, role, status, name string, at time.Time) string {
	t.Helper()
	return w.addJoined(t, uuid.NewString(), role, status, name, at)
}

type auditRow struct {
	Actor   string
	Payload map[string]any
}

func (w invWorld) auditRows(t *testing.T, event string) []auditRow {
	t.Helper()
	rows, err := w.super.Query(context.Background(),
		`SELECT actor, payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, w.tenant, event)
	if err != nil {
		t.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		if err := rows.Scan(&r.Actor, &raw); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatalf("decode payload %s: %v", raw, err)
		}
		out = append(out, r)
	}
	return out
}

func (w invWorld) auditTotal(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, w.tenant).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func payloadKeys(p map[string]any) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// erasureOf reads the person_erasures row of a user; found is false when there is none.
func (w invWorld) erasureOf(t *testing.T, user string) (email string, generation int, found bool) {
	t.Helper()
	err := w.super.QueryRow(context.Background(),
		`SELECT email, generation FROM person_erasures WHERE user_id = $1::uuid`, user).Scan(&email, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false
	}
	if err != nil {
		t.Fatalf("read person_erasures: %v", err)
	}
	return email, generation, true
}

func (w invWorld) eraseJobs(t *testing.T, user string) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'person_erase' AND args->>'user_id' = $1`, user).Scan(&n); err != nil {
		t.Fatalf("count person_erase jobs: %v", err)
	}
	return n
}

// requireNoErase fails when any of the users has an erasure row or job.
func (w invWorld) requireNoErase(t *testing.T, users ...string) {
	t.Helper()
	for _, u := range users {
		if _, _, found := w.erasureOf(t, u); found {
			t.Errorf("person_erasures has a row for %s, want none", u)
		}
		if n := w.eraseJobs(t, u); n != 0 {
			t.Errorf("person_erase jobs for %s = %d, want 0", u, n)
		}
	}
}

func (w invWorld) requireUnscheduled(t *testing.T) {
	t.Helper()
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("tenant deletion columns = %+v, want all NULL (the workspace is never scheduled by the self route)", c)
	}
}

func (w invWorld) asWithEmail(user, email string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: w.tenant, Email: email})
}

// endState is a member's image after the DEL-02 end: removed, email NULL, grants gone, name and role kept.
func endState(before memberImage) memberImage {
	before.Status, before.Email, before.AllClients, before.Clients, before.Roles = "removed", "<nil>", false, 0, 0
	return before
}

func requireImage(t *testing.T, w invWorld, who, user string, want memberImage) {
	t.Helper()
	if got := imageOf(t, w.super, w.tenant, user); got != want {
		t.Errorf("%s row = %+v, want %+v", who, got, want)
	}
}

func TestAccountDeletionPreview_NamesTheHeir(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 preview heir")
	p1 := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	w.member(t, "preparer", "active", "Tunde Bello", joined(2))
	roleBefore := imageOf(t, w.super, w.tenant, p1)
	auditBefore := w.auditTotal(t)

	got, err := w.store.PreviewAccountDeletion(w.adminCtx())
	if err != nil {
		t.Fatalf("PreviewAccountDeletion: %v", err)
	}
	if got.Outcome != OutcomeHandOn || got.HeirName == nil || *got.HeirName != "Chidi Eze" {
		t.Fatalf("preview = %+v (heir %v), want hand_on naming Chidi Eze", got, got.HeirName)
	}

	for _, blank := range []*string{strp(""), strp("   "), nil} {
		if _, err := w.super.Exec(context.Background(),
			`UPDATE memberships SET display_name = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p1, blank); err != nil {
			t.Fatalf("blank the name: %v", err)
		}
		got, err := w.store.PreviewAccountDeletion(w.adminCtx())
		if err != nil {
			t.Fatalf("PreviewAccountDeletion with a blank name: %v", err)
		}
		if got.Outcome != OutcomeHandOn || got.HeirName == nil || *got.HeirName != p1+"@members.test" {
			t.Errorf("blank name %v: preview = %+v (heir %v), want hand_on naming the email %s", blank, got, got.HeirName, p1+"@members.test")
		}
	}

	if after := imageOf(t, w.super, w.tenant, p1); after.Role != roleBefore.Role || after.Status != roleBefore.Status {
		t.Errorf("heir after previews = %+v, want role and status unchanged", after)
	}
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d (a preview writes nothing)", n, auditBefore)
	}
}

func TestAccountDeletionPreview_LeaveAndDeleteHaveNoHeir(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 preview no heir")

	alone, err := w.store.PreviewAccountDeletion(w.adminCtx())
	if err != nil {
		t.Fatalf("PreviewAccountDeletion (sole member): %v", err)
	}
	if alone.Outcome != OutcomeDeleteWorkspace || alone.HeirName != nil {
		t.Errorf("sole member: preview = %+v, want delete_workspace with a nil heir", alone)
	}

	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	left, err := w.store.PreviewAccountDeletion(w.as(p))
	if err != nil {
		t.Fatalf("PreviewAccountDeletion (preparer): %v", err)
	}
	if left.Outcome != OutcomeLeave || left.HeirName != nil {
		t.Errorf("preparer: preview = %+v, want leave with a nil heir", left)
	}
}

func TestAccountDeletionPreview_TakesNoRowLock(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 preview no lock")
	w.member(t, "preparer", "active", "Chidi Eze", joined(1))

	hold, err := w.super.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin the holding transaction: %v", err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(context.Background(), `SELECT 1 FROM memberships WHERE tenant_id = $1 FOR UPDATE`, w.tenant); err != nil {
		t.Fatalf("lock the membership rows: %v", err)
	}

	ctx, cancel := context.WithTimeout(w.adminCtx(), 2*time.Second)
	defer cancel()
	got, err := w.store.PreviewAccountDeletion(ctx)
	if err != nil {
		t.Fatalf("PreviewAccountDeletion while the rows are locked: %v (it must read without a lock)", err)
	}
	if got.Outcome != OutcomeHandOn {
		t.Errorf("outcome = %q, want hand_on", got.Outcome)
	}
}

func TestAccountDeletionPreview_AnswersOnALockedWorkspace(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 preview locked")
	lockByOwner(t, w.super, w.tenant, w.admin)

	alone, err := w.store.PreviewAccountDeletion(w.adminCtx())
	if err != nil {
		t.Fatalf("PreviewAccountDeletion on a locked workspace: %v (a read-write seam would answer %v)", err, db.ErrWorkspaceLocked)
	}
	if alone.Outcome != OutcomeDeleteWorkspace {
		t.Errorf("sole admin: outcome = %q, want delete_workspace", alone.Outcome)
	}

	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	got, err := w.store.PreviewAccountDeletion(w.as(p))
	if err != nil || got.Outcome != OutcomeLeave {
		t.Errorf("preparer: preview = %+v, err = %v, want leave and no error", got, err)
	}
}

func TestDeleteMyAccount_AMemberLeavesAndIsErased(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 leave")
	p, _ := w.grantedMember(t, "Grace Eze")
	pBefore, aBefore := imageOf(t, w.super, w.tenant, p), imageOf(t, w.super, w.tenant, w.admin)

	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Fatalf("DeleteMyAccount(leave): %v", err)
	}

	requireImage(t, w, "caller", p, endState(pBefore))
	requireImage(t, w, "admin", w.admin, aBefore)

	rows := w.auditRows(t, evAccountDeleted)
	if len(rows) != 1 || rows[0].Actor != p {
		t.Fatalf("%s rows = %+v, want exactly one with actor %s", evAccountDeleted, rows, p)
	}
	if !slices.Equal(payloadKeys(rows[0].Payload), []string{"role", "user_id"}) || rows[0].Payload["user_id"] != p || rows[0].Payload["role"] != "preparer" {
		t.Errorf("payload = %v, want exactly {user_id: %s, role: preparer}", rows[0].Payload, p)
	}
	if n := len(w.auditRows(t, evAdminHandedOn)); n != 0 {
		t.Errorf("%s rows = %d, want 0 for a leave", evAdminHandedOn, n)
	}

	email, gen, found := w.erasureOf(t, p)
	if !found || email != p+"@members.test" || gen != 1 {
		t.Errorf("person_erasures = (%q, gen %d, found %v), want the caller's email at generation 1", email, gen, found)
	}
	if n := w.eraseJobs(t, p); n != 1 {
		t.Errorf("person_erase jobs for the caller = %d, want 1", n)
	}
	w.requireNoErase(t, w.admin)
}

func TestDeleteMyAccount_AnAdminWithAnotherAdminLeaves(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 two admins")
	a2 := w.member(t, "admin", "active", "Bola Ade", joined(1))
	a1Before, a2Before := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, a2)

	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeLeave); err != nil {
		t.Fatalf("DeleteMyAccount(leave): %v", err)
	}
	requireImage(t, w, "leaving admin", w.admin, endState(a1Before))
	requireImage(t, w, "other admin", a2, a2Before)
	if n := len(w.auditRows(t, evAdminHandedOn)); n != 0 {
		t.Errorf("%s rows = %d, want 0", evAdminHandedOn, n)
	}
	if n := len(w.auditRows(t, evAccountDeleted)); n != 1 {
		t.Errorf("%s rows = %d, want 1", evAccountDeleted, n)
	}
}

func TestDeleteMyAccount_TheLastAdminHandsOnToTheFirstJoined(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 hand on")
	p1 := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	p2 := w.member(t, "preparer", "active", "Tunde Bello", joined(2))
	aBefore, p1Before, p2Before := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, p1), imageOf(t, w.super, w.tenant, p2)

	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Fatalf("DeleteMyAccount(hand_on): %v", err)
	}

	heir := p1Before
	heir.Role = "admin"
	requireImage(t, w, "heir", p1, heir)
	requireImage(t, w, "second member", p2, p2Before)
	requireImage(t, w, "caller", w.admin, endState(aBefore))

	handed := w.auditRows(t, evAdminHandedOn)
	if len(handed) != 1 || handed[0].Actor != w.admin {
		t.Fatalf("%s rows = %+v, want exactly one with actor %s", evAdminHandedOn, handed, w.admin)
	}
	if !slices.Equal(payloadKeys(handed[0].Payload), []string{"from", "user_id"}) || handed[0].Payload["user_id"] != p1 || handed[0].Payload["from"] != "preparer" {
		t.Errorf("hand-on payload = %v, want exactly {user_id: %s, from: preparer}", handed[0].Payload, p1)
	}
	deleted := w.auditRows(t, evAccountDeleted)
	if len(deleted) != 1 || deleted[0].Actor != w.admin || deleted[0].Payload["user_id"] != w.admin || deleted[0].Payload["role"] != "admin" {
		t.Errorf("%s rows = %+v, want one with actor and user_id %s and role admin", evAccountDeleted, deleted, w.admin)
	}

	email, _, found := w.erasureOf(t, w.admin)
	if !found || email != w.admin+"@members.test" || w.eraseJobs(t, w.admin) != 1 {
		t.Errorf("caller erasure = (%q, found %v), jobs = %d, want the caller's email queued once", email, found, w.eraseJobs(t, w.admin))
	}
	w.requireNoErase(t, p1, p2)
	w.requireUnscheduled(t)
}

func TestDeleteMyAccount_ATieGoesToTheLowerUserID(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 tie")
	lo, hi := uuid.NewString(), uuid.NewString()
	if lo > hi {
		lo, hi = hi, lo
	}
	// the higher id is seeded first, so insertion order cannot decide the tie
	p1 := w.addJoined(t, hi, "preparer", "active", "Chidi Eze", joined(1))
	p2 := w.addJoined(t, lo, "preparer", "active", "Tunde Bello", joined(1))
	p1Before := imageOf(t, w.super, w.tenant, p1)

	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Fatalf("DeleteMyAccount(hand_on): %v", err)
	}
	if got := imageOf(t, w.super, w.tenant, p2); got.Role != "admin" {
		t.Errorf("lower user id %s role = %q, want admin", p2, got.Role)
	}
	requireImage(t, w, "higher user id", p1, p1Before)
}

func TestDeleteMyAccount_SuspendedAndInvitedNeverInherit(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 never inherit")
	s := w.member(t, "preparer", "suspended", "Sue Pend", joined(0))
	i := w.member(t, "preparer", "invited", "Ivy Nvited", joined(0))
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(2))
	sBefore, iBefore := imageOf(t, w.super, w.tenant, s), imageOf(t, w.super, w.tenant, i)

	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Fatalf("DeleteMyAccount(hand_on): %v", err)
	}
	if got := imageOf(t, w.super, w.tenant, p); got.Role != "admin" {
		t.Errorf("active member role = %q, want admin (the only eligible heir)", got.Role)
	}
	requireImage(t, w, "suspended member", s, sBefore)
	requireImage(t, w, "invited member", i, iBefore)

	only := newAccWorld(t, "DEL-08-01 never inherit, nobody eligible")
	only.member(t, "preparer", "suspended", "Sue Pend", joined(0))
	only.member(t, "preparer", "invited", "Ivy Nvited", joined(0))
	got, err := only.store.PreviewAccountDeletion(only.adminCtx())
	if err != nil {
		t.Fatalf("PreviewAccountDeletion: %v", err)
	}
	if got.Outcome != OutcomeDeleteWorkspace || got.HeirName != nil {
		t.Errorf("preview = %+v, want delete_workspace with a nil heir", got)
	}
}

func TestDeleteMyAccount_TheLastMemberIsRefused(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 last member")
	w.member(t, "preparer", "suspended", "Sue Pend", joined(1))
	aBefore, auditBefore := imageOf(t, w.super, w.tenant, w.admin), w.auditTotal(t)

	for _, o := range []AccountDeletionOutcome{OutcomeLeave, OutcomeHandOn} {
		if err := w.store.DeleteMyAccount(w.adminCtx(), o); !errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Errorf("DeleteMyAccount(%s) err = %v, want ErrDeletionOutcomeChanged", o, err)
		}
	}
	w.requireUnscheduled(t)
	requireImage(t, w, "last member", w.admin, aBefore)
	w.requireNoErase(t, w.admin)
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d", n, auditBefore)
	}
}

func TestDeleteMyAccount_AChangedOutcomeIs409(t *testing.T) {
	t.Run("the server would hand on, the body says leave", func(t *testing.T) {
		w := newAccWorld(t, "DEL-08-01 changed 1")
		p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
		aBefore, pBefore, auditBefore := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, p), w.auditTotal(t)

		if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeLeave); !errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Fatalf("err = %v, want ErrDeletionOutcomeChanged", err)
		}
		requireImage(t, w, "admin", w.admin, aBefore)
		requireImage(t, w, "member", p, pBefore)
		w.requireNoErase(t, w.admin, p)
		if n := w.auditTotal(t); n != auditBefore {
			t.Errorf("audit rows = %d, want %d", n, auditBefore)
		}
		if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
			t.Errorf("control: the confirmed outcome hand_on: %v", err)
		}
	})

	t.Run("the server would leave, the body says hand_on", func(t *testing.T) {
		w := newAccWorld(t, "DEL-08-01 changed 2")
		a2 := w.member(t, "admin", "active", "Bola Ade", joined(1))
		p := w.member(t, "preparer", "active", "Chidi Eze", joined(2))
		before := []memberImage{imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, a2), imageOf(t, w.super, w.tenant, p)}
		auditBefore := w.auditTotal(t)

		if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); !errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Fatalf("err = %v, want ErrDeletionOutcomeChanged", err)
		}
		for i, u := range []string{w.admin, a2, p} {
			requireImage(t, w, u, u, before[i])
		}
		w.requireNoErase(t, w.admin, a2, p)
		if n := w.auditTotal(t); n != auditBefore {
			t.Errorf("audit rows = %d, want %d", n, auditBefore)
		}
		if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeLeave); err != nil {
			t.Errorf("control: the confirmed outcome leave: %v", err)
		}
	})
}

func TestDeleteMyAccount_EndsOnlyTheCaller(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 only the caller")
	p, _ := w.grantedMember(t, "Grace Eze")
	q, _ := w.grantedMember(t, "Tunde Bello")
	pBefore, qBefore, aBefore := imageOf(t, w.super, w.tenant, p), imageOf(t, w.super, w.tenant, q), imageOf(t, w.super, w.tenant, w.admin)

	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Fatalf("DeleteMyAccount(leave): %v", err)
	}
	requireImage(t, w, "caller", p, endState(pBefore))
	requireImage(t, w, "other member", q, qBefore)
	requireImage(t, w, "admin", w.admin, aBefore)
	w.requireNoErase(t, q, w.admin)
}

func TestDeleteMyAccount_TwoLastAdminsLeavingAtOnceNeverScheduleTheWorkspace(t *testing.T) {
	super, app := dbTestPools(t)
	for round := 0; round < 20; round++ {
		w := accWorldOn(t, super, app, "DEL-08-01 race")
		a2 := w.member(t, "admin", "active", "Bola Ade", joined(1))

		e1, e2 := race(
			func() error { return w.store.DeleteMyAccount(w.adminCtx(), OutcomeLeave) },
			func() error { return w.store.DeleteMyAccount(w.as(a2), OutcomeLeave) },
		)
		nils := 0
		for _, e := range []error{e1, e2} {
			switch {
			case e == nil:
				nils++
			case !errors.Is(e, ErrDeletionOutcomeChanged):
				t.Fatalf("round %d: err = %v, want nil or ErrDeletionOutcomeChanged", round, e)
			}
		}
		if nils != 1 {
			t.Fatalf("round %d: %d calls succeeded (errs %v, %v), want exactly one", round, nils, e1, e2)
		}
		w.requireUnscheduled(t)
		var active int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND role = 'admin' AND status = 'active'`, w.tenant).Scan(&active); err != nil {
			t.Fatalf("count active admins: %v", err)
		}
		if active != 1 {
			t.Fatalf("round %d: active admins = %d, want 1", round, active)
		}
	}
}

func TestDeleteMyAccount_AFailedAuditRollsBack(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 audit rollback")
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	aBefore, pBefore, auditBefore := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, p), w.auditTotal(t)
	drop := failAudit(t, w.super, w.tenant, evAccountDeleted)

	err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn)
	if pgCode(err) != "23514" {
		t.Fatalf("err = %v (SQLSTATE %q), want the forced audit failure 23514", err, pgCode(err))
	}
	requireImage(t, w, "heir", p, pBefore)
	requireImage(t, w, "caller", w.admin, aBefore)
	w.requireNoErase(t, w.admin)
	w.requireUnscheduled(t)
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d (the hand-on row rolls back with the rest)", n, auditBefore)
	}

	drop()
	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Errorf("after the trigger is gone: %v", err)
	}
}

func TestDeleteMyAccount_NoEraseQueueFailsClosed(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 no queue")
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	pBefore, auditBefore := imageOf(t, w.super, w.tenant, p), w.auditTotal(t)

	bare := NewStore(w.app)
	err := bare.DeleteMyAccount(w.as(p), OutcomeLeave)
	if err == nil {
		t.Fatal("DeleteMyAccount succeeded on a store with no erase queue")
	}
	for _, s := range []error{ErrDeletionOutcomeChanged, ErrNoEraseEmail, db.ErrNotActiveMember, db.ErrWorkspaceLocked} {
		if errors.Is(err, s) {
			t.Errorf("err = %v, want an internal failure, not %v", err, s)
		}
	}
	requireImage(t, w, "caller", p, pBefore)
	if got := imageOf(t, w.super, w.tenant, p); got.Status != "active" || got.Email == "<nil>" {
		t.Errorf("caller = %+v, want still active with an email", got)
	}
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d", n, auditBefore)
	}

	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Errorf("control: the same call on a store with a queue: %v", err)
	}
}

func TestDeleteMyAccount_ANullRowEmailUsesTheTokenEmail(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 null row email")
	for _, rowEmail := range []*string{nil, strp(""), strp("   ")} {
		p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
		if _, err := w.super.Exec(context.Background(),
			`UPDATE memberships SET email = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p, rowEmail); err != nil {
			t.Fatalf("set the row email: %v", err)
		}

		if err := w.store.DeleteMyAccount(w.asWithEmail(p, "P@Example.test"), OutcomeLeave); err != nil {
			t.Fatalf("row email %v: DeleteMyAccount(leave): %v", rowEmail, err)
		}
		if email, _, found := w.erasureOf(t, p); !found || email != "p@example.test" {
			t.Errorf("row email %v: person_erasures = (%q, found %v), want the token email p@example.test", rowEmail, email, found)
		}
		if got := imageOf(t, w.super, w.tenant, p); got.Status != "removed" {
			t.Errorf("row email %v: caller status = %q, want removed", rowEmail, got.Status)
		}
	}
}

func TestDeleteMyAccount_NoEmailAnywhereIsRefused(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 no email")
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET email = NULL WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p); err != nil {
		t.Fatalf("null the row email: %v", err)
	}
	pBefore, auditBefore := imageOf(t, w.super, w.tenant, p), w.auditTotal(t)

	if err := w.store.DeleteMyAccount(w.asWithEmail(p, ""), OutcomeLeave); !errors.Is(err, ErrNoEraseEmail) {
		t.Fatalf("err = %v, want ErrNoEraseEmail", err)
	}
	requireImage(t, w, "caller", p, pBefore)
	w.requireNoErase(t, p)
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d", n, auditBefore)
	}

	if err := w.store.DeleteMyAccount(w.asWithEmail(p, "p@example.test"), OutcomeLeave); err != nil {
		t.Errorf("control: the same call with a token email: %v", err)
	}
}

func TestDeleteMyAccount_ASuspendedOrLockedCallerIsRefused(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 refused callers")
	s := w.member(t, "preparer", "suspended", "Sue Pend", joined(1))
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(2))
	sBefore, pBefore, auditBefore := imageOf(t, w.super, w.tenant, s), imageOf(t, w.super, w.tenant, p), w.auditTotal(t)

	if err := w.store.DeleteMyAccount(w.as(s), OutcomeLeave); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("suspended caller: err = %v, want db.ErrNotActiveMember", err)
	}
	lockByOwner(t, w.super, w.tenant, w.admin)
	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Errorf("locked workspace: err = %v, want db.ErrWorkspaceLocked", err)
	}
	requireImage(t, w, "suspended caller", s, sBefore)
	requireImage(t, w, "locked-workspace caller", p, pBefore)
	w.requireNoErase(t, s, p)
	if n := w.auditTotal(t); n != auditBefore {
		t.Errorf("audit rows = %d, want %d", n, auditBefore)
	}

	unlockByOwner(t, w.super, w.tenant)
	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Errorf("control: the same caller after the unlock: %v", err)
	}
}

func TestDeleteMyAccount_ARepeatCallIsRefused(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 repeat")
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))

	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Fatalf("first DeleteMyAccount(leave): %v", err)
	}
	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("second call: err = %v, want db.ErrNotActiveMember", err)
	}
	if _, gen, found := w.erasureOf(t, p); !found || gen != 1 {
		t.Errorf("person_erasures generation = %d (found %v), want 1", gen, found)
	}
	if n := w.eraseJobs(t, p); n != 1 {
		t.Errorf("person_erase jobs = %d, want 1", n)
	}
	if n := len(w.auditRows(t, evAccountDeleted)); n != 1 {
		t.Errorf("%s rows = %d, want 1", evAccountDeleted, n)
	}
}

func TestDeleteMyAccount_OtherTenantsAreUntouched(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 cross tenant A")
	other := newAccWorld(t, "DEL-08-01 cross tenant B")

	// the caller and the heir hold a second membership, under the same user id, in tenant B
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	heir := w.member(t, "preparer", "active", "Tunde Bello", joined(2))
	for _, u := range []string{p, heir} {
		other.addJoined(t, u, "preparer", "active", "Same Person", joined(0))
	}
	pOther, heirOther, adminOther := imageOf(t, w.super, other.tenant, p), imageOf(t, w.super, other.tenant, heir), imageOf(t, w.super, other.tenant, other.admin)

	if err := w.store.DeleteMyAccount(w.as(p), OutcomeLeave); err != nil {
		t.Fatalf("DeleteMyAccount(leave): %v", err)
	}
	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Fatalf("DeleteMyAccount(hand_on): %v", err)
	}
	if got := imageOf(t, w.super, w.tenant, heir); got.Role != "admin" {
		t.Fatalf("heir role in the caller's tenant = %q, want admin", got.Role)
	}
	requireImage(t, other, "caller in tenant B", p, pOther)
	requireImage(t, other, "heir in tenant B", heir, heirOther)
	requireImage(t, other, "admin of tenant B", other.admin, adminOther)
	if n := other.auditTotal(t); n != 0 {
		t.Errorf("tenant B audit rows = %d, want 0", n)
	}

	// a member of tenant B only, naming tenant A in the token, is not a member of A
	outsider := other.member(t, "admin", "active", "Out Sider", joined(3))
	if err := w.store.DeleteMyAccount(w.as(outsider), OutcomeLeave); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("outsider: err = %v, want db.ErrNotActiveMember", err)
	}
	if _, err := w.store.PreviewAccountDeletion(w.as(outsider)); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("outsider preview: err = %v, want db.ErrNotActiveMember", err)
	}
	if got := imageOf(t, w.super, other.tenant, outsider); got.Status != "active" {
		t.Errorf("outsider status = %q, want active", got.Status)
	}
}

func TestAccountDeletionPreview_ASuspendedCallerIsRefused(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 suspended preview")
	s := w.member(t, "preparer", "suspended", "Sue Pend", joined(1))
	if _, err := w.store.PreviewAccountDeletion(w.as(s)); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("suspended preview: err = %v, want db.ErrNotActiveMember", err)
	}
	if got, err := w.store.PreviewAccountDeletion(w.adminCtx()); err != nil || got.Outcome != OutcomeDeleteWorkspace {
		t.Errorf("control: admin preview = %+v, err = %v, want delete_workspace (a suspended member is not counted)", got, err)
	}
}

func TestDeleteMyAccount_AHeirKeepsTheirGrants(t *testing.T) {
	w := newAccWorld(t, "DEL-08-01 heir grants")
	heir, _ := w.grantedMember(t, "Grace Eze")
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET created_at = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, heir, joined(1)); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
	before := imageOf(t, w.super, w.tenant, heir)

	if err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn); err != nil {
		t.Fatalf("DeleteMyAccount(hand_on): %v", err)
	}
	want := before
	want.Role = "admin"
	requireImage(t, w, "heir", heir, want)
}

func TestDeleteMyAccount_TheSoleAdminAndTheHeirDeletingAtOnceKeepOneAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	for round := 0; round < 20; round++ {
		w := accWorldOn(t, super, app, "DEL-08-01 heir race")
		heir := w.member(t, "preparer", "active", "Chidi Eze", joined(1))

		e1, e2 := race(
			func() error { return w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn) },
			func() error { return w.store.DeleteMyAccount(w.as(heir), OutcomeLeave) },
		)
		nils := 0
		for _, e := range []error{e1, e2} {
			switch {
			case e == nil:
				nils++
			case !errors.Is(e, ErrDeletionOutcomeChanged):
				t.Fatalf("round %d: err = %v, want nil or ErrDeletionOutcomeChanged", round, e)
			}
		}
		if nils != 1 {
			t.Fatalf("round %d: %d calls succeeded (errs %v, %v), want exactly one", round, nils, e1, e2)
		}
		w.requireUnscheduled(t)
		var active, admins int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*), count(*) FILTER (WHERE role = 'admin') FROM memberships WHERE tenant_id = $1 AND status = 'active'`, w.tenant).Scan(&active, &admins); err != nil {
			t.Fatalf("count active members: %v", err)
		}
		if active != 1 || admins != 1 {
			t.Fatalf("round %d: active members = %d, admins = %d, want one member who is an admin", round, active, admins)
		}
	}
}
