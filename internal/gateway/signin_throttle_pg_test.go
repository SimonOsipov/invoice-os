package gateway

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ownAddress returns a random address and deletes its rows, in every scope, as the superuser on cleanup (D21).
func ownAddress(t *testing.T, d handoffDB) string {
	t.Helper()
	addr := "flake04-" + randHex(t, 8) + "@corp.example"
	h := sha256.Sum256([]byte(signInKey(addr)))
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE key_hash = $1`, h[:])
	})
	return addr
}

// instance stands for one gateway: its own pool and an in-process map that must stay unused.
func instance(name string, max int, pool *pgxpool.Pool) *SignInThrottle {
	th := NewSignInThrottle(name, max, SignInMaxKeys, SignInWindow, time.Now)
	th.Shared(pool, slog.New(slog.DiscardHandler))
	return th
}

// throttleRow reads the shared row for (scope, addr) as the superuser; remaining is expires_at - now().
func throttleRow(t *testing.T, d handoffDB, scope, addr string) (n int, remaining time.Duration) {
	t.Helper()
	h := sha256.Sum256([]byte(signInKey(addr)))
	var secs float64
	err := d.super.QueryRow(context.Background(), `SELECT n, extract(epoch FROM expires_at - now())::float8
		FROM gateway_throttle_counts WHERE scope = $1 AND key_hash = $2`, scope, h[:]).Scan(&n, &secs)
	if err == pgx.ErrNoRows {
		t.Fatalf("no gateway_throttle_counts row for scope %q: the throttle did not count in Postgres", scope)
	}
	if err != nil {
		t.Fatalf("read the throttle row: %v", err)
	}
	return n, time.Duration(secs * float64(time.Second))
}

// reserveSplit makes SignInMaxFailures reservations alternating between a and b.
func reserveSplit(t *testing.T, a, b *SignInThrottle, addr string) {
	t.Helper()
	for i := 0; i < SignInMaxFailures; i++ {
		th := a
		if i%2 == 1 {
			th = b
		}
		if !th.Reserve(addr) {
			t.Fatalf("Reserve #%d = false, want true: the limit is SignInMaxFailures", i+1)
		}
	}
}

func TestRLS_GatewayThrottle_TenFailuresSplitOverTwoInstancesLockTheEleventh(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)

	reserveSplit(t, a, b, addr)

	if a.Reserve(addr) {
		t.Errorf("Reserve #%d on A = true, want false: B's reservations count on A", SignInMaxFailures+1)
	}
	if b.Reserve(addr) {
		t.Errorf("Reserve #%d on B = true, want false: A's reservations count on B", SignInMaxFailures+1)
	}
}

func TestRLS_GatewayThrottle_ARefundOnOneInstanceFreesASlotOnTheOther(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)
	reserveSplit(t, a, b, addr)

	b.Refund(addr)

	if !a.Reserve(addr) {
		t.Error("Reserve on A after a Refund on B = false, want true")
	}
	if b.Reserve(addr) {
		t.Error("second Reserve after one Refund = true, want false")
	}
}

func TestRLS_GatewayThrottle_AResetOnOneInstanceClearsTheOther(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)
	reserveSplit(t, a, b, addr)
	if n, _ := throttleRow(t, d, "sign-in", addr); n != SignInMaxFailures {
		t.Fatalf("row n = %d before Reset, want %d", n, SignInMaxFailures)
	}

	a.Reset(addr)

	if !b.Reserve(addr) {
		t.Error("Reserve on B after a Reset on A = false, want true")
	}
	if n, _ := throttleRow(t, d, "sign-in", addr); n != 1 {
		t.Errorf("row n = %d after Reset and one Reserve, want 1", n)
	}
}

func TestRLS_GatewayThrottle_AnExpiredWindowStartsANewCount(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)
	reserveSplit(t, a, b, addr)
	if n, _ := throttleRow(t, d, "sign-in", addr); n != SignInMaxFailures {
		t.Fatalf("row n = %d before expiry, want %d", n, SignInMaxFailures)
	}
	h := sha256.Sum256([]byte(signInKey(addr)))
	if _, err := d.super.Exec(context.Background(), `UPDATE gateway_throttle_counts SET expires_at = now() - interval '1 second'
		WHERE scope = 'sign-in' AND key_hash = $1`, h[:]); err != nil {
		t.Fatal(err)
	}

	if !b.Reserve(addr) {
		t.Fatal("Reserve on B after the window ended = false, want true")
	}

	n, remaining := throttleRow(t, d, "sign-in", addr)
	if n != 1 {
		t.Errorf("row n = %d after the window ended, want 1", n)
	}
	if remaining < SignInWindow-10*time.Second || remaining > SignInWindow {
		t.Errorf("expires_at is %v away, want about SignInWindow (%v)", remaining, SignInWindow)
	}
}

func TestRLS_GatewayThrottle_ScopesCountApart(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	resendA, resendB := instance("resend-address", ResendPerAddress, d.a), instance("resend-address", ResendPerAddress, d.b)
	signIn := instance("sign-in", SignInMaxFailures, d.a)

	for i, th := range []*SignInThrottle{resendA, resendB, resendA} {
		if !th.Reserve(addr) {
			t.Fatalf("resend Reserve #%d = false, want true: the limit is ResendPerAddress", i+1)
		}
	}
	if resendB.Reserve(addr) {
		t.Errorf("resend Reserve #%d = true, want false", ResendPerAddress+1)
	}
	if !signIn.Reserve(addr) {
		t.Error("sign-in Reserve for the same address = false, want true: scopes count apart")
	}
	if n, _ := throttleRow(t, d, "resend-address", addr); n != ResendPerAddress {
		t.Errorf("resend-address row n = %d, want %d", n, ResendPerAddress)
	}
	if n, _ := throttleRow(t, d, "sign-in", addr); n != 1 {
		t.Errorf("sign-in row n = %d, want 1", n)
	}
}

func TestRLS_GatewayThrottle_TheRowHoldsAHashNotTheAddress(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	local := "mixed-" + randHex(t, 8)
	folded := local + "@corp.example"
	h := sha256.Sum256([]byte(folded))
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE key_hash = $1`, h[:])
	})
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)

	if !a.Reserve(" " + strings.ToUpper(local[:1]) + local[1:] + "@Corp.Example ") {
		t.Fatal("Reserve of the padded, mixed-case spelling on A = false, want true")
	}
	if !b.Reserve(folded) {
		t.Fatal("Reserve of the folded spelling on B = false, want true")
	}

	var rows, n int
	if err := d.super.QueryRow(context.Background(), `SELECT count(*), coalesce(max(n), 0) FROM gateway_throttle_counts
		WHERE scope = 'sign-in' AND key_hash = $1`, h[:]).Scan(&rows, &n); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || n != 2 {
		t.Errorf("rows = %d, n = %d for sha256 of the folded address, want 1 row and n = 2", rows, n)
	}
	var leaks int
	if err := d.super.QueryRow(context.Background(), `SELECT count(*) FROM gateway_throttle_counts t
		WHERE t.scope ILIKE '%' || $1 || '%' OR position(convert_to($1, 'UTF8') in t.key_hash) > 0`, local).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Errorf("%d row(s) carry the address text, want 0: only its hash is stored", leaks)
	}
}

