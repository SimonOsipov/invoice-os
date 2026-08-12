package approval

// QA: adversarial coverage for Store.PublishPolicy, beyond the eleven acceptance criteria
// in policy_publish_test.go.
//
// Four defects the AC set could not see, each pinned here after a mutation survived the
// whole suite: the approval_policies row lock (drop FOR UPDATE and nothing died), the
// admin gate (never called and nothing died), the 23505 -> ErrConflict mapping (point
// uniqueViolationOn at a different constraint and nothing died), and deleted_at IS NULL
// on the resolution (drop it and nothing died).
//
// The lock and the conflict proofs need a controlled interleaving. Each opens a second
// session that holds a lock and does not commit, then polls pg_blocking_pids until the
// publish under test is observably waiting on THAT session's backend. The poll is an
// outcome oracle, not a timing one: a publish that returns while the lock is held cannot
// have taken it, so the racing branch fails immediately rather than after a sleep, and the
// bounded deadline only ever converts a hang into a readable failure.
//
// Publishing twice inside ONE transaction is not constructible through this seam and is
// deliberately not faked: PublishPolicy owns its transaction end to end, so a second call
// is a second transaction, which is TestPublish_SecondPublishIsNothingToPublish.

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- interleaving harness -----------------------------------------------------

// lockWaitDeadline bounds the poll below. Reaching it means the publish never blocked and
// never returned, which is a failure however long the wait.
const lockWaitDeadline = 20 * time.Second

// backendPID is the pid serving tx. Every wait assertion is attributed to this pid, so a
// suite running beside other worktrees on the same cluster cannot see someone else's
// contention and call it ours.
func backendPID(t *testing.T, ctx context.Context, tx pgx.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read backend pid: %v", err)
	}
	return pid
}

// blockerTx opens an app-role transaction with the tenant GUC set — the app role, not the
// superuser, because these tables are FORCE RLS and a superuser session would contend
// through a different visibility.
func blockerTx(t *testing.T, ctx context.Context, app *pgxpool.Pool, tenantID string) (pgx.Tx, int) {
	t.Helper()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	setTenantGUC(t, ctx, tx, tenantID)
	return tx, backendPID(t, ctx, tx)
}

