package tenancy

import (
	"context"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/approval"
)

const strandRole = "k"

// strandWorld is an in-house workspace with admin A (w.admin) and reviewer R, the only member of role K.
type strandWorld struct {
	staffAccWorld
	reviewer string
}

func newStrandWorld(t *testing.T) strandWorld {
	t.Helper()
	w := newStaffAccWorld(t)
	w.setKind(t, "in_house")
	r := lsReviewer(t, w.invWorld, "Rita Reviewer", nil)
	seedRoleStaffedBy(t, w.super, w.tenant, strandRole, r)
	return strandWorld{staffAccWorld: w, reviewer: r}
}

func (w strandWorld) self(o AccountDeletionOutcome, heir string) (StrandedAlert, error) {
	return w.store.DeleteMyAccount(w.as(w.reviewer), o, heir)
}

func (w strandWorld) staff() (StrandedAlert, error) {
	return w.store.StaffDeleteAccount(w.ctx, w.tenant, w.reviewer, OutcomeLeave, "", staffAccReason)
}

func (w strandWorld) number(t *testing.T, invoice string) string {
	t.Helper()
	var n string
	if err := w.super.QueryRow(context.Background(), `SELECT invoice_number FROM invoices WHERE id = $1`, invoice).Scan(&n); err != nil {
		t.Fatalf("read invoice number: %v", err)
	}
	return n
}

func (w strandWorld) ref(t *testing.T, invoice string) approval.InvoiceRef {
	return approval.InvoiceRef{ID: invoice, Number: w.number(t, invoice), RoleKey: strandRole, RoleTitle: strandRole}
}

func (w strandWorld) recipient(user string) adminRecipient {
	return adminRecipient{id: user, email: user + "@members.test"}
}

func (w strandWorld) requireDeleted(t *testing.T) {
	t.Helper()
	if st := imageOf(t, w.super, w.tenant, w.reviewer).Status; st != "removed" {
		t.Errorf("reviewer status = %q, want removed", st)
	}
	if n := w.eraseJobs(t, w.reviewer); n != 1 {
		t.Errorf("person_erase jobs for the reviewer = %d, want 1", n)
	}
}

func (w strandWorld) requireAlert(t *testing.T, a StrandedAlert, invoices ...approval.InvoiceRef) {
	t.Helper()
	if a.err != nil {
		t.Errorf("alert.err = %v, want nil", a.err)
	}
	if a.TenantID != w.tenant {
		t.Errorf("alert.TenantID = %q, want %q", a.TenantID, w.tenant)
	}
	if a.Workspace != staffAccTenant {
		t.Errorf("alert.Workspace = %q, want %q", a.Workspace, staffAccTenant)
	}
	if !reflect.DeepEqual(a.Stranded.Invoices, invoices) || a.Stranded.Total != len(invoices) {
		t.Errorf("alert.Stranded = %+v, want invoices %+v and total %d", a.Stranded, invoices, len(invoices))
	}
}

func requireEmptyAlert(t *testing.T, a StrandedAlert) {
	t.Helper()
	if !reflect.DeepEqual(a, StrandedAlert{TenantID: a.TenantID}) {
		t.Errorf("alert = %+v, want empty", a)
	}
}

// deletionPayloadKeys reads the keys of the one membership.account_deleted row each path writes.
func (w strandWorld) deletionPayloadKeys(t *testing.T, path string) []string {
	t.Helper()
	if path == "staff" {
		for _, r := range staffEvents(t, w.super, w.tenant) {
			if r.Event == evAccountDeleted {
				return payloadKeys(r.Payload)
			}
		}
		t.Fatal("no staff membership.account_deleted row")
	}
	rows := w.auditRows(t, evAccountDeleted)
	if len(rows) != 1 {
		t.Fatalf("%s rows = %d, want 1", evAccountDeleted, len(rows))
	}
	return payloadKeys(rows[0].Payload)
}

func wantKeys(path string) []string {
	if path == "staff" {
		return []string{"reason", "role", "user_id"}
	}
	return []string{"role", "user_id"}
}

// sqlTracer cancels the one statement whose text contains cancel and records every statement.
type sqlTracer struct {
	cancel string
	mu     *sync.Mutex
	seen   *[]string
}

func (c sqlTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	*c.seen = append(*c.seen, d.SQL)
	c.mu.Unlock()
	if c.cancel == "" || !strings.Contains(d.SQL, c.cancel) {
		return ctx
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	return dead
}

func (sqlTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// tracedStore is w's store on a pool that fails the statements containing cancel; seen lists every statement.
func (w strandWorld) tracedStore(t *testing.T, cancel string) (st *Store, seen func() []string) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	var mu sync.Mutex
	var stmts []string
	cfg.ConnConfig.Tracer = sqlTracer{cancel: cancel, mu: &mu, seen: &stmts}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced pool: %v", err)
	}
	t.Cleanup(p.Close)
	st = NewStore(p)
	st.eraseQueue = w.store.eraseQueue
	return st, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(stmts)
	}
}

