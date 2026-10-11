package invoice

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

var errNotImplemented = errors.New("invoice: not implemented")

// CodeEntry is one hit of the NRS code-list search.
type CodeEntry struct {
	Code    string  `json:"code"`
	Label   string  `json:"label"`
	Percent *string `json:"percent"`
}

// codeSearchLists are the lists the search serves; the stub allows none.
var codeSearchLists = []string{}

// SearchCodes is a stub: no codes, no error.
func (s *Store) SearchCodes(ctx context.Context, list, q string, limit int) ([]CodeEntry, error) {
	return nil, nil
}

// CodeSearchHandler is a stub.
func CodeSearchHandler(search func(ctx context.Context, list, q string, limit int) ([]CodeEntry, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, msg := statusForErr(errNotImplemented)
		writeError(w, status, msg)
	}
}