// waitUntilBlockedBy returns once some backend is waiting on a lock held by holderPID. It
// fails the moment done fires instead: the held lock cannot be acquired, so a call that
// finished never asked for it.
func waitUntilBlockedBy(t *testing.T, super *pgxpool.Pool, holderPID int, done <-chan error, what string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(lockWaitDeadline)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("%s returned (err = %v) while the row was locked by pid %d — it never took "+
				"the lock, so a concurrent draft write can rewrite steps under the version it "+
				"just sealed and die 23001", what, err, holderPID)
		case <-deadline:
			t.Fatalf("%s neither blocked on pid %d nor returned within %s", what, holderPID, lockWaitDeadline)
		case <-tick.C:
			var n int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM pg_stat_activity
				  WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))`,
				holderPID).Scan(&n); err != nil {
				t.Fatalf("poll pg_blocking_pids: %v", err)
			}
			if n > 0 {
				return
			}
		}
	}
}

// awaitPublish takes the result once the blocker has let go.
func awaitPublish(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(lockWaitDeadline):
		t.Fatal("PublishPolicy did not return after the blocking transaction ended")
		return nil
	}
}

// publishInBackground runs one publish and reports its error exactly once.
func publishInBackground(store *Store, ctx context.Context, policyID string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := store.PublishPolicy(ctx, policyID)
		done <- err
	}()
	return done
}

// --- THE lock -----------------------------------------------------------------

// TestPublish_WaitsForTheLockedPolicyRow: publish writes approval_policy_versions and
// nothing else, so the only thing serialising it against a draft write is the FOR UPDATE
// on approval_policies. Dropping that FOR UPDATE leaves every acceptance-criteria spec
// green, and a concurrent PutDraft then rewrites steps under the version publish just
// sealed and dies 23001.
//
// Both holders are real statements from the two writers publish races: PutDraft's opening
// SELECT ... FOR UPDATE, and the UPDATE of the policy row that PutDraft and the soft
// delete both issue. Neither is simulated with a lock publish would not meet.
func TestPublish_WaitsForTheLockedPolicyRow(t *testing.T) {
	holders := []struct {
		name string
		sql  string
	}{
		{"a draft write holding SELECT FOR UPDATE", `SELECT id FROM approval_policies WHERE id = $1 FOR UPDATE`},
		{"a writer updating the policy row", `UPDATE approval_policies SET name = name WHERE id = $1`},
	}
	for _, h := range holders {
		t.Run(h.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			ctx := context.Background()
			tenantID := policyTenant(t, super, "APPR-05 publish-lock "+h.name)
			c, _ := activeAdmin(t, super, tenantID)

			seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID, _ := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

			blocker, holderPID := blockerTx(t, ctx, app, tenantID)
			if _, err := blocker.Exec(ctx, h.sql, policyID); err != nil {
				t.Fatalf("blocker %s: %v", h.name, err)
			}

			done := publishInBackground(NewStore(app, stubFingerprinter), c, policyID)
			waitUntilBlockedBy(t, super, holderPID, done, "PublishPolicy")

			// Still a draft while the writer holds the row: the publish is waiting, not
			// racing ahead of it.
			if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
				t.Errorf("version row = %+v while the policy row is locked, want still "+
					"(sealed false, is_active false)", v)
			}

			if err := blocker.Rollback(ctx); err != nil {
				t.Fatalf("release the blocker: %v", err)
			}
			if err := awaitPublish(t, done); err != nil {
				t.Fatalf("PublishPolicy after the lock was released: %v, want success", err)
			}
			if v := versionRow(t, super, versionID); !v.Sealed || !v.IsActive {
				t.Errorf("version row = %+v, want (sealed true, is_active true)", v)
			}
		})
	}
}

// TestPublish_SamePolicyConcurrentPublishesSerialise pins the reason the version row needs
// no FOR UPDATE of its own: the policy row lock already orders same-policy publishers, and
// the loser's draft resolution then runs on a fresh statement snapshot and finds the
// version sealed. The loser is ErrPolicyNothingToPublish — never ErrConflict, which would
// mean both had passed the resolution and raced for the tenant's active slot.
func TestPublish_SamePolicyConcurrentPublishesSerialise(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-same-policy-race")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, _ := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

	// A closed channel releases both goroutines at once, so they overlap rather than run
	// back to back. The assertions hold in every interleaving, overlapping or not.
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := store.PublishPolicy(c, policyID)
			results <- err
		}()
	}
	close(start)

	published, nothingToPublish := 0, 0
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			published++
		case errors.Is(err, ErrPolicyNothingToPublish):
			nothingToPublish++
		case errors.Is(err, ErrConflict):
			t.Errorf("a same-policy publisher answered ErrConflict — both passed the draft "+
				"resolution, so the policy row lock did not order them: %v", err)
		default:
			t.Errorf("concurrent PublishPolicy: err = %v, want nil or ErrPolicyNothingToPublish", err)
		}
	}
	if published != 1 || nothingToPublish != 1 {
		t.Errorf("outcomes = (%d published, %d nothing-to-publish), want exactly (1, 1)",
			published, nothingToPublish)
	}

	if v := versionRow(t, super, versionID); !v.Sealed || !v.IsActive {
		t.Errorf("version row = %+v, want (sealed true, is_active true)", v)
	}
	if n := len(versionRows(t, super, policyID)); n != 1 {
		t.Errorf("policy has %d versions, want 1 — a losing publish mints nothing", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 1 {
		t.Errorf("approval_policy.published audit rows = %d, want exactly 1", n)
	}
}

// --- the concurrent-publish loser ---------------------------------------------

// TestPublish_LosingTheActiveSlotIsErrConflict drives the 23505 the design promises as a
// 409. The competitor claims the tenant's one_active slot in an uncommitted transaction,
// so the publish under test passes its own deactivation (which cannot see an uncommitted
// row) and then waits on the unique index; committing the competitor turns that wait into
// the constraint violation.
//
// Mapping is by constraint NAME: uniqueViolationOn pointed at any other constraint leaves
// the whole acceptance-criteria suite green and answers 500 here.
func TestPublish_LosingTheActiveSlotIsErrConflict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := policyTenant(t, super, "APPR-05 publish-loses-the-slot")
	c, _ := activeAdmin(t, super, tenantID)

	competitorPolicy := seedApprovalPolicy(t, super, tenantID, "Competitor")
	competitorVersion := seedApprovalDraft(t, super, tenantID, competitorPolicy)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	blocker, holderPID := blockerTx(t, ctx, app, tenantID)
	if _, err := blocker.Exec(ctx,
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true, published_at = now(), published_by = $2
		  WHERE id = $1`, competitorVersion, "competing-publisher"); err != nil {
		t.Fatalf("competitor claims the active slot: %v", err)
	}

	done := publishInBackground(NewStore(app, stubFingerprinter), c, policyID)
	waitUntilBlockedBy(t, super, holderPID, done, "PublishPolicy")
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("commit the competitor: %v", err)
	}

	err := awaitPublish(t, done)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PublishPolicy losing the active slot: err = %v, want ErrConflict", err)
	}
	if status, msg := policyStatusForErr(err); status != http.StatusConflict ||
		msg != "another version was published first — reload the policy and try again" {
		t.Errorf("policyStatusForErr = (%d, %q), want (409, the policy-domain message)", status, msg)
	}

	// Nothing written, and no retry: a retry would have deactivated the competitor and
	// taken the slot with a tree this caller never re-validated.
	v := versionRow(t, super, versionID)
	if v.Sealed || v.IsActive {
		t.Errorf("the losing version = %+v, want still (sealed false, is_active false)", v)
	}
	if v.PublishedAt != nil || v.PublishedBy != nil {
		t.Errorf("the losing version's published_at/by = (%s, %s), want both NULL",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 1 || ids[0] != competitorVersion {
		t.Errorf("active version ids = %v, want exactly [%s] — the loser must not retry",
			ids, competitorVersion)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
}

// TestPublish_RoleDeletedWhileTheSealWaitsStillSeals: the gate reads live roles at its own
// statement and is never re-run, so a role deleted after it passes cannot retract a
// publish already under way. The result is AC-8's state reached through a race — a sealed,
// active version whose step names a dead role — and specifically not a late refusal or a
// raw Postgres error.
func TestPublish_RoleDeletedWhileTheSealWaitsStillSeals(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := policyTenant(t, super, "APPR-05 publish-role-dies-mid-seal")
	c, _ := activeAdmin(t, super, tenantID)

	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	holderPolicy := seedApprovalPolicy(t, super, tenantID, "Slot holder")
	holderVersion := seedApprovalDraft(t, super, tenantID, holderPolicy)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, stepID := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "tax-reviewer")

	blocker, holderPID := blockerTx(t, ctx, app, tenantID)
	if _, err := blocker.Exec(ctx,
		`UPDATE approval_policy_versions SET sealed = true, is_active = true WHERE id = $1`,
		holderVersion); err != nil {
		t.Fatalf("hold the active slot: %v", err)
	}

	done := publishInBackground(NewStore(app, stubFingerprinter), c, policyID)
	waitUntilBlockedBy(t, super, holderPID, done, "PublishPolicy")

	// The gate has already run and passed; the role dies while the seal waits.
	softDeleteWorkflowRole(t, super, roleID)
	before := stepSnapshot(t, super, stepID)

	// Rolled back, not committed: the slot is free again, so the publish proceeds rather
	// than hitting the 23505 TestPublish_LosingTheActiveSlotIsErrConflict covers.
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release the slot: %v", err)
	}
	if err := awaitPublish(t, done); err != nil {
		t.Fatalf("PublishPolicy: %v, want success — the gate is not re-run after it passes", err)
	}

	v := versionRow(t, super, versionID)
	if !v.Sealed || !v.IsActive {
		t.Errorf("version row = %+v, want (sealed true, is_active true)", v)
	}
	if after := stepSnapshot(t, super, stepID); after != before {
		t.Errorf("step snapshot = %s, want the unchanged %s", after, before)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 1 {
		t.Errorf("approval_policy.published audit rows = %d, want 1", n)
	}
}

// --- the admin gate -----------------------------------------------------------

// TestPublish_NeedsAnActiveAdmin: publish is a write, so it carries CreatePolicy's gate.
// Removing requireActiveAdmin from the publish path left every acceptance-criteria spec
// green, because all of them call it as an active admin.
func TestPublish_NeedsAnActiveAdmin(t *testing.T) {
	callers := []struct{ name, role, status string }{
		{"a preparer", "preparer", "active"},
		{"a suspended admin", "admin", "suspended"},
	}
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			super, _ := dbTestPools(t)
			tenantID := policyTenant(t, super, "APPR-05 publish-permission "+tc.name)
			c, _ := callerCtx(t, super, tenantID, tc.role, tc.status)

			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedApprovalDraft(t, super, tenantID, policyID)

			traced, rec := tracedAppPool(t)
			rec.reset()
			_, err := NewStore(traced, stubFingerprinter).PublishPolicy(c, policyID)
			if !errors.Is(err, ErrNotPermitted) {
				t.Errorf("PublishPolicy as %s: err = %v, want ErrNotPermitted", tc.name, err)
			}
			if sql := rec.mentioning("memberships"); len(sql) == 0 {
				t.Error("no memberships statement was issued — requireActiveAdmin did not run")
			}
			if sql := rec.mentioning("approval_policies"); len(sql) != 0 {
				t.Errorf("the refused caller still read %d approval_policies statements: %v — the "+
					"gate must precede every policy row read", len(sql), sql)
			}
			if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
				t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
				t.Errorf("approval_policy.published audit rows = %d, want 0", n)
			}
		})
	}

	// Control: the same store publishes for an active admin, so the refusals above are not
	// a store that refuses everyone.
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-permission control")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraft(t, super, tenantID, policyID)
	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("control: PublishPolicy as an active admin: %v, want success", err)
	}
}

