package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	evEraseByID     = "person_erase_by_id" // notifications.EraseByIDArgs.Kind()
	evEraseByEmail  = "person_erase"       // the kind EnqueueErasure queues
	forcedFailCode  = "23514"              // check_violation, the trigger helpers' ERRCODE
	staffAccReason  = "customer asked by email"
	staffAccTenant  = "DEL-09-05 staff account"
	staffAccOtherTn = "DEL-09-05 staff account B"
)

// staffAccWorld is a firm tenant with an active admin, an insert-only erase queue and a house member acting as staff.
type staffAccWorld struct {
	invWorld
	houseID, staff string
	ctx            context.Context
}

func newStaffAccWorld(t *testing.T) staffAccWorld {
	t.Helper()
	super, app := dbTestPools(t)
	return staffAccWorldOn(t, super, app, staffAccTenant)
}

func staffAccWorldOn(t *testing.T, super, app *pgxpool.Pool, name string) staffAccWorld {
	t.Helper()
	w := accWorldOn(t, super, app, name)
	t.Cleanup(func() { // runs before the tenant delete; the by-id jobs are global
		_, _ = super.Exec(context.Background(),
			`DELETE FROM river_job WHERE kind = $2 AND args->>'user_id' IN (SELECT user_id::text FROM memberships WHERE tenant_id = $1)`, w.tenant, evEraseByID)
	})
	houseID, staff := houseMember(t, super, "active")
	return staffAccWorld{invWorld: w, houseID: houseID, staff: staff, ctx: staffCtx(staff)}
}

// del is the staff delete with the standard reason.
func (w staffAccWorld) del(target string, o AccountDeletionOutcome, heir string) error {
	return w.store.StaffDeleteAccount(w.ctx, w.tenant, target, o, heir, staffAccReason)
}

type accStaffRow struct {
	Actor, Event, Subject string
	Payload               map[string]any
}

// staffEvents reads every staff row of the tenant, oldest first.
func staffEvents(t *testing.T, super *pgxpool.Pool, tenant string) []accStaffRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor::text, event, coalesce(subject_id::text, ''), payload FROM public.staff_audit_log WHERE tenant_id = $1 ORDER BY id`, tenant)
	if err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	defer rows.Close()
	var out []accStaffRow
	for rows.Next() {
		var r accStaffRow
		var raw []byte
		if err := rows.Scan(&r.Actor, &r.Event, &r.Subject, &raw); err != nil {
			t.Fatalf("scan staff_audit_log: %v", err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatalf("decode staff payload %s: %v", raw, err)
		}
		out = append(out, r)
	}
	return out
}

func eventNames(rows []accStaffRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Event
	}
	return out
}

func (w staffAccWorld) byIDJobs(t *testing.T, user string) []string {
	t.Helper()
	rows, err := w.super.Query(context.Background(),
		`SELECT args::text FROM river_job WHERE kind = $1 AND args->>'user_id' = $2 ORDER BY id`, evEraseByID, user)
	if err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan river_job: %v", err)
		}
		out = append(out, a)
	}
	return out
}

// requireNoErase is invWorld's check plus the by-id jobs.
func (w staffAccWorld) requireNoErase(t *testing.T, users ...string) {
	t.Helper()
	w.invWorld.requireNoErase(t, users...)
	for _, u := range users {
		if jobs := w.byIDJobs(t, u); len(jobs) != 0 {
			t.Errorf("%s jobs for %s = %v, want none", evEraseByID, u, jobs)
		}
	}
}

// snapshot is every membership image of the listed users plus the row counts a refused delete must not move.
type accSnapshot struct {
	w      staffAccWorld
	users  []string
	images []memberImage
	audit  int
}

func (w staffAccWorld) snapshot(t *testing.T, users ...string) accSnapshot {
	t.Helper()
	s := accSnapshot{w: w, users: users, audit: w.auditTotal(t)}
	for _, u := range users {
		s.images = append(s.images, imageOf(t, w.super, w.tenant, u))
	}
	return s
}

// requireUnchanged fails on any moved image, any staff row, any customer audit row and any erase for the users.
func (s accSnapshot) requireUnchanged(t *testing.T) {
	t.Helper()
	for i, u := range s.users {
		requireImage(t, s.w.invWorld, "member "+u, u, s.images[i])
	}
	if n := len(staffEvents(t, s.w.super, s.w.tenant)); n != 0 {
		t.Errorf("staff_audit_log rows of the tenant = %d, want 0", n)
	}
	if n := s.w.auditTotal(t); n != s.audit {
		t.Errorf("audit_log rows = %d, want %d", n, s.audit)
	}
	s.w.requireNoErase(t, s.users...)
	s.w.requireUnscheduled(t)
}

// failEraseInsert makes the insert of an erase job for one user raise; the trigger is dropped on cleanup.
func failEraseInsert(t *testing.T, super *pgxpool.Pool, user string) {
	t.Helper()
	ctx := context.Background()
	name := "erase_job_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx,
		`CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced erase job failure' USING ERRCODE = 'check_violation'; END; $$`); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	if _, err := super.Exec(ctx,
		`CREATE TRIGGER `+name+` BEFORE INSERT ON public.river_job FOR EACH ROW
		 WHEN (NEW.kind IN (`+quoteLiteral(evEraseByEmail)+`, `+quoteLiteral(evEraseByID)+`) AND NEW.args->>'user_id' = `+quoteLiteral(user)+`) EXECUTE FUNCTION `+name+`()`); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
}

