package workspacepurge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

const (
	warnNoEmail  = "workspace purge: a member had no usable email; erased by user id"
	infoPurged   = "workspace purge: purged"
	errTenantMsg = "workspace purge: tenant failed"
	eraseKind    = "person_erase"                                // notifications.EraseArgs.Kind
	eraseByID    = "person_erase_by_id"                          // notifications.EraseByIDArgs.Kind
	creatorNS    = "83584a9e-a526-4186-9fbb-508011cac1cc"        // tenancy.workspaceNamespace
	workerActorN = "https://ascomply.com/actors/workspace-purge" // workerActor's name
)

// longMail is 255 bytes: one over what notifications.UsableEraseEmail accepts.
var longMail = strings.Repeat("a", 239) + "@example.invalid"

// memStore is a map-backed document.ObjectStore. A hook runs before each List and
// DeleteMany; failDelete removes the first key of a batch and then fails.
type memStore struct {
	mu          sync.Mutex
	keys        map[string]struct{}
	lists       int
	deleteCalls int
	ops         []string // the tenant id of each List and DeleteMany, in call order
	onList      func(ctx context.Context, prefix string)
	onDelete    func(keys []string)
	failDelete  func(keys []string) bool
}

var _ document.ObjectStore = (*memStore)(nil)

func tenantOfKey(key string) string {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (s *memStore) Put(_ context.Context, key string, _ io.ReadSeeker, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key] = struct{}{}
	return nil
}

func (s *memStore) Get(context.Context, string, string) (document.Object, error) {
	return document.Object{}, errors.New("memStore: Get is not used")
}

func (s *memStore) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	s.lists++
	s.ops = append(s.ops, tenantOfKey(prefix))
	hook := s.onList
	s.mu.Unlock()
	if hook != nil {
		hook(ctx, prefix)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.under(prefix), nil
}

func (s *memStore) DeleteMany(_ context.Context, keys []string) error {
	s.mu.Lock()
	s.deleteCalls++
	if len(keys) > 0 {
		s.ops = append(s.ops, tenantOfKey(keys[0]))
	}
	hook, fail := s.onDelete, s.failDelete
	s.mu.Unlock()
	if hook != nil {
		hook(keys)
	}
	if fail != nil && len(keys) > 0 && fail(keys) {
		s.mu.Lock()
		delete(s.keys, keys[0])
		s.mu.Unlock()
		return errors.New("forced delete failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.keys, k)
	}
	return nil
}

// under returns up to 1,000 keys under prefix, sorted.
func (s *memStore) under(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out
}

func (s *memStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

func (s *memStore) deletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCalls
}

func (s *memStore) opsFor(tenants ...string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, o := range s.ops {
		if slices.Contains(tenants, o) {
			out = append(out, o)
		}
	}
	return out
}

// fakeAccounts is the AccountReader; the default answer is an error, so an unplanned read fails the run.
type fakeAccounts struct {
	mu    sync.Mutex
	calls []string
	fn    func(userID string) (string, error)
}

func (f *fakeAccounts) UserEmail(_ context.Context, userID string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, userID)
	fn := f.fn
	f.mu.Unlock()
	if fn == nil {
		return "", errors.New("fakeAccounts: unplanned read")
	}
	return fn(userID)
}

func (f *fakeAccounts) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type logRec struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (l logRec) text() string {
	var b strings.Builder
	b.WriteString(l.msg)
	for k, v := range l.attrs {
		fmt.Fprintf(&b, " %s=%s", k, v)
	}
	return b.String()
}

// logSink collects slog records.
type logSink struct {
	mu   sync.Mutex
	recs []logRec
}

type sinkHandler struct {
	sink  *logSink
	attrs []slog.Attr
}

func (h sinkHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h sinkHandler) Handle(_ context.Context, r slog.Record) error {
	rec := logRec{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	for _, a := range h.attrs {
		rec.attrs[a.Key] = a.Value.Resolve().String()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Resolve().String()
		return true
	})
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.recs = append(h.sink.recs, rec)
	return nil
}

func (h sinkHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return sinkHandler{sink: h.sink, attrs: append(slices.Clone(h.attrs), as...)}
}

func (h sinkHandler) WithGroup(string) slog.Handler { return h }

func (s *logSink) logger() *slog.Logger { return slog.New(sinkHandler{sink: s}) }

func (s *logSink) all() []logRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.recs)
}

func (s *logSink) find(level slog.Level, msg string) []logRec {
	var out []logRec
	for _, r := range s.all() {
		if r.level == level && r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// rig is one test's pools, fakes and seeds. Cleanup removes every tenant, erasure and job it made.
type rig struct {
	t        *testing.T
	super    *pgxpool.Pool // postgres: seed, read back, clean up
	app      *pgxpool.Pool // invoice_app: what the worker runs as
	reader   *pgxpool.Pool // invoice_tenant_reader
	river    *river.Client[pgx.Tx]
	store    *memStore
	accounts *fakeAccounts
	logs     *logSink
	users    []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	appURL, superURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || superURL == "" || readerURL == "" {
		t.Skip("workspacepurge db-integration test skipped: set DATABASE_URL, DATABASE_SUPERUSER_URL and DATABASE_READER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	open := func(url string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatalf("pool config: %v", err)
		}
		cfg.MaxConns = 16
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("pool: %v", err)
		}
		t.Cleanup(p.Close)
		return p
	}
	r := &rig{t: t, super: open(superURL), app: open(appURL), reader: open(readerURL),
		store: &memStore{keys: map[string]struct{}{}}, accounts: &fakeAccounts{}, logs: &logSink{}}
	rc, err := river.NewClient(riverpgxv5.New(r.app), &river.Config{}) // insert-only
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	r.river = rc
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM river_job WHERE kind IN ('person_erase', 'person_erase_by_id') AND args->>'user_id' = ANY($1::text[])`,
			`DELETE FROM person_erasures WHERE user_id = ANY($1::uuid[])`,
		} {
			if _, err := r.super.Exec(context.Background(), q, r.users); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return r
}

func (r *rig) purger() *Purger {
	p := &Purger{Pool: r.app, Reader: r.reader, River: r.river, Objects: r.store, Logger: r.logs.logger()}
	if r.accounts != nil { // a nil *fakeAccounts in the interface would not be a nil reader
		p.Accounts = r.accounts
	}
	return p
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.super.Exec(context.Background(), q, args...); err != nil {
		r.t.Fatalf("exec %q: %v", q, err)
	}
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.super.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		r.t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// tenant seeds an unscheduled firm tenant and removes it, with its audit_log and staff_audit_log rows, on cleanup.
func (r *rig) tenant() string {
	r.t.Helper()
	id := uuid.NewString()
	r.exec(`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, "Purge "+id[:8])
	r.removeOnCleanup(id)
	return id
}

