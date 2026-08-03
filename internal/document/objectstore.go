package document

import (
	"context"
	"io"
)

// ObjectStore is this package's own minimal view of object storage, so no
// *s3.Client surfaces above s3.go.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
	Get(ctx context.Context, key, rangeHeader string) (Object, error)
}

// Object is one Get result. Body is handed back UNREAD — the caller streams and
// closes it; nothing here buffers the object.
type Object struct {
	Body io.ReadCloser
	// Size is the byte count of Body. On a 206 that is the range's length, not
	// the object's full size — the total is only in ContentRange's /N suffix.
	Size         int64
	ContentRange string // "" unless a range was requested and honoured
	Partial      bool
}