func TestStaffAccountDeletion_PreviewAnswersTheDel08Outcome(t *testing.T) {
	type built struct {
		w        staffAccWorld
		target   string
		heir     string
		heirName string
	}
	cases := []struct {
		name  string
		build func(t *testing.T) built
		want  AccountDeletionOutcome
	}{
		{"a preparer", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			return built{w: w, target: w.member(t, "preparer", "active", "Chidi Eze", joined(1))}
		}, OutcomeLeave},
		{"a suspended preparer", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			return built{w: w, target: w.member(t, "preparer", "suspended", "Sue Pend", joined(1))}
		}, OutcomeLeave},
		{"an invited preparer", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			return built{w: w, target: w.member(t, "preparer", "invited", "Ivy Nvited", joined(1))}
		}, OutcomeLeave},
		{"an admin beside another active admin", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			w.member(t, "admin", "active", "Bola Ade", joined(1))
			return built{w: w, target: w.admin}
		}, OutcomeLeave},
		{"the only active admin beside other active members", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
			return built{w: w, target: w.admin, heir: p, heirName: "Chidi Eze"}
		}, OutcomeHandOn},
		{"the only active admin beside a suspended member", func(t *testing.T) built {
			w := newStaffAccWorld(t)
			w.member(t, "preparer", "suspended", "Sue Pend", joined(1))
			return built{w: w, target: w.admin}
		}, OutcomeDeleteWorkspace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.build(t)
			snap := b.w.snapshot(t, b.target)

			got, err := b.w.store.StaffPreviewAccountDeletion(b.w.ctx, b.w.tenant, b.target)
			if err != nil {
				t.Fatalf("StaffPreviewAccountDeletion: %v", err)
			}
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if b.heir == "" {
				if got.HeirName != nil || got.HeirUserID != nil {
					t.Errorf("preview = %+v, want no heir", got)
				}
			} else if got.HeirUserID == nil || *got.HeirUserID != b.heir || got.HeirName == nil || *got.HeirName != b.heirName {
				t.Errorf("heir = (%v, %v), want (%s, %s)", got.HeirUserID, got.HeirName, b.heir, b.heirName)
			}
			snap.requireUnchanged(t) // a preview writes nothing
		})
	}
}

func TestStaffAccountDeletion_LeaveEndsTheMemberAndQueuesTheErase(t *testing.T) {
	w := newStaffAccWorld(t)
	a2 := w.member(t, "admin", "active", "Bola Ade", joined(1))
	p, _ := w.grantedMember(t, "Grace Eze")
	pBefore, aBefore, a2Before := imageOf(t, w.super, w.tenant, p), imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, a2)

	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Fatalf("StaffDeleteAccount(leave): %v", err)
	}

	requireImage(t, w.invWorld, "target", p, endState(pBefore))
	requireImage(t, w.invWorld, "first admin", w.admin, aBefore)
	requireImage(t, w.invWorld, "second admin", a2, a2Before)

	rows := staffEvents(t, w.super, w.tenant)
	if !slices.Equal(eventNames(rows), []string{evAccountDeleted}) {
		t.Fatalf("staff events = %v, want exactly [%s]", eventNames(rows), evAccountDeleted)
	}
	r := rows[0]
	if r.Actor != w.staff || r.Subject != p {
		t.Errorf("staff row actor/subject = %s/%s, want %s/%s", r.Actor, r.Subject, w.staff, p)
	}
	if !slices.Equal(keysOf(r.Payload), []string{"reason", "role", "user_id"}) || r.Payload["user_id"] != p || r.Payload["role"] != "preparer" || r.Payload["reason"] != staffAccReason {
		t.Errorf("staff payload = %v, want exactly {user_id: %s, role: preparer, reason: %q}", r.Payload, p, staffAccReason)
	}
	cust := w.auditRows(t, evAccountDeleted)
	if len(cust) != 1 || cust[0].Actor != audit.SupportActor {
		t.Errorf("audit_log %s rows = %+v, want one with actor %q", evAccountDeleted, cust, audit.SupportActor)
	}

	email, gen, found := w.erasureOf(t, p)
	if !found || email != p+"@members.test" || gen != 1 {
		t.Errorf("person_erasures = (%q, gen %d, found %v), want P's row email at generation 1", email, gen, found)
	}
	if n := w.eraseJobs(t, p); n != 1 {
		t.Errorf("%s jobs for P = %d, want 1", evEraseByEmail, n)
	}
	if jobs := w.byIDJobs(t, p); len(jobs) != 0 {
		t.Errorf("%s jobs for P = %v, want none", evEraseByID, jobs)
	}
	w.requireNoErase(t, w.admin, a2)
	w.requireUnscheduled(t)
}