// removeOnCleanup runs purge_tenant (it removes the tenant's audit_log rows) and then deletes the tenant's
// append-only staff rows as the superuser under 'replica', the way internal/audit's tests do.
func (r *rig) removeOnCleanup(id string) {
	r.t.Cleanup(func() {
		ctx := context.Background()
		if err := db.WithinTenantTx(ctx, r.app, id, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT public.purge_tenant($1::uuid)`, id)
			return err
		}); err != nil {
			r.t.Errorf("cleanup: purge_tenant %s: %v", id, err)
		}
		tx, err := r.super.Begin(ctx)
		if err != nil {
			r.t.Errorf("cleanup: begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{`SET LOCAL session_replication_role = 'replica'`, `DELETE FROM staff_audit_log WHERE tenant_id = '` + id + `'`} {
			if _, err := tx.Exec(ctx, q); err != nil {
				r.t.Errorf("cleanup: %s: %v", q, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			r.t.Errorf("cleanup: commit: %v", err)
		}
	})
}

// scheduleAt sets the schedule from a SQL time expression; the trigger derives the due time (30 days later).
func (r *rig) scheduleAt(tenant, expr string) {
	r.t.Helper()
	r.exec(`UPDATE tenants SET deletion_scheduled_at = `+expr+`, deletion_scheduled_by = gen_random_uuid() WHERE id = $1`, tenant)
}

func (r *rig) due(tenant string) string {
	r.t.Helper()
	r.scheduleAt(tenant, "now() - interval '31 days'")
	return tenant
}

func (r *rig) user() string {
	id := uuid.NewString()
	r.users = append(r.users, id)
	return id
}

// users returns n new user ids in ascending order, the order the member read returns them in.
func (r *rig) sortedUsers(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = r.user()
	}
	sort.Strings(ids)
	return ids
}

func (r *rig) mail(prefix string) string {
	return fmt.Sprintf("%s-%s@example.invalid", prefix, uuid.NewString()[:8])
}

func ptr(s string) *string { return &s }

func (r *rig) member(tenant, user, role, status string, email *string, name string) {
	r.t.Helper()
	var dn *string
	if name != "" {
		dn = &name
	}
	r.exec(`INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, $3, $4, $5, $6)`,
		tenant, user, role, status, dn, email)
}

// put stores n objects under the tenant's prefix and returns their keys.
func (r *rig) put(tenant string, n int) []string {
	keys := make([]string, n)
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for i := range keys {
		keys[i] = "tenants/" + tenant + "/" + uuid.NewString()
		r.store.keys[keys[i]] = struct{}{}
	}
	return keys
}

func (r *rig) objects(tenant string) int { return len(r.store.under("tenants/" + tenant + "/")) }

func (r *rig) tenantRows(tenant string) int {
	return r.count(`SELECT count(*) FROM tenants WHERE id = $1`, tenant)
}

func (r *rig) memberRows(tenant string) int {
	return r.count(`SELECT count(*) FROM memberships WHERE tenant_id = $1`, tenant)
}

func (r *rig) erasures(users ...string) int {
	return r.count(`SELECT count(*) FROM person_erasures WHERE user_id = ANY($1::uuid[])`, users)
}

func (r *rig) jobs(kind string, users ...string) int {
	return r.count(`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'user_id' = ANY($2::text[])`, kind, users)
}

func (r *rig) marker(tenant string) *time.Time {
	r.t.Helper()
	var at *time.Time
	if err := r.super.QueryRow(context.Background(), `SELECT deletion_purge_started_at FROM tenants WHERE id = $1`, tenant).Scan(&at); err != nil {
		r.t.Fatalf("read the marker: %v", err)
	}
	return at
}

// isDue reports whether the tenant row exists and its due time has passed.
func (r *rig) isDue(tenant string) bool {
	return r.count(`SELECT count(*) FROM tenants WHERE id = $1 AND deletion_due_at <= now()`, tenant) == 1
}

func (r *rig) dueAt(tenant string) time.Time {
	r.t.Helper()
	var at time.Time
	if err := r.super.QueryRow(context.Background(), `SELECT deletion_due_at FROM tenants WHERE id = $1`, tenant).Scan(&at); err != nil {
		r.t.Fatalf("read deletion_due_at: %v", err)
	}
	return at
}

type staffRow struct {
	Actor, Event string
	TenantID     *string
	SubjectID    *string
	Raw          string
	Payload      map[string]any
}

func (r *rig) staff(tenant string) []staffRow {
	r.t.Helper()
	rows, err := r.super.Query(context.Background(),
		`SELECT actor::text, event, tenant_id::text, subject_id::text, payload::text FROM staff_audit_log WHERE tenant_id = $1 ORDER BY id`, tenant)
	if err != nil {
		r.t.Fatalf("read staff_audit_log: %v", err)
	}
	defer rows.Close()
	var out []staffRow
	for rows.Next() {
		var s staffRow
		if err := rows.Scan(&s.Actor, &s.Event, &s.TenantID, &s.SubjectID, &s.Raw); err != nil {
			r.t.Fatalf("scan staff row: %v", err)
		}
		if err := json.Unmarshal([]byte(s.Raw), &s.Payload); err != nil {
			r.t.Fatalf("decode payload %s: %v", s.Raw, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatalf("read staff_audit_log: %v", err)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// requireStepErr asserts the error format "workspace purge: tenant <id>: <step>: <cause>".
func requireStepErr(t *testing.T, err error, tenant, step string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want a %q step error for tenant %s", step, tenant)
	}
	if want := "workspace purge: tenant " + tenant + ": " + step + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %q, want the prefix %q", err, want)
	}
}

func requireResult(t *testing.T, got Result, err error, want Result) {
	t.Helper()
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got != want {
		t.Fatalf("Result = %+v, want %+v", got, want)
	}
}

// adminCtx is a signed-in admin of the tenant, as the cancel route sees them.
func adminCtx(tenant, user string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func TestPurge_ErasesTheMembersDeletesTheFilesAndPurgesTheRows(t *testing.T) {
	r := newRig(t)
	a, b := r.due(r.tenant()), r.tenant()
	ids := r.sortedUsers(5)
	admin, prep, invited, gone, goneWithMail := ids[0], ids[1], ids[2], ids[3], ids[4]
	adminMail, prepMail, invitedMail := "  Ada.Obi-"+uuid.NewString()[:8]+"@Example.Invalid ", r.mail("prep"), r.mail("inv")
	r.member(a, admin, "admin", "active", &adminMail, "Ada Obi")
	r.member(a, prep, "preparer", "suspended", &prepMail, "")
	r.member(a, invited, "reviewer", "invited", &invitedMail, "")
	r.member(a, gone, "preparer", "removed", nil, "")
	r.member(a, goneWithMail, "preparer", "removed", ptr(r.mail("gone")), "")
	bUser := r.user()
	bMail := r.mail("b")
	r.member(b, bUser, "admin", "active", &bMail, "")
	c, cUser, cMail := r.due(r.tenant()), r.user(), r.mail("c") // due too, but Purge(A) is not about C
	r.member(c, cUser, "admin", "active", &cMail, "")
	r.put(a, 3)
	bKeys := r.put(b, 1)
	cKeys := r.put(c, 1)
	for _, tn := range []string{a, b} {
		r.exec(`INSERT INTO business_entities (id, tenant_id, name) VALUES (gen_random_uuid(), $1, 'Entity')`, tn)
	}

	got, err := r.purger().Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 3, Objects: 3})

	for _, c := range []struct{ user, email string }{
		{admin, strings.ToLower(strings.TrimSpace(adminMail))}, {prep, prepMail}, {invited, invitedMail},
	} {
		var email *string
		if err := r.super.QueryRow(context.Background(), `SELECT email FROM person_erasures WHERE user_id = $1`, c.user).Scan(&email); err != nil {
			t.Fatalf("erasure row of %s: %v", c.user, err)
		}
		if email == nil || *email != c.email {
			t.Errorf("erasure email of %s = %v, want %q", c.user, email, c.email)
		}
		if n := r.jobs(eraseKind, c.user); n != 1 {
			t.Errorf("%s jobs of %s = %d, want 1", eraseKind, c.user, n)
		}
	}
	if n := r.erasures(gone, goneWithMail); n != 0 {
		t.Errorf("erasure rows of removed members = %d, want 0", n)
	}
	if n := r.jobs(eraseKind, gone, goneWithMail) + r.jobs(eraseByID, gone, goneWithMail); n != 0 {
		t.Errorf("jobs of removed members = %d, want 0", n)
	}
	if calls := r.accounts.got(); len(calls) != 0 {
		t.Errorf("account reads = %v, want none: every non-removed member had a usable email", calls)
	}
	if n := r.objects(a); n != 0 {
		t.Errorf("objects left under A = %d, want 0", n)
	}
	for _, c := range []struct {
		what string
		n    int
	}{
		{"tenants", r.tenantRows(a)}, {"memberships", r.memberRows(a)},
		{"business_entities", r.count(`SELECT count(*) FROM business_entities WHERE tenant_id = $1`, a)},
	} {
		if c.n != 0 {
			t.Errorf("A's %s rows = %d, want 0", c.what, c.n)
		}
	}

	// The other tenant: rows, member, file and entity stay; nobody of it is erased.
	if r.tenantRows(b) != 1 || r.memberRows(b) != 1 || r.count(`SELECT count(*) FROM business_entities WHERE tenant_id = $1`, b) != 1 {
		t.Error("tenant B lost its tenant, member or entity row")
	}
	if left := r.store.under("tenants/" + b + "/"); !slices.Equal(left, bKeys) {
		t.Errorf("B's objects = %v, want %v", left, bKeys)
	}
	if r.erasures(bUser) != 0 || r.jobs(eraseKind, bUser) != 0 {
		t.Error("B's member has an erasure row or job")
	}
	if r.marker(b) != nil {
		t.Error("B has a purge marker")
	}

	// A tenant that is due as well waits for its own Purge.
	if r.tenantRows(c) != 1 || r.memberRows(c) != 1 || r.marker(c) != nil || !r.isDue(c) {
		t.Error("due tenant C was touched: its row, member, marker or due time changed")
	}
	if r.erasures(cUser) != 0 || r.jobs(eraseKind, cUser) != 0 || len(r.staff(c)) != 0 {
		t.Error("due tenant C's member has an erasure row or job, or C has a staff row")
	}
	if left := r.store.under("tenants/" + c + "/"); !slices.Equal(left, cKeys) {
		t.Errorf("C's objects = %v, want %v", left, cKeys)
	}
}

func TestPurge_RunsTheStepsInOrder(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	admin, mail := r.user(), r.mail("ord")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 2)

	type seen struct{ erasures, tenants int }
	var first *seen
	var atDelete []struct{ tenants, staff, audit int }
	r.store.onList = func(context.Context, string) {
		if first == nil {
			first = &seen{r.erasures(admin), r.tenantRows(a)}
		}
	}
	r.store.onDelete = func([]string) {
		atDelete = append(atDelete, struct{ tenants, staff, audit int }{
			r.tenantRows(a), len(r.staff(a)), r.count(`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, a)})
	}

	got, err := r.purger().Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 2})

	if first == nil {
		t.Fatal("the file step never listed")
	}
	if first.erasures != 1 || first.tenants != 1 {
		t.Errorf("at the first List: committed erasure rows = %d, tenant rows = %d; want 1 and 1", first.erasures, first.tenants)
	}
	if len(atDelete) == 0 {
		t.Fatal("the file step never deleted")
	}
	for i, s := range atDelete {
		if s.tenants != 1 || s.staff != 0 || s.audit != 0 {
			t.Errorf("at DeleteMany %d: tenant rows = %d, staff rows = %d, audit rows = %d; want 1, 0, 0 (the purge comes after the files)", i, s.tenants, s.staff, s.audit)
		}
	}
	if r.objects(a) != 0 || r.tenantRows(a) != 0 {
		t.Errorf("after Purge: objects = %d, tenant rows = %d; want 0 and 0", r.objects(a), r.tenantRows(a))
	}
}

