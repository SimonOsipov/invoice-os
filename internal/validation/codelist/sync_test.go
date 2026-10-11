package codelist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// flipServer serves a body and status that a test can change between syncs.
type flipServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
}

func newFlipServer(t *testing.T, body string) *flipServer {
	t.Helper()
	f := &flipServer{status: 200, body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *flipServer) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func envelope(entries ...string) string {
	return `{"code":200,"data":[` + strings.Join(entries, ",") + `]}`
}

func entry(code, desc string) string {
	return fmt.Sprintf(`{"code":%q,"description":%q}`, code, desc)
}

const abcBody = `{"code":200,"data":[` +
	`{"code":"A","description":"a"},{"code":"B","description":"b1"},{"code":"B","description":"b2"},{"code":"C","description":"c"}]}`

func newSyncer(t *testing.T, app *pgxpool.Pool, srvURL string) *Syncer {
	t.Helper()
	return NewSyncer(app, srvURL, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type codeRow struct{ code, entries string }

func listRows(t *testing.T, super *pgxpool.Pool, list string) []codeRow {
	t.Helper()
	rows, err := super.Query(context.Background(), `SELECT code, entries::text FROM nrs_codes WHERE list = $1 ORDER BY code`, list)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (codeRow, error) {
		var c codeRow
		return c, r.Scan(&c.code, &c.entries)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type syncRow struct {
	entryCount             int
	added, removed, change []string
	status                 string
}

func syncRows(t *testing.T, super *pgxpool.Pool, list string) []syncRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT entry_count, added, removed, changed, status FROM nrs_code_list_syncs WHERE list = $1 ORDER BY synced_at, id`, list)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (syncRow, error) {
		var s syncRow
		return s, r.Scan(&s.entryCount, &s.added, &s.removed, &s.change, &s.status)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wantStrings(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if got == nil || !slices.Equal(got, append([]string{}, want...)) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

// release runs the runbook's guarded UPDATE as invoice_migrator and returns the rows it changed.
func release(t *testing.T, super *pgxpool.Pool, list string) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE nrs_code_list_syncs SET status = 'released' WHERE id = (SELECT id FROM nrs_code_list_syncs WHERE list = $1 ORDER BY synced_at DESC, id DESC LIMIT 1) AND status = 'held'`, list)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return tag.RowsAffected()
}

// cEntries returns entries for codes c<lo>..c<hi> with one description.
func cEntries(lo, hi int, desc string) []string {
	var out []string
	for i := lo; i <= hi; i++ {
		out = append(out, entry(fmt.Sprintf("c%02d", i), desc))
	}
	return out
}

func pull(parts ...[]string) string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return envelope(all...)
}

// seedN syncs c01..c<n> into a fresh list.
func seedN(t *testing.T, n int) (*Syncer, *flipServer, List, *pgxpool.Pool) {
	t.Helper()
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	srv := newFlipServer(t, pull(cEntries(1, n, "x")))
	s := newSyncer(t, app, srv.URL)
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	return s, srv, l, super
}

func wantHeld(t *testing.T, ch Change, err error, list string) {
	t.Helper()
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), list) {
		t.Fatalf("err = %v, want ErrHeld naming %s", err, list)
	}
	if !ch.Held {
		t.Errorf("Change.Held = false, want true")
	}
}

func testList(name string) List { return List{Name: name, CodeKey: "code"} }

// seeded syncs {A,B(x2),C} into a fresh list and returns its pieces.
func seeded(t *testing.T) (*Syncer, *flipServer, List, *pgxpool.Pool) {
	t.Helper()
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	srv := newFlipServer(t, abcBody)
	s := newSyncer(t, app, srv.URL)
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	return s, srv, l, super
}

