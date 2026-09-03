// pagestore.go: PageStore renders a document's pages and PUTs each one. It writes no rows —
// the transaction boundary belongs to the worker.
package extraction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// PageSink stores one rendered page. No content type: document.ObjectStore.Put takes none, and
// the source-document path sets none either.
type PageSink func(ctx context.Context, key string, body []byte) error

// PageKey is the object key for one rendered page. Both segments are server-derived — the
// tenant from the job's own args, the hash from the document's bytes — so no caller-supplied
// text reaches a key.
//
// The tenant segment is never case-transformed: extraction_page_images_key_tenant_scoped
// compares it against uuid::text, which renders lowercase
// (TestPageKey_DoesNotCaseTransformTheTenantSegment). The v1 segment is the render profile, so
// a second profile is a new object rather than an overwrite.
func PageKey(tenantID, contentHash string, page int) string {
	return fmt.Sprintf("tenants/%s/pages/%s/v1/p%04d.png", tenantID, contentHash, page)
}

// PageStore turns a document into stored page images. It touches no database.
type PageStore struct {
	Reader PageReader
	Sink   PageSink
}

// Ingest reads the document once and PUTs every page, returning the rows the caller should
// write together with the page text the layout fingerprint is computed from. A sink failure
// aborts the read and returns nothing of either, so no caller can commit a row or a layout for
// a page that was never stored (TestPageStore_ASinkFailureStopsTheIngest).
func (s *PageStore) Ingest(ctx context.Context, tenantID string, doc Document) ([]PageImage, []TokenPage, PageResult, error) {
	sum := sha256.Sum256(doc.Bytes)
	hash := hex.EncodeToString(sum[:])

	var images []PageImage
	var tokens []TokenPage
	collect := CollectTokens(&tokens)
	res, err := s.Reader.Read(ctx, doc, func(p Page) error {
		key := PageKey(tenantID, hash, p.Number)
		// Page.ImagePNG is borrowed for the duration of this call, which is where it is sent.
		if err := s.Sink(ctx, key, p.ImagePNG); err != nil {
			return err
		}
		if err := collect(p); err != nil {
			return err
		}
		images = append(images, PageImage{
			Page:       p.Number,
			WidthPx:    p.ImageWidth,
			HeightPx:   p.ImageHeight,
			StorageKey: key,
		})
		return nil
	})
	if err != nil {
		return nil, nil, PageResult{}, err
	}
	return images, tokens, res, nil
}
