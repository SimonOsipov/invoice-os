package systemhealth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// Handler serves GET /v1/staff/health: the database read first, then the fleet read.
// A fleet failure answers "unknown" and never fails the route.
func Handler(read func(context.Context, time.Time) (map[string]int64, []Sample, error), fleet FleetReader, now func() time.Time, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := now().UTC()
		jobs, samples, err := read(r.Context(), at.Add(-TrendWindow))
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		case err != nil:
			log.Error("systemhealth: staff health", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}
		f, err := fleet(r.Context())
		if err != nil {
			log.Warn("systemhealth: fleet read failed", "err", err)
			f = gateway.FleetHealth{Status: FleetUnknown}
		}
		if f.Services == nil {
			f.Services = []gateway.ServiceHealth{}
		}
		jobs = Counts(jobs)
		trend := make([]TrendPoint, 0, len(samples))
		for _, s := range samples {
			c := Counts(s.JobCounts)
			trend = append(trend, TrendPoint{TakenAt: s.TakenAt, ServicesDown: s.ServicesDown, QueueDepth: QueueDepth(c), DeadLettered: c["dead_lettered"]})
		}
		writeJSON(w, http.StatusOK, Snapshot{CheckedAt: at, Fleet: f, Jobs: jobs, QueueDepth: QueueDepth(jobs), Trend: trend})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
