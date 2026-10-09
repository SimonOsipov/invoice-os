package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type HubSpotClient interface {
	Upsert(ctx context.Context, c Contact) error
	// OpenDemoDeal creates a deal for c unless c already has an open one.
	OpenDemoDeal(ctx context.Context, c Contact, dealName string) error
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

const (
	hubspotContacts = "/crm/v3/objects/contacts"
	hubspotDeals    = "/crm/v3/objects/deals"
	hubspotV4       = "/crm/v4/objects/contacts"

	// ceiling: change these if sales renames or replaces the first stage.
	demoPipeline       = "default"
	demoStage          = "5716044996"
	dealToContactAssoc = 3

	maxVendorBody = 1 << 20
)

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
	status, _, err := doJSONBody(ctx, hc, method, u, token, body)
	return status, err
}

// doJSONBody also returns the response body, capped at maxVendorBody. A nil body sends none.
func doJSONBody(ctx context.Context, hc *http.Client, method, u, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, &DeliveryError{}
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, &DeliveryError{}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return sendBody(hc, req)
}

func send(hc *http.Client, req *http.Request) (int, error) {
	status, _, err := sendBody(hc, req)
	return status, err
}

func sendBody(hc *http.Client, req *http.Request) (int, []byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, &DeliveryError{}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxVendorBody))
	if err != nil {
		return 0, nil, &DeliveryError{}
	}
	return resp.StatusCode, b, nil
}

// statusError is nil for a 2xx.
func statusError(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return &DeliveryError{Status: status}
}

// OpenDemoDeal finds or creates the contact, then creates a deal unless the contact has an open one.
// HubSpot is the record: a retry after a lost create response finds the first attempt's open deal.
func (h *HubSpot) OpenDemoDeal(ctx context.Context, c Contact, dealName string) error {
	id, err := h.contactID(ctx, c)
	if err != nil {
		return err
	}
	open, err := h.hasOpenDeal(ctx, id)
	if err != nil || open {
		return err
	}
	status, err := doJSON(ctx, h.hc, http.MethodPost, h.baseURL+hubspotDeals, h.token, map[string]any{
		"properties": map[string]string{"dealname": dealName, "pipeline": demoPipeline, "dealstage": demoStage},
		"associations": []any{map[string]any{
			"to":    map[string]string{"id": id},
			"types": []any{map[string]any{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": dealToContactAssoc}},
		}},
	})
	if err != nil {
		return err
	}
	return statusError(status)
}

// getJSON reads a body only on want; any other status is a *DeliveryError and a 2xx body that
// does not decode is a status-0 one.
func (h *HubSpot) getJSON(ctx context.Context, method, u string, in, out any, want ...int) (int, error) {
	status, b, err := doJSONBody(ctx, h.hc, method, u, h.token, in)
	if err != nil {
		return 0, err
	}
	if !slices.Contains(want, status) {
		return status, nil
	}
	if json.Unmarshal(b, out) != nil {
		return 0, &DeliveryError{}
	}
	return status, nil
}

type hsID struct {
	ID string `json:"id"`
}

func (h *HubSpot) contactID(ctx context.Context, c Contact) (string, error) {
	getURL := h.baseURL + hubspotContacts + "/" + url.PathEscape(c.Email) + "?idProperty=email"
	var got hsID
	status, err := h.getJSON(ctx, http.MethodGet, getURL, nil, &got, http.StatusOK)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		return got.ID, nil
	}
	if status != http.StatusNotFound {
		return "", &DeliveryError{Status: status}
	}

	props := map[string]string{"email": c.Email}
	for k, v := range map[string]string{"firstname": c.FirstName, "lastname": c.LastName, "company": c.Company} {
		if v != "" {
			props[k] = v
		}
	}
	got = hsID{}
	status, err = h.getJSON(ctx, http.MethodPost, h.baseURL+hubspotContacts, map[string]any{"properties": props}, &got, http.StatusOK, http.StatusCreated)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return got.ID, nil
	}
	if status != http.StatusConflict {
		return "", &DeliveryError{Status: status}
	}
	// Another writer (the browser's Forms call) created it between our GET and POST.
	got = hsID{}
	status, err = h.getJSON(ctx, http.MethodGet, getURL, nil, &got, http.StatusOK)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", &DeliveryError{Status: http.StatusConflict}
	}
	return got.ID, nil
}

// hasOpenDeal judges only the deals the batch read returns. A returned deal with no
// hs_is_closed value counts as open: a missed duplicate guard is worse than a missed deal.
func (h *HubSpot) hasOpenDeal(ctx context.Context, contactID string) (bool, error) {
	// ceiling: reads the first 100 deals, page it above that.
	var assoc struct {
		Results []struct {
			ToObjectID json.Number `json:"toObjectId"`
		} `json:"results"`
	}
	status, err := h.getJSON(ctx, http.MethodGet,
		h.baseURL+hubspotV4+"/"+url.PathEscape(contactID)+"/associations/deals?limit=100", nil, &assoc, http.StatusOK)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, &DeliveryError{Status: status}
	}
	if len(assoc.Results) == 0 {
		return false, nil
	}
	inputs := make([]map[string]string, 0, len(assoc.Results))
	for _, r := range assoc.Results {
		inputs = append(inputs, map[string]string{"id": r.ToObjectID.String()})
	}

	var deals struct {
		Results []struct {
			Properties struct {
				Closed *string `json:"hs_is_closed"`
			} `json:"properties"`
		} `json:"results"`
	}
	// A 207 carries the deals HubSpot could read plus per-id errors.
	status, err = h.getJSON(ctx, http.MethodPost, h.baseURL+hubspotDeals+"/batch/read",
		map[string]any{"properties": []string{"hs_is_closed"}, "inputs": inputs}, &deals, http.StatusOK, http.StatusMultiStatus)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK && status != http.StatusMultiStatus {
		return false, &DeliveryError{Status: status}
	}
	for _, d := range deals.Results {
		if d.Properties.Closed == nil || *d.Properties.Closed != "true" {
			return true, nil
		}
	}
	return false, nil
}
