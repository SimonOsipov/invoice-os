// AUDIT-10-03 site 14 of 14. document's statusForErr has no db.ErrNoTenant arm
// at all, so an unmapped error takes its 500 default — and DownloadHandler logs
// that default as a fault. Without this arm GET /v1/documents/{id} and
// GET /v1/documents/{id}/sheet would 500 a suspended member and log it as a bug.
//
// statusForErr is unexported and this package has no in-package test file, so
// the arm is driven through DownloadHandler.
package document_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestDocumentDownloadHandler_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("document: store: %w", db.ErrNotActiveMember)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			open := func(context.Context, string, string) (document.Document, document.Object, error) {
				return document.Document{}, document.Object{}, tc.err
			}
			rec := doDownload(t, open, &id, uuid.NewString(), "")

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response %q: %v", rec.Body.String(), err)
			}
			if body.Error != db.NotActiveMemberMessage {
				t.Errorf("error = %q, want db.NotActiveMemberMessage %q", body.Error, db.NotActiveMemberMessage)
			}
		})
	}
}
