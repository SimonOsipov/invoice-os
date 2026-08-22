package archive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// bodyWriter is satisfied later by the streaming zip assembler (a later subtask) --
// selectExchange only defines the seam here. WriteBody creates one ZIP entry named
// name and writes body verbatim (D-6: bodies are files, never CSV cells).
type bodyWriter interface {
	WriteBody(name string, body []byte) error
}

// exchangeCSVHeader: left nil (Mode A, RED) -- the pinned 18-column header lands with
// the implementation. Bodies are file-name cells (request_body_file/
// response_body_file), never body bytes (D-6).
var exchangeCSVHeader []string

// selectExchangeSQL: left empty (Mode A, RED) -- must never JOIN against invoices (see
// TestExchangeSQL_ContainsNoJoinAgainstInvoices). The pinned query lands with the
// implementation.
const selectExchangeSQL = ""

// selectExchange writes exchange.csv, re-scrubbing both header maps through
// submission.ScrubHeaders on the way OUT regardless of what write time stored (D-7),
// so AC 8 holds even for a row planted directly against the table. Stub for
// Stage 2.5 (Mode A).
func selectExchange(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter, bw bodyWriter) error {
	return errors.New("archive: selectExchange not implemented")
}

// emptyIntIfNil mirrors emptyIfNil for http_status/latency_ms. Stub for Stage 2.5
// (Mode A) -- returns an obviously-wrong marker so any exact-match assertion fails.
func emptyIntIfNil(n *int) string {
	return "__STUB_NOT_IMPLEMENTED__"
}
