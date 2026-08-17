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
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
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

// publishStaleOwnPolicy seeds the tenant with demopolicy's OWN pre-notify
// shape: three steps, no Q10 notify — sealed, active, published_by "system".
// Written out rather than by calling publishSeedPolicy, which now writes the
// CURRENT plan and would leave every supersede case below vacuous. The shape
// guard is what keeps this fixture stale as the plan moves.
func publishStaleOwnPolicy(t *testing.T, app *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	stale := []approval.Step{
		{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("100000.00"),
			Then: []approval.Step{{Kind: "approval", WorkflowRoleKey: ptr(seededRoleKey)}},
			Else: []approval.Step{{Kind: "autoapprove"}}},
	}
	if shapeOf(stale) == shapeOf(inhousePlan.steps) {
		t.Fatal("the stale fixture matches the current in-house plan, so no supersede case here exercises anything")
	}

	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var policyID, versionID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id::text`,
			tenantID, wantPolicyName).Scan(&policyID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
			 VALUES ($1, $2, 1) RETURNING id::text`, tenantID, policyID).Scan(&versionID); err != nil {
			return err
		}
		if err := writeLane(ctx, tx, tenantID, versionID, nil, nil, stale); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE approval_policy_versions
			    SET sealed = true, is_active = true, published_at = now(), published_by = $2
			  WHERE id = $1`, versionID, wantSeedActor)
		return err
	}); err != nil {
		t.Fatalf("publish the stale own policy: %v", err)
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
		// NAME-scoped, not a raw count: ensureDraft still runs unconditionally
		// even when a foreign policy governs, so the tenant now holds the
		// foreign policy PLUS our Executive escalation draft (2 total) — the
		// property this proves is that our NAMED policy never appears, not
		// that nothing else got written.
		names := policyNames(t, super, f.tenantID)
		if len(names) != 2 {
			t.Fatalf("the tenant holds %d named polic(y/ies) %v, want 2 — the foreign policy plus our draft", len(names), names)
		}
		if slices.Contains(names, wantPolicyName) {
			t.Errorf("the seeder wrote its own %q alongside the human one; a foreign active version must leave it alone", wantPolicyName)
		}
		if !slices.Contains(names, "Executive escalation") {
			t.Errorf("names = %v, want the draft \"Executive escalation\" still written even though a foreign policy governs", names)
		}
		if res.RunsArmed != len(backlogTotals) {
			t.Errorf("armed %d, want %d — the sweep runs under whichever version is active", res.RunsArmed, len(backlogTotals))
		}
	})

	t.Run("policy reactivated after a deactivation", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy reactivate note")
		f.addBacklog()

		first, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("first boot: %v", err)
		}
		if _, err := super.Exec(ctx,
			`UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, first.VersionID); err != nil {
			t.Fatalf("deactivate the seeded version: %v", err)
		}

		res, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("reactivate boot: %v", err)
		}
		if res.Note != "policy reactivated; backlog armed" {
			t.Errorf("Note = %q, want %q", res.Note, "policy reactivated; backlog armed")
		}
	})

	t.Run("policy superseded after a shape change", func(t *testing.T) {
		f := newFixture(t, super, app, "demopolicy supersede note")
		f.addBacklog()
		publishStaleOwnPolicy(t, app, f.tenantID)

		res, err := seedTenant(ctx, app, f.tenantID)
		if err != nil {
			t.Fatalf("supersede boot: %v", err)
		}
		if res.Note != "policy superseded; backlog armed" {
			t.Errorf("Note = %q, want %q", res.Note, "policy superseded; backlog armed")
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
		"approval_policies":        2, // the active policy plus the Executive escalation draft
		"approval_policy_versions": 2,
		"approval_policy_steps":    9, // 4 active (with the Q10 notify) + 5 draft (polH2)
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
	if n := countRows(t, super, "approval_policy_versions", f.tenantID); n != 2 {
		t.Errorf("%d version(s) after the race, want 2 (the active version plus the draft)", n)
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

	// Two names now: the active policy plus the Executive escalation draft.
	// versionsOf's version,id order is a tie-break across the two policies
	// (both sit at version 1) — deliberately NOT used here for "the" version;
	// activeVersionOf resolves it deterministically instead.
	names := policyNames(t, super, f.tenantID)
	if len(names) != 2 || !slices.Contains(names, wantPolicyName) {
		t.Errorf("policy names = %v, want exactly 2 including %q — e2e/topology/roles.spec.ts records this literal", names, wantPolicyName)
	}
	active := activeVersionOf(t, super, f.tenantID)
	if active.PublishedBy == nil {
		t.Errorf("published_by is NULL, want %q", wantSeedActor)
	} else if *active.PublishedBy != wantSeedActor {
		t.Errorf("published_by = %q, want %q — PublishPolicy would have stamped a human who never touched it",
			*active.PublishedBy, wantSeedActor)
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
	if n := countRows(t, super, "approval_policy_versions", f.tenantID); n != 2 {
		t.Errorf("%d version(s), want 2 (the active version plus the draft) — the draft is written whether or not anything is armable", n)
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 0 {
		t.Errorf("awaiting_approval = %d with nothing validated, want 0", got)
	}
}

// AC-6. A policy deactivated between boots is REACTIVATED, not duplicated:
// the SAME version comes back active and sealed, one policy row of that name
// survives, and the boot reports it via Note (Result carries no
// VersionReactivated field to assert on directly — Note is the one
// observable channel this suite has for it).
func TestSeed_ReactivatesADeactivatedPolicyRatherThanDuplicatingIt(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy reactivate not duplicate")
	f.addBacklog()
	ctx := context.Background()

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, first.VersionID); err != nil {
		t.Fatalf("deactivate the seeded version: %v", err)
	}
	wipeRuns(t, super, f.tenantID)

	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("reactivate boot: %v", err)
	}
	if res.Note != "policy reactivated; backlog armed" {
		t.Errorf("Note = %q, want %q", res.Note, "policy reactivated; backlog armed")
	}
	if res.VersionID != first.VersionID {
		t.Errorf("reactivate boot reports version %s, want the SAME version %s reactivated, not a new one", res.VersionID, first.VersionID)
	}
	if res.RunsArmed != len(backlogTotals) {
		t.Errorf("reactivate boot armed %d run(s), want %d", res.RunsArmed, len(backlogTotals))
	}

	var namedCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policies WHERE tenant_id = $1 AND name = $2 AND deleted_at IS NULL`,
		f.tenantID, wantPolicyName).Scan(&namedCount); err != nil {
		t.Fatalf("count %q policies: %v", wantPolicyName, err)
	}
	if namedCount != 1 {
		t.Errorf("%d live %q polic(y/ies), want exactly 1 — reactivation must not duplicate", namedCount, wantPolicyName)
	}

	active := activeVersionOf(t, super, f.tenantID)
	if !active.Sealed || !active.IsActive {
		t.Errorf("the reactivated version is sealed=%v is_active=%v, want both true", active.Sealed, active.IsActive)
	}
	if active.ID != first.VersionID {
		t.Errorf("the active version is %s, want the SAME version %s reactivated", active.ID, first.VersionID)
	}
}

// AC-6. A soft-deleted policy must not be resurrected: the boot that finds no
// active version writes a NEW live policy, and the dead row keeps its
// deleted_at. This is a GUARD, true both before and after Stage 3 — today's
// seeder has no name-based reuse at all, so "create fresh when nothing is
// active" already produces this shape; the point is that it must KEEP doing
// so once Stage 3 adds probe-B-by-name (which must itself filter
// deleted_at IS NULL).
func TestSeed_ASoftDeletedSeededPolicyIsNotResurrected(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy soft delete not resurrected")
	f.addBacklog()
	ctx := context.Background()

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	var deadPolicyID string
	if err := super.QueryRow(ctx,
		`SELECT policy_id::text FROM approval_policy_versions WHERE id = $1`, first.VersionID).Scan(&deadPolicyID); err != nil {
		t.Fatalf("read the seeded policy id: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, deadPolicyID); err != nil {
		t.Fatalf("soft-delete the policy: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, first.VersionID); err != nil {
		t.Fatalf("deactivate the deleted policy's version: %v", err)
	}

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("re-seed boot: %v", err)
	}

	var deadStillDeleted bool
	if err := super.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM approval_policies WHERE id = $1`, deadPolicyID).Scan(&deadStillDeleted); err != nil {
		t.Fatalf("re-read the soft-deleted policy: %v", err)
	}
	if !deadStillDeleted {
		t.Error("the soft-deleted policy's deleted_at was cleared; it must stay dead")
	}

	names := policyNames(t, super, f.tenantID) // already scoped deleted_at IS NULL
	if !slices.Contains(names, wantPolicyName) {
		t.Errorf("live policy names = %v, want a NEW live %q", names, wantPolicyName)
	}
	active := activeVersionOf(t, super, f.tenantID)
	if active.ID == first.VersionID {
		t.Error("the active version is the soft-deleted one; a live tenant must not be governed by a dead policy")
	}
	if !active.Sealed || !active.IsActive {
		t.Errorf("the new active version is sealed=%v is_active=%v, want both true", active.Sealed, active.IsActive)
	}
}

// AC-7. A required seat (the firm plan's compliance) with no active holder
// refuses the create entirely: error, Note names it, zero rows.
func TestSeed_RefusesWhenARequiredSeatIsUnstaffed(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFirmFixture(t, super, app, "demopolicy firm refuses")
	f.addBacklog()
	suspendHolders(t, super, f.tenantID, "compliance")
	ctx := context.Background()

	res, err := seedTenantPlan(ctx, app, f.tenantID, firmPlan)
	if err == nil {
		t.Fatal("a required seat (compliance) with no active holder must fail the create")
	}
	if !strings.Contains(res.Note, "compliance") {
		t.Errorf("Note = %q, want it to name compliance", res.Note)
	}
	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_runs"} {
		if n := countRows(t, super, table, f.tenantID); n != 0 {
			t.Errorf("%s holds %d row(s) after the refused create, want 0", table, n)
		}
	}
}

// AC-7. An unstaffed DRAFT seat (ceo, which the in-house tenant does not even
// define as a role) must never refuse the seed — seatProblem takes only the
// plan's REQUIRED seats (fin_dir here), never a draft's.
func TestSeed_AnUnstaffedDraftSeatNeverRefusesTheSeed(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy draft seat never refuses")
	f.addSeat("cfo", "CFO", "suspended")
	f.addBacklog()
	ctx := context.Background()

	res, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("an unstaffed DRAFT seat must never refuse the seed: %v", err)
	}
	if !res.VersionCreated {
		t.Error("VersionCreated = false; the required seat (fin_dir) is staffed")
	}

	versionID := draftVersionOf(t, super, f.tenantID) // fails loudly if the draft is absent
	var ceoSteps int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_steps WHERE version_id = $1 AND workflow_role_key = 'ceo'`,
		versionID).Scan(&ceoSteps); err != nil {
		t.Fatalf("count ceo steps on the draft: %v", err)
	}
	if ceoSteps == 0 {
		t.Error("the draft names no ceo step; this test's premise (an unstaffed DRAFT seat) is not exercised")
	}

	roles, active := activeHolders(t, super, f.tenantID, "ceo")
	if roles != 0 {
		t.Errorf("ceo resolves to %d live workflow_roles row(s), want 0 — this test's premise requires the seat to not exist", roles)
	}
	if active != 0 {
		t.Errorf("ceo has %d active holder(s), want 0", active)
	}
}

// AC-8. Every approval step on every ACTIVE version of both demo tenants
// resolves to a live role with at least one active holder; quality_reviewer
// (the firm's deliberately unstaffed seat) appears on none. FLOORED: without
// a minimum population this loop is vacuously satisfied by zero rows.
func TestSeed_NoActivePolicySeatIsUnstaffedAcrossTheDemoTenants(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	if err := db.Seed(ctx, os.Getenv("DATABASE_SUPERUSER_URL"), dbsql.FS); err != nil {
		t.Fatalf("db.Seed: %v", err)
	}
	for _, tenantID := range []string{inhouseDemoTenantID, firmDemoTenantID} {
		for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps", "approval_runs"} {
			if got := countRows(t, super, table, tenantID); got != 0 {
				t.Fatalf("tenant %s already holds %d %s row(s); this test's teardown would delete rows it did not create", tenantID, got, table)
			}
		}
		id := tenantID
		t.Cleanup(func() { teardownApprovalRows(t, super, id) })
	}

	if _, err := Seed(ctx, app, nil); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var activeVersions int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = ANY($1) AND is_active`,
		[]string{inhouseDemoTenantID, firmDemoTenantID}).Scan(&activeVersions); err != nil {
		t.Fatalf("count active versions: %v", err)
	}
	if activeVersions < 2 {
		t.Fatalf("%d active version(s) across both tenants, want at least 2 — the floor below would be vacuous", activeVersions)
	}

	rows, err := super.Query(ctx,
		`SELECT s.tenant_id::text, s.workflow_role_key FROM approval_policy_steps s
		   JOIN approval_policy_versions v ON v.id = s.version_id
		  WHERE s.tenant_id = ANY($1) AND v.is_active AND s.kind = 'approval'`,
		[]string{inhouseDemoTenantID, firmDemoTenantID})
	if err != nil {
		t.Fatalf("read active-version approval steps: %v", err)
	}
	type pair struct{ tenantID, key string }
	var pairs []pair
	for rows.Next() {
		var tid string
		var k *string
		if err := rows.Scan(&tid, &k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if k == nil {
			t.Fatal("an approval step's workflow_role_key is NULL")
		}
		pairs = append(pairs, pair{tid, *k})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_policy_steps: %v", err)
	}
	if len(pairs) < 5 {
		t.Fatalf("walked %d approval step(s), want at least 5 — the loop below would be vacuous", len(pairs))
	}

	seen := map[pair]bool{}
	for _, p := range pairs {
		if seen[p] {
			continue
		}
		seen[p] = true
		roles, active := activeHolders(t, super, p.tenantID, p.key)
		if roles == 0 {
			t.Errorf("tenant %s: workflow_role_key %q resolves to no live role", p.tenantID, p.key)
		}
		if active == 0 {
			t.Errorf("tenant %s: workflow_role_key %q has no ACTIVE holder", p.tenantID, p.key)
		}
		if p.key == "quality_reviewer" {
			t.Errorf("tenant %s: an active-policy lane names quality_reviewer, an unstaffed seat", p.tenantID)
		}
	}
}

