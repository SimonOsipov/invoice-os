package erp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// --- fixtures ---------------------------------------------------------------------------

// seedActive sets a connection active on a grant that has a secret_ref and a secret in secrets,
// through the superuser pool.
func seedActive(t *testing.T, super *pgxpool.Pool, secrets *MemorySecrets, tenant, entity, key string) (connID, grantID string) {
	t.Helper()
	grantID = seedHeldGrant(t, super, secrets, tenant)
	return seedConnRow(t, super, connRow{tenant: tenant, entity: entity, key: key, status: "active", grant: grantID}), grantID
}

// seedHeldGrant inserts a grant whose secret_ref names a secret that secrets holds.
func seedHeldGrant(t *testing.T, super *pgxpool.Pool, secrets *MemorySecrets, tenant string) string {
	t.Helper()
	id := uuid.NewString()
	ref := SecretName("", id)
	mustExec(t, super,
		"INSERT INTO erp_grants (id, tenant_id, system, kind, secret_ref) VALUES ($1, $2, 'fake', 'oauth_refresh', $3)", id, tenant, ref)
	if err := secrets.Create(context.Background(), ref, []byte(uuid.NewString())); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	return id
}

// holds reports whether the store still has a secret of that id.
func holds(secrets SecretStore, id string) bool {
	_, _, err := secrets.Access(context.Background(), id)
	return !errors.Is(err, ErrSecretNotFound)
}

func secretIDs(m *MemorySecrets) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.m))
	for id := range m.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// flakyDeletes fails its first left Delete calls and then deletes.
type flakyDeletes struct {
	SecretStore
	left atomic.Int32
}

func (f *flakyDeletes) Delete(ctx context.Context, id string) error {
	if f.left.Add(-1) >= 0 {
		return errors.New("injected delete failure")
	}
	return f.SecretStore.Delete(ctx, id)
}

// releaseRig is one firm with an admin, a memory secret store and a worker over it. The store is
// wired as portfolio is, with no secret store: only the job touches secrets.
type releaseRig struct {
	super, app *pgxpool.Pool
	tenant     string
	admin      context.Context
	mem        *MemorySecrets
	q          *queue.Client
	store      *Store
	worker     *SecretDeleteWorker
}

func newReleaseRig(t *testing.T, failDeletes int) *releaseRig {
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
	reg, err := NewRegistry(NewFake(testRedirect))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	mem := NewMemorySecrets()
	flaky := &flakyDeletes{SecretStore: mem}
	flaky.left.Store(int32(failDeletes))
	return &releaseRig{
		super: super, app: app, tenant: tenant, mem: mem, q: q,
		admin:  seedMember(t, super, tenant, "admin", "active", true),
		store:  NewStore(app, reg, nil, q),
		worker: &SecretDeleteWorker{Pool: app, Secrets: flaky},
	}
}

func (r *releaseRig) client(t *testing.T, name string) string {
	return seedClient(t, r.super, r.tenant, name)
}

// deleteJob is one enqueued erp_secret_delete row; args stay raw JSON so a test names no Go field.
type deleteJob struct {
	id   int64
	args []byte
}

func deleteJobsOf(t *testing.T, super *pgxpool.Pool, tenant string) []deleteJob {
	t.Helper()
	rows, err := super.Query(context.Background(),
		"SELECT id, args FROM river_job WHERE kind = 'erp_secret_delete' AND args->>'tenant_id' = $1 ORDER BY id", tenant)
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
	if err := rows.Err(); err != nil {
		t.Fatalf("read river_job: %v", err)
	}
	return out
}

func (r *releaseRig) jobs(t *testing.T) []deleteJob { return deleteJobsOf(t, r.super, r.tenant) }

func (j deleteJob) argMap(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(j.args, &m); err != nil {
		t.Fatalf("job %d args %s: %v", j.id, j.args, err)
	}
	return m
}

func (r *releaseRig) work(j deleteJob, attempt int) error {
	var args SecretDeleteArgs
	if err := json.Unmarshal(j.args, &args); err != nil {
		return err
	}
	return r.worker.Work(context.Background(), &river.Job[SecretDeleteArgs]{
		JobRow: &rivertype.JobRow{ID: j.id, Kind: "erp_secret_delete", Attempt: attempt, MaxAttempts: 1000},
		Args:   args,
	})
}

