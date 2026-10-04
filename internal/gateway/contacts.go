package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// MarketingConsent is the sentence the person was shown and when they ticked it (RFC 3339).
type MarketingConsent struct {
	Text, At string
}

// RegistrantContact is one verified registrant; Consent is nil when no tick is on record.
type RegistrantContact struct {
	UserID, Email, DisplayName, WorkspaceName string
	Consent                                   *MarketingConsent
}

// DemoRequest is one demo-request form submission.
type DemoRequest struct {
	Email, Name, Company, MarketingConsentText string
}

// ContactSink delivers a contact to the notifications service.
type ContactSink interface {
	Registrant(ctx context.Context, c RegistrantContact) error
	DemoRequest(ctx context.Context, d DemoRequest) error
}

// handOffDelays are the waits before the second and third attempt; tests shorten them.
var handOffDelays = []time.Duration{5 * time.Second, 30 * time.Second}

// NewHTTPContactSink posts to the notifications service at base with the gateway token.
func NewHTTPContactSink(base *url.URL, client *http.Client, gatewayToken string) ContactSink {
	return unbuiltSink{}
}

type unbuiltSink struct{}

func (unbuiltSink) Registrant(context.Context, RegistrantContact) error {
	return errors.New("contact sink is not built")
}

func (unbuiltSink) DemoRequest(context.Context, DemoRequest) error {
	return errors.New("contact sink is not built")
}

// handOffRegistrant runs the attempts for one registrant; the caller owns the goroutine and the context.
func handOffRegistrant(ctx context.Context, log *slog.Logger, sink ContactSink, user RegistrantContact) {
}