// --- what the resolution refuses ----------------------------------------------

// TestPublish_UnknownMalformedAndDeletedAreNotFound: all three answer 404, never a 400 that
// would confirm the id's shape and never a raw 22P02 that answers 500. The soft-deleted
// case is the one the delete subtask makes reachable: dropping deleted_at IS NULL from the
// resolution leaves every other spec green.
func TestPublish_UnknownMalformedAndDeletedAreNotFound(t *testing.T) {
	super, appPool := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-not-found")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(appPool, stubFingerprinter)

	for _, id := range []string{"not-a-uuid", "", "11111111-1111-1111-1111-111111111111"} {
		_, err := store.PublishPolicy(c, id)
		if !errors.Is(err, ErrPolicyNotFound) {
			t.Errorf("PublishPolicy(%q): err = %v, want ErrPolicyNotFound", id, err)
		}
		if code := pgCode(err); code != "" {
			t.Errorf("PublishPolicy(%q) surfaced a raw Postgres error (SQLSTATE %s) — it answers "+
				"500, not 404", id, code)
		}
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)
	softDeleteApprovalPolicy(t, super, policyID)

	if _, err := store.PublishPolicy(c, policyID); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("PublishPolicy of a soft-deleted policy: err = %v, want ErrPolicyNotFound", err)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("the deleted policy's version = %+v, want still (sealed false, is_active false)", v)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
}

