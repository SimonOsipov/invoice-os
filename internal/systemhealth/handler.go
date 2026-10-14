package systemhealth

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// Snapshot is the GET /v1/staff/health body.
type Snapshot struct {
	CheckedAt  time.Time           `json:"checked_at"`
	Fleet      gateway.FleetHealth `json:"fleet"`
	Jobs       map[string]int64    `json:"jobs"`
	QueueDepth int64               `json:"queue_depth"`
	Trend      []TrendPoint        `json:"trend"`
}

// TrendPoint is one sample on the wire; ServicesDown is null for an unknown sample.
type TrendPoint struct {
	TakenAt      time.Time `json:"taken_at"`
	ServicesDown *int      `json:"services_down"`
	QueueDepth   int64     `json:"queue_depth"`
	DeadLettered int64     `json:"dead_lettered"`
}

// Handler serves GET /v1/staff/health.
func Handler(read func(context.Context, time.Time) (map[string]int64, []Sample, error), fleet FleetReader, now func() time.Time, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
