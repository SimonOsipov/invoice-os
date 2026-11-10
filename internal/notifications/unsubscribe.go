package notifications

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errUnknownToken = errors.New("unsubscribe: unknown token")
	// errUnavailable stands for a failed Resend read, local write or PATCH; it carries no cause
	// because a database message can hold the address.
	errUnavailable = errors.New("unsubscribe: unavailable")
)

// UnsubscribeStore reads and updates mail_unsubscribes. An address carries no tenant.
type UnsubscribeStore struct{ pool *pgxpool.Pool }

func NewUnsubscribeStore(pool *pgxpool.Pool) *UnsubscribeStore { return &UnsubscribeStore{pool: pool} }

// Lookup returns the masked address and whether the token is already unsubscribed. It writes nothing.
func (s *UnsubscribeStore) Lookup(ctx context.Context, token string) (string, bool, error) {
	var email string
	var done bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT email, unsubscribed_at IS NOT NULL FROM mail_unsubscribes WHERE token = $1`, token).Scan(&email, &done)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, errUnknownToken
	}
	if err != nil {
		return "", false, err
	}
	return maskEmail(email), done, nil
}

// Press records the unsubscribe and opts the address out of Marketing, in one transaction that
// locks the row (D4). A nil rs (mode off) records locally only. It returns "unsubscribed" or "already".
//
// ceiling: a COMMIT failure after a sent PATCH leaves Resend opted out and no local record; the retry records it.
// ceiling: the row lock is held across one Resend call, bounded by the Resend client timeout.
func (s *UnsubscribeStore) Press(ctx context.Context, token string, rs ResendClient) (string, error) {
	status := "unsubscribed"
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		var done bool
		err := tx.QueryRow(ctx, `SELECT email, unsubscribed_at IS NOT NULL FROM mail_unsubscribes WHERE token = $1 FOR UPDATE`, token).Scan(&email, &done)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownToken
		}
		if err != nil {
			return err
		}
		if done {
			status = "already"
			return nil
		}
		var present bool
		if rs != nil {
			st, err := rs.ContactState(ctx, email)
			if err != nil {
				return errUnavailable
			}
			present = !st.Missing
		}
		if _, err := tx.Exec(ctx, `UPDATE mail_unsubscribes SET unsubscribed_at = now() WHERE token = $1 AND unsubscribed_at IS NULL`, token); err != nil {
			return errUnavailable
		}
		if present && rs.OptOutMarketing(ctx, email) != nil {
			return errUnavailable
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// maskEmail keeps the first character of the local part and the domain.
func maskEmail(email string) string {
	local, domain, _ := strings.Cut(email, "@")
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domain
}

type unsubscribeBody struct {
	Token string `json:"token"`
}

// decodeToken answers 400 for a body or token that is not a canonical UUID, and 404 for a proxied request.
func decodeToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b unsubscribeBody
	if !decodeIntake(w, r, &b) {
		return "", false
	}
	id, err := uuid.Parse(b.Token)
	if err != nil || len(b.Token) != 36 {
		writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "token is invalid"})
		return "", false
	}
	return id.String(), true
}

func UnsubscribeLookupHandler(store *UnsubscribeStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := decodeToken(w, r)
		if !ok {
			return
		}
		masked, done, err := store.Lookup(r.Context(), token)
		switch {
		case errors.Is(err, errUnknownToken):
			writeJSONBody(w, http.StatusNotFound, map[string]string{"error": "unknown link"})
		case err != nil:
			log.ErrorContext(r.Context(), "unsubscribe: lookup failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		default:
			writeJSONBody(w, http.StatusOK, map[string]any{"masked": masked, "unsubscribed": done})
		}
	}
}

// UnsubscribeHandler is the press; a nil rs is contacts mode off.
func UnsubscribeHandler(store *UnsubscribeStore, rs ResendClient, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := decodeToken(w, r)
		if !ok {
			return
		}
		status, err := store.Press(r.Context(), token, rs)
		switch {
		case errors.Is(err, errUnknownToken):
			writeJSONBody(w, http.StatusNotFound, map[string]string{"error": "unknown link"})
		case errors.Is(err, errUnavailable):
			log.WarnContext(r.Context(), "unsubscribe: press unavailable")
			writeJSONBody(w, http.StatusServiceUnavailable, map[string]string{"error": "unsubscribe is unavailable"})
		case err != nil:
			log.ErrorContext(r.Context(), "unsubscribe: press failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "unsubscribe failed"})
		default:
			writeJSONBody(w, http.StatusOK, map[string]string{"status": status})
		}
	}
}

// OptOutMarketing sets the Marketing topic to opt_out. It never sends unsubscribed, opt_in or a suppression.
func (r *Resend) OptOutMarketing(ctx context.Context, email string) error {
	body := []map[string]string{{"id": r.topicID, "subscription": "opt_out"}}
	status, err := doJSON(ctx, r.hc, http.MethodPatch, r.baseURL+"/contacts/"+url.PathEscape(email)+"/topics", r.apiKey, body)
	if err != nil {
		return err
	}
	return statusError(status)
}
