package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type nlRig struct {
	e  env
	ns *NewsletterStore
}

func newNLRig(t *testing.T) *nlRig {
	t.Helper()
	e := newEnv(t)
	rc, err := river.NewClient(riverpgxv5.New(e.app), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	return &nlRig{e: e, ns: NewNewsletterStore(e.app, rc)}
}

// email returns a unique address whose request, subscriber and sync-job rows go at cleanup.
func (r *nlRig) email(t *testing.T) string {
	t.Helper()
	email := uniqueEmail(t, r.e, "nl")
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM blog_subscription_requests WHERE email = $1`, email)
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM blog_subscribers WHERE email = $1`, email)
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM river_job WHERE kind = 'blog_subscriber_sync' AND args->>'email' = $1`, email)
	})
	return email
}

// categories inserts n categories at rising positions and returns their ids in position order.
func (r *nlRig) categories(t *testing.T, n int) []string {
	t.Helper()
	base := 1_000_000 + rand.IntN(1_000_000_000)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "nl-" + uuid.NewString()[:8]
		_, err := r.e.admin.Exec(context.Background(), `
			INSERT INTO blog_categories (id, name, headline, icon, tone, position)
			VALUES ($1, $2, 'h', 'package', 0, $3)`, ids[i], "Name "+ids[i], base+i*10)
		if err != nil {
			t.Fatalf("insert category: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = r.e.admin.Exec(context.Background(), `DELETE FROM blog_categories WHERE id = ANY($1)`, ids)
	})
	return ids
}

type nlRequest struct {
	Hash      []byte
	IDs       []string
	Frequency string
	Expires   time.Time
}

func (r *nlRig) request(t *testing.T, email string) (nlRequest, bool) {
	t.Helper()
	var q nlRequest
	err := r.e.app.QueryRow(context.Background(),
		`SELECT token_hash, category_ids, frequency, expires_at FROM blog_subscription_requests WHERE email = $1`, email).
		Scan(&q.Hash, &q.IDs, &q.Frequency, &q.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false
	}
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	return q, true
}

type nlSubscriber struct {
	IDs         []string
	Frequency   string
	Version     int64
	ConfirmedAt time.Time
	DeliveredAt *time.Time
}

func (r *nlRig) subscriber(t *testing.T, email string) (nlSubscriber, bool) {
	t.Helper()
	var s nlSubscriber
	err := r.e.app.QueryRow(context.Background(),
		`SELECT category_ids, frequency, version, confirmed_at, resend_delivered_at FROM blog_subscribers WHERE email = $1`, email).
		Scan(&s.IDs, &s.Frequency, &s.Version, &s.ConfirmedAt, &s.DeliveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false
	}
	if err != nil {
		t.Fatalf("read subscriber: %v", err)
	}
	return s, true
}

// jobs returns the args of every job of kinds that names the email.
func (r *nlRig) jobs(t *testing.T, email string, kinds ...string) []string {
	t.Helper()
	rows, err := r.e.admin.Query(context.Background(),
		`SELECT args::text FROM river_job WHERE kind = ANY($1) AND args->>'email' = $2 ORDER BY id`, kinds, email)
	if err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan job: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func hashOf(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (r *nlRig) subscribe(t *testing.T, email string, ids []string, freq string) string {
	t.Helper()
	tok, _, err := r.ns.Subscribe(context.Background(), SubscribeIntake{Email: email, CategoryIDs: ids, Frequency: freq})
	if err != nil {
		t.Fatalf("Subscribe() err = %v", err)
	}
	return tok
}

func TestNewsletter_SubscribeStoresHashedTokenAndExpiry(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 3)
	email := r.email(t)
	// Out of position order on purpose.
	tok, names, err := r.ns.Subscribe(context.Background(), SubscribeIntake{Email: email, CategoryIDs: []string{ids[2], ids[0]}, Frequency: "monthly"})
	if err != nil {
		t.Fatalf("Subscribe() err = %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(tok) != 43 || len(raw) != 32 {
		t.Fatalf("token = %q (%d chars), want 43 base64url chars of 32 bytes", tok, len(tok))
	}
	q, ok := r.request(t, email)
	if !ok {
		t.Fatal("no request row")
	}
	if !reflect.DeepEqual(q.Hash, hashOf(tok)) {
		t.Errorf("stored hash is not sha256(token)")
	}
	if want := []string{ids[0], ids[2]}; !reflect.DeepEqual(q.IDs, want) {
		t.Errorf("ids = %v, want %v (position order)", q.IDs, want)
	}
	if want := []string{"Name " + ids[0], "Name " + ids[2]}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if q.Frequency != "monthly" {
		t.Errorf("frequency = %q", q.Frequency)
	}
	if d := time.Until(q.Expires); d < 48*time.Hour-time.Minute || d > 48*time.Hour+time.Minute {
		t.Errorf("expires in %v, want 48h +-1m", d)
	}
	var stored int
	if err := r.e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM blog_subscription_requests WHERE encode(token_hash, 'escape') = $1 OR encode(token_hash, 'base64') = $1`, tok).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("token stored in the clear: count=%d err=%v", stored, err)
	}
}

