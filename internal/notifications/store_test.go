package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const consentText = "Allow marketing communications: ASComply Africa may email me product news and offers.  I can unsubscribe at any time. é"

type env struct {
	store *Store
	app   *pgxpool.Pool // invoice_app: what the service runs as
	admin *pgxpool.Pool // superuser: cleanup only (invoice_app cannot DELETE contacts)
}

// newEnv skips without DB env, like internal/tenancy's DB tests.
func newEnv(t *testing.T) env {
	t.Helper()
	appURL, adminURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("notifications db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	// Insert-only client: the store only enqueues.
	rc, err := river.NewClient(riverpgxv5.New(app), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	return env{store: &Store{pool: app, river: rc}, app: app, admin: admin}
}

// uniqueEmail returns a normalized address and removes its rows and jobs at cleanup.
func uniqueEmail(t *testing.T, e env, prefix string) string {
	t.Helper()
	email := fmt.Sprintf("%s-%s@corp.example", prefix, uuid.NewString()[:8])
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := e.admin.Exec(ctx, `DELETE FROM river_job WHERE kind = 'contact_deliver' AND args->>'email' = $1`, email); err != nil {
			t.Errorf("cleanup river_job: %v", err)
		}
		if _, err := e.admin.Exec(ctx, `DELETE FROM contacts WHERE email = $1`, email); err != nil {
			t.Errorf("cleanup contacts: %v", err)
		}
	})
	return email
}

type contactRow struct {
	UserID, First, Last, Company  *string
	RegisteredAt, DemoRequestedAt *time.Time
	ConsentText                   *string
	ConsentedAt                   *time.Time
	Version                       int64
	HubSpotAt, ResendAt           *time.Time
}

func readRow(t *testing.T, e env, email string) contactRow {
	t.Helper()
	var r contactRow
	err := e.app.QueryRow(context.Background(), `
		SELECT user_id::text, first_name, last_name, company, registered_at, demo_requested_at,
		       marketing_consent_text, marketing_consented_at, version, hubspot_delivered_at, resend_delivered_at
		FROM contacts WHERE email = $1`, email).Scan(
		&r.UserID, &r.First, &r.Last, &r.Company, &r.RegisteredAt, &r.DemoRequestedAt,
		&r.ConsentText, &r.ConsentedAt, &r.Version, &r.HubSpotAt, &r.ResendAt)
	if err != nil {
		t.Fatalf("read contacts row %q: %v", email, err)
	}
	return r
}

// rowCount counts rows whose normalized key is email, so a stray differently-cased row is seen too.
func rowCount(t *testing.T, e env, email string) int {
	t.Helper()
	var n int
	if err := e.app.QueryRow(context.Background(), `SELECT count(*) FROM contacts WHERE lower(btrim(email)) = $1`, email).Scan(&n); err != nil {
		t.Fatalf("count contacts: %v", err)
	}
	return n
}

type job struct {
	Queue, State string
	Args         DeliverArgs
}

