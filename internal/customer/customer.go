// Package customer is the workspace buyer directory: CUST's `customers` record.
package customer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrValidation = errors.New("customer: validation")
	ErrNotFound   = errors.New("customer: not found")

	errNotImplemented = errors.New("customer: not implemented")
)

// Customer is a customers row. edited_fields is not on the wire.
type Customer struct {
	ID            string    `json:"id"`
	TIN           string    `json:"tin"`
	Name          *string   `json:"name"`
	ContactPerson *string   `json:"contact_person"`
	BuyerType     *string   `json:"buyer_type"`
	Email         *string   `json:"email"`
	Telephone     *string   `json:"telephone"`
	Street        *string   `json:"street"`
	City          *string   `json:"city"`
	PostalZone    *string   `json:"postal_zone"`
	Country       *string   `json:"country"`
	State         *string   `json:"state"`
	LGA           *string   `json:"lga"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListFilter is the Store.List query. TIN matches exactly after a trim.
type ListFilter struct {
	TIN    string
	Limit  int
	Offset int
}

// UpdateInput is the Store.Update argument: nil leaves a column unchanged.
type UpdateInput struct {
	Name          *string
	ContactPerson *string
	BuyerType     *string
	Email         *string
	Telephone     *string
	Street        *string
	City          *string
	PostalZone    *string
	Country       *string
	State         *string
	LGA           *string
}

// Store reads and corrects customers as the invoice_app role.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]Customer, int, error) {
	return nil, 0, errNotImplemented
}

func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Customer, error) {
	return Customer{}, errNotImplemented
}

// LookupByTIN runs in the caller's tenant tx; nil, nil when no record matches.
func LookupByTIN(ctx context.Context, tx pgx.Tx, tin string) (*Customer, error) {
	return nil, errNotImplemented
}

func ListHandler(list func(ctx context.Context, f ListFilter) ([]Customer, int, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, errNotImplemented.Error())
	}
}

func UpdateHandler(update func(ctx context.Context, id string, in UpdateInput) (Customer, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, errNotImplemented.Error())
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
