package platform

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
		"http://tenancy.internal:8080/v1/x",
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

func TestIDTokenCache_RefreshesForDifferentAudiencesRunInParallel(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	const n = 10
	errs := make(chan error, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tr.cache.token(t.Context(), tr.base, fmt.Sprintf("https://svc%d.run.app", i))
			errs <- err
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("token: %v", err)
		}
	}
	if elapsed >= 300*time.Millisecond {
		t.Errorf("%d audiences took %v, want < 300ms (refreshes must not serialise)", n, elapsed)
	}
}

func TestIDTokenCache_ConcurrentSameAudienceFetchesOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var fetches atomic.Int32
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	const n = 10
	toks := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := tr.cache.token(t.Context(), tr.base, "https://one.run.app")
			if err != nil {
				t.Errorf("token: %v", err)
			}
			toks <- tok
		}()
	}
	wg.Wait()
	close(toks)
	got := 0
	for tok := range toks {
		if tok == "" {
			t.Error("empty token")
		}
		got++
	}
	if got != n {
		t.Errorf("got %d tokens, want %d", got, n)
	}
	if f := fetches.Load(); f != 1 {
		t.Errorf("metadata fetches = %d, want 1", f)
	}
}

func TestIDTokenCache_StalledAudienceDoesNotBlockAnother(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	entered := make(chan struct{})
	release := make(chan struct{})
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "slow.run.app") {
			close(entered)
			<-release
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	var once sync.Once
	releaseSlow := func() { once.Do(func() { close(release) }) }
	defer releaseSlow()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	slowErr := make(chan error, 1)
	go func() {
		_, err := tr.cache.token(t.Context(), tr.base, "https://slow.run.app")
		slowErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("slow audience never reached the metadata server")
	}

	type result struct {
		tok string
		err error
	}
	fast := make(chan result, 1)
	go func() {
		tok, err := tr.cache.token(t.Context(), tr.base, "https://fast.run.app")
		fast <- result{tok, err}
	}()
	select {
	case r := <-fast:
		if r.err != nil || r.tok == "" {
			t.Errorf("fast audience: token %q, err %v; want a token", r.tok, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Error("fast audience still waiting 2s behind a stalled audience")
	}

	releaseSlow()
	if err := <-slowErr; err == nil {
		t.Error("slow audience returned no error after a 500")
	}
}

func TestIDTokenCache_CancelledCallerDoesNotFailTheWaiter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	entered := make(chan struct{})
	var fetches atomic.Int32
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fetches.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	ctx, cancel := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := tr.cache.token(ctx, tr.base, "https://a.run.app")
		firstErr <- err
	}()
	<-entered
	type result struct {
		tok string
		err error
	}
	second := make(chan result, 1)
	go func() {
		tok, err := tr.cache.token(t.Context(), tr.base, "https://a.run.app")
		second <- result{tok, err}
	}()
	cancel()
	if err := <-firstErr; err == nil {
		t.Error("cancelled caller returned no error")
	}
	select {
	case r := <-second:
		if r.err != nil || r.tok == "" {
			t.Errorf("waiter: token %q, err %v; want a token", r.tok, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never returned")
	}
}

func TestIDTokenCache_ErrorIsNotCachedAndOtherAudienceKeepsItsToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var badFetches atomic.Int32
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "bad.run.app") && badFetches.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, fakeJWT(now.Add(time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now })

	if _, err := tr.cache.token(t.Context(), tr.base, "https://bad.run.app"); err == nil {
		t.Fatal("first bad fetch: want error")
	}
	if tok, err := tr.cache.token(t.Context(), tr.base, "https://good.run.app"); err != nil || tok == "" {
		t.Fatalf("good audience: token %q, err %v", tok, err)
	}
	if tok, err := tr.cache.token(t.Context(), tr.base, "https://bad.run.app"); err != nil || tok == "" {
		t.Errorf("bad audience retry: token %q, err %v; want a refetch, not a cached error", tok, err)
	}
	if got := badFetches.Load(); got != 2 {
		t.Errorf("bad audience fetches = %d, want 2", got)
	}
}

func TestIDTokenCache_ConcurrentExpiringAudiencesEachRefetchOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var clock atomic.Int64
	var mu sync.Mutex
	fetched := map[string]int{}
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched[r.URL.Query().Get("audience")]++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, fakeJWT(now.Add(time.Duration(clock.Load())+time.Hour)))
	}))
	defer md.Close()
	tr := newIDTokenTransport(http.DefaultTransport, md.URL, func() time.Time { return now.Add(time.Duration(clock.Load())) })

	const n = 6
	aud := func(i int) string { return fmt.Sprintf("https://svc%d.run.app", i) }
	for i := range n {
		if _, err := tr.cache.token(t.Context(), tr.base, aud(i)); err != nil {
			t.Fatal(err)
		}
	}
	clock.Store(int64(56 * time.Minute))
	mu.Lock()
	clear(fetched)
	mu.Unlock()

	var wg sync.WaitGroup
	for range 3 {
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if tok, err := tr.cache.token(t.Context(), tr.base, aud(i)); err != nil || tok == "" {
					t.Errorf("%s: token %q err %v", aud(i), tok, err)
				}
			}()
		}
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(fetched) != n {
		t.Fatalf("refreshed %d audiences, want %d", len(fetched), n)
	}
	for a, c := range fetched {
		if c != 1 {
			t.Errorf("%s fetched %d times, want 1", a, c)
		}
	}
}
