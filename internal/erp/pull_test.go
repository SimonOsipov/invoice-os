package erp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// --- fixtures ---------------------------------------------------------------------------

// pullEnv is an ingest env whose connector counts and scripts ListChanged, plus a pull worker.
type pullEnv struct {
	*ingestEnv
	queue   *queue.Client
	pull    *PullWorker
	cursors [][]byte                                       // the cursor of every ListChanged call
	onList  func(call int, cursor []byte) (Changes, error) // nil: the fake
}

func newPull(t *testing.T, key string) *pullEnv {
	t.Helper()
	super, app := storePools(t)
	return newPullOn(t, super, app, key)
}

func newPullOn(t *testing.T, super, app *pgxpool.Pool, key string) *pullEnv {
	t.Helper()
	e := newIngestOn(t, super, app, key)
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	p := &pullEnv{ingestEnv: e, queue: q}
	base := testFake(t)
	e.script = scriptConnector{
		Connector: base,
		list: func(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error) {
			p.cursors = append(p.cursors, slices.Clone(cursor))
			if p.onList != nil {
				return p.onList(len(p.cursors), cursor)
			}
			return base.ListChanged(ctx, c, scope, cursor)
		},
		fetch: func(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
			e.fetches.Add(1)
			if e.onFetch != nil {
				return e.onFetch(ctx, ref)
			}
			return base.Fetch(ctx, c, ref)
		},
	}
	if e.reg, err = NewRegistry(e.script); err != nil {
		t.Fatalf("registry: %v", err)
	}
	e.worker = e.workerWith(invoiceStore(e))
	p.pull = &PullWorker{Pool: app, Queue: q, Registry: e.reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), "DELETE FROM river_job WHERE args->>'tenant_id' = $1", e.tenant)
	})
	return p
}

func (p *pullEnv) pullArgs() PullArgs { return PullArgs{TenantID: p.tenant, ConnectionID: p.conn} }

func (p *pullEnv) pullAs(ctx context.Context, a PullArgs, attempt int) error {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	job := &river.Job[PullArgs]{
		JobRow: &rivertype.JobRow{ID: p.jobSeq.Add(1), Attempt: attempt, MaxAttempts: 8},
		Args:   a,
	}
	return p.pull.Work(ctx, job)
}

func (p *pullEnv) pullNow() error {
	p.t.Helper()
	return p.pullAs(context.Background(), p.pullArgs(), 1)
}

func (p *pullEnv) mustPull() {
	p.t.Helper()
	if err := p.pullNow(); err != nil {
		p.t.Fatalf("pull Work: %v", err)
	}
}

type queuedIngest struct {
	ID   int64
	Args IngestArgs
}

