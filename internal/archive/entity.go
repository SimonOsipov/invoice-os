package archive

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Entity is business_entities' name+TIN subset the bundle needs (D-8: TIN nil = column NULL).
type Entity struct {
	ID   string
	Name string
	TIN  *string
}

// ErrEntityNotFound: no visible row for the given id, whether absent or another
// tenant's (AC-2: the two cases are indistinguishable to the caller).
var ErrEntityNotFound = errors.New("archive: entity not found")

// selectEntity reads business_entities under RLS; a row invisible to the caller's
// tenant scans zero rows exactly like a nonexistent id (AC-2).
func selectEntity(ctx context.Context, tx pgx.Tx, entityID string) (Entity, error) {
	canonical, err := normalizeEntityID(entityID)
	if err != nil {
		return Entity{}, err
	}
	var e Entity
	err = tx.QueryRow(ctx, `SELECT id, name, tin FROM business_entities WHERE id = $1`, canonical).Scan(&e.ID, &e.Name, &e.TIN)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entity{}, ErrEntityNotFound
	}
	if err != nil {
		return Entity{}, fmt.Errorf("archive: select entity: %w", err)
	}
	return e, nil
}

// normalizeEntityID re-parses entityID (already uuid.Parse-validated by parseRequest)
// to canonical form. Postgres's uuid_in rejects urn:uuid:... (SQLSTATE 22P02) even
// though uuid.Parse accepts it and Request.EntityID keeps it raw on purpose.
func normalizeEntityID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("archive: entity id %q: %w", raw, err)
	}
	return id.String(), nil
}
