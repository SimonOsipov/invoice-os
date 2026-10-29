package systemhealth

import (
	"reflect"
	"slices"
	"testing"
)

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
		"nightly": "-nightly", "Nightly": "", "nightly2": "", " nightly": "", "nightly-pr-3": "-pr-3",
	} {
		if got := PreviewSuffix(in); got != want {
			t.Errorf("PreviewSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

type kinds = map[string]map[string]int64

func wantTotals(t *testing.T, name string, got JobTotals, depth, dead int64) {
	t.Helper()
	if got.QueueDepth != depth || got.DeadLettered != dead {
		t.Errorf("%s = %+v, want queue_depth %d dead_lettered %d", name, got, depth, dead)
	}
}

func TestJobViewOf_SumsQueueStatesAndDiscardedPerOwner(t *testing.T) {
	v := JobViewOf(kinds{
		"submission_submit":  {"available": 1, "scheduled": 2, "retryable": 3, "running": 4, "discarded": 5, "completed": 9, "cancelled": 9},
		"extraction_extract": {"discarded": 2},
		"contact_deliver":    {"available": 7, "discarded": 1},
	}, 4)
	wantTotals(t, "submission", v.Services["submission"], 10, 6)
	wantTotals(t, "notifications", v.Services["notifications"], 7, 1)
	wantTotals(t, "worker", v.Services["worker"], 17, 7)
	wantTotals(t, "total", v.Total, 17, 7)
}

func TestJobViewOf_SubmissionDeadLettersCountWithNoRiverRow(t *testing.T) {
	v := JobViewOf(nil, 3)
	wantTotals(t, "submission", v.Services["submission"], 0, 3)
	wantTotals(t, "worker", v.Services["worker"], 0, 3)
	wantTotals(t, "total", v.Total, 0, 3)
	wantTotals(t, "notifications", v.Services["notifications"], 0, 0)
}

func TestJobViewOf_SubmissionPollDiscardsAreNotCountedTwice(t *testing.T) {
	v := JobViewOf(kinds{"submission_poll": {"discarded": 2}}, 2)
	wantTotals(t, "submission", v.Services["submission"], 0, 2)
	wantTotals(t, "total", v.Total, 0, 2)
}

func TestJobViewOf_AnUnmappedKindCountsInTheTotalAndWorkerOnly(t *testing.T) {
	v := JobViewOf(kinds{"nl_other": {"available": 4}}, 0)
	wantTotals(t, "total", v.Total, 4, 0)
	wantTotals(t, "worker", v.Services["worker"], 4, 0)
	wantTotals(t, "submission", v.Services["submission"], 0, 0)
	wantTotals(t, "notifications", v.Services["notifications"], 0, 0)
	if got := sortedKeys(v.Services); !slices.Equal(got, []string{"notifications", "submission", "worker"}) {
		t.Errorf("services keys = %v, want an unmapped kind to add none", got)
	}
}

func TestJobViewOf_NoRowsGivesEveryOwnerAtZero(t *testing.T) {
	for name, in := range map[string]kinds{"nil": nil, "empty": {}} {
		v := JobViewOf(in, 0)
		if got := sortedKeys(v.Services); !slices.Equal(got, []string{"notifications", "submission", "worker"}) {
			t.Errorf("%s: services keys = %v", name, got)
		}
		wantTotals(t, name+" total", v.Total, 0, 0)
		for k, s := range v.Services {
			wantTotals(t, name+" "+k, s, 0, 0)
		}
	}
}

func TestJobViewOf_PendingIsNotQueued(t *testing.T) {
	v := JobViewOf(kinds{"demo_deal": {"pending": 3, "completed": 4, "cancelled": 5}, "nl_other": {"pending": 1, "completed": 1}}, 0)
	wantTotals(t, "notifications", v.Services["notifications"], 0, 0)
	wantTotals(t, "worker", v.Services["worker"], 0, 0)
	wantTotals(t, "total", v.Total, 0, 0)
}

func TestJobViewOf_SubmissionKindDiscardsAreSkippedWhenNoSubmissionJobIsDead(t *testing.T) {
	v := JobViewOf(kinds{"submission_submit": {"discarded": 5}, "submission_poll": {"discarded": 6}}, 0)
	wantTotals(t, "submission", v.Services["submission"], 0, 0)
	wantTotals(t, "total", v.Total, 0, 0)
}

func TestJobViewOf_WorkerEqualsTotalAndOwnersPartitionMappedKinds(t *testing.T) {
	v := JobViewOf(kinds{
		"submission_poll":      {"retryable": 1, "discarded": 2},
		"extraction_extract":   {"scheduled": 3, "discarded": 4},
		"registrant_intake":    {"running": 5, "discarded": 6},
		"blog_subscriber_sync": {"available": 7},
		"nl_other":             {"available": 8, "discarded": 9},
	}, 10)
	if v.Services["worker"] != v.Total {
		t.Errorf("worker = %+v, total = %+v, want equal", v.Services["worker"], v.Total)
	}
	wantTotals(t, "total", v.Total, 24, 10+4+6+9)
	wantTotals(t, "submission", v.Services["submission"], 4, 10+4)
	wantTotals(t, "notifications", v.Services["notifications"], 12, 6)
}

func TestJobOwners_MapEachKindToItsService(t *testing.T) {
	want := map[string]string{
		"submission_submit": "submission", "submission_poll": "submission", "extraction_extract": "submission",
		"contact_deliver": "notifications", "demo_deal": "notifications", "blog_subscriber_sync": "notifications", "registrant_intake": "notifications",
	}
	if !reflect.DeepEqual(JobOwners, want) {
		t.Errorf("JobOwners = %v, want %v", JobOwners, want)
	}
}

func TestDeadFromSubmissionJobs_AreTheSubmissionWriters(t *testing.T) {
	got := slices.Clone(DeadFromSubmissionJobs)
	slices.Sort(got)
	if !slices.Equal(got, []string{"submission_poll", "submission_submit"}) {
		t.Errorf("DeadFromSubmissionJobs = %v", got)
	}
	for _, k := range DeadFromSubmissionJobs {
		if JobOwners[k] != "submission" {
			t.Errorf("JobOwners[%s] = %q, want submission", k, JobOwners[k])
		}
	}
}
