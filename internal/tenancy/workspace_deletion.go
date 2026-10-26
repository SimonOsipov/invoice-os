package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ScheduleDeletionFunc schedules the caller's workspace for deletion and returns the due time.
type ScheduleDeletionFunc func(ctx context.Context, typedName string) (time.Time, error)

// CancelDeletionFunc clears the caller's workspace deletion schedule.
type CancelDeletionFunc func(ctx context.Context) error

// maxScheduleDeletionBodyBytes bounds the schedule body before it is decoded.
const maxScheduleDeletionBodyBytes = 4 << 10

// ScheduleWorkspaceDeletion locks the caller's workspace for deletion 30 days out. The caller must be an
// active admin who types the exact name; the audit row commits in the same transaction.
func (s *Store) ScheduleWorkspaceDeletion(ctx context.Context, typedName string) (time.Time, error) {
	var due time.Time
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireActiveAdminCaller(ctx, tx, caller.Subject, ErrDeleteWorkspaceNotPermitted); err != nil {
			return err
		}
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&name); err != nil {
			return err
		}
		if typedName != name {
			return ErrWorkspaceNameMismatch
		}
		// The tenants_deletion_guard trigger derives deletion_due_at.
		err := tx.QueryRow(ctx,
			`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $1
			  WHERE deletion_scheduled_at IS NULL RETURNING deletion_due_at`, caller.Subject).Scan(&due)
		if err != nil {
			var pgErr *pgconn.PgError
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return db.ErrWorkspaceLocked
			case errors.As(err, &pgErr) && pgErr.ConstraintName == "house_tenant_not_deletable":
				return ErrHouseWorkspace
			}
			return err
		}
		return audit.Record(ctx, tx, caller.Subject, "workspace.deletion_scheduled",
			map[string]any{"due_at": due.UTC().Format(time.RFC3339)})
	})
	if err != nil {
		return time.Time{}, err
	}
	return due, nil
}

// CancelWorkspaceDeletion clears a schedule. It skips the seam, which refuses a locked workspace,
// and gates the active admin itself. A workspace that is not scheduled is a no-op.
func (s *Store) CancelWorkspaceDeletion(ctx context.Context) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	if _, err := uuid.Parse(id.Subject); err != nil {
		return db.ErrNoTenant
	}
	return db.WithinTenantTx(ctx, s.pool, id.TenantID, func(tx pgx.Tx) error {
		var status, role string
		if err := tx.QueryRow(ctx,
			`SELECT status, role FROM memberships WHERE user_id = $1`, id.Subject,
		).Scan(&status, &role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.ErrNotActiveMember
			}
			return err
		}
		if status != "active" {
			return db.ErrNotActiveMember
		}
		if role != "admin" {
			return ErrCancelDeletionNotPermitted
		}
		var due *time.Time
		if err := tx.QueryRow(ctx, `SELECT deletion_due_at FROM tenants FOR UPDATE`).Scan(&due); err != nil {
			return err
		}
		if due == nil {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`); err != nil {
			return err
		}
		return audit.Record(ctx, tx, id.Subject, "workspace.deletion_cancelled",
			map[string]any{"due_at": due.UTC().Format(time.RFC3339)})
	})
}

type scheduleDeletionRequest struct {
	WorkspaceName string `json:"workspace_name"`
}

// ScheduleDeletionHandler returns POST /v1/workspace/deletion: identity (401), capped decode (400),
// schedule, 200 with the due time. The typed name reaches the store unchanged.
func ScheduleDeletionHandler(schedule ScheduleDeletionFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxScheduleDeletionBodyBytes)
		var req scheduleDeletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		due, err := schedule(r.Context(), req.WorkspaceName)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: schedule workspace deletion", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"deletion_due_at": due.UTC().Format(time.RFC3339)})
	}
}

// CancelDeletionHandler returns POST /v1/workspace/deletion/cancel: identity (401), cancel,
// 200 `{"deletion_due_at": null}`.
func CancelDeletionHandler(cancel CancelDeletionFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := cancel(r.Context()); err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: cancel workspace deletion", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, map[string]*string{"deletion_due_at": nil})
	}
}
