// reconcile_internal_test.go: EXTR-05-03's tolerance constant. Package extraction: the
// constant is unexported.
package extraction

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestReconcile_ToleranceIsOneMinorUnit(t *testing.T) {
	got, err := decimal.NewFromString(reconcileTolerance)
	if err != nil {
		t.Fatalf("decimal.NewFromString(reconcileTolerance) error: %v", err)
	}
	want, err := decimal.NewFromString("0.01")
	if err != nil {
		t.Fatalf("test setup: decimal.NewFromString(\"0.01\") error: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("reconcileTolerance parses to %s, want 0.01", got)
	}
}
