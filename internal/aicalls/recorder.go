// Package aicalls writes one ai_calls row per AI call, in the background.
package aicalls

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Recorder is the airecord.Sink that stores each call.
type Recorder struct {
	pool    *pgxpool.Pool
	logger  *slog.Logger
	timeout time.Duration
	wg      sync.WaitGroup
}

// NewRecorder returns a Recorder with a 2 s write timeout.
func NewRecorder(pool *pgxpool.Pool, logger *slog.Logger) *Recorder {
	return &Recorder{pool: pool, logger: logger, timeout: 2 * time.Second}
}

// Record writes c in the background, so a slow or down database never delays the AI call.
// ceiling: one goroutine per call, unbounded; add a bounded queue above ~100 calls/s
func (r *Recorder) Record(c airecord.Call) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.write(c)
	}()
}

// Wait blocks until the pending writes finish, at most one write timeout.
// Call it before the pool closes, or in-flight rows are lost.
func (r *Recorder) Wait() {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(r.timeout):
	}
}

func (r *Recorder) write(c airecord.Call) {
	if c.PublicCheck {
		r.writePublicCheck(c)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	// 15 significant digits is a float64's exact decimal precision: no sum artifact reaches numeric.
	cost := strconv.FormatFloat(c.Cost, 'g', 15, 64)
	err := db.WithinTenantTx(ctx, r.pool, c.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
			 VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)`,
			c.TenantID, c.Client, c.Purpose, c.Model, c.InputTokens, c.OutputTokens, cost, c.LatencyMS, c.Attempts, c.Outcome)
		return err
	})
	if err != nil {
		r.logger.LogAttrs(context.Background(), slog.LevelWarn, "ai call not recorded",
			slog.String("client", c.Client), slog.String("purpose", c.Purpose),
			slog.String("tenant_id", c.TenantID), slog.Any("err", err))
	}
}

// writePublicCheck inserts a tenant_id NULL row on the bare pool: no tenant tx, and no RETURNING
// since tenant_isolation hides the row from invoice_app.
func (r *Recorder) writePublicCheck(c airecord.Call) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	cost := strconv.FormatFloat(c.Cost, 'g', 15, 64)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
		 VALUES (NULL, $1, $2, $3, $4, $5, $6::numeric, $7, $8, $9)`,
		c.Client, c.Purpose, c.Model, c.InputTokens, c.OutputTokens, cost, c.LatencyMS, c.Attempts, c.Outcome)
	if err != nil {
		r.logger.LogAttrs(context.Background(), slog.LevelWarn, "ai call not recorded",
			slog.String("client", c.Client), slog.String("purpose", c.Purpose), slog.Any("err", err))
	}
}
