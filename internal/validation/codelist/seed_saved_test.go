package codelist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

func TestSyncAll_NullNRSSeedsEveryList(t *testing.T) {
	f := newSeedFixture(t)

	err := f.pull(t, nil)

	if !errors.Is(err, ErrEmptyList) {
		t.Errorf("SyncAll err = %v, want ErrEmptyList", err)
	}
	for _, l := range Lists {
		if n := len(f.rows(t, l.Name)); n < 1 {
			t.Errorf("%s holds %d rows after SyncAll with NRS data:null, want >= 1", l.Name, n)
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

// failedFetches serves each way a fetch fails without ErrEmptyList; the TLS one fails the x509 handshake.
func failedFetches(t *testing.T) map[string]string {
	t.Helper()
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
	}
	body := func(b string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, b) }
	}
	out := map[string]string{}
	for name, h := range map[string]http.HandlerFunc{
		"500": status(500), "503": status(503),
		"bad json": body(`{not json`), "not obj": body(`{"data":[1]}`),
	} {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		out[name] = srv.URL
	}
	tlsSrv := httptest.NewTLSServer(body(nullBody))
	t.Cleanup(tlsSrv.Close)
	out["tls x509"] = tlsSrv.URL
	return out
}

func failOnce(t *testing.T, f *seedFixture, url string) error {
	t.Helper()
	f.s.baseURL = url
	err := f.s.SyncAll(context.Background())
	if err == nil || errors.Is(err, ErrEmptyList) {
		t.Fatalf("SyncAll err = %v, want a fetch error that is not ErrEmptyList", err)
	}
	return err
}

func TestSyncAll_FetchFailureOnEmptyDBSeedsEveryListFromSavedCopy(t *testing.T) {
	for name, url := range failedFetches(t) {
		t.Run(name, func(t *testing.T) {
			f := newSeedFixture(t)
			failOnce(t, f, url)
			for _, l := range seededLists {
				if n := len(f.rows(t, l)); n < 1 {
					t.Errorf("%s holds %d rows after a %s fetch failure, want the saved copy", l, n, name)
				}
			}
		})
	}
}

func TestSyncAll_FetchFailureLeavesAHeldListUntouched(t *testing.T) {
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
	for name, url := range failedFetches(t) {
		if name != "tls x509" && name != "503" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			f := newSeedFixture(t)
			_ = f.pull(t, map[string]string{"states": envelope(parts...)})
			before := f.rows(t, "states")
			if len(before) < 36 || hasCode(before, "NG-LA") {
				t.Fatalf("seed: states holds %d rows (NG-LA %v), want >= 36 without NG-LA", len(before), hasCode(before, "NG-LA"))
			}
			failOnce(t, f, url)
			after := f.rows(t, "states")
			if len(after) != len(before) || hasCode(after, "NG-LA") {
				t.Errorf("states = %d rows (NG-LA %v) after a failed fetch, want %d rows without NG-LA", len(after), hasCode(after, "NG-LA"), len(before))
			}
		})
	}
}

func TestSyncAll_FetchFailureWithSeedingOffSeedsNothing(t *testing.T) {
	for name, url := range failedFetches(t) {
		t.Run(name, func(t *testing.T) {
			f := newSeedFixture(t)
			f.s.seed = nil
			failOnce(t, f, url)
			for real := range f.names {
				if n := len(f.rows(t, real)); n != 0 {
					t.Errorf("%s holds %d rows with seeding off, want 0", real, n)
				}
				if n := len(f.syncs(t, real)); n != 0 {
					t.Errorf("%s has %d sync rows with seeding off, want 0", real, n)
				}
			}
		})
	}
}

// A gate log must show which mode NRS was in: the fallback record carries the fetch error.
func TestSyncAll_FetchFailureSeedLogNamesTheListAndTheFetchError(t *testing.T) {
	f := newSeedFixture(t)
	var buf bytes.Buffer
	f.s.log = slog.New(slog.NewJSONHandler(&buf, nil))

	failOnce(t, f, failedFetches(t)["tls x509"])

	seeded := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "codelist seeded from saved copy" {
			continue
		}
		list, _ := rec["list"].(string)
		errText, _ := rec["err"].(string)
		seeded[list] = errText
	}
	if len(seeded) == 0 {
		t.Fatalf("no \"codelist seeded from saved copy\" record in the log:\n%s", buf.String())
	}
	for _, l := range seededLists {
		errText, ok := seeded[f.names[l]]
		if !ok {
			t.Errorf("no seeded record for list %s", l)
		} else if !strings.Contains(errText, "x509") {
			t.Errorf("%s seeded record err = %q, want the x509 fetch error", l, errText)
		}
	}
}

