// A seam error from the role lookup keeps its own refusal; it must not read as
// "not an approver". Covers TransitionHandler and BatchSubmitHandler.
package invoice

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// wantWorkspaceLockedMessage and wantNotActiveMemberMessage copy
// db.WorkspaceLockedMessage and db.NotActiveMemberMessage, so a reword fails here.
const (
	wantWorkspaceLockedMessage = "this workspace is scheduled for deletion"
	wantNotActiveMemberMessage = "your membership in this workspace is not active"
)

func TestTransmitGate_SeamErrorFromRoleLookupKeepsItsRefusal(t *testing.T) {
	if db.WorkspaceLockedMessage != wantWorkspaceLockedMessage || db.NotActiveMemberMessage != wantNotActiveMemberMessage {
		t.Fatalf("copied message drifted from db constants: %q / %q", db.WorkspaceLockedMessage, db.NotActiveMemberMessage)
	}
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	cases := []struct {
		name string
		role string
		err  error
		want string
	}{
		{"locked workspace", "", db.ErrWorkspaceLocked, db.WorkspaceLockedMessage},
		{"suspended caller", "", db.ErrNotActiveMember, db.NotActiveMemberMessage},
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

func TestTransmitGate_NoTenantFromRoleLookupIs401(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rec, _ := doBatchSubmitAs(t, nil, fixedRoleStub("", errors.Join(db.ErrNoTenant)), &id, oneInvoiceBatchBody(t, uuid.NewString()))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("batch submit status = %d, want 401", rec.Code)
	}
}
