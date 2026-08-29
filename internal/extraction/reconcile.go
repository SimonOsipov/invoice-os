// reconcile.go: EXTR-05-03/04. The decision stage between Resolve's candidates and the store --
// decides which reading to trust, which to flag, and why. Pure: no database, no clock, no
// network, no goroutine, no map on the path (resolve.go's own posture, inherited here).
package extraction

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

// reconcileTolerance is one kobo. The printed subtotal is itself a rounded number, so a
// one-minor-unit gap is a rounding artifact and not a disagreement. Deliberately looser than
// internal/validation's 0.005, which checks stored numbers a human or a spreadsheet supplied.
const reconcileTolerance = "0.01"

// Reconcile decides which candidate to trust per field and checks the document's own
// arithmetic. Not yet implemented -- EXTR-05-03/04 land the logic.
func Reconcile(in Input) []FieldResult {
	panic("not implemented")
}
