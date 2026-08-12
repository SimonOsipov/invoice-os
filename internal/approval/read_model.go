package approval

import (
	"encoding/json"
	"time"
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

// RunDecision is one approval_run_decisions row.
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

// isApprover mirrors internal/invoice/store.go:1412 -- kept in sync by inspection, not import
// (unexported across packages). Also mirrors roles.ts's isApprover (roles.ts:405-407).
func isApprover(accessRole string) bool {
	return false // stub: executor implements
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
	return resolutionResult{} // stub: executor implements
}

// resolveHolder mirrors roles.ts:119-124 (resolve) over resolution().
func resolveHolder(roleExists bool, holders []holderInput) Resolved {
	return Resolved{} // stub: executor implements
}

// inspectorResolveHolder mirrors roles.ts:127-133 (inspectorResolve) over the
// same resolution() -- deliberately omits +N (roles.ts:126). Its only caller
// in this story is the mirror test (D22).
func inspectorResolveHolder(roleExists bool, holders []holderInput) Resolved {
	return Resolved{} // stub: executor implements
}

// holderName mirrors toMember's display_name ?? email ?? user_id ladder (members.ts:546).
func holderName(displayName, email *string, userID string) string {
	return "" // stub: executor implements
}

// roleTitle mirrors roleOf's deleted-role fallback (roles.ts:63).
func roleTitle(roleExists bool, liveTitle string) string {
	return "" // stub: executor implements
}
