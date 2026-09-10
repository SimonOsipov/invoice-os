// vocabulary_scope_test.go: EXTR-26-01, T-01.4. External package: Resolve, Reconcile,
// Tier1Rules and HeaderFields are all exported, so no internal hook is needed.
package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// vsAdvisoryPage is one synthetic header page carrying all ten HeaderFields, with buyerLabel as
// the only thing that ever varies between two calls. The buyer name sits BELOW its label, the
// arrangement T-01.4 exists to prove the loosened rule resolves exactly like the exact one.
func vsAdvisoryPage(buyerLabel string) []extraction.TokenPage {
	return rvPage(
		rvTok("Invoice No: INV-2601", 0.10, 0.04, 0.40, 0.06),
		rvTok("Issue Date: 2026-09-10", 0.10, 0.14, 0.40, 0.16),
		rvTok("Supplier: Lagos Advisory Partners", 0.10, 0.24, 0.40, 0.26),
		rvTok("Supplier TIN: 99999999-1300", 0.10, 0.34, 0.40, 0.36),
		rvTok(buyerLabel, 0.10, 0.44, 0.20, 0.46),
		rvTok("Enugu Ceramics Limited", 0.10, 0.47, 0.32, 0.49),
		rvTok("Buyer TIN: 99999999-1302", 0.10, 0.57, 0.40, 0.59),
		rvTok("Currency: NGN", 0.10, 0.67, 0.40, 0.69),
		rvTok("Subtotal: 3,187,420.00", 0.10, 0.77, 0.40, 0.79),
		rvTok("VAT: 239,056.50", 0.10, 0.87, 0.40, 0.89),
		rvTok("Total: 3,426,476.50", 0.10, 0.97, 0.40, 0.99),
	)
}

func vsDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// T-01.4: two pages identical except "Bill to" vs "Billed to" must decide every header field
// the same way. RED today: the loosened page resolves buyer_name missing, so the two sides
// differ.
func TestReconcile_ALoosenedPartyLabelDecidesExactlyAsTheExactOneDoes(t *testing.T) {
	rules := rvGeneric()

	exact := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsAdvisoryPage("Bill to"), rules)})
	loosened := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(vsAdvisoryPage("Billed to"), rules)})

	// Non-vacuity: both sides must actually have produced the ten header fields, or the
	// per-field loop below would range over nothing and pass trivially.
	if len(exact) < len(extraction.HeaderFields) || len(loosened) < len(extraction.HeaderFields) {
		t.Fatalf("Reconcile returned %d (exact) and %d (loosened) result(s), want at least %d each", len(exact), len(loosened), len(extraction.HeaderFields))
	}

	// Positive control: the exact spelling must itself resolve buyer_name to the printed name,
	// or an accidental equality below (both sides missing) would prove nothing.
	var control *extraction.FieldResult
	for i := range exact[:len(extraction.HeaderFields)] {
		if exact[i].Name == "buyer_name" {
			control = &exact[i]
		}
	}
	const wantName = "Enugu Ceramics Limited"
	if control == nil || control.Reason != extraction.ReasonNone || control.Value == nil || *control.Value != wantName {
		t.Fatalf("the exact-spelling page's buyer_name = %+v, want ReasonNone / %q -- the fixture must resolve on its own before the loosened page can be compared against it", control, wantName)
	}

	for i, field := range extraction.HeaderFields {
		a, b := exact[i], loosened[i]
		if a.Name != field || b.Name != field {
			t.Fatalf("result[%d].Name = %q / %q, want %q in both -- Reconcile did not return HeaderFields order", i, a.Name, b.Name, field)
		}
		if a.Reason != b.Reason {
			t.Errorf("%s: Reason = %q (exact) vs %q (loosened), want equal", field, a.Reason, b.Reason)
		}
		if (a.Value == nil) != (b.Value == nil) || (a.Value != nil && *a.Value != *b.Value) {
			t.Errorf("%s: Value = %s (exact) vs %s (loosened), want equal", field, vsDeref(a.Value), vsDeref(b.Value))
		}
		if len(a.Alternatives) != len(b.Alternatives) {
			t.Errorf("%s: %d alternative(s) (exact) vs %d (loosened), want equal", field, len(a.Alternatives), len(b.Alternatives))
		}
	}
}
