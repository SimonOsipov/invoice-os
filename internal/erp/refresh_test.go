package erp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// refreshStub is a connector whose Refresh the test scripts; every other method is the nil interface.
type refreshStub struct {
	Connector
	calls  atomic.Int32
	enter  chan struct{} // closed on the first Refresh call, when set
	hold   chan struct{} // Refresh blocks until it is closed, when set
	expiry time.Time
	fn     func(c Credentials) ([]byte, error) // nil: a fresh secret
	once   sync.Once
	mu     sync.Mutex
	last   []byte
}

func (s *refreshStub) System() string { return "fake" }

func (s *refreshStub) Refresh(_ context.Context, c Credentials) (GrantMaterial, error) {
	s.calls.Add(1)
	if s.enter != nil {
		s.once.Do(func() { close(s.enter) })
	}
	if s.hold != nil {
		<-s.hold
	}
	secret := []byte("new-" + uuid.NewString())
	if s.fn != nil {
		var err error
		if secret, err = s.fn(c); err != nil {
			return GrantMaterial{}, err
		}
	}
	s.mu.Lock()
	s.last = secret
	s.mu.Unlock()
	exp := s.expiry
	return GrantMaterial{Kind: GrantOAuthRefresh, Secret: secret, ExpiresAt: &exp}, nil
}

func (s *refreshStub) lastSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.last)
}

// flakySecrets fails AddVersion while addFails is positive (negative: always) and DisableVersion when disableFails.
type flakySecrets struct {
	SecretStore
	addFails     atomic.Int32
	disableFails atomic.Bool
}

func (f *flakySecrets) AddVersion(ctx context.Context, id string, p []byte) (string, error) {
	n := f.addFails.Load()
	if n < 0 || (n > 0 && f.addFails.Add(-1) >= 0) {
		return "", errors.New("injected add failure")
	}
	return f.SecretStore.AddVersion(ctx, id, p)
}

func (f *flakySecrets) DisableVersion(ctx context.Context, id, v string) error {
	if f.disableFails.Load() {
		return errors.New("injected disable failure")
	}
	return f.SecretStore.DisableVersion(ctx, id, v)
}

type refreshRig struct {
	super, app *pgxpool.Pool
	tenant     string
	mem        *MemorySecrets
	secrets    *flakySecrets
	stub       *refreshStub
	worker     *RefreshWorker
	logs       *bytes.Buffer
	expiry     time.Time
}

func newRefreshRig(t *testing.T) *refreshRig {
	t.Helper()
	super, app := storePools(t)
	tenant := seedTenant(t, super, "firm")
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), "DELETE FROM river_job WHERE args->>'tenant_id' = $1", tenant)
	})
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	mem := NewMemorySecrets()
	r := &refreshRig{super: super, app: app, tenant: tenant, mem: mem, secrets: &flakySecrets{SecretStore: mem}, logs: &bytes.Buffer{}}
	r.expiry = time.Now().Add(time.Hour).Truncate(time.Second)
	r.stub = &refreshStub{expiry: r.expiry}
	reg, err := NewRegistry(r.stub)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	r.worker = &RefreshWorker{
		Pool: app, Registry: reg, Secrets: r.secrets, Queue: q,
		Logger:          slog.New(slog.NewJSONHandler(r.logs, nil)),
		addVersionDelay: time.Millisecond,
	}
	return r
}

// grant seeds a grant with a secret and returns its id and secret name.
func (r *refreshRig) grant(t *testing.T) (id, ref string) {
	t.Helper()
	id = seedHeldGrant(t, r.super, r.mem, r.tenant)
	return id, SecretName("", id)
}

func (r *refreshRig) work(jobID int64, grant string, version int64, attempt int) error {
	return r.worker.Work(context.Background(), &river.Job[RefreshArgs]{
		JobRow: &rivertype.JobRow{ID: jobID, Kind: "erp_token_refresh", Attempt: attempt, MaxAttempts: 8},
		Args:   RefreshArgs{TenantID: r.tenant, GrantID: grant, Version: version},
	})
}

