package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The status hangStatus holds the request open until the client gives up.
const hangStatus = -1

type seenReq struct {
	Method, URI, Query, Auth, CType string
	Body                            []byte
}

// path is the raw request path, query cut off.
func (s seenReq) path() string {
	p, _, _ := strings.Cut(s.URI, "?")
	return p
}

func (s seenReq) label() string { return s.Method + " " + s.path() }

type fakeVendor struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []seenReq
}

func newVendor(t *testing.T, respond func(s seenReq) (int, string)) *fakeVendor {
	t.Helper()
	v := &fakeVendor{}
	release := make(chan struct{})
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := seenReq{
			Method: r.Method, URI: r.RequestURI, Query: r.URL.RawQuery,
			Auth: r.Header.Get("Authorization"), CType: r.Header.Get("Content-Type"), Body: body,
		}
		v.mu.Lock()
		v.got = append(v.got, s)
		v.mu.Unlock()
		status, resp := respond(s)
		if status == hangStatus {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(v.srv.Close)
	t.Cleanup(func() { close(release) })
	return v
}

func (v *fakeVendor) calls() []seenReq {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]seenReq(nil), v.got...)
}

func (v *fakeVendor) labels() []string {
	var out []string
	for _, s := range v.calls() {
		out = append(out, s.label())
	}
	return out
}

// shortClient makes a hung vendor fail in 150 ms instead of the 10 s default.
func shortClient() *http.Client { return &http.Client{Timeout: 150 * time.Millisecond} }

func jsonProps(t *testing.T, s seenReq) map[string]string {
	t.Helper()
	var b struct {
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal(s.Body, &b); err != nil {
		t.Fatalf("%s body %q is not JSON: %v", s.label(), s.Body, err)
	}
	return b.Properties
}

func bothTags() []string { return []string{"registered", "demo request"} }

func fullContact(email string) Contact {
	return Contact{
		Email: email, FirstName: "Ada", LastName: "Lovelace", Company: "Analytical Engines",
		Tags: bothTags(), MarketingEligible: true, ResendApplies: true,
	}
}

const hsCreatePath = "/crm/v3/objects/contacts"

func hubspotAt(v *fakeVendor, hc *http.Client) *HubSpot {
	return NewHubSpot(v.srv.URL, Keys{HubSpotToken: "hs-tok"}, hc)
}

// hubspotRoutes answers the PATCH with patch and the create POST with create.
func hubspotRoutes(patch, create int) func(seenReq) (int, string) {
	return func(s seenReq) (int, string) {
		switch {
		case s.Method == http.MethodPatch:
			return patch, `{"id":"1"}`
		case s.Method == http.MethodPost && s.path() == hsCreatePath:
			return create, `{"id":"1"}`
		}
		return 500, `{"message":"unexpected request"}`
	}
}

func TestHubSpotUpsert_PatchesByEmailWithAppendedTags(t *testing.T) {
	v := newVendor(t, hubspotRoutes(200, 201))
	if err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example")); err != nil {
		t.Fatalf("Upsert() err = %v, want nil", err)
	}
	calls := v.calls()
	if len(calls) != 1 {
		t.Fatalf("requests = %v, want exactly one PATCH", v.labels())
	}
	c := calls[0]
	if c.Method != http.MethodPatch || c.path() != hsCreatePath+"/ada@corp.example" {
		t.Errorf("request = %s, want PATCH %s/ada@corp.example", c.label(), hsCreatePath)
	}
	if c.Query != "idProperty=email" {
		t.Errorf("query = %q, want %q", c.Query, "idProperty=email")
	}
	if c.Auth != "Bearer hs-tok" {
		t.Errorf("Authorization = %q, want %q", c.Auth, "Bearer hs-tok")
	}
	if !strings.HasPrefix(c.CType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", c.CType)
	}
	want := map[string]string{
		"firstname": "Ada", "lastname": "Lovelace", "company": "Analytical Engines",
		"ascomply_contact_tags": ";registered;demo_request",
	}
	if got := jsonProps(t, c); !reflect.DeepEqual(got, want) {
		t.Errorf("properties = %v, want %v", got, want)
	}
}

