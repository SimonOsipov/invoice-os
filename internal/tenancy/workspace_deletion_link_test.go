package tenancy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// freshToken is a well-formed (43-character base64url) token that no link holds.
func freshToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random token: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// knownLinkToken gives the recipient's live link a token the test knows.
func knownLinkToken(t *testing.T, super *pgxpool.Pool, tenant, recipient string) string {
	t.Helper()
	tok := freshToken(t)
	tag, err := super.Exec(context.Background(),
		`UPDATE workspace_deletion_links SET token_hash = sha256(convert_to($1, 'UTF8'))
		  WHERE recipient_user_id = $2 AND tenant_id = $3
		    AND expires_at = (SELECT deletion_due_at FROM tenants WHERE id = $3)`, tok, recipient, tenant)
	if err != nil {
		t.Fatalf("set the link token: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("live links of %s = %d, want 1", recipient, tag.RowsAffected())
	}
	return tok
}

// linkWorld is a workspace scheduled by its admin A, with live links for admins B and C.
type linkWorld struct {
	invWorld
	b, c, preparer, suspended string
	tokB, tokC                string
	due                       time.Time
}

func newLinkWorld(t *testing.T) *linkWorld {
	t.Helper()
	w := &linkWorld{invWorld: newInvWorld(t, obi, "Ada Obi")}
	w.b = w.addMember(t, "admin", "active", "Bola Eze")
	w.c = w.addMember(t, "admin", "active", "Chidi Eze")
	w.preparer = w.addMember(t, "preparer", "active", "Pat Eze")
	w.suspended = w.addMember(t, "admin", "suspended", "Sade Eze")
	w.schedule(t)
	return w
}

// schedule locks the workspace as A and notes the tokens of B and C.
func (w *linkWorld) schedule(t *testing.T) {
	t.Helper()
	s, err := w.store.ScheduleWorkspaceDeletion(w.adminCtx(), obi)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	w.due = s.DueAt
	w.tokB = knownLinkToken(t, w.super, w.tenant, w.b)
	w.tokC = knownLinkToken(t, w.super, w.tenant, w.c)
}

// cancelByRoute cancels through the plain cancel route, as the notifier serves it.
func (w *linkWorld) cancelByRoute(ctx context.Context) error {
	return (&DeletionNotifier{Store: w.store, Sender: &apiSender{}}).Cancel(ctx)
}

func (w *linkWorld) requireLocked(t *testing.T) {
	t.Helper()
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil {
		t.Error("the workspace is no longer scheduled for deletion")
	}
}

func TestDeletionLink_AnotherAdminCancelsWithAValidLink(t *testing.T) {
	w := newLinkWorld(t)
	old := deletionOf(t, w.super, w.tenant)
	if _, err := w.store.ListMemberships(w.adminCtx()); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("control: ListMemberships while locked: err = %v, want db.ErrWorkspaceLocked", err)
	}

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); err != nil {
		t.Fatalf("CancelWorkspaceDeletionByLink: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns after the cancel = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Fatalf("%s rows = %d, want 1", evCancelled, n)
	}
	if got := auditActor(t, w.app, w.tenant, evCancelled); got != w.b {
		t.Errorf("audit actor = %q, want the cancelling admin %q", got, w.b)
	}
	p := auditPayloads(t, w.super, w.tenant, evCancelled)[0]
	want := old.DueAt.UTC().Format(time.RFC3339)
	if !slices.Equal(keysOf(p), []string{"due_at"}) || p["due_at"] != want {
		t.Errorf("payload = %v, want exactly due_at = %s", p, want)
	}
	if _, err := w.store.ListMemberships(w.as(w.b)); err != nil {
		t.Errorf("ListMemberships after the cancel: %v", err)
	}
}

