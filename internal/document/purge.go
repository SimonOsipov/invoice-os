package document

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DeleteTenantObjects removes every object under one tenant's key prefix and
// returns how many it deleted. On an error it returns the count deleted so far.
func DeleteTenantObjects(ctx context.Context, store ObjectStore, tenantID string) (int, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, fmt.Errorf("document: purge objects: tenant id: %w", err)
	}
	// Writers store the lower-case uuid text, so the prefix must too.
	prefix := "tenants/" + id.String() + "/"

	deleted := make(map[string]struct{})
	total := 0
	for {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			return total, err
		}
		if len(keys) == 0 {
			return total, nil
		}
		for _, k := range keys {
			if _, again := deleted[k]; again {
				return total, fmt.Errorf("document: purge objects: %s still lists after delete", k)
			}
		}
		if err := store.DeleteMany(ctx, keys); err != nil {
			return total, err
		}
		for _, k := range keys {
			deleted[k] = struct{}{}
		}
		total += len(keys)
	}
}
