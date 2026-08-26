// AUDIT-10-03 site 12 of 14, plus the assertion the two AUDIT-10-02 gate tests
// inherited: those could only pin non-200 while the mapping did not exist.
package invoice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// notActiveMemberEnvelope decodes the error envelope locally. submitGateBody is
// the submit gate's wire mirror and carries no Error field on purpose; widening
// it would make it mirror something it does not own.
type notActiveMemberEnvelope struct {
	Error string `json:"error"`
}

// assertNotActiveMember403 is the shared oracle for the refusal on the wire.
// 403 rather than 401 is load-bearing: authedFetch.ts branches on 401 alone and
// would sign the session out instead of showing the reason.
func assertNotActiveMember403(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	var got notActiveMemberEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if got.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want db.NotActiveMemberMessage %q", got.Error, db.NotActiveMemberMessage)
	}
}

func TestInvoiceStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("invoice: get: %w", db.ErrNotActiveMember)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForErr(tc.err)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", status)
			}
			if msg != db.NotActiveMemberMessage {
				t.Errorf("msg = %q, want db.NotActiveMemberMessage %q", msg, db.NotActiveMemberMessage)
			}
		})
	}
}
