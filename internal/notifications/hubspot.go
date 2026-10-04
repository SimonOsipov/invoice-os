package notifications

// STUB (AUTH-17-03 red): compile-only; the executor replaces it.

import (
	"context"
	"net/http"
)

type HubSpotClient interface {
	Upsert(ctx context.Context, c Contact) error
}

type HubSpot struct{}

// NewHubSpot: a nil hc means http.Client{Timeout: 10s}.
func NewHubSpot(baseURL string, k Keys, hc *http.Client) *HubSpot { return &HubSpot{} }

func (h *HubSpot) Upsert(ctx context.Context, c Contact) error { return nil }
