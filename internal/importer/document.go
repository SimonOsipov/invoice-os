// document.go: the settled-extraction read (EXTR-06-01, task-761). See
// .ralph/EXTR-06-finalized.md, "The settled-extraction input type".
//
// SettledExtraction is a Mode A stub for red-spec authoring: signature only, no query, no
// logic. The real implementation (two db.WithinRequestTenantTx statements) lands when this
// subtask's tests go from RED to GREEN.
package importer

import "context"

// extractedField is one rank-0 extraction_field_results row: the decided reading.
// Alternatives (candidate_rank >= 1) are never read here -- a human resolves those on the
// review screen (EXTR-15/EXTR-16), not the writer.
type extractedField struct {
	Name   string
	Value  *string // NULL for an unreadable/missing field
	Reason *string // extraction_field_results.reason_code; NULL when the field is clean
}

// SettledExtraction is the newest succeeded extraction job for one document, plus that job's
// decided readings. Fields is never nil.
type SettledExtraction struct {
	JobID    string
	Filename string // documents.filename, "" when the row carries none
	Fields   []extractedField
}

// SettledExtraction is unimplemented (EXTR-06-01 Mode A stub).
func (s *Store) SettledExtraction(ctx context.Context, documentID string) (SettledExtraction, error) {
	return SettledExtraction{}, nil
}
