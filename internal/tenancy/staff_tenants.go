package tenancy

// stub: SUPP-02-03 executor replaces. Zero values only, so the red tests compile and fail on their assertions.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type StaffTenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) StaffTenants(context.Context) ([]StaffTenant, error) {
	return nil, errors.New("not implemented")
}

func StaffTenantsHandler(func(context.Context) ([]StaffTenant, error), *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}

func staffReadError(err error) error { return err }