// runJobs drives every job as River does, again after an error, and returns each job's error count.
func (r *releaseRig) runJobs(t *testing.T, observe func(j deleteJob, attempt int, err error)) []int {
	t.Helper()
	jobs := r.jobs(t)
	failures := make([]int, len(jobs))
	for i, j := range jobs {
		for attempt := 1; ; attempt++ {
			if attempt > 10 {
				t.Fatalf("job %d still failing after 10 attempts", j.id)
			}
			err := r.work(j, attempt)
			if observe != nil {
				observe(j, attempt, err)
			}
			if err == nil {
				break
			}
			failures[i]++
		}
	}
	return failures
}

func (r *releaseRig) idempotencyKeys(t *testing.T, key string) int {
	return countOf(t, r.super, "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2", r.tenant, key)
}

type auditRow struct {
	actor   string
	payload map[string]any
	entity  *string
}

func (r *releaseRig) audit(t *testing.T, event string) []auditRow {
	t.Helper()
	rows, err := r.super.Query(context.Background(),
		"SELECT actor, payload::text, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id", r.tenant, event)
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

// --- AC 5: a release enqueues one delete job, in its own transaction ---------------------

func TestRelease_DisconnectEnqueuesOneDeleteJob(t *testing.T) {
	r := newReleaseRig(t, 0)
	e1, e2 := r.client(t, "Client One"), r.client(t, "Client Two")
	c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
	plain, plainGrant := seedActiveConn(t, r.super, r.tenant, e2, "fake-co-2") // a grant with no secret_ref

	if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if grantExists(t, r.super, g) {
		t.Error("the released grant row still exists")
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d erp_secret_delete jobs, want 1", len(jobs))
	}
	want := map[string]any{"tenant_id": r.tenant, "secret_ref": SecretName("", g), "connection_id": c, "system": "fake", "revoke": true, "grant_kind": "oauth_refresh"}
	if got := jobs[0].argMap(t); !reflect.DeepEqual(got, want) {
		t.Errorf("job args = %v, want exactly %v", got, want)
	}
	if n := r.idempotencyKeys(t, "erp_secret_delete:"+g); n != 1 {
		t.Errorf("%d idempotency keys erp_secret_delete:%s, want 1", n, g)
	}
	if s := stateOf(t, r.super, c); s.status != "disconnected" || s.grant != nil {
		t.Errorf("connection = %+v, want disconnected with no grant", s)
	}
	// The request only enqueues: the secret goes when the job runs.
	if !holds(r.mem, SecretName("", g)) {
		t.Error("the secret was deleted on the request path, want it deleted by the job")
	}

	// A grant without a secret_ref has nothing to delete.
	if _, err := r.store.Disconnect(r.admin, e2, plain); err != nil {
		t.Fatalf("Disconnect (no secret): %v", err)
	}
	if grantExists(t, r.super, plainGrant) {
		t.Error("the released grant row without a secret_ref still exists")
	}
	if n := len(r.jobs(t)); n != 1 {
		t.Errorf("%d jobs after releasing a grant with no secret_ref, want still 1", n)
	}

	r.runJobs(t, nil)
	if holds(r.mem, SecretName("", g)) {
		t.Error("the secret survives its job")
	}
}

func TestRelease_SharedGrantIsKept(t *testing.T) {
	r := newReleaseRig(t, 0)
	e1, e2, e3 := r.client(t, "Client One"), r.client(t, "Client Two"), r.client(t, "Client Three")
	c1, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
	c2 := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e2, key: "fake-co-1", status: "active", grant: g})
	c3 := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e3, key: "fake-co-1", status: "active", grant: g})

	for _, step := range []struct{ entity, conn string }{{e1, c1}, {e2, c2}} {
		if _, err := r.store.Disconnect(r.admin, step.entity, step.conn); err != nil {
			t.Fatalf("Disconnect %s: %v", step.conn, err)
		}
		if !grantExists(t, r.super, g) {
			t.Fatalf("the shared grant was deleted while %s still uses it", c3)
		}
		if n := len(r.jobs(t)); n != 0 {
			t.Errorf("%d delete jobs while the grant is in use, want 0", n)
		}
		if n := r.idempotencyKeys(t, "erp_secret_delete:"+g); n != 0 {
			t.Errorf("%d delete idempotency keys while the grant is in use, want 0", n)
		}
	}
	if _, err := r.store.Disconnect(r.admin, e3, c3); err != nil {
		t.Fatalf("Disconnect %s: %v", c3, err)
	}
	if grantExists(t, r.super, g) {
		t.Error("the grant survives its last connection")
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs after the last release, want 1", len(jobs))
	}
	if got := jobs[0].argMap(t)["connection_id"]; got != c3 {
		t.Errorf("job connection_id = %v, want the last releaser %s", got, c3)
	}
}