func TestSync_FirstSyncStoresEveryCode(t *testing.T) {
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	srv := newFlipServer(t, abcBody)

	ch, err := newSyncer(t, app, srv.URL).SyncList(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	rows := listRows(t, super, l.Name)
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}
	if rows[1].code != "B" || strings.Count(rows[1].entries, `"code"`) != 2 {
		t.Errorf("B row = %+v, want 2 entries", rows[1])
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 1 {
		t.Fatalf("%d sync rows, want 1", len(syncs))
	}
	wantStrings(t, "added", syncs[0].added, "A", "B", "C")
	wantStrings(t, "removed", syncs[0].removed)
	wantStrings(t, "changed", syncs[0].change)
	if syncs[0].entryCount != 4 || ch.Entries != 4 {
		t.Errorf("entry_count %d, Change.Entries %d, want 4", syncs[0].entryCount, ch.Entries)
	}
}

func TestSync_RecordsAddedRemovedChanged(t *testing.T) {
	s, srv, l, super := seeded(t)
	srv.set(200, envelope(entry("A", "a"), entry("B", "b-new"), entry("D", "d")))

	held, err := s.SyncList(context.Background(), l)
	wantHeld(t, held, err, l.Name)
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	ch, err := s.SyncList(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	wantStrings(t, "Added", ch.Added, "D")
	wantStrings(t, "Removed", ch.Removed, "C")
	wantStrings(t, "Changed", ch.Changed, "B")
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 3 {
		t.Fatalf("%d sync rows, want 3", len(syncs))
	}
	last := syncs[len(syncs)-1]
	if last.status != "applied" {
		t.Errorf("last status = %q, want applied", last.status)
	}
	wantStrings(t, "row added", last.added, "D")
	wantStrings(t, "row removed", last.removed, "C")
	wantStrings(t, "row changed", last.change, "B")
	rows := listRows(t, super, l.Name)
	if len(rows) != 3 || rows[0].code != "A" || rows[1].code != "B" || rows[2].code != "D" {
		t.Fatalf("rows = %+v, want A, B, D", rows)
	}
	if !strings.Contains(rows[1].entries, "b-new") || strings.Contains(rows[1].entries, "b1") {
		t.Errorf("B entries not replaced: %s", rows[1].entries)
	}
}

func TestSync_UnchangedSyncRecordsEmptyDiff(t *testing.T) {
	s, srv, l, super := seeded(t)
	srv.set(200, `{"data":[{"description":"a","code":"A"},{"description":"b1","code":"B"},{"description":"b2","code":"B"},{"description":"c","code":"C"}],"code":200}`)

	ch, err := s.SyncList(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Added)+len(ch.Removed)+len(ch.Changed) != 0 {
		t.Errorf("Change = %+v, want empty", ch)
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 2 {
		t.Fatalf("%d sync rows, want 2", len(syncs))
	}
	wantStrings(t, "added", syncs[1].added)
	wantStrings(t, "removed", syncs[1].removed)
	wantStrings(t, "changed", syncs[1].change)
}

func assertUnchanged(t *testing.T, super *pgxpool.Pool, list string, rows []codeRow, syncs int) {
	t.Helper()
	if got := listRows(t, super, list); !slices.Equal(got, rows) {
		t.Errorf("rows changed: %+v, want %+v", got, rows)
	}
	if got := len(syncRows(t, super, list)); got != syncs {
		t.Errorf("%d sync rows, want %d", got, syncs)
	}
}

func TestSync_NRSErrorKeepsPreviousList(t *testing.T) {
	s, srv, l, super := seeded(t)
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))
	srv.set(500, "boom")
	if _, err := s.SyncList(context.Background(), l); err == nil {
		t.Fatal("want error")
	}
	assertUnchanged(t, super, l.Name, rows, n)
}

func TestSync_EmptyListKeepsPreviousList(t *testing.T) {
	s, srv, l, super := seeded(t)
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))
	srv.set(200, `{"code":200,"data":[]}`)
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrEmptyList) {
		t.Fatalf("err = %v, want ErrEmptyList", err)
	}
	assertUnchanged(t, super, l.Name, rows, n)
}

func TestSync_MalformedListKeepsPreviousList(t *testing.T) {
	s, srv, l, super := seeded(t)
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))
	srv.set(200, envelope(entry("A", "a"), `{"description":"no code"}`))
	if _, err := s.SyncList(context.Background(), l); err == nil {
		t.Fatal("want error")
	}
	assertUnchanged(t, super, l.Name, rows, n)
}

func TestSync_DBErrorRollsBackTheList(t *testing.T) {
	s, srv, l, super := seeded(t)
	// C is dropped (DELETE runs first); D's \u0000 makes the upsert fail.
	srv.set(200, envelope(entry("A", "a-new"), entry("B", "b1"), `{"code":"D","description":"x\u0000y"}`))
	held, err := s.SyncList(context.Background(), l)
	wantHeld(t, held, err, l.Name)
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))
	_, err = s.SyncList(context.Background(), l)
	if err == nil || !strings.Contains(err.Error(), "upsert") {
		t.Fatalf("err = %v, want an upsert error", err)
	}
	assertUnchanged(t, super, l.Name, rows, n)
}

