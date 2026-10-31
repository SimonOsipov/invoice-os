package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

const (
	liveHubSpotPortal = 148915098
	maxTries          = 5
	retryWait         = 2 * time.Second
	erasePace         = 200 * time.Millisecond // 5 erases a second per vendor
	maxPages          = 1000
	maxBody           = 1 << 20
)

// vendors makes every vendor read. It reads a body only on success, and no error text carries a
// URL, an address or a token: a failure is a status, 0 for no answer.
type vendors struct {
	hc             *http.Client
	hsToken, rsKey string
	sleep          func(time.Duration)
	hsErase        notifications.HubSpotEraser
	rsErase        notifications.ResendEraser
}

// retry runs fn until it answers something other than 429, at most maxTries times.
func (v *vendors) retry(fn func() int) int {
	status := 0
	for try := 1; try <= maxTries; try++ {
		if status = fn(); status != http.StatusTooManyRequests {
			return status
		}
		if try < maxTries {
			v.sleep(retryWait)
		}
	}
	return status
}

func (v *vendors) do(method, rawURL, token string, body any) (int, []byte) {
	var raw []byte
	status := v.retry(func() int {
		raw = nil
		var rd io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return 0
			}
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, rawURL, rd)
		if err != nil {
			return 0
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := v.hc.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		raw, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return resp.StatusCode
	})
	return status, raw
}

func (v *vendors) hsGet(path string, out any) int {
	status, raw := v.do(http.MethodGet, notifications.HubSpotBaseURL+path, v.hsToken, nil)
	if status == http.StatusOK && json.Unmarshal(raw, out) != nil {
		return 0
	}
	return status
}

func (v *vendors) rsGet(path string, out any) int {
	status, raw := v.do(http.MethodGet, notifications.ResendBaseURL+path, v.rsKey, nil)
	if status == http.StatusOK && json.Unmarshal(raw, out) != nil {
		return 0
	}
	return status
}

func answered(status int) string {
	if status == 0 {
		return "no usable answer"
	}
	return fmt.Sprintf("answered %d", status)
}

// portal refuses the live portal. On a refusal detail says why, and the caller makes no other HubSpot call.
func (v *vendors) portal() (ok bool, detail string) {
	var d struct {
		PortalID int64 `json:"portalId"`
	}
	if status := v.hsGet("/account-info/v3/details", &d); status != http.StatusOK {
		return false, "HubSpot " + answered(status)
	}
	switch {
	case d.PortalID == 0:
		return false, "portal id is missing"
	case d.PortalID == liveHubSpotPortal:
		return false, "token belongs to the live portal"
	}
	return true, ""
}

// configured reads the deal stage and the contact property the services write.
func (v *vendors) configured(stage string) (ok bool, detail string) {
	var stages struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if status := v.hsGet("/crm/v3/pipelines/deals/default/stages", &stages); status != http.StatusOK {
		return false, "stages read " + answered(status)
	}
	found := false
	for _, s := range stages.Results {
		found = found || s.ID == stage
	}
	if !found {
		return false, "deal stage is not in the default pipeline"
	}
	var prop struct{}
	if status := v.hsGet("/crm/v3/properties/contacts/ascomply_contact_tags", &prop); status != http.StatusOK {
		return false, "property ascomply_contact_tags " + answered(status)
	}
	return true, ""
}

type resendContact struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