// releaseIn runs releaseGrant as a reconnect does: the caller has cleared its connection's grant_id
// in the same transaction, and after is what the transaction returns.
func (r *releaseRig) releaseIn(t *testing.T, conn, grant string, after error) error {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, r.app, r.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE erp_connections SET grant_id = NULL WHERE id = $1", conn); err != nil {
			return err
		}
		if err := releaseGrant(ctx, tx, r.q, grant, conn, false); err != nil {
			return err
		}
		return after
	})
}

func TestRelease_ReconnectReleaseEnqueuesOneDeleteJob(t *testing.T) {
	r := newReleaseRig(t, 0)
	e1 := r.client(t, "Client One")
	g := seedHeldGrant(t, r.super, r.mem, r.tenant)
	c := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e1, key: "fake-co-1", status: "reconsent", grant: g})

	if err := r.releaseIn(t, c, g, nil); err != nil {
		t.Fatalf("release: %v", err)
	}
	if grantExists(t, r.super, g) {
		t.Error("the released grant row still exists")
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs, want 1", len(jobs))
	}
	if got := jobs[0].argMap(t); got["secret_ref"] != SecretName("", g) || got["connection_id"] != c {
		t.Errorf("job args = %v, want secret_ref %s and connection_id %s", got, SecretName("", g), c)
	} else if _, ok := got["revoke"]; ok {
		t.Errorf("job args = %v, a reconnect's release must not revoke", got)
	} else if _, ok := got["grant_kind"]; ok {
		t.Errorf("job args = %v, a reconnect's release carries no grant_kind", got)
	}
	if n := r.idempotencyKeys(t, "erp_secret_delete:"+g); n != 1 {
		t.Errorf("%d idempotency keys, want 1", n)
	}
}

// failOnCommit makes the tenant's next disconnect fail when its transaction commits, so everything
// the transaction did before, in whatever order, has been done and must roll back. The returned
// func removes the trigger.
func failOnCommit(t *testing.T, super *pgxpool.Pool, tenant string) (remove func()) {
	t.Helper()
	name := "erp_test_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	mustExec(t, super, "CREATE FUNCTION public."+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'erp test: forced commit failure'; END $$")
	mustExec(t, super, "CREATE CONSTRAINT TRIGGER "+name+" AFTER UPDATE ON erp_connections DEFERRABLE INITIALLY DEFERRED FOR EACH ROW"+
		" WHEN (NEW.tenant_id = '"+tenant+"' AND NEW.status = 'disconnected') EXECUTE FUNCTION public."+name+"()")
	removed := false
	remove = func() {
		if removed {
			return
		}
		removed = true
		mustExec(t, super, "DROP TRIGGER "+name+" ON erp_connections")
		mustExec(t, super, "DROP FUNCTION public."+name+"()")
	}
	t.Cleanup(remove)
	return remove
}

