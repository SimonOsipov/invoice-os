package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/actor"
)

// ExportCSVHeader is the column order of audit_log.csv in the workspace export.
var ExportCSVHeader = []string{
	"id", "created_at", "event", "actor", "actor_name", "actor_kind",
	"entity_id", "company_name", "company_scope", "invoice_id", "payload",
}

const exportPageSize = 500

type exportRow struct {
	id        int64
	createdAt time.Time
	event     string
	actor     string
	entityID  *string
	company   *string
	invoiceID *string
	payload   []byte
}

// WriteExportCSV writes every audit row of tx's tenant, newest first, and returns the
// row count. It writes no tenant_id predicate: RLS on tx's tenant bounds the read.
func WriteExportCSV(ctx context.Context, tx pgx.Tx, w interface{ Write([]string) error }) (int, error) {
	if err := w.Write(ExportCSVHeader); err != nil {
		return 0, err
	}
	var cur *Cursor
	total := 0
	for {
		page, err := exportPage(ctx, tx, cur)
		if err != nil {
			return total, err
		}
		if len(page) == 0 {
			return total, nil
		}
		subjects := make([]string, len(page))
		for i, r := range page {
			subjects[i] = r.actor
		}
		labels, err := actor.Resolve(ctx, tx, subjects)
		if err != nil {
			return total, fmt.Errorf("audit: resolve actors: %w", err)
		}
		for _, r := range page {
			l := labels[r.actor]
			var compact bytes.Buffer
			if err := json.Compact(&compact, r.payload); err != nil {
				return total, fmt.Errorf("audit: compact payload of row %d: %w", r.id, err)
			}
			rec := []string{
				strconv.FormatInt(r.id, 10),
				r.createdAt.UTC().Format(time.RFC3339Nano),
				r.event,
				r.actor,
				l.Text,
				string(l.Kind),
				csvCell(r.entityID),
				csvCell(r.company),
				string(ScopeOf(r.event, r.entityID)),
				csvCell(r.invoiceID),
				compact.String(),
			}
			if err := w.Write(rec); err != nil {
				return total, err
			}
			total++
		}
		last := page[len(page)-1]
		cur = &Cursor{CreatedAt: last.createdAt, ID: last.id}
		if len(page) < exportPageSize {
			return total, nil
		}
	}
}

func exportPage(ctx context.Context, tx pgx.Tx, cur *Cursor) ([]exportRow, error) {
	sql := `SELECT a.id, a.created_at, a.event, a.actor, a.entity_id::text, be.name, a.invoice_id::text, a.payload
	          FROM audit_log a
	          LEFT JOIN business_entities be ON be.id = a.entity_id`
	var args []any
	if cur != nil {
		sql += ` WHERE (a.created_at, a.id) < ($1, $2)`
		args = append(args, cur.CreatedAt, cur.ID)
	}
	sql += ` ORDER BY a.created_at DESC, a.id DESC LIMIT ` + strconv.Itoa(exportPageSize)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query export page: %w", err)
	}
	defer rows.Close()
	var out []exportRow
	for rows.Next() {
		var r exportRow
		if err := rows.Scan(&r.id, &r.createdAt, &r.event, &r.actor, &r.entityID, &r.company, &r.invoiceID, &r.payload); err != nil {
			return nil, fmt.Errorf("audit: scan export row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: read export page: %w", err)
	}
	return out, nil
}

func csvCell(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