func TestSyncAll_OneFailingListDoesNotStopOthers(t *testing.T) {
	super, app := dbTestPools(t)
	bad, good := testList(newListName(t, super)), testList(newListName(t, super))
	okSrv := newFlipServer(t, abcBody)
	failSrv := newFlipServer(t, "boom")
	failSrv.set(500, "boom")
	mux := http.NewServeMux()
	mux.Handle("/"+bad.Name, failSrv.Config.Handler)
	mux.Handle("/"+good.Name, okSrv.Config.Handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := newSyncer(t, app, srv.URL)
	s.lists = []List{bad, good}
	err := s.SyncAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), bad.Name) || strings.Contains(err.Error(), good.Name) {
		t.Fatalf("err = %v, want it to name only %s", err, bad.Name)
	}
	if len(listRows(t, super, good.Name)) != 3 || len(syncRows(t, super, good.Name)) != 1 {
		t.Errorf("good list not synced")
	}
	if len(listRows(t, super, bad.Name)) != 0 || len(syncRows(t, super, bad.Name)) != 0 {
		t.Errorf("failing list wrote rows")
	}
}

func TestSync_ConcurrentSyncsSerialise(t *testing.T) {
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	s := newSyncer(t, app, newFlipServer(t, abcBody).URL)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.SyncList(context.Background(), l)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 2 {
		t.Fatalf("%d sync rows, want 2", len(syncs))
	}
	if withAdded := len(syncs[0].added) > 0 != (len(syncs[1].added) > 0); !withAdded {
		t.Errorf("added = %v / %v, want exactly one non-empty", syncs[0].added, syncs[1].added)
	}
}

// A sync of a list whose advisory lock is held waits for it. The test holds the
// lock itself, so no timing decides the outcome.
func TestSync_WaitsForTheListAdvisoryLock(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	l := testList(newListName(t, super))
	s := newSyncer(t, app, newFlipServer(t, abcBody).URL)

	holder, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	const key = `hashtext('nrs_codes:' || $1)`
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(`+key+`)`, l.Name); err != nil {
		t.Fatal(err)
	}
	// The holder's committed result: A with other entries. The waiter must read it after the lock.
	if _, err := holder.Exec(ctx, `INSERT INTO nrs_codes (list, code, entries) VALUES ($1, 'A', '[{"code":"A","description":"held"}]')`, l.Name); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, err := s.SyncList(ctx, l); done <- err }()

	deadline := time.Now().Add(10 * time.Second)
	for waiting := false; !waiting; {
		select {
		case err := <-done:
			t.Fatalf("SyncList returned (%v) while another transaction held the list lock", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("SyncList never waited on the list lock")
		}
		if err := super.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND objid = `+key+`)`, l.Name).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(syncRows(t, super, l.Name)); n != 0 {
		t.Fatalf("%d sync rows written while the lock was held", n)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 1 {
		t.Fatalf("%d sync rows after release, want 1", len(syncs))
	}
	wantStrings(t, "added", syncs[0].added, "B", "C")
	wantStrings(t, "changed", syncs[0].change, "A")
}

