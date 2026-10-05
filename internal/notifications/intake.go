package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IntakeStore is what the intake and self-read handlers need; *Store satisfies it.
type IntakeStore interface {
	Registrant(ctx context.Context, in RegistrantIntake) error
	DemoRequest(ctx context.Context, in DemoIntake) error
	Me(ctx context.Context, email string) (Contact, error)
}

var _ IntakeStore = (*Store)(nil)

const maxIntakeBody = 16 << 10

type registrantBody struct {
	UserID        string `json:"user_id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	WorkspaceName string `json:"workspace_name"`
	Consent       *struct {
		Text string `json:"text"`
		At   string `json:"at"`
	} `json:"marketing_consent"`
}

type demoBody struct {
	Email                string `json:"email"`
	Name                 string `json:"name"`
	Company              string `json:"company"`
	MarketingConsentText string `json:"marketing_consent_text"`
}

// decodeIntake answers 404 for a proxied request (the gateway's own call has no X-User-ID header, not even an empty one),
// and 400 for a body that does not decode. It reports whether the caller may go on.
func decodeIntake(w http.ResponseWriter, r *http.Request, dst any) bool {
	if len(r.Header.Values("X-User-ID")) > 0 {
		writeJSONBody(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIntakeBody))
	if err := dec.Decode(dst); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return false
	}
	return true
}

func intakeResult(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if err != nil {
		log.ErrorContext(r.Context(), "contacts: intake failed", slog.Any("err", err))
		writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "intake failed"})
		return
	}
	writeJSONBody(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func RegistrantsHandler(store IntakeStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b registrantBody
		if !decodeIntake(w, r, &b) {
			return
		}
		if strings.TrimSpace(b.Email) == "" {
			writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
			return
		}
		if b.UserID != "" {
			if _, err := uuid.Parse(b.UserID); err != nil || len(b.UserID) != 36 {
				writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "user_id is invalid"})
				return
			}
		}
		in := RegistrantIntake{UserID: b.UserID, Email: b.Email, DisplayName: b.DisplayName, WorkspaceName: b.WorkspaceName}
		if b.Consent != nil && b.Consent.Text != "" {
			// Fail closed: consent without a readable time is not recorded.
			if at, err := time.Parse(time.RFC3339, b.Consent.At); err != nil {
				log.WarnContext(r.Context(), "contacts: marketing consent time unreadable; consent dropped", slog.String("user_id", b.UserID))
			} else {
				in.ConsentText, in.ConsentAt = b.Consent.Text, at
			}
		}
		intakeResult(w, r, log, store.Registrant(r.Context(), in))
	}
}

func DemoRequestsHandler(store IntakeStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b demoBody
		if !decodeIntake(w, r, &b) {
			return
		}
		if strings.TrimSpace(b.Email) == "" {
			writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
			return
		}
		intakeResult(w, r, log, store.DemoRequest(r.Context(), DemoIntake{
			Email: b.Email, Name: b.Name, Company: b.Company, ConsentText: b.MarketingConsentText,
		}))
	}
}

type destination struct {
	DeliveredAt *time.Time `json:"delivered_at"`
	Applies     *bool      `json:"applies,omitempty"`
}

// MeHandler reads the caller's own row by the token's email, which the gateway sets.
func MeHandler(store IntakeStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := store.Me(r.Context(), r.Header.Get("X-User-Email"))
		if errors.Is(err, ErrNotFound) {
			writeJSONBody(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if err != nil {
			log.ErrorContext(r.Context(), "contacts: self-read failed", slog.Any("err", err))
			writeJSONBody(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
			return
		}
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		writeJSONBody(w, http.StatusOK, map[string]any{
			"email":              c.Email,
			"tags":               tags,
			"marketing_eligible": c.MarketingEligible,
			"hubspot":            destination{DeliveredAt: c.HubSpotDeliveredAt},
			"resend":             destination{DeliveredAt: c.ResendDeliveredAt, Applies: &c.ResendApplies},
			"mode":               c.Mode,
		})
	}
}

func writeJSONBody(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
