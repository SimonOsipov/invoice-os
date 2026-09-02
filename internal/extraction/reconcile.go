// reconcile.go: EXTR-05-03/04. The decision stage between Resolve's candidates and the store --
// decides which reading to trust, which to flag, and why. Pure: no database, no clock, no
// network, no goroutine, no map on the path (resolve.go's own posture, inherited here).
package extraction

import (
	"slices"

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
	// No omitempty: a nil slice marshals to null; never nil here, non-empty only when
	// Field.Reason is ReasonAmbiguous.
	Alternatives []Field `json:"alternatives"`
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

// decideField picks the value for one HeaderFields member out of every candidate naming it. No
// candidate is ReasonMissing; the head candidate (compareCandidates order) decides the value,
// and every peer sharing its Tier and Distance is "equal standing" (D-14) -- deduped by value
// before counting (D-15), so two readings of the same value are one answer, not an ambiguity.
func decideField(cands []Candidate, field string) FieldResult {
	var peers []Candidate
	for _, c := range cands {
		if c.Field == field {
			peers = append(peers, c)
		}
	}
	if len(peers) == 0 {
		return FieldResult{Field: Field{Name: field, Reason: ReasonMissing}, Alternatives: []Field{}}
	}
	slices.SortFunc(peers, compareCandidates) // peers is a fresh slice; in.Candidates is untouched

	head := peers[0]
	var group []Candidate
	for _, c := range peers {
		if c.Tier == head.Tier && c.Distance == head.Distance {
			group = append(group, c)
		}
	}

	var deduped []Candidate
	for _, c := range group {
		dup := false
		for _, d := range deduped {
			if d.Value == c.Value {
				dup = true
				break
			}
		}
		if !dup {
			deduped = append(deduped, c)
		}
	}

	v := deduped[0].Value
	result := FieldResult{
		Field:        Field{Name: field, Value: &v, Region: deduped[0].Region, Reason: ReasonNone},
		Alternatives: []Field{},
	}
	if len(deduped) < 2 {
		return result
	}
	result.Reason = ReasonAmbiguous
	alts := make([]Field, 0, len(deduped)-1)
	for _, c := range deduped[1:] {
		altVal := c.Value
		alts = append(alts, Field{Name: field, Value: &altVal, Region: c.Region, Reason: ReasonNone})
	}
	result.Alternatives = alts
	return result
}

// reconcileLines decides the line_items block and its per-row flags. lineSum and haveLineTotal
// feed the subtotal-vs-lines check in Reconcile; haveLineTotal is also what the block's own
// reason turns on (D-21): a row present with no parseable total never ran the sum check, so it
// reads ReasonMissing, not the ReasonNone a genuinely reconciled block gets.
func reconcileLines(lines []DocLine) (block FieldResult, lineSum decimal.Decimal, haveLineTotal bool, rowFlags []FieldResult) {
	block = FieldResult{Field: Field{Name: "line_items", Reason: ReasonMissing}, Alternatives: []Field{}}

	for _, line := range lines {
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
			v := *line.LineTotal // copied: a caller mutating its DocLine must not reach an emitted row
			rowFlags = append(rowFlags, FieldResult{
				Field: Field{
					Name:   LineFieldName(line.Index, LineRoleLineTotal),
					Value:  &v,
					Region: line.Region,
					Reason: ReasonInconsistent,
				},
				Alternatives: []Field{},
			})
		}
	}

	if haveLineTotal {
		block.Reason = ReasonNone
	}
	return block, lineSum, haveLineTotal, rowFlags
}

// Reconcile decides which candidate to trust per field and checks the document's own
// arithmetic. Total over HeaderFields (Core AC 1): one FieldResult per member, in HeaderFields
// order, then the line_items block, then one row per populated line-item cell. A candidate
// naming a field outside HeaderFields is ignored.
func Reconcile(in Input) []FieldResult {
	out := make([]FieldResult, 0, len(HeaderFields)+1+len(in.Lines)*len(LineRoles))
	for _, field := range HeaderFields {
		out = append(out, decideField(in.Candidates, field))
	}

	lineItems, lineSum, haveLineTotal, rowFlags := reconcileLines(in.Lines)

	// The sum check may only tighten an already-clean subtotal (D-7): an ambiguous or missing
	// subtotal keeps its own reason rather than being rewritten to inconsistent.
	if haveLineTotal {
		for i := range out {
			if out[i].Name != "subtotal" || out[i].Reason != ReasonNone {
				continue
			}
			if printedSubtotal, ok := parseMoney(out[i].Value); ok {
				if exceedsTolerance(lineSum.Sub(printedSubtotal).Abs()) {
					out[i].Reason = ReasonInconsistent
				}
			}
			break
		}
	}

	// Q11's advisory supplier check: does the decided supplier reading match the signed-in
	// entity. Advisory only -- it writes nothing; store.go overwrites these fields on every
	// write regardless. Each field is gated independently, and only a ReasonNone field moves.
	if in.Entity.TIN != "" {
		flagIfInconsistent(out, "supplier_tin", func(v string) bool { return v == in.Entity.TIN })
	}
	if in.Entity.Name != "" {
		entityName := liNormalizeHeaderText(in.Entity.Name)
		flagIfInconsistent(out, "supplier_name", func(v string) bool { return liNormalizeHeaderText(v) == entityName })
	}

	out = append(out, lineItems)
	out = append(out, composeLineRows(LineItemResults(in.Lines), rowFlags)...)
	return out
}

// composeLineRows emits exactly one rank-0 row per (index, role): the value row, or the
// arithmetic flag where reconcileLines raised one for that same name. Reading order is kept, so
// a flagged total sits in its own line's run rather than trailing the block. Nested slice scans,
// not a lookup map: rows per document are few and reconcile.go stays off maps.
func composeLineRows(values, flags []FieldResult) []FieldResult {
	out := make([]FieldResult, 0, len(values)+len(flags))
	for _, v := range values {
		row := v
		for _, f := range flags {
			if f.Name == v.Name {
				row = f
				break
			}
		}
		out = append(out, row)
	}
	for _, f := range flags {
		if !rcNamed(values, f.Name) {
			out = append(out, f) // a flag with no value row to replace is never dropped
		}
	}
	return out
}

// rcNamed reports whether rows carries one named name.
func rcNamed(rows []FieldResult, name string) bool {
	for _, r := range rows {
		if r.Name == name {
			return true
		}
	}
	return false
}

// flagIfInconsistent sets ReasonInconsistent on the named field when it is decided
// (ReasonNone) and match reports it disagrees with the entity. A missing or ambiguous field
// is left alone (AC-6): the supplier check never overrides an earlier reason.
func flagIfInconsistent(out []FieldResult, name string, match func(string) bool) {
	for i := range out {
		if out[i].Name != name || out[i].Reason != ReasonNone {
			continue
		}
		if out[i].Value != nil && !match(*out[i].Value) {
			out[i].Reason = ReasonInconsistent
		}
		return
	}
}