func TestStaffAccountDeletion_HandOnGivesAdminToTheEarliestActiveMember(t *testing.T) {
	w := newStaffAccWorld(t)
	lo, hi := uuid.NewString(), uuid.NewString()
	if lo > hi {
		lo, hi = hi, lo
	}
	s := w.member(t, "preparer", "suspended", "Sue Pend", joined(0))
	i := w.member(t, "preparer", "invited", "Ivy Nvited", joined(0))
	// the higher id is seeded first, so insertion order cannot decide the tie
	r := w.addJoined(t, hi, "reviewer", "active", "Tunde Bello", joined(1))
	p := w.addJoined(t, lo, "preparer", "active", "Chidi Eze", joined(1))
	// joined last under the lowest id: neither the latest join nor the lowest id alone picks P
	late := w.addJoined(t, "00000000-"+uuid.NewString()[9:], "reviewer", "active", "Late Joiner", joined(5))
	aBefore, sBefore, iBefore, rBefore, pBefore, lateBefore := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, s),
		imageOf(t, w.super, w.tenant, i), imageOf(t, w.super, w.tenant, r), imageOf(t, w.super, w.tenant, p), imageOf(t, w.super, w.tenant, late)

	if err := w.del(w.admin, OutcomeHandOn, p); err != nil {
		t.Fatalf("StaffDeleteAccount(hand_on): %v", err)
	}

	heir := pBefore
	heir.Role = "admin"
	requireImage(t, w.invWorld, "heir", p, heir)
	requireImage(t, w.invWorld, "suspended member", s, sBefore)
	requireImage(t, w.invWorld, "invited member", i, iBefore)
	requireImage(t, w.invWorld, "later active member", r, rBefore)
	requireImage(t, w.invWorld, "last active member", late, lateBefore)
	requireImage(t, w.invWorld, "target", w.admin, endState(aBefore))

	rows := staffEvents(t, w.super, w.tenant)
	if !slices.Equal(eventNames(rows), []string{evAdminHandedOn, evAccountDeleted}) {
		t.Fatalf("staff events = %v, want [%s %s] in that order", eventNames(rows), evAdminHandedOn, evAccountDeleted)
	}
	handed, deleted := rows[0], rows[1]
	if handed.Actor != w.staff || handed.Subject != p {
		t.Errorf("hand-on row actor/subject = %s/%s, want %s/%s", handed.Actor, handed.Subject, w.staff, p)
	}
	if !slices.Equal(keysOf(handed.Payload), []string{"from", "reason", "to", "user_id"}) || handed.Payload["user_id"] != p ||
		handed.Payload["from"] != "preparer" || handed.Payload["to"] != "admin" || handed.Payload["reason"] != staffAccReason {
		t.Errorf("hand-on payload = %v, want {user_id: %s, from: preparer, to: admin, reason: %q}", handed.Payload, p, staffAccReason)
	}
	if deleted.Actor != w.staff || deleted.Subject != w.admin {
		t.Errorf("deleted row actor/subject = %s/%s, want %s/%s", deleted.Actor, deleted.Subject, w.staff, w.admin)
	}
	if !slices.Equal(keysOf(deleted.Payload), []string{"reason", "role", "user_id"}) || deleted.Payload["user_id"] != w.admin ||
		deleted.Payload["role"] != "admin" || deleted.Payload["reason"] != staffAccReason {
		t.Errorf("deleted payload = %v, want {user_id: %s, role: admin, reason: %q}", deleted.Payload, w.admin, staffAccReason)
	}
	for _, ev := range []string{evAdminHandedOn, evAccountDeleted} {
		if cust := w.auditRows(t, ev); len(cust) != 1 || cust[0].Actor != audit.SupportActor {
			t.Errorf("audit_log %s rows = %+v, want one with actor %q", ev, cust, audit.SupportActor)
		}
	}

	if email, _, found := w.erasureOf(t, w.admin); !found || email != w.admin+"@members.test" || w.eraseJobs(t, w.admin) != 1 {
		t.Errorf("target erasure = (%q, found %v), jobs = %d, want the row email queued once", email, found, w.eraseJobs(t, w.admin))
	}
	w.requireNoErase(t, p, r, s, i, late)
	w.requireUnscheduled(t)
}

func TestStaffAccountDeletion_TheLastActiveMemberIsRefused(t *testing.T) {
	w := newStaffAccWorld(t)
	w.member(t, "preparer", "suspended", "Sue Pend", joined(1))
	snap := w.snapshot(t, w.admin)

	for _, o := range []AccountDeletionOutcome{OutcomeLeave, OutcomeHandOn} {
		err := w.del(w.admin, o, heirIf(o, uuid.NewString()))
		if !errors.Is(err, ErrLastActiveMember) {
			t.Errorf("StaffDeleteAccount(%s) err = %v, want ErrLastActiveMember", o, err)
		}
		if errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Errorf("StaffDeleteAccount(%s) err = %v, want the last-member refusal, not an outcome change", o, err)
		}
	}
	snap.requireUnchanged(t)

	// control: with a second active member the same delete goes through
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(2))
	if err := w.del(w.admin, OutcomeHandOn, p); err != nil {
		t.Errorf("control: StaffDeleteAccount(hand_on) with a second active member: %v", err)
	}
}