func (r *refreshRig) version(t *testing.T, grant string) int {
	return countOf(t, r.super, "SELECT version FROM erp_grants WHERE id = $1", grant)
}

func (r *refreshRig) latest(t *testing.T, ref string) string {
	t.Helper()
	b, _, err := r.mem.Access(context.Background(), ref)
	if err != nil {
		t.Fatalf("access %s: %v", ref, err)
	}
	return string(b)
}

func (r *refreshRig) events(t *testing.T, event string) []auditRow {
	t.Helper()
	return auditOf(t, r.super, r.tenant, event)
}

func (r *refreshRig) nextJobs(t *testing.T) []deleteJob {
	t.Helper()
	rows, err := r.super.Query(context.Background(), "SELECT id, args FROM river_job WHERE kind = 'erp_token_refresh' AND args->>'tenant_id' = $1 ORDER BY id", r.tenant)
	if err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []deleteJob
	for rows.Next() {
		var j deleteJob
		if err := rows.Scan(&j.id, &j.args); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, j)
	}
	return out
}

func auditOf(t *testing.T, super *pgxpool.Pool, tenant, event string) []auditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		"SELECT actor, payload::text, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id", tenant, event)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		var payload string
		if err := rows.Scan(&a.actor, &payload, &a.entity); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal([]byte(payload), &a.payload); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
		out = append(out, a)
	}
	return out
}

func isCancel(err error) bool {
	var c *rivertype.JobCancelError
	return errors.As(err, &c)
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// twoActive seeds two active connections on one grant, one per client.
func (r *refreshRig) twoActive(t *testing.T, grant string) (c1, c2 string) {
	t.Helper()
	e1, e2 := seedClient(t, r.super, r.tenant, "One"), seedClient(t, r.super, r.tenant, "Two")
	c1 = seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e1, key: "k1", status: "active", grant: grant})
	c2 = seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e2, key: "k2", status: "active", grant: grant})
	return c1, c2
}

func TestRefresh_ReplacesTheTokenAndSchedulesTheNext(t *testing.T) {
	r := newRefreshRig(t)
	g, ref := r.grant(t)
	if err := r.work(101, g, 1, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got, want := r.latest(t, ref), r.stub.lastSecret(); got != want || got == "" {
		t.Errorf("latest secret = %q, want the connector's %q", got, want)
	}
	if !r.mem.m[ref][0].disabled {
		t.Error("the version the job read is still enabled")
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("grant version = %d, want 2", v)
	}
	var exp time.Time
	mustScan(t, r.super, "SELECT expires_at FROM erp_grants WHERE id = $1", []any{g}, &exp)
	if !exp.Equal(r.expiry) {
		t.Errorf("expires_at = %v, want %v", exp, r.expiry)
	}
	ev := r.events(t, "erp.token.refreshed")
	if len(ev) != 1 || ev[0].actor != "system" || !reflect.DeepEqual(ev[0].payload, map[string]any{"system": "fake", "grant_id": g}) {
		t.Errorf("refreshed events = %+v, want one {system, grant_id}", ev)
	}
	jobs := r.nextJobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d next jobs, want 1", len(jobs))
	}
	var args RefreshArgs
	if err := json.Unmarshal(jobs[0].args, &args); err != nil || args != (RefreshArgs{TenantID: r.tenant, GrantID: g, Version: 2}) {
		t.Errorf("next job args = %s (%v), want {tenant, %s, 2}", jobs[0].args, err, g)
	}
	var at time.Time
	mustScan(t, r.super, "SELECT scheduled_at FROM river_job WHERE id = $1", []any{jobs[0].id}, &at)
	if !at.Equal(r.expiry.Add(-5 * time.Minute)) {
		t.Errorf("next job scheduled at %v, want %v", at, r.expiry.Add(-5*time.Minute))
	}
}