func TestDeletionLink_AnyAdminOfTheWorkspaceMayUseALink(t *testing.T) {
	w := newLinkWorld(t)

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.c), w.tokB); err != nil {
		t.Fatalf("C cancels with B's link: %v, want nil (the link is not bound to its recipient)", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if got := auditActor(t, w.app, w.tenant, evCancelled); got != w.c {
		t.Errorf("audit actor = %q, want C %q", got, w.c)
	}
}

func TestDeletionLink_OnlyAnActiveAdminOfThatWorkspaceCancels(t *testing.T) {
	w := newLinkWorld(t)
	foreign := newInvWorld(t, "Elsewhere Ltd", "Olu Eze")
	lockByOwner(t, foreign.super, foreign.tenant, foreign.admin)
	foreignDue := deletionOf(t, foreign.super, foreign.tenant).DueAt

	invited := w.addMember(t, "admin", "invited", "Ivy Eze")
	removed := w.addMember(t, "admin", "removed", "Rita Eze")
	cases := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"an invited admin", w.as(invited), db.ErrNotActiveMember},
		{"a removed admin", w.as(removed), db.ErrNotActiveMember},
		{"no identity at all", context.Background(), db.ErrNoTenant},
		{"an active preparer", w.as(w.preparer), ErrCancelDeletionNotPermitted},
		{"a suspended admin", w.as(w.suspended), db.ErrNotActiveMember},
		{"an admin of another workspace", foreign.adminCtx(), ErrDeletionLinkNotValid},
		{"a non-UUID subject", w.as("not-a-uuid"), db.ErrNoTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.store.CancelWorkspaceDeletionByLink(tc.ctx, w.tokB)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			w.requireLocked(t)
			if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
				t.Errorf("%s rows = %d, want 0", evCancelled, n)
			}
		})
	}
	if c := deletionOf(t, foreign.super, foreign.tenant); c.DueAt == nil || !c.DueAt.Equal(*foreignDue) {
		t.Errorf("the other workspace's due = %v, want %v unchanged", c.DueAt, foreignDue)
	}
	if n := auditCount(t, foreign.app, foreign.tenant, evCancelled); n != 0 {
		t.Errorf("the other workspace has %d %s rows, want 0", n, evCancelled)
	}
	// Control: an active admin with the same token cancels, so the refusals above were about the caller.
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.c), w.tokB); err != nil {
		t.Errorf("control: an active admin with the same token: %v", err)
	}
}

func TestDeletionLink_ABadTokenIsRefused(t *testing.T) {
	w := newLinkWorld(t)
	cases := []struct{ name, token string }{
		{"a random well-formed token", freshToken(t)},
		{"an empty token", ""},
		{"42 characters", w.tokB[:42]},
		{"44 characters", w.tokB + "A"},
		{"43 characters outside base64url", w.tokB[:42] + "+"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), tc.token)
			if !errors.Is(err, ErrDeletionLinkNotValid) {
				t.Errorf("err = %v, want ErrDeletionLinkNotValid", err)
			}
			w.requireLocked(t)
			if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
				t.Errorf("%s rows = %d, want 0", evCancelled, n)
			}
		})
	}

	t.Run("an expired link", func(t *testing.T) {
		ctx := context.Background()
		if _, err := w.super.Exec(ctx,
			`UPDATE tenants SET deletion_scheduled_at = deletion_scheduled_at - interval '31 days' WHERE id = $1`, w.tenant); err != nil {
			t.Fatalf("move the schedule back: %v", err)
		}
		if _, err := w.super.Exec(ctx,
			`UPDATE workspace_deletion_links SET expires_at = (SELECT deletion_due_at FROM tenants WHERE id = $1)
			  WHERE tenant_id = $1`, w.tenant); err != nil {
			t.Fatalf("move the links back: %v", err)
		}
		if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Before(time.Now()) {
			t.Fatalf("fixture: due = %v, want a time in the past", c.DueAt)
		}
		_, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB)
		if !errors.Is(err, ErrDeletionLinkNotValid) {
			t.Errorf("err = %v, want ErrDeletionLinkNotValid", err)
		}
		w.requireLocked(t)
		if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
			t.Errorf("%s rows = %d, want 0", evCancelled, n)
		}

		// Control: the same token works again once the due time is back in the future.
		if _, err := w.super.Exec(ctx,
			`UPDATE tenants SET deletion_scheduled_at = deletion_scheduled_at + interval '31 days' WHERE id = $1`, w.tenant); err != nil {
			t.Fatalf("move the schedule forward: %v", err)
		}
		if _, err := w.super.Exec(ctx,
			`UPDATE workspace_deletion_links SET expires_at = (SELECT deletion_due_at FROM tenants WHERE id = $1)
			  WHERE tenant_id = $1`, w.tenant); err != nil {
			t.Fatalf("move the links forward: %v", err)
		}
		if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); err != nil {
			t.Errorf("control: the same token with a future due time: %v", err)
		}
	})
}

