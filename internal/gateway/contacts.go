package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"
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
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notifications answered %d", resp.StatusCode)
	}
	return nil
}

// handOffRegistrant delivers user to the sink in the background: up to three attempts, one WARN after the last fails.
// A nil sink is a no-op. The log carries the user id only.
func handOffRegistrant(ctx context.Context, log *slog.Logger, sink ContactSink, user RegistrantContact) {
	if sink == nil {
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
		log.WarnContext(ctx, "contacts: registrant hand-off failed", slog.String("user_id", user.UserID), slog.Int("attempts", len(delays)+1))
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
