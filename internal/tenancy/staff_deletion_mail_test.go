package tenancy

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// staffMailWorld is a staff world with admins A1 (the tenant's seeded one) and A2 active, A3 suspended and a preparer.
type staffMailWorld struct {
	staffDelWorld
	a1, a2, a3, p string
}

func withStaffMailMembers(t *testing.T, w staffDelWorld) staffMailWorld {
	t.Helper()
	m := staffMailWorld{staffDelWorld: w, a1: w.admin}
	m.a2 = w.addMember(t, "admin", "active", "Bola Eze")
	m.a3 = w.addMember(t, "admin", "suspended", "Sade Eze")
	m.p = w.addMember(t, "preparer", "active", "Pat Eze")
	return m
}

func newStaffMailWorld(t *testing.T) staffMailWorld {
	t.Helper()
	return withStaffMailMembers(t, newStaffDelWorld(t))
}

func (w staffMailWorld) wantRecipients() []string { return sortedEmails(w.a1, w.a2) }

func TestStaffDeletionMail_OneLinkPerActiveAdmin(t *testing.T) {
	w := newStaffMailWorld(t)
	n, _ := notifierFor(w.invWorld, &apiSender{})

	due, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}

	rows := linkRows(t, w.super, w.tenant)
	var got []string
	for _, r := range rows {
		got = append(got, r.Recipient)
		if !r.Expires.Equal(due) {
			t.Errorf("expires_at for %s = %v, want the due time %v", r.Recipient, r.Expires, due)
		}
	}
	slices.Sort(got)
	if want := sortedUsers(w.a1, w.a2); !slices.Equal(got, want) {
		t.Errorf("link recipients = %v, want %v (not %s or %s)", got, want, w.a3, w.p)
	}
}

func TestStaffDeletionMail_EveryAdminIsMailedFromSupport(t *testing.T) {
	w := newStaffMailWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	due, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText)
	if err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}

	msgs := requireOneBatchTo(t, sender, w.wantRecipients())
	wantDate := due.UTC().Format("2 January 2006") + " (UTC)"
	for _, m := range msgs {
		if m.Subject != accountmail.DeletionNoticeSubject {
			t.Errorf("subject to %s = %q, want %q", m.To, m.Subject, accountmail.DeletionNoticeSubject)
		}
		for _, want := range []string{obi, audit.SupportActor, wantDate} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("mail to %s lacks %q", m.To, want)
			}
		}
	}
}

func TestStaffDeletionMail_TheOutcomeIsRecordedOnThePathTenant(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		sender     *apiSender
	}{
		{"sender ok", "sent", &apiSender{}},
		{"sender fails", "failed", failing500()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newStaffMailWorld(t)
			n, _ := notifierFor(w.invWorld, tc.sender)

			due, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText)
			if err != nil {
				t.Fatalf("StaffSchedule: %v", err)
			}
			if due.IsZero() {
				t.Error("StaffSchedule returned a zero due time")
			}
			rows := linkRows(t, w.super, w.tenant)
			if len(rows) != 2 {
				t.Fatalf("link rows = %d, want 2", len(rows))
			}
			for _, r := range rows {
				if r.Status != tc.want {
					t.Errorf("send_status for %s = %q, want %q", r.Recipient, r.Status, tc.want)
				}
			}
		})
	}
}

func TestStaffDeletionMail_AStaffLinkCancels(t *testing.T) {
	w := newStaffMailWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)
	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	tok := tokenOf(t, messageTo(t, sender.messages(), emailOf(w.a1)))

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.a1), tok); err != nil {
		t.Fatalf("CancelWorkspaceDeletionByLink: %v", err)
	}
	if due := deletionOf(t, w.super, w.tenant).DueAt; due != nil {
		t.Errorf("deletion_due_at = %v after the link, want unscheduled", due)
	}
}

