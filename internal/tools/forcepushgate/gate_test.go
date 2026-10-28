package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// PR #140 (BUG-04) as GitHub actually recorded it: the branch was force-pushed at
// 10:25:30, the PR was marked ready at 12:38:33, and it was reviewed at 12:46:45.
// The rewrite happened while it was still a draft, so nothing a reviewer had read
// was destroyed — this must PASS, and it is the case that keeps the gate narrow.
var pr140 = []event{
	{Event: "committed", CreatedAt: "2026-08-06T06:54:49Z"},
	{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T10:25:30Z"},
	{Event: "committed", CreatedAt: "2026-08-06T12:30:51Z"},
	{Event: "ready_for_review", CreatedAt: "2026-08-06T12:38:33Z"},
	{Event: "reviewed", SubmittedAt: "2026-08-06T12:46:45Z", State: "commented"},
	{Event: "merged", CreatedAt: "2026-08-06T17:10:01Z"},
}

func ready(created string) pull { return pull{Number: 140, Draft: false, CreatedAt: created} }

const bot = "dependabot[bot]"

func prBy(login, created string) pull {
	p := ready(created)
	p.User.Login = login
	return p
}

func pushBy(login, at string) event {
	e := event{Event: "head_ref_force_pushed", CreatedAt: at}
	e.Actor.Login = login
	return e
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		pr       pull
		timeline []event
		wantOK   bool
		wantsMsg string
	}{
		{
			name:     "PR #140 as it happened: rewrite while still a draft",
			pr:       ready("2026-08-06T06:50:00Z"),
			timeline: pr140,
			wantOK:   true,
			wantsMsg: "no force push since",
		},
		{
			name: "the same rewrite moved to AFTER the PR went ready",
			pr:   ready("2026-08-06T06:50:00Z"),
			timeline: []event{
				{Event: "ready_for_review", CreatedAt: "2026-08-06T12:38:33Z"},
				{Event: "reviewed", SubmittedAt: "2026-08-06T12:46:45Z"},
				{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T13:00:00Z"},
			},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name: "a fresh review after the rewrite clears it",
			pr:   ready("2026-08-06T06:50:00Z"),
			timeline: []event{
				{Event: "ready_for_review", CreatedAt: "2026-08-06T12:38:33Z"},
				{Event: "reviewed", SubmittedAt: "2026-08-06T12:46:45Z"},
				{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T13:00:00Z"},
				{Event: "reviewed", SubmittedAt: "2026-08-06T13:30:00Z"},
			},
			wantOK:   true,
			wantsMsg: "reviewed again at",
		},
		{
			name: "a review BEFORE the rewrite does not clear it",
			pr:   ready("2026-08-06T06:50:00Z"),
			timeline: []event{
				{Event: "ready_for_review", CreatedAt: "2026-08-06T12:00:00Z"},
				{Event: "reviewed", SubmittedAt: "2026-08-06T12:10:00Z"},
				{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T12:20:00Z"},
			},
			wantOK: false,
		},
		{
			name: "only the LAST rewrite matters — an earlier one already re-reviewed",
			pr:   ready("2026-08-06T06:50:00Z"),
			timeline: []event{
				{Event: "ready_for_review", CreatedAt: "2026-08-06T12:00:00Z"},
				{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T12:20:00Z"},
				{Event: "reviewed", SubmittedAt: "2026-08-06T12:30:00Z"},
				{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T12:40:00Z"},
			},
			wantOK: false,
		},
		{
			name:     "a PR opened ready is reviewable from creation",
			pr:       ready("2026-08-06T06:50:00Z"),
			timeline: []event{{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T07:00:00Z"}},
			wantOK:   false,
		},
		{
			name:     "a draft is free to rewrite",
			pr:       pull{Number: 1, Draft: true, CreatedAt: "2026-08-06T06:50:00Z"},
			timeline: []event{{Event: "head_ref_force_pushed", CreatedAt: "2026-08-06T07:00:00Z"}},
			wantOK:   true,
			wantsMsg: "draft",
		},
		{
			name:     "an empty timeline is a broken fetch, never a clean PR",
			pr:       ready("2026-08-06T06:50:00Z"),
			timeline: nil,
			wantOK:   false,
			wantsMsg: "fetch is broken",
		},
		{
			name:     "an unreadable created_at fails rather than passing",
			pr:       pull{Number: 1, CreatedAt: "not-a-time"},
			timeline: pr140,
			wantOK:   false,
			wantsMsg: "cannot read",
		},
		{
			name: "dependabot rebase passes without review",
			pr:   prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{
				{Event: "committed", CreatedAt: "2026-10-20T08:58:00Z"},
				pushBy(bot, "2026-10-21T07:46:21Z"),
			},
			wantOK:   true,
			wantsMsg: bot,
		},
		{
			name: "two dependabot rebases pass",
			pr:   prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{
				pushBy(bot, "2026-10-21T07:46:21Z"),
				pushBy(bot, "2026-10-22T07:46:21Z"),
			},
			wantOK:   true,
			wantsMsg: bot,
		},
		{
			name:     "dependabot pr with no force push keeps the no rewrite reason",
			pr:       prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{{Event: "committed", CreatedAt: "2026-10-20T08:58:00Z"}},
			wantOK:   true,
			wantsMsg: "no force push since",
		},
		{
			name:     "empty timeline on a dependabot pr still fails",
			pr:       prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: nil,
			wantOK:   false,
			wantsMsg: "fetch is broken",
		},
		{
			name:     "human force push on a dependabot pr fails",
			pr:       prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{pushBy("SimonOsipov", "2026-10-21T07:46:21Z")},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name: "human push then bot push fails",
			pr:   prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{
				pushBy("SimonOsipov", "2026-10-21T07:00:00Z"),
				pushBy(bot, "2026-10-21T08:00:00Z"),
			},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name: "bot push then human push fails",
			pr:   prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{
				pushBy(bot, "2026-10-21T07:00:00Z"),
				pushBy("SimonOsipov", "2026-10-21T08:00:00Z"),
			},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name: "human push before ready does not block the exemption",
			pr:   prBy(bot, "2026-10-20T08:00:00Z"),
			timeline: []event{
				pushBy("SimonOsipov", "2026-10-20T10:00:00Z"),
				{Event: "ready_for_review", CreatedAt: "2026-10-20T12:00:00Z"},
				pushBy(bot, "2026-10-20T13:00:00Z"),
			},
			wantOK:   true,
			wantsMsg: bot,
		},
		{
			name:     "bot force push on a human pr fails",
			pr:       prBy("SimonOsipov", "2026-10-20T08:58:33Z"),
			timeline: []event{pushBy(bot, "2026-10-21T07:46:21Z")},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name:     "human pr human push still fails",
			pr:       prBy("SimonOsipov", "2026-10-20T08:58:33Z"),
			timeline: []event{pushBy("SimonOsipov", "2026-10-21T07:46:21Z")},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name:     "missing actor on a force push fails",
			pr:       prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{{Event: "head_ref_force_pushed", CreatedAt: "2026-10-21T07:46:21Z"}},
			wantOK:   false,
			wantsMsg: "nobody has reviewed it since",
		},
		{
			name: "review after a human push on a dependabot pr clears it",
			pr:   prBy(bot, "2026-10-20T08:58:33Z"),
			timeline: []event{
				pushBy("SimonOsipov", "2026-10-21T07:46:21Z"),
				{Event: "reviewed", SubmittedAt: "2026-10-21T09:00:00Z"},
			},
			wantOK:   true,
			wantsMsg: "reviewed again at",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.pr, tc.timeline)
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (reason: %s)", got.OK, tc.wantOK, got.Reason)
			}
			if tc.wantsMsg != "" && !strings.Contains(got.Reason, tc.wantsMsg) {
				t.Fatalf("reason %q does not mention %q", got.Reason, tc.wantsMsg)
			}
		})
	}

	// Exact match only: a widening to prefix, case-fold or trim must turn a row green.
	t.Run("near miss logins are not exempt", func(t *testing.T) {
		nearMisses := []string{"dependabot", "Dependabot[bot]", "dependabot[bot] ", "dependabot-preview[bot]", "renovate[bot]", ""}
		const created = "2026-10-20T08:58:33Z"
		const at = "2026-10-21T07:46:21Z"

		// Control: the exact pair is exempt, so a row below failing means something.
		if v := decide(prBy(bot, created), []event{pushBy(bot, at)}); !v.OK {
			t.Fatalf("control: the exact pair must pass, got %q", v.Reason)
		}
		for _, login := range nearMisses {
			t.Run("pusher "+login, func(t *testing.T) {
				if v := decide(prBy(bot, created), []event{pushBy(login, at)}); v.OK {
					t.Fatalf("pusher %q on a bot PR must not be exempt, got %q", login, v.Reason)
				}
			})
			t.Run("author "+login, func(t *testing.T) {
				if v := decide(prBy(login, created), []event{pushBy(bot, at)}); v.OK {
					t.Fatalf("author %q with a bot pusher must not be exempt, got %q", login, v.Reason)
				}
			})
		}
	})
}

func TestFailureMessageNamesTheRerun(t *testing.T) {
	pr := prBy("SimonOsipov", "2026-10-20T08:58:33Z")
	v := decide(pr, []event{pushBy("SimonOsipov", "2026-10-21T07:46:21Z")})
	if v.OK {
		t.Fatalf("control: a human push with no review must fail, got %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "re-run the CI workflow") {
		t.Errorf("reason %q does not tell the author to re-run the CI workflow", v.Reason)
	}
	if strings.Contains(v.Reason, "clears this check on its own") {
		t.Errorf("reason %q still claims a review clears the check on its own", v.Reason)
	}
}

// The gate reads GitHub's own JSON. If a field name drifts, every event decodes
// to the zero value and the gate reports a clean PR forever.
func TestDecodesGitHubsFieldNames(t *testing.T) {
	raw := `[
	  {"event":"head_ref_force_pushed","created_at":"2026-08-06T10:25:30Z","actor":{"login":"dependabot[bot]","type":"Bot","id":49699333}},
	  {"event":"ready_for_review","created_at":"2026-08-06T12:38:33Z"},
	  {"event":"reviewed","submitted_at":"2026-08-06T12:46:45Z","state":"commented"}
	]`
	var timeline []event
	if err := json.Unmarshal([]byte(raw), &timeline); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(timeline) != 3 {
		t.Fatalf("decoded %d events, want 3", len(timeline))
	}
	for i, e := range timeline {
		if e.Event == "" {
			t.Errorf("event %d decoded with an empty Event field", i)
		}
		if _, ok := e.when(); !ok {
			t.Errorf("event %d (%s) has no readable timestamp", i, e.Event)
		}
	}
	if timeline[2].SubmittedAt == "" {
		t.Error("a review decoded without its submitted_at — reviews would never clear the gate")
	}
	if timeline[0].Actor.Login != bot {
		t.Errorf("force push actor = %q, want %q: the pusher would read as empty", timeline[0].Actor.Login, bot)
	}

	var pr pull
	rawPR := `{"number":499,"draft":false,"created_at":"2026-10-20T08:58:33Z","user":{"login":"dependabot[bot]","type":"Bot"}}`
	if err := json.Unmarshal([]byte(rawPR), &pr); err != nil {
		t.Fatalf("unmarshal pull: %v", err)
	}
	if pr.User.Login != bot {
		t.Errorf("PR author = %q, want %q: the author would read as empty", pr.User.Login, bot)
	}
}
