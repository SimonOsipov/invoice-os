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
// store.go:33-47 shape). Every type here is asserted against its method: a
// declared-but-unasserted function type is dead code no vet catches.
//
// PutDraft takes []stepInput, not *[]stepInput: presence is the handler's call (a nil
// pointer is a 400 there), and nil and an empty slice are the same store state.
// PublishPolicy takes no body at all — published_by is the caller's subject.
type (
	PolicyLister    func(ctx context.Context) ([]Policy, error)
	PolicyGetter    func(ctx context.Context, id string) (Policy, error)
	PolicyCreator   func(ctx context.Context, name, scope string) (Policy, error)
	PolicyDrafter   func(ctx context.Context, id string, name, scope *string, steps []stepInput) (Policy, error)
	PolicyPublisher func(ctx context.Context, id string) (Policy, error)
	PolicyDeleter   func(ctx context.Context, id string) (Policy, error)
)

var (
	_ PolicyLister    = new(Store).ListPolicies
	_ PolicyGetter    = new(Store).GetPolicy
	_ PolicyCreator   = new(Store).CreatePolicy
	_ PolicyDrafter   = new(Store).PutDraft
	_ PolicyPublisher = new(Store).PublishPolicy
	_ PolicyDeleter   = new(Store).DeletePolicy
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
	// cond_amount::text: the money rule (internal/invoice/store.go:44-53). pgx scans a
	// bare numeric into a *string too, but drops the scale at zero — 0.00 reads back as
	// "0" (TestPolicy_CondAmountKeepsItsScaleAtZero). Do not remove the cast.
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
// No access-role gate of its own, the same as ListRoles: any caller the request seam
// admits may list (TestPolicy_ReadNeedsNoAdminRoleWriteDoes).
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

// PutDraft rewrites a policy's open draft wholesale: the submitted tree replaces that
// version's whole step set. A policy holding no open draft is forked a fresh version
// numbered max+1 carrying only what was sent — a fork copies no steps.
//
// A sealed version is never reached: the resolution predicate is NOT sealed, never "the
// active version", because approval_policy_versions_one_active spans the tenant and a
// policy can hold sealed versions with no active one. approval_policy_steps_content_lock
// would raise 23001 if the predicate ever broke, and that stays an honest 500 — it is a
// plpgsql RAISE carrying no constraint name, so no mapper can catch it by accident.
//
// Statement order is the security property, as in CreatePolicy: the id is parsed and both
// normalizers run ABOVE the transaction, so neither a malformed uuid (22P02) nor an
// unnormalized scope (23514) reaches SQL — both carry no sentinel and would answer 500
// where this seam promises 404 and 400. requireActiveAdmin is then the first statement in
// the closure, before any policy row is read.
func (s *Store) PutDraft(ctx context.Context, id string, name, scope *string, steps []stepInput) (Policy, error) {
	// GetPolicy's parse, and its choice of sentinel: 400 against 404 on a path resource
	// would be an existence oracle.
	u, err := uuid.Parse(id)
	if err != nil {
		return Policy{}, ErrPolicyNotFound
	}
	// Copied, never written through: the pointers belong to the caller (UpdateRole,
	// store.go:211-218). normalizeScope's output set is exactly what
	// approval_policies_scope_check accepts, so a non-nil "" reaches SQL as the default.
	if name != nil {
		n, err := normalizeName(*name)
		if err != nil {
			return Policy{}, err
		}
		name = &n
	}
	if scope != nil {
		sc, err := normalizeScope(*scope)
		if err != nil {
			return Policy{}, err
		}
		scope = &sc
	}
	if err := validateTree(steps); err != nil {
		return Policy{}, err
	}
	rows := flattenSteps(steps)

	p := newPolicy()
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)

		if err := requireActiveAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}

		// FOR UPDATE serialises concurrent forks, and must stay above the draft resolution
		// below: unlocked, two callers both resolve "no draft" and the loser raises 23505 on
		// approval_policy_versions_one_draft. deleted_at IS NULL is the existence predicate,
		// so a soft-deleted policy is not reopened. u.String() is the canonical form —
		// uuid.Parse also accepts the urn and braced spellings ::uuid rejects.
		var policyID string
		if err := tx.QueryRow(ctx,
			`SELECT id
			   FROM approval_policies
			  WHERE id = $1 AND deleted_at IS NULL
			    FOR UPDATE`, u.String(),
		).Scan(&policyID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPolicyNotFound
			}
			return err
		}

		// coalesce rather than Go-merged values: only the fields that were sent appear in
		// the effective write. $3 is the NORMALIZED local, never the argument as received.
		if err := tx.QueryRow(ctx,
			`UPDATE approval_policies
			    SET name = coalesce($2::text, name), scope = coalesce($3::text, scope)
			  WHERE id = $1
			  RETURNING id, name, scope`, policyID, name, scope,
		).Scan(&p.ID, &p.Name, &p.Scope); err != nil {
			return err
		}

		var versionID string
		var version int
		err := tx.QueryRow(ctx,
			`SELECT id, version
			   FROM approval_policy_versions
			  WHERE policy_id = $1 AND NOT sealed`, policyID,
		).Scan(&versionID, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			// max+1 folds into the INSERT so no window exists between reading the max and
			// using it. tenant_id is explicit: the column has no DEFAULT. sealed and is_active
			// take their false DEFAULTs — publishing is its own seam.
			err = tx.QueryRow(ctx,
				`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
				 SELECT $1::uuid, $2::uuid, coalesce(max(version), 0) + 1
				   FROM approval_policy_versions
				  WHERE policy_id = $2
				 RETURNING id, version`,
				caller.TenantID, policyID,
			).Scan(&versionID, &version)
		}
		if err != nil {
			return err
		}

		// Whole-set delete then re-insert. The tuples this transaction deleted are already
		// dead to its own uniqueness check, so rewriting the same slots needs no renumbering
		// dance (the SetRoleMembers precedent, store.go:409-414).
		if _, err := tx.Exec(ctx,
			`DELETE FROM approval_policy_steps WHERE version_id = $1`, versionID); err != nil {
			return err
		}
		if err := insertPolicySteps(ctx, tx, caller.TenantID, versionID, rows); err != nil {
			return err
		}

		// The written draft supplies Version, Sealed and Status: it is unsealed by
		// construction, and it is the policy's top version because a version is only ever
		// minted as max+1 and sealing never mints one.
		takeTopVersion(&p, PolicyVersion{Version: version})

		// GetPolicy's version read. Not in the plan's enumerated statement order, but the
		// PUT answers a whole Policy and a versions: [] here would contradict the very next
		// GET of the same row.
		versions, err := tx.Query(ctx,
			`SELECT `+policyVersionColumns+`
			   FROM approval_policy_versions
			  WHERE policy_id = $1
			  ORDER BY version DESC`, policyID)
		if err != nil {
			return err
		}
		for versions.Next() {
			_, _, pv, err := scanPolicyVersionRow(versions)
			if err != nil {
				versions.Close()
				return err
			}
			p.Versions = append(p.Versions, pv)
		}
		versions.Close()
		if err := versions.Err(); err != nil {
			return err
		}

		// Read back rather than echoed from the request: numeric(14,2) normalises scale on
		// write, so a step sent as "0" is stored as 0.00 and a response assembled in memory
		// would disagree with the next GET of the same row
		// (TestPutDraft_CondAmountScaleIsCanonicalInTheResponse).
		trees, err := readPolicyTrees(ctx, tx, []string{versionID})
		if err != nil {
			return err
		}
		p.Steps = trees[versionID]

		// Last statement in the closure: a failing audit write rolls the whole rewrite back
		// (TestPutDraft_AuditsInSameTx). version is the WRITTEN draft's, the forked max+1
		// when a fork happened.
		return audit.Record(ctx, tx, caller.Subject, "approval_policy.updated", map[string]any{
			"policy_id": p.ID,
			"version":   version,
		})
	})
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}

