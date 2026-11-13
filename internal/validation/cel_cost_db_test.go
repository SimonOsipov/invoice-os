package validation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestCEL_DB_GoldenRulesSameResultUnderLimit reads every golden cel rule and `when`
// from the migrated rules table, so a rule added later is covered without an edit.
func TestCEL_DB_GoldenRulesSameResultUnderLimit(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	type celRow struct{ typ, target, params string }
	var cels []celRow
	rows, err := app.Query(ctx, `SELECT DISTINCT type, COALESCE(target, ''), params::text FROM rules WHERE type = 'cel'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r celRow
		if err := rows.Scan(&r.typ, &r.target, &r.params); err != nil {
			t.Fatal(err)
		}
		cels = append(cels, r)
	}
	rows.Close()
	var whens []string
	rows, err = app.Query(ctx, `SELECT DISTINCT "when" FROM rules WHERE "when" IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			t.Fatal(err)
		}
		whens = append(whens, w)
	}
	rows.Close()
	if len(cels) == 0 || len(whens) == 0 {
		t.Fatalf("read %d cel rules and %d when clauses, want both non-empty", len(cels), len(whens))
	}

	payloads := map[string]Payload{"valid": validInvoicePayload(), "bad": badInvoicePayload(), "100 lines": manyLines(100)}
	for name, p := range payloads {
		for _, c := range cels {
			r := Rule{Key: "k", Type: TypeCEL, Target: c.target, Params: json.RawMessage(c.params),
				Severity: "error", Message: "m", Scope: "document", Enabled: true}
			sameWithAndWithoutLimit(t, "cel "+c.params+" on "+name, p, RuleSet{Rules: []Rule{r}})
		}
		for i := range whens {
			r := Rule{Key: "k", Type: TypeCEL, Params: json.RawMessage(`{"expr":"true"}`), When: &whens[i],
				Severity: "error", Message: "m", Scope: "document", Enabled: true}
			sameWithAndWithoutLimit(t, "when "+whens[i]+" on "+name, p, RuleSet{Rules: []Rule{r}})
		}
	}
}

// slowDraft opens a draft holding one rule that five nested .all() make run for hours.
func slowDraft(t *testing.T) (*Store, context.Context, func() bool) {
	t.Helper()
	super, _, store, _, ctx := draftSetup(t)
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddDraftRule(ctx, "e2e-slow", mustValid(t, "e2e-slow", "cel", "m", celParams(nested100(5)))); err != nil {
		t.Fatal(err)
	}
	return store, ctx, func() bool { return isSealed(t, super, draft.RuleSetVersionID) }
}

func TestDraft_DeadlineStopsSlowDraftRule(t *testing.T) {
	store, ctx, _ := slowDraft(t)
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := store.TestDraft(ctx, NewDefaultEngine(), map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v, want under 5s", d)
	}
}

func TestPublishDraft_DeadlineStopsSlowDraftRule(t *testing.T) {
	store, ctx, sealed := slowDraft(t)
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	far, _ := time.Parse("2006-01-02", deskFarDate)
	start := time.Now()
	_, err := store.PublishDraft(ctx, NewDefaultEngine(), far)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v, want under 5s", d)
	}
	if sealed() {
		t.Error("the draft was published")
	}
}
