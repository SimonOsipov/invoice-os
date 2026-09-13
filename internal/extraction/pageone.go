package extraction

import (
	"context"
	"errors"
	"time"
)

// ceiling: clears the ~2 s cold wasm compile, not two long worker reads holding the pool; revisit on page-read WARNs.
const pageOneReadTimeout = 5 * time.Second

// ReadPageOne returns page 1's positioned text. ok is false with a nil error when the document
// has no page 1.
type ReadPageOne func(ctx context.Context, documentID string) (TokenPage, bool, error)

// errPageOneRead stops the read after the first page; PageOneReader reports it as success.
var errPageOneRead = errors.New("extraction: page one read")

// PageOneReader reads through open, the audited opener, and never past the first page. ctx
// bounds the pool borrow too (TestPageOneReader_AnExhaustedPoolReturnsAtItsDeadline).
func PageOneReader(open OpenDocument, r PageReader) ReadPageOne {
	return func(ctx context.Context, documentID string) (TokenPage, bool, error) {
		doc, err := open(ctx, documentID)
		if err != nil {
			return TokenPage{}, false, err
		}

		var page TokenPage
		found := false
		_, err = r.Read(ctx, doc, func(p Page) error {
			// Pages arrive in ascending order: a first page that is not 1 means there is no page 1.
			if p.Number == 1 {
				page = TokenPage{Number: p.Number, WidthPt: p.WidthPt, HeightPt: p.HeightPt, Tokens: p.Tokens}
				found = true
			}
			return errPageOneRead
		})
		if err != nil && !errors.Is(err, errPageOneRead) {
			return TokenPage{}, false, err
		}
		return page, found, nil
	}
}