// PublishPolicy seals a policy's open draft and makes it the tenant's active version.
//
// Publishing a NEW version that names a dead role is refused at the door; a role deleted
// AFTER publish leaves the sealed version active and its step blocking.
// TestPublish_RejectsDanglingRole and TestPublish_RoleDeletedAfterPublishLeavesVersionActive
// are the pair.
//
// No body is read at all: published_by is the caller's subject and published_at is now().
// Statement order is the security property, as in PutDraft — the id is parsed ABOVE the
// transaction so a malformed uuid never reaches SQL as a 22P02 that carries no sentinel,
// and requireActiveAdmin is the first statement in the closure.
func (s *Store) PublishPolicy(ctx context.Context, id string) (Policy, error) {
	// GetPolicy's parse and its choice of sentinel: 400 against 404 on a path resource
	// would be an existence oracle.
	u, err := uuid.Parse(id)
	if err != nil {
		return Policy{}, ErrPolicyNotFound
	}

	p := newPolicy()
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)

		if err := requireActiveAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}

		// THE lock, and the only one that matters. Publish writes approval_policy_versions
		// and nothing else, so without this row lock a concurrent PutDraft holding it sails
		// past, and its DELETE FROM approval_policy_steps then dies 23001 under the version
		// this sealed. FOR UPDATE on the version row would not serialise them — PutDraft
		// never locks that row. Must stay the first row-read, mirroring PutDraft above.
		if err := tx.QueryRow(ctx,
			`SELECT id, name, scope
			   FROM approval_policies
			  WHERE id = $1 AND deleted_at IS NULL
			    FOR UPDATE`, u.String(),
		).Scan(&p.ID, &p.Name, &p.Scope); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPolicyNotFound
			}
			return err
		}

		// NOT sealed, never "the active version": one_active spans the tenant, so a policy
		// can hold sealed versions with no active one. one_draft caps this at one row.
		var versionID string
		var version int
		if err := tx.QueryRow(ctx,
			`SELECT id, version
			   FROM approval_policy_versions
			  WHERE policy_id = $1 AND NOT sealed`, p.ID,
		).Scan(&versionID, &version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPolicyNothingToPublish
			}
			return err
		}

		// One read, two jobs: the gate's input and the response's tree. Publish writes no
		// step, so re-reading after the seal would only repeat this.
		trees, err := readPolicyTrees(ctx, tx, []string{versionID})
		if err != nil {
			return err
		}
		p.Steps = trees[versionID]

		// ListRoles' predicate. Collected here rather than taken from liveRoleKeys, which is
		// a test-only superuser read-back returning []string.
		roles, err := tx.Query(ctx, `SELECT key FROM workflow_roles WHERE deleted_at IS NULL`)
		if err != nil {
			return err
		}
		liveKeys := map[string]bool{}
		for roles.Next() {
			var key string
			if err := roles.Scan(&key); err != nil {
				roles.Close()
				return err
			}
			liveKeys[key] = true
		}
		// Closed explicitly rather than deferred: the statements below reuse this
		// transaction's connection.
		roles.Close()
		if err := roles.Err(); err != nil {
			return err
		}

		if err := validateForPublish(p.Steps, liveKeys); err != nil {
			return err
		}

		// Its own statement, and TENANT-wide: one_active is ON (tenant_id) WHERE is_active,
		// so publishing this policy deactivates whichever policy held the slot. No policy
		// predicate — RLS is the tenant scope. Deactivation is not un-publishing; sealed,
		// published_at and published_by are untouched
		// (TestPublish_DeactivatesTheTenantsOtherPolicy).
		if _, err := tx.Exec(ctx,
			`UPDATE approval_policy_versions SET is_active = false WHERE is_active`); err != nil {
			return err
		}

		// One statement, after the deactivation: is_active is never set on a row that is not
		// simultaneously becoming sealed, or approval_policy_versions_active_is_sealed raises
		// 23514. now() is the transaction timestamp, so published_at is the audit row's
		// created_at (TestPublish_StampsActorAndTxTimestamp).
		//
		// No RowsAffected guard: the row resolved above cannot vanish under the policy lock —
		// invoice_app holds no DELETE on this table (TestRLS_ApprovalPolicyTablesGrantMatrix).
		if _, err := tx.Exec(ctx,
			`UPDATE approval_policy_versions
			    SET sealed = true, is_active = true, published_at = now(), published_by = $2
			  WHERE id = $1`, versionID, caller.Subject); err != nil {
			// A concurrent publish won the tenant's slot. By constraint name, so a 23505 on
			// any other unique stays a 500, and never retried: a retry would publish a tree
			// this caller did not re-validate.
			if uniqueViolationOn(err, "approval_policy_versions_one_active") {
				return ErrConflict
			}
			return err
		}

		// AFTER the seal, and that ordering is load-bearing: ArmTx resolves the version by
		// `WHERE is_active`, so a sweep placed any earlier arms the version this publish just
		// replaced — or, on a tenant's first publish, nothing at all.
		if err := s.sweepValidatedBacklog(ctx, tx, caller.TenantID, caller.Subject); err != nil {
			return err
		}

		// Read AFTER the seal, or the response answers sealed:false, is_active:false,
		// published_at:null on a row that is none of those
		// (TestPublish_EmptyPolicyAllowed). The just-sealed version is the top one: a version
		// is only ever minted as max+1 and sealing mints none.
		versions, err := tx.Query(ctx,
			`SELECT `+policyVersionColumns+`
			   FROM approval_policy_versions
			  WHERE policy_id = $1
			  ORDER BY version DESC`, p.ID)
		if err != nil {
			return err
		}
		for versions.Next() {
			_, _, pv, err := scanPolicyVersionRow(versions)
			if err != nil {
				versions.Close()
				return err
			}
			if len(p.Versions) == 0 {
				takeTopVersion(&p, pv)
			}
			p.Versions = append(p.Versions, pv)
		}
		versions.Close()
		if err := versions.Err(); err != nil {
			return err
		}

		// Last statement in the closure: a failing audit write rolls the seal back
		// (TestPublish_AuditsInSameTx). version is the SEALED version's number.
		return audit.Record(ctx, tx, caller.Subject, "approval_policy.published", map[string]any{
			"policy_id": p.ID,
			"version":   version,
		})
	})
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}

