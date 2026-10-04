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
	permanent := []int{400, 401, 403, 404, 409, 422}
	transient := []int{408, 429, 500, 502, 503}
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

// chain flattens err and everything it wraps, as Sentry's exception chain does.
func errChain(err error) []string {
	var out []string
	for ; err != nil; err = errors.Unwrap(err) {
		out = append(out, err.Error())
	}
	return out
}

func TestClients_ErrorsNeverCarryTheEmail(t *testing.T) {
	const email = "ada.lovelace+test@corp.example"
	needles := []string{email, url.QueryEscape(email), "ada.lovelace", "corp.example"}
	// The vendor echoes the address back, as a 409 or 422 message may.
	echo := `{"message":"contact ` + email + ` already exists"}`

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
