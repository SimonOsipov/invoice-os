// Package jsonbody decodes a capped JSON request body. Decode drains the rest
// of the body through the cap because json.Decoder stops after one value, so
// trailing bytes would otherwise slip past the limit.
package jsonbody

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	// DefaultLimit caps routes that carry no line set.
	DefaultLimit int64 = 1 << 20
	// LineItemsLimit caps routes that carry an invoice line set.
	LineItemsLimit int64 = 4 << 20
)

// Decode reads one JSON value from r.Body into dst, reading at most limit+1
// bytes. Bytes after the value are ignored unless they push the body over limit.
func Decode(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, r.Body); TooLarge(err) {
		return err
	}
	return nil
}

// TooLarge reports whether err came from a body over its limit.
func TooLarge(err error) bool {
	return errors.As(err, new(*http.MaxBytesError))
}
