package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const eraseEmail = "marker.person+x@ex.test"

// hsErase scripts the HubSpot erase endpoints. A zero status means 200.
type hsErase struct {
	contact, gdpr int
	patch, del    map[string]int // by deal id
	page1, page2  string         // the nth association read answers pageN, then an empty page
	reads         int
}

func (c *hsErase) st(v int) int {
	if v == 0 {
		return 200
	}
	return v
}

func (c *hsErase) respond(s seenReq) (int, string) {
	p := s.path()
	switch {
	case s.Method == http.MethodGet && p == hsCreatePath+"/"+eraseEmailPath:
		return c.st(c.contact), `{"id":"7"}`
	case s.Method == http.MethodGet && p == "/crm/v4/objects/contacts/7/associations/deals":
		if strings.Contains(s.Query, "after=") {
			return 400, `{"message":"cursor after a delete"}`
		}
		c.reads++
		page := ""
		switch c.reads {
		case 1:
			page = c.page1
		case 2:
			page = c.page2
		}
		if page == "" {
			page = noDeals
		}
		return 200, page
	case s.Method == http.MethodPatch && strings.HasPrefix(p, "/crm/v3/objects/deals/"):
		return c.st(c.patch[strings.TrimPrefix(p, "/crm/v3/objects/deals/")]), `{}`
	case s.Method == http.MethodDelete && strings.HasPrefix(p, "/crm/v3/objects/deals/"):
		return c.st(c.del[strings.TrimPrefix(p, "/crm/v3/objects/deals/")]), ``
	case s.Method == http.MethodPost && p == hsCreatePath+"/gdpr-delete":
		return c.st(c.gdpr), ``
	}
	return 500, `{"message":"unexpected request"}`
}

var eraseEmailPath = url.PathEscape(eraseEmail)

const (
	twoDeals = `{"results":[{"toObjectId":1},{"toObjectId":2}],"paging":{"next":{"after":"cur/2"}}}` // a server that still offers a cursor
	oneDeal  = `{"results":[{"toObjectId":3}]}`
	noDeals  = `{"results":[]}`
)

