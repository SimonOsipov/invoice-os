package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

// eraseTimeout bounds one attempt.
const eraseTimeout = 3 * time.Minute

// EraseArgs carries the user id and the row generation, never the email: River keeps a finished job's args.
type EraseArgs struct {
	UserID     string `json:"user_id"`
	Generation int64  `json:"generation"`
}

func (EraseArgs) Kind() string { return "person_erase" }

// InsertOpts sets no UniqueOpts: every enqueue queues its own job, also while an older one runs.
func (EraseArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

// AccountDeleter deletes the sign-in account.
type AccountDeleter interface {
	DeleteUser(ctx context.Context, userID string) error
}

var _ AccountDeleter = (*gotrueadmin.Client)(nil)

var (
	errContactsOff = errors.New("notifications: contacts are off; the vendor step waits")
	errAccountOff  = errors.New("notifications: no account client; the account step waits")
	errCameBack    = errors.New("notifications: a copy of the person came back; the vendor steps run again")
	errRerequested = errors.New("notifications: erasure re-requested")
)

const (
	stepSweep   = "sweep"
	stepAccount = "account"
	stepHubSpot = "hubspot"
	stepResend  = "resend"
	stepFinal   = "final"
)

// eraseKinds are the job kinds that can carry a person.
var eraseKinds = []string{DeliverArgs{}.Kind(), DemoDealArgs{}.Kind(), RegistrantArgs{}.Kind(), BlogSyncArgs{}.Kind()}

// UsableEraseEmail reports whether EnqueueErasure accepts the address.
func UsableEraseEmail(email string) bool {
	n := len(strings.ToLower(strings.TrimSpace(email)))
	return n >= 3 && n <= 254
}

// EnqueueErasure records the erasure and queues its job in tx. A repeat call resets the row and queues another job.
func EnqueueErasure(ctx context.Context, tx pgx.Tx, rc *river.Client[pgx.Tx], userID, email string) error {
	id, err := uuid.Parse(userID)
	if err != nil {
		return errors.New("notifications: erase user id is not a UUID")
	}
	if !UsableEraseEmail(email) {
		return errors.New("notifications: erase email is blank or out of range")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	var gen int64
	err = tx.QueryRow(ctx, `
		INSERT INTO person_erasures (user_id, email) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			email = EXCLUDED.email, requested_at = now(), local_erased_at = NULL, account_erased_at = NULL,
			hubspot_erased_at = NULL, resend_erased_at = NULL, vendor_mode = NULL, superseded_at = NULL,
			finished_at = NULL, generation = person_erasures.generation + 1
		RETURNING generation`, id.String(), email).Scan(&gen)
	if err != nil {
		return fmt.Errorf("notifications: record erasure: %w", err)
	}
	if _, err = rc.InsertTx(ctx, tx, EraseArgs{UserID: id.String(), Generation: gen}, nil); err != nil {
		return fmt.Errorf("notifications: queue erasure: %w", err)
	}
	return nil
}

// EraseWorker erases one person. A nil HubSpot or Resend means contacts are off; a nil Account, no account client.
type EraseWorker struct {
	river.WorkerDefaults[EraseArgs]
	Pool    *pgxpool.Pool
	River   *river.Client[pgx.Tx] // set by the wiring after queue.New
	HubSpot HubSpotEraser
	Resend  ResendEraser
	Account AccountDeleter
	Mode    Mode
	Logger  *slog.Logger
}

func (w *EraseWorker) Timeout(*river.Job[EraseArgs]) time.Duration { return eraseTimeout }

type erasureRow struct {
	email                                string
	generation                           int64
	local, account, hubspot, resend, fin bool
}

// Work runs every unfinished step and returns the first error. Neither the email nor the user id reaches a log line or an error.
func (w *EraseWorker) Work(ctx context.Context, job *river.Job[EraseArgs]) error {
	id, err := uuid.Parse(job.Args.UserID)
	if err != nil {
		return river.JobCancel(errors.New("notifications: erase job has a bad user id"))
	}
	uid := id.String()

	var r erasureRow
	var email *string
	err = w.Pool.QueryRow(ctx, `
		SELECT email, generation, local_erased_at IS NOT NULL, account_erased_at IS NOT NULL,
		       hubspot_erased_at IS NOT NULL, resend_erased_at IS NOT NULL, finished_at IS NOT NULL
		FROM person_erasures WHERE user_id = $1`, uid).Scan(&email, &r.generation, &r.local, &r.account, &r.hubspot, &r.resend, &r.fin)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("notifications: no erasure row for the job"))
	}
	if err != nil {
		return fmt.Errorf("notifications: read erasure: %w", err)
	}
	if r.fin || email == nil || r.generation != job.Args.Generation {
		return nil
	}
	r.email = *email

	steps := []struct {
		name string
		done *bool
		run  func() error
	}{
		{stepSweep, &r.local, func() error { return w.localSweep(ctx, uid, r) }},
		{stepAccount, &r.account, func() error { return w.accountStep(ctx, uid, r) }},
		{stepHubSpot, &r.hubspot, func() error { return w.vendorStep(ctx, uid, r, stepHubSpot) }},
		{stepResend, &r.resend, func() error { return w.vendorStep(ctx, uid, r, stepResend) }},
	}
	var first error
	for _, s := range steps {
		if *s.done {
			continue
		}
		err := s.run()
		switch {
		case err == nil:
			*s.done = true
		case errors.Is(err, errRerequested):
			w.Logger.WarnContext(ctx, "erasure: re-requested during the run; the newer job runs it")
			return nil
		case s.name == stepSweep:
			return err
		case first == nil:
			first = err
		}
	}
	if first != nil {
		return first
	}
	err = w.finalSweep(ctx, uid, r)
	if errors.Is(err, errRerequested) {
		w.Logger.WarnContext(ctx, "erasure: re-requested during the run; the newer job runs it")
		return nil
	}
	return err
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// updateRow updates the row of the generation the attempt read; a miss means a re-request landed.
// set may use $3 onward.
func updateRow(ctx context.Context, q execer, set, uid string, gen int64, args ...any) error {
	tag, err := q.Exec(ctx, `UPDATE person_erasures SET `+set+` WHERE user_id = $1 AND generation = $2`,
		append([]any{uid, gen}, args...)...)
	if err != nil {
		return fmt.Errorf("notifications: update erasure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errRerequested
	}
	return nil
}

func lockDelivery(ctx context.Context, tx pgx.Tx, dest, email string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:' || $1::text || ':' || $2::text, 0))`, dest, email); err != nil {
		return fmt.Errorf("notifications: lock erasure: %w", err)
	}
	return nil
}

type sweepResult struct {
	count      int  // rows deleted plus jobs cancelled
	superseded bool // another account holds the address
}

// sweep removes the person's local copies in tx. Steps 1 and 5 both call it.
func (w *EraseWorker) sweep(ctx context.Context, tx pgx.Tx, uid, email string) (sweepResult, error) {
	var res sweepResult
	for _, dest := range []string{destHubSpot, destResend} {
		if err := lockDelivery(ctx, tx, dest, email); err != nil {
			return res, err
		}
	}
	// Another account on the address keeps its rows and its non-final jobs; finished jobs are history.
	var err error
	if res.superseded, err = holdsAddress(ctx, tx, uid, email); err != nil {
		return res, err
	}

	// ceiling: seq-scans river_job args; index `args->>'email'` above ~100k rows
	byEmail := `kind = ANY($1) AND (lower(btrim(args->>'email')) = $3 OR lower(args->>'user_id') = $2)`
	byEmailArgs := []any{eraseKinds, uid, email}
	liveWhere, liveArgs := byEmail, byEmailArgs
	if res.superseded {
		liveWhere, liveArgs = `kind = ANY($1) AND lower(args->>'user_id') = $2`, []any{eraseKinds, uid}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM river_job WHERE state IN ('available', 'pending', 'retryable', 'running', 'scheduled') AND `+liveWhere+` ORDER BY id`, liveArgs...)
	if err != nil {
		return res, fmt.Errorf("notifications: find jobs: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, fmt.Errorf("notifications: read job: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("notifications: find jobs: %w", err)
	}
	for _, id := range ids {
		if _, err := w.River.JobCancelTx(ctx, tx, id); err != nil {
			return res, fmt.Errorf("notifications: cancel job: %w", err)
		}
	}
	res.count += len(ids)

	// Cancelled first, so the jobs just cancelled go too; a running job stays, marked.
	type del struct {
		sql  string
		args []any
	}
	deletes := []del{
		{`DELETE FROM river_job WHERE state IN ('completed', 'cancelled', 'discarded') AND ` + byEmail, byEmailArgs},
		{`DELETE FROM registrant_handoffs WHERE lower(user_id) = $1`, []any{uid}},
		{`DELETE FROM contacts WHERE email = $1 AND (user_id IS NULL OR user_id::text = $2)`, []any{email, uid}},
	}
	if !res.superseded {
		deletes = append(deletes,
			del{`DELETE FROM blog_subscribers WHERE email = $1`, []any{email}},
			del{`DELETE FROM blog_subscription_requests WHERE email = $1`, []any{email}})
	}
	for _, d := range deletes {
		tag, err := tx.Exec(ctx, d.sql, d.args...)
		if err != nil {
			return res, fmt.Errorf("notifications: delete copies: %w", err)
		}
		res.count += int(tag.RowsAffected())
	}
	// Not in res.count: an invitation made between steps 1 and 5 is removed here without a rerun (TestErase_AnInvitationCreatedMidRunIsRemovedWithoutARerun).
	if _, err := tx.Exec(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2, $3)`, uid, email, !res.superseded); err != nil {
		return res, fmt.Errorf("notifications: erase invitations: %w", err)
	}
	return res, nil
}

func (w *EraseWorker) fail(ctx context.Context, step string, err error) error {
	if err != nil && !errors.Is(err, errRerequested) {
		logEraseFailure(ctx, w.Logger, step, err)
	}
	return err
}

// localSweep is step 1.
func (w *EraseWorker) localSweep(ctx context.Context, uid string, r erasureRow) error {
	return w.fail(ctx, stepSweep, pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		if _, err := w.sweep(ctx, tx, uid, r.email); err != nil {
			return err
		}
		return updateRow(ctx, tx, `local_erased_at = now()`, uid, r.generation)
	}))
}

