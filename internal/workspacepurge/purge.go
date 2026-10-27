// Package workspacepurge erases a workspace whose deletion is due: its members, its files, then its rows.
package workspacepurge

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

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// staff_audit_log.actor is NOT NULL; the worker is no person.
var workerActor = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://ascomply.com/actors/workspace-purge"))

var errNoAccountReader = errors.New("no account client to read a member's email")

const (
	stepErase = "erase"
	stepFiles = "files"
	stepPurge = "purge"
)

// AccountReader reads the email a member's sign-in account holds; "" means the account is gone.
type AccountReader interface {
	UserEmail(ctx context.Context, userID string) (string, error)
}

// Purger purges due workspaces. Pool is invoice_app; Reader is invoice_tenant_reader and only dueTenants uses it.
type Purger struct {
	Pool     *pgxpool.Pool
	Reader   *pgxpool.Pool
	River    *river.Client[pgx.Tx]
	Objects  document.ObjectStore
	Accounts AccountReader
	Logger   *slog.Logger // nil = slog.Default()
}

// Result counts what one Purge did: erasures queued and objects deleted by this run.
type Result struct {
	Purged  bool
	Members int
	Objects int
}

type stepError struct {
	tenantID, step string
	err            error
}

func (e *stepError) Error() string {
	return fmt.Sprintf("workspace purge: tenant %s: %s: %v", e.tenantID, e.step, e.err)
}

func (e *stepError) Unwrap() error { return e.err }

// RunOnce purges every due tenant in due order. One tenant's failure does not stop the others; a done ctx does.
func (p *Purger) RunOnce(ctx context.Context) error {
	log := p.Logger
	if log == nil {
		log = slog.Default()
	}
	tenants, err := dueTenants(ctx, p.Reader)
	if err != nil {
		return fmt.Errorf("workspace purge: enumerate due tenants: %w", err)
	}
	var failures []error
	for _, id := range tenants {
		if err := p.purgeLogged(ctx, log, id); err != nil {
			if ctx.Err() != nil { // shutdown, not a tenant fault
				break
			}
			failures = append(failures, err)
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, fmt.Errorf("workspace purge: interrupted: %w", err))
	}
	return errors.Join(failures...)
}

// purgeLogged runs Purge for one tenant and logs the outcome; a failure is logged unless ctx is done.
func (p *Purger) purgeLogged(ctx context.Context, log *slog.Logger, id string) error {
	res, err := p.Purge(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			step := ""
			var se *stepError
			if errors.As(err, &se) {
				step = se.step
			}
			log.ErrorContext(ctx, "workspace purge: tenant failed", "tenant_id", id, "step", step, "error", err)
		}
		return err
	}
	if res.Purged {
		log.InfoContext(ctx, "workspace purge: purged", "tenant_id", id, "members", res.Members, "objects", res.Objects)
	}
	return nil
}

// lockTenant takes the session advisory lock for one tenant's purge on a held connection.
// ok is false when another runner holds it; release frees the lock and the connection.
func (p *Purger) lockTenant(ctx context.Context, tenantID string) (release func(), ok bool, err error) {
	conn, err := p.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('workspace_purge:' || $1::text, 0))`, tenantID).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return func() {
		var unlocked bool
		uctx := context.WithoutCancel(ctx)
		if err := conn.QueryRow(uctx, `SELECT pg_advisory_unlock(hashtextextended('workspace_purge:' || $1::text, 0))`, tenantID).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Conn().Close(uctx) // closing the session frees the lock; the pool drops the dead conn
		}
		conn.Release()
	}, true, nil
}

// Purge runs the three steps for one tenant. A tenant that is not due, or that another runner is purging, returns Result{} and nil.
func (p *Purger) Purge(ctx context.Context, tenantID string) (Result, error) {
	release, ok, err := p.lockTenant(ctx, tenantID)
	if err != nil {
		return Result{}, &stepError{tenantID, stepErase, err}
	}
	if !ok {
		return Result{}, nil
	}
	defer release()
	log := p.Logger
	if log == nil {
		log = slog.Default()
	}
	members, byID, due, err := p.eraseMembers(ctx, tenantID)
	if err != nil {
		return Result{}, &stepError{tenantID, stepErase, err}
	}
	if !due {
		return Result{}, nil
	}
	for range byID {
		log.WarnContext(ctx, "workspace purge: a member had no usable email; erased by user id")
	}

	// ceiling: an extraction job running now can write a page after this step; revisit if orphan pages show up.
	objects, err := document.DeleteTenantObjects(ctx, p.Objects, tenantID)
	if err != nil {
		return Result{}, &stepError{tenantID, stepFiles, err}
	}

	purged, err := p.purgeRows(ctx, tenantID, members, objects)
	if err != nil {
		return Result{}, &stepError{tenantID, stepPurge, err}
	}
	if !purged {
		return Result{}, nil
	}
	return Result{Purged: true, Members: members, Objects: objects}, nil
}

// dueTenants lists the tenants whose deletion is due or already started, earliest first.
func dueTenants(ctx context.Context, reader *pgxpool.Pool) ([]string, error) {
	rows, err := reader.Query(ctx, `SELECT id::text FROM tenants WHERE deletion_due_at <= now() OR deletion_purge_started_at IS NOT NULL ORDER BY deletion_due_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// lockDue takes the tenant row; false means the tenant is not due or started (never scheduled, cancelled or purged).
