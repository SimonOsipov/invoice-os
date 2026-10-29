package accountevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// env holds the invoice_app pool the worker uses, a superuser pool for seeding and a River
// client that only inserts. The outbox is global, so each test empties it first (run with -p 1).
type env struct {
	app, admin *pgxpool.Pool
	rc         *river.Client[pgx.Tx]
	tag        string
	tenants    []string
	users      []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	appURL, adminURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("accountevents db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatalf("app pool config: %v", err)
	}
	cfg.MaxConns = 8
	app, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	rc, err := river.NewClient(riverpgxv5.New(app), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	e := &env{app: app, admin: admin, rc: rc, tag: uuid.NewString()[:8]}
	e.mustExec(t, `DELETE FROM account_event_handoffs`)
	t.Cleanup(e.cleanup)
	return e
}

func (e *env) mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("test setup: exec %q: %v", sql, err)
	}
}

func (e *env) mustCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// cleanup removes this test's rows; audit_log is append-only, so it goes in a replica-role transaction.
func (e *env) cleanup() {
	ctx := context.Background()
	_, _ = e.admin.Exec(ctx, `DELETE FROM account_event_handoffs`)
	_, _ = e.admin.Exec(ctx, `DELETE FROM river_job WHERE kind = 'account_audit' AND
		(args->>'user_id' = ANY($1) OR args->>'tenant_id' = ANY($2) OR args->>'ip' LIKE $3)`, e.users, e.tenants, e.tag+"%")
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`)
	_, _ = tx.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id::text = ANY($1) OR actor = ANY($2)`, e.tenants, e.users)
	_, _ = tx.Exec(ctx, `DELETE FROM memberships WHERE tenant_id::text = ANY($1)`, e.tenants)
	_, _ = tx.Exec(ctx, `DELETE FROM tenants WHERE id::text = ANY($1)`, e.tenants)
	_ = tx.Commit(ctx)
}

func (e *env) tenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	e.mustExec(t, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, "AUDIT-13-03 "+e.tag)
	e.tenants = append(e.tenants, id)
	return id
}

func (e *env) user() string {
	u := uuid.NewString()
	e.users = append(e.users, u)
	return u
}

func (e *env) email(prefix string) string {
	return fmt.Sprintf("%s-%s@corp.example.test", prefix, e.tag)
}

// member seeds a membership with the given status and email (nil for none).
func (e *env) member(t *testing.T, tenant, user, status string, email any) {
	t.Helper()
	e.mustExec(t, `INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email, all_clients)
		VALUES ($1, $2, 'admin', $3, 'Test Member', $4, true)`, tenant, user, status, email)
}

// enqueue writes one outbox row as the gateway would; userID and email may be nil.
func (e *env) enqueue(t *testing.T, event string, userID, email any, ip, ua string, at time.Time) {
	t.Helper()
	e.mustExec(t, `INSERT INTO account_event_handoffs (event, user_id, email, ip, user_agent, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, event, userID, email, ip, ua, at)
}

func (e *env) outbox(t *testing.T) int {
	t.Helper()
	return e.mustCount(t, `SELECT count(*) FROM account_event_handoffs`)
}

// jobs reads the account_audit jobs whose ip starts with this test's tag or whose user is given.
func (e *env) jobs(t *testing.T, users ...string) []AuditArgs {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT args FROM river_job WHERE kind = 'account_audit' AND (args->>'user_id' = ANY($1) OR args->>'ip' LIKE $2) ORDER BY id`,
		users, e.tag+"%")
	if err != nil {
		t.Fatalf("read account_audit jobs: %v", err)
	}
	defer rows.Close()
	var out []AuditArgs
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var a AuditArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("decode job args %s: %v", raw, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func tenantsOf(jobs []AuditArgs) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.TenantID)
	}
	sort.Strings(out)
	return out
}

func sorted(s ...string) []string {
	sort.Strings(s)
	return s
}

// capture returns a logger that keeps every record as a decoded JSON object, plus the raw text.
func capture() (*slog.Logger, func() []map[string]any, func() string) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	records := func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				panic(err)
			}
			out = append(out, m)
		}
		return out
	}
	return log, records, buf.String
}

func atLevel(records []map[string]any, level string) []map[string]any {
	var out []map[string]any
	for _, r := range records {
		if r["level"] == level {
			out = append(out, r)
		}
	}
	return out
}

func mentions(r map[string]any, s string) bool {
	b, _ := json.Marshal(r)
	return strings.Contains(string(b), s)
}
