package validation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

var testCodes = []string{"NG-AB-ANO", "NG-AB-ASO", "NG-LA-IKJ"}

// seedCodes inserts testCodes for a fresh t-<uuid> list and deletes them on cleanup.
func seedCodes(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	list := "t-" + uuid.NewString()
	t.Cleanup(func() {
		for _, table := range []string{"nrs_codes", "nrs_code_list_syncs"} {
			_, _ = super.Exec(context.Background(), `DELETE FROM `+table+` WHERE list = $1`, list)
		}
	})
	for _, c := range testCodes {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]')`, list, c); err != nil {
			t.Fatalf("seed nrs_codes: %v", err)
		}
	}
	return list
}

type ruleSeed struct {
	key, params string
	enabled     bool
}

// activateRules makes a throwaway version carrying the given enum rules the one in force today.
func activateRules(t *testing.T, super *pgxpool.Pool, rules ...ruleSeed) {
	t.Helper()
	id, _ := seedVersion(t, super)
	for _, r := range rules {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, message, enabled)
			 VALUES ($1, $2, 'enum', 'supplier.postal_address.lga', $3::jsonb, 'error', 'bad code', $4)`,
			id, r.key, r.params, r.enabled); err != nil {
			t.Fatalf("seed rule %s: %v", r.key, err)
		}
	}
	sealAndDate(t, super, id, todayUTC())
}

type loaders map[string]func() (RuleSet, error)

func loadersFor(app *pgxpool.Pool) loaders {
	s := NewStore(app)
	return loaders{
		"global": func() (RuleSet, error) { return s.LoadActiveRuleSetGlobal(context.Background()) },
		"tenant": func() (RuleSet, error) { return s.LoadActiveRuleSet(newTestIdentity()) },
	}
}

func ruleByKey(t *testing.T, rs RuleSet, key string) Rule {
	t.Helper()
	for _, r := range rs.Rules {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("rule %q not in rule set", key)
	return Rule{}
}

func TestCodeLists_LoadedAlongsideRuleSet(t *testing.T) {
	super, app := dbTestPools(t)
	list := seedCodes(t, super)
	activateRules(t, super,
		ruleSeed{"zz-list", `{"list":"` + list + `"}`, true},
		ruleSeed{"zz-inline", `{"values":["A"]}`, true})
	for name, load := range loadersFor(app) {
		t.Run(name, func(t *testing.T) {
			rs, err := load()
			if err != nil {
				t.Fatal(err)
			}
			got := ruleByKey(t, rs, "zz-list").Codes
			if len(got) != len(testCodes) {
				t.Fatalf("Codes = %v, want %v", got, testCodes)
			}
			for _, c := range testCodes {
				if _, ok := got[c]; !ok {
					t.Errorf("Codes missing %s", c)
				}
			}
			if c := ruleByKey(t, rs, "zz-inline").Codes; c != nil {
				t.Errorf("inline rule Codes = %v, want nil", c)
			}
		})
	}
}

func TestCodeLists_NoListRuleLoadsAsToday(t *testing.T) {
	super, app := dbTestPools(t)
	activateRules(t, super, ruleSeed{"zz-inline", `{"values":["A"]}`, true})
	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs.Rules {
		if r.Codes != nil {
			t.Errorf("rule %s Codes = %v, want nil", r.Key, r.Codes)
		}
	}
}

func TestCodeLists_MissingListFailsLoud(t *testing.T) {
	super, app := dbTestPools(t)
	list := "t-" + uuid.NewString()
	activateRules(t, super, ruleSeed{"zz-list", `{"list":"` + list + `"}`, true})
	for name, load := range loadersFor(app) {
		t.Run(name, func(t *testing.T) {
			_, err := load()
			if !errors.Is(err, ErrCodeListMissing) || !errors.Is(err, ErrNoActiveRuleSet) {
				t.Fatalf("err = %v, want ErrCodeListMissing wrapping ErrNoActiveRuleSet", err)
			}
			if !strings.Contains(err.Error(), list) {
				t.Errorf("err %q does not name list %s", err, list)
			}
		})
	}
}

func TestCodeLists_FailedSyncKeepsValidationWorking(t *testing.T) {
	super, app := dbTestPools(t)
	list := "t-" + uuid.NewString()
	t.Cleanup(func() {
		for _, table := range []string{"nrs_codes", "nrs_code_list_syncs"} {
			_, _ = super.Exec(context.Background(), `DELETE FROM `+table+` WHERE list = $1`, list)
		}
	})
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"data":[{"code":"NG-AB-ANO"},{"code":"NG-AB-ASO"},{"code":"NG-LA-IKJ"}]}`)
	}))
	t.Cleanup(srv.Close)
	s := codelist.NewSyncer(app, srv.URL, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l := codelist.List{Name: list, CodeKey: "code"}
	if _, err := s.SyncList(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, err := s.SyncList(context.Background(), l); err == nil {
		t.Fatal("second sync succeeded, want failure")
	}

	activateRules(t, super, ruleSeed{"zz-list", `{"list":"` + list + `"}`, true})
	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := ruleByKey(t, rs, "zz-list")
	if res := mustEvalList(t, r, lgaInvoice("NG-AB-ANO")); len(res.Violations) != 0 {
		t.Errorf("known code violations = %+v, want none", res.Violations)
	}
	wantOneViolation(t, mustEvalList(t, r, lgaInvoice("NG-AB-XXX")), "NRS list: "+list, "NG-AB-XXX")
}

func TestCodeLists_DisabledListRuleNeedsNoList(t *testing.T) {
	super, app := dbTestPools(t)
	list := "t-" + uuid.NewString()
	activateRules(t, super,
		ruleSeed{"zz-list", `{"list":"` + list + `"}`, false},
		ruleSeed{"zz-inline", `{"values":["A"]}`, true})
	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := ruleByKey(t, rs, "zz-list").Codes; c != nil {
		t.Errorf("disabled rule Codes = %v, want nil", c)
	}
}

func TestCodeLists_MalformedListParamDoesNotFailLoad(t *testing.T) {
	super, app := dbTestPools(t)
	activateRules(t, super, ruleSeed{"zz-list", `{"list":5}`, true})
	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDefaultEngine().Evaluate(Payload{"invoice": map[string]any{}}, RuleSet{Version: rs.Version, Rules: []Rule{ruleByKey(t, rs, "zz-list")}}); err == nil {
		t.Error("Evaluate succeeded, want a config error")
	}
}

type queryCounter struct {
	pgx.Tx
	n    int
	sqls []string
}

func (c *queryCounter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	c.sqls = append(c.sqls, sql)
	return c.Tx.Query(ctx, sql, args...)
}

func countingTx(t *testing.T, app *pgxpool.Pool) *queryCounter {
	t.Helper()
	tx, err := app.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return &queryCounter{Tx: tx}
}

func listParams(list string) []byte { return []byte(`{"list":"` + list + `"}`) }

func TestCodeLists_AttachRunsOneQueryForAllLists(t *testing.T) {
	super, app := dbTestPools(t)
	a, b := seedCodes(t, super), seedCodes(t, super)
	enum := func(key, list string) Rule {
		return Rule{Key: key, Type: TypeEnum, Scope: "document", Enabled: true, Params: listParams(list)}
	}
	rules := []Rule{enum("a1", a), enum("a2", a), enum("b1", b),
		{Key: "inline", Type: TypeEnum, Scope: "document", Enabled: true, Params: []byte(`{"values":["A"]}`)}}
	tx := countingTx(t, app)
	if err := attachCodeLists(context.Background(), tx, rules); err != nil {
		t.Fatal(err)
	}
	if tx.n != 1 {
		t.Errorf("queries = %d, want 1 for 2 distinct lists", tx.n)
	}
	for _, i := range []int{0, 1, 2} {
		if len(rules[i].Codes) != len(testCodes) {
			t.Errorf("rule %s Codes = %v, want %d codes", rules[i].Key, rules[i].Codes, len(testCodes))
		}
	}
}

func TestCodeLists_AttachSkipsRulesTheEngineSkips(t *testing.T) {
	_, app := dbTestPools(t)
	missing := "t-" + uuid.NewString()
	rules := []Rule{
		{Key: "off", Type: TypeEnum, Scope: "document", Enabled: false, Params: listParams(missing)},
		{Key: "line-scope", Type: TypeEnum, Scope: "line", Enabled: true, Params: listParams(missing)},
		{Key: "not-enum", Type: TypeFormat, Scope: "document", Enabled: true, Params: listParams(missing)},
		{Key: "blank", Type: TypeEnum, Scope: "document", Enabled: true, Params: listParams("")},
		{Key: "null", Type: TypeEnum, Scope: "document", Enabled: true, Params: []byte(`{"list":null}`)},
		{Key: "bad", Type: TypeEnum, Scope: "document", Enabled: true, Params: []byte(`{"list":5}`)},
		{Key: "notobj", Type: TypeEnum, Scope: "document", Enabled: true, Params: []byte(`5`)},
	}
	tx := countingTx(t, app)
	if err := attachCodeLists(context.Background(), tx, rules); err != nil {
		t.Fatal(err)
	}
	if tx.n != 0 {
		t.Errorf("queries = %d, want 0 when no selected rule names a list", tx.n)
	}
	for _, r := range rules {
		if r.Codes != nil {
			t.Errorf("rule %s Codes = %v, want nil", r.Key, r.Codes)
		}
	}
}

func TestCodeLists_BlankAndNullListDoNotFailLoad(t *testing.T) {
	super, app := dbTestPools(t)
	activateRules(t, super,
		ruleSeed{"zz-blank", `{"list":""}`, true},
		ruleSeed{"zz-null", `{"list":null}`, true},
		ruleSeed{"zz-notobj", `5`, true})
	if _, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background()); err != nil {
		t.Fatalf("load failed: %v", err)
	}
}

