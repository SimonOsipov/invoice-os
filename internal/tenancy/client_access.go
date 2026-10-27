package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var (
	ErrClientAccessNotPermitted = errors.New("tenancy: client access not permitted")
	ErrClientAccessNotFirm      = errors.New("tenancy: client access applies to firm workspaces only")
	ErrClientAccessAdmin        = errors.New("tenancy: an admin sees every client")
	ErrUnknownClient            = errors.New("tenancy: unknown client")
	ErrInvalidClientAccess      = errors.New("tenancy: invalid client access")
)

// ClientAccess is a member's client scope: every client, or the listed ones.
type ClientAccess struct {
	All bool
	IDs []string
}

// MarshalJSON writes "all" or the id array, [] when empty.
func (a ClientAccess) MarshalJSON() ([]byte, error) {
	if a.All {
		return []byte(`"all"`), nil
	}
	ids := slices.Clone(a.IDs)
	if ids == nil {
		ids = []string{}
	}
	slices.Sort(ids)
	return json.Marshal(ids)
}

// UnmarshalJSON accepts "all" or an array of strings.
func (a *ClientAccess) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if s != "all" {
			return ErrInvalidClientAccess
		}
		*a = ClientAccess{All: true}
		return nil
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return ErrInvalidClientAccess
	}
	*a = ClientAccess{IDs: ids}
	return nil
}

// MemberAccess is a membership row plus its client access; nil marshals as null (in-house).
type MemberAccess struct {
	Membership
	ClientAccess *ClientAccess `json:"client_access"`
}

// parseClientIDs canonicalises uuid text and refuses a duplicate in any spelling.
func parseClientIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, ErrInvalidClientAccess
		}
		c := id.String()
		if seen[c] {
			return nil, ErrInvalidClientAccess
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

// readMemberAccess reads membership rows (all, or one user when userID is set)
// with their client access, in the caller's transaction.
func readMemberAccess(ctx context.Context, tx pgx.Tx, userID string) ([]MemberAccess, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM tenants`).Scan(&kind); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT m.user_id, m.role, m.status, m.display_name, m.email, m.all_clients,
		        coalesce(array_agg(c.entity_id::text ORDER BY c.entity_id::text)
		                 FILTER (WHERE c.entity_id IS NOT NULL), '{}')
		   FROM memberships m
		   LEFT JOIN membership_clients c ON c.tenant_id = m.tenant_id AND c.user_id = m.user_id
		  WHERE $1::uuid IS NULL OR m.user_id = $1
		  GROUP BY m.id
		  ORDER BY m.created_at, m.user_id`, nullIfEmpty(userID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberAccess{}
	for rows.Next() {
		var m MemberAccess
		var all bool
		var ids []string
		if err := rows.Scan(&m.UserID, &m.Role, &m.Status, &m.DisplayName, &m.Email, &all, &ids); err != nil {
			return nil, err
		}
		if kind == "firm" {
			m.ClientAccess = &ClientAccess{All: all || m.Role == "admin", IDs: ids}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMemberClients replaces a member's whole client access and audits it in
// the same transaction. Statement order matters: the admin check runs first so a
// non-admin is refused alike for every target, and the row lock serialises two
// writes to one member (TestClientAccess_ConcurrentSetsDoNotMerge).
func (s *Store) SetMemberClients(ctx context.Context, userID string, access ClientAccess) (MemberAccess, error) {
	var out MemberAccess
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if _, err := requireInviteAdmin(ctx, tx, caller.Subject); err != nil {
			if errors.Is(err, ErrInviteNotPermitted) {
				return ErrClientAccessNotPermitted
			}
			return err
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM tenants`).Scan(&kind); err != nil {
			return err
		}
		if kind != "firm" {
			return ErrClientAccessNotFirm
		}
		var role, target string
		if err := tx.QueryRow(ctx,
			`SELECT user_id::text, role FROM memberships WHERE user_id = $1 AND status <> 'removed' FOR UPDATE`, userID,
		).Scan(&target, &role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrMembershipNotFound
			}
			return err
		}
		if role == "admin" {
			return ErrClientAccessAdmin
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET all_clients = $2 WHERE user_id = $1`, target, access.All); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM membership_clients WHERE user_id = $1`, target); err != nil {
			return err
		}
		ids := []string{}
		if !access.All {
			ids = slices.Clone(access.IDs)
			slices.Sort(ids)
			if _, err := tx.Exec(ctx,
				`INSERT INTO membership_clients (tenant_id, user_id, entity_id)
				 SELECT m.tenant_id, m.user_id, e FROM memberships m, unnest($2::uuid[]) AS e WHERE m.user_id = $1`,
				target, ids); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "membership_clients_tenant_entity_fk" {
					return ErrUnknownClient
				}
				return err
			}
		}
		if err := audit.Record(ctx, tx, caller.Subject, "membership.client_access_set", map[string]any{
			"user_id": target, "all_clients": access.All, "client_ids": ids,
		}); err != nil {
			return err
		}
		rows, err := readMemberAccess(ctx, tx, target)
		if err != nil {
			return err
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return MemberAccess{}, err
	}
	return out, nil
}

// MemberClientsSetter replaces one member's client access; Store.SetMemberClients is the production one.
type MemberClientsSetter func(ctx context.Context, userID string, access ClientAccess) (MemberAccess, error)

const maxSetClientsBodyBytes = 64 * 1024

// SetMemberClientsHandler returns PUT /v1/memberships/{user_id}/clients: identity
// (401) -> capped decode and id grammar (400) -> path uuid (404) -> store -> 200.
// The body is checked before the path, as PATCH does.
func SetMemberClientsHandler(set MemberClientsSetter, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSetClientsBodyBytes)
		var req struct {
			ClientAccess *ClientAccess `json:"client_access"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err == nil {
			_, err = io.Copy(io.Discard, r.Body) // errors when the whole body exceeds the cap
		}
		if err != nil || req.ClientAccess == nil {
			status, msg := statusForErr(ErrInvalidClientAccess)
			writeError(w, status, msg)
			return
		}
		access := *req.ClientAccess
		if !access.All {
			ids, err := parseClientIDs(access.IDs)
			if err != nil {
				status, msg := statusForErr(err)
				writeError(w, status, msg)
				return
			}
			access.IDs = ids
		}
		userID, err := uuid.Parse(r.PathValue("user_id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		row, err := set(r.Context(), userID.String(), access)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: set member clients", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, row)
	}
}
