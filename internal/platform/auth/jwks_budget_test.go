package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// Tokens carry real-time exp, so only v.now moves; the clock never touches token validity.
var budgetT0 = time.Unix(1_800_000_000, 0)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) at(d time.Duration) {
	c.mu.Lock()
	c.t = budgetT0.Add(d)
	c.mu.Unlock()
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type budgetRig struct {
	iss   *MockIssuer
	jwks  *swapHandler
	clock *fakeClock
	logs  *lockedBuffer
	v     *Verifier
}

// newBudgetRig wires a verifier (1h TTL) to a counting JWKS server and a fake clock at t0.
func newBudgetRig(t *testing.T) *budgetRig {
	t.Helper()
	iss := mustIssuer(t)
	h := &swapHandler{h: iss.JWKSHandler()}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	logs := &lockedBuffer{}
	v, err := NewVerifier(Config{
		Issuer:   iss.issuer,
		JWKSURL:  srv.URL,
		CacheTTL: time.Hour,
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	clock := &fakeClock{t: budgetT0}
	v.now = clock.now
	return &budgetRig{iss: iss, jwks: h, clock: clock, logs: logs, v: v}
}

func (r *budgetRig) token(t *testing.T) string {
	return mustMint(t, r.iss, MintOptions{Subject: testSubject, TenantID: "tenant-x"})
}

func (r *budgetRig) prime(t *testing.T) {
	t.Helper()
	assertVerifies(t, r.v, r.token(t), "tenant-x", "prime")
	if n := r.jwks.count(); n != 1 {
		t.Fatalf("after prime: JWKS fetches = %d, want 1", n)
	}
}

func (r *budgetRig) wantFetches(t *testing.T, want int, label string) {
	t.Helper()
	if n := r.jwks.count(); n != want {
		t.Fatalf("%s: JWKS fetches = %d, want %d", label, n, want)
	}
}

func refused(t *testing.T, v *Verifier, tok, label string) {
	t.Helper()
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("%s: Verify err = %v, want ErrUnauthorized", label, err)
	}
}

func failing() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}

// signWithKid signs valid claims with iss's key under a kid the JWKS never serves.
func signWithKid(t *testing.T, iss *MockIssuer, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, validClaims(iss))
	tok.Header["kid"] = kid
	s, err := tok.SignedString(iss.key)
	if err != nil {
		t.Fatalf("signWithKid: %v", err)
	}
	return s
}

func TestJWKS_TTLExpiryRefetches(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	r.clock.at(59 * time.Minute)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+59m")
	r.wantFetches(t, 1, "t0+59m (cache fresh)")

	r.clock.at(61 * time.Minute)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+61m")
	r.wantFetches(t, 2, "t0+61m (TTL expired)")
}

func TestJWKS_TTLBoundaryIsExclusive(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	r.clock.at(time.Hour - time.Nanosecond)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+1h-1ns")
	r.wantFetches(t, 1, "t0+1h-1ns (cache fresh)")

	r.clock.at(time.Hour)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+1h")
	r.wantFetches(t, 2, "exactly fetchedAt+TTL (expired)")
}

func TestJWKS_ServesStaleWhenRefetchFails(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	if strings.Contains(r.logs.String(), "level=WARN") {
		t.Fatalf("WARN logged before any failed refetch:\n%s", r.logs.String())
	}

	r.jwks.set(failing())
	r.clock.at(2 * time.Hour)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+2h, refetch failing, stale key")
	r.wantFetches(t, 2, "t0+2h (refetch attempted)")
	if !strings.Contains(r.logs.String(), "level=WARN") {
		t.Fatalf("serving stale keys logged no WARN; log:\n%s", r.logs.String())
	}
}

func TestJWKS_StaleServeWarnsOncePerRefetchInterval(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())

	for i := 0; i < 50; i++ {
		r.clock.at(2*time.Hour + time.Duration(i)*29*time.Second/49)
		assertVerifies(t, r.v, r.token(t), "tenant-x", fmt.Sprintf("stale serve #%d", i))
	}
	if n := strings.Count(r.logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("50 stale serves inside one interval: WARN lines = %d, want 1", n)
	}

	r.clock.at(2*time.Hour + 30*time.Second)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "stale serve after the interval")
	if n := strings.Count(r.logs.String(), "level=WARN"); n != 2 {
		t.Fatalf("after the interval: WARN lines = %d, want 2", n)
	}
}

