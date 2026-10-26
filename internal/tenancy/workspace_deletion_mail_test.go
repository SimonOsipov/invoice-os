package tenancy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var cancelTokenRe = regexp.MustCompile(`#cancel-deletion=([A-Za-z0-9_-]{43})(?:[^A-Za-z0-9_-]|$)`)

type linkRow struct {
	ID, Recipient, Status string
	Hash                  []byte
	Expires               time.Time
}

func linkRows(t *testing.T, super *pgxpool.Pool, tenant string) []linkRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id::text, recipient_user_id::text, send_status, token_hash, expires_at
		   FROM workspace_deletion_links WHERE tenant_id = $1 ORDER BY recipient_user_id`, tenant)
	if err != nil {
		t.Fatalf("read the deletion links: %v", err)
	}
	defer rows.Close()
	var out []linkRow
	for rows.Next() {
		var r linkRow
		if err := rows.Scan(&r.ID, &r.Recipient, &r.Status, &r.Hash, &r.Expires); err != nil {
			t.Fatalf("scan a deletion link: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the deletion links: %v", err)
	}
	return out
}

func linkFor(rows []linkRow, user string) (linkRow, bool) {
	for _, r := range rows {
		if r.Recipient == user {
			return r, true
		}
	}
	return linkRow{}, false
}

func notifierFor(w invWorld, s accountmail.Sender) (*DeletionNotifier, *syncBuf) {
	logs := &syncBuf{}
	return &DeletionNotifier{Store: w.store, Sender: s, Logger: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

func messageTo(t *testing.T, msgs []accountmail.Message, email string) accountmail.Message {
	t.Helper()
	for _, m := range msgs {
		if m.To == email {
			return m
		}
	}
	t.Fatalf("no message to %s among %d", email, len(msgs))
	return accountmail.Message{}
}

func tokenOf(t *testing.T, m accountmail.Message) string {
	t.Helper()
	g := cancelTokenRe.FindStringSubmatch(m.HTML)
	if g == nil {
		t.Fatalf("message to %s holds no 43-character cancel token", m.To)
	}
	return g[1]
}

func emailOf(user string) string { return user + "@members.test" }

// failTable makes an INSERT or UPDATE of one tenant's rows in a table raise; the trigger is gone at cleanup.
func failTable(t *testing.T, super *pgxpool.Pool, table, op, tenant string) {
	t.Helper()
	ctx := context.Background()
	name := "del_mail_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx,
		`CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced failure' USING ERRCODE = 'check_violation'; END; $$`); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	if _, err := super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE %s ON %s FOR EACH ROW WHEN (NEW.tenant_id = %s::uuid) EXECUTE FUNCTION %s()`,
		name, op, table, quoteLiteral(tenant), name)); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
}

func clearEmail(t *testing.T, super *pgxpool.Pool, user string) {
	t.Helper()
	if _, err := super.Exec(context.Background(), `UPDATE memberships SET email = NULL WHERE user_id = $1`, user); err != nil {
		t.Fatalf("clear the membership email: %v", err)
	}
}

// assertNoPII fails when the log holds any of the strings.
func assertNoPII(t *testing.T, log string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(log, s) {
			t.Errorf("log holds %q; it may hold counts and a status only:\n%s", s, log)
		}
	}
}