func TestRelease_RolledBackReleaseEnqueuesNothing(t *testing.T) {
	t.Run("the release function in a transaction that fails after it", func(t *testing.T) {
		r := newReleaseRig(t, 0)
		e1 := r.client(t, "Client One")
		g := seedHeldGrant(t, r.super, r.mem, r.tenant)
		c := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e1, key: "fake-co-1", status: "reconsent", grant: g})
		before := erpState(t, r.super, r.tenant)

		later := errors.New("later statement failed")
		if err := r.releaseIn(t, c, g, later); !errors.Is(err, later) {
			t.Fatalf("transaction = %v, want it to fail with the later statement's error", err)
		}
		if got := erpState(t, r.super, r.tenant); got != before {
			t.Errorf("a rolled-back release changed the rows:\nbefore %s\nafter  %s", before, got)
		}
		if n := len(r.jobs(t)); n != 0 {
			t.Errorf("%d delete jobs after a rollback, want 0", n)
		}
		if n := r.idempotencyKeys(t, "erp_secret_delete:"+g); n != 0 {
			t.Errorf("%d idempotency keys after a rollback, want 0", n)
		}
		// The same release commits, so the rollback was the cause.
		if err := r.releaseIn(t, c, g, nil); err != nil {
			t.Fatalf("control: release: %v", err)
		}
		if n := len(r.jobs(t)); n != 1 {
			t.Errorf("control: %d delete jobs after a committed release, want 1", n)
		}
	})
	t.Run("a disconnect that fails at commit", func(t *testing.T) {
		r := newReleaseRig(t, 0)
		e1 := r.client(t, "Client One")
		c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
		before := erpState(t, r.super, r.tenant)
		remove := failOnCommit(t, r.super, r.tenant)

		if _, err := r.store.Disconnect(r.admin, e1, c); err == nil {
			t.Fatal("Disconnect succeeded, want the commit to fail")
		}
		if got := erpState(t, r.super, r.tenant); got != before {
			t.Errorf("a failed disconnect changed the rows:\nbefore %s\nafter  %s", before, got)
		}
		if s := stateOf(t, r.super, c); s.status != "active" || deref(s.grant) != g {
			t.Errorf("connection = %+v, want active on %s", s, g)
		}
		if n := len(r.jobs(t)); n != 0 {
			t.Errorf("%d delete jobs after a failed disconnect, want 0", n)
		}
		if n := r.idempotencyKeys(t, "erp_secret_delete:"+g); n != 0 {
			t.Errorf("%d idempotency keys after a failed disconnect, want 0", n)
		}
		remove()
		if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
			t.Fatalf("control: Disconnect without the trigger: %v", err)
		}
		if n := len(r.jobs(t)); n != 1 {
			t.Errorf("control: %d delete jobs after a committed disconnect, want 1", n)
		}
	})
}

// --- AC 6: the delete job ---------------------------------------------------------------

func TestSecretDeleteWorker_DeletesAndAuditsOnce(t *testing.T) {
	r := newReleaseRig(t, 0)
	e1 := r.client(t, "Client One")
	c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
	_, neighbour := seedActive(t, r.super, r.mem, r.tenant, r.client(t, "Client Two"), "fake-co-2")
	if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if n := len(r.audit(t, "erp.token.deleted")); n != 0 {
		t.Fatalf("%d erp.token.deleted rows before the job ran, want 0", n)
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs, want 1", len(jobs))
	}

	for run := 1; run <= 2; run++ {
		if err := r.work(jobs[0], 1); err != nil {
			t.Fatalf("Work run %d: %v", run, err)
		}
	}
	if holds(r.mem, SecretName("", g)) {
		t.Error("the secret survives its delete job")
	}
	if !holds(r.mem, SecretName("", neighbour)) {
		t.Error("the delete job removed another grant's secret")
	}
	rows := r.audit(t, "erp.token.deleted")
	if len(rows) != 1 {
		t.Fatalf("%d erp.token.deleted rows after two runs of one job, want 1", len(rows))
	}
	if want := map[string]any{"system": "fake", "connection_id": c}; !reflect.DeepEqual(rows[0].payload, want) {
		t.Errorf("payload = %v, want exactly %v", rows[0].payload, want)
	}
	if rows[0].actor != "system" || rows[0].entity != nil {
		t.Errorf("actor %q, entity %v, want actor system and a workspace-level row", rows[0].actor, rows[0].entity)
	}
}

