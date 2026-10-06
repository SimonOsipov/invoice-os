package tenancy

// RED STUB (RESEND-05-05, compile-only). The executor deletes this file and
// implements these symbols in invitations.go and tenancy.go per the subtask.

import (
	"context"
	"errors"
	"time"
)

const maxInviteMailsPerDay = 20

var (
	ErrInviteNotPermitted   = errors.New("stub: not permitted")
	ErrInvitationNotFound   = errors.New("stub: not found")
	ErrInvitationNotPending = errors.New("stub: not pending")
	ErrDailyInviteLimit     = errors.New("stub: daily limit")
)

type Invitation struct {
	ID, Email, Role, Status string
	ExpiresAt, CreatedAt    time.Time
	InvitedBy, Delivery     string
}

type IssuedInvite struct {
	ID, Email, Role    string
	ExpiresAt          time.Time
	Token              string
	TokenHash          []byte
	Workspace, Inviter string
}

func (s *Store) CreateInvitations(ctx context.Context, emails []string, role string) ([]IssuedInvite, error) {
	return nil, nil
}

func (s *Store) ResendInvitation(ctx context.Context, id string) (IssuedInvite, error) {
	return IssuedInvite{}, nil
}

func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) { return nil, nil }

func (s *Store) RecordInviteDelivery(ctx context.Context, id string, tokenHash []byte, sent bool) error {
	return nil
}
