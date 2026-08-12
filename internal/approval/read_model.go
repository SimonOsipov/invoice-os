package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Resolved is the card/canvas/inspector shared shape -- amber warning travels
// with the text (roles.ts:34).
type Resolved struct {
	Text string `json:"text"`
	Warn bool   `json:"warn"`
}

// RunStep is one approval_run_steps row. No omitempty: the key set stays
// fixed whatever the kind (Step's precedent, policy.go:17-30).
type RunStep struct {
	Ord               int        `json:"ord"`
	Kind              string     `json:"kind"`
	State             string     `json:"state"`
	WorkflowRoleKey   *string    `json:"workflow_role_key"`
	WorkflowRoleTitle *string    `json:"workflow_role_title"`
	Holder            *Resolved  `json:"holder"`
	SLAHours          *int       `json:"sla_hours"`
	DueAt             *time.Time `json:"due_at"`
	Overdue           bool       `json:"overdue"`
	SatisfiedAt       *time.Time `json:"satisfied_at"`
	SatisfiedBy       *string    `json:"satisfied_by"`
	NotifyTarget      *string    `json:"notify_target"`
	NotifyChannel     *string    `json:"notify_channel"`
}

// RunDecision is one approval_decisions row.
type RunDecision struct {
	RunStepID string    `json:"run_step_id"`
	Ord       int       `json:"ord"`
	Decision  string    `json:"decision"`
	Actor     string    `json:"actor"`
	DecidedAt time.Time `json:"decided_at"`
	Reason    *string   `json:"reason"`
}

// Run is the read model for GET/POST .../approval[s]. Never embed anonymously --
// the promoted MarshalJSON would hijack the outer struct.
type Run struct {
	RunID     string        `json:"run_id"`
	State     string        `json:"state"`
	OpenedAt  time.Time     `json:"opened_at"`
	ClosedAt  *time.Time    `json:"closed_at"`
	ClosedBy  *string       `json:"closed_by"`
	Steps     []RunStep     `json:"steps"`
	Decisions []RunDecision `json:"decisions"`
}

// MarshalJSON: same []-never-null rule as Policy's (policy.go:70-80).
func (r Run) MarshalJSON() ([]byte, error) {
	type runJSON Run
	v := runJSON(r)
	if v.Steps == nil {
		v.Steps = []RunStep{}
	}
	if v.Decisions == nil {
		v.Decisions = []RunDecision{}
	}
	return json.Marshal(v)
}

// ErrRunNotFound is the run-read sentinel: unknown, cross-tenant, malformed-uuid and
// no-run-row invoice ids all answer alike (the GetPolicy no-oracle rule, policy.go:136).
var ErrRunNotFound = errors.New("approval: run not found")

// RunReader is the read-model seam, declared beside the method that satisfies it
// (policy_store.go:16-30's shape).
type RunReader func(ctx context.Context, invoiceID string) (Run, error)

var _ RunReader = new(Store).ApprovalRun

