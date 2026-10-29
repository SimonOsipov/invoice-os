package accountevents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// NewDeviceArgs is one account_new_device job. It carries no tenant: the email goes once per sign-in.
type NewDeviceArgs struct {
	UserID     string `json:"user_id"`
	Email      string `json:"email"`
	Browser    string `json:"browser"`
	OS         string `json:"os"`
	IP         string `json:"ip"`
	OccurredAt string `json:"occurred_at"`
}

func (NewDeviceArgs) Kind() string { return "account_new_device" }

// InsertOpts allows one attempt: a failed send is not retried, so the member gets at most one mail.
func (NewDeviceArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1} }

// NewDeviceWorker records the sign-in's browser and OS pair and mails the member when the pair is new.
type NewDeviceWorker struct {
	river.WorkerDefaults[NewDeviceArgs]
	Pool   *pgxpool.Pool
	Sender accountmail.Sender
	Logger *slog.Logger
}

// AddNewDeviceTo registers w on the worker's one bundle.
func AddNewDeviceTo(bundle *river.Workers, w *NewDeviceWorker) { river.AddWorker(bundle, w) }

// One statement: had reads the snapshot from before the insert.
const recordPair = `WITH had AS (SELECT EXISTS (SELECT 1 FROM account_known_devices WHERE user_id = $1::uuid) AS v),
	ins AS (INSERT INTO account_known_devices (user_id, browser, os) VALUES ($1::uuid, $2, $3)
		ON CONFLICT DO NOTHING RETURNING 1)
	SELECT had.v, EXISTS (SELECT 1 FROM ins) FROM had`

func (w *NewDeviceWorker) Work(ctx context.Context, job *river.Job[NewDeviceArgs]) error {
	a := job.Args
	var had, inserted bool
	if err := w.Pool.QueryRow(ctx, recordPair, a.UserID, a.Browser, a.OS).Scan(&had, &inserted); err != nil {
		return fmt.Errorf("accountevents: record device: %w", err)
	}
	if !had || !inserted {
		return nil
	}
	at, err := time.Parse(time.RFC3339, a.OccurredAt)
	if err != nil {
		return river.JobCancel(fmt.Errorf("accountevents: bad sign-in time: %w", err))
	}
	subject, html, err := accountmail.RenderNewDevice(accountmail.NewDeviceMail{
		Device: Device{Browser: a.Browser, OS: a.OS}.Label(), IP: a.IP, At: at,
	})
	if err != nil {
		return fmt.Errorf("accountevents: render new-device mail: %w", err)
	}
	if err := w.Sender.Send(ctx, []accountmail.Message{{To: a.Email, Subject: subject, HTML: html}}); err != nil {
		// The error is a *SendError or ErrNotConfigured: neither holds the address.
		w.Logger.WarnContext(ctx, "account-new-device: send failed", "error", err)
		return fmt.Errorf("accountevents: send new-device mail: %w", err)
	}
	return nil
}
