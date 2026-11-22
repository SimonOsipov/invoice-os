package erp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// ErrUnknownWebhookSystem is a system with no registered connector, or one that reports no Webhooks.
var ErrUnknownWebhookSystem = errors.New("erp: no webhook receiver for this system")

// ErrWebhookUnverified is a webhook the connector refused.
var ErrWebhookUnverified = errors.New("erp: webhook not verified")

// webhookEnvelopeLimit bounds the gateway's forwarded envelope (D16, D33).
const webhookEnvelopeLimit = 128 << 10

// Webhooks turns a verified vendor webhook into one erp.pull per active target connection.
type Webhooks struct {
	Pool     *pgxpool.Pool
	River    *river.Client[pgx.Tx]
	Registry Registry
	Logger   *slog.Logger
}

// Receive returns how many targets it enqueued or found already in flight.
// Stub: ERPF-06-03 replaces it.
func (w *Webhooks) Receive(_ context.Context, _ string, _ http.Header, _ []byte) (int, error) {
	return 0, errors.New("not implemented")
}

// WebhookHandler serves POST /internal/erp/webhooks/{system} for the gateway.
// Stub: ERPF-06-03 replaces it.
func WebhookHandler(_ func(context.Context, string, http.Header, []byte) (int, error), _ *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