func TestHubSpotUpsert_TagsKeepTheLeadingSemicolon(t *testing.T) {
	for name, tc := range map[string]struct {
		tags []string
		want string
	}{
		"registered only": {[]string{"registered"}, ";registered"},
		"demo only":       {[]string{"demo request"}, ";demo_request"},
		"both":            {bothTags(), ";registered;demo_request"},
	} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t, hubspotRoutes(200, 201))
			c := fullContact("ada@corp.example")
			c.Tags = tc.tags
			if err := hubspotAt(v, nil).Upsert(t.Context(), c); err != nil {
				t.Fatalf("Upsert() err = %v, want nil", err)
			}
			calls := v.calls()
			if len(calls) != 1 {
				t.Fatalf("requests = %v, want one PATCH", v.labels())
			}
			if got := jsonProps(t, calls[0])["ascomply_contact_tags"]; got != tc.want {
				t.Errorf("ascomply_contact_tags = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty value clears the tags HubSpot already holds, so a contact with no tags sends none.
func TestHubSpotUpsert_EmptyTagsSendNoTagsProperty(t *testing.T) {
	for name, tags := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t, hubspotRoutes(200, 201))
			c := fullContact("ada@corp.example")
			c.Tags = tags
			if err := hubspotAt(v, nil).Upsert(t.Context(), c); err != nil {
				t.Fatalf("Upsert() err = %v, want nil", err)
			}
			calls := v.calls()
			if len(calls) != 1 {
				t.Fatalf("requests = %v, want one PATCH", v.labels())
			}
			if got, present := jsonProps(t, calls[0])["ascomply_contact_tags"]; present {
				t.Errorf("ascomply_contact_tags = %q, want the property dropped", got)
			}
		})
	}
}

func TestHubSpotUpsert_CreatesOn404(t *testing.T) {
	v := newVendor(t, hubspotRoutes(404, 201))
	if err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example")); err != nil {
		t.Fatalf("Upsert() err = %v, want nil", err)
	}
	calls := v.calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %v, want a PATCH then one POST", v.labels())
	}
	patch, post := calls[0], calls[1]
	if patch.Method != http.MethodPatch {
		t.Errorf("first request = %s, want the PATCH", patch.label())
	}
	if post.Method != http.MethodPost || post.path() != hsCreatePath || post.Query != "" {
		t.Errorf("second request = %s ?%s, want POST %s with no query", post.label(), post.Query, hsCreatePath)
	}
	if post.Auth != "Bearer hs-tok" {
		t.Errorf("POST Authorization = %q, want %q", post.Auth, "Bearer hs-tok")
	}
	want := jsonProps(t, patch)
	want["email"] = "ada@corp.example"
	if got := jsonProps(t, post); !reflect.DeepEqual(got, want) {
		t.Errorf("POST properties = %v, want the PATCH properties plus email: %v", got, want)
	}
}

func TestHubSpotUpsert_DropsBlankProperties(t *testing.T) {
	v := newVendor(t, hubspotRoutes(404, 201))
	c := fullContact("ada@corp.example")
	c.LastName, c.Company = "", ""
	if err := hubspotAt(v, nil).Upsert(t.Context(), c); err != nil {
		t.Fatalf("Upsert() err = %v, want nil", err)
	}
	calls := v.calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %v, want a PATCH then one POST", v.labels())
	}
	for _, s := range calls {
		props := jsonProps(t, s)
		for _, blank := range []string{"company", "lastname"} {
			if _, ok := props[blank]; ok {
				t.Errorf("%s sends blank %q: %v", s.Method, blank, props)
			}
		}
		if props["firstname"] != "Ada" {
			t.Errorf("%s firstname = %q, want Ada", s.Method, props["firstname"])
		}
	}
}

func TestHubSpotUpsert_ErrorsOnFailure(t *testing.T) {
	t.Run("PATCH 503 stops, no create", func(t *testing.T) {
		v := newVendor(t, hubspotRoutes(503, 201))
		err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example"))
		var de *DeliveryError
		if !errors.As(err, &de) || de.Status != 503 {
			t.Fatalf("err = %v, want a *DeliveryError with status 503", err)
		}
		if got := v.labels(); len(got) != 1 {
			t.Errorf("requests = %v, want the PATCH only", got)
		}
	})

	t.Run("POST 409 is an error", func(t *testing.T) {
		v := newVendor(t, hubspotRoutes(404, 409))
		err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example"))
		var de *DeliveryError
		if !errors.As(err, &de) || de.Status != 409 {
			t.Fatalf("err = %v, want a *DeliveryError with status 409", err)
		}
		if got := v.labels(); len(got) != 2 {
			t.Errorf("requests = %v, want a PATCH then one POST", got)
		}
	})

	t.Run("any 2xx delivers", func(t *testing.T) {
		for _, ok := range []int{200, 201, 202, 204, 299} {
			t.Run(http.StatusText(ok), func(t *testing.T) {
				v := newVendor(t, hubspotRoutes(ok, 201))
				if err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example")); err != nil {
					t.Errorf("PATCH %d: err = %v, want nil", ok, err)
				}
				if got := v.labels(); len(got) != 1 {
					t.Errorf("PATCH %d: requests = %v, want the PATCH only", ok, got)
				}
				v = newVendor(t, hubspotRoutes(404, ok))
				if err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example")); err != nil {
					t.Errorf("POST %d: err = %v, want nil", ok, err)
				}
			})
		}
	})

	t.Run("3xx without a Location is an error", func(t *testing.T) {
		for _, bad := range []int{300, 304} {
			v := newVendor(t, hubspotRoutes(bad, 201))
			err := hubspotAt(v, nil).Upsert(t.Context(), fullContact("ada@corp.example"))
			var de *DeliveryError
			if !errors.As(err, &de) || de.Status != bad {
				t.Errorf("PATCH %d: err = %v, want a *DeliveryError with that status", bad, err)
			}
		}
	})

	t.Run("a hung PATCH times out", func(t *testing.T) {
		v := newVendor(t, hubspotRoutes(hangStatus, 201))
		err := hubspotAt(v, shortClient()).Upsert(t.Context(), fullContact("ada@corp.example"))
		var de *DeliveryError
		if !errors.As(err, &de) || de.Status != 0 {
			t.Fatalf("err = %v, want a *DeliveryError with status 0", err)
		}
		if got := v.labels(); len(got) != 1 {
			t.Errorf("requests = %v, want the PATCH only", got)
		}
	})
}

