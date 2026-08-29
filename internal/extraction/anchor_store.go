// anchor_store.go: the learned-rule read. Stage 3 implements AnchorRulesFor; this stub keeps
// EXTR-04-05's RED tests failing on assertion, never on a compile error.
package extraction

import (
	"context"
	"errors"
)

// AnchorRule is one stored rule: its row identity plus its decoded body.
type AnchorRule struct {
	ID    string
	Field string
	Rule  Rule
}

// AnchorRulesFor returns the tenant's anchor rules for one layout fingerprint, newest first,
// never a nil slice. Not yet implemented.
func (s *Store) AnchorRulesFor(ctx context.Context, tenantID, fingerprint string) ([]AnchorRule, error) {
	return []AnchorRule{}, errors.New("AnchorRulesFor: not implemented")
}