func TestDeletionLink_ANonAdminLearnsNothingAboutTheToken(t *testing.T) {
	w := newLinkWorld(t)
	for name, token := range map[string]string{"a random token": freshToken(t), "a valid token": w.tokB} {
		t.Run(name, func(t *testing.T) {
			_, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.preparer), token)
			if !errors.Is(err, ErrCancelDeletionNotPermitted) {
				t.Errorf("err = %v, want ErrCancelDeletionNotPermitted", err)
			}
			if errors.Is(err, ErrDeletionLinkNotValid) {
				t.Errorf("err = %v tells a non-admin about the token", err)
			}
		})
	}
	w.requireLocked(t)
	// Control: an admin with a bad token reads the token refusal, so the role check is what differs.
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), freshToken(t)); !errors.Is(err, ErrDeletionLinkNotValid) {
		t.Errorf("control: an admin with a random token: err = %v, want ErrDeletionLinkNotValid", err)
	}
}

func TestDeletionLink_EveryLinkIsVoidAfterACancel(t *testing.T) {
	w := newLinkWorld(t)
	oldB, oldC := w.tokB, w.tokC

	if err := w.cancelByRoute(w.adminCtx()); err != nil {
		t.Fatalf("cancel by the plain route: %v", err)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Fatalf("deletion columns after the cancel = %+v, want all NULL", c)
	}
	for name, tok := range map[string]string{"B's": oldB, "C's": oldC} {
		if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), tok); err != nil {
			t.Errorf("%s token after the plain cancel: err = %v, want the no-op nil", name, err)
		}
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Errorf("%s rows = %d, want 1 (the no-op link cancels write none)", evCancelled, n)
	}

	w.schedule(t)
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); err != nil {
		t.Fatalf("cancel by link in the second deletion: %v", err)
	}
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokC); err != nil {
		t.Errorf("C's token after B's link cancel: err = %v, want the no-op nil", err)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 2 {
		t.Errorf("%s rows = %d, want 2", evCancelled, n)
	}
}

// A held link meets a workspace another admin already unlocked: it answers as the plain route does.
func TestDeletionLink_AHeldLinkAfterAnotherAdminsCancelIsANoOp(t *testing.T) {
	w := newLinkWorld(t)
	held := w.tokB
	if _, err := w.store.CancelWorkspaceDeletion(w.as(w.c)); err != nil {
		t.Fatalf("A cancel by C: %v", err)
	}
	res, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), held)
	if err != nil {
		t.Fatalf("link cancel on an unscheduled workspace: err = %v, want nil", err)
	}
	if res.Workspace != "" || len(res.Mails) != 0 {
		t.Errorf("result = %+v, want the zero result (no mail)", res)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Errorf("%s rows = %d, want 1 (C's only)", evCancelled, n)
	}

	// The no-op comes after the caller gates: a refused caller is refused on an unscheduled workspace too.
	refused := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"an active preparer", w.as(w.preparer), ErrCancelDeletionNotPermitted},
		{"a suspended admin", w.as(w.suspended), db.ErrNotActiveMember},
		{"no identity at all", context.Background(), db.ErrNoTenant},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			for route, cancel := range map[string]func() error{
				"link":  func() error { _, err := w.store.CancelWorkspaceDeletionByLink(tc.ctx, held); return err },
				"plain": func() error { _, err := w.store.CancelWorkspaceDeletion(tc.ctx); return err },
			} {
				if err := cancel(); !errors.Is(err, tc.want) {
					t.Errorf("%s route: err = %v, want %v", route, err, tc.want)
				}
			}
		})
	}
}

// On the caller's own unscheduled workspace, only a token of that tenant's own links is a no-op.
func TestDeletionLink_AnUnscheduledWorkspaceRefusesATokenItNeverIssued(t *testing.T) {
	w := newLinkWorld(t)
	outsider := newInvWorld(t, "Elsewhere Ltd", "Olu Eze")
	for name, token := range map[string]string{
		"another workspace's token": w.tokB,
		"a garbage token":           freshToken(t),
		"a malformed token":         "nope",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.CancelWorkspaceDeletionByLink(outsider.adminCtx(), token); !errors.Is(err, ErrDeletionLinkNotValid) {
				t.Errorf("err = %v, want ErrDeletionLinkNotValid", err)
			}
		})
	}
	w.requireLocked(t)

	if err := w.cancelByRoute(w.adminCtx()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), freshToken(t)); !errors.Is(err, ErrDeletionLinkNotValid) {
		t.Errorf("garbage token on the unscheduled own workspace: err = %v, want ErrDeletionLinkNotValid", err)
	}
}

