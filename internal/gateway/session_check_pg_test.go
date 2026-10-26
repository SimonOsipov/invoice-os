// gateway_session_revocations is global (no tenant, no RLS). Each test owns a random subject (D21).
// CI runs these in the RLS job, where DATABASE_GATEWAY_URL is set.
package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// A session first checked right after a sign-out re-asks only inside revocationSlack, then caches.
func TestRLS_GatewayRevocations_ANewSessionIsReAskedOnlyInsideTheSlack(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	setRevokedAgo := func(subject, interval string) {
		t.Helper()
		_, err := d.super.Exec(context.Background(), `INSERT INTO gateway_session_revocations (subject, revoked_at)
			VALUES ($1, now() - $2::interval) ON CONFLICT (subject) DO UPDATE SET revoked_at = EXCLUDED.revoked_at`, subject, interval)
		if err != nil {
			t.Fatalf("place the revocation: %v", err)
		}
	}
	for _, c := range []struct {
		name, ago string
		want      int
	}{
		{"revoked now: inside the slack", "0 seconds", 2},
		{"revoked 10 s ago: outside the slack", "10 seconds", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			subject := ownSubject(t, d)
			fake := newUserFake(t, http.StatusOK, gtUser)
			rg := newSessionRig(t, fake.URL, nil, nil)
			rg.sessions.Shared(d.b)
			tok := rg.signer.token(t, subject, sid1)
			if rec := rg.get(tok); rec.Code != http.StatusOK || fake.Hits() != 1 {
				t.Fatalf("first request = %d with %d /user calls, want 200 and 1", rec.Code, fake.Hits())
			}
			setRevokedAgo(subject, c.ago)
			if rec := rg.get(tok); rec.Code != http.StatusOK {
				t.Fatalf("request after the revocation = %d, want 200", rec.Code)
			}
			if n := fake.Hits(); n != c.want {
				t.Fatalf("GoTrue /user calls = %d after the revocation, want %d", n, c.want)
			}

			// The fake clock stands still, so the row ageing past the slack is what lets the cache answer.
			setRevokedAgo(subject, "10 seconds")
			for range 3 {
				rg.get(tok)
			}
			if n := fake.Hits(); n != c.want {
				t.Errorf("GoTrue /user calls = %d once the revocation is outside the slack, want %d (cached)", n, c.want)
			}
		})
	}
}

// Sign-out is keyed on the subject: another live session of the subject asks GoTrue and GoTrue's answer stands.
func TestRLS_GatewayRevocations_ASecondSessionOfTheSubjectAsksGoTrueAndItsAnswerStands(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.Shared(d.b)
	refusal := rg.refusal(t)
	tok1, tok2 := rg.signer.token(t, subject, sid1), rg.signer.token(t, subject, sid2)
	for _, tok := range []string{tok1, tok2} {
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Fatalf("request before sign-out = %d, want 200", rec.Code)
		}
	}
	if n := fake.Hits(); n != 2 {
		t.Fatalf("GoTrue /user calls = %d before sign-out, want 2", n)
	}
	other := NewSessionChecker(fake.URL, testClient(), rg.clock.Now, slog.New(slog.DiscardHandler))
	other.Shared(d.a)
	other.EvictSubject(context.Background(), subject)
	// Session 1 was signed out; session 2 still lives in GoTrue.
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+tok1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, gtError(403, "session_not_found"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, gtUser)
	})

	assertRevoked(t, rg.get(tok1), refusal)
	if rec := rg.get(tok2); rec.Code != http.StatusOK {
		t.Errorf("the second session = %d, want 200 (GoTrue still holds it)", rec.Code)
	}
	if n := fake.Hits(); n != 4 {
		t.Errorf("GoTrue /user calls = %d, want 4 (each session asked GoTrue once after the sign-out)", n)
	}
}

// A call GoTrue answered live before the sign-out stores a verdict that predates it; the next request refuses.
func TestRLS_GatewayRevocations_AVerdictAskedBeforeASignOutDoesNotOutliveIt(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.Shared(d.b)
	refusal := rg.refusal(t)
	tok := rg.signer.token(t, subject, sid1)
	release, open := gate(t)
	fake.setAnswer(held(release))
	first := make(chan int, 1)
	go func() { first <- rg.get(tok).Code }()
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 1 }) {
		t.Fatalf("GoTrue /user calls = %d, want the first request's call", fake.Hits())
	}

	other := NewSessionChecker(fake.URL, testClient(), rg.clock.Now, slog.New(slog.DiscardHandler))
	other.Shared(d.a)
	other.EvictSubject(context.Background(), subject)
	open()
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the in-flight request = %d, want 200 (GoTrue answered it live)", code)
	}
	fake.set(http.StatusForbidden, gtError(403, "session_not_found"))

	assertRevoked(t, rg.get(tok), refusal)
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d, want 2 (the stored verdict was not trusted)", n)
	}
}

