package approval

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Step is the nested wire shape of one approval_policy_steps row. No omitempty on
// any field: the key set stays ten whatever the kind, and then/else are [] never null.
type Step struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"` // approval|condition|notify|autoapprove
	WorkflowRoleKey *string `json:"workflow_role_key"`
	SLAHours        *int    `json:"sla_hours"` // null = no deadline
	CondOp          *string `json:"cond_op"`
	CondAmount      *string `json:"cond_amount"` // numeric(14,2) as decimal text
	NotifyTarget    *string `json:"notify_target"`
	NotifyChannel   *string `json:"notify_channel"`
	Then            []Step  `json:"then"`
	Else            []Step  `json:"else"`
}

// MarshalJSON substitutes [] for a nil lane, which struct tags cannot do. Nested
// steps re-enter it, so depth is covered (TestPolicy_StepMarshalsTenKeys).
//
// Never embed Step anonymously in a response struct: the promoted method would
// hijack the outer struct and drop its sibling fields.
func (s Step) MarshalJSON() ([]byte, error) {
	type stepJSON Step
	v := stepJSON(s)
	if v.Then == nil {
		v.Then = []Step{}
	}
	if v.Else == nil {
		v.Else = []Step{}
	}
	return json.Marshal(v)
}

type PolicyVersion struct {
	Version     int     `json:"version"`
	Sealed      bool    `json:"sealed"`
	IsActive    bool    `json:"is_active"`
	PublishedAt *string `json:"published_at"`
	PublishedBy *string `json:"published_by"`
}

type Policy struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Scope    string          `json:"scope"`
	Status   string          `json:"status"`  // "draft" if an unsealed version exists, else "published"
	Version  int             `json:"version"` // the version steps belongs to
	Sealed   bool            `json:"sealed"`
	Steps    []Step          `json:"steps"`
	Versions []PolicyVersion `json:"versions"` // newest first
}

// MarshalJSON: same [] never null rule as Step's, for a stepless policy
// (TestPolicy_PolicyMarshalsStableKeys). Do not embed Policy anonymously either.
func (p Policy) MarshalJSON() ([]byte, error) {
	type policyJSON Policy
	v := policyJSON(p)
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	if v.Versions == nil {
		v.Versions = []PolicyVersion{}
	}
	return json.Marshal(v)
}

// stepInput declares no id field, so a client-supplied one is dropped at decode.
type stepInput struct {
	Kind            string      `json:"kind"`
	WorkflowRoleKey *string     `json:"workflow_role_key"`
	SLAHours        *int        `json:"sla_hours"`
	CondOp          *string     `json:"cond_op"`
	CondAmount      *string     `json:"cond_amount"`
	NotifyTarget    *string     `json:"notify_target"`
	NotifyChannel   *string     `json:"notify_channel"`
	Then            []stepInput `json:"then"`
	Else            []stepInput `json:"else"`
}

type createPolicyRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"` // optional; "" means the default scope
}

// Steps is *[]stepInput: a nil slice means "clear the tree" at the store, so
// without the presence check {} would silently wipe a policy's steps.
type putDraftRequest struct {
	Name  *string      `json:"name"`
	Scope *string      `json:"scope"`
	Steps *[]stepInput `json:"steps"`
}

// stepRow is one approval_policy_steps row, minus the columns the store owns
// (tenant_id, version_id, created_at).
type stepRow struct {
	ID              string  // server-minted uuid
	ParentStepID    *string // nil at root
	Branch          *string // nil at root; "then"/"else" in a lane
	Ord             int     // 0-based, dense, per lane
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	CondOp          *string
	CondAmount      *string
	NotifyTarget    *string
	NotifyChannel   *string
}

const (
	policyScopeAll     = "All invoices"
	maxPolicyBodyBytes = 64 * 1024 // the maxStaffBodyBytes precedent; no step-count cap

	// sweepCap ceilings the publish sweep's transaction. A literal, deliberately not
	// raisable by env or request — see docs/approvals.md §5 for the operator path.
	sweepCap = 5000
)

// Sentinels for the policy seam. statusForErr in approval.go stays untouched;
// policyStatusForErr is a second mapper so its messages can name policies.
var (
	ErrPolicyNotFound         = errors.New("approval: policy not found")
	ErrPolicyStepRole         = errors.New("approval: step names an unknown workflow role")
	ErrPolicyEmptyBranches    = errors.New("approval: condition has two empty lanes")
	ErrPolicyNothingToPublish = errors.New("approval: nothing to publish")
	ErrSweepCapExceeded       = errors.New("approval: validated backlog exceeds the publish sweep cap")
)

// The sets the approval_policy_steps CHECK constraints accept.
var (
	policyStepKinds = map[string]bool{"approval": true, "condition": true, "notify": true, "autoapprove": true}
	policyCondOps   = map[string]bool{">": true, ">=": true, "<": true, "<=": true}
)

// condAmountCeiling is the first value numeric(14,2) cannot hold.
var condAmountCeiling = decimal.NewFromInt(1_000_000_000_000)

