// upload_storer_test.go: the two adapters POST /v1/documents is wired through --
// newDocumentStorer and newExtractionEnqueuer. main_test.go's AST scan proves main() BUILDS
// them; nothing until now drove either one, so the reuse verdict crossed three hops
// (document.Service.Store -> StoredDocument -> the 201 body) with no spec on the middle one.
//
// Driven through the real extraction.UploadHandler rather than by calling the adapter alone:
// the claim is about what reaches the WIRE, and a json tag is part of that path.
//
// Helpers use a ds* prefix; wt ea ew are taken.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var dsIdentity = auth.Identity{
	Subject:  "e5b10009-0000-4000-8000-000000000002",
	Role:     "authenticated",
	TenantID: "33333333-3333-3333-3333-333333333333",
}

// dsScript stands in for document.Service.Store: it answers with the row and the reuse
// verdict the test dictates, and records what the handler handed it.
type dsScript struct {
	doc    document.Document
	reused bool
	err    error

	calls          int
	enqueues       int
	gotFilename    string
	gotContentType string
	gotSize        int64
	gotBody        []byte
	gotEnqueueID   string
}

func (s *dsScript) store(_ context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, bool, error) {
	s.calls++
	s.gotFilename, s.gotContentType, s.gotSize = filename, contentType, size
	if body != nil {
		s.gotBody, _ = io.ReadAll(body)
	}
	return s.doc, s.reused, s.err
}

// dsPost drives extraction.UploadHandler over the real newDocumentStorer adapter once and
// returns the status plus the decoded body.
func dsPost(t *testing.T, s *dsScript, filename string, content []byte) (int, []byte, map[string]json.RawMessage) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	enqueue := func(_ context.Context, documentID string) (bool, error) {
		s.enqueues++
		s.gotEnqueueID = documentID
		return false, nil
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/documents", &buf)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r = r.WithContext(auth.WithIdentity(r.Context(), dsIdentity))
	rec := httptest.NewRecorder()
	extraction.UploadHandler(newDocumentStorer(s.store), enqueue, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	decoded := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec.Code, raw, decoded
}

func dsRow(id, filename, contentType string, size int64) document.Document {
	return document.Document{ID: id, Filename: &filename, DeclaredContentType: &contentType, SizeBytes: size}
}

// TestNewDocumentStorer_CarriesTheReuseVerdictOntoTheWire exercises BOTH arms. The adapter
// forwards a bool it neither computes nor checks, so an arm dropped, hardcoded or inverted
// anywhere between document.Service.Store and the json tag lands here.
func TestNewDocumentStorer_CarriesTheReuseVerdictOntoTheWire(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(map[bool]string{false: "reused=false", true: "reused=true"}[want], func(t *testing.T) {
			s := &dsScript{doc: dsRow("doc-42", "scan.pdf", "application/pdf", 13), reused: want}
			code, raw, decoded := dsPost(t, s, "scan.pdf", []byte("%PDF-1.7 fake"))

			if code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", code, raw)
			}
			if s.calls != 1 {
				t.Fatalf("the document store ran %d time(s), want 1; the body below is not this call's", s.calls)
			}
			field, ok := decoded["reused"]
			if !ok {
				t.Fatalf("the 201 carries no reused key (body=%s); an absent verdict reads as unknown", raw)
			}
			var got bool
			if err := json.Unmarshal(field, &got); err != nil {
				t.Fatalf("decode reused from %s: %v", field, err)
			}
			if got != want {
				t.Errorf("reused = %t, want document.Service.Store's verdict %t -- the caller is told the wrong thing about whether these bytes were already held", got, want)
			}
		})
	}
}

