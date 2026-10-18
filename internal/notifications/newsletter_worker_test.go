package notifications

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

type blogCall struct{ S BlogSubscriber }

type recBlogResend struct {
	mu    sync.Mutex
	calls []blogCall
	fn    func(n int, s BlogSubscriber) error
}

func (r *recBlogResend) SyncBlog(_ context.Context, s BlogSubscriber) error {
	r.mu.Lock()
	r.calls = append(r.calls, blogCall{s})
	n, fn := len(r.calls), r.fn
	r.mu.Unlock()
	if fn != nil {
		return fn(n, s)
	}
	return nil
}

func (r *recBlogResend) ContactUnsubscribed(context.Context, string) (bool, error) { return false, nil }

func (r *recBlogResend) got() []blogCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

type blogRig struct {
	e    env
	rs   *recBlogResend
	w    *BlogSyncWorker
	logs *qaLogSink
}

func newBlogRig(t *testing.T, mode Mode) *blogRig {
	t.Helper()
	e := newEnv(t)
	r := &blogRig{e: e, rs: &recBlogResend{}, logs: &qaLogSink{}}
	r.w = &BlogSyncWorker{Pool: e.app, Resend: r.rs, Mode: mode, Logger: r.logs.logger()}
	return r
}

func (r *blogRig) subscriber(t *testing.T, version int64) string {
	t.Helper()
	email := uniqueEmail(t, r.e, "blog")
	t.Cleanup(func() {
		_, _ = r.e.admin.Exec(context.Background(), `DELETE FROM blog_subscribers WHERE email = $1`, email)
	})
	_, err := r.e.app.Exec(context.Background(), `
		INSERT INTO blog_subscribers (email, category_ids, frequency, confirmed_at, version)
		VALUES ($1, ARRAY['a','b'], 'weekly', now(), $2)`, email, version)
	if err != nil {
		t.Fatalf("insert subscriber: %v", err)
	}
	return email
}

func (r *blogRig) work(email string, version int64) error {
	return r.w.Work(context.Background(), &river.Job[BlogSyncArgs]{Args: BlogSyncArgs{Email: email, Version: version}})
}

type blogRow struct {
	Version     int64
	DeliveredAt *time.Time
	Mode        *string
}

func (r *blogRig) row(t *testing.T, email string) blogRow {
	t.Helper()
	var b blogRow
	err := r.e.app.QueryRow(context.Background(),
		`SELECT version, resend_delivered_at, delivery_mode FROM blog_subscribers WHERE email = $1`, email).
		Scan(&b.Version, &b.DeliveredAt, &b.Mode)
	if err != nil {
		t.Fatalf("read subscriber: %v", err)
	}
	return b
}

