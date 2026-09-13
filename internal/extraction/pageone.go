package extraction

import "context"

type ReadPageOne func(ctx context.Context, documentID string) (TokenPage, bool, error)

func PageOneReader(open OpenDocument, r PageReader) ReadPageOne {
	return func(context.Context, string) (TokenPage, bool, error) { return TokenPage{}, false, nil }
}
