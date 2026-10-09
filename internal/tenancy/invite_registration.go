package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxInviteRegBodyBytes bounds the {"email"} body of the pending route.
const maxInviteRegBodyBytes = maxInviteTokenBodyBytes

// InvitationPendingForEmail reports whether the address has a live invite in any tenant.
// The nil-uuid GUC scopes every table but the SECURITY DEFINER lookup to none.
func (s *Store) InvitationPendingForEmail(ctx context.Context, email string) (bool, error) {
	var pending bool
	err := db.WithinTenantTx(ctx, s.pool, uuid.Nil.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT public.invitation_pending_for_email($1)`, strings.ToLower(email)).Scan(&pending)
	})
	return pending, err
}

// ClaimInvitationRegistration spends the token's one registration; first is false after the first claim.
func (s *Store) ClaimInvitationRegistration(ctx context.Context, token string) (email string, first bool, err error) {
	if !inviteTokenShape.MatchString(token) {
		return "", false, ErrInvitationNotValid
	}
	var tenantID string
	err = db.WithinTenantTx(ctx, s.pool, uuid.Nil.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT tenant_id, email FROM public.invitation_by_token($1)`, token).Scan(&tenantID, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotValid
		}
		return err
	})
	if err != nil {
		return "", false, err
	}
	hash := sha256.Sum256([]byte(token))
	err = db.WithinTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var got string
		err := tx.QueryRow(ctx, `UPDATE invitations SET registered_token_hash = token_hash
			 WHERE token_hash = $1 AND status = 'pending' AND expires_at > now()
			   AND registered_token_hash IS DISTINCT FROM token_hash
			RETURNING invitee_email`, hash[:]).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		first = err == nil
		return err
	})
	if err != nil {
		return "", false, err
	}
	return email, first, nil
}

// ReleaseInvitationRegistration clears the token's own claim.
func (s *Store) ReleaseInvitationRegistration(ctx context.Context, token string) error {
	if !inviteTokenShape.MatchString(token) {
		return ErrInvitationNotValid
	}
	var invitationID, tenantID string
	err := db.WithinTenantTx(ctx, s.pool, uuid.Nil.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT invitation_id, tenant_id FROM public.invitation_by_token($1)`, token).Scan(&invitationID, &tenantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotValid
		}
		return err
	})
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	return db.WithinTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE invitations SET registered_token_hash = NULL
			 WHERE id = $1 AND token_hash = $2 AND registered_token_hash = token_hash`, invitationID, hash[:])
		return err
	})
}

// failInviteReg answers a lookup error; only a 500 is logged, and never with the request's values.
func failInviteReg(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	status, msg := statusForErr(err)
	if status == http.StatusInternalServerError {
		log.ErrorContext(r.Context(), "tenancy: "+op, slog.Any("err", err))
	}
	writeError(w, status, msg)
}

// InvitationPendingHandler returns POST /internal/invitations/pending: capped
// decode and non-blank email (400), lookup, 200 `{pending}`. No identity is read.
func InvitationPendingHandler(pending func(ctx context.Context, email string) (bool, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxInviteRegBodyBytes)
		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ok, err := pending(r.Context(), req.Email)
		if err != nil {
			failInviteReg(w, r, log, "invitation pending", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"pending": ok})
	}
}

// InvitationRegisterClaimHandler returns POST /internal/invitations/register: capped
// decode (400), claim, 200 `{email, first}`. The token is the only credential.
func InvitationRegisterClaimHandler(claim func(ctx context.Context, token string) (string, bool, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := readInviteToken(w, r)
		if !ok {
			return
		}
		email, first, err := claim(r.Context(), token)
		if err != nil {
			failInviteReg(w, r, log, "claim invitation registration", err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Email string `json:"email"`
			First bool   `json:"first"`
		}{email, first})
	}
}

// InvitationRegisterReleaseHandler returns POST /internal/invitations/release: capped
// decode (400), release, 204.
func InvitationRegisterReleaseHandler(release func(ctx context.Context, token string) error, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := readInviteToken(w, r)
		if !ok {
			return
		}
		if err := release(r.Context(), token); err != nil {
			failInviteReg(w, r, log, "release invitation registration", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
