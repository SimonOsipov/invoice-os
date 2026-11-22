package erp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store.Complete is driven as the worker drives it: no identity in the context, the tenant read from
// the state. The contract these tests pin on the Store (unexported, so no caller sets them):
//   - newGrantID func() string mints the grant id before the secret is created (nil means uuid.NewString);
//   - secretScope is the scope of SecretName (empty in production and locally);
//   - Complete logs through slog.Default().
// Complete returns its outcome as a string-kinded type; err is unconstrained when the outcome is failed.

// --- fixtures ---------------------------------------------------------------------------

// exchangeSpy wraps the fake connector: it counts Exchange calls, remembers what the vendor returned
// and lets a test replace the exchange.
type exchangeSpy struct {
	*Fake
	mu       sync.Mutex
	calls    int
	last     ExchangeResult
	override func(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
}

func (s *exchangeSpy) Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
	s.mu.Lock()
	s.calls++
	fn := s.override
	s.mu.Unlock()
	var res ExchangeResult
	var err error
	if fn != nil {
		res, err = fn(ctx, req)
	} else {
		res, err = s.Fake.Exchange(ctx, req)
	}
	if err == nil {
		s.mu.Lock()
		s.last = res
		s.mu.Unlock()
	}
	return res, err
}

func (s *exchangeSpy) exchanged() (calls int, last ExchangeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.last
}

// secretSpy is a memory store that records every create and delete and can fail either.
type secretSpy struct {
	*MemorySecrets
	mu                   sync.Mutex
	createErr, deleteErr error
	created, deleted     []string
}

func (s *secretSpy) Create(ctx context.Context, id string, payload []byte) error {
	s.mu.Lock()
	failure := s.createErr
	s.mu.Unlock()
	if failure != nil {
		return failure
	}
	err := s.MemorySecrets.Create(ctx, id, payload)
	if err == nil {
		s.mu.Lock()
		s.created = append(s.created, id)
		s.mu.Unlock()
	}
	return err
}

func (s *secretSpy) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, id)
	failure := s.deleteErr
	s.mu.Unlock()
	if failure != nil {
		return failure
	}
	return s.MemorySecrets.Delete(ctx, id)
}

func (s *secretSpy) failCreate(err error) { s.mu.Lock(); s.createErr = err; s.mu.Unlock() }
func (s *secretSpy) failDelete(err error) { s.mu.Lock(); s.deleteErr = err; s.mu.Unlock() }

func (s *secretSpy) counts() (created, deleted int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created), len(s.deleted)
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logBuf) errorRecords() int {
	n := 0
	for _, line := range strings.Split(l.text(), "\n") {
		if strings.Contains(line, "\"level\":\"ERROR\"") {
			n++
		}
	}
	return n
}

// captureLogs routes slog.Default() into a buffer for the test; no erp test runs in parallel.
func captureLogs(t *testing.T) *logBuf {
	t.Helper()
	buf := &logBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

type completeRig struct {
	super, app *pgxpool.Pool
	tenant     string
	admin      context.Context
	e1         string
	ex         *exchangeSpy
	sec        *secretSpy
	store      *Store
}

func newCompleteRig(t *testing.T) *completeRig {
	t.Helper()
	super, app := storePools(t)
	tenant := seedTenant(t, super, "firm")
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), "DELETE FROM river_job WHERE args->>'tenant_id' = $1", tenant)
	})
	ex := &exchangeSpy{Fake: NewFake(testRedirect)}
	sec := &secretSpy{MemorySecrets: NewMemorySecrets()}
	return &completeRig{
		super: super, app: app, tenant: tenant, ex: ex, sec: sec,
		admin: seedMember(t, super, tenant, "admin", "active", true),
		e1:    seedClient(t, super, tenant, "Client One"),
		store: storeOver(t, app, sec, ex),
	}
}

func (r *completeRig) client(t *testing.T, name string) string {
	return seedClient(t, r.super, r.tenant, name)
}

// connect starts a consent on the client's keyless pending row and returns the row and the state.
func (r *completeRig) connect(t *testing.T, entity string) (Connection, string) {
	t.Helper()
	conn, consent, _, err := r.store.Connect(r.admin, entity, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return conn, consentState(t, consent)
}

func (r *completeRig) connectByID(t *testing.T, entity, id string) string {
	t.Helper()
	_, consent, _, err := r.store.Connect(r.admin, entity, ConnectInput{System: "fake", ConnectionID: id})
	if err != nil {
		t.Fatalf("Connect by id: %v", err)
	}
	return consentState(t, consent)
}

func (r *completeRig) complete(state, code, vendorError string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := r.store.Complete(ctx, CallbackInput{State: state, Code: code, Error: vendorError})
	return string(out), err
}

// wantOutcome: every outcome but failed is an answer, not an error.
func wantOutcome(t *testing.T, label, got string, err error, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: outcome = %q (err %v), want %q", label, got, err, want)
		return
	}
	if want != "failed" && err != nil {
		t.Errorf("%s: outcome %q came with err %v, want nil", label, want, err)
	}
}

