// vocabulary.go: the field vocabulary. Resolve (EXTR-04-07) and Tier1Rules (EXTR-04-08) both
// read it, so neither owns it.
package extraction

import "github.com/SimonOsipov/invoice-os/internal/invoicefields"

// HeaderFields is the field vocabulary in a fixed order -- invoices column names (D-4).
// EXTR-05 reads it to know which fields got no candidate at all.
var HeaderFields = invoicefields.ExtractHeaderKeys()

// LineRoles is one line's cells in emit order: reading order, left to right.
var LineRoles = invoicefields.ExtractLineKeys()
