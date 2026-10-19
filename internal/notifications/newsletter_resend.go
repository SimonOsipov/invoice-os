package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// BlogSubscriber is a confirmed newsletter subscriber. CategoryIDs are in category position order.
type BlogSubscriber struct {
	Email       string
	CategoryIDs []string
	Frequency   string
}

type BlogResendClient interface {
	SyncBlog(ctx context.Context, s BlogSubscriber) error
	ContactUnsubscribed(ctx context.Context, email string) (bool, error)
}

type BlogResend struct {
	baseURL, apiKey string
	keys            BlogKeys
	hc              *http.Client
}

// NewBlogResend: a nil hc means http.Client{Timeout: 10s}.
func NewBlogResend(baseURL, apiKey string, k BlogKeys, hc *http.Client) *BlogResend {
	if hc == nil {
		hc = NewHTTPClient(nil)
	}
	return &BlogResend{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, keys: k, hc: hc}
}

// SyncBlog writes the properties, adds the contact to segment Blog and opts it in to topic Blog.
// It never sends `unsubscribed`, an `opt_out` or a suppression call, so a global unsubscribe
// stays. It stops at the first failure.
func (r *BlogResend) SyncBlog(ctx context.Context, s BlogSubscriber) error {
	contact := r.baseURL + "/contacts/" + url.PathEscape(s.Email)
	props := map[string]string{
		"blog_categories": "," + strings.Join(s.CategoryIDs, ",") + ",",
		"blog_frequency":  s.Frequency,
	}

	status, _, err := r.get(ctx, contact, false)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		status, err = doJSON(ctx, r.hc, http.MethodPost, r.baseURL+"/contacts", r.apiKey,
			map[string]any{"email": s.Email, "properties": props})
	} else if err = statusError(status); err == nil {
		status, err = doJSON(ctx, r.hc, http.MethodPatch, contact, r.apiKey, map[string]any{"properties": props})
	}
	if err != nil {
		return err
	}
	if err := statusError(status); err != nil {
		return err
	}

	status, err = doJSON(ctx, r.hc, http.MethodPost, contact+"/segments/"+url.PathEscape(r.keys.SegmentID), r.apiKey, struct{}{})
	if err != nil {
		return err
	}
	if err := statusError(status); err != nil {
		return err
	}

	topics := []map[string]string{{"id": r.keys.TopicID, "subscription": "opt_in"}}
	if status, err = doJSON(ctx, r.hc, http.MethodPatch, contact+"/topics", r.apiKey, topics); err != nil {
		return err
	}
	return statusError(status)
}

// ContactUnsubscribed reads the contact's global unsubscribe flag. A missing contact is false.
func (r *BlogResend) ContactUnsubscribed(ctx context.Context, email string) (bool, error) {
	status, body, err := r.get(ctx, r.baseURL+"/contacts/"+url.PathEscape(email), true)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if err := statusError(status); err != nil {
		return false, err
	}
	var c struct {
		Unsubscribed bool `json:"unsubscribed"`
	}
	if json.Unmarshal(body, &c) != nil {
		return false, &DeliveryError{Status: status}
	}
	return c.Unsubscribed, nil
}

// get returns a 404 as a status, not an error. The caller checks any other status.
func (r *BlogResend) get(ctx context.Context, u string, readBody bool) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, &DeliveryError{}
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	return sendBody(r.hc, req, readBody)
}
