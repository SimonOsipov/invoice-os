package invoice

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const (
	ruleNoteOriginalMissing       = "note-original-missing"
	ruleNoteOriginalNotCommercial = "note-original-not-commercial"
	ruleNoteOriginalNotAccepted   = "note-original-not-accepted"
	ruleNoteOriginalNoIRN         = "note-original-no-irn"
	ruleCreditNoteTotalUnusable   = "credit-note-total-unusable"
	ruleCreditNoteCapUnreadable   = "credit-note-cap-unreadable"
	ruleCreditNoteOverCap         = "credit-note-over-cap"
)

var noteCheckKeys = []string{
	ruleNoteOriginalMissing, ruleNoteOriginalNotCommercial, ruleNoteOriginalNotAccepted,
	ruleNoteOriginalNoIRN, ruleCreditNoteTotalUnusable, ruleCreditNoteCapUnreadable,
	ruleCreditNoteOverCap,
}

// originalFacts is what a note's checks read about its original.
type originalFacts struct {
	Found          bool
	Number         string
	DocumentType   submission.DocumentType
	Status         Status
	IRN            *string
	IssueDate      *time.Time
	Total          *string // ::text
	CreditedBefore string  // CreditedSoFarTx, scale 2
}

func isNote(inv Invoice) bool { return false }

// readOriginalFactsTx: lock adds FOR UPDATE on the original; excludeID is the note under check.
// A nil originalID returns Found=false and runs no query.
func readOriginalFactsTx(ctx context.Context, tx pgx.Tx, originalID *string, excludeID string, lock bool) (originalFacts, error) {
	return originalFacts{}, nil
}

// locksOriginal is true for credit notes only: a debit note has no cap (D3).
func locksOriginal(inv Invoice) bool { return false }

// noteViolations is pure and returns nil for a commercial invoice.
func noteViolations(inv Invoice, f originalFacts) []Violation { return nil }

// withoutNoteChecks drops the noteCheckKeys from vs.
func withoutNoteChecks(vs []Violation) []Violation { return vs }