func jobsFor(t *testing.T, e env, email string) []job {
	t.Helper()
	rows, err := e.app.Query(context.Background(), `
		SELECT queue, state::text, args FROM river_job
		WHERE kind = 'contact_deliver' AND args->>'email' = $1 ORDER BY id`, email)
	if err != nil {
		t.Fatalf("query river_job: %v", err)
	}
	defer rows.Close()
	var out []job
	for rows.Next() {
		var j job
		var raw []byte
		if err := rows.Scan(&j.Queue, &j.State, &raw); err != nil {
			t.Fatalf("scan river_job: %v", err)
		}
		if err := json.Unmarshal(raw, &j.Args); err != nil {
			t.Fatalf("decode job args %s: %v", raw, err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate river_job: %v", err)
	}
	return out
}

// jobKeys renders jobs as sorted "destination@version", asserting the queue on the way.
func jobKeys(t *testing.T, jobs []job) []string {
	t.Helper()
	keys := make([]string, 0, len(jobs))
	for _, j := range jobs {
		if j.Queue != "contacts" {
			t.Errorf("job queue = %q, want %q", j.Queue, "contacts")
		}
		keys = append(keys, fmt.Sprintf("%s@%d", j.Args.Destination, j.Args.Version))
	}
	slices.Sort(keys)
	return keys
}

func requireJobs(t *testing.T, e env, email string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := jobKeys(t, jobsFor(t, e, email)); !slices.Equal(got, want) {
		t.Fatalf("jobs for %s = %v, want %v", email, got, want)
	}
}

func dbNow(t *testing.T, e env) time.Time {
	t.Helper()
	var now time.Time
	if err := e.app.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("db clock: %v", err)
	}
	return now
}

func deref(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

func requireSetBetween(t *testing.T, name string, got *time.Time, lo, hi time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is NULL, want a server time", name)
	}
	if got.Before(lo) || got.After(hi) {
		t.Fatalf("%s = %v, want within [%v, %v]", name, got, lo, hi)
	}
}

func requireSameTime(t *testing.T, name string, got, want *time.Time) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("%s: got %v, want %v (both must be set)", name, got, want)
	}
	if !got.Equal(*want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func markDelivered(t *testing.T, e env, email string) {
	t.Helper()
	if _, err := e.app.Exec(context.Background(),
		`UPDATE contacts SET hubspot_delivered_at = now(), resend_delivered_at = now() WHERE email = $1`, email); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
}

func TestStore_RegistrantCreatesTaggedRowAndTwoJobs(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "reg")
	uid := uuid.NewString()

	before := dbNow(t, e)
	if err := e.store.Registrant(context.Background(), RegistrantIntake{
		UserID: uid, Email: email, DisplayName: "Ada   Lovelace King", WorkspaceName: "Analytical Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	after := dbNow(t, e)

	r := readRow(t, e, email)
	requireSetBetween(t, "registered_at", r.RegisteredAt, before, after)
	if r.DemoRequestedAt != nil || r.ConsentText != nil || r.ConsentedAt != nil {
		t.Errorf("unticked registrant row carries demo/consent facts: demo=%v consent=%q", r.DemoRequestedAt, deref(r.ConsentText))
	}
	if deref(r.First) != "Ada" || deref(r.Last) != "Lovelace King" {
		t.Errorf("names = %q / %q, want %q / %q (split on the first whitespace run)", deref(r.First), deref(r.Last), "Ada", "Lovelace King")
	}
	if deref(r.Company) != "Analytical Engines" {
		t.Errorf("company = %q, want workspace name", deref(r.Company))
	}
	if deref(r.UserID) != uid {
		t.Errorf("user_id = %q, want %q", deref(r.UserID), uid)
	}
	if r.Version != 1 || r.HubSpotAt != nil || r.ResendAt != nil {
		t.Errorf("version=%d hubspot=%v resend=%v, want 1, NULL, NULL", r.Version, r.HubSpotAt, r.ResendAt)
	}
	jobs := jobsFor(t, e, email)
	requireJobs(t, e, email, "hubspot@1", "resend@1")
	for _, j := range jobs {
		if j.State != "available" {
			t.Errorf("job %s state = %q, want available", j.Args.Destination, j.State)
		}
	}
}

func TestStore_UntickedDemoQueuesHubSpotOnly(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "demo")

	before := dbNow(t, e)
	if err := e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	after := dbNow(t, e)

	r := readRow(t, e, email)
	requireSetBetween(t, "demo_requested_at", r.DemoRequestedAt, before, after)
	if r.RegisteredAt != nil || r.ConsentText != nil || r.ConsentedAt != nil {
		t.Errorf("unticked demo row carries registered/consent facts: registered=%v consent=%q", r.RegisteredAt, deref(r.ConsentText))
	}
	if deref(r.First) != "Grace" || deref(r.Last) != "Hopper" || deref(r.Company) != "Navy" {
		t.Errorf("first/last/company = %q/%q/%q, want Grace/Hopper/Navy", deref(r.First), deref(r.Last), deref(r.Company))
	}
	requireJobs(t, e, email, "hubspot@1")
}

func TestStore_TickedDemoRecordsConsentAndQueuesBoth(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "tick")

	before := dbNow(t, e)
	if err := e.store.DemoRequest(context.Background(), DemoIntake{
		Email: email, Name: "Grace Hopper", Company: "Navy", ConsentText: consentText,
	}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	after := dbNow(t, e)

	r := readRow(t, e, email)
	if deref(r.ConsentText) != consentText {
		t.Errorf("marketing_consent_text = %q, want the text verbatim %q", deref(r.ConsentText), consentText)
	}
	requireSetBetween(t, "marketing_consented_at", r.ConsentedAt, before, after)
	requireJobs(t, e, email, "hubspot@1", "resend@1")
}

func TestStore_RegistrantThenDemoIsOneRowWithBothTags(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "ada")

	if err := e.store.Registrant(context.Background(), RegistrantIntake{
		UserID: uuid.NewString(), Email: " " + strings.ToUpper(email), DisplayName: "Ada Lovelace", WorkspaceName: "Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	if err := e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Ada Lovelace", Company: "Engines"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}

	if n := rowCount(t, e, email); n != 1 {
		t.Fatalf("rows for %s = %d, want 1", email, n)
	}
	r := readRow(t, e, email) // fails if the stored key is not the normalized email
	if r.RegisteredAt == nil || r.DemoRequestedAt == nil {
		t.Fatalf("registered_at=%v demo_requested_at=%v, want both set", r.RegisteredAt, r.DemoRequestedAt)
	}
	if r.Version != 2 {
		t.Errorf("version = %d, want 2 (the demo request added a fact)", r.Version)
	}
}

func TestStore_DemoThenRegistrantIsOneRowWithBothTags(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "ada")

	if err := e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Ada Lovelace", Company: "Engines"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	if err := e.store.Registrant(context.Background(), RegistrantIntake{
		UserID: uuid.NewString(), Email: "  " + strings.ToUpper(email) + "  ", DisplayName: "Ada Lovelace", WorkspaceName: "Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}

	if n := rowCount(t, e, email); n != 1 {
		t.Fatalf("rows for %s = %d, want 1", email, n)
	}
	r := readRow(t, e, email)
	if r.RegisteredAt == nil || r.DemoRequestedAt == nil {
		t.Fatalf("registered_at=%v demo_requested_at=%v, want both set", r.RegisteredAt, r.DemoRequestedAt)
	}
	if r.Version != 2 {
		t.Errorf("version = %d, want 2 (the registrant added a fact)", r.Version)
	}
}