// accountStep is step 2.
func (w *EraseWorker) accountStep(ctx context.Context, uid string, r erasureRow) error {
	if w.Account == nil {
		w.Logger.WarnContext(ctx, "erasure: no account client; the account step waits", slog.String("step", stepAccount))
		return errAccountOff
	}
	if err := w.Account.DeleteUser(ctx, uid); err != nil {
		return w.fail(ctx, stepAccount, err)
	}
	return w.fail(ctx, stepAccount, updateRow(ctx, w.Pool, `account_erased_at = now()`, uid, r.generation))
}

// vendorStep is step 3 (hubspot) or 4 (resend).
func (w *EraseWorker) vendorStep(ctx context.Context, uid string, r erasureRow, step string) error {
	var call func(context.Context, string) error
	switch {
	case step == stepHubSpot && w.HubSpot != nil:
		call = w.HubSpot.EraseContact
	case step == stepResend && w.Resend != nil:
		call = w.Resend.EraseContact
	default:
		w.Logger.WarnContext(ctx, "erasure: contacts are off; the vendor step waits", slog.String("step", step))
		return errContactsOff
	}
	// ceiling: the lock and its connection are held across the vendor erase; a same-email delivery waits on it
	return w.fail(ctx, step, pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		if err := lockDelivery(ctx, tx, step, r.email); err != nil {
			return err
		}
		// The delete wipes the unsubscribe and the topic opt-outs; read them first for the rebuild.
		var st ResendState
		if step == stepResend {
			var err error
			if st, err = w.Resend.ContactState(ctx, r.email); err != nil {
				return err
			}
			if st, err = w.failSafeState(ctx, tx, uid, r.email, st); err != nil {
				return err
			}
		}
		if err := call(ctx, r.email); err != nil {
			return err
		}
		if err := updateRow(ctx, tx, `vendor_mode = $3, `+step+`_erased_at = now()`, uid, r.generation, string(w.Mode)); err != nil {
			return err
		}
		return w.rebuild(ctx, tx, uid, r.email, step, st)
	}))
}