// ingestJobs lists the tenant's erp.ingest jobs with an id above after, oldest first.
func (p *pullEnv) ingestJobs(after int64) []queuedIngest {
	p.t.Helper()
	rows, err := p.super.Query(context.Background(),
		"SELECT id, args FROM river_job WHERE kind = 'erp.ingest' AND args->>'tenant_id' = $1 AND id > $2 ORDER BY id", p.tenant, after)
	if err != nil {
		p.t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []queuedIngest
	for rows.Next() {
		var q queuedIngest
		var raw []byte
		if err := rows.Scan(&q.ID, &raw); err != nil {
			p.t.Fatalf("scan river_job: %v", err)
		}
		if err := json.Unmarshal(raw, &q.Args); err != nil {
			p.t.Fatalf("decode args: %v", err)
		}
		out = append(out, q)
	}
	return out
}

func (p *pullEnv) lastJobID() int64 {
	var id int64
	mustScan(p.t, p.super, "SELECT coalesce(max(id), 0) FROM river_job", nil, &id)
	return id
}

func docIDsOf(js []queuedIngest) []string {
	var ids []string
	for _, j := range js {
		ids = append(ids, j.Args.SourceDocID)
	}
	return ids
}

func (p *pullEnv) runIngests(js []queuedIngest) {
	p.t.Helper()
	for _, j := range js {
		if err := p.workAs(context.Background(), p.worker, j.Args, 1); err != nil {
			p.t.Fatalf("ingest %s rev %s: %v", j.Args.SourceDocID, j.Args.SourceRevision, err)
		}
		// River would complete the job; a completed job still counts for the args uniqueness.
		mustExec(p.t, p.super, "UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1", j.ID)
	}
}

func (p *pullEnv) connState() (cursor []byte, epoch int64, lastSync *time.Time) {
	p.t.Helper()
	mustScan(p.t, p.super, "SELECT cursor, ingest_epoch, last_sync_at FROM erp_connections WHERE id = $1", []any{p.conn}, &cursor, &epoch, &lastSync)
	return cursor, epoch, lastSync
}

func (p *pullEnv) setCursor(c []byte) {
	p.t.Helper()
	mustExec(p.t, p.super, "UPDATE erp_connections SET cursor = $2 WHERE id = $1", p.conn, c)
}

func (p *pullEnv) disconnect() {
	p.t.Helper()
	mustExec(p.t, p.super, "UPDATE erp_connections SET status = 'disconnected', grant_id = NULL WHERE id = $1", p.conn)
}

func (p *pullEnv) reconnect() {
	p.t.Helper()
	g := seedGrant(p.t, p.super, p.tenant, "")
	mustExec(p.t, p.super, "UPDATE erp_connections SET status = 'active', grant_id = $2 WHERE id = $1", p.conn, g)
}

func (p *pullEnv) keyCount(epoch int64, docID, rev string, deleted bool) int {
	return countOf(p.t, p.super, "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2",
		p.tenant, IngestKey(p.conn, epoch, docID, rev, deleted))
}

// fakeFinalCursor is the cursor the fake hands back on its last page.
func fakeFinalCursor(t *testing.T, key string) []byte {
	t.Helper()
	var cursor []byte
	for range 20 {
		ch, err := testFake(t).ListChanged(context.Background(), Credentials{}, key, cursor)
		if err != nil {
			t.Fatalf("fake ListChanged: %v", err)
		}
		cursor = ch.Cursor
		if !ch.More {
			return cursor
		}
	}
	t.Fatal("the fake never ended its listing")
	return nil
}

func wantDocIDs(t *testing.T, js []queuedIngest, want ...string) {
	t.Helper()
	got := docIDsOf(js)
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("enqueued ingests = %v, want %v", got, w)
	}
}

// --- AC-1 -----------------------------------------------------------------------------

func TestPullArgs_UniquePerConnectionUntilCompleted(t *testing.T) {
	p := newPull(t, keyOne)
	other := p.addConnection(p.addClient("second client"), "fake-co-2")
	ctx := context.Background()
	c := p.queue.River()

	first, err := c.Insert(ctx, p.pullArgs(), nil)
	if err != nil {
		t.Fatalf("insert pull: %v", err)
	}
	if first.UniqueSkippedAsDuplicate {
		t.Fatal("the first pull of a connection was skipped as a duplicate")
	}
	again, err := c.Insert(ctx, p.pullArgs(), nil)
	if err != nil {
		t.Fatalf("insert pull again: %v", err)
	}
	if !again.UniqueSkippedAsDuplicate {
		t.Error("a second pull of the same connection was inserted while the first is queued")
	}
	sibling, err := c.Insert(ctx, PullArgs{TenantID: p.tenant, ConnectionID: other}, nil)
	if err != nil {
		t.Fatalf("insert pull of another connection: %v", err)
	}
	if sibling.UniqueSkippedAsDuplicate {
		t.Error("a pull of another connection was skipped as a duplicate")
	}

	mustExec(t, p.super, "UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1", first.Job.ID)
	next, err := c.Insert(ctx, p.pullArgs(), nil)
	if err != nil {
		t.Fatalf("insert pull after completion: %v", err)
	}
	if next.UniqueSkippedAsDuplicate {
		t.Error("a pull was skipped as a duplicate of a completed one")
	}
}

// --- AC-2 -----------------------------------------------------------------------------

