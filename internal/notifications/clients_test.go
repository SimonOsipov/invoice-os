package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

type clSpied struct {
	Method, Host, Path, Auth string
	Remaining                time.Duration // time left on the request context; 0 without a deadline
}

// clSpyTransport records every request and answers 200 unless respond says otherwise.
type clSpyTransport struct {
	mu      sync.Mutex
	reqs    []clSpied
	respond func(n int, r *http.Request) *http.Response
}

func clReply(r *http.Request, code int, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: code, Status: strconv.Itoa(code), Header: header, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
}

func (s *clSpyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var left time.Duration
	if d, ok := r.Context().Deadline(); ok {
		left = time.Until(d)
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, clSpied{r.Method, r.URL.Host, r.URL.Path, r.Header.Get("Authorization"), left})
	n := len(s.reqs)
	s.mu.Unlock()
	if s.respond != nil {
		return s.respond(n, r), nil
	}
	return clReply(r, http.StatusOK, nil), nil
}

func (s *clSpyTransport) seen() []clSpied {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clSpied(nil), s.reqs...)
}

func clGetenv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func clFourKeys(extra ...string) map[string]string {
	m := map[string]string{
		"HUBSPOT_TOKEN": "hs-secret-token", "RESEND_API_KEY": "rs-secret-key",
		"RESEND_SEGMENT_ID": "seg-1", "RESEND_TOPIC_ID": "top-1",
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

var clAda = Contact{Email: "ada@corp.example", FirstName: "Ada", Tags: []string{"registered"}, MarketingEligible: true}

func TestDeliveryClients_PreviewNeverUsesTheTransport(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		vars      map[string]string
	}{
		{"pr-7 with four keys", "pr-7", clFourKeys()},
		{"a named fork with four keys", "ascomply-pr-12", clFourKeys()},
		{"pr-7 with the flag and four keys", "pr-7", clFourKeys("CONTACTS_FAKE", "true")},
		{"pr-7 with a bad flag and a partial key set", "pr-7", map[string]string{"CONTACTS_FAKE": "banana", "HUBSPOT_TOKEN": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &clSpyTransport{}
			mode, hs, rs, err := DeliveryClients(clGetenv(tc.vars), tc.env, spy)
			if err != nil {
				t.Fatalf("deliveryClients err = %v, want nil: a preview environment never refuses to boot", err)
			}
			if mode != ModeFake {
				t.Fatalf("mode = %q, want fake", mode)
			}
			if hs == nil || rs == nil {
				t.Fatalf("clients = %v, %v, want a fake HubSpot and a fake Resend", hs, rs)
			}
			if err := hs.Upsert(t.Context(), clAda); err != nil {
				t.Errorf("Upsert err = %v, want nil", err)
			}
			for _, optIn := range []bool{true, false} {
				if err := rs.Sync(t.Context(), clAda, optIn); err != nil {
					t.Errorf("Sync(optIn=%v) err = %v, want nil", optIn, err)
				}
			}
			if got := spy.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) %+v, want none", len(got), got)
			}
		})
	}
}

func TestDeliveryClients_FakeFlagUsesNoTransport(t *testing.T) {
	spy := &clSpyTransport{}
	mode, hs, rs, err := DeliveryClients(clGetenv(map[string]string{"CONTACTS_FAKE": "true"}), "production", spy)
	if err != nil || mode != ModeFake || hs == nil || rs == nil {
		t.Fatalf("deliveryClients = %q, %v, %v, %v, want fake with both clients", mode, hs, rs, err)
	}
	_ = hs.Upsert(t.Context(), clAda)
	_ = rs.Sync(t.Context(), clAda, true)
	if got := spy.seen(); len(got) != 0 {
		t.Errorf("the transport saw %+v, want no request in fake mode", got)
	}
}

