// AUDIT-10-03 site 1 of 14: archive's statusForErr must map the seam's
// suspended-member refusal to 403, not fall through to its 500 default.
package archive

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestArchiveStatusForErr_NotActiveMemberIs403 pins the arm. 403 rather than
// 401 is load-bearing: authedFetch.ts branches on 401 alone and would sign the
// session out, which Core AC 2 forbids.
func TestArchiveStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("archive: assemble: %w", db.ErrNotActiveMember)},
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
