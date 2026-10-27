package tenancy

import (
	"context"
	"crypto/sha256"
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

// CancelDeletionByLinkFunc cancels the caller's workspace deletion with a link token.
type CancelDeletionByLinkFunc func(ctx context.Context, token string) error

// maxScheduleDeletionBodyBytes bounds the schedule body before it is decoded.
const maxScheduleDeletionBodyBytes = 4 << 10

// IssuedDeletionLink is one minted cancel link. Token exists only here, for the mail.
type IssuedDeletionLink struct{ ID, Email, Token string }

// ScheduledDeletion is the outcome of a schedule: the due time and the links to mail.
type ScheduledDeletion struct {
	DueAt                time.Time
	Workspace, Requester string
	Links                []IssuedDeletionLink
}

// IssuedCancelMail is one cancel-mail row to send.
type IssuedCancelMail struct{ ID, Email string }

// CancelledDeletion is the outcome of a cancel: the cancelled due time and the mail to send.
type CancelledDeletion struct {
	DueAt                time.Time
	Workspace, Canceller string
	Mails                []IssuedCancelMail
}

// ScheduleWorkspaceDeletion locks the caller's workspace for deletion 30 days out. The caller must be an
// active admin who types the exact name; the audit row and one cancel link per other active admin
// commit in the same transaction.
func (s *Store) ScheduleWorkspaceDeletion(ctx context.Context, typedName string) (ScheduledDeletion, error) {
	var out ScheduledDeletion
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
		if err := audit.Record(ctx, tx, caller.Subject, "workspace.deletion_scheduled",
			map[string]any{"due_at": due.UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		var requester string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(display_name, ''), email, '') FROM memberships WHERE user_id = $1`,
			caller.Subject).Scan(&requester); err != nil {
			return err
		}
		links, err := issueDeletionLinks(ctx, tx, caller.TenantID, caller.Subject, due)
		if err != nil {
			return err
		}
		out = ScheduledDeletion{DueAt: due, Workspace: name, Requester: requester, Links: links}
		return nil
	})
	if err != nil {
		return ScheduledDeletion{}, err
	}
	return out, nil
}

type adminRecipient struct{ id, email string }

// otherActiveAdmins lists the active admins with an email other than subject, in a stable order.
func otherActiveAdmins(ctx context.Context, tx pgx.Tx, subject string) ([]adminRecipient, error) {
	rows, err := tx.Query(ctx,
		`SELECT user_id::text, email FROM memberships
		  WHERE role = 'admin' AND status = 'active' AND user_id <> $1 AND email IS NOT NULL
		  ORDER BY user_id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var admins []adminRecipient
	for rows.Next() {
		var a adminRecipient
		if err := rows.Scan(&a.id, &a.email); err != nil {
			return nil, err
		}
		admins = append(admins, a)
	}
	return admins, rows.Err()
}

// issueDeletionLinks writes one link per other active admin with an email; the link dies with the due time.
func issueDeletionLinks(ctx context.Context, tx pgx.Tx, tenantID, subject string, due time.Time) ([]IssuedDeletionLink, error) {
	admins, err := otherActiveAdmins(ctx, tx, subject)
	if err != nil {
		return nil, err
	}
	var links []IssuedDeletionLink
	for _, a := range admins {
		tok, hash, err := mintToken()
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO workspace_deletion_links (tenant_id, recipient_user_id, token_hash, expires_at)
			 VALUES ($1, $2, $3, $4) RETURNING id::text`, tenantID, a.id, hash, due).Scan(&id); err != nil {
			return nil, err
		}
		links = append(links, IssuedDeletionLink{ID: id, Email: a.email, Token: tok})
	}
	return links, nil
}

// issueCancelMails writes one cancel-mail row per other active admin with an email.
func issueCancelMails(ctx context.Context, tx pgx.Tx, tenantID, subject string, due time.Time) ([]IssuedCancelMail, error) {
	admins, err := otherActiveAdmins(ctx, tx, subject)
	if err != nil {
		return nil, err
	}
	mails := make([]IssuedCancelMail, 0, len(admins))
	for _, a := range admins {
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO workspace_deletion_cancel_mails (tenant_id, recipient_user_id, cancelled_due_at)
			 VALUES ($1, $2, $3) RETURNING id::text`, tenantID, a.id, due).Scan(&id); err != nil {
			return nil, err
		}
		mails = append(mails, IssuedCancelMail{ID: id, Email: a.email})
	}
	return mails, nil
}

// CancelWorkspaceDeletion clears a schedule and returns the mail to send. A workspace that is not
// scheduled is a no-op with a zero result.
func (s *Store) CancelWorkspaceDeletion(ctx context.Context) (CancelledDeletion, error) {
	return s.cancelScheduledDeletion(ctx, nil)
}

// CancelWorkspaceDeletionByLink cancels with a link token of the deletion.
func (s *Store) CancelWorkspaceDeletionByLink(ctx context.Context, token string) (CancelledDeletion, error) {
	return s.cancelScheduledDeletion(ctx, &token)
}

// cancelScheduledDeletion skips the seam, which refuses a locked workspace, and gates the active admin itself.
func (s *Store) cancelScheduledDeletion(ctx context.Context, token *string) (CancelledDeletion, error) {
	var out CancelledDeletion
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return out, db.ErrNoTenant
	}
	if _, err := uuid.Parse(id.Subject); err != nil {
		return out, db.ErrNoTenant
	}
	err := db.WithinTenantTx(ctx, s.pool, id.TenantID, func(tx pgx.Tx) error {
		out = CancelledDeletion{}
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
		// Unscheduled is a no-op on the plain route. A link is a no-op only when the token is one of
		// this tenant's own (the lost race); any other token is a refusal.
		if due == nil {
			if token == nil {
				return nil
			}
			if !inviteTokenShape.MatchString(*token) {
				return ErrDeletionLinkNotValid
			}
			hash := sha256.Sum256([]byte(*token))
			var held bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM workspace_deletion_links WHERE token_hash = $1)`,
				hash[:]).Scan(&held); err != nil {
				return err
			}
			if !held {
				return ErrDeletionLinkNotValid
			}
			return nil
		}
		if token != nil {
			if !inviteTokenShape.MatchString(*token) {
				return ErrDeletionLinkNotValid
			}
			hash := sha256.Sum256([]byte(*token))
			var live bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM workspace_deletion_links
				  WHERE token_hash = $1 AND expires_at = $2 AND expires_at > now())`,
				hash[:], *due).Scan(&live); err != nil {
				return err
			}
			if !live {
				return ErrDeletionLinkNotValid
			}
		}
		if err := clearDeletionSchedule(ctx, tx); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, id.Subject, "workspace.deletion_cancelled",
			map[string]any{"due_at": due.UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		var workspace, canceller string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&workspace); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(display_name, ''), email, '') FROM memberships WHERE user_id = $1`,
			id.Subject).Scan(&canceller); err != nil {
			return err
		}
		mails, err := issueCancelMails(ctx, tx, id.TenantID, id.Subject, *due)
		if err != nil {
			return err
		}
		out = CancelledDeletion{DueAt: *due, Workspace: workspace, Canceller: canceller, Mails: mails}
		return nil
	})
	if err != nil {
		return CancelledDeletion{}, err
	}
	return out, nil
}

// clearDeletionSchedule is the only clear of a schedule. The tenants_purge_guard trigger refuses
// it once the purge started.
func clearDeletionSchedule(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "tenant_deletion_underway" {
		return ErrDeletionUnderway
	}
	return err
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

type cancelByLinkRequest struct {
	Token string `json:"token"`
}

// CancelDeletionByLinkHandler returns POST /v1/workspace/deletion/cancel-by-link: identity (401),
// capped decode (400), cancel, 200 `{"deletion_due_at": null}`.
func CancelDeletionByLinkHandler(cancel CancelDeletionByLinkFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxInviteTokenBodyBytes)
		var req cancelByLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := cancel(r.Context(), req.Token); err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: cancel workspace deletion by link", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, map[string]*string{"deletion_due_at": nil})
	}
}
