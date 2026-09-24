package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func warnCount(r *budgetRig) int { return strings.Count(r.logs.String(), "level=WARN") }

// goroutinesIn counts live goroutines whose stack contains fn.
func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, fn) {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, label string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(time.Millisecond)
	}
}

// blockingJWKS serves good once released; entered closes when the first request arrives.
type blockingJWKS struct {
	good    http.Handler
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	rel     sync.Once
}

func newBlockingJWKS(t *testing.T, good http.Handler) *blockingJWKS {
	b := &blockingJWKS{good: good, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(b.unblock)
	return b
}

func (b *blockingJWKS) unblock() { b.rel.Do(func() { close(b.release) }) }

func (b *blockingJWKS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		b.good.ServeHTTP(w, r)
	case <-r.Context().Done():
	}
}

const jwksKeysFrame = "auth.(*Verifier).jwksKeys"

func TestJWKS_StaleWindowBoundaryIsExclusive(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())

	r.clock.at(7*time.Hour - time.Nanosecond)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "fetchedAt+TTL+6h-1ns")
	r.wantFetches(t, 2, "fetchedAt+TTL+6h-1ns (refetch failed)")

	r.clock.at(7 * time.Hour)
	refused(t, r.v, r.token(t), "exactly fetchedAt+TTL+6h")
	r.wantFetches(t, 2, "exactly fetchedAt+TTL+6h (inside failure window)")
}

func TestJWKS_StaleServeAfterFailureBacksOffAndWarnsOncePerWindow(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())

	for i, at := range []time.Duration{2 * time.Hour, 2*time.Hour + time.Second, 2*time.Hour + 29*time.Second} {
		r.clock.at(at)
		assertVerifies(t, r.v, r.token(t), "tenant-x", "stale serve")
		if got := warnCount(r); got != 1 {
			t.Fatalf("after stale serve #%d: WARN lines = %d, want 1", i+1, got)
		}
	}
	r.wantFetches(t, 2, "three stale serves inside one failure window")

	r.clock.at(2*time.Hour + 30*time.Second)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "stale serve after the window")
	r.wantFetches(t, 3, "failure window elapsed")
	if got := warnCount(r); got != 2 {
		t.Fatalf("after the window: WARN lines = %d, want 2", got)
	}
}

func TestJWKS_RecoveryAfterStaleServeStopsWarning(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())
	r.clock.at(2 * time.Hour)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "stale serve")
	if warnCount(r) != 1 {
		t.Fatalf("stale serve: WARN lines = %d, want 1", warnCount(r))
	}

	key2 := mustIssuer(t)
	key2.issuer = r.iss.issuer
	r.jwks.set(key2.JWKSHandler())
	r.clock.at(2*time.Hour + 30*time.Second)
	assertVerifies(t, r.v, mustMint(t, key2, MintOptions{Subject: testSubject, TenantID: "t2"}), "t2", "recovered JWKS")
	r.wantFetches(t, 3, "recovery fetch")

	// The recovered set is fresh: the old key is gone and no fetch or WARN follows.
	r.clock.at(2*time.Hour + 31*time.Second)
	assertVerifies(t, r.v, mustMint(t, key2, MintOptions{Subject: testSubject, TenantID: "t2"}), "t2", "fresh after recovery")
	r.wantFetches(t, 3, "fresh after recovery")
	if warnCount(r) != 1 {
		t.Fatalf("after recovery: WARN lines = %d, want 1; log:\n%s", warnCount(r), r.logs.String())
	}
}

func TestJWKS_FailedForcedFetchAnchorsTheWindow(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())

	t1 := 5 * time.Second
	r.clock.at(t1)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1, JWKS failing")
	r.wantFetches(t, 2, "failed forced fetch at t1")

	// A failed forced fetch leaves the fresh cache usable.
	r.clock.at(t1 + 10*time.Second)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "cached key after a failed forced fetch")
	if warnCount(r) != 0 {
		t.Fatalf("fresh cache served with a WARN; log:\n%s", r.logs.String())
	}

	r.clock.at(t1 + 30*time.Second - time.Millisecond)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1+29.999s")
	r.wantFetches(t, 2, "t1+29.999s (inside window)")

	r.clock.at(t1 + 30*time.Second)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1+30s")
	r.wantFetches(t, 3, "exactly t1+30s (boundary allows)")
}

func TestJWKS_BadSignatureOnKnownKidIsThrottled(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	forger := mustIssuer(t)
	forger.issuer, forger.kid = r.iss.issuer, r.iss.kid
	r.clock.at(time.Second)
	for i := 0; i < 20; i++ {
		refused(t, r.v, mustMint(t, forger, MintOptions{Subject: testSubject}), "known kid, wrong key")
	}
	r.wantFetches(t, 2, "20 bad signatures on a known kid")
}