func TestStore_UntickedLaterNeverClearsConsent(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "keep")
	ctx := context.Background()

	// Seeded ticked demo row, so the failure is in the merge under test.
	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, first_name, last_name, company, demo_requested_at, marketing_consent_text, marketing_consented_at)
		VALUES ($1, 'Grace', 'Hopper', 'Navy', now(), $2, now())`, email, consentText); err != nil {
		t.Fatalf("seed ticked row: %v", err)
	}
	first := readRow(t, e, email)

	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email}); err != nil {
		t.Fatalf("unticked Registrant: %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("unticked DemoRequest: %v", err)
	}

	r := readRow(t, e, email)
	if r.RegisteredAt == nil {
		t.Fatal("registered_at is NULL: the unticked registrant did not merge into the row")
	}
	if deref(r.ConsentText) != consentText {
		t.Errorf("consent text = %q, want unchanged %q", deref(r.ConsentText), consentText)
	}
	requireSameTime(t, "marketing_consented_at", r.ConsentedAt, first.ConsentedAt)
	if deref(r.First) != "Grace" {
		t.Errorf("first_name = %q, want %q kept (blank intake names never overwrite)", deref(r.First), "Grace")
	}
}

func TestStore_SecondTickKeepsFirstConsent(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "twice")
	ctx := context.Background()

	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, demo_requested_at, marketing_consent_text, marketing_consented_at)
		VALUES ($1, now(), $2, now() - interval '1 day')`, email, consentText); err != nil {
		t.Fatalf("seed ticked row: %v", err)
	}
	first := readRow(t, e, email)

	laterAt := first.ConsentedAt.Add(time.Hour).UTC().Truncate(time.Microsecond)
	if err := e.store.Registrant(ctx, RegistrantIntake{
		UserID: uuid.NewString(), Email: email, ConsentText: "second text via registrant", ConsentAt: laterAt,
	}); err != nil {
		t.Fatalf("second tick (registrant): %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{
		Email: email, Name: "Grace Hopper", Company: "Navy", ConsentText: "third text via demo",
	}); err != nil {
		t.Fatalf("third tick (demo): %v", err)
	}

	r := readRow(t, e, email)
	if r.RegisteredAt == nil {
		t.Fatal("registered_at is NULL: the registrant did not merge into the row")
	}
	if deref(r.ConsentText) != consentText {
		t.Errorf("consent text = %q, want the first %q", deref(r.ConsentText), consentText)
	}
	requireSameTime(t, "marketing_consented_at", r.ConsentedAt, first.ConsentedAt)
}

