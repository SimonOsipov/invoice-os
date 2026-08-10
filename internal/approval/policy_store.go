package approval

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The policy handler seam, declared beside the methods that satisfy it (the
// store.go:33-47 shape). Only the three this subtask implements: a declared-but-
// unasserted function type is dead code no vet catches, and the draft/publish/delete
// signatures belong to the subtasks that build them.
type (
	PolicyLister  func(ctx context.Context) ([]Policy, error)
	PolicyGetter  func(ctx context.Context, id string) (Policy, error)
	PolicyCreator func(ctx context.Context, name, scope string) (Policy, error)
)

var (
	_ PolicyLister  = new(Store).ListPolicies
	_ PolicyGetter  = new(Store).GetPolicy
	_ PolicyCreator = new(Store).CreatePolicy
)

// newPolicy is what every read and the create start from: lanes and versions are []
// rather than nil, so the wire never renders null. A policy carrying no version row at
// all keeps this draft status — unreachable through CreatePolicy, and draft is the
// inert state.
func newPolicy() Policy {
	return Policy{Status: "draft", Steps: []Step{}, Versions: []PolicyVersion{}}
}

// takeTopVersion records the highest version's bits. Sealed and Status come from that
// one row: approval_policy_versions_one_draft caps a policy at one unsealed version, so
// "an unsealed version exists" is exactly "the top version is unsealed". Deriving the two
// separately is how they drift if that index is ever relaxed.
func takeTopVersion(p *Policy, pv PolicyVersion) {
	p.Version, p.Sealed = pv.Version, pv.Sealed
	p.Status = "published"
	if !pv.Sealed {
		p.Status = "draft"
	}
}

// policyVersionColumns is the one column list both version reads use, so the scan below
// serves each of them.
const policyVersionColumns = `policy_id, id, version, sealed, is_active, published_at, published_by`

// scanPolicyVersionRow reads one version row: its policy id, its own id, and the wire
// shape. published_at is formatted in Go rather than read as ::text — the column's text
// form is not RFC3339, which is what every other timestamp on this API marshals to.
func scanPolicyVersionRow(rows pgx.Rows) (policyID, versionID string, pv PolicyVersion, err error) {
	var at *time.Time
	if err := rows.Scan(&policyID, &versionID, &pv.Version, &pv.Sealed, &pv.IsActive, &at, &pv.PublishedBy); err != nil {
		return "", "", PolicyVersion{}, err
	}
	if at != nil {
		s := at.UTC().Format(time.RFC3339Nano)
		pv.PublishedAt = &s
	}
	return policyID, versionID, pv, nil
}

// readPolicyTrees returns the WHOLE step set of each named version, nested. Whole-set is
// the contract, not an optimisation: nestSteps drops a child whose parent is absent and
// promotes a branch-less child to a root, so a partial read yields a wrong tree instead
// of an error — hence the only predicate is on version_id.
//
// Plural so ListPolicies stays constant in policy count; GetPolicy passes one id. An
// empty slice is legal: ANY of an empty array matches nothing.
func readPolicyTrees(ctx context.Context, tx pgx.Tx, versionIDs []string) (map[string][]Step, error) {
	// cond_amount::text: Step.CondAmount is a *string so an exact decimal round-trips,
	// never a float64 or a numeric (the D13 money rule, internal/invoice/store.go:44-53).
	rows, err := tx.Query(ctx,
		`SELECT version_id, id, parent_step_id, branch, ord, kind,
		        workflow_role_key, sla_hours, cond_op, cond_amount::text,
		        notify_target, notify_channel
		   FROM approval_policy_steps
		  WHERE version_id = ANY($1::uuid[])
		  ORDER BY version_id, ord`, versionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byVersion := map[string][]stepRow{}
	for rows.Next() {
		var versionID string
		var r stepRow
		if err := rows.Scan(&versionID, &r.ID, &r.ParentStepID, &r.Branch, &r.Ord, &r.Kind,
			&r.WorkflowRoleKey, &r.SLAHours, &r.CondOp, &r.CondAmount,
			&r.NotifyTarget, &r.NotifyChannel); err != nil {
			return nil, err
		}
		byVersion[versionID] = append(byVersion[versionID], r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trees := make(map[string][]Step, len(versionIDs))
	for _, id := range versionIDs {
		// nestSteps of nothing is [], never nil, so a stepless version needs no branch.
		trees[id] = nestSteps(byVersion[id])
	}
	return trees, nil
}

// ListPolicies returns the tenant's live policies, each with its version list newest
// first and the highest version's step tree. Three statements, constant in the number of
// policies: the policies, every version, then every step of the top versions in ONE
// query, grouped in Go.
//
// No access-role gate at all, the same as ListRoles: any caller holding a tenant claim
// may list (TestPolicy_ReadNeedsNoAdminRoleWriteDoes).
func (s *Store) ListPolicies(ctx context.Context) ([]Policy, error) {
	policies := []Policy{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, name, scope
			   FROM approval_policies
			  WHERE deleted_at IS NULL
			  ORDER BY created_at, id`)
		if err != nil {
			return err
		}
		idx := map[string]int{} // policy id -> index into policies
		for rows.Next() {
			p := newPolicy()
			if err := rows.Scan(&p.ID, &p.Name, &p.Scope); err != nil {
				rows.Close()
				return err
			}
			idx[p.ID] = len(policies)
			policies = append(policies, p)
		}
		// Closed explicitly rather than deferred: the next query reuses this
		// transaction's connection.
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// Unfiltered, like ListRoles' staffing read: a soft-deleted policy's versions are
		// absent from idx and dropped, so its steps are never even asked for.
		versions, err := tx.Query(ctx,
			`SELECT `+policyVersionColumns+`
			   FROM approval_policy_versions
			  ORDER BY policy_id, version DESC`)
		if err != nil {
			return err
		}
		topOf := map[string]int{} // top version id -> index into policies
		versionIDs := []string{}
		for versions.Next() {
			policyID, versionID, pv, err := scanPolicyVersionRow(versions)
			if err != nil {
				versions.Close()
				return err
			}
			i, ok := idx[policyID]
			if !ok {
				continue
			}
			// version DESC makes the first surviving row per policy the highest one. It
			// alone supplies Version, Sealed, Status and the version whose steps are read,
			// so Steps and Version provably name the same version.
			if len(policies[i].Versions) == 0 {
				takeTopVersion(&policies[i], pv)
				topOf[versionID] = i
				versionIDs = append(versionIDs, versionID)
			}
			policies[i].Versions = append(policies[i].Versions, pv)
		}
		versions.Close()
		if err := versions.Err(); err != nil {
			return err
		}

		trees, err := readPolicyTrees(ctx, tx, versionIDs)
		if err != nil {
			return err
		}
		for versionID, i := range topOf {
			policies[i].Steps = trees[versionID]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policies, nil
}

// GetPolicy returns one live policy, or ErrPolicyNotFound. Ungated like ListPolicies.
func (s *Store) GetPolicy(ctx context.Context, id string) (Policy, error) {
	// Parsed above the tx, the SetRoleMembers precedent: 22P02 carries no constraint name,
	// so a malformed id reaching SQL is unmappable and answers 500. ErrPolicyNotFound and
	// not ErrValidation, unlike a body-supplied uuid — 400 against 404 on a path resource
	// would be an existence oracle (TestPolicy_GetUnknownAndMalformedAreNotFound).
	u, err := uuid.Parse(id)
	if err != nil {
		return Policy{}, ErrPolicyNotFound
	}

	p := newPolicy()
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// u.String() is the canonical form: uuid.Parse also accepts the urn and braced
		// spellings Postgres's ::uuid rejects. deleted_at IS NULL is the existence predicate.
		if err := tx.QueryRow(ctx,
			`SELECT id, name, scope
			   FROM approval_policies
			  WHERE id = $1 AND deleted_at IS NULL`, u.String(),
		).Scan(&p.ID, &p.Name, &p.Scope); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPolicyNotFound
			}
			return err
		}

		versions, err := tx.Query(ctx,
			`SELECT `+policyVersionColumns+`
			   FROM approval_policy_versions
			  WHERE policy_id = $1
			  ORDER BY version DESC`, p.ID)
		if err != nil {
			return err
		}
		topVersionID := ""
		for versions.Next() {
			_, versionID, pv, err := scanPolicyVersionRow(versions)
			if err != nil {
				versions.Close()
				return err
			}
			if len(p.Versions) == 0 {
				takeTopVersion(&p, pv)
				topVersionID = versionID
			}
			p.Versions = append(p.Versions, pv)
		}
		versions.Close()
		if err := versions.Err(); err != nil {
			return err
		}

		if topVersionID == "" {
			return nil
		}
		trees, err := readPolicyTrees(ctx, tx, []string{topVersionID})
		if err != nil {
			return err
		}
		p.Steps = trees[topVersionID]
		return nil
	})
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}

// CreatePolicy inserts a policy and its empty draft version 1, auditing both in the same
// transaction. Statement order is the security property, on two axes:
//
// Both normalizers run ABOVE the transaction and REPLACE their argument, so the caller's
// raw string is unreachable from the INSERT (the CreateRole precedent, store.go:128-133).
// That replacement is the whole mechanism: normalizeScope's output set is exactly what
// approval_policies_scope_check accepts, and an unnormalized scope reaching SQL raises a
// 23514 that carries no sentinel, which policyStatusForErr answers 500 rather than the
// 400 this seam promises (TestCreatePolicy_EmptyScopeNormalizedBeforeSQL).
//
// requireActiveAdmin is then the first statement in the closure, before any policy row is
// read, so a suspended or non-admin caller is refused with nothing written
// (TestPolicy_CreatePermissionCheckedBeforeRowRead).
func (s *Store) CreatePolicy(ctx context.Context, name, scope string) (Policy, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Policy{}, err
	}
	scope, err = normalizeScope(scope)
	if err != nil {
		return Policy{}, err
	}

	created := newPolicy()
	created.Version = 1
	created.Versions = append(created.Versions, PolicyVersion{Version: 1})
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)

		if err := requireActiveAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}

		// tenant_id is explicit: the column has no DEFAULT and the RLS WITH CHECK ties it
		// to the GUC. scope is the normalized local, never the argument as received.
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policies (tenant_id, name, scope)
			 VALUES ($1, $2, $3)
			 RETURNING id, name, scope`,
			caller.TenantID, name, scope,
		).Scan(&created.ID, &created.Name, &created.Scope); err != nil {
			return err
		}

		// sealed and is_active take their false DEFAULTs. Never set is_active here:
		// approval_policy_versions_one_active spans the whole tenant, and publishing is
		// its own seam.
		if _, err := tx.Exec(ctx,
			`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
			 VALUES ($1, $2, 1)`,
			caller.TenantID, created.ID); err != nil {
			return err
		}

		// Last statement in the closure: a failing audit write rolls both inserts back
		// (TestPolicy_CreateAuditsInSameTx). tenant_id comes from the GUC via the audit_log
		// column DEFAULT; policy_id is the RETURNING row's.
		return audit.Record(ctx, tx, caller.Subject, "approval_policy.created", map[string]any{
			"policy_id": created.ID,
			"version":   1,
		})
	})
	if err != nil {
		return Policy{}, err
	}
	return created, nil
}