// failSafeState covers a lost state: another account's contact was delivered to Resend, yet Resend has none.
// An earlier attempt deleted it and failed before the restore. Treat the state as unsubscribed
// (TestErase_ARetryAfterAFailedRestoreRestoresUnsubscribed).
func (w *EraseWorker) failSafeState(ctx context.Context, tx pgx.Tx, uid, email string, st ResendState) (ResendState, error) {
	if !st.Missing {
		return st, nil
	}
	held, err := holdsAddress(ctx, tx, uid, email)
	if err != nil || !held {
		return st, err
	}
	var lost bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM contacts WHERE email = $1 AND user_id IS NOT NULL AND user_id::text <> $2 AND resend_delivered_at IS NOT NULL)
		    OR EXISTS (SELECT 1 FROM blog_subscribers WHERE email = $1 AND resend_delivered_at IS NOT NULL)`,
		email, uid).Scan(&lost)
	if err != nil {
		return st, fmt.Errorf("notifications: check lost state: %w", err)
	}
	if !lost {
		return st, nil
	}
	w.Logger.WarnContext(ctx, "erasure: the Resend state is unknown; the contact is restored unsubscribed")
	return ResendState{Unsubscribed: true}, nil
}

// holdsAddress reports whether another account holds the address: a contacts row, a hand-off or a non-final intake job.
func holdsAddress(ctx context.Context, tx pgx.Tx, uid, email string) (bool, error) {
	var held bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM contacts WHERE email = $1 AND user_id IS NOT NULL AND user_id::text <> $2)
		    OR EXISTS (SELECT 1 FROM registrant_handoffs WHERE lower(btrim(email)) = $1 AND lower(user_id) <> $2)
		    OR EXISTS (SELECT 1 FROM river_job WHERE kind = $3 AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
		               AND lower(btrim(args->>'email')) = $1 AND lower(args->>'user_id') <> $2)`,
		email, uid, RegistrantArgs{}.Kind()).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("notifications: check address: %w", err)
	}
	return held, nil
}

