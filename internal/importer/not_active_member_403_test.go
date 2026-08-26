// AUDIT-10-03 site 13 of 14: importer's statusForErr has no db.ErrNoTenant arm
// at all, so an unmapped error takes its 500 default. Without this arm
// POST /v1/imports, POST /v1/imports/preview and GET /v1/imports/{id} would 500
// a suspended member instead of refusing them with a reason.
package importer

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestImporterStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("importer: create batch: %w", db.ErrNotActiveMember)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForErr(tc.err)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (a 500 here is the pre-existing default, not a mapping)", status)
			}
			if msg != db.NotActiveMemberMessage {
				t.Errorf("msg = %q, want db.NotActiveMemberMessage %q", msg, db.NotActiveMemberMessage)
			}
		})
	}
}
