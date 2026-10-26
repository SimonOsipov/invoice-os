// gateway_session_revocations is global (no tenant, no RLS). Each test owns a random subject (D21).
// CI runs these in the RLS job, where DATABASE_GATEWAY_URL is set.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestRLS_GatewayRevocations_ASignOutThroughOnePoolRefusesTheCachedSessionOnAnother(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.Shared(d.b)
	refusal := rg.refusal(t)
	tok := rg.signer.token(t, subject, sid1)
	for i := range 2 {
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Fatalf("request %d before sign-out = %d, want 200", i+1, rec.Code)
		}
	}
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue /user calls = %d before sign-out, want 1 (the second request is cached)", n)
	}

	other := NewSessionChecker(fake.URL, testClient(), rg.clock.Now, slog.New(slog.DiscardHandler))
	other.Shared(d.a)
	other.EvictSubject(context.Background(), subject)
	fake.set(http.StatusForbidden, gtError(403, "session_not_found"))

	assertRevoked(t, rg.get(tok), refusal)
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d after the sign-out on the other pool, want 2", n)
	}
}

// Holds today: no revocation means the cache answers, as before.
func TestRLS_GatewayRevocations_ALiveSessionStaysCached(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.Shared(d.b)
	tok := rg.signer.token(t, subject, sid1)

	for i := range 5 {
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, rec.Code)
		}
	}
	t0 := rg.clock.Now()
	for _, s := range []struct {
		at   time.Duration
		want int
	}{
		{0, 1},
		{SessionCheckTTL - time.Nanosecond, 1},
		{SessionCheckTTL, 2},
	} {
		rg.clock.Advance(t0.Add(s.at).Sub(rg.clock.Now()))
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Errorf("t0+%v: status = %d, want 200", s.at, rec.Code)
		}
		if n := fake.Hits(); n != s.want {
			t.Errorf("t0+%v: GoTrue /user calls = %d, want %d", s.at, n, s.want)
		}
	}
}

// The row sits 10 s outside and 10 s inside the 21 s window of a 20 s old entry (D21).
func TestRLS_GatewayRevocations_TheWindowDecidesAReCheck(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.Shared(d.b)
	tok := rg.signer.token(t, subject, sid1)
	if rec := rg.get(tok); rec.Code != http.StatusOK || fake.Hits() != 1 {
		t.Fatalf("first request = %d with %d /user calls, want 200 and 1", rec.Code, fake.Hits())
	}
	rg.clock.Advance(20 * time.Second)
	revokeAgo := func(secs int) {
		t.Helper()
		_, err := d.super.Exec(context.Background(), `INSERT INTO gateway_session_revocations (subject, revoked_at)
			VALUES ($1, now() - $2::int * interval '1 second')
			ON CONFLICT (subject) DO UPDATE SET revoked_at = EXCLUDED.revoked_at`, subject, secs)
		if err != nil {
			t.Fatalf("place the revocation: %v", err)
		}
	}

	revokeAgo(31)
	if rec := rg.get(tok); rec.Code != http.StatusOK {
		t.Fatalf("request with a revocation outside the window = %d, want 200", rec.Code)
	}
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue /user calls = %d with a revocation outside the window, want 1 (cache kept)", n)
	}

	revokeAgo(11)
	if rec := rg.get(tok); rec.Code != http.StatusOK {
		t.Fatalf("request with a revocation inside the window = %d, want 200", rec.Code)
	}
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d with a revocation inside the window, want 2 (GoTrue asked)", n)
	}
}

func TestRLS_GatewayRevocations_EvictSweepsOldRowsAndUpdatesItsOwn(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	x, y := ownSubject(t, d), ownSubject(t, d)
	count := func(sub string) int {
		t.Helper()
		var n int
		if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_session_revocations WHERE subject = $1`, sub).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	revokedAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := d.super.QueryRow(ctx, `SELECT revoked_at FROM gateway_session_revocations WHERE subject = $1`, y).Scan(&at); err != nil {
			t.Fatalf("read Y's revocation: %v", err)
		}
		return at
	}
	if _, err := d.super.Exec(ctx, `INSERT INTO gateway_session_revocations (subject, revoked_at)
		VALUES ($1, now() - $2 * interval '1 microsecond')`, x, (revocationKeep + time.Second).Microseconds()); err != nil {
		t.Fatalf("place X's old revocation: %v", err)
	}
	if count(x) != 1 {
		t.Fatal("X's old row was not placed")
	}
	c := NewSessionChecker(nil, nil, time.Now, slog.New(slog.DiscardHandler))
	c.Shared(d.a)

	c.EvictSubject(ctx, y)
	first := revokedAt()
	time.Sleep(20 * time.Millisecond)
	c.EvictSubject(ctx, y)
	second := revokedAt()

	if n := count(x); n != 0 {
		t.Errorf("X rows = %d after an eviction, want 0 (older than revocationKeep, swept)", n)
	}
	if n := count(y); n != 1 {
		t.Errorf("Y rows = %d after two evictions, want 1", n)
	}
	if !second.After(first) {
		t.Errorf("Y revoked_at = %v after the repeat, want later than %v", second, first)
	}
}
