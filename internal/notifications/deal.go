package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const destHubSpotDeal = "hubspot_deal"

// DemoDealArgs carries the request's own values; the merged contact row keeps the first company.
type DemoDealArgs struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Company string `json:"company"`
}

func (DemoDealArgs) Kind() string { return "demo_deal" }

func (DemoDealArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: QueueContacts} }

func demoDealName(company, name string) string {
	if c := strings.TrimSpace(company); c != "" {
		return c + " — demo request"
	}
	if n := strings.TrimSpace(name); n != "" {
		return n + " — demo request"
	}
	return "Demo request"
}

// DemoDealWorker opens one HubSpot deal for a demo request.
type DemoDealWorker struct {
	river.WorkerDefaults[DemoDealArgs]
	Pool    *pgxpool.Pool
	HubSpot HubSpotClient
	Logger  *slog.Logger
}

// Work holds the HubSpot contact job's advisory lock so the two jobs of one person never run together.
func (w *DemoDealWorker) Work(ctx context.Context, job *river.Job[DemoDealArgs]) error {
	// ceiling: the lock and its connection are held across up to four 10 s vendor calls on the shared contacts queue (MaxWorkers 2); revisit if MaxWorkers rises past the pool size or contact jobs run late behind deal jobs.
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notifications: begin demo deal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:' || 'hubspot' || ':' || $1::text, 0))`, job.Args.Email); err != nil {
		return fmt.Errorf("notifications: lock demo deal: %w", err)
	}

	first, last := splitName(job.Args.Name)
	c := Contact{Email: job.Args.Email, FirstName: first, LastName: last, Company: job.Args.Company}
	if err = w.HubSpot.OpenDemoDeal(ctx, c, demoDealName(job.Args.Company, job.Args.Name)); err != nil {
		logDeliveryFailure(ctx, w.Logger, destHubSpotDeal, err)
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit demo deal: %w", err)
	}
	return nil
}
