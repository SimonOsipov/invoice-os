package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
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
	appCfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatalf("app pool config: %v", err)
	}
	// Each held delivery attempt pins two connections (River's test tx and the worker's locked tx); the default is max(4, NumCPU).
	appCfg.MaxConns = 16
	app, err := pgxpool.NewWithConfig(ctx, appCfg)
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
		if _, err := e.admin.Exec(ctx, `DELETE FROM river_job WHERE kind IN ('contact_deliver', 'demo_deal') AND args->>'email' = $1`, email); err != nil {
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

// dealJobsFor lists the demo_deal jobs queued for email, oldest first.
func dealJobsFor(t *testing.T, e env, email string) []DemoDealArgs {
	t.Helper()
	rows, err := e.app.Query(context.Background(), `
		SELECT args FROM river_job WHERE kind = 'demo_deal' AND args->>'email' = $1 ORDER BY id`, email)
	if err != nil {
		t.Fatalf("query demo_deal jobs: %v", err)
	}
	defer rows.Close()
	var out []DemoDealArgs
	for rows.Next() {
		var raw []byte
		var a DemoDealArgs
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan demo_deal job: %v", err)
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("decode demo_deal args %s: %v", raw, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read demo_deal jobs: %v", err)
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

	// Different offsets so a swapped column cannot pass.
	if _, err := e.app.Exec(ctx, `UPDATE contacts SET hubspot_delivered_at = now() - interval '2 hours',
		resend_delivered_at = now() - interval '1 hour', delivery_mode = 'fake' WHERE email = $1`, both); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered := readRow(t, e, both)
	c, err = e.store.Me(ctx, both)
	if err != nil {
		t.Fatalf("Me after delivery: %v", err)
	}
	requireSameTime(t, "hubspot delivered_at", c.HubSpotDeliveredAt, delivered.HubSpotAt)
	requireSameTime(t, "resend delivered_at", c.ResendDeliveredAt, delivered.ResendAt)
	if c.Mode == nil || *c.Mode != "fake" {
		t.Errorf("mode = %v, want fake", c.Mode)
	}

	// Case and surrounding spaces resolve to the same row, and the email comes back normalized.
	c, err = e.store.Me(ctx, "  "+strings.ToUpper(both)+" ")
	if err != nil {
		t.Fatalf("Me with case/space variant: %v", err)
	}
	if c.Email != both || !slices.Equal(c.Tags, []string{"registered", "demo request"}) {
		t.Errorf("Me variant = %q %v, want %q with both tags", c.Email, c.Tags, both)
	}

	// Registered without the tick: Resend applies, marketing eligibility does not.
	regOnly := uniqueEmail(t, e, "me-reg")
	if _, err := e.app.Exec(ctx, `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`, regOnly); err != nil {
		t.Fatalf("seed registered-only row: %v", err)
	}
	c, err = e.store.Me(ctx, regOnly)
	if err != nil {
		t.Fatalf("Me registered-only: %v", err)
	}
	if want := []string{"registered"}; !slices.Equal(c.Tags, want) {
		t.Errorf("registered-only tags = %v, want %v", c.Tags, want)
	}
	if c.MarketingEligible || !c.ResendApplies {
		t.Errorf("registered-only marketing_eligible=%v resend_applies=%v, want false, true", c.MarketingEligible, c.ResendApplies)
	}

	// Demo booker with the tick: one tag, marketing-eligible, Resend applies.
	tickedDemo := uniqueEmail(t, e, "me-tick")
	if _, err := e.app.Exec(ctx, `INSERT INTO contacts (email, demo_requested_at, marketing_consent_text, marketing_consented_at)
		VALUES ($1, now(), 'ticked', now())`, tickedDemo); err != nil {
		t.Fatalf("seed ticked demo row: %v", err)
	}
	c, err = e.store.Me(ctx, tickedDemo)
	if err != nil {
		t.Fatalf("Me ticked demo: %v", err)
	}
	if want := []string{"demo request"}; !slices.Equal(c.Tags, want) {
		t.Errorf("ticked-demo tags = %v, want %v", c.Tags, want)
	}
	if !c.MarketingEligible || !c.ResendApplies {
		t.Errorf("ticked-demo marketing_eligible=%v resend_applies=%v, want true, true", c.MarketingEligible, c.ResendApplies)
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

func TestStore_RegistrantConsentTextAndTime(t *testing.T) {
	ctx := context.Background()

	t.Run("text with a time stores both", func(t *testing.T) {
		e := newEnv(t)
		email := uniqueEmail(t, e, "regtick")
		at := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
		if err := e.store.Registrant(ctx, RegistrantIntake{
			UserID: uuid.NewString(), Email: email, ConsentText: consentText, ConsentAt: at,
		}); err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		r := readRow(t, e, email)
		if deref(r.ConsentText) != consentText {
			t.Errorf("consent text = %q, want %q", deref(r.ConsentText), consentText)
		}
		requireSameTime(t, "marketing_consented_at", r.ConsentedAt, &at)
		requireJobs(t, e, email, "hubspot@1", "resend@1")
	})

	t.Run("text without a time takes the server time", func(t *testing.T) {
		e := newEnv(t)
		email := uniqueEmail(t, e, "regnotime")
		before := dbNow(t, e)
		if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, ConsentText: consentText}); err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		after := dbNow(t, e)
		r := readRow(t, e, email)
		if deref(r.ConsentText) != consentText {
			t.Errorf("consent text = %q, want %q", deref(r.ConsentText), consentText)
		}
		requireSetBetween(t, "marketing_consented_at", r.ConsentedAt, before, after)
	})

	t.Run("a time without text is unticked", func(t *testing.T) {
		e := newEnv(t)
		email := uniqueEmail(t, e, "regnotext")
		at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
		if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, ConsentAt: at}); err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		r := readRow(t, e, email)
		if r.RegisteredAt == nil {
			t.Fatal("registered_at is NULL: the intake did not land")
		}
		if r.ConsentText != nil || r.ConsentedAt != nil {
			t.Errorf("consent = %q at %v, want none: a time alone is not a tick", deref(r.ConsentText), r.ConsentedAt)
		}
		c, err := e.store.Me(ctx, email)
		if err != nil || c.MarketingEligible {
			t.Errorf("Me = %+v, %v; want not marketing-eligible", c, err)
		}
	})
}

func TestStore_UntickedDemoNeverGetsAResendJob(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "nodemo")
	ctx := context.Background()

	// Repeats, case and space variants, a delivered row: Resend stays out.
	for _, form := range []string{email, strings.ToUpper(email), "  " + email + " ", email} {
		if err := e.store.DemoRequest(ctx, DemoIntake{Email: form, Name: "Grace Hopper", Company: "Navy"}); err != nil {
			t.Fatalf("unticked DemoRequest(%q): %v", form, err)
		}
	}
	if n := rowCount(t, e, email); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	requireJobs(t, e, email, "hubspot@1")
	if _, err := e.app.Exec(ctx, `UPDATE contacts SET hubspot_delivered_at = now() WHERE email = $1`, email); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("repeat after delivery: %v", err)
	}
	requireJobs(t, e, email, "hubspot@1")
	if r := readRow(t, e, email); r.Version != 1 || r.ConsentText != nil || r.ConsentedAt != nil {
		t.Errorf("version=%d consent=%q, want 1 and none", r.Version, deref(r.ConsentText))
	}

	// Positive pair: the tick is what brings Resend in.
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy", ConsentText: consentText}); err != nil {
		t.Fatalf("ticked DemoRequest: %v", err)
	}
	if r := readRow(t, e, email); r.Version != 2 || r.HubSpotAt != nil {
		t.Errorf("version=%d hubspot=%v, want 2 and NULL: a tick alone is a new fact", r.Version, r.HubSpotAt)
	}
	requireJobs(t, e, email, "hubspot@1", "hubspot@2", "resend@2")
}