func TestSync_ChangeListsAreSorted(t *testing.T) {
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	srv := newFlipServer(t, envelope(entry("c1", "x"), entry("c2", "x"), entry("c3", "x"), entry("c4", "x"), entry("c5", "x")))
	s := newSyncer(t, app, srv.URL)
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	srv.set(200, envelope(entry("c1", "y"), entry("c2", "y"), entry("c3", "y"), entry("d1", "x"), entry("d2", "x"), entry("d3", "x")))
	held, err := s.SyncList(context.Background(), l)
	wantHeld(t, held, err, l.Name)
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	ch, err := s.SyncList(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	wantStrings(t, "Added", ch.Added, "d1", "d2", "d3")
	wantStrings(t, "Removed", ch.Removed, "c4", "c5")
	wantStrings(t, "Changed", ch.Changed, "c1", "c2", "c3")
	syncs := syncRows(t, super, l.Name)
	row := syncs[len(syncs)-1]
	wantStrings(t, "row added", row.added, "d1", "d2", "d3")
	wantStrings(t, "row removed", row.removed, "c4", "c5")
	wantStrings(t, "row changed", row.change, "c1", "c2", "c3")
}

func TestSync_ShrinkOverTenPercentIsHeld(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	rows := listRows(t, super, l.Name)
	srv.set(200, pull(cEntries(1, 17, "x")))
	ch, err := s.SyncList(context.Background(), l)
	wantHeld(t, ch, err, l.Name)
	assertUnchanged(t, super, l.Name, rows, 2)
}

func TestSync_HeldPullWritesNoUpsert(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	rows := listRows(t, super, l.Name)
	srv.set(200, pull(cEntries(1, 1, "y"), cEntries(2, 17, "x"), []string{entry("n1", "x")}))
	ch, err := s.SyncList(context.Background(), l)
	wantHeld(t, ch, err, l.Name)
	assertUnchanged(t, super, l.Name, rows, 2)
}

func TestSync_ThresholdIsOfTheStoredCount(t *testing.T) {
	s, srv, l, _ := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x"), cEntries(21, 40, "x")))
	ch, err := s.SyncList(context.Background(), l)
	wantHeld(t, ch, err, l.Name)
}

func TestSync_SmallListSingleRemovalIsHeld(t *testing.T) {
	t.Run("3 stored", func(t *testing.T) {
		s, srv, l, _ := seeded(t)
		srv.set(200, envelope(entry("A", "a"), entry("B", "b1")))
		ch, err := s.SyncList(context.Background(), l)
		wantHeld(t, ch, err, l.Name)
	})
	t.Run("9 stored", func(t *testing.T) {
		s, srv, l, _ := seedN(t, 9)
		srv.set(200, pull(cEntries(1, 8, "x")))
		ch, err := s.SyncList(context.Background(), l)
		wantHeld(t, ch, err, l.Name)
	})
	t.Run("10 stored applies", func(t *testing.T) {
		s, srv, l, super := seedN(t, 10)
		srv.set(200, pull(cEntries(1, 9, "x")))
		if _, err := s.SyncList(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		if n := len(listRows(t, super, l.Name)); n != 9 {
			t.Errorf("%d rows, want 9", n)
		}
	})
}

func TestSync_HeldPullIsRecorded(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x")))
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 2 || syncs[0].status != "applied" {
		t.Fatalf("sync rows = %+v, want applied then held", syncs)
	}
	h := syncs[1]
	if h.status != "held" || h.entryCount != 17 {
		t.Errorf("held row = %+v, want status held, entry_count 17", h)
	}
	wantStrings(t, "removed", h.removed, "c18", "c19", "c20")
	wantStrings(t, "added", h.added)
	wantStrings(t, "changed", h.change)
}

// mux serves each list's flipServer under /<list>.
func mux(t *testing.T, srvs map[string]*flipServer) string {
	t.Helper()
	m := http.NewServeMux()
	for name, f := range srvs {
		m.Handle("/"+name, f.Config.Handler)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSyncAll_HeldListDoesNotStopOthers(t *testing.T) {
	super, app := dbTestPools(t)
	held, good := testList(newListName(t, super)), testList(newListName(t, super))
	heldSrv, goodSrv := newFlipServer(t, pull(cEntries(1, 20, "x"))), newFlipServer(t, abcBody)
	s := newSyncer(t, app, mux(t, map[string]*flipServer{held.Name: heldSrv, good.Name: goodSrv}))
	s.lists = []List{held, good}
	if err := s.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	heldSrv.set(200, pull(cEntries(1, 17, "x")))
	err := s.SyncAll(context.Background())
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), held.Name) || strings.Contains(err.Error(), good.Name) {
		t.Fatalf("err = %v, want ErrHeld naming only %s", err, held.Name)
	}
	if n := len(syncRows(t, super, good.Name)); n != 2 {
		t.Errorf("good list has %d sync rows, want 2", n)
	}
	if n := len(listRows(t, super, held.Name)); n != 20 {
		t.Errorf("held list has %d rows, want 20", n)
	}
}

func TestSync_RemovalOfExactlyTenPercentApplies(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 18, "x")))
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if n := len(listRows(t, super, l.Name)); n != 18 {
		t.Errorf("%d rows, want 18", n)
	}
	syncs := syncRows(t, super, l.Name)
	last := syncs[len(syncs)-1]
	if last.status != "applied" {
		t.Errorf("status = %q, want applied", last.status)
	}
	wantStrings(t, "removed", last.removed, "c19", "c20")
}

