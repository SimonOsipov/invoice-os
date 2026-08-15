// Adversarial coverage for internal/demopolicy, added after the seeder landed:
// the Note vocabulary, the anti-join's two skip cases, a racing second boot, a
// seat that goes unstaffed after sealing, and the properties the seeder writes
// once and nothing else re-reads.
//
// Same two DSNs and the same skip-fails-CI gate as demopolicy_test.go.
package demopolicy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Written as literals, never as the package's own constants: an assertion that
// reads policyName or seedActor moves with whatever it is meant to pin.
const (
	wantPolicyName    = "Company approval policy"
	wantSeedActor     = "system"
	wantRoleMissing   = "workflow role fin_dir not found"
	wantRoleUnstaffed = "workflow role fin_dir has no active holder"
)

// --- extra harness ------------------------------------------------------------

// dropSeat hard-deletes a workflow_roles row and its holders, leaving a tenant
// with no seat at all — the shape newFixture never produces.
func dropSeat(t *testing.T, super *pgxpool.Pool, tenantID, key string) {
	t.Helper()
	ctx := context.Background()
	if _, err := super.Exec(ctx,
		`DELETE FROM workflow_role_members m
		  USING workflow_roles r
		  WHERE m.workflow_role_id = r.id AND r.tenant_id = $1 AND r.key = $2`, tenantID, key); err != nil {
		t.Fatalf("drop holders of %q: %v", key, err)
	}
	if _, err := super.Exec(ctx,
		`DELETE FROM workflow_roles WHERE tenant_id = $1 AND key = $2`, tenantID, key); err != nil {
		t.Fatalf("drop seat %q: %v", key, err)
	}
}

// suspendHolders flips every holder of one seat to a suspended membership,
// which is what turns a satisfiable policy into one nobody can act on.
func suspendHolders(t *testing.T, super *pgxpool.Pool, tenantID, key string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE memberships ms SET status = 'suspended'
		  WHERE ms.tenant_id = $1
		    AND ms.user_id IN (SELECT m.user_id FROM workflow_role_members m
		                        JOIN workflow_roles r ON r.id = m.workflow_role_id
		                       WHERE r.tenant_id = $1 AND r.key = $2)`, tenantID, key)
	if err != nil {
		t.Fatalf("suspend holders of %q: %v", key, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("suspended no holder of %q; the degraded path below would not be exercised", key)
	}
}

// publishForeignPolicy writes a sealed active version under a name this seeder
// never uses — the sticky-human-publish shape.
func publishForeignPolicy(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	var policyID, versionID, rootID string
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, 'Human published policy') RETURNING id::text`,
		tenantID).Scan(&policyID); err != nil {
		t.Fatalf("foreign policy: %v", err)
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id::text`,
		tenantID, policyID).Scan(&versionID); err != nil {
		t.Fatalf("foreign version: %v", err)
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, cond_op, cond_amount)
		 VALUES ($1, $2, 0, 'condition', '>', 100000) RETURNING id::text`,
		tenantID, versionID).Scan(&rootID); err != nil {
		t.Fatalf("foreign root: %v", err)
	}
	for _, s := range []struct{ branch, kind, key string }{
		{"then", "approval", seededRoleKey},
		{"else", "autoapprove", ""},
	} {
		var key *string
		if s.key != "" {
			key = &s.key
		}
		if _, err := super.Exec(ctx,
			`INSERT INTO approval_policy_steps
			        (tenant_id, version_id, parent_step_id, branch, ord, kind, workflow_role_key)
			 VALUES ($1, $2, $3, $4, 0, $5, $6)`,
			tenantID, versionID, rootID, s.branch, s.kind, key); err != nil {
			t.Fatalf("foreign %s step: %v", s.branch, err)
		}
	}
	if _, err := super.Exec(ctx,
		`UPDATE approval_policy_versions SET sealed = true, is_active = true, published_at = now(),
		        published_by = 'ngozi' WHERE id = $1`, versionID); err != nil {
		t.Fatalf("foreign seal: %v", err)
	}
}

// --- specs ---------------------------------------------------------------------

