// party.go -- EXTR-22. Which side of the invoice a token belongs to.
package extraction

// Party is the owner of a token: the party whose heading last introduced it.
type Party int

const (
	PartyUnknown Party = iota // no party heading precedes the token on its page
	PartySupplier
	PartyBuyer
)

// partyHeadings pairs a party with the anchorLexicon id that names it. Read by id, never
// re-spelled: a forked pattern drifts from the fingerprint silently (anchorPattern's own rule).
var partyHeadings = []struct {
	labelID string
	party   Party
}{
	{"supplier_name", PartySupplier},
	{"buyer_name", PartyBuyer},
}

// partyHeading is which party a token's text heads, or PartyUnknown. A token both vocabularies
// match heads neither: a heading naming both parties names neither.
func partyHeading(text string) Party {
	_ = text
	return PartyUnknown
}

// partyOrder is one Party per token, in the page's own token order: the party of the nearest
// party heading at or before that token. One page in, so a heading cannot reach the next one.
func partyOrder(page TokenPage) []Party {
	_ = page
	return nil
}
