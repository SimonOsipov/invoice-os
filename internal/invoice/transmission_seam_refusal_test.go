// A seam error from the role lookup keeps its own refusal; it must not read as
// "not an approver". Covers TransitionHandler and BatchSubmitHandler.
package invoice

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// wantWorkspaceLockedMessage copies db.WorkspaceLockedMessage, so a reword fails here.
const wantWorkspaceLockedMessage = "this workspace is scheduled for deletion"

func TestTransmitGate_SeamErrorFromRoleLookupKeepsItsRefusal(t *testing.T) {
	if db.WorkspaceLockedMessage != wantWorkspaceLockedMessage {
		t.Fatalf("copied message drifted from db.WorkspaceLockedMessage: %q", db.WorkspaceLockedMessage)
	}
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	cases := []struct {
		name string
		role string
		err  error
		want string
	}{
		{"locked workspace", "", db.ErrWorkspaceLocked, db.WorkspaceLockedMessage},
		{"suspended approver keeps the approver text", "admin", db.ErrNotActiveMember, wantNotApproverTransmitReason},
		{"preparer still gets the approver text", "preparer", nil, wantNotApproverTransmitReason},
	}
	for _, c := range cases {
		t.Run("batch submit/"+c.name, func(t *testing.T) {
			submit := func(context.Context, BatchSubmitInput) (BatchSubmitResult, error) {
				t.Error("submit called after a refused role lookup")
				return BatchSubmitResult{}, nil
			}
			rec, resp := doBatchSubmitAs(t, submit, fixedRoleStub(c.role, c.err), &id, oneInvoiceBatchBody(t, uuid.NewString()))
			if rec.Code != http.StatusForbidden || resp.Error != c.want {
				t.Errorf("got %d %q, want 403 %q", rec.Code, resp.Error, c.want)
			}
		})
		t.Run("transition/"+c.name, func(t *testing.T) {
			transition := func(context.Context, string, Status) (Invoice, error) {
				t.Error("transition called after a refused role lookup")
				return Invoice{}, nil
			}
			rec, resp := doInvoiceTransitionAs(t, transition, fixedRoleStub(c.role, c.err), &id, uuid.NewString(), transitionToQueuedBody)
			if rec.Code != http.StatusForbidden || resp.Error != c.want {
				t.Errorf("got %d %q, want 403 %q", rec.Code, resp.Error, c.want)
			}
		})
	}
}
