package erp

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/importer"
)

// IngestArgs is one document revision to take. Stub: the executor replaces this file whole.
type IngestArgs struct {
	TenantID       string `json:"tenant_id"`
	ConnectionID   string `json:"connection_id"`
	SourceDocID    string `json:"source_doc_id"`
	SourceRevision string `json:"source_revision"`
	Deleted        bool   `json:"deleted"`
}

func (IngestArgs) Kind() string { return "" }

func (a IngestArgs) Tenant() string { return a.TenantID }

func (IngestArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

// IngestKey is the EnqueueTx dedupe key of one ingest.
func IngestKey(connectionID string, epoch int64, sourceDocID, sourceRevision string, deleted bool) string {
	return ""
}

type IngestWorker struct {
	river.WorkerDefaults[IngestArgs]
	Pool     *pgxpool.Pool
	Registry Registry
	Invoices importer.DraftCreator
	Gate     importer.DraftValidator
	Logger   *slog.Logger
}

func (w *IngestWorker) Work(context.Context, *river.Job[IngestArgs]) error { return nil }