// TestPublish_DeactivatesAnActiveVersionUnderASoftDeletedPolicy: the deactivation predicate
// is is_active alone — no policy id, and no deleted_at either. A soft-deleted policy that
// still holds the tenant's slot must lose it, or the incoming publish would raise 23505
// against a policy no read can even see.
func TestPublish_DeactivatesAnActiveVersionUnderASoftDeletedPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-over-a-deleted-policy")
	c, _ := activeAdmin(t, super, tenantID)

	deletedPolicy := seedApprovalPolicy(t, super, tenantID, "Retired")
	deletedVersion := seedApprovalPolicyVersionN(t, super, tenantID, deletedPolicy, 1)
	publishApprovalPolicyVersion(t, super, deletedVersion, "earlier-publisher")
	softDeleteApprovalPolicy(t, super, deletedPolicy)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy while a soft-deleted policy holds the slot: %v, want success", err)
	}
	if v := versionRow(t, super, deletedVersion); !v.Sealed || v.IsActive {
		t.Errorf("the deleted policy's version = %+v, want (sealed true, is_active false)", v)
	}
	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 1 || ids[0] != versionID {
		t.Errorf("active version ids = %v, want exactly [%s]", ids, versionID)
	}
}

// --- the gate, below the root -------------------------------------------------