func TestPurge_WritesOneStaffRowWithNoPersonalData(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	admin, mail := r.user(), r.mail("staff")
	r.member(a, admin, "admin", "active", &mail, "Adaeze Obi")
	r.put(a, 2)
	var scheduledBy string
	if err := r.super.QueryRow(context.Background(), `SELECT deletion_scheduled_by::text FROM tenants WHERE id = $1`, a).Scan(&scheduledBy); err != nil {
		t.Fatal(err)
	}
	dueAt := r.dueAt(a)

	got, err := r.purger().Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 2})

	rows := r.staff(a)
	if len(rows) != 1 {
		t.Fatalf("staff rows = %d, want 1", len(rows))
	}
	s := rows[0]
	if s.Event != "workspace.purged" {
		t.Errorf("event = %q, want workspace.purged", s.Event)
	}
	if s.TenantID == nil || *s.TenantID != a {
		t.Errorf("tenant_id = %v, want %s", s.TenantID, a)
	}
	if want := uuid.NewSHA1(uuid.NameSpaceURL, []byte(workerActorN)).String(); s.Actor != want || s.Actor != workerActor.String() {
		t.Errorf("actor = %s, want workerActor %s", s.Actor, want)
	}
	if s.SubjectID != nil {
		t.Errorf("subject_id = %v, want NULL", *s.SubjectID)
	}
	if !slices.Equal(sortedKeys(s.Payload), []string{"due_at", "members", "objects"}) {
		t.Errorf("payload keys = %v, want exactly [due_at members objects]", sortedKeys(s.Payload))
	}
	if s.Payload["due_at"] != dueAt.UTC().Format(time.RFC3339) || s.Payload["members"] != float64(1) || s.Payload["objects"] != float64(2) {
		t.Errorf("payload = %v, want due_at %s, members 1, objects 2", s.Payload, dueAt.UTC().Format(time.RFC3339))
	}
	for _, leak := range []string{mail, "Adaeze", "Obi", admin, scheduledBy, "@"} {
		if strings.Contains(s.Raw, leak) {
			t.Errorf("payload %s holds %q", s.Raw, leak)
		}
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, a); n != 0 {
		t.Errorf("audit_log rows of the purged tenant = %d, want 0", n)
	}
}

