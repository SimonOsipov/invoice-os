package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const destHubSpotDeal = "hubspot_deal"

// Deal sources; an absent source is a demo request.
const (
	dealSourceSignUp         = "sign_up"
	dealSourceReadinessCheck = "readiness_check"
)

// demoDealTimeout is six vendor calls plus a 20 s lock wait behind a HubSpot contact job.
const demoDealTimeout = 6*vendorTimeout + 20*time.Second

// DemoDealArgs carries the request's own values, not the merged row's.
type DemoDealArgs struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Company string `json:"company"`
	Source  string `json:"source,omitempty"`
	// ceiling: read only for jobs queued before Source; drop once no demo_deal job carries sign_up.
	LegacySignUp bool `json:"sign_up,omitempty"`
}

func (DemoDealArgs) Kind() string { return "demo_deal" }

func (DemoDealArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: QueueContacts} }

func dealName(company, name, label, fallback string) string {
	if c := strings.TrimSpace(company); c != "" {
		return c + " — " + label
	}
	if n := strings.TrimSpace(name); n != "" {
		return n + " — " + label
	}
	return fallback
}

// DemoDealWorker opens one HubSpot deal for a demo request, a first sign-up or a readiness check.
type DemoDealWorker struct {
	river.WorkerDefaults[DemoDealArgs]
	Pool    *pgxpool.Pool
	HubSpot HubSpotClient
	Logger  *slog.Logger
}

// Timeout covers the worst chain: the lock wait plus six vendor calls. River's 60 s default cancels it mid-chain.
func (w *DemoDealWorker) Timeout(*river.Job[DemoDealArgs]) time.Duration { return demoDealTimeout }

// Work holds the HubSpot contact job's advisory lock so the two jobs of one person never run together.
func (w *DemoDealWorker) Work(ctx context.Context, job *river.Job[DemoDealArgs]) error {
	label, fallback := "demo request", "Demo request"
	switch src := job.Args.Source; src {
	case "":
		if job.Args.LegacySignUp {
			label, fallback = "sign-up", "Sign-up"
		}
	case dealSourceSignUp:
		label, fallback = "sign-up", "Sign-up"
	case dealSourceReadinessCheck:
		label, fallback = "readiness check", "Readiness check"
	default:
		return river.JobCancel(fmt.Errorf("notifications: unknown deal source %q", src))
	}
	// ceiling: the lock and its connection are held across up to six 10 s vendor calls on the shared contacts queue (MaxWorkers 2); revisit if MaxWorkers rises past the pool size or contact jobs run late behind deal jobs.
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
	if err = w.HubSpot.OpenDemoDeal(ctx, c, dealName(job.Args.Company, job.Args.Name, label, fallback)); err != nil {
		logDeliveryFailure(ctx, w.Logger, destHubSpotDeal, err)
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit demo deal: %w", err)
	}
	return nil
}
