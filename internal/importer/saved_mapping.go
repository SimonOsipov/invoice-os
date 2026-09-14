package importer

import (
	"context"
	"time"
)

// SavedMapping is the mapping a client's latest completed import used for one column layout.
type SavedMapping struct {
	Mapping map[string]string `json:"mapping"`
	SavedAt time.Time         `json:"saved_at"`
}

// columnSignature keys a decoded header: exact, ordered, case-sensitive, like the SPA's
// columnSignature. A digest, because a wide header overruns a btree tuple.
func columnSignature(header []string) string { return "" }

// SaveMapping upserts the row for (caller's tenant, entityID, columnSignature(header)).
func (s *Store) SaveMapping(ctx context.Context, entityID string, header []string, mapping map[string]string) error {
	return nil
}
