package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/workspacepurge/purgeargs"
)

const (
	maxStaffBodyBytes   = 8 << 10
	maxStaffReasonRunes = 500
)

// withinStaffTenant proves the path tenant through staff_read_tenants in WithinStaffTx, then runs fn in that tenant's transaction.
// A malformed id answers ErrTenantNotFound before any statement.
func (s *Store) withinStaffTenant(ctx context.Context, tenantID string, opts pgx.TxOptions, fn func(tx pgx.Tx, tenant, actor uuid.UUID) error) error {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return ErrTenantNotFound
	}
	var found bool
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.staff_read_tenants($1) WHERE id = $2)`, actor, id).Scan(&found)
		return db.StaffReadError("tenancy: staff tenant check", qerr)
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrTenantNotFound
	}
	if s.afterStaffCheck != nil {
		s.afterStaffCheck()
	}
	return db.WithinTenantTxOpts(ctx, s.pool, id.String(), opts, func(tx pgx.Tx) error { return fn(tx, id, actor) })
}

// readStaffBody decodes one JSON object into dst and writes the error response itself.
func readStaffBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxStaffBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

// The staffjobs rule (readReason); one copy per package.
// jsonb refuses U+0000, so the audit write would otherwise fail as a 500.
func staffReason(raw string) (string, bool) {
	reason := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxStaffReasonRunes || strings.ContainsRune(reason, 0) {
		return "", false
	}
	return reason, true
}

type staffReasonRequest struct {
	Reason string `json:"reason"`
}

// scheduleTenantDeletion is the DEL-04 schedule with the staff member as actor.
func scheduleTenantDeletion(ctx context.Context, tx pgx.Tx, actor uuid.UUID) (time.Time, error) {
	var due time.Time
	// The tenants_deletion_guard trigger derives deletion_due_at.
	err := tx.QueryRow(ctx,
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $1
		  WHERE deletion_scheduled_at IS NULL RETURNING deletion_due_at`, actor).Scan(&due)
	if err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No row: already scheduled, or the tenant vanished after the staff check.
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants)`).Scan(&exists); qerr != nil {
				return time.Time{}, qerr
			}
			if !exists {
				return time.Time{}, ErrTenantNotFound
			}
			return time.Time{}, db.ErrWorkspaceLocked
		case errors.As(err, &pgErr) && pgErr.ConstraintName == "house_tenant_not_deletable":
			return time.Time{}, ErrHouseWorkspace
		}
		return time.Time{}, err
	}
	return due, nil
}

// StaffScheduleWorkspaceDeletion schedules the path tenant's deletion; the staff row and one cancel link
// per active admin with an email commit in the same transaction.
func (s *Store) StaffScheduleWorkspaceDeletion(ctx context.Context, tenantID, reason string) (ScheduledDeletion, error) {
	var out ScheduledDeletion
	err := s.withinStaffTenant(ctx, tenantID, pgx.TxOptions{}, func(tx pgx.Tx, tenant, actor uuid.UUID) error {
		due, err := scheduleTenantDeletion(ctx, tx, actor)
		if err != nil {
			return err
		}
		if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor: actor, Event: "workspace.deletion_scheduled", TenantID: tenant,
			Payload: map[string]any{"due_at": due.UTC().Format(time.RFC3339), "reason": reason},
		}); err != nil {
			return err
		}
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&name); err != nil {
			return err
		}
		// The staff actor matches no member, so every active admin with an email gets a link.
		links, err := issueDeletionLinks(ctx, tx, tenant.String(), actor.String(), due)
		if err != nil {
			return err
		}
		out = ScheduledDeletion{DueAt: due, Workspace: name, Requester: audit.SupportActor, Links: links}
		return nil
	})
	if err != nil {
		return ScheduledDeletion{}, err
	}
	return out, nil
}

// StaffCancelWorkspaceDeletion clears a schedule and writes one cancel-mail row per active admin with an email.
// A tenant that is not scheduled is a no-op with a zero result and no audit row.
func (s *Store) StaffCancelWorkspaceDeletion(ctx context.Context, tenantID, reason string) (CancelledDeletion, error) {
	var out CancelledDeletion
	err := s.withinStaffTenant(ctx, tenantID, pgx.TxOptions{}, func(tx pgx.Tx, tenant, actor uuid.UUID) error {
		out = CancelledDeletion{}
		var due *time.Time
		var name string
		if err := tx.QueryRow(ctx, `SELECT deletion_due_at, name FROM tenants FOR UPDATE`).Scan(&due, &name); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTenantNotFound // purged after the staff check
			}
			return err
		}
		if due == nil {
			return nil
		}
		if err := clearDeletionSchedule(ctx, tx); err != nil {
			return err
		}
		if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor: actor, Event: "workspace.deletion_cancelled", TenantID: tenant,
			Payload: map[string]any{"due_at": due.UTC().Format(time.RFC3339), "reason": reason},
		}); err != nil {
			return err
		}
		mails, err := issueCancelMails(ctx, tx, tenant.String(), actor.String(), *due)
		if err != nil {
			return err
		}
		out = CancelledDeletion{DueAt: *due, Workspace: name, Canceller: audit.SupportActor, Mails: mails}
		return nil
	})
	if err != nil {
		return CancelledDeletion{}, err
	}
	return out, nil
}

// staffDeletionStatus maps a staff deletion error; the caller logs the 500.
func staffDeletionStatus(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrNotStaff):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, ErrTenantNotFound):
		return http.StatusNotFound, "tenant not found"
	case errors.Is(err, ErrMembershipNotFound):
		return http.StatusNotFound, "membership not found"
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusConflict, db.WorkspaceLockedMessage
	case errors.Is(err, ErrWorkspaceNameMismatch):
		return http.StatusBadRequest, "the workspace name does not match"
	case errors.Is(err, ErrHouseWorkspace):
		return http.StatusConflict, "the ASComply staff workspace cannot be deleted"
	case errors.Is(err, ErrDeletionUnderway):
		return http.StatusConflict, "this workspace is already being deleted"
	case errors.Is(err, ErrDeletionOutcomeChanged):
		return http.StatusConflict, "the workspace changed; review the page and confirm again"
	case errors.Is(err, ErrLastActiveMember):
		return http.StatusConflict, "this is the last active member; start a workspace deletion instead"
	}
	return http.StatusInternalServerError, "internal server error"
}

func writeStaffDeletionError(w http.ResponseWriter, log *slog.Logger, op string, err error) {
	status, msg := staffDeletionStatus(err)
	if status == http.StatusInternalServerError {
		log.Error(op, "err", err)
	}
	writeError(w, status, msg)
}

func readStaffReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req staffReasonRequest
	if !readStaffBody(w, r, &req) {
		return "", false
	}
	reason, ok := staffReason(req.Reason)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("reason must be 1 to %d characters", maxStaffReasonRunes))
	}
	return reason, ok
}

// StaffScheduleDeletionHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffScheduleDeletionHandler(schedule func(ctx context.Context, tenantID, reason string) (time.Time, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reason, ok := readStaffReason(w, r)
		if !ok {
			return
		}
		due, err := schedule(r.Context(), r.PathValue("id"), reason)
		if err != nil {
			writeStaffDeletionError(w, log, "tenancy: staff schedule deletion response", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deletion_due_at": due.UTC().Format(time.RFC3339)})
	}
}

// StaffCancelDeletionHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffCancelDeletionHandler(cancel func(ctx context.Context, tenantID, reason string) error, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reason, ok := readStaffReason(w, r)
		if !ok {
			return
		}
		if err := cancel(r.Context(), r.PathValue("id"), reason); err != nil {
			writeStaffDeletionError(w, log, "tenancy: staff cancel deletion response", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deletion_due_at": nil})
	}
}

// StaffPurgeWorkspace starts the purge of the path tenant now, scheduling it first when it is not.
// The staff row and the workspace_purge job commit with the marker.
func (s *Store) StaffPurgeWorkspace(ctx context.Context, tenantID, typedName, reason string) (time.Time, error) {
	if s.eraseQueue == nil {
		return time.Time{}, errNoEraseQueue
	}
	var startedAt time.Time
	err := s.withinStaffTenant(ctx, tenantID, pgx.TxOptions{}, func(tx pgx.Tx, tenant, actor uuid.UUID) error {
		var name string
		var scheduled, started bool
		err := tx.QueryRow(ctx, `SELECT name, deletion_scheduled_at IS NOT NULL, deletion_purge_started_at IS NOT NULL FROM tenants FOR UPDATE`).
			Scan(&name, &scheduled, &started)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTenantNotFound // purged after the staff check
		}
		if err != nil {
			return err
		}
		if typedName != name {
			return ErrWorkspaceNameMismatch
		}
		if started {
			return ErrDeletionUnderway
		}
		if !scheduled {
			if _, err := scheduleTenantDeletion(ctx, tx, actor); err != nil {
				return err
			}
		}
		// The staff decision is final: the marker closes every cancel before the worker starts.
		if err := tx.QueryRow(ctx, `UPDATE tenants SET deletion_purge_started_at = now() RETURNING deletion_purge_started_at`).Scan(&startedAt); err != nil {
			return err
		}
		if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor: actor, Event: "workspace.purge_started", TenantID: tenant,
			Payload: map[string]any{"reason": reason},
		}); err != nil {
			return err
		}
		_, err = s.eraseQueue.InsertTx(ctx, tx, purgeargs.Args{TenantID: tenant.String()}, nil)
		return err
	})
	if err != nil {
		return time.Time{}, err
	}
	return startedAt, nil
}

type staffPurgeRequest struct {
	WorkspaceName string `json:"workspace_name"`
	Reason        string `json:"reason"`
}

// StaffPurgeHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffPurgeHandler(purge func(ctx context.Context, tenantID, typedName, reason string) (time.Time, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req staffPurgeRequest
		if !readStaffBody(w, r, &req) {
			return
		}
		reason, ok := staffReason(req.Reason)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("reason must be 1 to %d characters", maxStaffReasonRunes))
			return
		}
		at, err := purge(r.Context(), r.PathValue("id"), req.WorkspaceName, reason)
		if err != nil {
			writeStaffDeletionError(w, log, "tenancy: staff purge response", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deletion_purge_started_at": at.UTC().Format(time.RFC3339)})
	}
}
