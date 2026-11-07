package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	evStrandedAlert = "membership.stranded_alert"
	alertRoleTitle  = "Head of Finance"
	alertReviewer   = "Zainab Bello"
)

// alertWorld is a strandWorld whose role K has a title unlike its key and a second active admin.
type alertWorld struct {
	strandWorld
	admin2 string
}

func newAlertWorld(t *testing.T) alertWorld {
	t.Helper()
	w := newStaffAccWorld(t)
	w.setKind(t, "in_house")
	r := lsReviewer(t, w.invWorld, alertReviewer, nil)
	seedRoleStaffedBy(t, w.super, w.tenant, strandRole, r)
	if _, err := w.super.Exec(context.Background(),
		`UPDATE workflow_roles SET title = $3 WHERE tenant_id = $1 AND key = $2`, w.tenant, strandRole, alertRoleTitle); err != nil {
		t.Fatalf("title the role: %v", err)
	}
	a2 := w.addMember(t, "admin", "active", "Bola Ade")
	return alertWorld{strandWorld: strandWorld{staffAccWorld: w, reviewer: r}, admin2: a2}
}

// soleSigner adds a reviewer who alone signs a new role, and a waiting run on it.
func (w alertWorld) soleSigner(t *testing.T, role string) (user, invoice string) {
	t.Helper()
	user = lsReviewer(t, w.invWorld, "Sole "+role, nil)
	seedRoleStaffedBy(t, w.super, w.tenant, role, user)
	invoice, _ = seedWaitingRun(t, w.super, w.tenant, role)
	return user, invoice
}

type alertRow struct {
	Actor      string
	EntityNull bool
	Payload    map[string]any
}

func alertRows(t *testing.T, super *pgxpool.Pool, tenant string) []alertRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, entity_id IS NULL, payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenant, evStrandedAlert)
	if err != nil {
		t.Fatalf("read alert rows: %v", err)
	}
	defer rows.Close()
	var out []alertRow
	for rows.Next() {
		var r alertRow
		var raw []byte
		if err := rows.Scan(&r.Actor, &r.EntityNull, &raw); err != nil {
			t.Fatalf("scan alert row: %v", err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatalf("decode alert payload %s: %v", raw, err)
		}
		out = append(out, r)
	}
	return out
}

func requireAlertRows(t *testing.T, super *pgxpool.Pool, tenant string, want int) []alertRow {
	t.Helper()
	rows := alertRows(t, super, tenant)
	if len(rows) != want {
		t.Fatalf("%s rows in tenant %s = %d, want %d", evStrandedAlert, tenant, len(rows), want)
	}
	return rows
}

func requireMessages(t *testing.T, got []accountmail.Message, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("messages = %d, want %d", len(got), want)
	}
}

func (w alertWorld) selfDelete(n *DeletionNotifier, user string) error {
	return n.DeleteMyAccount(w.as(user), OutcomeLeave, "")
}

func (w alertWorld) staffDelete(n *DeletionNotifier, user string) error {
	return n.StaffDeleteAccount(w.ctx, w.tenant, user, OutcomeLeave, "", staffAccReason)
}

func TestStrandedAlertMail_EachActiveAdminGetsOneMail(t *testing.T) {
	w := newAlertWorld(t)
	inv, _ := seedWaitingRun(t, w.super, w.tenant, strandRole)
	capture := &accountmail.Capture{}
	n, _ := notifierFor(w.invWorld, capture)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	msgs := capture.Messages()
	requireMessages(t, msgs, 2)
	number := w.number(t, inv)
	for _, a := range []string{w.admin, w.admin2} {
		m := messageTo(t, msgs, emailOf(a))
		if m.Subject != accountmail.StrandedAlertSubject {
			t.Errorf("subject to %s = %q, want %q", m.To, m.Subject, accountmail.StrandedAlertSubject)
		}
		for _, want := range []string{alertRoleTitle, number} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("mail to %s lacks %q", m.To, want)
			}
		}
	}
}

func TestStrandedAlertMail_StaffDeletionMailsTheAdmins(t *testing.T) {
	w := newAlertWorld(t)
	inv, _ := seedWaitingRun(t, w.super, w.tenant, strandRole)
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.staffDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.StaffDeleteAccount: %v", err)
	}
	msgs := s.messages()
	requireMessages(t, msgs, 2)
	for _, a := range []string{w.admin, w.admin2} {
		m := messageTo(t, msgs, emailOf(a))
		if m.Subject != accountmail.StrandedAlertSubject || !strings.Contains(m.HTML, w.number(t, inv)) {
			t.Errorf("mail to %s = subject %q, html without the invoice number", m.To, m.Subject)
		}
	}
}