func TestSecretDeleteWorker_NotFoundIsDone(t *testing.T) {
	r := newReleaseRig(t, 0)
	e1 := r.client(t, "Client One")
	c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
	if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// The secret is already gone: a purge or an earlier attempt deleted it.
	if err := r.mem.Delete(context.Background(), SecretName("", g)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs, want 1", len(jobs))
	}
	if err := r.work(jobs[0], 1); err != nil {
		t.Fatalf("Work on a missing secret: %v, want nil", err)
	}
	if rows := r.audit(t, "erp.token.deleted"); len(rows) != 1 || rows[0].payload["connection_id"] != c {
		t.Errorf("erp.token.deleted rows = %v, want one for connection %s", rows, c)
	}
}

func TestSecretDeleteArgs_RetryScheduleCapsAtAnHour(t *testing.T) {
	var args SecretDeleteArgs
	if err := json.Unmarshal([]byte(`{"tenant_id":"t1","secret_ref":"s","connection_id":"c","system":"fake"}`), &args); err != nil {
		t.Fatal(err)
	}
	if args.Kind() != "erp_secret_delete" || args.Tenant() != "t1" {
		t.Errorf("kind %q, tenant %q, want erp_secret_delete and t1 (the tenant_id key purge_tenant matches)", args.Kind(), args.Tenant())
	}
	if got := args.InsertOpts().MaxAttempts; got != 1000 {
		t.Errorf("MaxAttempts = %d, want 1000", got)
	}

	w := &SecretDeleteWorker{}
	for _, c := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second}, {5, 625 * time.Second}, {7, 2401 * time.Second}, {8, time.Hour}, {50, time.Hour}, {999, time.Hour},
	} {
		at := time.Now()
		job := &river.Job[SecretDeleteArgs]{JobRow: &rivertype.JobRow{Attempt: c.attempt, AttemptedAt: &at}, Args: args}
		got := w.NextRetry(job).Sub(at)
		if got < c.want || got > c.want+500*time.Millisecond {
			t.Errorf("NextRetry after attempt %d = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestSecretDeleteArgs_PurgeTenantRemovesThePendingJob(t *testing.T) {
	r, u := newReleaseRig(t, 0), newReleaseRig(t, 0)
	for _, rig := range []*releaseRig{r, u} {
		e := rig.client(t, "Client")
		c, _ := seedActive(t, rig.super, rig.mem, rig.tenant, e, "fake-co-1")
		if _, err := rig.store.Disconnect(rig.admin, e, c); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		if n := len(rig.jobs(t)); n != 1 {
			t.Fatalf("%d delete jobs before the purge, want 1", n)
		}
	}

	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, r.app, r.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT public.purge_tenant($1::uuid)", r.tenant)
		return err
	}); err != nil {
		t.Fatalf("purge_tenant: %v", err)
	}
	if n := len(r.jobs(t)); n != 0 {
		t.Errorf("%d delete jobs of the purged tenant remain, want 0", n)
	}
	if n := len(u.jobs(t)); n != 1 {
		t.Errorf("%d delete jobs of the other tenant, want 1 untouched", n)
	}
}

// --- AC 7: no secret of a released grant remains ----------------------------------------

// wantSecrets asserts the store holds exactly the secrets of the grants in kept, so a released
// grant's secret, and nothing else's, is gone.
func (r *releaseRig) wantSecrets(t *testing.T, when string, released, kept []string) {
	t.Helper()
	want := make([]string, 0, len(kept))
	for _, g := range kept {
		want = append(want, SecretName("", g))
	}
	sort.Strings(want)
	if got := secretIDs(r.mem); !reflect.DeepEqual(got, want) {
		var left []string
		for _, g := range released {
			if holds(r.mem, SecretName("", g)) {
				left = append(left, SecretName("", g))
			}
		}
		t.Errorf("%s: store holds %v, want exactly %v (released secrets still held: %v)", when, got, want, left)
	}
}

