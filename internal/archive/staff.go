// staff.go: STUB for the red tests; the executor replaces every body (Design "Go shape").
package archive

import (
	"context"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
)

// ErrTenantNotFound: the path tenant is malformed or not in staff_read_tenants.
var ErrTenantNotFound = errors.New("archive: tenant not found")

var errStaffNotImplemented = errors.New("not implemented")

// StaffEntity is one company of a tenant, as the staff list shows it.
type StaffEntity struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	TIN  *string `json:"tin"`
}

func (s *Store) withinStaffTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	return errStaffNotImplemented
}

func (s *Store) StaffEntities(ctx context.Context, tenantID string) ([]StaffEntity, error) {
	return nil, errStaffNotImplemented
}

func (s *Store) StaffPreview(ctx context.Context, tenantID string, r Request) (Preview, error) {
	return Preview{}, errStaffNotImplemented
}

func (s *Store) StaffAssemble(ctx context.Context, tenantID string, r Request, w io.Writer, onStart func(filename string)) error {
	return errStaffNotImplemented
}
