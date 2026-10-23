package platform

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeJWT(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "h." + payload + ".s"
}

type recordRT struct {
	reqs []*http.Request
}

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.reqs = append(r.reqs, req)
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

func TestIDTokenTransport_SignsOnlyRunAppHosts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var audiences []string
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "no flavor", http.StatusForbidden)
			return
		}
		audiences = append(audiences, r.URL.Query().Get("audience"))
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()

	rec := &recordRT{}
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), md.URL) {
			return http.DefaultTransport.RoundTrip(req)
		}
		return rec.RoundTrip(req)
	})
	tr := newIDTokenTransport(base, md.URL, func() time.Time { return now })

	for _, u := range []string{
		"https://tenancy-abc-uc.a.run.app/v1/x",
		"https://tenancy-abc-uc.a.run.app/v1/y",
		"http://tenancy.railway.internal:8080/v1/x",
		"https://api.openrouter.ai/v1",
	} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer user-jwt")
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip(%s): %v", u, err)
		}
		if req.Header.Get("X-Serverless-Authorization") != "" {
			t.Errorf("RoundTrip(%s) changed the caller's request", u)
		}
	}

	want := []string{"Bearer " + fakeJWT(now.Add(time.Hour)), "Bearer " + fakeJWT(now.Add(time.Hour)), "", ""}
	for i, r := range rec.reqs {
		if got := r.Header.Get("X-Serverless-Authorization"); got != want[i] {
			t.Errorf("%s: X-Serverless-Authorization = %q, want %q", r.URL, got, want[i])
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user-jwt" {
			t.Errorf("%s: Authorization = %q, want the caller's", r.URL, got)
		}
	}
	if len(audiences) != 1 || audiences[0] != "https://tenancy-abc-uc.a.run.app" {
		t.Errorf("metadata audiences = %v, want one fetch for https://tenancy-abc-uc.a.run.app", audiences)
	}
}

func TestIDTokenTransport_TagURLUsesTheServiceAudience(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var audiences []string
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audiences = append(audiences, r.URL.Query().Get("audience"))
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), md.URL) {
			return http.DefaultTransport.RoundTrip(req)
		}
		return (&recordRT{}).RoundTrip(req)
	}), md.URL, func() time.Time { return now })

	req, _ := http.NewRequest(http.MethodGet, "https://c-abc1234---tenancy-123.africa-south1.run.app/healthz", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(audiences) != 1 || audiences[0] != "https://tenancy-123.africa-south1.run.app" {
		t.Errorf("metadata audiences = %v, want the service URL", audiences)
	}
}

func TestIDTokenTransport_RefetchesNearExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fetches := 0
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	for _, at := range []time.Duration{0, 54 * time.Minute, 56 * time.Minute} {
		now = time.Unix(1_800_000_000, 0).Add(at)
		if _, err := tr.cache.token(t.Context(), tr.base, "https://x.run.app"); err != nil {
			t.Fatalf("token at +%v: %v", at, err)
		}
	}
	if fetches != 2 {
		t.Errorf("metadata fetches = %d, want 2 (cached at +54m, refetched inside the last 5 minutes)", fetches)
	}
}

func TestIDTokenTransport_FailsWithoutAToken(t *testing.T) {
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer md.Close()
	rec := &recordRT{}
	tr := newIDTokenTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), md.URL) {
			return http.DefaultTransport.RoundTrip(req)
		}
		return rec.RoundTrip(req)
	}), md.URL, time.Now)

	req, _ := http.NewRequest(http.MethodGet, "https://validation-abc-uc.a.run.app/healthz", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip without a token returned no error")
	}
	if len(rec.reqs) != 0 {
		t.Errorf("sent %d requests without a token, want 0", len(rec.reqs))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTraceTransport_CarriesTheIDToken(t *testing.T) {
	saved := idTokens
	idTokens = &idTokenCache{metadata: "http://metadata.invalid/identity", now: time.Now, tokens: map[string]idToken{}}
	t.Cleanup(func() { idTokens = saved })

	asked := false
	rt := TraceTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		asked = asked || r.URL.Host == "metadata.invalid"
		return nil, fmt.Errorf("no network in this test")
	}))
	req, _ := http.NewRequest(http.MethodGet, "https://validation-1.africa-south1.run.app/healthz", nil)
	_, _ = rt.RoundTrip(req)
	if !asked {
		t.Error("TraceTransport sent a run.app request without asking the metadata server for an ID token")
	}
}