func TestStaffDeletionMail_ALinkAfterAStaffCancelChangesNothing(t *testing.T) {
	w := newStaffMailWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)
	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	tok := tokenOf(t, messageTo(t, sender.messages(), emailOf(w.a1)))
	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel: %v", err)
	}
	before := auditCount(t, w.app, w.tenant, evCancelled)

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.a1), tok); err != nil {
		t.Fatalf("the link after a staff cancel: err = %v, want nil", err)
	}
	if due := deletionOf(t, w.super, w.tenant).DueAt; due != nil {
		t.Errorf("deletion_due_at = %v, want unscheduled", due)
	}
	if got := auditCount(t, w.app, w.tenant, evCancelled); got != before {
		t.Errorf("%s rows = %d, want %d: the link must change nothing", evCancelled, got, before)
	}
}

func TestStaffDeletionMail_ALinkAfterPurgeNowIsRefused(t *testing.T) {
	w := withStaffMailMembers(t, newStaffPurgeWorld(t, "Ada Obi"))
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)
	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	tok := tokenOf(t, messageTo(t, sender.messages(), emailOf(w.a2)))
	at, err := w.store.StaffPurgeWorkspace(w.ctx, w.tenant, obi, purgeReason)
	if err != nil {
		t.Fatalf("StaffPurgeWorkspace: %v", err)
	}
	due := deletionOf(t, w.super, w.tenant).DueAt

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.a2), tok); !errors.Is(err, ErrDeletionUnderway) {
		t.Errorf("the link after a purge now: err = %v, want ErrDeletionUnderway", err)
	}
	if m := purgeStartedAt(t, w.super, w.tenant); m == nil || !m.Equal(at) {
		t.Errorf("deletion_purge_started_at = %v, want %v unchanged", m, at)
	}
	if got := deletionOf(t, w.super, w.tenant).DueAt; got == nil || due == nil || !got.Equal(*due) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", got, due)
	}
}

func TestStaffDeletionMail_ARestartVoidsTheOldLinks(t *testing.T) {
	w := newStaffMailWorld(t)
	first, second := &apiSender{}, &apiSender{}
	n, _ := notifierFor(w.invWorld, first)
	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("first StaffSchedule: %v", err)
	}
	old := tokenOf(t, messageTo(t, first.messages(), emailOf(w.a1)))
	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel: %v", err)
	}
	n.Sender = second
	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("second StaffSchedule: %v", err)
	}
	fresh := tokenOf(t, messageTo(t, second.messages(), emailOf(w.a1)))

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.a1), old); !errors.Is(err, ErrDeletionLinkNotValid) {
		t.Errorf("the first start's token: err = %v, want ErrDeletionLinkNotValid", err)
	}
	if deletionOf(t, w.super, w.tenant).DueAt == nil {
		t.Fatal("the old token cancelled the new schedule")
	}
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.a1), fresh); err != nil {
		t.Fatalf("the second start's token: %v", err)
	}
	if due := deletionOf(t, w.super, w.tenant).DueAt; due != nil {
		t.Errorf("deletion_due_at = %v after the new link, want unscheduled", due)
	}
}

func TestStaffCancelMail_EveryActiveAdminIsToldBySupport(t *testing.T) {
	w := newStaffMailWorld(t)
	lockByOwner(t, w.super, w.tenant, w.staff)
	cancelled := deletionOf(t, w.super, w.tenant).DueAt
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel: %v", err)
	}

	msgs := requireOneBatchTo(t, sender, w.wantRecipients())
	wantDate := cancelled.UTC().Format("2 January 2006") + " (UTC)"
	for _, m := range msgs {
		if m.Subject != accountmail.DeletionCancelledSubject {
			t.Errorf("subject to %s = %q, want %q", m.To, m.Subject, accountmail.DeletionCancelledSubject)
		}
		for _, want := range []string{obi, audit.SupportActor, wantDate} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("mail to %s lacks %q", m.To, want)
			}
		}
	}
	rows := cancelMailRows(t, w.super, w.tenant)
	if got, want := sortedRecipients(rows), sortedUsers(w.a1, w.a2); !slices.Equal(got, want) {
		t.Errorf("cancel-mail recipients = %v, want %v (not %s or %s)", got, want, w.a3, w.p)
	}
	for _, r := range rows {
		if !r.CancelledDue.Equal(*cancelled) {
			t.Errorf("cancelled_due_at for %s = %v, want %v", r.Recipient, r.CancelledDue, cancelled)
		}
	}
}

