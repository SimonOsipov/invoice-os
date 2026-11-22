package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

// Outcome is the answer to a callback; the gateway renders one page per value.
type Outcome string

const (
	OutcomeConnected Outcome = "connected"
	OutcomeInvalid   Outcome = "invalid"
	OutcomeDenied    Outcome = "denied"
	OutcomeFailed    Outcome = "failed"
	OutcomeConflict  Outcome = "conflict"
	OutcomeEnded     Outcome = "ended"
)

// CallbackInput is what the vendor's redirect carried; none of it is ever logged.
type CallbackInput struct{ State, Code, Error string }

const (
	completeBudget     = 20 * time.Second // inside the gateway's 30 s call
	exchangeBudget     = 10 * time.Second
	inlineDeleteBudget = 10 * time.Second
	refreshLead        = 5 * time.Minute
	callbackBodyLimit  = 4 << 10
)

// errKeyTaken: another connection of the client already holds the vendor's company key.
var errKeyTaken = errors.New("erp: vendor key already connected")

// consent is the state's connection, read in the first transaction.
type consent struct{ connectionID, adminID, system string }

// Complete finishes a consent. tx1 consumes the single-use state and checks that it can still act; the
// exchange and the secret create hold no transaction; tx2 activates the connection. A failure after the
// secret exists deletes it inline unless tx2 turns out to have committed. An error comes back only when tx1 could not run, so the state is unspent.
func (s *Store) Complete(ctx context.Context, in CallbackInput) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, completeBudget)
	defer cancel()
	tenantID, hash, ok := ParseState(in.State)
	if !ok {
		return OutcomeInvalid, nil
	}

	var c consent
	var stop Outcome
	if err := db.WithinTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) (err error) {
		c, stop, err = consumeState(ctx, tx, hash, in.Error != "")
		return err
	}); err != nil {
		return "", fmt.Errorf("erp: consume callback state: %w", err)
	}
	if stop != "" {
		return stop, nil
	}

	connector, ok := s.reg[c.system]
	if !ok || s.secrets == nil {
		slog.WarnContext(ctx, "erp: callback has no connector or secret store", slog.String("system", c.system))
		return OutcomeFailed, nil
	}
	xctx, xcancel := context.WithTimeout(ctx, exchangeBudget)
	res, err := connector.Exchange(xctx, ExchangeRequest{Code: in.Code})
	xcancel()
	if err != nil { // the error may quote the vendor's reply, so only the system is logged
		slog.WarnContext(ctx, "erp: code exchange failed", slog.String("system", c.system))
		return OutcomeFailed, nil
	}

	grantID := uuid.NewString()
	if s.newGrantID != nil {
		grantID = s.newGrantID()
	}
	ref := SecretName(s.secretScope, grantID)
	if err := s.secrets.Create(ctx, ref, res.Grant.Secret); err != nil {
		slog.WarnContext(ctx, "erp: create grant secret failed", slog.String("system", c.system), slog.Any("err", err))
		return OutcomeFailed, nil
	}

	var final Outcome
	err = db.WithinTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) (err error) {
		final, err = s.activate(ctx, tx, tenantID, c, res, grantID, ref)
		return err
	})
	switch {
	case errors.Is(err, errKeyTaken):
		final = OutcomeConflict
	case err != nil:
		slog.WarnContext(ctx, "erp: activate connection failed", slog.String("system", c.system), slog.Any("err", err))
		if s.settleTx2Error(ctx, tenantID, c.connectionID, grantID, ref) {
			return OutcomeConnected, nil
		}
		return OutcomeFailed, nil
	}
	if final == OutcomeConnected {
		return final, nil
	}
	s.deleteUnused(ctx, ref)
	return final, nil
}

// consumeState spends the state and says whether the callback may go on: a non-empty Outcome stops it.
// The state is spent even when it stops at ended or denied.
func consumeState(ctx context.Context, tx pgx.Tx, hash []byte, denied bool) (consent, Outcome, error) {
	var c consent
	err := tx.QueryRow(ctx,
		`UPDATE erp_oauth_states SET used_at = now()
		 WHERE state_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING connection_id::text, user_id::text`, hash).Scan(&c.connectionID, &c.adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, OutcomeInvalid, nil
	}
	if err != nil {
		return c, "", err
	}
	if err := requireAdmin(ctx, tx, c.adminID); err != nil {
		if errors.Is(err, ErrNotPermitted) {
			return c, OutcomeEnded, nil
		}
		return c, "", err
	}
	if locked, err := db.WorkspaceLocked(ctx, tx); err != nil || locked {
		if err != nil {
			return c, "", err
		}
		return c, OutcomeEnded, nil
	}
	err = tx.QueryRow(ctx, `SELECT system FROM erp_connections WHERE id = $1 AND status = 'pending' FOR UPDATE`, c.connectionID).Scan(&c.system)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, OutcomeEnded, nil
	}
	if err != nil {
		return c, "", err
	}
	if denied {
		return c, OutcomeDenied, nil
	}
	return c, "", nil
}

