package erp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// subRecorder wraps the fake and records each EnsureSubscriptions call.
type subRecorder struct {
	*Fake
	mu      sync.Mutex
	scopes  []string
	kinds   []GrantKind
	noHooks bool
	failOn  string
	onCall  func()
}

func (r *subRecorder) Capabilities() Capabilities {
	if r.noHooks {
		return Capabilities{}
	}
	return r.Fake.Capabilities()
}

func (r *subRecorder) EnsureSubscriptions(ctx context.Context, c Credentials, scope string) error {
	if r.onCall != nil {
		r.onCall()
	}
	r.mu.Lock()
	r.scopes = append(r.scopes, scope)
	r.kinds = append(r.kinds, c.Kind)
	r.mu.Unlock()
	if scope == r.failOn {
		return errors.New("vendor down")
	}
	return r.Fake.EnsureSubscriptions(ctx, c, scope)
}

type subEnv struct {
	super, app *pgxpool.Pool
	rec        *subRecorder
	buf        *bytes.Buffer
	keeper     *SubscriptionKeeper
}

func newSubEnv(t *testing.T) *subEnv {
	t.Helper()
	super, app := storePools(t)
	readerURL := os.Getenv("DATABASE_READER_URL")
	if readerURL == "" {
		t.Skip("erp subscriptions test skipped: set DATABASE_READER_URL")
	}
	reader, err := pgxpool.New(context.Background(), readerURL)
	if err != nil {
		t.Fatalf("connect reader: %v", err)
	}
	t.Cleanup(reader.Close)
	rec := &subRecorder{Fake: testFake(t)}
	reg, err := NewRegistry(rec)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	buf := &bytes.Buffer{}
	return &subEnv{super: super, app: app, rec: rec, buf: buf,
		keeper: &SubscriptionKeeper{Reader: reader, Pool: app, Registry: reg, Logger: slog.New(slog.NewTextHandler(buf, nil))}}
}

// conn seeds an active connection on an oauth_refresh grant.
func (e *subEnv) conn(t *testing.T, tenant, entity, key string) {
	t.Helper()
	g := seedGrant(t, e.super, tenant, "")
	mustExec(t, e.super, "UPDATE erp_grants SET kind = 'oauth_refresh' WHERE id = $1", g)
	seedConnRow(t, e.super, connRow{tenant: tenant, entity: entity, key: key, status: "active", grant: g})
}

func TestSubscriptions_EnsuresEachActiveConnectionAcrossTenants(t *testing.T) {
	e := newSubEnv(t)
	a, b := seedTenant(t, e.super, "firm"), seedTenant(t, e.super, "firm")
	e.conn(t, a, seedClient(t, e.super, a, "sub A1"), "fake-co-1")
	e.conn(t, a, seedClient(t, e.super, a, "sub A2"), "fake-co-2")
	e.conn(t, b, seedClient(t, e.super, b, "sub B1"), "fake-co-1")

	if err := e.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got := slices.Clone(e.rec.scopes)
	slices.Sort(got)
	if want := []string{"fake-co-1", "fake-co-1", "fake-co-2"}; !slices.Equal(got, want) {
		t.Errorf("scopes = %v, want %v", got, want)
	}
	for _, k := range e.rec.kinds {
		if k != GrantOAuthRefresh {
			t.Errorf("credentials kind = %q, want %q", k, GrantOAuthRefresh)
		}
	}
}

func TestSubscriptions_SkipsInactiveUnregisteredWebhooklessAndLocked(t *testing.T) {
	e := newSubEnv(t)
	a := seedTenant(t, e.super, "firm")
	for i, st := range []string{"pending", "reconsent", "disconnected"} {
		seedConnRow(t, e.super, connRow{tenant: a, entity: seedClient(t, e.super, a, "sub skip "+st), key: "fake-skip-" + st, status: st})
		_ = i
	}
	entO := seedClient(t, e.super, a, "sub other")
	gO := seedGrant(t, e.super, a, "")
	mustExec(t, e.super, "INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id) VALUES ($1, $2, 'other', 'other-1', 'active', $3)", a, entO, gO)
	b := seedTenant(t, e.super, "firm")
	e.conn(t, b, seedClient(t, e.super, b, "sub B"), "fake-locked")
	lockWorkspace(t, e.super, b, uuid.NewString())
	c := seedTenant(t, e.super, "firm")
	e.conn(t, c, seedClient(t, e.super, c, "sub C"), "fake-co-1")

	if err := e.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if want := []string{"fake-co-1"}; !slices.Equal(e.rec.scopes, want) {
		t.Errorf("scopes = %v, want exactly %v", e.rec.scopes, want)
	}

	e.rec.scopes, e.rec.noHooks = nil, true
	if err := e.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce without Webhooks: %v", err)
	}
	if len(e.rec.scopes) != 0 {
		t.Errorf("a connector without Webhooks was called: %v", e.rec.scopes)
	}
}

func TestSubscriptions_OneFailureDoesNotStopTheOthers(t *testing.T) {
	e := newSubEnv(t)
	a := seedTenant(t, e.super, "firm")
	e.conn(t, a, seedClient(t, e.super, a, "sub F1"), "fake-co-1")
	e.conn(t, a, seedClient(t, e.super, a, "sub F2"), "fake-co-2")
	e.rec.failOn = "fake-co-1"

	err := e.keeper.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "vendor down") {
		t.Fatalf("RunOnce err = %v, want one wrapping the failure", err)
	}
	if !slices.Contains(e.rec.scopes, "fake-co-2") {
		t.Errorf("the healthy connection was not called: %v", e.rec.scopes)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(e.buf.String()), "\n") {
		if strings.Contains(l, a) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "level=ERROR") {
		t.Fatalf("log lines for the tenant = %q, want one error line", lines)
	}
	for _, k := range []string{"tenant_id=", "connection_id=", "system=", "err="} {
		if !strings.Contains(lines[0], k) {
			t.Errorf("log line lacks %s: %s", k, lines[0])
		}
	}
	if strings.Contains(lines[0], "fake-co-1") {
		t.Errorf("log line carries the external key: %s", lines[0])
	}
	var keys []string
	for _, m := range regexp.MustCompile(`(?:^| )([a-z_]+)=`).FindAllStringSubmatch(lines[0], -1) {
		keys = append(keys, m[1])
	}
	slices.Sort(keys)
	if want := []string{"connection_id", "err", "level", "msg", "system", "tenant_id", "time"}; !slices.Equal(keys, want) {
		t.Errorf("log keys = %v, want %v", keys, want)
	}
}

func TestSubscriptions_HoldNoConnectionDuringTheVendorCall(t *testing.T) {
	e := newSubEnv(t)
	a := seedTenant(t, e.super, "firm")
	e.conn(t, a, seedClient(t, e.super, a, "sub H1"), "fake-co-1")
	e.conn(t, a, seedClient(t, e.super, a, "sub H2"), "fake-co-2")
	var held []int32
	e.rec.onCall = func() { held = append(held, e.app.Stat().AcquiredConns()) }

	if err := e.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(held) == 0 {
		t.Fatal("no vendor call observed")
	}
	for _, n := range held {
		if n != 0 {
			t.Errorf("%d connection(s) held during EnsureSubscriptions, want 0", n)
		}
	}
}

func TestSubscriptions_NoWebhookConnectorReadsNothing(t *testing.T) {
	reg, err := NewRegistry(&subRecorder{Fake: testFake(t), noHooks: true})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	k := &SubscriptionKeeper{Registry: reg}
	if err := k.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}
