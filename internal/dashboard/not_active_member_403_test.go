// AUDIT-10-03 site 5 of 14: dashboard's statusForErr must map the seam's
// suspended-member refusal to 403, not fall through to its 500 default.
package dashboard

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestDashboardStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("dashboard: rollup: %w", db.ErrNotActiveMember)},
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