// AC-10. The boot log's whole vocabulary, so "did nothing, but why" is an
// asserted contract rather than a table in a design note. Both role branches are
// errors and both must leave the tenant untouched.
func TestSeed_NoteNamesEveryCauseItCanReport(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	t.Run("tenant absent", func(t *testing.T) {
		res, err := seedTenant(ctx, app, uuid.NewString())
		if err != nil {
			t.Fatalf("an allowlisted uuid with no tenants row must skip, not fail: %v", err)
		}
		if res.Note != "tenant absent" {
			t.Errorf("Note = %q, want %q", res.Note, "tenant absent")
		}
		if res.VersionCreated || res.VersionID != "" || res.BacklogFound != 0 {
			t.Errorf("Result = %+v, want the zero value beside the note", res)
		}
	})

	t.Run("workflow role not found", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy no seat")
		f.addBacklog()
		// Both real demo tenants carry fin_dir, so a leak past RLS reads as a
		// staffed seat here and this case would report the wrong cause.
		dropSeat(t, super, f.tenantID, seededRoleKey)

		res, err := seedTenant(ctx, app, f.tenantID)
		if err == nil {
			t.Fatal("a seat that does not exist must fail the create, not publish a policy nobody can satisfy")
		}
		if res.Note != wantRoleMissing {
			t.Errorf("Note = %q, want %q", res.Note, wantRoleMissing)
		}
		for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_runs"} {
			if n := countRows(t, super, table, f.tenantID); n != 0 {
				t.Errorf("%s holds %d row(s) after the refused create, want 0", table, n)
			}
		}
	})

	t.Run("workflow role has no active holder", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy suspended seat")
		f.addBacklog()
		suspendHolders(t, super, f.tenantID, seededRoleKey)

		res, err := seedTenant(ctx, app, f.tenantID)
		if err == nil {
			t.Fatal("a seat whose every holder is suspended must fail the create")
		}
		if res.Note != wantRoleUnstaffed {
			t.Errorf("Note = %q, want %q — a member-row count cannot tell these two apart", res.Note, wantRoleUnstaffed)
		}
		if n := countRows(t, super, "approval_policy_versions", f.tenantID); n != 0 {
			t.Errorf("the refused create left %d version(s), want 0", n)
		}
	})

	t.Run("policy created then already armed then re-armed", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy note sequence")
		f.addBacklog()

		first, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("first boot: %v", err)
		}
		if first.Note != "policy created; backlog armed" {
			t.Errorf("first boot Note = %q", first.Note)
		}

		steady, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("steady boot: %v", err)
		}
		if steady.Note != "policy already active; backlog already armed" {
			t.Errorf("steady boot Note = %q", steady.Note)
		}

		wipeRuns(t, super, f.tenantID)
		rearm, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("re-arm boot: %v", err)
		}
		if rearm.Note != "policy already active; backlog re-armed" {
			t.Errorf("re-arm boot Note = %q — the post-reset path is the one an operator reads to tell convergence from a no-op", rearm.Note)
		}
	})

	t.Run("an active version this seeder did not write governs", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy foreign publish")
		f.addBacklog()
		publishForeignPolicy(t, super, f.tenantID)

		res, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("a human-published policy must be reported, not refused: %v", err)
		}
		if res.Note != "an active version this seeder did not write governs" {
			t.Errorf("Note = %q — this is the difference between the seeder working and the seeder having been overridden", res.Note)
		}
		if n := countRows(t, super, "approval_policies", f.tenantID); n != 1 {
			t.Errorf("the seeder wrote a second policy alongside the human one (%d total)", n)
		}
		if res.RunsArmed != len(backlogTotals) {
			t.Errorf("armed %d, want %d — the sweep runs under whichever version is active", res.RunsArmed, len(backlogTotals))
		}
	})
}

// The seat resolves on every boot but is fatal only while creating: refusing the
// sweep over staffing that changed after sealing would leave awaiting_approval
// reading counts.validated, which is the failure this package exists to prevent.
// A sealed version's role key cannot be edited, so there is nothing else to do.
func TestSeed_SeatSuspendedAfterSealingDegradesWithoutRefusingTheSweep(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy seat lost after seal")
	reader := f.addSeat("controller", "Controller", "active")
	f.addBacklog()
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	suspendHolders(t, super, f.tenantID, seededRoleKey)
	wipeRuns(t, super, f.tenantID)

	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("a seat lost after sealing must not refuse the sweep: %v", err)
	}
	if res.RunsArmed != len(backlogTotals) {
		t.Errorf("armed %d run(s), want %d", res.RunsArmed, len(backlogTotals))
	}
	if !strings.HasPrefix(res.Note, wantRoleUnstaffed) {
		t.Errorf("Note = %q, want it to lead with %q — the degradation is reported or it is invisible", res.Note, wantRoleUnstaffed)
	}
	if got := rollupFor(t, app, f.tenantID, reader).Totals.AwaitingApproval; got != 3 {
		t.Errorf("awaiting_approval = %d, want 3 — refusing the sweep here is what puts it at counts.validated", got)
	}
}

