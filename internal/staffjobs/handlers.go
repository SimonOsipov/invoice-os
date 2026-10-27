package staffjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
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

const (
	maxBodyBytes   = 8 << 10
	maxReasonRunes = 500
)

type reasonRequest struct {
	Reason string `json:"reason"`
}

// readReason applies the body rules of internal/validation/staff_rules.go and writes the error response itself.
func readReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req reasonRequest
	err := dec.Decode(&req)
	if err == nil {
		// A second value, or an oversize tail, must not slip through.
		if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return "", false
	}
	reason := strings.TrimSpace(req.Reason)
	// jsonb refuses U+0000, so the audit write would fail as a 500.
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxReasonRunes || strings.ContainsRune(reason, 0) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("reason must be 1 to %d characters", maxReasonRunes))
		return "", false
	}
	return reason, true
}

// RedriveHandler repeats no staff check: auth.RequireStaff runs before it.
func RedriveHandler(redrive func(ctx context.Context, id, reason string) (Detail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reason, ok := readReason(w, r)
		if !ok {
			return
		}
		detail, err := redrive(r.Context(), r.PathValue("id"), reason)
		var st invoice.StatusError
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case errors.Is(err, ErrJobNotFound):
			writeError(w, http.StatusNotFound, "job not found")
		case errors.Is(err, db.ErrWorkspaceLocked):
			writeError(w, http.StatusConflict, db.WorkspaceLockedMessage)
		case errors.Is(err, ErrJobState):
			writeError(w, http.StatusConflict, "only a dead_lettered job can be re-driven")
		case errors.Is(err, ErrAcknowledged):
			writeError(w, http.StatusConflict, "the Access Point acknowledged this submission; re-sending may file it twice")
		case errors.Is(err, ErrRiverDuplicate):
			writeError(w, http.StatusConflict, "another River job already holds this submission")
		case errors.Is(err, ErrRiverRunning):
			writeError(w, http.StatusConflict, "the previous River job is still running; retry shortly")
		case errors.Is(err, invoice.ErrResolvedOutside):
			writeError(w, http.StatusConflict, "the invoice was resolved outside the system")
		case errors.As(err, &st):
			writeError(w, http.StatusConflict, fmt.Sprintf("the invoice is %s, not failed", st.Have))
		case err != nil:
			log.Error("staffjobs: redrive response", "err", err)
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

// CancelHandler repeats no staff check: auth.RequireStaff runs before it.
func CancelHandler(cancel func(ctx context.Context, id, reason string) (Detail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reason, ok := readReason(w, r)
		if !ok {
			return
		}
		detail, err := cancel(r.Context(), r.PathValue("id"), reason)
		var st invoice.StatusError
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case errors.Is(err, ErrJobNotFound):
			writeError(w, http.StatusNotFound, "job not found")
		case errors.Is(err, ErrJobState):
			writeError(w, http.StatusConflict, "only a queued job can be cancelled")
		case errors.As(err, &st):
			writeError(w, http.StatusConflict, fmt.Sprintf("the invoice is %s, not queued", st.Have))
		case err != nil:
			log.Error("staffjobs: cancel response", "err", err)
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