func TestStrandedAlert_SelfDeletionOfTheLastSignerAlertsTheAdmins(t *testing.T) {
	w := newStrandWorld(t)
	inv, _ := seedWaitingRun(t, w.super, w.tenant, strandRole)

	a, err := w.self(OutcomeLeave, "")
	if err != nil {
		t.Fatalf("DeleteMyAccount: %v", err)
	}
	w.requireDeleted(t)
	w.requireAlert(t, a, w.ref(t, inv))
	if !reflect.DeepEqual(a.Admins, []adminRecipient{w.recipient(w.admin)}) {
		t.Errorf("alert.Admins = %+v, want [admin A]", a.Admins)
	}
}

func TestStrandedAlert_StaffDeletionOfTheLastSignerAlertsTheAdmins(t *testing.T) {
	w := newStrandWorld(t)
	inv, _ := seedWaitingRun(t, w.super, w.tenant, strandRole)

	a, err := w.staff()
	if err != nil {
		t.Fatalf("StaffDeleteAccount: %v", err)
	}
	w.requireDeleted(t)
	w.requireAlert(t, a, w.ref(t, inv))
	if !reflect.DeepEqual(a.Admins, []adminRecipient{w.recipient(w.admin)}) {
		t.Errorf("alert.Admins = %+v, want [admin A]", a.Admins)
	}
}

func TestStrandedAlert_TheDeletionRowKeepsTodaysKeys(t *testing.T) {
	for _, path := range []string{"self", "staff"} {
		t.Run(path, func(t *testing.T) {
			w := newStrandWorld(t)
			seedWaitingRun(t, w.super, w.tenant, strandRole)
			var a StrandedAlert
			var err error
			if path == "self" {
				a, err = w.self(OutcomeLeave, "")
			} else {
				a, err = w.staff()
			}
			if err != nil || a.Stranded.Total != 1 {
				t.Fatalf("deletion = (total %d, %v), want it to strand one invoice", a.Stranded.Total, err)
			}
			if got := w.deletionPayloadKeys(t, path); !slices.Equal(got, wantKeys(path)) {
				t.Errorf("payload keys = %v, want %v", got, wantKeys(path))
			}
		})
	}
}

func TestStrandedAlert_AnotherSignerLeftSendsNothing(t *testing.T) {
	for _, path := range []string{"self", "staff"} {
		t.Run(path, func(t *testing.T) {
			w := newStrandWorld(t)
			r2 := lsReviewer(t, w.invWorld, "Rosa Reviewer", nil)
			seedRoleStaffedBy(t, w.super, w.tenant, "k2", w.reviewer, r2)
			seedWaitingRun(t, w.super, w.tenant, "k2")
			var a StrandedAlert
			var err error
			if path == "self" {
				a, err = w.self(OutcomeLeave, "")
			} else {
				a, err = w.staff()
			}
			if err != nil {
				t.Fatal(err)
			}
			requireEmptyAlert(t, a)
			if got := w.deletionPayloadKeys(t, path); !slices.Equal(got, wantKeys(path)) {
				t.Errorf("payload keys = %v, want %v", got, wantKeys(path))
			}
		})
	}
}

func TestStrandedAlert_NoWaitingRunSendsNothing(t *testing.T) {
	w := newStrandWorld(t)
	st, seen := w.tracedStore(t, "")

	a, err := st.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyAlert(t, a)
	for _, sql := range seen() {
		if strings.Contains(sql, "FROM tenants") {
			t.Errorf("a deletion that strands nothing read the workspace: %s", sql)
		}
	}
}

func TestStrandedAlert_ListsTheRolesOtherStrandedInvoices(t *testing.T) {
	w := newStrandWorld(t)
	x, _ := seedWaitingRun(t, w.super, w.tenant, strandRole)
	y, _ := seedWaitingRunOn(t, w.super, w.tenant, strandRole, seedEntity(t, w.super, w.tenant), w.reviewer)

	a, err := w.self(OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	w.requireAlert(t, a, w.ref(t, x), w.ref(t, y))
}

func TestStrandedAlert_AMemberOfAnAlreadyStrandedRoleSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		inRole     bool
	}{{"a preparer in the role", "preparer", true}, {"a reviewer in no role", "reviewer", false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newStrandWorld(t)
			m := w.member(t, tc.role, "active", "Pat Member", joined(1))
			if tc.inRole {
				seedRoleStaffedBy(t, w.super, w.tenant, "k3", m)
			} else {
				seedRoleStaffedBy(t, w.super, w.tenant, "k3")
			}
			seedWaitingRun(t, w.super, w.tenant, "k3")

			a, err := w.store.DeleteMyAccount(w.as(m), OutcomeLeave, "")
			if err != nil {
				t.Fatal(err)
			}
			requireEmptyAlert(t, a)
		})
	}
}

