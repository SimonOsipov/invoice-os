package codelist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
)

const nullBody = `{"code":200,"data":null}`

var seededLists = []string{"states", "lgas", "hs-codes", "services-codes", "invoice-quantity-codes", "tax-categories"}

// fakeNRS answers served[list] for a list and data:null for every other path.
func fakeNRS(t *testing.T, served map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := served[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			body = nullBody
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedFixture is a Syncer over t-<uuid> copies of every registry list. The copies of the
// real lists with a saved file serve that file's bytes; seeding is on.
type seedFixture struct {
	s     *Syncer
	super *pgxpool.Pool
	names map[string]string // real list name -> t- name
	srv   func(served map[string]string) string
}

func newSeedFixture(t *testing.T) *seedFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := &seedFixture{super: super, names: map[string]string{}}
	files := fstest.MapFS{}
	var lists []List
	for _, l := range Lists {
		tl := List{Name: newListName(t, super), CodeKey: l.CodeKey}
		f.names[l.Name] = tl.Name
		lists = append(lists, tl)
		if body, err := saved.ReadFile("saved/" + l.Name + ".json"); err == nil {
			files["saved/"+tl.Name+".json"] = &fstest.MapFile{Data: body}
		}
	}
	f.srv = func(served map[string]string) string {
		byT := map[string]string{}
		for real, body := range served {
			byT[f.names[real]] = body
		}
		return fakeNRS(t, byT).URL
	}
	f.s = newSyncer(t, app, f.srv(nil))
	f.s.lists, f.s.seed = lists, files
	return f
}

// pull points the syncer at a fake NRS serving served (real list name -> body).
func (f *seedFixture) pull(t *testing.T, served map[string]string) error {
	t.Helper()
	f.s.baseURL = f.srv(served)
	return f.s.SyncAll(context.Background())
}

func (f *seedFixture) rows(t *testing.T, real string) []codeRow {
	t.Helper()
	return listRows(t, f.super, f.names[real])
}

func (f *seedFixture) syncs(t *testing.T, real string) []syncRow {
	t.Helper()
	return syncRows(t, f.super, f.names[real])
}

// SyncAll's error is not asserted: NRS answering empty is a failure whether or not the saved copy covers it.
func TestSyncAll_NullNRSOnEmptyDBSeedsFromSavedCopy(t *testing.T) {
	f := newSeedFixture(t)

	_ = f.pull(t, nil)

	for _, list := range seededLists {
		if n := len(f.rows(t, list)); n < 1 {
			t.Errorf("list %s holds %d nrs_codes rows after SyncAll with NRS data:null, want >= 1", list, n)
		}
	}
}

func TestSyncAll_NullNRSLeavesAHeldListUntouched(t *testing.T) {
	f := newSeedFixture(t)
	// A subset of the saved states: the seed would only add NG-LA, so the shrink guard cannot mask a missing emptiness check.
	grouped, _, _, _ := savedCopy(saved, listByName(t, "states"))
	var parts []string
	for code, es := range grouped {
		if code != "NG-LA" {
			for _, e := range es {
				parts = append(parts, string(e))
			}
		}
	}

	_ = f.pull(t, map[string]string{"states": envelope(parts...)})
	before := f.rows(t, "states")
	if len(before) < 36 || hasCode(before, "NG-LA") {
		t.Fatalf("seed: states holds %d rows (NG-LA %v), want >= 36 without NG-LA", len(before), hasCode(before, "NG-LA"))
	}

	_ = f.pull(t, nil)
	after := f.rows(t, "states")

	if len(after) != len(before) {
		t.Fatalf("states rows %d -> %d, want unchanged", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("states row %d changed: %+v -> %+v", i, before[i], after[i])
		}
	}
	if hasCode(after, "NG-LA") {
		t.Errorf("states gained NG-LA from the saved copy")
	}
}

func hasCode(rows []codeRow, code string) bool {
	for _, r := range rows {
		if r.code == code {
			return true
		}
	}
	return false
}

// The seed is recorded released, so a real pull that drops most seeded codes replaces it instead of being held.
func TestSyncAll_RealPullReplacesTheSeed(t *testing.T) {
	f := newSeedFixture(t)

	_ = f.pull(t, nil)
	if n := len(f.rows(t, "states")); n < 10 {
		t.Fatalf("seed: states holds %d rows, want >= 10", n)
	}

	real := envelope(entry("NG-XX", "Xland"), entry("NG-YY", "Yland"))
	err := f.pull(t, map[string]string{"states": real})
	if errors.Is(err, ErrHeld) {
		t.Fatalf("SyncAll held the real pull: %v", err)
	}
	rows := f.rows(t, "states")
	if len(rows) != 2 || !hasCode(rows, "NG-XX") || hasCode(rows, "NG-LA") {
		t.Errorf("states after real pull = %+v, want only NG-XX and NG-YY", rows)
	}
}

func TestSyncAll_NullNRSSeedsNoCopylessList(t *testing.T) {
	f := newSeedFixture(t)
	err := f.pull(t, nil)
	if !errors.Is(err, ErrEmptyList) {
		t.Errorf("SyncAll err = %v, want ErrEmptyList", err)
	}
	if n := len(f.rows(t, "states")); n < 1 {
		t.Fatalf("states holds %d rows, want a seed", n)
	}
	for _, name := range []string{"countries", "currencies", "vat-exemptions"} {
		if n := len(f.rows(t, name)); n != 0 {
			t.Errorf("%s holds %d rows, want 0", name, n)
		}
	}
}

func TestSyncAll_NullNRSWithSeedingOffSeedsNothing(t *testing.T) {
	f := newSeedFixture(t)
	f.s.seed = nil

	err := f.pull(t, nil)

	if !errors.Is(err, ErrEmptyList) {
		t.Errorf("SyncAll err = %v, want ErrEmptyList", err)
	}
	for real := range f.names {
		if n := len(f.rows(t, real)); n != 0 {
			t.Errorf("%s holds %d rows with seeding off, want 0", real, n)
		}
		if n := len(f.syncs(t, real)); n != 0 {
			t.Errorf("%s has %d sync rows with seeding off, want 0", real, n)
		}
	}
}

func TestSyncAll_PartialNullSeedsOnlyTheNullLists(t *testing.T) {
	f := newSeedFixture(t)
	_ = f.pull(t, map[string]string{"states": envelope(entry("NG-XX", "Xland"))})
	if rows := f.rows(t, "states"); len(rows) != 1 || rows[0].code != "NG-XX" {
		t.Errorf("states = %+v, want only NG-XX", rows)
	}
	if rows := f.rows(t, "lgas"); len(rows) < 1 {
		t.Errorf("lgas holds %d rows, want a seed", len(rows))
	}
	if s := f.syncs(t, "states"); len(s) != 1 || s[0].status != "applied" {
		t.Errorf("states syncs = %+v, want one applied", s)
	}
}

func TestSyncAll_SecondNullPullAfterSeedAddsNoSyncRow(t *testing.T) {
	f := newSeedFixture(t)
	_ = f.pull(t, nil)
	first := f.syncs(t, "states")
	if len(first) != 1 || first[0].status != "released" {
		t.Fatalf("states syncs after seed = %+v, want one released", first)
	}
	_ = f.pull(t, nil)
	if got := f.syncs(t, "states"); len(got) != 1 {
		t.Errorf("states syncs = %d after second null pull, want 1", len(got))
	}
}

func TestSyncAll_FetchFailuresDoNotSeed(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{not json`) },
		"not obj":  func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[1]}`) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSeedFixture(t)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			f.s.baseURL = srv.URL
			err := f.s.SyncAll(context.Background())
			if err == nil || errors.Is(err, ErrEmptyList) {
				t.Errorf("err = %v, want a non-empty-list error", err)
			}
			for _, l := range seededLists {
				if n := len(f.rows(t, l)); n != 0 {
					t.Errorf("%s holds %d rows after %s, want 0", l, n, name)
				}
			}
		})
	}
}