func TestStaffAccountDeletion_AChangedOutcomeIsRefused(t *testing.T) {
	type setup struct {
		w           staffAccWorld
		users       []string
		target      string
		body        AccountDeletionOutcome
		bodyHeir    string
		confirm     AccountDeletionOutcome
		confirmHeir string
	}
	cases := []struct {
		name  string
		build func(t *testing.T) setup
	}{
		{"the body says leave while the server says hand_on", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			return setup{w: w, users: []string{w.admin, p}, target: w.admin, body: OutcomeLeave, confirm: OutcomeHandOn, confirmHeir: p}
		}},
		{"the body says hand_on while the server says leave", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			w.member(t, "admin", "active", "Bola Ade", joined(1))
			r := w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
			return setup{w: w, users: []string{w.admin, r}, target: w.admin, body: OutcomeHandOn, bodyHeir: r, confirm: OutcomeLeave}
		}},
		{"the body names another heir than the server's", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			r := w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
			return setup{w: w, users: []string{w.admin, p, r}, target: w.admin, body: OutcomeHandOn, bodyHeir: r, confirm: OutcomeHandOn, confirmHeir: p}
		}},
		{"the body names the heir in upper case", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			return setup{w: w, users: []string{w.admin, p}, target: w.admin, body: OutcomeHandOn, bodyHeir: strings.ToUpper(p), confirm: OutcomeHandOn, confirmHeir: p}
		}},
		{"the body names an unknown heir", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			return setup{w: w, users: []string{w.admin, p}, target: w.admin, body: OutcomeHandOn, bodyHeir: uuid.NewString(), confirm: OutcomeHandOn, confirmHeir: p}
		}},
		{"the body names the target as the heir", func(t *testing.T) setup {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			return setup{w: w, users: []string{w.admin, p}, target: w.admin, body: OutcomeHandOn, bodyHeir: w.admin, confirm: OutcomeHandOn, confirmHeir: p}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.build(t)
			snap := s.w.snapshot(t, s.users...)

			if err := s.w.del(s.target, s.body, s.bodyHeir); !errors.Is(err, ErrDeletionOutcomeChanged) {
				t.Fatalf("err = %v, want ErrDeletionOutcomeChanged", err)
			}
			snap.requireUnchanged(t)

			if err := s.w.del(s.target, s.confirm, s.confirmHeir); err != nil {
				t.Errorf("control: the confirmed outcome %s: %v", s.confirm, err)
			}
		})
	}
}

