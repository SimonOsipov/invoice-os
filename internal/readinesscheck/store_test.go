package readinesscheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("readinesscheck db test skipped: set DATABASE_URL (or run `make test-db PKGS=./internal/readinesscheck/...`)")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// addr returns a unique lower-case address and removes its rows at cleanup.
func addr(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	a := fmt.Sprintf("rc-%s@x.io", uuid.NewString()[:8])
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM readiness_checks WHERE email = $1`, a)
		_, _ = pool.Exec(context.Background(), `DELETE FROM readiness_check_requests WHERE email = $1`, a)
	})
	return a
}

func TestStore_CountFoldsTheAddress(t *testing.T) {
	pool := testPool(t)
	a, b := addr(t, pool), addr(t, pool)
	s := &Store{Pool: pool}
	ctx := context.Background()
	seedChecks(t, pool, a, 2)
	seedChecks(t, pool, b, 1)
	n, err := s.Count(ctx, " "+a[:1]+"C"+a[2:]+" ")
	if err != nil || n != 2 {
		t.Fatalf("Count = %d, %v; want 2", n, err)
	}
}

func TestStore_CountOfANewAddressIsZero(t *testing.T) {
	pool := testPool(t)
	n, err := (&Store{Pool: pool}).Count(context.Background(), addr(t, pool))
	if err != nil || n != 0 {
		t.Fatalf("Count = %d, %v; want 0", n, err)
	}
}

// seedChecks writes n summary rows for the address.
func seedChecks(t *testing.T, pool *pgxpool.Pool, email string, n int) {
	t.Helper()
	for range n {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO readiness_checks (email, file_type, row_count, failed_rule_ids) VALUES (lower(btrim($1)), 'csv', 0, '{}')`, email); err != nil {
			t.Fatal(err)
		}
	}
}

// Skips without DB env, like internal/publiccheck.
func TestFieldLabel_EveryGlobalRuleTargetHasALabel(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("readinesscheck db test skipped: set DATABASE_URL (or run `make test-db PKGS=./internal/readinesscheck/...`)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(ctx, `SELECT DISTINCT target FROM rules WHERE target <> ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatal(err)
		}
		n++
		if fieldLabel(target, "") == target {
			t.Errorf("rule target %q has no field label", target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no rule targets: the migrated database has no global rules")
	}
}

// recMerger records each merge and what its transaction sees.
type recMerger struct {
	mu    sync.Mutex
	calls []mergeCall
	err   error
}

type mergeCall struct {
	email, text     string
	txRows, txHelds int
}

func (m *recMerger) ReadinessCheckTx(ctx context.Context, tx pgx.Tx, email, text string) error {
	c := mergeCall{email: email, text: text}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM readiness_checks WHERE email = $1`, email).Scan(&c.txRows); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM readiness_check_requests WHERE email = $1`, email).Scan(&c.txHelds); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
	return m.err
}

func (m *recMerger) got() []mergeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// sender records the counts it was called with and, per call, what the pool sees while the transaction is still open.
type sender struct {
	pool  *pgxpool.Pool
	merge *recMerger
	delay time.Duration
	err   error

	mu       sync.Mutex
	counts   []int
	merged   []int // merges done when send ran
	poolRows []int // readiness_checks rows the pool saw for the address when send ran
	poolHeld []int // held rows the pool saw for the address when send ran
}

func (s *sender) send(ctx context.Context, h Held, count int) error {
	var rows, held int
	if s.pool != nil {
		q := func(sql string, dst *int) {
			_ = s.pool.QueryRow(context.Background(), sql, h.Email).Scan(dst)
		}
		q(`SELECT count(*) FROM readiness_checks WHERE email = $1`, &rows)
		q(`SELECT count(*) FROM readiness_check_requests WHERE email = $1`, &held)
	}
	s.mu.Lock()
	s.counts = append(s.counts, count)
	s.poolRows = append(s.poolRows, rows)
	s.poolHeld = append(s.poolHeld, held)
	if s.merge != nil {
		s.merged = append(s.merged, len(s.merge.got()))
	}
	s.mu.Unlock()
	time.Sleep(s.delay)
	return s.err
}

