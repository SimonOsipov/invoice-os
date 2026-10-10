package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MarketingConsent is the sentence the person was shown and when they ticked it (RFC 3339).
type MarketingConsent struct {
	Text string `json:"text"`
	At   string `json:"at"`
}

// RegistrantContact is one verified registrant; Consent is nil when no tick is on record.
type RegistrantContact struct {
	UserID, Email, DisplayName, WorkspaceName string
	Consent                                   *MarketingConsent
}

// DemoRequest is one demo-request form submission.
type DemoRequest struct {
	Email                string `json:"email"`
	Name                 string `json:"name"`
	Company              string `json:"company"`
	MarketingConsentText string `json:"marketing_consent_text"`
}

// ContactSink delivers a contact to the notifications service.
type ContactSink interface {
	Registrant(ctx context.Context, c RegistrantContact) error
	DemoRequest(ctx context.Context, d DemoRequest) error
}

// handOffDelays are the waits before the second and third attempt; tests shorten them.
var handOffDelays = []time.Duration{5 * time.Second, 30 * time.Second}

const sinkCallTimeout = 5 * time.Second

type httpContactSink struct {
	base   *url.URL
	client *http.Client
	token  string
}

// NewHTTPContactSink posts to the notifications service at base with the gateway token.
// It never follows a redirect: a 3xx is an error.
func NewHTTPContactSink(base *url.URL, client *http.Client, gatewayToken string) ContactSink {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &httpContactSink{base: base, client: &c, token: gatewayToken}
}

func (s *httpContactSink) Registrant(ctx context.Context, c RegistrantContact) error {
	body := struct {
		UserID        string            `json:"user_id"`
		Email         string            `json:"email"`
		DisplayName   string            `json:"display_name"`
		WorkspaceName string            `json:"workspace_name"`
		Consent       *MarketingConsent `json:"marketing_consent,omitempty"`
	}{c.UserID, c.Email, c.DisplayName, c.WorkspaceName, c.Consent}
	return s.post(ctx, "registrants", body)
}

func (s *httpContactSink) DemoRequest(ctx context.Context, d DemoRequest) error {
	return s.post(ctx, "demo-requests", d)
}

// post sends body to /internal/contacts/<name>; the error text carries no body.
func (s *httpContactSink) post(ctx context.Context, name string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sinkCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base.JoinPath("internal", "contacts", name).String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGoTrueBodyBytes))
	if resp.StatusCode != http.StatusAccepted {
		return sinkStatusError(resp.StatusCode)
	}
	return nil
}

// sinkStatusError is any answer from notifications but 202; the hand-off WARN logs its code.
type sinkStatusError int

func (e sinkStatusError) Error() string { return fmt.Sprintf("notifications answered %d", int(e)) }

// failureStatus is the HTTP status behind err, or 0 for a transport or timeout error.
func failureStatus(err error) int {
	var se sinkStatusError
	if errors.As(err, &se) {
		return int(se)
	}
	return 0
}

// handOffRegistrant delivers user to the sink in the background: up to three attempts, one WARN after the last fails.
// A nil sink is a no-op; a blank email logs one WARN naming source and skips. The log carries the user id and the failing status only.
func handOffRegistrant(ctx context.Context, log *slog.Logger, source string, sink ContactSink, user RegistrantContact) {
	if sink == nil {
		return
	}
	if strings.TrimSpace(user.Email) == "" {
		log.WarnContext(ctx, source+": gotrue user has no email; no hand-off")
		return
	}
	ctx = context.WithoutCancel(ctx)
	delays := slices.Clone(handOffDelays)
	go func() {
		var err error
		for attempt := 0; ; attempt++ {
			if err = sink.Registrant(ctx, user); err == nil {
				return
			}
			if attempt >= len(delays) {
				break
			}
			time.Sleep(delays[attempt])
		}
		log.WarnContext(ctx, "contacts: registrant hand-off failed", slog.String("user_id", user.UserID), slog.Int("attempts", len(delays)+1), slog.Int("status", failureStatus(err)))
	}()
}