func (c *queryCounter) count(substr string) (n int) {
	for _, q := range c.sqls {
		if strings.Contains(q, substr) {
			n++
		}
	}
	return n
}

// listRuleVersion makes a version dated `from` with one enabled enum rule naming list.
func listRuleVersion(t *testing.T, super *pgxpool.Pool, from, key, list string) {
	t.Helper()
	id, _ := seedVersion(t, super)
	if _, err := super.Exec(context.Background(),
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, message, enabled)
		 VALUES ($1, $2, 'enum', 'supplier.postal_address.lga', $3::jsonb, 'error', 'bad code', true)`,
		id, key, `{"list":"`+list+`"}`); err != nil {
		t.Fatalf("seed rule %s: %v", key, err)
	}
	sealAndDate(t, super, id, from)
}

func TestStore_LoadForDatesLoadsEachVersionOnce(t *testing.T) {
	super, app := dbTestPools(t)
	a, _ := seedVersion(t, super)
	seedFullRule(t, super, a, ruleFixture{Key: "t-a", Enabled: true})
	sealAndDate(t, super, a, "3001-01-01")
	b, _ := seedVersion(t, super)
	seedFullRule(t, super, b, ruleFixture{Key: "t-b", Enabled: true})
	sealAndDate(t, super, b, "3001-06-01")

	tx := countingTx(t, app)
	got, err := loadForDatesTx(context.Background(), tx, []string{"3001-02-01", "3001-03-01", "3001-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len(result) = %d, want 3", len(got))
	}
	if n := tx.count("FROM rules WHERE"); n != 2 {
		t.Errorf("rules SELECT ran %d times, want 2 (one per distinct version)", n)
	}
	if n := tx.count("rule_set_version_for"); n != 1 {
		t.Errorf("date SELECT ran %d times, want 1", n)
	}
}

func TestCodeLists_EveryLoadedVersionCarriesItsLists(t *testing.T) {
	super, app := dbTestPools(t)
	listA, listB := seedCodes(t, super), seedCodes(t, super)
	listRuleVersion(t, super, "3001-01-01", "t-a", listA)
	listRuleVersion(t, super, "3001-06-01", "t-b", listB)

	got, err := NewStore(app).LoadForDates(context.Background(), []string{"3001-02-01", "3001-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	for date, key := range map[string]string{"3001-02-01": "t-a", "3001-07-01": "t-b"} {
		if c := ruleByKey(t, got[date], key).Codes; len(c) != len(testCodes) {
			t.Errorf("%s: rule %s Codes = %v, want %d codes", date, key, c, len(testCodes))
		}
	}
}

func TestCodeLists_MissingListInOneVersionFailsTheLoad(t *testing.T) {
	super, app := dbTestPools(t)
	listA := seedCodes(t, super)
	missing := "t-" + uuid.NewString()
	listRuleVersion(t, super, "3001-01-01", "t-a", listA)
	listRuleVersion(t, super, "3001-06-01", "t-b", missing)

	_, err := NewStore(app).LoadForDates(context.Background(), []string{"3001-02-01", "3001-07-01"})
	if !errors.Is(err, ErrCodeListMissing) || !errors.Is(err, ErrNoActiveRuleSet) {
		t.Fatalf("err = %v, want ErrCodeListMissing and ErrNoActiveRuleSet", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err %q does not name the list %s", err, missing)
	}
}