func TestHubSpotErase_RenamesAndDeletesEveryDealThenGDPRDeletes(t *testing.T) {
	v := newVendor(t, (&hsErase{page1: twoDeals, page2: oneDeal}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("EraseContact() err = %v, want nil", err)
	}
	want := []string{
		"GET " + hsCreatePath + "/" + eraseEmailPath,
		"GET /crm/v4/objects/contacts/7/associations/deals",
		"PATCH /crm/v3/objects/deals/1", "DELETE /crm/v3/objects/deals/1",
		"PATCH /crm/v3/objects/deals/2", "DELETE /crm/v3/objects/deals/2",
		"GET /crm/v4/objects/contacts/7/associations/deals",
		"PATCH /crm/v3/objects/deals/3", "DELETE /crm/v3/objects/deals/3",
		"GET /crm/v4/objects/contacts/7/associations/deals",
		"POST " + hsCreatePath + "/gdpr-delete",
	}
	got := v.labels()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	calls := v.calls()
	for _, i := range []int{1, 6, 9} {
		if calls[i].Query != "limit=100" {
			t.Errorf("association query %d = %q, want the first page with no cursor", i, calls[i].Query)
		}
	}
	for _, s := range calls {
		if s.Auth != "Bearer hs-tok" {
			t.Errorf("%s auth = %q, want the bearer token", s.label(), s.Auth)
		}
		if s.Method == http.MethodPatch && string(s.Body) != `{"properties":{"dealname":"Erased contact"}}` {
			t.Errorf("%s body = %s, want the rename", s.label(), s.Body)
		}
	}
	if g := string(calls[len(calls)-1].Body); g != `{"idProperty":"email","objectId":"`+eraseEmail+`"}` {
		t.Errorf("gdpr-delete body = %s, want exactly objectId and idProperty", g)
	}
}

func TestHubSpotErase_NoDealsGoesStraightToGDPRDelete(t *testing.T) {
	v := newVendor(t, (&hsErase{page1: noDeals}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	for _, l := range v.labels() {
		if strings.Contains(l, "/deals/") {
			t.Errorf("deal call %q with no deals", l)
		}
	}
	if got := v.labels(); len(got) != 3 || got[2] != "POST "+hsCreatePath+"/gdpr-delete" {
		t.Errorf("calls = %v, want contact read, associations, gdpr-delete", got)
	}
}

func TestHubSpotErase_MissingContactIsSuccess(t *testing.T) {
	v := newVendor(t, (&hsErase{contact: 404}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := v.labels(); len(got) != 1 {
		t.Errorf("calls = %v, want only the contact read", got)
	}
}

func TestHubSpotErase_MissingDealIsSkipped(t *testing.T) {
	v := newVendor(t, (&hsErase{page1: `{"results":[{"toObjectId":1},{"toObjectId":2}]}`,
		patch: map[string]int{"1": 404}, del: map[string]int{"2": 404}}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	got := strings.Join(v.labels(), "\n")
	if strings.Contains(got, "DELETE /crm/v3/objects/deals/1") {
		t.Errorf("deal 1 deleted after its rename 404:\n%s", got)
	}
	if !strings.Contains(got, "DELETE /crm/v3/objects/deals/2") || !strings.Contains(got, "gdpr-delete") {
		t.Errorf("calls =\n%s\nwant the delete of deal 2 and gdpr-delete", got)
	}
}

func TestHubSpotErase_GDPRDeleteNotFoundIsSuccess(t *testing.T) {
	v := newVendor(t, (&hsErase{page1: noDeals, gdpr: 404}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestHubSpotErase_FailureStopsAtTheCall(t *testing.T) {
	t.Run("deal delete 500", func(t *testing.T) {
		v := newVendor(t, (&hsErase{page1: `{"results":[{"toObjectId":1}]}`, del: map[string]int{"1": 500}}).respond)
		var de *DeliveryError
		if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 500 {
			t.Fatalf("err = %v, want *DeliveryError{500}", err)
		}
		if strings.Contains(strings.Join(v.labels(), "\n"), "gdpr-delete") {
			t.Error("gdpr-delete sent after a failed deal delete")
		}
	})
	t.Run("gdpr-delete 403", func(t *testing.T) {
		v := newVendor(t, (&hsErase{page1: noDeals, gdpr: 403}).respond)
		var de *DeliveryError
		if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 403 || !de.Permanent() {
			t.Fatalf("err = %v, want a permanent *DeliveryError{403}", err)
		}
	})
	for name, c := range map[string]hsErase{
		"contact read 500": {contact: 500},
		"deal rename 500":  {page1: `{"results":[{"toObjectId":1}]}`, patch: map[string]int{"1": 500}},
		"gdpr-delete 500":  {page1: noDeals, gdpr: 500},
	} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t, c.respond)
			var de *DeliveryError
			if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 500 {
				t.Fatalf("err = %v, want *DeliveryError{500}", err)
			}
		})
	}
	t.Run("associations 500", func(t *testing.T) {
		v := newVendor(t, func(s seenReq) (int, string) {
			if strings.Contains(s.path(), "/associations/") {
				return 500, ``
			}
			return (&hsErase{}).respond(s)
		})
		var de *DeliveryError
		if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 500 {
			t.Fatalf("err = %v, want *DeliveryError{500}", err)
		}
		if strings.Contains(strings.Join(v.labels(), "\n"), "gdpr-delete") {
			t.Error("gdpr-delete sent after a failed association read")
		}
	})
}

func TestHubSpotErase_TransportFailureIsStatusZero(t *testing.T) {
	v := newVendor(t, (&hsErase{}).respond)
	v.srv.Close()
	var de *DeliveryError
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 0 {
		t.Fatalf("err = %v, want *DeliveryError{0}", err)
	}
}

func TestResendErase_DeletesTheContactByEmail(t *testing.T) {
	v := newVendor(t, func(seenReq) (int, string) { return 200, `{"deleted":true}` })
	if err := resendAt(v, nil).EraseContact(t.Context(), "ada+x@ex.test"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	calls := v.calls()
	if len(calls) != 1 || calls[0].Method != http.MethodDelete || calls[0].path() != "/contacts/ada+x@ex.test" {
		t.Fatalf("calls = %v, want one DELETE /contacts/ada+x@ex.test", v.labels())
	}
	if calls[0].Auth != "Bearer "+resendAt(v, nil).apiKey {
		t.Errorf("auth = %q, want the bearer key", calls[0].Auth)
	}
}

func TestResendErase_PathEscapesTheEmail(t *testing.T) {
	v := newVendor(t, func(seenReq) (int, string) { return 200, `{}` })
	if err := resendAt(v, nil).EraseContact(t.Context(), "a/b;c@ex.test"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := v.calls()[0].path(); got != "/contacts/a%2Fb%3Bc@ex.test" {
		t.Errorf("path = %q, want the email path-escaped", got)
	}
}

func TestResendErase_MissingContactIsSuccess(t *testing.T) {
	v := newVendor(t, func(seenReq) (int, string) { return 404, `{}` })
	if err := resendAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestResendErase_MakesNoOtherCall(t *testing.T) {
	v := newVendor(t, func(seenReq) (int, string) { return 200, `{}` })
	_ = resendAt(v, nil).EraseContact(t.Context(), eraseEmail)
	if got := v.labels(); len(got) != 1 || !strings.HasPrefix(got[0], "DELETE /contacts/") {
		t.Fatalf("calls = %v, want only the one contact DELETE", got)
	}
	for _, s := range v.calls() {
		for _, bad := range []string{"/suppressions", "/topics", "/segments"} {
			if strings.Contains(s.path(), bad) {
				t.Errorf("call %s touches %s", s.label(), bad)
			}
		}
	}
}

func TestResendErase_FailureIsADeliveryError(t *testing.T) {
	for _, st := range []int{422, 500} {
		v := newVendor(t, func(seenReq) (int, string) { return st, `{}` })
		var de *DeliveryError
		if err := resendAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != st {
			t.Errorf("status %d: err = %v, want *DeliveryError with that status", st, err)
		}
	}
}

func TestVendorErase_ErrorsCarryNoEmail(t *testing.T) {
	local, _, _ := strings.Cut(eraseEmail, "@")
	var errs []error
	for _, c := range []hsErase{{contact: 500}, {page1: noDeals, gdpr: 403}, {page1: `{"results":[{"toObjectId":1}]}`, del: map[string]int{"1": 500}}} {
		errs = append(errs, hubspotAt(newVendor(t, c.respond), nil).EraseContact(t.Context(), eraseEmail))
	}
	closed := newVendor(t, (&hsErase{}).respond)
	closed.srv.Close()
	errs = append(errs, hubspotAt(closed, nil).EraseContact(t.Context(), eraseEmail))
	rs := newVendor(t, func(seenReq) (int, string) { return 500, eraseEmail })
	errs = append(errs, resendAt(rs, nil).EraseContact(t.Context(), eraseEmail))
	rc := newVendor(t, func(seenReq) (int, string) { return 500, `{}` })
	rc.srv.Close()
	errs = append(errs, resendAt(rc, nil).EraseContact(t.Context(), eraseEmail))
	for i, err := range errs {
		if err == nil {
			t.Errorf("case %d: err = nil, want a failure", i)
		} else if strings.Contains(err.Error(), local) || strings.Contains(err.Error(), "ex.test") {
			t.Errorf("case %d: error %q carries the email", i, err)
		}
	}
}

type eraseBlockedTransport struct{ t *testing.T }

func (f eraseBlockedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("fake erase made a network call")
	return nil, errors.New("blocked")
}

func TestFakeErase_MakesNoCall(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = eraseBlockedTransport{t}
	t.Cleanup(func() { http.DefaultTransport = old })
	ctx := context.Background()
	if err := (FakeHubSpot{}).EraseContact(ctx, eraseEmail); err != nil {
		t.Errorf("FakeHubSpot err = %v, want nil", err)
	}
	if err := (FakeResend{}).EraseContact(ctx, eraseEmail); err != nil {
		t.Errorf("FakeResend err = %v, want nil", err)
	}
}

func TestHubSpotErase_StatusMapping(t *testing.T) {
	oneDealPage := `{"results":[{"toObjectId":1}]}`
	at := map[string]hsErase{
		"contact read": {contact: 0},
		"deal rename":  {page1: oneDealPage},
		"deal delete":  {page1: oneDealPage},
		"gdpr-delete":  {page1: noDeals},
	}
	for _, st := range []int{401, 403, 429, 500, 503} {
		for name, base := range at {
			c := base
			switch name {
			case "contact read":
				c.contact = st
			case "deal rename":
				c.patch = map[string]int{"1": st}
			case "deal delete":
				c.del = map[string]int{"1": st}
			case "gdpr-delete":
				c.gdpr = st
			}
			v := newVendor(t, c.respond)
			var de *DeliveryError
			err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail)
			if !errors.As(err, &de) || de.Status != st {
				t.Fatalf("%s %d: err = %v, want *DeliveryError{%d}", name, st, err, st)
			}
			if want := st == 401 || st == 403; de.Permanent() != want {
				t.Errorf("%s %d: Permanent() = %v, want %v", name, st, de.Permanent(), want)
			}
			got := strings.Join(v.labels(), "\n")
			if name == "deal rename" && strings.Contains(got, "DELETE") {
				t.Errorf("%d: deal deleted after a failed rename", st)
			}
			if name != "gdpr-delete" && strings.Contains(got, "gdpr-delete") {
				t.Errorf("%s %d: gdpr-delete sent after the failure", name, st)
			}
		}
	}
}

func TestHubSpotErase_PathEscapesTheEmail(t *testing.T) {
	v := newVendor(t, func(s seenReq) (int, string) { return 404, `{}` })
	if err := hubspotAt(v, nil).EraseContact(t.Context(), "a/b?c@ex.test"); err != nil {
		t.Fatalf("err = %v", err)
	}
	c := v.calls()
	if len(c) != 1 || c[0].path() != hsCreatePath+"/a%2Fb%3Fc@ex.test" || c[0].Query != "idProperty=email" {
		t.Fatalf("calls = %v query %q, want the escaped email path and idProperty=email only", v.labels(), c[0].Query)
	}
}

func TestHubSpotErase_DealIDKeepsPrecision(t *testing.T) {
	v := newVendor(t, (&hsErase{page1: `{"results":[{"toObjectId":9007199254740993}]}`}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(v.labels(), "\n"); !strings.Contains(got, "PATCH /crm/v3/objects/deals/9007199254740993\n") {
		t.Errorf("calls =\n%s\nwant the exact deal id", got)
	}
}

func TestHubSpotErase_OversizedPageIsStatusZero(t *testing.T) {
	v := newVendor(t, func(s seenReq) (int, string) {
		if strings.Contains(s.path(), "/associations/") {
			return 200, `{"results":[],"pad":"` + strings.Repeat("x", maxVendorBody+10) + `"}`
		}
		return (&hsErase{}).respond(s)
	})
	var de *DeliveryError
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 0 {
		t.Fatalf("err = %v, want *DeliveryError{0}", err)
	}
	if strings.Contains(strings.Join(v.labels(), "\n"), "gdpr-delete") {
		t.Error("gdpr-delete sent after an unreadable page")
	}
}

func TestResendErase_StatusMapping(t *testing.T) {
	for _, st := range []int{401, 403, 429, 503} {
		v := newVendor(t, func(seenReq) (int, string) { return st, `{}` })
		var de *DeliveryError
		err := resendAt(v, nil).EraseContact(t.Context(), eraseEmail)
		if !errors.As(err, &de) || de.Status != st {
			t.Fatalf("%d: err = %v, want *DeliveryError{%d}", st, err, st)
		}
		if want := st == 401 || st == 403; de.Permanent() != want {
			t.Errorf("%d: Permanent() = %v, want %v", st, de.Permanent(), want)
		}
	}
}

func TestResendErase_TransportFailureIsStatusZero(t *testing.T) {
	v := newVendor(t, func(seenReq) (int, string) { return 200, `{}` })
	v.srv.Close()
	var de *DeliveryError
	if err := resendAt(v, nil).EraseContact(t.Context(), eraseEmail); !errors.As(err, &de) || de.Status != 0 {
		t.Fatalf("err = %v, want *DeliveryError{0}", err)
	}
}

func TestHubSpotErase_ContactReadFailureMakesNoOtherCall(t *testing.T) {
	v := newVendor(t, (&hsErase{contact: 500}).respond)
	if err := hubspotAt(v, nil).EraseContact(t.Context(), eraseEmail); err == nil {
		t.Fatal("err = nil, want a failure")
	}
	if got := v.labels(); len(got) != 1 {
		t.Errorf("calls = %v, want only the contact read", got)
	}
}