func TestSyncAll_ShrinkAfterRealPullIsHeldAgain(t *testing.T) {
	f := newSeedFixture(t)
	_ = f.pull(t, nil)
	big := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		big = append(big, entry(fmt.Sprintf("NG-Z%02d", i), "z"))
	}
	if err := f.pull(t, map[string]string{"states": envelope(big...)}); errors.Is(err, ErrHeld) {
		t.Fatalf("real pull after seed held: %v", err)
	}
	if n := len(f.rows(t, "states")); n != 20 {
		t.Fatalf("states = %d rows, want 20", n)
	}
	err := f.pull(t, map[string]string{"states": envelope(big[:10]...)})
	if !errors.Is(err, ErrHeld) {
		t.Errorf("shrink after release: err = %v, want ErrHeld", err)
	}
	if n := len(f.rows(t, "states")); n != 20 {
		t.Errorf("states = %d rows after held shrink, want 20", n)
	}
}

func TestSavedCopies_StatesHoldLagos(t *testing.T) {
	states, _, _, _ := savedCopy(saved, listByName(t, "states"))
	if _, ok := states["NG-LA"]; !ok {
		t.Errorf("saved states has no NG-LA (%d codes)", len(states))
	}
}

func TestSavedCopies_StandardVATIsSevenPointFive(t *testing.T) {
	grouped, _, _, _ := savedCopy(saved, listByName(t, "tax-categories"))
	var v []struct {
		Percent string `json:"percent"`
	}
	if err := json.Unmarshal(mustJSONArray(grouped["STANDARD_VAT"]), &v); err != nil || len(v) != 1 || v[0].Percent != "7.5" {
		t.Errorf("STANDARD_VAT = %+v (err %v), want one entry with percent 7.5", v, err)
	}
}

