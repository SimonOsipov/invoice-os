package approval

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Stub so can_sign_test.go compiles; the real body lands with the APSC-14-01 implementation.
func unsignableReasonsTx(ctx context.Context, tx pgx.Tx, probes []CreatorProbe) ([]string, error) {
	return nil, nil
}