func TestPurge_ATenantThatIsNotDueIsUntouched(t *testing.T) {
	type snap struct {
		Rows, Members int
		Sched, Due    *time.Time
		Marker        *time.Time
	}
	read := func(r *rig, id string) snap {
		s := snap{Rows: r.tenantRows(id), Members: r.memberRows(id)}
		if s.Rows == 1 {
			if err := r.super.QueryRow(context.Background(),
				`SELECT deletion_scheduled_at, deletion_due_at, deletion_purge_started_at FROM tenants WHERE id = $1`, id).Scan(&s.Sched, &s.Due, &s.Marker); err != nil {
				r.t.Fatal(err)
			}
		}
		return s
	}
	seed := func(r *rig, id string) {
		u, m := r.user(), r.mail("nd")
		r.member(id, u, "admin", "active", &m, "")
		r.put(id, 1)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, r *rig) string
		seen  bool // the tenant row exists, so the controls below apply
	}{
		{"scheduled 29 days ago", func(t *testing.T, r *rig) string {
			id := r.tenant()
			r.scheduleAt(id, "now() - interval '29 days'")
			seed(r, id)
			return id
		}, true},
		{"unscheduled", func(t *testing.T, r *rig) string {
			id := r.tenant()
			seed(r, id)
			return id
		}, true},
		{"a tenant that does not exist", func(t *testing.T, r *rig) string { return uuid.NewString() }, false},
		{"already purged", func(t *testing.T, r *rig) string {
			id := r.due(r.tenant())
			seed(r, id)
			got, err := r.purger().Purge(context.Background(), id)
			requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 1})
			return id
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			id := c.setup(t, r)
			before, erasedBefore := read(r, id), r.count(`SELECT count(*) FROM person_erasures`)
			staffBefore, deletesBefore, objsBefore := len(r.staff(id)), r.store.deletes(), r.store.size()
			if c.seen && (before.Rows != 1 || before.Members != 1 || objsBefore != 1) {
				t.Fatalf("control: seeded %+v with %d objects, want one tenant, one member, one object", before, objsBefore)
			}
			if c.name == "already purged" && staffBefore != 1 {
				t.Fatalf("control: staff rows after the first purge = %d, want 1", staffBefore)
			}

			got, err := r.purger().Purge(context.Background(), id)
			requireResult(t, got, err, Result{})

			if after := read(r, id); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("tenant after = %+v, before = %+v; want unchanged", after, before)
			}
			if n := r.count(`SELECT count(*) FROM person_erasures`); n != erasedBefore {
				t.Errorf("erasure rows %d -> %d, want unchanged", erasedBefore, n)
			}
			if n := r.store.deletes(); n != deletesBefore {
				t.Errorf("DeleteMany calls %d -> %d, want unchanged", deletesBefore, n)
			}
			if n := r.store.size(); n != objsBefore {
				t.Errorf("stored objects %d -> %d, want unchanged", objsBefore, n)
			}
			if n := len(r.staff(id)); n != staffBefore {
				t.Errorf("staff rows %d -> %d, want unchanged", staffBefore, n)
			}
		})
	}
}

