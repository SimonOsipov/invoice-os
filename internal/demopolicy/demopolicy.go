// Package demopolicy gives each demo tenant one active sealed approval policy
// and keeps its validated backlog armed, so awaiting_approval is non-zero and
// the Approvals badge is observable on the deploy gate. The in-house tenant
// also carries an unpublished draft naming four seats, two of which nobody
// holds actively (cfo suspended, ceo unstaffed) -- the demo's unstaffed-seat
// example.
//
// It CONVERGES rather than inserting-if-absent. db.Reset truncates approval_runs
// and deliberately leaves the three policy tables standing, so a seeder that
// no-ops once its policy exists arms nothing on the second deploy and every
// validated invoice then satisfies awaiting_approval vacuously
// (TestSeed_AfterResetRearmsSeededInvoices). Three writes converge it: a first
// publish, a reactivation of a version something deactivated, and a version N+1
// when the active version's step shape no longer matches the plan.
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
//
// RESIDUAL, accepted: an invoice armed under version N keeps version N's trail
// after a supersede. A sealed version's steps are immutable, so only invoices
// validated after the supersede render the new shape.
package demopolicy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	firmTenantID    = "11111111-1111-1111-1111-111111111111"
	inhouseTenantID = "22222222-2222-2222-2222-222222222222"
)

// DemoTenants is the tenant allowlist -- the safety boundary, not ENVIRONMENT,
// which reads "development" on production and would gate fail-open. A hardcoded
// list of demo tenants and never a parameter: a caller-supplied tenant is what
// would make "publish an approval policy over a real tenant's invoices"
// representable.
//
// TestSeed_AllowlistHoldsBothPersonaTenants pins its value.
var DemoTenants = []string{inhouseTenantID, firmTenantID}

const (
	// The name must match neither sweep e2e/topology/roles.spec.ts runs as the
	// in-house persona: DeletePolicy deactivates the governed version in the same
	// transaction (TestSeed_ArmsOnlyTheDemoTenants).
	policyName = "Company approval policy"

	// Hard-coded, never derived: approval.newRoleKey slugs to hyphens while
	// db/seed.dev.sql writes underscores, and workflow_role_key has no FK
	// (TestSeed_ResolvesTheWorkflowRoleKeyToAStaffedSeat).
	roleKey = "fin_dir"

	// Three of the in-house tenant's four validated invoices sit above this and
	// one below, which is what makes awaiting_approval differ from
	// counts.validated. Lowering it puts the deploy gate's own 1,075 fixtures on
	// the approval lane (TestSeed_InhouseCanFileFixtureStaysOnTheAutoapproveLane).
	condAmount = "100000.00"

	// approval.sweepCap's value without its fail-whole semantics: a boot step
	// arms what it read and leaves any remainder to the next boot.
	sweepLimit = 5000

	// Matches ArmTx's closed_by and invoice.SystemActor.
	seedActor = "system"
)

// Reported rather than tolerated because the alternative is silent: an unstaffed
// seat yields a policy, published and armed, every step of which blocks, with
// nothing red anywhere. Format strings, so the note names WHICH seat.
const (
	noteRoleMissing   = "workflow role %s not found"
	noteRoleUnstaffed = "workflow role %s has no active holder"
)

func strp(s string) *string { return &s }

// plan is one tenant's demo policy set. A pointer type throughout, so planFor's
// answer compares by identity (TestSeed_PlanIsChosenByTenantIdNotKind), and
// read-only once declared.
type plan struct {
	policyName string
	steps      []approval.Step

	// requiredSeats are the seats a SEEDED invoice can actually reach, and the
	// only ones an unstaffed holder may refuse the seed over. Never the firm's
	// fin_dir/cfo, which sit above the plan's two conditions while the largest
	// seeded firm invoice is 193,500, and never a draft's.
	requiredSeats []string

	// draft is written unsealed and inactive, or nil when the plan carries none.
	draft *draftPlan
}

type draftPlan struct {
	name  string
	steps []approval.Step
}

