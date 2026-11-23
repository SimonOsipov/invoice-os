package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

// ErrUnknownWebhookSystem is a system with no registered connector, or one that reports no Webhooks.
var ErrUnknownWebhookSystem = errors.New("erp: no webhook receiver for this system")

// ErrWebhookUnverified is a webhook the connector refused.
var ErrWebhookUnverified = errors.New("erp: webhook not verified")

// webhookEnvelopeLimit bounds the gateway's forwarded envelope.
const webhookEnvelopeLimit = 128 << 10

// Webhooks turns a verified vendor webhook into one erp.pull per active target connection.
type Webhooks struct {
	Pool     *pgxpool.Pool
	River    *river.Client[pgx.Tx]
	Registry Registry
}

// maxWebhookScopes caps the distinct company keys one webhook may look up.
const maxWebhookScopes = 50

type webhookTarget struct{ tenant, connection string }

// Receive returns how many targets it enqueued or found already in flight.
// The tenant of each pull comes from erp_webhook_targets, never from ctx.
func (w *Webhooks) Receive(ctx context.Context, system string, h http.Header, body []byte) (int, error) {
	c, ok := w.Registry[system]
	if !ok || !c.Capabilities().Webhooks {
		return 0, ErrUnknownWebhookSystem
	}
	hints, err := c.VerifyWebhook(ctx, h, body)
	if err != nil {
		return 0, ErrWebhookUnverified
	}
	seen := map[string]bool{}
	var scopes []string
	for _, hint := range hints {
		if hint.Scope == "" || seen[hint.Scope] {
			continue
		}
		// ceiling: scopes past maxWebhookScopes wait for the pull sweep; revisit if a vendor batches more companies
		if len(scopes) == maxWebhookScopes {
			break
		}
		seen[hint.Scope] = true
		scopes = append(scopes, hint.Scope)
	}
	if len(scopes) == 0 {
		return 0, nil
	}
	var targets []webhookTarget
	err = db.WithinTenantTx(ctx, w.Pool, uuid.Nil.String(), func(tx pgx.Tx) error {
		for _, scope := range scopes {
			rows, err := tx.Query(ctx, `SELECT tenant_id::text, connection_id::text FROM public.erp_webhook_targets($1, $2)`, system, scope)
			if err != nil {
				return err
			}
			ts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (webhookTarget, error) {
				var t webhookTarget
				return t, r.Scan(&t.tenant, &t.connection)
			})
			if err != nil {
				return err
			}
			targets = append(targets, ts...)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("erp: webhook target lookup: %w", err)
	}
	n := 0
	for _, t := range targets {
		err := db.WithinTenantTx(ctx, w.Pool, t.tenant, func(tx pgx.Tx) error {
			if locked, err := db.WorkspaceLocked(ctx, tx); err != nil || locked {
				return err
			}
			if _, err := w.River.InsertTx(ctx, tx, PullArgs{TenantID: t.tenant, ConnectionID: t.connection}, nil); err != nil {
				return err
			}
			n++
			return nil
		})
		if err != nil {
			return n, fmt.Errorf("erp: enqueue webhook pull: %w", err)
		}
	}
	return n, nil
}

type webhookEnvelope struct {
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

// WebhookHandler serves POST /internal/erp/webhooks/{system} for the gateway.
// It logs counts and the system only: no body, header, key or error text.
func WebhookHandler(receive func(context.Context, string, http.Header, []byte) (int, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var env webhookEnvelope
		if err := jsonbody.Decode(w, r, webhookEnvelopeLimit, &env); err != nil {
			if jsonbody.TooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		system := r.PathValue("system")
		n, err := receive(r.Context(), system, http.Header(env.Headers), env.Body)
		switch {
		case errors.Is(err, ErrUnknownWebhookSystem):
			writeError(w, http.StatusNotFound, "unknown system")
		case errors.Is(err, ErrWebhookUnverified):
			writeError(w, http.StatusUnauthorized, "webhook not verified")
		case err != nil:
			log.ErrorContext(r.Context(), "erp: receive webhook", slog.String("system", system))
			writeError(w, http.StatusInternalServerError, "internal server error")
		default:
			log.InfoContext(r.Context(), "erp: webhook received", slog.String("system", system), slog.Int("targets", n))
			writeJSON(w, http.StatusOK, map[string]string{"outcome": "accepted"})
		}
	}
}