func TestDeliveryClients_RealUsesTheTransport(t *testing.T) {
	for _, env := range []string{"production", ""} {
		t.Run("env_"+env, func(t *testing.T) {
			spy := &clSpyTransport{}
			mode, hs, rs, err := DeliveryClients(clGetenv(clFourKeys()), env, spy)
			if err != nil || mode != ModeReal {
				t.Fatalf("deliveryClients = %q, err %v, want real", mode, err)
			}
			if hs == nil || rs == nil {
				t.Fatalf("clients = %v, %v, want both", hs, rs)
			}
			if err := hs.Upsert(t.Context(), clAda); err != nil {
				t.Fatalf("Upsert err = %v", err)
			}
			got := spy.seen()
			if len(got) != 1 || got[0].Method != http.MethodPatch || got[0].Host != "api.hubapi.com" ||
				!strings.HasPrefix(got[0].Path, "/crm/v3/objects/contacts/") || got[0].Auth != "Bearer hs-secret-token" {
				t.Fatalf("Upsert reached the transport as %+v, want one authorised PATCH to api.hubapi.com", got)
			}
			if err := rs.Sync(t.Context(), clAda, true); err != nil {
				t.Fatalf("Sync err = %v", err)
			}
			got = spy.seen()[1:]
			if len(got) == 0 {
				t.Fatal("Sync never reached the transport")
			}
			for _, r := range got {
				if r.Host != "api.resend.com" || r.Auth != "Bearer rs-secret-key" {
					t.Errorf("Sync request %+v, want api.resend.com with the Resend key", r)
				}
			}
		})
	}
}

func TestDeliveryClients_NightlyReadsTheKeys(t *testing.T) {
	mode, hs, rs, err := DeliveryClients(clGetenv(clFourKeys()), "nightly", &clSpyTransport{})
	if err != nil || mode != ModeReal {
		t.Fatalf("DeliveryClients = %q, err %v, want real", mode, err)
	}
	if _, fake := hs.(FakeHubSpot); fake || hs == nil {
		t.Errorf("hubspot = %T, want the real client", hs)
	}
	if _, fake := rs.(FakeResend); fake || rs == nil {
		t.Errorf("resend = %T, want the real client", rs)
	}
}

func TestDeliveryClients_OffReturnsNoClients(t *testing.T) {
	for _, env := range []string{"production", ""} {
		spy := &clSpyTransport{}
		mode, hs, rs, err := DeliveryClients(clGetenv(nil), env, spy)
		if err != nil || mode != ModeOff {
			t.Fatalf("env %q: deliveryClients = %q, err %v, want off", env, mode, err)
		}
		if hs != nil || rs != nil {
			t.Errorf("env %q: clients = %v, %v, want nil interfaces", env, hs, rs)
		}
	}
}

func TestDeliveryClients_ModeErrorIsReturned(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"partial keys":        {"HUBSPOT_TOKEN": "hs-secret-token"},
		"flag plus a key":     {"CONTACTS_FAKE": "true", "RESEND_API_KEY": "rs-secret-key"},
		"unreadable flag":     {"CONTACTS_FAKE": "banana"},
		"flag plus four keys": clFourKeys("CONTACTS_FAKE", "true"),
	} {
		t.Run(name, func(t *testing.T) {
			spy := &clSpyTransport{}
			mode, hs, rs, err := DeliveryClients(clGetenv(vars), "production", spy)
			if err == nil {
				t.Fatalf("deliveryClients = %q, %v, %v, want an error", mode, hs, rs)
			}
			if hs != nil || rs != nil {
				t.Errorf("clients = %v, %v alongside an error, want nil", hs, rs)
			}
			for _, secret := range []string{"hs-secret-token", "rs-secret-key"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q carries a secret value", err)
				}
			}
		})
	}
}

