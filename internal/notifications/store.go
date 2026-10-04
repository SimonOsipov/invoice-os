package notifications

// TEST-FIRST STUB (AUTH-17-02 Mode A): compile-only. The executor replaces every body.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

var ErrNotFound = errors.New("notifications: contact not found")

type Store struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

type RegistrantIntake struct {
	UserID, Email, DisplayName, WorkspaceName string
	ConsentText                               string
	ConsentAt                                 time.Time
}

type DemoIntake struct {
	Email, Name, Company string
	ConsentText          string
}

type Contact struct {
	Email              string
	Tags               []string
	MarketingEligible  bool
	ResendApplies      bool
	HubSpotDeliveredAt *time.Time
	ResendDeliveredAt  *time.Time
	Mode               *string
}

func (s *Store) Registrant(ctx context.Context, in RegistrantIntake) error { return nil }

func (s *Store) DemoRequest(ctx context.Context, in DemoIntake) error { return nil }

func (s *Store) Me(ctx context.Context, email string) (Contact, error) {
	return Contact{}, errors.New("not implemented")
}
