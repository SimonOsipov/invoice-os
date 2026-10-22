package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// RegistrantArgs is one row of registrant_handoffs, written by the gateway.
type RegistrantArgs struct {
	UserID        string `json:"user_id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	WorkspaceName string `json:"workspace_name"`
	ConsentText   string `json:"consent_text"`
	ConsentAt     string `json:"consent_at"`
}

// Kind runs on the default queue: intake records the contact in every mode, off included.
func (RegistrantArgs) Kind() string { return "registrant_intake" }

// RegistrantWorker records a handed-off registrant. A failure retries on River's default policy.
type RegistrantWorker struct {
	river.WorkerDefaults[RegistrantArgs]
	Store  IntakeStore
	Logger *slog.Logger
}

func (w *RegistrantWorker) Work(ctx context.Context, job *river.Job[RegistrantArgs]) error {
	a := job.Args
	if strings.TrimSpace(a.Email) == "" || !validUserID(a.UserID) {
		return river.JobCancel(errors.New("notifications: registrant hand-off has no email or a bad user_id"))
	}
	in := RegistrantIntake{UserID: a.UserID, Email: a.Email, DisplayName: a.DisplayName, WorkspaceName: a.WorkspaceName}
	in.ConsentText, in.ConsentAt = consent(ctx, w.Logger, a.UserID, a.ConsentText, a.ConsentAt)
	return w.Store.Registrant(ctx, in)
}

// HandoffInterval is how often the worker moves gateway rows; it is the added delay before HubSpot.
const HandoffInterval = 5 * time.Second

// AddRegistrantTo registers w in every contacts mode.
func AddRegistrantTo(bundle *river.Workers, w *RegistrantWorker) { river.AddWorker(bundle, w) }

// handoffBatch bounds one move; a backlog drains over several ticks.
const handoffBatch = 100

// MoveRegistrantHandoffs moves gateway rows into River jobs, in one transaction.
func MoveRegistrantHandoffs(pool *pgxpool.Pool, rc *river.Client[pgx.Tx]) func(context.Context) error {
	return func(ctx context.Context) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			// DELETE hands each row to one mover; the jobs commit with it or neither does.
			rows, err := tx.Query(ctx, `DELETE FROM registrant_handoffs WHERE id IN
				(SELECT id FROM registrant_handoffs ORDER BY created_at LIMIT $1)
				RETURNING user_id, email, display_name, workspace_name, consent_text, consent_at`, handoffBatch)
			if err != nil {
				return fmt.Errorf("notifications: read registrant hand-offs: %w", err)
			}
			var jobs []river.InsertManyParams
			for rows.Next() {
				var a RegistrantArgs
				if err := rows.Scan(&a.UserID, &a.Email, &a.DisplayName, &a.WorkspaceName, &a.ConsentText, &a.ConsentAt); err != nil {
					rows.Close()
					return fmt.Errorf("notifications: read registrant hand-off: %w", err)
				}
				jobs = append(jobs, river.InsertManyParams{Args: a})
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("notifications: read registrant hand-offs: %w", err)
			}
			if len(jobs) == 0 {
				return nil
			}
			if _, err := rc.InsertManyTx(ctx, tx, jobs); err != nil {
				return fmt.Errorf("notifications: queue registrant hand-offs: %w", err)
			}
			return nil
		})
	}
}