func lockDue(ctx context.Context, tx pgx.Tx) (time.Time, bool, error) {
	var due time.Time
	err := tx.QueryRow(ctx, `SELECT deletion_due_at FROM tenants WHERE deletion_due_at <= now() OR deletion_purge_started_at IS NOT NULL FOR UPDATE`).Scan(&due)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return due, err == nil, err
}

type member struct {
	userID string
	email  string // "" with byID set
	byID   bool
}

// eraseMembers queues one erasure per non-removed member and sets the purge marker, in one transaction.
// It returns the queued count, how many were by user id, and false when the tenant is not due.
func (p *Purger) eraseMembers(ctx context.Context, tenantID string) (queued, byID int, due bool, err error) {
	err = db.WithinTenantTx(ctx, p.Pool, tenantID, func(tx pgx.Tx) error {
		_, ok, err := lockDue(ctx, tx)
		if err != nil || !ok {
			return err
		}
		due = true
		rows, err := tx.Query(ctx, `SELECT user_id::text, email FROM memberships WHERE status <> 'removed' ORDER BY user_id`)
		if err != nil {
			return err
		}
		var ms []member
		for rows.Next() {
			var m member
			var email *string
			if err := rows.Scan(&m.userID, &email); err != nil {
				rows.Close()
				return err
			}
			if email != nil {
				m.email = *email
			}
			ms = append(ms, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// ceiling: one GoTrue read per member without a usable email, under the tenant row lock; revisit if a purge holds the lock for seconds.
		for i := range ms {
			if notifications.UsableEraseEmail(ms[i].email) {
				continue
			}
			if p.Accounts == nil {
				return errNoAccountReader
			}
			got, err := p.Accounts.UserEmail(ctx, ms[i].userID)
			if err != nil {
				return fmt.Errorf("read a member's account email: %w", err)
			}
			if notifications.UsableEraseEmail(got) {
				ms[i].email = got
			} else {
				ms[i].email, ms[i].byID = "", true
			}
		}

		if _, err := tx.Exec(ctx, `UPDATE tenants SET deletion_purge_started_at = now() WHERE deletion_purge_started_at IS NULL`); err != nil {
			return err
		}
		for _, m := range ms {
			if m.byID {
				err = notifications.EnqueueErasureByID(ctx, tx, p.River, m.userID)
				byID++
			} else {
				err = notifications.EnqueueErasure(ctx, tx, p.River, m.userID, m.email)
			}
			if err != nil {
				return err
			}
		}
		queued = len(ms)
		return nil
	})
	if err != nil {
		return 0, 0, false, err
	}
	return queued, byID, due, nil
}

// purgeRows writes the staff row, then removes the tenant's rows, in one transaction.
// It returns false when the tenant is no longer due (another run purged it, or it was cancelled).
func (p *Purger) purgeRows(ctx context.Context, tenantID string, members, objects int) (purged bool, err error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return false, err
	}
	err = db.WithinTenantTx(ctx, p.Pool, tenantID, func(tx pgx.Tx) error {
		due, ok, err := lockDue(ctx, tx)
		if err != nil || !ok {
			return err
		}
		// Before purge_tenant: its audit step removes the tenant row RecordStaff also writes.
		if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor:    workerActor,
			Event:    "workspace.purged",
			TenantID: id,
			Payload: map[string]any{
				"due_at":  due.UTC().Format(time.RFC3339),
				"members": members,
				"objects": objects,
			},
		}); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT public.purge_tenant($1::uuid)`, tenantID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("purge_tenant returned %d, want 1", n)
		}
		purged = true
		return nil
	})
	return purged && err == nil, err
}