// Every cond_amount literal carries the column's numeric(14,2) scale. shapeOf
// compares the DB's ::text rendering, which always shows two decimals, so an
// unscaled literal never matches and the seeder republishes on every boot
// (TestSeed_SupersedesItsOwnStaleActiveVersionWithVersionTwo's second boot).
// No sla_hours anywhere: a deadline renders every seeded run overdue two days
// after the persistent environment's last deploy.
var (
	// firmPlan mirrors polF1's step shape and role sequence
	// (frontend/app/src/lib/policies.fixture.ts), not verbatim -- it omits
	// sla_hours (see "No sla_hours anywhere" above) and node f1n1's delegate: true.
	firmPlan = &plan{
		policyName:    "Standard approval policy",
		requiredSeats: []string{"fin_mgr", "compliance"},
		steps: []approval.Step{
			{Kind: "approval", WorkflowRoleKey: strp("fin_mgr")},
			{Kind: "condition", CondOp: strp(">"), CondAmount: strp("250000000.00"),
				Then: []approval.Step{{Kind: "approval", WorkflowRoleKey: strp("fin_dir")}}},
			{Kind: "condition", CondOp: strp(">"), CondAmount: strp("1000000000.00"),
				Then: []approval.Step{
					{Kind: "approval", WorkflowRoleKey: strp("cfo")},
					{Kind: "notify", NotifyTarget: strp("Audit Committee"), NotifyChannel: strp("Email")},
				}},
			{Kind: "approval", WorkflowRoleKey: strp("compliance")},
		},
	}

	// inhousePlan keeps the threshold the badge oracle depends on and adds
	// polH1's trailing notify. Its draft is polH2 renamed: cfo's sole holder is
	// suspended and ceo has none, which is the point of shipping it.
	inhousePlan = &plan{
		policyName:    policyName,
		requiredSeats: []string{roleKey},
		steps: []approval.Step{
			{Kind: "condition", CondOp: strp(">"), CondAmount: strp(condAmount),
				Then: []approval.Step{{Kind: "approval", WorkflowRoleKey: strp(roleKey)}},
				Else: []approval.Step{{Kind: "autoapprove"}}},
			{Kind: "notify", NotifyTarget: strp("Tax Team"), NotifyChannel: strp("In-app")},
		},
		draft: &draftPlan{
			name: "Executive escalation",
			steps: []approval.Step{
				{Kind: "approval", WorkflowRoleKey: strp("line_mgr")},
				{Kind: "approval", WorkflowRoleKey: strp(roleKey)},
				{Kind: "condition", CondOp: strp(">"), CondAmount: strp("1000000000.00"),
					Then: []approval.Step{
						{Kind: "approval", WorkflowRoleKey: strp("cfo")},
						{Kind: "approval", WorkflowRoleKey: strp("ceo")},
					}},
			},
		},
	}
)

// planFor picks a tenant's plan by ID, never by tenants.kind: every throwaway
// fixture in the suite is in_house and must keep the in-house behaviour.
func planFor(tenantID string) *plan {
	if tenantID == firmTenantID {
		return firmPlan
	}
	return inhousePlan
}

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
		// VersionID and Note carry the LAST tenant's; the per-tenant log line
		// below is the record for each.
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

// seedTenant converges ONE tenant on its own plan.
func seedTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Result, error) {
	return seedTenantPlan(ctx, pool, tenantID, planFor(tenantID))
}

// activeVersion is the tenant's governing version, whoever wrote it.
type activeVersion struct {
	versionID   string
	policyID    string
	policyName  string
	publishedBy string // "" when NULL
}