// created parses the two timestamp shapes Resend answers; ok is false for anything else.
func (c resendContact) created() (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999Z07"} {
		if t, err := time.Parse(layout, c.CreatedAt); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type hsPage struct {
	Results []struct {
		Properties map[string]*string `json:"properties"`
	} `json:"results"`
	Paging struct {
		Next struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

// hsList reads every page of a HubSpot object list and returns the values of one property.
func (v *vendors) hsList(object, property string) ([]string, error) {
	var out []string
	after := ""
	for page := 0; page < maxPages; page++ {
		path := "/crm/v3/objects/" + object + "?limit=100&properties=" + property
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var p hsPage
		if status := v.hsGet(path, &p); status != http.StatusOK {
			return nil, fmt.Errorf("HubSpot %s list %s", object, answered(status))
		}
		for _, r := range p.Results {
			if val := r.Properties[property]; val != nil {
				out = append(out, *val)
			}
		}
		if after = p.Paging.Next.After; after == "" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("HubSpot %s list passed %d pages", object, maxPages)
}

func (v *vendors) rsList() ([]resendContact, error) {
	var out []resendContact
	after := ""
	for page := 0; page < maxPages; page++ {
		path := "/contacts?limit=100"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var p struct {
			HasMore bool            `json:"has_more"`
			Data    []resendContact `json:"data"`
		}
		if status := v.rsGet(path, &p); status != http.StatusOK {
			return nil, fmt.Errorf("Resend list %s", answered(status))
		}
		out = append(out, p.Data...)
		if !p.HasMore || len(p.Data) == 0 {
			return out, nil
		}
		after = p.Data[len(p.Data)-1].ID
	}
	return nil, fmt.Errorf("Resend list passed %d pages", maxPages)
}

// erase paces and retries a vendor eraser. Its error is a *notifications.DeliveryError: a status only.
func (v *vendors) erase(fn func(context.Context) error) error {
	var err error
	v.retry(func() int {
		v.sleep(erasePace)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err = fn(ctx)
		var de *notifications.DeliveryError
		if errors.As(err, &de) {
			return de.Status
		}
		return 0
	})
	return err
}

func (v *vendors) eraseHubSpot(email string) error {
	return v.erase(func(ctx context.Context) error { return v.hsErase.EraseContact(ctx, email) })
}

func (v *vendors) eraseResend(email string) error {
	return v.erase(func(ctx context.Context) error { return v.rsErase.EraseContact(ctx, email) })
}

// hsContact reads a contact's id and tags by address; found is false until it exists.
func (v *vendors) hsContact(email string) (id, tags string, status int) {
	var c struct {
		ID         string             `json:"id"`
		Properties map[string]*string `json:"properties"`
	}
	status = v.hsGet("/crm/v3/objects/contacts/"+url.PathEscape(email)+"?idProperty=email&properties=ascomply_contact_tags", &c)
	if t := c.Properties["ascomply_contact_tags"]; t != nil {
		tags = *t
	}
	return c.ID, tags, status
}

func hasTag(tags, want string) bool {
	for _, t := range strings.Split(tags, ";") {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// demoDeal reads the deals of a contact and reports whether one has the demo name and the stage.
func (v *vendors) demoDeal(contactID, stage string) (ok bool, detail string) {
	var assoc struct {
		Results []struct {
			ToObjectID json.Number `json:"toObjectId"`
		} `json:"results"`
	}
	if status := v.hsGet("/crm/v4/objects/contacts/"+url.PathEscape(contactID)+"/associations/deals", &assoc); status != http.StatusOK {
		return false, "deal association read " + answered(status)
	}
	if len(assoc.Results) == 0 {
		return false, "contact has no deal"
	}
	nameOK := false
	for _, r := range assoc.Results {
		var d struct {
			Properties map[string]*string `json:"properties"`
		}
		if status := v.hsGet("/crm/v3/objects/deals/"+url.PathEscape(r.ToObjectID.String())+"?properties=dealname,dealstage", &d); status != http.StatusOK {
			return false, "deal read " + answered(status)
		}
		name, st := d.Properties["dealname"], d.Properties["dealstage"]
		n := name != nil && strings.HasSuffix(*name, demoSuffix)
		s := st != nil && *st == stage
		if n && s {
			return true, ""
		}
		nameOK = nameOK || n
	}
	if !nameOK {
		return false, "no deal has the demo request name"
	}
	return false, "the demo deal is not in the configured stage"
}
