// aimerge.go: AC-3's per-field decision table between the engine's Reconcile output and the
// AI's checked reading, plus the arithmetic pass on a subtotal/total the merge decides.
package extraction

import (
	"slices"
	"strings"
)

// mergeAI decides each header field from engine (Reconcile's output) and answer (askAI's
// output), per AC-3's table, then runs checkMergedAmounts on any subtotal/total it decided.
// Same length and order as engine; only HeaderFields rows with a non-blank answer can change.
func mergeAI(engine []FieldResult, answer map[string]string, pages []TokenPage, lines []DocLine) []FieldResult {
	out := slices.Clone(engine)
	var moved []int

	for i := range out {
		name := out[i].Name
		if !slices.Contains(HeaderFields, name) {
			continue
		}
		raw, ok := answer[name]
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		got, checked := checkAI(name, raw, pages)

		switch out[i].Reason {
		case ReasonNone:
			if !checked || collapse(*out[i].Value) == collapse(got.Value) {
				continue
			}
			v := got.Value
			out[i].Reason = ReasonAmbiguous
			out[i].Alternatives = []Field{{Name: name, Value: &v, Region: got.Region, Reason: ReasonNone}}

		case ReasonAmbiguous:
			if !checked {
				continue
			}
			readings := append([]Field{out[i].Field}, out[i].Alternatives...)
			match := -1
			for j, r := range readings {
				if collapse(*r.Value) == collapse(got.Value) {
					match = j
					break
				}
			}
			switch {
			case match >= 0:
				v := got.Value
				out[i].Value = &v
				out[i].Region = readings[match].Region
				out[i].Reason = ReasonNone
				out[i].Alternatives = []Field{}
				moved = append(moved, i)
			case len(readings) >= maxCandidatesPerField:
				// full: no room for a ninth reading
			default:
				v := got.Value
				alts := make([]Field, len(out[i].Alternatives), len(out[i].Alternatives)+1)
				copy(alts, out[i].Alternatives)
				out[i].Alternatives = append(alts, Field{Name: name, Value: &v, Region: got.Region, Reason: ReasonNone})
			}

		case ReasonMissing:
			if checked {
				v := got.Value
				out[i].Value = &v
				out[i].Region = got.Region
				out[i].Reason = ReasonNone
				out[i].Alternatives = []Field{}
				moved = append(moved, i)
			} else {
				t := strings.TrimSpace(raw)
				out[i].Value = nil
				out[i].Region = nil
				out[i].Reason = ReasonUnreadable
				out[i].Alternatives = []Field{{Name: name, Value: &t, Region: nil, Reason: ReasonNone}}
			}
		}
	}

	checkMergedAmounts(out, moved, lines)
	return out
}

// checkMergedAmounts re-runs Reconcile's own arithmetic on a subtotal/total mergeAI itself
// decided (moved): Reconcile checked only its own candidates, never an AI-sourced value.
// Subtotal first -- an amount it flags here is not evidence for the total check that follows.
func checkMergedAmounts(out []FieldResult, moved []int, lines []DocLine) {
	for _, i := range moved {
		if out[i].Name != "subtotal" {
			continue
		}
		A, ok := parseMoney(out[i].Value)
		if !ok {
			continue
		}
		_, lineSum, have, _ := reconcileLines(lines)
		if have && exceedsTolerance(lineSum.Sub(A).Abs()) {
			out[i].Reason = ReasonInconsistent
		}
	}
	for _, i := range moved {
		if out[i].Name != totalField {
			continue
		}
		A, ok := parseMoney(out[i].Value)
		if !ok {
			continue
		}
		sub, subOK := decidedMoney(out, "subtotal")
		vat, vatOK := decidedMoney(out, "vat")
		if subOK && vatOK && exceedsTolerance(sub.Add(vat).Sub(A).Abs()) {
			out[i].Reason = ReasonInconsistent
		}
	}
}
