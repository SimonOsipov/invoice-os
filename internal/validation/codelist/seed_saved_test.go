package codelist

import (
	"context"
	"errors"
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
	real := envelope(entry("NG-XX", "Xland"), entry("NG-YY", "Yland"))
	ctx := context.Background()

	_ = newSyncer(t, app, fakeNRS(t, map[string]string{"states": real}).URL).SyncAll(ctx)
	before := listRows(t, super, "states")
	if len(before) != 2 {
		t.Fatalf("seed: states holds %d rows, want 2", len(before))
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
