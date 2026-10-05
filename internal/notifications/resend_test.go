package notifications

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func resendAt(v *fakeVendor, hc *http.Client) *Resend {
	return NewResend(v.srv.URL, Keys{ResendAPIKey: "rs-key", ResendSegmentID: "seg-1", ResendTopicID: "top-1"}, hc)
}

func regContact(email string) Contact {
	return Contact{
		Email: email, FirstName: "Ada", LastName: "Lovelace",
		Tags: []string{"registered"}, ResendApplies: true,
	}
}

func demoContact(email string) Contact {
	return Contact{
		Email: email, FirstName: "Ada", LastName: "Lovelace",
		Tags: []string{"demo request"}, MarketingEligible: true, ResendApplies: true,
	}
}

// resendStatus answers each Resend call with its own status.
type resendStatus struct{ get, create, segment, topics int }

func (r resendStatus) respond(s seenReq) (int, string) {
	p := s.path()
	switch {
	case s.Method == http.MethodGet:
		return r.get, `{"object":"contact"}`
	case s.Method == http.MethodPost && strings.Contains(p, "/segments/"):
		return r.segment, `{"id":"seg-1"}`
	case s.Method == http.MethodPost:
		return r.create, `{"object":"contact","id":"c1"}`
	case s.Method == http.MethodPatch && strings.HasSuffix(p, "/topics"):
		return r.topics, `{"object":"contact_topics"}`
	}
	return 500, `{"message":"unexpected request"}`
}

var (
	resendAbsent  = resendStatus{get: 404, create: 201, segment: 200, topics: 200}
	resendPresent = resendStatus{get: 200, create: 201, segment: 200, topics: 200}
)

const adaEmail = "ada@corp.example"

func syncOK(t *testing.T, v *fakeVendor, c Contact, optIn bool) {
	t.Helper()
	if err := resendAt(v, nil).Sync(t.Context(), c, optIn); err != nil {
		t.Fatalf("Sync() err = %v, want nil", err)
	}
}

func TestResendSync_CreatesOnlyWhenAbsent(t *testing.T) {
	t.Run("GET 404 creates once", func(t *testing.T) {
		v := newVendor(t, resendAbsent.respond)
		syncOK(t, v, regContact(adaEmail), false)
		want := []string{"GET /contacts/" + adaEmail, "POST /contacts", "POST /contacts/" + adaEmail + "/segments/seg-1"}
		if got := v.labels(); !reflect.DeepEqual(got, want) {
			t.Fatalf("requests = %v, want %v", got, want)
		}
		calls := v.calls()
		for _, c := range calls {
			if c.Auth != "Bearer rs-key" {
				t.Errorf("%s Authorization = %q, want %q", c.label(), c.Auth, "Bearer rs-key")
			}
		}
		if !strings.HasPrefix(calls[1].CType, "application/json") {
			t.Errorf("create Content-Type = %q, want application/json", calls[1].CType)
		}
		var body map[string]any
		if err := json.Unmarshal(calls[1].Body, &body); err != nil {
			t.Fatalf("create body %q is not JSON: %v", calls[1].Body, err)
		}
		wantBody := map[string]any{"email": adaEmail, "first_name": "Ada", "last_name": "Lovelace"}
		if !reflect.DeepEqual(body, wantBody) {
			t.Errorf("create body = %v, want %v", body, wantBody)
		}
	})

	t.Run("GET 200 never creates", func(t *testing.T) {
		v := newVendor(t, resendPresent.respond)
		syncOK(t, v, regContact(adaEmail), false)
		want := []string{"GET /contacts/" + adaEmail, "POST /contacts/" + adaEmail + "/segments/seg-1"}
		if got := v.labels(); !reflect.DeepEqual(got, want) {
			t.Errorf("requests = %v, want %v (no create)", got, want)
		}
	})

	t.Run("no name sends the email only", func(t *testing.T) {
		v := newVendor(t, resendAbsent.respond)
		c := regContact(adaEmail)
		c.FirstName, c.LastName = "", ""
		syncOK(t, v, c, false)
		calls := v.calls()
		if len(calls) < 2 {
			t.Fatalf("requests = %v, want a create", v.labels())
		}
		var body map[string]any
		if err := json.Unmarshal(calls[1].Body, &body); err != nil {
			t.Fatalf("create body %q is not JSON: %v", calls[1].Body, err)
		}
		if want := map[string]any{"email": adaEmail}; !reflect.DeepEqual(body, want) {
			t.Errorf("create body = %v, want %v", body, want)
		}
	})

	t.Run("a blank name is not sent", func(t *testing.T) {
		v := newVendor(t, resendAbsent.respond)
		c := regContact(adaEmail)
		c.LastName = ""
		syncOK(t, v, c, false)
		calls := v.calls()
		if len(calls) < 2 {
			t.Fatalf("requests = %v, want a create", v.labels())
		}
		var body map[string]any
		if err := json.Unmarshal(calls[1].Body, &body); err != nil {
			t.Fatalf("create body %q is not JSON: %v", calls[1].Body, err)
		}
		if _, ok := body["last_name"]; ok {
			t.Errorf("create body sends a blank last_name: %v", body)
		}
		if body["first_name"] != "Ada" {
			t.Errorf("create first_name = %v, want Ada", body["first_name"])
		}
	})
}