func TestJWKS_BudgetIsPerIssuer(t *testing.T) {
	a := newCountedIssuer(t, "urn:ascomply:auth:a")
	b := newCountedIssuer(t, "urn:ascomply:auth:b")
	v := multiVerifier(t, a, b)
	clock := &fakeClock{t: budgetT0}
	v.now = clock.now

	assertVerifies(t, v, mustMint(t, a.iss, MintOptions{Subject: testSubject}), "", "prime A")
	assertVerifies(t, v, mustMint(t, b.iss, MintOptions{Subject: testSubject}), "", "prime B")

	clock.at(time.Second)
	for i := 0; i < 10; i++ {
		refused(t, v, signWithKid(t, a.iss, uuid.NewString()), "unknown kid on A")
	}
	if n := a.hits.count(); n != 2 {
		t.Fatalf("A: JWKS fetches = %d, want 2 (prime, one forced)", n)
	}

	// A's forced window is open; B's first forced fetch must still go out.
	clock.at(2 * time.Second)
	refused(t, v, signWithKid(t, b.iss, uuid.NewString()), "unknown kid on B")
	if n := b.hits.count(); n != 2 {
		t.Fatalf("B: JWKS fetches = %d, want 2 (prime, forced)", n)
	}

	// A's failed refetch must not throttle B's TTL refetch.
	a.hits.set(failing())
	clock.at(2 * time.Hour)
	assertVerifies(t, v, mustMint(t, a.iss, MintOptions{Subject: testSubject}), "", "A stale")
	clock.at(2*time.Hour + time.Second)
	assertVerifies(t, v, mustMint(t, b.iss, MintOptions{Subject: testSubject}), "", "B after TTL")
	if na, nb := a.hits.count(), b.hits.count(); na != 3 || nb != 3 {
		t.Fatalf("fetches A=%d B=%d, want 3 and 3", na, nb)
	}
}

func TestJWKS_ConcurrentFailedRefetchCoalescesAndServesStale(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(100 * time.Millisecond)
		failing().ServeHTTP(w, req)
	}))
	r.clock.at(2 * time.Hour)

	const n = 16
	tok := r.token(t)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.v.Verify(context.Background(), tok)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	got := 0
	for err := range errs {
		got++
		if err != nil {
			t.Errorf("concurrent stale Verify: %v", err)
		}
	}
	if got != n {
		t.Fatalf("collected %d results, want %d", got, n)
	}
	r.wantFetches(t, 2, "16 concurrent verifies, failing refetch")
	if warnCount(r) != 1 {
		t.Fatalf("WARN lines = %d, want 1 (one per refetch interval)", warnCount(r))
	}
}

func TestJWKS_CancelledCallersDoNotFailTheSharedFetch(t *testing.T) {
	// No cache, so a failed shared fetch has no stale keys to hide behind.
	r := newBudgetRig(t)
	blk := newBlockingJWKS(t, r.iss.JWKSHandler())
	r.jwks.set(blk)
	tok := r.token(t)
	baseline := goroutinesIn(jwksKeysFrame)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() { _, err := r.v.Verify(leaderCtx, tok); leaderErr <- err }()
	<-blk.entered

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() { _, err := r.v.Verify(waiterCtx, tok); waiterErr <- err }()
	patientErr := make(chan error, 1)
	go func() { _, err := r.v.Verify(context.Background(), tok); patientErr <- err }()
	waitFor(t, "three callers inside jwksKeys", func() bool { return goroutinesIn(jwksKeysFrame) >= baseline+3 })

	cancelWaiter()
	select {
	case err := <-waiterErr:
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("cancelled waiter: err = %v, want ErrUnauthorized", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter did not return while the fetch was in flight")
	}

	cancelLeader()
	blk.unblock()
	for label, ch := range map[string]chan error{"leader": leaderErr, "patient waiter": patientErr} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s: Verify: %v (a cancelled caller failed the shared fetch)", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not return", label)
		}
	}
	r.wantFetches(t, 1, "one shared fetch")
	waitFor(t, "no caller left inside jwksKeys", func() bool { return goroutinesIn(jwksKeysFrame) <= baseline })
}

func TestJWKS_HungFetchReleasesTheInflightSlot(t *testing.T) {
	r := newBudgetRig(t)
	r.v.http = &http.Client{Timeout: 200 * time.Millisecond}
	r.jwks.set(newBlockingJWKS(t, r.iss.JWKSHandler()))
	tok := r.token(t)

	start := time.Now()
	refused(t, r.v, tok, "hung JWKS, no cache")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("hung fetch held Verify for %v", d)
	}
	r.wantFetches(t, 1, "hung fetch")

	r.jwks.set(r.iss.JWKSHandler())
	r.clock.at(10 * time.Second)
	refused(t, r.v, tok, "inside the failure window")
	r.wantFetches(t, 1, "inside the failure window")

	r.clock.at(30 * time.Second)
	assertVerifies(t, r.v, tok, "tenant-x", "after the window")
	r.wantFetches(t, 2, "after the window")
}

