package readinesscheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// ErrLinkInvalid is an unknown, used or expired confirm token.
var ErrLinkInvalid = errors.New("readinesscheck: confirm link invalid")

// ContactMerger merges the readiness-check contact inside the caller's transaction.
type ContactMerger interface {
	ReadinessCheckTx(ctx context.Context, tx pgx.Tx, email, consentText string) error
}

// Store reads and writes readiness_checks and the held results on the pool: rows carry no tenant.
type Store struct {
	Pool     *pgxpool.Pool
	Contacts ContactMerger
}

// Held is one completed check waiting for its confirm press.
type Held struct {
	Email   string
	Summary Summary
	Mail    accountmail.ReadinessCheckMail
}

const (
	heldLife       = "24 hours"
	releaseTimeout = 5 * time.Second
)

// mintToken returns 32 random bytes as base64url and the SHA-256 of its text.
func mintToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	return tok, h[:], nil
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// marshalMail encodes the mail for jsonb, which rejects \u0000. A NUL becomes U+FFFD, as the mail template renders it.
func marshalMail(m accountmail.ReadinessCheckMail) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil || !bytes.Contains(b, []byte(`\u0000`)) {
		return b, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(replaceNul(v))
}

func replaceNul(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "\x00", "\uFFFD")
	case []any:
		for i := range x {
			x[i] = replaceNul(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = replaceNul(x[k])
		}
	}
	return v
}

// Hold stores the result under a new token, after deleting expired rows. A second hold never replaces the first.
func (s *Store) Hold(ctx context.Context, h Held) (string, error) {
	tok, hash, err := mintToken()
	if err != nil {
		return "", fmt.Errorf("readinesscheck: mint token: %w", err)
	}
	mail, err := marshalMail(h.Mail)
	if err != nil {
		return "", fmt.Errorf("readinesscheck: marshal mail: %w", err)
	}
	ids := h.Summary.FailedRuleIDs
	if ids == nil {
		ids = []string{}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("readinesscheck: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM readiness_check_requests WHERE expires_at <= now()`); err != nil {
		return "", fmt.Errorf("readinesscheck: purge expired: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO readiness_check_requests (email, token_hash, file_type, row_count, failed_rule_ids, mail, expires_at)
		 VALUES (lower(btrim($1)), $2, $3, $4, $5, $6, now() + interval '`+heldLife+`')`,
		h.Email, hash, h.Summary.FileType, h.Summary.RowCount, ids, mail); err != nil {
		return "", fmt.Errorf("readinesscheck: hold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("readinesscheck: commit hold: %w", err)
	}
	return tok, nil
}

// Release deletes one held row on a context the caller's cancel does not reach.
func (s *Store) Release(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `DELETE FROM readiness_check_requests WHERE token_hash = $1`, tokenHash(token))
	return err
}

// PurgeExpired deletes the held rows past their expiry.
func (s *Store) PurgeExpired(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM readiness_check_requests WHERE expires_at <= now()`)
	return err
}

// Confirm consumes the held row and, in one transaction, records the check, merges the contact and calls send
// with the count before this check. Any error rolls all of it back, so the link works again. fresh is false
// when the address had reached the limit: nothing is recorded or merged.
func (s *Store) Confirm(ctx context.Context, token, consentText string, send func(ctx context.Context, h Held, count int) error) (h Held, fresh bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var mail []byte
	err = tx.QueryRow(ctx,
		`DELETE FROM readiness_check_requests WHERE token_hash = $1 AND expires_at > now()
		 RETURNING email, file_type, row_count, failed_rule_ids, mail`, tokenHash(token)).
		Scan(&h.Email, &h.Summary.FileType, &h.Summary.RowCount, &h.Summary.FailedRuleIDs, &mail)
	if errors.Is(err, pgx.ErrNoRows) {
		return Held{}, false, ErrLinkInvalid
	}
	if err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: consume held row: %w", err)
	}
	h.Summary.Email = h.Email
	if err := json.Unmarshal(mail, &h.Mail); err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: read held mail: %w", err)
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('readiness-check:' || $1::text, 0))`, h.Email); err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: lock address: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM readiness_checks WHERE email = $1`, h.Email).Scan(&count); err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: count: %w", err)
	}
	fresh = count < MaxRunsPerEmail
	if fresh {
		if _, err := tx.Exec(ctx,
			`INSERT INTO readiness_checks (email, file_type, row_count, failed_rule_ids) VALUES ($1, $2, $3, $4)`,
			h.Email, h.Summary.FileType, h.Summary.RowCount, h.Summary.FailedRuleIDs); err != nil {
			return Held{}, false, fmt.Errorf("readinesscheck: insert summary: %w", err)
		}
		if err := s.Contacts.ReadinessCheckTx(ctx, tx, h.Email, consentText); err != nil {
			return Held{}, false, err
		}
	}
	if err := send(ctx, h, count); err != nil {
		return Held{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Held{}, false, fmt.Errorf("readinesscheck: commit: %w", err)
	}
	return h, fresh, nil
}

// Summary is one completed check; the file and the client IP are never stored.
type Summary struct {
	Email         string
	FileType      string
	RowCount      int
	FailedRuleIDs []string
}

// Count returns the checks already recorded for the address.
func (s *Store) Count(ctx context.Context, email string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM readiness_checks WHERE email = lower(btrim($1))`, email).Scan(&n)
	return n, err
}

// AISpendReached reports whether the check's AI spend for the UTC day has reached the ceiling.
func (s *Store) AISpendReached(ctx context.Context, ceiling string) (bool, error) {
	var reached bool
	err := s.Pool.QueryRow(ctx, `SELECT public.public_check_ai_cost_today() >= $1::numeric`, ceiling).Scan(&reached)
	return reached, err
}