func TestStaffAccountDeletion_NoUsableEmailErasesByID(t *testing.T) {
	cases := []struct {
		name  string
		email *string
		byID  bool
	}{
		{"a NULL email", nil, true},
		{"the address a@", strp("a@"), true},
		{"a blank address", strp("   "), true},
		{"the shortest usable address a@b", strp("a@b"), false}, // notifications.UsableEraseEmail: 3 to 254 bytes
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			if _, err := w.super.Exec(context.Background(),
				`UPDATE memberships SET email = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p, tc.email); err != nil {
				t.Fatalf("set the row email: %v", err)
			}

			if err := w.del(p, OutcomeLeave, ""); err != nil {
				t.Fatalf("StaffDeleteAccount(leave): %v", err)
			}

			if got := imageOf(t, w.super, w.tenant, p); got.Status != "removed" || got.Email != "<nil>" {
				t.Errorf("target = %+v, want removed with a NULL email", got)
			}
			if n := len(staffEvents(t, w.super, w.tenant)); n != 1 {
				t.Errorf("staff rows = %d, want 1 (the delete goes on)", n)
			}
			_, _, erased := w.erasureOf(t, p)
			byID := w.byIDJobs(t, p)
			if tc.byID {
				if erased || w.eraseJobs(t, p) != 0 {
					t.Errorf("person_erasures row = %v, %s jobs = %d, want neither", erased, evEraseByEmail, w.eraseJobs(t, p))
				}
				var args map[string]any
				if len(byID) != 1 || json.Unmarshal([]byte(byID[0]), &args) != nil || len(args) != 1 || args["user_id"] != p {
					t.Errorf("%s jobs = %v, want exactly one with args {user_id: %s}", evEraseByID, byID, p)
				}
				return
			}
			if !erased || w.eraseJobs(t, p) != 1 || len(byID) != 0 {
				t.Errorf("person_erasures row = %v, %s jobs = %d, %s jobs = %v, want the row and one by-email job", erased, evEraseByEmail, w.eraseJobs(t, p), evEraseByID, byID)
			}
		})
	}
}

func TestStaffAccountDeletion_Refusals(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	removed := w.removedRow(t, "Rae Moved")
	snap := w.snapshot(t, w.admin, p, removed)

	t.Run("a locked workspace", func(t *testing.T) {
		lockByOwner(t, w.super, w.tenant, w.admin)
		defer unlockByOwner(t, w.super, w.tenant)
		for _, target := range []string{p, uuid.NewString()} {
			if err := w.del(target, OutcomeLeave, ""); !errors.Is(err, db.ErrWorkspaceLocked) {
				t.Errorf("target %s: err = %v, want db.ErrWorkspaceLocked", target, err)
			}
		}
	})
	t.Run("a target that is not an active-or-suspended member of the path tenant", func(t *testing.T) {
		for name, target := range map[string]string{"a removed member": removed, "a random uuid": uuid.NewString(), "a malformed id": "x", "an empty id": ""} {
			if err := w.del(target, OutcomeLeave, ""); !errors.Is(err, ErrMembershipNotFound) {
				t.Errorf("%s: delete err = %v, want ErrMembershipNotFound", name, err)
			}
			if _, err := w.store.StaffPreviewAccountDeletion(w.ctx, w.tenant, target); !errors.Is(err, ErrMembershipNotFound) {
				t.Errorf("%s: preview err = %v, want ErrMembershipNotFound", name, err)
			}
		}
	})
	t.Run("an unknown or malformed tenant", func(t *testing.T) {
		for name, tenant := range map[string]string{"a random uuid": uuid.NewString(), "a malformed id": "x"} {
			if err := w.store.StaffDeleteAccount(w.ctx, tenant, p, OutcomeLeave, "", staffAccReason); !errors.Is(err, ErrTenantNotFound) {
				t.Errorf("%s: delete err = %v, want ErrTenantNotFound", name, err)
			}
			if _, err := w.store.StaffPreviewAccountDeletion(w.ctx, tenant, p); !errors.Is(err, ErrTenantNotFound) {
				t.Errorf("%s: preview err = %v, want ErrTenantNotFound", name, err)
			}
		}
	})
	snap.requireUnchanged(t)

	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Errorf("control: the same delete on an unlocked workspace: %v", err)
	}
}

func TestStaffAccountDeletion_ActsOnlyOnThePathTenant(t *testing.T) {
	w := newStaffAccWorld(t)
	b := staffAccWorldOn(t, w.super, w.app, staffAccOtherTn)

	// the target and the heir hold a second membership, under the same user id, in tenant B
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	b.addJoined(t, p, "preparer", "active", "Same Person", joined(0))
	b.addJoined(t, w.admin, "admin", "active", "Same Admin", joined(0))
	pB, adminB, ownAdminB := imageOf(t, w.super, b.tenant, p), imageOf(t, w.super, b.tenant, w.admin), imageOf(t, w.super, b.tenant, b.admin)

	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Fatalf("StaffDeleteAccount(leave) in A: %v", err)
	}
	if got := imageOf(t, w.super, w.tenant, p); got.Status != "removed" {
		t.Fatalf("control: the target's status in A = %q, want removed", got.Status)
	}
	q := w.member(t, "preparer", "active", "Tunde Bello", joined(2))
	b.addJoined(t, q, "preparer", "active", "Same Person Q", joined(0))
	qB := imageOf(t, w.super, b.tenant, q)
	if err := w.del(w.admin, OutcomeHandOn, q); err != nil {
		t.Fatalf("StaffDeleteAccount(hand_on) in A: %v", err)
	}
	if got := imageOf(t, w.super, w.tenant, q); got.Role != "admin" {
		t.Fatalf("control: the heir's role in A = %q, want admin", got.Role)
	}

	requireImage(t, b.invWorld, "same id as the leaver, in B", p, pB)
	requireImage(t, b.invWorld, "same id as the deleted admin, in B", w.admin, adminB)
	requireImage(t, b.invWorld, "same id as the heir, in B", q, qB)
	requireImage(t, b.invWorld, "B's own admin", b.admin, ownAdminB)
	if n := len(staffEvents(t, w.super, b.tenant)); n != 0 {
		t.Errorf("staff_audit_log rows of B = %d, want 0", n)
	}
	if n := b.auditTotal(t); n != 0 {
		t.Errorf("audit_log rows of B = %d, want 0", n)
	}

	t.Run("a member of B only is not found through A", func(t *testing.T) {
		outsider := b.member(t, "preparer", "active", "Out Sider", joined(3))
		before := imageOf(t, w.super, b.tenant, outsider)
		if err := w.del(outsider, OutcomeLeave, ""); !errors.Is(err, ErrMembershipNotFound) {
			t.Errorf("delete err = %v, want ErrMembershipNotFound", err)
		}
		if _, err := w.store.StaffPreviewAccountDeletion(w.ctx, w.tenant, outsider); !errors.Is(err, ErrMembershipNotFound) {
			t.Errorf("preview err = %v, want ErrMembershipNotFound", err)
		}
		requireImage(t, b.invWorld, "the outsider", outsider, before)
		b.requireNoErase(t, outsider)
	})
}

func TestStaffAccountDeletion_AFailedStaffRowRollsBack(t *testing.T) {
	cases := []struct {
		name    string
		handOn  bool
		failing string
	}{
		{"leave with the deleted row failing", false, evAccountDeleted},
		{"hand_on with the deleted row failing, after the heir's role change", true, evAccountDeleted},
		{"hand_on with the hand-on row failing", true, evAdminHandedOn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			target, outcome, heir := p, OutcomeLeave, ""
			users := []string{w.admin, p}
			if tc.handOn {
				target, outcome, heir = w.admin, OutcomeHandOn, p
			} else {
				users = append(users, w.member(t, "admin", "active", "Bola Ade", joined(2)))
			}
			snap := w.snapshot(t, users...)
			drop := failStaffAudit(t, w.super, w.tenant, tc.failing)

			err := w.del(target, outcome, heir)
			if pgCode(err) != forcedFailCode {
				t.Fatalf("err = %v (SQLSTATE %q), want the forced failure %s", err, pgCode(err), forcedFailCode)
			}
			snap.requireUnchanged(t)
			if got := imageOf(t, w.super, w.tenant, target); got.Status != "active" || got.Email == "<nil>" {
				t.Errorf("target = %+v, want active with its email", got)
			}

			drop()
			if err := w.del(target, outcome, heir); err != nil {
				t.Errorf("control: the same delete once the trigger is gone: %v", err)
			}
		})
	}
}

func TestStaffAccountDeletion_AFailedEraseInsertRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email *string
	}{
		{"a by-email job", nil},
		{"a by-id job", strp("a@")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
			if tc.email != nil {
				if _, err := w.super.Exec(context.Background(),
					`UPDATE memberships SET email = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p, tc.email); err != nil {
					t.Fatalf("set the row email: %v", err)
				}
			}
			snap := w.snapshot(t, w.admin, p)
			failEraseInsert(t, w.super, p)

			err := w.del(p, OutcomeLeave, "")
			if pgCode(err) != forcedFailCode {
				t.Fatalf("err = %v (SQLSTATE %q), want the forced failure %s", err, pgCode(err), forcedFailCode)
			}
			snap.requireUnchanged(t) // no staff row, no erasure row, the target still active
		})
	}
}

