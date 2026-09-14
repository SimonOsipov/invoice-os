package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// SavedMapping is the mapping a client's latest completed import used for one column layout.
type SavedMapping struct {
	Mapping map[string]string `json:"mapping"`
	SavedAt time.Time         `json:"saved_at"`
}

// columnSignature keys a decoded header: exact, ordered, case-sensitive, like the SPA's
// columnSignature. A digest, because a wide header overruns a btree tuple.
func columnSignature(header []string) string {
	b, _ := json.Marshal(header) // []string always marshals.
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SaveMapping upserts the row for (caller's tenant, entityID, columnSignature(header)).
// 23503/22P02 -> ErrValidation, mirroring CreateBatch's mapping.
// ceiling: a header holding a NUL byte cannot be stored in jsonb (22P05); that save fails and is logged.
func (s *Store) SaveMapping(ctx context.Context, entityID string, header []string, mapping map[string]string) error {
	payload, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	signature := columnSignature(header)

	return db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		identity, _ := auth.IdentityFromContext(ctx)

		if _, err := tx.Exec(ctx,
			`INSERT INTO import_mappings (tenant_id, entity_id, column_signature, mapping)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (tenant_id, entity_id, column_signature)
			 DO UPDATE SET mapping = EXCLUDED.mapping, saved_at = now()`,
			identity.TenantID, entityID, signature, payload,
		); err != nil {
			switch pgCode(err) {
			case "23503", "22P02":
				return ErrValidation
			}
			return err
		}
		return nil
	})
}

// SavedMapping returns the row for (entityID, columnSignature(header)) under RLS, or nil.
//
//	SELECT mapping, saved_at FROM import_mappings WHERE entity_id = $1 AND column_signature = $2
//
// pgx.ErrNoRows -> (nil, nil); 22P02 -> ErrValidation (GetBatch's mapping, store.go:286-293).
func (s *Store) SavedMapping(ctx context.Context, entityID string, header []string) (*SavedMapping, error) {
	signature := columnSignature(header)

	var rawMapping []byte
	var savedAt time.Time
	txErr := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT mapping, saved_at FROM import_mappings WHERE entity_id = $1 AND column_signature = $2`,
			entityID, signature,
		).Scan(&rawMapping, &savedAt)
	})
	if txErr != nil {
		if errors.Is(txErr, pgx.ErrNoRows) {
			return nil, nil
		}
		if pgCode(txErr) == "22P02" {
			return nil, ErrValidation
		}
		return nil, txErr
	}

	var mapping map[string]string
	if err := json.Unmarshal(rawMapping, &mapping); err != nil {
		return nil, err
	}
	return &SavedMapping{Mapping: mapping, SavedAt: savedAt}, nil
}