// failStage builds a server that fails one named vendor call.
type failStage struct {
	name  string
	build func(t *testing.T, status int, body string) (*fakeVendor, func(*http.Client) error)
}

func hubspotStages(email string) []failStage {
	return []failStage{
		{"hubspot PATCH", func(t *testing.T, status int, body string) (*fakeVendor, func(*http.Client) error) {
			v := newVendor(t, func(s seenReq) (int, string) { return status, body })
			return v, func(hc *http.Client) error { return hubspotAt(v, hc).Upsert(t.Context(), fullContact(email)) }
		}},
		{"hubspot POST", func(t *testing.T, status int, body string) (*fakeVendor, func(*http.Client) error) {
			v := newVendor(t, func(s seenReq) (int, string) {
				if s.Method == http.MethodPatch {
					return 404, `{}`
				}
				return status, body
			})
			return v, func(hc *http.Client) error { return hubspotAt(v, hc).Upsert(t.Context(), fullContact(email)) }
		}},
	}
}

func resendStages(email string) []failStage {
	run := func(v *fakeVendor) func(*http.Client) error {
		return func(hc *http.Client) error {
			return resendAt(v, hc).Sync(context.Background(), regContact(email), true)
		}
	}
	// failAt answers 404 to the GET and 2xx to every call except the named one.
	failAt := func(method, suffix string) func(*testing.T, int, string) (*fakeVendor, func(*http.Client) error) {
		return func(t *testing.T, status int, body string) (*fakeVendor, func(*http.Client) error) {
			v := newVendor(t, func(s seenReq) (int, string) {
				isTarget := s.Method == method && strings.HasSuffix(s.path(), suffix)
				switch {
				case isTarget:
					return status, body
				case s.Method == http.MethodGet:
					return 404, `{}`
				}
				return 200, `{"id":"1"}`
			})
			return v, run(v)
		}
	}
	return []failStage{
		{"resend GET", func(t *testing.T, status int, body string) (*fakeVendor, func(*http.Client) error) {
			v := newVendor(t, func(s seenReq) (int, string) { return status, body })
			return v, run(v)
		}},
		{"resend create", failAt(http.MethodPost, "/contacts")},
		{"resend segment", failAt(http.MethodPost, "/segments/seg-1")},
		{"resend topics", failAt(http.MethodPatch, "/topics")},
	}
}

func TestClients_ClassifyPermanentAndTransient(t *testing.T) {
	permanent := []int{400, 401, 403, 404, 409, 422, 499}
	transient := []int{300, 304, 408, 429, 500, 502, 503, 599}
	all := append(hubspotStages("ada@corp.example"), resendStages("ada@corp.example")...)

	for _, st := range all {
		for _, status := range append(append([]int{}, permanent...), transient...) {
			// A 404 on a PATCH or GET is the not-found branch, not a failure.
			if status == 404 && (st.name == "hubspot PATCH" || st.name == "resend GET") {
				continue
			}
			t.Run(st.name+"/"+http.StatusText(status), func(t *testing.T) {
				_, call := st.build(t, status, `{}`)
				err := call(nil)
				var de *DeliveryError
				if !errors.As(err, &de) {
					t.Fatalf("err = %v, want a *DeliveryError", err)
				}
				if de.Status != status {
					t.Errorf("Status = %d, want %d", de.Status, status)
				}
				if !strings.Contains(err.Error(), strconv.Itoa(status)) {
					t.Errorf("err = %q, want it to carry the status %d", err, status)
				}
				wantPermanent := status >= 400 && status < 500 && status != 408 && status != 429
				if de.Permanent() != wantPermanent {
					t.Errorf("Permanent() = %v for %d, want %v", de.Permanent(), status, wantPermanent)
				}
			})
		}

		t.Run(st.name+"/timeout", func(t *testing.T) {
			_, call := st.build(t, hangStatus, "")
			err := call(shortClient())
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want a *DeliveryError", err)
			}
			if de.Status != 0 || de.Permanent() {
				t.Errorf("timeout: Status = %d, Permanent() = %v, want 0 and transient", de.Status, de.Permanent())
			}
		})
	}
}

