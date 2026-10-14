package staffjobs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// Compile stubs for SUPP-06-06 Mode A: the executor deletes this file and writes the real Cancel and CancelHandler.

func (s *Store) Cancel(ctx context.Context, id, reason string) (Detail, error) {
	return Detail{}, errors.New("not implemented")
}

func CancelHandler(cancel func(ctx context.Context, id, reason string) (Detail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
