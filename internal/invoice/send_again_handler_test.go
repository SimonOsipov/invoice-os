package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The five 409 sentences of the send-again API contract, restated so a reworded constant fails here.
const (
	wantSendAgainNotFailed     = "This invoice is no longer failed. Refresh to see its status."
	wantSendAgainResolved      = "This invoice is marked resolved outside ASComply and cannot be sent again."
	wantSendAgainNoteChecks    = "This note no longer passes its checks and cannot be sent again."
	wantSendAgainNotResendable = "This invoice cannot be sent again from this screen. Ask ASComply support."
	wantSendAgainBusy          = "This invoice is already being sent. Refresh to see its status."
	wantSendAgainPreparer      = "An approver can send this invoice again."
)

type sendAgainFake struct {
	calls []string
	inv   Invoice
	err   error
}

func (f *sendAgainFake) send(_ context.Context, id string) (Invoice, error) {
	f.calls = append(f.calls, id)
	return f.inv, f.err
}

func doSendAgain(t *testing.T, f *sendAgainFake, role func(context.Context) (string, error), id *auth.Identity, invoiceID string, log *slog.Logger) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+invoiceID+"/send-again", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SendAgainHandler(f.send, role, log).ServeHTTP(rec, r)
	var resp invoiceBody
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func sendAgainIdentity() *auth.Identity {
	return &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
}

func TestSendAgainHandler_SuccessIs200WithTheInvoice(t *testing.T) {
	for _, role := range []string{"admin", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			id := uuid.NewString()
			f := &sendAgainFake{inv: Invoice{ID: id, Status: StatusQueued}}
			rec, resp := doSendAgain(t, f, fixedRoleStub(role, nil), sendAgainIdentity(), id, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Status != "queued" || resp.ID != id {
				t.Errorf("body status=%q id=%q, want queued and %q", resp.Status, resp.ID, id)
			}
			if len(f.calls) != 1 || f.calls[0] != id {
				t.Errorf("sendAgain calls = %v, want exactly [%s]", f.calls, id)
			}
		})
	}
}

func TestSendAgainHandler_PreparerIs403BeforeTheStore(t *testing.T) {
	real, random := uuid.NewString(), uuid.NewString()
	f := &sendAgainFake{inv: Invoice{ID: real, Status: StatusQueued}}
	var bodies []string
	for _, role := range []string{"preparer", ""} {
		for _, invoiceID := range []string{real, random} {
			rec, resp := doSendAgain(t, f, fixedRoleStub(role, nil), sendAgainIdentity(), invoiceID, nil)
			if rec.Code != http.StatusForbidden || resp.Error != wantNotApproverTransmitReason {
				t.Errorf("role %q id %s: got %d %q, want 403 %q", role, invoiceID, rec.Code, resp.Error, wantNotApproverTransmitReason)
			}
			bodies = append(bodies, rec.Body.String())
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("sendAgain called %v for a non-approver, want never", f.calls)
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("403 body differs between a real and a random id: %q vs %q", b, bodies[0])
		}
	}
	// Positive pair: the same fake is reached by an approver.
	if rec, _ := doSendAgain(t, f, fixedRoleStub("admin", nil), sendAgainIdentity(), real, nil); rec.Code != http.StatusOK || len(f.calls) != 1 {
		t.Errorf("admin: status %d, calls %v, want 200 and one call", rec.Code, f.calls)
	}
}

func TestSendAgainHandler_RoleReadErrorFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unknown error", errors.New("db down"), wantNotApproverTransmitReason},
		{"suspended member", db.ErrNotActiveMember, wantNotApproverTransmitReason},
		{"no tenant", db.ErrNoTenant, wantNotApproverTransmitReason},
		{"locked workspace", db.ErrWorkspaceLocked, db.WorkspaceLockedMessage},
		{"wrapped locked workspace", fmt.Errorf("role: %w", db.ErrWorkspaceLocked), db.WorkspaceLockedMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &sendAgainFake{}
			// "admin" with an error proves the error, not the role, decides.
			rec, resp := doSendAgain(t, f, fixedRoleStub("admin", c.err), sendAgainIdentity(), uuid.NewString(), nil)
			if rec.Code != http.StatusForbidden || resp.Error != c.want {
				t.Errorf("got %d %q, want 403 %q", rec.Code, resp.Error, c.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("sendAgain called after a refused role read")
			}
		})
	}
}