// A database error from the server, not a dead socket, logs no subject, token or email.
func TestRLS_GatewayRevocations_AServerRefusalLogsNoSubjectTokenOrEmail(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	log, buf := captureLog()
	rg := newSessionRig(t, fake.URL, nil, log)
	rg.sessions.Shared(d.app) // invoice_app holds no grant on the table
	tok := rg.signer.token(t, subject, sid1)
	for i := range 2 {
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, rec.Code)
		}
	}
	rg.sessions.EvictSubject(context.Background(), subject)

	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d, want 2 (the refused read asked GoTrue)", n)
	}
	if n := len(recordsNamed(t, buf, "session check: revocation read failed")); n != 1 {
		t.Errorf("read failure records = %d, want 1", n)
	}
	if n := len(recordsNamed(t, buf, "session check: record sign-out")); n != 1 {
		t.Errorf("write failure records = %d, want 1", n)
	}
	requireNoLeak(t, buf, tok, subject, "ada@corp.example")
}

// A row stays while a cache entry could still match it (under 31 s) and goes once it is three TTLs old.
func TestRLS_GatewayRevocations_EvictKeepsRowsAnEntryCouldMatchAndSweepsOldOnes(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	young, old, trigger := ownSubject(t, d), ownSubject(t, d), ownSubject(t, d)
	for sub, ago := range map[string]time.Duration{young: SessionCheckTTL, old: 3 * SessionCheckTTL} {
		if _, err := d.super.Exec(ctx, `INSERT INTO gateway_session_revocations (subject, revoked_at)
			VALUES ($1, now() - $2 * interval '1 microsecond')`, sub, ago.Microseconds()); err != nil {
			t.Fatalf("place a revocation: %v", err)
		}
	}
	c := NewSessionChecker(nil, nil, time.Now, slog.New(slog.DiscardHandler))
	c.Shared(d.a)

	c.EvictSubject(ctx, trigger)

	var kept, swept int
	if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_session_revocations WHERE subject = $1`, young).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_session_revocations WHERE subject = $1`, old).Scan(&swept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Errorf("the %v-old row count = %d, want 1 (an entry of that age could still match it)", SessionCheckTTL, kept)
	}
	if swept != 0 {
		t.Errorf("the %v-old row count = %d, want 0 (no entry can match it)", 3*SessionCheckTTL, swept)
	}
}

// A pool whose reads work and writes fail: only the local eviction can make the next request ask GoTrue.
func TestRLS_GatewayRevocations_AFailedWriteStillEvictsLocally(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_GATEWAY_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	subject := ownSubject(t, d)
	fake := newUserFake(t, http.StatusOK, gtUser)
	log, buf := captureLog()
	rg := newSessionRig(t, fake.URL, nil, log)
	rg.sessions.Shared(ro)
	tok := rg.signer.token(t, subject, sid1)
	for i := range 2 {
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, rec.Code)
		}
	}
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue /user calls = %d before the eviction, want 1 (cached, the read works)", n)
	}

	rg.sessions.EvictSubject(context.Background(), subject)
	if rec := rg.get(tok); rec.Code != http.StatusOK {
		t.Fatalf("request after the eviction = %d, want 200", rec.Code)
	}

	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d after the eviction, want 2 (no row was written, so the local eviction did it)", n)
	}
	if recs := recordsNamed(t, buf, "session check: record sign-out"); len(recs) != 1 || recs[0]["level"] != "ERROR" {
		t.Errorf("sign-out write records = %v, want exactly one ERROR", recs)
	}
	if n := len(recordsNamed(t, buf, "session check: revocation read failed")); n != 0 {
		t.Errorf("read failure records = %d, want 0 (the pool reads)", n)
	}
}