func TestStore_RepeatWithoutNewFactQueuesNothing(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "same")
	ctx := context.Background()

	// Seeded delivered row: every fact of the repeat is already set.
	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, first_name, last_name, company, registered_at, hubspot_delivered_at, resend_delivered_at)
		VALUES ($1, 'Ada', 'Lovelace', 'Engines', now(), now(), now())`, email); err != nil {
		t.Fatalf("seed delivered row: %v", err)
	}
	before := readRow(t, e, email)

	if err := e.store.Registrant(ctx, RegistrantIntake{
		UserID: uuid.NewString(), Email: email, DisplayName: "Ada Byron", WorkspaceName: "Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}

	after := readRow(t, e, email)
	// Positive pair: the merge ran (a changed name lands), yet it queued and re-delivered nothing.
	if deref(after.Last) != "Byron" {
		t.Errorf("last_name = %q, want %q: the repeat intake was not merged", deref(after.Last), "Byron")
	}
	if after.Version != 1 {
		t.Errorf("version = %d, want 1 (no new fact)", after.Version)
	}
	requireSameTime(t, "hubspot_delivered_at", after.HubSpotAt, before.HubSpotAt)
	requireSameTime(t, "resend_delivered_at", after.ResendAt, before.ResendAt)
	requireJobs(t, e, email) // none: both destinations are delivered
}

func TestStore_RepeatRequeuesAnUndeliveredDestination(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "gone")
	ctx := context.Background()

	// HubSpot undelivered and no job left; Resend delivered.
	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, first_name, last_name, company, registered_at, resend_delivered_at)
		VALUES ($1, 'Ada', 'Lovelace', 'Engines', now(), now())`, email); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	requireJobs(t, e, email)

	if err := e.store.Registrant(ctx, RegistrantIntake{
		UserID: uuid.NewString(), Email: email, DisplayName: "Ada Lovelace", WorkspaceName: "Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}

	requireJobs(t, e, email, "hubspot@1")
	if r := readRow(t, e, email); r.Version != 1 || r.ResendAt == nil {
		t.Errorf("version=%d resend_delivered_at=%v, want 1 and still set", r.Version, r.ResendAt)
	}
}

func TestStore_NewFactBumpsVersionAndClearsDelivery(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "bump")
	ctx := context.Background()

	if err := e.store.Registrant(ctx, RegistrantIntake{
		UserID: uuid.NewString(), Email: email, DisplayName: "Ada Lovelace", WorkspaceName: "Engines",
	}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	markDelivered(t, e, email)

	if err := e.store.DemoRequest(ctx, DemoIntake{
		Email: email, Name: "Ada Lovelace", Company: "Engines", ConsentText: consentText,
	}); err != nil {
		t.Fatalf("ticked DemoRequest: %v", err)
	}

	r := readRow(t, e, email)
	if r.Version != 2 {
		t.Errorf("version = %d, want 2", r.Version)
	}
	if r.HubSpotAt != nil || r.ResendAt != nil {
		t.Errorf("hubspot=%v resend=%v, want both NULL after a new fact", r.HubSpotAt, r.ResendAt)
	}
	// The version-1 jobs are still queued: a version-2 job must not dedupe against them.
	requireJobs(t, e, email, "hubspot@1", "resend@1", "hubspot@2", "resend@2")
}

func TestStore_MeReadsOwnRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	both := uniqueEmail(t, e, "me")
	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at, version)
		VALUES ($1, now(), now(), 'ticked', now(), 3)`, both); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	c, err := e.store.Me(ctx, both)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if want := []string{"registered", "demo request"}; !slices.Equal(c.Tags, want) {
		t.Errorf("tags = %v, want %v", c.Tags, want)
	}
	if !c.MarketingEligible || !c.ResendApplies {
		t.Errorf("marketing_eligible=%v resend_applies=%v, want true, true", c.MarketingEligible, c.ResendApplies)
	}
	if c.HubSpotDeliveredAt != nil || c.ResendDeliveredAt != nil || c.Mode != nil {
		t.Errorf("delivery state = %v %v mode %v, want all NULL before any delivery", c.HubSpotDeliveredAt, c.ResendDeliveredAt, c.Mode)
	}

	if _, err := e.app.Exec(ctx, `UPDATE contacts SET hubspot_delivered_at = now(), delivery_mode = 'fake' WHERE email = $1`, both); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered := readRow(t, e, both)
	c, err = e.store.Me(ctx, both)
	if err != nil {
		t.Fatalf("Me after delivery: %v", err)
	}
	requireSameTime(t, "hubspot delivered_at", c.HubSpotDeliveredAt, delivered.HubSpotAt)
	if c.ResendDeliveredAt != nil {
		t.Errorf("resend delivered_at = %v, want NULL", c.ResendDeliveredAt)
	}
	if c.Mode == nil || *c.Mode != "fake" {
		t.Errorf("mode = %v, want fake", c.Mode)
	}

	// Demo booker without the tick: one tag, no Resend.
	demoOnly := uniqueEmail(t, e, "me-demo")
	if _, err := e.app.Exec(ctx, `INSERT INTO contacts (email, demo_requested_at) VALUES ($1, now())`, demoOnly); err != nil {
		t.Fatalf("seed demo-only row: %v", err)
	}
	c, err = e.store.Me(ctx, demoOnly)
	if err != nil {
		t.Fatalf("Me demo-only: %v", err)
	}
	if want := []string{"demo request"}; !slices.Equal(c.Tags, want) {
		t.Errorf("demo-only tags = %v, want %v", c.Tags, want)
	}
	if c.MarketingEligible || c.ResendApplies {
		t.Errorf("demo-only marketing_eligible=%v resend_applies=%v, want false, false", c.MarketingEligible, c.ResendApplies)
	}
}

func TestStore_MeUnknownEmailIsNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	known := uniqueEmail(t, e, "known")
	if _, err := e.app.Exec(ctx, `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`, known); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	// Positive pair: a known email resolves, so ErrNotFound below is not "always".
	if c, err := e.store.Me(ctx, known); err != nil || c.Email != known {
		t.Fatalf("Me(known) = %+v, %v; want the row for %s", c, err, known)
	}

	_, err := e.store.Me(ctx, uniqueEmail(t, e, "unknown"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Me(unknown) error = %v, want ErrNotFound", err)
	}
}
