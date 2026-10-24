package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const destHubSpotDeal = "hubspot_deal"

// demoDealTimeout is six vendor calls plus a 20 s lock wait behind a HubSpot contact job.
const demoDealTimeout = 6*vendorTimeout + 20*time.Second

// DemoDealArgs carries the request's own values, not the merged row's.
type DemoDealArgs struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Company string `json:"company"`
	// SignUp marks a first-registration deal; a job queued without the key is a demo deal.
	SignUp bool `json:"sign_up,omitempty"`
}

func (DemoDealArgs) Kind() string { return "demo_deal" }

func (DemoDealArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: QueueContacts} }

func dealName(company, name, label string) string {
	if c := strings.TrimSpace(company); c != "" {
		return c + " — " + label
	}
	if n := strings.TrimSpace(name); n != "" {
		return n + " — " + label
	}
	r, size := utf8.DecodeRuneInString(label)
	return string(unicode.ToUpper(r)) + label[size:]
}

func demoDealName(company, name string) string { return dealName(company, name, "demo request") }

// DemoDealWorker opens one HubSpot deal for a demo request or a first sign-up.
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
	label := "demo request"
	if job.Args.SignUp {
		label = "sign-up"
	}
	if err = w.HubSpot.OpenDemoDeal(ctx, c, dealName(job.Args.Company, job.Args.Name, label)); err != nil {
		logDeliveryFailure(ctx, w.Logger, destHubSpotDeal, err)
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit demo deal: %w", err)
	}
	return nil
}