func TestBlogSync_DeliversOnceAndRecords(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	email := r.subscriber(t, 1)
	if err := r.work(email, 1); err != nil {
		t.Fatalf("Work() err = %v", err)
	}
	want := []blogCall{{BlogSubscriber{Email: email, CategoryIDs: []string{"a", "b"}, Frequency: "weekly"}}}
	if got := r.rs.got(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
	row := r.row(t, email)
	if row.DeliveredAt == nil || row.Mode == nil || *row.Mode != "real" {
		t.Fatalf("row = %+v, want delivered with mode real", row)
	}
	if err := r.work(email, 1); err != nil {
		t.Fatalf("second Work() err = %v", err)
	}
	if n := len(r.rs.got()); n != 1 {
		t.Errorf("calls after second Work = %d, want 1", n)
	}
}

func TestBlogSync_StaleVersionMakesNoCall(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	email := r.subscriber(t, 2)
	if err := r.work(email, 1); err != nil {
		t.Fatalf("Work() err = %v", err)
	}
	if n := len(r.rs.got()); n != 0 {
		t.Errorf("calls = %d, want 0", n)
	}
	if row := r.row(t, email); row.DeliveredAt != nil {
		t.Errorf("delivery set on a stale job: %+v", row)
	}
}

func TestBlogSync_MissingRowIsNil(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	if err := r.work(uniqueEmail(t, r.e, "blog-none"), 1); err != nil {
		t.Fatalf("Work() err = %v, want nil", err)
	}
	if n := len(r.rs.got()); n != 0 {
		t.Errorf("calls = %d, want 0", n)
	}
}

func TestBlogSync_OutageRetriesThenArrives(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	r.rs.fn = func(n int, _ BlogSubscriber) error {
		if n == 1 {
			return &DeliveryError{Status: 503}
		}
		return nil
	}
	email := r.subscriber(t, 1)
	var de *DeliveryError
	if err := r.work(email, 1); !errors.As(err, &de) || de.Status != 503 {
		t.Fatalf("first Work() err = %v, want *DeliveryError{503}", err)
	}
	if row := r.row(t, email); row.DeliveredAt != nil {
		t.Fatalf("delivery set after an outage: %+v", row)
	}
	if err := r.work(email, 1); err != nil {
		t.Fatalf("second Work() err = %v", err)
	}
	if row := r.row(t, email); row.DeliveredAt == nil {
		t.Errorf("delivery unset after the retry: %+v", row)
	}
}

func TestBlogSync_WaitsForTheContactDeliverLock(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	email := r.subscriber(t, 1)
	ctx := context.Background()
	tx, err := r.e.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_deliver:resend:' || $1::text, 0))`, email); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- r.work(email, 1) }()
	select {
	case err := <-done:
		t.Fatalf("Work returned %v while another tx held the lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if n := len(r.rs.got()); n != 0 {
		t.Fatalf("calls before release = %d, want 0", n)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Work() err = %v", err)
		}
	case <-time.After(qaHoldFor):
		t.Fatal("Work never returned after the lock was released")
	}
	if n := len(r.rs.got()); n != 1 {
		t.Errorf("calls after release = %d, want 1", n)
	}
}

func TestBlogSync_VersionMovedDuringCallRecordsNothing(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	email := r.subscriber(t, 1)
	r.rs.fn = func(int, BlogSubscriber) error {
		_, err := r.e.app.Exec(context.Background(), `UPDATE blog_subscribers SET version = 2 WHERE email = $1`, email)
		return err
	}
	if err := r.work(email, 1); err != nil {
		t.Fatalf("Work() err = %v, want nil", err)
	}
	if row := r.row(t, email); row.Version != 2 || row.DeliveredAt != nil {
		t.Errorf("row = %+v, want version 2 and delivery unset", row)
	}
}

type failingTransport struct{ hits int }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.hits++
	return nil, errors.New("test: network is off")
}

func TestBlogSync_FakeModeRecordsFake(t *testing.T) {
	ft := &failingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ft
	t.Cleanup(func() { http.DefaultTransport = old })

	r := newBlogRig(t, ModeFake)
	r.w.Resend = FakeBlogResend{}
	email := r.subscriber(t, 1)
	if err := r.work(email, 1); err != nil {
		t.Fatalf("Work() err = %v", err)
	}
	if row := r.row(t, email); row.DeliveredAt == nil || row.Mode == nil || *row.Mode != "fake" {
		t.Errorf("row = %+v, want delivered with mode fake", row)
	}
	if ft.hits != 0 {
		t.Errorf("fake mode made %d HTTP calls", ft.hits)
	}
}

func TestBlogSync_WorkerHasNoHubSpotField(t *testing.T) {
	hs := reflect.TypeOf((*HubSpotClient)(nil)).Elem()
	typ := reflect.TypeOf(BlogSyncWorker{})
	for i := range typ.NumField() {
		if f := typ.Field(i); f.Type == hs {
			t.Errorf("BlogSyncWorker field %s has the HubSpot client type", f.Name)
		}
	}
}

func TestBlogSync_FailureLogCarriesNoEmail(t *testing.T) {
	r := newBlogRig(t, ModeReal)
	v := newVendor(t, func(seenReq) (int, string) { return 422, `{"message":"bad"}` })
	r.w.Resend = NewBlogResend(v.srv.URL, "rs-key", BlogKeys{TopicID: "t", SegmentID: "s"}, nil)
	email := r.subscriber(t, 1)
	err := r.work(email, 1)
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 422 {
		t.Fatalf("Work() err = %v, want *DeliveryError{422}", err)
	}
	errs := r.logs.at(slog.LevelError)
	if len(errs) != 1 || !strings.Contains(errs[0].Text, "status=422") {
		t.Fatalf("ERROR lines = %+v, want one with status=422", errs)
	}
	local, _, _ := strings.Cut(email, "@")
	qaRequireNoLeak(t, err, []*qaLogSink{r.logs}, email, local)
}