// seedTenantPlan converges ONE tenant on the GIVEN plan, allowlisted or not.
// Unexported for the same reason demodocs.seedTenant is: Seed's allowlist is the
// boundary, and the per-tenant entry point exists so the suite can drive a
// throwaway tenant — with the firm plan too, which planFor never answers for one.
//
// One transaction for the whole tenant, so any constraint the statement order
// would trip rolls that tenant back whole and leaves no half-seeded state.
func seedTenantPlan(ctx context.Context, pool *pgxpool.Pool, tenantID string, p *plan) (Result, error) {
	var res Result
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// One converger per tenant at a time. clearActive would otherwise let a
		// boot that probed before a sibling committed deactivate that sibling and
		// insert a SECOND policy of the same name -- approval_policies has no
		// unique index on name, and one_active no longer refuses it. Released on
		// commit or rollback (TestSeed_ConcurrentBootsLeaveOneActiveVersion).
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID); err != nil {
			res.Note = "tenant lock failed"
			return err
		}

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
		var active activeVersion
		err := tx.QueryRow(ctx,
			`SELECT v.id::text, v.policy_id::text, p.name, coalesce(v.published_by, '')
			   FROM approval_policy_versions v
			   JOIN approval_policies p ON p.id = v.policy_id
			  WHERE v.is_active`).
			Scan(&active.versionID, &active.policyID, &active.policyName, &active.publishedBy)
		governed := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && governed {
			res.Note = "active-version probe failed"
			return err
		}

		// Resolved on EVERY boot, not only the one that writes the policy: a seat
		// staffed at publish and suspended later leaves a policy every step of
		// which blocks. Fatal only on the insert path — refusing over staffing
		// that changed afterwards would leave awaiting_approval reading
		// counts.validated, which is the failure this package exists to prevent,
		// and the sealed version's role key cannot be edited anyway.
		problem, err := seatProblem(ctx, tx, p.requiredSeats)
		if err != nil {
			res.Note = "role resolution failed"
			return err
		}

		seeded := governed && active.policyName == p.policyName
		var reactivated, superseded bool
		switch {
		case governed:
			res.VersionID = active.versionID
			versionID, did, err := supersedeIfStale(ctx, tx, tenantID, p, active)
			if err != nil {
				res.Note = "superseding the seeded policy failed"
				return err
			}
			if did {
				res.VersionID, superseded = versionID, true
			}
		default:
			// Probe B: our policy exists but nothing activated it. Reactivating the
			// sealed version it already carries is the only convergence that does
			// not duplicate the policy row.
			sealedID, err := sealedVersionByName(ctx, tx, p.policyName)
			if err != nil {
				res.Note = "sealed-version probe failed"
				return err
			}
			if sealedID != "" {
				if err := reactivateSealedVersion(ctx, tx, tenantID, sealedID); err != nil {
					res.Note = "reactivating the seeded policy failed"
					return err
				}
				res.VersionID, reactivated, seeded = sealedID, true, true
				break
			}
			if problem != "" {
				res.Note = problem
				return errors.New("demopolicy: " + problem)
			}
			versionID, err := publishSeedPolicy(ctx, tx, tenantID, p)
			if err != nil {
				res.Note = "publishing the seeded policy failed"
				return err
			}
			res.VersionID, res.VersionCreated, seeded = versionID, true, true
		}

		// Unconditional, and deliberately outside the branch above: the draft is
		// config, not governance, so a foreign active version does not withhold it.
		if p.draft != nil {
			if err := ensureDraft(ctx, tx, tenantID, p.draft); err != nil {
				res.Note = "writing the draft policy failed"
				return err
			}
		}

		// UNCONDITIONAL, outside the branch above: this half IS the convergence.
		if err := armBacklog(ctx, tx, tenantID, &res); err != nil {
			res.Note = "backlog sweep failed"
			return err
		}

		switch {
		case res.VersionCreated:
			res.Note = "policy created; backlog armed"
		case superseded:
			res.Note = "policy superseded; backlog armed"
		case reactivated:
			res.Note = "policy reactivated; backlog armed"
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

// seatProblem reports why one of the plan's required seats cannot be satisfied,
// or "" when they all can. Keys are walked IN ORDER so the reported cause is
// deterministic. The memberships join is the whole point: a member-row count
// passes a seat whose only holder is suspended.
func seatProblem(ctx context.Context, tx pgx.Tx, keys []string) (string, error) {
	for _, key := range keys {
		var roles, active int
		if err := tx.QueryRow(ctx,
			`SELECT count(DISTINCT r.id),
			        count(*) FILTER (WHERE ms.status = 'active')
			   FROM workflow_roles r
			   LEFT JOIN workflow_role_members m
			          ON m.tenant_id = r.tenant_id AND m.workflow_role_id = r.id
			   LEFT JOIN memberships ms
			          ON ms.tenant_id = m.tenant_id AND ms.user_id = m.user_id
			  WHERE r.key = $1 AND r.deleted_at IS NULL`, key).Scan(&roles, &active); err != nil {
			return "", err
		}
		// Two distinct causes, and the log must say which: an absent seat is a
		// seed-data gap, a staffed-then-suspended one is an operator action.
		switch {
		case roles == 0:
			return fmt.Sprintf(noteRoleMissing, key), nil
		case active == 0:
			return fmt.Sprintf(noteRoleUnstaffed, key), nil
		}
	}
	return "", nil
}

// clearActive frees the tenant's one active slot, mirroring PublishPolicy's own
// deactivation (policy_store.go:617-620). Issued immediately before EVERY
// activation: one_active is a partial UNIQUE index, so activating over a held
// slot raises 23505 and rolls the tenant back. No policy predicate -- RLS is the
// tenant scope, and deactivation is not un-publishing.
func clearActive(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE approval_policy_versions SET is_active = false WHERE is_active`)
	return err
}

// sealedVersionByName finds the newest sealed version of the plan's policy, or
// "" when there is none. deleted_at IS NULL is load-bearing: a policy a demo
// user deleted must not come back active
// (TestSeed_ASoftDeletedSeededPolicyIsNotResurrected).
func sealedVersionByName(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	var versionID string
	err := tx.QueryRow(ctx,
		`SELECT v.id::text
		   FROM approval_policy_versions v
		   JOIN approval_policies p ON p.id = v.policy_id
		  WHERE p.name = $1 AND p.deleted_at IS NULL AND v.sealed
		  ORDER BY v.version DESC
		  LIMIT 1`, name).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return versionID, err
}

// reactivateSealedVersion puts an already-sealed version back in the tenant's
// active slot. tenantID is not a predicate -- RLS is -- and is taken for
// call-site symmetry with the other two activation paths.
func reactivateSealedVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID string) error {
	if err := clearActive(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, versionID)
	return err
}

// publishSeedPolicy writes the policy, version 1, its steps and the seal. The
// ORDER is the contract: a step INSERT after the seal raises 23001, and
// is_active ahead of sealed raises 23514 (TestSeed_IsIdempotentAcrossBoots).
//
// The plan is an argument rather than resolved here: seedTenantPlan may be
// driving a plan planFor would never answer for this tenant.
func publishSeedPolicy(ctx context.Context, tx pgx.Tx, tenantID string, p *plan) (string, error) {
	// scope is left to its column DEFAULT: CHECK (scope = 'All invoices') admits
	// exactly one value.
	var policyID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id::text`,
		tenantID, p.policyName).Scan(&policyID); err != nil {
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
	if err := writeLane(ctx, tx, tenantID, versionID, nil, nil, p.steps); err != nil {
		return "", err
	}

	if err := clearActive(ctx, tx); err != nil {
		return "", err
	}
	// One statement: is_active is never set on a row that is not simultaneously
	// becoming sealed. published_by is the literal system, not the human subject
	// PublishPolicy would have stamped.
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

// supersedeIfStale publishes version N+1 when the active version is one THIS
// seeder wrote and its shape has drifted from the plan. Three guards, all
// load-bearing (TestSeed_NeverSupersedesAVersionItDidNotPublish): another
// policy is never reached into, a human's version is left strictly alone even
// when stale, and a matching shape writes nothing at all.
func supersedeIfStale(ctx context.Context, tx pgx.Tx, tenantID string, p *plan, active activeVersion) (string, bool, error) {
	if active.policyName != p.policyName || active.publishedBy != seedActor {
		return "", false, nil
	}
	current, err := readStepTree(ctx, tx, active.versionID)
	if err != nil {
		return "", false, err
	}
	if shapeOf(current) == shapeOf(p.steps) {
		return "", false, nil
	}

	// A sealed version's steps are immutable, so a new version is the only
	// convergence there is.
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) + 1 FROM approval_policy_versions WHERE policy_id = $1`,
		active.policyID).Scan(&version); err != nil {
		return "", false, err
	}
	var versionID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
		 VALUES ($1, $2, $3) RETURNING id::text`,
		tenantID, active.policyID, version).Scan(&versionID); err != nil {
		return "", false, err
	}
	if err := writeLane(ctx, tx, tenantID, versionID, nil, nil, p.steps); err != nil {
		return "", false, err
	}
	if err := clearActive(ctx, tx); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true, published_at = now(), published_by = $2
		  WHERE id = $1`, versionID, seedActor); err != nil {
		return "", false, err
	}

	return versionID, true, audit.Record(ctx, tx, seedActor, "approval_policy.published", map[string]any{
		"policy_id":             active.policyID,
		"version":               version,
		"superseded_version_id": active.versionID,
	})
}

// draftNamespace derives a draft policy's id from (tenant, name). FROZEN:
// changing it re-derives every draft id and duplicates the row it exists to
// keep unique.
var draftNamespace = uuid.MustParse("9d4c3b6a-1e2f-4a58-9c73-0f5b6d8e2a41")

// ensureDraft writes an unsealed, inactive policy the demo can show but nothing
// governs. approval_policies carries no unique index on name, so the id is
// derived and ON CONFLICT DO NOTHING is the only guard against a racing boot
// duplicating it. The probe sees soft-deleted rows too, so a demo delete is
// never resurrected.
func ensureDraft(ctx context.Context, tx pgx.Tx, tenantID string, d *draftPlan) error {
	policyID := uuid.NewSHA1(draftNamespace, []byte(tenantID+"|"+d.name)).String()

	var claimed string
	err := tx.QueryRow(ctx,
		`INSERT INTO approval_policies (id, tenant_id, name) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO NOTHING RETURNING id::text`,
		policyID, tenantID, d.name).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the draft already exists, or a racing boot won it
	}
	if err != nil {
		return err
	}

	var versionID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
		 VALUES ($1, $2, 1) RETURNING id::text`,
		tenantID, policyID).Scan(&versionID); err != nil {
		return err
	}
	if err := writeLane(ctx, tx, tenantID, versionID, nil, nil, d.steps); err != nil {
		return err
	}
	return audit.Record(ctx, tx, seedActor, "approval_policy.created", map[string]any{
		"policy_id": policyID,
		"name":      d.name,
	})
}

