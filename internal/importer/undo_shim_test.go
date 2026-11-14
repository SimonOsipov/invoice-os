package importer

// SECAUD-50-03 red shim: lets the undo tests compile before the product code exists.
// DELETE THIS FILE when store.go and handlers.go declare these symbols.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

type UndoResult struct{}

var (
	ErrUndoForbidden = errors.New("importer: undo forbidden")
	ErrNotUndoable   = errors.New("importer: not undoable")
	ErrImportSent    = errors.New("importer: import sent")
)

func (s *Store) UndoBatch(ctx context.Context, id string) (UndoResult, error) {
	return UndoResult{}, nil
}

func UndoHandler(undo func(ctx context.Context, id string) (UndoResult, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
