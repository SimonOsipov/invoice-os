// Package ubl renders a stored invoice as a UBL 2.1 Invoice document.
//
// The output is structurally well-formed UBL declaring the PEPPOL BIS 3.0 profile and
// faithfully reflecting stored invoice content. It is NOT a validator-certified document:
// EN 16931 mandates seller and buyer postal address + country code, and nothing in this
// system stores them -- see [followup-bis-party-address-gap]. No comment, error string or
// test name here may claim otherwise [ubl-conformance-is-structural-not-certified].
package ubl

import (
	"errors"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ErrIncomplete is returned when Missing reports content the document cannot be built
// without. Callers branch with errors.Is.
var ErrIncomplete = errors.New("ubl: invoice is missing content the document needs")

// errNotImplemented is the BUG-04-01 Stage-3 seam. Delete it with the real body.
var errNotImplemented = errors.New("ubl: not implemented")

// Render returns the UBL document for c, or ErrIncomplete when Missing(c) is non-empty.
func Render(c submission.Canonical) ([]byte, error) {
	_ = c
	return nil, errNotImplemented
}

// Missing lists the content the document cannot be constructed without, in a fixed order.
// nil when nothing is missing. A constructability gate, not a conformance oracle.
func Missing(c submission.Canonical) []string {
	_ = c
	return nil
}
