package erp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// sqlTracer calls onStart before a statement is sent and onEnd once it has finished.
type sqlTracer struct {
	onStart func(sql string)
	onEnd   func(sql string)
}

type sqlKey struct{}

func (s sqlTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if s.onStart != nil {
		s.onStart(d.SQL)
	}
	return context.WithValue(ctx, sqlKey{}, d.SQL)
}

func (s sqlTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if s.onEnd != nil {
		sql, _ := ctx.Value(sqlKey{}).(string)
		s.onEnd(sql)
	}
}

// lossyConn fails its next Read once lost is set, after the server has had time to apply what was written.
type lossyConn struct {
	net.Conn
	lost *atomic.Bool
}

func (c lossyConn) Read(p []byte) (int, error) {
	if c.lost.CompareAndSwap(true, false) {
		time.Sleep(300 * time.Millisecond)
		return 0, io.ErrUnexpectedEOF
	}
	return c.Conn.Read(p)
}

// tracedAppPool is the app pool with a tracer and, when lost is set, a dialer whose connections can lose a reply.
func tracedAppPool(t *testing.T, tr pgx.QueryTracer, lost *atomic.Bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("erp store test skipped: DATABASE_URL unset")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = tr
	if lost != nil {
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return lossyConn{Conn: c, lost: lost}, nil
		}
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestRefresh_FinalAttemptReconsentsWhenTheJobContextIsDead(t *testing.T) {
	r := newRefreshRig(t)
	g, _ := r.grant(t)
	e := seedClient(t, r.super, r.tenant, "One")
	c := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e, key: "k", status: "active", grant: g})
	r.stub.fn = func(Credentials) ([]byte, error) { return nil, errors.New("vendor down") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // River's job timeout or a worker shutdown ended attempt 8
	err := r.worker.Work(ctx, &river.Job[RefreshArgs]{
		JobRow: &rivertype.JobRow{ID: 1101, Kind: "erp_token_refresh", Attempt: 8, MaxAttempts: 8},
		Args:   RefreshArgs{TenantID: r.tenant, GrantID: g, Version: 1},
	})
	if err == nil || isCancel(err) {
		t.Fatalf("Work = %v, want the attempt's error", err)
	}
	if s := stateOf(t, r.super, c); s.status != "reconsent" {
		t.Errorf("connection is %s, want reconsent", s.status)
	}
	if f := r.events(t, "erp.token.refresh_failed"); len(f) != 1 || f[0].payload["reason"] != "exhausted" {
		t.Errorf("refresh_failed = %+v, want one exhausted", f)
	}
	if n := len(r.events(t, "erp.connection.reauth_required")); n != 1 {
		t.Errorf("%d reauth_required, want 1", n)
	}
}

func TestStore_ReauthoriseWhileAnotherKeylessPendingExistsIsRefused(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")

	a, _, _, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := store.Disconnect(admin, e1, a.ID); err != nil {
		t.Fatalf("disconnect A: %v", err)
	}
	b, _, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil || !created || b.ID == a.ID {
		t.Fatalf("connect B = %+v created %v err %v, want a new row", b, created, err)
	}
	before := erpState(t, super, tenant)

	_, _, _, err = store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: a.ID})
	wantErr(t, "re-authorise A beside pending B", err, ErrKeylessPending)
	if after := erpState(t, super, tenant); after != before {
		t.Errorf("a refused re-authorise changed state:\nbefore %s\nafter  %s", before, after)
	}
}

func TestStore_ConnectRetriesWhenTheConflictingRowIsGoneBeforeTheSelect(t *testing.T) {
	super, _ := storePools(t)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	p := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, status: "pending"})
	g := seedGrant(t, super, tenant, "")
	var once sync.Once
	pool := tracedAppPool(t, sqlTracer{onEnd: func(sql string) {
		if !strings.Contains(sql, "ON CONFLICT") {
			return
		}
		// A callback activates the conflicting row between the insert and the select.
		once.Do(func() {
			mustExec(t, super, "UPDATE erp_connections SET status = 'active', external_key = 'k', grant_id = $2 WHERE id = $1", p, g)
		})
	}}, nil)
	store := storeOver(t, pool, nil, NewFake(testRedirect))

	conn, _, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect = %v, want a new pending row", err)
	}
	if !created || conn.ID == p || conn.Status != "pending" {
		t.Errorf("Connect = %s %s created %v, want a new pending row beside active %s", conn.ID, conn.Status, created, p)
	}
}

