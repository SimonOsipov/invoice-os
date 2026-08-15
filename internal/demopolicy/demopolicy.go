// Package demopolicy gives the in-house demo tenant one active sealed approval
// policy and keeps its validated backlog armed, so awaiting_approval is non-zero
// and the Approvals badge is observable on the deploy gate.
//
// It CONVERGES rather than inserting-if-absent. db.Reset truncates approval_runs
// and deliberately leaves the three policy tables standing, so a seeder that
// no-ops once its policy exists arms nothing on the second deploy and every
// validated invoice then satisfies awaiting_approval vacuously
// (TestSeed_AfterResetRearmsSeededInvoices).
//
// It writes its own SQL rather than driving approval.Store.PublishPolicy, which
// seals the version found by WHERE NOT sealed and so answers
// ErrPolicyNothingToPublish on the second boot.
//
// RESIDUAL, recorded and undefended: a gateway restarted out of band runs Reset
// again and re-truncates approval_runs, and nothing re-runs this seeder because
// the invoice service did not restart. The fleet stays green, /healthz stays 200,
// and awaiting_approval silently reads counts.validated. Recovery is one operator
// action -- restart the invoice service. Also in docs/approvals.md.
package demopolicy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DemoTenants is the tenant allowlist — the safety boundary, not ENVIRONMENT,
// which reads "development" on production and would gate fail-open. Never a
// parameter: a caller-supplied tenant is what would make "publish an approval
// policy over a real tenant's invoices" representable.
//
// TestSeed_AllowlistExcludesTheFirmTenant pins its value.
var DemoTenants = []string{"22222222-2222-2222-2222-222222222222"}

const (
	// The name must match neither sweep e2e/topology/roles.spec.ts runs as the
	// in-house persona: DeletePolicy deactivates the governed version in the same
	// transaction (TestSeed_ArmsOnlyTheInHouseDemoTenant).
	policyName = "Company approval policy"

	// Hard-coded, never derived: approval.newRoleKey slugs to hyphens while
	// db/seed.dev.sql writes underscores, and workflow_role_key has no FK
	// (TestSeed_ResolvesTheWorkflowRoleKeyToAStaffedSeat).
	roleKey = "fin_dir"

	// Three of the in-house tenant's four validated invoices sit above this and
	// one below, which is what makes awaiting_approval differ from
	// counts.validated. Lowering it puts the deploy gate's own 1,075 fixtures on
	// the approval lane (TestSeed_InhouseCanFileFixtureStaysOnTheAutoapproveLane).
	condAmount = "100000"

	// approval.sweepCap's value without its fail-whole semantics: a boot step
	// arms what it read and leaves any remainder to the next boot.
	sweepLimit = 5000

	// Matches ArmTx's closed_by and invoice.SystemActor.
	seedActor = "system"
)

// Reported rather than tolerated because the alternative is silent: an unstaffed
// seat yields a policy, published and armed, every step of which blocks, with
// nothing red anywhere.
const (
	noteRoleMissing   = "workflow role " + roleKey + " not found"
	noteRoleUnstaffed = "workflow role " + roleKey + " has no active holder"
)

// Result is what one Seed call did. BacklogFound and RunsArmed are separate
// fields: equal means every candidate armed, a gap means ArmTx answered
// RunID == "" — which happens only when no active version exists.
type Result struct {
	VersionCreated bool   // the policy/version/step statements ran on THIS call
	VersionID      string // the tenant's active version, created now or found
	BacklogFound   int    // rows the anti-join returned, BEFORE arming
	RunsArmed      int    // ArmTx calls that wrote a run
	Note           string // which "did nothing, but why" cause applies
}

