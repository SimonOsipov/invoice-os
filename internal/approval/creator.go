package approval

// Compile stubs for the APPR-17-01 red tests. The executor replaces every body.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrCreatorRefused = errors.New("approval: the creator cannot approve their own invoice")

// CreatorProbe is one invoice and the role key its pending step waits on (nil: none).
type CreatorProbe struct {
	InvoiceID string
	RoleKey   *string
}

type CreatorStanding struct {
	IsCreator       bool
	OtherApprover   bool
	OtherRoleHolder bool
}

// newCreatorRefused builds the error decisionStatusForErr maps to 403 with reason.
func newCreatorRefused(reason string) error { return ErrCreatorRefused }

func CreatorStandingTx(ctx context.Context, tx pgx.Tx, subject string, probes []CreatorProbe) (map[string]CreatorStanding, error) {
	return map[string]CreatorStanding{}, nil
}
