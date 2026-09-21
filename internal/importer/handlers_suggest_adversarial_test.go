// handlers_suggest_adversarial_test.go: Test Spec row 13, the suggest-mapping error ladder
// (§2, 15 rows), fake-driven -- no DB. Cross-tenant 404 needs real RLS and lives in
// handlers_suggest_db_test.go instead.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestSuggestHandler_ErrorLadder(t *testing.T) {
	cases := []struct {
		name        string
		noIdentity  bool
		body        func(open *fakeDocOpen) string
		openErr     error
		nilBody     bool
		content     []byte
		filename    string
		contentType string
		lookupErr   error
		wantStatus  int
		wantMsg     string
	}{
		{
			name:       "1 no identity, even against a malformed body",
			noIdentity: true,
			// Deliberately malformed: proves identity is checked BEFORE the body decode.
			body:       func(open *fakeDocOpen) string { return "not json" },
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "unauthorized",
		},
		{
			name: "2 body over 4KiB",
			body: func(open *fakeDocOpen) string {
				return `{"entity_id":"` + uuid.NewString() + `","document_id":"` + open.doc.ID + `","pad":"` + strings.Repeat("x", 5*1024) + `"}`
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantMsg:    "request body exceeds the size limit",
		},
		{
			name:       "3 malformed JSON",
			body:       func(open *fakeDocOpen) string { return "{not json" },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid request body",
		},
		{
			name:       "4 entity_id empty",
			body:       func(open *fakeDocOpen) string { return `{"document_id":"` + open.doc.ID + `"}` },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "entity_id is required",
		},
		{
			name:       "5 entity_id malformed",
			body:       func(open *fakeDocOpen) string { return `{"entity_id":"nope","document_id":"` + open.doc.ID + `"}` },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "entity_id must be a well-formed uuid",
		},
		{
			name:       "6 document_id empty",
			body:       func(open *fakeDocOpen) string { return `{"entity_id":"` + uuid.NewString() + `"}` },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "document_id is required",
		},
		{
			name:       "7 document_id malformed",
			body:       func(open *fakeDocOpen) string { return `{"entity_id":"` + uuid.NewString() + `","document_id":"nope"}` },
			wantStatus: http.StatusBadRequest,
			wantMsg:    "document_id must be a well-formed uuid",
		},
		{
			name:       "8 open document.ErrNotFound",
			openErr:    document.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "not found",
		},
		{
			name:       "9 open document.ErrValidation",
			openErr:    document.ErrValidation,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "document_id must be a well-formed uuid",
		},
		{
			name:       "10 open db.ErrNotActiveMember",
			openErr:    fmt.Errorf("wrap: %w", db.ErrNotActiveMember),
			wantStatus: http.StatusForbidden,
			wantMsg:    db.NotActiveMemberMessage,
		},
		{
			name:       "11 open other error",
			openErr:    errors.New("object storage unreachable: get"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal server error",
		},
		{
			name:       "12 nil object body",
			nilBody:    true,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal server error",
		},
		{
			name:        "13 unrecognized format",
			filename:    "scan.pdf",
			contentType: "application/pdf",
			content:     []byte("%PDF-1.7\n"),
			wantStatus:  http.StatusBadRequest,
			wantMsg:     "unrecognized file format",
		},
		{
			name:       "14 row-1 decode error",
			content:    bytes.Repeat([]byte{0x00, 0x01, 0x02}, 64),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "could not decode uploaded file",
		},
		{
			name:       "15 lookup error",
			lookupErr:  fmt.Errorf("wrap: %w", db.ErrNotActiveMember),
			wantStatus: http.StatusForbidden,
			wantMsg:    db.NotActiveMemberMessage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filename, content, contentType := tc.filename, tc.content, tc.contentType
			if filename == "" {
				filename = "data.csv"
			}
			if contentType == "" {
				contentType = "text/csv"
			}
			if content == nil && !tc.nilBody {
				content = csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}})
			}

			var open *fakeDocOpen
			var openFn openSpec
			if tc.nilBody {
				docID := uuid.NewString()
				fn, cd := "data.csv", "text/csv"
				openFn = func(ctx context.Context, _, rangeHeader string) (document.Document, document.Object, error) {
					return document.Document{ID: docID, Filename: &fn, DeclaredContentType: &cd}, document.Object{}, nil
				}
				open = &fakeDocOpen{doc: document.Document{ID: docID}}
			} else {
				open = newFakeDocOpen(filename, contentType, content)
				open.err = tc.openErr
				openFn = open.fn()
			}

			lookup := &lookupSpy{err: tc.lookupErr}

			var id *auth.Identity
			if !tc.noIdentity {
				i := testIdentity()
				id = &i
			}

			body := tc.body
			if body == nil {
				body = func(o *fakeDocOpen) string { return suggestReqBody(uuid.NewString(), o.doc.ID) }
			}

			rec, raw := doSuggestRequest(t, openFn, lookup.fn(), &fakeSuggester{enabled: true}, nil, id, body(open))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, raw)
			}
			var errBody struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &errBody); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if errBody.Error != tc.wantMsg {
				t.Errorf("error = %q, want %q", errBody.Error, tc.wantMsg)
			}
		})
	}
}