func TestNewsletter_MixedCaseEmailIsStoredLowerCase(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	local := "ada-" + uuid.NewString()[:8]
	mixed, lower := " "+local+"@Corp.CO ", local+"@corp.co"
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM blog_subscription_requests WHERE email = $1`, lower)
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM blog_subscribers WHERE email = $1`, lower)
		_, _ = r.e.admin.Exec(ctx, `DELETE FROM river_job WHERE kind = 'blog_subscriber_sync' AND args->>'email' = $1`, lower)
	})
	tok := r.subscribe(t, mixed, ids, "weekly")
	if _, ok := r.request(t, lower); !ok {
		t.Fatal("no request row keyed by the lower-case email")
	}
	if got, err := r.ns.Confirm(context.Background(), hashOf(tok)); err != nil || got != lower {
		t.Fatalf("Confirm() = %q, %v; want %q", got, err, lower)
	}
	if _, ok := r.subscriber(t, lower); !ok {
		t.Fatal("no subscriber keyed by the lower-case email")
	}
	jobs := r.jobs(t, lower, "blog_subscriber_sync")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want one for %s", jobs, lower)
	}
}

func TestNewsletter_DuplicateIdsAreDeduped(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 2)
	email := r.email(t)
	r.subscribe(t, email, []string{ids[1], ids[0], ids[1]}, "weekly")
	q, _ := r.request(t, email)
	if !reflect.DeepEqual(q.IDs, ids) {
		t.Errorf("ids = %v, want %v", q.IDs, ids)
	}
}

func TestNewsletter_SubscribeQueuesNothing(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	email := r.email(t)
	r.subscribe(t, email, ids, "weekly")
	if jobs := r.jobs(t, email, "blog_subscriber_sync", "contact_deliver", "demo_deal"); len(jobs) != 0 {
		t.Errorf("jobs = %v, want none", jobs)
	}
	if _, ok := r.subscriber(t, email); ok {
		t.Error("subscriber row exists before confirm")
	}
}

func TestNewsletter_UnknownCategoryStoresNothing(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	email := r.email(t)
	_, _, err := r.ns.Subscribe(context.Background(), SubscribeIntake{Email: email, CategoryIDs: []string{ids[0], "nope"}, Frequency: "weekly"})
	if !errors.Is(err, ErrUnknownCategory) {
		t.Fatalf("err = %v, want ErrUnknownCategory", err)
	}
	if _, ok := r.request(t, email); ok {
		t.Error("request row stored for an unknown category")
	}
}

func TestNewsletter_ResubscribeReplacesTheToken(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	email := r.email(t)
	first := r.subscribe(t, email, ids, "weekly")
	second := r.subscribe(t, email, ids, "monthly")
	var n int
	if err := r.e.app.QueryRow(context.Background(), `SELECT count(*) FROM blog_subscription_requests WHERE email = $1`, email).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, %v; want 1", n, err)
	}
	if _, err := r.ns.Confirm(context.Background(), hashOf(first)); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("Confirm(first) err = %v, want ErrLinkInvalid", err)
	}
	if _, err := r.ns.Confirm(context.Background(), hashOf(second)); err != nil {
		t.Errorf("Confirm(second) err = %v", err)
	}
}

// seedRequest inserts a request directly and returns its token hash.
func (r *nlRig) seedRequest(t *testing.T, email string, expires string) []byte {
	t.Helper()
	hash := hashOf(uuid.NewString())
	_, err := r.e.admin.Exec(context.Background(), `
		INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at)
		VALUES ($1, ARRAY['x'], 'weekly', $2, now() + $3::interval)`, email, hash, expires)
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	t.Cleanup(func() {
		_, _ = r.e.admin.Exec(context.Background(), `DELETE FROM blog_subscription_requests WHERE email = $1`, email)
	})
	return hash
}

func TestNewsletter_SubscribePurgesExpiredRequests(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	other := r.email(t)
	r.seedRequest(t, other, "-1 second")
	r.subscribe(t, r.email(t), ids, "weekly")
	if _, ok := r.request(t, other); ok {
		t.Error("expired request of another email survived Subscribe")
	}
}

func TestNewsletter_PurgeExpiredDeletesOnlyExpired(t *testing.T) {
	r := newNLRig(t)
	gone, kept := r.email(t), r.email(t)
	r.seedRequest(t, gone, "-1 second")
	r.seedRequest(t, kept, "1 hour")
	if err := r.ns.PurgeExpired(context.Background()); err != nil {
		t.Fatalf("PurgeExpired() err = %v", err)
	}
	if _, ok := r.request(t, gone); ok {
		t.Error("expired request survived the purge")
	}
	if _, ok := r.request(t, kept); !ok {
		t.Error("live request was purged")
	}
}

