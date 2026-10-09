package audit

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Red-phase stub so staff_test.go compiles before RecordStaff exists. The executor deletes this
// whole file when it adds RecordStaff to audit.go; the redeclaration error is the reminder.
func RecordStaff(_ context.Context, _ pgx.Tx, _ uuid.UUID, _ uuid.UUID, _ string, _ any) error {
	return errors.New("audit.RecordStaff: not implemented")
}