// TestPublish_GateRecursesIntoConditionLanes: a condition whose lanes are populated
// satisfies the branch rule and is then walked, so an approval naming a dead role inside a
// lane is refused with the step-role sentinel. A gate that checked only root steps would
// publish this.
func TestPublish_GateRecursesIntoConditionLanes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-gate-recursion")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	deadRole := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, super, deadRole)
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("tax-reviewer"),
	})

	_, err := store.PublishPolicy(c, policyID)
	if !errors.Is(err, ErrPolicyStepRole) {
		t.Errorf("PublishPolicy with a dead role inside a lane: err = %v, want ErrPolicyStepRole", err)
	}
	if errors.Is(err, ErrPolicyEmptyBranches) {
		t.Errorf("PublishPolicy: err = %v, want the step-role sentinel — the lane is populated", err)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
	}

	// Positive control: the same nesting with a live role publishes, so the refusal is the
	// role rule and not the recursion refusing every nested step.
	livePolicyID := seedApprovalPolicy(t, super, tenantID, "Live sign-off")
	liveVersionID := seedApprovalDraft(t, super, tenantID, livePolicyID)
	liveCondID := seedApprovalPolicyStepInLane(t, super, tenantID, liveVersionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, liveVersionID, seedStepSpec{
		ParentStepID: &liveCondID, Branch: ptr("then"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("engagement-partner"),
	})
	if _, err := store.PublishPolicy(c, livePolicyID); err != nil {
		t.Fatalf("control: PublishPolicy with a live role inside a lane: %v, want success", err)
	}
}

// --- the response -------------------------------------------------------------

// TestPublish_AnswersTheSealedTree: the 200 carries the whole nested tree, not just the
// version bits. The acceptance-criteria set only asserts the tree of an EMPTY policy, so a
// publish that answered steps: [] on a populated policy would pass all of it.
func TestPublish_AnswersTheSealedTree(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-answers-the-tree")
	c, adminID := activeAdmin(t, super, tenantID)

	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("then"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("engagement-partner"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})

	got, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID)
	if err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	if len(got.Steps) != 2 {
		t.Fatalf("returned tree has %d root steps, want 2: %+v", len(got.Steps), got.Steps)
	}
	if got.Steps[0].Kind != "condition" || got.Steps[1].Kind != "notify" {
		t.Errorf("root kinds = (%q, %q), want (condition, notify) in ord order",
			got.Steps[0].Kind, got.Steps[1].Kind)
	}
	if len(got.Steps[0].Then) != 1 || got.Steps[0].Then[0].Kind != "approval" {
		t.Fatalf("the condition's then lane = %+v, want one approval step", got.Steps[0].Then)
	}
	if key := got.Steps[0].Then[0].WorkflowRoleKey; key == nil || *key != "engagement-partner" {
		t.Errorf("the nested approval's role key = %s, want %q", strOrNull(key), "engagement-partner")
	}
	if len(got.Steps[0].Else) != 0 {
		t.Errorf("the condition's else lane = %+v, want empty and non-nil", got.Steps[0].Else)
	}
	if !got.Sealed || got.Status != "published" || got.Version != 1 {
		t.Errorf("returned policy = (version %d, sealed %v, status %q), want (1, true, published)",
			got.Version, got.Sealed, got.Status)
	}
	if len(got.Versions) != 1 {
		t.Fatalf("returned versions = %+v, want exactly one", got.Versions)
	}
	if pv := got.Versions[0]; !pv.Sealed || !pv.IsActive || pv.PublishedAt == nil ||
		pv.PublishedBy == nil || *pv.PublishedBy != adminID {
		t.Errorf("returned versions[0] = %+v, want sealed, active and stamped with %q", pv, adminID)
	}
}

