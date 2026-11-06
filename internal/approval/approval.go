// Package approval is the server behind Settings › Roles: workflow roles and who
// staffs them.
//
// "Role" is overloaded in this repo. approval.Role is the WORKFLOW role — an
// approval seat in workflow_roles. It is neither the Postgres/JWT role
// (auth.Identity.Role) nor the access role in memberships.role
// (admin/preparer/reviewer, backed by the global roles table), which this package
// never reads.
package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Role is the wire mirror of frontend/app/src/lib/roles.ts — four keys, so the SPA
// restructures no component. `desc` is the column `description`. No omitempty on
// any field: the key set stays four even when desc is "" and members is empty.
type Role struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Desc    string   `json:"desc"`
	Members []string `json:"members"` // user_ids in this role's own `ord` order
}

// Sentinels for the workflow-role write seam.
var (
	ErrValidation   = errors.New("approval: validation")
	ErrNotFound     = errors.New("approval: workflow role not found")
	ErrNotPermitted = errors.New("approval: not permitted")
	ErrConflict     = errors.New("approval: conflict")
	ErrRoleInUse    = errors.New("approval: workflow role is in use")
)

// roleInUseMessage is the 409 sentence when a policy in force names the role.
const roleInUseMessage = "this workflow role is named by a policy in force — publish each policy without the role, or delete the policy, first"

// PolicyRef names one policy in the role-in-use refusal.
type PolicyRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// roleInRunMessage is the 409 sentence when only an open run waits on the role.
const roleInRunMessage = "an open approval run is waiting on this workflow role — wait until it is decided, or edit its invoice to cancel it, first"

// roleLastHolderMessage is the 409 sentence when a staffing write leaves no signer on a role an open run waits on.
const roleLastHolderMessage = "an open approval run is waiting on this workflow role — keep at least one admin or reviewer on it until the run is decided, or edit its invoice to cancel it, first"

// InvoiceRef names one invoice whose open run waits on the role.
type InvoiceRef struct {
	ID     string `json:"id"`
	Number string `json:"invoice_number"`
}

// roleInUse is DeleteRole's and SetRoleMembers' refusal; it carries the policies and the waiting invoices
// (at most roleInRunListCap of invoicesTotal) so the handler can list them.
type roleInUse struct {
	policies      []PolicyRef
	invoices      []InvoiceRef
	invoicesTotal int
	lastHolder    bool // a staffing write left no signer; policies stay empty
}

func (e *roleInUse) Error() string { return ErrRoleInUse.Error() }
func (e *roleInUse) Unwrap() error { return ErrRoleInUse }

// message is the 409 sentence for the lists this refusal carries.
func (e *roleInUse) message() string {
	switch {
	case e.lastHolder:
		return roleLastHolderMessage
	case len(e.invoices) > 0 && len(e.policies) > 0:
		return roleInUseMessage + "; " + roleInRunMessage
	case len(e.invoices) > 0:
		return roleInRunMessage
	default:
		return roleInUseMessage
	}
}

// Body caps applied before decode — the platform server sets no limit of its own.
const (
	maxCreateBodyBytes   = 4 * 1024  // title + desc
	maxStaffBodyBytes    = 64 * 1024 // an ordered uuid array; ~1,700 ids against a real ceiling in the dozens
	maxDecisionBodyBytes = 4 * 1024  // decision + reason
)

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// newRoleKey ports the SPA's newRoleKey (roles.ts). `taken` MUST include
// soft-deleted keys, or a re-minted key inherits a sealed step.
func newRoleKey(taken map[string]bool, title string) string {
	base := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if base == "" {
		base = "role"
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		key := fmt.Sprintf("%s-%d", base, n)
		if !taken[key] {
			return key
		}
	}
}

// statusForErr maps a store error to its HTTP status + message. The messages are
// hand-written rather than err.Error() so the "approval: " sentinel prefix never
// reaches the SPA, which renders them as the reason.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusForbidden, db.WorkspaceLockedMessage
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, "invalid request"
	case errors.Is(err, ErrNotPermitted):
		return http.StatusForbidden, "only an admin can change workflow roles"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "workflow role not found"
	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "that role was just created — try again"
	case errors.Is(err, ErrRoleInUse):
		var inUse *roleInUse
		if errors.As(err, &inUse) {
			return http.StatusConflict, inUse.message()
		}
		return http.StatusConflict, roleInUseMessage
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