// A 301 on a PATCH is the response, never followed as a GET to the Location.
func TestDeliveryClients_RealDoesNotFollowRedirects(t *testing.T) {
	redirect := func(n int, r *http.Request) *http.Response {
		if n == 1 {
			return clReply(r, http.StatusMovedPermanently, http.Header{"Location": {"https://evil.example/steal"}})
		}
		return clReply(r, http.StatusOK, nil)
	}
	for name, call := range map[string]func(hs HubSpotClient, rs ResendClient) error{
		"hubspot PATCH": func(hs HubSpotClient, _ ResendClient) error {
			return hs.Upsert(t.Context(), clAda)
		},
		"resend GET": func(_ HubSpotClient, rs ResendClient) error {
			return rs.Sync(t.Context(), clAda, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			spy := &clSpyTransport{respond: redirect}
			mode, hs, rs, err := DeliveryClients(clGetenv(clFourKeys()), "production", spy)
			if err != nil || mode != ModeReal || hs == nil || rs == nil {
				t.Fatalf("deliveryClients = %q, %v, %v, %v, want real with both clients", mode, hs, rs, err)
			}
			cerr := call(hs, rs)
			var de *DeliveryError
			if !errors.As(cerr, &de) || de.Status != http.StatusMovedPermanently {
				t.Errorf("err = %v, want a *DeliveryError with status 301: a redirect is not a delivery", cerr)
			}
			for _, r := range spy.seen() {
				if r.Host == "evil.example" {
					t.Errorf("the client followed the redirect to %+v", r)
				}
			}
			if n := len(spy.seen()); n != 1 {
				t.Errorf("the transport saw %d requests, want 1", n)
			}
		})
	}
}

func TestDeliveryClients_RealTimesOutAfterTenSeconds(t *testing.T) {
	spy := &clSpyTransport{}
	_, hs, rs, err := DeliveryClients(clGetenv(clFourKeys()), "production", spy)
	if err != nil || hs == nil || rs == nil {
		t.Fatalf("deliveryClients err = %v, clients %v %v", err, hs, rs)
	}
	_ = hs.Upsert(t.Context(), clAda)
	_ = rs.Sync(t.Context(), clAda, false)
	got := spy.seen()
	if len(got) < 2 {
		t.Fatalf("the transport saw %d requests, want at least one per vendor", len(got))
	}
	for _, r := range got {
		if r.Remaining <= 8*time.Second || r.Remaining > 10*time.Second {
			t.Errorf("%s %s%s carried %v of deadline, want a client timeout of 10s", r.Method, r.Host, r.Path, r.Remaining)
		}
	}
}

func clWiring(t *testing.T, env string, vars map[string]string) NewsletterWiring {
	t.Helper()
	contacts, _, _, err := DeliveryClients(clGetenv(vars), env, nil)
	if err != nil {
		t.Fatalf("deliveryClients: %v", err)
	}
	w, err := NewsletterClients(clGetenv(vars), env, contacts, nil)
	if err != nil {
		t.Fatalf("newsletterClients: %v", err)
	}
	return w
}

func TestNewsletterClients_PreviewIsCaptureAndFake(t *testing.T) {
	vars := clFourKeys("RESEND_BLOG_TOPIC_ID", "bt", "RESEND_BLOG_SEGMENT_ID", "bs", "RESEND_SENDING_KEY", "sk")
	w := clWiring(t, "pr-7", vars)
	if _, ok := w.Sender.(*accountmail.Capture); !ok || w.MailMode != accountmail.ModeCapture || w.Mode != ModeFake || !w.Enabled {
		t.Errorf("wiring = %+v, want capture mail, fake newsletter, enabled", w)
	}
	if _, ok := w.Resend.(FakeBlogResend); !ok {
		t.Errorf("resend client = %T, want FakeBlogResend", w.Resend)
	}
}

