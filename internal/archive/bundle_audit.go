package archive

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// bundleAudit is the payload of both evidence_bundle events.
type bundleAudit struct {
	EntityID string `json:"entity_id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
}

// recordBundleEvent writes a download's audit rows in their own short transaction. It skips the
// seam's lock gate so a locked workspace's download is still audited.
func (s *Store) recordBundleEvent(ctx context.Context, tenantID string, write func(pgx.Tx) error) error {
	return db.WithinTenantTxOpts(ctx, s.pool, tenantID, pgx.TxOptions{}, write)
}

func (s *Store) writeBundleEvent(ctx context.Context, tenantID string, write func(pgx.Tx) error) error {
	if s.auditTx != nil {
		return s.auditTx(ctx, tenantID, write)
	}
	return s.recordBundleEvent(ctx, tenantID, write)
}

// firstWriteLog calls log once, before the first bytes go to w.
type firstWriteLog struct {
	w    io.Writer
	log  func()
	done bool
}

func (f *firstWriteLog) Write(p []byte) (int, error) {
	if !f.done {
		f.done = true
		f.log()
	}
	return f.w.Write(p)
}

// streamAudited runs read, writes the exported row, then closes the ZIP. After the first byte any
// failure writes the failed row instead; the original error is returned.
func (s *Store) streamAudited(ctx context.Context, w io.Writer, tenantID, actor string, r Request,
	read func(io.Writer) (*bundleWriter, error), exported, failed func(ctx context.Context, tx pgx.Tx, p bundleAudit) error) error {
	sent := &sentCounter{w: w}
	fw := &firstWriteLog{w: sent, log: func() {
		slog.Default().InfoContext(ctx, "archive: evidence bundle download started", bundleLogAttrs(actor, tenantID, r)...)
	}}
	payload := func(bw *bundleWriter, bytes int64) bundleAudit {
		p := bundleAudit{
			EntityID: uuid.MustParse(r.EntityID).String(),
			From:     r.From.UTC().Format(time.RFC3339),
			To:       r.To.UTC().Format(time.RFC3339),
			Bytes:    bytes,
		}
		if bw != nil {
			p.Files = len(bw.entries)
		}
		return p
	}

	bw, err := read(fw)
	if err == nil {
		var sum int64
		for _, e := range bw.entries {
			sum += e.Bytes
		}
		p := payload(bw, sum)
		err = s.writeBundleEvent(ctx, tenantID, func(tx pgx.Tx) error { return exported(ctx, tx, p) })
		if err == nil {
			// Close alone writes the central directory; a failure here keeps the committed row.
			return bw.Close()
		}
	}
	if sent.wrote {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		p := payload(bw, sent.n)
		if aerr := s.writeBundleEvent(actx, tenantID, func(tx pgx.Tx) error { return failed(actx, tx, p) }); aerr != nil {
			slog.Default().ErrorContext(ctx, "archive: evidence bundle failure not audited",
				append(bundleLogAttrs(actor, tenantID, r), "files", p.Files, "bytes", p.Bytes, "err", aerr)...)
		}
	}
	return err
}

func bundleLogAttrs(actor, tenantID string, r Request) []any {
	return []any{
		"actor", actor, "tenant_id", tenantID, "entity_id", r.EntityID,
		"from", r.From.UTC().Format(time.RFC3339), "to", r.To.UTC().Format(time.RFC3339),
	}
}
