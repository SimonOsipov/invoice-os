package approval

import (
	"errors"
	"testing"
)

func TestPickIneligibleDelegate(t *testing.T) {
	a := &delegateIneligible{name: "Ada", step: "Manager"}
	b := &delegateIneligible{name: "Bola", step: "Partner"}
	tree := []Step{{ID: "s1", Then: []Step{{ID: "s2"}}, Else: []Step{{ID: "s3"}}}}

	if err := pickIneligibleDelegate(map[string]*delegateIneligible{}, nil, tree); err != nil {
		t.Fatalf("no offenders: got %v", err)
	}
	if got := pickIneligibleDelegate(map[string]*delegateIneligible{"s3": b, "s2": a}, b, tree); got != error(a) {
		t.Fatalf("walk order: want then-lane offender, got %v", got)
	}
	got := pickIneligibleDelegate(map[string]*delegateIneligible{"unplaced": b}, b, tree)
	if !errors.Is(got, ErrPolicyDelegateIneligible) {
		t.Fatalf("unplaced offender must fail closed, got %v", got)
	}
	var di *delegateIneligible
	if !errors.As(got, &di) || di != b {
		t.Fatalf("want first row names, got %v", di)
	}
}
