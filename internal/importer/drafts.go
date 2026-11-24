package importer

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// DraftCreator creates one draft invoice; *invoice.Store satisfies it.
type DraftCreator interface {
	Create(ctx context.Context, in invoice.CreateInput) (invoice.Invoice, error)
}

// DraftValidator runs the created set through the validation gate in one call.
type DraftValidator interface {
	ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error)
}

// DraftResult is index-aligned with the inputs.
type DraftResult struct {
	Invoice     *invoice.Invoice // nil when refused or not reached
	Err         error            // the domain refusal, e.g. invoice.ErrDuplicateNumber
	Message     string           // domainCreateErrorMessage(Err)
	Unvalidated bool
	Violations  []invoice.Violation
}

type DraftOutcome struct {
	Results                []DraftResult
	RuleSetVersion         *int // nil when nothing was created
	InvoicesClean          int
	InvoicesWithViolations int
}

// CreateDrafts creates each input, validates the created set in one gate call, then calls
// finalize once: "failed" on an operational fault (whose error it returns), else "completed".
func CreateDrafts(ctx context.Context, c DraftCreator, v DraftValidator, ins []invoice.CreateInput,
	finalize func(ctx context.Context, out DraftOutcome, status string) error) (DraftOutcome, error) {
	out := DraftOutcome{Results: make([]DraftResult, len(ins))}
	var created []int
	for i, in := range ins {
		inv, err := c.Create(ctx, in)
		if err == nil {
			out.Results[i].Invoice = &inv
			created = append(created, i)
			continue
		}
		msg, ok := domainCreateErrorMessage(err)
		if !ok {
			// The create error is the real fault; a finalize failure is secondary.
			_ = finalize(ctx, out, "failed")
			return out, err
		}
		out.Results[i].Err = err
		out.Results[i].Message = msg
	}

	// Nothing created: the gate would answer version 0 having evaluated nothing.
	if len(created) > 0 {
		invs := make([]invoice.Invoice, len(created))
		for k, i := range created {
			invs[k] = *out.Results[i].Invoice
		}
		// A validator error is never a domain refusal: it is a fault, not bad input.
		bo, err := v.ValidateBatch(ctx, invs)
		if err != nil {
			_ = finalize(ctx, out, "failed")
			return out, err
		}
		version := bo.RuleSetVersion
		out.RuleSetVersion = &version
		out.InvoicesClean = bo.Clean
		out.InvoicesWithViolations = bo.WithViolations
		unvalidated := make(map[string]bool, len(bo.Unvalidated))
		for _, id := range bo.Unvalidated {
			unvalidated[id] = true
		}
		for _, i := range created {
			id := out.Results[i].Invoice.ID
			out.Results[i].Unvalidated = unvalidated[id]
			if !unvalidated[id] {
				out.Results[i].Violations = bo.ByID[id]
			}
		}
	}

	return out, finalize(ctx, out, "completed")
}