// Seed converges every allowlisted tenant on armed and reports what it did.
// Every tenant reports, including the ones that did nothing: a boot step that is
// silent on a no-op is indistinguishable from one that never ran.
func Seed(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (Result, error) {
	var total Result
	var failed []string
	for _, tenantID := range DemoTenants {
		res, err := seedTenant(ctx, pool, tenantID)
		total.BacklogFound += res.BacklogFound
		total.RunsArmed += res.RunsArmed
		total.VersionCreated = total.VersionCreated || res.VersionCreated
		// The allowlist holds one tenant, so carrying its version and Note into
		// the aggregate loses nothing and keeps the boot log's fields aligned.
		total.VersionID, total.Note = res.VersionID, res.Note

		if logger != nil {
			logger.Info("demopolicy: tenant converged",
				"tenant", tenantID, "outcome", res.Note, "version", res.VersionID,
				"version_created", res.VersionCreated, "backlog_found", res.BacklogFound,
				"runs_armed", res.RunsArmed, "error", err)
		}
		// One tenant's failure must not cost the others theirs.
		if err != nil {
			failed = append(failed, tenantID)
		}
	}
	if len(failed) > 0 {
		return total, fmt.Errorf("demopolicy: %d of %d tenants failed: %s",
			len(failed), len(DemoTenants), strings.Join(failed, ", "))
	}
	return total, nil
}

// seedTenant converges ONE tenant, allowlisted or not. Unexported for the same
// reason demodocs.seedTenant is: Seed's allowlist is the boundary, and the
// per-tenant entry point exists so the suite can drive a throwaway tenant.
//
// One transaction for the whole tenant, so any constraint the statement order
// would trip rolls that tenant back whole and leaves no half-seeded state.
func seedTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Result, error) {
	var res Result
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// RLS is the only tenant filter from here down; tenant_id is still written
		// as a column value because the composite FKs require it.
		var present bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1)`, tenantID).Scan(&present); err != nil {
			res.Note = "tenant lookup failed"
			return err
		}
		if !present {
			res.Note = "tenant absent"
			return nil
		}

		// is_active alone is the whole resolve, matching ArmTx: one_active caps it
		// at one row per tenant and active_is_sealed makes active imply sealed.
		var seeded bool
		err := tx.QueryRow(ctx,
			`SELECT v.id::text, p.name = $1
			   FROM approval_policy_versions v
			   JOIN approval_policies p ON p.id = v.policy_id
			  WHERE v.is_active`, policyName).Scan(&res.VersionID, &seeded)
		creating := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !creating {
			res.Note = "active-version probe failed"
			return err
		}

		// Resolved on EVERY boot, not only the one that writes the policy: a seat
		// staffed at publish and suspended later leaves a policy every step of
		// which blocks. Fatal only while creating — refusing the sweep over
		// staffing that changed afterwards would leave awaiting_approval reading
		// counts.validated, which is the failure this package exists to prevent,
		// and the sealed version's role key cannot be edited anyway.
		problem, err := seatProblem(ctx, tx)
		if err != nil {
			res.Note = "role resolution failed"
			return err
		}
		if creating {
			if problem != "" {
				res.Note = problem
				return errors.New("demopolicy: " + problem)
			}
			versionID, err := publishSeedPolicy(ctx, tx, tenantID)
			if err != nil {
				res.Note = "publishing the seeded policy failed"
				return err
			}
			res.VersionID, res.VersionCreated, seeded = versionID, true, true
		}

		// UNCONDITIONAL, outside the branch above: this half IS the convergence.
		if err := armBacklog(ctx, tx, tenantID, &res); err != nil {
			res.Note = "backlog sweep failed"
			return err
		}

		switch {
		case res.VersionCreated:
			res.Note = "policy created; backlog armed"
		case !seeded:
			// The sticky-human-publish case: awaiting_approval is no longer the
			// controlled 3, and nothing else would say so.
			res.Note = "an active version this seeder did not write governs"
		case res.BacklogFound == 0:
			res.Note = "policy already active; backlog already armed"
		default:
			res.Note = "policy already active; backlog re-armed"
		}
		if problem != "" {
			res.Note = problem + "; " + res.Note
		}
		return nil
	})
	return res, err
}

// seatProblem reports why the approval step's seat cannot be satisfied, or "" when
// it can. The memberships join is the whole point: a member-row count passes a
// seat whose only holder is suspended.
func seatProblem(ctx context.Context, tx pgx.Tx) (string, error) {
	var roles, active int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT r.id),
		        count(*) FILTER (WHERE ms.status = 'active')
		   FROM workflow_roles r
		   LEFT JOIN workflow_role_members m
		          ON m.tenant_id = r.tenant_id AND m.workflow_role_id = r.id
		   LEFT JOIN memberships ms
		          ON ms.tenant_id = m.tenant_id AND ms.user_id = m.user_id
		  WHERE r.key = $1 AND r.deleted_at IS NULL`, roleKey).Scan(&roles, &active); err != nil {
		return "", err
	}
	// Two distinct causes, and the log must say which: an absent seat is a
	// seed-data gap, a staffed-then-suspended one is an operator action.
	switch {
	case roles == 0:
		return noteRoleMissing, nil
	case active == 0:
		return noteRoleUnstaffed, nil
	}
	return "", nil
}

