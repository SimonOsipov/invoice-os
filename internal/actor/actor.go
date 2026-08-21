// Package actor resolves an audit_log actor into a display Label. Consumed by
// AUDIT-04, AUDIT-05 and the history endpoint. Stdlib only -- internal/audit,
// internal/invoice, internal/approval and internal/tenancy all import it.
package actor

// Kind classifies how a Label's Text was produced.
type Kind string

const (
	KindSystem Kind = "system" // literal stored actor "system"
	KindPerson Kind = "person" // resolved from a membership row
	KindRaw    Kind = "raw"    // free text, or a subject nothing can name
)

// Label.Text is never empty for a non-empty subject: KindRaw carries the
// stored actor verbatim.
type Label struct {
	Text string
	Kind Kind
}

// Name applies the Q6 ladder: display_name -> email -> subject.
//
// D-31: a non-nil pointer to "" is absent and falls through at every rung,
// exactly like nil. This deliberately diverges from internal/approval's
// holderName (read_model.go:421), which stops on a non-nil "" -- pinned by
// TestHolderName_EmptyStringDisplayNameDoesNotFallThrough. Do not reconcile
// the two; see TestActorName_DivergesFromHolderNameDeliberately.
//
// The subject is returned byte-for-byte: never parsed, normalised or
// truncated. Classifying it (KindSystem, shape gates) belongs to Resolve.
func Name(displayName, email *string, subject string) Label {
	// "" falls through like nil (D-31): TestActorName_EmptyStringFallsThrough.
	if displayName != nil && *displayName != "" {
		return Label{Text: *displayName, Kind: KindPerson}
	}
	if email != nil && *email != "" {
		return Label{Text: *email, Kind: KindPerson}
	}
	return Label{Text: subject, Kind: KindRaw}
}
