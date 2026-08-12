package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// the only tenant scope -- no role gate.
//
// STUB for APPR-07-02's Test-Spec stage (task-487): the query logic is the executor's,
// not this subtask's. Returns the zero Run and a nil error so every RED test in
// read_model_db_test.go fails on an assertion, never a compile error.
func (s *Store) ApprovalRun(ctx context.Context, invoiceID string) (Run, error) {
	return Run{}, nil
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