func TestPull_EnqueuesOneIngestPerRefAndSavesTheCursor(t *testing.T) {
	p := newPull(t, keyOne)
	var before time.Time
	mustScan(t, p.super, "SELECT now()", nil, &before)

	p.mustPull()

	js := p.ingestJobs(0)
	wantDocIDs(t, js, "fake-co-1-inv-1", "fake-co-1-inv-2", "fake-co-1-inv-3")
	for _, j := range js {
		a := j.Args
		if a.TenantID != p.tenant || a.ConnectionID != p.conn || a.SourceRevision != "1" || a.Deleted {
			t.Errorf("ingest args = %+v, want tenant %s, connection %s, revision 1, not deleted", a, p.tenant, p.conn)
		}
	}
	cursor, epoch, lastSync := p.connState()
	if want := fakeFinalCursor(t, keyOne); !bytes.Equal(cursor, want) {
		t.Errorf("cursor = %x, want the fake's final cursor %x", cursor, want)
	}
	if epoch != 0 {
		t.Errorf("ingest_epoch = %d, want 0", epoch)
	}
	if lastSync == nil || lastSync.Before(before) {
		t.Errorf("last_sync_at = %v, want a time of this pull (after %v)", lastSync, before)
	}
	if n := len(p.cursors); n < 2 {
		t.Errorf("ListChanged called %d times, want at least 2 (the fake pages by 2)", n)
	}
}

func TestPull_ResumesFromTheStoredCursor(t *testing.T) {
	p := newPull(t, keyOne)
	page1, err := testFake(t).ListChanged(context.Background(), Credentials{}, keyOne, nil)
	if err != nil || !page1.More {
		t.Fatalf("fake page 1: %+v, %v", page1, err)
	}
	p.setCursor(page1.Cursor)

	p.mustPull()

	wantDocIDs(t, p.ingestJobs(0), "fake-co-1-inv-3")
	if len(p.cursors) == 0 || !bytes.Equal(p.cursors[0], page1.Cursor) {
		t.Errorf("first ListChanged cursor = %x, want the stored %x", p.cursors, page1.Cursor)
	}
}

func TestPull_CopiesTheDeletedFlag(t *testing.T) {
	p := newPull(t, "fake-co-3")

	p.mustPull()

	js := p.ingestJobs(0)
	if len(js) != 6 {
		t.Fatalf("enqueued %d ingests, want 6", len(js))
	}
	for _, j := range js {
		want := j.Args.SourceDocID == "fake-co-3-inv-6"
		if j.Args.Deleted != want {
			t.Errorf("%s deleted = %v, want %v", j.Args.SourceDocID, j.Args.Deleted, want)
		}
	}
}

func TestPull_TenantComesFromTheArgs(t *testing.T) {
	a := newPull(t, keyOne)
	b := newPullOn(t, a.super, a.app, keyOne)
	ctxA := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: a.tenant})

	if err := b.pullAs(ctxA, b.pullArgs(), 1); err != nil {
		t.Fatalf("pull of B under A's identity: %v", err)
	}

	if n := len(b.ingestJobs(0)); n != 3 {
		t.Errorf("tenant B ingests = %d, want 3", n)
	}
	if cursor, _, _ := b.connState(); cursor == nil {
		t.Error("tenant B cursor is NULL, want saved")
	}
	if n := len(a.ingestJobs(0)); n != 0 {
		t.Errorf("tenant A ingests = %d, want 0", n)
	}
	if cursor, _, lastSync := a.connState(); cursor != nil || lastSync != nil {
		t.Errorf("tenant A cursor/last_sync_at = %x/%v, want untouched", cursor, lastSync)
	}
}

// A pull whose args name another tenant's connection sees no row under RLS: it lists and enqueues nothing.
func TestPull_AnotherTenantsConnectionListsNothing(t *testing.T) {
	a := newPull(t, keyOne)
	b := newPullOn(t, a.super, a.app, keyOne)

	if err := a.pullAs(context.Background(), PullArgs{TenantID: a.tenant, ConnectionID: b.conn}, 1); err != nil {
		t.Fatalf("pull: %v, want nil: the connection is not visible to the args' tenant", err)
	}
	if len(a.cursors) != 0 || len(b.cursors) != 0 {
		t.Errorf("ListChanged calls A/B = %d/%d, want 0/0", len(a.cursors), len(b.cursors))
	}
	if n := len(a.ingestJobs(0)) + len(b.ingestJobs(0)); n != 0 {
		t.Errorf("ingest jobs = %d, want 0", n)
	}
	if cursor, epoch, lastSync := b.connState(); cursor != nil || epoch != 0 || lastSync != nil {
		t.Errorf("B's cursor/epoch/last_sync_at = %x/%d/%v, want untouched", cursor, epoch, lastSync)
	}
}