func TestStaffAccountDeletion_NoEraseQueueFailsClosed(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	snap := w.snapshot(t, w.admin, p)

	bare := NewStore(w.app)
	for _, o := range []AccountDeletionOutcome{OutcomeLeave, OutcomeHandOn} {
		err := bare.StaffDeleteAccount(w.ctx, w.tenant, p, o, heirIf(o, uuid.NewString()), staffAccReason)
		if !errors.Is(err, errNoEraseQueue) {
			t.Errorf("%s: err = %v, want errNoEraseQueue", o, err)
		}
	}
	snap.requireUnchanged(t)

	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Errorf("control: the same delete on a store with a queue: %v", err)
	}
}

func TestStaffAccountDeletion_RefusesWithoutAStaffCaller(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	snap := w.snapshot(t, w.admin, p)
	_, suspended := houseMember(t, w.super, "suspended")

	callers := map[string]context.Context{
		"no caller":                          context.Background(),
		"a customer admin":                   w.adminCtx(),
		"the staff flag on a customer admin": staffCtx(w.admin),
		"a suspended house member":           staffCtx(suspended),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.StaffPreviewAccountDeletion(ctx, w.tenant, p); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("preview: err = %v, want db.ErrNotStaff", err)
			}
			if err := w.store.StaffDeleteAccount(ctx, w.tenant, p, OutcomeLeave, "", staffAccReason); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("delete: err = %v, want db.ErrNotStaff", err)
			}
		})
	}
	snap.requireUnchanged(t)

	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Errorf("control: the staff caller on the same target: %v", err)
	}
}

// A suspended target in a workspace with no active member decides delete_workspace, so the delete answers the last-member 409 (pinned surprise).
func TestStaffAccountDeletion_ASuspendedTargetBesideNoActiveMemberAnswersLastMember(t *testing.T) {
	w := newStaffAccWorld(t)
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, w.tenant, w.admin); err != nil {
		t.Fatalf("suspend the only admin: %v", err)
	}
	snap := w.snapshot(t, w.admin)

	got, err := w.store.StaffPreviewAccountDeletion(w.ctx, w.tenant, w.admin)
	if err != nil || got.Outcome != OutcomeDeleteWorkspace {
		t.Fatalf("preview = %+v, %v; want delete_workspace", got, err)
	}
	if err := w.del(w.admin, OutcomeLeave, ""); !errors.Is(err, ErrLastActiveMember) {
		t.Errorf("err = %v, want ErrLastActiveMember", err)
	}
	snap.requireUnchanged(t)

	w.member(t, "preparer", "active", "Chidi Eze", joined(1)) // control: an active member makes it a plain leave
	if err := w.del(w.admin, OutcomeLeave, ""); err != nil {
		t.Errorf("control: leave beside an active member: %v", err)
	}
}