func TestStrandedAlertMail_TheAlertRowRecordsTheSend(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	var inside atomic.Int64
	inside.Store(-1)
	s := &apiSender{fn: func(context.Context, []accountmail.Message) error {
		var c int64
		if err := w.super.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, w.tenant, evStrandedAlert).Scan(&c); err != nil {
			t.Errorf("count alert rows inside Send: %v", err)
		}
		inside.Store(c)
		return nil
	}}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	if got := inside.Load(); got != 0 {
		t.Errorf("alert rows inside Send = %d, want 0 (the row is written after Send returns)", got)
	}
	rows := requireAlertRows(t, w.super, w.tenant, 1)
	if rows[0].Actor != "system" || !rows[0].EntityNull {
		t.Errorf("alert row = actor %q, entity NULL %v, want system and NULL", rows[0].Actor, rows[0].EntityNull)
	}
	want := map[string]any{"stranded_role_keys": []any{strandRole}, "stranded_invoices_total": float64(1), "admins": float64(2), "delivery": "sent"}
	if !reflect.DeepEqual(rows[0].Payload, want) {
		t.Errorf("alert payload = %v, want %v", rows[0].Payload, want)
	}
	if got := w.deletionPayloadKeys(t, "self"); !reflect.DeepEqual(got, wantKeys("self")) {
		t.Errorf("deletion row keys = %v, want %v", got, wantKeys("self"))
	}
}

