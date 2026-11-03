package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func failedWith(kind *string, doc submission.DocumentType) Invoice {
	return Invoice{ID: "x", Status: StatusFailed, FailureKind: kind, DocumentType: doc}
}

func kindPtr(k submission.FailureKind) *string { s := string(k); return &s }

func assertSendAgain(t *testing.T, can bool, reason *string, wantCan bool, wantReason string) {
	t.Helper()
	assertApprovalGate(t, can, reason, wantCan, wantReason)
}

func TestSendAgainGate_OffersTheActionOnARetryableFailedInvoice(t *testing.T) {
	for _, k := range []submission.FailureKind{submission.FailurePayloadNotBuilt, submission.FailureNeverAcknowledged} {
		for _, role := range []string{"admin", "reviewer"} {
			can, reason := sendAgainGate(failedWith(kindPtr(k), submission.CommercialInvoice), role, true)
			assertSendAgain(t, can, reason, true, "")
		}
	}
}

func TestSendAgainGate_NotOfferedOffFailedOrOnAnUnretryableKind(t *testing.T) {
	for _, st := range allStatuses {
		if st == StatusFailed {
			continue
		}
		can, reason := sendAgainGate(Invoice{Status: st, FailureKind: kindPtr(submission.FailureNeverAcknowledged)}, "admin", true)
		assertSendAgain(t, can, reason, false, "")
	}
	can, reason := sendAgainGate(failedWith(kindPtr(submission.FailureAcknowledgedNoVerdict), submission.CommercialInvoice), "admin", true)
	assertSendAgain(t, can, reason, false, "")
	can, reason = sendAgainGate(failedWith(nil, submission.CommercialInvoice), "admin", true)
	assertSendAgain(t, can, reason, false, "")
}

func TestSendAgainGate_PreparerReason(t *testing.T) {
	for _, role := range []string{"preparer", ""} {
		can, reason := sendAgainGate(failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CommercialInvoice), role, true)
		assertSendAgain(t, can, reason, false, wantSendAgainPreparer)
	}
}

func TestSendAgainGate_ResolvedOutsideAndNoteReasons(t *testing.T) {
	now := time.Now()
	resolved := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CommercialInvoice)
	resolved.KeptAsIsAt = &now
	can, reason := sendAgainGate(resolved, "admin", true)
	assertSendAgain(t, can, reason, false, wantSendAgainResolved)

	note := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CreditNote)
	can, reason = sendAgainGate(note, "admin", false)
	assertSendAgain(t, can, reason, false, wantSendAgainNoteChecks)

	inv := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CommercialInvoice)
	can, reason = sendAgainGate(inv, "admin", false)
	assertSendAgain(t, can, reason, true, "")
}

func TestSendAgainGate_RungOrder(t *testing.T) {
	now := time.Now()
	n := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CreditNote)
	n.KeptAsIsAt = &now
	can, reason := sendAgainGate(n, "preparer", false)
	assertSendAgain(t, can, reason, false, wantSendAgainPreparer)
	can, reason = sendAgainGate(n, "admin", false)
	assertSendAgain(t, can, reason, false, wantSendAgainResolved)
}

// The GET gate and the POST answer the same bytes for rungs 4 and 5.
func TestSendAgainGate_ReasonsAreThePostSentences(t *testing.T) {
	now := time.Now()
	resolved := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CommercialInvoice)
	resolved.KeptAsIsAt = &now
	note := failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CreditNote)
	cases := []struct {
		name string
		inv  Invoice
		err  error
	}{{"resolved", resolved, ErrResolvedOutside}, {"note", note, ErrNoteChecksFailed}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, reason := sendAgainGate(c.inv, "admin", false)
			if reason == nil {
				t.Fatal("gate reason = nil")
			}
			rec, resp := doSendAgain(t, &sendAgainFake{err: c.err}, fixedRoleStub("admin", nil), sendAgainIdentity(), uuid.NewString(), nil)
			if rec.Code != http.StatusConflict || resp.Error != *reason {
				t.Errorf("POST = %d %q, want 409 %q", rec.Code, resp.Error, *reason)
			}
		})
	}
}

func doGateGet(t *testing.T, inv Invoice, facts ApprovalFacts, log *slog.Logger) map[string]json.RawMessage {
	t.Helper()
	id := &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(context.Context, string) (Invoice, error) { return inv, nil }
	rec, _ := doInvoiceGetGatedLog(t, get, fixedRoleStub("admin", nil), factsStub(facts), id, uuid.NewString(), log)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGetHandler_SendAgainKeysAlwaysPresent(t *testing.T) {
	raw := doGateGet(t, Invoice{ID: "d", Status: StatusDraft}, ApprovalFacts{NoteChecksPass: true}, nil)
	if string(raw["can_send_again"]) != "false" || string(raw["send_again_blocked_reason"]) != "null" {
		t.Errorf("draft: can_send_again=%s reason=%s, want false and null", raw["can_send_again"], raw["send_again_blocked_reason"])
	}
	raw = doGateGet(t, failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CommercialInvoice), ApprovalFacts{NoteChecksPass: true}, nil)
	if string(raw["can_send_again"]) != "true" || string(raw["send_again_blocked_reason"]) != "null" {
		t.Errorf("failed: can_send_again=%s reason=%s, want true and null", raw["can_send_again"], raw["send_again_blocked_reason"])
	}
}

func TestGetHandler_SendAgainFailsClosedOnANoteReadError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	// NoteChecksPass true: the error alone must close the gate.
	facts := ApprovalFacts{TransmitClear: true, NoteChecksPass: true, NoteChecksErr: errors.New("boom")}
	raw := doGateGet(t, failedWith(kindPtr(submission.FailureNeverAcknowledged), submission.CreditNote), facts, log)
	if string(raw["can_send_again"]) != "false" || string(raw["send_again_blocked_reason"]) != `"`+wantSendAgainNoteChecks+`"` {
		t.Errorf("can_send_again=%s reason=%s, want false and the note sentence", raw["can_send_again"], raw["send_again_blocked_reason"])
	}
	if !bytes.Contains(buf.Bytes(), []byte("boom")) {
		t.Errorf("note read error not logged: %s", buf.String())
	}
}
