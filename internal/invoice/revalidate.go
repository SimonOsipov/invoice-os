package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DemoteRevalidatedTx walks a validated invoice back to draft after a re-run of
// the rule set: stamps the re-evaluated violations + rule_set_version_id, then
// transitions validated->draft as RevalidateActor. The caller owns the tx
// (Mark*Tx precedent, actor.go).
//
// Errors propagate RAW so their SQLSTATE survives — a phantom ruleSetVersionID
// must surface as the FK's 23503
// (TestDemoteRevalidated_AtomicityRollsBackOnWriteFailure).
func (s *Store) DemoteRevalidatedTx(ctx context.Context, tx pgx.Tx, id, tenantID string, vs []Violation, ruleSetVersionID string) (Invoice, error) {
	var locked Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, id,
	), &locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return Invoice{}, ErrValidation
		}
		return Invoice{}, err
	}

	// One guard for both restartability and concurrency: the only
	// content-mutating route, Store.Edit, already demotes to draft in its own
	// tx, so a row still validated post-lock cannot have drifted.
	if locked.Status != StatusValidated {
		return locked, nil
	}

	// Normalize the SLICE before marshalling ([violations-write]):
	// json.Marshal(nil []Violation) yields the JSON scalar null, which binds
	// happily to `violations jsonb NOT NULL` and silently poisons the column.
	if vs == nil {
		vs = []Violation{}
	}
	violationsJSON, err := json.Marshal(vs)
	if err != nil {
		return Invoice{}, fmt.Errorf("marshal violations: %w", err)
	}

	var inv Invoice
	if err := scanInvoice(tx.QueryRow(ctx,
		`UPDATE invoices SET violations = $1, rule_set_version_id = $2 WHERE id = $3 RETURNING `+invoiceColumns,
		violationsJSON, ruleSetVersionID, id,
	), &inv); err != nil {
		return Invoice{}, err
	}

	actor := RevalidateActor(tenantID)
	// transitionTx writes the history row AND the invoice.transitioned audit
	// row; its RETURNING re-reads the stamp above on the same tx.
	if inv, err = transitionTx(ctx, tx, id, StatusValidated, StatusDraft, actor); err != nil {
		return Invoice{}, err
	}

	// No run outlives the promotion it belonged to (APPR-06-07, D37). The sweep has no
	// request identity, so the canceller is RevalidateActor's fixed literal.
	if _, err := approval.CancelLiveRunTx(ctx, tx, id, actor.Subject); err != nil {
		return Invoice{}, err
	}

	if err := audit.Record(ctx, tx, actor.Subject, "invoice.validated", map[string]any{
		"id":                  id,
		"rule_set_version_id": ruleSetVersionID,
		"outcome":             "demoted",
		"violation_count":     len(vs),
	}); err != nil {
		return Invoice{}, err
	}

	return inv, nil
}

// ErrRevalidatePrivilegedRole refuses a SUPERUSER/BYPASSRLS connection: the
// tenant_isolation policy would be inert and the pass could demote invoices
// across tenants with no error to show for it (mirrors
// importer.ErrBackfillPrivilegedRole, internal/importer/backfill.go:23).
var ErrRevalidatePrivilegedRole = errors.New("invoice: refusing to revalidate over a SUPERUSER/BYPASSRLS role; run as invoice_app")

// RevalidateResult is one tenant's re-validation run summary. Demoted is
// reported identically on both paths -- a dry run writes nothing but still
// counts the yield a real run would produce.
// Skipped counts a row that stopped being validated between the list read
// and the write lock (DemoteRevalidatedTx's own no-op guard).
type RevalidateResult struct {
	Examined int
	Demoted  int
	Clean    int
	Skipped  int
	Notes    []string
}

// revalidateChunkSize bounds one 04 round trip; defaultValidateTimeout
// (validator.go) is sized for ~500 invoices, so 200 sits well inside it.
const revalidateChunkSize = 200