func TestComplete_CommitAppliedButReportedLostKeepsTheSecret(t *testing.T) {
	r := newCompleteRig(t)
	lost := &atomic.Bool{}
	var commits atomic.Int32
	pool := tracedAppPool(t, sqlTracer{onStart: func(sql string) {
		if sql == "commit" && commits.Add(1) == 2 { // tx1 is the first, tx2 the second
			lost.Store(true)
		}
	}}, lost)
	store := storeOver(t, pool, r.sec, r.ex)
	conn, state := r.connect(t, r.e1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := store.Complete(ctx, CallbackInput{State: state, Code: "fake-co-1"})
	wantOutcome(t, "commit applied", string(out), err, "connected")

	if s := stateOf(t, r.super, conn.ID); s.status != "active" || s.grant == nil {
		t.Fatalf("connection = %+v, want active with a grant", s)
	}
	if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 1 {
		t.Errorf("store holds %v, want the active grant's secret kept", ids)
	}
	if _, deleted := r.sec.counts(); deleted != 0 {
		t.Errorf("%d secret deletes, want 0", deleted)
	}
}

func TestComplete_Tx2ErrorKeepsTheSecretWhenTheReReadFails(t *testing.T) {
	r := newCompleteRig(t)
	logs := captureLogs(t)
	pool := tracedAppPool(t, sqlTracer{}, nil)
	store := storeOver(t, pool, r.sec, r.ex)
	pool.Close()
	ref := SecretName("", "grant-1")
	if err := r.sec.Create(context.Background(), ref, []byte("payload-1")); err != nil {
		t.Fatal(err)
	}

	if store.settleTx2Error(context.Background(), r.tenant, "9d9c2b3e-0000-4000-8000-000000000001", "grant-1", ref) {
		t.Error("settle reported a commit it could not read")
	}
	if !holds(r.sec, ref) {
		t.Error("the secret was deleted although the re-read failed")
	}
	if strings.Contains(logs.text(), "payload-1") {
		t.Error("a log line carries the secret payload")
	}
}

func TestComplete_Tx2NotCommittedByActivateEndedWhenAdminOrWorkspaceChanged(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, r *completeRig)
	}{
		{"the admin was demoted", func(t *testing.T, r *completeRig) {
			mustExec(t, r.super, "UPDATE memberships SET role = 'preparer' WHERE tenant_id = $1 AND user_id = $2", r.tenant, subjectOf(r.admin))
		}},
		{"the admin was suspended", func(t *testing.T, r *completeRig) {
			mustExec(t, r.super, "UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2", r.tenant, subjectOf(r.admin))
		}},
		{"the workspace is scheduled for deletion", func(t *testing.T, r *completeRig) {
			lockWorkspace(t, r.super, r.tenant, subjectOf(r.admin))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCompleteRig(t)
			conn, state := r.connect(t, r.e1)
			// The change lands between tx1 and tx2, while the vendor exchange runs.
			r.ex.override = func(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
				c.mutate(t, r)
				return r.ex.Fake.Exchange(ctx, req)
			}

			out, err := r.complete(state, "fake-co-1", "")
			wantOutcome(t, c.name, out, err, "ended")

			r.wantPending(t, c.name, conn.ID, nil)
			if n := grantCount(t, r.super, r.tenant); n != 0 {
				t.Errorf("%d grants, want 0", n)
			}
			if ids := secretIDs(r.sec.MemorySecrets); len(ids) != 0 {
				t.Errorf("store holds %v, want the new secret deleted", ids)
			}
			if n := eventCount(t, r.super, r.tenant, "erp.connection.authorised"); n != 0 {
				t.Errorf("%d erp.connection.authorised events, want 0", n)
			}
		})
	}
}