func TestSyncAll_ConnectionRefusedSeedsFromSavedCopyAndReturnsTheFetchError(t *testing.T) {
	f := newSeedFixture(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	err := failOnce(t, f, url)

	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the connection refused fetch error", err)
	}
	for _, l := range seededLists {
		if n := len(f.rows(t, l)); n < 1 {
			t.Errorf("%s holds %d rows after a refused connection, want the saved copy", l, n)
		}
	}
}

// A cancelled context also fails the seed's own transaction, so nothing is seeded and the error surfaces.
func TestSyncAll_CancelledContextSeedsNothingAndSurfacesAnError(t *testing.T) {
	f := newSeedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.s.baseURL = f.srv(nil)

	err := f.s.SyncAll(ctx)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("SyncAll err = %v, want context.Canceled", err)
	}
	for _, l := range seededLists {
		if n := len(f.rows(t, l)); n != 0 {
			t.Errorf("%s holds %d rows after a cancelled sync, want 0", l, n)
		}
	}
}

func TestSyncAll_FetchFailureWithACorruptSavedCopySurfacesTheSeedError(t *testing.T) {
	f := newSeedFixture(t)
	f.s.seed = fstest.MapFS{"saved/" + f.names["states"] + ".json": &fstest.MapFile{Data: []byte(`{not json`)}}

	err := failOnce(t, f, failedFetches(t)["503"])

	if !strings.Contains(err.Error(), "saved copy of "+f.names["states"]) {
		t.Errorf("err = %v, want the saved copy error for states", err)
	}
	if n := len(f.rows(t, "states")); n != 0 {
		t.Errorf("states holds %d rows from a corrupt saved copy, want 0", n)
	}
}

func TestSyncAll_FetchFailureWithoutASavedFileReturnsTheFetchError(t *testing.T) {
	f := newSeedFixture(t)
	f.s.seed = fstest.MapFS{}

	err := failOnce(t, f, failedFetches(t)["503"])

	if !strings.Contains(err.Error(), "status 503") || strings.Contains(err.Error(), "saved copy") {
		t.Errorf("err = %v, want only the 503 fetch error", err)
	}
	for _, l := range seededLists {
		if n := len(f.rows(t, l)); n != 0 {
			t.Errorf("%s holds %d rows with no saved file, want 0", l, n)
		}
	}
}

func TestSyncAll_RealPullAfterAFetchFailureSeedReplacesTheSeed(t *testing.T) {
	f := newSeedFixture(t)
	failOnce(t, f, failedFetches(t)["tls x509"])

	err := f.pull(t, map[string]string{"states": envelope(entry("NG-XX", "Xland"), entry("NG-YY", "Yland"))})

	if errors.Is(err, ErrHeld) {
		t.Fatalf("real pull held after a fetch-failure seed: %v", err)
	}
	if rows := f.rows(t, "states"); len(rows) != 2 || hasCode(rows, "NG-LA") {
		t.Errorf("states = %+v, want only NG-XX and NG-YY", rows)
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

func mustJSONArray(es []json.RawMessage) []byte {
	b, _ := json.Marshal(es)
	return b
}

// The files are raw NRS bodies: no "source" key, and every list decodes by its CodeKey.
func TestSavedCopies_AreRawNRSForEveryList(t *testing.T) {
	entries, err := saved.ReadDir("saved")
	if err != nil || len(entries) != len(Lists) {
		t.Fatalf("saved dir: %v, %d files, want %d", err, len(entries), len(Lists))
	}
	for _, l := range Lists {
		_, n, ok, err := savedCopy(saved, l)
		if !ok || err != nil || n < 1 {
			t.Errorf("%s: ok=%v err=%v entries=%d, want a non-empty saved copy", l.Name, ok, err, n)
		}
		body, _ := saved.ReadFile("saved/" + l.Name + ".json")
		var env map[string]json.RawMessage
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		if _, has := env["source"]; has {
			t.Errorf("%s carries a source key, want the raw NRS body", l.Name)
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
