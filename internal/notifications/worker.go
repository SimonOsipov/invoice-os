package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const QueueContacts = "contacts"

// DeliverArgs: Version is the row version the job was queued for ([version-guard]).
type DeliverArgs struct {
	Email       string `json:"email"`
	Destination string `json:"destination"`
	Version     int64  `json:"version"`
}

func (DeliverArgs) Kind() string { return "contact_deliver" }

// InsertOpts omits completed from ByState so a later delivery is never blocked; River requires the other four.
func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: QueueContacts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRetryable,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// DeliverWorker delivers one contact to one destination. Mode is what a delivery records.
type DeliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	Pool    *pgxpool.Pool
	HubSpot HubSpotClient
	Resend  ResendClient
	Mode    Mode
	Logger  *slog.Logger
}

// Work delivers the row at the version the job was queued for. A newer version has its own job,
// so an older job returns nil. An advisory lock per (destination, email) runs jobs for one person one at a time. Neither the email nor a name reaches a log line or an error.
func (w *DeliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	dest := job.Args.Destination
	if dest != destHubSpot && dest != destResend {
		return river.JobCancel(fmt.Errorf("notifications: unknown destination %q", dest))
	}

	var (
		version                   int64
		registered, demo, consent bool
		delivered, optInSent      bool
		first, last, company      string
	)
	// ceiling: the lock and its connection are held across the vendor call (10 s timeout, MaxWorkers 2); revisit if MaxWorkers rises past the pool size.
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notifications: begin delivery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:' || $1::text || ':' || $2::text, 0))`, dest, job.Args.Email); err != nil {
		return fmt.Errorf("notifications: lock delivery: %w", err)
	}

	err = tx.QueryRow(ctx, `
		SELECT version, COALESCE(first_name, ''), COALESCE(last_name, ''), COALESCE(company, ''),
		       registered_at IS NOT NULL, demo_requested_at IS NOT NULL, marketing_consented_at IS NOT NULL,
		       CASE WHEN $2::text = 'hubspot' THEN hubspot_delivered_at ELSE resend_delivered_at END IS NOT NULL,
		       resend_opt_in_sent_at IS NOT NULL
		FROM contacts WHERE email = $1`, job.Args.Email, dest).Scan(
		&version, &first, &last, &company, &registered, &demo, &consent, &delivered, &optInSent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notifications: read contact: %w", err)
	}
	if delivered || version != job.Args.Version {
		return nil
	}

	c := Contact{Email: job.Args.Email, FirstName: first, LastName: last, Company: company, MarketingEligible: consent}
	if registered {
		c.Tags = append(c.Tags, "registered")
	}
	if demo {
		c.Tags = append(c.Tags, "demo request")
	}

	if dest == destHubSpot {
		err = w.HubSpot.Upsert(ctx, c)
	} else {
		if !registered && !consent {
			return nil
		}
		sendOptIn := consent && !optInSent
		if err = w.Resend.Sync(ctx, c, sendOptIn); err == nil && sendOptIn {
			_, err = tx.Exec(ctx, `UPDATE contacts SET resend_opt_in_sent_at = now() WHERE email = $1 AND resend_opt_in_sent_at IS NULL`, c.Email)
		}
	}
	if err != nil {
		w.logFailure(ctx, dest, err)
		return err
	}

	col := dest + "_delivered_at"
	_, err = tx.Exec(ctx, `UPDATE contacts SET `+col+` = now(), delivery_mode = $2 WHERE email = $1 AND version = $3`,
		c.Email, string(w.Mode), version)
	if err != nil {
		return fmt.Errorf("notifications: record delivery: %w", err)
	}
	// A miss means a newer version, and its own job, landed during delivery.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit delivery: %w", err)
	}
	return nil
}

func (w *DeliverWorker) logFailure(ctx context.Context, dest string, err error) {
	var de *DeliveryError
	if errors.As(err, &de) && de.Permanent() {
		w.Logger.ErrorContext(ctx, "contacts: "+dest+" rejected the delivery", slog.String("destination", dest), slog.Int("status", de.Status))
		return
	}
	status := 0
	if de != nil {
		status = de.Status
	}
	w.Logger.WarnContext(ctx, "contacts: "+dest+" delivery failed", slog.String("destination", dest), slog.Int("status", status))
}