func TestNewsletter_ConfirmIsSingleUse(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	tok := r.subscribe(t, r.email(t), ids, "weekly")
	if _, err := r.ns.Confirm(context.Background(), hashOf(tok)); err != nil {
		t.Fatalf("first Confirm() err = %v", err)
	}
	if _, err := r.ns.Confirm(context.Background(), hashOf(tok)); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("second Confirm() err = %v, want ErrLinkInvalid", err)
	}
}

func TestNewsletter_ExpiredTokenIsInvalid(t *testing.T) {
	r := newNLRig(t)
	email := r.email(t)
	hash := r.seedRequest(t, email, "-1 second")
	if _, err := r.ns.Confirm(context.Background(), hash); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("Confirm() err = %v, want ErrLinkInvalid", err)
	}
	if _, ok := r.subscriber(t, email); ok {
		t.Error("subscriber created from an expired token")
	}
}

func TestNewsletter_ConcurrentConfirmsOneWins(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	email := r.email(t)
	hash := hashOf(r.subscribe(t, email, ids, "weekly"))
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.ns.Confirm(context.Background(), hash)
		}()
	}
	wg.Wait()
	var ok, invalid int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrLinkInvalid):
			invalid++
		}
	}
	if ok != 1 || invalid != 1 {
		t.Errorf("errs = %v, want one nil and one ErrLinkInvalid", errs)
	}
	if n := len(r.jobs(t, email, "blog_subscriber_sync")); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
}

func TestNewsletter_ConfirmUpsertsAndQueuesOneJob(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 2)
	email := r.email(t)
	tok := r.subscribe(t, email, ids, "every_post")
	if _, err := r.ns.Confirm(context.Background(), hashOf(tok)); err != nil {
		t.Fatalf("Confirm() err = %v", err)
	}
	s, ok := r.subscriber(t, email)
	if !ok || s.Version != 1 || s.Frequency != "every_post" || !reflect.DeepEqual(s.IDs, ids) || s.ConfirmedAt.IsZero() {
		t.Fatalf("subscriber = %+v, ok=%v", s, ok)
	}
	jobs := r.jobs(t, email, "blog_subscriber_sync")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want 1", jobs)
	}
	var args BlogSyncArgs
	if err := json.Unmarshal([]byte(jobs[0]), &args); err != nil || args != (BlogSyncArgs{Email: email, Version: 1}) {
		t.Errorf("args = %+v, %v", args, err)
	}
	if _, ok := r.request(t, email); ok {
		t.Error("request row survived the confirm")
	}
}

func TestNewsletter_ReconfirmOverwritesAndBumpsVersion(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 2)
	email := r.email(t)
	if _, err := r.ns.Confirm(context.Background(), hashOf(r.subscribe(t, email, ids[:1], "weekly"))); err != nil {
		t.Fatalf("first Confirm() err = %v", err)
	}
	if _, err := r.e.admin.Exec(context.Background(),
		`UPDATE blog_subscribers SET resend_delivered_at = now(), delivery_mode = 'real' WHERE email = $1`, email); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if _, err := r.ns.Confirm(context.Background(), hashOf(r.subscribe(t, email, ids[1:], "monthly"))); err != nil {
		t.Fatalf("second Confirm() err = %v", err)
	}
	s, _ := r.subscriber(t, email)
	if s.Version != 2 || s.Frequency != "monthly" || !reflect.DeepEqual(s.IDs, ids[1:]) || s.DeliveredAt != nil {
		t.Errorf("subscriber = %+v, want v2, [b], monthly, undelivered", s)
	}
	jobs := r.jobs(t, email, "blog_subscriber_sync")
	if len(jobs) != 2 {
		t.Fatalf("jobs = %v, want 2", jobs)
	}
	var args BlogSyncArgs
	if err := json.Unmarshal([]byte(jobs[1]), &args); err != nil || args.Version != 2 {
		t.Errorf("second job args = %+v, %v; want version 2", args, err)
	}
}

type otherArgs struct{}

func (otherArgs) Kind() string { return "nl_other" }

type otherWorker struct {
	river.WorkerDefaults[otherArgs]
}

func (otherWorker) Work(context.Context, *river.Job[otherArgs]) error { return nil }

func TestNewsletter_FailedQueueRollsBackTheConfirm(t *testing.T) {
	r := newNLRig(t)
	ids := r.categories(t, 1)
	email := r.email(t)
	tok := r.subscribe(t, email, ids, "weekly")
	// A client that registers other kinds only refuses a blog_subscriber_sync insert.
	workers := river.NewWorkers()
	river.AddWorker(workers, otherWorker{})
	rc, err := river.NewClient(riverpgxv5.New(r.e.app), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}}})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	bad := NewNewsletterStore(r.e.app, rc)
	if _, err := bad.Confirm(context.Background(), hashOf(tok)); err == nil || errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("Confirm() err = %v, want a queue error", err)
	}
	if _, ok := r.request(t, email); !ok {
		t.Error("request row gone after a failed queue insert")
	}
	if _, ok := r.subscriber(t, email); ok {
		t.Error("subscriber row stayed after a failed queue insert")
	}
}
