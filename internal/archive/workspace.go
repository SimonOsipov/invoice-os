package archive

import (
	"context"
	"errors"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// STUB: signatures only; DEL-06-02 replaces every body.

type ObjectOpener func(ctx context.Context, key string) (io.ReadCloser, error)

const maxConcurrentExports = 1

var (
	ErrExportNotPermitted = errors.New("archive: only an admin can download the workspace export")
	ErrExportBusy         = errors.New("archive: another workspace export is running")
	ErrExportRunning      = errors.New("archive: an export of this workspace is already running")
)

type WorkspaceStore struct {
	pool        *pgxpool.Pool
	open        ObjectOpener
	slots       chan struct{}
	auditFailed func(ctx context.Context, id auth.Identity, files int, bytes int64) error
}

func NewWorkspaceStore(pool *pgxpool.Pool, open ObjectOpener) *WorkspaceStore {
	return &WorkspaceStore{pool: pool, open: open, slots: make(chan struct{}, maxConcurrentExports)}
}

func exportTag(tenantID string) string { return "" }

func (s *WorkspaceStore) AssembleWorkspace(ctx context.Context, w io.Writer, onStart func(filename string)) error {
	return errors.New("archive: AssembleWorkspace not implemented")
}

func (s *WorkspaceStore) PreflightWorkspace(ctx context.Context) error {
	return errors.New("archive: PreflightWorkspace not implemented")
}

func companyDir(name, id string, taken map[string]bool) string { return "" }

func documentExt(filename *string) string { return "" }