func TestDeletionMail_EveryOtherActiveAdminGetsOneLink(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	c := w.addMember(t, "admin", "active", "Chidi Eze")
	s := w.addMember(t, "admin", "suspended", "Sade Eze")
	r := w.addMember(t, "admin", "removed", "Rita Eze")
	p := w.addMember(t, "preparer", "active", "Pat Eze")
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	due, err := n.Schedule(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	calls := sender.sent()
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("Send calls = %v batches, want 1 batch of 2 messages", len(calls))
	}
	msgs := calls[0]
	var to []string
	for _, m := range msgs {
		to = append(to, m.To)
	}
	slices.Sort(to)
	if want := []string{emailOf(b), emailOf(c)}; !slices.Equal(to, want) {
		t.Errorf("recipients = %v, want exactly the other active admins %v", to, want)
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 2 {
		t.Fatalf("link rows = %d, want 2", len(rows))
	}
	for _, other := range []string{w.admin, s, r, p} {
		if _, ok := linkFor(rows, other); ok {
			t.Errorf("a link row exists for %s, who must get none", other)
		}
	}
	for _, rec := range []string{b, c} {
		row, ok := linkFor(rows, rec)
		if !ok {
			t.Fatalf("no link row for %s", rec)
		}
		if !row.Expires.Equal(due) {
			t.Errorf("expires_at for %s = %v, want the due time %v", rec, row.Expires, due)
		}
		tok := tokenOf(t, messageTo(t, msgs, emailOf(rec)))
		if h := sha256.Sum256([]byte(tok)); !slices.Equal(row.Hash, h[:]) {
			t.Errorf("sha256(token) for %s does not match its row's hash", rec)
		}
	}
	if tokenOf(t, msgs[0]) == tokenOf(t, msgs[1]) {
		t.Error("both recipients got the same token")
	}
}

func TestDeletionMail_MailNamesWorkspaceRequesterAndDate(t *testing.T) {
	check := func(t *testing.T, w invWorld, wantRequester string) {
		w.addMember(t, "admin", "active", "Bola Eze")
		w.addMember(t, "admin", "active", "Chidi Eze")
		sender := &apiSender{}
		n, _ := notifierFor(w, sender)

		due, err := n.Schedule(w.adminCtx(), obi)
		if err != nil {
			t.Fatalf("Schedule: %v", err)
		}
		msgs := sender.messages()
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2", len(msgs))
		}
		wantDate := due.UTC().Format("2 January 2006") + " (UTC)"
		for _, m := range msgs {
			if m.Subject != accountmail.DeletionNoticeSubject {
				t.Errorf("subject = %q, want %q", m.Subject, accountmail.DeletionNoticeSubject)
			}
			for _, want := range []string{obi, wantRequester, wantDate} {
				if !strings.Contains(m.HTML, want) {
					t.Errorf("mail to %s lacks %q", m.To, want)
				}
			}
		}
	}
	t.Run("the display name", func(t *testing.T) {
		check(t, newInvWorld(t, obi, "Ada Okafor"), "Ada Okafor")
	})
	t.Run("the email when there is no display name", func(t *testing.T) {
		w := newInvWorld(t, obi, "")
		check(t, w, emailOf(w.admin))
	})
}

func TestDeletionMail_ARequesterWithNoNameOrEmailReadsAsAWorkspaceAdmin(t *testing.T) {
	w := newInvWorld(t, obi, "")
	clearEmail(t, w.super, w.admin)
	w.addMember(t, "admin", "active", "Bola Eze")
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	if _, err := n.Schedule(w.adminCtx(), obi); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].HTML, "A workspace admin") {
		t.Errorf("mail lacks the fallback requester %q", "A workspace admin")
	}
}

func TestDeletionMail_ASentBatchIsRecorded(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	w.addMember(t, "admin", "active", "Bola Eze")
	w.addMember(t, "admin", "active", "Chidi Eze")
	n, _ := notifierFor(w, &apiSender{})

	if _, err := n.Schedule(w.adminCtx(), obi); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 2 {
		t.Fatalf("link rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Status != "sent" {
			t.Errorf("send_status for %s = %q, want sent", r.Recipient, r.Status)
		}
	}
}