func TestRLS_GatewayThrottle_ConcurrentReservesNeverOvershoot(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)

	const callers = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < callers; i++ {
		th := a
		if i%2 == 1 {
			th = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if th.Reserve(addr) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != SignInMaxFailures {
		t.Errorf("%d of %d concurrent reservations admitted over two instances, want exactly SignInMaxFailures (%d)", admitted, callers, SignInMaxFailures)
	}
}

func TestRLS_GatewayThrottle_SweepDropsExpiredRows(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	expiredScope, expiredKey := ownThrottleRow(t, d)
	liveScope, liveKey := ownThrottleRow(t, d)
	ctx := context.Background()
	for _, r := range []struct {
		scope string
		key   []byte
		at    string
	}{{expiredScope, expiredKey, `now() - interval '1 minute'`}, {liveScope, liveKey, `now() + interval '10 minutes'`}} {
		if _, err := d.super.Exec(ctx, `INSERT INTO gateway_throttle_counts (scope, key_hash, n, expires_at) VALUES ($1, $2, 3, `+r.at+`)`, r.scope, r.key); err != nil {
			t.Fatal(err)
		}
	}
	count := func(scope string) int {
		var n int
		if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_throttle_counts WHERE scope = $1`, scope).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(expiredScope) != 1 || count(liveScope) != 1 {
		t.Fatal("setup: both rows must exist before the first Reserve")
	}

	if !instance("sign-in", SignInMaxFailures, d.a).Reserve(ownAddress(t, d)) {
		t.Fatal("first Reserve of a new shared throttle = false, want true")
	}

	if got := count(expiredScope); got != 0 {
		t.Errorf("expired rows left = %d, want 0: the first Admit sweeps", got)
	}
	if got := count(liveScope); got != 1 {
		t.Errorf("live rows left = %d, want 1: the sweep keeps unexpired rows", got)
	}
}

func errorRecords(logs *logStore) []string {
	var out []string
	for _, r := range logs.all() {
		if r.level == slog.LevelError {
			out = append(out, r.text)
		}
	}
	return out
}

func TestRLS_GatewayThrottle_ARefundFloorsAtZeroAndStaysInItsScope(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	log, logs := newCaptureLog()
	signIn := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	signIn.Shared(d.a, log)
	resend := NewSignInThrottle("resend-address", ResendPerAddress, SignInMaxKeys, SignInWindow, time.Now)
	resend.Shared(d.b, log)

	signIn.Refund(addr)
	var rows int
	h := sha256.Sum256([]byte(signInKey(addr)))
	if err := d.super.QueryRow(context.Background(), `SELECT count(*) FROM gateway_throttle_counts WHERE key_hash = $1`, h[:]).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("a Refund with no row made %d row(s), want 0", rows)
	}

	if !signIn.Reserve(addr) {
		t.Fatal("first Reserve = false, want true")
	}
	signIn.Refund(addr)
	signIn.Refund(addr)
	if n, _ := throttleRow(t, d, "sign-in", addr); n != 0 {
		t.Errorf("row n = %d after two Refunds of one reservation, want 0", n)
	}

	reserveN(t, signIn, addr, SignInMaxFailures)
	resend.Refund(addr)
	if signIn.Reserve(addr) {
		t.Error("a Refund in the resend-address scope freed a sign-in slot, want scopes apart")
	}
	if errs := errorRecords(logs); len(errs) != 0 {
		t.Errorf("ERROR records = %q, want none: a Refund below zero is a no-op, not a CHECK violation", errs)
	}
}

func TestRLS_GatewayThrottle_AResetTouchesOnlyItsOwnAddressAndScope(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr, other := ownAddress(t, d), ownAddress(t, d)
	log, logs := newCaptureLog()
	signIn := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	signIn.Shared(d.a, log)
	resend := NewSignInThrottle("resend-address", ResendPerAddress, SignInMaxKeys, SignInWindow, time.Now)
	resend.Shared(d.b, log)
	reserveN(t, signIn, addr, 2)
	reserveN(t, signIn, other, 3)
	reserveN(t, resend, addr, 1)

	signIn.Reset(addr)

	if n, _ := throttleRow(t, d, "sign-in", other); n != 3 {
		t.Errorf("other address row n = %d after a Reset of one address, want 3", n)
	}
	if n, _ := throttleRow(t, d, "resend-address", addr); n != 1 {
		t.Errorf("resend-address row n = %d after a sign-in Reset, want 1", n)
	}
	var rows int
	h := sha256.Sum256([]byte(signInKey(addr)))
	if err := d.super.QueryRow(context.Background(), `SELECT count(*) FROM gateway_throttle_counts WHERE scope = 'sign-in' AND key_hash = $1`, h[:]).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("sign-in rows for the reset address = %d, want 0", rows)
	}
	signIn.Reset(ownAddress(t, d))
	if errs := errorRecords(logs); len(errs) != 0 {
		t.Errorf("ERROR records = %q, want none: Reset of an address with no row is a no-op", errs)
	}
}

func TestRLS_GatewayThrottle_AnOpenWindowIsNeitherExtendedNorEndedEarly(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)
	reserveSplit(t, a, b, addr)
	h := sha256.Sum256([]byte(signInKey(addr)))
	if _, err := d.super.Exec(context.Background(), `UPDATE gateway_throttle_counts SET expires_at = now() + interval '30 seconds'
		WHERE scope = 'sign-in' AND key_hash = $1`, h[:]); err != nil {
		t.Fatal(err)
	}

	if a.Reserve(addr) {
		t.Error("Reserve in a window with 30 s left and n = max = true, want false")
	}

	n, remaining := throttleRow(t, d, "sign-in", addr)
	if n != SignInMaxFailures {
		t.Errorf("row n = %d after a refused Reserve, want %d", n, SignInMaxFailures)
	}
	if remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("expires_at is %v away after a refused Reserve, want at most 30 s: a refusal does not move the window", remaining)
	}
	b.Refund(addr)
	if !b.Reserve(addr) {
		t.Fatal("Reserve after a Refund = false, want true")
	}
	if _, remaining = throttleRow(t, d, "sign-in", addr); remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("expires_at is %v away after an admitted Reserve in an open window, want at most 30 s: the window starts at the first attempt", remaining)
	}
}

func TestRLS_GatewayThrottle_ConcurrentReservesOnAnExpiredRowAdmitExactlyTheLimit(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	addr := ownAddress(t, d)
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)
	reserveSplit(t, a, b, addr)
	h := sha256.Sum256([]byte(signInKey(addr)))
	if _, err := d.super.Exec(context.Background(), `UPDATE gateway_throttle_counts SET expires_at = now() - interval '1 second'
		WHERE scope = 'sign-in' AND key_hash = $1`, h[:]); err != nil {
		t.Fatal(err)
	}

	const callers = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	var admitted atomic.Int32
	for i := 0; i < callers; i++ {
		th := a
		if i%2 == 1 {
			th = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if th.Reserve(addr) {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := admitted.Load(); got != SignInMaxFailures {
		t.Errorf("%d of %d concurrent reservations admitted on an expired row, want exactly %d: one new window", got, callers, SignInMaxFailures)
	}
	if n, _ := throttleRow(t, d, "sign-in", addr); n != SignInMaxFailures {
		t.Errorf("row n = %d, want %d", n, SignInMaxFailures)
	}
}

func TestRLS_GatewayThrottle_SpellingsThatFoldAlikeShareOneRowIncludingNonASCII(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	local := "élise-" + randHex(t, 8)
	h := sha256.Sum256([]byte(local + "@corp.example"))
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM gateway_throttle_counts WHERE key_hash = $1`, h[:])
	})
	a, b := instance("sign-in", SignInMaxFailures, d.a), instance("sign-in", SignInMaxFailures, d.b)

	if !a.Reserve("\t" + strings.ToUpper(local) + "@CORP.example\n") {
		t.Fatal("Reserve of the upper-cased, padded spelling = false, want true")
	}
	if !b.Reserve(local + "@corp.example") {
		t.Fatal("Reserve of the folded spelling = false, want true")
	}

	var rows, n int
	if err := d.super.QueryRow(context.Background(), `SELECT count(*), coalesce(max(n), 0) FROM gateway_throttle_counts
		WHERE scope = 'sign-in' AND key_hash = $1`, h[:]).Scan(&rows, &n); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || n != 2 {
		t.Errorf("rows = %d, n = %d for sha256 of the folded non-ASCII address, want 1 row and n = 2", rows, n)
	}
}