func TestDeletionLink_ALinkOfAnEarlierDeletionIsVoid(t *testing.T) {
	w := newLinkWorld(t)
	oldB := w.tokB
	if err := w.cancelByRoute(w.adminCtx()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	w.schedule(t)

	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), oldB); !errors.Is(err, ErrDeletionLinkNotValid) {
		t.Errorf("the earlier deletion's token: err = %v, want ErrDeletionLinkNotValid", err)
	}
	w.requireLocked(t)
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); err != nil {
		t.Errorf("the new deletion's token: %v, want nil", err)
	}
}

func TestDeletionLink_AFailedAuditKeepsTheLockAndTheLink(t *testing.T) {
	w := newLinkWorld(t)
	drop := failAudit(t, w.super, w.tenant, evCancelled)

	_, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB)
	if err == nil {
		t.Fatal("CancelWorkspaceDeletionByLink succeeded although its audit write fails")
	}
	requireNoSentinel(t, err)
	if errors.Is(err, ErrDeletionLinkNotValid) {
		t.Errorf("err = %v, want the audit failure, not a refusal", err)
	}
	if c := deletionOf(t, w.super, w.tenant); c.DueAt == nil || !c.DueAt.Equal(w.due) {
		t.Errorf("deletion_due_at = %v, want %v unchanged", c.DueAt, w.due)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}

	drop()
	if _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); err != nil {
		t.Errorf("the same token after the trigger is gone: %v, want nil (the link stayed valid)", err)
	}
}

func TestDeletionLink_TwoConcurrentLinkCancelsAuditOnce(t *testing.T) {
	w := newLinkWorld(t)

	errA, errB := race(
		func() error { _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB); return err },
		func() error { _, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.c), w.tokC); return err },
	)
	if errA != nil || errB != nil {
		t.Errorf("results = %v, %v: want both nil (the loser is a no-op)", errA, errB)
	}
	if c := deletionOf(t, w.super, w.tenant); !c.empty() {
		t.Errorf("deletion columns = %+v, want all NULL", c)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 1 {
		t.Errorf("%s rows = %d, want exactly 1", evCancelled, n)
	}
}

func TestDeletionLink_ATokenAfterThePurgeIsRefused(t *testing.T) {
	w := newLinkWorld(t)
	ctx := context.Background()
	tx, err := w.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, w.tenant); err != nil {
		t.Fatalf("bind the tenant: %v", err)
	}
	var removed int
	if err := tx.QueryRow(ctx, `SELECT public.purge_tenant($1::uuid)`, w.tenant).Scan(&removed); err != nil {
		t.Fatalf("purge_tenant: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the purge: %v", err)
	}
	if removed != 1 {
		t.Fatalf("purge_tenant removed %d tenants, want 1", removed)
	}

	_, err = w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB)
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("a link cancel after the purge: err = %v, want db.ErrNotActiveMember", err)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0", evCancelled, n)
	}
}

// A cancel parked behind the winner's row lock must see the winner's commit, not the schedule it read first.
func TestDeletionLink_ACancelWaitingForTheLockSeesTheWinnersCancel(t *testing.T) {
	w := newLinkWorld(t)
	ctx := context.Background()
	winner, err := w.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = winner.Rollback(ctx) }()
	if _, err := winner.Exec(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR UPDATE`, w.tenant); err != nil {
		t.Fatalf("hold the tenant row: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := w.store.CancelWorkspaceDeletionByLink(w.as(w.b), w.tokB)
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := w.super.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancel never waited for the tenant row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := winner.Exec(ctx,
		`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`, w.tenant); err != nil {
		t.Fatalf("the winner clears the schedule: %v", err)
	}
	if err := winner.Commit(ctx); err != nil {
		t.Fatalf("the winner commits: %v", err)
	}

	if err := <-done; err != nil {
		t.Errorf("the loser's err = %v, want the no-op nil", err)
	}
	if n := auditCount(t, w.app, w.tenant, evCancelled); n != 0 {
		t.Errorf("%s rows = %d, want 0 (the winner here wrote none)", evCancelled, n)
	}
}
