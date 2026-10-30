package invoice

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

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

func isNote(inv Invoice) bool {
	return inv.DocumentType == submission.CreditNote || inv.DocumentType == submission.DebitNote
}

// readOriginalFactsTx: lock adds FOR UPDATE on the original; excludeID is the note under check.
// A nil originalID returns Found=false and runs no query.
func readOriginalFactsTx(ctx context.Context, tx pgx.Tx, originalID *string, excludeID string, lock bool) (originalFacts, error) {
	if originalID == nil {
		return originalFacts{}, nil
	}
	q := `SELECT invoice_number, document_type, status, irn, issue_date, total::text
	        FROM invoices WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	f := originalFacts{Found: true}
	err := tx.QueryRow(ctx, q, *originalID).Scan(
		&f.Number, &f.DocumentType, &f.Status, &f.IRN, &f.IssueDate, &f.Total)
	if errors.Is(err, pgx.ErrNoRows) {
		return originalFacts{}, nil
	}
	if err != nil {
		return originalFacts{}, err
	}
	if f.CreditedBefore, err = CreditedSoFarTx(ctx, tx, *originalID, excludeID); err != nil {
		return originalFacts{}, err
	}
	return f, nil
}

// locksOriginal is true for credit notes only: a debit note has no cap (D3).
func locksOriginal(inv Invoice) bool { return inv.DocumentType == submission.CreditNote }

func noteErr(key, msg, path string) Violation {
	return Violation{RuleKey: key, Severity: "error", Message: msg, Path: path}
}

// noteViolations is pure and returns nil for a commercial invoice.
func noteViolations(inv Invoice, f originalFacts) []Violation {
	if !isNote(inv) {
		return nil
	}
	if inv.OriginalInvoiceID == nil {
		return []Violation{noteErr(ruleNoteOriginalMissing, "This note names no original invoice.", "")}
	}
	if !f.Found {
		return []Violation{noteErr(ruleNoteOriginalMissing,
			fmt.Sprintf("Original invoice %s was not found.", *inv.OriginalInvoiceID), "")}
	}
	var vs []Violation
	if f.DocumentType != submission.CommercialInvoice {
		vs = append(vs, noteErr(ruleNoteOriginalNotCommercial,
			fmt.Sprintf("Original invoice %s is not a commercial invoice.", f.Number), ""))
	}
	if f.Status != StatusAccepted {
		vs = append(vs, noteErr(ruleNoteOriginalNotAccepted,
			fmt.Sprintf("Original invoice %s is not accepted by NRS.", f.Number), ""))
	}
	if f.IRN == nil || strings.TrimSpace(*f.IRN) == "" {
		vs = append(vs, noteErr(ruleNoteOriginalNoIRN,
			fmt.Sprintf("Original invoice %s has no IRN.", f.Number), ""))
	}
	if inv.DocumentType != submission.CreditNote {
		return vs
	}

	// Postgres sorts NaN above every number, so the cap is compared here, not in SQL.
	total, ok := parseMoney(inv.Total)
	if !ok || total.IsNegative() {
		return append(vs, noteErr(ruleCreditNoteTotalUnusable,
			"Credit note total is missing or not a valid non-negative amount.", "total"))
	}
	orig, okO := parseMoney(f.Total)
	credited, okC := parseMoney(&f.CreditedBefore)
	if !okO || !okC {
		return append(vs, noteErr(ruleCreditNoteCapUnreadable,
			fmt.Sprintf("Cannot read the total or credited amount of original invoice %s.", f.Number), ""))
	}
	if credited.Add(total).GreaterThan(orig) {
		left := decimal.Max(decimal.Zero, orig.Sub(credited))
		vs = append(vs, noteErr(ruleCreditNoteOverCap,
			fmt.Sprintf("Credit note total %s exceeds the %s still creditable on original invoice %s.",
				total.StringFixed(2), left.StringFixed(2), f.Number), "total"))
	}
	return vs
}

func parseMoney(s *string) (decimal.Decimal, bool) {
	if s == nil {
		return decimal.Decimal{}, false
	}
	d, err := decimal.NewFromString(*s)
	return d, err == nil
}

// withoutNoteChecks drops the noteCheckKeys from vs.
func withoutNoteChecks(vs []Violation) []Violation {
	return slices.DeleteFunc(slices.Clone(vs), func(v Violation) bool {
		return slices.Contains(noteCheckKeys, v.RuleKey)
	})
}
