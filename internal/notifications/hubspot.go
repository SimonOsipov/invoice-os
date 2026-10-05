package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HubSpotClient interface {
	Upsert(ctx context.Context, c Contact) error
}

type HubSpot struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewHubSpot: a nil hc means http.Client{Timeout: 10s}.
func NewHubSpot(baseURL string, k Keys, hc *http.Client) *HubSpot {
	if hc == nil {
		hc = NewHTTPClient(nil)
	}
	return &HubSpot{baseURL: strings.TrimRight(baseURL, "/"), token: k.HubSpotToken, hc: hc}
}

// NewHTTPClient is the vendor client: 10 s timeout, and a 3xx is the response, never followed.
// A nil rt means http.DefaultTransport.
func NewHTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt, Timeout: 10 * time.Second, CheckRedirect: noRedirect}
}

// noRedirect surfaces a 3xx as the response; following it would turn a write into a GET.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

const hubspotContacts = "/crm/v3/objects/contacts"

// Upsert PATCHes by email and creates on 404.
func (h *HubSpot) Upsert(ctx context.Context, c Contact) error {
	props := map[string]string{}
	for k, v := range map[string]string{"firstname": c.FirstName, "lastname": c.LastName, "company": c.Company} {
		if v != "" {
			props[k] = v
		}
	}
	var tags strings.Builder
	for _, t := range c.Tags {
		tags.WriteString(";" + strings.ReplaceAll(t, " ", "_"))
	}
	if tags.Len() > 0 { // an empty value would clear tags HubSpot already holds
		props["ascomply_contact_tags"] = tags.String()
	}

	patchURL := h.baseURL + hubspotContacts + "/" + url.PathEscape(c.Email) + "?idProperty=email"
	status, err := doJSON(ctx, h.hc, http.MethodPatch, patchURL, h.token, map[string]any{"properties": props})
	if err != nil {
		return err
	}
	if status != http.StatusNotFound {
		return statusError(status)
	}
	props["email"] = c.Email
	status, err = doJSON(ctx, h.hc, http.MethodPost, h.baseURL+hubspotContacts, h.token, map[string]any{"properties": props})
	if err != nil {
		return err
	}
	return statusError(status)
}

// doJSON returns the response status. A transport failure becomes a *DeliveryError{0}: the
// *url.Error is dropped because its text carries the request URL.
func doJSON(ctx context.Context, hc *http.Client, method, u, token string, body any) (int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, &DeliveryError{}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(buf))
	if err != nil {
		return 0, &DeliveryError{}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return send(hc, req)
}

func send(hc *http.Client, req *http.Request) (int, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return 0, &DeliveryError{}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// statusError is nil for a 2xx.
func statusError(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return &DeliveryError{Status: status}
}