func TestRelease_NoSecretOfAReleasedGrantRemains(t *testing.T) {
	t.Run("disconnect", func(t *testing.T) {
		r := newReleaseRig(t, 0)
		e1, e2 := r.client(t, "Client One"), r.client(t, "Client Two")
		c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
		_, neighbour := seedActive(t, r.super, r.mem, r.tenant, e2, "fake-co-2")
		if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		if got := r.runJobs(t, nil); !reflect.DeepEqual(got, []int{0}) {
			t.Errorf("job errors = %v, want one job with none", got)
		}
		r.wantSecrets(t, "after the job", []string{g}, []string{neighbour})
	})

	t.Run("a shared grant released by its second disconnect", func(t *testing.T) {
		r := newReleaseRig(t, 0)
		e1, e2, e3 := r.client(t, "Client One"), r.client(t, "Client Two"), r.client(t, "Client Three")
		c1, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
		c2 := seedConnRow(t, r.super, connRow{tenant: r.tenant, entity: e2, key: "fake-co-1", status: "active", grant: g})
		_, neighbour := seedActive(t, r.super, r.mem, r.tenant, e3, "fake-co-2")

		if _, err := r.store.Disconnect(r.admin, e1, c1); err != nil {
			t.Fatalf("Disconnect first: %v", err)
		}
		if got := r.runJobs(t, nil); len(got) != 0 {
			t.Errorf("%d jobs ran after the first disconnect, want 0", len(got))
		}
		r.wantSecrets(t, "while the second connection uses the grant", nil, []string{g, neighbour})
		if _, err := r.store.Disconnect(r.admin, e2, c2); err != nil {
			t.Fatalf("Disconnect second: %v", err)
		}
		r.runJobs(t, nil)
		r.wantSecrets(t, "after the last release", []string{g}, []string{neighbour})
	})

	t.Run("a delete that fails three times", func(t *testing.T) {
		r := newReleaseRig(t, 3)
		e1, e2 := r.client(t, "Client One"), r.client(t, "Client Two")
		c, g := seedActive(t, r.super, r.mem, r.tenant, e1, "fake-co-1")
		_, neighbour := seedActive(t, r.super, r.mem, r.tenant, e2, "fake-co-2")
		if _, err := r.store.Disconnect(r.admin, e1, c); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		if s := stateOf(t, r.super, c); s.status != "disconnected" {
			t.Fatalf("connection = %+v right after disconnect, want disconnected", s)
		}
		failures := r.runJobs(t, func(_ deleteJob, attempt int, err error) {
			if s := stateOf(t, r.super, c); s.status != "disconnected" {
				t.Errorf("attempt %d (err %v): connection = %+v, want disconnected whatever the job does", attempt, err, s)
			}
			wantEvents := 0
			if err == nil {
				wantEvents = 1
			} else if !holds(r.mem, SecretName("", g)) {
				t.Errorf("attempt %d failed but the secret is already gone", attempt)
			}
			if n := len(r.audit(t, "erp.token.deleted")); n != wantEvents {
				t.Errorf("attempt %d (err %v): %d erp.token.deleted rows, want %d (recorded once the secret is gone)", attempt, err, n, wantEvents)
			}
		})
		if !reflect.DeepEqual(failures, []int{3}) {
			t.Errorf("job errors = %v, want one job that failed 3 times and then finished", failures)
		}
		r.wantSecrets(t, "after the fourth attempt", []string{g}, []string{neighbour})
	})

	t.Run("a reconsent connection re-authorised", func(t *testing.T) {
		r := newReleaseRig(t, 0)
		e1, e2 := r.client(t, "Client One"), r.client(t, "Client Two")
		reg, err := NewRegistry(NewFake(testRedirect))
		if err != nil {
			t.Fatalf("registry: %v", err)
		}
		// The callback runs on the worker, which holds the secret store.
		worker := NewStore(r.app, reg, r.mem, r.q)
		c, g1 := connectActive(t, worker, r.admin, r.super, e1, "fake-co-1")
		_, neighbour := connectActive(t, worker, r.admin, r.super, e2, "fake-co-2")
		mustExec(t, r.super, "UPDATE erp_connections SET status = 'reconsent' WHERE id = $1", c)
		r.wantSecrets(t, "before the re-authorisation", nil, []string{g1, neighbour})

		_, consent, _, err := worker.Connect(r.admin, e1, ConnectInput{System: "fake", ConnectionID: c})
		if err != nil {
			t.Fatalf("Connect by id: %v", err)
		}
		out, err := worker.Complete(context.Background(), CallbackInput{State: consentState(t, consent), Code: "fake-co-1"})
		if err != nil || string(out) != "connected" {
			t.Fatalf("Complete = %q, %v, want connected", out, err)
		}
		g2 := stateOf(t, r.super, c).grant
		if g2 == nil || *g2 == g1 {
			t.Fatalf("connection grant = %v, want a new grant (old %s)", g2, g1)
		}
		if got := r.runJobs(t, nil); !reflect.DeepEqual(got, []int{0}) {
			t.Errorf("job errors = %v, want one job with none", got)
		}
		r.wantSecrets(t, "after the job", []string{g1}, []string{*g2, neighbour})
	})
}