// The anti-join is the whole idempotence, and both of its excluded states cost
// something different when bypassed.
func TestSeed_AntiJoinSkipsInvoicesThatAlreadyCarryARun(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy anti-join")
	openInv := f.addValidatedInvoice("DEMO-T-AJ-OPEN", &backlogTotals[0])
	approvedInv := f.addValidatedInvoice("DEMO-T-AJ-APPROVED", &backlogTotals[3])
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if state, _, _ := runOf(t, super, openInv); state != "open" {
		t.Fatalf("the above-threshold invoice is %q, want open", state)
	}
	if state, _, _ := runOf(t, super, approvedInv); state != "approved" {
		t.Fatalf("the below-threshold invoice is %q, want approved", state)
	}

	fresh := f.addValidatedInvoice("DEMO-T-AJ-FRESH", &backlogTotals[1])
	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if res.BacklogFound != 1 || res.RunsArmed != 1 {
		t.Errorf("second boot found %d and armed %d, want 1 and 1", res.BacklogFound, res.RunsArmed)
	}
	if _, _, runs := runOf(t, super, fresh); runs != 1 {
		t.Errorf("the new invoice carries %d run(s), want 1", runs)
	}
	for _, prior := range []struct{ label, id string }{
		{"the open invoice", openInv}, {"the approved invoice", approvedInv},
	} {
		if _, _, runs := runOf(t, super, prior.id); runs != 1 {
			t.Errorf("%s carries %d run(s), want 1 — the anti-join excludes it", prior.label, runs)
		}
	}

	// What the anti-join is protecting against, proven rather than reasoned:
	// ArmTx does not catch either of these, and the costs differ.
	t.Run("re-arming an open run raises 23505", func(t *testing.T) {
		err := armOutsideTheAntiJoin(t, app, f.tenantID, openInv)
		if err == nil {
			t.Fatal("a second arm over an open run committed; approval_runs_one_open is what should have stopped it")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Errorf("second arm failed with %v, want SQLSTATE 23505", err)
		}
	})
	t.Run("re-arming an approved run writes a silent second run", func(t *testing.T) {
		if err := armOutsideTheAntiJoin(t, app, f.tenantID, approvedInv); err != nil {
			t.Fatalf("a second arm over an approved run raised %v; one_open is partial, so nothing refuses it", err)
		}
	})
}

// armOutsideTheAntiJoin drives ArmTx directly and always rolls back, so it
// measures what the anti-join prevents without leaving the row behind.
func armOutsideTheAntiJoin(t *testing.T, app *pgxpool.Pool, tenantID, invoiceID string) error {
	t.Helper()
	ctx := context.Background()
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		fp, err := invoice.FingerprintTx(ctx, tx, invoiceID)
		if err != nil {
			return err
		}
		if _, err := approval.ArmTx(ctx, tx, tenantID, invoiceID, fp, seedActor); err != nil {
			return err
		}
		return errAlwaysRollBack
	})
	if errors.Is(err, errAlwaysRollBack) {
		return nil
	}
	return err
}

var errAlwaysRollBack = errors.New("demopolicy qa: roll back")

// A fleet can start more than one invoice container, and both run this at boot.
// approval_policy_versions_one_active is a partial unique index, so the loser
// must fail cleanly and non-fatally rather than crash-loop.
func TestSeed_ConcurrentBootsLeaveOneActiveVersion(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy concurrent boots")
	f.addBacklog()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = seedTenant(context.Background(), app, f.tenantID)
		}(i)
	}
	close(start)
	wg.Wait()

	// A boot that starts after another committed takes the idempotent path and
	// also returns nil, so the count is a floor. What must hold either way is the
	// row shape below: the index refuses the second active version, and the loser
	// rolls back rather than crashing.
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won == 0 {
		t.Errorf("every racing boot failed (%v); one must win", errs)
	}
	for table, want := range map[string]int{
		"approval_policies":        1,
		"approval_policy_versions": 1,
		"approval_policy_steps":    3,
		"approval_runs":            len(backlogTotals),
	} {
		if got := countRows(t, super, table, f.tenantID); got != want {
			t.Errorf("%s holds %d row(s) after the race, want %d — the loser must roll back whole", table, got, want)
		}
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 3 {
		t.Errorf("awaiting_approval = %d after the race, want 3", got)
	}
}