func TestStrandedAlertMail_TheAlertRowLandsInTheDeletionsTenant(t *testing.T) {
	w := newAlertWorld(t)
	other := seedTenant(t, w.super, "APSC-16-04 tenant B")
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	r2, _ := w.soleSigner(t, "k2")
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.staffDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.StaffDeleteAccount: %v", err)
	}
	requireMessages(t, s.messages(), 2)
	requireAlertRows(t, w.super, w.tenant, 1)
	requireAlertRows(t, w.super, other, 0)
	requireAlertRows(t, w.super, w.houseID, 0)

	if err := w.selfDelete(n, r2); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	requireAlertRows(t, w.super, w.tenant, 2)
	requireAlertRows(t, w.super, other, 0)
	requireAlertRows(t, w.super, w.houseID, 0)

	// The app role sees the rows only from the deletion's tenant.
	seen := func(tenant string) (c int) {
		err := db.WithinTenantTx(context.Background(), w.app, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE event = $1`, evStrandedAlert).Scan(&c)
		})
		if err != nil {
			t.Fatalf("read alert rows as tenant %s: %v", tenant, err)
		}
		return c
	}
	if a, b := seen(w.tenant), seen(other); a != 2 || b != 0 {
		t.Errorf("alert rows seen from the deletion's tenant = %d (want 2), from another tenant = %d (want 0)", a, b)
	}
}

func TestStrandedAlertMail_NothingStrandedSendsNothing(t *testing.T) {
	w := newAlertWorld(t)
	r2 := lsReviewer(t, w.invWorld, "Rosa Reviewer", nil)
	seedRoleStaffedBy(t, w.super, w.tenant, "k2", w.reviewer, r2)
	seedWaitingRun(t, w.super, w.tenant, "k2")
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	w.requireDeleted(t)
	if len(s.sent()) != 0 {
		t.Errorf("Send called %d times for a deletion that strands nothing", len(s.sent()))
	}
	requireAlertRows(t, w.super, w.tenant, 0)

	// Control: the same world alerts once R2 is the last signer.
	if err := w.selfDelete(n, r2); err != nil {
		t.Fatalf("notifier.DeleteMyAccount (last signer): %v", err)
	}
	requireMessages(t, s.messages(), 2)
	requireAlertRows(t, w.super, w.tenant, 1)
}

func TestStrandedAlertMail_NoRecipientSendsNothing(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	r2, _ := w.soleSigner(t, "k2")
	clearEmail(t, w.super, w.admin)
	clearEmail(t, w.super, w.admin2)
	s := &apiSender{}
	n, logs := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	w.requireDeleted(t)
	if len(s.sent()) != 0 {
		t.Errorf("Send called %d times with no recipient", len(s.sent()))
	}
	requireAlertRows(t, w.super, w.tenant, 0)
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no recipient is not an error, log:\n%s", logs.String())
	}

	// Control: with an address back, the next stranding deletion alerts.
	if _, err := w.super.Exec(context.Background(), `UPDATE memberships SET email = $2 WHERE user_id = $1`, w.admin, emailOf(w.admin)); err != nil {
		t.Fatalf("restore the admin email: %v", err)
	}
	if err := w.selfDelete(n, r2); err != nil {
		t.Fatalf("notifier.DeleteMyAccount (control): %v", err)
	}
	requireMessages(t, s.messages(), 1)
	requireAlertRows(t, w.super, w.tenant, 1)
}

func TestStrandedAlertMail_AJudgementErrorSendsNothing(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	r2, _ := w.soleSigner(t, "k2")
	s := &apiSender{}
	st, _ := w.tracedStore(t, "-- unsignableReasonsTx")
	broken := &DeletionNotifier{Store: st, Sender: s, Logger: slog.New(slog.NewTextHandler(&syncBuf{}, nil))}

	if err := broken.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, ""); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	w.requireDeleted(t)
	if len(s.sent()) != 0 {
		t.Errorf("Send called %d times after a failed judgement", len(s.sent()))
	}
	requireAlertRows(t, w.super, w.tenant, 0)

	// Control: the same world on a healthy store alerts.
	n, _ := notifierFor(w.invWorld, s)
	if err := w.selfDelete(n, r2); err != nil {
		t.Fatalf("notifier.DeleteMyAccount (control): %v", err)
	}
	requireMessages(t, s.messages(), 2)
	requireAlertRows(t, w.super, w.tenant, 1)
}

func TestStrandedAlertMail_AStoreErrorSendsNothing(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)
	drop := failAudit(t, w.super, w.tenant, evAccountDeleted)

	err := w.selfDelete(n, w.reviewer)
	if pgCode(err) != forcedFailCode {
		t.Fatalf("notifier.DeleteMyAccount err = %v, want the store's error with code %s", err, forcedFailCode)
	}
	if len(s.sent()) != 0 {
		t.Errorf("Send called %d times after a rolled-back deletion", len(s.sent()))
	}
	requireAlertRows(t, w.super, w.tenant, 0)

	// Control: once the store succeeds the same call alerts.
	drop()
	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount (control): %v", err)
	}
	requireMessages(t, s.messages(), 2)
	requireAlertRows(t, w.super, w.tenant, 1)
}

func TestStrandedAlertMail_NamesNoPerson(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	msgs := s.messages()
	requireMessages(t, msgs, 2)
	rows := requireAlertRows(t, w.super, w.tenant, 1)
	payload, _ := json.Marshal(rows[0].Payload)
	for _, secret := range []string{alertReviewer, emailOf(w.reviewer)} {
		for _, m := range msgs {
			if strings.Contains(m.HTML, secret) {
				t.Errorf("mail to %s names %q", m.To, secret)
			}
		}
		if strings.Contains(string(payload), secret) {
			t.Errorf("alert payload %s names %q", payload, secret)
		}
	}
}

func TestStrandedAlertMail_AFailedSendKeepsTheDeletion(t *testing.T) {
	w := newStrandWorld(t) // one admin
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	s := &apiSender{fn: func(context.Context, []accountmail.Message) error { return &accountmail.SendError{Status: 500} }}
	n, logs := notifierFor(w.invWorld, s)

	if err := n.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, ""); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v, want nil after a failed send", err)
	}
	w.requireDeleted(t)
	for _, want := range []string{"tenancy: stranded alert failed", "count=1", "status=500"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
	rows := requireAlertRows(t, w.super, w.tenant, 1)
	if rows[0].Payload["delivery"] != "failed" || rows[0].Payload["admins"] != float64(1) {
		t.Errorf("alert payload = %v, want delivery failed and admins 1", rows[0].Payload)
	}
	assertNoPII(t, logs.String(), emailOf(w.admin), "Rita Reviewer", emailOf(w.reviewer))
}

func TestStrandedAlertMail_AFailedRowKeepsTheDeletion(t *testing.T) {
	w := newStrandWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	s := &apiSender{}
	n, logs := notifierFor(w.invWorld, s)
	failAudit(t, w.super, w.tenant, evStrandedAlert)

	if err := n.DeleteMyAccount(w.as(w.reviewer), OutcomeLeave, ""); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v, want nil after a failed row write", err)
	}
	w.requireDeleted(t)
	requireMessages(t, s.messages(), 1)
	if !strings.Contains(logs.String(), "tenancy: stranded alert outcome not recorded") {
		t.Errorf("log lacks the row-write failure line:\n%s", logs.String())
	}
	assertNoPII(t, logs.String(), emailOf(w.admin), "Rita Reviewer", emailOf(w.reviewer))
}

// cancelAfterCommit cancels a context once the first COMMIT it sees has finished.
type cancelAfterCommit struct {
	cancel context.CancelFunc
	hit    *atomic.Bool
}

func (c cancelAfterCommit) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(d.SQL), "commit") {
		c.hit.Store(true)
	}
	return ctx
}

func (c cancelAfterCommit) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
	if c.hit.Load() {
		c.cancel()
	}
}

func TestStrandedAlertMail_ACancelledClientStillSends(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	ctx, cancel := context.WithCancel(w.as(w.reviewer))
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = cancelAfterCommit{cancel: cancel, hit: &atomic.Bool{}}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced pool: %v", err)
	}
	t.Cleanup(p.Close)
	st := NewStore(p)
	st.eraseQueue = w.store.eraseQueue
	var live atomic.Bool
	live.Store(true)
	s := &apiSender{fn: func(sctx context.Context, _ []accountmail.Message) error {
		live.Store(sctx.Err() == nil)
		return nil
	}}
	n := &DeletionNotifier{Store: st, Sender: s, Logger: slog.New(slog.NewTextHandler(&syncBuf{}, nil))}

	if err := n.DeleteMyAccount(ctx, OutcomeLeave, ""); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("fixture: the client context was never cancelled after the commit")
	}
	requireMessages(t, s.messages(), 2)
	if !live.Load() {
		t.Error("Send ran on the cancelled client context")
	}
	requireAlertRows(t, w.super, w.tenant, 1)
}

func TestStrandedAlertMail_ListsTwentyAndCountsTheRest(t *testing.T) {
	w := newAlertWorld(t)
	entity := seedEntity(t, w.super, w.tenant)
	for range 25 {
		seedWaitingRunOn(t, w.super, w.tenant, strandRole, entity, "")
	}
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	msgs := s.messages()
	requireMessages(t, msgs, 2)
	for _, m := range msgs {
		if got := strings.Count(m.HTML, "INV-"); got != 20 {
			t.Errorf("mail to %s lists %d invoices, want 20", m.To, got)
		}
		if !strings.Contains(m.HTML, "And 5 more invoices") {
			t.Errorf("mail to %s lacks %q", m.To, "And 5 more invoices")
		}
	}
	rows := requireAlertRows(t, w.super, w.tenant, 1)
	if rows[0].Payload["stranded_invoices_total"] != float64(25) {
		t.Errorf("stranded_invoices_total = %v, want 25", rows[0].Payload["stranded_invoices_total"])
	}
}

func TestStrandedAlertMail_ARepeatedDeletionMailsOnce(t *testing.T) {
	w := newAlertWorld(t)
	seedWaitingRun(t, w.super, w.tenant, strandRole)
	b, _ := w.soleSigner(t, "k2")
	s := &apiSender{}
	n, _ := notifierFor(w.invWorld, s)

	if err := w.selfDelete(n, w.reviewer); err != nil {
		t.Fatalf("notifier.DeleteMyAccount: %v", err)
	}
	requireMessages(t, s.messages(), 2)
	if err := w.selfDelete(n, w.reviewer); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("second self-deletion err = %v, want %v", err, db.ErrNotActiveMember)
	}
	requireMessages(t, s.messages(), 2)
	requireAlertRows(t, w.super, w.tenant, 1)

	if err := w.staffDelete(n, b); err != nil {
		t.Fatalf("notifier.StaffDeleteAccount: %v", err)
	}
	requireMessages(t, s.messages(), 4)
	requireAlertRows(t, w.super, w.tenant, 2)
	if err := w.staffDelete(n, b); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("second staff deletion err = %v, want %v", err, ErrMembershipNotFound)
	}
	requireMessages(t, s.messages(), 4)
	requireAlertRows(t, w.super, w.tenant, 2)
}
