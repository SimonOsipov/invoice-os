package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
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

// RevalidateResult is one tenant's re-validation run summary (task-412 /
// BUG-05-03). Demoted is reported identically on both paths -- a dry run
// writes nothing but still counts the yield a real run would produce.
// Skipped counts a row that stopped being validated between the list read
// and the write lock (DemoteRevalidatedTx's own no-op guard).
type RevalidateResult struct {
	Examined int
	Demoted  int
	Clean    int
	Skipped  int
	Notes    []string
}

// RevalidateActive re-evaluates every status='validated' invoice for
// tenantID against the active rule set (via gate, over HTTP -- never
// internal/validation in-process) and demotes any that now carry a blocking
// violation back to draft via DemoteRevalidatedTx. See this package's
// gate.go header for why every invoice evaluated here must be
// Store.Get-hydrated, never Store.List-sourced.
//
// STAGE 2.5 (Mode A) STUB: not yet implemented (task-412). Every
// TestRevalidateActive_*/TestRevalidateAllTenants_*/TestRevalidateVerify_*
// spec in revalidate_test.go must fail on this sentinel error, never on a
// compile error.
func RevalidateActive(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *Store,
	gate *Gate,
	tenantID string,
	dryRun bool,
) (RevalidateResult, error) {
	return RevalidateResult{}, errors.New("invoice: RevalidateActive not implemented")
}