// --- AC-4 (task-484): the cap boundary is > sweepCap, never >= sweepCap --------

// TestPublish_SweepAtExactlyCapSucceeds: exactly sweepCap (5,000) validated invoices
// publish and arm all 5,000 — the refusal in TestPublish_SweepAboveCapReturns409 only
// fires one row past this. Measured ~5.2s end to end (5,000 real ArmTx calls at
// ~0.9ms/arm, 5,000 runs + 5,000 steps, dev :5433). Uses policyTenant and registers
// NO invoice cleanup of its own: policyTenant already registers
// teardownSealedApprovalFixture AFTER seedTenant's, so LIFO deletes
// approval_run_steps -> approval_runs before the tenant cascade reaches invoices. A
// second t.Cleanup deleting invoices would run FIRST under LIFO and die 23001 on
// approval_runs_tenant_invoice_fk (observed).
func TestPublish_SweepAtExactlyCapSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-sweep-at-cap")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "At-cap Corp")
	bulkSeedValidatedInvoices(t, super, tenantID, entityID, "at-cap", sweepCap)

	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, roleID, member, 0)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy at exactly the sweep cap: %v, want success", err)
	}

	if n := rowCount(t, super, "approval_runs", tenantID); n != sweepCap {
		t.Errorf("approval_runs rows = %d, want %d — every validated invoice at exactly the cap must arm", n, sweepCap)
	}
	if n := rowCount(t, super, "approval_run_steps", tenantID); n != sweepCap {
		t.Errorf("approval_run_steps rows = %d, want %d", n, sweepCap)
	}
}

// --- AC-2 (task-484): an existing open-or-approved run is not a candidate ------

// TestPublish_SweepSkipsInvoicesWithAnApprovedRun: one validated invoice already
// carries a run closed 'approved' (the autoapprove/empty-policy shape D39 documents),
// beside a validated invoice with no run at all -> PublishPolicy sweeps only the
// second. The first keeps exactly its one pre-existing run — pinning the anti-join's
// state IN ('open','approved') leg, not just its 'open' leg.
func TestPublish_SweepSkipsInvoicesWithAnApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-sweep-skips-approved")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Skip Corp")

	alreadyArmedID := seedInvoice(t, super, tenantID, entityID, "sweep-skip-already-armed")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated' WHERE id = $1`, alreadyArmedID); err != nil {
		t.Fatalf("validate invoice: %v", err)
	}
	priorPolicyID := seedApprovalPolicy(t, super, tenantID, "Prior policy")
	priorVersionID := seedApprovalPolicyVersionN(t, super, tenantID, priorPolicyID, 1)
	existingRunID := seedApprovalRun(t, super, tenantID, alreadyArmedID, priorVersionID)
	closeApprovalRunFor(t, super, existingRunID, "approved", "system")

	candidateID := seedInvoice(t, super, tenantID, entityID, "sweep-skip-candidate")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated' WHERE id = $1`, candidateID); err != nil {
		t.Fatalf("validate invoice: %v", err)
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	if n := approvalRunCountForInvoice(t, super, alreadyArmedID); n != 1 {
		t.Errorf("approval_runs rows for the already-armed invoice = %d, want still exactly 1 — it must not be re-armed", n)
	}
	if n := approvalRunCountForInvoice(t, super, candidateID); n != 1 {
		t.Errorf("approval_runs rows for the candidate invoice = %d, want 1", n)
	}
}

