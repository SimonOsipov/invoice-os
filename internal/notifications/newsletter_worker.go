package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// BlogSyncArgs: Version is the blog_subscribers version the job was queued for.
type BlogSyncArgs struct {
	Email   string `json:"email"`
	Version int64  `json:"version"`
}

func (BlogSyncArgs) Kind() string { return "blog_subscriber_sync" }

func (BlogSyncArgs) InsertOpts() river.InsertOpts { return DeliverArgs{}.InsertOpts() }

// BlogSyncWorker writes one confirmed subscriber to Resend. It holds no HubSpot client.
type BlogSyncWorker struct {
	river.WorkerDefaults[BlogSyncArgs]
	Pool   *pgxpool.Pool
	Resend BlogResendClient
	Mode   Mode
	Logger *slog.Logger
}

// Work takes the lock DeliverWorker takes for resend, so one person's Resend writes run one at a
// time. A stale or delivered job makes no call. Neither the email nor a category reaches a log line.
func (w *BlogSyncWorker) Work(ctx context.Context, job *river.Job[BlogSyncArgs]) error {
	// ceiling: the lock and its connection are held across the vendor call (10 s timeout); revisit if MaxWorkers rises past the pool size.
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notifications: begin blog sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:resend:' || $1::text, 0))`, job.Args.Email); err != nil {
		return fmt.Errorf("notifications: lock blog sync: %w", err)
	}

	s := BlogSubscriber{Email: job.Args.Email}
	var (
		version   int64
		delivered bool
	)
	err = tx.QueryRow(ctx, `
		SELECT version, category_ids, frequency, resend_delivered_at IS NOT NULL
		FROM blog_subscribers WHERE email = $1`, s.Email).Scan(&version, &s.CategoryIDs, &s.Frequency, &delivered)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notifications: read blog subscriber: %w", err)
	}
	if delivered || version != job.Args.Version {
		return nil
	}

	if err = w.Resend.SyncBlog(ctx, s); err != nil {
		logDeliveryFailure(ctx, w.Logger, destResend, err)
		return err
	}

	// A miss means a newer version, and its own job, landed during delivery.
	if _, err = tx.Exec(ctx, `UPDATE blog_subscribers SET resend_delivered_at = now(), delivery_mode = $2 WHERE email = $1 AND version = $3`,
		s.Email, string(w.Mode), version); err != nil {
		return fmt.Errorf("notifications: record blog sync: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit blog sync: %w", err)
	}
	return nil
}
