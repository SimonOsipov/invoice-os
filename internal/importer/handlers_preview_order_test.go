// handlers_preview_order_test.go: POST /v1/imports/preview stores the bytes BEFORE it judges
// the format ([store-before-decode]), so the id its 400 names still downloads the file.
//
// Until now the only pin on that ordering was e2e/api/contract-import.spec.ts:312-321, which a
// push to main does not run. Its sibling POST /v1/documents inverts the ordering deliberately
// (EXTR-09-02), which is exactly the circumstance under which someone "fixes" this one to
// match. These specs are the Go-side owner of "the preview is unchanged".
package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// pvoStore counts calls and hands back a fixed id, so a body naming that id can only have come
// from a store that ran.
type pvoStore struct {
	mu    sync.Mutex
	calls int
	id    string
}

func newPvoStore() *pvoStore { return &pvoStore{id: uuid.NewString()} }

func (s *pvoStore) store(_ context.Context, _, _ string, _ int64, body io.ReadSeeker) (document.Document, bool, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	// document.Service.Store's reader handling, byte for byte: hash pass, one rewind, PUT pass.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return document.Document{}, false, err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return document.Document{}, false, err
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		return document.Document{}, false, err
	}
	return document.Document{ID: s.id}, false, nil
}

func (s *pvoStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func pvoServe(t *testing.T, s *pvoStore, filename, fileContentType string, content []byte) (int, []byte, map[string]json.RawMessage) {
	t.Helper()
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := buildMultipartBody(t, "", "", filename, fileContentType, content)

	r := httptest.NewRequest("POST", "/v1/imports/preview", body)
	r.Header.Set("Content-Type", contentType)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	PreviewHandler(s.store, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	decoded := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec.Code, raw, decoded
}

// TestPreviewHandler_RefusalStillNamesTheStoredDocument: both post-store 400s name the
// document, and the store ran on the way to each. A handler that classified first would 400
// with no document_id and a store count of zero.
func TestPreviewHandler_RefusalStillNamesTheStoredDocument(t *testing.T) {
	// Control: the accepted path proves the store seam is reached at all.
	accepted := newPvoStore()
	code, raw, _ := pvoServe(t, accepted, "data.csv", "", []byte("a,b\n1,2\n"))
	if code != http.StatusOK {
		t.Fatalf("control: a clean csv returned %d, want 200 (body=%s); the refusal assertions below would not be about a store that works", code, raw)
	}
	if accepted.count() != 1 {
		t.Fatalf("control: the store seam ran %d time(s) on a clean csv, want 1", accepted.count())
	}

	for _, c := range []struct {
		name            string
		filename        string
		fileContentType string
		content         []byte
		wantError       string
	}{
		{"an unrecognized format", "data.txt", "application/octet-stream", []byte("whatever"), "unrecognized file format"},
		{"an undecodable xlsx", "data.xlsx", xlsxContentType, []byte("not a zip file"), "could not decode uploaded file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newPvoStore()
			code, raw, decoded := pvoServe(t, s, c.filename, c.fileContentType, c.content)

			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", code, raw)
			}
			if s.count() != 1 {
				t.Errorf("the store seam ran %d time(s) before the refusal, want 1 -- the preview stores BEFORE it judges the format, and the id its 400 names has to come from somewhere", s.count())
			}
			var gotErr string
			if err := json.Unmarshal(decoded["error"], &gotErr); err != nil {
				t.Fatalf("decode error from %s: %v", raw, err)
			}
			if gotErr != c.wantError {
				t.Errorf("error = %q, want %q", gotErr, c.wantError)
			}

			field, ok := decoded["document_id"]
			if !ok {
				t.Fatalf("the 400 carries no document_id (body=%s); the bytes were stored and are now unretrievable", raw)
			}
			var gotID string
			if err := json.Unmarshal(field, &gotID); err != nil {
				t.Fatalf("decode document_id from %s: %v", field, err)
			}
			if gotID != s.id {
				t.Errorf("document_id = %q, want the stored row's %q", gotID, s.id)
			}
		})
	}
}

// TestPreviewHandler_PreStoreRefusalsNameNoDocument: the counter-case. A refusal that happens
// BEFORE the store must not name a document -- there is none, and an invented id is worse than
// an absent one. Without this, "the 400 names a document" would read as a rule about every 400.
func TestPreviewHandler_PreStoreRefusalsNameNoDocument(t *testing.T) {
	s := newPvoStore()
	code, raw, decoded := pvoServe(t, s, "", "", nil)

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a form with no file part (body=%s)", code, raw)
	}
	if s.count() != 0 {
		t.Errorf("the store seam ran %d time(s) for a request with no file part, want 0", s.count())
	}
	if _, ok := decoded["document_id"]; ok {
		t.Errorf("a pre-store 400 names a document_id (body=%s); nothing was stored", raw)
	}
}
