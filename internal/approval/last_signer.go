package approval

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Compile-only stubs so the APSC-13-01 red tests build; the executor replaces every body.

func StaffedRoleKeysTx(ctx context.Context, tx pgx.Tx, subject string) ([]string, error) {
	return nil, nil
}

func RefuseUnsignedWaitingRolesTx(ctx context.Context, tx pgx.Tx, keys []string) error {
	return nil
}

// WriteRoleInUse stands in for the rename of writeRoleInUse; the executor renames and drops this.
func WriteRoleInUse(w http.ResponseWriter, err error) { writeRoleInUse(w, err) }