func TestStore_RepeatOfEachKindAddsNoFact(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		intake   func(s *Store, email string) error
		wantJobs []string
	}{
		{"registrant", func(s *Store, email string) error {
			return s.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email})
		}, []string{"hubspot@1", "resend@1"}},
		{"ticked registrant", func(s *Store, email string) error {
			return s.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, ConsentText: consentText})
		}, []string{"hubspot@1", "resend@1"}},
		{"unticked demo", func(s *Store, email string) error {
			return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"})
		}, []string{"hubspot@1"}},
		{"ticked demo", func(s *Store, email string) error {
			return s.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy", ConsentText: consentText})
		}, []string{"hubspot@1", "resend@1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			email := uniqueEmail(t, e, "rep")
			if err := tc.intake(e.store, email); err != nil {
				t.Fatalf("first intake: %v", err)
			}
			requireJobs(t, e, email, tc.wantJobs...)
			if _, err := e.app.Exec(ctx, `UPDATE contacts SET hubspot_delivered_at = now() - interval '2 hours',
				resend_delivered_at = CASE WHEN registered_at IS NOT NULL OR marketing_consented_at IS NOT NULL
				                           THEN now() - interval '1 hour' END WHERE email = $1`, email); err != nil {
				t.Fatalf("mark delivered: %v", err)
			}
			before := readRow(t, e, email)

			if err := tc.intake(e.store, strings.ToUpper(email)); err != nil {
				t.Fatalf("repeat intake: %v", err)
			}
			after := readRow(t, e, email)
			if after.Version != before.Version {
				t.Errorf("version = %d, want %d (no new fact)", after.Version, before.Version)
			}
			requireSameTime(t, "hubspot_delivered_at", after.HubSpotAt, before.HubSpotAt)
			if before.ResendAt != nil {
				requireSameTime(t, "resend_delivered_at", after.ResendAt, before.ResendAt)
			}
			requireJobs(t, e, email, tc.wantJobs...)
		})
	}
}