// publishSeedPolicy writes the policy, its version, the three steps and the seal.
// The ORDER is the contract: a step INSERT after the seal raises 23001, and
// is_active ahead of sealed raises 23514 (TestSeed_IsIdempotentAcrossBoots).
func publishSeedPolicy(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	// scope is left to its column DEFAULT: CHECK (scope = 'All invoices') admits
	// exactly one value.
	var policyID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id::text`,
		tenantID, policyName).Scan(&policyID); err != nil {
		return "", err
	}

	// sealed and is_active stay at their false defaults: the steps below cannot be
	// inserted under a sealed version.
	var versionID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
		 VALUES ($1, $2, 1) RETURNING id::text`,
		tenantID, policyID).Scan(&versionID); err != nil {
		return "", err
	}

	var rootID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, cond_op, cond_amount)
		 VALUES ($1, $2, 0, 'condition', '>', $3::numeric) RETURNING id::text`,
		tenantID, versionID, condAmount).Scan(&rootID); err != nil {
		return "", err
	}

	// Both lanes at ord 0: slot_uq is UNIQUE NULLS NOT DISTINCT over
	// (version_id, parent_step_id, branch, ord) and the branch differs, so ord
	// restarts per lane. sla_hours stays NULL — a deadline would render every
	// seeded run overdue two days after the persistent environment's last deploy.
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_policy_steps
		        (tenant_id, version_id, parent_step_id, branch, ord, kind, workflow_role_key)
		 VALUES ($1, $2, $3, 'then', 0, 'approval', $4)`,
		tenantID, versionID, rootID, roleKey); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_policy_steps
		        (tenant_id, version_id, parent_step_id, branch, ord, kind)
		 VALUES ($1, $2, $3, 'else', 0, 'autoapprove')`,
		tenantID, versionID, rootID); err != nil {
		return "", err
	}

	// One statement: is_active is never set on a row that is not simultaneously
	// becoming sealed. No tenant-wide deactivation ahead of it — the probe in
	// seedTenant already proved no active version exists. published_by is the
	// literal system, not the human subject PublishPolicy would have stamped.
	if _, err := tx.Exec(ctx,
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true, published_at = now(), published_by = $2
		  WHERE id = $1`, versionID, seedActor); err != nil {
		return "", err
	}

	return versionID, audit.Record(ctx, tx, seedActor, "approval_policy.published", map[string]any{
		"policy_id": policyID,
		"version":   1,
	})
}

// armBacklog is sweepValidatedBacklog's anti-join without its fail-whole cap: a
// boot step arms what it read and leaves any remainder to the next boot, because
// its failure mode is crash-looping an environment.
//
// Self-idempotent — when nothing was truncated every validated invoice already
// carries a live run and the anti-join returns zero rows, which is why this is
// safe to run on every boot.
func armBacklog(ctx context.Context, tx pgx.Tx, tenantID string, res *Result) error {
	// FOR UPDATE is required, not defensive: ArmTx documents that its caller
	// already holds the invoice row lock. OF i keeps approval_runs unlocked.
	rows, err := tx.Query(ctx,
		`SELECT i.id::text
		   FROM invoices i
		  WHERE i.status = 'validated'
		    AND NOT EXISTS (
		        SELECT 1 FROM approval_runs r
		         WHERE r.invoice_id = i.id AND r.state IN ('open','approved')
		    )
		  ORDER BY i.id
		  LIMIT $1
		    FOR UPDATE OF i`, sweepLimit)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	// Closed before the loop, not deferred: ArmTx reuses this transaction's
	// connection, which an open cursor holds.
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	res.BacklogFound = len(ids)

	for _, id := range ids {
		fp, err := invoice.FingerprintTx(ctx, tx, id)
		if err != nil {
			return err
		}
		armed, err := approval.ArmTx(ctx, tx, tenantID, id, fp, seedActor)
		if err != nil {
			return err
		}
		if armed.RunID != "" {
			res.RunsArmed++
		}
	}
	return nil
}
