package notifications

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type ResendClient interface {
	Sync(ctx context.Context, c Contact, sendOptIn bool) error
}

type Resend struct {
	baseURL, apiKey, segmentID, topicID string
	hc                                  *http.Client
}

// NewResend: a nil hc means http.Client{Timeout: 10s}.
func NewResend(baseURL string, k Keys, hc *http.Client) *Resend {
	if hc == nil {
		hc = NewHTTPClient(nil)
	}
	return &Resend{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: k.ResendAPIKey,
		segmentID: k.ResendSegmentID, topicID: k.ResendTopicID, hc: hc,
	}
}

// Sync creates the contact when absent, adds a registrant to the segment, and opts in when
// asked. It never unsubscribes. It stops at the first failure.
func (r *Resend) Sync(ctx context.Context, c Contact, sendOptIn bool) error {
	contact := r.baseURL + "/contacts/" + url.PathEscape(c.Email)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, contact, nil)
	if err != nil {
		return &DeliveryError{}
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	status, err := send(r.hc, req)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		body := map[string]string{"email": c.Email}
		if c.FirstName != "" {
			body["first_name"] = c.FirstName
		}
		if c.LastName != "" {
			body["last_name"] = c.LastName
		}
		if status, err = doJSON(ctx, r.hc, http.MethodPost, r.baseURL+"/contacts", r.apiKey, body); err != nil {
			return err
		}
	}
	if err := statusError(status); err != nil {
		return err
	}

	if slices.Contains(c.Tags, "registered") {
		status, err = doJSON(ctx, r.hc, http.MethodPost, contact+"/segments/"+url.PathEscape(r.segmentID), r.apiKey, struct{}{})
		if err != nil {
			return err
		}
		if err := statusError(status); err != nil {
			return err
		}
	}
	if sendOptIn {
		body := []map[string]string{{"id": r.topicID, "subscription": "opt_in"}}
		if status, err = doJSON(ctx, r.hc, http.MethodPatch, contact+"/topics", r.apiKey, body); err != nil {
			return err
		}
		return statusError(status)
	}
	return nil
}
