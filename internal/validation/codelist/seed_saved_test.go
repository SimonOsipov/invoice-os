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

// emptyRealLists clears the real lists and again on cleanup; the real names are global rows.
func emptyRealLists(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	clear := func() {
		for _, table := range []string{"nrs_codes", "nrs_code_list_syncs"} {
			if _, err := super.Exec(context.Background(), `DELETE FROM `+table+` WHERE list = ANY($1)`, seededLists); err != nil {
				t.Fatalf("clear %s: %v", table, err)
			}
		}
	}
	clear()
	t.Cleanup(clear)
}

// SyncAll's error is not asserted: NRS answering empty is a failure whether or not the saved copy covers it.
func TestSyncAll_NullNRSOnEmptyDBSeedsFromSavedCopy(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	srv := fakeNRS(t, nil)

	_ = newSyncer(t, app, srv.URL).SyncAll(context.Background())

	for _, list := range seededLists {
		if n := len(listRows(t, super, list)); n < 1 {
			t.Errorf("list %s holds %d nrs_codes rows after SyncAll with NRS data:null, want >= 1", list, n)
		}
	}
	if rows := listRows(t, super, "states"); !hasCode(rows, "NG-LA") {
		t.Errorf("states has no NG-LA row (%d rows)", len(rows))
	}
	var percent string
	err := super.QueryRow(context.Background(),
		`SELECT entries->0->>'percent' FROM nrs_codes WHERE list = 'tax-categories' AND code = 'STANDARD_VAT'`).Scan(&percent)
	if err != nil {
		t.Errorf("tax-categories STANDARD_VAT: %v", err)
	} else if percent != "7.5" {
		t.Errorf("STANDARD_VAT percent = %q, want 7.5", percent)
	}
}

// Guard (may pass before the fix): a list the DB already holds is never touched by the saved copy.
func TestSyncAll_NullNRSLeavesAHeldListUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	// A subset of the saved states: the seed would only add NG-LA, so the shrink guard cannot mask a missing emptiness check.
	grouped, _, _, _ := savedCopy(listByName(t, "states"))
	var parts []string
	for code, es := range grouped {
		if code != "NG-LA" {
			for _, e := range es {
				parts = append(parts, string(e))
			}
		}
	}
	real := envelope(parts...)
	ctx := context.Background()

	_ = newSyncer(t, app, fakeNRS(t, map[string]string{"states": real}).URL).SyncAll(ctx)
	before := listRows(t, super, "states")
	if len(before) < 36 || hasCode(before, "NG-LA") {
		t.Fatalf("seed: states holds %d rows (NG-LA %v), want >= 36 without NG-LA", len(before), hasCode(before, "NG-LA"))
	}

	_ = newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(ctx)
	after := listRows(t, super, "states")

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
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	ctx := context.Background()

	_ = newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(ctx)
	if n := len(listRows(t, super, "states")); n < 10 {
		t.Fatalf("seed: states holds %d rows, want >= 10", n)
	}

	real := envelope(entry("NG-XX", "Xland"), entry("NG-YY", "Yland"))
	err := newSyncer(t, app, fakeNRS(t, map[string]string{"states": real}).URL).SyncAll(ctx)
	if errors.Is(err, ErrHeld) {
		t.Fatalf("SyncAll held the real pull: %v", err)
	}
	rows := listRows(t, super, "states")
	if len(rows) != 2 || !hasCode(rows, "NG-XX") || hasCode(rows, "NG-LA") {
		t.Errorf("states after real pull = %+v, want only NG-XX and NG-YY", rows)
	}
}

func TestSyncAll_NullNRSSeedsNoCopylessList(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	err := newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(context.Background())
	if !errors.Is(err, ErrEmptyList) {
		t.Errorf("SyncAll err = %v, want ErrEmptyList", err)
	}
	if n := len(listRows(t, super, "states")); n < 1 {
		t.Fatalf("states holds %d rows, want a seed", n)
	}
	for _, name := range []string{"countries", "currencies", "vat-exemptions"} {
		if _, err := newSyncer(t, app, fakeNRS(t, nil).URL).SyncList(context.Background(), listByName(t, name)); !errors.Is(err, ErrEmptyList) {
			t.Errorf("%s err = %v, want ErrEmptyList", name, err)
		}
		if n := len(listRows(t, super, name)); n != 0 {
			t.Errorf("%s holds %d rows, want 0", name, n)
		}
	}
}

