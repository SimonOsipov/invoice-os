// Package aicalls writes one ai_calls row per AI call, in the background.
package aicalls

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
)

// Recorder is the airecord.Sink that stores each call under its tenant.
type Recorder struct {
	pool    *pgxpool.Pool
	logger  *slog.Logger
	timeout time.Duration
}

// NewRecorder returns a Recorder with a 2 s write timeout.
func NewRecorder(pool *pgxpool.Pool, logger *slog.Logger) *Recorder {
	return &Recorder{pool: pool, logger: logger, timeout: 2 * time.Second}
}

// Record writes c in the background, so a slow or down database never delays the AI call.
func (r *Recorder) Record(c airecord.Call) {}

func (r *Recorder) write(c airecord.Call) {}