// sweepValidatedBacklog arms one run per invoice sitting at `validated` with no live run,
// inside PublishPolicy's transaction. This IS the backfill — there is no data migration
// and no background pass — so when publish answers 200 the new version already governs the
// whole backlog, not only invoices validated after it. A second publish arms zero: every
// invoice the first sweep touched now carries a run the anti-join excludes.
//
// RLS is the only tenant filter, as everywhere else in this store. tenantID is passed
// through for the run row ArmTx stamps, not used as a predicate.
func (s *Store) sweepValidatedBacklog(ctx context.Context, tx pgx.Tx, tenantID, actor string) error {
	// FOR UPDATE is required, not defensive: ArmTx documents that its caller already holds
	// the invoice row lock, and this transaction's only other lock is on approval_policies.
	// Without it a concurrent Store.Edit demotion between this SELECT and the arm below
	// leaves an open run on a draft invoice — the approval_run_orphaned drift.
	//
	// `OF i` keeps the anti-join's approval_runs unlocked. ORDER BY i.id gives concurrent
	// sweeps one lock order. LIMIT sweepCap+1 bounds the read and still tells at-cap from
	// over-cap apart.
	rows, err := tx.Query(ctx,
		`SELECT i.id
		   FROM invoices i
		  WHERE i.status = 'validated'
		    AND NOT EXISTS (
		        SELECT 1 FROM approval_runs r
		         WHERE r.invoice_id = i.id AND r.state IN ('open','approved')
		    )
		  ORDER BY i.id
		  LIMIT $1
		    FOR UPDATE OF i`, sweepCap+1)
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
	// Closed before the loop, not deferred: ArmTx reuses this transaction's connection,
	// which an open cursor holds. The row locks are unaffected — they live until commit.
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Strictly greater. Exactly sweepCap publishes and arms
	// (TestPublish_SweepAtExactlyCapSucceeds); one more refuses whole
	// (TestPublish_SweepAboveCapReturns409), rolling the seal back with it.
	if len(ids) > sweepCap {
		return ErrSweepCapExceeded
	}
	if len(ids) == 0 {
		return nil
	}
	// Fail closed rather than arm every run with an empty content_fingerprint, which no
	// later comparison could tell from a real one.
	if s.fingerprinter == nil {
		return errors.New("approval: publish sweep has no fingerprinter — cmd/invoice/main.go wires invoice.FingerprintTx")
	}

	// No SAVEPOINT per invoice: the anti-join plus the row lock closes the 23505 window on
	// approval_runs_one_open, so an error here is a real invariant breach that must roll the
	// publish back rather than skip one invoice. sweepCap subtransactions would also overflow
	// the backend's 64-entry subxid cache, which is a cost paid by every other session.
	for _, id := range ids {
		fp, err := s.fingerprinter(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := ArmTx(ctx, tx, tenantID, id, fp, actor); err != nil {
			return err
		}
	}
	return nil
}

// DeletePolicy stamps deleted_at and, in the same transaction, deactivates the version the
// policy was governing with: without that a soft-deleted policy keeps deciding every
// invoice while being invisible to every read. Deactivation is not un-publishing —
// seal_guard refuses only sealed -> unsealed, and published_at/by survive.
//
// This deletes a POLICY, not an approval DECISION (Decision Q12): no version row is ever
// removed. invoice_app holds no DELETE on approval_policy_versions, approval_decisions is
// GRANT SELECT, INSERT only, and approval_runs -> approval_policy_versions is ON DELETE
// RESTRICT.
//
// The returned Policy is inert: only ID, Name and Scope are carried through, so a policy
// published at v3 still answers status "draft", version 0, steps [] and versions []
// (the DeleteRole precedent, store.go:309-312).
func (s *Store) DeletePolicy(ctx context.Context, id string) (Policy, error) {
	// GetPolicy's parse and its choice of sentinel: 400 against 404 on a path resource
	// would be an existence oracle.
	u, err := uuid.Parse(id)
	if err != nil {
		return Policy{}, ErrPolicyNotFound
	}

	p := newPolicy()
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)

		if err := requireActiveAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}

		// Must stay the first statement touching a policy row: the row-level exclusive lock it
		// takes is what serialises this against PublishPolicy's FOR UPDATE, in that lock order
		// (TestDeletePolicy_StampsThePolicyRowBeforeTheVersionWrite — the race spec passes
		// against a later stamp too). deleted_at IS NULL is both the
		// existence predicate and the idempotency mechanism — under READ COMMITTED a second
		// delete re-evaluates it, matches nothing, and is ErrPolicyNotFound rather than a
		// re-stamp. now() is the transaction timestamp the audit row's created_at also takes.
		if err := tx.QueryRow(ctx,
			`UPDATE approval_policies
			    SET deleted_at = now()
			  WHERE id = $1 AND deleted_at IS NULL
			RETURNING id, name, scope`, u.String(),
		).Scan(&p.ID, &p.Name, &p.Scope); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPolicyNotFound
			}
			return err
		}

		// Policy-scoped, unlike publish's tenant-wide deactivation: this policy is the one
		// leaving. No RowsAffected guard — 0 rows is the never-published case
		// (TestDeletePolicy_UnpublishedPolicyKeepsItsDraft).
		if _, err := tx.Exec(ctx,
			`UPDATE approval_policy_versions
			    SET is_active = false
			  WHERE policy_id = $1 AND is_active`, p.ID); err != nil {
			return err
		}

		// The AUDIT payload's number and nothing else's: Version names the version Steps
		// belongs to, and Steps is [] (TestDeletePolicy_ReturnsAnInertDraftShape). coalesce
		// is the handling for a policy carrying no version row at all.
		var version int
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(version), 0)
			   FROM approval_policy_versions
			  WHERE policy_id = $1`, p.ID,
		).Scan(&version); err != nil {
			return err
		}

		// Last statement in the closure: a failing audit write rolls the stamp and the
		// deactivation back (TestDeletePolicy_AuditsInSameTx).
		return audit.Record(ctx, tx, caller.Subject, "approval_policy.deleted", map[string]any{
			"policy_id": p.ID,
			"version":   version,
		})
	})
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}

// stepArrays is the nine columns both INSERT batches share, one slice per column: unnest
// takes an array per column, never an array of rows. The nullable seven bind as []*string
// and []*int, which is what carries a NULL through a batch that also holds values
// (TestPutDraft_NullableColumnsRoundTripInOneBatch).
type stepArrays struct {
	ids            []string
	ords           []int
	kinds          []string
	roleKeys       []*string
	slaHours       []*int
	condOps        []*string
	condAmounts    []*string
	notifyTargets  []*string
	notifyChannels []*string
}

func stepArraysOf(rows []stepRow) stepArrays {
	a := stepArrays{
		ids:            make([]string, len(rows)),
		ords:           make([]int, len(rows)),
		kinds:          make([]string, len(rows)),
		roleKeys:       make([]*string, len(rows)),
		slaHours:       make([]*int, len(rows)),
		condOps:        make([]*string, len(rows)),
		condAmounts:    make([]*string, len(rows)),
		notifyTargets:  make([]*string, len(rows)),
		notifyChannels: make([]*string, len(rows)),
	}
	for i, r := range rows {
		a.ids[i], a.ords[i], a.kinds[i] = r.ID, r.Ord, r.Kind
		a.roleKeys[i], a.slaHours[i] = r.WorkflowRoleKey, r.SLAHours
		a.condOps[i], a.condAmounts[i] = r.CondOp, r.CondAmount
		a.notifyTargets[i], a.notifyChannels[i] = r.NotifyTarget, r.NotifyChannel
	}
	return a
}

// insertPolicySteps writes one version's whole flattened tree in two statements, roots
// then children. The roots statement omits parent_step_id and branch entirely rather than
// binding an all-NULL array, and partitioning on ParentStepID == nil is where the
// depth-two invariant becomes explicit at the call site. That invariant is validateTree's
// (policy.go:227-232), not approval_policy_steps_depth_cap's — the CHECK forbids a
// condition CHILD, not depth.
//
// ord is bound explicitly, never derived by WITH ORDINALITY: ord restarts in every lane,
// so array position is right for the roots and wrong for the children, where a two-lane
// condition must write ord 0 twice (TestPutDraft_TwoLaneConditionKeepsPerLaneOrd).
// Zero-length arrays insert zero rows, so an empty tree needs no branch.
func insertPolicySteps(ctx context.Context, tx pgx.Tx, tenantID, versionID string, rows []stepRow) error {
	roots, children := []stepRow{}, []stepRow{}
	for _, r := range rows {
		if r.ParentStepID == nil {
			roots = append(roots, r)
			continue
		}
		children = append(children, r)
	}

	// Ids bind as text[] and cast, the treatment cond_amount gets: text[] is what pgx
	// encodes without inference risk.
	r := stepArraysOf(roots)
	if _, err := tx.Exec(ctx,
		`INSERT INTO approval_policy_steps
		     (tenant_id, version_id, id, ord, kind, workflow_role_key, sla_hours,
		      cond_op, cond_amount, notify_target, notify_channel)
		 SELECT $1::uuid, $2::uuid, s.id::uuid, s.ord, s.kind, s.role_key, s.sla,
		        s.cond_op, s.cond_amount::numeric, s.notify_target, s.notify_channel
		   FROM unnest($3::text[], $4::int[], $5::text[], $6::text[], $7::int[],
		               $8::text[], $9::text[], $10::text[], $11::text[])
		        AS s(id, ord, kind, role_key, sla, cond_op, cond_amount, notify_target, notify_channel)`,
		tenantID, versionID, r.ids, r.ords, r.kinds, r.roleKeys, r.slaHours,
		r.condOps, r.condAmounts, r.notifyTargets, r.notifyChannels,
	); err != nil {
		return err
	}

	c := stepArraysOf(children)
	parents := make([]string, len(children))
	branches := make([]string, len(children))
	for i, row := range children {
		// Both non-NULL by construction: flattenLane sets parent and branch together, so a
		// half-populated child — which nestSteps would silently promote to a root — is
		// unreachable from this path.
		parents[i], branches[i] = *row.ParentStepID, *row.Branch
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO approval_policy_steps
		     (tenant_id, version_id, id, parent_step_id, branch, ord, kind,
		      workflow_role_key, sla_hours, cond_op, cond_amount, notify_target, notify_channel)
		 SELECT $1::uuid, $2::uuid, s.id::uuid, s.parent::uuid, s.branch, s.ord, s.kind,
		        s.role_key, s.sla, s.cond_op, s.cond_amount::numeric, s.notify_target, s.notify_channel
		   FROM unnest($3::text[], $4::text[], $5::text[], $6::int[], $7::text[],
		               $8::text[], $9::int[], $10::text[], $11::text[], $12::text[], $13::text[])
		        AS s(id, parent, branch, ord, kind, role_key, sla, cond_op, cond_amount,
		             notify_target, notify_channel)`,
		tenantID, versionID, c.ids, parents, branches, c.ords, c.kinds,
		c.roleKeys, c.slaHours, c.condOps, c.condAmounts, c.notifyTargets, c.notifyChannels,
	)
	return err
}