func TestJWKS_StaleWarnCarriesNoTokenOrKeyMaterial(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	var set jwks
	rec := httptest.NewRecorder()
	r.iss.JWKSHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	if len(set.Keys) == 0 || set.Keys[0].X == "" {
		t.Fatalf("JWKS has no EC key to look for: %+v", set)
	}
	body := rec.Body.String()
	r.jwks.set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, body, http.StatusServiceUnavailable)
	}))

	r.clock.at(2 * time.Hour)
	tok := r.token(t)
	assertVerifies(t, r.v, tok, "tenant-x", "stale serve")
	logs := r.logs.String()
	if warnCount(r) != 1 {
		t.Fatalf("WARN lines = %d, want 1; log:\n%s", warnCount(r), logs)
	}
	sig := tok[strings.LastIndex(tok, ".")+1:]
	for label, needle := range map[string]string{"token": tok, "signature": sig, "jwk x": set.Keys[0].X, "jwk y": set.Keys[0].Y} {
		if strings.Contains(logs, needle) {
			t.Errorf("stale WARN leaks the %s; log:\n%s", label, logs)
		}
	}
}

// panicOnceTransport panics on its first round trip, then behaves normally.
type panicOnceTransport struct {
	mu sync.Mutex
	n  int
}

func (p *panicOnceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.n++
	first := p.n == 1
	p.mu.Unlock()
	if first {
		panic("jwks transport panic")
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestJWKS_PanickingFetchDoesNotWedgeTheIssuer(t *testing.T) {
	r := newBudgetRig(t)
	r.v.http = &http.Client{Transport: &panicOnceTransport{}}
	tok := r.token(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("first Verify did not panic; the repro is broken")
			}
		}()
		_, _ = r.v.Verify(context.Background(), tok)
	}()
	r.wantFetches(t, 0, "panicking fetch never reached the server")

	r.clock.at(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, err := r.v.Verify(ctx, tok)
	if err != nil {
		t.Fatalf("Verify after a panicking fetch: %v (issuer wedged; JWKS fetches = %d)", err, r.jwks.count())
	}
	if id.Subject != testSubject || id.TenantID != "tenant-x" {
		t.Fatalf("identity = %+v, want subject %s tenant tenant-x", id, testSubject)
	}
	r.wantFetches(t, 1, "fetch after the panic")
}

// A recovery then a new outage warns again; the throttle spans one outage, not the process.
func TestJWKS_StaleWarnReturnsAfterRecoveryThenNewFailure(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())
	r.clock.at(2 * time.Hour)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "first outage")
	if got := warnCount(r); got != 1 {
		t.Fatalf("first outage: WARN lines = %d, want 1", got)
	}

	r.jwks.set(r.iss.JWKSHandler())
	r.clock.at(2*time.Hour + 30*time.Second)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "recovery")
	r.wantFetches(t, 3, "recovery fetch")

	r.jwks.set(failing())
	r.clock.at(3*time.Hour + 31*time.Second)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "second outage")
	r.wantFetches(t, 4, "second outage fetch")
	if got := warnCount(r); got != 2 {
		t.Fatalf("second outage: WARN lines = %d, want 2; log:\n%s", got, r.logs.String())
	}
}

// The throttle is per issuer: one issuer's WARN never silences another's.
func TestJWKS_StaleWarnThrottleIsPerIssuer(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	logs := &lockedBuffer{}
	v, err := NewVerifier(Config{
		Issuer: a.iss.issuer, JWKSURL: a.url, CacheTTL: time.Hour,
		Additional: []TrustedIssuer{{Issuer: b.iss.issuer, JWKSURL: b.url}},
		Logger:     slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	clock := &fakeClock{t: budgetT0}
	v.now = clock.now
	tokA := mustMint(t, a.iss, MintOptions{Subject: testSubject, TenantID: "ta"})
	tokB := mustMint(t, b.iss, MintOptions{Subject: testSubject, TenantID: "tb"})
	assertVerifies(t, v, tokA, "ta", "prime a")
	assertVerifies(t, v, tokB, "tb", "prime b")

	a.hits.set(failing())
	b.hits.set(failing())
	clock.at(2 * time.Hour)
	assertVerifies(t, v, tokA, "ta", "stale a")
	clock.at(2*time.Hour + time.Second)
	assertVerifies(t, v, tokB, "tb", "stale b")

	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Fatalf("WARN lines = %d, want 2 (one per issuer); log:\n%s", n, out)
	}
	for _, iss := range []string{a.iss.issuer, b.iss.issuer} {
		if !strings.Contains(out, "issuer="+iss) {
			t.Errorf("no WARN names issuer %s; log:\n%s", iss, out)
		}
	}
}