// The race that recurs: after the first deploy the policy always exists, so two
// containers booting together both skip the create and both sweep the same
// backlog. approval_runs_one_open refuses the second arm and ArmTx does not
// catch it, so the losing boot must roll back whole and report — never crash,
// never double-arm.
func TestSeed_ConcurrentRearmsLeaveOneRunPerInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy concurrent re-arm")
	ids := f.addBacklog()
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	wipeRuns(t, super, f.tenantID)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := seedTenant(context.Background(), app, f.tenantID)
			errs[i] = err
			if res.VersionCreated {
				t.Errorf("boot %d created a version; the policy was already active", i)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won == 0 {
		t.Errorf("every racing re-arm failed (%v); the backlog would stay unarmed and the badge would read counts.validated", errs)
	}
	for _, id := range ids {
		if _, _, runs := runOf(t, super, id); runs != 1 {
			t.Errorf("an invoice carries %d run(s) after the race, want 1", runs)
		}
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 3 {
		t.Errorf("awaiting_approval = %d after the race, want 3", got)
	}
	if n := countRows(t, super, "approval_policy_versions", f.tenantID); n != 1 {
		t.Errorf("%d version(s) after the race, want 1", n)
	}
}

// The boot call site bounds Seed because the sweep takes FOR UPDATE row locks
// before app.Run, and an unbounded wait would keep /healthz from ever answering.
// Blocking the sweep on a held lock is the deterministic form of that: the
// deadline must expire mid-transaction, roll the tenant back whole, and leave
// the next boot able to arm the backlog.
func TestSeed_ADeadlineOnAHeldRowLockRollsTheTenantBackWhole(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy held row lock")
	ids := f.addBacklog()
	ctx := context.Background()

	blocker, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the blocking tx: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `SELECT id FROM invoices WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatalf("hold the row lock: %v", err)
	}

	bounded, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	_, err = seedTenant(bounded, app, f.tenantID)
	cancel()
	if err == nil {
		t.Fatal("seedTenant returned nil while another transaction held the row lock; the deadline never bit")
	}
	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps", "approval_runs"} {
		if n := countRows(t, super, table, f.tenantID); n != 0 {
			t.Errorf("%s holds %d row(s) after the expired boot, want 0 — the policy statements ran before the sweep blocked", table, n)
		}
	}

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release the row lock: %v", err)
	}
	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("the boot after the lock cleared: %v", err)
	}
	if res.RunsArmed != len(backlogTotals) {
		t.Errorf("the next boot armed %d, want %d — an expired boot must cost one deploy, not the backlog", res.RunsArmed, len(backlogTotals))
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 3 {
		t.Errorf("awaiting_approval = %d after recovery, want 3", got)
	}
}

// Three properties the seeder writes once and no other test reads back: the name
// e2e/topology/roles.spec.ts's in-house sweep must not match, the system actor
// that keeps a human out of a policy she never touched, and the NULL deadline
// that stops every seeded run rendering overdue two days after the last deploy.
func TestSeed_WritesTheSystemActorTheExactNameAndNoDeadline(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy written facts")
	f.addBacklog()

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	names := policyNames(t, super, f.tenantID)
	if len(names) != 1 || names[0] != wantPolicyName {
		t.Errorf("policy names = %v, want exactly [%q] — e2e/topology/roles.spec.ts records this literal", names, wantPolicyName)
	}
	versions := versionsOf(t, super, f.tenantID)
	if len(versions) != 1 {
		t.Fatalf("%d version(s), want 1", len(versions))
	}
	if versions[0].PublishedBy == nil {
		t.Errorf("published_by is NULL, want %q", wantSeedActor)
	} else if *versions[0].PublishedBy != wantSeedActor {
		t.Errorf("published_by = %q, want %q — PublishPolicy would have stamped a human who never touched it",
			*versions[0].PublishedBy, wantSeedActor)
	}

	var steps, dated int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE due_at IS NOT NULL)
		   FROM approval_run_steps WHERE tenant_id = $1`, f.tenantID).Scan(&steps, &dated); err != nil {
		t.Fatalf("read approval_run_steps: %v", err)
	}
	if steps == 0 {
		t.Fatal("no run steps materialised, so the deadline assertion below proves nothing")
	}
	if dated != 0 {
		t.Errorf("%d of %d run step(s) carry a due_at; an SLA renders every seeded run overdue two days after the last deploy", dated, steps)
	}

	var audits int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log
		  WHERE tenant_id = $1 AND event = 'approval_policy.published' AND actor = $2`,
		f.tenantID, wantSeedActor).Scan(&audits); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if audits != 1 {
		t.Errorf("audit_log holds %d approval_policy.published row(s) by %q, want 1", audits, wantSeedActor)
	}
}

// A tenant with an active policy and nothing validated is a no-op, not an error:
// the sweep must tolerate an empty backlog on the boot that creates the policy.
func TestSeed_ActivePolicyWithNoValidatedInvoicesArmsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy empty backlog")
	ctx := context.Background()

	if n := validatedCount(t, super, f.tenantID); n != 0 {
		t.Fatalf("the fixture holds %d validated invoice(s); this case needs none", n)
	}

	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("an empty backlog must not fail the boot: %v", err)
	}
	if !res.VersionCreated {
		t.Error("VersionCreated = false; the policy is written whether or not anything is armable")
	}
	if res.BacklogFound != 0 || res.RunsArmed != 0 {
		t.Errorf("found %d and armed %d, want 0 and 0", res.BacklogFound, res.RunsArmed)
	}
	if n := countRows(t, super, "approval_policy_versions", f.tenantID); n != 1 {
		t.Errorf("%d version(s), want 1", n)
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 0 {
		t.Errorf("awaiting_approval = %d with nothing validated, want 0", got)
	}
}