// activate is tx2. A non-connected Outcome with a nil error commits nothing but the row lock.
func (s *Store) activate(ctx context.Context, tx pgx.Tx, tenantID string, c consent, res ExchangeResult, grantID, ref string) (Outcome, error) {
	if err := requireAdmin(ctx, tx, c.adminID); err != nil {
		if errors.Is(err, ErrNotPermitted) {
			return OutcomeEnded, nil
		}
		return "", err
	}
	if locked, err := db.WorkspaceLocked(ctx, tx); err != nil || locked {
		if err != nil {
			return "", err
		}
		return OutcomeEnded, nil
	}
	var key, oldGrant *string
	err := tx.QueryRow(ctx,
		`SELECT external_key, grant_id::text FROM erp_connections WHERE id = $1 AND status = 'pending' FOR UPDATE`,
		c.connectionID).Scan(&key, &oldGrant)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutcomeEnded, nil
	}
	if err != nil {
		return "", err
	}
	if key != nil && *key != res.ExternalKey { // a reconnect to another company
		return OutcomeConflict, nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO erp_grants (id, tenant_id, system, kind, secret_ref, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		grantID, tenantID, c.system, string(res.Grant.Kind), ref, res.Grant.ExpiresAt); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE erp_connections SET status = 'active', external_key = $2, grant_id = $3, updated_at = now() WHERE id = $1`,
		c.connectionID, res.ExternalKey, grantID); err != nil {
		if pgCode(err) == "23505" {
			return "", errKeyTaken
		}
		return "", err
	}
	if oldGrant != nil {
		if err := releaseGrant(ctx, tx, s.q, *oldGrant, c.connectionID); err != nil {
			return "", err
		}
	}
	if err := audit.Record(ctx, tx, c.adminID, "erp.connection.authorised", map[string]any{"system": c.system, "connection_id": c.connectionID}); err != nil {
		return "", err
	}
	if exp := res.Grant.ExpiresAt; exp != nil {
		if _, err := s.q.EnqueueTx(ctx, tx, tenantID, "erp_refresh:"+grantID+":1",
			RefreshArgs{TenantID: tenantID, GrantID: grantID, Version: FirstGrantVersion}, &river.InsertOpts{ScheduledAt: exp.Add(-refreshLead)}); err != nil {
			return "", err
		}
	}
	return OutcomeConnected, nil
}

// deleteUnused removes the secret of a callback that did not connect; on its own context, because the caller's may be spent.
// ceiling: a failed delete leaves an orphan secret (a preview's is labelled and torn down with the PR).
func (s *Store) deleteUnused(ctx context.Context, ref string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inlineDeleteBudget)
	defer cancel()
	if err := s.secrets.Delete(dctx, ref); err != nil {
		slog.ErrorContext(ctx, "erp: delete unused grant secret failed", slog.String("secret", ref), slog.Any("err", err))
	}
}

// settleTx2Error runs when tx2 returned an error, which may follow a COMMIT the server applied. It
// re-reads the connection on its own context and deletes the secret only when the new grant is not
// the connection's; it reports whether the commit applied. When the re-read fails the secret stays.
func (s *Store) settleTx2Error(ctx context.Context, tenantID, connectionID, grantID, ref string) (committed bool) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inlineDeleteBudget)
	defer cancel()
	var held *string
	err := db.WithinTenantTx(rctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(rctx, `SELECT grant_id::text FROM erp_connections WHERE id = $1`, connectionID).Scan(&held)
	})
	if err != nil {
		slog.ErrorContext(ctx, "erp: could not confirm whether the connection activated; secret kept")
		return false
	}
	if held != nil && *held == grantID {
		return true
	}
	s.deleteUnused(ctx, ref)
	return false
}

type callbackRequest struct {
	State string `json:"state"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// CallbackHandler serves POST /internal/erp/callback for the gateway. It answers the outcome only and
// never echoes or logs the request.
func CallbackHandler(complete func(context.Context, CallbackInput) (Outcome, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req callbackRequest
		if err := jsonbody.Decode(w, r, callbackBodyLimit, &req); err != nil {
			if jsonbody.TooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		out, err := complete(r.Context(), CallbackInput{State: req.State, Code: req.Code, Error: req.Error})
		if err != nil {
			log.ErrorContext(r.Context(), "erp: complete callback", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]Outcome{"outcome": out})
	}
}
