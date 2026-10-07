package tenancy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ceiling: per workspace only; five workspaces can spend the Resend 100/day and one address can get 20 a day; revisit on real invite traffic or a Resend 429
const maxInviteMailsPerDay = 20

// Invitation is one pending-invite row of GET /v1/invitations.
type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	InvitedBy string    `json:"invited_by"`
	Delivery  string    `json:"delivery"`
}

// IssuedInvite is one freshly minted invite. Token exists only here, for the mail.
type IssuedInvite struct {
	ID, Email, Role    string
	ExpiresAt          time.Time
	Token              string
	TokenHash          []byte
	Workspace, Inviter string
}

// mintToken returns 32 random bytes as base64url and the SHA-256 of its text.
func mintToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	return tok, h[:], nil
}

// requireInviteAdmin reads the caller's own active row before any other read, so a
// non-admin is refused alike for every target. It returns the caller's mail name.
func requireInviteAdmin(ctx context.Context, tx pgx.Tx, subject string) (string, error) {
	var role string
	var name, email *string
	if err := tx.QueryRow(ctx,
		`SELECT role, display_name, email FROM memberships WHERE user_id = $1 AND status = 'active'`, subject,
	).Scan(&role, &name, &email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInviteNotPermitted
		}
		return "", err
	}
	if role != "admin" {
		return "", ErrInviteNotPermitted
	}
	switch {
	case name != nil && *name != "":
		return *name, nil
	case email != nil:
		return *email, nil
	}
	return "", nil
}

// lockInviteSends serialises the daily-limit count per tenant until commit.
func lockInviteSends(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('invitation_send:' || $1::text, 0))`, tenantID)
	return err
}

func countInviteMails(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM audit_log
		  WHERE event IN ('invitation.sent', 'invitation.resent')
		    AND created_at > now() - interval '24 hours'
		    AND coalesce((payload->>'counted')::boolean, true)`).Scan(&n)
	return n, err
}

// CreateInvitations issues one pending invite per address, or reissues an existing
// pending one. It does not normalise addresses.
func (s *Store) CreateInvitations(ctx context.Context, emails []string, role string) ([]IssuedInvite, error) {
	var out []IssuedInvite
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = nil
		caller, _ := auth.IdentityFromContext(ctx)
		inviter, err := requireInviteAdmin(ctx, tx, caller.Subject)
		if err != nil {
			return err
		}
		if err := lockInviteSends(ctx, tx, caller.TenantID); err != nil {
			return err
		}
		n, err := countInviteMails(ctx, tx)
		if err != nil {
			return err
		}
		if n+len(emails) > maxInviteMailsPerDay {
			return ErrDailyInviteLimit
		}
		var workspace string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&workspace); err != nil {
			return err
		}
		for _, email := range emails {
			tok, hash, err := mintToken()
			if err != nil {
				return err
			}
			inv := IssuedInvite{Email: email, Role: role, Token: tok, TokenHash: hash, Workspace: workspace, Inviter: inviter}
			if err := tx.QueryRow(ctx,
				`INSERT INTO invitations (tenant_id, invitee_email, role, token_hash, expires_at, invited_by, send_status)
				 VALUES ($1, $2, $3, $4, now() + make_interval(days => $5), $6, 'sending')
				 ON CONFLICT (tenant_id, invitee_email) WHERE status = 'pending' DO UPDATE
				    SET token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at,
				        role = EXCLUDED.role, invited_by = EXCLUDED.invited_by, send_status = 'sending'
				 RETURNING id, expires_at`,
				caller.TenantID, email, role, hash, accountmail.InviteValidDays, caller.Subject,
			).Scan(&inv.ID, &inv.ExpiresAt); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, caller.Subject, "invitation.sent", map[string]any{
				"invitation_id": inv.ID,
				"role":          role,
			}); err != nil {
				return err
			}
			out = append(out, inv)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResendInvitation issues a new token for a pending invite, keeping its role and
// inviter. A resend of a failed send is not counted against the daily limit.
func (s *Store) ResendInvitation(ctx context.Context, id string) (IssuedInvite, error) {
	var inv IssuedInvite
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if _, err := requireInviteAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := lockInviteSends(ctx, tx, caller.TenantID); err != nil {
			return err
		}
		var status, sendStatus, invitedBy string
		if err := tx.QueryRow(ctx,
			`SELECT invitee_email, role, status, send_status, invited_by::text FROM invitations WHERE id = $1 FOR UPDATE`, id,
		).Scan(&inv.Email, &inv.Role, &status, &sendStatus, &invitedBy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvitationNotFound
			}
			return err
		}
		if status != "pending" {
			return ErrInvitationNotPending
		}
		counted := sendStatus != "failed"
		if counted {
			n, err := countInviteMails(ctx, tx)
			if err != nil {
				return err
			}
			if n+1 > maxInviteMailsPerDay {
				return ErrDailyInviteLimit
			}
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&inv.Workspace); err != nil {
			return err
		}
		// No status filter: a suspended inviter is still the one who invited.
		var inviter *string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(display_name, ''), email) FROM memberships WHERE user_id = $1`, invitedBy,
		).Scan(&inviter); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if inviter != nil {
			inv.Inviter = *inviter
		}
		tok, hash, err := mintToken()
		if err != nil {
			return err
		}
		inv.Token, inv.TokenHash, inv.ID = tok, hash, id
		if err := tx.QueryRow(ctx,
			`UPDATE invitations
			    SET token_hash = $2, expires_at = now() + make_interval(days => $3), send_status = 'sending'
			  WHERE id = $1 RETURNING id, expires_at`,
			id, hash, accountmail.InviteValidDays,
		).Scan(&inv.ID, &inv.ExpiresAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, caller.Subject, "invitation.resent", map[string]any{
			"invitation_id": inv.ID,
			"role":          inv.Role,
			"counted":       counted,
		})
	})
	if err != nil {
		return IssuedInvite{}, err
	}
	return inv, nil
}

// ListInvitations returns the tenant's pending invites that carry a token.
func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) {
	out := []Invitation{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = []Invitation{}
		caller, _ := auth.IdentityFromContext(ctx)
		if _, err := requireInviteAdmin(ctx, tx, caller.Subject); err != nil {
			return err
		}
		// ceiling: unpaged; page it above ~500 pending rows
		rows, err := tx.Query(ctx,
			`SELECT id::text, invitee_email, role, status, expires_at, created_at, invited_by::text, send_status
			   FROM invitations
			  WHERE status = 'pending' AND token_hash IS NOT NULL
			  ORDER BY created_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i Invitation
			if err := rows.Scan(&i.ID, &i.Email, &i.Role, &i.Status, &i.ExpiresAt, &i.CreatedAt, &i.InvitedBy, &i.Delivery); err != nil {
				return err
			}
			out = append(out, i)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordInviteDelivery stores the send outcome only while tokenHash is still the
// row's current hash, so a stale send cannot overwrite a newer resend.
func (s *Store) RecordInviteDelivery(ctx context.Context, id string, tokenHash []byte, sent bool) error {
	status := "failed"
	if sent {
		status = "sent"
	}
	return db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE invitations SET send_status = $3 WHERE id = $1 AND token_hash = $2`, id, tokenHash, status)
		return err
	})
}
