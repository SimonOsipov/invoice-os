package aicalls

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// lines returns the parsed JSON log lines so far.
func (s *syncBuf) lines(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	raw := strings.TrimSpace(s.b.String())
	s.mu.Unlock()
	if raw == "" {
		return nil
	}
	var out []map[string]any
	for _, l := range strings.Split(raw, "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func newLogger() (*slog.Logger, *syncBuf) {
	b := &syncBuf{}
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})), b
}

// wantOneWarn asserts exactly one log line: the not-recorded WARN for c, and no ERROR.
func wantOneWarn(t *testing.T, b *syncBuf, c airecord.Call, wantErrContains string) {
	t.Helper()
	lines := b.lines(t)
	if len(lines) == 0 {
		t.Fatal("no log line, want one WARN \"ai call not recorded\"")
	}
	for _, l := range lines {
		if l["level"] == "ERROR" {
			t.Errorf("ERROR line logged: %v", l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %v", len(lines), lines)
	}
	m := lines[0]
	if m["level"] != "WARN" || m["msg"] != "ai call not recorded" {
		t.Errorf("level/msg = %v/%v, want WARN/ai call not recorded", m["level"], m["msg"])
	}
	if m["client"] != c.Client || m["purpose"] != c.Purpose || m["tenant_id"] != c.TenantID {
		t.Errorf("client/purpose/tenant_id = %v/%v/%v, want %s/%s/%s",
			m["client"], m["purpose"], m["tenant_id"], c.Client, c.Purpose, c.TenantID)
	}
	e, _ := m["err"].(string)
	if e == "" || !strings.Contains(e, wantErrContains) {
		t.Errorf("err = %q, want it to contain %q", e, wantErrContains)
	}
}

type env struct {
	app, super *pgxpool.Pool
}

// newEnv skips without DB env, like internal/notifications.
func newEnv(t *testing.T) env {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("aicalls db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)
	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(super.Close)
	return env{app: app, super: super}
}

// tenants seeds n throwaway tenants and removes them and their rows at test end.
func (e env) tenants(t *testing.T, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
		if _, err := e.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'aicalls throwaway tenant')`, ids[i]); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = e.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE tenant_id = $1`, id)
			_, _ = e.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
		}
	})
	return ids
}

type aiRow struct {
	TenantID, Client, Purpose, Model string
	In, Out                          int64
	Cost                             string
	LatencyMS                        int64
	Attempts                         int
	Outcome                          string
	HasID, HasCreatedAt              bool
}

