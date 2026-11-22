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
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

var (
	ErrNotPermitted        = errors.New("erp: only an admin can manage ERP connections")
	ErrUnknownSystem       = errors.New("erp: unknown system")
	ErrAlreadyConnected    = errors.New("erp: already connected")
	ErrAlreadyDisconnected = errors.New("erp: already disconnected")
	ErrNoSuchRecord        = errors.New("erp: no such client or connection")
)

// Connection is one client's link to one ERP system; it carries no grant field.
type Connection struct {
	ID          string     `json:"id"`
	EntityID    string     `json:"entity_id"`
	System      string     `json:"system"`
	ExternalKey *string    `json:"external_key"`
	Status      string     `json:"status"`
	LastSyncAt  *time.Time `json:"last_sync_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ConnectInput names the system; ConnectionID, when set, re-authorises that connection.
type ConnectInput struct{ System, ConnectionID string }

// Store reads and writes erp_connections and erp_grants as invoice_app; every method is one request transaction.
type Store struct {
	pool    *pgxpool.Pool
	reg     Registry
	secrets SecretStore // nil on portfolio: only the worker touches secrets
	q       *queue.Client

	newGrantID  func() string // nil: uuid.NewString; a test seam
	secretScope string        // the slot on a preview or the nightly, else empty
}

// WithSecretScope sets the scope of the secret names Complete creates (SecretName).
func (s *Store) WithSecretScope(scope string) *Store {
	s.secretScope = scope
	return s
}

func NewStore(pool *pgxpool.Pool, reg Registry, secrets SecretStore, q *queue.Client) *Store {
	return &Store{pool: pool, reg: reg, secrets: secrets, q: q}
}

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

// connectTTL is how long a consent link lives.
const connectTTL = "10 minutes"

// Connect starts a consent: it finds or creates the connection (the client's one keyless pending row,
// or the one named by in.ConnectionID), replaces its states with a fresh single-use one and returns
// the connector's consent URL. created is true for a new row.
func (s *Store) Connect(ctx context.Context, entityID string, in ConnectInput) (Connection, string, bool, error) {
	var conn Connection
	var consentURL string
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

		var err error
		if in.ConnectionID != "" {
			err = reauthorise(ctx, tx, entityID, in, &conn)
		} else {
			created, err = keylessPending(ctx, tx, caller.TenantID, entityID, in.System, &conn)
		}
		if err != nil {
			return err
		}

		// A newer consent link replaces the older one.
		if _, err := tx.Exec(ctx, `DELETE FROM erp_oauth_states WHERE connection_id = $1`, conn.ID); err != nil {
			return err
		}
		state, hash := NewState(caller.TenantID)
		if _, err := tx.Exec(ctx,
			`INSERT INTO erp_oauth_states (tenant_id, state_hash, connection_id, user_id, expires_at)
			 VALUES ($1, $2, $3, $4, now() + $5::interval)`,
			caller.TenantID, hash, conn.ID, caller.Subject, connectTTL); err != nil {
			return err
		}
		res, err := connector.Authorize(ctx, AuthorizeRequest{State: state})
		if err != nil {
			return err
		}
		consentURL = res.ConsentURL
		if created {
			return audit.Record(ctx, tx, caller.Subject, "erp.connection.created", map[string]any{"system": conn.System, "connection_id": conn.ID})
		}
		return nil
	})
	if err != nil {
		return Connection{}, "", false, err
	}
	return conn, consentURL, created, nil
}

// keylessPending returns the client's one keyless pending row for the system, inserting it when absent.
func keylessPending(ctx context.Context, tx pgx.Tx, tenantID, entityID, system string, conn *Connection) (created bool, err error) {
	err = scanConnection(tx.QueryRow(ctx,
		`INSERT INTO erp_connections (tenant_id, entity_id, system, status) VALUES ($1, $2, $3, 'pending')
		 ON CONFLICT (tenant_id, entity_id, system) WHERE external_key IS NULL AND status = 'pending' DO NOTHING
		 RETURNING `+connectionCols, tenantID, entityID, system), conn)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	// The conflicting row is committed (the insert waited for it); FOR UPDATE serialises the two connects.
	err = scanConnection(tx.QueryRow(ctx,
		`SELECT `+connectionCols+` FROM erp_connections
		 WHERE entity_id = $1 AND system = $2 AND external_key IS NULL AND status = 'pending' FOR UPDATE`,
		entityID, system), conn)
	return false, err
}

// reauthorise sets the client's non-active connection of that system pending, keeping its grant.
func reauthorise(ctx context.Context, tx pgx.Tx, entityID string, in ConnectInput, conn *Connection) error {
	if _, err := uuid.Parse(in.ConnectionID); err != nil {
		return ErrNoSuchRecord
	}
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM erp_connections WHERE id = $1 AND entity_id = $2 AND system = $3 FOR UPDATE`,
		in.ConnectionID, entityID, in.System).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchRecord
		}
		return err
	}
	if status == "active" {
		return ErrAlreadyConnected
	}
	return scanConnection(tx.QueryRow(ctx,
		`UPDATE erp_connections SET status = 'pending', updated_at = now() WHERE id = $1 RETURNING `+connectionCols,
		in.ConnectionID), conn)
}

// Disconnect keeps the row, clears its grant and, once no connection uses the grant, deletes it and enqueues its secret delete.
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
			return ErrNoSuchRecord
		}
		var status string
		var grantID *string
		if err := tx.QueryRow(ctx,
			`SELECT status, grant_id FROM erp_connections WHERE id = $1 AND entity_id = $2 FOR UPDATE`,
			connectionID, entityID).Scan(&status, &grantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoSuchRecord
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
			if err := releaseGrant(ctx, tx, s.q, *grantID, connectionID); err != nil {
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

// requireClient finds the client under the seam's RLS client scope; anything else is ErrNoSuchRecord.
func requireClient(ctx context.Context, tx pgx.Tx, entityID string) error {
	if _, err := uuid.Parse(entityID); err != nil {
		return ErrNoSuchRecord
	}
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM business_entities WHERE id = $1`, entityID).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchRecord
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