// RevalidateActive re-evaluates every status='validated' invoice for
// tenantID against the active rule set (via gate, over HTTP -- never
// internal/validation in-process) and demotes any that now carry a blocking
// violation back to draft via DemoteRevalidatedTx. See this package's
// gate.go header for why every invoice evaluated here must be
// Store.Get-hydrated, never Store.List-sourced.
//
// dryRun evaluates and reports without writing; it is also how --verify
// re-scans, since stored violations reflect the rule set active at the last
// validation and no SQL predicate can answer "would this fail today".
func RevalidateActive(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *Store,
	gate *Gate,
	tenantID string,
	dryRun bool,
) (RevalidateResult, error) {
	if err := refuseRevalidatePrivilegedRole(ctx, pool); err != nil {
		return RevalidateResult{}, err
	}

	// Built from our OWN tenantID argument rather than a caller's context, so
	// "run as A, demote B's invoices" is unrepresentable.
	ctx = auth.WithIdentity(ctx, auth.Identity{
		Subject:  RevalidateActor(tenantID).Subject,
		Role:     "authenticated",
		TenantID: tenantID,
	})

	ids, err := validatedInvoiceIDs(ctx, pool, tenantID)
	if err != nil {
		return RevalidateResult{}, err
	}

	var res RevalidateResult
	for chunk := range slices.Chunk(ids, revalidateChunkSize) {
		items := make([]EvalItem, 0, len(chunk))
		for _, id := range chunk {
			// Get, NEVER List: List leaves LineItems nil and MBSPayload cannot
			// tell that from a genuinely line-less invoice, which would fire
			// line-items-required on the whole run (gate.go's header).
			inv, err := store.Get(ctx, id)
			if err != nil {
				return RevalidateResult{}, fmt.Errorf("invoice: revalidate: get invoice %s: %w", id, err)
			}
			items = append(items, EvalItem{Ref: inv.ID, Invoice: inv})
		}

		out, err := gate.Evaluate(ctx, items)
		if err != nil {
			// ErrUpstream/ErrNoActiveRuleSet propagate RAW and abort the tenant:
			// an outage is never a verdict.
			return RevalidateResult{}, err
		}

		for _, it := range items {
			res.Examined++
			vs := out.ByRef[it.Ref]
			if !HasBlockingViolation(vs) {
				res.Clean++
				continue
			}
			res.Notes = append(res.Notes, fmt.Sprintf("invoice %s: blocked by %s", it.Ref, strings.Join(blockingRuleKeys(vs), ", ")))

			if dryRun {
				res.Demoted++
				continue
			}
			demoted, err := demoteRevalidated(ctx, pool, store, tenantID, it.Ref, vs, out.RuleSetVersionID)
			if err != nil {
				return RevalidateResult{}, fmt.Errorf("invoice: revalidate: demote invoice %s: %w", it.Ref, err)
			}
			if demoted {
				res.Demoted++
			} else {
				res.Skipped++
			}
		}
	}
	return res, nil
}

// refuseRevalidatePrivilegedRole fails closed before the first invoice is
// read. pg_roles is world-readable, so this costs one query.
func refuseRevalidatePrivilegedRole(ctx context.Context, pool *pgxpool.Pool) error {
	var privileged bool
	if err := pool.QueryRow(ctx,
		`SELECT rolbypassrls OR rolsuper FROM pg_roles WHERE rolname = current_user`,
	).Scan(&privileged); err != nil {
		return fmt.Errorf("invoice: check current_user privileges: %w", err)
	}
	if privileged {
		return ErrRevalidatePrivilegedRole
	}
	return nil
}

// validatedInvoiceIDs lists the tenant's validated invoices. RLS scopes the
// SELECT; no tenant_id predicate is written by hand. The status filter IS the
// terminal-status exclusion, and ORDER BY id makes the run deterministic.
func validatedInvoiceIDs(ctx context.Context, pool *pgxpool.Pool, tenantID string) ([]string, error) {
	var ids []string
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM invoices WHERE status = 'validated' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("invoice: revalidate: list validated invoices: %w", err)
	}
	return ids, nil
}

// demoteRevalidated runs one demotion in its own tx and reports whether it
// happened. The status re-read is what keeps Skipped honest: DemoteRevalidatedTx
// silently no-ops on a row that stopped being validated, and counting that as a
// demotion would inflate the only number the run reports.
func demoteRevalidated(ctx context.Context, pool *pgxpool.Pool, store *Store, tenantID, id string, vs []Violation, ruleSetVersionID string) (bool, error) {
	var demoted bool
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if Status(status) != StatusValidated {
			return nil
		}
		if _, err := store.DemoteRevalidatedTx(ctx, tx, id, tenantID, vs, ruleSetVersionID); err != nil {
			return err
		}
		demoted = true
		return nil
	})
	return demoted, err
}

// blockingRuleKeys names the severity:"error" rules behind a demotion, for the
// run's Notes (what --verify lists).
func blockingRuleKeys(vs []Violation) []string {
	var keys []string
	for _, v := range vs {
		if v.Severity == "error" {
			keys = append(keys, v.RuleKey)
		}
	}
	return keys
}