func TestSync_AddsAndChangesNeverHold(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 5, "y"), cEntries(6, 20, "x"), cEntries(21, 30, "x")))
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	syncs := syncRows(t, super, l.Name)
	last := syncs[len(syncs)-1]
	if len(last.added) != 10 || len(last.change) != 5 || last.status != "applied" {
		t.Errorf("last = %+v, want 10 added, 5 changed, applied", last)
	}
}

func TestSync_ReleasedHoldAppliesTheNextPull(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	short := pull(cEntries(1, 17, "x"))
	srv.set(200, short)
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if n := len(listRows(t, super, l.Name)); n != 17 {
		t.Errorf("%d rows, want 17", n)
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 3 {
		t.Fatalf("%d sync rows, want 3", len(syncs))
	}
	for i, want := range []string{"applied", "released", "applied"} {
		if syncs[i].status != want {
			t.Errorf("row %d status = %q, want %q", i, syncs[i].status, want)
		}
	}
	wantStrings(t, "removed", syncs[2].removed, "c18", "c19", "c20")
}

func TestSync_ReleaseEndsAfterOneApply(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x")))
	_, _ = s.SyncList(context.Background(), l)
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	srv.set(200, pull(cEntries(1, 14, "x")))
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld after the release was consumed", err)
	}
}

func TestSync_ReleaseIsPerList(t *testing.T) {
	super, app := dbTestPools(t)
	a, b := testList(newListName(t, super)), testList(newListName(t, super))
	aSrv, bSrv := newFlipServer(t, pull(cEntries(1, 20, "x"))), newFlipServer(t, pull(cEntries(1, 20, "x")))
	s := newSyncer(t, app, mux(t, map[string]*flipServer{a.Name: aSrv, b.Name: bSrv}))
	s.lists = []List{a, b}
	if err := s.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	aSrv.set(200, pull(cEntries(1, 17, "x")))
	bSrv.set(200, pull(cEntries(1, 17, "x")))
	if err := s.SyncAll(context.Background()); err == nil {
		t.Fatal("want both lists held")
	}
	if n := release(t, super, a.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	err := s.SyncAll(context.Background())
	if !errors.Is(err, ErrHeld) || strings.Contains(err.Error(), a.Name) || !strings.Contains(err.Error(), b.Name) {
		t.Fatalf("err = %v, want ErrHeld naming only %s", err, b.Name)
	}
	if n := len(listRows(t, super, a.Name)); n != 17 {
		t.Errorf("released list has %d rows, want 17", n)
	}
	if n := len(listRows(t, super, b.Name)); n != 20 {
		t.Errorf("other list has %d rows, want 20", n)
	}
}

func TestSync_OlderHeldRowForcesNothing(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x")))
	for range 2 {
		if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
			t.Fatalf("err = %v, want ErrHeld", err)
		}
	}
	if _, err := super.Exec(context.Background(), `UPDATE nrs_code_list_syncs SET status = 'released' WHERE id = (SELECT id FROM nrs_code_list_syncs WHERE list = $1 AND status = 'held' ORDER BY synced_at, id LIMIT 1)`, l.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld (only the latest row counts)", err)
	}
}

func TestSync_FailedFetchKeepsTheRelease(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	short := pull(cEntries(1, 17, "x"))
	srv.set(200, short)
	_, _ = s.SyncList(context.Background(), l)
	if n := release(t, super, l.Name); n != 1 {
		t.Fatalf("release affected %d rows, want 1", n)
	}
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))

	srv.set(500, "boom")
	if _, err := s.SyncList(context.Background(), l); err == nil || errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want a fetch error", err)
	}
	srv.set(200, `{"code":200,"data":[]}`)
	if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrEmptyList) {
		t.Fatalf("err = %v, want ErrEmptyList", err)
	}
	assertUnchanged(t, super, l.Name, rows, n)

	srv.set(200, short)
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if got := len(listRows(t, super, l.Name)); got != 17 {
		t.Errorf("%d rows, want 17", got)
	}
}

