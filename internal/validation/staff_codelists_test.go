// Store.CodeListSyncs against the dev DB and StaffCodeListSyncsHandler with a fake read.
// nrs_code_list_syncs is global and may hold real rows: fixtures use `t-<uuid>` list names, are written
// through the superuser pool, are read back by those names only and are deleted in t.Cleanup. No t.Parallel().
package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bindCodeListSyncsHandler builds StaffCodeListSyncsHandler over a read that reports the list it was given
// and returns the zero value of the read's result type. The ENGI-12-05 executor assigns it in an init in
// this file, e.g.
//
//	func init() { bindCodeListSyncsHandler = func(on func(*string)) http.Handler { return StaffCodeListSyncsHandler(
//		func(_ context.Context, l *string) (T, error) { on(l); return T{}, nil }, nil) } }
//
// where T is the read's result type.
var bindCodeListSyncsHandler func(onRead func(list *string)) http.Handler

type syncRow struct {
	syncedAt             time.Time
	entryCount           int
	added, removed, chng []string
	id                   string
}

func newSyncList() string { return "t-" + uuid.NewString() }

// seedSyncs inserts rows for list as the owner and deletes the list's rows in t.Cleanup.
func seedSyncs(t *testing.T, super *pgxpool.Pool, list string, rows ...syncRow) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		if _, err := super.Exec(ctx, `DELETE FROM nrs_code_list_syncs WHERE list = $1`, list); err != nil {
			t.Errorf("cleanup sync rows of %s: %v", list, err)
		}
	})
	for _, r := range rows {
		id := r.id
		if id == "" {
			id = uuid.NewString()
		}
		if _, err := super.Exec(ctx,
			`INSERT INTO nrs_code_list_syncs (id, list, synced_at, entry_count, added, removed, changed) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			id, list, r.syncedAt, r.entryCount, orEmpty(r.added), orEmpty(r.removed), orEmpty(r.chng)); err != nil {
			t.Fatalf("seed sync row of %s: %v", list, err)
		}
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// readSyncs calls Store.CodeListSyncs(ctx, list) by reflection, so the package compiles before the method
// exists, and returns the first result decoded from its JSON along with the JSON text.
func readSyncs(t *testing.T, store *Store, list *string) (map[string]any, string) {
	t.Helper()
	m := reflect.ValueOf(store).MethodByName("CodeListSyncs")
	if !m.IsValid() {
		t.Fatal("Store.CodeListSyncs does not exist")
	}
	if mt := m.Type(); mt.NumIn() != 2 || mt.NumOut() != 2 || mt.In(1) != reflect.TypeOf((*string)(nil)) {
		t.Fatalf("Store.CodeListSyncs has signature %s, want (context.Context, *string) (T, error)", mt)
	}
	out := m.Call([]reflect.Value{reflect.ValueOf(staffCtx(t, uuid.NewString())), reflect.ValueOf(list)})
	if err, _ := out[1].Interface().(error); err != nil {
		t.Fatalf("Store.CodeListSyncs: %v", err)
	}
	raw, err := json.Marshal(out[0].Interface())
	if err != nil {
		t.Fatalf("marshal the sync log: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("sync log %s is not a JSON object: %v", raw, err)
	}
	return body, string(raw)
}

func objects(t *testing.T, v any, what string) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %T (%v), want a JSON array", what, v, v)
	}
	out := make([]map[string]any, len(arr))
	for i, e := range arr {
		o, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] = %T, want an object", what, i, e)
		}
		out[i] = o
	}
	return out
}

func timeOf(t *testing.T, v any, what string) time.Time {
	t.Helper()
	s, _ := v.(string)
	got, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("%s = %v, want an RFC 3339 time: %v", what, v, err)
	}
	return got
}

func strings0(t *testing.T, v any, what string) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %T (%v), want a JSON array, not null", what, v, v)
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i], _ = e.(string)
	}
	return out
}

func TestStaffCodeLists_SummaryIsTheNewestRowPerList(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Microsecond)

	a, b, c := newSyncList(), newSyncList(), newSyncList()
	// The newest row of each list is neither the first nor the largest one, so an oldest-row,
	// first-row or largest-count pick is wrong.
	seedSyncs(t, super, a,
		syncRow{syncedAt: base.Add(2 * time.Hour), entryCount: 50, added: []string{"X"}},
		syncRow{syncedAt: base.Add(3 * time.Hour), entryCount: 12, added: []string{"A1", "A2"}, removed: []string{"R1"}, chng: []string{"C1", "C2", "C3"}},
		syncRow{syncedAt: base.Add(1 * time.Hour), entryCount: 99},
	)
	seedSyncs(t, super, b,
		syncRow{syncedAt: base.Add(5 * time.Hour), entryCount: 7, removed: []string{"R1", "R2", "R3", "R4"}},
		syncRow{syncedAt: base.Add(4 * time.Hour), entryCount: 70, added: []string{"X"}},
		syncRow{syncedAt: base.Add(6 * time.Hour), entryCount: 8, added: []string{"B1"}, removed: []string{"R1", "R2"}, chng: []string{"C1"}},
	)
	// Two rows at one instant: the larger id is the newest.
	seedSyncs(t, super, c,
		syncRow{syncedAt: base, entryCount: 3, added: []string{"X"}, id: "00000000-0000-4000-8000-000000000001"},
		syncRow{syncedAt: base, entryCount: 4, added: []string{"Y", "Z"}, id: "ffffffff-ffff-4fff-8fff-ffffffffffff"},
	)
	want := map[string]struct {
		at                         time.Time
		entries, added, rem, chang int
	}{
		a: {base.Add(3 * time.Hour), 12, 2, 1, 3},
		b: {base.Add(6 * time.Hour), 8, 1, 2, 1},
		c: {base, 4, 2, 0, 0},
	}

	body, raw := readSyncs(t, store, nil)
	lists := objects(t, body["lists"], "lists")
	if len(lists) < 3 {
		t.Fatalf("summary holds %d lists, want at least the 3 seeded: %s", len(lists), raw)
	}
	seen := map[string]bool{}
	var names []string
	for _, l := range lists {
		name, _ := l["list"].(string)
		names = append(names, name)
		w, mine := want[name]
		if !mine {
			continue
		}
		if seen[name] {
			t.Errorf("list %s appears twice in the summary", name)
		}
		seen[name] = true
		if got := timeOf(t, l["synced_at"], name+" synced_at"); !got.Equal(w.at) {
			t.Errorf("%s synced_at = %s, want %s", name, got, w.at)
		}
		for k, wantN := range map[string]int{"entry_count": w.entries, "added_count": w.added, "removed_count": w.rem, "changed_count": w.chang} {
			if got, ok := l[k].(float64); !ok || int(got) != wantN {
				t.Errorf("%s %s = %v, want %d", name, k, l[k], wantN)
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("summary carries %d of the 3 seeded lists: %s", len(seen), raw)
	}

	// Ordered by list, in the database's own order, one entry per list.
	rows, err := super.Query(context.Background(), `SELECT DISTINCT list FROM nrs_code_list_syncs ORDER BY list`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var wantNames []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		wantNames = append(wantNames, n)
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("summary lists = %v, want one per list ordered by list: %v", names, wantNames)
	}
}

func TestStaffCodeLists_ListDetailNewestFirstCappedAt30(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	base := time.Now().UTC().Add(-96 * time.Hour).Truncate(time.Microsecond)

	list, other, unknown := newSyncList(), newSyncList(), newSyncList()
	var rows []syncRow
	for i := 0; i < 31; i++ {
		r := syncRow{syncedAt: base.Add(time.Duration(i) * time.Hour), entryCount: i + 1, added: []string{fmt.Sprintf("a%d", i)}}
		if i%2 == 0 {
			r.chng = []string{fmt.Sprintf("c%d", i), fmt.Sprintf("d%d", i)}
		} else {
			r.removed = []string{fmt.Sprintf("r%d", i)}
		}
		rows = append(rows, r)
	}
	// Insert in a scrambled order so the order cannot be the insertion order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].entryCount%7 < rows[j].entryCount%7 })
	seedSyncs(t, super, list, rows...)
	seedSyncs(t, super, other, syncRow{syncedAt: base.Add(time.Hour), entryCount: 999})

	body, raw := readSyncs(t, store, &list)
	if body["list"] != list {
		t.Errorf("list = %v, want %s", body["list"], list)
	}
	syncs := objects(t, body["syncs"], "syncs")
	if len(syncs) != 30 {
		t.Fatalf("detail holds %d syncs, want 30 of the 31 seeded", len(syncs))
	}
	for k, s := range syncs {
		i := 30 - k // newest first: row 30 down to row 1; row 0 is the one cut
		if got := timeOf(t, s["synced_at"], "synced_at"); !got.Equal(base.Add(time.Duration(i) * time.Hour)) {
			t.Errorf("syncs[%d] synced_at = %s, want row %d at %s", k, got, i, base.Add(time.Duration(i)*time.Hour))
		}
		if n, _ := s["entry_count"].(float64); int(n) != i+1 {
			t.Errorf("syncs[%d] entry_count = %v, want %d", k, s["entry_count"], i+1)
		}
		if got, want := strings0(t, s["added"], "added"), []string{fmt.Sprintf("a%d", i)}; !reflect.DeepEqual(got, want) {
			t.Errorf("syncs[%d] added = %v, want %v", k, got, want)
		}
		var wantRem, wantChg []string
		wantRem, wantChg = []string{}, []string{}
		if i%2 == 0 {
			wantChg = []string{fmt.Sprintf("c%d", i), fmt.Sprintf("d%d", i)}
		} else {
			wantRem = []string{fmt.Sprintf("r%d", i)}
		}
		if got := strings0(t, s["removed"], "removed"); !reflect.DeepEqual(got, wantRem) {
			t.Errorf("syncs[%d] removed = %v, want %v", k, got, wantRem)
		}
		if got := strings0(t, s["changed"], "changed"); !reflect.DeepEqual(got, wantChg) {
			t.Errorf("syncs[%d] changed = %v, want %v", k, got, wantChg)
		}
	}
	if strings.Contains(raw, "null") || !strings.Contains(raw, `"removed":[]`) || !strings.Contains(raw, `"changed":[]`) {
		t.Errorf("empty arrays must marshal as [] and no field as null: %.300s", raw)
	}
	for _, sy := range syncs {
		if n, _ := sy["entry_count"].(float64); n == 999 {
			t.Errorf("detail leaks the row of another list: %.300s", raw)
		}
	}

	body, raw = readSyncs(t, store, &unknown)
	if body["list"] != unknown {
		t.Errorf("unknown list: list = %v, want %s", body["list"], unknown)
	}
	if s := objects(t, body["syncs"], "unknown list syncs"); len(s) != 0 {
		t.Errorf("unknown list holds %d syncs, want 0", len(s))
	}
	if !strings.Contains(raw, `"syncs":[]`) {
		t.Errorf("unknown list must answer \"syncs\":[], got %s", raw)
	}
}

// A later migration adds a column (ENGI-20 adds a sync state): the read names its columns, so it must not care.
func TestStaffCodeLists_ReadSurvivesAnExtraColumn(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	const col = "zz_extra_probe"
	drop := func() {
		if _, err := super.Exec(ctx, `ALTER TABLE nrs_code_list_syncs DROP COLUMN IF EXISTS `+col); err != nil {
			t.Errorf("drop the probe column: %v", err)
		}
	}
	drop()
	t.Cleanup(drop) // registered before the seeds, so it runs after their deletes
	if _, err := super.Exec(ctx, `ALTER TABLE nrs_code_list_syncs ADD COLUMN `+col+` text NOT NULL DEFAULT 'applied'`); err != nil {
		t.Fatalf("add the probe column: %v", err)
	}

	list := newSyncList()
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	seedSyncs(t, super, list,
		syncRow{syncedAt: base, entryCount: 2, added: []string{"A"}},
		syncRow{syncedAt: base.Add(time.Hour), entryCount: 5, added: []string{"B"}, chng: []string{"C"}},
	)

	body, raw := readSyncs(t, store, nil)
	var found bool
	for _, l := range objects(t, body["lists"], "lists") {
		if l["list"] == list {
			found = true
			if n, _ := l["entry_count"].(float64); n != 5 {
				t.Errorf("summary entry_count = %v with the extra column, want 5", l["entry_count"])
			}
		}
	}
	if !found {
		t.Errorf("summary lost the seeded list with the extra column: %.300s", raw)
	}

	body, _ = readSyncs(t, store, &list)
	syncs := objects(t, body["syncs"], "syncs")
	if len(syncs) != 2 {
		t.Fatalf("detail holds %d syncs with the extra column, want 2", len(syncs))
	}
	if n, _ := syncs[0]["entry_count"].(float64); n != 5 {
		t.Errorf("newest sync entry_count = %v with the extra column, want 5", syncs[0]["entry_count"])
	}
}

func TestStaffCodeListsHandler_ListParam(t *testing.T) {
	if bindCodeListSyncsHandler == nil {
		t.Fatal("StaffCodeListSyncsHandler does not exist: bindCodeListSyncsHandler is not assigned")
	}
	serve := func(target string) (code int, body string, calls []*string) {
		h := bindCodeListSyncsHandler(func(l *string) { calls = append(calls, l) })
		mux := http.NewServeMux()
		mux.Handle("GET /v1/staff/code-list-syncs", h)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Code, strings.TrimSpace(rec.Body.String()), calls
	}
	const route = "/v1/staff/code-list-syncs"

	for _, name := range []string{"hs-codes", "a", "0", strings.Repeat("a", 64), "a-b-9"} {
		code, body, calls := serve(route + "?list=" + name)
		if code != 200 || len(calls) != 1 || calls[0] == nil || *calls[0] != name {
			t.Errorf("?list=%s: %d %s, read called with %v, want 200 and the list %q", name, code, body, calls, name)
		}
	}

	code, body, calls := serve(route)
	if code != 200 || len(calls) != 1 || calls[0] != nil {
		t.Errorf("no list: %d %s, read called with %v, want 200 and one call with nil", code, body, calls)
	}

	for _, bad := range []string{
		"HS", "Hs-codes", "a%2Fb", "a_b", "hs%20codes", "hs-codes%0A", "%E2%82%AC", strings.Repeat("a", 65), "a%00b",
	} {
		code, body, calls := serve(route + "?list=" + bad)
		if code != 400 || body != `{"error":"invalid list"}` || len(calls) != 0 {
			t.Errorf("?list=%s: %d %s, read called %d times, want 400 {\"error\":\"invalid list\"} and no call", bad, code, body, len(calls))
		}
	}
}