// --- vendor revoke ----------------------------------------------------------------------

type revokeSpy struct {
	Connector
	order *[]string
	got   *Credentials
	calls int
	fn    func(ctx context.Context) error
}

func (s *revokeSpy) Revoke(ctx context.Context, c Credentials) error {
	s.calls++
	*s.order = append(*s.order, "revoke")
	cc := c
	cc.Secret = append([]byte(nil), c.Secret...)
	*s.got = cc
	if s.fn != nil {
		return s.fn(ctx)
	}
	return nil
}

type orderedSecrets struct {
	SecretStore
	order *[]string
}

func (o orderedSecrets) Delete(ctx context.Context, id string) error {
	*o.order = append(*o.order, "delete")
	return o.SecretStore.Delete(ctx, id)
}

// revokeRig is a release rig whose worker revokes through a spy and logs into buf.
func revokeRig(t *testing.T, fn func(ctx context.Context) error) (*releaseRig, *revokeSpy, *[]string, *strings.Builder) {
	t.Helper()
	r := newReleaseRig(t, 0)
	var order []string
	spy := &revokeSpy{Connector: testFake(t), order: &order, got: &Credentials{}, fn: fn}
	var buf strings.Builder
	r.worker = &SecretDeleteWorker{
		Pool: r.app, Secrets: orderedSecrets{SecretStore: r.mem, order: &order},
		Registry: Registry{"fake": spy}, Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	}
	return r, spy, &order, &buf
}

func disconnectJob(t *testing.T, r *releaseRig) (deleteJob, string, []byte) {
	t.Helper()
	e := r.client(t, "Client One")
	c, g := seedActive(t, r.super, r.mem, r.tenant, e, "fake-co-1")
	secret, _, _ := r.mem.Access(context.Background(), SecretName("", g))
	if _, err := r.store.Disconnect(r.admin, e, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs, want 1", len(jobs))
	}
	return jobs[0], g, secret
}