// A page commits only while the connection is as the pull found it: a disconnect, an epoch bump or a
// replaced cursor mid-pull leaves the listed page unqueued and the connection as the other writer left it.
func TestPull_StopsWhenTheConnectionChangesMidPull(t *testing.T) {
	for _, tc := range []struct{ name, set string }{
		{"disconnected", "status = 'disconnected', grant_id = NULL"},
		{"epoch bumped", "ingest_epoch = ingest_epoch + 1"},
		{"cursor replaced", "cursor = '\\x0f'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPull(t, keyOne)
			base := testFake(t)
			p.onList = func(call int, cursor []byte) (Changes, error) {
				if call == 1 {
					mustExec(t, p.super, "UPDATE erp_connections SET "+tc.set+" WHERE id = $1", p.conn)
				}
				return base.ListChanged(context.Background(), Credentials{}, keyOne, cursor)
			}
			if err := p.pullNow(); err != nil {
				t.Fatalf("pull: %v", err)
			}
			if n := len(p.ingestJobs(0)); n != 0 {
				t.Errorf("ingest jobs = %d, want 0: the page was listed for a connection that changed", n)
			}
			if tc.name == "cursor replaced" {
				if cursor, _, _ := p.connState(); string(cursor) != "\x0f" {
					t.Errorf("cursor = %x, want the replaced 0f kept", cursor)
				}
			}
		})
	}
}

// --- AC-3 -----------------------------------------------------------------------------

func TestPull_SameRevisionIsEnqueuedOnce(t *testing.T) {
	p := newPull(t, keyOne)
	p.mustPull()
	listsAfterFirst := len(p.cursors)
	mustExec(t, p.super, "UPDATE erp_connections SET cursor = NULL WHERE id = $1", p.conn)

	p.mustPull()

	if len(p.cursors) <= listsAfterFirst {
		t.Fatalf("the second pull listed nothing (%d calls), so it proves no dedupe", len(p.cursors))
	}
	if n := len(p.ingestJobs(0)); n != 3 {
		t.Errorf("ingest jobs = %d, want 3", n)
	}
	if n := countOf(t, p.super, "SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE 'erp.ingest:%'", p.tenant); n != 3 {
		t.Errorf("ingest idempotency keys = %d, want 3", n)
	}
}

func TestPull_NewRevisionIsEnqueuedAgain(t *testing.T) {
	p := newPull(t, keyOne)
	p.onList = func(call int, _ []byte) (Changes, error) {
		return Changes{Refs: []DocRef{{SourceDocID: "doc-1", SourceRevision: strconv.Itoa(call), Scope: keyOne}}, Cursor: []byte("c" + strconv.Itoa(call))}, nil
	}

	p.mustPull()
	p.mustPull()

	js := p.ingestJobs(0)
	if len(js) != 2 || js[0].Args.SourceRevision != "1" || js[1].Args.SourceRevision != "2" {
		t.Fatalf("ingests = %+v, want doc-1 at revision 1 then 2", js)
	}
}

func TestPull_VoidAtTheSameRevisionReachesTheDocument(t *testing.T) {
	p := newPull(t, keyOne)
	p.onList = func(call int, _ []byte) (Changes, error) {
		ref := DocRef{SourceDocID: "doc-1", SourceRevision: "1", Scope: keyOne, Deleted: call > 1}
		return Changes{Refs: []DocRef{ref}, Cursor: []byte("c" + strconv.Itoa(call))}, nil
	}
	p.serve(fakeDocument("doc-1", "SC-0001", custOne, seedLine))

	p.mustPull()
	p.mustPull()

	js := p.ingestJobs(0)
	if len(js) != 2 || js[0].Args.Deleted || !js[1].Args.Deleted {
		t.Fatalf("ingests = %+v, want a live one then a deleted one", js)
	}
	p.runIngests(js[:1])
	d := p.wantState("doc-1", "drafted")
	if d.Deleted != nil || d.InvoiceID == nil {
		t.Fatalf("after the live ingest: deleted_in_erp_at = %v, invoice = %v, want NULL and an invoice", d.Deleted, d.InvoiceID)
	}
	before := p.snapshot(*d.InvoiceID)

	p.runIngests(js[1:])

	d = p.wantState("doc-1", "drafted")
	if d.Deleted == nil {
		t.Error("deleted_in_erp_at is NULL after the void ingest, want set")
	}
	if after := p.snapshot(*d.InvoiceID); after != before {
		t.Errorf("the invoice changed:\nbefore %s\nafter  %s", before, after)
	}
}

// --- AC-4 -----------------------------------------------------------------------------

