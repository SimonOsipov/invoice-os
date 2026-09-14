package extraction_test

// Outside corpus_ and wild_ on purpose: no corpus ratchet and no endtoend require-list counts these.
const (
	fxLearnedTypedTotal     = "learned_typed_total.pdf"
	fxLearnedTypedTotalTwin = "learned_typed_total_twin.pdf"
)

// Tier-1 reads the amount as vat and leaves total missing; a rule learned from one document's
// amount fills the other's (TestLearnedTypedTotal_TheAmountTokenTeachesARuleTheTwinReads).
func fxBuildLearnedTypedTotal(invoiceNo, issueDate, supplier, amount string) []byte {
	return fxNairaTextPage(true,
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: " + invoiceNo},
		fxLine{12, 72, 672, "Invoice Date: " + issueDate},
		fxLine{12, 72, 650, "Supplier: " + supplier},
		fxLine{12, 72, 630, "Currency: NGN"},
		fxLine{12, 72, 240, "Paid: " + fxNaira + "0.00"},
		fxLine{12, 72, 204, "Total"},
		fxLine{12, 150, 204, "VAT inclusive"},
		fxLine{12, 300, 204, fxNaira + amount},
	)
}