// errChain flattens err and everything it wraps, as Sentry's exception chain does.
func errChain(err error) []string {
	var out []string
	for ; err != nil; err = errors.Unwrap(err) {
		out = append(out, err.Error())
	}
	return out
}

func TestClients_ErrorsNeverCarryTheEmail(t *testing.T) {
	const email = "ada.lovelace+test@corp.example"
	needles := []string{email, url.QueryEscape(email), "ada.lovelace", "corp.example", "hs-tok", "rs-key"}
	// The vendor echoes the address back, as a 409 or 422 message may.
	echo := `{"message":"contact ` + email + ` already exists","auth":"Bearer hs-tok rs-key"}`

	assertClean := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("err = nil, want a failure")
		}
		for _, text := range errChain(err) {
			for _, n := range needles {
				if strings.Contains(strings.ToLower(text), strings.ToLower(n)) {
					t.Errorf("error text %q carries %q", text, n)
				}
			}
		}
	}

	for _, st := range append(hubspotStages(email), resendStages(email)...) {
		t.Run(st.name+"/vendor echoes the address", func(t *testing.T) {
			_, call := st.build(t, 422, echo)
			assertClean(t, call(nil))
		})
		t.Run(st.name+"/server error", func(t *testing.T) {
			_, call := st.build(t, 500, echo)
			assertClean(t, call(nil))
		})
		// net/http's url.Error prints the request URL, which holds the address.
		t.Run(st.name+"/timeout", func(t *testing.T) {
			_, call := st.build(t, hangStatus, "")
			assertClean(t, call(shortClient()))
		})
		t.Run(st.name+"/connection refused", func(t *testing.T) {
			v, call := st.build(t, 200, "{}")
			v.srv.Close()
			assertClean(t, call(shortClient()))
		})
	}
}

func TestClients_DefaultClientTimesOutAfterTenSeconds(t *testing.T) {
	keys := Keys{HubSpotToken: "t", ResendAPIKey: "k", ResendSegmentID: "s", ResendTopicID: "o"}
	if got := NewHubSpot("http://x", keys, nil).hc.Timeout; got != 10*time.Second {
		t.Errorf("HubSpot default timeout = %v, want 10s", got)
	}
	if got := NewResend("http://x", keys, nil).hc.Timeout; got != 10*time.Second {
		t.Errorf("Resend default timeout = %v, want 10s", got)
	}

	own := &http.Client{Timeout: time.Second}
	if NewHubSpot("http://x", keys, own).hc != own || NewResend("http://x", keys, own).hc != own {
		t.Error("an injected client is not used as given")
	}
}

func TestClients_CancelledContextSendsNothingAndIsTransient(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range map[string]func(v *fakeVendor) error{
		"hubspot": func(v *fakeVendor) error { return hubspotAt(v, nil).Upsert(ctx, fullContact(adaEmail)) },
		"resend":  func(v *fakeVendor) error { return resendAt(v, nil).Sync(ctx, regContact(adaEmail), true) },
	} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t, func(seenReq) (int, string) { return 200, `{}` })
			err := call(v)
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want a *DeliveryError", err)
			}
			if de.Status != 0 || de.Permanent() {
				t.Errorf("Status = %d, Permanent() = %v, want 0 and transient", de.Status, de.Permanent())
			}
			if got := v.labels(); len(got) != 0 {
				t.Errorf("requests = %v, want none", got)
			}
		})
	}
}

// A request that cannot be built (a malformed base URL) is a status-0 transient error whose
// text never carries the URL. River's 25 attempts bound the retries.
func TestClients_UnbuildableRequestIsATransientDeliveryError(t *testing.T) {
	const bad = "http://bad\x7fhost.invalid"
	keys := Keys{HubSpotToken: "t", ResendAPIKey: "k", ResendSegmentID: "s", ResendTopicID: "o"}
	for name, err := range map[string]error{
		"hubspot": NewHubSpot(bad, keys, nil).Upsert(t.Context(), fullContact(adaEmail)),
		"resend":  NewResend(bad, keys, nil).Sync(t.Context(), regContact(adaEmail), true),
	} {
		t.Run(name, func(t *testing.T) {
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want a *DeliveryError", err)
			}
			if de.Status != 0 || de.Permanent() {
				t.Errorf("Status = %d, Permanent() = %v, want 0 and transient", de.Status, de.Permanent())
			}
			for _, text := range errChain(err) {
				if strings.Contains(text, "invalid") || strings.Contains(text, adaEmail) {
					t.Errorf("error text %q carries the URL or the address", text)
				}
			}
		})
	}
}