func TestPull_ConnectorErrorKeepsCommittedPages(t *testing.T) {
	p := newPull(t, keyOne)
	p.onList = func(call int, cursor []byte) (Changes, error) {
		if call == 1 {
			return Changes{Refs: []DocRef{{SourceDocID: "doc-a", SourceRevision: "1", Scope: keyOne}}, Cursor: []byte("p1"), More: true}, nil
		}
		if string(cursor) != "p1" {
			return Changes{}, fmt.Errorf("page 2 asked with cursor %q, want p1", cursor)
		}
		return Changes{}, errVendor
	}

	err := p.pullNow()

	if !errors.Is(err, errVendor) {
		t.Fatalf("Work err = %v, want the vendor error", err)
	}
	if _, snoozed := snoozeOf(err); snoozed {
		t.Error("a vendor error became a snooze")
	}
	wantDocIDs(t, p.ingestJobs(0), "doc-a")
	cursor, _, lastSync := p.connState()
	if string(cursor) != "p1" || lastSync == nil {
		t.Errorf("cursor/last_sync_at = %q/%v, want p1 and a time", cursor, lastSync)
	}
}

// --- AC-5 -----------------------------------------------------------------------------

func TestPull_SyncStoppedListsNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, p *pullEnv, a *PullArgs)
		exist bool
	}{
		{"active connection (control)", func(t *testing.T, p *pullEnv, _ *PullArgs) {
			mustExec(t, p.super, "UPDATE erp_connections SET cursor = NULL WHERE id = $1", p.conn)
		}, true},
		{"connection missing", func(t *testing.T, p *pullEnv, a *PullArgs) { a.ConnectionID = uuid.NewString() }, false},
		{"disconnected", func(t *testing.T, p *pullEnv, _ *PullArgs) { p.disconnect() }, true},
		{"reconsent", func(t *testing.T, p *pullEnv, _ *PullArgs) {
			mustExec(t, p.super, "UPDATE erp_connections SET status = 'reconsent' WHERE id = $1", p.conn)
		}, true},
		{"system not in the registry", func(t *testing.T, p *pullEnv, _ *PullArgs) {
			reg, err := NewRegistry(renamedConnector{Connector: p.script, name: "other"})
			if err != nil {
				t.Fatalf("registry: %v", err)
			}
			p.pull.Registry = reg
		}, true},
		{"workspace scheduled for deletion", func(t *testing.T, p *pullEnv, _ *PullArgs) {
			lockWorkspace(t, p.super, p.tenant, uuid.NewString())
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPull(t, keyOne)
			mustExec(t, p.super, "UPDATE erp_connections SET cursor = '\\x0102' WHERE id = $1", p.conn)
			a := p.pullArgs()
			tc.setup(t, p, &a)

			if err := p.pullAs(context.Background(), a, 1); err != nil {
				t.Fatalf("Work: %v, want nil: sync stopped is not an error", err)
			}
			if tc.name == "active connection (control)" { // an active connection lists, so the other cases stop for their condition alone
				if len(p.cursors) == 0 || len(p.ingestJobs(0)) != 3 {
					t.Errorf("an active connection: ListChanged calls = %d, ingest jobs = %d, want a listing and 3 jobs", len(p.cursors), len(p.ingestJobs(0)))
				}
				return
			}

			if n := len(p.cursors); n != 0 {
				t.Errorf("ListChanged called %d times, want 0", n)
			}
			if n := len(p.ingestJobs(0)); n != 0 {
				t.Errorf("ingest jobs = %d, want 0", n)
			}
			if !tc.exist {
				return
			}
			cursor, epoch, lastSync := p.connState()
			if !bytes.Equal(cursor, []byte{1, 2}) || epoch != 0 || lastSync != nil {
				t.Errorf("cursor/epoch/last_sync_at = %x/%d/%v, want 0102/0/NULL: only a skipped ingest bumps the epoch", cursor, epoch, lastSync)
			}
		})
	}
}

// --- AC-6 -----------------------------------------------------------------------------

