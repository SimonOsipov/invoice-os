// Package docadapter adapts the document service and object store to the extraction seams.
// internal/extraction never imports internal/document; the submission service and the worker
// both build these adapters.
package docadapter

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// MaxDocumentBytes: documents.size_bytes CHECKs <= this (migrations/20260802163544_documents.sql).
const MaxDocumentBytes = 15 << 20

// Open is document.Service.Open's shape, so a test can substitute one.
type Open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error)

// Opener adapts the document service to the extraction seam: whole object, no range, capped
// read, body closed exactly once on every path. ctx is forwarded untouched -- the worker or the
// correction route has already put the tenant identity on it, and RLS scopes the row lookup by
// that identity (TestOpener_ForwardsContextVerbatim).
func Opener(open Open) extraction.OpenDocument {
	return func(ctx context.Context, documentID string) (extraction.Document, error) {
		doc, obj, err := open(ctx, documentID, "")
		// Registered before the error branch: an open that hands back both a body and an
		// error still closes (TestOpener_ClosesBodyWhenOpenErrors).
		if obj.Body != nil {
			defer func() { _ = obj.Body.Close() }()
		}
		if err != nil {
			return extraction.Document{}, err
		}
		if obj.Body == nil {
			return extraction.Document{}, fmt.Errorf("docadapter: document %s opened with no body", documentID)
		}
		// +1 so an at-the-cap document still reads whole and only an over-cap one is
		// detectable. The cap bounds the READ, never obj.Size, which a ranged or lying
		// store understates (TestOpener_CapsAtDocumentSizeCeiling).
		b, err := io.ReadAll(io.LimitReader(obj.Body, MaxDocumentBytes+1))
		if err != nil {
			return extraction.Document{}, err
		}
		if len(b) > MaxDocumentBytes {
			return extraction.Document{}, fmt.Errorf(
				"docadapter: document %s exceeds the %d-byte ceiling", documentID, MaxDocumentBytes)
		}
		var ct string
		if doc.DeclaredContentType != nil {
			ct = *doc.DeclaredContentType
		}
		return extraction.Document{Bytes: b, ContentType: ct}, nil
	}
}

// PageSink adapts the object store to the extraction seam. bytes.Reader is handed over at
// offset 0 because Put transmits from the reader's CURRENT position, the same trap
// document.Service.Store rewinds for (TestPageSink_PutsToTheDocumentStore).
func PageSink(objects document.ObjectStore) extraction.PageSink {
	return func(ctx context.Context, key string, body []byte) error {
		return objects.Put(ctx, key, bytes.NewReader(body), int64(len(body)))
	}
}

// PageObjectReader adapts the object store to the page-image read seam: whole object, no
// range, body handed over UNREAD so the handler streams it. The key arrives already selected
// off an RLS-visible row, so nothing here rewrites it (TestPageObjectReader_ReadsTheKeyItIsGiven).
func PageObjectReader(objects document.ObjectStore) extraction.PageObject {
	return func(ctx context.Context, key string) (io.ReadCloser, int64, error) {
		obj, err := objects.Get(ctx, key, "")
		// Closed ON the error branch: a Get that hands back both a body and an error still owes
		// a close (TestPageObjectReader_ClosesBodyWhenGetErrors).
		if err != nil {
			if obj.Body != nil {
				_ = obj.Body.Close()
			}
			return nil, 0, err
		}
		if obj.Body == nil {
			return nil, 0, fmt.Errorf("docadapter: page object %s opened with no body", key)
		}
		return obj.Body, obj.Size, nil
	}
}
