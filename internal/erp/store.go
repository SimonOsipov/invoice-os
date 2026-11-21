package erp

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var (
	ErrNotPermitted        = errors.New("erp: only an admin can manage ERP connections")
	ErrUnknownSystem       = errors.New("erp: unknown system")
	ErrUnknownExternalKey  = errors.New("erp: unknown external key")
	ErrAlreadyConnected    = errors.New("erp: already connected")
	ErrAlreadyDisconnected = errors.New("erp: already disconnected")
	ErrSecretStoreMissing  = errors.New("erp: no secret store to hold a grant secret")
)

// Connection is one client's link to one ERP system; it carries no grant field.
type Connection struct {
	ID          string
	EntityID    string
	System      string
	ExternalKey *string
	Status      string
	LastSyncAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ConnectInput struct{ System, ExternalKey string }

// Store reads and writes erp_connections and erp_grants as invoice_app; every method is one request transaction.
type Store struct {
	pool *pgxpool.Pool
	reg  Registry
}

func NewStore(pool *pgxpool.Pool, reg Registry) *Store { return &Store{pool: pool, reg: reg} }

const connectionCols = `id, entity_id, system, external_key, status, last_sync_at, created_at, updated_at`

var readOnly = pgx.TxOptions{AccessMode: pgx.ReadOnly}

func scanConnection(row pgx.Row, c *Connection) error {
	return row.Scan(&c.ID, &c.EntityID, &c.System, &c.ExternalKey, &c.Status, &c.LastSyncAt, &c.CreatedAt, &c.UpdatedAt)
}

// Gate is the admin check alone, so a handler can refuse before it reads a body.
func (s *Store) Gate(ctx context.Context) error {
	return db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		id, _ := auth.IdentityFromContext(ctx)
		return requireAdmin(ctx, tx, id.Subject)
	})
}