func TestResendSync_RegistrantJoinsSegment(t *testing.T) {
	t.Run("registrant, no opt-in", func(t *testing.T) {
		v := newVendor(t, resendPresent.respond)
		syncOK(t, v, regContact(adaEmail), false)
		want := []string{"GET /contacts/" + adaEmail, "POST /contacts/" + adaEmail + "/segments/seg-1"}
		if got := v.labels(); !reflect.DeepEqual(got, want) {
			t.Errorf("requests = %v, want %v (segment POST, no topics call)", got, want)
		}
	})

	// Only registrants hold the "Registered" segment; the unticked-people rule hangs on it.
	t.Run("a demo booker does not join", func(t *testing.T) {
		v := newVendor(t, resendAbsent.respond)
		syncOK(t, v, demoContact(adaEmail), true)
		want := []string{"GET /contacts/" + adaEmail, "POST /contacts", "PATCH /contacts/" + adaEmail + "/topics"}
		if got := v.labels(); !reflect.DeepEqual(got, want) {
			t.Errorf("requests = %v, want %v (opt-in, no segment)", got, want)
		}
	})
}

func TestResendSync_OptInOnlyWhenAsked(t *testing.T) {
	t.Run("asked", func(t *testing.T) {
		v := newVendor(t, resendPresent.respond)
		syncOK(t, v, regContact(adaEmail), true)
		want := []string{
			"GET /contacts/" + adaEmail,
			"POST /contacts/" + adaEmail + "/segments/seg-1",
			"PATCH /contacts/" + adaEmail + "/topics",
		}
		if got := v.labels(); !reflect.DeepEqual(got, want) {
			t.Fatalf("requests = %v, want %v", got, want)
		}
		topics := v.calls()[2]
		if !strings.HasPrefix(topics.CType, "application/json") || topics.Auth != "Bearer rs-key" {
			t.Errorf("topics Content-Type = %q, Authorization = %q", topics.CType, topics.Auth)
		}
		var body []map[string]string
		if err := json.Unmarshal(topics.Body, &body); err != nil {
			t.Fatalf("topics body %q is not a JSON array of objects: %v", topics.Body, err)
		}
		want2 := []map[string]string{{"id": "top-1", "subscription": "opt_in"}}
		if !reflect.DeepEqual(body, want2) {
			t.Errorf("topics body = %v, want %v", body, want2)
		}
	})

	t.Run("not asked", func(t *testing.T) {
		for name, st := range map[string]resendStatus{"absent": resendAbsent, "present": resendPresent} {
			t.Run(name, func(t *testing.T) {
				v := newVendor(t, st.respond)
				syncOK(t, v, regContact(adaEmail), false)
				calls := v.calls()
				if len(calls) < 2 {
					t.Fatalf("requests = %v, want at least the GET and the segment POST", v.labels())
				}
				for _, c := range calls {
					if c.Method == http.MethodPatch || strings.Contains(c.path(), "topics") ||
						strings.Contains(string(c.Body), "opt_in") {
						t.Errorf("%s carries an opt-in: %s", c.label(), c.Body)
					}
				}
			})
		}
	})
}