// A client disconnect must not drop the shared sign-out row.
func TestRLS_GatewayRevocations_ACancelledRequestStillRecordsTheSignOut(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	subject := ownSubject(t, d)
	c := NewSessionChecker(nil, nil, time.Now, slog.New(slog.DiscardHandler))
	c.Shared(d.a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c.EvictSubject(ctx, subject)

	var n int
	if err := d.super.QueryRow(context.Background(), `SELECT count(*) FROM gateway_session_revocations WHERE subject = $1`, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sign-out rows = %d after a cancelled request, want 1", n)
	}
}

// A client disconnect must not read as a failed revocation read.
func TestRLS_GatewayRevocations_ACancelledRequestReadsTheTableWithoutAWarn(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	log, buf := captureLog()
	c := NewSessionChecker(nil, nil, time.Now, log)
	c.Shared(d.a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now()

	revoked := c.signedOutSince(ctx, sessionEntry{subject: ownSubject(t, d), live: true, checkedAt: now}, now)

	if revoked {
		t.Error("signedOutSince = true, want false: no sign-out row exists")
	}
	if n := len(recordsNamed(t, buf, "session check: revocation read failed")); n != 0 {
		t.Errorf("revocation read failed records = %d, want 0", n)
	}
}

// The sign-out was recorded, so a failed sweep must not log "record sign-out".
func TestRLS_GatewayRevocations_AFailedSweepIsNotAFailedRecord(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	subject, marker := ownSubject(t, d), ownSubject(t, d)
	fn := "flake04_block_" + randHex(t, 4)
	for _, q := range []string{
		`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'sweep blocked'; END $$`,
		`CREATE TRIGGER ` + fn + ` BEFORE DELETE ON gateway_session_revocations FOR EACH ROW WHEN (OLD.subject = '` + marker + `') EXECUTE FUNCTION ` + fn + `()`,
	} {
		if _, err := d.super.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.super.Exec(ctx, `DROP TRIGGER `+fn+` ON gateway_session_revocations`)
		_, _ = d.super.Exec(ctx, `DROP FUNCTION `+fn+`()`)
	})
	if _, err := d.super.Exec(ctx, `INSERT INTO gateway_session_revocations (subject, revoked_at)
		VALUES ($1, now() - $2 * interval '1 microsecond')`, marker, (revocationKeep + time.Second).Microseconds()); err != nil {
		t.Fatal(err)
	}
	log, buf := captureLog()
	c := NewSessionChecker(nil, nil, time.Now, log)
	c.Shared(d.a)

	c.EvictSubject(ctx, subject)

	if n := len(recordsNamed(t, buf, "session check: record sign-out")); n != 0 {
		t.Errorf("record sign-out records = %d, want 0: the INSERT succeeded", n)
	}
	if n := len(recordsNamed(t, buf, "session check: sweep sign-outs")); n != 1 {
		t.Errorf("sweep records = %d, want 1", n)
	}
	if strings.Contains(buf.String(), subject) {
		t.Errorf("the log carries the subject, want none: %s", buf.String())
	}
	var n int
	if err := d.super.QueryRow(ctx, `SELECT count(*) FROM gateway_session_revocations WHERE subject = $1`, subject).Scan(&n); err != nil || n != 1 {
		t.Errorf("sign-out rows = %d (err %v), want 1", n, err)
	}
}

// A sign-out write stuck behind a row lock gives up after handoffDBTimeout, even for a live request.
func TestRLS_GatewayRevocations_AStalledWriteIsBounded(t *testing.T) {
	d := requireHandoffDB(t)
	requireSharedStateTables(t, d)
	ctx := context.Background()
	subject := ownSubject(t, d)
	if _, err := d.super.Exec(ctx, `INSERT INTO gateway_session_revocations (subject, revoked_at) VALUES ($1, now())`, subject); err != nil {
		t.Fatal(err)
	}
	tx, err := d.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT 1 FROM gateway_session_revocations WHERE subject = $1 FOR UPDATE`, subject); err != nil {
		t.Fatal(err)
	}
	log, buf := captureLog()
	c := NewSessionChecker(nil, nil, time.Now, log)
	c.Shared(d.a)

	done := make(chan struct{})
	go func() { c.EvictSubject(ctx, subject); close(done) }()
	select {
	case <-done:
	case <-time.After(handoffDBTimeout + 3*time.Second):
		t.Fatal("EvictSubject still waiting on a locked row, want it bounded by handoffDBTimeout")
	}
	if n := len(recordsNamed(t, buf, "session check: record sign-out")); n != 1 {
		t.Errorf("record sign-out records = %d, want 1", n)
	}
}
