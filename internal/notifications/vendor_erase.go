package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// HubSpotEraser and ResendEraser remove one person from a vendor. A vendor 404 is success.
type HubSpotEraser interface {
	EraseContact(ctx context.Context, email string) error
}

type ResendEraser interface {
	EraseContact(ctx context.Context, email string) error
}

var (
	_ HubSpotEraser = (*HubSpot)(nil)
	_ HubSpotEraser = FakeHubSpot{}
	_ ResendEraser  = (*Resend)(nil)
	_ ResendEraser  = FakeResend{}
)

const (
	erasedDealName = "Erased contact"
	// maxDealRounds bounds the read-delete rounds; the attempt timeout bounds them too.
	maxDealRounds = 200
)

// EraseContact renames and deletes every deal of the contact, then GDPR-deletes the contact.
// Deals go first: a retry that finds no contact knows the deals are done.
func (h *HubSpot) EraseContact(ctx context.Context, email string) error {
	var c hsID
	status, err := h.getJSON(ctx, http.MethodGet,
		h.baseURL+hubspotContacts+"/"+url.PathEscape(email)+"?idProperty=email", nil, &c, http.StatusOK)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status != http.StatusOK {
		return &DeliveryError{Status: status}
	}

	// Never follow `after` across a delete: the cursor would apply to the shrunken set. Read the first page again.
	u := h.baseURL + hubspotV4 + "/" + url.PathEscape(c.ID) + "/associations/deals?limit=100"
	for round := 0; ; round++ {
		if round >= maxDealRounds {
			return errors.New("notifications: hubspot deals remain after the round cap")
		}
		var page struct {
			Results []struct {
				ToObjectID json.Number `json:"toObjectId"`
			} `json:"results"`
		}
		status, err := h.getJSON(ctx, http.MethodGet, u, nil, &page, http.StatusOK)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return &DeliveryError{Status: status}
		}
		for _, r := range page.Results {
			if err := h.eraseDeal(ctx, r.ToObjectID.String()); err != nil {
				return err
			}
		}
	}

	status, err = doJSON(ctx, h.hc, http.MethodPost, h.baseURL+hubspotContacts+"/gdpr-delete", h.token,
		map[string]string{"objectId": email, "idProperty": "email"})
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	return statusError(status)
}

// eraseDeal blanks the deal name, then deletes it: a deleted deal stays restorable with its name.
		if len(page.Results) == 0 {
			break
		}
func (h *HubSpot) eraseDeal(ctx context.Context, id string) error {
	dealURL := h.baseURL + hubspotDeals + "/" + url.PathEscape(id)
	status, err := doJSON(ctx, h.hc, http.MethodPatch, dealURL, h.token,
		map[string]any{"properties": map[string]string{"dealname": erasedDealName}})
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if err := statusError(status); err != nil {
		return err
	}
	if status, err = doJSON(ctx, h.hc, http.MethodDelete, dealURL, h.token, nil); err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	return statusError(status)
}

// EraseContact deletes the contact. It makes no suppression, topic or segment call.
func (r *Resend) EraseContact(ctx context.Context, email string) error {
	status, err := doJSON(ctx, r.hc, http.MethodDelete, r.baseURL+"/contacts/"+url.PathEscape(email), r.apiKey, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	return statusError(status)
}
