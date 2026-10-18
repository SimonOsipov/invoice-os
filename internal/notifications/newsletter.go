package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

var (
	ErrUnknownCategory = errors.New("notifications: unknown blog category")
	ErrLinkInvalid     = errors.New("notifications: blog confirm link is unknown, used or expired")
)

// SubscribeIntake: Email is normalised in SQL (lower, btrim); Frequency is already validated.
type SubscribeIntake struct {
	Email       string
	CategoryIDs []string
	Frequency   string
}

// NewsletterStore holds subscription requests and subscribers. Both tables are global (no tenant), so it uses the pool.
type NewsletterStore struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

func NewNewsletterStore(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *NewsletterStore {
	return &NewsletterStore{pool: pool, river: riverClient}
}

// mintSubscribeToken returns 32 random bytes as base64url and the SHA-256 of its text.
func mintSubscribeToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	return tok, h[:], nil
}

// Subscribe stores one pending request per email, replacing an earlier token. It returns the
// token for the mail and the category names in position order.
func (s *NewsletterStore) Subscribe(ctx context.Context, in SubscribeIntake) (string, []string, error) {
	ids := slices.Clone(in.CategoryIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)

	token, hash, err := mintSubscribeToken()
	if err != nil {
		return "", nil, fmt.Errorf("notifications: mint subscribe token: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("notifications: begin subscribe: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, `DELETE FROM blog_subscription_requests WHERE expires_at <= now()`); err != nil {
		return "", nil, fmt.Errorf("notifications: purge subscription requests: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT id, name FROM blog_categories WHERE id = ANY($1) ORDER BY position`, ids)
	if err != nil {
		return "", nil, fmt.Errorf("notifications: read categories: %w", err)
	}
	var ordered, names []string
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			rows.Close()
			return "", nil, fmt.Errorf("notifications: scan category: %w", err)
		}
		ordered, names = append(ordered, id), append(names, name)
	}
	if err = rows.Err(); err != nil {
		return "", nil, fmt.Errorf("notifications: read categories: %w", err)
	}
	if len(ordered) != len(ids) {
		return "", nil, ErrUnknownCategory
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at)
		VALUES (lower(btrim($1)), $2, $3, $4, now() + interval '48 hours')
		ON CONFLICT (email) DO UPDATE SET
		    category_ids = EXCLUDED.category_ids, frequency = EXCLUDED.frequency, token_hash = EXCLUDED.token_hash,
		    expires_at = EXCLUDED.expires_at, created_at = now()`,
		in.Email, ordered, in.Frequency, hash); err != nil {
		return "", nil, fmt.Errorf("notifications: store subscription request: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("notifications: commit subscribe: %w", err)
	}
	return token, names, nil
}

// Confirm consumes the request once and queues the Resend sync in the same transaction. It returns the subscriber email.
func (s *NewsletterStore) Confirm(ctx context.Context, tokenHash []byte) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("notifications: begin confirm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		email, frequency string
		ids              []string
	)
	err = tx.QueryRow(ctx, `
		DELETE FROM blog_subscription_requests WHERE token_hash = $1 AND expires_at > now()
		RETURNING email, category_ids, frequency`, tokenHash).Scan(&email, &ids, &frequency)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLinkInvalid
	}
	if err != nil {
		return "", fmt.Errorf("notifications: consume subscription request: %w", err)
	}

	var version int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO blog_subscribers (email, category_ids, frequency, confirmed_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (email) DO UPDATE SET
		    category_ids = EXCLUDED.category_ids, frequency = EXCLUDED.frequency, version = blog_subscribers.version + 1,
		    confirmed_at = now(), resend_delivered_at = NULL, delivery_mode = NULL, updated_at = now()
		RETURNING version`, email, ids, frequency).Scan(&version); err != nil {
		return "", fmt.Errorf("notifications: upsert blog subscriber: %w", err)
	}
	if _, err = s.river.InsertTx(ctx, tx, BlogSyncArgs{Email: email, Version: version}, nil); err != nil {
		return "", fmt.Errorf("notifications: queue blog sync: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("notifications: commit confirm: %w", err)
	}
	return email, nil
}

// PurgeExpired deletes lapsed requests so an address that never confirms is not kept.
func (s *NewsletterStore) PurgeExpired(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM blog_subscription_requests WHERE expires_at <= now()`); err != nil {
		return fmt.Errorf("notifications: purge subscription requests: %w", err)
	}
	return nil
}