// ApprovalRun assembles one invoice's most recent approval run: its steps in ord order,
// holder/title resolved through resolveHolder/roleTitle, and its decision ledger. RLS is
// the only tenant scope -- no role gate, no tenant_id predicate anywhere below
// (store.go:27-30).
//
// Run -> steps -> decisions -> the distinct role keys' holders, each its own statement
// stitched in Go by index map -- this package's readPolicyTrees/ListRoles shape, not a
// JOIN (there are none elsewhere in this package's non-test code).
func (s *Store) ApprovalRun(ctx context.Context, invoiceID string) (Run, error) {
	// GetPolicy's parse and sentinel choice (policy_store.go:218-221): malformed input
	// never reaches SQL as an unmappable 22P02.
	u, err := uuid.Parse(invoiceID)
	if err != nil {
		return Run{}, ErrRunNotFound
	}

	run := Run{Steps: []RunStep{}, Decisions: []RunDecision{}}
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT id, state, opened_at, closed_at, closed_by
			   FROM approval_runs
			  WHERE invoice_id = $1
			  ORDER BY opened_at DESC
			  LIMIT 1`, u.String(),
		).Scan(&run.RunID, &run.State, &run.OpenedAt, &run.ClosedAt, &run.ClosedBy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRunNotFound
			}
			return err
		}

		return assembleRunStepsAndDecisions(ctx, tx, &run)
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// assembleRunStepsAndDecisions fills run.Steps and run.Decisions for an already-resolved
// run.RunID -- the tx-scoped half ApprovalRun and decideTx both call (defect finding,
// item 3), so POST's response body matches the same run's fresh GET rather than
// duplicating this assembly. Five statements (steps, three role-resolution queries,
// decisions); ApprovalRun's own run-header lookup above is the sixth
// (TestApprovalRun_SixStatementsRegardlessOfStepAndRoleCount).
func assembleRunStepsAndDecisions(ctx context.Context, tx pgx.Tx, run *Run) error {
	run.Steps = []RunStep{}
	run.Decisions = []RunDecision{}

	// stepOrd resolves the decision ledger's ord (AC-6): approval_decisions has no
	// ord column of its own, so it comes from the step it decided on, without a join.
	stepOrd := map[string]int{}
	roleKeys := []string{}
	seenKeys := map[string]bool{}

	rows, err := tx.Query(ctx,
		`SELECT id, ord, kind, workflow_role_key, sla_hours, due_at, state,
		        satisfied_at, satisfied_by, notify_target, notify_channel
		   FROM approval_run_steps
		  WHERE run_id = $1
		  ORDER BY ord`, run.RunID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var st RunStep
		if err := rows.Scan(&id, &st.Ord, &st.Kind, &st.WorkflowRoleKey, &st.SLAHours, &st.DueAt,
			&st.State, &st.SatisfiedAt, &st.SatisfiedBy, &st.NotifyTarget, &st.NotifyChannel); err != nil {
			rows.Close()
			return err
		}
		// AC-5: gated on state, not just a due_at comparison -- due_at is stamped by
		// kind (engine.go:225-226), so an autoapprove-settled step can still carry one.
		st.Overdue = st.State == "pending" && st.DueAt != nil && st.DueAt.Before(time.Now())
		stepOrd[id] = st.Ord
		if st.WorkflowRoleKey != nil && !seenKeys[*st.WorkflowRoleKey] {
			seenKeys[*st.WorkflowRoleKey] = true
			roleKeys = append(roleKeys, *st.WorkflowRoleKey)
		}
		run.Steps = append(run.Steps, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	exists, titles, holders, err := resolveRunRoles(ctx, tx, roleKeys)
	if err != nil {
		return err
	}
	for i := range run.Steps {
		// Gated on the role key being present, never on kind == "approval": nothing in
		// the schema stops a role key on a notify/autoapprove step (policy.go:262-269),
		// and running a nil key through resolveHolder(false, nil) would wrongly answer
		// "Role no longer exists" instead of leaving both fields null.
		key := run.Steps[i].WorkflowRoleKey
		if key == nil {
			continue
		}
		title := roleTitle(exists[*key], titles[*key])
		run.Steps[i].WorkflowRoleTitle = &title
		holder := resolveHolder(exists[*key], holders[*key])
		run.Steps[i].Holder = &holder
	}

	drows, err := tx.Query(ctx,
		`SELECT run_step_id, decision, actor, decided_at, reason
		   FROM approval_decisions
		  WHERE run_id = $1
		  ORDER BY decided_at`, run.RunID)
	if err != nil {
		return err
	}
	for drows.Next() {
		var d RunDecision
		if err := drows.Scan(&d.RunStepID, &d.Decision, &d.Actor, &d.DecidedAt, &d.Reason); err != nil {
			drows.Close()
			return err
		}
		d.Ord = stepOrd[d.RunStepID]
		run.Decisions = append(run.Decisions, d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return err
	}
	// decided_at then ord (AC-6): ord is only known after the step pass above, so the
	// tie-break is finished here rather than in SQL.
	sort.SliceStable(run.Decisions, func(i, j int) bool {
		a, b := run.Decisions[i], run.Decisions[j]
		if !a.DecidedAt.Equal(b.DecidedAt) {
			return a.DecidedAt.Before(b.DecidedAt)
		}
		return a.Ord < b.Ord
	})

	return nil
}

// resolveRunRoles resolves title/exists and ordered holder inputs for a set of distinct
// role keys: the roles, then their staffing, then those holders' memberships -- three
// statements constant in the key count, the ListRoles/readPolicyTrees shape, never a
// JOIN and never a tenant_id predicate (RLS is the only scope, store.go:27-30).
func resolveRunRoles(ctx context.Context, tx pgx.Tx, keys []string) (exists map[string]bool, titles map[string]string, holders map[string][]holderInput, err error) {
	exists = map[string]bool{}
	titles = map[string]string{}
	holders = map[string][]holderInput{}

	roleKeyOf := map[string]string{} // role id -> key
	roleIDs := []string{}
	rows, err := tx.Query(ctx,
		`SELECT id, key, title FROM workflow_roles WHERE key = ANY($1::text[]) AND deleted_at IS NULL`, keys)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var id, key, title string
		if err := rows.Scan(&id, &key, &title); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		exists[key] = true
		titles[key] = title
		roleKeyOf[id] = key
		roleIDs = append(roleIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	// Holders must return in workflow_role_members.ord order: resolveHolder distinguishes
	// the first holder from the first ACTIVE one.
	userIDs := []string{}
	seenUsers := map[string]bool{}
	roleUsers := map[string][]string{} // role id -> user ids, in ord order
	memberRows, err := tx.Query(ctx,
		`SELECT workflow_role_id, user_id
		   FROM workflow_role_members
		  WHERE workflow_role_id = ANY($1::uuid[])
		  ORDER BY workflow_role_id, ord`, roleIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	for memberRows.Next() {
		var roleID, userID string
		if err := memberRows.Scan(&roleID, &userID); err != nil {
			memberRows.Close()
			return nil, nil, nil, err
		}
		roleUsers[roleID] = append(roleUsers[roleID], userID)
		if !seenUsers[userID] {
			seenUsers[userID] = true
			userIDs = append(userIDs, userID)
		}
	}
	memberRows.Close()
	if err := memberRows.Err(); err != nil {
		return nil, nil, nil, err
	}

	type memberInfo struct {
		displayName, email *string
		status, role       string
	}
	members := map[string]memberInfo{}
	mrows, err := tx.Query(ctx,
		`SELECT user_id, display_name, email, status, role
		   FROM memberships
		  WHERE user_id = ANY($1::uuid[])`, userIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	for mrows.Next() {
		var userID string
		var mi memberInfo
		if err := mrows.Scan(&userID, &mi.displayName, &mi.email, &mi.status, &mi.role); err != nil {
			mrows.Close()
			return nil, nil, nil, err
		}
		members[userID] = mi
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, nil, nil, err
	}

	for roleID, key := range roleKeyOf {
		for _, userID := range roleUsers[roleID] {
			mi := members[userID]
			holders[key] = append(holders[key], holderInput{
				Name:       holderName(mi.displayName, mi.email, userID),
				Status:     mi.status,
				AccessRole: mi.role,
			})
		}
	}
	return exists, titles, holders, nil
}

// isApprover mirrors internal/invoice/store.go:1412 -- kept in sync by inspection, not import
// (unexported across packages). Also mirrors roles.ts's isApprover (roles.ts:405-407).
func isApprover(accessRole string) bool {
	return accessRole == "admin" || accessRole == "reviewer"
}

// holderInput is one row of the ordered holder list `resolution` classifies --
// the plain-struct port of roles.ts's Member, no DB/store dependency.
type holderInput struct {
	Name       string
	Status     string // "active" | "suspended" | ...
	AccessRole string // "admin" | "reviewer" | "preparer"
}

type resolutionKind int

const (
	resMissing resolutionKind = iota
	resNone
	resBlocked
	resOK
)

type resolutionResult struct {
	kind    resolutionKind
	primary string
	extra   int
}

// resolution mirrors roles.ts:104-113's five states, so resolveHolder and
// inspectorResolveHolder cannot disagree about them. roleExists stands in for
// `list.some(r => r.key === key)`; holders is already in ord order.
func resolution(roleExists bool, holders []holderInput) resolutionResult {
	if !roleExists {
		return resolutionResult{kind: resMissing}
	}
	if len(holders) == 0 {
		return resolutionResult{kind: resNone}
	}
	var active []holderInput
	for _, h := range holders {
		if h.Status == "active" && isApprover(h.AccessRole) {
			active = append(active, h)
		}
	}
	extra := len(holders) - 1 // counts the OTHER holders, active or not -- roles.ts:109-110
	if len(active) == 0 {
		return resolutionResult{kind: resBlocked, primary: holders[0].Name, extra: extra}
	}
	return resolutionResult{kind: resOK, primary: active[0].Name, extra: extra}
}

// resolveHolder mirrors roles.ts:119-124 (resolve) over resolution().
func resolveHolder(roleExists bool, holders []holderInput) Resolved {
	res := resolution(roleExists, holders)
	switch res.kind {
	case resMissing:
		return Resolved{Text: "Role no longer exists", Warn: true}
	case resNone:
		return Resolved{Text: "Nobody assigned", Warn: true}
	case resBlocked:
		return Resolved{Text: withExtra(res.primary, res.extra), Warn: true}
	default: // resOK
		return Resolved{Text: withExtra(res.primary, res.extra), Warn: false}
	}
}

// inspectorResolveHolder mirrors roles.ts:127-133 (inspectorResolve) over the
// same resolution() -- deliberately omits +N (roles.ts:126). Its only caller
// in this story is the mirror test (D22).
func inspectorResolveHolder(roleExists bool, holders []holderInput) Resolved {
	res := resolution(roleExists, holders)
	switch res.kind {
	case resMissing:
		return Resolved{Text: "Role no longer exists", Warn: true}
	case resNone:
		return Resolved{Text: "Nobody holds this role — this step will block", Warn: true}
	case resBlocked:
		return Resolved{Text: "Currently: " + res.primary + " — this step will block", Warn: true}
	default: // resOK
		return Resolved{Text: "Currently: " + res.primary, Warn: false}
	}
}

// withExtra appends " +N" when other holders exist, roles.ts:122's shape (resolve only).
func withExtra(primary string, extra int) string {
	if extra > 0 {
		return fmt.Sprintf("%s +%d", primary, extra)
	}
	return primary
}

// holderName mirrors toMember's display_name ?? email ?? user_id ladder (members.ts:546).
func holderName(displayName, email *string, userID string) string {
	if displayName != nil {
		return *displayName
	}
	if email != nil {
		return *email
	}
	return userID
}

// roleTitle mirrors roleOf's deleted-role fallback (roles.ts:63).
func roleTitle(roleExists bool, liveTitle string) string {
	if !roleExists {
		return "Deleted role"
	}
	return liveTitle
}