type sweepEnv struct {
	t        *testing.T
	super    *pgxpool.Pool
	sweeper  *Sweeper
	firstJob int64
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	super, app := storePools(t)
	readerURL := os.Getenv("DATABASE_READER_URL")
	if readerURL == "" {
		t.Skip("erp sweep test skipped: set DATABASE_READER_URL")
	}
	reader, err := pgxpool.New(context.Background(), readerURL)
	if err != nil {
		t.Fatalf("connect reader: %v", err)
	}
	t.Cleanup(reader.Close)
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	reg, err := NewRegistry(scriptConnector{Connector: testFake(t)})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	s := &sweepEnv{t: t, super: super}
	mustScan(t, super, "SELECT coalesce(max(id), 0) FROM river_job", nil, &s.firstJob)
	// The sweep reaches every tenant of the dev database, so drop every pull it queued.
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), "DELETE FROM river_job WHERE kind = 'erp.pull' AND id > $1", s.firstJob)
	})
	s.sweeper = &Sweeper{Reader: reader, Pool: app, River: q.River(), Registry: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s
}

func (s *sweepEnv) sweep() {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.sweeper.SweepOnce(ctx); err != nil {
		s.t.Fatalf("SweepOnce: %v", err)
	}
}

// pulls lists the erp.pull jobs of the tenants as "tenant/connection", sorted.
func (s *sweepEnv) pulls(tenants ...string) []string {
	s.t.Helper()
	rows, err := s.super.Query(context.Background(),
		"SELECT args FROM river_job WHERE kind = 'erp.pull' AND args->>'tenant_id' = ANY($1::text[])", tenants)
	if err != nil {
		s.t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw []byte
		var a PullArgs
		if err := rows.Scan(&raw); err != nil {
			s.t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			s.t.Fatalf("decode: %v", err)
		}
		out = append(out, a.TenantID+"/"+a.ConnectionID)
	}
	slices.Sort(out)
	return out
}

func wantPulls(t *testing.T, got []string, want ...string) {
	t.Helper()
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("erp.pull jobs = %v, want %v", got, w)
	}
}

func TestSweep_EnqueuesOnePullPerActiveConnectionAcrossTenants(t *testing.T) {
	s := newSweepEnv(t)
	tenA := seedTenant(t, s.super, "firm")
	entA := seedClient(t, s.super, tenA, "sweep client A")
	activeA, _ := seedActiveConn(t, s.super, tenA, entA, "fake-co-1")
	seedConnRow(t, s.super, connRow{tenant: tenA, entity: entA, key: "fake-co-2", status: "disconnected"})
	otherSystem, _ := seedActiveConn(t, s.super, tenA, entA, "fake-co-3")
	mustExec(t, s.super, "UPDATE erp_connections SET system = 'other' WHERE id = $1", otherSystem)
	tenB := seedTenant(t, s.super, "firm")
	entB := seedClient(t, s.super, tenB, "sweep client B")
	activeB, _ := seedActiveConn(t, s.super, tenB, entB, "fake-co-1")

	s.sweep()

	wantPulls(t, s.pulls(tenA, tenB), tenA+"/"+activeA, tenB+"/"+activeB)
}

func TestSweep_SkipsALockedWorkspace(t *testing.T) {
	s := newSweepEnv(t)
	tenA := seedTenant(t, s.super, "firm")
	entA := seedClient(t, s.super, tenA, "sweep client A")
	seedActiveConn(t, s.super, tenA, entA, "fake-co-1")
	lockWorkspace(t, s.super, tenA, uuid.NewString())
	tenB := seedTenant(t, s.super, "firm")
	entB := seedClient(t, s.super, tenB, "sweep client B")
	activeB, _ := seedActiveConn(t, s.super, tenB, entB, "fake-co-1")

	s.sweep()

	wantPulls(t, s.pulls(tenA, tenB), tenB+"/"+activeB)
}

func TestSweep_TwiceDoesNotDuplicateAQueuedPull(t *testing.T) {
	s := newSweepEnv(t)
	tenA := seedTenant(t, s.super, "firm")
	entA := seedClient(t, s.super, tenA, "sweep client A")
	connA, _ := seedActiveConn(t, s.super, tenA, entA, "fake-co-1")
	tenB := seedTenant(t, s.super, "firm")
	entB := seedClient(t, s.super, tenB, "sweep client B")
	connB, _ := seedActiveConn(t, s.super, tenB, entB, "fake-co-2")

	s.sweep()
	s.sweep()

	wantPulls(t, s.pulls(tenA, tenB), tenA+"/"+connA, tenB+"/"+connB)
}

// --- AC-7 -----------------------------------------------------------------------------