func TestSync_ReleaseUpdateIgnoresANonHeldLatestRow(t *testing.T) {
	t.Run("latest applied", func(t *testing.T) {
		s, srv, l, super := seedN(t, 20)
		if n := release(t, super, l.Name); n != 0 {
			t.Errorf("release affected %d rows, want 0", n)
		}
		srv.set(200, pull(cEntries(1, 17, "x")))
		if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
			t.Fatalf("err = %v, want ErrHeld", err)
		}
	})
	t.Run("older held under newer applied", func(t *testing.T) {
		s, srv, l, super := seedN(t, 20)
		short, full := pull(cEntries(1, 17, "x")), pull(cEntries(1, 20, "x"))
		srv.set(200, short)
		_, _ = s.SyncList(context.Background(), l)
		srv.set(200, full)
		if _, err := s.SyncList(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		if n := release(t, super, l.Name); n != 0 {
			t.Errorf("release affected %d rows, want 0", n)
		}
		srv.set(200, short)
		if _, err := s.SyncList(context.Background(), l); !errors.Is(err, ErrHeld) {
			t.Fatalf("err = %v, want ErrHeld", err)
		}
	})
}

func TestSync_HeldThenCompletePullApplies(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x")))
	_, _ = s.SyncList(context.Background(), l)
	srv.set(200, pull(cEntries(1, 20, "x")))
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if n := len(listRows(t, super, l.Name)); n != 20 {
		t.Errorf("%d rows, want 20", n)
	}
	syncs := syncRows(t, super, l.Name)
	if last := syncs[len(syncs)-1]; last.status != "applied" {
		t.Errorf("last status = %q, want applied", last.status)
	}
}

func TestSync_HeldRowWriteFailureIsReportedNotHeld(t *testing.T) {
	s, srv, l, super := seedN(t, 20)
	ctx := context.Background()
	fn := "fail_held_" + strings.ReplaceAll(strings.TrimPrefix(l.Name, "t-"), "-", "_")
	if _, err := super.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF NEW.list = '`+l.Name+`' AND NEW.status = 'held' THEN RAISE EXCEPTION 'held insert refused'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(ctx, `DROP TRIGGER IF EXISTS `+fn+` ON nrs_code_list_syncs`)
		_, _ = super.Exec(ctx, `DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	if _, err := super.Exec(ctx, `CREATE TRIGGER `+fn+` BEFORE INSERT ON nrs_code_list_syncs FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	rows := listRows(t, super, l.Name)
	srv.set(200, pull(cEntries(1, 17, "x")))
	_, err := s.SyncList(ctx, l)
	if err == nil || errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "held insert refused") {
		t.Fatalf("err = %v, want the held-insert DB error, not ErrHeld", err)
	}
	assertUnchanged(t, super, l.Name, rows, 1)
}

func TestSync_LaterCommitHasLaterSyncedAt(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	l := testList(newListName(t, super))
	s := newSyncer(t, app, newFlipServer(t, abcBody).URL)

	holder, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	const key = `hashtext('nrs_codes:' || $1)`
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(`+key+`)`, l.Name); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.SyncList(ctx, l); done <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for waiting := false; !waiting; {
		if time.Now().After(deadline) {
			t.Fatal("SyncList never waited on the list lock")
		}
		if err := super.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND objid = `+key+`)`, l.Name).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// entry_count 99 marks the holder's row.
	if _, err := holder.Exec(ctx, `INSERT INTO nrs_code_list_syncs (list, entry_count, added, removed, changed, synced_at) VALUES ($1, 99, '{}', '{}', '{}', clock_timestamp())`, l.Name); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 2 || syncs[0].entryCount != 99 {
		t.Fatalf("sync rows = %+v, want the holder's row (99) then the waiter's", syncs)
	}
}

func TestSync_FirstSyncIsNeverHeld(t *testing.T) {
	super, app := dbTestPools(t)
	l := testList(newListName(t, super))
	ch, err := newSyncer(t, app, newFlipServer(t, abcBody).URL).SyncList(context.Background(), l)
	if err != nil || ch.Held {
		t.Fatalf("err = %v, held = %v, want a clean apply", err, ch.Held)
	}
	syncs := syncRows(t, super, l.Name)
	if len(listRows(t, super, l.Name)) != 3 || len(syncs) != 1 || syncs[0].status != "applied" {
		t.Errorf("rows/syncs = %d/%+v, want 3 rows and one applied row", len(listRows(t, super, l.Name)), syncs)
	}
}
