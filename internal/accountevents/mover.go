package accountevents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// HandoffInterval is how often the worker moves gateway rows.
const HandoffInterval = 5 * time.Second

// handoffBatch bounds one move; a backlog drains over several ticks.
const handoffBatch = 100

type handoff struct {
	event, ip, ua string
	userID, email *string
	at            time.Time
}

// newDeviceJob builds the one new-device job of a sign-in; the gateway sets both user id and address.
func newDeviceJob(h handoff) (NewDeviceArgs, bool) {
	if h.userID == nil || h.email == nil || strings.TrimSpace(*h.email) == "" {
		return NewDeviceArgs{}, false
	}
	d := Parse(h.ua)
	return NewDeviceArgs{UserID: *h.userID, Email: strings.TrimSpace(*h.email), Browser: d.Browser, OS: d.OS, IP: h.ip, OccurredAt: h.at.UTC().Format(time.RFC3339)}, true
}

// MoveHandoffs moves outbox rows into one account_audit job per active workspace, plus one account_new_device job per sign-in, in one transaction.
func MoveHandoffs(pool *pgxpool.Pool, rc *river.Client[pgx.Tx], log *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			// DELETE hands each row to one mover; the jobs commit with it or neither does.
			rows, err := tx.Query(ctx, `DELETE FROM account_event_handoffs WHERE id IN
				(SELECT id FROM account_event_handoffs ORDER BY occurred_at, id LIMIT $1)
				RETURNING event, user_id::text, email, ip, user_agent, occurred_at`, handoffBatch)
			if err != nil {
				return fmt.Errorf("accountevents: read hand-offs: %w", err)
			}
			var batch []handoff
			for rows.Next() {
				var h handoff
				if err := rows.Scan(&h.event, &h.userID, &h.email, &h.ip, &h.ua, &h.at); err != nil {
					rows.Close()
					return fmt.Errorf("accountevents: read hand-off: %w", err)
				}
				batch = append(batch, h)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("accountevents: read hand-offs: %w", err)
			}
			if len(batch) == handoffBatch {
				log.Warn("account-event-handoff: full batch", "size", len(batch))
			}

			var jobs []river.InsertManyParams
			for _, h := range batch {
				found, err := tx.Query(ctx, `SELECT tenant_id::text, user_id::text FROM public.account_event_memberships($1, $2)`, h.userID, h.email)
				if err != nil {
					return fmt.Errorf("accountevents: resolve members: %w", err)
				}
				n := 0
				for found.Next() {
					a := AuditArgs{Event: h.event, IP: h.ip, Device: Parse(h.ua).Label(), OccurredAt: h.at.UTC().Format(time.RFC3339)}
					if err := found.Scan(&a.TenantID, &a.UserID); err != nil {
						found.Close()
						return fmt.Errorf("accountevents: resolve member: %w", err)
					}
					jobs = append(jobs, river.InsertManyParams{Args: a})
					n++
				}
				if err := found.Err(); err != nil {
					return fmt.Errorf("accountevents: resolve members: %w", err)
				}
				if n > 0 && h.event == "account.signed_in" {
					if nd, ok := newDeviceJob(h); ok {
						jobs = append(jobs, river.InsertManyParams{Args: nd})
					} else {
						log.Debug("account-event-handoff: sign-in without user or address; no new-device job")
					}
				}
				if n == 0 {
					log.Info("account-event-handoff: no active member", "event", h.event)
				}
			}
			if len(jobs) == 0 {
				return nil
			}
			if _, err := rc.InsertManyTx(ctx, tx, jobs); err != nil {
				return fmt.Errorf("accountevents: queue audit jobs: %w", err)
			}
			return nil
		})
	}
}