func TestStrandedAlert_ANamedDelegateIsJudged(t *testing.T) {
	w := newStrandWorld(t)
	seedRoleStaffedBy(t, w.super, w.tenant, "k4")
	d := lsReviewer(t, w.invWorld, "Dara Delegate", nil)
	inv, run := seedWaitingRun(t, w.super, w.tenant, "k4")
	lsDelegate(t, w.super, run, true, &d)

	a, err := w.store.DeleteMyAccount(w.as(d), OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	want := approval.InvoiceRef{ID: inv, Number: w.number(t, inv), RoleKey: "k4", RoleTitle: "k4"}
	if !reflect.DeepEqual(a.Stranded.Invoices, []approval.InvoiceRef{want}) || a.Stranded.Total != 1 {
		t.Errorf("alert.Stranded = %+v, want the delegate's invoice on k4", a.Stranded)
	}
}

func TestStrandedAlert_ALimitedReviewerIsJudgedInReach(t *testing.T) {
	w := newStaffAccWorld(t) // a firm: a limited reviewer reaches only its clients
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	r := lsReviewer(t, w.invWorld, "Rita Reviewer", []string{e1})
	seedRoleStaffedBy(t, w.super, w.tenant, strandRole, r)
	in, _ := seedWaitingRunOn(t, w.super, w.tenant, strandRole, e1, "")
	seedWaitingRunOn(t, w.super, w.tenant, strandRole, e2, "")
	sw := strandWorld{staffAccWorld: w, reviewer: r}

	a, err := sw.self(OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	sw.requireAlert(t, a, sw.ref(t, in))
}

func TestStrandedAlert_AStillSignableStepDoesNotAlertForAnOlderStrandedInvoice(t *testing.T) {
	w := newStaffAccWorld(t) // a firm
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	r := lsReviewer(t, w.invWorld, "Rita Reviewer", []string{e1})
	r2 := lsReviewer(t, w.invWorld, "Rosa Reviewer", []string{e1})
	seedRoleStaffedBy(t, w.super, w.tenant, strandRole, r, r2)
	seedWaitingRunOn(t, w.super, w.tenant, strandRole, e1, "")
	seedWaitingRunOn(t, w.super, w.tenant, strandRole, e2, "") // nobody in the role reaches e2
	sw := strandWorld{staffAccWorld: w, reviewer: r}

	a, err := sw.staff() // staff reads every client, so the older invoice is in the judgement's sight
	if err != nil {
		t.Fatal(err)
	}
	sw.requireDeleted(t)
	requireEmptyAlert(t, a)
}

func TestStrandedAlert_RecipientsAfterAHandOn(t *testing.T) {
	w := newStaffAccWorld(t)
	w.setKind(t, "in_house")
	h := w.member(t, "preparer", "active", "Hana Heir", joined(1))
	seedRoleStaffedBy(t, w.super, w.tenant, strandRole, w.admin)
	seedWaitingRun(t, w.super, w.tenant, strandRole)

	a, err := w.store.DeleteMyAccount(w.adminCtx(), OutcomeHandOn, h)
	if err != nil {
		t.Fatal(err)
	}
	sw := strandWorld{staffAccWorld: w}
	if a.Stranded.Total != 1 || !reflect.DeepEqual(a.Admins, []adminRecipient{sw.recipient(h)}) {
		t.Errorf("alert = %+v, want one stranded invoice and the heir as the only recipient", a)
	}
}

func TestStrandedAlert_AnAdminWithNoEmailIsSkipped(t *testing.T) {
	w := newStrandWorld(t)
	a2 := w.addMember(t, "admin", "active", "Bola Ade")
	a3 := w.addMember(t, "admin", "active", "Chidi Eze")
	clearEmail(t, w.super, a2)
	seedWaitingRun(t, w.super, w.tenant, strandRole)

	a, err := w.self(OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{w.admin, a3}
	slices.Sort(ids)
	want := []adminRecipient{w.recipient(ids[0]), w.recipient(ids[1])}
	if !reflect.DeepEqual(a.Admins, want) {
		t.Errorf("alert.Admins = %+v, want %+v (the admin with no email skipped, user-id order)", a.Admins, want)
	}
}

func TestStrandedAlert_NoAdminWithAnEmailLeavesNoRecipient(t *testing.T) {
	w := newStrandWorld(t)
	clearEmail(t, w.super, w.admin)
	seedWaitingRun(t, w.super, w.tenant, strandRole)

	a, err := w.self(OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	w.requireDeleted(t)
	if a.Stranded.Total != 1 || len(a.Admins) != 0 {
		t.Errorf("alert = %+v, want one stranded invoice and no recipient", a)
	}
}

func TestStrandedAlert_AFailedBeforeReadNeverBlocksTheDeletion(t *testing.T) {
	requireFailedReadKeepsTheDeletion(t, "-- SignedWaitingStepsTx")
}

// afterReads are statements only the judgement after endMembership runs; the unsignableReasonsTx read also runs before it.
var afterReads = []string{"SELECT title FROM workflow_roles", "SELECT name FROM tenants"}

func TestStrandedAlert_AFailedAfterReadNeverBlocksTheDeletion(t *testing.T) {
	for _, cancel := range afterReads {
		t.Run(cancel, func(t *testing.T) { requireFailedReadKeepsTheDeletion(t, cancel) })
	}
}

// requireFailedReadKeepsTheDeletion fails the statements matching cancel, then deletes on each path.
func requireFailedReadKeepsTheDeletion(t *testing.T, cancel string) {
	t.Helper()
	for _, path := range []string{"self", "staff"} {
		t.Run(path, func(t *testing.T) {
			w := newStrandWorld(t)
			seedWaitingRun(t, w.super, w.tenant, strandRole)
			st, _ := w.tracedStore(t, cancel)
			var a StrandedAlert
			var err error
			if path == "self" {
				a, err = st.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, "")
			} else {
				a, err = st.StaffDeleteAccount(w.ctx, w.tenant, w.reviewer, OutcomeLeave, "", staffAccReason)
			}
			if err != nil {
				t.Fatalf("%s deletion: %v, want the deletion to commit", path, err)
			}
			w.requireDeleted(t)
			if a.Stranded.Total != 0 || a.err == nil {
				t.Errorf("alert = %+v, want an empty alert with err set", a)
			}
			if got := w.deletionPayloadKeys(t, path); !slices.Equal(got, wantKeys(path)) {
				t.Errorf("payload keys = %v, want %v", got, wantKeys(path))
			}
		})
	}
}

func TestStrandedAlert_AJudgementErrorIsLogged(t *testing.T) {
	w := newStrandWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	st, _ := w.tracedStore(t, afterReads[1])
	logs := &syncBuf{}
	n := &DeletionNotifier{Store: st, Sender: &accountmail.Capture{}, Logger: slog.New(slog.NewTextHandler(logs, nil))}

	if err := n.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, ""); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	if !strings.Contains(logs.String(), "tenancy: stranded check failed") {
		t.Errorf("log lacks the judgement failure line:\n%s", logs.String())
	}
	assertNoPII(t, logs.String(), "Rita Reviewer", w.reviewer+"@members.test", w.admin+"@members.test")
}

func TestStrandedAlert_ARolledBackDeletionReturnsNoAlert(t *testing.T) {
	w := newStrandWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	failAudit(t, w.super, w.tenant, evAccountDeleted)

	a, err := w.self(OutcomeLeave, "")
	if pgCode(err) != forcedFailCode || !reflect.DeepEqual(a, StrandedAlert{}) {
		t.Errorf("self = (%+v, %v), want the zero alert and code %s", a, err, forcedFailCode)
	}
	a, err = w.staff()
	if pgCode(err) != forcedFailCode || !reflect.DeepEqual(a, StrandedAlert{}) {
		t.Errorf("staff = (%+v, %v), want the zero alert and code %s", a, err, forcedFailCode)
	}
	if st := imageOf(t, w.super, w.tenant, w.reviewer).Status; st != "active" {
		t.Errorf("reviewer status = %q, want active", st)
	}
}

func TestStrandedAlert_AnotherTenantsRunDoesNotAlert(t *testing.T) {
	w := newStrandWorld(t)
	other := seedTenant(t, w.super, "APSC-16 other tenant")
	rb := uuid.NewString()
	seedIdentityMembership(t, w.super, other, rb, "reviewer", "active", strp("Rob Other"), strp(rb+"@members.test"))
	seedRoleStaffedBy(t, w.super, other, strandRole, rb)
	seedWaitingRun(t, w.super, other, strandRole)

	a, err := w.self(OutcomeLeave, "")
	if err != nil {
		t.Fatal(err)
	}
	requireEmptyAlert(t, a)
}