func TestNewsletterClients_NightlyIsFakeAndEnabled(t *testing.T) {
	vars := clFourKeys("RESEND_BLOG_TOPIC_ID", "bt", "RESEND_BLOG_SEGMENT_ID", "bs", "RESEND_SENDING_KEY", "sk")
	w := clWiring(t, "nightly", vars)
	if w.Mode != ModeFake || !w.Enabled {
		t.Errorf("wiring = %+v, want fake newsletter, enabled", w)
	}
	if _, ok := w.Resend.(FakeBlogResend); !ok {
		t.Errorf("resend client = %T, want FakeBlogResend", w.Resend)
	}
}

func TestNewsletterClients_NightlyMailIsCaptureEvenWithASendingKey(t *testing.T) {
	vars := clFourKeys("RESEND_BLOG_TOPIC_ID", "bt", "RESEND_BLOG_SEGMENT_ID", "bs", "RESEND_SENDING_KEY", "sk")
	w := clWiring(t, "nightly", vars)
	if _, ok := w.Sender.(*accountmail.Capture); !ok || w.MailMode != accountmail.ModeCapture {
		t.Errorf("wiring = %+v, want capture mail", w)
	}
}

func TestNewsletterClients_ProductionWithoutBlogKeysIsDisabled(t *testing.T) {
	w := clWiring(t, "production", clFourKeys("RESEND_SENDING_KEY", "sk"))
	if w.Enabled || w.Mode != ModeOff {
		t.Errorf("wiring = %+v, want disabled", w)
	}
}

func TestNewsletterClients_NoSendingKeyIsDisabled(t *testing.T) {
	w := clWiring(t, "production", clFourKeys("RESEND_BLOG_TOPIC_ID", "bt", "RESEND_BLOG_SEGMENT_ID", "bs"))
	if w.Enabled || w.Mode != ModeReal || w.MailMode != accountmail.ModeOff {
		t.Errorf("wiring = %+v, want real newsletter, mail off, disabled", w)
	}
}

// clDealStage runs OpenDemoDeal through DeliveryClients on a stub HubSpot and returns the
// dealstage of the deal create.
func clDealStage(t *testing.T, env string, vars map[string]string) string {
	t.Helper()
	var stage string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/associations/deals"):
			body = `{"results":[]}`
		case r.Method == http.MethodGet:
			body = `{"id":"1"}`
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deals"):
			var in struct {
				Properties map[string]string `json:"properties"`
			}
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &in); err != nil {
				t.Errorf("deal create body: %v", err)
			}
			stage = in.Properties["dealstage"]
		}
		resp := clReply(r, http.StatusOK, nil)
		resp.Body = io.NopCloser(strings.NewReader(body))
		return resp, nil
	})
	mode, hs, _, err := DeliveryClients(clGetenv(vars), env, rt)
	if err != nil || mode != ModeReal {
		t.Fatalf("DeliveryClients = %q, %v, want real", mode, err)
	}
	if err := hs.OpenDemoDeal(t.Context(), clAda, "Deal"); err != nil {
		t.Fatalf("OpenDemoDeal: %v", err)
	}
	return stage
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeliveryClients_DealStageVariableReachesTheDealCreate(t *testing.T) {
	if got := clDealStage(t, "nightly", clFourKeys("HUBSPOT_DEAL_STAGE", "appointmentscheduled")); got != "appointmentscheduled" {
		t.Errorf("dealstage = %q, want appointmentscheduled", got)
	}
}

func TestDeliveryClients_NoDealStageVariableKeepsTheConstant(t *testing.T) {
	if got := clDealStage(t, "production", clFourKeys()); got != "5716044996" {
		t.Errorf("dealstage = %q, want 5716044996", got)
	}
}

func TestDeliveryClients_DealStageAloneLeavesContactsOff(t *testing.T) {
	mode, hs, rs, err := DeliveryClients(clGetenv(map[string]string{"HUBSPOT_DEAL_STAGE": "appointmentscheduled"}), "nightly", &clSpyTransport{})
	if err != nil || mode != ModeOff || hs != nil || rs != nil {
		t.Errorf("DeliveryClients = %q, %v, %v, %v, want off with nil clients", mode, hs, rs, err)
	}
}
