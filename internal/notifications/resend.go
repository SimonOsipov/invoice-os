package notifications

// STUB (AUTH-17-03 red): compile-only; the executor replaces it.

import (
	"context"
	"net/http"
)

type ResendClient interface {
	Sync(ctx context.Context, c Contact, sendOptIn bool) error
}

type Resend struct{}

// NewResend: a nil hc means http.Client{Timeout: 10s}.
func NewResend(baseURL string, k Keys, hc *http.Client) *Resend { return &Resend{} }

func (r *Resend) Sync(ctx context.Context, c Contact, sendOptIn bool) error { return nil }
