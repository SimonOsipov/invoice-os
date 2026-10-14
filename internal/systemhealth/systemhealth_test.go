package systemhealth

import "testing"

func TestCounts_ZeroFillsEveryStateAndKeepsOthers(t *testing.T) {
	got := Counts(map[string]int64{"accepted": 2, "weird": 1})
	for _, s := range JobStates {
		if _, ok := got[s]; !ok {
			t.Errorf("state %q missing", s)
		}
	}
	if len(JobStates) != 8 || got["cancelled"] != 0 || got["accepted"] != 2 || got["weird"] != 1 {
		t.Errorf("got %v", got)
	}
}

func TestQueueDepth_SumsTheThreeLiveStates(t *testing.T) {
	all := map[string]int64{}
	for _, s := range JobStates {
		all[s] = 1
	}
	if got := QueueDepth(all); got != 3 {
		t.Errorf("QueueDepth = %d, want 3", got)
	}
}
