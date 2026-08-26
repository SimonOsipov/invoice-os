// AUDIT-10-03 sites 9, 10 and 11 of 14: approval carries three mappers, and all
// three must map the seam's suspended-member refusal to 403 rather than take
// their 500 default. The three are separate functions with separate wordings, so
// one arm does not cover the other two.
package approval

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestApprovalStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, m := range []struct {
		mapper string
		fn     func(error) (int, string)
	}{
		{"statusForErr", statusForErr},
		{"policyStatusForErr", policyStatusForErr},
		{"decisionStatusForErr", decisionStatusForErr},
	} {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{"bare", db.ErrNotActiveMember},
			{"wrapped", fmt.Errorf("approval: %w", db.ErrNotActiveMember)},
		} {
			t.Run(m.mapper+"/"+tc.name, func(t *testing.T) {
				status, msg := m.fn(tc.err)
				if status != http.StatusForbidden {
					t.Errorf("%s status = %d, want 403", m.mapper, status)
				}
				if msg != db.NotActiveMemberMessage {
					t.Errorf("%s msg = %q, want db.NotActiveMemberMessage %q", m.mapper, msg, db.NotActiveMemberMessage)
				}
			})
		}
	}
}
