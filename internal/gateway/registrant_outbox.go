package gateway

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RegistrantOutbox writes each registrant to registrant_handoffs as invoice_gateway, inside the
// request; the worker moves the row into a River job. Demo requests still go to sink.
type RegistrantOutbox struct {
	ContactSink
	pool *pgxpool.Pool
}

func NewRegistrantOutbox(pool *pgxpool.Pool, sink ContactSink) *RegistrantOutbox {
	return &RegistrantOutbox{ContactSink: sink, pool: pool}
}

const outboxTimeout = 3 * time.Second

func (o *RegistrantOutbox) Registrant(ctx context.Context, c RegistrantContact) error {
	ctx, cancel := context.WithTimeout(ctx, outboxTimeout)
	defer cancel()
	var text, at string
	if c.Consent != nil {
		text, at = c.Consent.Text, c.Consent.At
	}
	_, err := o.pool.Exec(ctx, `INSERT INTO registrant_handoffs
		(user_id, email, display_name, workspace_name, consent_text, consent_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, c.UserID, c.Email, c.DisplayName, c.WorkspaceName, text, at)
	return err
}
