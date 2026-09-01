// handlers_detail_internal_test.go: statusForErr's narrowness control. EXTR-11-02 adds ONE
// branch to it, and statusForErr is shared with POST /v1/documents (handlers_upload.go:101,119)
// -- so a branch that widened would move a second route nobody was looking at.
package extraction

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Green today by design: it is what keeps the new ErrNotFound arm from swallowing anything the
// three existing arms already answered. A 404 for an error that is not ErrNotFound would hide a
// broken read behind an empty screen.
func TestStatusForErr_UnknownErrorIsStill500(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"unknown", errors.New("dial tcp 10.0.0.7:5432: connection refused"), http.StatusInternalServerError, "internal server error"},
		{"wrapped unknown", fmt.Errorf("extraction: read job: %w", errors.New("boom")), http.StatusInternalServerError, "internal server error"},
		{"pgx no rows lookalike", errors.New("no rows in result set"), http.StatusInternalServerError, "internal server error"},
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