func TestSendAgainHandler_RefusalsAre409WithTheirSentence(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not failed", StatusError{Have: StatusQueued}, wantSendAgainNotFailed},
		{"not failed, wrapped", fmt.Errorf("x: %w", StatusError{Have: StatusAccepted}), wantSendAgainNotFailed},
		{"resolved outside", ErrResolvedOutside, wantSendAgainResolved},
		{"note checks", ErrNoteChecksFailed, wantSendAgainNoteChecks},
		{"not resendable", ErrNotResendable, wantSendAgainNotResendable},
		{"busy", ErrResendBusy, wantSendAgainBusy},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &sendAgainFake{err: c.err}
			rec, resp := doSendAgain(t, f, fixedRoleStub("admin", nil), sendAgainIdentity(), uuid.NewString(), nil)
			if rec.Code != http.StatusConflict || resp.Error != c.want {
				t.Errorf("got %d %q, want 409 %q", rec.Code, resp.Error, c.want)
			}
			seen[resp.Error] = true
		})
	}
	if len(seen) != 5 {
		t.Errorf("distinct refusal sentences = %d, want 5 (one per reason)", len(seen))
	}
}

func TestSendAgainHandler_NotFoundValidationAnd500(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"not found", ErrNotFound, http.StatusNotFound, "not found"},
		{"validation", fmt.Errorf("%w: id must be a uuid", ErrValidation), http.StatusBadRequest, ""},
		{"locked workspace", db.ErrWorkspaceLocked, http.StatusForbidden, db.WorkspaceLockedMessage},
		{"unknown", errors.New("boom: secret detail"), http.StatusInternalServerError, "internal server error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf.Reset()
			f := &sendAgainFake{err: c.err}
			rec, resp := doSendAgain(t, f, fixedRoleStub("admin", nil), sendAgainIdentity(), uuid.NewString(), log)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, c.code, rec.Body.String())
			}
			if c.msg != "" && resp.Error != c.msg {
				t.Errorf("error = %q, want %q", resp.Error, c.msg)
			}
			if c.code == http.StatusBadRequest && !strings.Contains(resp.Error, "id must be a uuid") {
				t.Errorf("400 error = %q, want the validation text", resp.Error)
			}
			lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1
			if c.code == http.StatusInternalServerError {
				if lines != 1 || !strings.Contains(buf.String(), "send again") {
					t.Errorf("log = %q, want one line naming the send again", buf.String())
				}
				if strings.Contains(rec.Body.String(), "secret detail") {
					t.Errorf("500 body leaks the error: %s", rec.Body.String())
				}
			} else if strings.TrimSpace(buf.String()) != "" {
				t.Errorf("logged on a non-500: %q", buf.String())
			}
		})
	}
}

func TestSendAgainHandler_NoIdentityIs401(t *testing.T) {
	f := &sendAgainFake{}
	roleCalled := false
	role := func(context.Context) (string, error) { roleCalled = true; return "admin", nil }
	rec, resp := doSendAgain(t, f, role, nil, uuid.NewString(), nil)
	if rec.Code != http.StatusUnauthorized || resp.Error != "unauthorized" {
		t.Errorf("got %d %q, want 401 unauthorized", rec.Code, resp.Error)
	}
	if roleCalled || len(f.calls) != 0 {
		t.Errorf("role read=%v, sendAgain calls=%v; want neither", roleCalled, f.calls)
	}
}
