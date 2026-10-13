package customrule

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const maxBodyBytes = 32 << 10

type listResponse struct {
	CustomRules []Rule `json:"custom_rules"`
}

// ruleBody is RuleInput with enabled required: a pointer tells absent from false.
type ruleBody struct {
	EntityID *string         `json:"entity_id"`
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
	Enabled  *bool           `json:"enabled"`
}

func ListHandler(list func(ctx context.Context) ([]Rule, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if !hasIdentity(w, r) {
			return
		}
		rules, err := list(r.Context())
		if err != nil {
			fail(w, r, log, err)
			return
		}
		if rules == nil {
			rules = []Rule{}
		}
		writeJSON(w, http.StatusOK, listResponse{CustomRules: rules})
	}
}

func CreateHandler(create func(ctx context.Context, in RuleInput) (Rule, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := decodeInput(w, r)
		if !ok {
			return
		}
		rule, err := create(r.Context(), in)
		if err != nil {
			fail(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusCreated, rule)
	}
}

func UpdateHandler(update func(ctx context.Context, id string, in RuleInput) (Rule, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := decodeInput(w, r)
		if !ok {
			return
		}
		rule, err := update(r.Context(), r.PathValue("id"), in)
		if err != nil {
			fail(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	}
}

func DeleteHandler(del func(ctx context.Context, id string) error, log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if !hasIdentity(w, r) {
			return
		}
		if err := del(r.Context(), r.PathValue("id")); err != nil {
			fail(w, r, log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func orDefault(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

func hasIdentity(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := auth.IdentityFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func decodeInput(w http.ResponseWriter, r *http.Request) (RuleInput, bool) {
	if !hasIdentity(w, r) {
		return RuleInput{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var b ruleBody
	if err := dec.Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return RuleInput{}, false
	}
	if b.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return RuleInput{}, false
	}
	return RuleInput{EntityID: b.EntityID, Type: b.Type, Target: b.Target, Params: b.Params,
		Severity: b.Severity, When: b.When, Message: b.Message, Enabled: *b.Enabled}, true
}

func fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var ie InvalidError
	switch {
	case errors.Is(err, db.ErrNoTenant):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, db.ErrNotActiveMember):
		writeError(w, http.StatusForbidden, db.NotActiveMemberMessage)
	case errors.Is(err, ErrNotPermitted):
		writeError(w, http.StatusForbidden, "only a workspace admin can change custom rules")
	case errors.As(err, &ie):
		writeError(w, http.StatusBadRequest, ie.Msg)
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "custom rule not found")
	case errors.Is(err, ErrCheckUnavailable):
		log.WarnContext(r.Context(), "customrule: check unavailable", slog.Any("err", err))
		writeError(w, http.StatusBadGateway, "custom rules cannot be checked right now")
	default:
		log.ErrorContext(r.Context(), "customrule: request failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal server error")
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
