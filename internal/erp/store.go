package erp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Placeholder so the store tests compile and fail on their assertions; the real store replaces this file.

var (
	ErrNotPermitted        = errors.New("erp: only an admin can manage ERP connections")
	ErrUnknownSystem       = errors.New("erp: unknown system")
	ErrUnknownExternalKey  = errors.New("erp: unknown external key")
	ErrAlreadyConnected    = errors.New("erp: already connected")
	ErrAlreadyDisconnected = errors.New("erp: already disconnected")
	ErrSecretStoreMissing  = errors.New("erp: no secret store to hold a grant secret")

	errStoreNotBuilt = errors.New("erp: store not built")
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

type Store struct {
	pool *pgxpool.Pool
	reg  Registry
}

func NewStore(pool *pgxpool.Pool, reg Registry) *Store { return &Store{pool: pool, reg: reg} }

func (s *Store) Gate(context.Context) error { return errStoreNotBuilt }

func (s *Store) List(context.Context, string) ([]Connection, error) { return nil, errStoreNotBuilt }

func (s *Store) Connect(context.Context, string, ConnectInput) (Connection, bool, error) {
	return Connection{}, false, errStoreNotBuilt
}

func (s *Store) Disconnect(context.Context, string, string) (Connection, error) {
	return Connection{}, errStoreNotBuilt
}

func (s *Store) Systems() []string { return nil }
