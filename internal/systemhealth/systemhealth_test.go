package systemhealth

import "testing"

func TestCounts_ZeroFillsEveryStateAndKeepsOthers(t *testing.T) {
	raw := map[string]int64{"accepted": 2, "weird": 1}
	got := Counts(raw)
	if len(raw) != 2 {
		t.Errorf("input mutated: %v", raw)
	}
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
	weighted := map[string]int64{}
	for i, s := range JobStates {
		weighted[s] = 1 << i
	}
	if got := QueueDepth(weighted); got != 1+2+4 {
		t.Errorf("QueueDepth with distinct weights = %d, want 7", got)
	}
}

func TestPreviewSuffix_IsThePRNumberOnAPreviewOnly(t *testing.T) {
	for in, want := range map[string]string{
		"pr-545": "-pr-545", "invoice-os-pr-7": "-pr-7",
		"production": "", "": "", "staging": "", "pr-": "", "pr-abc": "", "PR-5": "",
		"pr-5": "-pr-5", "xpr-5": "", "pr-545-x": "", "pr-5\n": "", "invoice-os-pr-": "", "x-pr-pr-7": "-pr-7",
	} {
		if got := PreviewSuffix(in); got != want {
			t.Errorf("PreviewSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