func TestSyncAll_PartialNullSeedsOnlyTheNullLists(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	real := envelope(entry("NG-XX", "Xland"))
	_ = newSyncer(t, app, fakeNRS(t, map[string]string{"states": real}).URL).SyncAll(context.Background())
	if rows := listRows(t, super, "states"); len(rows) != 1 || rows[0].code != "NG-XX" {
		t.Errorf("states = %+v, want only NG-XX", rows)
	}
	if rows := listRows(t, super, "lgas"); len(rows) < 1 {
		t.Errorf("lgas holds %d rows, want a seed", len(rows))
	}
	if s := syncRows(t, super, "states"); len(s) != 1 || s[0].status != "applied" {
		t.Errorf("states syncs = %+v, want one applied", s)
	}
}

func TestSyncAll_SecondNullPullAfterSeedAddsNoSyncRow(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	ctx := context.Background()
	_ = newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(ctx)
	first := syncRows(t, super, "states")
	if len(first) != 1 || first[0].status != "released" {
		t.Fatalf("states syncs after seed = %+v, want one released", first)
	}
	_ = newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(ctx)
	if got := syncRows(t, super, "states"); len(got) != 1 {
		t.Errorf("states syncs = %d after second null pull, want 1", len(got))
	}
}

func TestSyncAll_FetchFailuresDoNotSeed(t *testing.T) {
	super, app := dbTestPools(t)
	for name, h := range map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{not json`) },
		"not obj":  func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[1]}`) },
	} {
		t.Run(name, func(t *testing.T) {
			emptyRealLists(t, super)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			err := newSyncer(t, app, srv.URL).SyncAll(context.Background())
			if err == nil || errors.Is(err, ErrEmptyList) {
				t.Errorf("err = %v, want a non-empty-list error", err)
			}
			for _, l := range seededLists {
				if n := len(listRows(t, super, l)); n != 0 {
					t.Errorf("%s holds %d rows after %s, want 0", l, n, name)
				}
			}
		})
	}
}

func TestSyncAll_ShrinkAfterRealPullIsHeldAgain(t *testing.T) {
	super, app := dbTestPools(t)
	emptyRealLists(t, super)
	ctx := context.Background()
	_ = newSyncer(t, app, fakeNRS(t, nil).URL).SyncAll(ctx)
	big := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		big = append(big, entry(fmt.Sprintf("NG-Z%02d", i), "z"))
	}
	if err := newSyncer(t, app, fakeNRS(t, map[string]string{"states": envelope(big...)}).URL).SyncAll(ctx); errors.Is(err, ErrHeld) {
		t.Fatalf("real pull after seed held: %v", err)
	}
	if n := len(listRows(t, super, "states")); n != 20 {
		t.Fatalf("states = %d rows, want 20", n)
	}
	err := newSyncer(t, app, fakeNRS(t, map[string]string{"states": envelope(big[:10]...)}).URL).SyncAll(ctx)
	if !errors.Is(err, ErrHeld) {
		t.Errorf("shrink after release: err = %v, want ErrHeld", err)
	}
	if n := len(listRows(t, super, "states")); n != 20 {
		t.Errorf("states = %d rows after held shrink, want 20", n)
	}
}

func TestSavedCopies_AreWellFormed(t *testing.T) {
	entries, err := saved.ReadDir("saved")
	if err != nil || len(entries) != 8 {
		t.Fatalf("saved dir: %v, %d files, want 8", err, len(entries))
	}
	raw := map[string]bool{"invoice-types": true, "payment-means": true, "tax-categories": true}
	for _, l := range Lists {
		grouped, n, ok, err := savedCopy(l)
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
	states, _, ok, err := savedCopy(listByName(t, "states"))
	if !ok || err != nil || len(states) < 37 {
		t.Fatalf("states: ok=%v err=%v n=%d", ok, err, len(states))
	}
	lgas, n, ok, err := savedCopy(listByName(t, "lgas"))
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