func TestRefresh_TwoRunsCallTheVendorOnce(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	r.stub.enter, r.stub.hold = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.work(201, g, 1, 1) }()
	<-r.stub.enter
	if err := r.work(202, g, 1, 1); err != nil { // claimed already: exits
		t.Errorf("second run: %v", err)
	}
	close(r.stub.hold)
	if err := <-done; err != nil {
		t.Errorf("first run: %v", err)
	}
	if n := r.stub.calls.Load(); n != 1 {
		t.Errorf("connector called %d times, want 1", n)
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
	if n := len(r.events(t, "erp.token.refreshed")); n != 1 {
		t.Errorf("%d refreshed events, want 1", n)
	}
}

func TestRefresh_AddVersionRetriedWithinTheAttempt(t *testing.T) {
	r := newRefreshRig(t)
	g, ref := r.grant(t)
	r.secrets.addFails.Store(2)
	if err := r.work(301, g, 1, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := r.stub.calls.Load(); n != 1 {
		t.Errorf("connector called %d times, want 1", n)
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
	if got := r.latest(t, ref); got != r.stub.lastSecret() {
		t.Errorf("latest = %q, want the connector's bytes", got)
	}
}

func TestRefresh_RetryAfterClaimStillRefreshes(t *testing.T) {
	r := newRefreshRig(t)
	g, ref := r.grant(t)
	r.secrets.addFails.Store(-1)
	if err := r.work(401, g, 1, 1); err == nil {
		t.Fatal("Work succeeded with every AddVersion failing")
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("version after the failed run = %d, want 2 (claim committed)", v)
	}
	if n := countOf(t, r.super, "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = 'job:401'", r.tenant); n != 1 {
		t.Errorf("%d claim markers, want 1", n)
	}
	first := r.stub.lastSecret()
	if n := strings.Count(r.logs.String(), "\"level\":\"ERROR\""); n != 1 {
		t.Errorf("%d error logs, want 1: %s", n, r.logs.String())
	}
	if strings.Contains(r.logs.String(), first) {
		t.Error("the error log carries the new token")
	}
	r.secrets.addFails.Store(0)
	if err := r.work(401, g, 1, 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("version = %d, want 2 (not 3)", v)
	}
	if got := r.latest(t, ref); got != r.stub.lastSecret() || got == first {
		t.Errorf("latest = %q, want the second call's bytes", got)
	}
}

func TestRefresh_ReleasedGrantExits(t *testing.T) {
	r := newRefreshRig(t)
	g, ref := r.grant(t)
	entity := seedClient(t, r.super, r.tenant, "One")
	c := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: entity, key: "k", status: "active", grant: g})
	admin := seedMember(t, r.super, r.tenant, "admin", "active", true)
	q, err := queue.New(r.app, queue.Config{})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := NewRegistry(NewFake(testRedirect))
	if _, err := NewStore(r.app, reg, nil, q).Disconnect(admin, entity, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	_ = ref
	if err := r.work(501, g, 1, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := r.stub.calls.Load(); n != 0 {
		t.Errorf("connector called %d times, want 0", n)
	}
	for _, e := range []string{"erp.token.refreshed", "erp.token.refresh_failed", "erp.connection.reauth_required"} {
		if n := len(r.events(t, e)); n != 0 {
			t.Errorf("%d %s events, want 0", n, e)
		}
	}
}

func TestRefresh_StaleVersionExits(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	mustExec(t, r.super, "UPDATE erp_grants SET version = 3 WHERE id = $1", g)
	if err := r.work(601, g, 1, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := r.stub.calls.Load(); n != 0 {
		t.Errorf("connector called %d times, want 0", n)
	}
	if v := r.version(t, g); v != 3 {
		t.Errorf("version = %d, want 3 (untouched)", v)
	}
}

func revokedStub(r *refreshRig) {
	r.stub.fn = func(Credentials) ([]byte, error) { return nil, ErrGrantRevoked }
}

func TestRefresh_RevokedMovesEveryConnectionToReconsent(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	c1, c2 := r.twoActive(t, g)
	revokedStub(r)
	if err := r.work(701, g, 1, 1); !isCancel(err) {
		t.Fatalf("Work = %v, want a JobCancel error", err)
	}
	for _, c := range []string{c1, c2} {
		if s := stateOf(t, r.super, c); s.status != "reconsent" {
			t.Errorf("connection %s is %s, want reconsent", c, s.status)
		}
	}
	failed := r.events(t, "erp.token.refresh_failed")
	if len(failed) != 1 || !reflect.DeepEqual(failed[0].payload, map[string]any{"system": "fake", "grant_id": g, "reason": "revoked"}) {
		t.Errorf("refresh_failed = %+v, want one revoked", failed)
	}
	var ids []string
	for _, a := range r.events(t, "erp.connection.reauth_required") {
		ids = append(ids, fmt.Sprint(a.payload["connection_id"]))
	}
	want := []string{c1, c2}
	sort.Strings(ids)
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("reauth_required for %v, want %v", ids, want)
	}
	if n := len(r.nextJobs(t)); n != 0 {
		t.Errorf("%d next jobs, want 0", n)
	}
}

func TestRefresh_LostTokenEndsInReconsent(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	c1, c2 := r.twoActive(t, g)
	seen := map[string]bool{}
	r.stub.fn = func(c Credentials) ([]byte, error) {
		if seen[string(c.Secret)] {
			return nil, ErrGrantRevoked
		}
		seen[string(c.Secret)] = true
		return []byte("new-" + uuid.NewString()), nil
	}
	r.secrets.addFails.Store(-1)
	if err := r.work(801, g, 1, 1); err == nil || isCancel(err) {
		t.Fatalf("first run = %v, want a retryable error", err)
	}
	if err := r.work(801, g, 1, 2); !isCancel(err) {
		t.Fatalf("second run = %v, want a JobCancel error", err)
	}
	for _, c := range []string{c1, c2} {
		if s := stateOf(t, r.super, c); s.status != "reconsent" {
			t.Errorf("connection %s is %s, want reconsent", c, s.status)
		}
	}
	if f := r.events(t, "erp.token.refresh_failed"); len(f) != 1 || f[0].payload["reason"] != "revoked" {
		t.Errorf("refresh_failed = %+v, want one revoked", f)
	}
}

func TestRefresh_TerminalWritesOncePerTransition(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	r.twoActive(t, g)
	revokedStub(r)
	if err := r.work(901, g, 1, 1); !isCancel(err) {
		t.Fatalf("Work = %v, want cancel", err)
	}
	before := erpState(t, r.super, r.tenant)
	if err := r.work(901, g, 1, 2); !isCancel(err) {
		t.Fatalf("repeat = %v, want cancel", err)
	}
	if after := erpState(t, r.super, r.tenant); after != before {
		t.Errorf("a repeated terminal write changed state:\nbefore %s\nafter  %s", before, after)
	}
	r.stub.fn = func(Credentials) ([]byte, error) { return nil, errors.New("vendor down") }
	if err := r.work(901, g, 1, 8); err == nil || isCancel(err) {
		t.Fatalf("attempt 8 = %v, want the error", err)
	}
	if after := erpState(t, r.super, r.tenant); after != before {
		t.Errorf("attempt 8 after reconsent changed state:\nbefore %s\nafter  %s", before, after)
	}
}

func TestRefresh_FinalAttemptIsExhausted(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	e := seedClient(t, r.super, r.tenant, "One")
	c := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e, key: "k", status: "active", grant: g})
	r.stub.fn = func(Credentials) ([]byte, error) { return nil, errors.New("vendor down") }
	if err := r.work(1001, g, 1, 7); err == nil {
		t.Fatal("attempt 7 succeeded")
	}
	if s := stateOf(t, r.super, c); s.status != "active" {
		t.Errorf("after attempt 7 the connection is %s, want active", s.status)
	}
	if n := erpAuditCount(t, r.super, r.tenant); n != 0 {
		t.Errorf("%d events after attempt 7, want 0", n)
	}
	if err := r.work(1001, g, 1, 8); err == nil || isCancel(err) {
		t.Fatalf("attempt 8 = %v, want the plain error", err)
	}
	if s := stateOf(t, r.super, c); s.status != "reconsent" {
		t.Errorf("after attempt 8 the connection is %s, want reconsent", s.status)
	}
	if f := r.events(t, "erp.token.refresh_failed"); len(f) != 1 || f[0].payload["reason"] != "exhausted" {
		t.Errorf("refresh_failed = %+v, want one exhausted", f)
	}
	if n := len(r.events(t, "erp.connection.reauth_required")); n != 1 {
		t.Errorf("%d reauth_required, want 1", n)
	}
	if n := len(r.nextJobs(t)); n != 0 {
		t.Errorf("%d next jobs, want 0", n)
	}
}