// rebuild re-queues the deliveries of another account that holds the address, whose vendor copy the erase removed.
// For Resend it re-creates the contact with the unsubscribe and opt-outs the erase found (TestErase_AnUnsubscribedContactStaysUnsubscribed).
func (w *EraseWorker) rebuild(ctx context.Context, tx pgx.Tx, uid, email, dest string, st ResendState) error {
	rebuilt := false
	var version int64
	var first, last string
	set := dest + `_delivered_at = NULL`
	if dest == destResend && !st.Unsubscribed && !st.MarketingOut {
		// The delete removed the Marketing opt_in; re-apply the one the row records (TestErase_ACleanContactGetsItsMarketingOptInBack).
		set += `, resend_opt_in_sent_at = NULL`
	}
	err := tx.QueryRow(ctx, `UPDATE contacts SET `+set+`
		WHERE email = $1 AND user_id IS NOT NULL AND user_id::text <> $2
		RETURNING version, COALESCE(first_name, ''), COALESCE(last_name, '')`, email, uid).Scan(&version, &first, &last)
	switch {
	case err == nil:
		if _, err = w.River.InsertTx(ctx, tx, DeliverArgs{Email: email, Destination: dest, Version: version}, nil); err != nil {
			return fmt.Errorf("notifications: queue rebuild: %w", err)
		}
		rebuilt = true
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("notifications: rebuild contact: %w", err)
	}
	if dest == destResend {
		held, err := holdsAddress(ctx, tx, uid, email)
		if err != nil {
			return err
		}
		// A blog sync opts the contact in to Blog; it must not undo an unsubscribe or a Blog opt-out.
		if held && !st.Unsubscribed && !st.otherTopicOut() {
			err = tx.QueryRow(ctx, `UPDATE blog_subscribers SET resend_delivered_at = NULL WHERE email = $1 RETURNING version`, email).Scan(&version)
			if err == nil {
				_, err = w.River.InsertTx(ctx, tx, BlogSyncArgs{Email: email, Version: version}, nil)
				rebuilt = true
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("notifications: rebuild blog subscriber: %w", err)
			}
		}
		if held && !st.zero() {
			if err := w.Resend.RestoreContact(ctx, email, first, last, st); err != nil {
				return err
			}
		}
	}
	if rebuilt {
		w.Logger.WarnContext(ctx, "erasure: the address registered again; its vendor contact is rebuilt")
	}
	return nil
}

// finalSweep is step 5: a copy that came back clears the vendor stamps and fails the attempt.
func (w *EraseWorker) finalSweep(ctx context.Context, uid string, r erasureRow) error {
	var cameBack bool
	err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		res, err := w.sweep(ctx, tx, uid, r.email)
		if err != nil {
			return err
		}
		if res.count > 0 {
			cameBack = true
			return updateRow(ctx, tx, `hubspot_erased_at = NULL, resend_erased_at = NULL`, uid, r.generation)
		}
		return updateRow(ctx, tx, `finished_at = now(), email = NULL, superseded_at = CASE WHEN $3 THEN now() END`, uid, r.generation, res.superseded)
	})
	if err == nil && cameBack {
		w.Logger.WarnContext(ctx, "erasure: a copy came back; the vendor steps run again", slog.String("step", stepFinal))
		return errCameBack
	}
	return w.fail(ctx, stepFinal, err)
}

// logEraseFailure logs the step and the status only: ERROR for a permanent 4xx, WARN otherwise.
func logEraseFailure(ctx context.Context, log *slog.Logger, step string, err error) {
	var de *DeliveryError
	var se *gotrueadmin.StatusError
	status := 0
	switch {
	case errors.As(err, &de):
		status = de.Status
	case errors.As(err, &se):
		status = se.Status
	}
	if status >= 400 && status < 500 && status != 408 && status != 429 {
		log.ErrorContext(ctx, "erasure: "+step+" rejected the erase", slog.String("step", step), slog.Int("status", status))
		return
	}
	log.WarnContext(ctx, "erasure: "+step+" failed", slog.String("step", step), slog.Int("status", status))
}

// AddEraseTo registers the erase worker on the worker's one bundle.
func AddEraseTo(bundle *river.Workers, w *EraseWorker) { river.AddWorker(bundle, w) }