// TestNewDocumentStorer_ProjectsTheStoredRowNotTheDeclaredValues: filename and size on the
// 201 must be the row's, because Service.Store sanitizes the one and recomputes the other.
// Echoing back what the caller sent would hide both coercions from every client.
func TestNewDocumentStorer_ProjectsTheStoredRowNotTheDeclaredValues(t *testing.T) {
	s := &dsScript{doc: dsRow("doc-7", "sanitized.pdf", "application/pdf", 4)}
	code, raw, decoded := dsPost(t, s, "../../etc/passwd.pdf", []byte("%PDF-1.7 much longer than four bytes"))

	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", code, raw)
	}
	// mime/multipart has already reduced the part filename to its base (Part.FileName,
	// RFC 7578 s4.2), so the handler never sees the traversal at all -- Service.Store's
	// SanitizeFilename is the SECOND layer, not the first. Pinned because a hand-rolled
	// multipart reader would remove that first layer with nothing else red.
	if s.gotFilename != "passwd.pdf" {
		t.Errorf("the document store was handed filename %q, want %q; the handler forwards the part filename verbatim and mime/multipart bases it", s.gotFilename, "passwd.pdf")
	}
	if strings.ContainsAny(s.gotFilename, `/\`) {
		t.Errorf("the document store was handed %q, which carries a path separator; the storage key is built from the tenant and the hash, but every downstream consumer of filename reads it as a leaf name", s.gotFilename)
	}
	if s.gotContentType != "application/pdf" {
		t.Errorf("the document store was handed content type %q, want the classifier's canonical application/pdf; the row must not record whatever header the client chose", s.gotContentType)
	}
	if string(s.gotBody) != "%PDF-1.7 much longer than four bytes" {
		t.Errorf("the document store read %q off the part, want the bytes the caller sent -- the hash and the object PUT are both computed from this reader", s.gotBody)
	}
	if s.gotEnqueueID != "doc-7" {
		t.Errorf("enqueue was handed %q, want the STORED id doc-7; the enqueue seam does no ownership check", s.gotEnqueueID)
	}

	for _, c := range []struct{ key, want string }{
		{"document_id", "doc-7"},
		{"filename", "sanitized.pdf"},
		{"content_type", "application/pdf"},
	} {
		field, ok := decoded[c.key]
		if !ok {
			t.Fatalf("the 201 carries no %q key (body=%s)", c.key, raw)
		}
		var got string
		if err := json.Unmarshal(field, &got); err != nil {
			t.Fatalf("decode %q from %s: %v", c.key, field, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want the stored row's %q", c.key, got, c.want)
		}
	}

	var size int64
	if err := json.Unmarshal(decoded["size_bytes"], &size); err != nil {
		t.Fatalf("decode size_bytes from %s: %v", decoded["size_bytes"], err)
	}
	if size != 4 {
		t.Errorf("size_bytes = %d, want the stored row's 4; Service.Store recomputes the byte count and the part was %d bytes long", size, s.gotSize)
	}
}

// TestNewDocumentStorer_NullFilenameAndContentTypeAreEmptyNotAPanic: documents.filename and
// declared_content_type go through nullif, so a row can carry SQL NULL and the Document
// pointers come back nil. Dereferencing either unguarded is a 500 on a request that succeeded.
func TestNewDocumentStorer_NullFilenameAndContentTypeAreEmptyNotAPanic(t *testing.T) {
	s := &dsScript{doc: document.Document{ID: "doc-null", SizeBytes: 3}}
	code, raw, decoded := dsPost(t, s, "scan.pdf", []byte("abc"))

	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a row whose filename and declared_content_type are NULL (body=%s)", code, raw)
	}
	for _, key := range []string{"filename", "content_type"} {
		field, ok := decoded[key]
		if !ok {
			t.Fatalf("the 201 carries no %q key (body=%s); AC-7 fixes the field set", key, raw)
		}
		var got string
		if err := json.Unmarshal(field, &got); err != nil {
			t.Fatalf("decode %q from %s: %v -- a NULL column must project as \"\", never as JSON null", key, field, err)
		}
		if got != "" {
			t.Errorf("%s = %q for a NULL column, want the empty string", key, got)
		}
	}
}

// TestNewDocumentStorer_StoreErrorReachesTheHandlerUnwrapped: statusForErr maps sentinels by
// errors.Is, so an adapter that swallowed or replaced the error would turn a suspended
// caller's 403 into a 500 -- and the handler's own spec, driven by its own spy, cannot see it.
func TestNewDocumentStorer_StoreErrorReachesTheHandlerUnwrapped(t *testing.T) {
	control := &dsScript{doc: dsRow("doc-1", "scan.pdf", "application/pdf", 3)}
	if code, raw, _ := dsPost(t, control, "scan.pdf", []byte("abc")); code != http.StatusCreated {
		t.Fatalf("control: a successful store returned %d, want 201 (body=%s); the status assertions below would not be about the error path", code, raw)
	}

	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"suspended member is 403", db.ErrNotActiveMember, http.StatusForbidden},
		{"no tenant is 401", db.ErrNoTenant, http.StatusUnauthorized},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &dsScript{err: c.err}
			code, raw, _ := dsPost(t, s, "scan.pdf", []byte("abc"))
			if code != c.want {
				t.Errorf("status = %d, want %d for %v (body=%s)", code, c.want, c.err, raw)
			}
			if s.enqueues != 0 {
				t.Errorf("enqueue ran %d time(s) after a failed store, want 0 -- there is no document to extract", s.enqueues)
			}
		})
	}
}
