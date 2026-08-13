package approval

// The transmit gate: the pure predicate plus the three tx-scoped reads that feed it.
// Every body below is a Mode-A stub — gate_test.go's specs are RED against them.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// GateFacts is one invoice's approval standing, for the detail page's gate.
type GateFacts struct {
	PolicyActive    bool
	ApprovedRun     bool
	RunState        string // "" when the invoice has no run at all
	PendingStepOrd  *int   // nil when no kind='approval' step is pending
	CallerHoldsRole bool
}

// RowFacts is one invoice row's approval standing, for the list.
type RowFacts struct {
	RunState          string     `json:"run_state"`
	PendingOrd        *int       `json:"pending_ord"`
	PendingRoleTitle  *string    `json:"pending_role_title"`
	PendingHolderWarn bool       `json:"pending_holder_warn"`
	DueAt             *time.Time `json:"due_at"`
	Overdue           bool       `json:"overdue"`
}

// TransmitClear reports whether an invoice may pass into queued.
func TransmitClear(policyActive, approvedRun bool) bool {
	return false // stub
}

// TransmitClearTx answers TransmitClear for a set of invoice ids.
func TransmitClearTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	return nil, nil // stub
}

// GateFactsTx reads one invoice's gate standing for subject.
func GateFactsTx(ctx context.Context, tx pgx.Tx, invoiceID, subject string) (GateFacts, error) {
	return GateFacts{}, nil // stub
}

// RowFactsTx reads the list-row approval standing of a set of invoice ids.
func RowFactsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]RowFacts, error) {
	return nil, nil // stub
}