// A redirect must not turn a refused request into a delivered one: Go follows a 301 on a
// PATCH or POST as a GET, and the target's 200 would read as success.
func TestClients_RedirectIsNotDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/moved" {
			w.Header().Set("Location", "/moved")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	keys := Keys{HubSpotToken: "t", ResendAPIKey: "k", ResendSegmentID: "s", ResendTopicID: "o"}
	for name, call := range map[string]func() error{
		"hubspot PATCH": func() error { return NewHubSpot(srv.URL, keys, nil).Upsert(t.Context(), fullContact(adaEmail)) },
		"resend GET":    func() error { return NewResend(srv.URL, keys, nil).Sync(t.Context(), regContact(adaEmail), true) },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var de *DeliveryError
			if !errors.As(err, &de) || de.Status != 301 || de.Permanent() {
				t.Errorf("err = %v, want a transient *DeliveryError with status 301", err)
			}
		})
	}
}

// dealPlan scripts the four HubSpot steps of OpenDemoDeal; a zero status falls to the default.
type dealPlan struct {
	contactGet, contactPost, contactGet2, assoc, batch, dealPost int
	contactBody, assocBody, batchBody, postContactBody           string
}

func (p dealPlan) respond(s seenReq) (int, string) {
	pick := func(st, def int) int {
		if st == 0 {
			return def
		}
		return st
	}
	or := func(b, def string) string {
		if b == "" {
			return def
		}
		return b
	}
	switch {
	case s.Method == http.MethodGet && strings.HasPrefix(s.path(), hsCreatePath+"/"):
		return pick(p.contactGet, 200), or(p.contactBody, `{"id":"77"}`)
	case s.Method == http.MethodPost && s.path() == hsCreatePath:
		return pick(p.contactPost, 201), or(p.postContactBody, `{"id":"91"}`)
	case s.Method == http.MethodGet && strings.Contains(s.path(), "/associations/deals"):
		return pick(p.assoc, 200), or(p.assocBody, `{"results":[]}`)
	case s.path() == "/crm/v3/objects/deals/batch/read":
		return pick(p.batch, 200), or(p.batchBody, `{"results":[]}`)
	case s.Method == http.MethodPost && s.path() == "/crm/v3/objects/deals":
		return pick(p.dealPost, 201), `{"id":"500"}`
	}
	return 500, `{"message":"unexpected request"}`
}

func assocOf(ids ...string) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, `{"toObjectId":`+id+`}`)
	}
	return `{"results":[` + strings.Join(parts, ",") + `]}`
}

func dealRes(closed string) string {
	return `{"id":"1","properties":{"hs_is_closed":` + closed + `}}`
}

func batchOf(res ...string) string { return `{"results":[` + strings.Join(res, ",") + `]}` }

func runDeal(t *testing.T, p dealPlan) (*fakeVendor, error) {
	t.Helper()
	v := newVendor(t, p.respond)
	return v, hubspotAt(v, shortClient()).OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "Analytical Engines — demo request")
}

func dealPosts(v *fakeVendor) []seenReq {
	var out []seenReq
	for _, s := range v.calls() {
		if s.Method == http.MethodPost && s.path() == "/crm/v3/objects/deals" {
			out = append(out, s)
		}
	}
	return out
}

func hasLabel(v *fakeVendor, label string) bool {
	for _, l := range v.labels() {
		if l == label {
			return true
		}
	}
	return false
}

const batchLabel = "POST /crm/v3/objects/deals/batch/read"

