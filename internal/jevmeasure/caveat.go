package jevmeasure

import (
	"bytes"
	"fmt"
	"strings"
)

// Caveat ids (§4.3). Each is a true number or fact that misleads once transcribed into the
// vault unless it travels with its own explanation -- the same failure shape as A52's
// provisional-wording marker.
const (
	caveatWordingProvisional       = "wording.provisional"
	caveatDoctypeTitleAnnounced    = "doctype.title_announced"
	caveatValuePlantedWrongs       = "value.planted_wrongs"
	caveatMappingDeclaredHeaderRow = "mapping.declared_header_row"
	caveatMappingAutoFewWrongs     = "mapping.auto_few_wrongs"
	caveatMappingAcceptedList      = "mapping.accepted_list"
	caveatMappingTwoHalves         = "mapping.two_halves"
)

// CaveatRegistry is every caveat the rendered report must carry, keyed by id. An entry added
// here and not wired into writeCaveats/writeWording fails
// TestJevReport_EveryRequiredCaveatIsRendered rather than escaping unnoticed (mirrors
// WordingRegistry).
func CaveatRegistry() map[string]string {
	return map[string]string{
		caveatWordingProvisional: "Provisional wording. The mapping and document-type text below was authored in CHECK-01-02, not taken " +
			"from the TypeSafe console session that produced the chosen thresholds. Core AC-8 asks for the vendor's " +
			"wording verbatim and no text below satisfies it. Reconcile against the live run before quoting this as settled.",
		caveatDoctypeTitleAnnounced: "All seven synthetic non-invoices announce their own type in a 24pt title line, and " +
			"four add an explicit disclaimer sentence. This score measures keyword presence, not document understanding: " +
			"it is an optimistic bound and is NOT evidence the check discriminates in production. Real non-invoices are " +
			"harder — a proforma headed TAX INVOICE, a receipt carrying an invoice number and a total, a statement laid " +
			"out as a line-item table.",
		caveatValuePlantedWrongs: "Every asked value row taken from the corpus labels right; the %d planted variants are " +
			"the only wrong rows in this check. The wrong-catch rate below is measured against deliberately corrupted " +
			"values, not against production error.",
		caveatMappingDeclaredHeaderRow: "AUTO is computed from each layout's DECLARED header row (layouts.json " +
			"`columns`), not from a detected one. Measured consequence: 56 placements against 50 from the first " +
			"physical row, differing only on title_01, title_02, title_05 and title_06, and identical on the 40 " +
			"layouts whose header_row is 1. Nothing deployed sets header_row > 1, so production never meets this case.",
		caveatMappingAutoFewWrongs: "Only %d of the AUTO placements are wrong (measured at design time: 5 of 56, all on " +
			"the `vat` field, where the alias table takes a VAT rate column for a VAT amount column). The wrong-caught " +
			"column below therefore moves in steps of roughly 20 percentage points.",
		caveatMappingAcceptedList: "A field's answer key is a LIST of accepted headers, not one answer. A placement " +
			"naming any member scores right. Two layouts (sw_zoho, sw_quickbooks_import) key line_description to two " +
			"headers because those exports ship two description columns.",
		caveatMappingTwoHalves: "mapping_check_auto and mapping_check_ai are the two halves of ONE check, measured on " +
			"mutually exclusive production paths. Their rates must never be summed or averaged.",
	}
}

// caveatOrder is each per-check caveat's fixed render order (§4.2's mockup): declared-header-row
// before auto-few-wrongs inside mapping_check_auto, accepted-list before two-halves inside
// mapping_check_ai. wording.provisional is not here -- it renders once, in writeWording.
var caveatOrder = []string{
	caveatDoctypeTitleAnnounced, caveatValuePlantedWrongs,
	caveatMappingDeclaredHeaderRow, caveatMappingAutoFewWrongs,
	caveatMappingAcceptedList, caveatMappingTwoHalves,
}

// caveatGate reports whether id applies to sec (§4.3's gate column). Gating is not optional: an
// ungated caveat text is exactly how a future edit reds
// TestReport_TheValueSectionHasNoConfidenceColumn for an unrelated reason.
func caveatGate(id string, sec checkSection, checksPresent map[string]bool) bool {
	switch id {
	case caveatDoctypeTitleAnnounced:
		// "the section's outcomes carry a non-empty Answer" is exactly what Confusion != nil
		// already means (buildConfusion).
		return sec.Confusion != nil
	case caveatValuePlantedWrongs:
		return sec.VariantCount > 0
	case caveatMappingDeclaredHeaderRow:
		return sec.Check == "mapping_check_auto"
	case caveatMappingAutoFewWrongs:
		return sec.Check == "mapping_check_auto" && sec.Wrong < 10
	case caveatMappingAcceptedList:
		return strings.HasPrefix(sec.Check, "mapping_check_")
	case caveatMappingTwoHalves:
		return sec.Check == "mapping_check_ai" && checksPresent["mapping_check_auto"] && checksPresent["mapping_check_ai"]
	default:
		return false
	}
}

// writeCaveats renders every caveat whose gate sec satisfies, in caveatOrder, right after the N
// block (documents/asked/not-asked/reasons) and before the SmallN/Degraded lines and the
// comparator (§4.2's mockup).
func writeCaveats(w *bytes.Buffer, sec checkSection, checksPresent map[string]bool) {
	reg := CaveatRegistry()
	for _, id := range caveatOrder {
		if caveatGate(id, sec, checksPresent) {
			fmt.Fprintf(w, "%s\n\n", caveatText(id, sec, reg))
		}
	}
}

// caveatText fills value.planted_wrongs and mapping.auto_few_wrongs with this run's own measured
// counts (VariantCount, Wrong) rather than the leftover literal N; every other id renders as
// registered.
func caveatText(id string, sec checkSection, reg map[string]string) string {
	switch id {
	case caveatValuePlantedWrongs:
		return fmt.Sprintf(reg[id], sec.VariantCount)
	case caveatMappingAutoFewWrongs:
		return fmt.Sprintf(reg[id], sec.Wrong)
	default:
		return reg[id]
	}
}
