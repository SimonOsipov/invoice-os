package gateway

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LinkAddress maps a confirm token to the masked address of its account.
type LinkAddress func(ctx context.Context, token string) (masked string, found bool, err error)

// PoolLinkAddress is a STUB for the red tests: it looks nothing up until SECAUD-26-02 lands.
func PoolLinkAddress(pool *pgxpool.Pool) LinkAddress { return nil }