func TestHubSpotOpenDemoDeal_CreatesTheDealAtTheFirstStage(t *testing.T) {
	v, err := runDeal(t, dealPlan{})
	if err != nil {
		t.Fatalf("OpenDemoDeal() err = %v, want nil", err)
	}
	want := []string{"GET " + hsCreatePath + "/ada@corp.example", "GET /crm/v4/objects/contacts/77/associations/deals", "POST /crm/v3/objects/deals"}
	if got := v.labels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	for _, c := range v.calls() {
		if c.Auth != "Bearer hs-tok" || c.CType != "application/json" {
			t.Errorf("%s auth = %q, content type = %q", c.label(), c.Auth, c.CType)
		}
	}
	var body struct {
		Properties   map[string]string `json:"properties"`
		Associations []struct {
			To    struct{ ID string } `json:"to"`
			Types []struct {
				Category string `json:"associationCategory"`
				TypeID   int    `json:"associationTypeId"`
			} `json:"types"`
		} `json:"associations"`
	}
	if err := json.Unmarshal(dealPosts(v)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	wantProps := map[string]string{"dealname": "Analytical Engines — demo request", "pipeline": "default", "dealstage": "5716044996"}
	if !reflect.DeepEqual(body.Properties, wantProps) {
		t.Errorf("properties = %v, want %v", body.Properties, wantProps)
	}
	if len(body.Associations) != 1 || body.Associations[0].To.ID != "77" ||
		len(body.Associations[0].Types) != 1 || body.Associations[0].Types[0].Category != "HUBSPOT_DEFINED" || body.Associations[0].Types[0].TypeID != 3 {
		t.Errorf("associations = %+v, want contact 77 via HUBSPOT_DEFINED 3", body.Associations)
	}
}

func TestHubSpotOpenDemoDeal_NoDealsSkipsTheBatchRead(t *testing.T) {
	v, err := runDeal(t, dealPlan{})
	if err != nil || hasLabel(v, batchLabel) {
		t.Errorf("err = %v, requests = %v, want nil and no batch read", err, v.labels())
	}
}

func dealAssocID(t *testing.T, v *fakeVendor) string {
	t.Helper()
	posts := dealPosts(v)
	if len(posts) != 1 {
		t.Fatalf("deal POSTs = %d, want 1 (requests %v)", len(posts), v.labels())
	}
	var b struct {
		Associations []struct {
			To struct{ ID string } `json:"to"`
		} `json:"associations"`
	}
	if err := json.Unmarshal(posts[0].Body, &b); err != nil || len(b.Associations) != 1 {
		t.Fatalf("deal body %q: %v", posts[0].Body, err)
	}
	return b.Associations[0].To.ID
}

func TestHubSpotOpenDemoDeal_CreatesTheContactOn404(t *testing.T) {
	v, err := runDeal(t, dealPlan{contactGet: 404})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if id := dealAssocID(t, v); id != "91" {
		t.Errorf("association id = %q, want 91", id)
	}
	var create seenReq
	for _, c := range v.calls() {
		if c.Method == http.MethodPost && c.path() == hsCreatePath {
			create = c
		}
	}
	want := map[string]string{"email": "ada@corp.example", "firstname": "Ada", "lastname": "Lovelace", "company": "Analytical Engines"}
	if got := jsonProps(t, create); !reflect.DeepEqual(got, want) {
		t.Errorf("contact properties = %v, want %v (no tags)", got, want)
	}

	// Blank fields are left out.
	v2 := newVendor(t, dealPlan{contactGet: 404}.respond)
	if err := hubspotAt(v2, nil).OpenDemoDeal(t.Context(), Contact{Email: "bare@corp.example", Tags: bothTags()}, "x"); err != nil {
		t.Fatal(err)
	}
	for _, c := range v2.calls() {
		if c.Method == http.MethodPost && c.path() == hsCreatePath {
			if got := jsonProps(t, c); !reflect.DeepEqual(got, map[string]string{"email": "bare@corp.example"}) {
				t.Errorf("bare contact properties = %v, want email only", got)
			}
		}
	}
}

func TestHubSpotOpenDemoDeal_ContactCreate409ReadsTheContactAgain(t *testing.T) {
	var gets int
	v := newVendor(t, func(s seenReq) (int, string) {
		if s.Method == http.MethodGet && strings.HasPrefix(s.path(), hsCreatePath+"/") {
			gets++
			if gets == 1 {
				return 404, `{}`
			}
			return 200, `{"id":"55"}`
		}
		return dealPlan{contactPost: 409}.respond(s)
	})
	if err := hubspotAt(v, nil).OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "n"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if id := dealAssocID(t, v); id != "55" {
		t.Errorf("association id = %q, want 55", id)
	}
}

func TestHubSpotOpenDemoDeal_ContactCreate409ThenStill404IsAnError(t *testing.T) {
	v, err := runDeal(t, dealPlan{contactGet: 404, contactPost: 409})
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 409 {
		t.Fatalf("err = %v, want *DeliveryError{409}", err)
	}
	if len(dealPosts(v)) != 0 {
		t.Errorf("a deal was created: %v", v.labels())
	}
}

func TestHubSpotOpenDemoDeal_OpenDealBlocksANewOne(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("1001"), batchBody: batchOf(dealRes(`"false"`))})
	if err != nil || len(dealPosts(v)) != 0 {
		t.Errorf("err = %v, requests = %v, want nil and no deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_ClosedDealsDoNotBlock(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("1001", "1002"), batchBody: batchOf(dealRes(`"true"`), dealRes(`"true"`))})
	if err != nil || len(dealPosts(v)) != 1 {
		t.Errorf("err = %v, requests = %v, want nil and one deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_OneOpenAmongClosedBlocks(t *testing.T) {
	v, err := runDeal(t, dealPlan{
		assocBody: assocOf("1", "2", "3"),
		batchBody: batchOf(dealRes(`"true"`), dealRes(`"true"`), dealRes(`"false"`)),
	})
	if err != nil || len(dealPosts(v)) != 0 {
		t.Errorf("err = %v, requests = %v, want nil and no deal POST", err, v.labels())
	}
	// The open deal sits in the middle too.
	v, err = runDeal(t, dealPlan{
		assocBody: assocOf("1", "2", "3"),
		batchBody: batchOf(dealRes(`"true"`), dealRes(`"false"`), dealRes(`"true"`)),
	})
	if err != nil || len(dealPosts(v)) != 0 {
		t.Errorf("middle open: err = %v, requests = %v, want no deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_MissingClosedPropertyCountsAsOpen(t *testing.T) {
	for _, res := range []string{dealRes(`null`), `{"id":"1","properties":{}}`} {
		v, err := runDeal(t, dealPlan{assocBody: assocOf("1"), batchBody: batchOf(res)})
		if err != nil || len(dealPosts(v)) != 0 {
			t.Errorf("%s: err = %v, requests = %v, want no deal POST", res, err, v.labels())
		}
	}
}

func TestHubSpotOpenDemoDeal_RetryAfterLostCreateResponseMakesOneDeal(t *testing.T) {
	var mu sync.Mutex
	var deals, posts int
	v := newVendor(t, func(s seenReq) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case s.Method == http.MethodGet && strings.Contains(s.path(), "/associations/deals"):
			if deals == 0 {
				return 200, `{"results":[]}`
			}
			return 200, assocOf("500")
		case s.path() == "/crm/v3/objects/deals/batch/read":
			return 200, batchOf(dealRes(`"false"`))
		case s.Method == http.MethodPost && s.path() == "/crm/v3/objects/deals":
			posts++
			deals++
			if posts == 1 {
				return 500, `{}`
			}
			return 201, `{"id":"500"}`
		}
		return dealPlan{}.respond(s)
	})
	hs := hubspotAt(v, nil)
	if err := hs.OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "n"); err == nil {
		t.Fatal("first call err = nil, want the lost response to fail")
	}
	if err := hs.OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "n"); err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if deals != 1 || len(dealPosts(v)) != 1 {
		t.Errorf("deals = %d, deal POSTs = %d, want 1 and 1", deals, len(dealPosts(v)))
	}
}

// dealStageLabels is the OpenDemoDeal request order; failingPlan makes stage i answer with status.
var dealStageLabels = []string{"contact", "associations", "batch", "deal"}

func failingPlan(stage, status int) dealPlan {
	p := dealPlan{assocBody: assocOf("1"), batchBody: batchOf(dealRes(`"true"`))}
	switch stage {
	case 0:
		p.contactGet = status
	case 1:
		p.assoc = status
	case 2:
		p.batch = status
	case 3:
		p.dealPost = status
	}
	return p
}

func TestHubSpotOpenDemoDeal_FailureAtEachStageStops(t *testing.T) {
	for stage, name := range dealStageLabels {
		for _, status := range []int{500, 429, 403, 400, hangStatus} {
			t.Run(name+"/"+strconv.Itoa(status), func(t *testing.T) {
				v, err := runDeal(t, failingPlan(stage, status))
				var de *DeliveryError
				if !errors.As(err, &de) {
					t.Fatalf("err = %v, want a *DeliveryError", err)
				}
				want := status
				if status == hangStatus {
					want = 0
				}
				if de.Status != want || de.Permanent() != (want == 400 || want == 403) {
					t.Errorf("Status = %d, Permanent() = %v, want %d", de.Status, de.Permanent(), want)
				}
				if got := len(v.calls()); got != stage+1 {
					t.Errorf("requests = %v, want %d (none after the failure)", v.labels(), stage+1)
				}
			})
		}
	}
}

func TestHubSpotOpenDemoDeal_BatchRead207JudgesTheReturnedDeals(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("1", "2", "3"), batch: 207,
		batchBody: `{"status":"COMPLETE","results":[` + dealRes(`"true"`) + `,` + dealRes(`"true"`) + `],"errors":[{"status":"error","category":"OBJECT_NOT_FOUND"}]}`})
	if err != nil || len(dealPosts(v)) != 1 {
		t.Errorf("closed+error+closed: err = %v, requests = %v, want one deal POST", err, v.labels())
	}
	v, err = runDeal(t, dealPlan{assocBody: assocOf("1", "2", "3"), batch: 207,
		batchBody: `{"status":"COMPLETE","results":[` + dealRes(`"false"`) + `,` + dealRes(`"true"`) + `],"errors":[{"status":"error"}]}`})
	if err != nil || len(dealPosts(v)) != 0 {
		t.Errorf("error+open+closed: err = %v, requests = %v, want no deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_ShortBatchResultIsJudgedAsReturned(t *testing.T) {
	for _, body := range []string{batchOf(dealRes(`"true"`)), batchOf()} {
		v, err := runDeal(t, dealPlan{assocBody: assocOf("1", "2", "3"), batchBody: body})
		if err != nil || len(dealPosts(v)) != 1 {
			t.Errorf("%s: err = %v, requests = %v, want one deal POST", body, err, v.labels())
		}
	}
}

func TestHubSpotOpenDemoDeal_BatchReadOtherStatusIsAnError(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("1"), batch: 502})
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 502 || len(dealPosts(v)) != 0 {
		t.Errorf("err = %v, requests = %v, want *DeliveryError{502} and no deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_UndecodableBodyIsTransient(t *testing.T) {
	v, err := runDeal(t, dealPlan{contactBody: "not json"})
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 0 || de.Permanent() {
		t.Fatalf("err = %v, want *DeliveryError{0}", err)
	}
	if n := len(v.calls()); n != 1 {
		t.Errorf("requests = %v, want only the contact GET", v.labels())
	}
}

func TestHubSpotOpenDemoDeal_RedirectIsNotFollowed(t *testing.T) {
	elsewhere := newVendor(t, func(seenReq) (int, string) { return 200, `{"id":"1"}` })
	redirect := newRedirectVendor(t, elsewhere.srv.URL)
	err := NewHubSpot(redirect.URL, Keys{HubSpotToken: "hs-tok"}, NewHTTPClient(nil)).OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "n")
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 301 {
		t.Errorf("err = %v, want *DeliveryError{301}", err)
	}
	if n := len(elsewhere.calls()); n != 0 {
		t.Errorf("redirect target received %d requests, want 0", n)
	}
}