// List has no role check: any active member who can see the client lists it.
func (s *Store) List(ctx context.Context, entityID string) ([]Connection, error) {
	out := []Connection{}
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, readOnly, func(tx pgx.Tx) error {
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+connectionCols+` FROM erp_connections WHERE entity_id = $1 ORDER BY created_at, id`, entityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Connection
			if err := scanConnection(rows, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Connect creates the connection, or re-authorises a non-active one; created is true for a new row.
func (s *Store) Connect(ctx context.Context, entityID string, in ConnectInput) (Connection, bool, error) {
	var conn Connection
	var created bool
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		connector, ok := s.reg[in.System]
		if !ok {
			return ErrUnknownSystem
		}
		res, err := connector.Authorize(ctx, AuthorizeRequest{ExternalKey: in.ExternalKey})
		if err != nil {
			if errors.Is(err, ErrUnknownScope) {
				return ErrUnknownExternalKey
			}
			return err
		}
		if res.Grant == nil {
			return errors.New("erp: connector returned no grant")
		}
		if len(res.Grant.Secret) > 0 {
			return ErrSecretStoreMissing
		}

		var existingID, status string
		var oldGrant *string
		err = tx.QueryRow(ctx,
			`SELECT id, status, grant_id FROM erp_connections
			 WHERE entity_id = $1 AND system = $2 AND external_key = $3 FOR UPDATE`,
			entityID, in.System, res.ExternalKey).Scan(&existingID, &status, &oldGrant)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists && status == "active" {
			return ErrAlreadyConnected
		}

		var grantID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO erp_grants (tenant_id, system, kind, expires_at) VALUES ($1, $2, $3, $4) RETURNING id`,
			caller.TenantID, in.System, string(res.Grant.Kind), res.Grant.ExpiresAt).Scan(&grantID); err != nil {
			return err
		}

		if !exists {
			err := scanConnection(tx.QueryRow(ctx,
				`INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id)
				 VALUES ($1, $2, $3, $4, 'active', $5) RETURNING `+connectionCols,
				caller.TenantID, entityID, in.System, res.ExternalKey, grantID), &conn)
			if err != nil {
				if pgCode(err) == "23505" {
					return ErrAlreadyConnected
				}
				return err
			}
			created = true
			if err := audit.Record(ctx, tx, caller.Subject, "erp.connection.created", map[string]any{"system": conn.System, "connection_id": conn.ID}); err != nil {
				return err
			}
			return audit.Record(ctx, tx, caller.Subject, "erp.connection.authorised", map[string]any{"system": conn.System, "connection_id": conn.ID})
		}

		if err := scanConnection(tx.QueryRow(ctx,
			`UPDATE erp_connections SET status = 'active', grant_id = $2, updated_at = now()
			 WHERE id = $1 RETURNING `+connectionCols, existingID, grantID), &conn); err != nil {
			return err
		}
		if oldGrant != nil {
			if err := releaseGrant(ctx, tx, *oldGrant); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, caller.Subject, "erp.connection.authorised", map[string]any{"system": conn.System, "connection_id": conn.ID})
	})
	if err != nil {
		return Connection{}, false, err
	}
	return conn, created, nil
}

// Disconnect keeps the row, clears its grant and deletes the grant once no connection uses it.
func (s *Store) Disconnect(ctx context.Context, entityID, connectionID string) (Connection, error) {
	var conn Connection
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := requireClient(ctx, tx, entityID); err != nil {
			return err
		}
		if _, err := uuid.Parse(connectionID); err != nil {
			return ErrNotFound
		}
		var status string
		var grantID *string
		if err := tx.QueryRow(ctx,
			`SELECT status, grant_id FROM erp_connections WHERE id = $1 AND entity_id = $2 FOR UPDATE`,
			connectionID, entityID).Scan(&status, &grantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == "disconnected" {
			return ErrAlreadyDisconnected
		}
		if err := scanConnection(tx.QueryRow(ctx,
			`UPDATE erp_connections SET status = 'disconnected', grant_id = NULL, updated_at = now()
			 WHERE id = $1 RETURNING `+connectionCols, connectionID), &conn); err != nil {
			return err
		}
		if grantID != nil {
			if err := releaseGrant(ctx, tx, *grantID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, caller.Subject, "erp.connection.revoked", map[string]any{"system": conn.System, "connection_id": conn.ID})
	})
	if err != nil {
		return Connection{}, err
	}
	return conn, nil
}

func (s *Store) Systems() []string { return s.reg.Systems() }

// releaseGrant runs after the caller cleared its connection's grant_id. It serialises on an
// advisory lock because invoice_app cannot row-lock erp_grants.
func releaseGrant(ctx context.Context, tx pgx.Tx, grantID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_grant:' || $1::text, 0))`, grantID); err != nil {
		return err
	}
	var secretRef *string
	if err := tx.QueryRow(ctx, `SELECT secret_ref FROM erp_grants WHERE id = $1`, grantID).Scan(&secretRef); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a sibling already deleted it
		}
		return err
	}
	if secretRef != nil {
		return ErrSecretStoreMissing
	}
	_, err := tx.Exec(ctx,
		`DELETE FROM erp_grants WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM erp_connections WHERE grant_id = $1)`, grantID)
	return err
}

// requireClient finds the client under the seam's RLS client scope; anything else is ErrNotFound.
func requireClient(ctx context.Context, tx pgx.Tx, entityID string) error {
	if _, err := uuid.Parse(entityID); err != nil {
		return ErrNotFound
	}
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM business_entities WHERE id = $1`, entityID).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// requireAdmin refuses any caller that is not an active admin. First statement in the tx.
func requireAdmin(ctx context.Context, tx pgx.Tx, subject string) error {
	var role string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships WHERE user_id = $1 AND status = 'active'`, subject,
	).Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted
		}
		return err
	}
	if role != "admin" {
		return ErrNotPermitted
	}
	return nil
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
