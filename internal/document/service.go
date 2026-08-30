package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Service composes the row store and the object store into the two operations
// callers actually perform: store a source document, and open one for reading.
type Service struct {
	store   *Store
	objects ObjectStore
}

func NewService(store *Store, objects ObjectStore) *Service {
	return &Service{store: store, objects: objects}
}

// Store hashes the body, PUTs the bytes, then upserts the row — in that order. A
// failed PUT returns before any row is written, so nothing downstream can
// believe a document exists; the reverse order would leave a row pointing at
// nothing, which is strictly worse than an orphaned object.
//
// The hash and the byte count are computed here; the size argument is only the
// value the caller declared, and is deliberately overridden. filename is
// sanitized here rather than by the caller, so every path into storage is
// coerced.
//
// reused is true when the tenant already held this content hash: it is Upsert's created
// flag, inverted once here so no caller has to.
func (s *Service) Store(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (doc Document, reused bool, err error) {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return Document{}, false, db.ErrNoTenant
	}

	h := sha256.New()
	n, err := io.Copy(h, body)
	if err != nil {
		return Document{}, false, fmt.Errorf("document: hash body: %w", err)
	}
	// Put transmits from the reader's CURRENT offset and the hash pass above left
	// it at EOF, so without this the PUT sends zero bytes under a declared length.
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return Document{}, false, fmt.Errorf("document: rewind body: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	key := StorageKey(id.TenantID, hash)
	if err := s.objects.Put(ctx, key, body, n); err != nil {
		return Document{}, false, err
	}

	d := Document{StorageKey: key, ContentHash: hash, SizeBytes: n}
	if name := SanitizeFilename(filename); name != "" {
		d.Filename = &name
	}
	if contentType != "" {
		d.DeclaredContentType = &contentType
	}

	stored, created, err := s.store.Upsert(ctx, d)
	if err != nil {
		return Document{}, false, err
	}
	return stored, !created, nil
}

// Open resolves the document row, then fetches its object. rangeHeader is
// forwarded verbatim. The row lookup runs first so a cross-tenant or absent id
// is refused by RLS before any object-storage call happens.
func (s *Service) Open(ctx context.Context, id, rangeHeader string) (Document, Object, error) {
	doc, err := s.store.Get(ctx, id)
	if err != nil {
		return Document{}, Object{}, err
	}
	obj, err := s.objects.Get(ctx, doc.StorageKey, rangeHeader)
	if err != nil {
		return Document{}, Object{}, err
	}
	return doc, obj, nil
}
