// party.go -- EXTR-22. Which side of the invoice a token belongs to.
package extraction

// Party is the owner of a token: the party whose heading last introduced it.
type Party int

const (
	PartyUnknown Party = iota // no party heading precedes the token on its page
	PartySupplier
	PartyBuyer
)

// partyField is the TIN field p owns. PartyUnknown falls back to the supplier because that is
// what a party-less TIN label filled before the partition existed, which is what makes the
// change monotone (TestTier1_ABareTINLabelBindsToTheHeadingBeforeIt).
func partyField(p Party) string {
	if p == PartyBuyer {
		return "buyer_tin"
	}
	return "supplier_tin"
}

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
	found := PartyUnknown
	matched := 0
	for _, m := range anchorLabelMatchers {
		p := PartyUnknown
		for _, h := range partyHeadings {
			if h.labelID == m.ID {
				p = h.party
			}
		}
		if p == PartyUnknown || !m.RE.MatchString(text) {
			continue
		}
		matched++
		found = p
	}
	if matched != 1 {
		return PartyUnknown
	}
	return found
}

// partyOrder is one Party per token, in the page's own token order: the party of the nearest
// party heading at or before that token. One page in, so a heading cannot reach the next one.
func partyOrder(page TokenPage) []Party {
	out := make([]Party, len(page.Tokens))
	current := PartyUnknown
	for i, tok := range page.Tokens {
		// Only a heading moves the block. An unconditional assignment would clear it at every
		// ordinary token and leave the partition holding headings alone.
		if p := partyHeading(tok.Text); p != PartyUnknown {
			current = p
		}
		out[i] = current
	}
	return out
}
