package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

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