// flattenSteps mints a fresh uuid per step and derives parent_step_id/branch/ord.
// Ids churn per call by design — nothing reads a step id back.
func flattenSteps(tree []stepInput) []stepRow {
	rows := make([]stepRow, 0, len(tree))
	flattenLane(&rows, tree, nil, nil)
	return rows
}

func flattenLane(rows *[]stepRow, lane []stepInput, parent, branch *string) {
	for i, s := range lane {
		// ord is 0-based and dense per lane, or approval_policy_steps_slot_uq
		// rejects the write.
		id := uuid.NewString()
		*rows = append(*rows, stepRow{
			ID:              id,
			ParentStepID:    parent,
			Branch:          branch,
			Ord:             i,
			Kind:            s.Kind,
			WorkflowRoleKey: s.WorkflowRoleKey,
			SLAHours:        s.SLAHours,
			CondOp:          s.CondOp,
			CondAmount:      s.CondAmount,
			NotifyTarget:    s.NotifyTarget,
			NotifyChannel:   s.NotifyChannel,
		})
		thenBranch, elseBranch := "then", "else"
		flattenLane(rows, s.Then, &id, &thenBranch)
		flattenLane(rows, s.Else, &id, &elseBranch)
	}
}

// nestSteps rebuilds the nested tree from flat rows, in each lane's ord order.
func nestSteps(rows []stepRow) []Step {
	roots := make([]stepRow, 0, len(rows))
	kids := map[string][]stepRow{}
	for _, r := range rows {
		if r.ParentStepID == nil || r.Branch == nil {
			roots = append(roots, r)
			continue
		}
		kids[laneKey(*r.ParentStepID, *r.Branch)] = append(kids[laneKey(*r.ParentStepID, *r.Branch)], r)
	}
	return nestLane(roots, kids)
}

func laneKey(parent, branch string) string { return parent + "\x00" + branch }

func nestLane(lane []stepRow, kids map[string][]stepRow) []Step {
	sort.SliceStable(lane, func(i, j int) bool { return lane[i].Ord < lane[j].Ord })
	out := make([]Step, 0, len(lane)) // non-nil, so a leaf's lanes are [] not null
	for _, r := range lane {
		out = append(out, Step{
			ID:              r.ID,
			Kind:            r.Kind,
			WorkflowRoleKey: r.WorkflowRoleKey,
			SLAHours:        r.SLAHours,
			CondOp:          r.CondOp,
			CondAmount:      r.CondAmount,
			NotifyTarget:    r.NotifyTarget,
			NotifyChannel:   r.NotifyChannel,
			Then:            nestLane(kids[laneKey(r.ID, "then")], kids),
			Else:            nestLane(kids[laneKey(r.ID, "else")], kids),
		})
	}
	return out
}

// validateTree is the pre-tx structural gate. An approval step with an empty role
// key is legal here — that gate is publish's (validateForPublish).
func validateTree(tree []stepInput) error {
	return validateLane(tree, false)
}

func validateLane(lane []stepInput, nested bool) error {
	for _, s := range lane {
		if !policyStepKinds[s.Kind] {
			return ErrValidation
		}
		// approval_policy_steps_depth_cap refuses a condition below the root.
		if s.Kind == "condition" && nested {
			return ErrValidation
		}
		if s.Kind != "condition" && (len(s.Then) > 0 || len(s.Else) > 0) {
			return ErrValidation
		}
		if err := validateStepFields(s); err != nil {
			return err
		}
		if err := validateLane(s.Then, true); err != nil {
			return err
		}
		if err := validateLane(s.Else, true); err != nil {
			return err
		}
	}
	return nil
}

// hasNUL reports whether p holds the one byte text will not take: Postgres raises 22021
// on it, carrying no constraint name, so policyStatusForErr answers 500 on client input
// (TestPolicy_ValidateTreeRefusesANULInEveryTextFieldAndKind). Refused rather than
// stripped — a name is not the server's to rewrite.
func hasNUL(p *string) bool { return p != nil && strings.IndexByte(*p, 0) >= 0 }

