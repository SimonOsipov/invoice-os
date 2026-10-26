package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// handOffSnooze is the wait between hand-off loops; a snooze costs no attempt.
const handOffSnooze = 30 * time.Second

// EraseByIDArgs carries the user id only; it erases a person who has no usable email.
type EraseByIDArgs struct {
	UserID string `json:"user_id"`
}

func (EraseByIDArgs) Kind() string { return "person_erase_by_id" }

func (EraseByIDArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

// EnqueueErasureByID queues the by-id erasure in tx.
func EnqueueErasureByID(ctx context.Context, tx pgx.Tx, rc *river.Client[pgx.Tx], userID string) error {
	id, err := uuid.Parse(userID)
	if err != nil {
		return errors.New("notifications: erase user id is not a UUID")
	}
	if _, err = rc.InsertTx(ctx, tx, EraseByIDArgs{UserID: id.String()}, nil); err != nil {
		return fmt.Errorf("notifications: queue erasure: %w", err)
	}
	return nil
}

// EraseByIDWorker erases a person by user id. With a contacts row it hands the email to EnqueueErasure; with none it removes
// the user-id-keyed copies and the account itself, writes no person_erasures row and calls no vendor.
type EraseByIDWorker struct {
	river.WorkerDefaults[EraseByIDArgs]
	Pool    *pgxpool.Pool
	River   *river.Client[pgx.Tx] // set by the wiring after queue.New
	Account AccountDeleter
	Logger  *slog.Logger
}

func (w *EraseByIDWorker) Timeout(*river.Job[EraseByIDArgs]) time.Duration { return eraseTimeout }

// Work hands off one contacts email at a time while a row remains; else it sweeps, deletes the account, then sweeps again.
// Every statement is idempotent, so a retry repeats the job.
func (w *EraseByIDWorker) Work(ctx context.Context, job *river.Job[EraseByIDArgs]) error {
	id, err := uuid.Parse(job.Args.UserID)
	if err != nil {
		return river.JobCancel(errors.New("notifications: erase job has a bad user id"))
	}
	uid := id.String()

	emails, err := w.contactEmails(ctx, uid)
	if err != nil {
		logEraseFailure(ctx, w.Logger, stepSweep, err)
		return err
	}
	if len(emails) > 0 {
		return w.handOff(ctx, uid, emails[0])
	}

	_, err = w.sweepTx(ctx, uid)
	if err != nil {
		logEraseFailure(ctx, w.Logger, stepSweep, err)
		return err
	}
	if w.Account == nil {
		w.Logger.WarnContext(ctx, "erasure: no account client; the account step waits", slog.String("step", stepAccount))
		return errAccountOff
	}
	if err := w.Account.DeleteUser(ctx, uid); err != nil {
		logEraseFailure(ctx, w.Logger, stepAccount, err)
		return err
	}
	// A running job can write a copy after the first sweep; a copy found here fails the attempt.
	removed, err := w.sweepTx(ctx, uid)
	if err != nil {
		logEraseFailure(ctx, w.Logger, stepFinal, err)
		return err
	}
	if removed > 0 {
		w.Logger.WarnContext(ctx, "erasure: a copy came back; the sweep runs again", slog.String("step", stepFinal))
		return errCameBack
	}
	return nil
}

// handOff queues the erasure of one contacts email unless one is running, then snoozes.
// A person_erasures row keeps one email, so each contacts row gets its own erasure in turn.
// person_erase is not in eraseKinds, so no sweep cancels the job queued here.
func (w *EraseByIDWorker) handOff(ctx context.Context, uid, email string) error {
	var running bool
	err := w.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM person_erasures WHERE user_id = $1 AND finished_at IS NULL)`, uid).Scan(&running)
	if err == nil && !running {
		err = pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error { return EnqueueErasure(ctx, tx, w.River, uid, email) })
	}
	if err != nil {
		logEraseFailure(ctx, w.Logger, stepSweep, err)
		return err
	}
	return river.JobSnooze(handOffSnooze)
}

// contactEmails returns the usable emails of the person's contacts rows.
func (w *EraseByIDWorker) contactEmails(ctx context.Context, uid string) ([]string, error) {
	rows, err := w.Pool.Query(ctx, `SELECT email FROM contacts WHERE user_id::text = $1 ORDER BY email`, uid)
	if err != nil {
		return nil, fmt.Errorf("notifications: read contacts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, fmt.Errorf("notifications: read contacts: %w", err)
		}
		if UsableEraseEmail(e) {
			out = append(out, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notifications: read contacts: %w", err)
	}
	return out, nil
}

func (w *EraseByIDWorker) sweepTx(ctx context.Context, uid string) (removed int64, err error) {
	err = pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		removed, err = w.sweep(ctx, tx, uid)
		return err
	})
	return removed, err
}

// sweep reports how many rows its deletes removed.
func (w *EraseByIDWorker) sweep(ctx context.Context, tx pgx.Tx, uid string) (int64, error) {
	var removed int64
	const where = `kind = ANY($1) AND lower(args->>'user_id') = $2`
	rows, err := tx.Query(ctx, `SELECT id FROM river_job WHERE state IN ('available', 'pending', 'retryable', 'running', 'scheduled') AND `+where+` ORDER BY id`, eraseKinds, uid)
	if err != nil {
		return 0, fmt.Errorf("notifications: find jobs: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("notifications: read job: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("notifications: find jobs: %w", err)
	}
	for _, id := range ids {
		if _, err := w.River.JobCancelTx(ctx, tx, id); err != nil {
			return 0, fmt.Errorf("notifications: cancel job: %w", err)
		}
	}
	// Cancelled first, so the jobs just cancelled go too; a running job stays, marked.
	for _, d := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM river_job WHERE state IN ('completed', 'cancelled', 'discarded') AND ` + where, []any{eraseKinds, uid}},
		{`DELETE FROM registrant_handoffs WHERE lower(user_id) = $1`, []any{uid}},
		{`DELETE FROM contacts WHERE user_id::text = $1`, []any{uid}},
	} {
		tag, err := tx.Exec(ctx, d.sql, d.args...)
		if err != nil {
			return 0, fmt.Errorf("notifications: delete copies: %w", err)
		}
		removed += tag.RowsAffected()
	}
	return removed, nil
}

// AddEraseByIDTo registers the by-id erase worker on the worker's one bundle.
func AddEraseByIDTo(bundle *river.Workers, w *EraseByIDWorker) { river.AddWorker(bundle, w) }