func TestJWKS_StaleWindowEnds(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)
	r.jwks.set(failing())

	// Inside TTL + 6h grace the stale key still verifies; this makes the refusal below non-vacuous.
	r.clock.at(time.Hour + 6*time.Hour - time.Minute)
	assertVerifies(t, r.v, r.token(t), "tenant-x", "t0+6h59m, inside stale window")

	r.clock.at(time.Hour + 6*time.Hour + time.Second)
	refused(t, r.v, r.token(t), "t0+7h+1s, stale window over")
}

func TestJWKS_UnknownKidRefetchIsThrottled(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	r.clock.at(time.Second)
	toks := make([]string, 50)
	for i := range toks {
		toks[i] = signWithKid(t, r.iss, uuid.NewString())
	}
	for i, tok := range toks {
		refused(t, r.v, tok, fmt.Sprintf("random kid #%d", i))
	}
	r.wantFetches(t, 2, "50 unknown kids at t0+1s")
}

func TestJWKS_RotationAcceptedAfterWindow(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	key2 := mustIssuer(t)
	r.jwks.set(key2.JWKSHandler())
	r.clock.at(time.Second)
	assertVerifies(t, r.v, mustMint(t, key2, MintOptions{Subject: testSubject, TenantID: "t2"}), "t2", "key2 at t0+1s (prime does not anchor)")
	r.wantFetches(t, 2, "key2 at t0+1s")

	key3 := mustIssuer(t)
	r.jwks.set(key3.JWKSHandler())
	tok3 := mustMint(t, key3, MintOptions{Subject: testSubject, TenantID: "t3"})
	r.clock.at(10 * time.Second)
	refused(t, r.v, tok3, "key3 at t0+10s, inside window")
	r.wantFetches(t, 2, "key3 at t0+10s (throttled)")

	r.clock.at(31 * time.Second)
	assertVerifies(t, r.v, tok3, "t3", "key3 at t0+31s, window elapsed")
	r.wantFetches(t, 3, "key3 at t0+31s")
}

func TestJWKS_ForcedRefetchWindowBoundary(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	t1 := 5 * time.Second
	r.clock.at(t1)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1")
	r.wantFetches(t, 2, "forced fetch at t1")

	r.clock.at(t1 + 30*time.Second - time.Millisecond)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1+29.999s")
	r.wantFetches(t, 2, "t1+29.999s (inside window)")

	r.clock.at(t1 + 30*time.Second)
	refused(t, r.v, signWithKid(t, r.iss, uuid.NewString()), "unknown kid at t1+30s")
	r.wantFetches(t, 3, "exactly t1+30s (boundary allows)")
}

func TestJWKS_FailedFetchBacksOff(t *testing.T) {
	r := newBudgetRig(t)
	r.jwks.set(failing())
	tok := r.token(t)

	for i := 0; i < 20; i++ {
		r.clock.at(time.Duration(i) * 10 * time.Second / 19)
		refused(t, r.v, tok, "no cache, failing JWKS")
	}
	r.wantFetches(t, 1, "20 verifies over t0..t0+10s")

	r.clock.at(31 * time.Second)
	refused(t, r.v, tok, "t0+31s, still failing")
	r.wantFetches(t, 2, "t0+31s (window elapsed)")
}

func TestJWKS_ConcurrentExpiryCoalesces(t *testing.T) {
	r := newBudgetRig(t)
	r.prime(t)

	good := r.iss.JWKSHandler()
	r.jwks.set(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(100 * time.Millisecond)
		good.ServeHTTP(w, req)
	}))
	r.clock.at(2 * time.Hour)

	const n = 32
	tok := r.token(t)
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := r.v.Verify(context.Background(), tok)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	got := 0
	for err := range errs {
		got++
		if err != nil {
			t.Errorf("concurrent Verify: %v", err)
		}
	}
	if got != n {
		t.Fatalf("collected %d results, want %d", got, n)
	}
	r.wantFetches(t, 2, "32 concurrent verifies on an expired cache (prime + one refetch)")
}

func TestJWKS_NoCacheAndFailedFetchRefuses(t *testing.T) {
	r := newBudgetRig(t)
	r.jwks.set(failing())
	tok := r.token(t)

	refused(t, r.v, tok, "no cache, failing JWKS")
	r.wantFetches(t, 1, "no cache (fetch attempted)")

	// The same token verifies once keys are reachable, so the refusal came from the missing keys.
	r.jwks.set(r.iss.JWKSHandler())
	r.clock.at(31 * time.Second)
	assertVerifies(t, r.v, tok, "tenant-x", "JWKS recovered at t0+31s")
}