func TestDeletionMail_AFailedSendIsRecordedAndTheScheduleStands(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	c := w.addMember(t, "admin", "active", "Chidi Eze")
	sender := failing500()
	n, logs := notifierFor(w, sender)

	due, err := n.Schedule(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("Schedule = %v, want nil: a mail failure never fails the schedule", err)
	}
	if cols := deletionOf(t, w.super, w.tenant); cols.DueAt == nil || !cols.DueAt.Equal(due) {
		t.Errorf("stored due = %v, want the returned %v (the workspace stays locked)", cols.DueAt, due)
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 2 {
		t.Fatalf("link rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Status != "failed" {
			t.Errorf("send_status for %s = %q, want failed", r.Recipient, r.Status)
		}
	}
	msgs := sender.messages()
	if len(msgs) != 2 {
		t.Fatalf("attempted messages = %d, want 2", len(msgs))
	}
	log := logs.String()
	for _, want := range []string{"count=2", "status=500"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	secrets := []string{emailOf(b), emailOf(c), emailOf(w.admin), "Ada Okafor", "Bola Eze", "Chidi Eze", obi}
	for _, m := range msgs {
		secrets = append(secrets, tokenOf(t, m))
	}
	assertNoPII(t, log, secrets...)
}

func TestDeletionMail_ACancelledClientStillRecordsTheSend(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	w.addMember(t, "admin", "active", "Bola Eze")
	ctx, cancel := context.WithCancel(w.adminCtx())
	defer cancel()
	var sendCtxErr error
	sender := &apiSender{fn: func(c context.Context, _ []accountmail.Message) error {
		cancel()
		sendCtxErr = c.Err()
		return nil
	}}
	n, _ := notifierFor(w, sender)

	if _, err := n.Schedule(ctx, obi); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("Send calls = %d, want 1", len(sender.sent()))
	}
	if sendCtxErr != nil {
		t.Errorf("the send ran on a cancelled context: %v", sendCtxErr)
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 1 || rows[0].Status != "sent" {
		t.Errorf("link rows = %+v, want one row reading sent (not sending)", rows)
	}
}

func TestDeletionMail_ARefusedScheduleMintsNoLink(t *testing.T) {
	// Each case has another active admin, so a refusal that still mints would leave a row and a message.
	cases := []struct {
		name string
		want error
	}{
		{"a preparer", ErrDeleteWorkspaceNotPermitted},
		{"the wrong name", ErrWorkspaceNameMismatch},
		{"an already locked workspace", db.ErrWorkspaceLocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newInvWorld(t, obi, "Ada Okafor")
			w.addMember(t, "admin", "active", "Bola Eze")
			preparer := w.addMember(t, "preparer", "active", "Pat Eze")
			sender := &apiSender{}
			n, _ := notifierFor(w, sender)
			caller, typed := w.adminCtx(), obi
			switch tc.name {
			case "a preparer":
				caller = w.as(preparer)
			case "the wrong name":
				typed = "not the name"
			case "an already locked workspace":
				lockByOwner(t, w.super, w.tenant, w.admin)
			}

			_, err := n.Schedule(caller, typed)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if got := len(sender.sent()); got != 0 {
				t.Errorf("Send calls = %d, want 0", got)
			}
			if rows := linkRows(t, w.super, w.tenant); len(rows) != 0 {
				t.Errorf("link rows = %d, want 0", len(rows))
			}
		})
	}

	t.Run("the house workspace", func(t *testing.T) {
		super, app := dbTestPools(t)
		house, admin := houseMember(t, super, "active")
		var name string
		if err := super.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, house).Scan(&name); err != nil {
			t.Fatalf("read the house name: %v", err)
		}
		t.Cleanup(func() { unlockByOwner(t, super, house) })
		other := uuid.NewString()
		seedIdentityMembership(t, super, house, other, "admin", "active", strp("House Admin"), strp(emailOf(other)))
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, house, other)
		})
		sender := &apiSender{}
		logs := &syncBuf{}
		n := &DeletionNotifier{Store: NewStore(app), Sender: sender, Logger: slog.New(slog.NewTextHandler(logs, nil))}
		before := len(linkRows(t, super, house))

		_, err := n.Schedule(auth.WithIdentity(context.Background(), auth.Identity{Subject: admin, Role: "authenticated", TenantID: house}), name)
		if !errors.Is(err, ErrHouseWorkspace) {
			t.Errorf("err = %v, want ErrHouseWorkspace", err)
		}
		if got := len(sender.sent()); got != 0 {
			t.Errorf("Send calls = %d, want 0", got)
		}
		if after := len(linkRows(t, super, house)); after != before {
			t.Errorf("house link rows = %d, want %d unchanged", after, before)
		}
	})

	// Control: the exact name from an admin mints and mails, so the refusals above were about the caller.
	t.Run("control: an admin with the exact name", func(t *testing.T) {
		w := newInvWorld(t, obi, "Ada Okafor")
		w.addMember(t, "admin", "active", "Bola Eze")
		sender := &apiSender{}
		n, _ := notifierFor(w, sender)
		if _, err := n.Schedule(w.adminCtx(), obi); err != nil {
			t.Fatalf("Schedule: %v", err)
		}
		if got := len(sender.messages()); got != 1 {
			t.Errorf("messages = %d, want 1", got)
		}
		if rows := linkRows(t, w.super, w.tenant); len(rows) != 1 {
			t.Errorf("link rows = %d, want 1", len(rows))
		}
	})
}

