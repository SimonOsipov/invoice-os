package archive

import (
	"context"
	"io"

	"github.com/jackc/pgx/v5"
)

// assemble is buildBundle followed by Close, for tests that drive one open tx.
func assemble(ctx context.Context, tx pgx.Tx, r Request, w io.Writer, o assembleOpts) error {
	bw, err := buildBundle(ctx, tx, r, w, o)
	if err != nil {
		return err
	}
	return bw.Close()
}
