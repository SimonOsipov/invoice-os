// reconcile.go: EXTR-05-03/04. The decision stage between Resolve's candidates and the store --
// decides which reading to trust, which to flag, and why. Pure: no database, no clock, no
// network, no goroutine, no map on the path (resolve.go's own posture, inherited here).
package extraction

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Input is everything the decision stage reads. No document, no database, no clock.
type Input struct {
	Candidates []Candidate // Resolve's output, already grouped and ordered
	Lines      []DocLine   // LineItems' output; nil when the reader found no table
	Entity     Entity      // the signed-in business entity, for the Q11 supplier check (EXTR-05-05)
}

// Entity is the signed-in business_entities row as the supplier check reads it. TIN is the
// MBS wire spelling NNNNNNNN-NNNN; empty when the entity carries none of its own.
type Entity struct {
	TIN  string
	Name string
}

// FieldResult is one reconciled field: the decided reading plus the alternatives an
// ambiguous field keeps. The decided Field carries the reason code; an alternative never does.
type FieldResult struct {
	Field
	Alternatives []Field // never nil; non-empty only when Field.Reason is ReasonAmbiguous
}

// reconcileTolerance is one kobo, looser than internal/validation's 0.005: validation checks
// numbers a human supplied for a stored invoice, this checks numbers read off a page where the
// printed subtotal is itself rounded.
const reconcileTolerance = "0.01"

// exceedsTolerance reports whether diff (already non-negative) is a real disagreement rather
// than a rounding artifact. Re-parses reconcileTolerance so every comparison shares one source.
func exceedsTolerance(diff decimal.Decimal) bool {
	tol, err := decimal.NewFromString(reconcileTolerance)
	if err != nil {
		return true // an unparseable tolerance fails closed, never lets a gap through unmeasured
	}
	return diff.GreaterThan(tol)
}

// parseMoney reads one printed amount off the page. A nil or unparseable value is "not
// checked", never folded to zero -- the caller skips the comparison it would have fed.
func parseMoney(s *string) (decimal.Decimal, bool) {
	if s == nil {
		return decimal.Decimal{}, false
	}
	v, err := decimal.NewFromString(*s)
	return v, err == nil
}

// singleCandidate returns the one candidate for field, and false when there are zero or more
// than one -- ambiguity resolution is EXTR-05-04's.
func singleCandidate(cands []Candidate, field string) (Candidate, bool) {
	var found Candidate
	count := 0
	for _, c := range cands {
		if c.Field == field {
			found = c
			count++
		}
	}
	return found, count == 1
}

// decideField turns one candidate into its own decided, unflagged result.
func decideField(c Candidate) FieldResult {
	v := c.Value
	return FieldResult{
		Field:        Field{Name: c.Field, Value: &v, Region: c.Region, Reason: ReasonNone},
		Alternatives: []Field{},
	}
}

// Reconcile decides which candidate to trust per field and checks the document's own
// arithmetic. This pass covers only subtotal (0-or-1 candidates), the line_items block presence
// check, per-row arithmetic and the line-sum-vs-subtotal check; totality over HeaderFields,
// alternatives and reason precedence are EXTR-05-04's.
func Reconcile(in Input) []FieldResult {
	out := make([]FieldResult, 0, len(in.Candidates)+2+len(in.Lines))

	// Every other field carrying exactly one candidate is decided as-is; subtotal is handled
	// below because its reason can still be overridden by the line-sum check.
	var handled []string
	for _, c := range in.Candidates {
		if c.Field == "subtotal" || slicesContain(handled, c.Field) {
			continue
		}
		handled = append(handled, c.Field)
		if only, ok := singleCandidate(in.Candidates, c.Field); ok {
			out = append(out, decideField(only))
		}
	}

	subtotalCandidate, subtotalDecided := singleCandidate(in.Candidates, "subtotal")
	subtotal := FieldResult{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}}
	if subtotalDecided {
		subtotal = decideField(subtotalCandidate)
	}

	lineItems := FieldResult{Field: Field{Name: "line_items", Reason: ReasonNone}, Alternatives: []Field{}}
	if len(in.Lines) == 0 {
		lineItems.Reason = ReasonMissing
	}

	var lineSum decimal.Decimal
	haveLineTotal := false
	var rowFlags []FieldResult
	for _, line := range in.Lines {
		if total, ok := parseMoney(line.LineTotal); ok {
			lineSum = lineSum.Add(total)
			haveLineTotal = true
		}

		if line.Quantity == nil || line.UnitPrice == nil || line.LineTotal == nil {
			continue // a row missing any of the three is not arithmetic-checked
		}
		qty, qtyOK := parseMoney(line.Quantity)
		price, priceOK := parseMoney(line.UnitPrice)
		printed, printedOK := parseMoney(line.LineTotal)
		if !qtyOK || !priceOK || !printedOK {
			continue
		}
		if exceedsTolerance(qty.Mul(price).Sub(printed).Abs()) {
			rowFlags = append(rowFlags, FieldResult{
				Field: Field{
					Name:   fmt.Sprintf("line_items[%d].line_total", line.Index),
					Value:  line.LineTotal,
					Region: line.Region,
					Reason: ReasonInconsistent,
				},
				Alternatives: []Field{},
			})
		}
	}

	// The sum check runs only when a subtotal was decided and at least one line carries a total;
	// either absent skips the comparison rather than defaulting to a mismatch (D-19).
	if subtotalDecided && haveLineTotal {
		if printedSubtotal, err := decimal.NewFromString(subtotalCandidate.Value); err == nil {
			if exceedsTolerance(lineSum.Sub(printedSubtotal).Abs()) {
				subtotal.Reason = ReasonInconsistent
			}
		}
	}

	out = append(out, subtotal, lineItems)
	out = append(out, rowFlags...)
	return out
}

// slicesContain is a small linear lookup -- this path stays off maps (resolve.go's posture).
func slicesContain(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
