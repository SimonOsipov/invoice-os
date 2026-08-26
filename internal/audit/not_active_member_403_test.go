// AUDIT-10-03 site 6 of 14. audit's statusForErr is unexported and this package
// has no in-package test file, so the arm is driven through ListHandler over a
// store that returns the sentinel — the same shape as
// TestAuditListHandler_StoreErrorsMapThroughStatusForErr.
package audit_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestAuditListHandler_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("audit: list: %w", db.ErrNotActiveMember)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &hndSpy{err: tc.err}
			w := hndGet(t, spy, "")

			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
			}
			body := hndBody(t, w)
			if got := body["error"]; got != db.NotActiveMemberMessage {
				t.Errorf("error = %v, want db.NotActiveMemberMessage %q", got, db.NotActiveMemberMessage)
			}
		})
	}
}