func TestPurge_ACancelBeforeTheLockWins(t *testing.T) {
	r := newRig(t)
	a := r.tenant()
	admin, mail := r.user(), r.mail("cancel")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 1)
	r.scheduleAt(a, "now() - interval '29 days 23 hours 59 minutes 58 seconds'") // due in 2 s

	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseX := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseX)
	xDone := make(chan error, 1)
	go func() {
		xDone <- db.WithinTenantTx(context.Background(), r.app, a, func(tx pgx.Tx) error {
			ctx := context.Background()
			var due *time.Time
			if err := tx.QueryRow(ctx, `SELECT deletion_due_at FROM tenants FOR UPDATE`).Scan(&due); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL`); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-xDone:
		t.Fatalf("the cancel transaction ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel transaction never locked the row")
	}
	time.Sleep(2500 * time.Millisecond)
	if !r.isDue(a) {
		t.Fatal("control: the tenant is not past its due time")
	}

	type res struct {
		got Result
		err error
	}
	pDone := make(chan res, 1)
	go func() {
		got, err := r.purger().Purge(context.Background(), a)
		pDone <- res{got, err}
	}()
	select {
	case p := <-pDone:
		t.Fatalf("Purge returned %+v, %v while the cancel held the row", p.got, p.err)
	case <-time.After(750 * time.Millisecond):
	}
	releaseX()
	if err := <-xDone; err != nil {
		t.Fatalf("the cancel transaction: %v", err)
	}
	select {
	case p := <-pDone:
		requireResult(t, p.got, p.err, Result{})
	case <-time.After(15 * time.Second):
		t.Fatal("Purge did not return after the cancel committed")
	}
	if n := r.erasures(admin); n != 0 {
		t.Errorf("erasure rows = %d, want 0", n)
	}
	if r.tenantRows(a) != 1 || r.memberRows(a) != 1 || r.objects(a) != 1 || r.store.deletes() != 0 {
		t.Error("the cancelled workspace lost its tenant, member or file")
	}
	if r.count(`SELECT count(*) FROM tenants WHERE id = $1 AND deletion_scheduled_at IS NULL AND deletion_due_at IS NULL`, a) != 1 || r.marker(a) != nil {
		t.Error("the cancel did not stay: the schedule or the marker came back")
	}
}

func TestPurge_ACancelAfterTheEraseStepIsRefused(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	admin, mail := r.user(), r.mail("late")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 1)

	var cancelErr error
	var markerSet, stillScheduled bool
	var called int
	r.store.onList = func(context.Context, string) {
		called++
		if called > 1 {
			return
		}
		markerSet = r.marker(a) != nil
		cancelErr = tenancy.NewStore(r.app).CancelWorkspaceDeletion(adminCtx(a, admin))
		stillScheduled = r.count(`SELECT count(*) FROM tenants WHERE id = $1 AND deletion_scheduled_at IS NOT NULL`, a) == 1
	}

	got, err := r.purger().Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 1})

	if called == 0 {
		t.Fatal("the file step never listed")
	}
	if !markerSet {
		t.Error("deletion_purge_started_at was not set when the file step began")
	}
	if !errors.Is(cancelErr, tenancy.ErrDeletionUnderway) {
		t.Errorf("cancel after the erase step: err = %v, want ErrDeletionUnderway", cancelErr)
	}
	if !stillScheduled {
		t.Error("the refused cancel cleared the schedule")
	}
	if r.tenantRows(a) != 0 {
		t.Error("the tenant row is still there after Purge")
	}
}

func TestPurge_AFailedFileStepResumes(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	admin, mail := r.user(), r.mail("files")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 3)
	fails := 0
	r.store.failDelete = func([]string) bool { // the first two batches each lose one key, then fail
		fails++
		return fails <= 2
	}
	p := r.purger()

	got, err := p.Purge(context.Background(), a)
	requireStepErr(t, err, a, "files")
	if got.Purged {
		t.Error("Result.Purged = true on a failed file step")
	}
	if r.tenantRows(a) != 1 || r.memberRows(a) != 1 || !r.isDue(a) {
		t.Error("after the failed file step the tenant is gone, lost a member, or is no longer due")
	}
	marker, dueAt := r.marker(a), r.dueAt(a)
	if marker == nil || r.erasures(admin) != 1 {
		t.Error("after the failed file step the erasure and the marker should be committed")
	}
	if n := len(r.staff(a)); n != 0 {
		t.Errorf("staff rows after the failed file step = %d, want 0", n)
	}
	if n := r.objects(a); n != 2 {
		t.Fatalf("control: objects left = %d, want 2 (the fake removed one before failing)", n)
	}

	if _, err = p.Purge(context.Background(), a); err == nil {
		t.Fatal("second Purge = nil, want the second forced failure")
	}
	if again := r.marker(a); marker == nil || again == nil || !again.Equal(*marker) || !r.dueAt(a).Equal(dueAt) {
		t.Errorf("a retry moved the marker (%v -> %v) or the due time", marker, again)
	}

	got, err = p.Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 1})
	if r.objects(a) != 0 || r.tenantRows(a) != 0 {
		t.Errorf("after the third Purge: objects = %d, tenant rows = %d; want 0 and 0", r.objects(a), r.tenantRows(a))
	}
	var gen int64
	if err := r.super.QueryRow(context.Background(), `SELECT generation FROM person_erasures WHERE user_id = $1`, admin).Scan(&gen); err != nil || gen != 3 {
		t.Errorf("erasure generation = %d (%v), want 3: each retry queues the member again", gen, err)
	}
	rows := r.staff(a)
	if len(rows) != 1 {
		t.Fatalf("staff rows = %d, want 1", len(rows))
	}
	if rows[0].Payload["objects"] != float64(1) || rows[0].Payload["members"] != float64(1) {
		t.Errorf("payload = %v, want this run's counts: members 1, objects 1", rows[0].Payload)
	}
}

func TestPurge_AFailedPurgeStepResumes(t *testing.T) {
	r := newRig(t)
	a, b := r.due(r.tenant()), r.tenant()
	admin, mail := r.user(), r.mail("lock")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 1)

	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseY := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseY)
	yDone := make(chan error, 1)
	go func() {
		yDone <- db.WithinTenantTx(context.Background(), r.app, b, func(tx pgx.Tx) error {
			if err := audit.Record(context.Background(), tx, "held-writer", "test.held", nil); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-yDone:
		t.Fatalf("the other tenant's writer ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the other tenant's writer never wrote")
	}

	p := r.purger()
	start := time.Now()
	_, err := p.Purge(context.Background(), a)
	requireStepErr(t, err, a, "purge")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Errorf("err = %v, want a 55P03 lock timeout in the chain", err)
	}
	if took := time.Since(start); took < 4*time.Second {
		t.Errorf("Purge failed after %v, want it to wait out purge_tenant's lock timeout (about 5 s)", took)
	}
	if r.tenantRows(a) != 1 || !r.isDue(a) || r.memberRows(a) != 1 {
		t.Error("after the failed purge step the tenant is gone or no longer due")
	}
	if n := len(r.staff(a)); n != 0 {
		t.Errorf("staff rows after the failed purge step = %d, want 0", n)
	}

	releaseY()
	if err := <-yDone; err != nil {
		t.Fatalf("the other tenant's writer: %v", err)
	}
	got, err := p.Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 0})
	if n := len(r.staff(a)); n != 1 {
		t.Errorf("staff rows after the retry = %d, want 1", n)
	}
	if r.tenantRows(b) != 1 {
		t.Error("the other tenant was purged")
	}
}

func TestPurge_AMemberWithoutAUsableEmailIsErasedWithTheAccountEmail(t *testing.T) {
	cases := []struct {
		name  string
		email *string
	}{
		{"NULL", nil},
		{"under 3 bytes", ptr("a@")},
		{"blank", ptr("  ")},
		{"255 bytes", ptr(longMail)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			a := r.due(r.tenant())
			admin, bola := r.user(), r.user()
			adminMail, bolaMail := r.mail("adm"), r.mail("bola")
			r.member(a, admin, "admin", "active", &adminMail, "Ada")
			r.member(a, bola, "preparer", "suspended", c.email, "Bola")
			r.accounts.fn = func(id string) (string, error) {
				if id != bola {
					return "", fmt.Errorf("unplanned read for %s", id)
				}
				return bolaMail, nil
			}

			got, err := r.purger().Purge(context.Background(), a)
			requireResult(t, got, err, Result{Purged: true, Members: 2})

			if calls := r.accounts.got(); !slices.Equal(calls, []string{bola}) {
				t.Errorf("account reads = %v, want exactly [%s]", calls, bola)
			}
			var email *string
			if err := r.super.QueryRow(context.Background(), `SELECT email FROM person_erasures WHERE user_id = $1`, bola).Scan(&email); err != nil {
				t.Fatalf("erasure row of Bola: %v", err)
			}
			if email == nil || *email != bolaMail {
				t.Errorf("erasure email = %v, want the account email %s", email, bolaMail)
			}
			if r.jobs(eraseKind, bola) != 1 || r.jobs(eraseByID, bola) != 0 {
				t.Errorf("Bola's jobs: %s %d, %s %d; want 1 and 0", eraseKind, r.jobs(eraseKind, bola), eraseByID, r.jobs(eraseByID, bola))
			}
			if n := len(r.logs.find(slog.LevelWarn, warnNoEmail)); n != 0 {
				t.Errorf("WARN records = %d, want 0: the account gave a usable email", n)
			}
		})
	}
}

func TestPurge_AMemberWithNoUsableEmailAnywhereIsErasedByID(t *testing.T) {
	cases := []struct {
		name   string
		answer string
	}{
		{"the account is gone", ""},
		{"one byte", "x"},
		{"255 bytes", longMail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			a := r.due(r.tenant())
			admin, bola, cleo := r.user(), r.user(), r.user()
			adminMail := r.mail("adm")
			r.member(a, admin, "admin", "active", &adminMail, "Ada")
			r.member(a, bola, "preparer", "suspended", nil, "Bola")
			r.member(a, cleo, "reviewer", "invited", nil, "Cleo")
			r.accounts.fn = func(string) (string, error) { return c.answer, nil }

			got, err := r.purger().Purge(context.Background(), a)
			requireResult(t, got, err, Result{Purged: true, Members: 3})

			for _, id := range []string{bola, cleo} {
				if n := r.jobs(eraseByID, id); n != 1 {
					t.Fatalf("%s jobs of %s = %d, want 1", eraseByID, id, n)
				}
				var args string
				if err := r.super.QueryRow(context.Background(), `SELECT args::text FROM river_job WHERE kind = $1 AND args->>'user_id' = $2`, eraseByID, id).Scan(&args); err != nil {
					t.Fatal(err)
				}
				var parsed map[string]any
				if err := json.Unmarshal([]byte(args), &parsed); err != nil || len(parsed) != 1 || parsed["user_id"] != id {
					t.Errorf("job args = %s, want exactly {user_id: %s}", args, id)
				}
				if n := r.erasures(id); n != 0 {
					t.Errorf("erasure rows of %s = %d, want 0", id, n)
				}
			}
			if r.erasures(admin) != 1 || r.jobs(eraseKind, admin) != 1 {
				t.Error("the admin with a usable email lost their normal erasure")
			}
			warns := r.logs.find(slog.LevelWarn, warnNoEmail)
			if len(warns) != 2 {
				t.Fatalf("WARN records = %d, want 2: one per member erased by id", len(warns))
			}
			for _, w := range warns {
				if len(w.attrs) != 0 {
					t.Errorf("WARN attrs = %v, want none", w.attrs)
				}
			}
			for _, l := range r.logs.all() {
				for _, leak := range []string{bola, "Bola", cleo, "Cleo"} {
					if strings.Contains(l.text(), leak) {
						t.Errorf("log record %q names a member", l.text())
					}
				}
			}
		})
	}
}

func TestPurge_AFailedAccountReadStopsTheTenant(t *testing.T) {
	cases := []struct {
		name     string
		accounts func(r *rig) // sets r.accounts
		check    func(t *testing.T, err error)
	}{
		{"the read answers 500", func(r *rig) {
			r.accounts.fn = func(string) (string, error) { return "", &gotrueadmin.StatusError{Status: 500} }
		}, func(t *testing.T, err error) {
			var se *gotrueadmin.StatusError
			if !errors.As(err, &se) || se.Status != 500 {
				t.Errorf("err = %v, want the 500 StatusError in the chain", err)
			}
		}},
		{"no account client", func(r *rig) { r.accounts = nil }, func(t *testing.T, err error) {
			if !errors.Is(err, errNoAccountReader) {
				t.Errorf("err = %v, want errNoAccountReader", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			a := r.due(r.tenant())
			ids := r.sortedUsers(2) // the admin sorts first: a loop that writes as it goes would queue it
			admin, bola := ids[0], ids[1]
			adminMail := r.mail("adm")
			r.member(a, admin, "admin", "active", &adminMail, "Ada")
			r.member(a, bola, "preparer", "suspended", nil, "Bola")
			r.put(a, 2)
			c.accounts(r)

			_, err := r.purger().Purge(context.Background(), a)
			requireStepErr(t, err, a, "erase")
			c.check(t, err)
			for _, leak := range []string{bola, "Bola", admin, "Ada", adminMail} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("err = %q holds %q", err, leak)
				}
			}
			if r.erasures(admin, bola) != 0 || r.jobs(eraseKind, admin, bola) != 0 || r.jobs(eraseByID, admin, bola) != 0 {
				t.Error("an erasure or job was queued although step 1 failed")
			}
			if r.marker(a) != nil {
				t.Error("the purge marker is set although step 1 failed")
			}
			if r.store.deletes() != 0 || r.objects(a) != 2 {
				t.Errorf("DeleteMany calls = %d, objects = %d; want 0 and 2", r.store.deletes(), r.objects(a))
			}
			if r.tenantRows(a) != 1 || r.memberRows(a) != 2 || !r.isDue(a) {
				t.Error("the tenant is not intact and due")
			}
			if n := len(r.logs.find(slog.LevelWarn, warnNoEmail)); n != 0 {
				t.Errorf("WARN records = %d, want 0 on a rolled-back step", n)
			}
			if err := tenancy.NewStore(r.app).CancelWorkspaceDeletion(adminCtx(a, admin)); err != nil {
				t.Errorf("an admin cancel after the failed step: %v, want nil", err)
			}
		})
	}
}

func TestPurge_AFailedStaffRowRollsBackThePurge(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	admin, mail := r.user(), r.mail("stafffail")
	r.member(a, admin, "admin", "active", &mail, "")
	r.put(a, 1)
	name := "purge_staff_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	r.exec(`CREATE FUNCTION ` + name + `() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced staff failure' USING ERRCODE = 'check_violation'; END; $$`)
	t.Cleanup(func() { _, _ = r.super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	r.exec(`CREATE TRIGGER ` + name + ` BEFORE INSERT ON staff_audit_log FOR EACH ROW
		WHEN (NEW.event = 'workspace.purged' AND NEW.tenant_id = '` + a + `'::uuid) EXECUTE FUNCTION ` + name + `()`)
	dueAt := r.dueAt(a)
	p := r.purger()

	_, err := p.Purge(context.Background(), a)
	requireStepErr(t, err, a, "purge")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Message != "forced staff failure" {
		t.Errorf("err = %v, want the forced staff-row failure in the chain", err)
	}
	if r.tenantRows(a) != 1 || !r.dueAt(a).Equal(dueAt) || r.marker(a) == nil {
		t.Error("after the failed staff write the tenant row, its due time or its marker changed")
	}
	if n := len(r.staff(a)); n != 0 {
		t.Errorf("staff rows = %d, want 0", n)
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, a); n != 0 {
		t.Errorf("audit_log rows = %d, want 0: the tenant row of the staff event rolls back with it", n)
	}

	r.exec(`DROP TRIGGER ` + name + ` ON staff_audit_log`)
	got, err := p.Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true, Members: 1, Objects: 0})
	if n := len(r.staff(a)); n != 1 {
		t.Errorf("staff rows after the retry = %d, want 1", n)
	}
}

func TestPurge_TwoRunsAtOncePurgeOnce(t *testing.T) {
	for round := 0; round < 3; round++ {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			r := newRig(t)
			a := r.due(r.tenant())
			admin, mail := r.user(), r.mail("race")
			r.member(a, admin, "admin", "active", &mail, "")
			r.put(a, 2)
			p := r.purger()

			var results [2]Result
			var errs [2]error
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range results {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[i], errs[i] = p.Purge(context.Background(), a)
				}()
			}
			close(start)
			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Errorf("run %d: %v", i, err)
				}
			}
			if purged := b2i(results[0].Purged) + b2i(results[1].Purged); purged != 1 {
				t.Errorf("runs that purged = %d (%+v), want exactly 1", purged, results)
			}
			if n := len(r.staff(a)); n != 1 {
				t.Errorf("staff rows = %d, want 1", n)
			}
			if r.tenantRows(a) != 0 || r.objects(a) != 0 {
				t.Errorf("tenant rows = %d, objects = %d; want 0 and 0", r.tenantRows(a), r.objects(a))
			}
		})
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestPurge_ATenantWithNoMemberToErasePurges(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	gone := r.user()
	r.member(a, gone, "admin", "removed", nil, "")

	got, err := r.purger().Purge(context.Background(), a)
	requireResult(t, got, err, Result{Purged: true})

	if calls := r.accounts.got(); len(calls) != 0 {
		t.Errorf("account reads = %v, want none for a removed member", calls)
	}
	rows := r.staff(a)
	if len(rows) != 1 {
		t.Fatalf("staff rows = %d, want 1", len(rows))
	}
	if rows[0].Payload["members"] != float64(0) || rows[0].Payload["objects"] != float64(0) {
		t.Errorf("payload = %v, want members 0 and objects 0", rows[0].Payload)
	}
	if r.tenantRows(a) != 0 || r.erasures(gone) != 0 {
		t.Errorf("tenant rows = %d, erasure rows = %d; want 0 and 0", r.tenantRows(a), r.erasures(gone))
	}
}

