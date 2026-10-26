package tenancy

import (
	"testing"
	"time"
)

func TestDecideAccountDeletion_Table(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	const (
		caller = "00000000-0000-4000-8000-0000000000c0"
		lo     = "00000000-0000-4000-8000-000000000001"
		hi     = "00000000-0000-4000-8000-000000000002"
	)
	m := func(id, role string, at time.Time) activeMember {
		return activeMember{UserID: id, Role: role, CreatedAt: at}
	}

	for _, tc := range []struct {
		name     string
		active   []activeMember
		want     AccountDeletionOutcome
		wantHeir string // "" = nil heir
	}{
		{"a preparer with one admin leaves", []activeMember{m(caller, "preparer", t1), m(hi, "admin", t1)}, OutcomeLeave, ""},
		{"a preparer leaves even when no admin is active", []activeMember{m(caller, "preparer", t1), m(hi, "reviewer", t2)}, OutcomeLeave, ""},
		{"an admin with a second active admin leaves", []activeMember{m(caller, "admin", t1), m(hi, "admin", t2)}, OutcomeLeave, ""},
		{"the sole admin hands on to the first joined", []activeMember{m(caller, "admin", t1), m(hi, "preparer", t2), m(lo, "reviewer", t1.Add(time.Minute))}, OutcomeHandOn, lo},
		{"the sole admin hands on to the earliest even when listed last", []activeMember{m(caller, "admin", t1), m(lo, "preparer", t2), m(hi, "preparer", t1.Add(time.Minute))}, OutcomeHandOn, hi},
		{"a tie on created_at goes to the lower user id", []activeMember{m(caller, "admin", t1), m(hi, "preparer", t2), m(lo, "preparer", t2)}, OutcomeHandOn, lo},
		{"a tie goes to the lower user id when listed the other way", []activeMember{m(caller, "admin", t1), m(lo, "preparer", t2), m(hi, "preparer", t2)}, OutcomeHandOn, lo},
		{"the sole admin alone deletes the workspace", []activeMember{m(caller, "admin", t1)}, OutcomeDeleteWorkspace, ""},
		{"no rows at all deletes the workspace", nil, OutcomeDeleteWorkspace, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, heir := decideAccountDeletion(caller, tc.active)
			if got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
			switch {
			case tc.wantHeir == "" && heir != nil:
				t.Errorf("heir = %q, want nil for %s", heir.UserID, tc.want)
			case tc.wantHeir != "" && (heir == nil || heir.UserID != tc.wantHeir):
				t.Errorf("heir = %v, want %s", heir, tc.wantHeir)
			}
		})
	}
}