// writeLane inserts one lane and recurses into each condition's branches. ord
// restarts per lane: slot_uq is UNIQUE NULLS NOT DISTINCT over
// (version_id, parent_step_id, branch, ord), so the branch is what separates two
// steps that share an ord.
func writeLane(ctx context.Context, tx pgx.Tx, tenantID, versionID string, parentID, branch *string, lane []approval.Step) error {
	for ord, s := range lane {
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policy_steps
			        (tenant_id, version_id, parent_step_id, branch, ord, kind,
			         workflow_role_key, cond_op, cond_amount, notify_target, notify_channel)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11)
			 RETURNING id::text`,
			tenantID, versionID, parentID, branch, ord, s.Kind,
			s.WorkflowRoleKey, s.CondOp, s.CondAmount, s.NotifyTarget, s.NotifyChannel).Scan(&id); err != nil {
			return err
		}
		then, els := "then", "else"
		if err := writeLane(ctx, tx, tenantID, versionID, &id, &then, s.Then); err != nil {
			return err
		}
		if err := writeLane(ctx, tx, tenantID, versionID, &id, &els, s.Else); err != nil {
			return err
		}
	}
	return nil
}

// readStepTree reads one version's steps flat and nests them in Go, matching
// approval.readPolicyTrees + nestSteps rather than a recursive CTE:
// approval_policy_steps_depth_cap forbids a condition CHILD, so no tree here is
// deeper than two. ORDER BY ord leaves each lane's bucket already ascending.
func readStepTree(ctx context.Context, tx pgx.Tx, versionID string) ([]approval.Step, error) {
	// cond_amount::text keeps the column's numeric(14,2) scale, which is what
	// shapeOf compares. A bare numeric scans back as "0" at zero scale.
	rows, err := tx.Query(ctx,
		`SELECT id::text, parent_step_id::text, branch, ord, kind, workflow_role_key,
		        sla_hours, cond_op, cond_amount::text, notify_target, notify_channel
		   FROM approval_policy_steps WHERE version_id = $1 ORDER BY ord`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		id   string
		step approval.Step
	}
	var roots []row
	lanes := map[string][]row{}
	for rows.Next() {
		var r row
		var parent, branch *string
		var ord int
		if err := rows.Scan(&r.id, &parent, &branch, &ord, &r.step.Kind, &r.step.WorkflowRoleKey,
			&r.step.SLAHours, &r.step.CondOp, &r.step.CondAmount,
			&r.step.NotifyTarget, &r.step.NotifyChannel); err != nil {
			return nil, err
		}
		if parent == nil || branch == nil {
			roots = append(roots, r)
			continue
		}
		key := *parent + "\x00" + *branch
		lanes[key] = append(lanes[key], r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var nest func(lane []row) []approval.Step
	nest = func(lane []row) []approval.Step {
		out := make([]approval.Step, 0, len(lane))
		for _, r := range lane {
			r.step.Then = nest(lanes[r.id+"\x00then"])
			r.step.Else = nest(lanes[r.id+"\x00else"])
			out = append(out, r.step)
		}
		return out
	}
	return nest(roots), nil
}

// shapeOf renders a step tree as one canonical string, so the plan's literals
// and a stored version compare by value. Every field a seeded step can carry is
// in the signature -- a drift the comparison cannot see is a supersede that
// never fires.
func shapeOf(steps []approval.Step) string {
	var b strings.Builder
	writeShape(&b, "", steps)
	return b.String()
}

func writeShape(b *strings.Builder, path string, lane []approval.Step) {
	for i, s := range lane {
		at := fmt.Sprintf("%s%d", path, i)
		fmt.Fprintf(b, "%s|%s|%s|%s|%s|%s|%s|%s\n", at, s.Kind,
			shapeField(s.WorkflowRoleKey), shapeField(s.SLAHours), shapeField(s.CondOp),
			shapeField(s.CondAmount), shapeField(s.NotifyTarget), shapeField(s.NotifyChannel))
		writeShape(b, at+".t", s.Then)
		writeShape(b, at+".e", s.Else)
	}
}

func shapeField[T any](v *T) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
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