func TestPull_StopsAfterFiftyPages(t *testing.T) {
	p := newPull(t, keyOne)
	p.onList = func(_ int, cursor []byte) (Changes, error) {
		i := 0
		if len(cursor) > 0 {
			i, _ = strconv.Atoi(string(cursor[1:]))
		}
		ref := DocRef{SourceDocID: fmt.Sprintf("doc-%02d", i), SourceRevision: "1", Scope: keyOne}
		return Changes{Refs: []DocRef{ref}, Cursor: []byte(fmt.Sprintf("p%d", i+1)), More: i < 59}, nil
	}

	if err := p.pullNow(); err != nil {
		t.Fatalf("Work: %v, want nil: the ceiling is not an error", err)
	}

	if n := len(p.ingestJobs(0)); n != 50 {
		t.Errorf("ingest jobs = %d, want 50", n)
	}
	if n := len(p.cursors); n != 50 {
		t.Errorf("ListChanged called %d times, want 50", n)
	}
	if cursor, _, _ := p.connState(); string(cursor) != "p50" {
		t.Errorf("cursor = %q, want p50", cursor)
	}

	p.mustPull() // the next tick continues from the saved cursor
	if n := len(p.ingestJobs(0)); n != 60 {
		t.Errorf("ingest jobs after the next pull = %d, want 60", n)
	}
}

// --- AC-8 -----------------------------------------------------------------------------

func TestPull_BadCursorRestartsFromTheBeginningWithoutDuplicates(t *testing.T) {
	p := newPull(t, keyOne)
	p.mustPull()
	if n := len(p.ingestJobs(0)); n != 3 {
		t.Fatalf("ingest jobs after the first pull = %d, want 3", n)
	}
	mustExec(t, p.super, "UPDATE erp_connections SET cursor = '\\x0a0b0c' WHERE id = $1", p.conn)

	if err := p.pullNow(); err != nil {
		t.Fatalf("Work on a bad cursor: %v, want nil", err)
	}
	cursor, epoch, _ := p.connState()
	if cursor != nil || epoch != 0 {
		t.Errorf("after a bad cursor: cursor/ingest_epoch = %x/%d, want NULL/0", cursor, epoch)
	}

	p.mustPull()

	if cursor, _, _ := p.connState(); !bytes.Equal(cursor, fakeFinalCursor(t, keyOne)) {
		t.Errorf("cursor after the restart = %x, want the fake's final cursor", cursor)
	}
	if n := len(p.ingestJobs(0)); n != 3 {
		t.Errorf("ingest jobs after the restart = %d, want still 3", n)
	}
}

func TestPull_RateLimitSnoozes(t *testing.T) {
	p := newPull(t, keyOne)
	p.onList = func(call int, _ []byte) (Changes, error) {
		if call == 1 {
			return Changes{Refs: []DocRef{{SourceDocID: "doc-a", SourceRevision: "1", Scope: keyOne}}, Cursor: []byte("p1"), More: true}, nil
		}
		return Changes{}, &RateLimitError{RetryAfter: 90 * time.Second}
	}

	err := p.pullNow()

	d, snoozed := snoozeOf(err)
	if !snoozed || d != 90*time.Second {
		t.Fatalf("Work err = %v, want a River snooze of 90s", err)
	}
	wantDocIDs(t, p.ingestJobs(0), "doc-a")
	if cursor, _, _ := p.connState(); string(cursor) != "p1" {
		t.Errorf("cursor = %q, want p1", cursor)
	}
}

func TestPull_BadCursorResetDoesNotRetakeAnExhaustedRow(t *testing.T) {
	p := newPull(t, keyOne)
	p.mustPull() // spends the epoch-0 key of every document
	p.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("exhausted")})
	before := p.docJSON(docOne)
	mustExec(t, p.super, "UPDATE erp_connections SET cursor = '\\x0a0b0c' WHERE id = $1", p.conn)

	for i := range 2 {
		if err := p.pullNow(); err != nil {
			t.Fatalf("Work %d: %v", i+1, err)
		}
	}

	if cursor, _, _ := p.connState(); !bytes.Equal(cursor, fakeFinalCursor(t, keyOne)) {
		t.Fatalf("cursor = %x, want the fake's final cursor: the second pull must have listed from the start", cursor)
	}
	n := 0
	for _, j := range p.ingestJobs(0) {
		if j.Args.SourceDocID == docOne {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ingest jobs for %s = %d, want 1", docOne, n)
	}
	if after := p.docJSON(docOne); after != before {
		t.Errorf("the exhausted row changed:\nbefore %s\nafter  %s", before, after)
	}
	d := p.doc(docOne)
	if d.State != "failed" || d.Failure == nil || *d.Failure != "exhausted" {
		t.Errorf("row = %s/%s, want failed/exhausted", d.State, str0(d.Failure))
	}
}

