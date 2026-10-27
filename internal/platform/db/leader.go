package db

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// claimRetry is how often a process that missed a task's lock tries again.
const claimRetry = 5 * time.Second

// Leader elects one process per periodic task with session advisory locks on one dedicated
// connection. A task keeps its lock until the connection drops, so a second worker instance
// runs none of the tasks the first one leads, and takes them over soon after the first stops.
// ceiling: a connection lost mid-run lets a peer start the task before this run ends.
type Leader struct {
	dsn string

	mu   sync.Mutex
	conn *pgx.Conn
	held map[string]bool
}

func NewLeader(dsn string) *Leader { return &Leader{dsn: dsn} }

// Lead wraps fn so it runs only while this process holds the lock for name. A call that misses
// the lock retries every claimRetry until it takes the lock or ctx ends; it then returns nil.
func (l *Leader) Lead(name string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		for {
			ok, err := l.claim(ctx, name)
			if err != nil {
				return err
			}
			if ok {
				return fn(ctx)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(claimRetry):
			}
		}
	}
}

// claim reports whether this process holds name's lock, taking it when free. A dead
// connection has lost every lock, so it is replaced and every task re-claimed.
func (l *Leader) claim(ctx context.Context, name string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil && l.conn.Ping(ctx) != nil {
		_ = l.conn.Close(context.Background())
		l.conn = nil
	}
	if l.conn == nil {
		// The pool's DSN may carry pool_max_conns, which pgx.Connect would send to Postgres.
		cfg, err := pgxpool.ParseConfig(l.dsn)
		if err != nil {
			return false, fmt.Errorf("db: leader connect: %w", err)
		}
		conn, err := pgx.ConnectConfig(ctx, cfg.ConnConfig)
		if err != nil {
			return false, fmt.Errorf("db: leader connect: %w", err)
		}
		l.conn, l.held = conn, map[string]bool{}
	}
	if l.held[name] {
		return true, nil
	}
	var ok bool
	if err := l.conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended('leader:' || $1, 0))`, name).Scan(&ok); err != nil {
		return false, fmt.Errorf("db: leader lock %s: %w", name, err)
	}
	l.held[name] = ok
	return ok, nil
}

// Start does nothing; the connection opens on the first claim. Satisfies platform.BackgroundWorker.
func (l *Leader) Start(context.Context) error { return nil }

// Stop closes the connection, releasing every lock. Register the Leader after the workers that
// use it: App.Run stops workers in registration order.
func (l *Leader) Stop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	err := l.conn.Close(ctx)
	l.conn = nil
	return err
}