func TestSavedCopies_EverySearchableListHasEntries(t *testing.T) {
	for _, name := range seededLists {
		if _, n, ok, err := savedCopy(saved, listByName(t, name)); !ok || err != nil || n < 1 {
			t.Errorf("%s: ok=%v err=%v entries=%d, want a non-empty saved copy", name, ok, err, n)
		}
	}
}

func mustJSONArray(es []json.RawMessage) []byte {
	b, _ := json.Marshal(es)
	return b
}

func TestSavedCopies_AreWellFormed(t *testing.T) {
	entries, err := saved.ReadDir("saved")
	if err != nil || len(entries) != 8 {
		t.Fatalf("saved dir: %v, %d files, want 8", err, len(entries))
	}
	raw := map[string]bool{"invoice-types": true, "payment-means": true, "tax-categories": true}
	for _, l := range Lists {
		grouped, n, ok, err := savedCopy(saved, l)
		if !ok {
			if raw[l.Name] || l.Name == "states" {
				t.Errorf("%s has no saved copy", l.Name)
			}
			continue
		}
		if err != nil || n < 1 || len(grouped) < 1 {
			t.Errorf("%s: err=%v entries=%d", l.Name, err, n)
		}
		body, _ := saved.ReadFile("saved/" + l.Name + ".json")
		var env struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		if raw[l.Name] && env.Source != "" {
			t.Errorf("%s is raw NRS data but carries source %q", l.Name, env.Source)
		}
		if !raw[l.Name] && !strings.HasPrefix(env.Source, "hand-built, not NRS data") {
			t.Errorf("%s source = %q, want hand-built marker", l.Name, env.Source)
		}
	}
}

func TestSavedCopies_LgasAndStatesAgree(t *testing.T) {
	states, _, ok, err := savedCopy(saved, listByName(t, "states"))
	if !ok || err != nil || len(states) < 37 {
		t.Fatalf("states: ok=%v err=%v n=%d", ok, err, len(states))
	}
	lgas, n, ok, err := savedCopy(saved, listByName(t, "lgas"))
	if !ok || err != nil || n <= 50 {
		t.Fatalf("lgas: ok=%v err=%v n=%d, want > 50", ok, err, n)
	}
	var la, ab int
	for code, es := range lgas {
		if strings.HasPrefix(code, "NG-LA-") {
			la++
		}
		if strings.HasPrefix(code, "NG-AB-") {
			ab++
		}
		for _, e := range es {
			var v struct {
				StateCode string `json:"state_code"`
			}
			if err := json.Unmarshal(e, &v); err != nil {
				t.Fatal(err)
			}
			if _, found := states[v.StateCode]; !found {
				t.Errorf("lga %s state_code %q not in saved states", code, v.StateCode)
			}
		}
	}
	if la == 0 || ab == 0 {
		t.Errorf("Lagos LGAs=%d Abia LGAs=%d, want both > 0", la, ab)
	}
}