func TestResendSync_NeverUnsubscribesOrOptsOut(t *testing.T) {
	var sawOptIn bool
	for name, st := range map[string]resendStatus{"absent": resendAbsent, "present": resendPresent} {
		for cname, c := range map[string]Contact{"registrant": regContact(adaEmail), "demo booker": demoContact(adaEmail)} {
			for _, optIn := range []bool{false, true} {
				t.Run(name+"/"+cname+"/"+map[bool]string{false: "no opt-in", true: "opt-in"}[optIn], func(t *testing.T) {
					v := newVendor(t, st.respond)
					syncOK(t, v, c, optIn)
					calls := v.calls()
					if len(calls) == 0 {
						t.Fatal("no request reached the vendor")
					}
					for _, call := range calls {
						text := strings.ToLower(call.URI + " " + string(call.Body))
						for _, banned := range []string{"unsubscribed", "opt_out"} {
							if strings.Contains(text, banned) {
								t.Errorf("%s carries %q: %s", call.label(), banned, call.Body)
							}
						}
						sawOptIn = sawOptIn || strings.Contains(string(call.Body), "opt_in")
					}
				})
			}
		}
	}
	if !sawOptIn {
		t.Error("no run sent an opt_in, so the scan above proved nothing")
	}
}

func TestResendSync_ErrorsOnFailure(t *testing.T) {
	cases := []struct {
		name      string
		st        resendStatus
		wantCalls int
	}{
		{"GET 500 stops", resendStatus{get: 500}, 1},
		{"GET 401 never creates", resendStatus{get: 401, create: 201}, 1},
		{"create 500 stops", resendStatus{get: 404, create: 500, segment: 200, topics: 200}, 2},
		{"segment 500 stops", resendStatus{get: 200, segment: 500, topics: 200}, 2},
		{"topics 500", resendStatus{get: 200, segment: 200, topics: 500}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newVendor(t, c.st.respond)
			err := resendAt(v, nil).Sync(t.Context(), regContact(adaEmail), true)
			var de *DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want a *DeliveryError", err)
			}
			if got := v.labels(); len(got) != c.wantCalls {
				t.Errorf("requests = %v, want %d (stop at the failure)", got, c.wantCalls)
			}
		})
	}

	t.Run("any 2xx delivers at every step", func(t *testing.T) {
		for _, ok := range []int{200, 201, 202, 204, 299} {
			for _, base := range []resendStatus{resendAbsent, resendPresent} {
				st := resendStatus{get: base.get, create: ok, segment: ok, topics: ok}
				if st.get == 200 {
					st.get = ok
				}
				v := newVendor(t, st.respond)
				if err := resendAt(v, nil).Sync(t.Context(), regContact(adaEmail), true); err != nil {
					t.Errorf("statuses %+v: err = %v, want nil", st, err)
				}
				if got := len(v.labels()); got < 3 {
					t.Errorf("statuses %+v: %d requests, want every step", st, got)
				}
			}
		}
	})

	t.Run("a 3xx at any step is an error", func(t *testing.T) {
		for _, bad := range []resendStatus{
			{get: 304}, {get: 404, create: 300}, {get: 200, segment: 304}, {get: 200, segment: 200, topics: 300},
		} {
			v := newVendor(t, bad.respond)
			var de *DeliveryError
			err := resendAt(v, nil).Sync(t.Context(), regContact(adaEmail), true)
			if !errors.As(err, &de) || de.Permanent() {
				t.Errorf("statuses %+v: err = %v, want a transient *DeliveryError", bad, err)
			}
		}
	})

	hangs := []struct {
		name      string
		st        resendStatus
		wantCalls int
	}{
		{"hung GET", resendStatus{get: hangStatus}, 1},
		{"hung segment", resendStatus{get: 200, segment: hangStatus, topics: 200}, 2},
	}
	for _, c := range hangs {
		t.Run(c.name+" times out", func(t *testing.T) {
			v := newVendor(t, c.st.respond)
			err := resendAt(v, shortClient()).Sync(t.Context(), regContact(adaEmail), true)
			var de *DeliveryError
			if !errors.As(err, &de) || de.Status != 0 {
				t.Fatalf("err = %v, want a *DeliveryError with status 0", err)
			}
			if got := v.labels(); len(got) != c.wantCalls {
				t.Errorf("requests = %v, want %d", got, c.wantCalls)
			}
		})
	}
}

