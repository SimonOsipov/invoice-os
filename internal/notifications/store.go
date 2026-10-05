package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

var ErrNotFound = errors.New("notifications: contact not found")

const (
	destHubSpot = "hubspot"
	destResend  = "resend"
)

type Store struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

func NewStore(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *Store {
	return &Store{pool: pool, river: riverClient}
}

// RegistrantIntake: empty UserID is NULL; empty ConsentText is unticked.
type RegistrantIntake struct {
	UserID, Email, DisplayName, WorkspaceName string
	ConsentText                               string
	ConsentAt                                 time.Time
}

// DemoIntake: the store stamps the demo time and, when ticked, the consent time.
type DemoIntake struct {
	Email, Name, Company string
	ConsentText          string
}

type Contact struct {
	Email              string
	FirstName          string
	LastName           string
	Company            string
	Tags               []string
	MarketingEligible  bool
	ResendApplies      bool
	HubSpotDeliveredAt *time.Time
	ResendDeliveredAt  *time.Time
	Mode               *string
}

// A fact that was NULL and is now set bumps version and clears delivery; names merge freely.
const newFactSQL = `(
        (contacts.registered_at IS NULL AND EXCLUDED.registered_at IS NOT NULL)
     OR (contacts.demo_requested_at IS NULL AND EXCLUDED.demo_requested_at IS NOT NULL)
     OR (contacts.marketing_consented_at IS NULL AND EXCLUDED.marketing_consented_at IS NOT NULL))`

const mergeSQL = `
INSERT INTO contacts (email, user_id, first_name, last_name, company,
                      registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at)
VALUES (lower(btrim($1)), $2::uuid, $3, $4, $5,
        CASE WHEN $6 THEN now() END,
        CASE WHEN $7 THEN now() END,
        $8::text,
        CASE WHEN $8::text IS NOT NULL THEN COALESCE($9::timestamptz, now()) END)
ON CONFLICT (email) DO UPDATE SET
    user_id                = COALESCE(contacts.user_id, EXCLUDED.user_id),
    first_name             = COALESCE(NULLIF(EXCLUDED.first_name, ''), contacts.first_name),
    last_name              = COALESCE(NULLIF(EXCLUDED.last_name, ''), contacts.last_name),
    company                = COALESCE(NULLIF(EXCLUDED.company, ''), contacts.company),
    registered_at          = COALESCE(contacts.registered_at, EXCLUDED.registered_at),
    demo_requested_at      = COALESCE(contacts.demo_requested_at, EXCLUDED.demo_requested_at),
    marketing_consent_text = COALESCE(contacts.marketing_consent_text, EXCLUDED.marketing_consent_text),
    marketing_consented_at = COALESCE(contacts.marketing_consented_at, EXCLUDED.marketing_consented_at),
    version                = contacts.version + CASE WHEN ` + newFactSQL + ` THEN 1 ELSE 0 END,
    hubspot_delivered_at   = CASE WHEN ` + newFactSQL + ` THEN NULL ELSE contacts.hubspot_delivered_at END,
    resend_delivered_at    = CASE WHEN ` + newFactSQL + ` THEN NULL ELSE contacts.resend_delivered_at END,
    updated_at             = now()
RETURNING email, version, hubspot_delivered_at IS NULL,
          resend_delivered_at IS NULL AND (registered_at IS NOT NULL OR marketing_consented_at IS NOT NULL)`

type intake struct {
	email, userID, first, last, company string
	registered, demo                    bool
	consentText                         string
	consentAt                           time.Time
}

func (s *Store) Registrant(ctx context.Context, in RegistrantIntake) error {
	first, last := splitName(in.DisplayName)
	return s.merge(ctx, intake{
		email: in.Email, userID: in.UserID, first: first, last: last, company: strings.TrimSpace(in.WorkspaceName),
		registered: true, consentText: in.ConsentText, consentAt: in.ConsentAt,
	})
}

func (s *Store) DemoRequest(ctx context.Context, in DemoIntake) error {
	first, last := splitName(in.Name)
	return s.merge(ctx, intake{
		email: in.Email, first: first, last: last, company: strings.TrimSpace(in.Company),
		demo: true, consentText: in.ConsentText,
	})
}

func (s *Store) merge(ctx context.Context, in intake) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notifications: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID, consentText *string
	if in.userID != "" {
		userID = &in.userID
	}
	var consentAt *time.Time
	if in.consentText != "" {
		consentText = &in.consentText
		if !in.consentAt.IsZero() {
			consentAt = &in.consentAt
		}
	}

	var email string
	var version int64
	var hubspotPending, resendPending bool
	err = tx.QueryRow(ctx, mergeSQL, in.email, userID, in.first, in.last, in.company,
		in.registered, in.demo, consentText, consentAt).Scan(&email, &version, &hubspotPending, &resendPending)
	if err != nil {
		return fmt.Errorf("notifications: merge contact: %w", err)
	}

	for _, d := range []struct {
		name    string
		pending bool
	}{{destHubSpot, hubspotPending}, {destResend, resendPending}} {
		if !d.pending {
			continue
		}
		dest := d.name
		if _, err := s.river.InsertTx(ctx, tx, DeliverArgs{Email: email, Destination: dest, Version: version}, nil); err != nil {
			return fmt.Errorf("notifications: queue %s delivery: %w", dest, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifications: commit: %w", err)
	}
	return nil
}

// splitName splits on the first run of whitespace, keeping the rest verbatim.
func splitName(full string) (first, last string) {
	full = strings.TrimSpace(full)
	i := strings.IndexFunc(full, unicode.IsSpace)
	if i < 0 {
		return full, ""
	}
	return full[:i], strings.TrimLeftFunc(full[i:], unicode.IsSpace)
}

func (s *Store) Me(ctx context.Context, email string) (Contact, error) {
	var c Contact
	var registered, demo, consented bool
	err := s.pool.QueryRow(ctx, `
		SELECT email, registered_at IS NOT NULL, demo_requested_at IS NOT NULL, marketing_consented_at IS NOT NULL,
		       hubspot_delivered_at, resend_delivered_at, delivery_mode
		FROM contacts WHERE email = lower(btrim($1))`, email).Scan(
		&c.Email, &registered, &demo, &consented, &c.HubSpotDeliveredAt, &c.ResendDeliveredAt, &c.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("notifications: read contact: %w", err)
	}
	if registered {
		c.Tags = append(c.Tags, "registered")
	}
	if demo {
		c.Tags = append(c.Tags, "demo request")
	}
	c.MarketingEligible = consented
	c.ResendApplies = registered || consented
	return c, nil
}