func (s *sender) calls() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.counts)
}

func heldFor(email, fileType string, rows int, ids []string) Held {
	return Held{Email: email, Summary: Summary{Email: email, FileType: fileType, RowCount: rows, FailedRuleIDs: ids},
		Mail: accountmail.ReadinessCheckMail{Checked: rows, Total: rows, ChecksLeft: 0, DocumentType: fileType,
			Invoices: []accountmail.ReadinessCheckInvoice{{Number: "INV-1", Total: "10.00", Passes: len(ids) == 0}}}}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

const (
	heldCount = `SELECT count(*) FROM readiness_check_requests WHERE email = $1`
	rowCount  = `SELECT count(*) FROM readiness_checks WHERE email = $1`
)

func TestStore_ConfirmInsertsTheSummaryAndConsumesTheRow(t *testing.T) {
	for _, prior := range []int{0, 1} {
		t.Run(fmt.Sprint(prior, " earlier checks"), func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			a := addr(t, pool)
			seedChecks(t, pool, a, prior)
			m := &recMerger{}
			s := &Store{Pool: pool, Contacts: m}
			held := heldFor(a, "xlsx", 5, []string{"r1", "r2"})
			token, err := s.Hold(ctx, held)
			if err != nil {
				t.Fatal(err)
			}
			snd := &sender{pool: pool, merge: m}
			got, fresh, err := s.Confirm(ctx, token, "the gateway sentence", snd.send)
			if err != nil || !fresh {
				t.Fatalf("Confirm = fresh %v, %v; want fresh and no error", fresh, err)
			}
			if gotJSON, _ := json.Marshal(got.Mail); got.Email != a || got.Summary.FileType != "xlsx" || got.Summary.RowCount != 5 ||
				!slices.Equal(got.Summary.FailedRuleIDs, []string{"r1", "r2"}) || !bytes.Equal(gotJSON, mustJSON(t, held.Mail)) {
				t.Errorf("Held = %+v, want what Hold stored", got)
			}
			if c := snd.calls(); !slices.Equal(c, []int{prior}) {
				t.Errorf("send counts = %v, want [%d]: the count before this check", c, prior)
			}
			calls := m.got()
			if len(calls) != 1 || calls[0].email != a || calls[0].text != "the gateway sentence" {
				t.Fatalf("merges = %+v, want one for the address with the gateway sentence", calls)
			}
			if calls[0].txRows != prior+1 || calls[0].txHelds != 0 {
				t.Errorf("the merge saw %d summary rows and %d held rows in its transaction; want %d and 0 (the Confirm transaction)", calls[0].txRows, calls[0].txHelds, prior+1)
			}
			if snd.merged[0] != 1 || snd.poolRows[0] != prior || snd.poolHeld[0] != 1 {
				t.Errorf("when send ran: merges %d, pool saw %d summary rows and %d held rows; want 1, %d and 1 (the send is inside the open transaction, after the merge)",
					snd.merged[0], snd.poolRows[0], snd.poolHeld[0], prior)
			}
			var ft string
			var rc int
			var ids []string
			if err := pool.QueryRow(ctx, `SELECT file_type, row_count, failed_rule_ids FROM readiness_checks WHERE email = $1 ORDER BY created_at DESC LIMIT 1`, a).Scan(&ft, &rc, &ids); err != nil {
				t.Fatal(err)
			}
			if ft != "xlsx" || rc != 5 || !slices.Equal(ids, []string{"r1", "r2"}) {
				t.Errorf("summary row = %q %d %v, want the held file type, row count and rule ids", ft, rc, ids)
			}
			if n := count(t, pool, rowCount, a); n != prior+1 {
				t.Errorf("summary rows = %d, want %d", n, prior+1)
			}
			if n := count(t, pool, heldCount, a); n != 0 {
				t.Errorf("held rows = %d, want 0", n)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStore_ConfirmPassesTheRecordedCount(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	seedChecks(t, pool, a, 3)
	m := &recMerger{}
	s := &Store{Pool: pool, Contacts: m}
	token, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	snd := &sender{pool: pool, merge: m}
	_, fresh, err := s.Confirm(ctx, token, consentS, snd.send)
	if err != nil || fresh {
		t.Fatalf("Confirm = fresh %v, %v; want not fresh and no error", fresh, err)
	}
	if c := snd.calls(); !slices.Equal(c, []int{3}) {
		t.Errorf("send counts = %v, want [3]", c)
	}
	if n := count(t, pool, rowCount, a); n != 3 {
		t.Errorf("summary rows = %d, want 3: a used address records nothing", n)
	}
	if calls := m.got(); len(calls) != 0 {
		t.Errorf("merges = %+v, want none", calls)
	}
	if n := count(t, pool, heldCount, a); n != 0 {
		t.Errorf("held rows = %d, want 0: the link is spent", n)
	}
}

func TestStore_ConfirmConsumesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	s := &Store{Pool: pool, Contacts: &recMerger{}}
	token, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	snd := &sender{}
	if _, _, err := s.Confirm(ctx, token, consentS, snd.send); err != nil {
		t.Fatalf("first Confirm = %v, want success", err)
	}
	if _, _, err := s.Confirm(ctx, token, consentS, snd.send); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("second Confirm = %v, want ErrLinkInvalid", err)
	}
	if c := snd.calls(); len(c) != 1 {
		t.Errorf("sends = %v, want one", c)
	}

	// Two presses at once: the loser waits on the row lock and finds it gone.
	b := addr(t, pool)
	token, err = s.Hold(ctx, heldFor(b, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	slow := &sender{delay: 200 * time.Millisecond}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, err := s.Confirm(ctx, token, consentS, slow.send)
			errs <- err
		}()
	}
	close(start)
	var ok, gone int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrLinkInvalid):
			gone++
		default:
			t.Errorf("Confirm = %v, want nil or ErrLinkInvalid", err)
		}
	}
	if ok != 1 || gone != 1 || len(slow.calls()) != 1 {
		t.Errorf("presses: %d sent, %d refused, %d sends; want 1, 1, 1", ok, gone, len(slow.calls()))
	}
	if n := count(t, pool, rowCount, b); n != 1 {
		t.Errorf("summary rows = %d, want 1", n)
	}
}

func TestStore_ConfirmRefusesAnExpiredRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	m := &recMerger{}
	s := &Store{Pool: pool, Contacts: m}
	expired, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.Hold(ctx, heldFor(a, "pdf", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE readiness_check_requests SET expires_at = now() - interval '1 minute' WHERE token_hash = sha256(convert_to($1, 'UTF8'))`, expired); err != nil {
		t.Fatal(err)
	}
	snd := &sender{}
	if _, _, err := s.Confirm(ctx, expired, consentS, snd.send); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("Confirm of the expired link = %v, want ErrLinkInvalid", err)
	}
	if c := snd.calls(); len(c) != 0 {
		t.Errorf("sends = %v, want none for the expired link", c)
	}
	if n := count(t, pool, rowCount, a); n != 0 || len(m.got()) != 0 {
		t.Errorf("summary rows = %d, merges = %d; want 0 and 0 after the expired link", n, len(m.got()))
	}
	// The control: the live link of the same address still works, so the refusal above came from the expiry.
	if h, fresh, err := s.Confirm(ctx, live, consentS, snd.send); err != nil || !fresh || h.Summary.FileType != "pdf" {
		t.Errorf("Confirm of the live link = %+v, fresh %v, %v; want its pdf result, fresh, no error", h.Summary, fresh, err)
	}
}

// realContacts is the notifications store the worker merges through, over an insert-only River client.
func realContacts(t *testing.T, pool *pgxpool.Pool) *notifications.Store {
	t.Helper()
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return notifications.NewStore(pool, rc)
}

// contactAddr is addr plus cleanup of the address's contact and jobs, which only the superuser may delete.
func contactAddr(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	url := os.Getenv("DATABASE_SUPERUSER_URL")
	if url == "" {
		t.Skip("readinesscheck db test skipped: set DATABASE_SUPERUSER_URL (or run `make test-db PKGS=./internal/readinesscheck/...`)")
	}
	admin, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	a := addr(t, pool)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.Exec(ctx, `DELETE FROM river_job WHERE args->>'email' = $1`, a)
		_, _ = admin.Exec(ctx, `DELETE FROM contacts WHERE email = $1`, a)
		admin.Close()
	})
	return a
}

const (
	contactCount = `SELECT count(*) FROM contacts WHERE email = $1`
	jobCount     = `SELECT count(*) FROM river_job WHERE args->>'email' = $1`
)

func TestStore_AFailedSendPutsTheRowBack(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := contactAddr(t, pool)
	s := &Store{Pool: pool, Contacts: realContacts(t, pool)}
	token, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("mail down")
	if _, _, err := s.Confirm(ctx, token, consentS, (&sender{err: boom}).send); !errors.Is(err, boom) {
		t.Fatalf("Confirm = %v, want the send's error", err)
	}
	for _, c := range []struct {
		name, sql string
		want      int
	}{{"held rows", heldCount, 1}, {"summary rows", rowCount, 0}, {"contacts", contactCount, 0}, {"jobs", jobCount, 0}} {
		if n := count(t, pool, c.sql, a); n != c.want {
			t.Errorf("%s after a failed send = %d, want %d", c.name, n, c.want)
		}
	}
	_, fresh, err := s.Confirm(ctx, token, consentS, (&sender{}).send)
	if err != nil || !fresh {
		t.Fatalf("second Confirm = fresh %v, %v; want fresh and no error", fresh, err)
	}
	for _, c := range []struct {
		name, sql string
		want      int
	}{{"held rows", heldCount, 0}, {"summary rows", rowCount, 1}, {"contacts", contactCount, 1}} {
		if n := count(t, pool, c.sql, a); n != c.want {
			t.Errorf("%s after the second press = %d, want %d", c.name, n, c.want)
		}
	}
	if n := count(t, pool, jobCount, a); n == 0 {
		t.Error("the second press queued no delivery job")
	}
}

func TestStore_AFailedMergeSendsNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	boom := errors.New("merge down")
	s := &Store{Pool: pool, Contacts: &recMerger{err: boom}}
	token, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	snd := &sender{}
	if _, _, err := s.Confirm(ctx, token, consentS, snd.send); !errors.Is(err, boom) {
		t.Fatalf("Confirm = %v, want the merge's error", err)
	}
	if c := snd.calls(); len(c) != 0 {
		t.Errorf("sends = %v, want none after a failed merge", c)
	}
	if h, r := count(t, pool, heldCount, a), count(t, pool, rowCount, a); h != 1 || r != 0 {
		t.Errorf("held rows = %d, summary rows = %d; want 1 and 0", h, r)
	}
}

func TestStore_ASecondHoldLeavesTheFirstLinkWorking(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	s := &Store{Pool: pool, Contacts: &recMerger{}}
	first, err := s.Hold(ctx, heldFor(a, "csv", 3, []string{"x"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Hold(ctx, heldFor(a, "pdf", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two holds returned one token")
	}
	if n := count(t, pool, heldCount, a); n != 2 {
		t.Fatalf("held rows = %d, want 2: a second hold replaces nothing", n)
	}
	snd := &sender{}
	for i, c := range []struct {
		token, fileType string
		rows            int
	}{{first, "csv", 3}, {second, "pdf", 0}} {
		h, fresh, err := s.Confirm(ctx, c.token, consentS, snd.send)
		if err != nil || !fresh {
			t.Fatalf("Confirm %d = fresh %v, %v; want fresh and no error", i+1, fresh, err)
		}
		if h.Summary.FileType != c.fileType || h.Summary.RowCount != c.rows {
			t.Errorf("Confirm %d Held = %+v, want its own %s result", i+1, h.Summary, c.fileType)
		}
	}
}

func TestStore_TwoLinksPressedTogetherRecordAtMostThree(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	seedChecks(t, pool, a, 2)
	s := &Store{Pool: pool, Contacts: &recMerger{}}
	var tokens []string
	for range 2 {
		tok, err := s.Hold(ctx, heldFor(a, "csv", 1, nil))
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, tok)
	}
	snd := &sender{delay: 200 * time.Millisecond}
	start := make(chan struct{})
	type result struct {
		fresh bool
		err   error
	}
	results := make(chan result, 2)
	for _, tok := range tokens {
		go func() {
			<-start
			_, fresh, err := s.Confirm(ctx, tok, consentS, snd.send)
			results <- result{fresh, err}
		}()
	}
	close(start)
	var fresh int
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Errorf("Confirm = %v", r.err)
		}
		if r.fresh {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d presses were fresh, want exactly 1", fresh)
	}
	if c := snd.calls(); !slices.Equal(slices.Sorted(slices.Values(c)), []int{2, 3}) {
		t.Errorf("send counts = %v, want [2 3]: the second press sees the first", c)
	}
	if n := count(t, pool, rowCount, a); n != 3 {
		t.Errorf("summary rows = %d, want at most 3", n)
	}
}

func TestStore_PurgeExpiredDeletesOnlyExpired(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	old, live := addr(t, pool), addr(t, pool)
	s := &Store{Pool: pool}
	for _, a := range []string{old, live} {
		if _, err := s.Hold(ctx, heldFor(a, "csv", 1, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE readiness_check_requests SET expires_at = now() - interval '1 minute' WHERE email = $1`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if o, l := count(t, pool, heldCount, old), count(t, pool, heldCount, live); o != 0 || l != 1 {
		t.Errorf("held rows: expired %d, live %d; want 0 and 1", o, l)
	}
}

func TestStore_HoldPurgesExpiredRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	old, other := addr(t, pool), addr(t, pool)
	s := &Store{Pool: pool}
	if _, err := s.Hold(ctx, heldFor(old, "csv", 1, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE readiness_check_requests SET expires_at = now() - interval '1 minute' WHERE email = $1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Hold(ctx, heldFor(other, "csv", 1, nil)); err != nil {
		t.Fatal(err)
	}
	if o, n := count(t, pool, heldCount, old), count(t, pool, heldCount, other); o != 0 || n != 1 {
		t.Errorf("held rows: expired %d, new %d; want 0 and 1", o, n)
	}
}

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestStore_HoldStoresOnlyTheTokenHash(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	token, err := (&Store{Pool: pool}).Hold(ctx, heldFor(a, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !tokenShape.MatchString(token) {
		t.Fatalf("token = %q, want 43 base64url characters", token)
	}
	var hash []byte
	var whole string
	if err := pool.QueryRow(ctx, `SELECT token_hash, row_to_json(r)::text FROM readiness_check_requests r WHERE email = $1`, a).Scan(&hash, &whole); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(token)); !bytes.Equal(hash, sum[:]) {
		t.Errorf("token_hash = %x, want sha256 of the token", hash)
	}
	if bytes.Contains([]byte(whole), []byte(token)) {
		t.Errorf("a column holds the token: %s", whole)
	}
}

// The link lives 24 hours, as the mail says.
func TestStore_HoldExpiresInTwentyFourHours(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	if _, err := (&Store{Pool: pool}).Hold(ctx, heldFor(a, "csv", 1, nil)); err != nil {
		t.Fatal(err)
	}
	var life time.Duration
	if err := pool.QueryRow(ctx, `SELECT expires_at - now() FROM readiness_check_requests WHERE email = $1`, a).Scan(&life); err != nil {
		t.Fatal(err)
	}
	if life > 24*time.Hour || life < 24*time.Hour-time.Minute {
		t.Errorf("a new hold expires in %v, want 24h", life)
	}
}

// The merge a registrant gets at the press is the one the intake gives; both write one marketing consent and queue the same jobs.
func TestStore_ConfirmMergesARegistrantLikeTheIntake(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	viaPress, viaIntake := contactAddr(t, pool), contactAddr(t, pool)
	ns := realContacts(t, pool)
	for _, a := range []string{viaPress, viaIntake} {
		if err := ns.Registrant(ctx, notifications.RegistrantIntake{UserID: uuid.NewString(), Email: a, DisplayName: "Ada Lovelace"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{Pool: pool, Contacts: ns}
	token, err := s.Hold(ctx, heldFor(viaPress, "csv", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := s.Confirm(ctx, token, consentS, (&sender{}).send); err != nil || !fresh {
		t.Fatalf("Confirm = fresh %v, %v", fresh, err)
	}
	if err := ns.ReadinessCheck(ctx, notifications.ReadinessIntake{Email: viaIntake, ConsentText: consentS}); err != nil {
		t.Fatal(err)
	}
	type facts struct {
		Consent                          string
		Consented, Registered, Readiness bool
		Version                          int64
	}
	read := func(a string) facts {
		var f facts
		var text *string
		if err := pool.QueryRow(ctx, `SELECT marketing_consent_text, marketing_consented_at IS NOT NULL, registered_at IS NOT NULL,
			readiness_checked_at IS NOT NULL, version FROM contacts WHERE email = $1`, a).
			Scan(&text, &f.Consented, &f.Registered, &f.Readiness, &f.Version); err != nil {
			t.Fatalf("read contact: %v", err)
		}
		if text != nil {
			f.Consent = *text
		}
		return f
	}
	jobs := func(a string) []string {
		rows, err := pool.Query(ctx, `SELECT kind || ' ' || coalesce(args->>'destination', '') || ' ' || coalesce(args->>'version', '') || ' ' || coalesce(args->>'source', '')
			FROM river_job WHERE args->>'email' = $1 ORDER BY 1`, a)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			out = append(out, k)
		}
		return out
	}
	p, i := read(viaPress), read(viaIntake)
	if p != i {
		t.Errorf("contact after the press = %+v, after the intake = %+v; want the same", p, i)
	}
	if p.Consent != consentS || !p.Consented || !p.Registered || !p.Readiness {
		t.Errorf("contact after the press = %+v, want the gateway sentence, a consent, registered and a readiness check", p)
	}
	pj, ij := jobs(viaPress), jobs(viaIntake)
	if !slices.Equal(pj, ij) || len(pj) == 0 {
		t.Errorf("jobs after the press = %v, after the intake = %v; want the same, not empty", pj, ij)
	}
}

// Hold folds the address; a check with no failed rules keeps an empty, non-null rule list through the press.
func TestStore_ConfirmFoldsTheAddressAndKeepsAnEmptyRuleList(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := addr(t, pool)
	m := &recMerger{}
	s := &Store{Pool: pool, Contacts: m}
	token, err := s.Hold(ctx, heldFor("  "+strings.ToUpper(a)+" ", "csv", 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, heldCount, a); n != 1 {
		t.Fatalf("held rows for the folded address = %d, want 1", n)
	}
	got, fresh, err := s.Confirm(ctx, token, "the gateway sentence", (&sender{}).send)
	if err != nil || !fresh || got.Email != a {
		t.Fatalf("Confirm = %q, fresh %v, %v; want the folded address, fresh, no error", got.Email, fresh, err)
	}
	if calls := m.got(); len(calls) != 1 || calls[0].email != a {
		t.Errorf("merges = %+v, want one for the folded address", calls)
	}
	var ids []string
	if err := pool.QueryRow(ctx, `SELECT failed_rule_ids FROM readiness_checks WHERE email = $1`, a).Scan(&ids); err != nil {
		t.Fatalf("no summary row for the folded address: %v", err)
	}
	if ids == nil || len(ids) != 0 {
		t.Errorf("failed_rule_ids = %#v, want an empty list", ids)
	}
}