func newRedirectVendor(t *testing.T, to string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHubSpotOpenDemoDeal_ErrorsNeverCarryTheEmail(t *testing.T) {
	const email = "ada.lovelace+test@corp.example"
	needles := []string{email, url.QueryEscape(email), "ada.lovelace", "corp.example", "Analytical Engines", "hs-tok"}
	echo := `{"message":"contact ` + email + ` Analytical Engines already exists"}`
	for stage, name := range dealStageLabels {
		for _, status := range []int{422, 500, hangStatus} {
			t.Run(name+"/"+strconv.Itoa(status), func(t *testing.T) {
				p := failingPlan(stage, status)
				switch stage {
				case 0:
					p.contactBody = echo
				case 1:
					p.assocBody = echo
				case 2:
					p.batchBody = echo
				}
				v := newVendor(t, p.respond)
				err := hubspotAt(v, shortClient()).OpenDemoDeal(t.Context(), Contact{Email: email, Company: "Analytical Engines"}, "Analytical Engines — demo request")
				if err == nil {
					t.Fatal("err = nil, want a failure")
				}
				for _, text := range errChain(err) {
					for _, n := range needles {
						if strings.Contains(strings.ToLower(text), strings.ToLower(n)) {
							t.Errorf("error text %q carries %q", text, n)
						}
					}
				}
			})
		}
	}
}

func TestHubSpotOpenDemoDeal_ContactCreateFailureStops(t *testing.T) {
	for _, status := range []int{500, 429, 400, 403, hangStatus} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			v, err := runDeal(t, dealPlan{contactGet: 404, contactPost: status})
			var de *DeliveryError
			want := status
			if status == hangStatus {
				want = 0
			}
			if !errors.As(err, &de) || de.Status != want {
				t.Fatalf("err = %v, want *DeliveryError{%d}", err, want)
			}
			if got := len(v.calls()); got != 2 {
				t.Errorf("requests = %v, want GET and POST contact only", v.labels())
			}
		})
	}
}