// validateStepFields also bounds the two numeric columns. An out-of-range value
// raises 22003, which carries no constraint name and so cannot be mapped to a 400
// downstream (TestPolicy_ValidateTreeCondAmountBounds, ...SlaHoursBounds). The
// bounds are checked wherever the field is present, not only on the kind that
// owns it, because the column is written whatever the kind.
func validateStepFields(s stepInput) error {
	// Kind is closed by policyStepKinds, cond_op by the vocabulary below and cond_amount by
	// decimal.NewFromString, so the three here are every client string that can still reach
	// a text column.
	for _, p := range []*string{s.WorkflowRoleKey, s.NotifyTarget, s.NotifyChannel} {
		if hasNUL(p) {
			return ErrValidation
		}
	}
	// Outside the switch, like the two bounds below: cond_op is written whatever the kind
	// carries it, and a value outside the four raises 23514 on
	// approval_policy_steps_cond_op_check, which carries no sentinel
	// (TestPolicy_ValidateTreeRefusesAForeignCondOpOnEveryKind).
	if s.CondOp != nil && !policyCondOps[*s.CondOp] {
		return ErrValidation
	}
	if s.SLAHours != nil && (*s.SLAHours < 0 || *s.SLAHours > math.MaxInt32) {
		return ErrValidation
	}
	if s.CondAmount != nil {
		if err := validateCondAmount(*s.CondAmount); err != nil {
			return err
		}
	}
	switch s.Kind {
	case "condition":
		// Presence only — the vocabulary is checked above, for every kind.
		if s.CondOp == nil {
			return ErrValidation
		}
		if s.CondAmount == nil {
			return ErrValidation
		}
	case "notify":
		if s.NotifyTarget == nil || strings.TrimSpace(*s.NotifyTarget) == "" {
			return ErrValidation
		}
		if s.NotifyChannel == nil || strings.TrimSpace(*s.NotifyChannel) == "" {
			return ErrValidation
		}
	}
	return nil
}

// validateCondAmount holds cond_amount inside numeric(14,2). Scale is rejected
// rather than rounded: 100.005 would store as 100.01, differing from what was sent.
func validateCondAmount(amount string) error {
	d, err := decimal.NewFromString(amount)
	if err != nil {
		return ErrValidation
	}
	if d.Exponent() < -2 {
		return ErrValidation
	}
	// Bound the exponent before comparing: Cmp rescales to the smaller exponent, so
	// "1e100000000" builds a 10^8-digit integer before it can be rejected. A zero
	// coefficient costs the same and numeric refuses "0e2000000000" anyway — hence no
	// zero short-circuit above this line
	// (TestPolicy_ValidateCondAmountRejectsAZeroTheColumnCannotHold).
	// Exponent 12 falls through to the ceiling, which rejects it; the rescale is bounded.
	if d.Exponent() > 12 || d.Abs().GreaterThanOrEqual(condAmountCeiling) {
		return ErrValidation
	}
	return nil
}

// validateForPublish is the gate at publish's door: an approval step must name a
// live workflow role, and a condition must have somewhere to go. An empty policy
// is publishable.
func validateForPublish(tree []Step, liveKeys map[string]bool) error {
	for _, s := range tree {
		switch s.Kind {
		case "approval":
			key := ""
			if s.WorkflowRoleKey != nil {
				key = *s.WorkflowRoleKey
			}
			if key == "" || !liveKeys[key] {
				return ErrPolicyStepRole
			}
		case "condition":
			if len(s.Then) == 0 && len(s.Else) == 0 {
				return ErrPolicyEmptyBranches
			}
		}
		if err := validateForPublish(s.Then, liveKeys); err != nil {
			return err
		}
		if err := validateForPublish(s.Else, liveKeys); err != nil {
			return err
		}
	}
	return nil
}

// normalizeName trims and refuses an empty name. Trim-and-validate rather than the
// inline strings.TrimSpace its siblings use, because two stores share this rule. A NUL is
// not whitespace, so the trim leaves it and only hasNUL stops it reaching text.
func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || hasNUL(&name) {
		return "", ErrValidation
	}
	return name, nil
}

// normalizeScope is the one place a scope reaches its stored form. Its OUTPUT set
// is exactly the set approval_policies_scope_check accepts, so no caller can send
// the column a value the CHECK refuses — "" is legal input and never a legal
// stored value. There is deliberately no second predicate to diverge from it.
func normalizeScope(scope string) (string, error) {
	switch strings.TrimSpace(scope) {
	case "", policyScopeAll: // absent means the default, not a value
		return policyScopeAll, nil
	default:
		return "", ErrValidation
	}
}

// policyStatusForErr is the policy seam's mapper. Messages are hand-written rather
// than err.Error() so the "approval: " sentinel prefix never reaches the SPA.
func policyStatusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, "invalid request"
	case errors.Is(err, ErrNotPermitted):
		return http.StatusForbidden, "only an admin can change approval policies"
	case errors.Is(err, ErrPolicyNotFound):
		return http.StatusNotFound, "approval policy not found"
	case errors.Is(err, ErrPolicyStepRole):
		return http.StatusConflict, "an approval step names a workflow role that no longer exists"
	case errors.Is(err, ErrPolicyEmptyBranches):
		return http.StatusConflict, "a condition must have at least one step in one of its two lanes"
	case errors.Is(err, ErrPolicyNothingToPublish):
		return http.StatusConflict, "this policy has no unpublished changes"
	// The message names the page rather than the remedy: the operator path is several
	// paragraphs long, so docs/approvals.md §5 carries it.
	case errors.Is(err, ErrSweepCapExceeded):
		return http.StatusConflict, "validated backlog exceeds the publish sweep cap — see docs/approvals.md"
	// The concurrent-publish loser, mapped from 23505 on
	// approval_policy_versions_one_active. Policy wording, not statusForErr's role-domain
	// string: the two mappers share the sentinel and nothing else.
	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "another version was published first — reload the policy and try again"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
