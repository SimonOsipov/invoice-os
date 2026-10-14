package staffjobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// jobStates is the submission_jobs_state_check set.
var jobStates = []string{"queued", "submitting", "pending", "accepted", "rejected", "failed", "dead_lettered", "cancelled"}

var maxAttempts = submission.SubmitArgs{}.InsertOpts().MaxAttempts

// ListHandler repeats no staff check: auth.RequireStaff runs before it.
func ListHandler(list func(ctx context.Context, state *string, tenant *uuid.UUID) ([]Job, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var state *string
		var tenant *uuid.UUID
		q := r.URL.Query()
		if s := q.Get("state"); s != "" {
			if !slices.Contains(jobStates, s) {
				writeError(w, http.StatusBadRequest, "unknown state")
				return
			}
			state = &s
		}
		if s := q.Get("tenant"); s != "" {
			id, err := uuid.Parse(s)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid tenant")
				return
			}
			tenant = &id
		}
		jobs, err := list(r.Context(), state, tenant)
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case err != nil:
			log.Error("staffjobs: list response", "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		default:
			if jobs == nil {
				jobs = []Job{}
			}
			for i := range jobs {
				jobs[i].MaxAttempts = maxAttempts
			}
			writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
		}
	}
}

// GetHandler repeats no staff check: auth.RequireStaff runs before it.
func GetHandler(get func(ctx context.Context, id string) (Detail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case errors.Is(err, ErrJobNotFound):
			writeError(w, http.StatusNotFound, "job not found")
		case err != nil:
			log.Error("staffjobs: get response", "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		default:
			detail.Job.MaxAttempts = maxAttempts
			if detail.History == nil {
				detail.History = []Step{}
			}
			writeJSON(w, http.StatusOK, detail)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
