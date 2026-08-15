package dashboard

import (
	"context"
	"sort"

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

// Rollup runs the per-entity rollup query AND the per-rule top-violations
// breakdown inside ONE db.WithinRequestTenantTx closure (RLS scopes
// invoices/business_entities to the caller's tenant, so neither query needs
// `WHERE tenant_id`), scans the per-entity rows into Clients (pre-declared as
// []Client{} so an empty tenant still marshals "clients":[], never null —
// AC-1/DASH-03), then sums Clients element-wise into Totals in Go (no second
// aggregate query). needs_attention cuts across draft/rejected/failed
// (AC-3): rejected always counts, failed counts unless resolved outside
// (kept_as_is_at set), a draft counts when its violations contain an
// error-severity entry OR its most recent approval run closed 'rejected'. That
// disjunction is a hand-maintained twin of the f.NeedsAttention list fragment
// (internal/invoice/store.go): only the i. alias and the correlation column
// differ, and TestStoreList_NeedsAttentionMatchesDashboardRollup compares the
// two by behaviour. awaiting_approval is the SECOND overlay and a sibling
// of needs_attention, never an eighth state: validated invoices an active
// policy blocks, the predicate copied from the awaiting_approval list filter
// (internal/invoice/store.go) so the badge and the filtered list cannot
// disagree about the word. Its NOT EXISTS (approved run) conjunct is satisfied
// VACUOUSLY by an invoice with zero runs, so a tenant with an active policy but
// nothing armed reads awaiting_approval == counts.validated
// (TestStoreRollup_AwaitingApprovalCountsAnUnarmedValidatedInvoice).
// TopViolations is grouped per entity_id AND rule_key and attached to each
// Client; the root/Totals list is the Go-side sum across entities, re-sorted
// invoices DESC then rule_key ASC (map iteration order isn't deterministic, so
// the sort can't be skipped).
func (s *Store) Rollup(ctx context.Context) (Rollup, error) {
	clients := []Client{}
	ruleSums := map[string]int{}

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`WITH flagged AS (
			    SELECT i.id, i.entity_id, e.name AS entity_name, i.status, i.vat, i.kept_as_is_at, i.violations,
			           (i.status = 'draft' AND i.rule_set_version_id IS NULL)                       AS is_never_validated,
			           (i.status = 'draft' AND i.violations @> '[{"severity": "error"}]'::jsonb)    AS is_blocked_by_rules,
			           (i.status = 'rejected' OR (i.status = 'failed' AND i.kept_as_is_at IS NULL)) AS is_failed_in_transmission,
			           coalesce(cf.f_field, false) AS fails_field,
			           coalesce(cf.f_tax,   false) AS fails_tax,
			           coalesce(cf.f_ident, false) AS fails_ident
			    FROM invoices i
			    JOIN business_entities e ON e.id = i.entity_id
			    -- CASE (not WHERE) guards a non-array violations value: jsonb_array_elements
			    -- errors on non-array input, and a WHERE here would drop the whole invoice
			    -- row instead of just zeroing its bar contribution.
			    LEFT JOIN LATERAL (
			        SELECT bool_or(v->>'rule_key' = ANY($1::text[])) AS f_field,
			               bool_or(v->>'rule_key' = ANY($2::text[])) AS f_tax,
			               bool_or(v->>'rule_key' = ANY($3::text[])) AS f_ident
			        FROM jsonb_array_elements(
			                 CASE WHEN jsonb_typeof(i.violations) = 'array' THEN i.violations ELSE '[]'::jsonb END) AS v
			        WHERE v->>'severity' = 'error'
			    ) cf ON true
			)
			-- needs_attention below keeps its exact literal disjunction (i.-qualified,
			-- no IN(...)), and its fourth arm reads the LATEST run only, through a
			-- derived table: TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare pins
			-- both, which is why the CTE is aliased i.
			SELECT i.entity_id, i.entity_name,
			       count(*) FILTER (WHERE i.status = 'draft')     AS draft,
			       count(*) FILTER (WHERE i.status = 'validated') AS validated,
			       count(*) FILTER (WHERE i.status = 'queued')    AS queued,
			       count(*) FILTER (WHERE i.status = 'submitted') AS submitted,
			       count(*) FILTER (WHERE i.status = 'accepted')  AS accepted,
			       count(*) FILTER (WHERE i.status = 'rejected')  AS rejected,
			       count(*) FILTER (WHERE i.status = 'failed')    AS failed,
			       count(*) FILTER (
			           WHERE i.status = 'rejected'
			              OR (i.status = 'failed' AND i.kept_as_is_at IS NULL)
			              OR (i.status = 'draft' AND i.violations @> '[{"severity": "error"}]'::jsonb)
			              OR (i.status = 'draft' AND EXISTS (
			                      SELECT 1 FROM (SELECT r.state FROM approval_runs r
			                                      WHERE r.invoice_id = i.id
			                                      ORDER BY r.opened_at DESC LIMIT 1) lr
			                       WHERE lr.state = 'rejected'))
			       ) AS needs_attention,
			       -- Copied from the awaiting_approval list filter (internal/invoice/store.go),
			       -- alias added. i.id is qualified: approval_runs has its own id, and a bare
			       -- id binds there and silently never matches.
			       count(*) FILTER (
			           WHERE i.status = 'validated'
			             AND EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)
			             AND NOT EXISTS (SELECT 1 FROM approval_runs r
			                              WHERE r.invoice_id = i.id AND r.state = 'approved')
			       ) AS awaiting_approval,
			       count(*)                                            AS total,
			       count(*) FILTER (WHERE i.is_never_validated)        AS never_validated,
			       count(*) FILTER (WHERE i.is_blocked_by_rules)       AS blocked_by_rules,
			       count(*) FILTER (WHERE i.is_failed_in_transmission) AS failed_in_transmission,
			       count(*) FILTER (WHERE NOT i.is_blocked_by_rules
			                          AND NOT i.is_failed_in_transmission
			                          AND NOT i.is_never_validated)    AS readiness_num,
			       count(*) FILTER (WHERE NOT i.is_never_validated AND NOT i.fails_field) AS bar_field_num,
			       count(*) FILTER (WHERE NOT i.is_never_validated AND NOT i.fails_tax)   AS bar_tax_num,
			       count(*) FILTER (WHERE NOT i.is_never_validated AND NOT i.fails_ident) AS bar_ident_num,
			       coalesce(sum(round(i.vat * 100)), 0)::bigint                           AS vat_kobo
			FROM flagged i
			GROUP BY i.entity_id, i.entity_name
			ORDER BY needs_attention DESC, i.entity_name ASC, i.entity_id ASC`,
			categoryKeys(CategoryFieldCompleteness), categoryKeys(CategoryTaxAccuracy), categoryKeys(CategoryIdentifiers),
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Client
			var total, neverValidated, blockedByRules, failedInTransmission int64
			var readinessNum, barFieldNum, barTaxNum, barIdentNum, vatKobo int64
			if err := rows.Scan(
				&c.EntityID, &c.EntityName,
				&c.Counts.Draft, &c.Counts.Validated, &c.Counts.Queued, &c.Counts.Submitted,
				&c.Counts.Accepted, &c.Counts.Rejected, &c.Counts.Failed,
				&c.NeedsAttention, &c.AwaitingApproval, &total,
				&neverValidated, &blockedByRules, &failedInTransmission,
				&readinessNum, &barFieldNum, &barTaxNum, &barIdentNum, &vatKobo,
			); err != nil {
				return err
			}
			c.Metrics = map[string]Metric{
				MetricReadiness:            {Num: readinessNum, Den: total},
				MetricBarFieldCompleteness: {Num: barFieldNum, Den: total},
				MetricBarTaxAccuracy:       {Num: barTaxNum, Den: total},
				MetricBarIdentifiersFormat: {Num: barIdentNum, Den: total},
				MetricBlockedByRules:       {Num: blockedByRules, Den: total},
				MetricFailedInTransmission: {Num: failedInTransmission, Den: total},
				MetricNeverValidated:       {Num: neverValidated, Den: total},
				MetricVATTracked:           {Num: vatKobo, Den: total},
			}
			c.TopViolations = []RuleCount{}
			clients = append(clients, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// jsonb_typeof(...) = 'array' guards jsonb_array_elements below: unlike
		// the per-entity query's `@>` predicate (which just returns false on a
		// type mismatch), jsonb_array_elements RAISES AN ERROR on non-array
		// input. Postgres pushes this predicate to the base-table scan (EXPLAIN-
		// confirmed), so it costs nothing on the array-shaped rows the real
		// write path always produces.
		ruleRows, err := tx.Query(ctx,
			`SELECT i.entity_id, v->>'rule_key' AS rule_key, count(DISTINCT i.id) AS invoices
			 FROM invoices i
			 CROSS JOIN LATERAL jsonb_array_elements(i.violations) AS v
			 WHERE jsonb_typeof(i.violations) = 'array'
			   AND v->>'severity' = 'error'
			   AND nullif(v->>'rule_key', '') IS NOT NULL
			 GROUP BY i.entity_id, 2
			 ORDER BY i.entity_id, 3 DESC, 2 ASC`,
		)
		if err != nil {
			return err
		}
		defer ruleRows.Close()
		byEntity := map[string][]RuleCount{}
		for ruleRows.Next() {
			var entityID string
			var rc RuleCount
			if err := ruleRows.Scan(&entityID, &rc.RuleKey, &rc.Invoices); err != nil {
				return err
			}
			byEntity[entityID] = append(byEntity[entityID], rc)
			ruleSums[rc.RuleKey] += rc.Invoices
		}
		if err := ruleRows.Err(); err != nil {
			return err
		}
		// Row order within each entity_id group is already invoices DESC,
		// rule_key ASC from the query -- no further per-client sort needed.
		for i := range clients {
			if list, ok := byEntity[clients[i].EntityID]; ok {
				clients[i].TopViolations = list
			}
		}
		return nil
	})
	if err != nil {
		return Rollup{}, err
	}

	totals := Bucket{Metrics: map[string]Metric{}, TopViolations: []RuleCount{}}
	for _, c := range clients {
		totals.Counts.Draft += c.Counts.Draft
		totals.Counts.Validated += c.Counts.Validated
		totals.Counts.Queued += c.Counts.Queued
		totals.Counts.Submitted += c.Counts.Submitted
		totals.Counts.Accepted += c.Counts.Accepted
		totals.Counts.Rejected += c.Counts.Rejected
		totals.Counts.Failed += c.Counts.Failed
		totals.NeedsAttention += c.NeedsAttention
		totals.AwaitingApproval += c.AwaitingApproval
		addMetrics(totals.Metrics, c.Metrics)
	}

	// Re-sort is mandatory: map iteration order is randomized, and
	// invoices-DESC/rule_key-ASC is a complete order (rule_key is unique
	// per group), not a probabilistic tie-break.
	topViolations := []RuleCount{}
	for k, v := range ruleSums {
		topViolations = append(topViolations, RuleCount{RuleKey: k, Invoices: v})
	}
	sort.Slice(topViolations, func(i, j int) bool {
		if topViolations[i].Invoices != topViolations[j].Invoices {
			return topViolations[i].Invoices > topViolations[j].Invoices
		}
		return topViolations[i].RuleKey < topViolations[j].RuleKey
	})
	totals.TopViolations = topViolations

	return Rollup{Totals: totals, Clients: clients, TopViolations: topViolations}, nil
}

// addMetrics sums src into dst element-wise, keyed by metric name -- a
// future metric costs one SQL FILTER and one map entry, never a change here.
func addMetrics(dst, src map[string]Metric) {
	for k, m := range src {
		d := dst[k]
		d.Num += m.Num
		d.Den += m.Den
		dst[k] = d
	}
}