func TestRunOnce_PurgesEveryDueTenantAndReportsAFailure(t *testing.T) {
	r := newRig(t)
	a, c, e, d := r.tenant(), r.tenant(), r.tenant(), r.tenant()
	r.scheduleAt(a, "now() - interval '40 days'")
	r.scheduleAt(c, "now() - interval '35 days'")
	r.scheduleAt(e, "now() - interval '33 days'") // due after the failing tenant
	r.scheduleAt(d, "now() - interval '5 days'")
	mails := map[string]string{}
	users := map[string]string{}
	for _, tn := range []string{a, c, e, d} {
		users[tn], mails[tn] = r.user(), r.mail("run")
		r.member(tn, users[tn], "admin", "active", ptr(mails[tn]), "Ada Obi")
	}
	r.put(a, 2)
	r.put(c, 2)
	r.put(e, 1)
	r.put(d, 1)
	r.store.failDelete = func(keys []string) bool { return tenantOfKey(keys[0]) == c }

	err := r.purger().RunOnce(context.Background())

	if err == nil {
		t.Fatal("RunOnce = nil, want the failure of the file step")
	}
	if !strings.Contains(err.Error(), "tenant "+c+": files") || strings.Contains(err.Error(), a) {
		t.Errorf("err = %q, want it to name tenant C and step files, and not A", err)
	}
	if r.tenantRows(a) != 0 || r.objects(a) != 0 {
		t.Error("A (due first, no fault) was not purged")
	}
	if r.tenantRows(e) != 0 || r.objects(e) != 0 || len(r.staff(e)) != 1 {
		t.Error("E (due after the failing tenant) was not purged")
	}
	if r.tenantRows(c) != 1 || r.memberRows(c) != 1 || !r.isDue(c) {
		t.Error("C (failed) is gone or no longer due")
	}
	if r.tenantRows(d) != 1 || r.memberRows(d) != 1 || r.objects(d) != 1 || r.erasures(users[d]) != 0 || r.marker(d) != nil {
		t.Error("D (not due) was touched")
	}
	// One at a time: the runs of file operations, collapsed, are A's, C's, E's; none for D.
	if ops := slices.Compact(r.store.opsFor(a, c, e, d)); !slices.Equal(ops, []string{a, c, e}) {
		t.Errorf("file operations by tenant = %v, want [A C E] in due order, not interleaved", ops)
	}

	failed := r.logs.find(slog.LevelError, errTenantMsg)
	var forC, forOthers int
	for _, f := range failed {
		if f.attrs["tenant_id"] == c {
			forC++
			if f.attrs["step"] != "files" || f.attrs["error"] == "" {
				t.Errorf("ERROR attrs = %v, want step files and a non-empty error", f.attrs)
			}
		} else if f.attrs["tenant_id"] == a || f.attrs["tenant_id"] == e || f.attrs["tenant_id"] == d {
			forOthers++
		}
	}
	if forC != 1 || forOthers != 0 {
		t.Errorf("ERROR records: for C %d (want 1), for A, E or D %d (want 0)", forC, forOthers)
	}
	var purgedA, purgedE int
	for _, p := range r.logs.find(slog.LevelInfo, infoPurged) {
		switch p.attrs["tenant_id"] {
		case e:
			purgedE++
			if p.attrs["members"] != "1" || p.attrs["objects"] != "1" {
				t.Errorf("INFO attrs = %v, want members 1 and objects 1", p.attrs)
			}
		case a:
			purgedA++
			if p.attrs["members"] != "1" || p.attrs["objects"] != "2" {
				t.Errorf("INFO attrs = %v, want members 1 and objects 2", p.attrs)
			}
		case c, d:
			t.Errorf("INFO purged logged for %s, which was not purged", p.attrs["tenant_id"])
		}
	}
	if purgedA != 1 || purgedE != 1 {
		t.Errorf("INFO purged records: for A %d, for E %d; want 1 each", purgedA, purgedE)
	}
	for _, l := range r.logs.all() {
		for _, tn := range []string{a, c, e, d} {
			for _, leak := range []string{mails[tn], "example.invalid", "Ada", users[tn]} {
				if strings.Contains(l.text(), leak) {
					t.Errorf("log record %q holds %q", l.text(), leak)
				}
			}
		}
	}
}

