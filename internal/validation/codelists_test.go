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

// activateRules makes a throwaway version carrying the given enum rules active.
func activateRules(t *testing.T, super *pgxpool.Pool, rules ...ruleSeed) {
	t.Helper()
	id, _ := seedVersion(t, super, false)
	for _, r := range rules {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, message, enabled)
			 VALUES ($1, $2, 'enum', 'supplier.postal_address.lga', $3::jsonb, 'error', 'bad code', $4)`,
			id, r.key, r.params, r.enabled); err != nil {
			t.Fatalf("seed rule %s: %v", r.key, err)
		}
	}
	sealAndActivate(t, super, id)
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
