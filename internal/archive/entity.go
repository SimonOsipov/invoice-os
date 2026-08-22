package archive

import (
	"context"
	"errors"

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

// errRedNotImplemented: AUDIT-05-03 Mode A stub sentinel. Stage 3 replaces every
// function that returns it with real query logic.
var errRedNotImplemented = errors.New("archive: not implemented (AUDIT-05-03 RED)")

// selectEntity: RED stub (Mode A). Real body -- normalizeEntityID then
// SELECT id, name, tin FROM business_entities WHERE id = $1, mapping pgx.ErrNoRows to
// ErrEntityNotFound -- lands in Stage 3.
func selectEntity(ctx context.Context, tx pgx.Tx, entityID string) (Entity, error) {
	return Entity{}, errRedNotImplemented
}

// normalizeEntityID: RED stub (Mode A). Real body -- uuid.Parse(raw) then .String() --
// canonicalizes at the DB boundary so a urn:uuid:-form id (which uuid.Parse accepts but
// Postgres's uuid_in rejects, SQLSTATE 22P02) still reaches its row. Lands in Stage 3.
func normalizeEntityID(raw string) (string, error) {
	return "", errRedNotImplemented
}