// gotrueUser is the part of a GoTrue user the hand-off reads.
type gotrueUser struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	UserMetadata struct {
		Registration *struct {
			WorkspaceName string `json:"workspace_name"`
			DisplayName   string `json:"display_name"`
		} `json:"registration"`
		MarketingConsent *MarketingConsent `json:"marketing_consent"`
	} `json:"user_metadata"`
}

func (u gotrueUser) contact() RegistrantContact {
	c := RegistrantContact{UserID: u.ID, Email: u.Email}
	if r := u.UserMetadata.Registration; r != nil {
		c.DisplayName, c.WorkspaceName = r.DisplayName, r.WorkspaceName
	}
	if mc := u.UserMetadata.MarketingConsent; mc != nil && mc.Text != "" {
		c.Consent = mc
	}
	return c
}

const maxDemoBodyBytes = 4096

// ceiling: in-process counts; one office behind a NAT shares 5 requests an hour. Raise it when a visitor reports the 429.
// ceiling: a full key map (10,000) refuses every new IP for up to an hour. Revisit when the demo-request full-map WARN fires.
const (
	DemoRequestPerIP   = 5
	DemoRequestWindow  = time.Hour
	DemoRequestMaxKeys = 10_000
)

// DemoRequestHandler takes the landing's demo-request form and hands it to the sink once, with no retry.
// perIP is spent per valid request and refunded only when the sink answers 4xx (it stored nothing); enforce=false logs the miss and lets the request through.
// ceiling: any address can be ticked; no marketing email to demo-route contacts until double opt-in exists.
func DemoRequestHandler(sink ContactSink, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var in struct {
			Email   string  `json:"email"`
			Name    string  `json:"name"`
			Company string  `json:"company"`
			Text    *string `json:"marketing_consent_text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDemoBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		d := DemoRequest{Email: strings.TrimSpace(in.Email), Name: strings.TrimSpace(in.Name), Company: strings.TrimSpace(in.Company)}
		if msg := validateDemoRequest(d, in.Text); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		key, source := clientKey(r)
		held := perIP.Reserve(key)
		if !held {
			log.WarnContext(r.Context(), "demo-request: limit reached",
				slog.String("limit", "ip"), slog.String("key_source", source), slog.Bool("enforced", enforce))
			if enforce {
				writeError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
		}
		if in.Text != nil {
			d.MarketingConsentText = *in.Text
		}
		if err := sink.DemoRequest(r.Context(), d); err != nil {
			status := failureStatus(err)
			if held && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
				perIP.Refund(key)
			}
			log.WarnContext(r.Context(), "contacts: demo request hand-off failed", slog.Int("status", status))
			writeError(w, http.StatusBadGateway, "demo request is unavailable")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	})
}

// validateDemoRequest returns the 400 message for the first bad field, or "".
func validateDemoRequest(d DemoRequest, text *string) string {
	if n := len(d.Email); n < 3 || n > 254 || strings.Count(d.Email, "@") != 1 || strings.IndexFunc(d.Email, unicode.IsSpace) >= 0 || strings.ContainsRune(d.Email, 0) {
		return "email is invalid"
	}
	for _, f := range []struct{ label, v string }{{"name", d.Name}, {"company", d.Company}} {
		if n := utf8.RuneCountInString(f.v); n < 1 || n > 200 {
			return f.label + " must be 1 to 200 characters"
		}
		if strings.ContainsRune(f.v, 0) {
			return f.label + " must not contain a NUL byte"
		}
	}
	if text != nil {
		if n := utf8.RuneCountInString(*text); strings.TrimSpace(*text) == "" || n > maxConsentTextChars || strings.ContainsRune(*text, 0) {
			return "marketing_consent_text must be 1 to 500 characters"
		}
	}
	return ""
}