// pathParts splits the raw request path (never the decoded one) into escaped segments.
func pathParts(s seenReq) []string { return strings.Split(strings.TrimPrefix(s.path(), "/"), "/") }

func unescapedSeg(t *testing.T, seg string) string {
	t.Helper()
	d, err := url.PathUnescape(seg)
	if err != nil {
		t.Fatalf("segment %q is not valid escaping: %v", seg, err)
	}
	return d
}

// Each address is a legal unquoted local part; the unescaped ones split, truncate
// or rewrite the path (see [path-escape-oracle]).
var pathBreakers = []string{
	"ada+test@corp.example",
	"a/b@corp.example",
	"q?x=1@corp.example",
	"h#frag@corp.example",
	"p%41@corp.example",
	"a+b/c?d#e%f@corp.example",
}

func TestClients_EscapeTheEmailInPaths(t *testing.T) {
	for _, email := range pathBreakers {
		t.Run("resend/"+email, func(t *testing.T) {
			v := newVendor(t, resendAbsent.respond)
			syncOK(t, v, regContact(email), true)
			calls := v.calls()
			if len(calls) != 4 {
				t.Fatalf("requests = %v, want GET, create, segment, topics", v.labels())
			}
			// Each case: the raw path split on "/" has exactly one address segment.
			wantShapes := [][]string{
				{"contacts", "*"},
				{"contacts"},
				{"contacts", "*", "segments", "seg-1"},
				{"contacts", "*", "topics"},
			}
			for i, call := range calls {
				if call.Query != "" {
					t.Errorf("%s: query = %q, want none (the address leaked into a query)", call.label(), call.Query)
				}
				parts := pathParts(call)
				if len(parts) != len(wantShapes[i]) {
					t.Errorf("%s: path %q has %d segments, want %d", call.Method, call.path(), len(parts), len(wantShapes[i]))
					continue
				}
				for j, want := range wantShapes[i] {
					if want == "*" {
						if got := unescapedSeg(t, parts[j]); got != email {
							t.Errorf("%s: address segment decodes to %q, want %q", call.Method, got, email)
						}
					} else if parts[j] != want {
						t.Errorf("%s: segment %d = %q, want %q", call.Method, j, parts[j], want)
					}
				}
			}
			var created struct {
				Email string `json:"email"`
			}
			if err := json.Unmarshal(calls[1].Body, &created); err != nil || created.Email != email {
				t.Errorf("create body email = %q (%v), want %q", created.Email, err, email)
			}
		})

		t.Run("hubspot/"+email, func(t *testing.T) {
			v := newVendor(t, hubspotRoutes(404, 201))
			if err := hubspotAt(v, nil).Upsert(t.Context(), fullContact(email)); err != nil {
				t.Fatalf("Upsert() err = %v, want nil", err)
			}
			calls := v.calls()
			if len(calls) != 2 {
				t.Fatalf("requests = %v, want a PATCH then one POST", v.labels())
			}
			patch := calls[0]
			parts := pathParts(patch)
			if len(parts) != 5 || strings.Join(parts[:4], "/") != "crm/v3/objects/contacts" {
				t.Fatalf("PATCH path %q, want /crm/v3/objects/contacts/<one segment>", patch.path())
			}
			if got := unescapedSeg(t, parts[4]); got != email {
				t.Errorf("PATCH address segment decodes to %q, want %q", got, email)
			}
			if patch.Query != "idProperty=email" {
				t.Errorf("PATCH query = %q, want exactly %q", patch.Query, "idProperty=email")
			}
			if got := jsonProps(t, calls[1])["email"]; got != email {
				t.Errorf("POST body email = %q, want %q", got, email)
			}
		})
	}
}