func TestPull_EpochBumpRelistsUnderNewKeys(t *testing.T) {
	p := newPull(t, keyOne)
	const doc4 = "fake-co-1-inv-4"
	withFourth := false
	base := testFake(t)
	p.onList = func(_ int, cursor []byte) (Changes, error) {
		ch, err := base.ListChanged(context.Background(), Credentials{}, keyOne, cursor)
		if err == nil && !ch.More && withFourth {
			ch.Refs = append(ch.Refs, DocRef{SourceDocID: doc4, SourceRevision: "1", Scope: keyOne})
		}
		return ch, err
	}
	fourth := fakeDocument(doc4, "FK1-0004", custOne, seedLine)
	p.onFetch = func(ctx context.Context, ref DocRef) (Document, error) {
		if ref.SourceDocID == doc4 {
			return fourth, nil
		}
		return base.Fetch(ctx, Credentials{}, ref)
	}

	p.mustPull()
	p.runIngests(p.ingestJobs(0))
	for _, id := range []string{"fake-co-1-inv-1", "fake-co-1-inv-2", "fake-co-1-inv-3"} {
		p.wantState(id, "drafted")
	}

	withFourth = true
	mark := p.lastJobID()
	p.mustPull()
	late := p.ingestJobs(mark)
	wantDocIDs(t, late, doc4)

	p.disconnect()
	p.runIngests(late) // skipped: the connection is not active
	if cursor, epoch, _ := p.connState(); cursor != nil || epoch != 1 {
		t.Fatalf("after the skipped ingest: cursor/ingest_epoch = %x/%d, want NULL/1", cursor, epoch)
	}
	p.noDoc(doc4)

	p.reconnect()
	mark = p.lastJobID()
	p.mustPull()
	relisted := p.ingestJobs(mark)
	wantDocIDs(t, relisted, "fake-co-1-inv-1", "fake-co-1-inv-2", "fake-co-1-inv-3", doc4)
	for _, id := range docIDsOf(relisted) {
		if p.keyCount(1, id, "1", false) != 1 || p.keyCount(0, id, "1", false) != 1 {
			t.Errorf("%s: want one epoch-1 key beside its spent epoch-0 key", id)
		}
	}

	fetched := p.fetches.Load()
	p.runIngests(relisted)

	p.wantState(doc4, "drafted")
	if got := p.fetches.Load() - fetched; got != 1 {
		t.Errorf("Fetch called %d times for the four relisted ingests, want 1: the three taken documents hit row 6", got)
	}
	if n := p.invoices(); n != 4 {
		t.Errorf("invoices = %d, want 4", n)
	}
}

func TestPull_ExhaustedRowIsRetakenAfterAnEpochBump(t *testing.T) {
	p := newPull(t, keyOne)
	p.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("exhausted")})
	p.disconnect()
	p.mustWork(docOne, "1") // a skipped ingest bumps the epoch
	if _, epoch, _ := p.connState(); epoch != 1 {
		t.Fatalf("ingest_epoch = %d, want 1", epoch)
	}
	p.reconnect()

	p.mustPull()

	var job *queuedIngest
	for _, j := range p.ingestJobs(0) {
		if j.Args.SourceDocID == docOne {
			job = &j
		}
	}
	if job == nil || job.Args.SourceRevision != "1" {
		t.Fatalf("no ingest of %s at revision 1 was enqueued", docOne)
	}
	if p.keyCount(1, docOne, "1", false) != 1 || p.keyCount(0, docOne, "1", false) != 0 {
		t.Error("want the ingest under the epoch-1 key and none under the epoch-0 key")
	}

	p.runIngests([]queuedIngest{*job})

	d := p.wantState(docOne, "drafted")
	if d.InvoiceID == nil || d.Failure != nil {
		t.Errorf("row invoice/failure = %v/%v, want an invoice and no failure", d.InvoiceID, d.Failure)
	}
}