// AC-11. publishSeedPolicy, called directly over a tenant that already holds
// a FOREIGN sealed active version, must clear that slot before activating
// its own — otherwise approval_policy_versions_one_active raises 23505.
func TestSeed_PublishClearsAStrayActiveVersionBeforeActivating(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy publish clears stray")
	publishForeignPolicy(t, super, f.tenantID)
	ctx := context.Background()

	var versionID string
	if err := db.WithinTenantTx(ctx, app, f.tenantID, func(tx pgx.Tx) error {
		var err error
		versionID, err = publishSeedPolicy(ctx, tx, f.tenantID, planFor(f.tenantID))
		return err
	}); err != nil {
		t.Fatalf("publishSeedPolicy over a stray foreign active version: %v", err)
	}

	var activeCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`,
		f.tenantID).Scan(&activeCount); err != nil {
		t.Fatalf("count active versions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("%d active version(s) after publish, want exactly 1", activeCount)
	}
	active := activeVersionOf(t, super, f.tenantID)
	if active.ID != versionID {
		t.Errorf("the active version is %s, want the newly published %s", active.ID, versionID)
	}
}

// AC-11. reactivateSealedVersion, called directly over a tenant whose OWN
// version is deactivated while a FOREIGN version holds the active slot, must
// clear that slot before reactivating ours.
func TestSeed_ReactivateClearsAStrayActiveVersionBeforeActivating(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy reactivate clears stray")
	ctx := context.Background()

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("seed the tenant's own version: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, first.VersionID); err != nil {
		t.Fatalf("deactivate the seeded version: %v", err)
	}
	publishForeignPolicy(t, super, f.tenantID) // now holds the active slot

	if err := db.WithinTenantTx(ctx, app, f.tenantID, func(tx pgx.Tx) error {
		return reactivateSealedVersion(ctx, tx, f.tenantID, first.VersionID)
	}); err != nil {
		t.Fatalf("reactivateSealedVersion over a stray foreign active version: %v", err)
	}

	var oursActive bool
	if err := super.QueryRow(ctx,
		`SELECT is_active FROM approval_policy_versions WHERE id = $1`, first.VersionID).Scan(&oursActive); err != nil {
		t.Fatalf("read the seeded version: %v", err)
	}
	if !oursActive {
		t.Error("the seeded version is inactive, want active after reactivation")
	}

	var strayActive bool
	if err := super.QueryRow(ctx,
		`SELECT coalesce(bool_or(v.is_active), false) FROM approval_policy_versions v
		   JOIN approval_policies p ON p.id = v.policy_id
		  WHERE v.tenant_id = $1 AND p.name = 'Human published policy'`, f.tenantID).Scan(&strayActive); err != nil {
		t.Fatalf("read the foreign version's state: %v", err)
	}
	if strayActive {
		t.Error("the foreign policy's version is still active; reactivation must clear it first")
	}
}

// AC-12. An active version this seeder wrote (published_by "system", our
// name) whose step shape no longer matches the plan is superseded by
// version N+1: v1 stays sealed and inactive, v2 is sealed, active, and
// carries the current shape, and exactly one version is active tenant-wide.
// The second boot proves the numeric(14,2) scale trap: cond_amount always
// renders at 2dp, so an unscaled literal in the comparator never matches and
// the seeder would republish on every boot.
func TestSeed_SupersedesItsOwnStaleActiveVersionWithVersionTwo(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy supersede v2")
	f.addBacklog()
	publishStaleOwnPolicy(t, app, f.tenantID)
	ctx := context.Background()

	var staleVersionID string
	if err := super.QueryRow(ctx,
		`SELECT id::text FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, f.tenantID).
		Scan(&staleVersionID); err != nil {
		t.Fatalf("read the stale active version: %v", err)
	}

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("first seedTenant over a stale own version: %v", err)
	}
	if first.Note != "policy superseded; backlog armed" {
		t.Errorf("Note = %q, want %q", first.Note, "policy superseded; backlog armed")
	}

	scopedVersionCount := func() int {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx,
			`SELECT count(*) FROM approval_policy_versions v JOIN approval_policies p ON p.id = v.policy_id
			  WHERE v.tenant_id = $1 AND p.name = $2`, f.tenantID, wantPolicyName).Scan(&n); err != nil {
			t.Fatalf("count versions of %q: %v", wantPolicyName, err)
		}
		return n
	}

	var policies int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policies WHERE tenant_id = $1 AND name = $2`,
		f.tenantID, wantPolicyName).Scan(&policies); err != nil {
		t.Fatalf("count policies named %q: %v", wantPolicyName, err)
	}
	if policies != 1 {
		t.Errorf("%d %q polic(y/ies), want exactly 1 — a supersede is a NEW VERSION, not a new policy", policies, wantPolicyName)
	}
	if n := scopedVersionCount(); n != 2 {
		t.Errorf("%q carries %d version(s), want 2 (v1 stale + v2 superseding)", wantPolicyName, n)
	}

	var staleSealed, staleActive bool
	if err := super.QueryRow(ctx,
		`SELECT sealed, is_active FROM approval_policy_versions WHERE id = $1`, staleVersionID).
		Scan(&staleSealed, &staleActive); err != nil {
		t.Fatalf("read the stale version: %v", err)
	}
	if !staleSealed || staleActive {
		t.Errorf("v1 (stale) sealed=%v is_active=%v, want sealed=true is_active=false", staleSealed, staleActive)
	}

	active := activeVersionOf(t, super, f.tenantID)
	if active.ID == staleVersionID {
		t.Error("the active version is STILL v1; supersede must publish v2")
	}
	if !active.Sealed {
		t.Error("v2 (active) is not sealed, want sealed")
	}
	if active.PublishedBy == nil || *active.PublishedBy != wantSeedActor {
		t.Errorf("v2's published_by = %v, want %q", active.PublishedBy, wantSeedActor)
	}

	var oneActive int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, f.tenantID).
		Scan(&oneActive); err != nil {
		t.Fatalf("count active versions: %v", err)
	}
	if oneActive != 1 {
		t.Errorf("%d active version(s) tenant-wide, want exactly 1", oneActive)
	}

	got := stepTreeOf(t, super, active.ID)
	notifyCount := 0
	for _, s := range got {
		if s.Kind == "notify" {
			notifyCount++
		}
	}
	if notifyCount != 1 {
		t.Errorf("v2 carries %d notify step(s), want exactly 1 — the shape supersede exists to fix", notifyCount)
	}

	second, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("second seedTenant: %v", err)
	}
	if second.Note == "policy superseded; backlog armed" {
		t.Error("the second boot superseded again; a matching shape must write nothing — this is the numeric(14,2) scale trap")
	}
	if n := scopedVersionCount(); n != 2 {
		t.Errorf("%q carries %d version(s) after the second boot, want still 2 — no v3", wantPolicyName, n)
	}
}

// AC-12. The three supersede guards, table-driven with the control in the
// SAME test: (a) published by a human and (b) a different policy entirely
// both pass vacuously today (today's seeder has no supersede logic at all,
// so it never touches either), so the CONTROL row (c) — system-published,
// our name, a stale shape — must live here too, or this test proves nothing.
// Counts are scoped to policies named wantPolicyName specifically: the
// in-house plan's Executive escalation draft is written unconditionally by
// every case here and must not be mistaken for a supersede.
func TestSeed_NeverSupersedesAVersionItDidNotPublish(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	cases := []struct {
		name         string
		setup        func(t *testing.T, f *fixture)
		wantVersions int
	}{
		{
			name: "published by a human",
			setup: func(t *testing.T, f *fixture) {
				publishStaleOwnPolicy(t, app, f.tenantID)
				if _, err := super.Exec(ctx,
					`UPDATE approval_policy_versions SET published_by = 'ngozi'
					   WHERE tenant_id = $1 AND is_active`, f.tenantID); err != nil {
					t.Fatalf("stamp a human publisher: %v", err)
				}
			},
			wantVersions: 1, // ours, untouched
		},
		{
			name: "a different policy entirely",
			setup: func(t *testing.T, f *fixture) {
				publishForeignPolicy(t, super, f.tenantID)
			},
			wantVersions: 0, // "Company approval policy" was never created under this branch
		},
		{
			name: "CONTROL: system-published, our name, stale shape",
			setup: func(t *testing.T, f *fixture) {
				publishStaleOwnPolicy(t, app, f.tenantID)
			},
			wantVersions: 2, // v1 stale + v2 superseding
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, super, app, "demopolicy never supersedes "+tc.name)
			f.addBacklog()
			tc.setup(t, f)

			if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
				t.Fatalf("seedTenant: %v", err)
			}
			var n int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM approval_policy_versions v JOIN approval_policies p ON p.id = v.policy_id
				  WHERE v.tenant_id = $1 AND p.name = $2`, f.tenantID, wantPolicyName).Scan(&n); err != nil {
				t.Fatalf("count versions of %q: %v", wantPolicyName, err)
			}
			if n != tc.wantVersions {
				t.Errorf("%d version(s) of %q, want %d", n, wantPolicyName, tc.wantVersions)
			}
		})
	}
}
