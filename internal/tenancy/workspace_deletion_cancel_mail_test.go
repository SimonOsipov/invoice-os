package tenancy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type cancelMailRow struct {
	Recipient, Status string
	CancelledDue      time.Time
}

func cancelMailRows(t *testing.T, super *pgxpool.Pool, tenant string) []cancelMailRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT recipient_user_id::text, send_status, cancelled_due_at
		   FROM workspace_deletion_cancel_mails WHERE tenant_id = $1 ORDER BY recipient_user_id`, tenant)
	if err != nil {
		t.Fatalf("read the cancel-mail rows: %v", err)
	}
	defer rows.Close()
	var out []cancelMailRow
	for rows.Next() {
		var r cancelMailRow
		if err := rows.Scan(&r.Recipient, &r.Status, &r.CancelledDue); err != nil {
			t.Fatalf("scan a cancel-mail row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the cancel-mail rows: %v", err)
	}
	return out
}

// sortedRecipients is the rows' recipients, sorted: user ids are random, so order proves nothing.
func sortedRecipients(rows []cancelMailRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Recipient)
	}
	slices.Sort(out)
	return out
}

func sortedUsers(users ...string) []string {
	out := slices.Clone(users)
	slices.Sort(out)
	return out
}

func sortedAddrs(msgs []accountmail.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.To)
	}
	slices.Sort(out)
	return out
}

func sortedEmails(users ...string) []string {
	var out []string
	for _, u := range users {
		out = append(out, emailOf(u))
	}
	slices.Sort(out)
	return out
}

func requireOneBatchTo(t *testing.T, sender *apiSender, want []string) []accountmail.Message {
	t.Helper()
	calls := sender.sent()
	if len(calls) != 1 {
		t.Fatalf("Send calls = %d, want 1 batch", len(calls))
	}
	if got := sortedAddrs(calls[0]); !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want exactly %v", got, want)
	}
	return calls[0]
}

func TestCancelMail_EveryOtherActiveAdminIsTold(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	c := w.addMember(t, "admin", "active", "Chidi Eze")
	s := w.addMember(t, "admin", "suspended", "Sade Eze")
	r := w.addMember(t, "admin", "removed", "Rita Eze")
	p := w.addMember(t, "preparer", "active", "Pat Eze")
	nul := w.addMember(t, "admin", "active", "Nia Eze")
	clearEmail(t, w.super, nul)
	lockByOwner(t, w.super, w.tenant, w.admin)
	old := deletionOf(t, w.super, w.tenant)
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	if err := n.Cancel(w.as(b)); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	msgs := requireOneBatchTo(t, sender, sortedEmails(w.admin, c))
	for _, m := range msgs {
		if m.Subject != accountmail.DeletionCancelledSubject {
			t.Errorf("subject to %s = %q, want %q", m.To, m.Subject, accountmail.DeletionCancelledSubject)
		}
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if got, want := sortedRecipients(rows), sortedUsers(w.admin, c); !slices.Equal(got, want) {
		t.Fatalf("cancel-mail recipients = %v, want %v (not the canceller %s, %s, %s, %s or %s)", got, want, b, s, r, p, nul)
	}
	for _, row := range rows {
		if row.Status != "sent" {
			t.Errorf("send_status for %s = %q, want sent", row.Recipient, row.Status)
		}
		if !row.CancelledDue.Equal(*old.DueAt) {
			t.Errorf("cancelled_due_at for %s = %v, want the cancelled due %v", row.Recipient, row.CancelledDue, old.DueAt)
		}
	}
	if got := auditCount(t, w.app, w.tenant, evCancelled); got != 1 {
		t.Errorf("%s rows = %d, want 1", evCancelled, got)
	}
}

func TestCancelMail_ALinkCancelMailsToo(t *testing.T) {
	w := newLinkWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if err := n.CancelByLink(w.as(w.c), w.tokB); err != nil {
		t.Fatalf("CancelByLink: %v", err)
	}

	msgs := requireOneBatchTo(t, sender, sortedEmails(w.admin, w.b))
	for _, m := range msgs {
		if m.Subject != accountmail.DeletionCancelledSubject {
			t.Errorf("subject to %s = %q, want %q", m.To, m.Subject, accountmail.DeletionCancelledSubject)
		}
		if strings.Contains(m.HTML, w.tokB) || strings.Contains(m.HTML, w.tokC) {
			t.Errorf("cancel mail to %s holds a deletion-link token", m.To)
		}
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if got, want := sortedRecipients(rows), sortedUsers(w.admin, w.b); !slices.Equal(got, want) {
		t.Errorf("cancel-mail recipients = %v, want %v", got, want)
	}
	for _, row := range rows {
		if row.Status != "sent" {
			t.Errorf("send_status for %s = %q, want sent", row.Recipient, row.Status)
		}
	}
}

func TestCancelMail_MailNamesWorkspaceCancellerAndDate(t *testing.T) {
	check := func(t *testing.T, w invWorld, canceller, wantName string) {
		w.addMember(t, "admin", "active", "Chidi Eze")
		lockByOwner(t, w.super, w.tenant, w.admin)
		due := deletionOf(t, w.super, w.tenant).DueAt
		sender := &apiSender{}
		n, _ := notifierFor(w, sender)

		if err := n.Cancel(w.as(canceller)); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		msgs := sender.messages()
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2", len(msgs))
		}
		wantDate := due.UTC().Format("2 January 2006") + " (UTC)"
		for _, m := range msgs {
			for _, want := range []string{obi, wantName, wantDate} {
				if !strings.Contains(m.HTML, want) {
					t.Errorf("mail to %s lacks %q", m.To, want)
				}
			}
		}
	}
	t.Run("the display name", func(t *testing.T) {
		w := newInvWorld(t, obi, "Ada Obi")
		check(t, w, w.addMember(t, "admin", "active", "Bola Ade"), "Bola Ade")
	})
	t.Run("the email when there is no display name", func(t *testing.T) {
		w := newInvWorld(t, obi, "Ada Obi")
		b := w.addMember(t, "admin", "active", "")
		check(t, w, b, emailOf(b))
	})
	t.Run("a workspace admin when there is neither", func(t *testing.T) {
		w := newInvWorld(t, obi, "Ada Obi")
		b := w.addMember(t, "admin", "active", "")
		clearEmail(t, w.super, b)
		check(t, w, b, "A workspace admin")
	})
}

func TestCancelMail_ANoOpOrRefusedCancelSendsNothing(t *testing.T) {
	cases := []struct {
		name     string
		locked   bool
		wantErr  error
		wantMsgs int
		cancel   func(t *testing.T, n *DeletionNotifier, w *linkWorld) error
	}{
		{"an unscheduled workspace", false, nil, 0,
			func(t *testing.T, n *DeletionNotifier, w *linkWorld) error { return n.Cancel(w.adminCtx()) }},
		{"a preparer", true, ErrCancelDeletionNotPermitted, 0,
			func(t *testing.T, n *DeletionNotifier, w *linkWorld) error { return n.Cancel(w.as(w.preparer)) }},
		{"a suspended admin", true, db.ErrNotActiveMember, 0,
			func(t *testing.T, n *DeletionNotifier, w *linkWorld) error { return n.Cancel(w.as(w.suspended)) }},
		{"a bad token", true, ErrDeletionLinkNotValid, 0,
			func(t *testing.T, n *DeletionNotifier, w *linkWorld) error {
				return n.CancelByLink(w.as(w.b), freshToken(t))
			}},
		// Control: a real cancel of the same world mails, so the zeros above are about the outcome.
		{"control: a real cancel", true, nil, 2,
			func(t *testing.T, n *DeletionNotifier, w *linkWorld) error { return n.Cancel(w.as(w.b)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newLinkWorld(t)
			if !tc.locked {
				unlockByOwner(t, w.super, w.tenant)
			}
			sender := &apiSender{}
			n, _ := notifierFor(w.invWorld, sender)

			err := tc.cancel(t, n, w)
			if (tc.wantErr == nil && err != nil) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got := len(sender.messages()); got != tc.wantMsgs {
				t.Errorf("messages sent = %d, want %d", got, tc.wantMsgs)
			}
			if got := len(cancelMailRows(t, w.super, w.tenant)); got != tc.wantMsgs {
				t.Errorf("cancel-mail rows = %d, want %d", got, tc.wantMsgs)
			}
		})
	}
}

func TestCancelMail_ASoleAdminSendsNothing(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	w.addMember(t, "admin", "suspended", "Sade Eze")
	w.addMember(t, "preparer", "active", "Pat Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	if err := n.Cancel(w.adminCtx()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if got := len(sender.sent()); got != 0 {
		t.Errorf("Send calls = %d, want 0", got)
	}
	if got := len(cancelMailRows(t, w.super, w.tenant)); got != 0 {
		t.Errorf("cancel-mail rows = %d, want 0", got)
	}

	// Control: with a second active admin the same cancel mails them.
	ctl := newInvWorld(t, obi, "Ada Obi")
	other := ctl.addMember(t, "admin", "active", "Bola Eze")
	lockByOwner(t, ctl.super, ctl.tenant, ctl.admin)
	csender := &apiSender{}
	cn, _ := notifierFor(ctl, csender)
	if err := cn.Cancel(ctl.adminCtx()); err != nil {
		t.Fatalf("control Cancel: %v", err)
	}
	if got := sortedAddrs(csender.messages()); !slices.Equal(got, []string{emailOf(other)}) {
		t.Errorf("control: recipients = %v, want [%s]", got, emailOf(other))
	}
}

func TestCancelMail_AFailedSendIsRecordedAndTheCancelStands(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	c := w.addMember(t, "admin", "active", "Chidi Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)
	sender := failing500()
	n, logs := notifierFor(w, sender)

	if err := n.Cancel(w.as(b)); err != nil {
		t.Fatalf("Cancel = %v, want nil: a mail failure never fails the cancel", err)
	}
	if cols := deletionOf(t, w.super, w.tenant); !cols.empty() {
		t.Errorf("deletion columns = %+v, want all NULL (the workspace is unlocked)", cols)
	}
	if got := auditCount(t, w.app, w.tenant, evCancelled); got != 1 {
		t.Errorf("%s rows = %d, want 1", evCancelled, got)
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if len(rows) != 2 {
		t.Fatalf("cancel-mail rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Status != "failed" {
			t.Errorf("send_status for %s = %q, want failed", r.Recipient, r.Status)
		}
	}
	if len(sender.messages()) != 2 {
		t.Fatalf("attempted messages = %d, want 2", len(sender.messages()))
	}
	log := logs.String()
	for _, want := range []string{"level=ERROR", "count=2", "status=500"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	assertNoPII(t, log, emailOf(w.admin), emailOf(b), emailOf(c), "Ada Obi", "Bola Eze", "Chidi Eze", obi)
}

func TestCancelMail_ACancelledClientStillRecordsTheSend(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)
	ctx, cancel := context.WithCancel(w.as(b))
	defer cancel()
	var sendCtxErr error
	sender := &apiSender{fn: func(c context.Context, _ []accountmail.Message) error {
		cancel()
		sendCtxErr = c.Err()
		return nil
	}}
	n, _ := notifierFor(w, sender)

	if err := n.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("Send calls = %d, want 1", len(sender.sent()))
	}
	if sendCtxErr != nil {
		t.Errorf("the send ran on a cancelled context: %v", sendCtxErr)
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if len(rows) != 1 || rows[0].Status != "sent" {
		t.Errorf("cancel-mail rows = %+v, want one row reading sent (not sending)", rows)
	}
}

func TestCancelMail_AFailedAuditSendsNothing(t *testing.T) {
	t.Run("the DEL-04 route", func(t *testing.T) {
		w := newLinkWorld(t)
		drop := failAudit(t, w.super, w.tenant, evCancelled)
		sender := &apiSender{}
		n, _ := notifierFor(w.invWorld, sender)

		err := n.Cancel(w.as(w.b))
		if err == nil {
			t.Fatal("Cancel succeeded although its audit write fails")
		}
		requireNoSentinel(t, err)
		requireRolledBack(t, w, sender)

		drop()
		if err := n.Cancel(w.as(w.b)); err != nil {
			t.Errorf("Cancel after the trigger is gone: %v", err)
		}
		if got := len(sender.messages()); got != 2 {
			t.Errorf("control: messages after the retry = %d, want 2", got)
		}
	})
	t.Run("the link route", func(t *testing.T) {
		w := newLinkWorld(t)
		drop := failAudit(t, w.super, w.tenant, evCancelled)
		sender := &apiSender{}
		n, _ := notifierFor(w.invWorld, sender)

		err := n.CancelByLink(w.as(w.b), w.tokB)
		if err == nil {
			t.Fatal("CancelByLink succeeded although its audit write fails")
		}
		requireNoSentinel(t, err)
		requireRolledBack(t, w, sender)

		drop()
		if err := n.CancelByLink(w.as(w.b), w.tokB); err != nil {
			t.Errorf("CancelByLink after the trigger is gone: %v", err)
		}
		if got := len(sender.messages()); got != 2 {
			t.Errorf("control: messages after the retry = %d, want 2", got)
		}
	})
}

func requireRolledBack(t *testing.T, w *linkWorld, sender *apiSender) {
	t.Helper()
	w.requireLocked(t)
	if got := len(cancelMailRows(t, w.super, w.tenant)); got != 0 {
		t.Errorf("cancel-mail rows = %d, want 0 (rolled back with the cancel)", got)
	}
	if got := len(sender.sent()); got != 0 {
		t.Errorf("Send calls = %d, want 0", got)
	}
}

func TestCancelMail_AScheduleBeforeTheRecordLeavesTheRowsSending(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Obi")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	c := w.addMember(t, "admin", "active", "Chidi Eze")
	lockByOwner(t, w.super, w.tenant, w.admin)
	var rescheduleErr error
	sender := &apiSender{fn: func(context.Context, []accountmail.Message) error {
		_, rescheduleErr = w.store.ScheduleWorkspaceDeletion(auth.WithIdentity(context.Background(),
			auth.Identity{Subject: w.admin, Role: "authenticated", TenantID: w.tenant}), obi)
		return nil
	}}
	n, logs := notifierFor(w, sender)

	if err := n.Cancel(w.as(b)); err != nil {
		t.Fatalf("Cancel = %v, want nil: the refused record never fails the cancel", err)
	}
	if got := len(sender.sent()); got != 1 {
		t.Fatalf("Send calls = %d, want 1", got)
	}
	if rescheduleErr != nil {
		t.Fatalf("fixture: the schedule inside Send failed: %v", rescheduleErr)
	}
	if cols := deletionOf(t, w.super, w.tenant); cols.DueAt == nil {
		t.Error("fixture: the workspace is not locked again")
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if len(rows) != 2 {
		t.Fatalf("cancel-mail rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Status != "sending" {
			t.Errorf("send_status for %s = %q, want sending (the record was refused)", r.Recipient, r.Status)
		}
	}
	log := logs.String()
	if !strings.Contains(log, "level=ERROR") || !strings.Contains(strings.ToLower(log), "record") {
		t.Errorf("log does not name the refused record at ERROR:\n%s", log)
	}
	assertNoPII(t, log, emailOf(w.admin), emailOf(b), emailOf(c), "Ada Obi", "Bola Eze", "Chidi Eze", obi)
}