func (r *completeRig) mustComplete(t *testing.T, label, state, code string) {
	t.Helper()
	got, err := r.complete(state, code, "")
	wantOutcome(t, label, got, err, "connected")
}

// connectActive connects a client and completes the callback, returning the active connection and its grant.
func connectActive(t *testing.T, store *Store, admin context.Context, super *pgxpool.Pool, entity, code string) (connID, grantID string) {
	t.Helper()
	conn, consent, _, err := store.Connect(admin, entity, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	out, err := store.Complete(context.Background(), CallbackInput{State: consentState(t, consent), Code: code})
	if err != nil || string(out) != "connected" {
		t.Fatalf("Complete = %q, %v, want connected", out, err)
	}
	s := stateOf(t, super, conn.ID)
	if s.status != "active" || s.grant == nil {
		t.Fatalf("connection after Complete = %+v, want active with a grant", s)
	}
	return conn.ID, *s.grant
}

// ercRows is the tenant's connections, grants, queued ERP jobs and ERP audit count, without the OAuth
// states: "changes nothing but the state's used_at" compares it.
func ercRows(t *testing.T, super *pgxpool.Pool, tenant string) string {
	t.Helper()
	var conns, grants string
	mustScan(t, super,
		"SELECT coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM erp_connections c WHERE c.tenant_id = $1), '[]')::text,"+
			" coalesce((SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM erp_grants g WHERE g.tenant_id = $1), '[]')::text",
		[]any{tenant}, &conns, &grants)
	jobs := countOf(t, super, "SELECT count(*) FROM river_job WHERE kind LIKE 'erp%' AND args->>'tenant_id' = $1", tenant)
	return fmt.Sprintf("connections=%s grants=%s jobs=%d audit=%d", conns, grants, jobs, erpAuditCount(t, super, tenant))
}

func (r *completeRig) usedStates(t *testing.T) (used, total int) {
	t.Helper()
	for _, s := range statesOf(t, r.super, r.tenant) {
		total++
		if s.used != nil {
			used++
		}
	}
	return used, total
}

type refreshJob struct {
	args        map[string]any
	scheduledAt time.Time
	maxAttempts int
}

func refreshJobsOf(t *testing.T, super *pgxpool.Pool, tenant string) []refreshJob {
	t.Helper()
	rows, err := super.Query(context.Background(),
		"SELECT args::text, scheduled_at, max_attempts FROM river_job WHERE kind = 'erp_token_refresh' AND args->>'tenant_id' = $1 ORDER BY id", tenant)
	if err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []refreshJob
	for rows.Next() {
		var j refreshJob
		var raw string
		var max int16
		if err := rows.Scan(&raw, &j.scheduledAt, &max); err != nil {
			t.Fatalf("scan: %v", err)
		}
		j.maxAttempts = int(max)
		if err := json.Unmarshal([]byte(raw), &j.args); err != nil {
			t.Fatalf("args %q: %v", raw, err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	return out
}

// wantNothingLeft: no grant row, no secret and no refresh job.
func (r *completeRig) wantNothingLeft(t *testing.T, label string) {
	t.Helper()
	if n := grantCount(t, r.super, r.tenant); n != 0 {
		t.Errorf("%s: %d grant rows, want 0", label, n)
	}
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
		t.Errorf("%s: store holds %v, want no secret", label, ids)
	}
	if n := len(refreshJobsOf(t, r.super, r.tenant)); n != 0 {
		t.Errorf("%s: %d refresh jobs, want 0", label, n)
	}
}

func (r *completeRig) wantPending(t *testing.T, label, conn string, key *string) {
	t.Helper()
	s := stateOf(t, r.super, conn)
	if s.status != "pending" || s.grant != nil || deref(s.key) != deref(key) {
		t.Errorf("%s: connection = %+v, want pending with key %s and no grant", label, s, deref(key))
	}
}

func near(a, b time.Time, d time.Duration) bool {
	diff := a.Sub(b)
	return diff < d && diff > -d
}

// --- AC 1: a good code activates the connection ------------------------------------------

func TestComplete_ConnectsWithAGoodCode(t *testing.T) {
	r := newCompleteRig(t)
	conn, state := r.connect(t, r.e1)

	got, err := r.complete(state, "fake-co-1", "")
	wantOutcome(t, "good code", got, err, "connected")

	_, vendor := r.ex.exchanged()
	if vendor.Grant.ExpiresAt == nil || len(vendor.Grant.Secret) == 0 {
		t.Fatalf("the recording fake returned %+v, want a grant with an expiry and a secret", vendor.Grant)
	}
	s := stateOf(t, r.super, conn.ID)
	if s.status != "active" || deref(s.key) != "fake-co-1" || s.grant == nil {
		t.Fatalf("connection = %+v, want active, key fake-co-1, with a grant", s)
	}
	wantRef := SecretName("", *s.grant)
	var ref, kind string
	var expires *time.Time
	mustScan(t, r.super, "SELECT secret_ref, kind, expires_at FROM erp_grants WHERE id = $1", []any{*s.grant}, &ref, &kind, &expires)
	if ref != wantRef {
		t.Errorf("secret_ref = %q, want %q", ref, wantRef)
	}
	if kind != string(vendor.Grant.Kind) {
		t.Errorf("grant kind = %q, want the exchange's %q", kind, vendor.Grant.Kind)
	}
	if expires == nil || !near(*expires, *vendor.Grant.ExpiresAt, 2*time.Millisecond) {
		t.Errorf("grant expires_at = %v, want the exchange's %v", expires, vendor.Grant.ExpiresAt)
	}
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 1 || ids[0] != wantRef {
		t.Errorf("store holds %v, want exactly %s", ids, wantRef)
	}
	if payload, _, err := r.sec.Access(context.Background(), wantRef); err != nil || !bytes.Equal(payload, vendor.Grant.Secret) {
		t.Errorf("stored secret = %q (err %v), want the bytes the connector returned", payload, err)
	}

	var actor, payloadJSON string
	mustScan(t, r.super, "SELECT actor, payload::text FROM audit_log WHERE tenant_id = $1 AND event = 'erp.connection.authorised'",
		[]any{r.tenant}, &actor, &payloadJSON)
	if actor != subjectOf(r.admin) {
		t.Errorf("erp.connection.authorised actor = %q, want the consenting admin %q", actor, subjectOf(r.admin))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil || payload["connection_id"] != conn.ID {
		t.Errorf("erp.connection.authorised payload = %s, want it to name connection %s", payloadJSON, conn.ID)
	}
	for _, leak := range []string{state, "fake-co-1", string(vendor.Grant.Secret)} {
		if strings.Contains(payloadJSON, leak) {
			t.Errorf("erp.connection.authorised payload %s carries %q", payloadJSON, leak)
		}
	}

	jobs := refreshJobsOf(t, r.super, r.tenant)
	if len(jobs) != 1 {
		t.Fatalf("%d refresh jobs, want 1", len(jobs))
	}
	if jobs[0].args["grant_id"] != *s.grant {
		t.Errorf("refresh job args = %v, want grant_id %s", jobs[0].args, *s.grant)
	}
	if want := vendor.Grant.ExpiresAt.Add(-5 * time.Minute); !near(jobs[0].scheduledAt, want, 2*time.Second) {
		t.Errorf("refresh job scheduled at %v, want expiry - 5 min = %v", jobs[0].scheduledAt, want)
	}
	if jobs[0].maxAttempts != 8 {
		t.Errorf("refresh job max_attempts = %d, want 8", jobs[0].maxAttempts)
	}
}

func TestComplete_NilExpiryEnqueuesNoRefresh(t *testing.T) {
	r := newCompleteRig(t)
	secret := []byte(uuid.NewString())
	r.ex.override = func(context.Context, ExchangeRequest) (ExchangeResult, error) {
		return ExchangeResult{ExternalKey: "fake-co-1", Grant: GrantMaterial{Kind: GrantAPIKey, Secret: secret}}, nil
	}
	conn, state := r.connect(t, r.e1)

	got, err := r.complete(state, "fake-co-1", "")
	wantOutcome(t, "nil expiry", got, err, "connected")

	s := stateOf(t, r.super, conn.ID)
	if s.status != "active" || s.grant == nil {
		t.Fatalf("connection = %+v, want active with a grant", s)
	}
	var kind string
	var expires *time.Time
	mustScan(t, r.super, "SELECT kind, expires_at FROM erp_grants WHERE id = $1", []any{*s.grant}, &kind, &expires)
	if kind != "api_key" || expires != nil {
		t.Errorf("grant = kind %q expires_at %v, want api_key with a NULL expiry", kind, expires)
	}
	if payload, _, err := r.sec.Access(context.Background(), SecretName("", *s.grant)); err != nil || !bytes.Equal(payload, secret) {
		t.Errorf("stored secret = %q (err %v), want the connector's bytes", payload, err)
	}
	if n := len(refreshJobsOf(t, r.super, r.tenant)); n != 0 {
		t.Errorf("%d erp_token_refresh jobs for a grant that never expires, want 0", n)
	}
}

func TestComplete_SecretNameCarriesTheScope(t *testing.T) {
	r := newCompleteRig(t)
	r.store.secretScope = "slot-1"
	conn, state := r.connect(t, r.e1)
	r.mustComplete(t, "scoped", state, "fake-co-1")

	s := stateOf(t, r.super, conn.ID)
	if s.grant == nil {
		t.Fatalf("connection = %+v, want a grant", s)
	}
	want := SecretName("slot-1", *s.grant)
	var ref string
	mustScan(t, r.super, "SELECT secret_ref FROM erp_grants WHERE id = $1", []any{*s.grant}, &ref)
	if ref != want || !strings.HasPrefix(ref, "erp-grant-slot-1-") {
		t.Errorf("secret_ref = %q, want %q", ref, want)
	}
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 1 || ids[0] != want {
		t.Errorf("store holds %v, want exactly %s", ids, want)
	}
}

// --- AC 2: one answer for every bad state, single use, newest only ----------------------

func TestComplete_InvalidStates(t *testing.T) {
	r := newCompleteRig(t)
	otherTenant := seedTenant(t, r.super, "firm")
	e2, e3 := r.client(t, "Client Two"), r.client(t, "Client Three")
	_, live := r.connect(t, r.e1)

	_, used := r.connect(t, e2)
	r.mustComplete(t, "the state to be reused", used, "fake-co-2")

	_, expired := r.connect(t, e3)
	mustExec(t, r.super,
		"UPDATE erp_oauth_states SET created_at = now() - interval '20 minutes', expires_at = now() - interval '10 minutes' WHERE state_hash = $1",
		hashOf(expired))

	unknown, _ := NewState(r.tenant)
	unknownTenant, _ := NewState(uuid.NewString())
	_, tail, _ := strings.Cut(live, ".")

	cases := []struct{ name, state, vendorError string }{
		{"unknown", unknown, ""},
		{"unknown tenant", unknownTenant, ""},
		{"used", used, ""},
		{"expired", expired, ""},
		{"malformed", "not-a-state", ""},
		{"empty", "", ""},
		{"upper-cased", strings.ToUpper(live), ""},
		{"one character short", live[:len(live)-1], ""},
		{"another tenant's prefix", otherTenant + "." + tail, ""},
		{"unknown with a vendor error", unknown, "access_denied"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := erpState(t, r.super, r.tenant) + "\n" + erpState(t, r.super, otherTenant)
			got, err := r.complete(c.state, "fake-co-1", c.vendorError)
			wantOutcome(t, c.name, got, err, "invalid")
			if after := erpState(t, r.super, r.tenant) + "\n" + erpState(t, r.super, otherTenant); after != before {
				t.Errorf("an invalid state changed rows:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
	if calls, _ := r.ex.exchanged(); calls != 1 {
		t.Errorf("%d exchanges, want 1 (the control that used a state): an invalid state must not reach the vendor", calls)
	}
	// The control: none of the refusals consumed the live state.
	r.mustComplete(t, "the live state after every refusal", live, "fake-co-1")
}

func TestComplete_StateIsSingleUse(t *testing.T) {
	r := newCompleteRig(t)
	conn, state := r.connect(t, r.e1)

	r.mustComplete(t, "first use", state, "fake-co-1")
	before := erpState(t, r.super, r.tenant)
	for _, code := range []string{"fake-co-1", "fake-co-2"} {
		got, err := r.complete(state, code, "")
		wantOutcome(t, "second use with "+code, got, err, "invalid")
	}
	if after := erpState(t, r.super, r.tenant); after != before {
		t.Errorf("a repeated state changed rows:\nbefore %s\nafter  %s", before, after)
	}
	if n := grantCount(t, r.super, r.tenant); n != 1 {
		t.Errorf("%d grants after two uses, want 1", n)
	}
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 1 {
		t.Errorf("store holds %v, want one secret", ids)
	}
	if s := stateOf(t, r.super, conn.ID); s.status != "active" || deref(s.key) != "fake-co-1" {
		t.Errorf("connection = %+v, want it still active on fake-co-1", s)
	}
}

func TestComplete_OnlyTheNewestStateWorks(t *testing.T) {
	r := newCompleteRig(t)
	conn1, s1 := r.connect(t, r.e1)
	conn2, s2 := r.connect(t, r.e1)
	if conn1.ID != conn2.ID {
		t.Fatalf("two connects made rows %s and %s, want one keyless row", conn1.ID, conn2.ID)
	}

	got, err := r.complete(s1, "fake-co-1", "")
	wantOutcome(t, "the older state", got, err, "invalid")
	r.wantPending(t, "after the older state", conn1.ID, nil)
	r.wantNothingLeft(t, "after the older state")

	r.mustComplete(t, "the newest state", s2, "fake-co-1")
	if s := stateOf(t, r.super, conn1.ID); s.status != "active" {
		t.Errorf("connection = %+v, want active", s)
	}
}

// --- AC 3: failures leave the pending row, no grant and no secret ------------------------

func TestComplete_FailuresLeaveThePendingRow(t *testing.T) {
	cases := []struct {
		name string
		want string
		// run may wire a failure into the rig; it returns the callback input.
		run func(r *completeRig, state string, cancel context.CancelFunc) CallbackInput
	}{
		{"the vendor denies", "denied", func(_ *completeRig, state string, _ context.CancelFunc) CallbackInput {
			return CallbackInput{State: state, Error: "access_denied"}
		}},
		{"a code the vendor refuses", "failed", func(_ *completeRig, state string, _ context.CancelFunc) CallbackInput {
			return CallbackInput{State: state, Code: "nope"}
		}},
		{"an exchange that never answers", "failed", func(r *completeRig, state string, cancel context.CancelFunc) CallbackInput {
			r.ex.override = func(ctx context.Context, _ ExchangeRequest) (ExchangeResult, error) {
				cancel() // the deadline passes while the vendor is silent
				<-ctx.Done()
				return ExchangeResult{}, ctx.Err()
			}
			return CallbackInput{State: state, Code: "fake-co-1"}
		}},
		{"a secret store that cannot create", "failed", func(r *completeRig, state string, _ context.CancelFunc) CallbackInput {
			r.sec.failCreate(errors.New("injected create failure"))
			return CallbackInput{State: state, Code: "fake-co-1"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCompleteRig(t)
			conn, state := r.connect(t, r.e1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			in := c.run(r, state, cancel)

			out, err := r.store.Complete(ctx, in)
			wantOutcome(t, c.name, string(out), err, c.want)

			r.wantPending(t, c.name, conn.ID, nil)
			r.wantNothingLeft(t, c.name)
			if n := eventCount(t, r.super, r.tenant, "erp.connection.authorised"); n != 0 {
				t.Errorf("%d erp.connection.authorised events after %s, want 0", n, c.name)
			}
			if used, total := r.usedStates(t); used != 1 || total != 1 {
				t.Errorf("states used = %d of %d, want the callback's state consumed", used, total)
			}
			// The consumed state is spent, and the row can still be connected afresh.
			got, err := r.complete(state, "fake-co-1", "")
			wantOutcome(t, "the same state again", got, err, "invalid")

			r.ex.override = nil
			r.sec.failCreate(nil)
			_, fresh := r.connect(t, r.e1)
			r.mustComplete(t, "a fresh state after "+c.name, fresh, "fake-co-1")
		})
	}
}

// --- AC 4: a callback that arrives too late is ended and changes nothing ----------------

func TestComplete_EndedCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, r *completeRig, conn Connection)
	}{
		{"the admin was demoted", func(t *testing.T, r *completeRig, _ Connection) {
			mustExec(t, r.super, "UPDATE memberships SET role = 'preparer' WHERE tenant_id = $1 AND user_id = $2", r.tenant, subjectOf(r.admin))
		}},
		{"the admin was suspended", func(t *testing.T, r *completeRig, _ Connection) {
			mustExec(t, r.super, "UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2", r.tenant, subjectOf(r.admin))
		}},
		{"the workspace is scheduled for deletion", func(t *testing.T, r *completeRig, _ Connection) {
			lockWorkspace(t, r.super, r.tenant, subjectOf(r.admin))
		}},
		{"the connection was disconnected", func(t *testing.T, r *completeRig, conn Connection) {
			if _, err := r.store.Disconnect(r.admin, r.e1, conn.ID); err != nil {
				t.Fatalf("Disconnect: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCompleteRig(t)
			conn, state := r.connect(t, r.e1)
			c.mutate(t, r, conn)
			before := ercRows(t, r.super, r.tenant)

			got, err := r.complete(state, "fake-co-1", "")
			wantOutcome(t, c.name, got, err, "ended")

			if after := ercRows(t, r.super, r.tenant); after != before {
				t.Errorf("an ended callback changed rows:\nbefore %s\nafter  %s", before, after)
			}
			if used, total := r.usedStates(t); used != 1 || total != 1 {
				t.Errorf("states used = %d of %d, want the state consumed (used_at set)", used, total)
			}
			if calls, _ := r.ex.exchanged(); calls != 0 {
				t.Errorf("%d vendor exchanges for an ended callback, want 0", calls)
			}
			if n := grantCount(t, r.super, r.tenant); n != 0 {
				t.Errorf("%d grants, want 0", n)
			}
			if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
				t.Errorf("store holds %v, want no secret", ids)
			}
		})
	}
}

func TestComplete_RacingDisconnectLeavesNoGrantAndNoSecret(t *testing.T) {
	r := newCompleteRig(t)
	conn, state := r.connect(t, r.e1)
	entered, release := make(chan struct{}), make(chan struct{})
	r.ex.override = func(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ExchangeResult{}, ctx.Err()
		}
		return r.ex.Fake.Exchange(ctx, req)
	}

	type answer struct {
		out string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		out, err := r.complete(state, "fake-co-1", "")
		done <- answer{out, err}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Complete never reached the vendor exchange")
	}
	if _, err := r.store.Disconnect(r.admin, r.e1, conn.ID); err != nil {
		t.Fatalf("Disconnect while the exchange is in flight: %v", err)
	}
	close(release)
	a := <-done
	wantOutcome(t, "callback after the disconnect committed", a.out, a.err, "ended")

	if s := stateOf(t, r.super, conn.ID); s.status != "disconnected" || s.grant != nil {
		t.Errorf("connection = %+v, want it still disconnected with no grant", s)
	}
	r.wantNothingLeft(t, "after the race")
	if created, deleted := r.sec.counts(); created != 1 || deleted != 1 {
		t.Errorf("secret creates = %d, deletes = %d, want the secret created and then deleted once", created, deleted)
	}
	if n := eventCount(t, r.super, r.tenant, "erp.connection.authorised"); n != 0 {
		t.Errorf("%d erp.connection.authorised events, want 0", n)
	}
}

// --- AC 5: a key clash is a conflict; a tx2 failure deletes the secret ------------------

func TestComplete_KeyCollisionIsConflict(t *testing.T) {
	r := newCompleteRig(t)
	e2, e3 := r.client(t, "Client Two"), r.client(t, "Client Three")

	t.Run("another connection of the client holds the key", func(t *testing.T) {
		seedActiveConn(t, r.super, r.tenant, r.e1, "fake-co-1")
		conn, state := r.connect(t, r.e1)
		got, err := r.complete(state, "fake-co-1", "")
		wantOutcome(t, "collision", got, err, "conflict")
		r.wantPending(t, "collision", conn.ID, nil)
		if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
			t.Errorf("store holds %v after a conflict, want the new secret deleted", ids)
		}
		if created, deleted := r.sec.counts(); created != 1 || deleted != 1 {
			t.Errorf("secret creates = %d, deletes = %d, want one of each", created, deleted)
		}
		if n := grantCount(t, r.super, r.tenant); n != 1 {
			t.Errorf("%d grants, want only the seeded one", n)
		}
	})

	t.Run("a re-authorised row keeps its own company", func(t *testing.T) {
		row := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e2, key: "fake-co-2", status: "disconnected"})
		state := r.connectByID(t, e2, row)
		got, err := r.complete(state, "fake-co-3", "")
		wantOutcome(t, "another company", got, err, "conflict")
		if s := stateOf(t, r.super, row); s.status != "pending" || deref(s.key) != "fake-co-2" || s.grant != nil {
			t.Errorf("row = %+v, want pending, still keyed fake-co-2, no grant", s)
		}
		if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
			t.Errorf("store holds %v after a conflict, want none", ids)
		}

		again := r.connectByID(t, e2, row)
		r.mustComplete(t, "the same company again", again, "fake-co-2")
		if s := stateOf(t, r.super, row); s.status != "active" || deref(s.key) != "fake-co-2" || s.grant == nil {
			t.Errorf("row = %+v, want active on fake-co-2", s)
		}
	})

	t.Run("another client may use the same company", func(t *testing.T) {
		conn, state := r.connect(t, e3)
		r.mustComplete(t, "the same company on another client", state, "fake-co-1")
		if s := stateOf(t, r.super, conn.ID); s.status != "active" || deref(s.key) != "fake-co-1" {
			t.Errorf("row = %+v, want active on fake-co-1", s)
		}
	})
}

// takenGrantID makes the next grant insert fail on its primary key: the id belongs to a grant that already exists.
func (r *completeRig) takenGrantID(t *testing.T) string {
	t.Helper()
	taken := seedGrant(t, r.super, r.tenant, "")
	r.store.newGrantID = func() string { return taken }
	return taken
}

func TestComplete_Tx2FailureDeletesTheSecret(t *testing.T) {
	r := newCompleteRig(t)
	taken := r.takenGrantID(t)
	conn, state := r.connect(t, r.e1)

	out, err := r.complete(state, "fake-co-1", "")
	wantOutcome(t, "tx2 failure", out, err, "failed")

	r.wantPending(t, "tx2 failure", conn.ID, nil)
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
		t.Errorf("store holds %v after tx2 failed, want the secret deleted", ids)
	}
	if created, deleted := r.sec.counts(); created != 1 || deleted != 1 {
		t.Errorf("secret creates = %d, deletes = %d, want one of each (the secret existed before tx2 failed)", created, deleted)
	}
	if n := grantCount(t, r.super, r.tenant); n != 1 || !grantExists(t, r.super, taken) {
		t.Errorf("%d grants, want only the pre-existing %s", n, taken)
	}
	if n := eventCount(t, r.super, r.tenant, "erp.connection.authorised"); n != 0 {
		t.Errorf("%d erp.connection.authorised events, want 0", n)
	}

	r.store.newGrantID = uuid.NewString
	_, fresh := r.connect(t, r.e1)
	r.mustComplete(t, "a fresh state with a fresh grant id", fresh, "fake-co-1")
}

func TestComplete_InlineDeleteFailureIsLogged(t *testing.T) {
	t.Run("tx2 fails", func(t *testing.T) {
		r := newCompleteRig(t)
		logs := captureLogs(t)
		r.sec.failDelete(errors.New("injected delete failure"))
		taken := r.takenGrantID(t)
		_, state := r.connect(t, r.e1)

		out, err := r.complete(state, "fake-co-1", "")
		wantOutcome(t, "tx2 failure with a failing delete", out, err, "failed")
		r.wantLogged(t, logs, state, "fake-co-1")
		if !holds(r.sec, SecretName("", taken)) {
			t.Errorf("secret %s is gone although its delete failed", SecretName("", taken))
		}
	})

	t.Run("a key collision", func(t *testing.T) {
		r := newCompleteRig(t)
		logs := captureLogs(t)
		seedActiveConn(t, r.super, r.tenant, r.e1, "fake-co-1")
		r.sec.failDelete(errors.New("injected delete failure"))
		_, state := r.connect(t, r.e1)

		out, err := r.complete(state, "fake-co-1", "")
		wantOutcome(t, "collision with a failing delete", out, err, "conflict")
		r.wantLogged(t, logs, state, "fake-co-1")
		if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 1 {
			t.Errorf("store holds %v, want the one secret whose delete failed", ids)
		}
	})
}

// wantLogged: exactly one error record, and no record carries the state, the code or the vendor's bytes.
func (r *completeRig) wantLogged(t *testing.T, logs *logBuf, state, code string) {
	t.Helper()
	if n := logs.errorRecords(); n != 1 {
		t.Errorf("%d error records, want 1 for the failed delete:\n%s", n, logs.text())
	}
	_, vendor := r.ex.exchanged()
	if len(vendor.Grant.Secret) == 0 {
		t.Fatal("the vendor returned no secret, so the log check has nothing to look for")
	}
	_, tail, _ := strings.Cut(state, ".")
	leaks := []string{state, tail, code, string(vendor.Grant.Secret)}
	var tokens struct{ Access, Refresh string }
	if json.Unmarshal(vendor.Grant.Secret, &tokens) == nil {
		leaks = append(leaks, tokens.Access, tokens.Refresh)
	}
	for _, leak := range leaks {
		if leak != "" && strings.Contains(logs.text(), leak) {
			t.Errorf("a log record carries %q:\n%s", leak, logs.text())
		}
	}
}

// --- AC 6: re-authorising releases the old grant ----------------------------------------

func TestComplete_ReauthoriseReleasesTheOldGrant(t *testing.T) {
	r := newCompleteRig(t)
	connID, g1 := connectActive(t, r.store, r.admin, r.super, r.e1, "fake-co-1")
	mustExec(t, r.super, "UPDATE erp_connections SET status = 'reconsent' WHERE id = $1", connID)
	if n := len(deleteJobsOf(t, r.super, r.tenant)); n != 0 {
		t.Fatalf("%d delete jobs before the re-authorisation, want 0", n)
	}

	state := r.connectByID(t, r.e1, connID)
	r.mustComplete(t, "re-authorise", state, "fake-co-1")

	s := stateOf(t, r.super, connID)
	if s.status != "active" || s.grant == nil || *s.grant == g1 {
		t.Fatalf("connection = %+v, want active on a new grant (old %s)", s, g1)
	}
	if grantExists(t, r.super, g1) {
		t.Errorf("the old grant %s still exists", g1)
	}
	jobs := deleteJobsOf(t, r.super, r.tenant)
	if len(jobs) != 1 {
		t.Fatalf("%d erp_secret_delete jobs, want 1", len(jobs))
	}
	if got := jobs[0].argMap(t)["secret_ref"]; got != SecretName("", g1) {
		t.Errorf("delete job secret_ref = %v, want the old grant's %s", got, SecretName("", g1))
	}
}

// --- AC 8: the worker route ---------------------------------------------------------------

func TestCallbackHandler_BodyLimitAndNoEcho(t *testing.T) {
	r := newCompleteRig(t)
	logs := captureLogs(t)
	h := CallbackHandler(r.store.Complete, slog.Default())
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/internal/erp/callback", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec
	}
	outcomeOf := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var a struct{ Outcome string }
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
			t.Fatalf("answer %q is not JSON: %v", rec.Body.String(), err)
		}
		return a.Outcome
	}
	padded := func(size int) string {
		head, tail := "{\"state\":\"x\",\"code\":\"", "\",\"error\":\"\"}"
		return head + strings.Repeat("a", size-len(head)-len(tail)) + tail
	}

	t.Run("4 KiB passes and 4 KiB plus one byte does not", func(t *testing.T) {
		rec := post(padded(4096))
		if rec.Code != http.StatusOK || outcomeOf(t, rec) != "invalid" {
			t.Errorf("a 4096-byte body = %d %s, want 200 invalid", rec.Code, rec.Body.String())
		}
		if rec := post(padded(4097)); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("a 4097-byte body = %d, want 413", rec.Code)
		}
		if rec := post(padded(5 * 1024)); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("a 5 KiB body = %d, want 413", rec.Code)
		}
	})

	t.Run("a good callback answers its outcome", func(t *testing.T) {
		_, state := r.connect(t, r.e1)
		body, _ := json.Marshal(map[string]string{"state": state, "code": "fake-co-1", "error": ""})
		rec := post(string(body))
		if rec.Code != http.StatusOK || outcomeOf(t, rec) != "connected" {
			t.Errorf("good callback = %d %s, want 200 connected", rec.Code, rec.Body.String())
		}
	})

	failures := []struct {
		name  string
		setup func(code string) string // returns the code to send
	}{
		{"a refused code", func(code string) string { return code }},
		{"a secret store that cannot create", func(string) string {
			r.sec.failCreate(errors.New("injected create failure"))
			return "fake-co-2"
		}},
	}
	for _, f := range failures {
		t.Run(f.name+" echoes and logs nothing", func(t *testing.T) {
			e := r.client(t, f.name)
			_, state := r.connect(t, e)
			code := f.setup("code-" + uuid.NewString())
			defer r.sec.failCreate(nil)
			body, _ := json.Marshal(map[string]string{"state": state, "code": code, "error": ""})
			rec := post(string(body))

			switch rec.Code {
			case http.StatusOK:
				if got := outcomeOf(t, rec); got != "failed" {
					t.Errorf("outcome = %q, want failed", got)
				}
			case http.StatusInternalServerError:
				if got := strings.TrimSpace(rec.Body.String()); got != "{\"error\":\"internal server error\"}" {
					t.Errorf("500 body = %s, want the fixed internal-error body", got)
				}
			default:
				t.Errorf("status = %d, want 200 failed or 500", rec.Code)
			}
			_, vendor := r.ex.exchanged()
			_, tail, _ := strings.Cut(state, ".")
			for _, leak := range []string{state, tail, code, string(vendor.Grant.Secret)} {
				if leak == "" {
					continue
				}
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("the answer %q echoes %q", rec.Body.String(), leak)
				}
				if strings.Contains(logs.text(), leak) {
					t.Errorf("a log record carries %q:\n%s", leak, logs.text())
				}
			}
		})
	}
}