func TestRunOnce_StopsOnADoneContext(t *testing.T) {
	r := newRig(t)
	first, second := r.tenant(), r.tenant()
	r.scheduleAt(first, "now() - interval '400 days'")
	r.scheduleAt(second, "now() - interval '399 days'")
	users := map[string]string{}
	for _, tn := range []string{first, second} {
		users[tn] = r.user()
		r.member(tn, users[tn], "admin", "active", ptr(r.mail("done")), "")
		r.put(tn, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var listed []string
	r.store.onList = func(_ context.Context, prefix string) {
		listed = append(listed, tenantOfKey(prefix))
		cancel()
	}

	err := r.purger().RunOnce(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunOnce = %v, want an error wrapping context.Canceled", err)
	}
	if len(listed) == 0 || listed[0] != first {
		t.Fatalf("first List was for %v, want the earliest-due tenant %s", listed, first)
	}
	if slices.Contains(listed, second) {
		t.Error("the loop went on to the second tenant after the context was done")
	}
	if r.tenantRows(second) != 1 || r.erasures(users[second]) != 0 || r.marker(second) != nil || r.objects(second) != 1 {
		t.Error("the second tenant was touched")
	}
	if r.tenantRows(first) != 1 || r.erasures(users[first]) != 1 {
		t.Error("control: the first tenant should keep its row and its committed erasure")
	}
	for _, f := range r.logs.find(slog.LevelError, errTenantMsg) {
		if f.attrs["tenant_id"] == first || f.attrs["tenant_id"] == second {
			t.Errorf("ERROR record for a shutdown: %v", f.attrs)
		}
	}
}

func TestDueTenants_ReadsOnlyDueTenants(t *testing.T) {
	r := newRig(t)
	a, later, notYet, unscheduled := r.tenant(), r.tenant(), r.tenant(), r.tenant()
	r.scheduleAt(a, "now() - interval '45 days'")
	r.scheduleAt(later, "now() - interval '31 days'")
	r.scheduleAt(notYet, "now() - interval '29 days'")

	got, err := dueTenants(context.Background(), r.reader)
	if err != nil {
		t.Fatalf("dueTenants: %v", err)
	}
	ia, il := slices.Index(got, a), slices.Index(got, later)
	if ia < 0 || il < 0 {
		t.Fatalf("dueTenants = %v, want both due tenants %s and %s", got, a, later)
	}
	if ia > il {
		t.Errorf("due tenant A is at %d, after the later-due tenant at %d", ia, il)
	}
	if slices.Contains(got, notYet) || slices.Contains(got, unscheduled) {
		t.Errorf("dueTenants = %v, holds a tenant that is not due or not scheduled", got)
	}
}

func TestPurge_TheRemovedCreatorCanCreateAWorkspaceAgain(t *testing.T) {
	r := newRig(t)
	creator := r.user()
	derived := uuid.NewSHA1(uuid.MustParse(creatorNS), []byte(creator)).String()
	r.removeOnCleanup(derived)
	store := tenancy.NewStore(r.app)
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: creator, Email: creator + "@example.invalid"})
	in := tenancy.ProvisionInput{WorkspaceName: "Creator Workspace", DisplayName: "Creator"}

	first, _, err := store.ProvisionWorkspace(ctx, in)
	if err != nil {
		t.Fatalf("first ProvisionWorkspace: %v", err)
	}
	if first.ID != derived {
		t.Fatalf("control: first tenant id = %s, want the derived %s", first.ID, derived)
	}
	other, otherMail := r.user(), r.mail("other")
	r.member(derived, other, "admin", "active", &otherMail, "")
	r.exec(`UPDATE memberships SET status = 'removed', email = NULL WHERE tenant_id = $1 AND user_id = $2`, derived, creator)
	r.due(derived)

	got, err := r.purger().Purge(context.Background(), derived)
	requireResult(t, got, err, Result{Purged: true, Members: 1})

	if n := r.erasures(creator); n != 0 {
		t.Errorf("erasure rows of the removed creator = %d, want 0", n)
	}
	second, _, err := store.ProvisionWorkspace(ctx, in)
	if err != nil {
		t.Fatalf("ProvisionWorkspace after the purge: %v (ErrAlreadyProvisioned = %v)", err, errors.Is(err, tenancy.ErrAlreadyProvisioned))
	}
	if second.ID != derived {
		r.removeOnCleanup(second.ID)
		t.Errorf("tenant id = %s, want the derived %s, not a random one", second.ID, derived)
	}
	if n := r.count(`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`, derived, creator); n != 1 {
		t.Errorf("active memberships of the creator = %d, want 1", n)
	}
}

