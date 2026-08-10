package approval

// STUB — declarations only, so policy_test.go fails on an assertion or on
// "not implemented" rather than on a compile error. The bodies are Stage 3's.

import "errors"

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

// Sentinels for the policy seam. statusForErr in approval.go stays untouched;
// policyStatusForErr is a second mapper so its messages can name policies.
var (
	ErrPolicyNotFound         = errors.New("approval: policy not found")
	ErrPolicyStepRole         = errors.New("approval: step names an unknown workflow role")
	ErrPolicyEmptyBranches    = errors.New("approval: condition has two empty lanes")
	ErrPolicyNothingToPublish = errors.New("approval: nothing to publish")
)

// flattenSteps mints a fresh uuid per step and derives parent_step_id/branch/ord.
func flattenSteps(tree []stepInput) ([]stepRow, []string) { panic("not implemented") }

// nestSteps rebuilds the nested tree from flat rows.
func nestSteps(rows []stepRow) []Step { panic("not implemented") }

// validateTree is the pre-tx structural gate.
func validateTree(tree []stepInput) error { panic("not implemented") }

// validateForPublish is the publish-time gate over the stored tree.
func validateForPublish(tree []Step, liveKeys map[string]bool) error { panic("not implemented") }

// normalizeScope is the one place a scope reaches its stored form; its output set
// is exactly the set approval_policies_scope_check accepts.
func normalizeScope(scope string) (string, error) { panic("not implemented") }

func policyStatusForErr(err error) (status int, msg string) { panic("not implemented") }
