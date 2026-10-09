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
}

func syncRows(t *testing.T, super *pgxpool.Pool, list string) []syncRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT entry_count, added, removed, changed FROM nrs_code_list_syncs WHERE list = $1 ORDER BY synced_at, id`, list)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (syncRow, error) {
		var s syncRow
		return s, r.Scan(&s.entryCount, &s.added, &s.removed, &s.change)
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

	ch, err := s.SyncList(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	wantStrings(t, "Added", ch.Added, "D")
	wantStrings(t, "Removed", ch.Removed, "C")
	wantStrings(t, "Changed", ch.Changed, "B")
	syncs := syncRows(t, super, l.Name)
	if len(syncs) != 2 {
		t.Fatalf("%d sync rows, want 2", len(syncs))
	}
	last := syncs[1]
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
	rows, n := listRows(t, super, l.Name), len(syncRows(t, super, l.Name))
	// C is dropped (DELETE runs first); D's \u0000 makes the upsert fail.
	srv.set(200, envelope(entry("A", "a-new"), entry("B", "b1"), `{"code":"D","description":"x\u0000y"}`))
	_, err := s.SyncList(context.Background(), l)
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