func TestSecretDeleteWorker_RevokesBeforeTheDelete(t *testing.T) {
	r, spy, order, _ := revokeRig(t, nil)
	j, g, secret := disconnectJob(t, r)
	if err := r.work(j, 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !reflect.DeepEqual(*order, []string{"revoke", "delete"}) {
		t.Errorf("call order = %v, want revoke then delete", *order)
	}
	if spy.calls != 1 {
		t.Errorf("%d Revoke calls, want 1", spy.calls)
	}
	if spy.got.Kind != GrantOAuthRefresh || string(spy.got.Secret) != string(secret) {
		t.Errorf("Revoke got kind %q and secret %q, want %q and the stored %q", spy.got.Kind, spy.got.Secret, GrantOAuthRefresh, secret)
	}
	if holds(r.mem, SecretName("", g)) {
		t.Error("the secret survives its delete job")
	}
	if n := len(r.audit(t, "erp.token.deleted")); n != 1 {
		t.Errorf("%d erp.token.deleted rows, want 1", n)
	}
}

func TestSecretDeleteWorker_SharedRealmSkipsTheRevoke(t *testing.T) {
	r, spy, _, _ := revokeRig(t, nil)
	key := "fake-co-" + uuid.NewString()
	e := r.client(t, "Client One")
	c, g := seedActive(t, r.super, r.mem, r.tenant, e, key)
	other := seedTenant(t, r.super, "firm")
	seedConnRow(t, r.super, connRow{tenant: other, entity: seedClient(t, r.super, other, "Other"), key: key, status: "active"})
	if _, err := r.store.Disconnect(r.admin, e, c); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	jobs := r.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d delete jobs, want 1", len(jobs))
	}
	if err := r.work(jobs[0], 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if spy.calls != 0 {
		t.Errorf("%d Revoke calls, want 0 while another tenant holds the realm", spy.calls)
	}
	if holds(r.mem, SecretName("", g)) {
		t.Error("the secret survives its delete job")
	}
}

func TestSecretDeleteWorker_AFailedRevokeStillDeletes(t *testing.T) {
	canary := "canary-" + uuid.NewString()
	for name, c := range map[string]struct {
		fn      func(ctx context.Context) error
		timeout time.Duration
	}{
		"error":   {fn: func(context.Context) error { return errors.New(canary) }},
		"timeout": {fn: func(ctx context.Context) error { <-ctx.Done(); return errors.New(canary + ctx.Err().Error()) }, timeout: 50 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			r, spy, _, buf := revokeRig(t, c.fn)
			r.worker.revokeTimeout = c.timeout
			j, g, secret := disconnectJob(t, r)
			if err := r.work(j, 1); err != nil {
				t.Fatalf("Work: %v, want nil", err)
			}
			if spy.calls != 1 {
				t.Errorf("%d Revoke calls, want 1", spy.calls)
			}
			if holds(r.mem, SecretName("", g)) {
				t.Error("the secret survives a failed revoke")
			}
			if n := len(r.audit(t, "erp.token.deleted")); n != 1 {
				t.Errorf("%d erp.token.deleted rows, want 1", n)
			}
			log := buf.String()
			if !strings.Contains(log, "system=fake") || strings.Contains(log, canary) || strings.Contains(log, string(secret)) {
				t.Errorf("log %q, want the system and no canary or secret", log)
			}
		})
	}
}

func TestSecretDeleteWorker_RevokeSkips(t *testing.T) {
	withoutRevoke := func(t *testing.T, j deleteJob) deleteJob {
		m := j.argMap(t)
		delete(m, "revoke")
		delete(m, "grant_kind")
		b, _ := json.Marshal(m)
		return deleteJob{id: j.id, args: b}
	}
	t.Run("no Revoke flag", func(t *testing.T) {
		r, spy, _, _ := revokeRig(t, nil)
		j, g, _ := disconnectJob(t, r)
		if err := r.work(withoutRevoke(t, j), 1); err != nil || spy.calls != 0 || holds(r.mem, SecretName("", g)) {
			t.Errorf("err %v, %d Revoke calls, secret held %v; want nil, 0, false", err, spy.calls, holds(r.mem, SecretName("", g)))
		}
	})
	t.Run("secret already deleted", func(t *testing.T) {
		r, spy, _, buf := revokeRig(t, nil)
		j, g, _ := disconnectJob(t, r)
		if err := r.mem.Delete(context.Background(), SecretName("", g)); err != nil {
			t.Fatal(err)
		}
		if err := r.work(j, 1); err != nil || spy.calls != 0 || buf.Len() != 0 {
			t.Errorf("err %v, %d Revoke calls, log %q, want nil, 0 and no log", err, spy.calls, buf.String())
		}
	})
	t.Run("system not registered", func(t *testing.T) {
		r, spy, _, _ := revokeRig(t, nil)
		r.worker.Registry = Registry{}
		j, g, _ := disconnectJob(t, r)
		if err := r.work(j, 1); err != nil || spy.calls != 0 || holds(r.mem, SecretName("", g)) {
			t.Errorf("err %v, %d Revoke calls, secret held %v; want nil, 0, false", err, spy.calls, holds(r.mem, SecretName("", g)))
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		r, spy, _, buf := revokeRig(t, func(context.Context) error { return ErrUnsupported })
		j, g, _ := disconnectJob(t, r)
		if err := r.work(j, 1); err != nil || spy.calls != 1 || holds(r.mem, SecretName("", g)) {
			t.Errorf("err %v, %d Revoke calls, secret held %v; want nil, 1, false", err, spy.calls, holds(r.mem, SecretName("", g)))
		}
		if buf.Len() != 0 {
			t.Errorf("log %q, want none for ErrUnsupported", buf.String())
		}
	})
}
