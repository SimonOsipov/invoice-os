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

// ResendState is the part of a Resend contact that a delete wipes and a person set: the global
// unsubscribe and the topic opt-outs. A missing contact has the zero state.
type ResendState struct {
	Unsubscribed bool
	MarketingOut bool     // the Marketing topic is opted out
	OptedOut     []string // every opted-out topic id, Marketing included
}

// otherTopicOut reports an opted-out topic other than Marketing, which is the Blog topic.
func (s ResendState) otherTopicOut() bool {
	n := len(s.OptedOut)
	if s.MarketingOut {
		n--
	}
	return n > 0
}

func (s ResendState) zero() bool { return !s.Unsubscribed && len(s.OptedOut) == 0 }

type ResendEraser interface {
	EraseContact(ctx context.Context, email string) error
	ContactState(ctx context.Context, email string) (ResendState, error)
	// RestoreContact re-creates a deleted contact with the saved state. It sends no opt_in.
	RestoreContact(ctx context.Context, email, first, last string, st ResendState) error
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
		if len(page.Results) == 0 {
			break
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

// ContactState reads the unsubscribe flag and the topic opt-outs. A 404 is the zero state.
func (r *Resend) ContactState(ctx context.Context, email string) (ResendState, error) {
	var st ResendState
	contact := r.baseURL + "/contacts/" + url.PathEscape(email)
	status, body, err := doJSONBody(ctx, r.hc, http.MethodGet, contact, r.apiKey, nil, true)
	if err != nil {
		return st, err
	}
	if status == http.StatusNotFound {
		return st, nil
	}
	if err := statusError(status); err != nil {
		return st, err
	}
	var c struct {
		Unsubscribed bool `json:"unsubscribed"`
	}
	if json.Unmarshal(body, &c) != nil {
		return st, &DeliveryError{Status: status}
	}
	st.Unsubscribed = c.Unsubscribed

	for after, pages := "", 0; ; pages++ {
		if pages >= maxTopicPages {
			return st, &DeliveryError{Status: status}
		}
		u := contact + "/topics?limit=100"
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		status, body, err = doJSONBody(ctx, r.hc, http.MethodGet, u, r.apiKey, nil, true)
		if err != nil {
			return st, err
		}
		if status == http.StatusNotFound {
			return st, nil
		}
		if err := statusError(status); err != nil {
			return st, err
		}
		var page struct {
			HasMore bool `json:"has_more"`
			Data    []struct {
				ID           string `json:"id"`
				Subscription string `json:"subscription"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &page) != nil {
			return st, &DeliveryError{Status: status}
		}
		for _, t := range page.Data {
			if t.Subscription == "opt_out" {
				st.OptedOut = append(st.OptedOut, t.ID)
				st.MarketingOut = st.MarketingOut || t.ID == r.topicID
			}
		}
		if !page.HasMore || len(page.Data) == 0 {
			return st, nil
		}
		after = page.Data[len(page.Data)-1].ID
	}
}

// maxTopicPages bounds the topic paging; an account has a handful of topics.
const maxTopicPages = 20

// RestoreContact creates the contact with the saved unsubscribe and opt-outs, so a delivery that
// follows finds it. It sends no opt_in and makes no suppression call.
func (r *Resend) RestoreContact(ctx context.Context, email, first, last string, st ResendState) error {
	body := map[string]any{"email": email}
	if first != "" {
		body["first_name"] = first
	}
	if last != "" {
		body["last_name"] = last
	}
	if st.Unsubscribed {
		body["unsubscribed"] = true
	}
	status, err := doJSON(ctx, r.hc, http.MethodPost, r.baseURL+"/contacts", r.apiKey, body)
	if err != nil {
		return err
	}
	if err := statusError(status); err != nil {
		return err
	}
	if len(st.OptedOut) == 0 {
		return nil
	}
	topics := make([]map[string]string, 0, len(st.OptedOut))
	for _, id := range st.OptedOut {
		topics = append(topics, map[string]string{"id": id, "subscription": "opt_out"})
	}
	status, err = doJSON(ctx, r.hc, http.MethodPatch, r.baseURL+"/contacts/"+url.PathEscape(email)+"/topics", r.apiKey, topics)
	if err != nil {
		return err
	}
	return statusError(status)
}