// rows reads the purpose's rows as the app role under tenant, so RLS applies.
func (e env) rows(t *testing.T, tenant, purpose string) []aiRow {
	t.Helper()
	var out []aiRow
	err := db.WithinTenantTx(context.Background(), e.app, tenant, func(tx pgx.Tx) error {
		rs, err := tx.Query(context.Background(),
			`SELECT tenant_id::text, client, purpose, model, input_tokens, output_tokens, cost::text,
			        latency_ms, attempts, outcome, id IS NOT NULL, created_at IS NOT NULL
			   FROM ai_calls WHERE purpose = $1`, purpose)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r aiRow
			if err := rs.Scan(&r.TenantID, &r.Client, &r.Purpose, &r.Model, &r.In, &r.Out, &r.Cost,
				&r.LatencyMS, &r.Attempts, &r.Outcome, &r.HasID, &r.HasCreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rs.Err()
	})
	if err != nil {
		t.Fatalf("read ai_calls: %v", err)
	}
	return out
}

// total counts the purpose's rows over every tenant, as the superuser.
func (e env) total(t *testing.T, purpose string) int {
	t.Helper()
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM ai_calls WHERE purpose = $1`, purpose).Scan(&n); err != nil {
		t.Fatalf("count ai_calls: %v", err)
	}
	return n
}

func uniquePurpose() string { return "t-" + uuid.NewString() }

// fullCall sets every field to a value distinct from its zero value; tokens and latency exceed int32.
func fullCall(tenant, purpose string) airecord.Call {
	return airecord.Call{
		TenantID: tenant, Client: "ai", Purpose: purpose, Model: "vendor/model-x",
		InputTokens: 3_000_000_000, OutputTokens: 7, Cost: 0.0014741,
		LatencyMS: 123_456_789_012, Attempts: 3, Outcome: "ok",
	}
}

func wantRow(t *testing.T, got aiRow, c airecord.Call, wantCost string) {
	t.Helper()
	want := aiRow{
		TenantID: c.TenantID, Client: c.Client, Purpose: c.Purpose, Model: c.Model,
		In: int64(c.InputTokens), Out: int64(c.OutputTokens), Cost: wantCost,
		LatencyMS: c.LatencyMS, Attempts: c.Attempts, Outcome: c.Outcome, HasID: true, HasCreatedAt: true,
	}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}
}

func TestWrite_StoresOneRowUnderTheCallsTenant(t *testing.T) {
	e := newEnv(t)
	ids := e.tenants(t, 2)
	t1, t2 := ids[0], ids[1]
	logger, buf := newLogger()
	c := fullCall(t1, uniquePurpose())

	NewRecorder(e.app, logger).write(c)

	got := e.rows(t, t1, c.Purpose)
	if len(got) != 1 {
		t.Fatalf("T1 sees %d rows, want 1 (log: %v)", len(got), buf.lines(t))
	}
	wantRow(t, got[0], c, "0.0014741")
	if n := len(e.rows(t, t2, c.Purpose)); n != 0 {
		t.Errorf("T2 sees %d of T1's rows, want 0", n)
	}
	if n := e.total(t, c.Purpose); n != 1 {
		t.Errorf("%d rows in total, want 1", n)
	}
	if l := buf.lines(t); len(l) != 0 {
		t.Errorf("healthy write logged %v", l)
	}
}

func TestWrite_AJevRowWithAnEmptyModelIsStored(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	c := airecord.Call{
		TenantID: t1, Client: "jev", Purpose: uniquePurpose(), Model: "",
		Cost: 0, Attempts: 2, Outcome: "skipped_unavailable",
	}

	NewRecorder(e.app, logger).write(c)

	got := e.rows(t, t1, c.Purpose)
	if len(got) != 1 {
		t.Fatalf("T1 sees %d rows, want 1 (log: %v)", len(got), buf.lines(t))
	}
	wantRow(t, got[0], c, "0")
	if got[0].Model != "" {
		t.Errorf("model = %q, want empty", got[0].Model)
	}
}

func TestWrite_StoresTheCostAsExactNumeric(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	rec := NewRecorder(e.app, logger)
	cases := []struct {
		cost float64
		want string
	}{
		{0.000123456, "0.000123456"},
		{0, "0"},
		{1.2e-5, "0.000012"},
		{0.0014741, "0.0014741"},
		{0.123456789012345, "0.123456789012345"}, // 15 significant digits survive
		{1e21, "1000000000000000000000"},
	}
	for _, tc := range cases {
		c := fullCall(t1, uniquePurpose())
		c.Cost = tc.cost
		rec.write(c)
		got := e.rows(t, t1, c.Purpose)
		if len(got) != 1 {
			t.Errorf("cost %v: %d rows, want 1 (log: %v)", tc.cost, len(got), buf.lines(t))
			continue
		}
		if got[0].Cost != tc.want {
			t.Errorf("cost %v stored as %q, want %q", tc.cost, got[0].Cost, tc.want)
		}
	}
}

func TestWrite_AFloatSumArtifactIsNotStored(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	a, b := 0.0010054, 0.0012224
	var sum float64
	sum += a
	sum += b
	for _, tc := range []struct {
		name string
		cost float64
		want string
	}{
		{"two prod costs", sum, "0.0022278"},
		{"0.1+0.2", func() float64 { x, y := 0.1, 0.2; return x + y }(), "0.3"},
		{"0.1+0.7 differs at the 16th digit", func() float64 { x, y := 0.1, 0.7; return x + y }(), "0.8"},
	} {
		c := fullCall(t1, uniquePurpose())
		c.Cost = tc.cost
		NewRecorder(e.app, logger).write(c)
		got := e.rows(t, t1, c.Purpose)
		if len(got) != 1 {
			t.Errorf("%s: %d rows, want 1 (log: %v)", tc.name, len(got), buf.lines(t))
			continue
		}
		if got[0].Cost != tc.want {
			t.Errorf("%s: stored %q, want %q", tc.name, got[0].Cost, tc.want)
		}
	}
}

// hungPool returns a pool on a listener that accepts connections and never replies, and the accepted count.
func hungPool(t *testing.T) (*pgxpool.Pool, func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-accepted
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@"+ln.Addr().String()+"/d?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(conns)
	}
}

func TestRecord_ReturnsBeforeAHungDatabaseAnswers(t *testing.T) {
	pool, accepted := hungPool(t)

	logger, buf := newLogger()
	rec := NewRecorder(pool, logger)
	rec.timeout = 200 * time.Millisecond
	c := fullCall(uuid.NewString(), uniquePurpose())

	start := time.Now()
	rec.Record(c)
	if d := time.Since(start); d >= 50*time.Millisecond {
		t.Errorf("Record took %v, want < 50ms", d)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(buf.lines(t)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // a second line would show here
	wantOneWarn(t, buf, c, "")
	if accepted() == 0 {
		t.Error("the listener saw no connection: the write never reached the database")
	}
}

func TestRecord_AHungDatabaseGivesUpAtTheTwoSecondDefault(t *testing.T) {
	pool, _ := hungPool(t)
	logger, buf := newLogger()
	c := fullCall(uuid.NewString(), uniquePurpose())

	NewRecorder(pool, logger).Record(c)

	time.Sleep(1500 * time.Millisecond)
	if l := buf.lines(t); len(l) != 0 {
		t.Fatalf("logged before the 2 s default elapsed: %v", l)
	}
	time.Sleep(1500 * time.Millisecond) // t = 3 s
	wantOneWarn(t, buf, c, "")
}

func TestWrite_ADownDatabaseLogsOnceAndInsertsNothing(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/d?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	logger, buf := newLogger()
	c := fullCall(uuid.NewString(), uniquePurpose())

	NewRecorder(pool, logger).write(c)

	wantOneWarn(t, buf, c, "refused")
}

func TestWrite_AMalformedTenantLogsOnce(t *testing.T) {
	e := newEnv(t)
	logger, buf := newLogger()
	c := fullCall("not-a-uuid", uniquePurpose())

	NewRecorder(e.app, logger).write(c)

	wantOneWarn(t, buf, c, db.ErrNoTenant.Error())
	if n := e.total(t, c.Purpose); n != 0 {
		t.Errorf("%d rows inserted, want 0", n)
	}
}

func TestWrite_AnUnknownTenantLogsOnce(t *testing.T) {
	e := newEnv(t)
	logger, buf := newLogger()
	c := fullCall(uuid.NewString(), uniquePurpose())

	NewRecorder(e.app, logger).write(c)

	wantOneWarn(t, buf, c, "23503") // foreign_key_violation
	if n := e.total(t, c.Purpose); n != 0 {
		t.Errorf("%d rows inserted, want 0", n)
	}
}

func TestWrite_ACheckViolationLogsOnce(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	c := fullCall(t1, uniquePurpose())
	c.Client = "x"

	NewRecorder(e.app, logger).write(c)

	wantOneWarn(t, buf, c, "23514") // check_violation, not a missing tenant
	if n := e.total(t, c.Purpose); n != 0 {
		t.Errorf("%d rows inserted, want 0", n)
	}
}

func TestRecord_LandsOneRowOnAHealthyDatabase(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	c := fullCall(t1, uniquePurpose())

	NewRecorder(e.app, logger).Record(c)

	var got []aiRow
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got = e.rows(t, t1, c.Purpose); len(got) > 0 {
			break
		}
	}
	if len(got) != 1 {
		t.Fatalf("T1 sees %d rows within 2s, want 1 (log: %v)", len(got), buf.lines(t))
	}
	wantRow(t, got[0], c, "0.0014741")
	time.Sleep(100 * time.Millisecond) // a duplicate or a late WARN would show here
	if n := e.total(t, c.Purpose); n != 1 {
		t.Errorf("%d rows in total, want 1", n)
	}
	if l := buf.lines(t); len(l) != 0 {
		t.Errorf("healthy Record logged %v", l)
	}
}

func TestRecord_ConcurrentCallsAcrossTwoTenantsEachLandUnderTheirOwnTenant(t *testing.T) {
	e := newEnv(t)
	ids := e.tenants(t, 2)
	logger, buf := newLogger()
	rec := NewRecorder(e.app, logger)
	purpose := uniquePurpose()
	const perTenant = 25

	var wg sync.WaitGroup
	for _, id := range ids {
		for range perTenant {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec.Record(fullCall(id, purpose))
			}()
		}
	}
	wg.Wait()

	for deadline := time.Now().Add(5 * time.Second); e.total(t, purpose) < 2*perTenant && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range ids {
		got := e.rows(t, id, purpose)
		if len(got) != perTenant {
			t.Errorf("tenant %s sees %d rows, want %d (log: %v)", id, len(got), perTenant, buf.lines(t))
		}
		for _, r := range got {
			if r.TenantID != id {
				t.Errorf("tenant %s sees a row of tenant %s", id, r.TenantID)
			}
		}
	}
	if n := e.total(t, purpose); n != 2*perTenant {
		t.Errorf("%d rows in total, want %d", n, 2*perTenant)
	}
	if l := buf.lines(t); len(l) != 0 {
		t.Errorf("healthy concurrent Records logged %v", l)
	}
}

func TestRecord_AfterThePoolClosesLogsOnceAndDoesNotPanic(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	logger, buf := newLogger()
	c := fullCall(t1, uniquePurpose())

	NewRecorder(pool, logger).Record(c)

	for deadline := time.Now().Add(2 * time.Second); len(buf.lines(t)) == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	wantOneWarn(t, buf, c, "closed")
	if n := e.total(t, c.Purpose); n != 0 {
		t.Errorf("%d rows inserted through a closed pool, want 0", n)
	}
}

func TestWait_ReturnsOnlyOnceTheRecordedRowIsPresent(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	logger, buf := newLogger()
	rec := NewRecorder(e.app, logger)
	rec.timeout = 4 * time.Second
	c := fullCall(t1, uniquePurpose())

	rec.Record(c)
	start := time.Now()
	rec.Wait()
	waited := time.Since(start)

	if n := e.total(t, c.Purpose); n != 1 {
		t.Fatalf("%d rows right after Wait, want 1 (log: %v)", n, buf.lines(t))
	}
	if waited > 2*time.Second {
		t.Errorf("Wait took %v after a finished write, want it to return as the write ends, well before the %v timeout", waited, rec.timeout)
	}
}

func TestWait_ReturnsWithinTheTimeoutAgainstAHungDatabase(t *testing.T) {
	pool, _ := hungPool(t)
	logger, _ := newLogger()
	rec := NewRecorder(pool, logger)
	rec.timeout = 200 * time.Millisecond
	rec.Record(fullCall(uuid.NewString(), uniquePurpose()))

	start := time.Now()
	rec.Wait()
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("Wait took %v, want about the 200ms timeout", d)
	}
}