func TestRefresh_DisableFailureDoesNotFailTheJob(t *testing.T) {
	r := newRefreshRig(t)
	g, ref := r.grant(t)
	r.secrets.disableFails.Store(true)
	if err := r.work(1101, g, 1, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if v := r.version(t, g); v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
	if !strings.Contains(r.logs.String(), "ERROR") || strings.Contains(r.logs.String(), r.stub.lastSecret()) {
		t.Errorf("want one error log without the token, got %s", r.logs.String())
	}
	_ = ref
}

func TestERPEvents_PayloadKeysAreExact(t *testing.T) {
	r := newRefreshRig(t)
	g1, ref1 := r.grant(t)
	g2, _ := r.grant(t)
	e1, e2 := seedClient(t, r.super, r.tenant, "One"), seedClient(t, r.super, r.tenant, "Two")
	seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e1, key: "k1", status: "active", grant: g1})
	c2 := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e2, key: "k2", status: "active", grant: g2})
	secrets := []string{}
	for _, ref := range []string{ref1, SecretName("", g2)} {
		secrets = append(secrets, r.latest(t, ref))
	}
	if err := r.work(1201, g1, 1, 1); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	secrets = append(secrets, r.stub.lastSecret())
	revokedStub(r)
	if err := r.work(1202, g2, 1, 1); !isCancel(err) {
		t.Fatalf("revoked: %v", err)
	}
	del := &SecretDeleteWorker{Pool: r.app, Secrets: r.mem}
	if err := del.Work(context.Background(), &river.Job[SecretDeleteArgs]{
		JobRow: &rivertype.JobRow{ID: 1203, Kind: "erp_secret_delete", Attempt: 1, MaxAttempts: 1000},
		Args:   SecretDeleteArgs{TenantID: r.tenant, SecretRef: ref1, ConnectionID: c2, System: "fake"},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := map[string][]string{
		"erp.token.refreshed":            {"grant_id", "system"},
		"erp.token.refresh_failed":       {"grant_id", "reason", "system"},
		"erp.connection.reauth_required": {"connection_id", "system"},
		"erp.token.deleted":              {"connection_id", "system"},
	}
	for event, keys := range want {
		rows := r.events(t, event)
		if len(rows) != 1 {
			t.Errorf("%d %s rows, want 1", len(rows), event)
			continue
		}
		if got := keysOf(rows[0].payload); !reflect.DeepEqual(got, keys) {
			t.Errorf("%s keys = %v, want %v", event, got, keys)
		}
		raw, _ := json.Marshal(rows[0].payload)
		for _, s := range secrets {
			if strings.Contains(string(raw), s) {
				t.Errorf("%s payload carries secret bytes", event)
			}
		}
	}
}
