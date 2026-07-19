package dashboard

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store computes the per-tenant dashboard rollup as the invoice_app role. It
// holds the app-role pool (DATABASE_URL); Rollup wraps
// db.WithinRequestTenantTx, so the app.current_tenant GUC is set for the
// transaction and RLS enforces isolation — no `WHERE tenant_id` appears
// anywhere in this package (AC-7).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Rollup runs the per-entity rollup query inside ONE db.WithinRequestTenantTx
// closure (RLS scopes both invoices and business_entities to the caller's
// tenant, so the join needs no `WHERE tenant_id`), scans the rows into
// Clients (pre-declared as []Client{} so an empty tenant still marshals
// "clients":[], never null — AC-1/DASH-03), then sums Clients element-wise
// into Totals in Go (no second aggregate query). needs_attention cuts across
// draft/rejected/failed (AC-3): rejected/failed always count, a draft counts
// only when its violations contain an error-severity entry.
// TopViolations is left empty here — M4-07-02 fills it in the same
// transaction.
func (s *Store) Rollup(ctx context.Context) (Rollup, error) {
	clients := []Client{}
	topViolations := []RuleCount{}

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT
			    i.entity_id,
			    e.name AS entity_name,
			    count(*) FILTER (WHERE i.status = 'draft')     AS draft,
			    count(*) FILTER (WHERE i.status = 'validated') AS validated,
			    count(*) FILTER (WHERE i.status = 'queued')    AS queued,
			    count(*) FILTER (WHERE i.status = 'submitted') AS submitted,
			    count(*) FILTER (WHERE i.status = 'accepted')  AS accepted,
			    count(*) FILTER (WHERE i.status = 'rejected')  AS rejected,
			    count(*) FILTER (WHERE i.status = 'failed')    AS failed,
			    count(*) FILTER (
			        WHERE i.status IN ('rejected', 'failed')
			           OR (i.status = 'draft' AND i.violations @> '[{"severity": "error"}]'::jsonb)
			    ) AS needs_attention
			 FROM invoices i
			 JOIN business_entities e ON e.id = i.entity_id
			 GROUP BY i.entity_id, e.name
			 ORDER BY needs_attention DESC, e.name ASC, i.entity_id ASC`,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Client
			if err := rows.Scan(
				&c.EntityID, &c.EntityName,
				&c.Counts.Draft, &c.Counts.Validated, &c.Counts.Queued, &c.Counts.Submitted,
				&c.Counts.Accepted, &c.Counts.Rejected, &c.Counts.Failed,
				&c.NeedsAttention,
			); err != nil {
				return err
			}
			clients = append(clients, c)
		}
		return rows.Err()
	})
	if err != nil {
		return Rollup{}, err
	}

	var totals Bucket
	for _, c := range clients {
		totals.Counts.Draft += c.Counts.Draft
		totals.Counts.Validated += c.Counts.Validated
		totals.Counts.Queued += c.Counts.Queued
		totals.Counts.Submitted += c.Counts.Submitted
		totals.Counts.Accepted += c.Counts.Accepted
		totals.Counts.Rejected += c.Counts.Rejected
		totals.Counts.Failed += c.Counts.Failed
		totals.NeedsAttention += c.NeedsAttention
	}

	return Rollup{Totals: totals, Clients: clients, TopViolations: topViolations}, nil
}