func TestDeletionMail_ASoleAdminSendsNothing(t *testing.T) {
	// Others exist but none is an active admin; the control below proves a second admin does get a link.
	w := newInvWorld(t, obi, "Ada Okafor")
	w.addMember(t, "admin", "suspended", "Sade Eze")
	w.addMember(t, "preparer", "active", "Pat Eze")
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	due, err := n.Schedule(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if cols := deletionOf(t, w.super, w.tenant); cols.DueAt == nil || !cols.DueAt.Equal(due) {
		t.Errorf("stored due = %v, want the returned %v", cols.DueAt, due)
	}
	if got := len(sender.sent()); got != 0 {
		t.Errorf("Send calls = %d, want 0", got)
	}
	if rows := linkRows(t, w.super, w.tenant); len(rows) != 0 {
		t.Errorf("link rows = %d, want 0", len(rows))
	}

	ctl := newInvWorld(t, obi, "Ada Okafor")
	ctl.addMember(t, "admin", "active", "Bola Eze")
	csender := &apiSender{}
	cn, _ := notifierFor(ctl, csender)
	if _, err := cn.Schedule(ctl.adminCtx(), obi); err != nil {
		t.Fatalf("control Schedule: %v", err)
	}
	if got := len(csender.messages()); got != 1 {
		t.Errorf("control: messages = %d, want 1", got)
	}
}

func TestDeletionMail_AnAdminWithNoEmailIsSkipped(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	nul := w.addMember(t, "admin", "active", "Nia Eze")
	clearEmail(t, w.super, nul)
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	if _, err := n.Schedule(w.adminCtx(), obi); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	msgs := sender.messages()
	if len(msgs) != 1 || msgs[0].To != emailOf(b) {
		t.Fatalf("messages = %+v, want exactly one, to %s", msgs, emailOf(b))
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 1 || rows[0].Recipient != b {
		t.Errorf("link rows = %+v, want one row for %s", rows, b)
	}
}

func TestDeletionMail_AFailedLinkInsertRollsTheScheduleBack(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	w.addMember(t, "admin", "active", "Bola Eze")
	failTable(t, w.super, "workspace_deletion_links", "INSERT", w.tenant)
	sender := &apiSender{}
	n, _ := notifierFor(w, sender)

	_, err := n.Schedule(w.adminCtx(), obi)
	if err == nil {
		t.Fatal("Schedule succeeded although the link insert fails")
	}
	requireNoSentinel(t, err)
	if cols := deletionOf(t, w.super, w.tenant); !cols.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", cols)
	}
	if got := auditCount(t, w.app, w.tenant, evScheduled); got != 0 {
		t.Errorf("%s rows = %d, want 0", evScheduled, got)
	}
	if got := len(sender.sent()); got != 0 {
		t.Errorf("Send calls = %d, want 0", got)
	}
}

func TestDeletionMail_AFailedOutcomeWriteKeepsTheSchedule(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	b := w.addMember(t, "admin", "active", "Bola Eze")
	failTable(t, w.super, "workspace_deletion_links", "UPDATE", w.tenant)
	sender := &apiSender{}
	n, logs := notifierFor(w, sender)

	due, err := n.Schedule(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("Schedule = %v, want nil: an outcome write failure never fails the schedule", err)
	}
	if cols := deletionOf(t, w.super, w.tenant); cols.DueAt == nil || !cols.DueAt.Equal(due) {
		t.Errorf("stored due = %v, want the returned %v", cols.DueAt, due)
	}
	rows := linkRows(t, w.super, w.tenant)
	if len(rows) != 1 || rows[0].Status != "sending" {
		t.Errorf("link rows = %+v, want one row still sending", rows)
	}
	msgs := sender.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	log := logs.String()
	if !strings.Contains(log, "level=ERROR") || !strings.Contains(strings.ToLower(log), "record") {
		t.Errorf("log does not name the failed record at ERROR:\n%s", log)
	}
	assertNoPII(t, log, emailOf(b), emailOf(w.admin), "Ada Okafor", "Bola Eze", obi, tokenOf(t, msgs[0]))
}

func TestDeletionMail_RecordDeliveryTouchesOnlyTheCallersTenant(t *testing.T) {
	a := newInvWorld(t, obi, "Ada Okafor")
	other := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")
	own := insertLink(t, a.super, a.tenant, a.addMember(t, "admin", "active", "Chidi Eze"))
	foreign := insertLink(t, other.super, other.tenant, other.addMember(t, "admin", "active", "Dayo Eze"))

	// Positive control first: the caller's own tenant's row is recorded.
	if err := a.store.RecordDeletionLinkDelivery(a.adminCtx(), own, true); err != nil {
		t.Fatalf("record on the caller's own link: %v", err)
	}
	if r := linkRows(t, a.super, a.tenant); len(r) != 1 || r[0].Status != "sent" {
		t.Errorf("own link rows = %+v, want one row reading sent", r)
	}

	if err := a.store.RecordDeletionLinkDelivery(a.adminCtx(), foreign, true); err != nil {
		t.Errorf("record on another tenant's link = %v, want nil (the row is invisible, nothing to change)", err)
	}
	if r := linkRows(t, other.super, other.tenant); len(r) != 1 || r[0].Status != "sending" {
		t.Errorf("the other tenant's link rows = %+v, want one row still sending", r)
	}
}

func TestDeletionMail_RecordDeliveryNeedsAnIdentity(t *testing.T) {
	w := newInvWorld(t, obi, "Ada Okafor")
	link := insertLink(t, w.super, w.tenant, w.addMember(t, "admin", "active", "Chidi Eze"))

	cases := map[string]context.Context{
		"no identity": context.Background(),
		"a non-UUID subject": auth.WithIdentity(context.Background(),
			auth.Identity{Subject: "not-a-uuid", Role: "authenticated", TenantID: w.tenant}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			if err := w.store.RecordDeletionLinkDelivery(ctx, link, true); !errors.Is(err, db.ErrNoTenant) {
				t.Errorf("err = %v, want db.ErrNoTenant", err)
			}
		})
	}
	if r := linkRows(t, w.super, w.tenant); len(r) != 1 || r[0].Status != "sending" {
		t.Errorf("link rows = %+v, want one row still sending", r)
	}
	// Control: with an identity the same call writes.
	if err := w.store.RecordDeletionLinkDelivery(w.adminCtx(), link, false); err != nil {
		t.Fatalf("record with an identity: %v", err)
	}
	if r := linkRows(t, w.super, w.tenant); len(r) != 1 || r[0].Status != "failed" {
		t.Errorf("link rows = %+v, want one row reading failed", r)
	}
}

// insertLink adds a sending link row for a recipient, as superuser.
func insertLink(t *testing.T, super *pgxpool.Pool, tenant, recipient string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO workspace_deletion_links (tenant_id, recipient_user_id, token_hash, expires_at)
		 VALUES ($1::uuid, $2::uuid, decode(md5($2::text) || md5($1::text), 'hex'), now() + interval '30 days') RETURNING id::text`,
		tenant, recipient).Scan(&id); err != nil {
		t.Fatalf("seed a deletion link: %v", err)
	}
	return id
}