// --- AC-5 (task-484): a nil Fingerprinter fails closed, never writes '' --------

// TestPublish_NilFingerprinterFailsRatherThanWritingEmpty: NewStore(app, nil) over a
// tenant with one validated invoice must fail the publish rather than arm a run with
// an empty content_fingerprint — D31's fail-closed rule. Positive control in the SAME
// fixture: a second, freshly-drafted policy published through a store built with
// stubFingerprinter succeeds and arms one run, so the refusal above cannot be "this
// store always refuses."
func TestPublish_NilFingerprinterFailsRatherThanWritingEmpty(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-nil-fingerprinter")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Nil-fingerprinter Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "nil-fp-invoice-1")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("validate invoice: %v", err)
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app, nil).PublishPolicy(c, policyID); err == nil {
		t.Fatal("PublishPolicy with a nil Fingerprinter and a non-empty candidate set succeeded, want an error")
	}
	if n := approvalRunCountForInvoice(t, super, invoiceID); n != 0 {
		t.Errorf("approval_runs rows = %d, want 0 — a nil Fingerprinter must not write an empty fingerprint", n)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false) — the whole publish must roll back", v)
	}

	// Positive control: the same fixture, a second policy, a store built with
	// stubFingerprinter — publishes and arms one run.
	controlPolicyID := seedApprovalPolicy(t, super, tenantID, "Control sign-off")
	seedApprovalDraft(t, super, tenantID, controlPolicyID)
	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, controlPolicyID); err != nil {
		t.Fatalf("control: PublishPolicy with stubFingerprinter: %v, want success — the refusal above "+
			"is vacuous unless this succeeds", err)
	}
	if n := approvalRunCountForInvoice(t, super, invoiceID); n != 1 {
		t.Errorf("control: approval_runs rows = %d, want 1", n)
	}
}

// --- AC-1 (task-484): the sweep writes what the Fingerprinter returns, never '' ---

// hexFingerprintPattern is the shape a real content_fingerprint must have: 64
// lowercase hex chars.
var hexFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestPublish_SweepFingerprintIsARealHash: the in-package companion to
// TestPublish_SweepFingerprintMatchesInvoiceContent (internal/invoice; D1 — this
// package cannot import internal/invoice). Proves the sweep passes stubFingerprinter's
// return value through to approval_runs.content_fingerprint verbatim: 64 lowercase hex
// chars, never "". The real-content proof — that the value equals the invoice's actual
// computed hash — lives in the internal/invoice test; this one only pins the plumbing.
func TestPublish_SweepFingerprintIsARealHash(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-sweep-fingerprint-shape")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Fingerprint-shape Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "fp-shape-invoice-1")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("validate invoice: %v", err)
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app, stubFingerprinter).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	var got string
	if err := super.QueryRow(context.Background(),
		`SELECT content_fingerprint FROM approval_runs WHERE invoice_id = $1`, invoiceID,
	).Scan(&got); err != nil {
		t.Fatalf("read approval_runs.content_fingerprint: %v", err)
	}
	if got == "" {
		t.Fatal(`content_fingerprint = "", want the Fingerprinter's return value`)
	}
	if !hexFingerprintPattern.MatchString(got) {
		t.Errorf("content_fingerprint = %q, want 64 lowercase hex chars", got)
	}
	if got != stubFingerprintValue {
		t.Errorf("content_fingerprint = %q, want the stub's literal %q — the sweep must pass the "+
			"Fingerprinter's return through verbatim", got, stubFingerprintValue)
	}
}