func TestHubSpotOpenDemoDeal_ContactCreate409ThenReadFailureIsA409(t *testing.T) {
	var gets int
	v := newVendor(t, func(s seenReq) (int, string) {
		if s.Method == http.MethodGet && strings.HasPrefix(s.path(), hsCreatePath+"/") {
			gets++
			if gets == 1 {
				return 404, `{}`
			}
			return 500, `{}`
		}
		return dealPlan{contactPost: 409}.respond(s)
	})
	err := hubspotAt(v, nil).OpenDemoDeal(t.Context(), fullContact("ada@corp.example"), "n")
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 409 || len(dealPosts(v)) != 0 {
		t.Errorf("err = %v, requests = %v, want *DeliveryError{409} and no deal POST", err, v.labels())
	}
}

func TestHubSpotOpenDemoDeal_BatchReadAsksForEveryAssociatedDeal(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("9007199254740993", "12", "13"), batchBody: batchOf(dealRes(`"true"`))})
	if err != nil {
		t.Fatal(err)
	}
	var batch seenReq
	for _, c := range v.calls() {
		if c.label() == batchLabel {
			batch = c
		}
	}
	var body struct {
		Properties []string            `json:"properties"`
		Inputs     []map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(batch.Body, &body); err != nil {
		t.Fatalf("batch body %q: %v", batch.Body, err)
	}
	wantIn := []map[string]string{{"id": "9007199254740993"}, {"id": "12"}, {"id": "13"}}
	if !reflect.DeepEqual(body.Inputs, wantIn) || !reflect.DeepEqual(body.Properties, []string{"hs_is_closed"}) {
		t.Errorf("batch body = %s, want inputs %v and properties [hs_is_closed]", batch.Body, wantIn)
	}
}

func TestHubSpotOpenDemoDeal_UndecodableBatchBodyIsTransient(t *testing.T) {
	v, err := runDeal(t, dealPlan{assocBody: assocOf("1"), batchBody: "not json"})
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 0 || len(dealPosts(v)) != 0 {
		t.Errorf("err = %v, requests = %v, want *DeliveryError{0} and no deal POST", err, v.labels())
	}
}