func TestStaffCancelMail_CarriesNoStaffIdentity(t *testing.T) {
	const name, email = "Zainab Staffperson", "zainab.staff@example.invalid"
	w := newStaffMailWorld(t)
	if _, err := w.super.Exec(w.ctx, `UPDATE memberships SET display_name = $2, email = $3 WHERE user_id = $1`, w.staff, name, email); err != nil {
		t.Fatalf("name the staff member: %v", err)
	}
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel: %v", err)
	}

	msgs := sender.messages()
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 2 notices and 2 cancel mails", len(msgs))
	}
	for _, m := range msgs {
		for _, secret := range []string{"Zainab", "Staffperson", email} {
			if strings.Contains(m.Subject, secret) || strings.Contains(m.HTML, secret) {
				t.Errorf("mail to %s (%q) holds %q", m.To, m.Subject, secret)
			}
		}
	}
}

func TestStaffCancelMail_ANoOpCancelSendsNothing(t *testing.T) {
	w := newStaffMailWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel of an unscheduled tenant: %v", err)
	}
	if rows := cancelMailRows(t, w.super, w.tenant); len(rows) != 0 {
		t.Errorf("cancel-mail rows = %d, want 0", len(rows))
	}
	if calls := sender.sent(); len(calls) != 0 {
		t.Errorf("Send calls = %d, want 0", len(calls))
	}
}

func TestStaffCancelMail_TheOutcomeIsRecordedOnThePathTenant(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		sender     *apiSender
	}{
		{"sender ok", "sent", &apiSender{}},
		{"sender fails", "failed", failing500()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newStaffMailWorld(t)
			lockByOwner(t, w.super, w.tenant, w.staff)
			n, _ := notifierFor(w.invWorld, tc.sender)

			if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
				t.Fatalf("StaffCancel: %v", err)
			}
			if due := deletionOf(t, w.super, w.tenant).DueAt; due != nil {
				t.Errorf("deletion_due_at = %v, want unscheduled", due)
			}
			rows := cancelMailRows(t, w.super, w.tenant)
			if len(rows) != 2 {
				t.Fatalf("cancel-mail rows = %d, want 2", len(rows))
			}
			for _, r := range rows {
				if r.Status != tc.want {
					t.Errorf("send_status for %s = %q, want %q", r.Recipient, r.Status, tc.want)
				}
			}
		})
	}
}

func TestStaffDeletionMail_AnAdminWithoutAnEmailGetsNoLinkOrCancelMail(t *testing.T) {
	w := newStaffMailWorld(t)
	clearEmail(t, w.super, w.a2)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	if rows := linkRows(t, w.super, w.tenant); len(rows) != 1 || rows[0].Recipient != w.a1 {
		t.Fatalf("link rows = %+v, want one for %s", rows, w.a1)
	}
	if err := n.StaffCancel(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffCancel: %v", err)
	}
	if rows := cancelMailRows(t, w.super, w.tenant); len(rows) != 1 || rows[0].Recipient != w.a1 {
		t.Fatalf("cancel-mail rows = %+v, want one for %s", rows, w.a1)
	}
}

func TestStaffDeletionMail_TheMailedDateIsTheStoredDueDate(t *testing.T) {
	w := newStaffMailWorld(t)
	sender := &apiSender{}
	n, _ := notifierFor(w.invWorld, sender)

	if _, err := n.StaffSchedule(w.ctx, w.tenant, staffReasonText); err != nil {
		t.Fatalf("StaffSchedule: %v", err)
	}
	stored := deletionOf(t, w.super, w.tenant).DueAt
	if stored == nil {
		t.Fatal("no stored due date")
	}
	want := stored.UTC().Format("2 January 2006") + " (UTC)"
	msgs := requireOneBatchTo(t, sender, w.wantRecipients())
	for _, m := range msgs {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("notice to %s lacks the stored due date %q", m.To, want)
		}
	}
}