func TestStaffAccountDeletion_ASuspendedOrInvitedTargetLeavesAndASuspendedAdminIsNoSecondAdmin(t *testing.T) {
	for _, status := range []string{"suspended", "invited"} {
		t.Run("a "+status+" target", func(t *testing.T) {
			w := newStaffAccWorld(t)
			p := w.member(t, "preparer", status, "Sue Pend", joined(1))
			other := w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
			adminBefore, otherBefore, pBefore := imageOf(t, w.super, w.tenant, w.admin), imageOf(t, w.super, w.tenant, other), imageOf(t, w.super, w.tenant, p)

			if err := w.del(p, OutcomeLeave, ""); err != nil {
				t.Fatalf("StaffDeleteAccount(leave): %v", err)
			}
			requireImage(t, w.invWorld, "target", p, endState(pBefore))
			requireImage(t, w.invWorld, "admin", w.admin, adminBefore)
			requireImage(t, w.invWorld, "other member", other, otherBefore)
			if rows := staffEvents(t, w.super, w.tenant); !slices.Equal(eventNames(rows), []string{evAccountDeleted}) || rows[0].Subject != p {
				t.Errorf("staff rows = %+v, want one %s for the target", rows, evAccountDeleted)
			}
			if _, _, found := w.erasureOf(t, p); !found || w.eraseJobs(t, p) != 1 {
				t.Errorf("erasure of the target: row %v, jobs %d, want the row and one job", found, w.eraseJobs(t, p))
			}
		})
	}
	t.Run("a suspended admin target hands nothing on", func(t *testing.T) {
		w := newStaffAccWorld(t)
		p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
		if _, err := w.super.Exec(context.Background(),
			`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, w.tenant, w.admin); err != nil {
			t.Fatalf("suspend the admin: %v", err)
		}
		pBefore, aBefore := imageOf(t, w.super, w.tenant, p), imageOf(t, w.super, w.tenant, w.admin)
		if err := w.del(w.admin, OutcomeHandOn, p); !errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Fatalf("hand_on err = %v, want ErrDeletionOutcomeChanged (a suspended admin hands nothing on)", err)
		}
		if err := w.del(w.admin, OutcomeLeave, ""); err != nil {
			t.Fatalf("leave: %v", err)
		}
		requireImage(t, w.invWorld, "target", w.admin, endState(aBefore))
		requireImage(t, w.invWorld, "the active member", p, pBefore)
	})
	t.Run("the only active admin beside a suspended admin hands on", func(t *testing.T) {
		w := newStaffAccWorld(t)
		sa := w.member(t, "admin", "suspended", "Sue Pend", joined(0))
		p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
		saBefore := imageOf(t, w.super, w.tenant, sa)
		if err := w.del(w.admin, OutcomeLeave, ""); !errors.Is(err, ErrDeletionOutcomeChanged) {
			t.Fatalf("leave err = %v, want ErrDeletionOutcomeChanged (the suspended admin is no second admin)", err)
		}
		if err := w.del(w.admin, OutcomeHandOn, p); err != nil {
			t.Fatalf("hand_on: %v", err)
		}
		requireImage(t, w.invWorld, "suspended admin", sa, saBefore)
		if got := imageOf(t, w.super, w.tenant, p); got.Role != "admin" {
			t.Errorf("heir role = %q, want admin", got.Role)
		}
	})
}

func TestStaffAccountDeletion_AHeirChangedUnderTheRowLockIsRefused(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	r := w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
	prev, err := w.store.StaffPreviewAccountDeletion(w.ctx, w.tenant, w.admin)
	if err != nil || prev.HeirUserID == nil || *prev.HeirUserID != p {
		t.Fatalf("preview = %+v, %v; want hand_on to %s", prev, err, p)
	}
	snap := w.snapshot(t, w.admin, r)

	// the holder suspends the previewed heir and keeps the row lock until the delete waits on it
	ctx := context.Background()
	tx, err := w.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	var pid int
	if _, err := tx.Exec(ctx, `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, w.tenant, p); err != nil {
		t.Fatalf("suspend the heir: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.del(w.admin, OutcomeHandOn, p) }()
	waitRowLockWaiters(t, w.invWorld, pid, 1, done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the suspension: %v", err)
	}

	if err := <-done; !errors.Is(err, ErrDeletionOutcomeChanged) {
		t.Fatalf("err = %v, want ErrDeletionOutcomeChanged", err)
	}
	if got := imageOf(t, w.super, w.tenant, p); got.Status != "suspended" || got.Role != "preparer" {
		t.Errorf("former heir = %+v, want suspended and still a preparer", got)
	}
	snap.requireUnchanged(t)

	if err := w.del(w.admin, OutcomeHandOn, r); err != nil { // control: the heir the lock shows
		t.Errorf("control: hand_on to the new heir: %v", err)
	}
}

func TestStaffAccountDeletion_NoNameOrEmailOfTheMemberIsWritten(t *testing.T) {
	w := newStaffAccWorld(t)
	const leaverName, leaverMail, heirName, heirMail = "Zyx Quillfeather", "zyx.quillfeather@leak.test", "Wren Marlowe", "wren.marlowe@leak.test"
	leaver := w.member(t, "preparer", "active", leaverName, joined(1))
	heir := w.member(t, "reviewer", "active", heirName, joined(2))
	for user, mail := range map[string]string{leaver: leaverMail, heir: heirMail} {
		if _, err := w.super.Exec(context.Background(),
			`UPDATE memberships SET email = $3 WHERE tenant_id = $1 AND user_id = $2`, w.tenant, user, mail); err != nil {
			t.Fatalf("set the row email: %v", err)
		}
	}
	if err := w.del(leaver, OutcomeLeave, ""); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := w.del(w.admin, OutcomeHandOn, heir); err != nil {
		t.Fatalf("hand_on: %v", err)
	}

	var text string
	for _, q := range []string{
		`SELECT coalesce(string_agg(to_jsonb(s)::text, ' '), '') FROM public.staff_audit_log s WHERE tenant_id = $1`,
		`SELECT coalesce(string_agg(to_jsonb(a)::text, ' '), '') FROM public.audit_log a WHERE tenant_id = $1`,
	} {
		var got string
		if err := w.super.QueryRow(context.Background(), q, w.tenant).Scan(&got); err != nil {
			t.Fatalf("read rows: %v", err)
		}
		text += got + " "
	}
	for _, id := range []string{leaver, heir, w.admin} { // the ids are the control: the rows exist and name the people by id
		if !strings.Contains(text, id) {
			t.Fatalf("no audit row names %s; rows: %s", id, text)
		}
	}
	for _, needle := range []string{leaverName, "quillfeather", leaverMail, heirName, "marlowe", heirMail, "members.test", "Ada Obi"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
			t.Errorf("an audit row carries %q: %s", needle, text)
		}
	}
}

func TestStaffAccountDeletion_ARepeatedDeleteAndAReasonTheStoreCannotWriteChangeNothing(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	if err := w.del(p, OutcomeLeave, ""); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := w.del(p, OutcomeLeave, ""); !errors.Is(err, ErrMembershipNotFound) {
		t.Errorf("repeat delete err = %v, want ErrMembershipNotFound", err)
	}
	if n := len(staffEvents(t, w.super, w.tenant)); n != 1 {
		t.Errorf("staff rows after a repeat = %d, want 1", n)
	}
	if n := w.eraseJobs(t, p); n != 1 {
		t.Errorf("%s jobs after a repeat = %d, want 1", evEraseByEmail, n)
	}

	// a NUL fails at the first staff row, after the heir's role change
	q := w.member(t, "reviewer", "active", "Tunde Bello", joined(2))
	before := w.snapshot(t, w.admin, q)
	before.audit = w.auditTotal(t)
	staffRows := len(staffEvents(t, w.super, w.tenant))
	err := w.store.StaffDeleteAccount(w.ctx, w.tenant, w.admin, OutcomeHandOn, q, "a\x00b")
	if err == nil || errors.Is(err, ErrDeletionOutcomeChanged) {
		t.Fatalf("hand_on with a NUL reason err = %v, want a write failure", err)
	}
	for i, u := range before.users {
		requireImage(t, w.invWorld, "member "+u, u, before.images[i])
	}
	if n := len(staffEvents(t, w.super, w.tenant)); n != staffRows {
		t.Errorf("staff rows = %d, want %d", n, staffRows)
	}
	if n := w.auditTotal(t); n != before.audit {
		t.Errorf("audit_log rows = %d, want %d", n, before.audit)
	}
	w.requireNoErase(t, w.admin, q)
}

// memberRoutes serves the two member routes over the real store behind the platform's staff gate.
func memberRoutes(t *testing.T, store *Store) http.Handler {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("tenancy")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /v1/staff/tenants/{id}/members/{user_id}/deletion", StaffAccountDeletionPreviewHandler(store.StaffPreviewAccountDeletion, slog.Default()))
	app.Mux.HandleFunc("POST /v1/staff/tenants/{id}/members/{user_id}/deletion", StaffDeleteAccountHandler(store.StaffDeleteAccount, slog.Default()))
	app.RequireGateway(staffGatewayToken)
	return app.Handler()
}

func TestStaffAccountDeletionRoutes_RefuseEveryNonStaffCallerAndServeStaff(t *testing.T) {
	w := newStaffAccWorld(t)
	h := memberRoutes(t, w.store)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	path := "/v1/staff/tenants/" + w.tenant + "/members/" + p + "/deletion"
	snap := w.snapshot(t, w.admin, p)

	call := func(method string, c staffCaller, gateway bool) *httptest.ResponseRecorder {
		body := ""
		if method == http.MethodPost {
			body = `{"outcome":"leave","reason":"customer asked by email"}`
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if gateway {
			r.Header.Set(platform.HeaderGatewayToken, staffGatewayToken)
		}
		r.Header.Set("X-User-ID", c.subject)
		r.Header.Set("X-User-Role", "authenticated")
		r.Header.Set("X-Tenant-ID", c.tenant)
		if c.staff {
			r.Header.Set("X-User-Staff", "true")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	refused := map[string]struct {
		c       staffCaller
		gateway bool
		status  []int
	}{
		"the tenant's own admin":                    {staffCaller{subject: w.admin, tenant: w.tenant}, true, []int{403}},
		"a house member without the staff claim":    {staffCaller{subject: w.staff}, true, []int{403}},
		"the staff claim on a customer admin":       {staffCaller{subject: w.admin, tenant: w.tenant, staff: true}, true, []int{403}},
		"the staff claim without the gateway token": {staffCaller{subject: w.staff, staff: true}, false, []int{401, 403}},
	}
	for name, tc := range refused {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := call(method, tc.c, tc.gateway)
			if !slices.Contains(tc.status, rec.Code) {
				t.Errorf("%s %s: got %d %q, want one of %v", method, name, rec.Code, rec.Body.String(), tc.status)
			}
		}
	}
	snap.requireUnchanged(t)

	rec := call(http.MethodGet, staffCaller{subject: w.staff, staff: true}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"leave"`) {
		t.Fatalf("staff control (GET) = %d %q, want 200 leave", rec.Code, rec.Body.String())
	}
	rec = call(http.MethodPost, staffCaller{subject: w.staff, staff: true}, true)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"outcome":"leave"}` {
		t.Fatalf("staff control (POST) = %d %q, want 200 {\"outcome\":\"leave\"}", rec.Code, rec.Body.String())
	}
	if got := imageOf(t, w.super, w.tenant, p); got.Status != "removed" {
		t.Errorf("target after the staff POST = %+v, want removed", got)
	}
}

// A store over a closed pool fails any statement, so a refusal it still gives was made before the first one.
func TestStaffAccountDeletion_TheGuardsRefuseBeforeAnyStatement(t *testing.T) {
	w := newStaffAccWorld(t)
	p := w.member(t, "preparer", "active", "Chidi Eze", joined(1))
	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	noQueue := NewStore(closed)
	withQueue := NewStore(closed)
	withQueue.SetEraseQueue(w.store.eraseQueue)

	if err := noQueue.StaffDeleteAccount(w.ctx, w.tenant, p, OutcomeLeave, "", staffAccReason); !errors.Is(err, errNoEraseQueue) {
		t.Errorf("no queue: err = %v, want errNoEraseQueue", err)
	}
	for name, ctx := range map[string]context.Context{"no caller": context.Background(), "a customer admin": w.adminCtx()} {
		if _, err := withQueue.StaffPreviewAccountDeletion(ctx, w.tenant, p); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s: preview err = %v, want db.ErrNotStaff", name, err)
		}
		if err := withQueue.StaffDeleteAccount(ctx, w.tenant, p, OutcomeLeave, "", staffAccReason); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s: delete err = %v, want db.ErrNotStaff", name, err)
		}
	}
	if err := withQueue.StaffDeleteAccount(w.ctx, "x", p, OutcomeLeave, "", staffAccReason); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("malformed tenant: delete err = %v, want ErrTenantNotFound", err)
	}
	// control: with a staff caller and a real tenant the closed pool is reached and fails
	err = withQueue.StaffDeleteAccount(w.ctx, w.tenant, p, OutcomeLeave, "", staffAccReason)
	if err == nil || errors.Is(err, db.ErrNotStaff) || errors.Is(err, errNoEraseQueue) || errors.Is(err, ErrTenantNotFound) {
		t.Errorf("control: err = %v, want a closed-pool failure", err)
	}
}