func TestStore_RepeatWhileJobLiveQueuesNoDuplicate(t *testing.T) {
	ctx := context.Background()
	intake := func(e env, email string) error {
		return e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, DisplayName: "Ada Lovelace"})
	}

	// Each state still counts as in flight: a repeat finds the job and queues nothing.
	for _, state := range []string{"available", "retryable", "scheduled", "running", "pending"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			email := uniqueEmail(t, e, "live")
			if err := intake(e, email); err != nil {
				t.Fatalf("first intake: %v", err)
			}
			requireJobs(t, e, email, "hubspot@1", "resend@1")
			if _, err := e.admin.Exec(ctx, `UPDATE river_job SET state = $2::river_job_state WHERE kind = 'contact_deliver' AND args->>'email' = $1`, email, state); err != nil {
				t.Fatalf("set job state %s: %v", state, err)
			}
			if err := intake(e, strings.ToUpper(email)); err != nil {
				t.Fatalf("repeat intake: %v", err)
			}
			requireJobs(t, e, email, "hubspot@1", "resend@1")
		})
	}

	// A completed job never blocks: its delivery time is still NULL, so the repeat queues it again.
	t.Run("completed", func(t *testing.T) {
		e := newEnv(t)
		email := uniqueEmail(t, e, "done")
		if err := intake(e, email); err != nil {
			t.Fatalf("first intake: %v", err)
		}
		if _, err := e.admin.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'contact_deliver' AND args->>'email' = $1`, email); err != nil {
			t.Fatalf("complete jobs: %v", err)
		}
		if err := intake(e, email); err != nil {
			t.Fatalf("repeat intake: %v", err)
		}
		requireJobs(t, e, email, "hubspot@1", "hubspot@1", "resend@1", "resend@1")
	})
}

func TestStore_ConcurrentIntakesForOneEmailLoseNoFact(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for round := 0; round < 5; round++ {
		email := uniqueEmail(t, e, "race")
		intakes := []func() error{
			func() error {
				return e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, DisplayName: "Ada Lovelace"})
			},
			func() error {
				return e.store.DemoRequest(ctx, DemoIntake{Email: strings.ToUpper(email), Name: "Ada Lovelace", Company: "Engines"})
			},
			func() error {
				return e.store.DemoRequest(ctx, DemoIntake{Email: " " + email + " ", Name: "Ada Lovelace", Company: "Engines", ConsentText: consentText})
			},
			func() error {
				return e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, ConsentText: "registrant tick"})
			},
			func() error {
				return e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Ada Lovelace", Company: "Engines"})
			},
			func() error {
				return e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email})
			},
		}
		errs := make([]error, len(intakes))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, fn := range intakes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = fn()
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d intake %d: %v", round, i, err)
			}
		}

		if n := rowCount(t, e, email); n != 1 {
			t.Fatalf("round %d: rows = %d, want 1", round, n)
		}
		r := readRow(t, e, email)
		if r.RegisteredAt == nil || r.DemoRequestedAt == nil || r.ConsentedAt == nil {
			t.Fatalf("round %d: registered=%v demo=%v consented=%v, want every fact set", round, r.RegisteredAt, r.DemoRequestedAt, r.ConsentedAt)
		}
		if got := deref(r.ConsentText); got != consentText && got != "registrant tick" {
			t.Errorf("round %d: consent text = %q, want one of the ticked texts", round, got)
		}
		if r.Version < 2 {
			t.Errorf("round %d: version = %d, want at least 2 (facts arrived from both intakes)", round, r.Version)
		}
		// The jobs at the final version exist for both destinations, and none is queued twice.
		keys := jobKeys(t, jobsFor(t, e, email))
		for _, dest := range []string{destHubSpot, destResend} {
			if !slices.Contains(keys, fmt.Sprintf("%s@%d", dest, r.Version)) {
				t.Errorf("round %d: jobs %v lack %s@%d", round, keys, dest, r.Version)
			}
		}
		for i := 1; i < len(keys); i++ {
			if keys[i] == keys[i-1] {
				t.Errorf("round %d: job %s queued twice in %v", round, keys[i], keys)
			}
		}
	}
}

func TestStore_FailedQueueRollsBackTheRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail(t, e, "atomic")

	// A River client pointed at a schema without tables makes InsertTx fail after the merge ran.
	rc, err := river.NewClient(riverpgxv5.New(e.app), &river.Config{Schema: "no_such_river_schema"})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	broken := &Store{pool: e.app, river: rc}
	if err := broken.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email}); err == nil {
		t.Fatal("Registrant with a failing queue returned nil")
	}
	if n := rowCount(t, e, email); n != 0 {
		t.Fatalf("rows after a failed intake = %d, want 0 (merge and queue are one transaction)", n)
	}

	// Positive pair: the same intake lands through a working store.
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	if n := rowCount(t, e, email); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestStore_RefusesAnEmailTheKeyRuleRejects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, bad := range []string{"", "   ", "a@", strings.Repeat("a", 250) + "@b.cd"} {
		if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: bad}); err == nil {
			t.Errorf("Registrant(%q) returned nil, want an error", bad)
		}
		if err := e.store.DemoRequest(ctx, DemoIntake{Email: bad, Name: "x", Company: "y"}); err == nil {
			t.Errorf("DemoRequest(%q) returned nil, want an error", bad)
		}
	}
}

func TestStore_NamesMergeAndKeepFirstUserID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail(t, e, "names")

	// A blank name never blocks a later real name; the last intake is blank and overwrites nothing.
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: " \t ", Company: ""}); err != nil {
		t.Fatalf("blank DemoRequest: %v", err)
	}
	first, second := uuid.NewString(), uuid.NewString()
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: first, Email: email, DisplayName: "Ada Lovelace", WorkspaceName: "Engines"}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: second, Email: email, DisplayName: "Ada Lovelace"}); err != nil {
		t.Fatalf("second Registrant: %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "  ", Company: ""}); err != nil {
		t.Fatalf("blank DemoRequest again: %v", err)
	}
	r := readRow(t, e, email)
	if deref(r.First) != "Ada" || deref(r.Last) != "Lovelace" {
		t.Errorf("names = %q / %q, want Ada / Lovelace", deref(r.First), deref(r.Last))
	}
	if deref(r.Company) != "Engines" {
		t.Errorf("company = %q, want %q kept (a blank company never overwrites)", deref(r.Company), "Engines")
	}
	if deref(r.UserID) != first {
		t.Errorf("user_id = %q, want the first non-null %q", deref(r.UserID), first)
	}

	// Long multi-byte names survive intact.
	longEmail := uniqueEmail(t, e, "long")
	lf, ll := strings.Repeat("é", 1500), strings.Repeat("ü", 1500)
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: longEmail, DisplayName: lf + " " + ll}); err != nil {
		t.Fatalf("long Registrant: %v", err)
	}
	if lr := readRow(t, e, longEmail); deref(lr.First) != lf || deref(lr.Last) != ll {
		t.Errorf("long names were altered: first %d runes, last %d runes", len([]rune(deref(lr.First))), len([]rune(deref(lr.Last))))
	}
}

func TestStore_WhitespaceCompanyNeverOverwritesACompany(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail(t, e, "company")

	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "  \t "}); err != nil {
		t.Fatalf("whitespace-company DemoRequest: %v", err)
	}
	if r := readRow(t, e, email); deref(r.Company) != "Navy" {
		t.Errorf("company = %q, want %q kept: a whitespace-only company is blank, as a whitespace-only name is", deref(r.Company), "Navy")
	}

	// The registrant path trims its workspace name the same way.
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, WorkspaceName: " \t "}); err != nil {
		t.Fatalf("whitespace-workspace Registrant: %v", err)
	}
	r := readRow(t, e, email)
	if r.RegisteredAt == nil {
		t.Fatal("registered_at is NULL: the registrant did not merge into the row")
	}
	if deref(r.Company) != "Navy" {
		t.Errorf("company = %q after a whitespace workspace name, want %q kept", deref(r.Company), "Navy")
	}
}

func TestSplitName(t *testing.T) {
	for _, tc := range []struct{ in, first, last string }{
		{"Ada Lovelace", "Ada", "Lovelace"},
		{"  Ada \t Lovelace  King  ", "Ada", "Lovelace  King"},
		{"Ada\u00a0Lovelace", "Ada", "Lovelace"},
		{"Madonna", "Madonna", ""},
		{"   ", "", ""},
		{"", "", ""},
	} {
		if first, last := splitName(tc.in); first != tc.first || last != tc.last {
			t.Errorf("splitName(%q) = %q, %q; want %q, %q", tc.in, first, last, tc.first, tc.last)
		}
	}
}

func TestStore_DemoRequestQueuesADealJob(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "deal")
	if err := e.store.DemoRequest(context.Background(), DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	var queue string
	if err := e.app.QueryRow(context.Background(), `SELECT queue FROM river_job WHERE kind = 'demo_deal' AND args->>'email' = $1`, email).Scan(&queue); err != nil {
		t.Fatalf("read the demo_deal job: %v", err)
	}
	if queue != QueueContacts {
		t.Errorf("demo_deal queue = %q, want %q", queue, QueueContacts)
	}
	want := []DemoDealArgs{{Email: email, Name: "Grace Hopper", Company: "Navy"}}
	if got := dealJobsFor(t, e, email); !slices.Equal(got, want) {
		t.Errorf("demo_deal jobs = %+v, want %+v", got, want)
	}
	requireJobs(t, e, email, "hubspot@1")
}

func TestStore_RepeatDemoRequestQueuesAnotherDealJob(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "dealrep")
	ctx := context.Background()
	if _, err := e.app.Exec(ctx, `
		INSERT INTO contacts (email, first_name, last_name, company, demo_requested_at, hubspot_delivered_at)
		VALUES ($1, 'Grace', 'Hopper', 'Navy', now(), now())`, email); err != nil {
		t.Fatalf("seed delivered row: %v", err)
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	requireJobs(t, e, email) // no new fact: no contact job
	if got := dealJobsFor(t, e, email); len(got) != 1 {
		t.Fatalf("demo_deal jobs after one repeat = %d, want 1", len(got))
	}
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	if got := dealJobsFor(t, e, email); len(got) != 2 {
		t.Fatalf("demo_deal jobs after two repeats = %d, want 2", len(got))
	}
}

func TestStore_DemoRequestsOfOneContactKeepEachRequestsOwnValues(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "dealown")
	ctx := context.Background()
	for _, c := range []string{"A", "B"} {
		if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: c}); err != nil {
			t.Fatalf("DemoRequest %s: %v", c, err)
		}
	}
	got := dealJobsFor(t, e, email)
	if len(got) != 2 || got[0].Company != "A" || got[1].Company != "B" {
		t.Errorf("demo_deal jobs = %+v, want companies A then B", got)
	}
	if c := deref(readRow(t, e, email).Company); c != "B" {
		t.Errorf("merged company = %q, want B (a later non-empty company overwrites)", c)
	}
}

func TestStore_DemoDealInsertFailureRollsBackTheIntake(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail(t, e, "dealfail")
	fn := "fail_demo_deal_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := e.admin.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.kind = 'demo_deal' AND NEW.args->>'email' = '%[2]s' THEN RAISE EXCEPTION 'test: refuse demo_deal'; END IF;
			RETURN NEW;
		END $$`, fn, email)); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+fn+` ON river_job`)
		_, _ = e.admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	if _, err := e.admin.Exec(ctx, `CREATE TRIGGER `+fn+` BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err == nil {
		t.Fatal("DemoRequest with a failing deal insert returned nil")
	}
	if n := rowCount(t, e, email); n != 0 {
		t.Errorf("rows after a failed intake = %d, want 0", n)
	}
	requireJobs(t, e, email)
}

func TestStore_RegistrantQueuesNoDealJob(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "regnodeal")
	if err := e.store.Registrant(context.Background(), RegistrantIntake{UserID: uuid.NewString(), Email: email, DisplayName: "Ada Lovelace"}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	if got := dealJobsFor(t, e, email); len(got) != 0 {
		t.Errorf("demo_deal jobs after a registration = %+v, want none", got)
	}
}

func TestStore_RegistrantAfterDemoQueuesNoSecondDealJob(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "demothenreg")
	ctx := context.Background()
	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	if err := e.store.Registrant(ctx, RegistrantIntake{UserID: uuid.NewString(), Email: email, DisplayName: "Grace Hopper"}); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	if got := dealJobsFor(t, e, email); len(got) != 1 {
		t.Errorf("demo_deal jobs = %d, want exactly 1", len(got))
	}
}

func TestStore_DemoDealJobArgsAreTheNormalisedRequest(t *testing.T) {
	e := newEnv(t)
	email := uniqueEmail(t, e, "dealnorm")
	if err := e.store.DemoRequest(context.Background(), DemoIntake{Email: "  " + strings.ToUpper(email) + " ", Name: "  Grace Hopper  ", Company: "  Navy  "}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}
	want := []DemoDealArgs{{Email: email, Name: "Grace Hopper", Company: "Navy"}}
	if got := dealJobsFor(t, e, email); !slices.Equal(got, want) {
		t.Errorf("demo_deal jobs = %+v, want %+v", got, want)
	}
}

func TestStore_DemoRequestCommitFailureLeavesNoDealJob(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail(t, e, "dealcommit")
	fn := "fail_commit_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := e.admin.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.email = '%[2]s' THEN RAISE EXCEPTION 'test: refuse at commit'; END IF;
			RETURN NULL;
		END $$`, fn, email)); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.admin.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+fn+` ON contacts`)
		_, _ = e.admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	if _, err := e.admin.Exec(ctx, `CREATE CONSTRAINT TRIGGER `+fn+` AFTER INSERT ON contacts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("create deferred trigger: %v", err)
	}

	if err := e.store.DemoRequest(ctx, DemoIntake{Email: email, Name: "Grace Hopper", Company: "Navy"}); err == nil {
		t.Fatal("DemoRequest with a failing commit returned nil")
	}
	if n := rowCount(t, e, email); n != 0 {
		t.Errorf("rows after a failed commit = %d, want 0", n)
	}
	if got := dealJobsFor(t, e, email); len(got) != 0 {
		t.Errorf("demo_deal jobs after a failed commit = %+v, want none (the insert must share the intake's transaction)", got)
	}
}
