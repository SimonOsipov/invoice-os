// tolerance_relationship_db_test.go (QA, task-992, AC-6): the reconciler's exceedsTolerance
// ("0.01", per-row/subtotal arithmetic read off the page) and the line_sum rule's tolerance
// (0.005, seeded) are looser/tighter on purpose AND measure different quantities -- the rule
// folds quantity x unit_price against subtotal, the reconciler's row check folds against
// line_total instead. This pins the relationship as an inequality read from both LIVE sources
// (reconcileTolerance is unexported, so source-reading is forced, not a shortcut), so neither
// side can silently drift or invert without reddening this test.
package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/shopspring/decimal"
)

var reconcileToleranceRe = regexp.MustCompile(`const\s+reconcileTolerance\s*=\s*"([^"]+)"`)

// readReconcileTolerance extracts reconcile.go's reconcileTolerance literal by regex over the
// source at path -- a miss returns "", never a zero-value guess.
func readReconcileTolerance(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := reconcileToleranceRe.FindSubmatch(src)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// lineSumParams mirrors evaluators_math.go's unexported line_sum params shape -- just the
// fields this test reads.
type lineSumParams struct {
	Amount    string  `json:"amount"`
	Quantity  string  `json:"quantity"`
	Expected  string  `json:"expected"`
	Tolerance float64 `json:"tolerance"`
}

func TestTolerances_TheReconcilerIsLooserThanTheBlockingRule(t *testing.T) {
	_, app := dbTestPools(t)

	// Negative control: a synthetic source with no reconcileTolerance const extracts nothing --
	// proves the regex is a live comparator, not two empty strings silently agreeing.
	synthetic := filepath.Join(t.TempDir(), "no_const.go")
	if err := os.WriteFile(synthetic, []byte("package extraction\n\nconst somethingElse = \"0.01\"\n"), 0o600); err != nil {
		t.Fatalf("write synthetic source: %v", err)
	}
	if got := readReconcileTolerance(t, synthetic); got != "" {
		t.Fatalf("readReconcileTolerance on a source without the const = %q, want empty", got)
	}

	reconcileStr := readReconcileTolerance(t, "../extraction/reconcile.go")
	if reconcileStr == "" {
		t.Fatal("reconcileTolerance not found in ../extraction/reconcile.go")
	}
	reconcileTol, err := decimal.NewFromString(reconcileStr)
	if err != nil {
		t.Fatalf("parse reconciler tolerance %q: %v", reconcileStr, err)
	}
	if reconcileTol.IsZero() {
		t.Fatal("reconciler tolerance parsed as zero, want a real non-zero value")
	}

	rs := loadActive(t, app)
	var rule *Rule
	for i := range rs.Rules {
		if rs.Rules[i].Key == "line-items-sum-subtotal" {
			rule = &rs.Rules[i]
			break
		}
	}
	if rule == nil {
		t.Fatal("line-items-sum-subtotal not found in the active rule set")
	}
	var params lineSumParams
	if err := json.Unmarshal(rule.Params, &params); err != nil {
		t.Fatalf("unmarshal line-items-sum-subtotal params: %v", err)
	}
	ruleTol := decimal.NewFromFloat(params.Tolerance)
	if ruleTol.IsZero() {
		t.Fatal("rule tolerance parsed as zero, want a real non-zero value")
	}

	// The relationship, never a literal pair: a future edit to either source cannot silently
	// invert which side is looser without reddening this.
	if !reconcileTol.GreaterThan(ruleTol) {
		t.Errorf("reconciler tolerance %s is not strictly greater than the rule's %s -- the reconciler must stay looser", reconcileTol, ruleTol)
	}

	// The two also measure DIFFERENT quantities, so they can disagree at ANY tolerance -- not
	// merely a tolerance mismatch. The rule folds quantity x unit_price against subtotal; the
	// reconciler's own row check folds against line_total instead (reconcile.go:160, pinned by
	// TestReconcile_RowOffByExactlyOneMinorUnitPasses's own fixture).
	if params.Amount != "unit_price" || params.Quantity != "quantity" || params.Expected != "subtotal" {
		t.Errorf("line-items-sum-subtotal params = %+v, want amount=unit_price quantity=quantity expected=subtotal", params)
	}
}
