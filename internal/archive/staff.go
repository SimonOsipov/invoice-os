// staff.go: the staff reads of one tenant's evidence bundle, preview and company list.
package archive

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ErrTenantNotFound: the path tenant is malformed or not in staff_read_tenants.
var ErrTenantNotFound = errors.New("archive: tenant not found")

// staffManifestActor is the manifest's generated_by for a staff download.
var staffManifestActor = manifestActor{Name: audit.SupportActor, Kind: "raw"}

// StaffEntity is one company of a tenant, as the staff list shows it.
type StaffEntity struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	TIN  *string `json:"tin"`
}

// withinStaffTenant checks the path tenant through staff_read_tenants in WithinStaffTx, then runs fn in that
// tenant's READ ONLY transaction. A malformed id answers ErrTenantNotFound before any tenant transaction.
func (s *Store) withinStaffTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	staff, err := db.StaffActor(ctx)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return ErrTenantNotFound
	}
	var found bool
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.staff_read_tenants($1) WHERE id = $2)`, staff, id).Scan(&found)
		return db.StaffReadError("archive: staff tenant check", qerr)
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrTenantNotFound
	}
	return db.WithinTenantTxOpts(ctx, s.pool, id.String(), bundleTxOptions, fn)
}

// StaffEntities lists the tenant's companies by name, then id.
func (s *Store) StaffEntities(ctx context.Context, tenantID string) ([]StaffEntity, error) {
	out := []StaffEntity{}
	err := s.withinStaffTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, name, tin FROM business_entities ORDER BY name, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e StaffEntity
			if err := rows.Scan(&e.ID, &e.Name, &e.TIN); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []StaffEntity{}
	}
	return out, nil
}

// StaffPreview is Preview for the path tenant.
func (s *Store) StaffPreview(ctx context.Context, tenantID string, r Request) (Preview, error) {
	var p Preview
	err := s.withinStaffTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		p, err = preview(ctx, tx, r, previewOpts{maxInvoices: s.maxInvoices})
		return err
	})
	if err != nil {
		return Preview{}, err
	}
	return p, nil
}

// StaffAssemble is Assemble for the path tenant, with generated_by "ASComply support".
func (s *Store) StaffAssemble(ctx context.Context, tenantID string, r Request, w io.Writer, onStart func(filename string)) error {
	return s.withinStaffTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return assemble(ctx, tx, r, w, assembleOpts{
			tenantID:    uuid.MustParse(tenantID).String(), // withinStaffTenant parsed it: spell it canonically
			maxInvoices: s.maxInvoices,
			now:         time.Now(),
			onStart:     onStart,
			generatedBy: &staffManifestActor,
		})
	})
}