func TestRunOnce_ATenantCancelledAfterTheDueReadIsSkipped(t *testing.T) {
	r := newRig(t)
	first, second := r.tenant(), r.tenant()
	r.scheduleAt(first, "now() - interval '40 days'")
	r.scheduleAt(second, "now() - interval '35 days'")
	users := map[string]string{}
	for _, tn := range []string{first, second} {
		users[tn] = r.user()
		r.member(tn, users[tn], "admin", "active", ptr(r.mail("stale")), "")
		r.put(tn, 1)
	}
	cancelled := false
	r.store.onList = func(_ context.Context, prefix string) { // the due list is already read: cancel the next tenant
		if !cancelled {
			cancelled = true
			r.exec(`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`, second)
		}
	}

	if err := r.purger().RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if !cancelled || r.tenantRows(first) != 0 {
		t.Fatalf("control: the first tenant was not purged (hook ran = %v)", cancelled)
	}
	if r.tenantRows(second) != 1 || r.memberRows(second) != 1 || r.objects(second) != 1 || r.erasures(users[second]) != 0 || r.marker(second) != nil {
		t.Error("the cancelled tenant lost its row, member or file, or got an erasure or a marker")
	}
	if n := len(r.staff(second)); n != 0 {
		t.Errorf("staff rows of the cancelled tenant = %d, want 0", n)
	}
	for _, p := range r.logs.find(slog.LevelInfo, infoPurged) {
		if p.attrs["tenant_id"] == second {
			t.Error("INFO purged logged for the cancelled tenant")
		}
	}
	if n := len(r.logs.find(slog.LevelError, errTenantMsg)); n != 0 {
		t.Errorf("ERROR records = %d, want 0: a cancelled tenant is not a fault", n)
	}
}

func TestRunOnce_ANilLoggerFallsBackToTheDefault(t *testing.T) {
	r := newRig(t)
	a := r.due(r.tenant())
	mail := r.mail("nolog")
	r.member(a, r.user(), "admin", "active", &mail, "")
	prev := slog.Default()
	slog.SetDefault(r.logs.logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	p := r.purger()
	p.Logger = nil

	if err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	found := false
	for _, rec := range r.logs.find(slog.LevelInfo, infoPurged) {
		found = found || rec.attrs["tenant_id"] == a
	}
	if !found || r.tenantRows(a) != 0 {
		t.Errorf("INFO purged for A on the default logger = %v, tenant rows = %d; want true and 0", found, r.tenantRows(a))
	}
}

func TestPurge_TheHouseTenantIsNeverPurged(t *testing.T) {
	r := newRig(t)
	var house string
	err := r.super.QueryRow(context.Background(), `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	if errors.Is(err, pgx.ErrNoRows) {
		house = r.tenant()
		r.exec(`INSERT INTO public.house_company (tenant_id) VALUES ($1)`, house)
		r.t.Cleanup(func() {
			_, _ = r.super.Exec(context.Background(), `DELETE FROM public.house_company WHERE tenant_id = $1`, house)
		})
	} else if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	if _, err := r.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now() - interval '31 days', deletion_scheduled_by = gen_random_uuid() WHERE id = $1`, house); err == nil {
		t.Fatal("control: the database accepted a deletion schedule on the house tenant")
	}
	rowsBefore, membersBefore := r.tenantRows(house), r.memberRows(house)
	erasedBefore := r.count(`SELECT count(*) FROM person_erasures`)

	got, err := r.purger().Purge(context.Background(), house)
	requireResult(t, got, err, Result{})

	if rowsBefore != 1 || r.tenantRows(house) != 1 || r.memberRows(house) != membersBefore || r.marker(house) != nil {
		t.Error("the house tenant, a member or the marker changed")
	}
	if n := r.count(`SELECT count(*) FROM person_erasures`); n != erasedBefore {
		t.Errorf("erasure rows %d -> %d, want unchanged", erasedBefore, n)
	}
	if r.store.deletes() != 0 || len(r.staff(house)) != 0 || len(r.accounts.got()) != 0 {
		t.Error("the house tenant had files deleted, a staff row or an account read")
	}
	if dueIDs, err := dueTenants(context.Background(), r.reader); err != nil || slices.Contains(dueIDs, house) {
		t.Errorf("dueTenants = %v, %v; want no house tenant", dueIDs, err)
	}
}
