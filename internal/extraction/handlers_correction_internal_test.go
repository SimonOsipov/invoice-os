package extraction

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The correction route adds three domain outcomes to the shared mapper. Each arm is a named
// sentinel, so an unrecognised error is still a 500 -- TestStatusForErr_UnknownErrorIsStill500
// is the other half of this claim and must keep passing.
func TestStatusForErr_MapsTheThreeInvoiceSentinels(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"no invoice for the document", ErrNoInvoiceForDocument, http.StatusConflict, "no invoice has been filed from this document"},
		{"wrapped no invoice", fmt.Errorf("submission: apply field: %w", ErrNoInvoiceForDocument), http.StatusConflict, "no invoice has been filed from this document"},
		{"invoice past the fixable states", ErrInvoiceNotEditable, http.StatusConflict, "this invoice can no longer be corrected"},
		{"value refused by the invoice", ErrValueRefused, http.StatusBadRequest, "the invoice refused this value"},

		// The existing arms, restated so a new case cannot be added by widening one of them.
		{"unknown", errors.New("dial tcp 10.0.0.7:5432: connection refused"), http.StatusInternalServerError, "internal server error"},
		{"not found", ErrNotFound, http.StatusNotFound, "not found"},
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"not active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForErr(tc.err)
			if status != tc.status || msg != tc.msg {
				t.Errorf("statusForErr(%v) = (%d, %q), want (%d, %q)", tc.err, status, msg, tc.status, tc.msg)
			}
		})
	}
}

// T10 (internal/extraction half): the rename seam's two refusals, both 409. The cmd/submission
// half is TestInvoiceNumberTakenReason_MatchesTheInvoiceRoutesOwnSentence
// (correction_route_test.go), which pins InvoiceNumberTakenReason against invoice.NumberTakenReason.
func TestStatusForErr_MapsTheTwoRenameRefusals(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"the number is already taken", ErrInvoiceNumberTaken, http.StatusConflict, InvoiceNumberTakenReason},
		{"the number is no longer correctable", ErrInvoiceNumberFixed, http.StatusConflict, InvoiceNumberFixedReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForErr(tc.err)
			if status != tc.status || msg != tc.msg {
				t.Errorf("statusForErr(%v) = (%d, %q), want (%d, %q)", tc.err, status, msg, tc.status, tc.msg)
			}
		})
	}
}
